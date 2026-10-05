// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package ack_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/ack"
)

type txKey struct{}

// recordingTx stands in for store.InTx: it marks the context it hands over,
// so the fakes can tell whether they ran inside the unit of work.
type recordingTx struct{ calls int }

func (r *recordingTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	r.calls++
	return fn(context.WithValue(ctx, txKey{}, true))
}

type txAwareStore struct {
	fakeStoreInTx bool
}

func (s *txAwareStore) Insert(ctx context.Context, _, _ string) (ack.RecordResult, error) {
	s.fakeStoreInTx = ctx.Value(txKey{}) == true
	return ack.RecordResult{ID: "ack-1"}, nil
}
func (s *txAwareStore) HasAcked(context.Context, string, string) (bool, error) { return false, nil }
func (s *txAwareStore) TransferAcks(ctx context.Context, _, _ string, _ bool) (int, int, []ack.TransferItem, error) {
	s.fakeStoreInTx = ctx.Value(txKey{}) == true
	return 1, 0, nil, nil
}

type txAwareAuditor struct {
	inTx bool
	err  error
}

func (a *txAwareAuditor) EmitAck(ctx context.Context, _ ack.RecordResult) error {
	a.inTx = ctx.Value(txKey{}) == true
	return a.err
}
func (a *txAwareAuditor) EmitTransfer(ctx context.Context, _ ack.TransferInput, _ ack.TransferResult) error {
	a.inTx = ctx.Value(txKey{}) == true
	return a.err
}

func TestRecordWritesTheAckAndItsAuditEventInOneTransaction(t *testing.T) {
	st, au, tx := &txAwareStore{}, &txAwareAuditor{}, &recordingTx{}
	svc := ack.NewService(st, au).WithTx(tx.InTx)

	_, err := svc.Record(context.Background(), ack.RecordInput{UserID: "u1", PolicyVersionID: "pv1"})
	require.NoError(t, err)
	require.Equal(t, 1, tx.calls)
	require.True(t, st.fakeStoreInTx, "the insert runs in the transaction")
	require.True(t, au.inTx, "the audit event is enqueued in the same transaction")
}

func TestRecordReturnsTheAuditFailureSoTheTransactionRollsBack(t *testing.T) {
	boom := errors.New("enqueue failed")
	svc := ack.NewService(&txAwareStore{}, &txAwareAuditor{err: boom}).WithTx((&recordingTx{}).InTx)
	_, err := svc.Record(context.Background(), ack.RecordInput{UserID: "u1", PolicyVersionID: "pv1"})
	require.ErrorIs(t, err, boom)
}

func TestTransferMovesAndAuditsInOneTransaction(t *testing.T) {
	st, au, tx := &txAwareStore{}, &txAwareAuditor{}, &recordingTx{}
	svc := ack.NewService(st, au).WithTx(tx.InTx)

	_, err := svc.Transfer(context.Background(), ack.TransferInput{SourceUserID: "a", TargetUserID: "b", ActorUserID: "admin"})
	require.NoError(t, err)
	require.Equal(t, 1, tx.calls)
	require.True(t, st.fakeStoreInTx)
	require.True(t, au.inTx)
}
