// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
)

type fakeWelcomeResolver struct {
	name, email string
	err         error
}

func (f *fakeWelcomeResolver) ResolveWelcomeRecipient(_ context.Context, _ string) (string, string, error) {
	return f.name, f.email, f.err
}

type capturedSend struct {
	kind, userID, to, dedupRef string
	vars                       any
}

type fakeWelcomeSender struct {
	last capturedSend
	err  error
}

func (f *fakeWelcomeSender) Send(_ context.Context, kind, userID, to, dedupRef string, vars any) error {
	f.last = capturedSend{kind: kind, userID: userID, to: to, dedupRef: dedupRef, vars: vars}
	return f.err
}

func TestResendWelcome_SendsWelcomeAccount(t *testing.T) {
	res := &fakeWelcomeResolver{name: "Erin Example", email: "erin@example.org"}
	snd := &fakeWelcomeSender{}
	h := grpcsvc.NewWelcomeHandler(res, snd, "https://policy.example.org/portal")

	resp, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: "u-1"})
	if err != nil {
		t.Fatalf("ResendWelcome: %v", err)
	}
	if !resp.GetSent() {
		t.Fatal("expected sent=true")
	}
	if snd.last.kind != "welcome-account" {
		t.Errorf("kind = %q, want welcome-account", snd.last.kind)
	}
	if snd.last.to != "erin@example.org" {
		t.Errorf("to = %q, want erin@example.org", snd.last.to)
	}
	if snd.last.userID != "u-1" {
		t.Errorf("userID = %q, want u-1", snd.last.userID)
	}
	vars, ok := snd.last.vars.(map[string]any)
	if !ok {
		t.Fatalf("vars type = %T, want map[string]any", snd.last.vars)
	}
	if vars["recipientName"] != "Erin Example" {
		t.Errorf("recipientName = %v, want Erin Example", vars["recipientName"])
	}
	if vars["accountUrl"] != "https://policy.example.org/portal" {
		t.Errorf("accountUrl = %v", vars["accountUrl"])
	}
}

func TestResendWelcome_FallsBackWhenNameEmpty(t *testing.T) {
	res := &fakeWelcomeResolver{name: "", email: "erin@example.org"}
	snd := &fakeWelcomeSender{}
	h := grpcsvc.NewWelcomeHandler(res, snd, "https://portal")

	if _, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: "u-1"}); err != nil {
		t.Fatalf("ResendWelcome: %v", err)
	}
	vars := snd.last.vars.(map[string]any)
	if vars["recipientName"] != "there" {
		t.Errorf("recipientName = %v, want fallback 'there'", vars["recipientName"])
	}
}

func TestResendWelcome_ValidatesUserID(t *testing.T) {
	h := grpcsvc.NewWelcomeHandler(&fakeWelcomeResolver{}, &fakeWelcomeSender{}, "https://portal")
	_, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestResendWelcome_NoEmailIsFailedPrecondition(t *testing.T) {
	h := grpcsvc.NewWelcomeHandler(&fakeWelcomeResolver{name: "Erin", email: ""}, &fakeWelcomeSender{}, "https://portal")
	_, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: "u-1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
	}
}

func TestResendWelcome_ResolverErrorIsInternal(t *testing.T) {
	h := grpcsvc.NewWelcomeHandler(&fakeWelcomeResolver{err: errors.New("boom")}, &fakeWelcomeSender{}, "https://portal")
	_, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: "u-1"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

func TestResendWelcome_SenderErrorIsInternal(t *testing.T) {
	res := &fakeWelcomeResolver{name: "Erin", email: "erin@example.org"}
	snd := &fakeWelcomeSender{err: errors.New("smtp down")}
	h := grpcsvc.NewWelcomeHandler(res, snd, "https://portal")
	_, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: "u-1"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
