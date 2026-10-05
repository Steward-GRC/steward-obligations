// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// Ack transfer resolutions record how a single policy_version's ack was
// reconciled when transferring a source user's acks onto a target user.
const (
	// ResolutionMoved: only the source had acked; the ack was re-pointed to the
	// target.
	ResolutionMoved = "MOVED"
	// ResolutionKeptEarliest: both users had acked and the source's ack was
	// earlier; the target row is backdated to the source's acked_at and the
	// source row dropped.
	ResolutionKeptEarliest = "KEPT_EARLIEST"
	// ResolutionTargetKept: both users had acked and the target's ack was
	// earlier or equal; the source row is dropped and the target's ack kept.
	ResolutionTargetKept = "TARGET_KEPT"
)

// AckTransferItemRow describes how one policy_version's acknowledgment was (or,
// for a dry-run, would be) reconciled during a transfer. SourceAckedAt is the
// source user's ack time; TargetAckedAt is the target user's pre-transfer ack
// time and is nil when only the source had acked (ResolutionMoved).
type AckTransferItemRow struct {
	PolicyVersionID string
	SourceAckedAt   time.Time
	TargetAckedAt   *time.Time
	Resolution      string
}

// Ack is a persisted acknowledgment row from the acknowledgments table.
type Ack struct {
	ID              string
	UserID          string
	PolicyVersionID string
	AckedAt         time.Time
}

// AcknowledgmentStore persists rows in the acknowledgments table.
type AcknowledgmentStore struct{ db *postgres.DB }

// NewAcknowledgmentStore returns an AcknowledgmentStore on db. Its calls join
// a transaction started with InTx.
func NewAcknowledgmentStore(p *postgres.DB) *AcknowledgmentStore {
	return &AcknowledgmentStore{db: p}
}

// RecordAck records an acknowledgment for (userID, policyVersionID). It is
// idempotent: if a row already exists for that pair, the existing row is
// returned unchanged. Callers may therefore safely retry without producing
// duplicate acks.
func (s *AcknowledgmentStore) RecordAck(ctx context.Context, userID, policyVersionID string) (Ack, error) {
	var a Ack
	err := querier(ctx, s.db).QueryRow(ctx, `
        INSERT INTO acknowledgments (user_id, policy_version_id)
        VALUES ($1,$2)
        ON CONFLICT (user_id, policy_version_id) DO UPDATE
            SET user_id = acknowledgments.user_id
        RETURNING id, user_id, policy_version_id, acked_at`,
		userID, policyVersionID).
		Scan(&a.ID, &a.UserID, &a.PolicyVersionID, &a.AckedAt)
	if err != nil {
		return Ack{}, err
	}
	return a, nil
}

// TransferAcks moves sourceUserID's acknowledgments onto targetUserID with
// dedupe, in a single transaction. It classifies every source ack by joining
// against the target's acks on policy_version_id:
//
//   - source-only -> ResolutionMoved (re-pointed to target)
//   - both, source earlier -> ResolutionKeptEarliest (target backdated, source dropped)
//   - both, target earlier/equal -> ResolutionTargetKept (source dropped, target kept)
//
// The returned counts and items always reflect what THIS call did, captured
// from a pre-mutation SELECT: moved = count(Moved); deduped = count(both-acked
// cases). When dryRun is true nothing is mutated. The mutation is idempotent —
// once the source rows are gone a re-run is a clean no-op (moved=0, deduped=0,
// items empty). It is an error to transfer a user onto itself.
func (s *AcknowledgmentStore) TransferAcks(ctx context.Context, sourceUserID, targetUserID string, dryRun bool) (moved int, deduped int, items []AckTransferItemRow, err error) {
	if sourceUserID == targetUserID {
		return 0, 0, nil, fmt.Errorf("transfer acks: source and target user must differ")
	}

	err = InTx(ctx, s.db, func(ctx context.Context) error {
		moved, deduped, items = 0, 0, nil
		tx, _ := TxFrom(ctx)
		// Classify before mutating: source LEFT JOIN target on policy_version_id.
		rows, err := tx.Query(ctx, `
        SELECT s.policy_version_id, s.acked_at, t.acked_at
        FROM acknowledgments s
        LEFT JOIN acknowledgments t
            ON t.user_id = $2 AND t.policy_version_id = s.policy_version_id
        WHERE s.user_id = $1
        ORDER BY s.policy_version_id`,
			sourceUserID, targetUserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				pvID  string
				srcAt time.Time
				tgtAt *time.Time
				item  AckTransferItemRow
			)
			if err := rows.Scan(&pvID, &srcAt, &tgtAt); err != nil {
				return err
			}
			item.PolicyVersionID = pvID
			item.SourceAckedAt = srcAt
			item.TargetAckedAt = tgtAt
			switch {
			case tgtAt == nil:
				item.Resolution = ResolutionMoved
				moved++
			case srcAt.Before(*tgtAt):
				item.Resolution = ResolutionKeptEarliest
				deduped++
			default:
				item.Resolution = ResolutionTargetKept
				deduped++
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if dryRun {
			return nil
		}

		// Re-home every source ack onto the target, keeping the earliest
		// acked_at on a collision, then drop the source rows.
		if _, err := tx.Exec(ctx, `
        INSERT INTO acknowledgments (id, user_id, policy_version_id, acked_at)
            SELECT gen_random_uuid(), $2, policy_version_id, acked_at
            FROM acknowledgments WHERE user_id = $1
        ON CONFLICT (user_id, policy_version_id) DO UPDATE
            SET acked_at = LEAST(acknowledgments.acked_at, EXCLUDED.acked_at)`,
			sourceUserID, targetUserID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM acknowledgments WHERE user_id = $1`, sourceUserID)
		return err
	})
	if err != nil {
		return 0, 0, nil, err
	}
	return moved, deduped, items, nil
}

// HasAcked reports whether the given user has acknowledged the given policy
// version.
func (s *AcknowledgmentStore) HasAcked(ctx context.Context, userID, policyVersionID string) (bool, error) {
	var exists bool
	err := querier(ctx, s.db).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM acknowledgments WHERE user_id=$1 AND policy_version_id=$2)`,
		userID, policyVersionID).Scan(&exists)
	return exists, err
}

// AckedUserIDsForVersion returns the distinct user IDs that have acknowledged
// the given policyVersionID. Used by the obligation resolver's Completion
// method to compute the live acked count and overdue set.
func (s *AcknowledgmentStore) AckedUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error) {
	rows, err := querier(ctx, s.db).Query(ctx,
		`SELECT DISTINCT user_id FROM acknowledgments WHERE policy_version_id = $1`,
		policyVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	return ids, rows.Err()
}

// VersionsAckedByUser returns the distinct policy version IDs the user has
// acknowledged. Used to reconcile a single user's acks after a membership
// change (they may have left the audience of policies they previously acked).
func (s *AcknowledgmentStore) VersionsAckedByUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := querier(ctx, s.db).Query(ctx,
		`SELECT DISTINCT policy_version_id FROM acknowledgments WHERE user_id = $1`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		ids = append(ids, v)
	}
	return ids, rows.Err()
}

// DeleteAcksForUsers removes ack rows for the given users on policyVersionID and
// returns the number of rows deleted. Used by the obligation resolver to purge
// acks that are orphaned when a user leaves a policy's obligated audience.
func (s *AcknowledgmentStore) DeleteAcksForUsers(ctx context.Context, policyVersionID string, userIDs []string) (int, error) {
	if len(userIDs) == 0 {
		return 0, nil
	}
	tag, err := querier(ctx, s.db).Exec(ctx,
		`DELETE FROM acknowledgments WHERE policy_version_id = $1 AND user_id = ANY($2)`,
		policyVersionID, userIDs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// AckedAtForVersion returns a map of user_id -> acked_at for every ack on the
// given policy version. Used to compute roster acked entries and avg-days-to-ack.
func (s *AcknowledgmentStore) AckedAtForVersion(ctx context.Context, policyVersionID string) (map[string]time.Time, error) {
	rows, err := querier(ctx, s.db).Query(ctx,
		`SELECT user_id, acked_at FROM acknowledgments WHERE policy_version_id = $1`,
		policyVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var uid string
		var at time.Time
		if err := rows.Scan(&uid, &at); err != nil {
			return nil, err
		}
		out[uid] = at
	}
	return out, rows.Err()
}

// DailyAckCounts returns, per UTC day at/after since, the number of acks for the
// version. Key format YYYY-MM-DD.
func (s *AcknowledgmentStore) DailyAckCounts(ctx context.Context, policyVersionID string, since time.Time) (map[string]int, error) {
	rows, err := querier(ctx, s.db).Query(ctx, `
        SELECT to_char(date_trunc('day', acked_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS d, COUNT(*)
        FROM acknowledgments
        WHERE policy_version_id = $1 AND acked_at >= $2
        GROUP BY d`,
		policyVersionID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var d string
		var c int
		if err := rows.Scan(&d, &c); err != nil {
			return nil, err
		}
		out[d] = c
	}
	return out, rows.Err()
}

// Acked reports, for each policy version ID in versionIDs, whether userID has
// already acknowledged it. The returned map has an entry for every supplied
// version ID; absent entries have value false.
func (s *AcknowledgmentStore) Acked(ctx context.Context, userID string, versionIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(versionIDs))
	for _, id := range versionIDs {
		result[id] = false
	}
	if len(versionIDs) == 0 {
		return result, nil
	}
	rows, err := querier(ctx, s.db).Query(ctx,
		`SELECT policy_version_id FROM acknowledgments
         WHERE user_id=$1 AND policy_version_id = ANY($2)`,
		userID, versionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pvID string
		if err := rows.Scan(&pvID); err != nil {
			return nil, err
		}
		result[pvID] = true
	}
	return result, rows.Err()
}
