// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	email "github.com/Bugs5382/go-email"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/Steward-GRC/steward-obligations/internal/notifytoken"
)

// recordingSidecar is a stub render sidecar that captures the vars of the most
// recent /render request so a test can assert on the values Send stamped in.
type recordingSidecar struct {
	mu   sync.Mutex
	vars map[string]any
}

func (r *recordingSidecar) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Vars map[string]any `json:"vars"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.vars = body.Vars
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"subject": "s", "html": "<p>h</p>"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *recordingSidecar) lastVars() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vars
}

const (
	testUnsubURL = "https://policy.example.org/notify/unsubscribe"
	testPrefsURL = "https://policy.example.org"
)

func newUnsubSender(t *testing.T, sidecarURL string, ft email.Transport, signer *notifytoken.Signer) *mail.Sender {
	t.Helper()
	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587",
		From: "sender@example.org", SidecarURL: sidecarURL, AppEnv: "prod",
	},
		mail.WithTransport(ft),
		mail.WithUnsubscribeLinks(signer, testUnsubURL, testPrefsURL, time.Hour),
	)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	return s
}

// tokenFromListUnsubscribe pulls the token query param out of a
// "<https://.../notify/unsubscribe?token=XYZ>" header value.
func tokenFromListUnsubscribe(t *testing.T, header string) string {
	t.Helper()
	trimmed := strings.TrimSuffix(strings.TrimPrefix(header, "<"), ">")
	u, err := url.Parse(trimmed)
	if err != nil {
		t.Fatalf("parse List-Unsubscribe %q: %v", header, err)
	}
	return u.Query().Get("token")
}

func TestSendOptionalCarriesListUnsubscribeAndPrefs(t *testing.T) {
	rec := &recordingSidecar{}
	sidecar := rec.server(t)
	ft := &fakeTransport{}
	signer, _ := notifytoken.NewSigner("unit-secret")
	s := newUnsubSender(t, sidecar.URL, ft, signer)

	// policy-published is an INFORMATIONAL (optional) kind.
	if err := s.Send(context.Background(), "policy-published", "user-42", "u@example.com", "ref", map[string]any{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := ft.sent()
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.ListUnsubscribe == "" {
		t.Fatal("optional send missing List-Unsubscribe header")
	}
	if m.ListUnsubscribePost != "List-Unsubscribe=One-Click" {
		t.Fatalf("want RFC 8058 one-click post, got %q", m.ListUnsubscribePost)
	}
	if !strings.HasPrefix(m.ListUnsubscribe, "<https://") || !strings.HasSuffix(m.ListUnsubscribe, ">") {
		t.Fatalf("List-Unsubscribe not angle-bracketed https URL: %q", m.ListUnsubscribe)
	}

	// The header's token must verify and name the recipient + category.
	tok := tokenFromListUnsubscribe(t, m.ListUnsubscribe)
	claims, err := signer.Verify(tok, notifytoken.PurposeUnsubscribe)
	if err != nil {
		t.Fatalf("verify unsubscribe token: %v", err)
	}
	if claims.UserID != "user-42" || claims.Category != "informational" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	// Both footer vars are stamped: signed preferences link + unsubscribe href.
	vars := rec.lastVars()
	prefs, _ := vars["preferencesUrl"].(string)
	if !strings.HasPrefix(prefs, testPrefsURL+"?token=") {
		t.Fatalf("preferencesUrl not a signed link: %q", prefs)
	}
	if href, _ := vars["unsubscribeHref"].(string); !strings.HasPrefix(href, testUnsubURL+"?token=") {
		t.Fatalf("unsubscribeHref not stamped: %q", vars["unsubscribeHref"])
	}
}

func TestSendMandatoryOmitsListUnsubscribeButKeepsPrefs(t *testing.T) {
	rec := &recordingSidecar{}
	sidecar := rec.server(t)
	ft := &fakeTransport{}
	signer, _ := notifytoken.NewSigner("unit-secret")
	s := newUnsubSender(t, sidecar.URL, ft, signer)

	// welcome-account is TRANSACTIONAL (mandatory) — no unsubscribe affordance.
	for _, kind := range []string{"welcome-account", "ack-required", "otp"} {
		ft.messages = nil
		if err := s.Send(context.Background(), kind, "user-9", "u@example.com", "ref", map[string]any{}); err != nil {
			t.Fatalf("Send %s: %v", kind, err)
		}
		m := ft.sent()[0]
		if m.ListUnsubscribe != "" || m.ListUnsubscribePost != "" {
			t.Fatalf("mandatory kind %s must not carry List-Unsubscribe headers: %q / %q", kind, m.ListUnsubscribe, m.ListUnsubscribePost)
		}
		vars := rec.lastVars()
		if prefs, _ := vars["preferencesUrl"].(string); !strings.HasPrefix(prefs, testPrefsURL+"?token=") {
			t.Fatalf("mandatory kind %s still gets a signed preferences link; got %q", kind, prefs)
		}
		if _, set := vars["unsubscribeHref"]; set {
			t.Fatalf("mandatory kind %s must not get an unsubscribeHref", kind)
		}
	}
}

func TestSendWithoutSignerAddsNoHeaders(t *testing.T) {
	sidecar := newStubSidecar(t, "s", "<p>h</p>")
	ft := &fakeTransport{}
	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.com", SMTPPort: "587",
		From: "sender@example.org", SidecarURL: sidecar.URL, AppEnv: "prod",
	}, mail.WithTransport(ft))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if err := s.Send(context.Background(), "policy-published", "user-1", "u@example.com", "ref", map[string]any{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if m := ft.sent()[0]; m.ListUnsubscribe != "" {
		t.Fatalf("no signer wired but List-Unsubscribe present: %q", m.ListUnsubscribe)
	}
}
