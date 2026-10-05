// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// ObligationBackend is the narrow interface the handler depends on; the
// concrete *obligation.Resolver satisfies it in production and tests inject
// fakes.
type ObligationBackend interface {
	MyObligations(ctx context.Context, userID string) ([]obligation.ObligationItem, error)
	MyAckSummary(ctx context.Context, userID string) (required int32, done int32, err error)
	ObligatedAudienceCount(ctx context.Context, policyID string) (int32, error)
}

// ObligationHandler implements obligationsv1.ObligationServiceServer.
type ObligationHandler struct {
	obligationsv1.UnimplementedObligationServiceServer
	resolver ObligationBackend
}

// NewObligationHandler returns an ObligationHandler backed by resolver.
func NewObligationHandler(resolver ObligationBackend) *ObligationHandler {
	return &ObligationHandler{resolver: resolver}
}

// GetMyObligations returns the outstanding policy-ack obligations for the
// requested user.
func (h *ObligationHandler) GetMyObligations(ctx context.Context, req *obligationsv1.GetMyObligationsRequest) (*obligationsv1.GetMyObligationsResponse, error) {
	items, err := h.resolver.MyObligations(ctx, req.GetUserId())
	if err != nil {
		return nil, storeUnavailable(ctx, "my_obligations", err)
	}
	protoItems := make([]*obligationsv1.ObligationItem, len(items))
	for i, item := range items {
		protoItems[i] = &obligationsv1.ObligationItem{
			PolicyId:        item.PolicyID,
			Number:          item.Number,
			Title:           item.Title,
			VersionNo:       item.VersionNo,
			PolicyVersionId: item.PolicyVersionID,
		}
	}
	return &obligationsv1.GetMyObligationsResponse{Obligations: protoItems}, nil
}

// GetObligatedAudienceCount returns the count of users obligated to ack the
// given policy.
func (h *ObligationHandler) GetObligatedAudienceCount(ctx context.Context, req *obligationsv1.GetObligatedAudienceCountRequest) (*obligationsv1.GetObligatedAudienceCountResponse, error) {
	count, err := h.resolver.ObligatedAudienceCount(ctx, req.GetPolicyId())
	if err != nil {
		return nil, storeUnavailable(ctx, "obligated_audience_count", err)
	}
	return &obligationsv1.GetObligatedAudienceCountResponse{Count: count}, nil
}

// MyAckSummary returns the requested user's acknowledgement-compliance summary:
// the number of policies they are obligated to ack (required) and how many of
// those they have already acked (done).
func (h *ObligationHandler) MyAckSummary(ctx context.Context, req *obligationsv1.MyAckSummaryRequest) (*obligationsv1.MyAckSummaryResponse, error) {
	required, done, err := h.resolver.MyAckSummary(ctx, req.GetUserId())
	if err != nil {
		return nil, storeUnavailable(ctx, "my_ack_summary", err)
	}
	return &obligationsv1.MyAckSummaryResponse{Required: required, Done: done}, nil
}
