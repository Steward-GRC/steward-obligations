// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	ackpkg "github.com/Steward-GRC/steward-obligations/internal/ack"
)

// AckStoreAdapter adapts *AcknowledgmentStore to satisfy ack.Store.
// ack.Store uses the domain-side RecordResult shape; AcknowledgmentStore uses
// store-side Ack rows. Holding the translation in a thin adapter keeps the ack
// domain package free of any dependency on the pgx-backed store types.
type AckStoreAdapter struct{ s *AcknowledgmentStore }

// NewAckStoreAdapter returns an adapter that bridges *AcknowledgmentStore to
// the ack.Store interface expected by ack.NewService.
func NewAckStoreAdapter(s *AcknowledgmentStore) *AckStoreAdapter {
	return &AckStoreAdapter{s: s}
}

// Insert delegates to AcknowledgmentStore.RecordAck, translating the result
// into the domain RecordResult. AlreadyExisted is reported as false here
// because the store-level RecordAck is unconditionally idempotent (ON CONFLICT
// DO UPDATE); the ack.Service short-circuits prior to Insert via HasAcked,
// which is the authoritative duplicate check.
func (a *AckStoreAdapter) Insert(ctx context.Context, userID, policyVersionID string) (ackpkg.RecordResult, error) {
	row, err := a.s.RecordAck(ctx, userID, policyVersionID)
	if err != nil {
		return ackpkg.RecordResult{}, err
	}
	return ackpkg.RecordResult{ID: row.ID, AlreadyExisted: false}, nil
}

// HasAcked delegates to AcknowledgmentStore.HasAcked unchanged.
func (a *AckStoreAdapter) HasAcked(ctx context.Context, userID, policyVersionID string) (bool, error) {
	return a.s.HasAcked(ctx, userID, policyVersionID)
}

// TransferAcks delegates to AcknowledgmentStore.TransferAcks, translating the
// store-side AckTransferItemRow rows into the domain-side ack.TransferItem
// shape so the ack package stays free of the pgx-backed store types.
func (a *AckStoreAdapter) TransferAcks(ctx context.Context, sourceUserID, targetUserID string, dryRun bool) (int, int, []ackpkg.TransferItem, error) {
	moved, deduped, rows, err := a.s.TransferAcks(ctx, sourceUserID, targetUserID, dryRun)
	if err != nil {
		return 0, 0, nil, err
	}
	var items []ackpkg.TransferItem
	for _, r := range rows {
		items = append(items, ackpkg.TransferItem{
			PolicyVersionID: r.PolicyVersionID,
			SourceAckedAt:   r.SourceAckedAt,
			TargetAckedAt:   r.TargetAckedAt,
			Resolution:      r.Resolution,
		})
	}
	return moved, deduped, items, nil
}
