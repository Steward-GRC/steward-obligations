// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// MailOutboxRow is one durable, held email in the mail_outbox table: the
// fully-rendered message plus replay bookkeeping. It is the store's own DTO so
// package store carries no dependency on internal/mail; cmd/server adapts
// between this and mail.OutboxItem.
type MailOutboxRow struct {
	ID        string
	Kind      string
	Recipient string
	From      string
	Subject   string
	HTML      string
	Text      string
	UserID    string
	DedupKey  string // "" ⇒ stored NULL (dedup disabled for the row)
	Attempts  int
	LastError string
}

// MailOutboxStore persists and drains the mail_outbox hold queue.
type MailOutboxStore struct{ db *postgres.DB }

// NewMailOutboxStore returns a MailOutboxStore on db.
func NewMailOutboxStore(p *postgres.DB) *MailOutboxStore {
	return &MailOutboxStore{db: p}
}

// Enqueue durably holds a rendered message. It is idempotent on dedup_key: a
// second Enqueue for a key that already has a non-failed row is a no-op (the
// partial unique index absorbs it via ON CONFLICT DO NOTHING), so a redelivered
// AMQP message never double-holds. A row with no dedup key is always inserted.
func (s *MailOutboxStore) Enqueue(ctx context.Context, r MailOutboxRow) error {
	var dedup *string
	if r.DedupKey != "" {
		dedup = &r.DedupKey
	}
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO mail_outbox (kind, recipient, from_addr, subject, html, text_body, user_id, dedup_key)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL AND status <> 'failed' DO NOTHING`,
		r.Kind, r.Recipient, r.From, r.Subject, r.HTML, r.Text, r.UserID, dedup)
	return err
}

// ClaimPending returns up to limit pending rows whose next_attempt_at has
// elapsed, oldest first (FIFO replay). Rows are not locked: the drainer is a
// single worker, so plain-read + mark is sufficient (a multi-replica drainer
// would add FOR UPDATE SKIP LOCKED here).
func (s *MailOutboxStore) ClaimPending(ctx context.Context, limit int) ([]MailOutboxRow, error) {
	rows, err := s.db.Querier().Query(ctx, `
        SELECT id, kind, recipient, from_addr, subject, html, text_body, user_id,
               COALESCE(dedup_key, ''), attempts, last_error
          FROM mail_outbox
         WHERE status = 'pending' AND next_attempt_at <= now()
         ORDER BY created_at ASC
         LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MailOutboxRow
	for rows.Next() {
		var r MailOutboxRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Recipient, &r.From, &r.Subject,
			&r.HTML, &r.Text, &r.UserID, &r.DedupKey, &r.Attempts, &r.LastError); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSent flips a row to sent (terminal) and stamps sent_at. A sent row is
// never re-claimed, so a replay delivers each held message exactly once.
func (s *MailOutboxStore) MarkSent(ctx context.Context, id string) error {
	_, err := s.db.Querier().Exec(ctx,
		`UPDATE mail_outbox SET status='sent', sent_at=now(), updated_at=now() WHERE id=$1`, id)
	return err
}

// MarkFailed flips a row to failed (terminal), recording lastErr.
func (s *MailOutboxStore) MarkFailed(ctx context.Context, id, lastErr string) error {
	_, err := s.db.Querier().Exec(ctx,
		`UPDATE mail_outbox SET status='failed', last_error=$2, updated_at=now() WHERE id=$1`, id, lastErr)
	return err
}

// Reschedule keeps a row pending after a transient replay error, bumping
// attempts and holding it until nextAttempt so a blip does not spin.
func (s *MailOutboxStore) Reschedule(ctx context.Context, id, lastErr string, nextAttempt time.Time) error {
	_, err := s.db.Querier().Exec(ctx,
		`UPDATE mail_outbox SET attempts=attempts+1, last_error=$2, next_attempt_at=$3, updated_at=now() WHERE id=$1`,
		id, lastErr, nextAttempt)
	return err
}

// CountByStatus returns how many rows currently hold the given status (for
// diagnostics/metrics and test assertions).
func (s *MailOutboxStore) CountByStatus(ctx context.Context, status string) (int, error) {
	var n int
	err := s.db.Querier().QueryRow(ctx, `SELECT count(*) FROM mail_outbox WHERE status=$1`, status).Scan(&n)
	return n, err
}
