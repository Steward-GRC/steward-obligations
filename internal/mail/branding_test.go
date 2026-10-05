// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

func capturingSidecar(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Vars map[string]any `json:"vars"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		*got = body.Vars
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"subject": "s", "html": "h"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSenderStampsTheAdopterBranding(t *testing.T) {
	var vars map[string]any
	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.org", SMTPPort: "587", SidecarURL: capturingSidecar(t, &vars).URL, AppEnv: "prod",
		LogoSrc: "https://policies.example.org/logo.png", ProductName: "Example Policies", LegalText: "Example Organisation, 1 Example Street",
	}, mail.WithTransport(&fakeTransport{}))
	require.NoError(t, err)
	require.NoError(t, s.Send(context.Background(), "ack-reminder", "u1", "erin@example.org", "pv1", map[string]any{}))
	require.Equal(t, "https://policies.example.org/logo.png", vars["logoSrc"])
	require.Equal(t, "Example Policies", vars["productName"])
	require.Equal(t, "Example Organisation, 1 Example Street", vars["legalText"])
}

func TestSenderLeavesBrandingOutWhenUnset(t *testing.T) {
	var vars map[string]any
	s, err := mail.NewSender(mail.SenderConfig{
		SMTPHost: "smtp.example.org", SMTPPort: "587", SidecarURL: capturingSidecar(t, &vars).URL, AppEnv: "prod",
	}, mail.WithTransport(&fakeTransport{}))
	require.NoError(t, err)
	require.NoError(t, s.Send(context.Background(), "ack-reminder", "u1", "erin@example.org", "pv1", map[string]any{"productName": "Kept"}))
	require.NotContains(t, vars, "logoSrc")
	require.NotContains(t, vars, "legalText")
	require.Equal(t, "Kept", vars["productName"], "a caller's own value wins")
}
