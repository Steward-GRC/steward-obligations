// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// NotifInserter is the narrow interface InAppChannel needs from the
// notification store: persist one row and return the populated record. Using
// an interface (rather than depending on *store.NotificationStore directly)
// lets tests run without spinning up Postgres while still letting production
// wire the concrete pgx-backed store in.
type NotifInserter interface {
	Insert(ctx context.Context, n store.Notification) (store.Notification, error)
}

// InAppChannel writes a notification row so clients can display it in their
// in-app notification inbox. The pending row is later transitioned to
// status="sent" by a downstream consumer.
type InAppChannel struct{ ins NotifInserter }

// NewInAppChannel returns an InAppChannel backed by the supplied inserter.
func NewInAppChannel(ins NotifInserter) *InAppChannel { return &InAppChannel{ins: ins} }

// Send persists a notification row with channel="in_app" and a JSON payload
// containing the policy version id, campaign id, and reminder type so the
// client can render an appropriate message and deep-link.
func (c *InAppChannel) Send(ctx context.Context, p AckReminderPayload) error {
	n := store.Notification{
		UserID:  p.UserID,
		Type:    "ack_reminder",
		Channel: "in_app",
		Payload: map[string]any{
			"policy_version_id": p.PolicyVersionID,
			"campaign_id":       p.CampaignID,
			"reminder_type":     p.Type,
		},
	}
	_, err := c.ins.Insert(ctx, n)
	return err
}
