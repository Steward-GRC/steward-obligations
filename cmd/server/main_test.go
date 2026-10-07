// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	identityv1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/identity/v1"
	"github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/audit"
	"google.golang.org/grpc"
)

// fakeIdentityRead is a minimal identityv1.IdentityReadServiceClient: the
// embedded (nil) interface satisfies the full surface, and only GetUser is
// overridden — the single RPC identityGRPCAdapter.ResolveTimezone calls.
type fakeIdentityRead struct {
	identityv1.IdentityReadServiceClient
	user *identityv1.User
	err  error
}

func (f fakeIdentityRead) GetUser(_ context.Context, _ *identityv1.GetUserRequest, _ ...grpc.CallOption) (*identityv1.GetUserResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &identityv1.GetUserResponse{User: f.user}, nil
}

// TestResolveTimezone_ReturnsIdentityZone proves the slice-9
// seam: the adapter reads the timezone Identity exposes on the User message so
// quiet-hours / digest windows evaluate in the user's real zone.
func TestResolveTimezone_ReturnsIdentityZone(t *testing.T) {
	a := &identityGRPCAdapter{client: fakeIdentityRead{user: &identityv1.User{Timezone: "America/New_York"}}}
	got, err := a.ResolveTimezone(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ResolveTimezone: %v", err)
	}
	if got != "America/New_York" {
		t.Fatalf("timezone: got %q want America/New_York", got)
	}
}

// TestResolveTimezone_UnsetIsEmpty proves an unset zone comes back empty, which
// notify.QuietHours treats as the UTC fallback (no regression).
func TestResolveTimezone_UnsetIsEmpty(t *testing.T) {
	a := &identityGRPCAdapter{client: fakeIdentityRead{user: &identityv1.User{}}}
	got, err := a.ResolveTimezone(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ResolveTimezone: %v", err)
	}
	if got != "" {
		t.Fatalf("unset timezone should be empty, got %q", got)
	}
}

// TestResolveTimezone_ErrorPropagates proves a lookup error surfaces so the
// caller (QuietHours) can fall back to UTC rather than silently mis-zoning.
func TestResolveTimezone_ErrorPropagates(t *testing.T) {
	a := &identityGRPCAdapter{client: fakeIdentityRead{err: errors.New("identity down")}}
	if _, err := a.ResolveTimezone(context.Background(), "u1"); err == nil {
		t.Fatal("expected error from ResolveTimezone when GetUser fails")
	}
}

// ---: the ack audit wiring itself ---

// capturingSink is a terminal ack.AuditSink that records what it was handed
// after the production composition has run.
type capturingSink struct{ events []audit.Event }

func (c *capturingSink) Emit(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

// TestAckAuditEmitter_AttributesImpersonationToAdmin drives the PRODUCTION
// composition (ackAuditEmitter, the single definition main uses) and asserts an
// impersonated acknowledgment is attributed to the real admin with the target
// preserved.
//
// This guards that main actually consumes the forwarded impersonator: a test
// that composes its own decorated sink can't detect a main that forgot to,
// only one that calls the same function main calls.
func TestAckAuditEmitter_AttributesImpersonationToAdmin(t *testing.T) {
	const (
		target = "u-erin"
		admin  = "u-alice"
	)

	sink := &capturingSink{}
	emitter := ackAuditEmitter(sink)

	// The context a handler sees under an impersonation: effective claims are
	// the target, and the admin arrives as the forwarded impersonator id — which
	// is all a service ever gets, since only the gateway holds the admin's
	// Claims.
	ctx := grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: target, Impersonator: admin})

	if err := emitter.EmitAck(ctx, ack.RecordResult{
		ID:              "ack-1",
		UserID:          target,
		PolicyVersionID: "pv-1",
	}); err != nil {
		t.Fatalf("EmitAck: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("got %d events, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.ActorUserID != admin {
		t.Fatalf("actor = %q, want the acting admin %q — main.go is not composing the "+
			"impersonation sink, so an acknowledgment made by an admin impersonating a "+
			"user is recorded as that user", ev.ActorUserID, admin)
	}
	if got := ev.Attributes["impersonated_user_id"]; got != target {
		t.Fatalf("impersonated_user_id = %q, want %q — an empty value means the actor was "+
			"not populated before the decorator ran, so the impersonated target was "+
			"silently dropped", got, target)
	}
}

// TestAckAuditEmitter_OrdinaryAckNamesTheUser guards the non-impersonated path
// so the assertion above cannot pass by attributing everything to an admin.
func TestAckAuditEmitter_OrdinaryAckNamesTheUser(t *testing.T) {
	sink := &capturingSink{}
	ctx := grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: "u-plain"})

	if err := ackAuditEmitter(sink).EmitAck(ctx, ack.RecordResult{
		ID: "ack-2", UserID: "u-plain", PolicyVersionID: "pv-1",
	}); err != nil {
		t.Fatalf("EmitAck: %v", err)
	}
	ev := sink.events[0]
	if ev.ActorUserID != "u-plain" {
		t.Errorf("actor = %q, want %q", ev.ActorUserID, "u-plain")
	}
	if _, present := ev.Attributes["impersonated_user_id"]; present {
		t.Error("impersonated_user_id present on a non-impersonated ack")
	}
}

type fakeIdentityUsers struct {
	identityv1.IdentityReadServiceClient
	users []*identityv1.User
	err   error
}

func (f fakeIdentityUsers) ListAllUsers(_ context.Context, _ *identityv1.ListAllUsersRequest, _ ...grpc.CallOption) (*identityv1.ListAllUsersResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &identityv1.ListAllUsersResponse{Users: f.users}, nil
}

func TestListComplianceAdminEmailsKeepsOnlyComplianceAdmins(t *testing.T) {
	a := &identityGRPCAdapter{client: fakeIdentityUsers{users: []*identityv1.User{
		{Id: "u1", Email: "grace@example.org", Roles: []string{"compliance-admin"}},
		{Id: "u2", Email: "alice@example.org", Roles: []string{"site-admin"}},
		{Id: "u3", Email: "", Roles: []string{"compliance-admin"}},
		{Id: "u4", Email: "erin@example.org"},
	}}}
	got, err := a.ListComplianceAdminEmails(context.Background())
	if err != nil {
		t.Fatalf("ListComplianceAdminEmails: %v", err)
	}
	if len(got) != 1 || got[0] != "grace@example.org" {
		t.Fatalf("got %v, want only the compliance admin with an address", got)
	}
	if _, err := (&identityGRPCAdapter{client: fakeIdentityUsers{err: errors.New("down")}}).ListComplianceAdminEmails(context.Background()); err == nil {
		t.Fatal("an identity failure must be returned")
	}
}
