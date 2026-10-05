// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestNotificationOutboxEnqueueDrain exercises the full outbox lifecycle:
// enqueue (idempotent on dedup_ref), list pending with vars round-tripped,
// distinct pending users, and drain (MarkSent).
func TestNotificationOutboxEnqueueDrain(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewNotificationOutboxStore(pool)

	row := store.OutboxRow{
		UserID: "u1", Kind: "policy-published", Category: "informational", Severity: "normal",
		DedupRef: "pv1", WindowKind: "daily", DigestKey: "u1:informational:daily",
		Vars: map[string]any{"title": "New Policy", "actionHref": "https://p/1", "actionLabel": "Read"},
	}
	if err := s.Enqueue(ctx, row); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Idempotent: same (user, kind, dedup_ref) does not double-batch.
	if err := s.Enqueue(ctx, row); err != nil {
		t.Fatalf("Enqueue (dup): %v", err)
	}

	// A second, distinct item for the same user.
	if err := s.Enqueue(ctx, store.OutboxRow{
		UserID: "u1", Kind: "workflow-awaiting-approval", Category: "workflow", DedupRef: "wf1",
		WindowKind: "daily", DigestKey: "u1:workflow:daily", Vars: map[string]any{"title": "Approve"},
	}); err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}

	n, err := s.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if n != 2 {
		t.Fatalf("pending = %d; want 2 (dedup dropped the redelivery)", n)
	}

	users, err := s.PendingUsers(ctx)
	if err != nil {
		t.Fatalf("PendingUsers: %v", err)
	}
	if len(users) != 1 || users[0] != "u1" {
		t.Errorf("PendingUsers = %v; want [u1]", users)
	}

	rows, err := s.ListPending(ctx, "u1")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListPending = %d rows; want 2", len(rows))
	}
	if rows[0].Vars["title"] != "New Policy" {
		t.Errorf("vars not round-tripped: %+v", rows[0].Vars)
	}

	if err := s.MarkSent(ctx, []string{rows[0].ID, rows[1].ID}); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	n, _ = s.CountPending(ctx)
	if n != 0 {
		t.Errorf("pending after drain = %d; want 0", n)
	}
}
