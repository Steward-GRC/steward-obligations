// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	email "github.com/Bugs5382/go-email"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// fakeTransport is an email.Transport that captures every delivered Message
// instead of opening a TCP socket. failN controls how many leading Send
// calls return a retryable email.TransientError before it starts
// succeeding, so tests can exercise the Retry middleware.
type fakeTransport struct {
	mu       sync.Mutex
	messages []email.Message
	calls    int
	failN    int
}

func (f *fakeTransport) Send(_ context.Context, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.calls <= f.failN {
		return email.TransientError{Err: errors.New("simulated transient transport failure")}
	}
	f.messages = append(f.messages, m)
	return nil
}

func (f *fakeTransport) sent() []email.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]email.Message(nil), f.messages...)
}

func (f *fakeTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newStubSidecar returns an httptest server implementing the sidecar's
// /render contract: it always returns the given subject/html regardless of
// the request body.
func newStubSidecar(t *testing.T, subject, html string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			t.Errorf("unexpected sidecar path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"subject": subject, "html": html})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSenderSendDeliversBrandedMessage(t *testing.T) {
	sidecar := newStubSidecar(t, "Your policy is due", "<p>Please acknowledge policy v1.</p>")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost:   "smtp.example.com",
		SMTPPort:   "587",
		From:       "sender@example.org",
		SidecarURL: sidecar.URL,
		AppEnv:     "prod",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", map[string]any{"policy_version_id": "pv1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := ft.sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(sent))
	}
	m := sent[0]

	if got := m.Recipients(); len(got) != 1 || got[0] != "user@example.com" {
		t.Errorf("recipients: got %v, want [user@example.com]", got)
	}
	if m.From != "sender@example.org" {
		t.Errorf("From: got %q", m.From)
	}
	if m.Subject != "Your policy is due" {
		t.Errorf("Subject: got %q", m.Subject)
	}
	if m.HTML != "<p>Please acknowledge policy v1.</p>" {
		t.Errorf("HTML: got %q", m.HTML)
	}
}

func TestSenderSendAppliesTheFromDefaultWhenUnset(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost:   "smtp.example.com",
		SMTPPort:   "587",
		SidecarURL: sidecar.URL,
		AppEnv:     "prod",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := ft.sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(sent))
	}
	if sent[0].From != "no-reply@example.org" {
		t.Errorf("From: got %q, want the default", sent[0].From)
	}
}

func TestSenderDevCatchAllRewritesRecipients(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost:      "smtp.example.com",
		SMTPPort:      "587",
		From:          "sender@example.org",
		SidecarURL:    sidecar.URL,
		AppEnv:        "dev",
		DevCatchAllTo: "dev-catch-all@example.org",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := ft.sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 delivered message, got %d", len(sent))
	}
	m := sent[0]

	if got := m.Recipients(); len(got) != 1 || got[0] != "dev-catch-all@example.org" {
		t.Errorf("recipients: got %v, want [dev-catch-all@example.org]", got)
	}
	if got := m.Headers["X-Dev-Original-Recipients"]; got != "user@example.com" {
		t.Errorf("X-Dev-Original-Recipients: got %q, want %q", got, "user@example.com")
	}
}

func TestSenderRetriesTransientTransportError(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{failN: 1}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost:   "smtp.example.com",
		SMTPPort:   "587",
		From:       "sender@example.org",
		SidecarURL: sidecar.URL,
		AppEnv:     "prod",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", nil); err != nil {
		t.Fatalf("Send: expected retry to recover from transient failure, got error: %v", err)
	}

	if got := ft.callCount(); got != 2 {
		t.Errorf("transport call count: got %d, want 2 (1 failure + 1 retry)", got)
	}
	if len(ft.sent()) != 1 {
		t.Fatalf("expected 1 delivered message after retry, got %d", len(ft.sent()))
	}
}

func TestSenderSendRejectsInvalidRecipient(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "h")
	ft := &fakeTransport{}

	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost:   "smtp.example.com",
		SMTPPort:   "587",
		From:       "sender@example.org",
		SidecarURL: sidecar.URL,
		AppEnv:     "prod",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	if err := s.Send(context.Background(), "ack-reminder", "u1", "not-an-email", "pv1", nil); err == nil {
		t.Fatal("expected Validate middleware to reject an invalid recipient address")
	}
	if len(ft.sent()) != 0 {
		t.Errorf("expected no delivery for an invalid recipient, got %d", len(ft.sent()))
	}
}

func TestNewSenderRejectsInvalidPort(t *testing.T) {
	if _, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com",
		SMTPPort: "not-a-port",
	}); err == nil {
		t.Fatal("expected NewSender to reject a non-numeric SMTPPort")
	}
}

func TestBuildSMTPConfigRejectsInvalidPort(t *testing.T) {
	if _, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost: "smtp.example.com",
		SMTPPort: "not-a-port",
	}, "sender@example.org"); err == nil {
		t.Fatal("expected BuildSMTPConfig to reject a non-numeric SMTPPort")
	}
}

func TestBuildSMTPConfigNoPasswordLeavesUserEmpty(t *testing.T) {
	got, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost: "127.0.0.1",
		SMTPPort: "1025",
		AppEnv:   "dev",
	}, "no-reply@example.org")
	if err != nil {
		t.Fatalf("BuildSMTPConfig: %v", err)
	}
	if got.User != "" {
		t.Errorf("User: got %q, want empty (no password configured -- no AUTH attempt)", got.User)
	}
}

func TestBuildSMTPConfigPasswordWithUsernameAuthsAsUsername(t *testing.T) {
	got, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		SMTPUsername: "AKIA-relay-user",
		Password:     "relay-secret",
		SMTPStartTLS: true,
		AppEnv:       "prod",
	}, "sender@example.org")
	if err != nil {
		t.Fatalf("BuildSMTPConfig: %v", err)
	}
	if got.User != "AKIA-relay-user" {
		t.Errorf("User: got %q, want configured SMTPUsername", got.User)
	}
}

func TestBuildSMTPConfigPasswordWithoutUsernameAuthsAsFrom(t *testing.T) {
	got, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		Password:     "relay-secret",
		SMTPStartTLS: true,
		AppEnv:       "prod",
	}, "sender@example.org")
	if err != nil {
		t.Fatalf("BuildSMTPConfig: %v", err)
	}
	if got.User != "sender@example.org" {
		t.Errorf("User: got %q, want From (prior behavior preserved when SMTPUsername unset)", got.User)
	}
}

func TestBuildSMTPConfigDevForcesTLSOffRegardlessOfStartTLS(t *testing.T) {
	got, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost:     "127.0.0.1",
		SMTPPort:     "1025",
		SMTPStartTLS: true,
		AppEnv:       "dev",
	}, "no-reply@example.org")
	if err != nil {
		t.Fatalf("BuildSMTPConfig: %v", err)
	}
	if got.TLS {
		t.Error("TLS: got true, want false in dev regardless of SMTPStartTLS")
	}
}

func TestBuildSMTPConfigNonDevHonorsStartTLS(t *testing.T) {
	onCfg, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		Password:     "relay-secret",
		SMTPStartTLS: true,
		AppEnv:       "prod",
	}, "sender@example.org")
	if err != nil {
		t.Fatalf("BuildSMTPConfig: %v", err)
	}
	if !onCfg.TLS {
		t.Error("TLS: got false, want true outside dev when SMTPStartTLS is set")
	}

	offCfg, err := mail.BuildSMTPConfig(mail.SenderConfig{
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		Password:     "relay-secret",
		SMTPStartTLS: false,
		AppEnv:       "prod",
	}, "sender@example.org")
	if err != nil {
		t.Fatalf("BuildSMTPConfig: %v", err)
	}
	if offCfg.TLS {
		t.Error("TLS: got true, want false outside dev when SMTPStartTLS is unset")
	}
}
