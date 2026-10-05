// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// fakeNotifInserter satisfies notify.NotifInserter so InAppChannel can be
// tested without spinning up Postgres. Records every Notification handed in.
type fakeNotifInserter struct {
	got store.Notification
	err error
}

func (f *fakeNotifInserter) Insert(_ context.Context, n store.Notification) (store.Notification, error) {
	f.got = n
	if f.err != nil {
		return store.Notification{}, f.err
	}
	n.ID = "n-1"
	return n, nil
}

func TestInAppChannelWritesNotificationRow(t *testing.T) {
	ins := &fakeNotifInserter{}
	c := notify.NewInAppChannel(ins)
	if err := c.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1", Type: "initial",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ins.got.UserID != "u1" {
		t.Errorf("UserID: got %q", ins.got.UserID)
	}
	if ins.got.Channel != "in_app" {
		t.Errorf("Channel: got %q", ins.got.Channel)
	}
	if ins.got.Type != "ack_reminder" {
		t.Errorf("Type: got %q", ins.got.Type)
	}
	if ins.got.Payload["policy_version_id"] != "pv1" {
		t.Errorf("payload.policy_version_id: got %v", ins.got.Payload["policy_version_id"])
	}
	if ins.got.Payload["campaign_id"] != "c1" {
		t.Errorf("payload.campaign_id: got %v", ins.got.Payload["campaign_id"])
	}
	if ins.got.Payload["reminder_type"] != "initial" {
		t.Errorf("payload.reminder_type: got %v", ins.got.Payload["reminder_type"])
	}
}

func TestInAppChannelPropagatesInsertError(t *testing.T) {
	ins := &fakeNotifInserter{err: errors.New("db down")}
	c := notify.NewInAppChannel(ins)
	if err := c.Send(context.Background(), notify.AckReminderPayload{UserID: "u1"}); err == nil {
		t.Errorf("expected insert error to propagate")
	}
}
