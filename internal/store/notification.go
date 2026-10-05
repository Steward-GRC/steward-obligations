// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// Notification represents a single outbound notification message persisted in
// the notifications table. Payload is rendered to JSONB on insert.
type Notification struct {
	ID        string
	UserID    string
	Type      string
	Channel   string
	Status    string
	Payload   map[string]any
	SentAt    *time.Time
	CreatedAt time.Time
}

// NotificationStore persists rows in the notifications table.
type NotificationStore struct{ db *postgres.DB }

// NewNotificationStore returns a NotificationStore on db.
func NewNotificationStore(p *postgres.DB) *NotificationStore {
	return &NotificationStore{db: p}
}

// Insert persists a new notification with status='pending' and returns the
// updated value with server-assigned id, created_at, and status fields filled
// in.
func (s *NotificationStore) Insert(ctx context.Context, n Notification) (Notification, error) {
	payload, err := json.Marshal(n.Payload)
	if err != nil {
		return n, err
	}
	n.Status = "pending"
	err = s.db.Querier().QueryRow(ctx, `
        INSERT INTO notifications (user_id, type, channel, status, payload)
        VALUES ($1,$2,$3,'pending',$4)
        RETURNING id, created_at`,
		n.UserID, n.Type, n.Channel, payload).
		Scan(&n.ID, &n.CreatedAt)
	if err != nil {
		return Notification{}, err
	}
	return n, nil
}

// MarkSent transitions the notification to status='sent' and stamps sent_at=now.
func (s *NotificationStore) MarkSent(ctx context.Context, id string) error {
	_, err := s.db.Querier().Exec(ctx,
		`UPDATE notifications SET status='sent', sent_at=now() WHERE id=$1`, id)
	return err
}

// MarkFailed transitions the notification to status='failed'. It does not
// touch sent_at; the dispatcher records failure reason out-of-band via audit.
func (s *NotificationStore) MarkFailed(ctx context.Context, id string) error {
	_, err := s.db.Querier().Exec(ctx,
		`UPDATE notifications SET status='failed' WHERE id=$1`, id)
	return err
}
