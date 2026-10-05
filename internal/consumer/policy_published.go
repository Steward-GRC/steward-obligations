// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package consumer contains AMQP message consumers for the obligations service.
// Each consumer decodes a JSON message body, calls the appropriate domain
// service, and returns an error only when the message should be nack'd and
// re-queued.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// ---------------------------------------------------------------------------
// Wire-format types (mirror lifecycle.PublishEvent in core — no import needed)
// ---------------------------------------------------------------------------

// publishEventVersion is the minimal subset of lifecycle.PolicyVersionContent
// the consumer needs off the wire.
//
// EffectiveDate is the policy's DISTINCT effective date, denormalized by core
// onto the publish event. It is a pointer and absent (nil) when the policy has
// no explicit effective date, in which case the consumer falls back to the
// publish timestamp for the email's "Effective" row.
//
// DocumentType is the document kind ("policy" | "procedure", ),
// denormalized by core onto the publish event. A PROCEDURE is excluded from the
// ack/obligation pipeline entirely: the consumer skips it before
// resolving any obligation, so a published procedure never creates an ack
// obligation, ack-required/reminder email, or escalation. Absent/empty on the
// wire (omitempty) is treated as "policy" — the historical default — so an
// ordinary policy publish is unchanged.
type publishEventVersion struct {
	PolicyID      string     `json:"PolicyID"`
	VersionID     string     `json:"VersionID"`
	EffectiveDate *time.Time `json:"EffectiveDate,omitempty"`
	DocumentType  string     `json:"DocumentType,omitempty"`
}

// documentTypeProcedure is the discriminator value core stamps on the publish
// event for a procedure. the obligations service excludes it from the
// ack/obligation pipeline. The comparison in Handle is case-insensitive
// (strings.EqualFold) because stops core from emitting policy.published
// for procedures at all — procedures move to their own "procedure.published" /
// "procedure.retired" routing keys, whose envelope stamps DocumentType as the
// uppercase "PROCEDURE", while this lowercase
// "procedure" value remains the historical spelling used elsewhere. cn
// never binds the new procedure.* routing keys, so this branch should be
// unreachable in normal operation; it exists purely as a defense-in-depth
// backstop in case a mis-routed or legacy-shaped procedure event ever lands on
// the policy.published queue, under either casing.
const documentTypeProcedure = "procedure"

// publishEvent mirrors lifecycle.PublishEvent. We keep it local so this
// package has no dependency on the core module.
type publishEvent struct {
	EventType   string              `json:"event_type"`
	PublishedAt time.Time           `json:"published_at"`
	Version     publishEventVersion `json:"version"`
}

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// ObligationResolver is the subset of obligation.Resolver that the consumer
// requires. *obligation.Resolver satisfies this interface; tests use fakes.
type ObligationResolver interface {
	// ResolvePolicyObligation returns the ack obligation metadata for policyID.
	ResolvePolicyObligation(ctx context.Context, policyID string) (obligation.PolicyObligation, error)
	// AudienceUsers returns the RACI-resolved ack audience for policyID: every
	// enabled user obligated to ack under the policy's home-category RACI chain
	// (with per-policy override handling). It fails loud (returns an error) when
	// the policy has no home category or the chain cannot be built, so an
	// accidental empty audience can never silently suppress notifications.
	AudienceUsers(ctx context.Context, policyID string) ([]obligation.User, error)
	// MyObligations returns every currently outstanding obligation for userID
	// (across ALL obligating policies, not just the one just published). The
	// consumer uses it to fold a persona's full due-ack backlog into ONE
	// consolidated reminder email instead of sending a separate email per
	// policy. *obligation.Resolver already implements this for the portal's
	// "my obligations" view, so reusing it here does not touch obligation
	// reconciliation itself -- it is a pure read.
	MyObligations(ctx context.Context, userID string) ([]obligation.ObligationItem, error)
	// PolicyDisplay returns the policy's human-facing display number (e.g.
	// "POL-001") and title. The consumer stamps these onto the notification
	// payload so emails show a human reference/title, never the raw policy
	// version UUID.
	PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error)
}

// AcksStore is the subset of the ack persistence layer the consumer needs.
type AcksStore interface {
	// AckedUserIDsForVersion returns the user IDs that have already acked the
	// given policyVersionID.
	AckedUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error)
}

// Notifier is the subset of notify.Dispatcher that the consumer needs. It
// covers both the ack-directed sends (reminder/ack-required, discriminated by
// AckReminderPayload.Type) and the informational policy-published fan-out.
type Notifier interface {
	SendAckReminder(ctx context.Context, p notify.AckReminderPayload)
	SendPolicyPublished(ctx context.Context, p notify.AckReminderPayload)
}

// AckAudienceSummary describes the ack audience materialized for a single
// published policy version. It is emitted as ONE audit-tier event per publish
// rather than one row per user, so a large audience does not flood the
// append-only audit hash chain.
type AckAudienceSummary struct {
	// PolicyVersionID is the published version whose ack audience was resolved.
	PolicyVersionID string
	// UserCount is the total number of users in the RACI-resolved audience.
	UserCount int
	// GroupID is the policy's owning group, when available (may be empty).
	GroupID string
}

// AckAudienceAuditor emits a single summary audit event describing the ack
// audience for a published policy version. Failures are logged by the caller
// but do NOT fail the publish handling: notifications are the primary
// deliverable and the summary is best-effort compliance evidence.
type AckAudienceAuditor interface {
	EmitAckAudience(ctx context.Context, s AckAudienceSummary) error
}

// NewUserGate throttles a brand-new account's very first ack-reminder cycle
// so it is never blasted with its entire standing ack backlog the moment
// the obligations service first observes it as obligated; the backlog stays
// visible in the portal, and the next normal reminder cycle after the grace
// window elapses delivers it by email like any other outstanding
// obligation. *store.UserFirstSeenStore satisfies this; nil (no gate wired)
// disables the throttle rather than blocking any delivery.
type NewUserGate interface {
	// ShouldSkipForNewUser records the first time userID is observed by the
	// ack-reminder path (a no-op if already recorded) and reports whether
	// userID is still inside the new-user grace window -- true means this
	// cycle's reminder must be skipped for userID.
	ShouldSkipForNewUser(ctx context.Context, userID string) (bool, error)
}

// NotifiedTracker records, per (user, version), whether a notification has
// already been emitted, so the consumer can classify a recipient's FIRST
// acknowledgement demand for a version (ack-required) apart from a repeat
// reminder (policy-ack-reminder). *store.UserVersionNotifiedStore satisfies
// it; nil (not wired) disables classification -- every outstanding recipient
// falls back to the existing repeat-reminder behavior.
type NotifiedTracker interface {
	MarkNotifiedIfFirst(ctx context.Context, userID, policyVersionID string) (bool, error)
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// PolicyPublishedConsumer handles AMQP messages from the "jobs" exchange on
// the "policy.published" routing key.
//
// For each message it:
// 1. Decodes the PolicyID and VersionID from the wire payload.
// 2. Resolves the policy's ack obligation from core (via ObligationResolver).
// 3. If requires_ack && on_change: derives the ack audience from the RACI
// decision engine (ObligationResolver.AudienceUsers — the same lineage-aware
// ruleset the ack resolver uses), subtracts users who already acked the
// current published version, and calls Notifier.SendAckReminder ONCE per
// outstanding user with their FULL due-ack backlog attached (never one
// email per policy -- see NewUserGate's doc for the new-user throttle
// that additionally holds back a freshly obligated account's very first
// cycle).
// 4. Otherwise silently returns (no notification, no error).
type PolicyPublishedConsumer struct {
	obl             ObligationResolver
	acks            AcksStore
	notifier        Notifier
	auditor         AckAudienceAuditor    // may be nil (audit summary is best-effort)
	cache           ObligatingInvalidator // optional
	newUsers        NewUserGate           // optional (new-user throttle)
	throttleEnabled bool                  // gates newUsers; default false, see WithNewUserThrottleEnabled
	portalURL       string                // optional; base URL for a consolidated item's AckURL
	notified        NotifiedTracker       // optional; classifies first (ack-required) vs repeat
}

// NewPolicyPublishedConsumer constructs a PolicyPublishedConsumer. auditor may
// be nil, in which case no ack-audience summary audit event is emitted.
func NewPolicyPublishedConsumer(obl ObligationResolver, acks AcksStore, notifier Notifier, auditor AckAudienceAuditor) *PolicyPublishedConsumer {
	return &PolicyPublishedConsumer{obl: obl, acks: acks, notifier: notifier, auditor: auditor}
}

// WithCacheInvalidator wires the obligating-set cache so a publish busts it (a
// newly published requires-ack policy joins the global obligating set). Returns
// the consumer for chaining; nil is a no-op.
func (c *PolicyPublishedConsumer) WithCacheInvalidator(inv ObligatingInvalidator) *PolicyPublishedConsumer {
	c.cache = inv
	return c
}

// WithNewUserGate wires the new-user throttle (see NewUserGate). Returns the
// consumer for chaining; nil disables the throttle (no behavior change). The
// throttle only actually applies once WithNewUserThrottleEnabled(true) is
// also set -- wiring the gate alone does not turn on suppression.
func (c *PolicyPublishedConsumer) WithNewUserGate(g NewUserGate) *PolicyPublishedConsumer {
	c.newUsers = g
	return c
}

// WithNewUserThrottleEnabled turns the new-user throttle (see NewUserGate) on
// or off. It defaults to false (off): the "first seen" signal is global
// per-user (first time the obligations service's ack-reminder path has EVER
// observed this user), not scoped to account-creation time -- there is no
// account-creation-time signal available to this service. At cold start
// (fresh deploy, empty user_notify_first_seen table) leaving this on by
// default would throttle EVERY currently-outstanding existing user's
// legitimate reminder for one cycle, mistaking "never seen by this table"
// for "brand-new account". Enable this deliberately, either after seeding
// user_notify_first_seen for the existing user base or after consciously
// accepting a one-cycle delay for everyone. Returns the consumer for
// chaining.
func (c *PolicyPublishedConsumer) WithNewUserThrottleEnabled(enabled bool) *PolicyPublishedConsumer {
	c.throttleEnabled = enabled
	return c
}

// WithPortalURL sets the base portal URL used to build each consolidated
// AckItem's AckURL (base + "/" + policyVersionID, matching notify.EmailChannel's
// own single-item derivation). Returns the consumer for chaining; the zero
// value produces a bare policy-version-id AckURL.
func (c *PolicyPublishedConsumer) WithPortalURL(url string) *PolicyPublishedConsumer {
	c.portalURL = url
	return c
}

// WithNotifiedTracker wires the per-(user, version) notified marker used to
// classify a recipient's first acknowledgement demand (ack-required) apart
// from a repeat reminder (policy-ack-reminder). Returns the consumer for
// chaining; nil leaves every outstanding recipient on the existing
// repeat-reminder path (no behavior change).
func (c *PolicyPublishedConsumer) WithNotifiedTracker(t NotifiedTracker) *PolicyPublishedConsumer {
	c.notified = t
	return c
}

// Handle processes a single raw AMQP message body (JSON-encoded PublishEvent).
// It is idempotent: re-delivery is safe because SendAckReminder is
// non-destructive and duplicate notifications are benign compared to missed
// ones.
func (c *PolicyPublishedConsumer) Handle(ctx context.Context, body []byte) error {
	var evt publishEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/policy_published: unmarshal: %w", err)
	}

	// P2 — the "no ack" boundary for Procedures. A published PROCEDURE
	// is excluded from the ack/obligation pipeline entirely: it must never
	// create an ack obligation, ack-required/reminder email, or escalation. Skip
	// it here, before any obligation resolution, audience materialization, or
	// send. This is the notify-layer gate (defense-in-depth alongside core, which
	// also excludes procedures from ResolvePolicyObligation/ListObligatingPolicies).
	// It suppresses ONLY the ack machinery — non-ack eventing (audit, AI reindex,
	// UI freshness) flows through separate consumers and is unaffected. Returning
	// nil acks the message (a procedure publish is a valid, fully-handled event,
	// not a failure to requeue). An empty/absent DocumentType is the historical
	// "policy" default and proceeds normally.
	//
	// defense-in-depth: cn does not bind the "procedure.published" /
	// "procedure.retired" routing keys introduces (procedures never
	// notify in the new design), so this consumer should never see a procedure
	// event at all. The check stays here — and is case-insensitive — purely as a
	// backstop in case a stray or mis-routed procedure event (under either the
	// historical lowercase "procedure" or 's uppercase "PROCEDURE" wire
	// value) ever lands on the policy.published queue.
	if strings.EqualFold(evt.Version.DocumentType, documentTypeProcedure) {
		logger := logctx.From(ctx)
		logger.Debug().Str("policy_id", evt.Version.PolicyID).
			Str("document_type", evt.Version.DocumentType).
			Msg("consumer/policy_published: skipping procedure-shaped event; procedures never notify")
		return nil
	}

	policyID := evt.Version.PolicyID
	versionID := evt.Version.VersionID
	if policyID == "" || versionID == "" {
		return fmt.Errorf("consumer/policy_published: missing PolicyID or VersionID in event")
	}

	// A publish can add a requires-ack policy to the global obligating set — bust
	// the cache so the next obligation/summary read reflects it (best-effort; TTL
	// is the safety net). Done regardless of on-change (the set includes on-publish).
	if c.cache != nil {
		_ = c.cache.InvalidateObligating(ctx)
	}

	// Step 1: resolve the obligation metadata for this policy.
	obl, err := c.obl.ResolvePolicyObligation(ctx, policyID)
	if err != nil {
		return fmt.Errorf("consumer/policy_published: resolve obligation for %q: %w", policyID, err)
	}

	// Step 2: only notify for requires_ack + on_change policies.
	// on_publish policies do not need re-ack on each new version; the user's
	// existing ack for any prior version continues to satisfy the obligation.
	if !obl.RequiresAck || !obl.OnChange {
		return nil
	}

	// Step 3: derive the ack audience from the RACI decision engine — every
	// enabled user for whom authz.Resolve(...).Ack is allowed over the policy's
	// home-category chain (with per-policy override handling), exactly as the ack
	// resolver obligates. AudienceUsers FAILS LOUD on a nil/empty chain or a
	// missing home category: we propagate that error to nack the message rather
	// than materialize an empty audience and silently drop every notification.
	audience, err := c.obl.AudienceUsers(ctx, policyID)
	if err != nil {
		return fmt.Errorf("consumer/policy_published: resolve RACI audience for %q: %w", policyID, err)
	}

	// Step 3b: emit ONE summary audit event describing the materialized ack
	// audience. We deliberately emit a single event per publish rather than one
	// audit row per user: a large audience would otherwise flood the
	// append-only audit hash chain. This is best-effort compliance evidence —
	// an emit failure must NOT fail the handler, because that would re-queue the
	// message and re-send every per-user notification below.
	if c.auditor != nil {
		_ = c.auditor.EmitAckAudience(ctx, AckAudienceSummary{
			PolicyVersionID: versionID,
			UserCount:       len(audience),
			// PolicyObligation carries no owning group id; leave GroupID empty.
		})
	}

	// Informational fan-out: every audience member gets a "new policy
	// published" notice once per (version, user), independent of ack state.
	// Deduplication is enforced downstream by internal/mail's Deduper hook
	// (key userID:policy-published:versionID), so a redelivered publish does
	// not re-notify. Failures degrade per-channel inside the dispatcher; they
	// must not fail the handler (which would requeue and re-run the ack sends
	// below).
	// Resolve the just-published policy's human display (number + title) ONCE so
	// every email below shows a human reference/title instead of the raw policy
	// version UUID. Best-effort: on failure leave them empty and the templates
	// fall back to a generic placeholder (never the UUID).
	pubNumber, pubTitle, err := c.obl.PolicyDisplay(ctx, policyID)
	if err != nil {
		logger := logctx.From(ctx)
		logger.Debug().Err(err).Str("policy_id", policyID).
			Msg("consumer/policy_published: PolicyDisplay lookup failed; emails omit the policy reference")
		pubNumber, pubTitle = "", ""
	}
	// pubPolicies is the single-policy AckItem for the just-published version —
	// used for the informational fan-out and as the fallback when a persona's
	// backlog lookup yields nothing. Nil when no human display is available.
	var pubPolicies []notify.AckItem
	if pubNumber != "" || pubTitle != "" {
		pubPolicies = []notify.AckItem{{Ref: pubNumber, Title: pubTitle, AckURL: c.ackURL(versionID)}}
	}

	// Resolve the effective date the policy-published email renders in its
	// "Effective" row, formatted human-readable (e.g. "September 1, 2026").
	// Prefer the policy's DISTINCT effective date carried on the event; when
	// the policy has no explicit effective date, fall back to the publish
	// timestamp. Empty only when neither is present, in which case EmailChannel
	// falls back to a non-empty placeholder rather than a blank row (was
	// : the email showed the publish date as "Effective"
	// even when the policy had its own distinct effective date, and showed the
	// literal "the publish date" placeholder when no date was available).
	effectiveDate := ""
	switch {
	case evt.Version.EffectiveDate != nil && !evt.Version.EffectiveDate.IsZero():
		effectiveDate = evt.Version.EffectiveDate.Format("January 2, 2006")
	case !evt.PublishedAt.IsZero():
		effectiveDate = evt.PublishedAt.Format("January 2, 2006")
	}

	for _, u := range audience {
		c.notifier.SendPolicyPublished(ctx, notify.AckReminderPayload{
			UserID:          u.ID,
			PolicyVersionID: versionID,
			Type:            "policy_published",
			Policies:        pubPolicies,
			EffectiveDate:   effectiveDate,
		})
	}

	// Step 4: fetch users who have already acked this specific version.
	alreadyAcked, err := c.acks.AckedUserIDsForVersion(ctx, versionID)
	if err != nil {
		return fmt.Errorf("consumer/policy_published: acked users for %q: %w", versionID, err)
	}
	ackedSet := make(map[string]struct{}, len(alreadyAcked))
	for _, uid := range alreadyAcked {
		ackedSet[uid] = struct{}{}
	}

	// Step 5: notify the outstanding subset -- ONE consolidated email per
	// persona, never one per policy.
	for _, u := range audience {
		if _, done := ackedSet[u.ID]; done {
			continue
		}

		// New-user throttle: a freshly obligated account is not blasted with
		// its whole standing backlog the moment it is first observed. A gate
		// failure must not suppress a legitimate reminder, so fall through
		// and send normally.
		//
		// Gated behind throttleEnabled (default false): the underlying
		// signal is "first time this user_id has ever appeared in
		// user_notify_first_seen", a global per-user marker, NOT
		// account-creation time (no such signal is available to this
		// service). At cold start that table is empty, so applying the
		// throttle unconditionally would misread every currently
		// outstanding EXISTING user as brand-new and delay their legitimate
		// reminder by a full cycle right after a deploy. Leave this off
		// until the table has been seeded for the existing user base, or
		// until a one-cycle delay across the board is accepted deliberately.
		if c.throttleEnabled && c.newUsers != nil {
			skip, err := c.newUsers.ShouldSkipForNewUser(ctx, u.ID)
			if err != nil {
				logger := logctx.From(ctx)
				logger.Warn().Err(err).Str("user_id", u.ID).
					Msg("consumer/policy_published: new-user gate check failed; sending reminder anyway")
			} else if skip {
				continue
			}
		}

		// Classify first vs repeat for this (user, version). The FIRST
		// notification for a still-un-acked version is an ack-required demand;
		// later notifications are repeat reminders. A tracker error must never
		// drop the send -- degrade to the repeat-reminder path.
		sendType := "bulk_sweep"
		if c.notified != nil {
			first, err := c.notified.MarkNotifiedIfFirst(ctx, u.ID, versionID)
			if err != nil {
				logger := logctx.From(ctx)
				logger.Warn().Err(err).Str("user_id", u.ID).
					Msg("consumer/policy_published: notified-tracker check failed; sending repeat reminder")
			} else if first {
				sendType = "ack_required"
			}
		}

		payload := notify.AckReminderPayload{
			UserID:          u.ID,
			PolicyVersionID: versionID,
			Type:            sendType,
		}

		// Consolidation: fold the persona's FULL outstanding ack backlog
		// (across every obligating policy, not just the one just published)
		// into this one email. A lookup failure degrades to today's
		// single-item payload (EmailChannel derives one item from
		// PolicyVersionID) rather than dropping the notification.
		items, err := c.obl.MyObligations(ctx, u.ID)
		if err != nil {
			logger := logctx.From(ctx)
			logger.Warn().Err(err).Str("user_id", u.ID).
				Msg("consumer/policy_published: MyObligations lookup failed; sending single-policy reminder")
		} else if len(items) > 0 {
			if sendType == "ack_required" {
				// ack-required is single-item: show only the just-published
				// policy (matched by version id). If it is not present in the
				// backlog, leave Policies empty and let EmailChannel derive a
				// single item from PolicyVersionID.
				for _, it := range items {
					if it.PolicyVersionID == versionID {
						payload.Policies = []notify.AckItem{{
							Ref:    ackRef(it),
							Title:  it.Title,
							AckURL: c.ackURL(it.PolicyVersionID),
						}}
						break
					}
				}
			} else {
				payload.Policies = make([]notify.AckItem, len(items))
				for i, it := range items {
					payload.Policies[i] = notify.AckItem{
						Ref:    ackRef(it),
						Title:  it.Title,
						AckURL: c.ackURL(it.PolicyVersionID),
					}
				}
			}
		}

		// Fallback: no backlog item matched (MyObligations failed, or the just-
		// published version was not in the persona's backlog) — use the resolved
		// single-policy display so the email shows a human reference/title, never
		// the version UUID.
		if len(payload.Policies) == 0 && pubPolicies != nil {
			payload.Policies = pubPolicies
		}

		c.notifier.SendAckReminder(ctx, payload)
	}

	return nil
}

// ackRef formats an obligation.ObligationItem's short reference shown to the
// recipient, e.g. "POL-014 v3".
func ackRef(it obligation.ObligationItem) string {
	if it.VersionNo > 0 {
		return fmt.Sprintf("%s v%d", it.Number, it.VersionNo)
	}
	return it.Number
}

// ackURL builds a consolidated AckItem's absolute acknowledgement link from
// the configured portal base URL, matching notify.EmailChannel's own
// single-item derivation (base + "/" + policyVersionID).
func (c *PolicyPublishedConsumer) ackURL(policyVersionID string) string {
	if c.portalURL == "" {
		return policyVersionID
	}
	return fmt.Sprintf("%s/%s", c.portalURL, policyVersionID)
}
