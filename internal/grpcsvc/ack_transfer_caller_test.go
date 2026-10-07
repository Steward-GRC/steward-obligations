// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/workloadauth"
)

func identityGrant() context.Context {
	return workloadauth.ContextWithGrant(context.Background(), workloadauth.Grant{
		Caller: workloadauth.Caller{Name: grpcsvc.CallerIdentity, ServiceAccount: "steward/steward-identity"},
		Access: workloadauth.OnBehalf,
	})
}

// The transfer carries the service the workload-auth check proved, so
// ack.transferred names it next to the admin.
func TestTransferAcknowledgmentsPassesTheVerifiedCaller(t *testing.T) {
	svc := &fakeAckSvc{}
	h := grpcsvc.NewAckHandler(svc, nil)

	if _, err := h.TransferAcknowledgments(identityGrant(), &obligationsv1.TransferAcknowledgmentsRequest{
		SourceUserId: "src", TargetUserId: "tgt", ActorUserId: "admin-1", MergeOperationId: "merge-1",
	}); err != nil {
		t.Fatalf("TransferAcknowledgments: %v", err)
	}
	if svc.transferIn.Caller != grpcsvc.CallerIdentity {
		t.Fatalf("caller=%q, want %q", svc.transferIn.Caller, grpcsvc.CallerIdentity)
	}
	if svc.transferIn.ActorUserID != "admin-1" {
		t.Fatalf("actor=%q, want the admin, not the service", svc.transferIn.ActorUserID)
	}
}

// A verified service is not a person: it never stands in for the actor, so a
// real transfer with a blank actor is refused even from identity.
func TestTransferAcknowledgmentsRefusesABlankActorFromAVerifiedService(t *testing.T) {
	svc := &fakeAckSvc{}
	h := grpcsvc.NewAckHandler(svc, nil)

	_, err := h.TransferAcknowledgments(identityGrant(), &obligationsv1.TransferAcknowledgmentsRequest{
		SourceUserId: "src", TargetUserId: "tgt", ActorUserId: " ",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	if svc.transferCalls != 0 {
		t.Fatal("the transfer ran without an actor")
	}
}
