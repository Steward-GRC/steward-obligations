// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestUserVersionNotifiedFirstThenRepeat verifies the first-vs-repeat
// contract: the first MarkNotifiedIfFirst for a (user, version) reports true
// (first notification), every subsequent call for the same pair reports false
// (repeat), and a different version for the same user is independently "first".
func TestUserVersionNotifiedFirstThenRepeat(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewUserVersionNotifiedStore(pool)

	const (
		user = "aaaaaaaa-3333-0000-0000-000000000001"
		verA = "bbbbbbbb-0000-0000-0000-0000000000aa"
		verB = "bbbbbbbb-0000-0000-0000-0000000000bb"
	)

	first, err := s.MarkNotifiedIfFirst(ctx, user, verA)
	if err != nil {
		t.Fatalf("MarkNotifiedIfFirst (first): %v", err)
	}
	if !first {
		t.Error("expected first=true on first notification for (user, verA)")
	}

	repeat, err := s.MarkNotifiedIfFirst(ctx, user, verA)
	if err != nil {
		t.Fatalf("MarkNotifiedIfFirst (repeat): %v", err)
	}
	if repeat {
		t.Error("expected first=false on second notification for (user, verA)")
	}

	// A different version for the same user is independently first.
	firstB, err := s.MarkNotifiedIfFirst(ctx, user, verB)
	if err != nil {
		t.Fatalf("MarkNotifiedIfFirst (verB): %v", err)
	}
	if !firstB {
		t.Error("expected first=true for a new version (user, verB)")
	}
}

// Pending ack state is a reminder anchor with no acknowledgement. Releasing it
// ends the reminders and never removes an acknowledgement or the anchor of
// someone who has acknowledged.
func TestUserVersionNotifiedPending(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	n := store.NewUserVersionNotifiedStore(pool)
	acks := store.NewAcknowledgmentStore(pool)

	const (
		pending = "aaaaaaaa-4444-0000-0000-000000000001"
		other   = "aaaaaaaa-4444-0000-0000-000000000002"
		acked   = "aaaaaaaa-4444-0000-0000-000000000003"
		verA    = "bbbbbbbb-4444-0000-0000-0000000000aa"
		verB    = "bbbbbbbb-4444-0000-0000-0000000000bb"
	)
	for _, u := range []string{pending, other, acked} {
		if _, err := n.MarkNotifiedIfFirst(ctx, u, verA); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.MarkNotifiedIfFirst(ctx, pending, verB); err != nil {
		t.Fatal(err)
	}
	if _, err := acks.RecordAck(ctx, acked, verA); err != nil {
		t.Fatalf("insert ack: %v", err)
	}

	got, err := n.PendingUserIDsForVersion(ctx, verA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("pending on verA: want the 2 who have not acked, got %v", got)
	}
	vs, err := n.VersionsPendingForUser(ctx, pending)
	if err != nil || len(vs) != 2 {
		t.Fatalf("versions pending for user: want 2, got %v (%v)", vs, err)
	}
	if vs, _ := n.VersionsPendingForUser(ctx, acked); len(vs) != 0 {
		t.Fatalf("an acknowledged version is not pending, got %v", vs)
	}

	// Asking to release the acknowledged user too must leave them alone.
	released, err := n.DeletePending(ctx, verA, []string{pending, acked})
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("want 1 released (the unacknowledged one), got %d", released)
	}
	if ok, _ := acks.HasAcked(ctx, acked, verA); !ok {
		t.Fatal("the acknowledgement must be kept")
	}
	if _, found, _ := n.GetBackoff(ctx, acked, verA); !found {
		t.Fatal("the acknowledged user's anchor must be kept")
	}
	if _, found, _ := n.GetBackoff(ctx, pending, verA); found {
		t.Fatal("the released user's anchor must be gone")
	}
	if _, found, _ := n.GetBackoff(ctx, pending, verB); !found {
		t.Fatal("another version's anchor must be kept")
	}
	if n0, err := n.DeletePending(ctx, verA, nil); err != nil || n0 != 0 {
		t.Fatalf("empty release: %d, %v", n0, err)
	}
}
