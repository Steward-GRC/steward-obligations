// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"sync"
	"time"
)

// DefaultDedupWindow is the idempotency window NewSender applies when
// WithDeduper is not supplied: a repeat send sharing the same (userID, kind,
// dedupRef) key within this window is skipped; after it elapses, the same
// key sends again (e.g. a legitimate follow-up reminder for the same
// policy).
const DefaultDedupWindow = 5 * time.Minute

// TTLDeduper is a process-local, mutex-guarded email.Deduper (see go-email's
// Deduper interface) that forgets a key once window has elapsed, so a key
// sent once is only suppressed for a bounded time rather than forever like
// go-email's own MemDeduper. It does not persist across restarts and does
// not coordinate across replicas -- a multi-replica deployment that needs
// durable, cross-process idempotency should back this with shared storage
// (e.g. Redis or Postgres) instead; that is a follow-up, not required for
// SP-3.
type TTLDeduper struct {
	mu     sync.Mutex
	window time.Duration
	seenAt map[string]time.Time
	now    func() time.Time
}

// NewTTLDeduper returns a ready-to-use TTLDeduper with the given window.
func NewTTLDeduper(window time.Duration) *TTLDeduper {
	return newTTLDeduper(window, time.Now)
}

// NewTTLDeduperWithClock returns a TTLDeduper whose notion of "now" is now
// instead of time.Now, so tests can advance time deterministically without
// sleeping.
func NewTTLDeduperWithClock(window time.Duration, now func() time.Time) *TTLDeduper {
	return newTTLDeduper(window, now)
}

func newTTLDeduper(window time.Duration, now func() time.Time) *TTLDeduper {
	return &TTLDeduper{window: window, seenAt: make(map[string]time.Time), now: now}
}

// Seen implements email.Deduper. A key past its window is treated as unseen
// and forgotten, so it stops occupying memory and a later Mark starts its
// window fresh.
func (d *TTLDeduper) Seen(_ context.Context, key string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	markedAt, ok := d.seenAt[key]
	if !ok {
		return false, nil
	}
	if d.now().Sub(markedAt) > d.window {
		delete(d.seenAt, key)
		return false, nil
	}
	return true, nil
}

// Mark implements email.Deduper.
func (d *TTLDeduper) Mark(_ context.Context, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.seenAt[key] = d.now()
	return nil
}
