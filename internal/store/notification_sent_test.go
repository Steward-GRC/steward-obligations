// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestNotificationSentWindowedDedup verifies the durable deduper's core
// contract: a marked key reads as seen within the window and unseen once the
// window elapses, so a redelivery is suppressed but a legitimate later reminder
// for the same key still sends.
func TestNotificationSentWindowedDedup(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	key := "u1:policy-ack-reminder:pv1"

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	s := store.NewNotificationSentStore(pool, 5*time.Minute).WithClock(func() time.Time { return now })

	if seen, err := s.Seen(ctx, key); err != nil || seen {
		t.Fatalf("Seen before Mark = %v, %v; want false, nil", seen, err)
	}
	if err := s.Mark(ctx, key); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if seen, err := s.Seen(ctx, key); err != nil || !seen {
		t.Fatalf("Seen within window = %v, %v; want true, nil", seen, err)
	}

	// Past the window the same key reads unseen so a legitimate follow-up sends.
	sLater := s.WithClock(func() time.Time { return now.Add(6 * time.Minute) })
	if seen, err := sLater.Seen(ctx, key); err != nil || seen {
		t.Fatalf("Seen past window = %v, %v; want false, nil", seen, err)
	}
}

// TestNotificationSentCrossReplica verifies dedup coordinates across processes:
// a key marked by one store instance is seen by an independent instance sharing
// the same table (the replica scenario the durable deduper exists for).
func TestNotificationSentCrossReplica(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	key := "u2:digest:general:2026-08-01"

	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	replicaA := store.NewNotificationSentStore(pool, time.Hour).WithClock(func() time.Time { return now })
	replicaB := store.NewNotificationSentStore(pool, time.Hour).WithClock(func() time.Time { return now })

	if err := replicaA.Mark(ctx, key); err != nil {
		t.Fatalf("replicaA.Mark: %v", err)
	}
	if seen, err := replicaB.Seen(ctx, key); err != nil || !seen {
		t.Fatalf("replicaB.Seen after replicaA.Mark = %v, %v; want true, nil", seen, err)
	}
}

// TestNotificationSentAcquireOnce verifies the digest per-window guard: exactly
// one caller wins the key; every later attempt (a re-fired tick, another
// replica) loses.
func TestNotificationSentAcquireOnce(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	key := "digest:ack:u3:2026-08-01"

	s := store.NewNotificationSentStore(pool, time.Hour)

	first, err := s.AcquireOnce(ctx, key)
	if err != nil || !first {
		t.Fatalf("first AcquireOnce = %v, %v; want true, nil", first, err)
	}
	second, err := s.AcquireOnce(ctx, key)
	if err != nil || second {
		t.Fatalf("second AcquireOnce = %v, %v; want false, nil", second, err)
	}
	// A different replica instance also loses.
	other := store.NewNotificationSentStore(pool, time.Hour)
	again, err := other.AcquireOnce(ctx, key)
	if err != nil || again {
		t.Fatalf("cross-replica AcquireOnce = %v, %v; want false, nil", again, err)
	}
}
