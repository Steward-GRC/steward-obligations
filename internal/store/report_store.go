// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"time"

	postgres "github.com/Bugs5382/go-postgres"

	"github.com/Steward-GRC/steward-obligations/internal/reporting"
)

// AckExportStore implements reporting.AckFetcher backed by Postgres. Rows are
// returned ordered by acked_at then id so audit-quality exports are
// reproducible across calls.
type AckExportStore struct{ db *postgres.DB }

// NewAckExportStore returns an AckExportStore on db.
func NewAckExportStore(p *postgres.DB) *AckExportStore { return &AckExportStore{db: p} }

// FetchAcks returns all acknowledgment rows for a given policy version.
func (s *AckExportStore) FetchAcks(ctx context.Context, policyVersionID string) ([]reporting.AckExportRow, error) {
	rows, err := s.db.Querier().Query(ctx, `
        SELECT id, user_id, policy_version_id, acked_at
        FROM acknowledgments
        WHERE policy_version_id = $1
        ORDER BY acked_at, id`, policyVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []reporting.AckExportRow
	for rows.Next() {
		var r reporting.AckExportRow
		var ackedAt time.Time
		if err := rows.Scan(&r.AckID, &r.UserID, &r.PolicyVersionID, &ackedAt); err != nil {
			return nil, err
		}
		r.AckedAt = ackedAt
		result = append(result, r)
	}
	return result, rows.Err()
}
