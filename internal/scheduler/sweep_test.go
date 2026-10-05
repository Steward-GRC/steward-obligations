// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/scheduler"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// ---- fakes ----

type fakeLister struct{ items []obligation.Outstanding }

func (f *fakeLister) OutstandingObligations(context.Context) ([]obligation.Outstanding, error) {
	return f.items, nil
}

type fakeState struct {
	states    map[string]store.BackoffState
	reminders map[string]int
	escalated map[string]bool
}

func newFakeState() *fakeState {
	return &fakeState{states: map[string]store.BackoffState{}, reminders: map[string]int{}, escalated: map[string]bool{}}
}
func key(u, v string) string { return u + "|" + v }

func (f *fakeState) EnsureAnchor(_ context.Context, u, v string, at time.Time) (store.BackoffState, error) {
	k := key(u, v)
	st, ok := f.states[k]
	if !ok {
		st = store.BackoffState{FirstNotifiedAt: at}
		f.states[k] = st
	}
	return st, nil
}
func (f *fakeState) RecordReminder(_ context.Context, u, v string, at time.Time) error {
	k := key(u, v)
	st := f.states[k]
	st.ReminderCount++
	st.LastRemindedAt = at
	f.states[k] = st
	f.reminders[k]++
	return nil
}
func (f *fakeState) RecordEscalation(_ context.Context, u, v string, at time.Time) error {
	k := key(u, v)
	st := f.states[k]
	st.Escalated = true
	f.states[k] = st
	f.escalated[k] = true
	return nil
}

type fakeReminders struct{ sent []notify.AckReminderPayload }

func (f *fakeReminders) SendAckReminder(_ context.Context, p notify.AckReminderPayload) {
	f.sent = append(f.sent, p)
}

type fakeEscalator struct{ sent []notify.EscalationPayload }

func (f *fakeEscalator) SendEscalationToManager(_ context.Context, p notify.EscalationPayload) {
	f.sent = append(f.sent, p)
}

// fakeCadence returns immediate unless the user is in the batch set.
type fakeCadence struct {
	batch map[string]notifpolicy.Cadence
}

func (f *fakeCadence) Resolve(_ context.Context, userID, _ string) (notifpolicy.Decision, error) {
	if c, ok := f.batch[userID]; ok {
		return notifpolicy.Decision{Deliver: true, Mode: notifpolicy.ModeBatch, Cadence: c}, nil
	}
	return notifpolicy.Decision{Deliver: true, Mode: notifpolicy.ModeImmediate, Cadence: notifpolicy.CadenceImmediate}, nil
}

type fakeManagers struct {
	mgr    string
	ok     bool
	called []string
}

func (f *fakeManagers) ResolveManager(_ context.Context, userID string) (string, bool, error) {
	f.called = append(f.called, userID)
	return f.mgr, f.ok, nil
}

func newSweep(t *testing.T, cfg scheduler.SweepConfig, now time.Time) *scheduler.Sweep {
	t.Helper()
	return scheduler.NewSweep(cfg).WithClock(func() time.Time { return now })
}

// TestSweepSendsReminderWhenDue verifies an immediate-cadence user gets a
// reminder once its back-off step is due, and the reminder count advances.
func TestSweepSendsReminderWhenDue(t *testing.T) {
	ctx := context.Background()
	anchor := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	now := anchor.Add(2 * 24 * time.Hour) // +2d: first reminder (due day 1) is due

	st := newFakeState()
	st.states[key("u1", "pv1")] = store.BackoffState{FirstNotifiedAt: anchor}
	rem := &fakeReminders{}
	esc := &fakeEscalator{}

	sw := newSweep(t, scheduler.SweepConfig{
		Lister:    &fakeLister{items: []obligation.Outstanding{{UserID: "u1", PolicyVersionID: "pv1", Number: "POL-1", VersionNo: 3, Title: "Access"}}},
		State:     st,
		Reminders: rem,
		Escalator: esc,
		Cadence:   &fakeCadence{},
		Managers:  &fakeManagers{},
		Backoff:   scheduler.BackoffConfig{EscalationAfter: 30 * 24 * time.Hour},
		PortalURL: "https://p/ack",
	}, now)

	if err := sw.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rem.sent) != 1 {
		t.Fatalf("reminders sent = %d; want 1", len(rem.sent))
	}
	if rem.sent[0].Type != "reminder" || rem.sent[0].Policies[0].Ref != "POL-1 v3" {
		t.Errorf("unexpected reminder payload: %+v", rem.sent[0])
	}
	if st.reminders[key("u1", "pv1")] != 1 {
		t.Errorf("reminder count not advanced: %d", st.reminders[key("u1", "pv1")])
	}
	if len(esc.sent) != 0 {
		t.Errorf("no escalation expected, got %d", len(esc.sent))
	}
}

// TestSweepEscalatesOnceThenStops verifies the escalation fires exactly once and
// a subsequent sweep no longer escalates or reminds the user.
func TestSweepEscalatesOnceThenStops(t *testing.T) {
	ctx := context.Background()
	anchor := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	now := anchor.Add(40 * 24 * time.Hour) // well past the 30d escalation threshold

	st := newFakeState()
	st.states[key("u1", "pv1")] = store.BackoffState{FirstNotifiedAt: anchor, ReminderCount: 5}
	rem := &fakeReminders{}
	esc := &fakeEscalator{}
	mgr := &fakeManagers{mgr: "boss", ok: true}

	cfg := scheduler.SweepConfig{
		Lister:    &fakeLister{items: []obligation.Outstanding{{UserID: "u1", PolicyVersionID: "pv1"}}},
		State:     st,
		Reminders: rem,
		Escalator: esc,
		Cadence:   &fakeCadence{},
		Managers:  mgr,
		Backoff:   scheduler.BackoffConfig{EscalationAfter: 30 * 24 * time.Hour},
	}

	if err := newSweep(t, cfg, now).Run(ctx); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if len(esc.sent) != 1 {
		t.Fatalf("escalations after run 1 = %d; want 1", len(esc.sent))
	}
	if esc.sent[0].UserID != "boss" {
		t.Errorf("escalation target = %q; want manager 'boss'", esc.sent[0].UserID)
	}
	if !st.escalated[key("u1", "pv1")] {
		t.Error("escalated_at not recorded")
	}

	// Second sweep: already escalated → no further escalation, no reminder.
	if err := newSweep(t, cfg, now.Add(8*24*time.Hour)).Run(ctx); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(esc.sent) != 1 {
		t.Errorf("escalations after run 2 = %d; want still 1 (once then stop)", len(esc.sent))
	}
	if len(rem.sent) != 0 {
		t.Errorf("reminders after escalation = %d; want 0 (manager owns it)", len(rem.sent))
	}
}

// TestSweepDigestUserDefersReminder verifies a user who folded compliance into a
// digest gets NO immediate reminder from the sweep (the drain handles it), but
// the anchor is still tracked so escalation timing works.
func TestSweepDigestUserDefersReminder(t *testing.T) {
	ctx := context.Background()
	anchor := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	now := anchor.Add(5 * 24 * time.Hour)

	st := newFakeState()
	st.states[key("digestUser", "pv1")] = store.BackoffState{FirstNotifiedAt: anchor}
	rem := &fakeReminders{}

	sw := newSweep(t, scheduler.SweepConfig{
		Lister:    &fakeLister{items: []obligation.Outstanding{{UserID: "digestUser", PolicyVersionID: "pv1"}}},
		State:     st,
		Reminders: rem,
		Escalator: &fakeEscalator{},
		Cadence:   &fakeCadence{batch: map[string]notifpolicy.Cadence{"digestUser": notifpolicy.CadenceDaily}},
		Managers:  &fakeManagers{},
		Backoff:   scheduler.BackoffConfig{EscalationAfter: 30 * 24 * time.Hour},
	}, now)

	if err := sw.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rem.sent) != 0 {
		t.Errorf("digest user got %d immediate reminders; want 0 (deferred to digest)", len(rem.sent))
	}
	if st.reminders[key("digestUser", "pv1")] != 0 {
		t.Errorf("digest user immediate back-off advanced; want 0")
	}
}

// TestSweepEscalationRecordedWithoutManager verifies that when no manager is
// resolvable, the escalation is still recorded (schedule stops) but no send
// occurs — the interim posture until an identity manager RPC exists.
func TestSweepEscalationRecordedWithoutManager(t *testing.T) {
	ctx := context.Background()
	anchor := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	now := anchor.Add(40 * 24 * time.Hour)

	st := newFakeState()
	st.states[key("u1", "pv1")] = store.BackoffState{FirstNotifiedAt: anchor}
	esc := &fakeEscalator{}
	mgr := &fakeManagers{ok: false}

	sw := newSweep(t, scheduler.SweepConfig{
		Lister:    &fakeLister{items: []obligation.Outstanding{{UserID: "u1", PolicyVersionID: "pv1"}}},
		State:     st,
		Reminders: &fakeReminders{},
		Escalator: esc,
		Cadence:   &fakeCadence{},
		Managers:  mgr,
		Backoff:   scheduler.BackoffConfig{EscalationAfter: 30 * 24 * time.Hour},
	}, now)

	if err := sw.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(esc.sent) != 0 {
		t.Errorf("escalation sent without manager = %d; want 0", len(esc.sent))
	}
	if !st.escalated[key("u1", "pv1")] {
		t.Error("escalation not recorded; schedule would keep pestering the user")
	}
}
