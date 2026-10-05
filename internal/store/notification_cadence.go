// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// This file holds the per-user cadence-preference stores added by
// : category cadences, advanced per-type overrides,
// and the digest window. Each is pure persistence over its own table; cadence
// and category values are stored as their lowercase text form (see
// internal/notifpolicy for the typed representation). A missing row means "no
// explicit choice" and is reported as found=false, never an error -- matching
// the opt-out semantics of the channel store.

// CategoryCadence is one (category, cadence) preference row.
type CategoryCadence struct {
	Category string
	Cadence  string
}

// NotificationCategoryPrefStore persists per-user, per-category cadence choices
// in the notification_category_prefs table.
type NotificationCategoryPrefStore struct{ db *postgres.DB }

// NewNotificationCategoryPrefStore returns a store on db.
func NewNotificationCategoryPrefStore(p *postgres.DB) *NotificationCategoryPrefStore {
	return &NotificationCategoryPrefStore{db: p}
}

// Get returns the user's cadence for a single category; found is false when no
// row exists.
func (s *NotificationCategoryPrefStore) Get(ctx context.Context, userID, category string) (cadence string, found bool, err error) {
	err = s.db.Querier().QueryRow(ctx,
		`SELECT cadence FROM notification_category_prefs WHERE user_id=$1 AND category=$2`,
		userID, category).Scan(&cadence)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return cadence, true, nil
}

// List returns all category cadences the user has explicitly set.
func (s *NotificationCategoryPrefStore) List(ctx context.Context, userID string) ([]CategoryCadence, error) {
	rows, err := s.db.Querier().Query(ctx,
		`SELECT category, cadence FROM notification_category_prefs WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryCadence
	for rows.Next() {
		var c CategoryCadence
		if err := rows.Scan(&c.Category, &c.Cadence); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Upsert inserts or replaces the user's cadence for a category.
func (s *NotificationCategoryPrefStore) Upsert(ctx context.Context, userID, category, cadence string) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_category_prefs (user_id, category, cadence)
        VALUES ($1,$2,$3)
        ON CONFLICT (user_id, category) DO UPDATE SET cadence = EXCLUDED.cadence`,
		userID, category, cadence)
	return err
}

// UsersWithCadence returns the distinct user IDs who set the given category to
// one of the supplied cadences. The digest drain uses it (category=compliance,
// cadences=daily/weekly) to find the users whose compliance reminders fold into
// a pending-ack digest, without scanning every user in the estate.
func (s *NotificationCategoryPrefStore) UsersWithCadence(ctx context.Context, category string, cadences []string) ([]string, error) {
	rows, err := s.db.Querier().Query(ctx,
		`SELECT user_id FROM notification_category_prefs WHERE category=$1 AND cadence = ANY($2)`,
		category, cadences)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TypeOverride is one (kind, cadence) advanced override row.
type TypeOverride struct {
	Kind    string
	Cadence string
}

// NotificationTypeOverrideStore persists per-user, per-type cadence overrides in
// the notification_type_overrides table.
type NotificationTypeOverrideStore struct{ db *postgres.DB }

// NewNotificationTypeOverrideStore returns a store on db.
func NewNotificationTypeOverrideStore(p *postgres.DB) *NotificationTypeOverrideStore {
	return &NotificationTypeOverrideStore{db: p}
}

// Get returns the user's override cadence for a kind; found is false when none.
func (s *NotificationTypeOverrideStore) Get(ctx context.Context, userID, kind string) (cadence string, found bool, err error) {
	err = s.db.Querier().QueryRow(ctx,
		`SELECT cadence FROM notification_type_overrides WHERE user_id=$1 AND kind=$2`,
		userID, kind).Scan(&cadence)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return cadence, true, nil
}

// List returns all per-type overrides the user has set.
func (s *NotificationTypeOverrideStore) List(ctx context.Context, userID string) ([]TypeOverride, error) {
	rows, err := s.db.Querier().Query(ctx,
		`SELECT kind, cadence FROM notification_type_overrides WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TypeOverride
	for rows.Next() {
		var o TypeOverride
		if err := rows.Scan(&o.Kind, &o.Cadence); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Upsert inserts or replaces the user's override for a kind.
func (s *NotificationTypeOverrideStore) Upsert(ctx context.Context, userID, kind, cadence string) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_type_overrides (user_id, kind, cadence)
        VALUES ($1,$2,$3)
        ON CONFLICT (user_id, kind) DO UPDATE SET cadence = EXCLUDED.cadence`,
		userID, kind, cadence)
	return err
}

// UsersWithCadence returns the distinct user IDs who set a per-type override for
// one of the supplied kinds to one of the supplied cadences. The digest drain
// unions this with the category query so a user who only overrode
// policy-ack-reminder (not the whole compliance category) to a digest is still a
// pending-ack candidate.
func (s *NotificationTypeOverrideStore) UsersWithCadence(ctx context.Context, kinds, cadences []string) ([]string, error) {
	rows, err := s.db.Querier().Query(ctx,
		`SELECT DISTINCT user_id FROM notification_type_overrides WHERE kind = ANY($1) AND cadence = ANY($2)`,
		kinds, cadences)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DigestWindow is a user's digest scheduling anchor. DailyHour is a local hour
// [0,23]; WeeklyDOW is the ISO day-of-week [1,7], 1=Mon.
type DigestWindow struct {
	DailyHour int
	WeeklyDOW int
}

// defaultDigestWindow matches the notification_digest_windows column defaults
// (daily_hour=8, weekly_dow=1) so an absent row resolves to the same values.
var defaultDigestWindow = DigestWindow{DailyHour: 8, WeeklyDOW: 1}

// NotificationDigestWindowStore persists the per-user digest window in the
// notification_digest_windows table.
type NotificationDigestWindowStore struct{ db *postgres.DB }

// NewNotificationDigestWindowStore returns a store on db.
func NewNotificationDigestWindowStore(p *postgres.DB) *NotificationDigestWindowStore {
	return &NotificationDigestWindowStore{db: p}
}

// Get returns the user's digest window, falling back to the system defaults
// (daily_hour=8, weekly_dow=1) when no row exists.
func (s *NotificationDigestWindowStore) Get(ctx context.Context, userID string) (DigestWindow, error) {
	w := defaultDigestWindow
	err := s.db.Querier().QueryRow(ctx,
		`SELECT daily_hour, weekly_dow FROM notification_digest_windows WHERE user_id=$1`, userID).
		Scan(&w.DailyHour, &w.WeeklyDOW)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return defaultDigestWindow, nil
		}
		return DigestWindow{}, err
	}
	return w, nil
}

// Upsert inserts or replaces the user's digest window.
func (s *NotificationDigestWindowStore) Upsert(ctx context.Context, userID string, w DigestWindow) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO notification_digest_windows (user_id, daily_hour, weekly_dow)
        VALUES ($1,$2,$3)
        ON CONFLICT (user_id) DO UPDATE SET
            daily_hour = EXCLUDED.daily_hour,
            weekly_dow = EXCLUDED.weekly_dow`,
		userID, w.DailyHour, w.WeeklyDOW)
	return err
}
