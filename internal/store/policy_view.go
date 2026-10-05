// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"time"

	postgres "github.com/Bugs5382/go-postgres"
)

// PolicyViewStore persists append-only in-app policy view events.
type PolicyViewStore struct{ db *postgres.DB }

// NewPolicyViewStore returns a PolicyViewStore on db.
func NewPolicyViewStore(p *postgres.DB) *PolicyViewStore { return &PolicyViewStore{db: p} }

// RecordView appends a view event for (userID, policyVersionID).
func (s *PolicyViewStore) RecordView(ctx context.Context, userID, policyVersionID string) error {
	_, err := s.db.Querier().Exec(ctx,
		`INSERT INTO policy_views (user_id, policy_version_id) VALUES ($1,$2)`,
		userID, policyVersionID)
	return err
}

// DistinctViewersForVersion returns the distinct user IDs that viewed the version.
func (s *PolicyViewStore) DistinctViewersForVersion(ctx context.Context, policyVersionID string) ([]string, error) {
	rows, err := s.db.Querier().Query(ctx,
		`SELECT DISTINCT user_id FROM policy_views WHERE policy_version_id = $1`,
		policyVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DailyDistinctViewers returns, per UTC day at/after since, the count of distinct
// users among audienceUserIDs who viewed the version. Empty audience -> empty map.
func (s *PolicyViewStore) DailyDistinctViewers(ctx context.Context, policyVersionID string, since time.Time, audienceUserIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(audienceUserIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Querier().Query(ctx, `
        SELECT to_char(date_trunc('day', viewed_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS d,
               COUNT(DISTINCT user_id)
        FROM policy_views
        WHERE policy_version_id = $1 AND viewed_at >= $2 AND user_id = ANY($3)
        GROUP BY d`,
		policyVersionID, since, audienceUserIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
