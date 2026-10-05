// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/reporting"
)

// fakeCompletionResolver fakes the completionResolver interface.
// It returns fixed total/acked/overdue values for testing.
type fakeCompletionResolver struct{}

func (f *fakeCompletionResolver) Completion(_ context.Context, _, _ string) (int, int, []obligation.User, error) {
	return 5, 3, []obligation.User{
		{ID: "u1", Email: "u1@example.com"},
		{ID: "u2", Email: "u2@example.com"},
	}, nil
}

func (f *fakeCompletionResolver) CompletionMetrics(_ context.Context, _, _ string) (float64, int, error) {
	return 2.5, 1, nil
}
func (f *fakeCompletionResolver) Roster(_ context.Context, _, _ string) ([]obligation.RosterEntry, []obligation.User, error) {
	return []obligation.RosterEntry{{ID: "u1", Email: "u1@example.org", AckedAt: time.Unix(1, 0)}},
		[]obligation.User{{ID: "u2", Email: "u2@example.org"}}, nil
}
func (f *fakeCompletionResolver) Activity(_ context.Context, _, _ string, days int) ([]obligation.ActivityDay, error) {
	return []obligation.ActivityDay{{Date: "2026-06-10", Acks: 1, Views: 2}}, nil
}

type fakeAckFetcher struct{}

func (f *fakeAckFetcher) FetchAcks(_ context.Context, _ string) ([]reporting.AckExportRow, error) {
	return []reporting.AckExportRow{{AckID: "ack-1", UserID: "u1", PolicyVersionID: "pv1"}}, nil
}

func startReportingTestServer(t *testing.T) obligationsv1.ReportingServiceClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	exporter := reporting.NewExporter(&fakeAckFetcher{})
	obligationsv1.RegisterReportingServiceServer(s, grpcsvc.NewReportingHandler(exporter, &fakeCompletionResolver{}))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewReportingServiceClient(conn)
}

func TestGetCompletionReport(t *testing.T) {
	client := startReportingTestServer(t)
	resp, err := client.GetCompletionReport(context.Background(), &obligationsv1.GetCompletionReportRequest{
		PolicyVersionId: "pv1",
		GroupId:         "p1",
	})
	if err != nil {
		t.Fatalf("GetCompletionReport: %v", err)
	}
	if resp.TotalAudience != 5 {
		t.Errorf("TotalAudience: got %d", resp.TotalAudience)
	}
	if resp.TotalAcked != 3 {
		t.Errorf("TotalAcked: got %d", resp.TotalAcked)
	}
	if len(resp.Overdue) != 2 {
		t.Errorf("Overdue count: got %d", len(resp.Overdue))
	}
}

func TestExportAcks_CSV(t *testing.T) {
	client := startReportingTestServer(t)
	resp, err := client.ExportAcks(context.Background(), &obligationsv1.ExportAcksRequest{
		PolicyVersionId: "pv1", Format: "csv",
	})
	if err != nil {
		t.Fatalf("ExportAcks: %v", err)
	}
	if resp.ContentType != "text/csv" {
		t.Errorf("content type: got %q", resp.ContentType)
	}
	if len(resp.Data) == 0 {
		t.Error("expected non-empty CSV data")
	}
}

func TestExportAcks_JSON(t *testing.T) {
	client := startReportingTestServer(t)
	resp, err := client.ExportAcks(context.Background(), &obligationsv1.ExportAcksRequest{
		PolicyVersionId: "pv1", Format: "json",
	})
	if err != nil {
		t.Fatalf("ExportAcks: %v", err)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("content type: got %q", resp.ContentType)
	}
	if len(resp.Data) == 0 {
		t.Error("expected non-empty JSON data")
	}
}

func TestGetAckRoster(t *testing.T) {
	client := startReportingTestServer(t)
	resp, err := client.GetAckRoster(context.Background(), &obligationsv1.GetAckRosterRequest{PolicyVersionId: "pv1"})
	if err != nil {
		t.Fatalf("GetAckRoster: %v", err)
	}
	if len(resp.GetAcked()) != 1 || resp.GetAcked()[0].GetUserId() != "u1" {
		t.Fatalf("acked: %+v", resp.GetAcked())
	}
	if len(resp.GetPending()) != 1 || resp.GetPending()[0].GetUserId() != "u2" {
		t.Fatalf("pending: %+v", resp.GetPending())
	}
}

func TestGetAckActivity(t *testing.T) {
	client := startReportingTestServer(t)
	resp, err := client.GetAckActivity(context.Background(), &obligationsv1.GetAckActivityRequest{PolicyVersionId: "pv1", Days: 7})
	if err != nil {
		t.Fatalf("GetAckActivity: %v", err)
	}
	if len(resp.GetDays()) != 1 {
		t.Fatalf("days: got %d, want 1", len(resp.GetDays()))
	}
	if resp.GetDays()[0].GetDate() != "2026-06-10" {
		t.Errorf("date: got %q, want %q", resp.GetDays()[0].GetDate(), "2026-06-10")
	}
	if resp.GetDays()[0].GetAcks() != 1 {
		t.Errorf("acks: got %d, want 1", resp.GetDays()[0].GetAcks())
	}
	if resp.GetDays()[0].GetViews() != 2 {
		t.Errorf("views: got %d, want 2", resp.GetDays()[0].GetViews())
	}
}

func TestGetCompletionReport_Metrics(t *testing.T) {
	client := startReportingTestServer(t)
	resp, err := client.GetCompletionReport(context.Background(), &obligationsv1.GetCompletionReportRequest{PolicyVersionId: "pv1"})
	if err != nil {
		t.Fatalf("GetCompletionReport: %v", err)
	}
	if resp.GetAvgDaysToAck() < 2.4 || resp.GetAvgDaysToAck() > 2.6 {
		t.Fatalf("avg: %v", resp.GetAvgDaysToAck())
	}
	if resp.GetViewedNotAckedCount() != 1 {
		t.Fatalf("vna: %d", resp.GetViewedNotAckedCount())
	}
}
