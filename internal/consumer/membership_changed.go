// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
)

// membershipChangedEvent is the minimal payload the consumer needs off the wire.
// Identity emits it when a user's directory-group membership changes (notably a
// removal), naming the user whose obligations may have shrunk.
type membershipChangedEvent struct {
	EventType string `json:"event_type"`
	UserID    string `json:"user_id"`
}

// UserAckReconciler releases a single user's pending acknowledgements once their
// group membership changes. Acknowledgements already given are kept.
// *obligation.Resolver satisfies this interface.
type UserAckReconciler interface {
	// ReconcileUserAcks re-checks every version the user has pending and
	// releases the ones they no longer owe; returns the number released. It
	// never deletes an acknowledgement.
	ReconcileUserAcks(ctx context.Context, userID string) (int, error)
}

// MembershipChangedConsumer handles AMQP messages on the "membership.changed"
// routing key. When a user is removed from a directory group, the policies
// whose audience included that group no longer obligate them. The consumer
// re-checks every version the user still owes and releases the ones they have
// left the audience of. Acknowledgements already given are kept.
type MembershipChangedConsumer struct {
	reconciler UserAckReconciler
}

// NewMembershipChangedConsumer constructs a MembershipChangedConsumer.
func NewMembershipChangedConsumer(r UserAckReconciler) *MembershipChangedConsumer {
	return &MembershipChangedConsumer{reconciler: r}
}

// Handle processes a single raw AMQP message body (JSON membershipChangedEvent).
// It is idempotent: re-delivery re-runs the reconcile, a no-op once the dropped
// pending entries are released.
func (c *MembershipChangedConsumer) Handle(ctx context.Context, body []byte) error {
	var evt membershipChangedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/membership_changed: unmarshal: %w", err)
	}
	if evt.UserID == "" {
		return fmt.Errorf("consumer/membership_changed: missing user_id in event")
	}
	if _, err := c.reconciler.ReconcileUserAcks(ctx, evt.UserID); err != nil {
		return fmt.Errorf("consumer/membership_changed: reconcile %q: %w", evt.UserID, err)
	}
	return nil
}
