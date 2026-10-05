// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"net"
	"testing"
	"time"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

type fakeViewRecorder struct{ lastUser, lastVer string }

func (f *fakeViewRecorder) RecordView(_ context.Context, userID, policyVersionID string) error {
	f.lastUser, f.lastVer = userID, policyVersionID
	return nil
}

const bufSize = 1 << 20

// startAckServer starts the AckService behind an interceptor that makes
// userID the forwarded actor of every call.
func startAckServer(t *testing.T, svc grpcsvc.AckServiceBackend, userID string) obligationsv1.AckServiceClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	interceptor := actorInterceptor(userID)
	s := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	obligationsv1.RegisterAckServiceServer(s, grpcsvc.NewAckHandler(svc, nil))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewAckServiceClient(conn)
}

// startAckServerWithViews starts the AckService with auth middleware and a ViewRecorder.
func startAckServerWithViews(t *testing.T, svc grpcsvc.AckServiceBackend, views grpcsvc.ViewRecorder, userID string) obligationsv1.AckServiceClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	interceptor := actorInterceptor(userID)
	s := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	obligationsv1.RegisterAckServiceServer(s, grpcsvc.NewAckHandler(svc, views))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewAckServiceClient(conn)
}

// startTestServer starts the AckService without auth middleware (for GetAckStatus tests).
func startTestServer(t *testing.T, svc grpcsvc.AckServiceBackend) obligationsv1.AckServiceClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	obligationsv1.RegisterAckServiceServer(s, grpcsvc.NewAckHandler(svc, nil))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewAckServiceClient(conn)
}

type fakeAckSvc struct {
	userIDSeen string

	// transfer stubbing / capture
	transferIn     ack.TransferInput
	transferCalls  int
	transferResult ack.TransferResult
}

func (f *fakeAckSvc) Record(_ context.Context, in ack.RecordInput) (ack.RecordResult, error) {
	f.userIDSeen = in.UserID
	return ack.RecordResult{ID: "ack-test-id", AlreadyExisted: false}, nil
}
func (f *fakeAckSvc) GetStatus(_ context.Context, userID, pvID string) (bool, error) {
	_, _ = userID, pvID
	return true, nil
}
func (f *fakeAckSvc) Transfer(_ context.Context, in ack.TransferInput) (ack.TransferResult, error) {
	f.transferCalls++
	f.transferIn = in
	return f.transferResult, nil
}

func TestRecordAckHandler(t *testing.T) {
	userID := "claims-user-id"
	svc := &fakeAckSvc{}
	client := startAckServer(t, svc, userID)

	// The bearer header is ignored: actorInterceptor sets the actor.
	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer stub-token"))

	resp, err := client.RecordAck(ctx, &obligationsv1.RecordAckRequest{
		PolicyVersionId: "pv1",
	})
	if err != nil {
		t.Fatalf("RecordAck: %v", err)
	}
	if resp.Acknowledgment == nil || resp.Acknowledgment.Id == "" {
		t.Error("expected non-empty ack ID in response")
	}
	if resp.Acknowledgment.UserId != userID {
		t.Errorf("UserId: got %q, want %q", resp.Acknowledgment.UserId, userID)
	}
	if svc.userIDSeen != userID {
		t.Errorf("handler passed userID %q to service, want %q", svc.userIDSeen, userID)
	}
}

func TestGetAckStatusHandler(t *testing.T) {
	client := startTestServer(t, &fakeAckSvc{})

	resp, err := client.GetAckStatus(context.Background(), &obligationsv1.GetAckStatusRequest{
		UserId: "u1", PolicyVersionId: "pv1",
	})
	if err != nil {
		t.Fatalf("GetAckStatus: %v", err)
	}
	if !resp.Acknowledged {
		t.Error("expected Acknowledged=true")
	}
	if resp.AckedAt == nil {
		t.Error("expected AckedAt to be set when acknowledged")
	}
}

func TestTransferAcknowledgmentsHandler(t *testing.T) {
	srcAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tgtAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	svc := &fakeAckSvc{
		transferResult: ack.TransferResult{
			Moved:   2,
			Deduped: 1,
			Items: []ack.TransferItem{
				{PolicyVersionID: "pv-moved", SourceAckedAt: srcAt, Resolution: store.ResolutionMoved},
				{PolicyVersionID: "pv-kept-earliest", SourceAckedAt: srcAt, TargetAckedAt: &tgtAt, Resolution: store.ResolutionKeptEarliest},
			},
		},
	}
	client := startTestServer(t, svc)

	resp, err := client.TransferAcknowledgments(context.Background(), &obligationsv1.TransferAcknowledgmentsRequest{
		SourceUserId:     "src-user",
		TargetUserId:     "tgt-user",
		ActorUserId:      "admin-user",
		DryRun:           true,
		MergeOperationId: "merge-42",
	})
	if err != nil {
		t.Fatalf("TransferAcknowledgments: %v", err)
	}

	// Request forwarded to backend verbatim.
	if svc.transferCalls != 1 {
		t.Fatalf("expected 1 backend call, got %d", svc.transferCalls)
	}
	if svc.transferIn.SourceUserID != "src-user" || svc.transferIn.TargetUserID != "tgt-user" {
		t.Errorf("forwarded users wrong: %+v", svc.transferIn)
	}
	if svc.transferIn.ActorUserID != "admin-user" || !svc.transferIn.DryRun || svc.transferIn.MergeOperationID != "merge-42" {
		t.Errorf("forwarded options wrong: %+v", svc.transferIn)
	}

	// Response mapping.
	if resp.Moved != 2 || resp.Deduped != 1 {
		t.Errorf("counts: got moved=%d deduped=%d, want 2/1", resp.Moved, resp.Deduped)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Items))
	}
	if resp.Items[0].PolicyVersionId != "pv-moved" ||
		resp.Items[0].Resolution != obligationsv1.AckTransferResolution_ACK_TRANSFER_RESOLUTION_MOVED {
		t.Errorf("item0 mapping wrong: %+v", resp.Items[0])
	}
	if resp.Items[0].GetTargetAckedAt() != nil {
		t.Errorf("MOVED item must have nil TargetAckedAt, got %v", resp.Items[0].GetTargetAckedAt())
	}
	if !resp.Items[0].GetSourceAckedAt().AsTime().Equal(srcAt) {
		t.Errorf("item0 SourceAckedAt: got %v want %v", resp.Items[0].GetSourceAckedAt().AsTime(), srcAt)
	}
	if resp.Items[1].Resolution != obligationsv1.AckTransferResolution_ACK_TRANSFER_RESOLUTION_KEPT_EARLIEST {
		t.Errorf("item1 resolution: got %v", resp.Items[1].Resolution)
	}
	if !resp.Items[1].GetTargetAckedAt().AsTime().Equal(tgtAt) {
		t.Errorf("item1 TargetAckedAt: got %v want %v", resp.Items[1].GetTargetAckedAt().AsTime(), tgtAt)
	}
}

func TestTransferAcknowledgmentsRejectsSameUser(t *testing.T) {
	svc := &fakeAckSvc{}
	client := startTestServer(t, svc)

	_, err := client.TransferAcknowledgments(context.Background(), &obligationsv1.TransferAcknowledgmentsRequest{
		SourceUserId: "same", TargetUserId: "same",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (err=%v)", status.Code(err), err)
	}
	if svc.transferCalls != 0 {
		t.Errorf("backend must not be called on validation failure; got %d calls", svc.transferCalls)
	}
}

func TestTransferAcknowledgmentsRequiresUsers(t *testing.T) {
	svc := &fakeAckSvc{}
	client := startTestServer(t, svc)

	cases := []*obligationsv1.TransferAcknowledgmentsRequest{
		{SourceUserId: "", TargetUserId: "t"},
		{SourceUserId: "s", TargetUserId: ""},
	}
	for _, req := range cases {
		if _, err := client.TransferAcknowledgments(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("req %+v: expected InvalidArgument, got %v", req, status.Code(err))
		}
	}
	if svc.transferCalls != 0 {
		t.Errorf("backend must not be called; got %d calls", svc.transferCalls)
	}
}

func TestRecordView(t *testing.T) {
	userID := "view-user-id"
	rec := &fakeViewRecorder{}
	client := startAckServerWithViews(t, &fakeAckSvc{}, rec, userID)

	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer stub-token"))

	_, err := client.RecordView(ctx, &obligationsv1.RecordViewRequest{
		PolicyVersionId: "pv-view-1",
	})
	if err != nil {
		t.Fatalf("RecordView: %v", err)
	}
	if rec.lastVer != "pv-view-1" {
		t.Errorf("lastVer: got %q, want %q", rec.lastVer, "pv-view-1")
	}
	if rec.lastUser != userID {
		t.Errorf("lastUser: got %q, want %q", rec.lastUser, userID)
	}
}

// actorInterceptor stands in for go-grpc-actor's server interceptor: it makes
// userID the forwarded actor of every call.
func actorInterceptor(userID string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(grpcactor.WithActor(ctx, grpcactor.Actor{Subject: userID}), req)
	}
}
