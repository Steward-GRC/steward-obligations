// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

func TestTTLDeduperSeenAfterMark(t *testing.T) {
	d := mail.NewTTLDeduper(time.Minute)
	ctx := context.Background()

	seen, err := d.Seen(ctx, "k1")
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if seen {
		t.Fatal("expected an unmarked key to be unseen")
	}

	if err := d.Mark(ctx, "k1"); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	seen, err = d.Seen(ctx, "k1")
	if err != nil {
		t.Fatalf("Seen after Mark: %v", err)
	}
	if !seen {
		t.Fatal("expected a marked key to be seen")
	}
}

func TestTTLDeduperExpiresAfterWindow(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	d := mail.NewTTLDeduperWithClock(time.Minute, func() time.Time { return clock() })
	ctx := context.Background()

	if err := d.Mark(ctx, "k1"); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	// Still within the window.
	now = now.Add(30 * time.Second)
	if seen, err := d.Seen(ctx, "k1"); err != nil || !seen {
		t.Fatalf("Seen within window: got seen=%v err=%v, want true, nil", seen, err)
	}

	// Past the window: the key should be forgotten.
	now = now.Add(31 * time.Second)
	if seen, err := d.Seen(ctx, "k1"); err != nil || seen {
		t.Fatalf("Seen past window: got seen=%v err=%v, want false, nil", seen, err)
	}
}

func TestTTLDeduperDistinctKeysIndependent(t *testing.T) {
	d := mail.NewTTLDeduper(time.Minute)
	ctx := context.Background()

	if err := d.Mark(ctx, "k1"); err != nil {
		t.Fatalf("Mark k1: %v", err)
	}

	if seen, err := d.Seen(ctx, "k2"); err != nil || seen {
		t.Fatalf("Seen k2: got seen=%v err=%v, want false, nil", seen, err)
	}
}
