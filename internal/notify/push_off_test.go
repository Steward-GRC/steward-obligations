// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notify"
)

type countingTokens struct{ calls int }

func (c *countingTokens) ResolveFCMToken(context.Context, string) (string, error) {
	c.calls++
	return "token", nil
}

func TestPushIsOffWithoutAProject(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	tokens := &countingTokens{}
	ch := notify.NewPushChannel("", tokens).WithEndpoint(srv.URL + "/%s")
	if err := ch.Send(context.Background(), notify.AckReminderPayload{UserID: "u1", PolicyVersionID: "pv1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if hit || tokens.calls != 0 {
		t.Fatalf("push sent with no project: endpoint hit=%v, token lookups=%d", hit, tokens.calls)
	}
}
