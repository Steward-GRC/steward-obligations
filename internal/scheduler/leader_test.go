// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/scheduler"
)

// TestLeaderSingleWriter verifies advisory-lock leader election gives
// single-writer semantics: while one replica holds the lock inside WithLock, a
// second replica contending on the same key does not run (ran=false).
func TestLeaderSingleWriter(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	const key int64 = 0x7357_0001 // isolated from LeaderLockKey and other tests

	a := scheduler.NewLeaderWithKey(pool, key)
	b := scheduler.NewLeaderWithKey(pool, key)

	var bRan bool
	ranA, err := a.WithLock(ctx, func(ctx context.Context) error {
		// While A holds the lock, B must fail to acquire it.
		var err error
		bRan, err = b.WithLock(ctx, func(context.Context) error { return nil })
		return err
	})
	if err != nil {
		t.Fatalf("a.WithLock: %v", err)
	}
	if !ranA {
		t.Fatal("expected replica A to acquire leadership")
	}
	if bRan {
		t.Error("expected replica B to be shut out while A held the lock (single-writer violated)")
	}

	// After A released, B can acquire it on a fresh attempt.
	ranB, err := b.WithLock(ctx, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("b.WithLock after release: %v", err)
	}
	if !ranB {
		t.Error("expected replica B to acquire leadership after A released")
	}
}
