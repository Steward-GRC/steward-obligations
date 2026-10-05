// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// NotifPref captures a user's per-channel notification preferences. The
// defaults applied when no row exists (email=true, in_app=true, push=false)
// match the column defaults in the notification_channel_prefs table.
type NotifPref struct {
	UserID string
	Email  bool
	InApp  bool
	Push   bool
}

// NotificationPrefStore persists per-user channel master switches in the
// notification_channel_prefs table. moved the
// channel booleans out of the legacy notification_prefs table into
// notification_channel_prefs (migration 0008 copies existing rows 1:1); the
// richer per-category cadence model lives in the sibling stores in this
// package.
type NotificationPrefStore struct{ db *postgres.DB }

// NewNotificationPrefStore returns a NotificationPrefStore backed by the
// supplied database.
func NewNotificationPrefStore(p *postgres.DB) *NotificationPrefStore {
	return &NotificationPrefStore{db: p}
}

// Upsert inserts or replaces the preferences row for the given user.
func (s *NotificationPrefStore) Upsert(ctx context.Context, p NotifPref) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_channel_prefs (user_id, email, in_app, push)
        VALUES ($1,$2,$3,$4)
        ON CONFLICT (user_id) DO UPDATE SET
            email  = EXCLUDED.email,
            in_app = EXCLUDED.in_app,
            push   = EXCLUDED.push`,
		p.UserID, p.Email, p.InApp, p.Push)
	return err
}

// Get returns the user's notification preferences, falling back to the system
// defaults (email=true, in_app=true, push=false) if no row exists for the
// user. Callers therefore never need to special-case pgx.ErrNoRows.
func (s *NotificationPrefStore) Get(ctx context.Context, userID string) (NotifPref, error) {
	p := NotifPref{UserID: userID, Email: true, InApp: true, Push: false}
	err := s.db.Querier().QueryRow(ctx,
		`SELECT email, in_app, push FROM notification_channel_prefs WHERE user_id=$1`, userID).
		Scan(&p.Email, &p.InApp, &p.Push)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, nil
		}
		return NotifPref{UserID: userID}, err
	}
	return p, nil
}
