// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// fakePrefStore returns a single canned NotifPref regardless of the userID
// passed in; tests construct one per scenario to flip on/off the channels
// under test.
type fakePrefStore struct {
	pref store.NotifPref
	err  error
}

func (f *fakePrefStore) Get(_ context.Context, _ string) (store.NotifPref, error) {
	return f.pref, f.err
}

// recordingChannel implements notify.Channel by capturing every payload it
// receives, letting tests assert which channels the dispatcher actually
// invoked.
type recordingChannel struct {
	payloads []notify.AckReminderPayload
	err      error
}

func (r *recordingChannel) Send(_ context.Context, p notify.AckReminderPayload) error {
	r.payloads = append(r.payloads, p)
	return r.err
}

func TestDispatcherFansOutToEnabledChannels(t *testing.T) {
	pref := store.NotifPref{UserID: "u1", Email: true, InApp: true, Push: false}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	// Pin the clock to 12:00 so quiet hours never gate the assertions; the
	// quiet-hour suppression logic is exercised by a dedicated test below.
	d := notify.NewDispatcher(prefStore, email, inapp, push).
		WithClock(func() int { return 12 })
	d.SendAckReminder(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1", Type: "initial",
	})

	if len(email.payloads) != 1 {
		t.Errorf("email: expected 1, got %d", len(email.payloads))
	}
	if len(inapp.payloads) != 1 {
		t.Errorf("in_app: expected 1, got %d", len(inapp.payloads))
	}
	if len(push.payloads) != 0 {
		t.Errorf("push: expected 0 (disabled), got %d", len(push.payloads))
	}
}

func TestDispatcherSkipsAllWhenPrefLookupFails(t *testing.T) {
	prefStore := &fakePrefStore{err: errors.New("db down")}
	email := &recordingChannel{}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	d := notify.NewDispatcher(prefStore, email, inapp, push)
	d.SendAckReminder(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1",
	})

	if len(email.payloads)+len(inapp.payloads)+len(push.payloads) != 0 {
		t.Errorf("expected no channel sends when pref lookup fails")
	}
}

func TestDispatcherSendEscalationRoutesToManager(t *testing.T) {
	pref := store.NotifPref{UserID: "mgr", Email: true, InApp: true, Push: true}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	d := notify.NewDispatcher(prefStore, email, inapp, push)
	// Force non-quiet hours so all three channels fire deterministically.
	d = d.WithTZResolver(fixedTZResolver{tz: "UTC"})
	d = d.WithClock(func() int { return 12 })

	d.SendEscalationToManagerExplicit(context.Background(), notify.EscalationPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1", ManagerUserID: "mgr",
	})

	for name, ch := range map[string]*recordingChannel{"email": email, "inapp": inapp, "push": push} {
		if len(ch.payloads) != 1 {
			t.Fatalf("%s: expected 1 escalation payload, got %d", name, len(ch.payloads))
		}
		if ch.payloads[0].UserID != "mgr" {
			t.Errorf("%s: escalation went to %q, expected manager", name, ch.payloads[0].UserID)
		}
		if ch.payloads[0].Type != "escalation" {
			t.Errorf("%s: payload type %q, expected escalation", name, ch.payloads[0].Type)
		}
	}
}

func TestDispatcherSendWorkflowEscalation(t *testing.T) {
	pref := store.NotifPref{UserID: "appr", Email: true, InApp: true, Push: false}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	d := notify.NewDispatcher(prefStore, email, inapp, push).
		WithTZResolver(fixedTZResolver{tz: "UTC"}).
		WithClock(func() int { return 12 })

	d.SendWorkflowEscalation(context.Background(), notify.WorkflowEscalationPayload{
		ApproverUserID: "appr", PolicyVersionID: "pv1",
		WorkflowRunID: "wr1", StageID: "s1",
	})

	if len(email.payloads) != 1 {
		t.Errorf("email: expected 1, got %d", len(email.payloads))
	}
	if len(inapp.payloads) != 1 {
		t.Errorf("in_app: expected 1, got %d", len(inapp.payloads))
	}
	if len(push.payloads) != 0 {
		t.Errorf("push: expected 0 (disabled), got %d", len(push.payloads))
	}
	if got := email.payloads[0].CampaignID; got != "wr1" {
		t.Errorf("CampaignID propagated from WorkflowRunID: got %q", got)
	}
	if got := email.payloads[0].Type; got != "workflow_escalation" {
		t.Errorf("Type: got %q, expected workflow_escalation", got)
	}
}

// fixedTZResolver always returns the same timezone string; lets quiet-hour
// tests be deterministic regardless of the host's local tz.
type fixedTZResolver struct{ tz string }

func (f fixedTZResolver) ResolveTimezone(_ context.Context, _ string) (string, error) {
	return f.tz, nil
}

// errTZResolver simulates an unresolvable timezone (Identity unavailable or the
// user has no zone set); InWindow must fall back to UTC.
type errTZResolver struct{}

func (errTZResolver) ResolveTimezone(_ context.Context, _ string) (string, error) {
	return "", context.DeadlineExceeded
}

// TestQuietHoursEvaluatesInResolvedLocalZone is the slice-9
// property: quiet hours (22:00–07:00) are evaluated in the user's REAL resolved
// timezone, not UTC. At a fixed instant of 03:00 UTC the same moment is inside
// the quiet window for a US-Eastern user (22:00/23:00 local, previous day) but
// OUTSIDE it for a Tokyo user (12:00 local) — driven entirely by the tz the
// resolver returns. An empty/unresolvable zone falls back to UTC.
func TestQuietHoursEvaluatesInResolvedLocalZone(t *testing.T) {
	// 2026-08-24 03:00:00 UTC. EDT = UTC-4 → 23:00 (in window). JST = UTC+9 → 12:00 (out).
	at := time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return at }

	eastern := notify.QuietHours{Resolver: fixedTZResolver{tz: "America/New_York"}, NowFn: nowFn}
	if !eastern.InWindow(context.Background(), "u-east") {
		t.Fatalf("03:00 UTC is 23:00 America/New_York — must be IN quiet hours")
	}

	tokyo := notify.QuietHours{Resolver: fixedTZResolver{tz: "Asia/Tokyo"}, NowFn: nowFn}
	if tokyo.InWindow(context.Background(), "u-jp") {
		t.Fatalf("03:00 UTC is 12:00 Asia/Tokyo — must be OUT of quiet hours")
	}

	// UTC baseline at the same instant: 03:00 UTC is in the window.
	utc := notify.QuietHours{Resolver: fixedTZResolver{tz: "UTC"}, NowFn: nowFn}
	if !utc.InWindow(context.Background(), "u-utc") {
		t.Fatalf("03:00 UTC must be IN quiet hours for a UTC user")
	}

	// Fallback: an unresolvable zone degrades to UTC (03:00 → in window), i.e.
	// no regression versus today's UTC-only behavior.
	fallback := notify.QuietHours{Resolver: errTZResolver{}, NowFn: nowFn}
	if !fallback.InWindow(context.Background(), "u-err") {
		t.Fatalf("unresolvable tz must fall back to UTC (03:00 → in quiet hours)")
	}
	// And the same fallback at 12:00 UTC is OUT of the window (proves it's really
	// UTC math, not an always-true stub).
	noonFallback := notify.QuietHours{Resolver: errTZResolver{}, NowFn: func() time.Time {
		return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	}}
	if noonFallback.InWindow(context.Background(), "u-err2") {
		t.Fatalf("12:00 UTC fallback must be OUT of quiet hours")
	}
}

func TestDispatcherQuietHoursSuppressesEmailAndPushButNotInApp(t *testing.T) {
	pref := store.NotifPref{UserID: "u1", Email: true, InApp: true, Push: true}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	// 23:00 UTC is in quiet hours (22:00–07:00).
	d := notify.NewDispatcher(prefStore, email, inapp, push).
		WithTZResolver(fixedTZResolver{tz: "UTC"}).
		WithClock(func() int { return 23 })

	d.SendAckReminder(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1",
	})

	if len(inapp.payloads) != 1 {
		t.Errorf("in_app: expected 1 (always delivered), got %d", len(inapp.payloads))
	}
	if len(email.payloads) != 0 {
		t.Errorf("email: expected 0 in quiet hours, got %d", len(email.payloads))
	}
	if len(push.payloads) != 0 {
		t.Errorf("push: expected 0 in quiet hours, got %d", len(push.payloads))
	}
}

func TestSendPolicyPublishedHonorsEmailPrefAndQuietHours(t *testing.T) {
	prefs := &fakePrefStore{pref: store.NotifPref{InApp: true, Email: true, Push: false}}
	email := &recordingChannel{}
	inApp := &recordingChannel{}
	push := &recordingChannel{}

	// Outside quiet hours: email + in-app both fire.
	d := notify.NewDispatcher(prefs, email, inApp, push).WithClock(func() int { return 12 })
	d.SendPolicyPublished(context.Background(), notify.AckReminderPayload{UserID: "u1", PolicyVersionID: "pv1", Type: "policy_published"})
	if len(email.payloads) != 1 || len(inApp.payloads) != 1 {
		t.Fatalf("outside quiet hours: want email=1 inApp=1, got email=%d inApp=%d", len(email.payloads), len(inApp.payloads))
	}
	if email.payloads[0].Type != "policy_published" {
		t.Errorf("email payload Type: got %q, want policy_published", email.payloads[0].Type)
	}

	// In quiet hours: in-app still fires, email is suppressed.
	email2 := &recordingChannel{}
	inApp2 := &recordingChannel{}
	dq := notify.NewDispatcher(prefs, email2, inApp2, push).WithClock(func() int { return 23 })
	dq.SendPolicyPublished(context.Background(), notify.AckReminderPayload{UserID: "u1", PolicyVersionID: "pv1", Type: "policy_published"})
	if len(email2.payloads) != 0 {
		t.Errorf("in quiet hours: want email suppressed, got %d", len(email2.payloads))
	}
	if len(inApp2.payloads) != 1 {
		t.Errorf("in quiet hours: want in-app delivered, got %d", len(inApp2.payloads))
	}
}

func TestDispatcherOutsideQuietHoursDeliversAll(t *testing.T) {
	pref := store.NotifPref{UserID: "u1", Email: true, InApp: true, Push: true}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	d := notify.NewDispatcher(prefStore, email, inapp, push).
		WithTZResolver(fixedTZResolver{tz: "UTC"}).
		WithClock(func() int { return 9 })

	d.SendAckReminder(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1",
	})

	if len(email.payloads) != 1 || len(inapp.payloads) != 1 || len(push.payloads) != 1 {
		t.Errorf("expected 1/1/1 sends (email/inapp/push), got %d/%d/%d",
			len(email.payloads), len(inapp.payloads), len(push.payloads))
	}
}

// dispatcherResolverReader is an in-memory notifpolicy.PrefReader for the
// resolver-wired dispatcher tests.
type dispatcherResolverReader struct {
	channels notifpolicy.Channels
	cat      map[notifpolicy.Category]notifpolicy.Cadence
}

func (r dispatcherResolverReader) Channels(context.Context, string) (notifpolicy.Channels, error) {
	return r.channels, nil
}
func (r dispatcherResolverReader) CategoryCadence(_ context.Context, _ string, c notifpolicy.Category) (notifpolicy.Cadence, bool, error) {
	v, ok := r.cat[c]
	return v, ok, nil
}
func (r dispatcherResolverReader) TypeOverride(context.Context, string, string) (notifpolicy.Cadence, bool, error) {
	return notifpolicy.CadenceUnspecified, false, nil
}

type neverQuiet struct{}

func (neverQuiet) InWindow(context.Context, string) bool { return false }

// TestDispatcherResolverSuppressesOptionalOff proves the wired resolver
// suppresses every channel when the user turns an optional category off.
func TestDispatcherResolverSuppressesOptionalOff(t *testing.T) {
	reader := dispatcherResolverReader{
		channels: notifpolicy.Channels{Email: true, InApp: true, Push: true},
		cat:      map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryInformational: notifpolicy.CadenceOff},
	}
	res := notifpolicy.NewResolver(reader, neverQuiet{})
	email, inapp, push := &recordingChannel{}, &recordingChannel{}, &recordingChannel{}
	d := notify.NewDispatcher(nil, email, inapp, push).WithResolver(res)

	d.SendPolicyPublished(context.Background(), notify.AckReminderPayload{UserID: "u1", Type: "policy_published"})

	if len(email.payloads)+len(inapp.payloads)+len(push.payloads) != 0 {
		t.Fatalf("informational off must suppress all channels; got email=%d inapp=%d push=%d",
			len(email.payloads), len(inapp.payloads), len(push.payloads))
	}
}

// TestDispatcherResolverMandatoryStillSends proves a wired resolver keeps
// mandatory compliance mail flowing even when the user set compliance off (the
// floor clamps to daily → still delivered).
func TestDispatcherResolverMandatoryStillSends(t *testing.T) {
	reader := dispatcherResolverReader{
		channels: notifpolicy.Channels{Email: true, InApp: true, Push: false},
		cat:      map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryCompliance: notifpolicy.CadenceOff},
	}
	res := notifpolicy.NewResolver(reader, neverQuiet{})
	email, inapp, push := &recordingChannel{}, &recordingChannel{}, &recordingChannel{}
	d := notify.NewDispatcher(nil, email, inapp, push).WithResolver(res)

	d.SendAckReminder(context.Background(), notify.AckReminderPayload{UserID: "u1", Type: "reminder"})

	if len(email.payloads) != 1 || len(inapp.payloads) != 1 {
		t.Fatalf("mandatory compliance must still send email+in-app; got email=%d inapp=%d",
			len(email.payloads), len(inapp.payloads))
	}
}

// recordingBatch captures BatchItems written to the digest outbox.
type recordingBatch struct {
	items []notify.BatchItem
	err   error
}

func (r *recordingBatch) WriteBatch(_ context.Context, it notify.BatchItem) error {
	if r.err != nil {
		return r.err
	}
	r.items = append(r.items, it)
	return nil
}

// TestDispatcherBatchWritesOutbox proves a mode=batch decision (informational
// set to a daily digest) is written to the outbox instead of sending, carrying
// the routing keys and window the drain needs.
func TestDispatcherBatchWritesOutbox(t *testing.T) {
	reader := dispatcherResolverReader{
		channels: notifpolicy.Channels{Email: true, InApp: true, Push: false},
		cat:      map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryInformational: notifpolicy.CadenceDaily},
	}
	res := notifpolicy.NewResolver(reader, neverQuiet{})
	email, inapp, push := &recordingChannel{}, &recordingChannel{}, &recordingChannel{}
	batch := &recordingBatch{}
	d := notify.NewDispatcher(nil, email, inapp, push).WithResolver(res).WithOutbox(batch)

	d.SendPolicyPublished(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", Type: "policy_published", EffectiveDate: "September 1, 2026",
		Policies: []notify.AckItem{{Ref: "POL-9", Title: "New Policy", AckURL: "https://p/pv1"}},
	})

	if got := len(email.payloads) + len(inapp.payloads) + len(push.payloads); got != 0 {
		t.Fatalf("batch decision must not send immediately; got %d channel sends", got)
	}
	if len(batch.items) != 1 {
		t.Fatalf("expected 1 outbox write, got %d", len(batch.items))
	}
	it := batch.items[0]
	if it.Kind != "policy-published" || it.Category != "informational" || it.WindowKind != "daily" {
		t.Errorf("unexpected batch routing: %+v", it)
	}
	if it.DigestKey != "u1:informational:daily" || it.DedupRef != "pv1" {
		t.Errorf("unexpected digest key/dedup: key=%q dedup=%q", it.DigestKey, it.DedupRef)
	}
	if it.Vars["title"] != "New Policy" || it.Vars["meta"] != "Effective September 1, 2026" {
		t.Errorf("unexpected batch vars: %+v", it.Vars)
	}
}

// TestDispatcherBatchFallbackOnOutboxError proves an outbox write failure falls
// back to an immediate send so a batched notice is never lost.
func TestDispatcherBatchFallbackOnOutboxError(t *testing.T) {
	reader := dispatcherResolverReader{
		channels: notifpolicy.Channels{Email: true, InApp: true, Push: false},
		cat:      map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryInformational: notifpolicy.CadenceDaily},
	}
	res := notifpolicy.NewResolver(reader, neverQuiet{})
	email, inapp, push := &recordingChannel{}, &recordingChannel{}, &recordingChannel{}
	d := notify.NewDispatcher(nil, email, inapp, push).WithResolver(res).WithOutbox(&recordingBatch{err: errors.New("db down")})

	d.SendPolicyPublished(context.Background(), notify.AckReminderPayload{UserID: "u1", Type: "policy_published"})

	if len(email.payloads) != 1 || len(inapp.payloads) != 1 {
		t.Fatalf("outbox failure must fall back to immediate send; got email=%d inapp=%d",
			len(email.payloads), len(inapp.payloads))
	}
}
