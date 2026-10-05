// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	pg "github.com/Bugs5382/go-postgres"

	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// insertAckAt inserts an acknowledgment row for (userID, pvID) with an explicit
// acked_at so transfer tests can control the earliest/latest ordering that the
// dedupe reconciliation keys on.
func insertAckAt(t *testing.T, pool *pg.DB, userID, pvID string, at time.Time) {
	t.Helper()
	_, err := pool.Querier().Exec(context.Background(),
		`INSERT INTO acknowledgments (user_id, policy_version_id, acked_at) VALUES ($1,$2,$3)`,
		userID, pvID, at)
	if err != nil {
		t.Fatalf("insertAckAt(%s,%s): %v", userID, pvID, err)
	}
}

// ackOwnerAndTime returns the acked_at for (userID, pvID), and whether a row
// exists.
func ackOwnerAndTime(t *testing.T, pool *pg.DB, userID, pvID string) (time.Time, bool) {
	t.Helper()
	var at time.Time
	err := pool.Querier().QueryRow(context.Background(),
		`SELECT acked_at FROM acknowledgments WHERE user_id=$1 AND policy_version_id=$2`,
		userID, pvID).Scan(&at)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

func findItem(items []store.AckTransferItemRow, pvID string) (store.AckTransferItemRow, bool) {
	for _, it := range items {
		if it.PolicyVersionID == pvID {
			return it, true
		}
	}
	return store.AckTransferItemRow{}, false
}

func TestRecordAckIdempotent(t *testing.T) {
	pool := newTestDB(t)
	as := store.NewAcknowledgmentStore(pool)
	ctx := context.Background()

	userID := "aaaaaaaa-0000-0000-0000-000000000001"
	pvID := "bbbbbbbb-0000-0000-0000-000000000001"

	first, err := as.RecordAck(ctx, userID, pvID)
	if err != nil {
		t.Fatalf("first RecordAck: %v", err)
	}
	if first.ID == "" {
		t.Error("expected non-empty ack ID")
	}
	if first.UserID != userID {
		t.Errorf("UserID: got %q, want %q", first.UserID, userID)
	}
	if first.PolicyVersionID != pvID {
		t.Errorf("PolicyVersionID: got %q, want %q", first.PolicyVersionID, pvID)
	}
	if first.AckedAt.IsZero() {
		t.Error("expected non-zero AckedAt")
	}

	second, err := as.RecordAck(ctx, userID, pvID)
	if err != nil {
		t.Fatalf("second RecordAck (idempotent): %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("idempotent: expected same ack ID %q, got %q", first.ID, second.ID)
	}
}

func TestAcked(t *testing.T) {
	pool := newTestDB(t)
	as := store.NewAcknowledgmentStore(pool)
	ctx := context.Background()

	userID := "cccccccc-0000-0000-0000-000000000001"
	pvID1 := "dddddddd-0000-0000-0000-000000000001"
	pvID2 := "dddddddd-0000-0000-0000-000000000002"
	pvIDNone := "dddddddd-0000-0000-0000-000000000099"

	if _, err := as.RecordAck(ctx, userID, pvID1); err != nil {
		t.Fatalf("RecordAck pvID1: %v", err)
	}

	result, err := as.Acked(ctx, userID, []string{pvID1, pvID2, pvIDNone})
	if err != nil {
		t.Fatalf("Acked: %v", err)
	}
	if !result[pvID1] {
		t.Errorf("expected Acked[%q]=true", pvID1)
	}
	if result[pvID2] {
		t.Errorf("expected Acked[%q]=false", pvID2)
	}
	if result[pvIDNone] {
		t.Errorf("expected Acked[%q]=false", pvIDNone)
	}
}

func TestTransferAcks(t *testing.T) {
	pool := newTestDB(t)
	as := store.NewAcknowledgmentStore(pool)
	ctx := context.Background()

	const (
		src = "11111111-0000-0000-0000-000000000001"
		tgt = "11111111-0000-0000-0000-000000000002"

		pvKeptEarliest = "22222222-0000-0000-0000-000000000001" // both, source earlier
		pvTargetKept   = "22222222-0000-0000-0000-000000000002" // both, target earlier
		pvMoved        = "22222222-0000-0000-0000-000000000003" // source-only
		pvTargetOnly   = "22222222-0000-0000-0000-000000000004" // target-only, untouched
	)

	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// (a) both acked, source earlier -> KEPT_EARLIEST
	insertAckAt(t, pool, src, pvKeptEarliest, early)
	insertAckAt(t, pool, tgt, pvKeptEarliest, late)
	// (b) both acked, target earlier -> TARGET_KEPT
	insertAckAt(t, pool, src, pvTargetKept, late)
	insertAckAt(t, pool, tgt, pvTargetKept, early)
	// (c) source-only -> MOVED
	insertAckAt(t, pool, src, pvMoved, early)
	// target-only row must remain untouched
	insertAckAt(t, pool, tgt, pvTargetOnly, late)

	moved, deduped, items, err := as.TransferAcks(ctx, src, tgt, false)
	if err != nil {
		t.Fatalf("TransferAcks: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved: got %d want 1", moved)
	}
	if deduped != 2 {
		t.Errorf("deduped: got %d want 2", deduped)
	}
	if len(items) != 3 {
		t.Fatalf("items: got %d want 3 (%+v)", len(items), items)
	}

	// Classification.
	if it, ok := findItem(items, pvKeptEarliest); !ok || it.Resolution != store.ResolutionKeptEarliest {
		t.Errorf("pvKeptEarliest resolution: %+v ok=%v", it, ok)
	} else if it.TargetAckedAt == nil || !it.TargetAckedAt.Equal(late) || !it.SourceAckedAt.Equal(early) {
		t.Errorf("pvKeptEarliest times wrong: %+v", it)
	}
	if it, ok := findItem(items, pvTargetKept); !ok || it.Resolution != store.ResolutionTargetKept {
		t.Errorf("pvTargetKept resolution: %+v ok=%v", it, ok)
	}
	if it, ok := findItem(items, pvMoved); !ok || it.Resolution != store.ResolutionMoved {
		t.Errorf("pvMoved resolution: %+v ok=%v", it, ok)
	} else if it.TargetAckedAt != nil {
		t.Errorf("pvMoved must have nil TargetAckedAt: %+v", it)
	}

	// (a) target row backdated to source's earlier acked_at; source row gone.
	if at, ok := ackOwnerAndTime(t, pool, tgt, pvKeptEarliest); !ok || !at.Equal(early) {
		t.Errorf("KEPT_EARLIEST: target acked_at got %v ok=%v, want backdated to %v", at, ok, early)
	}
	if _, ok := ackOwnerAndTime(t, pool, src, pvKeptEarliest); ok {
		t.Error("KEPT_EARLIEST: source row should be gone")
	}

	// (b) target unchanged at its earlier time; source gone.
	if at, ok := ackOwnerAndTime(t, pool, tgt, pvTargetKept); !ok || !at.Equal(early) {
		t.Errorf("TARGET_KEPT: target acked_at got %v ok=%v, want unchanged %v", at, ok, early)
	}
	if _, ok := ackOwnerAndTime(t, pool, src, pvTargetKept); ok {
		t.Error("TARGET_KEPT: source row should be gone")
	}

	// (c) MOVED: version now belongs to target, source gone.
	if _, ok := ackOwnerAndTime(t, pool, tgt, pvMoved); !ok {
		t.Error("MOVED: target should now hold the version")
	}
	if _, ok := ackOwnerAndTime(t, pool, src, pvMoved); ok {
		t.Error("MOVED: source row should be gone")
	}

	// target-only untouched.
	if at, ok := ackOwnerAndTime(t, pool, tgt, pvTargetOnly); !ok || !at.Equal(late) {
		t.Errorf("target-only row disturbed: at=%v ok=%v", at, ok)
	}

	// (e) idempotent re-run: no source rows -> clean no-op.
	moved2, deduped2, items2, err := as.TransferAcks(ctx, src, tgt, false)
	if err != nil {
		t.Fatalf("second TransferAcks: %v", err)
	}
	if moved2 != 0 || deduped2 != 0 || len(items2) != 0 {
		t.Errorf("idempotent re-run not a no-op: moved=%d deduped=%d items=%d", moved2, deduped2, len(items2))
	}
}

func TestTransferAcksNeitherAcked(t *testing.T) {
	pool := newTestDB(t)
	as := store.NewAcknowledgmentStore(pool)

	// (d) neither user has acked anything -> no-op.
	moved, deduped, items, err := as.TransferAcks(context.Background(),
		"33333333-0000-0000-0000-000000000001",
		"33333333-0000-0000-0000-000000000002", false)
	if err != nil {
		t.Fatalf("TransferAcks: %v", err)
	}
	if moved != 0 || deduped != 0 || len(items) != 0 {
		t.Errorf("empty transfer: moved=%d deduped=%d items=%d", moved, deduped, len(items))
	}
}

func TestTransferAcksDryRun(t *testing.T) {
	pool := newTestDB(t)
	as := store.NewAcknowledgmentStore(pool)
	ctx := context.Background()

	const (
		src        = "44444444-0000-0000-0000-000000000001"
		tgt        = "44444444-0000-0000-0000-000000000002"
		pvBoth     = "55555555-0000-0000-0000-000000000001"
		pvMovedDry = "55555555-0000-0000-0000-000000000002"
	)
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	insertAckAt(t, pool, src, pvBoth, early)
	insertAckAt(t, pool, tgt, pvBoth, late)
	insertAckAt(t, pool, src, pvMovedDry, early)

	moved, deduped, items, err := as.TransferAcks(ctx, src, tgt, true)
	if err != nil {
		t.Fatalf("dry-run TransferAcks: %v", err)
	}
	if moved != 1 || deduped != 1 || len(items) != 2 {
		t.Errorf("dry-run counts: moved=%d deduped=%d items=%d, want 1/1/2", moved, deduped, len(items))
	}

	// Nothing mutated: source rows still present, target unchanged.
	if _, ok := ackOwnerAndTime(t, pool, src, pvBoth); !ok {
		t.Error("dry-run must not delete source rows (pvBoth)")
	}
	if _, ok := ackOwnerAndTime(t, pool, src, pvMovedDry); !ok {
		t.Error("dry-run must not delete source rows (pvMovedDry)")
	}
	if at, ok := ackOwnerAndTime(t, pool, tgt, pvBoth); !ok || !at.Equal(late) {
		t.Errorf("dry-run must not backdate target: at=%v ok=%v", at, ok)
	}
	if _, ok := ackOwnerAndTime(t, pool, tgt, pvMovedDry); ok {
		t.Error("dry-run must not create target row for source-only version")
	}
}

func TestTransferAcksRejectsSameUser(t *testing.T) {
	pool := newTestDB(t)
	as := store.NewAcknowledgmentStore(pool)
	if _, _, _, err := as.TransferAcks(context.Background(), "same", "same", false); err == nil {
		t.Fatal("expected error transferring a user onto itself")
	}
}

func TestAcknowledgmentStore_AckedAtAndDaily(t *testing.T) {
	pool := newTestDB(t)
	s := store.NewAcknowledgmentStore(pool)
	ctx := context.Background()

	const ver = "22222222-2222-2222-2222-222222222222"
	uA := "aaaaaaaa-0000-0000-0000-000000000001"
	uB := "aaaaaaaa-0000-0000-0000-000000000002"
	if _, err := s.RecordAck(ctx, uA, ver); err != nil {
		t.Fatalf("ack A: %v", err)
	}
	if _, err := s.RecordAck(ctx, uB, ver); err != nil {
		t.Fatalf("ack B: %v", err)
	}

	at, err := s.AckedAtForVersion(ctx, ver)
	if err != nil {
		t.Fatalf("AckedAtForVersion: %v", err)
	}
	if len(at) != 2 || at[uA].IsZero() || at[uB].IsZero() {
		t.Fatalf("AckedAtForVersion: got %v", at)
	}

	daily, err := s.DailyAckCounts(ctx, ver, time.Now().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("DailyAckCounts: %v", err)
	}
	total := 0
	for _, c := range daily {
		total += c
	}
	if total != 2 {
		t.Fatalf("DailyAckCounts total: got %d want 2 (%v)", total, daily)
	}
}
