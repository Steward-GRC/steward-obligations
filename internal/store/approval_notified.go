// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	postgres "github.com/Bugs5382/go-postgres"
)

// ApprovalNotifiedStore tracks, per (task, approver), whether
// the obligations service has already sent the "workflow-awaiting-approval" email to
// that approver for that task. It backs durable dedup in the
// workflow.approval_requested consumer: the workflow's assign_stage action is
// re-entrant and an idempotent re-submit of the same policy version re-runs it,
// so the same event is emitted repeatedly for a still-pending task. task_id is
// the stable per-(policy version, stage) key "<policy_version_id>:<stage_index>"
// — a genuinely NEW version yields a new task_id and therefore does re-notify.
//
// Unlike UserVersionNotifiedStore's single MarkNotifiedIfFirst, this store
// splits the check (AlreadyNotified) from the record (MarkNotified) so the
// consumer records the send only AFTER the email actually goes out. A failed
// send therefore leaves no marker and is retried on redelivery, rather than
// being permanently suppressed.
type ApprovalNotifiedStore struct {
	db *postgres.DB
}

// NewApprovalNotifiedStore returns a store on db.
func NewApprovalNotifiedStore(p *postgres.DB) *ApprovalNotifiedStore {
	return &ApprovalNotifiedStore{db: p}
}

// AlreadyNotified reports whether the (taskID, approverUserID) approver has
// already been emailed for this task.
func (s *ApprovalNotifiedStore) AlreadyNotified(ctx context.Context, taskID, approverUserID string) (bool, error) {
	var exists bool
	err := s.db.Querier().QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM approval_notified
            WHERE task_id = $1 AND approver_user_id = $2
        )`, taskID, approverUserID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// MarkNotified records that the (taskID, approverUserID) approver has now been
// emailed for this task. It is idempotent: a repeat call is a no-op via the
// primary-key ON CONFLICT, so a redelivery that races past AlreadyNotified
// still leaves exactly one row.
func (s *ApprovalNotifiedStore) MarkNotified(ctx context.Context, taskID, approverUserID string) error {
	_, err := s.db.Querier().Exec(ctx, `
        INSERT INTO approval_notified (task_id, approver_user_id)
        VALUES ($1, $2)
        ON CONFLICT (task_id, approver_user_id) DO NOTHING`, taskID, approverUserID)
	return err
}
