// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// OutboxRow is one pending (or drained) roll-up notification in the
// notification_outbox table. Immediate sends never
// create one; a digest-bound event (the resolver's mode=batch decision) is
// written here by the dispatcher instead of sending, and the scheduler drains
// it into the user's digest. Vars carries the DigestItem fields captured at
// event time so the drain renders the row without re-resolving anything.
type OutboxRow struct {
	ID string
	// UserID is the recipient the digest is assembled for.
	UserID string
	// Kind is the render-sidecar template kind of the batched event.
	Kind string
	// Category is the taxonomy category, used to place the row in a digest section.
	Category string
	// Severity drives digest ordering (highest first).
	Severity string
	// DedupRef is the business key (policy version, workflow run…) that makes a
	// redelivered event idempotent via the pending partial-unique index.
	DedupRef string
	// Vars are the DigestItem fields (title, meta, actionHref, actionLabel).
	Vars map[string]any
	// WindowKind is the user's cadence for this category at enqueue time
	// (daily|weekly); the drain fires daily rows when the daily-hour matches and
	// weekly rows when the daily-hour AND the weekday match.
	WindowKind string
	// DigestKey is the drain unit user_id:category:window.
	DigestKey string
	// CreatedAt is the enqueue time (newest-first ordering within a section).
	CreatedAt time.Time
}

// NotificationOutboxStore persists and drains the notification_outbox roll-up
// queue.
type NotificationOutboxStore struct{ db *postgres.DB }

// NewNotificationOutboxStore returns a store on db.
func NewNotificationOutboxStore(p *postgres.DB) *NotificationOutboxStore {
	return &NotificationOutboxStore{db: p}
}

// Enqueue durably records a digest-bound event. It is idempotent on
// (user_id, kind, dedup_ref) for a non-empty dedup_ref: a redelivered publish or
// retire event that already has a pending row is a no-op (the partial unique
// index absorbs it via ON CONFLICT DO NOTHING), so a user never sees the same
// item twice in one digest. A row with no dedup_ref is always inserted.
func (s *NotificationOutboxStore) Enqueue(ctx context.Context, r OutboxRow) error {
	vars := r.Vars
	if vars == nil {
		vars = map[string]any{}
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	_, err = s.db.Querier().Exec(ctx, `
        INSERT INTO notification_outbox
            (user_id, kind, category, severity, dedup_ref, vars, window_kind, digest_key)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
        ON CONFLICT (user_id, kind, dedup_ref)
            WHERE sent_at IS NULL AND dedup_ref <> '' DO NOTHING`,
		r.UserID, r.Kind, r.Category, r.Severity, r.DedupRef, raw, r.WindowKind, r.DigestKey)
	return err
}

// PendingUsers returns the distinct user IDs with at least one pending
// (un-drained) row — the drain's candidate set for the general digest.
func (s *NotificationOutboxStore) PendingUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.Querier().Query(ctx,
		`SELECT DISTINCT user_id FROM notification_outbox WHERE sent_at IS NULL`)
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

// ListPending returns a user's pending rows, oldest first. The drain filters by
// window (daily vs weekly) in memory since the set per user is small.
func (s *NotificationOutboxStore) ListPending(ctx context.Context, userID string) ([]OutboxRow, error) {
	rows, err := s.db.Querier().Query(ctx, `
        SELECT id, user_id, kind, category, severity, dedup_ref, vars, window_kind, digest_key, created_at
          FROM notification_outbox
         WHERE user_id=$1 AND sent_at IS NULL
         ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		var (
			r   OutboxRow
			raw []byte
		)
		if err := rows.Scan(&r.ID, &r.UserID, &r.Kind, &r.Category, &r.Severity,
			&r.DedupRef, &raw, &r.WindowKind, &r.DigestKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &r.Vars); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSent flips the given rows to drained (sent_at=now) so a later drain
// never re-batches them. A drained digest send therefore consumes its rows
// exactly once.
func (s *NotificationOutboxStore) MarkSent(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Querier().Exec(ctx,
		`UPDATE notification_outbox SET sent_at=now() WHERE id = ANY($1) AND sent_at IS NULL`, ids)
	return err
}

// CountPending returns how many rows are currently pending (diagnostics/tests).
func (s *NotificationOutboxStore) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.db.Querier().QueryRow(ctx,
		`SELECT count(*) FROM notification_outbox WHERE sent_at IS NULL`).Scan(&n)
	return n, err
}
