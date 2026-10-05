// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package ack_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/ack"
)

type fakeAckStore struct {
	inserted []string        // userID:pvID pairs
	ackedMap map[string]bool // key: userID+":"+pvID

	// transfer stubbing
	transferMoved   int
	transferDeduped int
	transferItems   []ack.TransferItem
	transferSource  string
	transferTarget  string
	transferDryRun  bool
	transferCalls   int
}

func (f *fakeAckStore) Insert(_ context.Context, userID, pvID string) (ack.RecordResult, error) {
	f.inserted = append(f.inserted, userID+":"+pvID)
	return ack.RecordResult{ID: "ack-1", AlreadyExisted: false}, nil
}

func (f *fakeAckStore) HasAcked(_ context.Context, userID, pvID string) (bool, error) {
	return f.ackedMap[userID+":"+pvID], nil
}

func (f *fakeAckStore) TransferAcks(_ context.Context, source, target string, dryRun bool) (int, int, []ack.TransferItem, error) {
	f.transferCalls++
	f.transferSource, f.transferTarget, f.transferDryRun = source, target, dryRun
	return f.transferMoved, f.transferDeduped, f.transferItems, nil
}

type fakeAuditEmitter struct {
	events    []string
	transfers []ack.TransferInput
}

func (f *fakeAuditEmitter) EmitAck(_ context.Context, a ack.RecordResult) error {
	f.events = append(f.events, a.ID)
	return nil
}

func (f *fakeAuditEmitter) EmitTransfer(_ context.Context, in ack.TransferInput, _ ack.TransferResult) error {
	f.transfers = append(f.transfers, in)
	return nil
}

func TestRecordAckEmitsAuditEvent(t *testing.T) {
	store := &fakeAckStore{ackedMap: map[string]bool{}}
	auditor := &fakeAuditEmitter{}
	svc := ack.NewService(store, auditor)

	result, err := svc.Record(context.Background(), ack.RecordInput{
		UserID: "u1", PolicyVersionID: "pv1",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if result.ID == "" {
		t.Error("expected non-empty ack ID")
	}
	if len(auditor.events) != 1 {
		t.Errorf("expected 1 audit event, got %d", len(auditor.events))
	}
}

func TestRecordAckAlreadyAckedIsIdempotent(t *testing.T) {
	store := &fakeAckStore{ackedMap: map[string]bool{"u1:pv1": true}}
	auditor := &fakeAuditEmitter{}
	svc := ack.NewService(store, auditor)

	result, err := svc.Record(context.Background(), ack.RecordInput{
		UserID: "u1", PolicyVersionID: "pv1",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !result.AlreadyExisted {
		t.Error("expected AlreadyExisted=true for duplicate")
	}
	// Audit event must NOT be emitted a second time for an already-existing ack
	if len(auditor.events) != 0 {
		t.Errorf("expected 0 audit events for duplicate, got %d", len(auditor.events))
	}
}

func TestTransferEmitsOneAuditEventOnMutation(t *testing.T) {
	store := &fakeAckStore{transferMoved: 2, transferDeduped: 1}
	auditor := &fakeAuditEmitter{}
	svc := ack.NewService(store, auditor)

	res, err := svc.Transfer(context.Background(), ack.TransferInput{
		SourceUserID: "src", TargetUserID: "tgt", ActorUserID: "admin",
		MergeOperationID: "merge-1",
	})
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if res.Moved != 2 || res.Deduped != 1 {
		t.Errorf("counts: got moved=%d deduped=%d, want 2/1", res.Moved, res.Deduped)
	}
	if store.transferSource != "src" || store.transferTarget != "tgt" {
		t.Errorf("store got source=%q target=%q", store.transferSource, store.transferTarget)
	}
	if store.transferDryRun {
		t.Error("expected non-dry-run call to store")
	}
	if len(auditor.transfers) != 1 {
		t.Fatalf("expected 1 transfer audit event, got %d", len(auditor.transfers))
	}
	if auditor.transfers[0].MergeOperationID != "merge-1" || auditor.transfers[0].ActorUserID != "admin" {
		t.Errorf("audit event carried wrong input: %+v", auditor.transfers[0])
	}
}

func TestTransferNoAuditWhenNothingMoved(t *testing.T) {
	store := &fakeAckStore{transferMoved: 0, transferDeduped: 0}
	auditor := &fakeAuditEmitter{}
	svc := ack.NewService(store, auditor)

	if _, err := svc.Transfer(context.Background(), ack.TransferInput{
		SourceUserID: "src", TargetUserID: "tgt",
	}); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if len(auditor.transfers) != 0 {
		t.Errorf("expected 0 audit events for empty transfer, got %d", len(auditor.transfers))
	}
}

func TestTransferDryRunNoAudit(t *testing.T) {
	store := &fakeAckStore{transferMoved: 3, transferDeduped: 2}
	auditor := &fakeAuditEmitter{}
	svc := ack.NewService(store, auditor)

	if _, err := svc.Transfer(context.Background(), ack.TransferInput{
		SourceUserID: "src", TargetUserID: "tgt", DryRun: true,
	}); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if !store.transferDryRun {
		t.Error("expected dry-run flag forwarded to store")
	}
	if len(auditor.transfers) != 0 {
		t.Errorf("dry-run must not emit audit; got %d events", len(auditor.transfers))
	}
}
