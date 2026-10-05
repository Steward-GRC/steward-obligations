// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestUserFirstSeenSkipsWithinGraceWindow verifies the core new-user-throttle
// contract: the first time a user is observed, they are within the grace
// window (skip=true); once the injected clock advances past the grace
// window, the same user is no longer throttled (skip=false).
func TestUserFirstSeenSkipsWithinGraceWindow(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	userID := "aaaaaaaa-2222-0000-0000-000000000001"

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	s := store.NewUserFirstSeenStore(pool, 24*time.Hour).WithClock(func() time.Time { return now })

	skip, err := s.ShouldSkipForNewUser(ctx, userID)
	if err != nil {
		t.Fatalf("ShouldSkipForNewUser (first observation): %v", err)
	}
	if !skip {
		t.Error("expected skip=true on first observation (within grace window)")
	}

	// Still within the grace window, a few hours later.
	sLater := s.WithClock(func() time.Time { return now.Add(6 * time.Hour) })
	skip, err = sLater.ShouldSkipForNewUser(ctx, userID)
	if err != nil {
		t.Fatalf("ShouldSkipForNewUser (still within grace): %v", err)
	}
	if !skip {
		t.Error("expected skip=true 6h after first observation (still within 24h grace window)")
	}

	// Past the grace window: normal ack-reminder delivery resumes.
	sPast := s.WithClock(func() time.Time { return now.Add(25 * time.Hour) })
	skip, err = sPast.ShouldSkipForNewUser(ctx, userID)
	if err != nil {
		t.Fatalf("ShouldSkipForNewUser (past grace): %v", err)
	}
	if skip {
		t.Error("expected skip=false 25h after first observation (past 24h grace window)")
	}
}

// TestUserFirstSeenPerUserIndependent verifies that the grace window is
// tracked per user, not globally.
func TestUserFirstSeenPerUserIndependent(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	s := store.NewUserFirstSeenStore(pool, time.Hour).WithClock(func() time.Time { return now })

	// user-old was first observed long before "now" (simulated by seeding at
	// an earlier clock reading, then re-checking at "now").
	oldUser := "aaaaaaaa-2222-0000-0000-000000000002"
	sSeed := store.NewUserFirstSeenStore(pool, time.Hour).WithClock(func() time.Time { return now.Add(-2 * time.Hour) })
	if _, err := sSeed.ShouldSkipForNewUser(ctx, oldUser); err != nil {
		t.Fatalf("seed old user: %v", err)
	}
	skip, err := s.ShouldSkipForNewUser(ctx, oldUser)
	if err != nil {
		t.Fatalf("ShouldSkipForNewUser(oldUser): %v", err)
	}
	if skip {
		t.Error("expected oldUser (first seen 2h ago, 1h grace) to no longer be throttled")
	}

	// newUser is observed for the first time right now: must be throttled.
	newUser := "aaaaaaaa-2222-0000-0000-000000000003"
	skip, err = s.ShouldSkipForNewUser(ctx, newUser)
	if err != nil {
		t.Fatalf("ShouldSkipForNewUser(newUser): %v", err)
	}
	if !skip {
		t.Error("expected newUser (first observation) to be throttled")
	}
}
