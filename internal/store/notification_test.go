// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

func TestNotificationInsertDefaultsToPending(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewNotificationStore(pool)
	ctx := context.Background()

	in := store.Notification{
		UserID:  "bbbbbbbb-2222-0000-0000-000000000001",
		Type:    "ack_required",
		Channel: "email",
		Payload: map[string]any{"policy_id": "p-1", "title": "Acceptable Use"},
	}
	got, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got.ID == "" {
		t.Error("expected non-empty ID")
	}
	if got.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestNotificationMarkSent(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewNotificationStore(pool)
	ctx := context.Background()

	in := store.Notification{
		UserID:  "bbbbbbbb-2222-0000-0000-000000000002",
		Type:    "ack_reminder",
		Channel: "in_app",
		Payload: map[string]any{"k": "v"},
	}
	got, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := s.MarkSent(ctx, got.ID); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	var status string
	var sentAt *string
	if err := pool.Querier().QueryRow(ctx, `SELECT status, sent_at::text FROM notifications WHERE id=$1`, got.ID).
		Scan(&status, &sentAt); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "sent" {
		t.Errorf("got status %q, want %q", status, "sent")
	}
	if sentAt == nil {
		t.Error("expected sent_at to be non-null after MarkSent")
	}
}

func TestNotificationMarkFailed(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewNotificationStore(pool)
	ctx := context.Background()

	in := store.Notification{
		UserID:  "bbbbbbbb-2222-0000-0000-000000000003",
		Type:    "ack_escalation",
		Channel: "push",
		Payload: map[string]any{"reason": "broken"},
	}
	got, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := s.MarkFailed(ctx, got.ID); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	var status string
	if err := pool.Querier().QueryRow(ctx, `SELECT status FROM notifications WHERE id=$1`, got.ID).
		Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "failed" {
		t.Errorf("got status %q, want %q", status, "failed")
	}
}
