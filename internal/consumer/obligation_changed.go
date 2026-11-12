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

// AckReconciler releases the pending acknowledgements of users who leave a
// policy's obligated audience. Acknowledgements already given are kept.
// *obligation.Resolver satisfies this interface; tests use a fake.
type AckReconciler interface {
	// ReconcilePolicyAcks releases the pending state of users no longer in the
	// audience across every version of policyID and returns the number of
	// entries released. It never deletes an acknowledgement.
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
// The consumer re-resolves the policy's current audience and releases the
// pending acknowledgement of any user no longer in it, so they stop being
// reminded. An acknowledgement already given is kept.
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
// dropped pending entries are released.
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
