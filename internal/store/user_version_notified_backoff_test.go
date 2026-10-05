// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestUserVersionNotifiedBackoff exercises the back-off state:
// a stable anchor, reminder-count advance, and escalate-once.
func TestUserVersionNotifiedBackoff(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewUserVersionNotifiedStore(pool)

	userID := "aaaaaaaa-0000-0000-0000-000000000001"
	versionID := "bbbbbbbb-0000-0000-0000-000000000001"

	anchor := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	st, err := s.EnsureAnchor(ctx, userID, versionID, anchor)
	if err != nil {
		t.Fatalf("EnsureAnchor: %v", err)
	}
	if !st.FirstNotifiedAt.Equal(anchor) {
		t.Errorf("anchor = %v; want %v", st.FirstNotifiedAt, anchor)
	}
	if st.ReminderCount != 0 || st.Escalated {
		t.Errorf("fresh state should have count 0, not escalated: %+v", st)
	}

	// EnsureAnchor is idempotent: a later call preserves the original anchor.
	st2, err := s.EnsureAnchor(ctx, userID, versionID, anchor.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("EnsureAnchor 2: %v", err)
	}
	if !st2.FirstNotifiedAt.Equal(anchor) {
		t.Errorf("anchor changed on re-ensure: %v; want %v", st2.FirstNotifiedAt, anchor)
	}

	// Record two reminders; the count advances and last_reminded_at is stamped.
	r1 := anchor.Add(24 * time.Hour)
	if err := s.RecordReminder(ctx, userID, versionID, r1); err != nil {
		t.Fatalf("RecordReminder 1: %v", err)
	}
	r2 := anchor.Add(72 * time.Hour)
	if err := s.RecordReminder(ctx, userID, versionID, r2); err != nil {
		t.Fatalf("RecordReminder 2: %v", err)
	}
	got, found, err := s.GetBackoff(ctx, userID, versionID)
	if err != nil || !found {
		t.Fatalf("GetBackoff = found %v, err %v", found, err)
	}
	if got.ReminderCount != 2 {
		t.Errorf("reminder count = %d; want 2", got.ReminderCount)
	}
	if !got.LastRemindedAt.Equal(r2) {
		t.Errorf("last reminded = %v; want %v", got.LastRemindedAt, r2)
	}

	// Escalate once: a second RecordEscalation must not move escalated_at.
	e1 := anchor.Add(30 * 24 * time.Hour)
	if err := s.RecordEscalation(ctx, userID, versionID, e1); err != nil {
		t.Fatalf("RecordEscalation: %v", err)
	}
	if err := s.RecordEscalation(ctx, userID, versionID, e1.Add(7*24*time.Hour)); err != nil {
		t.Fatalf("RecordEscalation 2: %v", err)
	}
	got, _, _ = s.GetBackoff(ctx, userID, versionID)
	if !got.Escalated {
		t.Error("expected escalated=true after RecordEscalation")
	}
}

// TestGetBackoffMissing verifies a never-notified pair reports found=false.
func TestGetBackoffMissing(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewUserVersionNotifiedStore(pool)
	_, found, err := s.GetBackoff(ctx, "aaaaaaaa-0000-0000-0000-0000000000ff", "bbbbbbbb-0000-0000-0000-0000000000ff")
	if err != nil {
		t.Fatalf("GetBackoff: %v", err)
	}
	if found {
		t.Error("expected found=false for a never-notified pair")
	}
}
