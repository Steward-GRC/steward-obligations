// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package obligation contains the standing-obligation resolver. It orchestrates
// four narrow interfaces — coreObligations (core gRPC), identityUsers
// (identity gRPC), categoryChains (core effective ruleset), and acksStore (local
// ack persistence) — to answer:
//
// - MyObligations(userID): which published policies must this user ack?
// - ObligatedAudienceCount(policyID): how many users are in-scope for this policy?
// - Completion(policyVersionID, groupID): total/acked/overdue for a version.
//
// # Ack-keying rules
//
// - on-change (OnChange=true): the user must have acked the EXACT current
// published version (PublishedVersionID). An ack for an older version does
// NOT satisfy the obligation.
// - on-publish (OnChange=false): any ack row for any version of the policy
// satisfies the obligation. A NEW published version does NOT re-obligate a
// user who already acked an older version.
//
// Obligation resolution (go-authz ordered model, see access.go):
//
// For user U and obligating policy P (home category = P's home group id):
// 1. override:= UserPolicyOverrides(U)[P.Number] (keyed by policy NUMBER)
// 2. if override == "deny" → NO obligation (skip policy).
// 3. levels:= EffectiveLevels(P.homeGroupID), one core call, nearest first
// 4. the person is U, their directory groups and, for the root seat only,
// the root role; a site admin who is not root follows the rows
// 5. if override == "allow" → obligated iff the Ack cell allows, without the
// Read requirement (the override opens the document out-of-band)
// 6. else → obligated iff Ack evaluates allowed AND Read evaluates allowed
// (Ack requires Read; owners are not asked by ownership; root's Ack
// follows the rule)
//
// Cross-service note: "acked any version" cannot be a pure SQL join inside
// the obligations service because the acknowledgments table stores policy_version_id
// but not policy_id, and the policy_versions table lives in core. The
// resolver therefore orchestrates the lookup: it asks core for the policy's
// version IDs (coreObligations.ListPolicyVersionIDs) and checks the local ack
// store (acksStore.Acked) across that version set. This keeps the ack store
// pure (version-scoped) and puts the policy->versions resolution where the core
// client already lives.
package obligation

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	"github.com/Steward-GRC/steward-obligations/internal/authz"
	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// ---------------------------------------------------------------------------
// Public data shapes shared between the resolver and handler packages.
// ---------------------------------------------------------------------------

// ObligatingPolicy is a flat projection of an obligating policy returned by
// core's ListObligatingPolicies RPC.
type ObligatingPolicy struct {
	PolicyID           string
	Number             string
	Title              string
	VersionNo          int32
	PublishedVersionID string
	OnChange           bool
}

// ObligationItem is the resolver output: one outstanding obligation for a user.
type ObligationItem struct {
	PolicyID        string
	Number          string
	Title           string
	VersionNo       int32
	PolicyVersionID string
	// DueAt is when the acknowledgement is due: the version's publish time plus
	// the configured days. Zero when core has no publish time or due dates are off.
	DueAt time.Time
}

// RecentAckItem is one acknowledgement the user recorded, named by its policy.
type RecentAckItem struct {
	PolicyID        string
	Number          string
	Title           string
	VersionNo       int32
	PolicyVersionID string
	AckedAt         time.Time
}

const (
	defaultRecentAcks = 10
	maxRecentAcks     = 100
)

// MyRecentAcks returns the user's own acknowledgements, newest first, each named
// by its policy number, title and version. limit <= 0 means the default of 10
// and the answer never exceeds 100. An acknowledgement whose policy core no
// longer knows is left out (logged), not an error.
func (r *Resolver) MyRecentAcks(ctx context.Context, userID string, limit int32) ([]RecentAckItem, error) {
	log := logctx.From(ctx)
	n := int(limit)
	if n <= 0 {
		n = defaultRecentAcks
	}
	if n > maxRecentAcks {
		n = maxRecentAcks
	}
	log.Debug().Int("limit", n).Msg("obligation: my recent acks: start")
	rows, err := r.acks.RecentAcks(ctx, userID, n)
	if err != nil {
		log.Error().Err(err).Msg("obligation: my recent acks: store read failed")
		return nil, err
	}
	out := make([]RecentAckItem, 0, len(rows))
	for _, a := range rows {
		policyID, versionNo, err := r.core.VersionRef(ctx, a.PolicyVersionID)
		if err != nil {
			log.Error().Err(err).Str("policy_version_id", a.PolicyVersionID).Msg("obligation: my recent acks: version lookup failed")
			return nil, err
		}
		if policyID == "" {
			log.Warn().Str("policy_version_id", a.PolicyVersionID).Msg("obligation: my recent acks: policy unknown to core, skipped")
			continue
		}
		number, title, err := r.core.PolicyDisplay(ctx, policyID)
		if err != nil {
			log.Error().Err(err).Str("policy_id", policyID).Msg("obligation: my recent acks: policy lookup failed")
			return nil, err
		}
		out = append(out, RecentAckItem{
			PolicyID: policyID, Number: number, Title: title,
			VersionNo: versionNo, PolicyVersionID: a.PolicyVersionID, AckedAt: a.AckedAt,
		})
	}
	log.Debug().Int("count", len(out)).Msg("obligation: my recent acks: done")
	return out, nil
}

// PolicyObligation is the resolved obligation metadata for a single policy
// (returned by ResolvePolicyObligation in core).
type PolicyObligation struct {
	RequiresAck        bool
	OnChange           bool
	PublishedVersionID string
}

// User is a minimal user record returned in Completion.Overdue.
type User struct {
	ID    string
	Email string
	// DueAt is set by Completion (overdue) and Roster (pending) to when the
	// person's acknowledgement is due; zero when none applies.
	DueAt time.Time
}

// ---------------------------------------------------------------------------
// Narrow dependency interfaces (satisfied by generated gRPC clients + store).
// ---------------------------------------------------------------------------

// coreObligations is the subset of the core PolicyServiceClient that
// the resolver requires.
type coreObligations interface {
	ListObligatingPolicies(ctx context.Context) ([]ObligatingPolicy, error)
	ResolvePolicyObligation(ctx context.Context, policyID string) (PolicyObligation, error)
	// PolicyIDForVersion returns the owning policy ID for a policyVersionID.
	PolicyIDForVersion(ctx context.Context, policyVersionID string) (string, error)
	// VersionRef returns the owning policy ID and the version number of a
	// policy version. An unknown version answers an empty policy ID.
	VersionRef(ctx context.Context, policyVersionID string) (policyID string, versionNo int32, err error)
	// VersionPublishedAt returns the publish timestamp for a policy version.
	VersionPublishedAt(ctx context.Context, policyVersionID string) (time.Time, error)
	// ListPolicyVersionIDs returns the version IDs for a policy.
	ListPolicyVersionIDs(ctx context.Context, policyID string) ([]string, error)
	// PolicyHomeGroup returns the policy's home group id.
	PolicyHomeGroup(ctx context.Context, policyID string) (string, error)
	// PolicyNumber returns the policy's display number (e.g. "POL-001") for the
	// given policy ID. Used by Completion/Roster/ReleaseDroppedPending to look up
	// per-policy overrides when evaluating obligation via the RACI chain.
	PolicyNumber(ctx context.Context, policyID string) (string, error)
	// PolicyDisplay returns the policy's display number and title in one call.
	// The notify consumer uses these so emails show a human reference/title
	// instead of the raw policy version UUID.
	PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error)
	// PolicySensitivity reports whether the policy is classified SENSITIVE. Used
	// by the sensitivity gate so a user without read_sensitive clearance is not
	// obligated to acknowledge a sensitive policy they can never open. Backed by
	// core GetPolicy (the policy carries its own sensitivity classification).
	PolicySensitivity(ctx context.Context, policyID string) (bool, error)
	// PolicyDocumentType returns the policy's document-type discriminator as a
	// stable lowercase string ("procedure" or "policy"). Used by audienceUsers to
	// exclude PROCEDURES from ack-bearing audiences: a procedure is
	// never ack-bearing, so it obligates/notifies no one. Backed by core GetPolicy
	// (the document carries its own document_type).
	PolicyDocumentType(ctx context.Context, policyID string) (string, error)
}

// identityUsers is the subset of the identity IdentityReadServiceClient the
// resolver requires.
type identityUsers interface {
	// UserAdGroups returns the AD group names the given user belongs to.
	UserAdGroups(ctx context.Context, userID string) ([]string, error)
	// UserPolicyOverrides returns the user's per-policy allow/deny overrides
	// keyed by policy NUMBER ("allow"/"deny").
	UserPolicyOverrides(ctx context.Context, userID string) (map[string]string, error)
	// ListAllUsers returns all ENABLED users — the RACI audience candidate set.
	ListAllUsers(ctx context.Context) ([]User, error)
	// UserReadClearance returns the user's GLOBAL roles, their individual
	// read_sensitive grant and whether they hold the root seat: the inputs to
	// the sensitivity gate (a user cleared to READ a sensitive policy may be
	// obligated to ack it) and to the root exemption. Backed by identity
	// GetUser (the same read that supplies policy overrides).
	UserReadClearance(ctx context.Context, userID string) (roles []string, readSensitive, root bool, err error)
}

// categoryChains reads a category's effective ruleset from core: the category
// first, then each ancestor, in one call. The levels feed the ordered model
// (access.go) to decide per-user obligations.
type categoryChains interface {
	// EffectiveLevels returns the levels for categoryID (home group id),
	// nearest first.
	EffectiveLevels(ctx context.Context, categoryID string) ([]*corev1.EffectiveLevel, error)
}

// acksStore is the subset of the ack persistence layer the resolver requires.
type acksStore interface {
	// Acked reports, for each versionID in the slice, whether userID has an
	// ack row for it.
	Acked(ctx context.Context, userID string, versionIDs []string) (map[string]bool, error)
	// AckedUserIDsForVersion returns the distinct user IDs that have acked the
	// given policyVersionID.
	AckedUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error)
	// VersionsAckedByUser returns the distinct policy version IDs the user has
	// acked.
	VersionsAckedByUser(ctx context.Context, userID string) ([]string, error)
	// RecentAcks returns the user's acknowledgements newest first, at most
	// limit rows (limit > 0).
	RecentAcks(ctx context.Context, userID string, limit int) ([]store.RecentAck, error)
	// AckedAtForVersion returns user_id -> acked_at for the version.
	AckedAtForVersion(ctx context.Context, policyVersionID string) (map[string]time.Time, error)
	// DailyAckCounts returns YYYY-MM-DD (UTC) -> ack count at/after since.
	DailyAckCounts(ctx context.Context, policyVersionID string, since time.Time) (map[string]int, error)
}

// pendingStore is the persisted state of acknowledgements still owed: for a
// version, the reminder anchor of each person who has not acknowledged it.
// Releasing it ends the reminders; it never touches an acknowledgement given.
type pendingStore interface {
	// PendingUserIDsForVersion returns the users with pending state on the
	// version and no acknowledgement of it.
	PendingUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error)
	// VersionsPendingForUser returns the versions the user has pending state
	// on and has not acknowledged.
	VersionsPendingForUser(ctx context.Context, userID string) ([]string, error)
	// DeletePending removes the pending state of the users on the version,
	// leaving anyone who has acknowledged it alone, and returns the count.
	DeletePending(ctx context.Context, policyVersionID string, userIDs []string) (int, error)
}

// viewsStore is the subset of the policy-view persistence the resolver requires.
type viewsStore interface {
	DistinctViewersForVersion(ctx context.Context, policyVersionID string) ([]string, error)
	DailyDistinctViewers(ctx context.Context, policyVersionID string, since time.Time, audienceUserIDs []string) (map[string]int, error)
}

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

// Resolver orchestrates the obligation algorithm across the four dependency
// interfaces.
type Resolver struct {
	core   coreObligations
	idn    identityUsers
	chains categoryChains
	acks   acksStore
	views  viewsStore
	// pending is the pending-ack state released when a person leaves an
	// audience; nil leaves it alone.
	pending pendingStore
	// ackDueDays is the days after publish an acknowledgement is due; 0 means
	// no due date.
	ackDueDays int
}

// DefaultAckDueDays is how long after publish an acknowledgement is due when
// WithAckDueDays is never called.
const DefaultAckDueDays = 14

// NewResolver constructs a Resolver. views is satisfied by *store.PolicyViewStore.
// chains is satisfied by a ChainAdapter wrapping the group-service client.
func NewResolver(core coreObligations, idn identityUsers, chains categoryChains, acks acksStore, views viewsStore) *Resolver {
	return &Resolver{core: core, idn: idn, chains: chains, acks: acks, views: views, ackDueDays: DefaultAckDueDays}
}

// WithPendingStore sets the pending-ack state released when a person drops out
// of an audience. It returns the resolver so it chains off NewResolver.
func (r *Resolver) WithPendingStore(p pendingStore) *Resolver {
	r.pending = p
	return r
}

// WithAckDueDays sets how many days after a version is published its
// acknowledgement is due. Zero (or less) turns due dates off. It returns the
// resolver so it chains off NewResolver.
func (r *Resolver) WithAckDueDays(days int) *Resolver {
	if days < 0 {
		days = 0
	}
	r.ackDueDays = days
	return r
}

// dueAt is the publish time of policyVersionID plus the configured days, or the
// zero time when due dates are off or core has no publish time for the version.
func (r *Resolver) dueAt(ctx context.Context, policyVersionID string) (time.Time, error) {
	if r.ackDueDays == 0 {
		return time.Time{}, nil
	}
	published, err := r.core.VersionPublishedAt(ctx, policyVersionID)
	if err != nil {
		return time.Time{}, err
	}
	if published.IsZero() {
		return time.Time{}, nil
	}
	return published.AddDate(0, 0, r.ackDueDays), nil
}

// ---------------------------------------------------------------------------
// Core obligation helpers
// ---------------------------------------------------------------------------

// userCtx holds pre-fetched per-user context for obligation evaluation.
type userCtx struct {
	adGroups  []string
	overrides map[string]string
	// roles + readSensitive are the subject's read-sensitive clearance inputs,
	// consumed by the sensitivity gate in obligated.
	roles         []string
	readSensitive bool
	// root is the root seat (identity's is_root): exempt from the sensitivity
	// gate and for Read, but its Ack follows the category rule.
	root bool
}

// policyCtx holds pre-fetched per-policy context for obligation evaluation.
type policyCtx struct {
	homeGroupID  string
	policyNumber string
	access       *categoryAccess
	// sensitive is the policy's SENSITIVE classification; the sensitivity gate in
	// obligated uses it to drop obligations for uncleared subjects.
	sensitive bool
}

// obligated reports whether a user must acknowledge a policy under its
// category rules. It is the one source of obligation truth for every path
// (my obligations, my summary, audiences, completion, rosters, releases):
//
//  1. a deny override: no obligation;
//  2. an allow override settles read, so only the Ack cell must allow;
//  3. otherwise the sensitivity gate, then the ordered Ack decision, which
//     requires Read.
//
// The sensitivity gate keeps an uncleared user from owing an acknowledgement
// for a sensitive policy they can never open. It mirrors the gateway's read
// gate, which an allow override also bypasses and root is never held by. Owners
// are not asked by ownership, root's Ack follows the rule, and a site admin who
// is not root is decided by the rows like anyone else.
func obligated(userID string, uc userCtx, pc policyCtx) bool {
	person := personFor(userID, uc.adGroups, uc.root)
	switch uc.overrides[pc.policyNumber] {
	case "deny":
		return false
	case "allow":
		return pc.access.ackAllowed(person, true)
	default:
		// The twin of the gateway's read gate: keep the two in lockstep.
		if pc.sensitive && !uc.root && !authz.HasCapability(clearance(uc), authz.PolicyReadSensitive) {
			return false
		}
		return pc.access.ackAllowed(person, false)
	}
}

// clearance is the subject the sensitivity gate checks: the user's roles and
// individual read-sensitive grant. No role reads a sensitive document; only
// the grant clears it.
func clearance(uc userCtx) authz.Subject {
	roles := make([]authz.Role, len(uc.roles))
	for i, r := range uc.roles {
		roles[i] = authz.Role(r)
	}
	return authz.Subject{Roles: roles, ReadSensitive: uc.readSensitive, Root: uc.root}
}

// accessFor returns the category rules for a home group, reading them from core
// once per request through cache. No chain source or no home group is an empty
// rule set, which obligates no one.
func (r *Resolver) accessFor(ctx context.Context, hg string, cache map[string]*categoryAccess) (*categoryAccess, error) {
	if a, ok := cache[hg]; ok {
		return a, nil
	}
	if r.chains == nil || hg == "" {
		return newCategoryAccess(nil), nil
	}
	levels, err := r.chains.EffectiveLevels(ctx, hg)
	if err != nil {
		return nil, err
	}
	a := newCategoryAccess(levels)
	cache[hg] = a
	return a, nil
}

// ResolvePolicyObligation returns the ack obligation metadata for policyID. It
// passes through to core so *Resolver alone satisfies the policy-published
// consumer's ObligationResolver interface (metadata lookup + AudienceUsers).
func (r *Resolver) ResolvePolicyObligation(ctx context.Context, policyID string) (PolicyObligation, error) {
	return r.core.ResolvePolicyObligation(ctx, policyID)
}

// PolicyDisplay returns the policy's display number and title, for the notify
// consumer to stamp onto email payloads (so recipients see a human reference,
// never the version UUID).
func (r *Resolver) PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error) {
	return r.core.PolicyDisplay(ctx, policyID)
}

// AudienceUsers returns the RACI-resolved ack audience for policyID: the full
// User records (with email) for every enabled user obligated to ack the policy
// under its home-category RACI chain (including per-policy override handling).
// It is the single audience source shared by the resolver's own read paths and
// the policy-published consumer, so a publish sweep notifies exactly the users
// the ack resolver would obligate.
//
// It fails loud (returns an error, never a silently-empty slice) when the
// policy has no home category or the chain cannot be built — matching the
// resolver's fail-loud rule so an accidental empty audience can never suppress
// notifications or (in the purge paths) mass-delete acks.
func (r *Resolver) AudienceUsers(ctx context.Context, policyID string) ([]User, error) {
	return r.audienceUsers(ctx, policyID)
}

// audienceUsers returns the full User records (with email) for users obligated
// by policyID. It calls ListAllUsers once, evaluates each user against the RACI
// chain, and returns the obligated subset. Used by Completion, Roster,
// CompletionMetrics, Activity, and AudienceUsers.
func (r *Resolver) audienceUsers(ctx context.Context, policyID string) ([]User, error) {
	// P3: exclude PROCEDURES from every ack-bearing audience. This closes
	// the P2-flagged gap for the audienceUsers-driven paths that BYPASS
	// ResolvePolicyObligation — the policy_retired consumer (via AudienceUsers) and
	// reporting Completion (which call audienceUsers directly) — plus, defensively,
	// Roster/purge/reconcile (via audienceFor). A procedure is never ack-bearing,
	// so it obligates/notifies NO ONE: return an empty audience. The lookup fails
	// loud (an errored lookup propagates and is NEVER silently treated as a policy),
	// matching this helper's existing fail-loud contract. For POLICIES behavior is
	// unchanged.
	docType, err := r.core.PolicyDocumentType(ctx, policyID)
	if err != nil {
		return nil, err
	}
	if docType == "procedure" {
		return nil, nil
	}

	hg, err := r.core.PolicyHomeGroup(ctx, policyID)
	if err != nil {
		return nil, err
	}
	num, err := r.core.PolicyNumber(ctx, policyID)
	if err != nil {
		return nil, err
	}

	if r.chains == nil || hg == "" {
		return nil, fmt.Errorf("no category chain available for policy %s (homeGroup=%q)", policyID, hg)
	}
	levels, err := r.chains.EffectiveLevels(ctx, hg)
	if err != nil {
		return nil, err
	}

	sensitive, err := r.core.PolicySensitivity(ctx, policyID)
	if err != nil {
		return nil, err
	}
	pc := policyCtx{homeGroupID: hg, policyNumber: num, access: newCategoryAccess(levels), sensitive: sensitive}

	users, err := r.idn.ListAllUsers(ctx)
	if err != nil {
		return nil, err
	}

	var out []User
	for _, u := range users {
		adGroups, err := r.idn.UserAdGroups(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		overrides, err := r.idn.UserPolicyOverrides(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		roles, readSensitive, root, err := r.idn.UserReadClearance(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		uc := userCtx{adGroups: adGroups, overrides: overrides, roles: roles, readSensitive: readSensitive, root: root}
		if obligated(u.ID, uc, pc) {
			out = append(out, u)
		}
	}
	return out, nil
}

// MyObligations returns the outstanding obligations for userID: all published
// policies whose RACI chain obligates the user AND whose ack requirement has not
// been satisfied.
func (r *Resolver) MyObligations(ctx context.Context, userID string) ([]ObligationItem, error) {
	// 1. Resolve user's AD group membership and overrides.
	adGroups, err := r.idn.UserAdGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	overrides, err := r.idn.UserPolicyOverrides(ctx, userID)
	if err != nil {
		return nil, err
	}
	roles, readSensitive, root, err := r.idn.UserReadClearance(ctx, userID)
	if err != nil {
		return nil, err
	}
	uc := userCtx{adGroups: adGroups, overrides: overrides, roles: roles, readSensitive: readSensitive, root: root}

	// 2. Fetch all currently obligating policies from core.
	policies, err := r.core.ListObligatingPolicies(ctx)
	if err != nil {
		return nil, err
	}

	// Per-request chain cache: build each category's chain once per policy home group.
	accessCache := make(map[string]*categoryAccess)

	var out []ObligationItem
	for _, p := range policies {
		// 3. Deny override skips immediately (no chain needed).
		if overrides[p.Number] == "deny" {
			continue
		}

		// 4. Build (or reuse) the RACI chain for this policy's home category.
		hg, err := r.core.PolicyHomeGroup(ctx, p.PolicyID)
		if err != nil {
			return nil, err
		}
		access, err := r.accessFor(ctx, hg, accessCache)
		if err != nil {
			return nil, err
		}

		sensitive, err := r.core.PolicySensitivity(ctx, p.PolicyID)
		if err != nil {
			return nil, err
		}
		pc := policyCtx{homeGroupID: hg, policyNumber: p.Number, access: access, sensitive: sensitive}
		if !obligated(userID, uc, pc) {
			continue
		}

		// 5. Ack-keying check.
		var cleared bool
		if p.OnChange {
			// on-change: user must have acked the exact current published version.
			m, err := r.acks.Acked(ctx, userID, []string{p.PublishedVersionID})
			if err != nil {
				return nil, err
			}
			cleared = m[p.PublishedVersionID]
		} else {
			// on-publish: an ack for ANY version of the policy clears the obligation.
			cleared, err = r.ackedAnyVersion(ctx, userID, p.PolicyID)
			if err != nil {
				return nil, err
			}
		}
		if cleared {
			continue
		}

		due, err := r.dueAt(ctx, p.PublishedVersionID)
		if err != nil {
			return nil, err
		}
		out = append(out, ObligationItem{
			PolicyID:        p.PolicyID,
			Number:          p.Number,
			Title:           p.Title,
			VersionNo:       p.VersionNo,
			PolicyVersionID: p.PublishedVersionID,
			DueAt:           due,
		})
	}
	return out, nil
}

// Outstanding is one live (user, policy version) ack obligation the user has not
// yet satisfied. It is the sweep's unit of work: the
// reminder sweep re-evaluates these each tick and applies the back-off.
type Outstanding struct {
	// UserID/Email identify the obligated recipient.
	UserID string
	Email  string
	// PolicyID/Number/Title/VersionNo describe the policy for reminder copy.
	PolicyID  string
	Number    string
	Title     string
	VersionNo int32
	// PolicyVersionID is the ack target and the back-off state key together with
	// UserID.
	PolicyVersionID string
}

// OutstandingObligations returns every live (user, version) obligation across
// all currently-obligating policies that has NOT been acked — the set the
// reminder sweep re-evaluates each tick. It reuses the same audience resolution
// and ack-keying rules as MyObligations/Completion (RACI chain via
// audienceUsers; on-change requires the exact published version, on-publish is
// cleared by an ack for any version), so the sweep obligates exactly whom the
// portal does. It is a pure read: it never mutates acks or obligations.
func (r *Resolver) OutstandingObligations(ctx context.Context) ([]Outstanding, error) {
	policies, err := r.core.ListObligatingPolicies(ctx)
	if err != nil {
		return nil, err
	}

	var out []Outstanding
	for _, p := range policies {
		// RACI-resolved audience (excludes procedures, applies sensitivity/overrides).
		audience, err := r.audienceUsers(ctx, p.PolicyID)
		if err != nil {
			return nil, err
		}
		if len(audience) == 0 {
			continue
		}

		// Users who acked the CURRENT published version (the on-change key).
		ackedIDs, err := r.acks.AckedUserIDsForVersion(ctx, p.PublishedVersionID)
		if err != nil {
			return nil, err
		}
		ackedSet := make(map[string]struct{}, len(ackedIDs))
		for _, id := range ackedIDs {
			ackedSet[id] = struct{}{}
		}

		for _, u := range audience {
			var cleared bool
			if p.OnChange {
				_, cleared = ackedSet[u.ID]
			} else {
				// on-publish: an ack for ANY version clears the obligation.
				cleared, err = r.ackedAnyVersion(ctx, u.ID, p.PolicyID)
				if err != nil {
					return nil, err
				}
			}
			if cleared {
				continue
			}
			out = append(out, Outstanding{
				UserID:          u.ID,
				Email:           u.Email,
				PolicyID:        p.PolicyID,
				Number:          p.Number,
				Title:           p.Title,
				VersionNo:       p.VersionNo,
				PolicyVersionID: p.PublishedVersionID,
			})
		}
	}
	return out, nil
}

// MyAckSummary returns the caller's acknowledgement-compliance figures:
//
// - required: number of published policies whose audience includes the user.
// - done: of those, how many the user has already acked.
//
// It mirrors MyObligations exactly (same RACI obligation + on-change / on-publish
// acked detection) but counts acked policies toward `done` instead of skipping them.
func (r *Resolver) MyAckSummary(ctx context.Context, userID string) (required int32, done int32, err error) {
	// 1. Resolve user's AD group membership and overrides.
	adGroups, err := r.idn.UserAdGroups(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	overrides, err := r.idn.UserPolicyOverrides(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	roles, readSensitive, root, err := r.idn.UserReadClearance(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	uc := userCtx{adGroups: adGroups, overrides: overrides, roles: roles, readSensitive: readSensitive, root: root}

	// 2. Fetch all currently obligating policies from core.
	policies, err := r.core.ListObligatingPolicies(ctx)
	if err != nil {
		return 0, 0, err
	}

	// Per-request chain cache.
	accessCache := make(map[string]*categoryAccess)

	for _, p := range policies {
		if overrides[p.Number] == "deny" {
			continue
		}

		hg, err := r.core.PolicyHomeGroup(ctx, p.PolicyID)
		if err != nil {
			return 0, 0, err
		}
		access, err := r.accessFor(ctx, hg, accessCache)
		if err != nil {
			return 0, 0, err
		}

		sensitive, err := r.core.PolicySensitivity(ctx, p.PolicyID)
		if err != nil {
			return 0, 0, err
		}
		pc := policyCtx{homeGroupID: hg, policyNumber: p.Number, access: access, sensitive: sensitive}
		if !obligated(userID, uc, pc) {
			continue
		}
		required++

		// Ack-keying check (identical to MyObligations).
		var cleared bool
		if p.OnChange {
			m, err := r.acks.Acked(ctx, userID, []string{p.PublishedVersionID})
			if err != nil {
				return 0, 0, err
			}
			cleared = m[p.PublishedVersionID]
		} else {
			cleared, err = r.ackedAnyVersion(ctx, userID, p.PolicyID)
			if err != nil {
				return 0, 0, err
			}
		}
		if cleared {
			done++
		}
	}
	return required, done, nil
}

// ackedAnyVersion reports whether userID has acked ANY version of policyID.
func (r *Resolver) ackedAnyVersion(ctx context.Context, userID, policyID string) (bool, error) {
	versionIDs, err := r.core.ListPolicyVersionIDs(ctx, policyID)
	if err != nil {
		return false, err
	}
	if len(versionIDs) == 0 {
		return false, nil
	}
	acked, err := r.acks.Acked(ctx, userID, versionIDs)
	if err != nil {
		return false, err
	}
	for _, v := range versionIDs {
		if acked[v] {
			return true, nil
		}
	}
	return false, nil
}

// ObligatedAudienceCount returns the count of users who are in-scope for the
// given policyID's obligation. Returns 0 if the policy has no ack requirement.
func (r *Resolver) ObligatedAudienceCount(ctx context.Context, policyID string) (int32, error) {
	obl, err := r.core.ResolvePolicyObligation(ctx, policyID)
	if err != nil {
		return 0, err
	}
	if !obl.RequiresAck {
		return 0, nil
	}
	audience, err := r.audienceUsers(ctx, policyID)
	if err != nil {
		return 0, err
	}
	return toInt32(len(audience)), nil
}

// Completion returns the live completion denominator for the supplied
// policyVersionID. Audience is resolved via the RACI chain (all enabled users
// for whom the chain yields an ack obligation).
//
// - total = distinct users in the policy's RACI audience
// - acked = distinct audience users with an ack row for policyVersionID
// - overdue = audience users minus those who acked
func (r *Resolver) Completion(ctx context.Context, policyVersionID, groupID string) (total int, acked int, overdue []User, err error) {
	// 1. Resolve the owning policy.
	policyID, err := r.core.PolicyIDForVersion(ctx, policyVersionID)
	if err != nil {
		return 0, 0, nil, err
	}

	// 2. List audience users via RACI chain.
	audience, err := r.audienceUsers(ctx, policyID)
	if err != nil {
		return 0, 0, nil, err
	}
	total = len(audience)

	// 3. Fetch user IDs that have acked the version.
	ackedIDs, err := r.acks.AckedUserIDsForVersion(ctx, policyVersionID)
	if err != nil {
		return 0, 0, nil, err
	}
	ackedSet := make(map[string]struct{}, len(ackedIDs))
	for _, id := range ackedIDs {
		ackedSet[id] = struct{}{}
	}

	// 4. Overdue = audience members who have not acked, each with the date
	// the version's acknowledgement is due.
	due, err := r.dueAt(ctx, policyVersionID)
	if err != nil {
		return 0, 0, nil, err
	}
	for _, u := range audience {
		if _, ok := ackedSet[u.ID]; !ok {
			u.DueAt = due
			overdue = append(overdue, u)
		}
	}

	// acked = audience members who DID ack (not all ack rows, which could include
	// users who have since left the audience and would push pct > 100%).
	acked = total - len(overdue)
	return total, acked, overdue, nil
}

// ---------------------------------------------------------------------------
// Roster / Activity / CompletionMetrics
// ---------------------------------------------------------------------------

// RosterEntry is one audience member in the ack roster.
type RosterEntry struct {
	ID      string
	Email   string
	AckedAt time.Time // zero for pending entries
}

// ActivityDay is one day in the ack/view activity series.
type ActivityDay struct {
	Date  string // YYYY-MM-DD (UTC)
	Acks  int
	Views int
}

// audienceFor resolves the obligated audience users for a version's owning
// policy using the RACI chain. It is the single audience source for all
// version-keyed calls (Roster, ReleaseDroppedPending, CompletionMetrics, Activity).
// Returns nil (empty) if the policy has no ack requirement.
func (r *Resolver) audienceFor(ctx context.Context, policyVersionID string) ([]User, error) {
	policyID, err := r.core.PolicyIDForVersion(ctx, policyVersionID)
	if err != nil {
		return nil, err
	}
	obl, err := r.core.ResolvePolicyObligation(ctx, policyID)
	if err != nil {
		return nil, err
	}
	if !obl.RequiresAck {
		return nil, nil
	}
	return r.audienceUsers(ctx, policyID)
}

// ReleaseDroppedPending removes the pending-ack state of people who are no
// longer in policyVersionID's current audience: they stop owing the
// acknowledgement and stop being reminded. An acknowledgement already given is
// an attestation and is kept, whoever is in the audience now. It returns the
// number of pending entries released.
func (r *Resolver) ReleaseDroppedPending(ctx context.Context, policyVersionID string) (int, error) {
	if r.pending == nil {
		return 0, nil
	}
	audience, err := r.audienceFor(ctx, policyVersionID)
	if err != nil {
		return 0, err
	}
	audSet := make(map[string]struct{}, len(audience))
	for _, u := range audience {
		audSet[u.ID] = struct{}{}
	}

	pendingIDs, err := r.pending.PendingUserIDsForVersion(ctx, policyVersionID)
	if err != nil {
		return 0, err
	}

	var dropped []string
	for _, id := range pendingIDs {
		if _, ok := audSet[id]; !ok {
			dropped = append(dropped, id)
		}
	}
	if len(dropped) == 0 {
		return 0, nil
	}
	return r.pending.DeletePending(ctx, policyVersionID, dropped)
}

// ReconcileUserAcks releases the user's pending-ack state on every version they
// no longer owe, after a membership change. Acknowledgements already given are
// kept.
func (r *Resolver) ReconcileUserAcks(ctx context.Context, userID string) (int, error) {
	if r.pending == nil {
		return 0, nil
	}
	versionIDs, err := r.pending.VersionsPendingForUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, v := range versionIDs {
		n, err := r.ReleaseDroppedPending(ctx, v)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// ReconcilePolicyAcks releases dropped-out people's pending-ack state across
// EVERY version of policyID. Acknowledgements already given are kept.
func (r *Resolver) ReconcilePolicyAcks(ctx context.Context, policyID string) (int, error) {
	versionIDs, err := r.core.ListPolicyVersionIDs(ctx, policyID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, v := range versionIDs {
		n, err := r.ReleaseDroppedPending(ctx, v)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// Roster returns the audience split into acked (with acked_at) and pending.
func (r *Resolver) Roster(ctx context.Context, policyVersionID, groupID string) (acked []RosterEntry, pending []User, err error) {
	_ = groupID
	audience, err := r.audienceFor(ctx, policyVersionID)
	if err != nil {
		return nil, nil, err
	}
	at, err := r.acks.AckedAtForVersion(ctx, policyVersionID)
	if err != nil {
		return nil, nil, err
	}
	due, err := r.dueAt(ctx, policyVersionID)
	if err != nil {
		return nil, nil, err
	}
	for _, u := range audience {
		if t, ok := at[u.ID]; ok {
			acked = append(acked, RosterEntry{ID: u.ID, Email: u.Email, AckedAt: t})
		} else {
			u.DueAt = due
			pending = append(pending, u)
		}
	}
	return acked, pending, nil
}

// CompletionMetrics returns avg-days-to-ack and the count of audience members who
// viewed but did not ack.
func (r *Resolver) CompletionMetrics(ctx context.Context, policyVersionID, groupID string) (avgDaysToAck float64, viewedNotAcked int, err error) {
	_ = groupID
	audience, err := r.audienceFor(ctx, policyVersionID)
	if err != nil {
		return 0, 0, err
	}
	audSet := make(map[string]struct{}, len(audience))
	for _, u := range audience {
		audSet[u.ID] = struct{}{}
	}
	at, err := r.acks.AckedAtForVersion(ctx, policyVersionID)
	if err != nil {
		return 0, 0, err
	}
	publishedAt, err := r.core.VersionPublishedAt(ctx, policyVersionID)
	if err != nil {
		return 0, 0, err
	}
	var sum float64
	var n int
	if !publishedAt.IsZero() {
		for id, t := range at {
			if _, ok := audSet[id]; !ok {
				continue
			}
			d := t.Sub(publishedAt).Hours() / 24
			if d < 0 {
				d = 0
			}
			sum += d
			n++
		}
	}
	if n > 0 {
		avgDaysToAck = sum / float64(n)
	}
	viewers, err := r.views.DistinctViewersForVersion(ctx, policyVersionID)
	if err != nil {
		return 0, 0, err
	}
	for _, v := range viewers {
		if _, inAud := audSet[v]; !inAud {
			continue
		}
		if _, acked := at[v]; !acked {
			viewedNotAcked++
		}
	}
	return avgDaysToAck, viewedNotAcked, nil
}

// Activity returns a zero-filled daily series of acks and distinct audience
// viewers over the trailing `days` window (oldest -> newest, UTC).
func (r *Resolver) Activity(ctx context.Context, policyVersionID, groupID string, days int) ([]ActivityDay, error) {
	_ = groupID
	if days <= 0 {
		days = 30
	}
	audience, err := r.audienceFor(ctx, policyVersionID)
	if err != nil {
		return nil, err
	}
	audIDs := make([]string, len(audience))
	for i, u := range audience {
		audIDs[i] = u.ID
	}
	now := time.Now().UTC()
	since := now.AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour)
	ackDaily, err := r.acks.DailyAckCounts(ctx, policyVersionID, since)
	if err != nil {
		return nil, err
	}
	viewDaily, err := r.views.DailyDistinctViewers(ctx, policyVersionID, since, audIDs)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityDay, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, ActivityDay{Date: d, Acks: ackDaily[d], Views: viewDaily[d]})
	}
	return out, nil
}
