// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// UserFirstSeenStore tracks, per user, the first moment the obligations service's
// ack-reminder path observed them as obligated. It backs the "new-user
// throttle" anti-spam rule (see consumer.NewUserGate): a brand-new account
// must never be blasted with its entire standing ack backlog in one email
// the moment its obligations are first materialized. The backlog remains
// visible in the portal in the meantime, and the very first normal reminder
// cycle after the grace window elapses delivers it by email like any other
// outstanding obligation.
type UserFirstSeenStore struct {
	db    *postgres.DB
	grace time.Duration
	nowFn func() time.Time
}

// NewUserFirstSeenStore returns a UserFirstSeenStore on db, throttling ack-reminder emails for `grace` after a user is first
// observed.
func NewUserFirstSeenStore(p *postgres.DB, grace time.Duration) *UserFirstSeenStore {
	return &UserFirstSeenStore{db: p, grace: grace, nowFn: time.Now}
}

// WithClock returns a copy of the store with an injected clock; tests use
// this to drive the grace-window boundary deterministically instead of
// sleeping past it in real time. The original is unchanged.
func (s *UserFirstSeenStore) WithClock(nowFn func() time.Time) *UserFirstSeenStore {
	s2 := *s
	s2.nowFn = nowFn
	return &s2
}

// ShouldSkipForNewUser records the first time userID is observed (a no-op
// against the stored first_seen_at if already recorded) and reports whether
// userID is still inside the new-user grace window measured from that
// first-seen moment. true means the caller must skip this cycle's
// ack-reminder for userID.
func (s *UserFirstSeenStore) ShouldSkipForNewUser(ctx context.Context, userID string) (bool, error) {
	now := s.nowFn()

	var firstSeenAt time.Time
	err := s.db.Querier().QueryRow(ctx, `
        INSERT INTO user_notify_first_seen (user_id, first_seen_at)
        VALUES ($1, $2)
        ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
        RETURNING first_seen_at`, userID, now).Scan(&firstSeenAt)
	if err != nil {
		return false, err
	}
	return firstSeenAt.Add(s.grace).After(now), nil
}
