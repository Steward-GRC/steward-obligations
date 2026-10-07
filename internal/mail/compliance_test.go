// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	email "github.com/Bugs5382/go-email"

	"github.com/Steward-GRC/steward-obligations/internal/audit"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// fakePrefSource reports emails-off for the userIDs listed in off; every
// other userID (including one never mentioned, i.e. "no explicit
// preference") is treated as enabled, matching the real NotifPref store's
// default-on/opt-out semantics.
type fakePrefSource struct {
	off map[string]bool
	err error
}

func (f fakePrefSource) EmailEnabled(_ context.Context, userID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return !f.off[userID], nil
}

// fakeQuietHours reports quiet-hours for the userIDs listed in quiet; every
// other userID is outside quiet hours.
type fakeQuietHours map[string]bool

func (f fakeQuietHours) InWindow(_ context.Context, userID string) bool {
	return f[userID]
}

// fakeAuditEmitter is a mail.AuditEmitter stub that records every event
// instead of publishing to RabbitMQ.
type fakeAuditEmitter struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeAuditEmitter) Emit(_ context.Context, ev audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeAuditEmitter) recorded() []audit.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]audit.Event(nil), f.events...)
}

// TestSenderSuppressesWhenEmailPrefOff covers the Suppressor honoring an
// emails-off preference: the send is skipped (ErrSuppressed), not delivered,
// and not audited as a send.
func TestSenderSuppressesWhenEmailPrefOff(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}
	emitter := &fakeAuditEmitter{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithRecorder(mail.NewAuditRecorder(emitter)),
		mail.WithSuppressor(fakePrefSource{off: map[string]bool{"u1": true}}, fakeQuietHours{}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	err = s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil)
	if !errors.Is(err, email.ErrSuppressed) {
		t.Fatalf("Send: got err %v, want wrapped email.ErrSuppressed", err)
	}
	if got := len(ft.sent()); got != 0 {
		t.Errorf("expected no delivery for a suppressed send, got %d", got)
	}
	if got := len(emitter.recorded()); got != 0 {
		t.Errorf("expected no audit record for a suppressed send, got %d", got)
	}
}

// TestSenderSuppressesDuringQuietHours covers the Suppressor's other trigger:
// a recipient in quiet hours, independent of their email preference.
func TestSenderSuppressesDuringQuietHours(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithSuppressor(fakePrefSource{}, fakeQuietHours{"u1": true}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	err = s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil)
	if !errors.Is(err, email.ErrSuppressed) {
		t.Fatalf("Send: got err %v, want wrapped email.ErrSuppressed", err)
	}
	if got := len(ft.sent()); got != 0 {
		t.Errorf("expected no delivery during quiet hours, got %d", got)
	}
}

// TestSenderWelcomeAccountBypassesSuppression covers the one exception: kind
// welcome-account always sends, even when the recipient has emails off AND
// is in quiet hours.
func TestSenderWelcomeAccountBypassesSuppression(t *testing.T) {
	sidecar := newStubSidecar(t, "Welcome", "<p>hi</p>")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithSuppressor(fakePrefSource{off: map[string]bool{"u1": true}}, fakeQuietHours{"u1": true}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "welcome-account", "u1", "user@example.com", "acct1", nil); err != nil {
		t.Fatalf("Send: expected welcome-account to bypass suppression, got err: %v", err)
	}
	if got := len(ft.sent()); got != 1 {
		t.Errorf("expected welcome-account to be delivered despite off pref + quiet hours, got %d sent", got)
	}
}

// TestSenderSSOAccountWelcomeBypassesSuppression covers SSO
// lifecycle consumer: kind sso-account-welcome (a JIT-provisioned account's
// first email) always sends, exactly like welcome-account.
func TestSenderSSOAccountWelcomeBypassesSuppression(t *testing.T) {
	sidecar := newStubSidecar(t, "Welcome", "<p>hi</p>")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithSuppressor(fakePrefSource{off: map[string]bool{"u1": true}}, fakeQuietHours{"u1": true}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "sso-account-welcome", "u1", "user@example.com", "sso1", nil); err != nil {
		t.Fatalf("Send: expected sso-account-welcome to bypass suppression, got err: %v", err)
	}
	if got := len(ft.sent()); got != 1 {
		t.Errorf("expected sso-account-welcome to be delivered despite off pref + quiet hours, got %d sent", got)
	}
}

// TestSenderBreakGlassAlertBypassesSuppression covers non-optional
// security alert: kind break-glass-alert always sends, regardless of the
// recipient's email preference or quiet hours -- a break-glass login notice
// must never be silently dropped.
func TestSenderBreakGlassAlertBypassesSuppression(t *testing.T) {
	sidecar := newStubSidecar(t, "Break Glass", "<p>alert</p>")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithSuppressor(fakePrefSource{off: map[string]bool{"admin1": true}}, fakeQuietHours{"admin1": true}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "break-glass-alert", "admin1", "admin@example.com", "break-glass:occ1:admin@example.com", nil); err != nil {
		t.Fatalf("Send: expected break-glass-alert to bypass suppression, got err: %v", err)
	}
	if got := len(ft.sent()); got != 1 {
		t.Errorf("expected break-glass-alert to be delivered despite off pref + quiet hours, got %d sent", got)
	}
}

// TestSenderBreakGlassReadAlertBypassesSuppression: the alert for a document
// read under a break-glass grant always sends, like the break-glass login
// alert.
func TestSenderBreakGlassReadAlertBypassesSuppression(t *testing.T) {
	sidecar := newStubSidecar(t, "Break Glass Read", "<p>alert</p>")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithSuppressor(fakePrefSource{off: map[string]bool{"owner1": true}}, fakeQuietHours{"owner1": true}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "break-glass-read-alert", "owner1", "owner@example.com", "break-glass-read:ev1:owner@example.com", nil); err != nil {
		t.Fatalf("Send: expected break-glass-read-alert to bypass suppression, got err: %v", err)
	}
	if got := len(ft.sent()); got != 1 {
		t.Errorf("expected break-glass-read-alert to be delivered despite off pref + quiet hours, got %d sent", got)
	}
}

// TestSenderNoPrefUserSends covers the default-on/opt-out rule: a userID the
// PrefSource has no explicit entry for is treated as enabled.
func TestSenderNoPrefUserSends(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithSuppressor(fakePrefSource{}, fakeQuietHours{}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "policy-ack-reminder", "u-never-set-a-pref", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("Send: expected default-on user to send, got err: %v", err)
	}
	if got := len(ft.sent()); got != 1 {
		t.Errorf("expected 1 delivered message for a no-pref (default-on) user, got %d", got)
	}
}

// TestSenderDedupeSkipsRepeatWithinWindow covers the Deduper: a second Send
// call with the same (userID, kind, dedupRef) is skipped -- delivered once,
// no error on the second call.
func TestSenderDedupeSkipsRepeatWithinWindow(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	}, mail.WithTransport(ft), mail.WithDeduper(mail.NewTTLDeduper(mail.DefaultDedupWindow)))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if err := s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("second (deduped) Send: expected a silent skip (nil error), got: %v", err)
	}

	if got := len(ft.sent()); got != 1 {
		t.Errorf("expected exactly 1 delivered message for two identical sends, got %d", got)
	}
}

// TestSenderDedupeAllowsDifferentKey covers the negative case: a different
// dedupRef (or userID, or kind) is a different key and always sends.
func TestSenderDedupeAllowsDifferentKey(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if err := s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv2", nil); err != nil {
		t.Fatalf("second Send (different dedupRef): %v", err)
	}

	if got := len(ft.sent()); got != 2 {
		t.Errorf("expected 2 delivered messages for two different dedup keys, got %d", got)
	}
}

// TestSenderRecorderRecordsSuccessfulSend covers the Recorder: a successful
// send produces exactly one audit record carrying kind/recipient/message id.
func TestSenderRecorderRecordsSuccessfulSend(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}
	emitter := &fakeAuditEmitter{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	}, mail.WithTransport(ft), mail.WithRecorder(mail.NewAuditRecorder(emitter)))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	events := emitter.recorded()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit record for 1 successful send, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "email.sent" {
		t.Errorf("Action: got %q, want email.sent", ev.Action)
	}
	if ev.ActorUserID != "u1" {
		t.Errorf("ActorUserID: got %q, want u1", ev.ActorUserID)
	}
	if ev.Attributes["kind"] != "policy-ack-reminder" {
		t.Errorf("Attributes[kind]: got %q", ev.Attributes["kind"])
	}
	if ev.Attributes["recipient"] != "user@example.com" {
		t.Errorf("Attributes[recipient]: got %q", ev.Attributes["recipient"])
	}
	if ev.Attributes["message_id"] == "" {
		t.Error("Attributes[message_id]: expected a non-empty message id")
	}
}

// TestSenderRecorderSkipsSuppressedSend covers the flip side: a suppressed
// send must not produce an audit record at all (it was never sent).
func TestSenderRecorderSkipsSuppressedSend(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}
	emitter := &fakeAuditEmitter{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587", From: "sender@example.org",
		SidecarURL: sidecar.URL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithRecorder(mail.NewAuditRecorder(emitter)),
		mail.WithSuppressor(fakePrefSource{off: map[string]bool{"u1": true}}, fakeQuietHours{}),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "policy-ack-reminder", "u1", "user@example.com", "pv1", nil); !errors.Is(err, email.ErrSuppressed) {
		t.Fatalf("Send: got %v, want wrapped email.ErrSuppressed", err)
	}
	if got := len(emitter.recorded()); got != 0 {
		t.Errorf("expected no audit record for a suppressed send, got %d", got)
	}
}
