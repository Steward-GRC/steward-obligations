// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

func TestPolicyViewStore_RecordAndAggregate(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewPolicyViewStore(pool)
	ctx := context.Background()

	const ver = "11111111-1111-1111-1111-111111111111"
	uA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	uB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	// uA views twice, uB once.
	for _, u := range []string{uA, uA, uB} {
		if err := s.RecordView(ctx, u, ver); err != nil {
			t.Fatalf("RecordView: %v", err)
		}
	}

	viewers, err := s.DistinctViewersForVersion(ctx, ver)
	if err != nil {
		t.Fatalf("DistinctViewersForVersion: %v", err)
	}
	if len(viewers) != 2 {
		t.Fatalf("distinct viewers: got %d want 2 (%v)", len(viewers), viewers)
	}

	// Daily distinct, audience = {uA}. uA viewed today (twice) -> 1 distinct.
	since := time.Now().AddDate(0, 0, -7)
	daily, err := s.DailyDistinctViewers(ctx, ver, since, []string{uA})
	if err != nil {
		t.Fatalf("DailyDistinctViewers: %v", err)
	}
	total := 0
	for _, c := range daily {
		total += c
	}
	if total != 1 {
		t.Fatalf("daily distinct (audience uA): got %d want 1 (%v)", total, daily)
	}
}
