// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// ---------------------------------------------------------------------------
// Fakes (fakeObligation from policy_published_test.go already satisfies
// consumer.RetireAudienceResolver: AudienceUsers + PolicyDisplay).
// ---------------------------------------------------------------------------

// fakeRetireNotifier satisfies consumer.RetireNotifier and records every
// SendPolicyRetired call.
type fakeRetireNotifier struct {
	retired []notify.AckReminderPayload
	// deletions is intentionally never written: the retire consumer must NOT
	// delete acks, so any ack-deletion path would have to be a distinct call
	// that this notifier never exposes. The test asserts on the audience-notify
	// count only.
}

func (f *fakeRetireNotifier) SendPolicyRetired(_ context.Context, p notify.AckReminderPayload) {
	f.retired = append(f.retired, p)
}

// fakeRetiredTracker satisfies consumer.RetiredNotifiedTracker. seen records
// which (policy, user) pairs have already been notified; the first call for a
// pair returns true, later calls return false.
type fakeRetiredTracker struct {
	seen map[string]bool // key: policy+"|"+user
	err  error
}

func (f *fakeRetiredTracker) MarkNotifiedIfFirst(_ context.Context, policyID, userID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	key := policyID + "|" + userID
	if f.seen[key] {
		return false, nil
	}
	f.seen[key] = true
	return true, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func retiredBody(t *testing.T, policyID, number, title string) []byte {
	t.Helper()
	evt := map[string]any{
		"event_type": "policy.retired",
		"retired_at": time.Now().UTC(),
		"policy_id":  policyID,
		"number":     number,
		"title":      title,
	}
	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal test event: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPolicyRetiredConsumer_NotifiesFullAudienceOnce asserts that a
// policy.retired event notifies every RACI audience member exactly once, with
// the event's own human number/title stamped on the payload.
func TestPolicyRetiredConsumer_NotifiesFullAudienceOnce(t *testing.T) {
	const policyID = "pol-ret"
	obl := &fakeObligation{
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}, {ID: "u2"}, {ID: "u3"}},
		},
	}
	notifier := &fakeRetireNotifier{}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier)
	if err := c.Handle(context.Background(), retiredBody(t, policyID, "POL-7", "Legacy VPN Policy")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.retired) != 3 {
		t.Fatalf("expected 3 retire notices (full audience), got %d: %+v", len(notifier.retired), notifier.retired)
	}
	notified := make(map[string]bool)
	for _, p := range notifier.retired {
		notified[p.UserID] = true
		if p.Type != "policy_retired" {
			t.Errorf("payload Type: got %q, want policy_retired", p.Type)
		}
		if len(p.Policies) != 1 || p.Policies[0].Ref != "POL-7" || p.Policies[0].Title != "Legacy VPN Policy" {
			t.Errorf("payload should carry the event's human ref/title, got %+v", p.Policies)
		}
		if p.Policies[0].Ref == policyID {
			t.Error("ref must never be the raw policy id")
		}
	}
	for _, id := range []string{"u1", "u2", "u3"} {
		if !notified[id] {
			t.Errorf("expected %s to be notified", id)
		}
	}
}

// TestPolicyRetiredConsumer_StampsRetiredDate proves the retirement notice
// carries a real, human-readable date derived from the event's retired_at
// timestamp (never the "recently" fallback) so retiredVars renders the actual
// date in its "Retired" row.
func TestPolicyRetiredConsumer_StampsRetiredDate(t *testing.T) {
	const policyID = "pol-date"
	obl := &fakeObligation{
		audiences: map[string][]obligation.User{policyID: {{ID: "u1"}}},
	}
	notifier := &fakeRetireNotifier{}

	retiredAt := time.Date(2026, time.August, 25, 9, 15, 0, 0, time.UTC)
	evt := map[string]any{
		"event_type": "policy.retired",
		"retired_at": retiredAt,
		"policy_id":  policyID,
		"number":     "POL-7",
		"title":      "Legacy VPN Policy",
	}
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier)
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.retired) != 1 {
		t.Fatalf("expected 1 retire notice, got %d", len(notifier.retired))
	}
	if got := notifier.retired[0].EffectiveDate; got != "August 25, 2026" {
		t.Errorf("EffectiveDate: got %q, want %q (from event retired_at)", got, "August 25, 2026")
	}
}

// TestPolicyRetiredConsumer_FallsBackToPolicyDisplay verifies that when the
// event carries no number/title, the consumer best-effort resolves them via
// PolicyDisplay so the email shows a human reference, never the raw id.
func TestPolicyRetiredConsumer_FallsBackToPolicyDisplay(t *testing.T) {
	const policyID = "pol-nodisplay"
	obl := &fakeObligation{
		audiences: map[string][]obligation.User{policyID: {{ID: "u1"}}},
		displays: map[string]struct{ number, title string }{
			policyID: {number: "POL-42", title: "Retired Data Policy"},
		},
	}
	notifier := &fakeRetireNotifier{}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier)
	if err := c.Handle(context.Background(), retiredBody(t, policyID, "", "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.retired) != 1 {
		t.Fatalf("expected 1 retire notice, got %d", len(notifier.retired))
	}
	got := notifier.retired[0]
	if len(got.Policies) != 1 || got.Policies[0].Ref != "POL-42" || got.Policies[0].Title != "Retired Data Policy" {
		t.Fatalf("expected the display fallback ref/title, got %+v", got.Policies)
	}
}

// TestPolicyRetiredConsumer_Idempotent verifies that a second delivery of the
// same retire event notifies no one again (durable dedup via the tracker), and
// no ack rows are ever touched (the consumer has no ack store at all).
func TestPolicyRetiredConsumer_Idempotent(t *testing.T) {
	const policyID = "pol-idem"
	obl := &fakeObligation{
		audiences: map[string][]obligation.User{policyID: {{ID: "u1"}, {ID: "u2"}}},
	}
	notifier := &fakeRetireNotifier{}
	tracker := &fakeRetiredTracker{}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier).WithNotifiedTracker(tracker)

	// First delivery: both notified.
	if err := c.Handle(context.Background(), retiredBody(t, policyID, "POL-1", "T")); err != nil {
		t.Fatalf("Handle (first): %v", err)
	}
	if len(notifier.retired) != 2 {
		t.Fatalf("first delivery: want 2 notices, got %d", len(notifier.retired))
	}

	// Second delivery of the SAME event: no one re-notified.
	if err := c.Handle(context.Background(), retiredBody(t, policyID, "POL-1", "T")); err != nil {
		t.Fatalf("Handle (redelivery): %v", err)
	}
	if len(notifier.retired) != 2 {
		t.Fatalf("redelivery must not re-notify; want still 2, got %d", len(notifier.retired))
	}
}

// TestPolicyRetiredConsumer_TrackerErrorSendsAnyway verifies a tracker failure
// degrades to sending (a duplicate notice beats a missed one) rather than
// silently suppressing the audience.
func TestPolicyRetiredConsumer_TrackerErrorSendsAnyway(t *testing.T) {
	const policyID = "pol-trackererr"
	obl := &fakeObligation{
		audiences: map[string][]obligation.User{policyID: {{ID: "u1"}}},
	}
	notifier := &fakeRetireNotifier{}
	tracker := &fakeRetiredTracker{err: errors.New("db unavailable")}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier).WithNotifiedTracker(tracker)
	if err := c.Handle(context.Background(), retiredBody(t, policyID, "POL-1", "T")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(notifier.retired) != 1 || notifier.retired[0].UserID != "u1" {
		t.Fatalf("expected u1 still notified despite tracker error, got %+v", notifier.retired)
	}
}

// TestPolicyRetiredConsumer_FailLoudOnAudienceError verifies that when the RACI
// audience cannot be resolved the consumer RETURNS the error (message nacked)
// and notifies no one.
func TestPolicyRetiredConsumer_FailLoudOnAudienceError(t *testing.T) {
	const policyID = "pol-nochain"
	wantErr := errors.New("no category chain available")
	obl := &fakeObligation{
		audienceErr: map[string]error{policyID: wantErr},
	}
	notifier := &fakeRetireNotifier{}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier)
	err := c.Handle(context.Background(), retiredBody(t, policyID, "POL-1", "T"))
	if err == nil {
		t.Fatal("want error when RACI audience cannot be resolved; got nil")
	}
	if len(notifier.retired) != 0 {
		t.Errorf("expected 0 notices on audience error, got %d", len(notifier.retired))
	}
}

// TestPolicyRetiredConsumer_MissingPolicyIDErrors verifies an event with no
// policy_id is rejected (nacked) rather than silently dropped.
func TestPolicyRetiredConsumer_MissingPolicyIDErrors(t *testing.T) {
	obl := &fakeObligation{}
	notifier := &fakeRetireNotifier{}

	c := consumer.NewPolicyRetiredConsumer(obl, notifier)
	if err := c.Handle(context.Background(), retiredBody(t, "", "POL-1", "T")); err == nil {
		t.Fatal("want error for empty policy_id; got nil")
	}
	if len(notifier.retired) != 0 {
		t.Errorf("expected 0 notices for empty policy_id, got %d", len(notifier.retired))
	}
}
