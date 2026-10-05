// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

func sampleRow(dedup string) store.MailOutboxRow {
	return store.MailOutboxRow{
		Kind:      "policy-ack-reminder",
		Recipient: "user@example.com",
		From:      "no-reply@example.org",
		Subject:   "Policy acknowledgements due",
		HTML:      "<p>hi</p>",
		Text:      "hi",
		UserID:    "11111111-1111-1111-1111-111111111111",
		DedupKey:  dedup,
	}
}

func TestMailOutbox_EnqueueAndClaim(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-1")))

	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	got := rows[0]
	require.NotEmpty(t, got.ID)
	require.Equal(t, "policy-ack-reminder", got.Kind)
	require.Equal(t, "user@example.com", got.Recipient)
	require.Equal(t, "Policy acknowledgements due", got.Subject)
	require.Equal(t, "<p>hi</p>", got.HTML)
	require.Equal(t, "dk-1", got.DedupKey)
	require.Equal(t, 0, got.Attempts)
}

func TestMailOutbox_EnqueueIdempotentOnDedupKey(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-dup")))
	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-dup")), "a redelivery re-enqueues the same key")

	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a duplicate dedup key must not create a second hold")
}

func TestMailOutbox_NullDedupKeyAlwaysInserts(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	require.NoError(t, s.Enqueue(ctx, sampleRow("")))
	require.NoError(t, s.Enqueue(ctx, sampleRow("")))

	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2, "rows with no dedup key are never collapsed")
}

func TestMailOutbox_MarkSentNotReclaimed(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-sent")))
	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	require.NoError(t, s.MarkSent(ctx, rows[0].ID))

	again, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, again, "a sent row is never re-claimed (replay-once)")

	sent, err := s.CountByStatus(ctx, "sent")
	require.NoError(t, err)
	require.Equal(t, 1, sent)
}

func TestMailOutbox_RescheduleHoldsUntilNextAttempt(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-retry")))
	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	id := rows[0].ID

	// Push next_attempt_at into the future: the row must not be claimable now.
	require.NoError(t, s.Reschedule(ctx, id, "503 transient", time.Now().Add(time.Hour)))
	notYet, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, notYet, "a rescheduled row is held until its next_attempt_at")

	// Backdate it: now claimable again, with attempts bumped.
	require.NoError(t, s.Reschedule(ctx, id, "503 transient", time.Now().Add(-time.Minute)))
	ready, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.Equal(t, 2, ready[0].Attempts, "each reschedule bumps attempts")
}

func TestMailOutbox_FailedFreesDedupKeyForReholding(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-fail")))
	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.NoError(t, s.MarkFailed(ctx, rows[0].ID, "permanent"))

	// The partial unique index excludes failed rows, so the same business key
	// can be re-held by a later attempt.
	require.NoError(t, s.Enqueue(ctx, sampleRow("dk-fail")))
	pending, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a failed row does not block re-holding the same dedup key")
}

func TestMailOutbox_ClaimFIFO(t *testing.T) {
	pool := newTestDB(t)
	ctx := context.Background()
	s := store.NewMailOutboxStore(pool)

	for _, dk := range []string{"a", "b", "c"} {
		r := sampleRow(dk)
		r.Recipient = dk + "@example.com"
		require.NoError(t, s.Enqueue(ctx, r))
		time.Sleep(5 * time.Millisecond) // ensure distinct created_at ordering
	}
	rows, err := s.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, "a@example.com", rows[0].Recipient)
	require.Equal(t, "b@example.com", rows[1].Recipient)
	require.Equal(t, "c@example.com", rows[2].Recipient)
}
