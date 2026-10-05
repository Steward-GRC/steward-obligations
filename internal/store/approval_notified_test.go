// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestApprovalNotifiedDedup verifies the durable per-(task, approver) dedup
// contract backing the workflow.approval_requested consumer: AlreadyNotified is
// false until MarkNotified records the pair, then true; a different approver on
// the same task and the same approver on a different task are independently
// "not yet notified". This also exercises migration 0006 (the table must exist
// and carry the (task_id, approver_user_id) primary key).
func TestApprovalNotifiedDedup(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewApprovalNotifiedStore(pool)

	const (
		taskA = "bbbbbbbb-0000-0000-0000-0000000000aa:0"
		taskB = "bbbbbbbb-0000-0000-0000-0000000000bb:1"
		usr1  = "aaaaaaaa-3333-0000-0000-000000000001"
		usr2  = "aaaaaaaa-3333-0000-0000-000000000002"
	)

	if done, err := s.AlreadyNotified(ctx, taskA, usr1); err != nil || done {
		t.Fatalf("pre-mark: want (false,nil), got (%v,%v)", done, err)
	}

	if err := s.MarkNotified(ctx, taskA, usr1); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	if done, err := s.AlreadyNotified(ctx, taskA, usr1); err != nil || !done {
		t.Fatalf("post-mark: want (true,nil), got (%v,%v)", done, err)
	}

	// MarkNotified is idempotent (ON CONFLICT DO NOTHING) — a redelivery that
	// races past AlreadyNotified must not error or duplicate.
	if err := s.MarkNotified(ctx, taskA, usr1); err != nil {
		t.Fatalf("MarkNotified (repeat): %v", err)
	}

	// Independent grains: different approver, and different task, are unaffected.
	if done, _ := s.AlreadyNotified(ctx, taskA, usr2); done {
		t.Error("a different approver on the same task must be independently un-notified")
	}
	if done, _ := s.AlreadyNotified(ctx, taskB, usr1); done {
		t.Error("the same approver on a different task must be independently un-notified")
	}
}
