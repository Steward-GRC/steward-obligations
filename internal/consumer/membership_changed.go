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

// UserAckReconciler purges a single user's acks that are orphaned once their
// group membership changes. *obligation.Resolver satisfies this interface.
type UserAckReconciler interface {
	// ReconcileUserAcks re-checks every version the user has acked and deletes the
	// ones they are no longer obligated for; returns the number of rows removed.
	ReconcileUserAcks(ctx context.Context, userID string) (int, error)
}

// MembershipChangedConsumer handles AMQP messages on the "membership.changed"
// routing key. When a user is removed from a directory group, the policies
// whose audience included that group no longer obligate them, so their earlier
// acknowledgements are orphaned. The consumer re-checks every version the user
// acked and removes the ones they have left the audience of.
type MembershipChangedConsumer struct {
	reconciler UserAckReconciler
}

// NewMembershipChangedConsumer constructs a MembershipChangedConsumer.
func NewMembershipChangedConsumer(r UserAckReconciler) *MembershipChangedConsumer {
	return &MembershipChangedConsumer{reconciler: r}
}

// Handle processes a single raw AMQP message body (JSON membershipChangedEvent).
// It is idempotent: re-delivery re-runs the reconcile, a no-op once orphaned
// acks are gone.
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
