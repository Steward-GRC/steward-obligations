// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

func TestQuietHoursStoreNoRowIsNotFound(t *testing.T) {
	s := store.NewNotificationQuietHoursStore(newTestDB(t))
	_, found, err := s.Get(context.Background(), "qh-user-none")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("a user with no stored window must report found=false")
	}
}

func TestQuietHoursStoreSetGetAndReplace(t *testing.T) {
	s := store.NewNotificationQuietHoursStore(newTestDB(t))
	ctx := context.Background()

	want := store.QuietWindow{Start: 22, End: 7, Timezone: "America/New_York"}
	if err := s.Set(ctx, "qh-user-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, found, err := s.Get(ctx, "qh-user-1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// A second Set replaces the first rather than adding a row.
	next := store.QuietWindow{Start: 9, End: 17, Timezone: "UTC"}
	if err := s.Set(ctx, "qh-user-1", next); err != nil {
		t.Fatalf("Set replace: %v", err)
	}
	got, _, _ = s.Get(ctx, "qh-user-1")
	if got != next {
		t.Fatalf("got %+v, want %+v", got, next)
	}

	// Another user is untouched.
	if _, found, _ := s.Get(ctx, "qh-user-2"); found {
		t.Fatal("a window must belong to one user only")
	}
}

func TestQuietHoursStoreClear(t *testing.T) {
	s := store.NewNotificationQuietHoursStore(newTestDB(t))
	ctx := context.Background()

	if err := s.Set(ctx, "qh-user-3", store.QuietWindow{Start: 23, End: 6, Timezone: "UTC"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Clear(ctx, "qh-user-3"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, found, _ := s.Get(ctx, "qh-user-3"); found {
		t.Fatal("Clear must remove the window")
	}
	// Clearing a user with no window is not an error.
	if err := s.Clear(ctx, "qh-user-none"); err != nil {
		t.Fatalf("Clear of nothing: %v", err)
	}
}

func TestQuietHoursTableRefusesBadRows(t *testing.T) {
	s := store.NewNotificationQuietHoursStore(newTestDB(t))
	ctx := context.Background()
	for _, w := range []store.QuietWindow{
		{Start: 24, End: 7, Timezone: "UTC"},
		{Start: 22, End: -1, Timezone: "UTC"},
		{Start: 5, End: 5, Timezone: "UTC"},
	} {
		if err := s.Set(ctx, "qh-user-bad", w); err == nil {
			t.Errorf("Set(%+v) must be refused by the table", w)
		}
	}
}
