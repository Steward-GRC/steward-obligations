// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// QuietWindow is a person's quiet-hours window: [Start, End) in local hours
// (0 to 23) of the IANA zone Timezone. It crosses midnight when Start > End.
type QuietWindow struct {
	Start    int
	End      int
	Timezone string
}

// NotificationQuietHoursStore persists the per-user window in the
// notification_quiet_hours table. A missing row means "no window chosen" and is
// reported as found=false, never an error.
type NotificationQuietHoursStore struct{ db *postgres.DB }

// NewNotificationQuietHoursStore returns a store on db.
func NewNotificationQuietHoursStore(p *postgres.DB) *NotificationQuietHoursStore {
	return &NotificationQuietHoursStore{db: p}
}

// Get returns the user's window; found is false when none is stored.
func (s *NotificationQuietHoursStore) Get(ctx context.Context, userID string) (w QuietWindow, found bool, err error) {
	err = s.db.Querier().QueryRow(ctx,
		`SELECT start_hour, end_hour, timezone FROM notification_quiet_hours WHERE user_id=$1`, userID).
		Scan(&w.Start, &w.End, &w.Timezone)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return QuietWindow{}, false, nil
		}
		return QuietWindow{}, false, err
	}
	return w, true, nil
}

// Set stores the window, replacing any earlier one.
func (s *NotificationQuietHoursStore) Set(ctx context.Context, userID string, w QuietWindow) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_quiet_hours (user_id, start_hour, end_hour, timezone)
        VALUES ($1,$2,$3,$4)
        ON CONFLICT (user_id) DO UPDATE SET
            start_hour = EXCLUDED.start_hour,
            end_hour   = EXCLUDED.end_hour,
            timezone   = EXCLUDED.timezone,
            updated_at = now()`,
		userID, w.Start, w.End, w.Timezone)
	return err
}

// Clear removes the user's window; clearing none is not an error.
func (s *NotificationQuietHoursStore) Clear(ctx context.Context, userID string) error {
	_, err := s.db.Querier().Exec(ctx, `DELETE FROM notification_quiet_hours WHERE user_id=$1`, userID)
	return err
}
