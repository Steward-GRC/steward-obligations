// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// fakeObligationBackend fakes the ObligationBackend for handler tests.
type fakeObligationBackend struct {
	myObligations          []obligation.ObligationItem
	obligatedAudienceCount int32
	ackSummaryRequired     int32
	ackSummaryDone         int32
}

func (f *fakeObligationBackend) MyObligations(_ context.Context, _ string) ([]obligation.ObligationItem, error) {
	return f.myObligations, nil
}

func (f *fakeObligationBackend) MyAckSummary(_ context.Context, _ string) (int32, int32, error) {
	return f.ackSummaryRequired, f.ackSummaryDone, nil
}

func (f *fakeObligationBackend) ObligatedAudienceCount(_ context.Context, _ string) (int32, error) {
	return f.obligatedAudienceCount, nil
}

func startObligationTestServer(t *testing.T, backend grpcsvc.ObligationBackend) obligationsv1.ObligationServiceClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	obligationsv1.RegisterObligationServiceServer(s, grpcsvc.NewObligationHandler(backend))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewObligationServiceClient(conn)
}

func TestGetMyObligations(t *testing.T) {
	backend := &fakeObligationBackend{
		myObligations: []obligation.ObligationItem{
			{PolicyID: "p1", Number: "POL-001", Title: "Policy One", VersionNo: 2, PolicyVersionID: "v1b"},
		},
	}
	client := startObligationTestServer(t, backend)

	resp, err := client.GetMyObligations(context.Background(), &obligationsv1.GetMyObligationsRequest{UserId: "u1"})
	if err != nil {
		t.Fatalf("GetMyObligations: %v", err)
	}
	if len(resp.GetObligations()) != 1 {
		t.Fatalf("expected 1 obligation, got %d", len(resp.GetObligations()))
	}
	got := resp.GetObligations()[0]
	if got.PolicyId != "p1" {
		t.Errorf("PolicyId: got %q, want %q", got.PolicyId, "p1")
	}
	if got.Number != "POL-001" {
		t.Errorf("Number: got %q, want %q", got.Number, "POL-001")
	}
	if got.VersionNo != 2 {
		t.Errorf("VersionNo: got %d, want 2", got.VersionNo)
	}
	if got.PolicyVersionId != "v1b" {
		t.Errorf("PolicyVersionId: got %q, want %q", got.PolicyVersionId, "v1b")
	}
}

func TestGetMyObligations_Empty(t *testing.T) {
	backend := &fakeObligationBackend{myObligations: nil}
	client := startObligationTestServer(t, backend)

	resp, err := client.GetMyObligations(context.Background(), &obligationsv1.GetMyObligationsRequest{UserId: "u1"})
	if err != nil {
		t.Fatalf("GetMyObligations: %v", err)
	}
	if len(resp.GetObligations()) != 0 {
		t.Errorf("expected 0 obligations, got %d", len(resp.GetObligations()))
	}
}

func TestGetObligatedAudienceCount(t *testing.T) {
	backend := &fakeObligationBackend{obligatedAudienceCount: 42}
	client := startObligationTestServer(t, backend)

	resp, err := client.GetObligatedAudienceCount(context.Background(), &obligationsv1.GetObligatedAudienceCountRequest{PolicyId: "p1"})
	if err != nil {
		t.Fatalf("GetObligatedAudienceCount: %v", err)
	}
	if resp.GetCount() != 42 {
		t.Errorf("Count: got %d, want 42", resp.GetCount())
	}
}

func TestMyAckSummary(t *testing.T) {
	backend := &fakeObligationBackend{ackSummaryRequired: 3, ackSummaryDone: 1}
	client := startObligationTestServer(t, backend)

	resp, err := client.MyAckSummary(context.Background(), &obligationsv1.MyAckSummaryRequest{UserId: "u1"})
	if err != nil {
		t.Fatalf("MyAckSummary: %v", err)
	}
	if resp.GetRequired() != 3 {
		t.Errorf("Required: got %d, want 3", resp.GetRequired())
	}
	if resp.GetDone() != 1 {
		t.Errorf("Done: got %d, want 1", resp.GetDone())
	}
}
