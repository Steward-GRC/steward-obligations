// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/jackc/pgx/v5"
)

// UserVersionNotifiedStore tracks, per (user, policy version), whether
// the obligations service has already emitted a notification to that user for that
// specific published version. It backs first-vs-repeat send classification in
// the policy.published consumer: the FIRST notification for a requires-ack
// (user, version) is an `ack-required` email; a later notification for the
// same still-un-acked (user, version) is a `policy-ack-reminder`. Unlike
// UserFirstSeenStore (a GLOBAL per-user "have we ever seen this user" marker),
// this marker is scoped to the exact version, which is what distinguishes a
// user's first demand for a version from a repeat reminder.
type UserVersionNotifiedStore struct {
	db *postgres.DB
}

// NewUserVersionNotifiedStore returns a store on db.
func NewUserVersionNotifiedStore(p *postgres.DB) *UserVersionNotifiedStore {
	return &UserVersionNotifiedStore{db: p}
}

// MarkNotifiedIfFirst records that userID has now been notified about
// policyVersionID and reports whether THIS call is the first such notification
// for that pair. It is idempotent: the insert is a no-op on an existing row,
// and only a call that actually created the row returns true. A duplicate
// delivery of the same publish event therefore classifies as a repeat (false)
// rather than re-sending an ack-required demand.
func (s *UserVersionNotifiedStore) MarkNotifiedIfFirst(ctx context.Context, userID, policyVersionID string) (bool, error) {
	tag, err := s.db.Querier().Exec(ctx, `
        INSERT INTO user_version_notified (user_id, policy_version_id)
        VALUES ($1, $2)
        ON CONFLICT (user_id, policy_version_id) DO NOTHING`, userID, policyVersionID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// BackoffState is the reminder-back-off + escalate-once state for one
// (user, policy_version), driving the recurring-compliance schedule. The
// anchor (FirstNotifiedAt, day 0 = the first ack demand) already persists;
// ReminderCount/LastRemindedAt/Escalated advance as the sweep re-evaluates the
// obligation. It is scheduler state, not display state.
type BackoffState struct {
	// FirstNotifiedAt is day 0: the first moment an ack demand was recorded for
	// this (user, version). The back-off schedule is measured from it.
	FirstNotifiedAt time.Time
	// ReminderCount is how many recurring reminders the sweep has already sent
	// (the first ack demand is NOT counted here).
	ReminderCount int
	// LastRemindedAt is the most recent recurring reminder send, zero if none.
	LastRemindedAt time.Time
	// Escalated reports whether the manager escalation has already fired (fires
	// once, then the sweep stops pestering the user directly).
	Escalated bool
}

// EnsureAnchor guarantees a back-off row exists for (userID, versionID),
// stamping FirstNotifiedAt=at on first insert (day 0), and returns the current
// state. The sweep calls it for every outstanding obligation so an obligation
// that was never notified through the publish path (e.g. an on-publish policy,
// or one materialized before this feature) still gets a stable anchor to back
// off from. An existing row's anchor is preserved.
func (s *UserVersionNotifiedStore) EnsureAnchor(ctx context.Context, userID, versionID string, at time.Time) (BackoffState, error) {
	var (
		st   BackoffState
		last *time.Time
		esc  *time.Time
	)
	err := s.db.Querier().QueryRow(ctx, `
        INSERT INTO user_version_notified (user_id, policy_version_id, first_notified_at)
        VALUES ($1, $2, $3)
        ON CONFLICT (user_id, policy_version_id) DO UPDATE SET user_id = EXCLUDED.user_id
        RETURNING first_notified_at, reminder_count, last_reminded_at, escalated_at`,
		userID, versionID, at).Scan(&st.FirstNotifiedAt, &st.ReminderCount, &last, &esc)
	if err != nil {
		return BackoffState{}, err
	}
	if last != nil {
		st.LastRemindedAt = *last
	}
	st.Escalated = esc != nil
	return st, nil
}

// GetBackoff returns the back-off state for (userID, versionID); found is false
// when no row exists.
func (s *UserVersionNotifiedStore) GetBackoff(ctx context.Context, userID, versionID string) (state BackoffState, found bool, err error) {
	var (
		last *time.Time
		esc  *time.Time
	)
	err = s.db.Querier().QueryRow(ctx, `
        SELECT first_notified_at, reminder_count, last_reminded_at, escalated_at
          FROM user_version_notified
         WHERE user_id=$1 AND policy_version_id=$2`, userID, versionID).
		Scan(&state.FirstNotifiedAt, &state.ReminderCount, &last, &esc)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BackoffState{}, false, nil
		}
		return BackoffState{}, false, err
	}
	if last != nil {
		state.LastRemindedAt = *last
	}
	state.Escalated = esc != nil
	return state, true, nil
}

// RecordReminder advances the back-off state after a recurring reminder is sent:
// it bumps reminder_count and stamps last_reminded_at=at, so the next sweep
// computes the following back-off step from the updated count.
func (s *UserVersionNotifiedStore) RecordReminder(ctx context.Context, userID, versionID string, at time.Time) error {
	_, err := s.db.Querier().Exec(ctx, `
        UPDATE user_version_notified
           SET reminder_count = reminder_count + 1, last_reminded_at = $3
         WHERE user_id=$1 AND policy_version_id=$2`, userID, versionID, at)
	return err
}

// RecordEscalation stamps escalated_at=at (idempotently: it is only set once, so
// a re-run never resets it), marking that the manager escalation has fired and
// the sweep should stop pestering the user directly.
func (s *UserVersionNotifiedStore) RecordEscalation(ctx context.Context, userID, versionID string, at time.Time) error {
	_, err := s.db.Querier().Exec(ctx, `
        UPDATE user_version_notified
           SET escalated_at = COALESCE(escalated_at, $3)
         WHERE user_id=$1 AND policy_version_id=$2`, userID, versionID, at)
	return err
}
