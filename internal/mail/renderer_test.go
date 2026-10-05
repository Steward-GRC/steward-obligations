// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

func TestSidecarRendererRendersFromSidecarResponse(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"subject": "Your policy is due",
			"html":    "<p>Please acknowledge policy v1.</p>",
		})
	}))
	defer server.Close()

	r := mail.NewSidecarRenderer(server.URL, nil)
	rendered, err := r.Render(context.Background(), "ack-reminder", map[string]any{"policy_version_id": "pv1"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if rendered.Subject != "Your policy is due" {
		t.Errorf("Subject: got %q", rendered.Subject)
	}
	if rendered.HTML != "<p>Please acknowledge policy v1.</p>" {
		t.Errorf("HTML: got %q", rendered.HTML)
	}
	if rendered.Text != "" {
		t.Errorf("Text: expected empty, got %q", rendered.Text)
	}

	if gotBody["kind"] != "ack-reminder" {
		t.Errorf("request kind: got %v", gotBody["kind"])
	}
	vars, ok := gotBody["vars"].(map[string]any)
	if !ok {
		t.Fatalf("request vars: missing or wrong type: %+v", gotBody)
	}
	if vars["policy_version_id"] != "pv1" {
		t.Errorf("request vars.policy_version_id: got %v", vars["policy_version_id"])
	}
}

func TestSidecarRendererReturnsErrorOn500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("template not found"))
	}))
	defer server.Close()

	r := mail.NewSidecarRenderer(server.URL, nil)
	_, err := r.Render(context.Background(), "ack-reminder", nil)
	if err == nil {
		t.Fatal("expected error on sidecar 500, got nil (no plaintext fallback allowed)")
	}
}

func TestSidecarRendererRespectsContextCancellation(t *testing.T) {
	unblock := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"subject": "s", "html": "h"})
	}))
	defer func() {
		close(unblock)
		server.Close()
	}()

	r := mail.NewSidecarRenderer(server.URL, &http.Client{Timeout: 5 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := r.Render(ctx, "ack-reminder", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from canceled context, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Render did not respect context cancellation (hung)")
	}
}
