// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"errors"
	"testing"

	outbox "github.com/Bugs5382/go-outbox"
	pg "github.com/Bugs5382/go-postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

func newOutbox(t *testing.T, db *pg.DB) *outbox.Outbox {
	t.Helper()
	ob, err := outbox.New(outbox.WithTable("audit_outbox"))
	require.NoError(t, err)
	require.NoError(t, ob.Migrate(context.Background(), db))
	return ob
}

func outboxRows(t *testing.T, db *pg.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Querier().QueryRow(context.Background(), `SELECT count(*) FROM audit_outbox`).Scan(&n))
	return n
}

func TestAnAckAndItsAuditMessageCommitTogether(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	acks := store.NewAcknowledgmentStore(db)
	pub := store.NewOutboxPublisher(db, newOutbox(t, db), "application/protobuf")
	user, pv := uuid.NewString(), uuid.NewString()

	require.NoError(t, store.InTx(ctx, db, func(ctx context.Context) error {
		if _, err := acks.RecordAck(ctx, user, pv); err != nil {
			return err
		}
		return pub.Publish(ctx, "audit.audit", []byte("event"))
	}))

	ok, err := acks.HasAcked(ctx, user, pv)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, outboxRows(t, db))
}

func TestAFailedAuditEnqueueRollsTheAckBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	acks := store.NewAcknowledgmentStore(db)
	pub := store.NewOutboxPublisher(db, newOutbox(t, db), "application/protobuf")
	user, pv := uuid.NewString(), uuid.NewString()
	boom := errors.New("boom")

	err := store.InTx(ctx, db, func(ctx context.Context) error {
		if _, err := acks.RecordAck(ctx, user, pv); err != nil {
			return err
		}
		if err := pub.Publish(ctx, "audit.audit", []byte("event")); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)

	ok, err := acks.HasAcked(ctx, user, pv)
	require.NoError(t, err)
	require.False(t, ok, "the ack must not survive without its audit event")
	require.Zero(t, outboxRows(t, db))
}

func TestPublishOutsideATransactionEnqueuesOnItsOwn(t *testing.T) {
	db := newTestDB(t)
	pub := store.NewOutboxPublisher(db, newOutbox(t, db), "application/protobuf")
	require.NoError(t, pub.Publish(context.Background(), "audit.audit", []byte("event")))
	require.Equal(t, 1, outboxRows(t, db))
}
