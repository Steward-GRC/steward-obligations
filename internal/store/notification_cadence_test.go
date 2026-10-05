// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestNotificationCategoryPrefStore exercises migration 0008's
// notification_category_prefs table: absent → found=false, upsert then get,
// upsert replaces, and List returns the set.
func TestNotificationCategoryPrefStore(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewNotificationCategoryPrefStore(pool)
	user := uuid.NewString()

	if _, found, err := s.Get(ctx, user, "compliance"); err != nil || found {
		t.Fatalf("pre-insert: want (_,false,nil), got found=%v err=%v", found, err)
	}
	if err := s.Upsert(ctx, user, "compliance", "daily"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if cad, found, err := s.Get(ctx, user, "compliance"); err != nil || !found || cad != "daily" {
		t.Fatalf("post-insert: want (daily,true,nil), got (%q,%v,%v)", cad, found, err)
	}
	if err := s.Upsert(ctx, user, "compliance", "weekly"); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}
	if cad, _, _ := s.Get(ctx, user, "compliance"); cad != "weekly" {
		t.Errorf("after replace: cadence=%q want weekly", cad)
	}
	_ = s.Upsert(ctx, user, "informational", "off")
	list, err := s.List(ctx, user)
	if err != nil || len(list) != 2 {
		t.Fatalf("List: want 2 rows, got %d (err=%v)", len(list), err)
	}
}

// TestNotificationTypeOverrideStore exercises notification_type_overrides.
func TestNotificationTypeOverrideStore(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewNotificationTypeOverrideStore(pool)
	user := uuid.NewString()

	if _, found, _ := s.Get(ctx, user, "policy-published"); found {
		t.Fatal("pre-insert override should be absent")
	}
	if err := s.Upsert(ctx, user, "policy-published", "daily"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if cad, found, _ := s.Get(ctx, user, "policy-published"); !found || cad != "daily" {
		t.Fatalf("want (daily,true), got (%q,%v)", cad, found)
	}
	if list, err := s.List(ctx, user); err != nil || len(list) != 1 {
		t.Fatalf("List: want 1, got %d (err=%v)", len(list), err)
	}
}

// TestNotificationDigestWindowStore exercises notification_digest_windows,
// including the default (8/1) when no row exists.
func TestNotificationDigestWindowStore(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewNotificationDigestWindowStore(pool)
	user := uuid.NewString()

	w, err := s.Get(ctx, user)
	if err != nil || w.DailyHour != 8 || w.WeeklyDOW != 1 {
		t.Fatalf("default window: want 8/1, got %+v (err=%v)", w, err)
	}
	if err := s.Upsert(ctx, user, store.DigestWindow{DailyHour: 18, WeeklyDOW: 5}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if w, _ := s.Get(ctx, user); w.DailyHour != 18 || w.WeeklyDOW != 5 {
		t.Errorf("after upsert: got %+v want 18/5", w)
	}
}

// TestChannelPrefStoreUsesNewTable proves the channel store now reads/writes
// notification_channel_prefs (migration 0008's cutover).
func TestChannelPrefStoreUsesNewTable(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewNotificationPrefStore(pool)
	user := uuid.NewString()

	// Default (no row) is email/in-app on, push off.
	if p, err := s.Get(ctx, user); err != nil || !p.Email || !p.InApp || p.Push {
		t.Fatalf("default channels: got %+v err=%v", p, err)
	}
	if err := s.Upsert(ctx, store.NotifPref{UserID: user, Email: false, InApp: true, Push: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, _ := s.Get(ctx, user)
	if got.Email || !got.InApp || !got.Push {
		t.Errorf("after upsert: got %+v", got)
	}
	// Confirm it landed in the new table specifically.
	var email bool
	if err := pool.Querier().QueryRow(ctx, `SELECT email FROM notification_channel_prefs WHERE user_id=$1`, user).Scan(&email); err != nil {
		t.Fatalf("read notification_channel_prefs: %v", err)
	}
	if email {
		t.Error("expected email=false persisted in notification_channel_prefs")
	}
}
