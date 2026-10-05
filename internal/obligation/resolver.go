// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package obligation contains the standing-obligation resolver. It orchestrates
// four narrow interfaces — coreObligations (core gRPC), identityUsers
// (identity gRPC), categoryChains (RACI chain builder), and acksStore (local
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
// Obligation resolution (RACI chain):
//
// For user U and obligating policy P (home category = P's home group id):
// 1. override:= UserPolicyOverrides(U)[P.Number] (keyed by policy NUMBER)
// 2. if override == "deny" → NO obligation (skip policy).
// 3. chain:= BuildCategoryChain(P.homeGroupID)
// 4. merit: the user and their directory groups, never site admin or root
// 5. if override == "allow" → obligated iff the ungated acknowledge rule allows
// (override grants READ out-of-band; the ack rule itself must still include U)
// 6. else → obligated iff Resolve(merit, chain).Ack.Allowed
// (covers: ack rule match AND read requirement; owners do NOT auto-ack)
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

	authz "github.com/Steward-GRC/steward-authz"
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
	// VersionPublishedAt returns the publish timestamp for a policy version.
	VersionPublishedAt(ctx context.Context, policyVersionID string) (time.Time, error)
	// ListPolicyVersionIDs returns the version IDs for a policy.
	ListPolicyVersionIDs(ctx context.Context, policyID string) ([]string, error)
	// PolicyHomeGroup returns the policy's home group id.
	PolicyHomeGroup(ctx context.Context, policyID string) (string, error)
	// PolicyNumber returns the policy's display number (e.g. "POL-001") for the
	// given policy ID. Used by Completion/Roster/PurgeOrphanedAcks to look up
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
	// UserReadClearance returns the user's GLOBAL roles and their individual
	// read_sensitive grant — the inputs to the sensitivity gate (a user cleared
	// to READ a sensitive policy may be obligated to ack it). Backed by identity
	// GetUser (the same read that supplies policy overrides).
	UserReadClearance(ctx context.Context, userID string) (roles []string, readSensitive bool, err error)
}

// categoryChains builds the RACI category-ruleset chain for a given category.
// The chain is walked leaf→root (target category first, then ancestors) and is
// consumed by authz.Resolve / authz.Acknowledgement to decide per-user obligations.
type categoryChains interface {
	// BuildCategoryChain returns the ruleset chain for categoryID (home group id).
	BuildCategoryChain(ctx context.Context, categoryID string) ([]authz.CategoryRuleset, error)
}

// acksStore is the subset of the ack persistence layer the resolver requires.
type acksStore interface {
	// Acked reports, for each versionID in the slice, whether userID has an
	// ack row for it.
	Acked(ctx context.Context, userID string, versionIDs []string) (map[string]bool, error)
	// AckedUserIDsForVersion returns the distinct user IDs that have acked the
	// given policyVersionID.
	AckedUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error)
	// DeleteAcksForUsers removes ack rows for the given users on the version and
	// returns the number of rows deleted.
	DeleteAcksForUsers(ctx context.Context, policyVersionID string, userIDs []string) (int, error)
	// VersionsAckedByUser returns the distinct policy version IDs the user has
	// acked.
	VersionsAckedByUser(ctx context.Context, userID string) ([]string, error)
	// AckedAtForVersion returns user_id -> acked_at for the version.
	AckedAtForVersion(ctx context.Context, policyVersionID string) (map[string]time.Time, error)
	// DailyAckCounts returns YYYY-MM-DD (UTC) -> ack count at/after since.
	DailyAckCounts(ctx context.Context, policyVersionID string, since time.Time) (map[string]int, error)
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
}

// NewResolver constructs a Resolver. views is satisfied by *store.PolicyViewStore.
// chains is satisfied by a ChainAdapter wrapping the group-service client.
func NewResolver(core coreObligations, idn identityUsers, chains categoryChains, acks acksStore, views viewsStore) *Resolver {
	return &Resolver{core: core, idn: idn, chains: chains, acks: acks, views: views}
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
}

// policyCtx holds pre-fetched per-policy context for obligation evaluation.
type policyCtx struct {
	homeGroupID  string
	policyNumber string
	chain        []authz.CategoryRuleset
	// sensitive is the policy's SENSITIVE classification; the sensitivity gate in
	// obligated uses it to drop obligations for uncleared subjects.
	sensitive bool
}

// obligated reports whether a user must acknowledge a policy under its
// category chain. It is the one source of obligation truth for every path
// (my obligations, my summary, audiences, completion, rosters, purges):
//
// 1. a deny override: no obligation;
// 2. an allow override settles read, so only an acknowledge rule must match;
// 3. otherwise the sensitivity gate, then the read-gated acknowledge decision.
//
// The sensitivity gate keeps an uncleared user from owing an acknowledgement
// for a sensitive policy they can never open. It mirrors the gateway's read
// gate, which an allow override also bypasses. The subject is evaluated on
// merit only: site admin and root never auto-acknowledge.
func obligated(ctx context.Context, userID string, uc userCtx, pc policyCtx) bool {
	merit := authz.Subject{UserID: userID, Groups: uc.adGroups}
	switch uc.overrides[pc.policyNumber] {
	case "deny":
		return false
	case "allow":
		return authz.Acknowledgement(ctx, merit, pc.chain).Allowed
	default:
		// The twin of the gateway's read gate: keep the two in lockstep.
		if pc.sensitive && !authz.HasCapability(clearance(uc), authz.PolicyReadSensitive) {
			return false
		}
		return authz.Resolve(ctx, merit, pc.chain).Acknowledge.Allowed
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
	return authz.Subject{Roles: roles, ReadSensitive: uc.readSensitive}
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
	chain, err := r.chains.BuildCategoryChain(ctx, hg)
	if err != nil {
		return nil, err
	}

	sensitive, err := r.core.PolicySensitivity(ctx, policyID)
	if err != nil {
		return nil, err
	}
	pc := policyCtx{homeGroupID: hg, policyNumber: num, chain: chain, sensitive: sensitive}

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
		roles, readSensitive, err := r.idn.UserReadClearance(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		uc := userCtx{adGroups: adGroups, overrides: overrides, roles: roles, readSensitive: readSensitive}
		if obligated(ctx, u.ID, uc, pc) {
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
	roles, readSensitive, err := r.idn.UserReadClearance(ctx, userID)
	if err != nil {
		return nil, err
	}
	uc := userCtx{adGroups: adGroups, overrides: overrides, roles: roles, readSensitive: readSensitive}

	// 2. Fetch all currently obligating policies from core.
	policies, err := r.core.ListObligatingPolicies(ctx)
	if err != nil {
		return nil, err
	}

	// Per-request chain cache: build each category's chain once per policy home group.
	chainCache := make(map[string][]authz.CategoryRuleset)

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
		chain, ok := chainCache[hg]
		if !ok && r.chains != nil && hg != "" {
			chain, err = r.chains.BuildCategoryChain(ctx, hg)
			if err != nil {
				return nil, err
			}
			chainCache[hg] = chain
		}

		sensitive, err := r.core.PolicySensitivity(ctx, p.PolicyID)
		if err != nil {
			return nil, err
		}
		pc := policyCtx{homeGroupID: hg, policyNumber: p.Number, chain: chain, sensitive: sensitive}
		if !obligated(ctx, userID, uc, pc) {
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

		out = append(out, ObligationItem{
			PolicyID:        p.PolicyID,
			Number:          p.Number,
			Title:           p.Title,
			VersionNo:       p.VersionNo,
			PolicyVersionID: p.PublishedVersionID,
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
	roles, readSensitive, err := r.idn.UserReadClearance(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	uc := userCtx{adGroups: adGroups, overrides: overrides, roles: roles, readSensitive: readSensitive}

	// 2. Fetch all currently obligating policies from core.
	policies, err := r.core.ListObligatingPolicies(ctx)
	if err != nil {
		return 0, 0, err
	}

	// Per-request chain cache.
	chainCache := make(map[string][]authz.CategoryRuleset)

	for _, p := range policies {
		if overrides[p.Number] == "deny" {
			continue
		}

		hg, err := r.core.PolicyHomeGroup(ctx, p.PolicyID)
		if err != nil {
			return 0, 0, err
		}
		chain, ok := chainCache[hg]
		if !ok && r.chains != nil && hg != "" {
			chain, err = r.chains.BuildCategoryChain(ctx, hg)
			if err != nil {
				return 0, 0, err
			}
			chainCache[hg] = chain
		}

		sensitive, err := r.core.PolicySensitivity(ctx, p.PolicyID)
		if err != nil {
			return 0, 0, err
		}
		pc := policyCtx{homeGroupID: hg, policyNumber: p.Number, chain: chain, sensitive: sensitive}
		if !obligated(ctx, userID, uc, pc) {
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

	// 4. Overdue = audience members who have not acked.
	for _, u := range audience {
		if _, ok := ackedSet[u.ID]; !ok {
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
// version-keyed calls (Roster, PurgeOrphanedAcks, CompletionMetrics, Activity).
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

// PurgeOrphanedAcks deletes ack rows for policyVersionID whose user is no longer
// in the version's current RACI audience.
func (r *Resolver) PurgeOrphanedAcks(ctx context.Context, policyVersionID string) (int, error) {
	audience, err := r.audienceFor(ctx, policyVersionID)
	if err != nil {
		return 0, err
	}
	audSet := make(map[string]struct{}, len(audience))
	for _, u := range audience {
		audSet[u.ID] = struct{}{}
	}

	ackedIDs, err := r.acks.AckedUserIDsForVersion(ctx, policyVersionID)
	if err != nil {
		return 0, err
	}

	var orphaned []string
	for _, id := range ackedIDs {
		if _, ok := audSet[id]; !ok {
			orphaned = append(orphaned, id)
		}
	}
	if len(orphaned) == 0 {
		return 0, nil
	}
	return r.acks.DeleteAcksForUsers(ctx, policyVersionID, orphaned)
}

// ReconcileUserAcks purges orphaned acks across every version the user has
// acked.
func (r *Resolver) ReconcileUserAcks(ctx context.Context, userID string) (int, error) {
	versionIDs, err := r.acks.VersionsAckedByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, v := range versionIDs {
		n, err := r.PurgeOrphanedAcks(ctx, v)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// ReconcilePolicyAcks purges orphaned acks across EVERY version of policyID.
func (r *Resolver) ReconcilePolicyAcks(ctx context.Context, policyID string) (int, error) {
	versionIDs, err := r.core.ListPolicyVersionIDs(ctx, policyID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, v := range versionIDs {
		n, err := r.PurgeOrphanedAcks(ctx, v)
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
	for _, u := range audience {
		if t, ok := at[u.ID]; ok {
			acked = append(acked, RosterEntry{ID: u.ID, Email: u.Email, AckedAt: t})
		} else {
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
