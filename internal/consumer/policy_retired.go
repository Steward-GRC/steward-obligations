// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// ---------------------------------------------------------------------------
// Wire-format type (mirrors core's policy.retired event — no core import)
// ---------------------------------------------------------------------------

// policyRetiredEvent mirrors the raw JSON core emits on the "jobs" exchange
// under the "policy.retired" routing key. We keep it local so this package has
// no dependency on the core module, exactly as policy_published.go mirrors
// lifecycle.PublishEvent.
type policyRetiredEvent struct {
	EventType string    `json:"event_type"` // "policy.retired"
	RetiredAt time.Time `json:"retired_at"`
	PolicyID  string    `json:"policy_id"`
	Number    string    `json:"number"`
	Title     string    `json:"title"`
}

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// RetireAudienceResolver is the subset of obligation.Resolver the retire
// consumer needs: the RACI-resolved FULL ack audience plus a human display
// lookup (number + title). *obligation.Resolver satisfies it; tests use a fake.
type RetireAudienceResolver interface {
	// AudienceUsers returns the RACI-resolved ack audience for policyID. It
	// fails loud (returns an error, never a silently-empty slice) when the
	// policy has no home category or the chain cannot be built.
	AudienceUsers(ctx context.Context, policyID string) ([]obligation.User, error)
	// PolicyDisplay returns the policy's human display number and title so the
	// email shows a human reference/title, never the raw policy UUID.
	PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error)
}

// RetireNotifier is the subset of notify.Dispatcher the retire consumer needs.
type RetireNotifier interface {
	SendPolicyRetired(ctx context.Context, p notify.AckReminderPayload)
}

// RetiredNotifiedTracker records, per (policy, user), whether the retire notice
// has already been sent, so a broker redelivery notifies each recipient at most
// once. *store.PolicyRetiredNotifiedStore satisfies it; nil (not wired)
// disables the guard (a redelivery would re-notify, which is benign but noisy).
type RetiredNotifiedTracker interface {
	MarkNotifiedIfFirst(ctx context.Context, policyID, userID string) (bool, error)
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// PolicyRetiredConsumer handles AMQP messages from the "jobs" exchange on the
// "policy.retired" routing key.
//
// On retire, core cancels the policy's outstanding obligations (a retired
// policy obligates no one, so it drops from every user's "my obligations"
// automatically because cn sources the obligating set from core's
// ListObligatingPolicies). COMPLETED acks in the acknowledgments table are KEPT
// as history. This consumer's ONLY job is therefore to NOTIFY the full ack
// audience that the policy is retired — it MUST NOT delete or mutate any ack
// rows.
//
// For each message it:
// 1. Decodes the PolicyID (errors if empty).
// 2. Best-effort busts the obligating-set cache (a retire shrinks it).
// 3. Resolves the FULL ack audience via AudienceUsers — on error RETURNS the
// error (nack/requeue), exactly like policy_published's fail-loud handling.
// 4. Derives the display number/title: prefers the event's own fields, and
// only when both are empty falls back to a best-effort PolicyDisplay.
// 5. Sends SendPolicyRetired once per audience user (skipping any already
// notified per the tracker), idempotent on redelivery.
type PolicyRetiredConsumer struct {
	obl      RetireAudienceResolver
	notifier RetireNotifier
	cache    ObligatingInvalidator  // optional
	notified RetiredNotifiedTracker // optional
}

// NewPolicyRetiredConsumer constructs a PolicyRetiredConsumer.
func NewPolicyRetiredConsumer(obl RetireAudienceResolver, notifier RetireNotifier) *PolicyRetiredConsumer {
	return &PolicyRetiredConsumer{obl: obl, notifier: notifier}
}

// WithCacheInvalidator wires the obligating-set cache so a retire busts it (the
// retired policy leaves the global obligating set). Returns the consumer for
// chaining; nil is a no-op.
func (c *PolicyRetiredConsumer) WithCacheInvalidator(inv ObligatingInvalidator) *PolicyRetiredConsumer {
	c.cache = inv
	return c
}

// WithNotifiedTracker wires the per-(policy, user) notified marker so each
// recipient is notified once even across redeliveries. Returns the consumer for
// chaining; nil disables the guard (no behavior change beyond possible
// duplicate notices on redelivery).
func (c *PolicyRetiredConsumer) WithNotifiedTracker(t RetiredNotifiedTracker) *PolicyRetiredConsumer {
	c.notified = t
	return c
}

// Handle processes a single raw AMQP message body (JSON policyRetiredEvent). It
// is idempotent: the per-(policy, user) tracker suppresses re-notification on
// redelivery, and it never mutates ack state.
func (c *PolicyRetiredConsumer) Handle(ctx context.Context, body []byte) error {
	var evt policyRetiredEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/policy_retired: unmarshal: %w", err)
	}
	if evt.PolicyID == "" {
		return fmt.Errorf("consumer/policy_retired: missing policy_id in event")
	}

	// A retire removes a requires-ack policy from the global obligating set —
	// bust the cache so the next obligation/summary read reflects it
	// (best-effort; the TTL is the safety net).
	if c.cache != nil {
		_ = c.cache.InvalidateObligating(ctx)
	}

	// Resolve the FULL ack audience from the RACI decision engine. AudienceUsers
	// FAILS LOUD on a nil/empty chain or a missing home category: we propagate
	// that error to nack/requeue the message rather than materialize an empty
	// audience and silently drop every notification.
	audience, err := c.obl.AudienceUsers(ctx, evt.PolicyID)
	if err != nil {
		return fmt.Errorf("consumer/policy_retired: resolve RACI audience for %q: %w", evt.PolicyID, err)
	}

	// Human display: prefer the event's own number/title; only when BOTH are
	// empty fall back to a best-effort core lookup. On failure leave them empty
	// and let the template render a generic placeholder — never the raw UUID.
	number, title := evt.Number, evt.Title
	if number == "" && title == "" {
		n, t, derr := c.obl.PolicyDisplay(ctx, evt.PolicyID)
		if derr != nil {
			logger := logctx.From(ctx)
			logger.Debug().Err(derr).Str("policy_id", evt.PolicyID).
				Msg("consumer/policy_retired: PolicyDisplay lookup failed; email omits the policy reference")
		} else {
			number, title = n, t
		}
	}

	// Resolve the human-readable retirement date the email renders in its
	// "Retired" row from the event's own timestamp. Empty when the event carries
	// no timestamp, in which case retiredVars falls back to a non-empty
	// placeholder rather than a blank row.
	retiredDate := ""
	if !evt.RetiredAt.IsZero() {
		retiredDate = evt.RetiredAt.Format("January 2, 2006")
	}

	for _, u := range audience {
		// Idempotency: notify each (policy, user) at most once across
		// redeliveries. A tracker error must never drop the send — degrade to
		// sending (a duplicate notice is far better than a missed one).
		if c.notified != nil {
			first, err := c.notified.MarkNotifiedIfFirst(ctx, evt.PolicyID, u.ID)
			if err != nil {
				logger := logctx.From(ctx)
				logger.Warn().Err(err).Str("user_id", u.ID).Str("policy_id", evt.PolicyID).
					Msg("consumer/policy_retired: notified-tracker check failed; sending anyway")
			} else if !first {
				continue
			}
		}

		c.notifier.SendPolicyRetired(ctx, notify.AckReminderPayload{
			UserID:        u.ID,
			Type:          "policy_retired",
			Policies:      []notify.AckItem{{Ref: number, Title: title}},
			EffectiveDate: retiredDate,
		})
	}

	return nil
}
