// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// Wire-format type (mirrors the obligation-changed event core/identity publish)
// ---------------------------------------------------------------------------

// obligationChangedEvent is the minimal payload the consumer needs off the wire.
// Publishers (core group/policy governance mutations, identity membership
// changes) emit ONE event per affected policy so the reconcile stays a simple,
// idempotent per-policy operation.
type obligationChangedEvent struct {
	EventType string `json:"event_type"`
	PolicyID  string `json:"policy_id"`
}

// ---------------------------------------------------------------------------
// Dependency interface
// ---------------------------------------------------------------------------

// AckReconciler purges acks that are orphaned when users leave a policy's
// obligated audience. *obligation.Resolver satisfies this interface; tests use
// a fake.
type AckReconciler interface {
	// ReconcilePolicyAcks deletes orphaned acks across every version of policyID
	// and returns the number of rows removed.
	ReconcilePolicyAcks(ctx context.Context, policyID string) (int, error)
}

// ObligatingInvalidator drops the cached global obligating-policy set so the next
// read repopulates it. *obligation.CachedCore satisfies this; nil disables it.
type ObligatingInvalidator interface {
	InvalidateObligating(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// ObligationChangedConsumer handles AMQP messages on the
// "policy.obligation_changed" routing key. Each message names one policy whose
// obligated audience may have shrunk (a group's ack trigger turned off, an
// audience override changed, a user removed from a group, a category moved).
// The consumer re-resolves the policy's current audience and deletes any ack
// whose user is no longer in it — enforcing "kill the ack when the obligation
// goes away."
type ObligationChangedConsumer struct {
	reconciler AckReconciler
	cache      ObligatingInvalidator // optional
}

// NewObligationChangedConsumer constructs an ObligationChangedConsumer.
func NewObligationChangedConsumer(r AckReconciler) *ObligationChangedConsumer {
	return &ObligationChangedConsumer{reconciler: r}
}

// WithCacheInvalidator wires the obligating-set cache so a governance/audience
// change busts it (the change can add/remove obligating policies). Returns the
// consumer for chaining; nil is a no-op.
func (c *ObligationChangedConsumer) WithCacheInvalidator(inv ObligatingInvalidator) *ObligationChangedConsumer {
	c.cache = inv
	return c
}

// Handle processes a single raw AMQP message body (JSON obligationChangedEvent).
// It is idempotent: re-delivery re-runs the reconcile, which is a no-op once the
// orphaned acks are gone.
func (c *ObligationChangedConsumer) Handle(ctx context.Context, body []byte) error {
	var evt obligationChangedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/obligation_changed: unmarshal: %w", err)
	}
	if evt.PolicyID == "" {
		return fmt.Errorf("consumer/obligation_changed: missing policy_id in event")
	}
	if _, err := c.reconciler.ReconcilePolicyAcks(ctx, evt.PolicyID); err != nil {
		return fmt.Errorf("consumer/obligation_changed: reconcile %q: %w", evt.PolicyID, err)
	}
	// A governance/audience change can add or remove obligating policies — bust
	// the cached global set (best-effort; a stale entry still expires via TTL).
	if c.cache != nil {
		_ = c.cache.InvalidateObligating(ctx)
	}
	return nil
}
