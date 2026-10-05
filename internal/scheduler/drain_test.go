// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/scheduler"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// ---- drain fakes ----

type fakeOutbox struct {
	rows   map[string][]store.OutboxRow
	sent   []string
	pusers []string
}

func (f *fakeOutbox) PendingUsers(context.Context) ([]string, error) { return f.pusers, nil }
func (f *fakeOutbox) ListPending(_ context.Context, u string) ([]store.OutboxRow, error) {
	return f.rows[u], nil
}
func (f *fakeOutbox) MarkSent(_ context.Context, ids []string) error {
	f.sent = append(f.sent, ids...)
	// drop the marked rows so a re-run finds nothing (mirrors the partial index)
	for u, rs := range f.rows {
		var keep []store.OutboxRow
		for _, r := range rs {
			marked := false
			for _, id := range ids {
				if r.ID == id {
					marked = true
				}
			}
			if !marked {
				keep = append(keep, r)
			}
		}
		f.rows[u] = keep
	}
	return nil
}

type fakePendingAck struct {
	obligations map[string][]obligation.ObligationItem
}

func (f *fakePendingAck) MyObligations(_ context.Context, u string) ([]obligation.ObligationItem, error) {
	return f.obligations[u], nil
}

type fakeCandidates struct{ users []string }

func (f *fakeCandidates) PendingAckDigestUsers(context.Context) ([]string, error) {
	return f.users, nil
}

type fakeWindows struct{ w store.DigestWindow }

func (f *fakeWindows) Get(context.Context, string) (store.DigestWindow, error) { return f.w, nil }

type fakeDigestCadence struct {
	batch map[string]notifpolicy.Cadence
}

func (f *fakeDigestCadence) Resolve(_ context.Context, u, _ string) (notifpolicy.Decision, error) {
	if c, ok := f.batch[u]; ok {
		return notifpolicy.Decision{Deliver: true, Mode: notifpolicy.ModeBatch, Cadence: c}, nil
	}
	return notifpolicy.Decision{Deliver: true, Mode: notifpolicy.ModeImmediate, Cadence: notifpolicy.CadenceImmediate}, nil
}

type sentDigest struct {
	kind, userID, to, dedupRef string
	vars                       map[string]any
}
type fakeDigestSender struct{ sent []sentDigest }

func (f *fakeDigestSender) Send(_ context.Context, kind, userID, to, dedupRef string, vars any) error {
	f.sent = append(f.sent, sentDigest{kind, userID, to, dedupRef, vars.(map[string]any)})
	return nil
}

type fakeEmails struct{}

func (fakeEmails) ResolveEmail(_ context.Context, u string) (string, error) {
	return u + "@example.org", nil
}

type fakeGuard struct{ held map[string]bool }

func (f *fakeGuard) AcquireOnce(_ context.Context, k string) (bool, error) {
	if f.held == nil {
		f.held = map[string]bool{}
	}
	if f.held[k] {
		return false, nil
	}
	f.held[k] = true
	return true, nil
}

// mondayAt8 is a Monday (ISO dow=1) at 08:00 UTC, matching the default window.
var mondayAt8 = time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)

func newDrain(cfg scheduler.DrainConfig, now time.Time) *scheduler.Drain {
	return scheduler.NewDrain(cfg).WithClock(func() time.Time { return now })
}

// TestDrainGeneralDigest verifies the general digest assembles outbox rows into
// the right sections, sends once, marks the rows sent, and a re-run within the
// window does not double-send.
func TestDrainGeneralDigest(t *testing.T) {
	ctx := context.Background()
	ob := &fakeOutbox{
		pusers: []string{"u1"},
		rows: map[string][]store.OutboxRow{
			"u1": {
				{ID: "r1", UserID: "u1", Kind: "policy-published", Category: "informational", WindowKind: "daily",
					Vars: map[string]any{"title": "New AI Policy", "meta": "Effective Sep 1", "actionHref": "https://p/1", "actionLabel": "Read"}},
				{ID: "r2", UserID: "u1", Kind: "workflow-awaiting-approval", Category: "workflow", WindowKind: "daily",
					Vars: map[string]any{"title": "Approve X", "actionHref": "https://p/2", "actionLabel": "Open"}},
			},
		},
	}
	snd := &fakeDigestSender{}
	drain := newDrain(scheduler.DrainConfig{
		Outbox:     ob,
		PendingAck: &fakePendingAck{},
		Candidates: &fakeCandidates{},
		Windows:    &fakeWindows{w: store.DigestWindow{DailyHour: 8, WeeklyDOW: 1}},
		Cadence:    &fakeDigestCadence{},
		Sender:     snd,
		Emails:     fakeEmails{},
		Guard:      &fakeGuard{},
		PortalURL:  "https://p",
	}, mondayAt8)

	if err := drain.DrainOnce(ctx); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("digests sent = %d; want 1", len(snd.sent))
	}
	got := snd.sent[0]
	if got.kind != "digest" || got.userID != "u1" || got.to != "u1@example.org" {
		t.Errorf("unexpected send envelope: %+v", got)
	}
	newAndUpdated := got.vars["newAndUpdated"].([]map[string]any)
	approvals := got.vars["approvals"].([]map[string]any)
	if len(newAndUpdated) != 1 || newAndUpdated[0]["title"] != "New AI Policy" {
		t.Errorf("newAndUpdated section wrong: %+v", newAndUpdated)
	}
	if len(approvals) != 1 || approvals[0]["title"] != "Approve X" {
		t.Errorf("approvals section wrong: %+v", approvals)
	}
	if len(ob.sent) != 2 {
		t.Errorf("rows marked sent = %d; want 2", len(ob.sent))
	}

	// Re-run within the same window: rows drained + guard held → no second send.
	if err := drain.DrainOnce(ctx); err != nil {
		t.Fatalf("DrainOnce re-run: %v", err)
	}
	if len(snd.sent) != 1 {
		t.Errorf("digests after re-run = %d; want still 1", len(snd.sent))
	}
}

// TestDrainPendingAckDigest verifies the pending-ack digest is assembled from
// LIVE obligations (not the outbox), sent separately, for a batch-cadence user.
func TestDrainPendingAckDigest(t *testing.T) {
	ctx := context.Background()
	snd := &fakeDigestSender{}
	drain := newDrain(scheduler.DrainConfig{
		Outbox: &fakeOutbox{},
		PendingAck: &fakePendingAck{obligations: map[string][]obligation.ObligationItem{
			"u9": {{Number: "POL-14", VersionNo: 3, Title: "Remote Access", PolicyVersionID: "pv9"}},
		}},
		Candidates: &fakeCandidates{users: []string{"u9"}},
		Windows:    &fakeWindows{w: store.DigestWindow{DailyHour: 8, WeeklyDOW: 1}},
		Cadence:    &fakeDigestCadence{batch: map[string]notifpolicy.Cadence{"u9": notifpolicy.CadenceDaily}},
		Sender:     snd,
		Emails:     fakeEmails{},
		Guard:      &fakeGuard{},
		PortalURL:  "https://p/ack",
	}, mondayAt8)

	if err := drain.DrainOnce(ctx); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if len(snd.sent) != 1 {
		t.Fatalf("pending-ack digests sent = %d; want 1", len(snd.sent))
	}
	got := snd.sent[0]
	awaiting := got.vars["awaitingAck"].([]map[string]any)
	if len(awaiting) != 1 || awaiting[0]["title"] != "Remote Access" || awaiting[0]["meta"] != "POL-14 v3" {
		t.Errorf("awaitingAck section wrong: %+v", awaiting)
	}
	if got.vars["awaitingAck"] == nil || got.dedupRef[:4] != "ack:" {
		t.Errorf("expected pending-ack dedupRef, got %q", got.dedupRef)
	}
}

// TestDrainSkipsOutsideWindow verifies nothing sends when the local hour does
// not match the user's digest window.
func TestDrainSkipsOutsideWindow(t *testing.T) {
	ctx := context.Background()
	snd := &fakeDigestSender{}
	ob := &fakeOutbox{
		pusers: []string{"u1"},
		rows: map[string][]store.OutboxRow{
			"u1": {{ID: "r1", UserID: "u1", Kind: "policy-published", Category: "informational", WindowKind: "daily", Vars: map[string]any{"title": "X"}}},
		},
	}
	// now is 09:00 but the window is 08:00.
	drain := newDrain(scheduler.DrainConfig{
		Outbox:     ob,
		PendingAck: &fakePendingAck{},
		Candidates: &fakeCandidates{},
		Windows:    &fakeWindows{w: store.DigestWindow{DailyHour: 8, WeeklyDOW: 1}},
		Cadence:    &fakeDigestCadence{},
		Sender:     snd,
		Emails:     fakeEmails{},
		Guard:      &fakeGuard{},
	}, mondayAt8.Add(time.Hour))

	if err := drain.DrainOnce(ctx); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if len(snd.sent) != 0 {
		t.Errorf("digests sent outside window = %d; want 0", len(snd.sent))
	}
}

// TestDrainWeeklyWaitsForDay verifies a weekly-window row does not drain on a
// non-matching weekday even at the right hour.
func TestDrainWeeklyWaitsForDay(t *testing.T) {
	ctx := context.Background()
	snd := &fakeDigestSender{}
	ob := &fakeOutbox{
		pusers: []string{"u1"},
		rows: map[string][]store.OutboxRow{
			"u1": {{ID: "r1", UserID: "u1", Kind: "policy-published", Category: "informational", WindowKind: "weekly", Vars: map[string]any{"title": "X"}}},
		},
	}
	// Tuesday 08:00 (dow=2) but weekly window is Monday (dow=1).
	tuesdayAt8 := mondayAt8.Add(24 * time.Hour)
	drain := newDrain(scheduler.DrainConfig{
		Outbox:     ob,
		PendingAck: &fakePendingAck{},
		Candidates: &fakeCandidates{},
		Windows:    &fakeWindows{w: store.DigestWindow{DailyHour: 8, WeeklyDOW: 1}},
		Cadence:    &fakeDigestCadence{},
		Sender:     snd,
		Emails:     fakeEmails{},
		Guard:      &fakeGuard{},
	}, tuesdayAt8)

	if err := drain.DrainOnce(ctx); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if len(snd.sent) != 0 {
		t.Errorf("weekly digest drained on wrong weekday = %d; want 0", len(snd.sent))
	}
}
