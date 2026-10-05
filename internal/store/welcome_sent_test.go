// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// TestWelcomeSent_ClaimOncePerAccount: Claim returns true exactly once per user
// id; every subsequent Claim returns false (the durable once-per-account
// guarantee). After Release the same user can be claimed again (retry path).
func TestWelcomeSent_ClaimOncePerAccount(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewWelcomeSentStore(pool)
	userID := "bbbbbbbb-3333-0000-0000-000000000001"

	claimed, err := s.Claim(ctx, userID)
	require.NoError(t, err)
	require.True(t, claimed, "first claim must win")

	claimed, err = s.Claim(ctx, userID)
	require.NoError(t, err)
	require.False(t, claimed, "second claim for the same account must lose")

	// Release rolls the claim back so a failed send can retry.
	require.NoError(t, s.Release(ctx, userID))
	claimed, err = s.Claim(ctx, userID)
	require.NoError(t, err)
	require.True(t, claimed, "after release the account can be claimed again")

	// Release of an unclaimed id is a no-op (safe).
	require.NoError(t, s.Release(ctx, "cccccccc-3333-0000-0000-000000000002"))
}

// TestWelcomeSent_DistinctAccountsIndependent: claims for different accounts do
// not interfere.
func TestWelcomeSent_DistinctAccountsIndependent(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewWelcomeSentStore(pool)

	c1, err := s.Claim(ctx, "dddddddd-3333-0000-0000-000000000001")
	require.NoError(t, err)
	c2, err := s.Claim(ctx, "dddddddd-3333-0000-0000-000000000002")
	require.NoError(t, err)
	require.True(t, c1)
	require.True(t, c2)
}
