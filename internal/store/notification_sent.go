// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// NotificationSentStore is the durable, cross-replica email.Deduper
// : it records, in the shared notification_sent table,
// that a (userID, kind, dedupRef) key has been sent so a digest/reminder isn't
// double-sent when the scheduler leader runs in one replica and the
// gRPC/consumer paths run in others. It is a drop-in for the process-local
// mail.TTLDeduper via mail.WithDeduper — its Seen/Mark method set satisfies
// go-email's Deduper interface structurally, so package store carries no
// dependency on go-email.
//
// Seen is WINDOWED, exactly like the TTLDeduper it replaces: a key sent longer
// ago than window is treated as unseen, so a legitimate follow-up (e.g. next
// week's reminder for the same policy) still sends after the window elapses,
// while a redelivery/duplicate within the window is suppressed. AcquireOnce is
// the complementary permanent guard used by the digest drain, where the drain
// unit's date is baked into the key so the row never needs to expire.
type NotificationSentStore struct {
	db     *postgres.DB
	window time.Duration
	nowFn  func() time.Time
}

// NewNotificationSentStore returns a store whose Seen honors the given
// idempotency window (mirroring mail.DefaultDedupWindow when the caller passes
// that value).
func NewNotificationSentStore(p *postgres.DB, window time.Duration) *NotificationSentStore {
	return &NotificationSentStore{db: p, window: window, nowFn: time.Now}
}

// WithClock returns a copy with an injected clock so tests can cross the window
// boundary deterministically without sleeping. The original is unchanged.
func (s *NotificationSentStore) WithClock(nowFn func() time.Time) *NotificationSentStore {
	s2 := *s
	s2.nowFn = nowFn
	return &s2
}

// Seen reports whether key was marked sent within the store's window. A key
// whose most recent mark is older than the window (or absent) reads as unseen.
// Implements go-email's Deduper.
func (s *NotificationSentStore) Seen(ctx context.Context, key string) (bool, error) {
	cutoff := s.nowFn().Add(-s.window)
	var exists bool
	err := s.db.Querier().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM notification_sent WHERE dedup_key=$1 AND sent_at > $2)`,
		key, cutoff).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// Mark records key as sent now, refreshing sent_at on a repeat so the window is
// measured from the latest send. Implements go-email's Deduper.
func (s *NotificationSentStore) Mark(ctx context.Context, key string) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_sent (dedup_key, sent_at)
        VALUES ($1, $2)
        ON CONFLICT (dedup_key) DO UPDATE SET sent_at = EXCLUDED.sent_at`,
		key, s.nowFn())
	return err
}

// AcquireOnce atomically claims a one-shot guard key, returning true only for
// the call that first inserts it. The digest drain uses it (with a date-scoped
// key like "digest:ack:<user>:2026-08-25") so at most one pending-ack / general
// digest is sent per user per window even as the ~15-min ticker re-fires while
// the local hour still matches. Unlike Seen it is not windowed — the date in the
// key makes each window a distinct row.
func (s *NotificationSentStore) AcquireOnce(ctx context.Context, key string) (bool, error) {
	tag, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_sent (dedup_key, sent_at)
        VALUES ($1, $2)
        ON CONFLICT (dedup_key) DO NOTHING`, key, s.nowFn())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteExpired removes windowed rows older than the retention horizon so the
// table does not grow without bound. Date-keyed AcquireOnce guards are also
// pruned once older than the horizon (a stale date guard can never re-fire, so
// removing it is safe). Best-effort hygiene; not on any hot path.
func (s *NotificationSentStore) DeleteExpired(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := s.nowFn().Add(-olderThan)
	tag, err := s.db.Querier().Exec(ctx, `DELETE FROM notification_sent WHERE sent_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
