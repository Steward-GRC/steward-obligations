// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

func TestNotificationPrefGetDefaults(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewNotificationPrefStore(pool)
	ctx := context.Background()

	userID := "aaaaaaaa-1111-0000-0000-000000000001"
	got, err := s.Get(ctx, userID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != userID {
		t.Errorf("got UserID %q, want %q", got.UserID, userID)
	}
	if !got.Email {
		t.Error("default Email should be true")
	}
	if !got.InApp {
		t.Error("default InApp should be true")
	}
	if got.Push {
		t.Error("default Push should be false")
	}
}

func TestNotificationPrefUpsertAndGet(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewNotificationPrefStore(pool)
	ctx := context.Background()

	userID := "aaaaaaaa-1111-0000-0000-000000000002"
	want := store.NotifPref{UserID: userID, Email: false, InApp: true, Push: true}
	if err := s.Upsert(ctx, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Get(ctx, userID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestNotificationPrefUpsertOverwrites(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewNotificationPrefStore(pool)
	ctx := context.Background()

	userID := "aaaaaaaa-1111-0000-0000-000000000003"

	first := store.NotifPref{UserID: userID, Email: true, InApp: false, Push: false}
	if err := s.Upsert(ctx, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	second := store.NotifPref{UserID: userID, Email: false, InApp: true, Push: true}
	if err := s.Upsert(ctx, second); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := s.Get(ctx, userID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != second {
		t.Errorf("got %+v, want %+v", got, second)
	}
}
