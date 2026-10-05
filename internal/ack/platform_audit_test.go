// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package ack_test

import (
	"context"
	"sync"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/audit"
)

// fakeAuditSink captures the audit.Event values the adapter publishes so tests
// can assert on the wire shape without standing up RabbitMQ.
type fakeAuditSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeAuditSink) Emit(_ context.Context, ev audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeAuditSink) recorded() []audit.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]audit.Event(nil), f.events...)
}

// TestEmitAckCarriesNonEmptyActor is the regression guard for
// : the ack.recorded audit event must name the user who
// acknowledged. An acknowledgment is the attestation that a named person read
// and accepted a policy, so an event with an empty ActorUserID is unusable as
// evidence. Seven prod rows were written with an empty actor before this fix.
func TestEmitAckCarriesNonEmptyActor(t *testing.T) {
	sink := &fakeAuditSink{}
	adapter := ack.NewPlatformAuditAdapter(sink)

	err := adapter.EmitAck(context.Background(), ack.RecordResult{
		ID:              "ack-1",
		UserID:          "u1",
		PolicyVersionID: "pv1",
	})
	if err != nil {
		t.Fatalf("EmitAck: %v", err)
	}

	events := sink.recorded()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	ev := events[0]

	if ev.ActorUserID == "" {
		t.Error("ack.recorded carries an empty ActorUserID: the acknowledgement names nobody")
	}
	if ev.ActorUserID != "u1" {
		t.Errorf("ActorUserID: got %q, want %q", ev.ActorUserID, "u1")
	}
	if ev.Action != "ack.recorded" {
		t.Errorf("Action: got %q, want ack.recorded", ev.Action)
	}
	if ev.Tier != audit.TierAudit {
		t.Errorf("Tier: got %v, want audit.TierAudit", ev.Tier)
	}
	if ev.Subject != "acknowledgment:ack-1" {
		t.Errorf("Subject: got %q, want acknowledgment:ack-1", ev.Subject)
	}
	if got := ev.Attributes["policy_version_id"]; got != "pv1" {
		t.Errorf("policy_version_id: got %q, want pv1", got)
	}
}

// TestRecordThroughRealAuditAdapterCarriesActor proves the full domain path —
// not just the adapter in isolation — lands a non-empty actor on the wire. It
// runs Service.Record against the real PlatformAuditAdapter over a fake sink,
// so a future change that drops the actor anywhere between RecordInput and the
// emitted event fails here. This is the path RecordAck takes in production,
// where in.UserID comes from the verified forwarded JWT claims.
func TestRecordThroughRealAuditAdapterCarriesActor(t *testing.T) {
	sink := &fakeAuditSink{}
	store := &fakeAckStore{ackedMap: map[string]bool{}}
	svc := ack.NewService(store, ack.NewPlatformAuditAdapter(sink))

	if _, err := svc.Record(context.Background(), ack.RecordInput{
		UserID:          "user-42",
		PolicyVersionID: "pv-7",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	events := sink.recorded()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].ActorUserID == "" {
		t.Fatal("ack.recorded emitted with an empty ActorUserID via Service.Record")
	}
	if events[0].ActorUserID != "user-42" {
		t.Errorf("ActorUserID: got %q, want user-42", events[0].ActorUserID)
	}
	if got := events[0].Attributes["policy_version_id"]; got != "pv-7" {
		t.Errorf("policy_version_id: got %q, want pv-7", got)
	}
}

// TestRecordStampsActorOntoResult pins the mechanism the audit event depends
// on: Record copies the verified acting user and target off RecordInput onto
// RecordResult. The store does not echo these back, which is the structural
// reason the event could not name an actor before.
func TestRecordStampsActorOntoResult(t *testing.T) {
	store := &fakeAckStore{ackedMap: map[string]bool{}}
	svc := ack.NewService(store, &fakeAuditEmitter{})

	res, err := svc.Record(context.Background(), ack.RecordInput{
		UserID:          "user-9",
		PolicyVersionID: "pv-9",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if res.UserID != "user-9" {
		t.Errorf("RecordResult.UserID: got %q, want user-9", res.UserID)
	}
	if res.PolicyVersionID != "pv-9" {
		t.Errorf("RecordResult.PolicyVersionID: got %q, want pv-9", res.PolicyVersionID)
	}
}

// TestEmitTransferCarriesNonEmptyActor covers the sibling event in the same
// adapter. ack.transferred already reads ActorUserID off TransferInput, so this
// locks that in alongside ack.recorded rather than leaving the pair
// inconsistently guarded.
func TestEmitTransferCarriesNonEmptyActor(t *testing.T) {
	sink := &fakeAuditSink{}
	adapter := ack.NewPlatformAuditAdapter(sink)

	err := adapter.EmitTransfer(context.Background(),
		ack.TransferInput{
			SourceUserID:     "src",
			TargetUserID:     "tgt",
			ActorUserID:      "admin-1",
			MergeOperationID: "merge-1",
		},
		ack.TransferResult{Moved: 2, Deduped: 1},
	)
	if err != nil {
		t.Fatalf("EmitTransfer: %v", err)
	}

	events := sink.recorded()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].ActorUserID == "" {
		t.Error("ack.transferred carries an empty ActorUserID")
	}
	if events[0].ActorUserID != "admin-1" {
		t.Errorf("ActorUserID: got %q, want admin-1", events[0].ActorUserID)
	}
	if events[0].Action != "ack.transferred" {
		t.Errorf("Action: got %q, want ack.transferred", events[0].Action)
	}
}
