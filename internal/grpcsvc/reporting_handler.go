// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/reporting"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// completionResolver is the narrow interface the ReportingHandler requires.
// Satisfied by *obligation.Resolver.
type completionResolver interface {
	Completion(ctx context.Context, policyVersionID, groupID string) (total int, acked int, overdue []obligation.User, err error)
	CompletionMetrics(ctx context.Context, policyVersionID, groupID string) (avgDaysToAck float64, viewedNotAcked int, err error)
	Roster(ctx context.Context, policyVersionID, groupID string) (acked []obligation.RosterEntry, pending []obligation.User, err error)
	Activity(ctx context.Context, policyVersionID, groupID string, days int) ([]obligation.ActivityDay, error)
}

// ReportingHandler implements obligationsv1.ReportingServiceServer.
type ReportingHandler struct {
	obligationsv1.UnimplementedReportingServiceServer
	exporter *reporting.Exporter
	resolver completionResolver
}

// NewReportingHandler returns a handler that uses resolver for the live
// completion denominator and exporter for audit-quality ack exports.
func NewReportingHandler(exp *reporting.Exporter, resolver completionResolver) *ReportingHandler {
	return &ReportingHandler{exporter: exp, resolver: resolver}
}

// GetCompletionReport returns the completion-percentage summary for the policy
// version (optionally scoped to a single audience group), including the list
// of overdue users for dashboards/escalations.
//
// The audience denominator (total) and overdue list are sourced from the live
// Resolver.Completion call; the acked count is also returned by Completion so
// it is consistent with the same snapshot.
func (h *ReportingHandler) GetCompletionReport(ctx context.Context, req *obligationsv1.GetCompletionReportRequest) (*obligationsv1.GetCompletionReportResponse, error) {
	total, acked, overdueUsers, err := h.resolver.Completion(ctx, req.GetPolicyVersionId(), req.GetGroupId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "completion resolver: %v", err)
	}

	var pct float32
	if total > 0 {
		pct = float32(acked) / float32(total) * 100
	}

	overdue := make([]*obligationsv1.OverdueEntry, len(overdueUsers))
	for i, u := range overdueUsers {
		overdue[i] = &obligationsv1.OverdueEntry{UserId: u.ID, Email: u.Email}
	}

	avgDays, viewedNotAcked, err := h.resolver.CompletionMetrics(ctx, req.GetPolicyVersionId(), req.GetGroupId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "completion metrics: %v", err)
	}
	return &obligationsv1.GetCompletionReportResponse{
		TotalAudience:       toInt32(total),
		TotalAcked:          toInt32(acked),
		CompletionPct:       pct,
		Overdue:             overdue,
		AvgDaysToAck:        float32(avgDays),
		ViewedNotAckedCount: toInt32(viewedNotAcked),
	}, nil
}

// ExportAcks returns the audit-quality acknowledgments export in the requested
// format ("csv" or "json"). The content-type is included in the response so
// gateways can forward it as a download.
func (h *ReportingHandler) ExportAcks(ctx context.Context, req *obligationsv1.ExportAcksRequest) (*obligationsv1.ExportAcksResponse, error) {
	data, contentType, err := h.exporter.Export(ctx, req.PolicyVersionId, req.Format)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "export acks: %v", err)
	}
	return &obligationsv1.ExportAcksResponse{Data: data, ContentType: contentType}, nil
}

// GetAckRoster returns the audience split into acked and pending entries.
func (h *ReportingHandler) GetAckRoster(ctx context.Context, req *obligationsv1.GetAckRosterRequest) (*obligationsv1.GetAckRosterResponse, error) {
	acked, pending, err := h.resolver.Roster(ctx, req.GetPolicyVersionId(), req.GetGroupId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ack roster: %v", err)
	}
	out := &obligationsv1.GetAckRosterResponse{
		Acked:   make([]*obligationsv1.AckRosterEntry, len(acked)),
		Pending: make([]*obligationsv1.AckRosterEntry, len(pending)),
	}
	for i, e := range acked {
		entry := &obligationsv1.AckRosterEntry{UserId: e.ID, Email: e.Email}
		if !e.AckedAt.IsZero() {
			entry.AckedAt = timestamppb.New(e.AckedAt)
		}
		out.Acked[i] = entry
	}
	for i, u := range pending {
		out.Pending[i] = &obligationsv1.AckRosterEntry{UserId: u.ID, Email: u.Email}
	}
	return out, nil
}

// GetAckActivity returns the daily ack/view series.
func (h *ReportingHandler) GetAckActivity(ctx context.Context, req *obligationsv1.GetAckActivityRequest) (*obligationsv1.GetAckActivityResponse, error) {
	days, err := h.resolver.Activity(ctx, req.GetPolicyVersionId(), req.GetGroupId(), int(req.GetDays()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ack activity: %v", err)
	}
	out := &obligationsv1.GetAckActivityResponse{Days: make([]*obligationsv1.AckActivityDay, len(days))}
	for i, d := range days {
		out.Days[i] = &obligationsv1.AckActivityDay{Date: d.Date, Acks: toInt32(d.Acks), Views: toInt32(d.Views)}
	}
	return out, nil
}
