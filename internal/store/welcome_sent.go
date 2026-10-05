// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	postgres "github.com/Bugs5382/go-postgres"
)

// WelcomeSentStore is the durable once-per-account guard for the welcome email.
// It backs 's "send once per account" rule: identity emits a
// unified account.created signal on every creation route, and both the
// account-created consumer (welcome-account) and the SSO lifecycle consumer
// (sso-account-welcome) claim through this store so a brand-new user receives
// exactly one welcome even if the create is retried, the event is redelivered,
// or a federated JIT fires more than once. Unlike the process-local
// mail.TTLDeduper it persists across restarts and coordinates across replicas.
type WelcomeSentStore struct {
	db *postgres.DB
}

// NewWelcomeSentStore returns a WelcomeSentStore on db.
func NewWelcomeSentStore(p *postgres.DB) *WelcomeSentStore {
	return &WelcomeSentStore{db: p}
}

// Claim atomically records that userID's welcome is being sent and reports
// whether THIS caller won the claim. It returns claimed=true exactly once per
// user id (the first caller); every subsequent caller gets claimed=false and
// must skip its welcome. The INSERT... ON CONFLICT DO NOTHING makes the claim
// race-safe across concurrent consumers and replicas.
//
// The caller that wins the claim owns the send: if the send fails it must call
// Release so a redelivery can re-claim and retry, otherwise the account would
// be permanently marked welcomed without an email ever going out.
func (s *WelcomeSentStore) Claim(ctx context.Context, userID string) (bool, error) {
	tag, err := s.db.Querier().Exec(ctx,
		`INSERT INTO welcome_email_sent (user_id) VALUES ($1)
		 ON CONFLICT (user_id) DO NOTHING`, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Release removes a claim so a failed send can be retried on redelivery. Safe
// to call for a user id that is not claimed (no-op).
func (s *WelcomeSentStore) Release(ctx context.Context, userID string) error {
	_, err := s.db.Querier().Exec(ctx, `DELETE FROM welcome_email_sent WHERE user_id = $1`, userID)
	return err
}
