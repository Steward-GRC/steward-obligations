// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// renderHoldSender builds a Sender whose sidecar URL is sidecarURL and whose
// outbox is ob, with no pause (the transport is healthy).
func renderHoldSender(t *testing.T, sidecarURL string, ob mail.OutboxWriter) (*mail.Sender, *fakeTransport) {
	t.Helper()
	ft := &fakeTransport{}
	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost:   "smtp.example.com",
		SMTPPort:   "587",
		From:       "sender@example.org",
		SidecarURL: sidecarURL,
		AppEnv:     "prod",
	}, mail.WithTransport(ft), mail.WithPauseGate(&fakeDecider{pause: false}, ob))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return s, ft
}

// A sidecar that answers 500 holds the email: the inputs go to the outbox, the
// send reports paused (so the consumer acks it), and nothing is delivered.
func TestSend_sidecar500HoldsTheEmailInTheOutbox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ob := &fakeOutbox{}
	s, ft := renderHoldSender(t, srv.URL, ob)

	err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", map[string]any{"policy_version_id": "pv1"})
	if !mail.IsPaused(err) {
		t.Fatalf("want a paused (held) result, got %v", err)
	}
	if len(ob.items) != 1 {
		t.Fatalf("want 1 held row, got %d", len(ob.items))
	}
	it := ob.items[0]
	if it.Kind != "ack-reminder" || it.Recipient != "user@example.com" || it.DedupKey != "u1:ack-reminder:pv1" {
		t.Fatalf("held row lost its identity: %+v", it)
	}
	if len(it.RenderVars) == 0 || it.HTML != "" {
		t.Fatalf("want render inputs and no rendered body, got vars=%q html=%q", it.RenderVars, it.HTML)
	}
	if ft.callCount() != 0 {
		t.Fatalf("nothing may be delivered, got %d transport calls", ft.callCount())
	}
}

// A sidecar that refuses the connection holds the email the same way.
func TestSend_sidecarDownHoldsTheEmailInTheOutbox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // closed port
	ob := &fakeOutbox{}
	s, _ := renderHoldSender(t, url, ob)

	err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", map[string]any{"k": "v"})
	if !mail.IsPaused(err) || len(ob.items) != 1 {
		t.Fatalf("want held, got err=%v rows=%d", err, len(ob.items))
	}
}

// A sidecar 4xx is the caller's bug (bad kind or vars), not an outage: it is
// not held.
func TestSend_sidecar4xxIsNotHeld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	ob := &fakeOutbox{}
	s, _ := renderHoldSender(t, srv.URL, ob)

	err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", map[string]any{})
	if err == nil || mail.IsPaused(err) || len(ob.items) != 0 {
		t.Fatalf("a 4xx must fail and not be held: err=%v rows=%d", err, len(ob.items))
	}
}

// Once the sidecar answers, replaying the held row renders it and delivers it.
func TestSendRendered_rendersHeldInputsAfterTheSidecarRecovers(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subject":"Your policy is due","html":"<p>hi</p>"}`))
	}))
	defer srv.Close()
	ob := &fakeOutbox{}
	s, ft := renderHoldSender(t, srv.URL, ob)

	if err := s.Send(context.Background(), "ack-reminder", "u1", "user@example.com", "pv1", map[string]any{"k": "v"}); !mail.IsPaused(err) {
		t.Fatalf("want held, got %v", err)
	}
	held := ob.items[0]

	// Still down: the replay fails with a transient error so the drainer retries.
	if err := s.SendRendered(context.Background(), held); err == nil || mail.IsPaused(err) {
		t.Fatalf("replay while the sidecar is down must fail transiently, got %v", err)
	}
	if ft.callCount() != 0 {
		t.Fatalf("nothing may be delivered yet")
	}

	up.Store(true)
	if err := s.SendRendered(context.Background(), held); err != nil {
		t.Fatalf("replay after recovery: %v", err)
	}
	sent := ft.sent()
	if len(sent) != 1 || sent[0].Subject != "Your policy is due" || sent[0].HTML != "<p>hi</p>" {
		t.Fatalf("want the rendered email delivered once, got %+v", sent)
	}
	if got := sent[0].Recipients(); len(got) != 1 || got[0] != "user@example.com" {
		t.Fatalf("recipients: %v", got)
	}
}
