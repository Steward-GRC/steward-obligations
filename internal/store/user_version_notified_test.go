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
