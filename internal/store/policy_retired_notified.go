// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	postgres "github.com/Bugs5382/go-postgres"
)

// PolicyRetiredNotifiedStore tracks, per (policy, user), whether
// the obligations service has already sent the "policy retired" notice to that user
// for that policy. It backs durable dedup in the policy.retired consumer: a
// broker redelivery (or a replica) must not re-email the same audience member
// for the same retirement. policy_id is the stable per-policy key; a genuinely
// different policy produces a new key and therefore does notify.
type PolicyRetiredNotifiedStore struct {
	db *postgres.DB
}

// NewPolicyRetiredNotifiedStore returns a store on db.
func NewPolicyRetiredNotifiedStore(p *postgres.DB) *PolicyRetiredNotifiedStore {
	return &PolicyRetiredNotifiedStore{db: p}
}

// MarkNotifiedIfFirst records that userID has now been notified about policyID's
// retirement and reports whether THIS call is the first such notification for
// that pair. It is idempotent: the insert is a no-op on an existing row, and
// only a call that actually created the row returns true. A duplicate delivery
// of the same retire event therefore reports false and the consumer skips it.
func (s *PolicyRetiredNotifiedStore) MarkNotifiedIfFirst(ctx context.Context, policyID, userID string) (bool, error) {
	tag, err := s.db.Querier().Exec(ctx, `
        INSERT INTO policy_retired_notified (policy_id, user_id)
        VALUES ($1, $2)
        ON CONFLICT (policy_id, user_id) DO NOTHING`, policyID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
