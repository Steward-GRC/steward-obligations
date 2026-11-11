// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	postgres "github.com/Bugs5382/go-postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Steward-GRC/steward-obligations/internal/inbox"
	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// InboxStore reads and marks the in-app notices in the notifications table,
// the rows with channel 'in_app'. Every query is scoped to one user. A user or
// notice id that is not a UUID can match no row, so it answers empty rather
// than failing the query.
type InboxStore struct{ db *postgres.DB }

// NewInboxStore returns an InboxStore on db.
func NewInboxStore(p *postgres.DB) *InboxStore { return &InboxStore{db: p} }

var _ inbox.Store = (*InboxStore)(nil)

const inboxColumns = `id, type, payload, created_at, read_at`

func scanInboxRow(row pgx.Row) (inbox.Row, error) {
	var (
		r       inbox.Row
		payload []byte
	)
	if err := row.Scan(&r.ID, &r.Type, &payload, &r.CreatedAt, &r.ReadAt); err != nil {
		return inbox.Row{}, err
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &r.Payload); err != nil {
			return inbox.Row{}, fmt.Errorf("decode notice payload: %w", err)
		}
	}
	return r, nil
}

// List returns the user's in-app notices newest first.
func (s *InboxStore) List(ctx context.Context, q inbox.Query) ([]inbox.Row, error) {
	if _, err := uuid.Parse(q.UserID); err != nil {
		return nil, nil
	}
	args := []any{q.UserID}
	sql := `SELECT ` + inboxColumns + ` FROM notifications WHERE user_id = $1 AND channel = 'in_app'`
	if len(q.Types) > 0 {
		args = append(args, q.Types)
		sql += fmt.Sprintf(` AND type = ANY($%d)`, len(args))
	}
	if len(q.ExcludeTypes) > 0 {
		args = append(args, q.ExcludeTypes)
		sql += fmt.Sprintf(` AND NOT (type = ANY($%d))`, len(args))
	}
	if q.After != nil {
		if _, err := uuid.Parse(q.After.ID); err != nil {
			return nil, inbox.ErrInvalidToken
		}
		args = append(args, q.After.CreatedAt, q.After.ID)
		sql += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	args = append(args, q.Limit)
	sql += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))

	rows, err := s.db.Querier().Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	defer rows.Close()
	var out []inbox.Row
	for rows.Next() {
		r, err := scanInboxRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan inbox: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list inbox: %w", err)
	}
	lg := logctx.From(ctx)
	lg.Trace().Str("user_id", q.UserID).Int("rows", len(out)).Msg("inbox store list")
	return out, nil
}

// CountUnread counts the user's unread in-app notices.
func (s *InboxStore) CountUnread(ctx context.Context, userID string) (int64, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return 0, nil
	}
	var n int64
	err := s.db.Querier().QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND channel = 'in_app' AND read_at IS NULL`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}

// MarkRead stamps read_at on the user's notice when it is still unset, and
// returns the notice. It reports false when the user has no such in-app notice.
func (s *InboxStore) MarkRead(ctx context.Context, userID, id string) (inbox.Row, bool, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return inbox.Row{}, false, nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return inbox.Row{}, false, nil
	}
	r, err := scanInboxRow(s.db.Querier().QueryRow(ctx, `
        UPDATE notifications SET read_at = COALESCE(read_at, now())
        WHERE id = $1 AND user_id = $2 AND channel = 'in_app'
        RETURNING `+inboxColumns, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return inbox.Row{}, false, nil
	}
	if err != nil {
		return inbox.Row{}, false, fmt.Errorf("mark read: %w", err)
	}
	return r, true, nil
}

// MarkAllRead stamps read_at on every unread in-app notice of the user and
// returns how many it marked.
func (s *InboxStore) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return 0, nil
	}
	tag, err := s.db.Querier().Exec(ctx,
		`UPDATE notifications SET read_at = now() WHERE user_id = $1 AND channel = 'in_app' AND read_at IS NULL`, userID)
	if err != nil {
		return 0, fmt.Errorf("mark all read: %w", err)
	}
	return tag.RowsAffected(), nil
}
