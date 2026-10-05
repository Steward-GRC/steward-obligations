// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notify"
)

type fakeFCMResolver struct {
	token string
	err   error
}

func (f fakeFCMResolver) ResolveFCMToken(_ context.Context, _ string) (string, error) {
	return f.token, f.err
}

func TestPushChannelSendsFCMPayload(t *testing.T) {
	var got struct {
		Path string
		Body map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pc := notify.NewPushChannel("test-proj", fakeFCMResolver{token: "tok-1"}).
		WithEndpoint(server.URL + "/v1/projects/%s/messages:send")

	if err := pc.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(got.Path, "test-proj") {
		t.Errorf("project id not in URL: %q", got.Path)
	}
	msg, ok := got.Body["message"].(map[string]any)
	if !ok {
		t.Fatalf("missing message object: %+v", got.Body)
	}
	if msg["token"] != "tok-1" {
		t.Errorf("token: got %v", msg["token"])
	}
	data, ok := msg["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %+v", msg)
	}
	if data["policy_version_id"] != "pv1" {
		t.Errorf("data.policy_version_id: got %v", data["policy_version_id"])
	}
	if data["campaign_id"] != "c1" {
		t.Errorf("data.campaign_id: got %v", data["campaign_id"])
	}
}

func TestPushChannelSkipsWhenNoToken(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	pc := notify.NewPushChannel("p", fakeFCMResolver{token: ""}).
		WithEndpoint(server.URL + "/v1/projects/%s/messages:send")
	if err := pc.Send(context.Background(), notify.AckReminderPayload{UserID: "u1"}); err != nil {
		t.Errorf("Send: %v", err)
	}
	if called {
		t.Errorf("FCM endpoint should not be hit when token is empty")
	}
}

func TestPushChannelReturnsErrorOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	pc := notify.NewPushChannel("p", fakeFCMResolver{token: "tok"}).
		WithEndpoint(server.URL + "/v1/projects/%s/messages:send")
	if err := pc.Send(context.Background(), notify.AckReminderPayload{UserID: "u1"}); err == nil {
		t.Errorf("expected error on non-2xx response")
	}
}

func TestPushChannelPropagatesResolverError(t *testing.T) {
	pc := notify.NewPushChannel("p", fakeFCMResolver{err: errors.New("identity down")})
	if err := pc.Send(context.Background(), notify.AckReminderPayload{UserID: "u1"}); err == nil {
		t.Errorf("expected resolver error to propagate")
	}
}
