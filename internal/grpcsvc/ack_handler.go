// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package grpcsvc contains the gRPC server-side handlers that wire the
// the obligations service domain services and persistence stores behind the
// proto-generated service interfaces from policy.compliance.v1.
//
// Each handler holds a small backend interface so that unit tests can inject
// fakes via the in-process bufconn pattern without needing a live database or
// platform/audit broker.
package grpcsvc

import (
	"context"
	"strings"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// AckServiceBackend is the narrow interface the handler depends on; the
// concrete *ack.Service satisfies it in production and tests inject fakes.
type AckServiceBackend interface {
	Record(ctx context.Context, in ack.RecordInput) (ack.RecordResult, error)
	GetStatus(ctx context.Context, userID, policyVersionID string) (bool, error)
	Transfer(ctx context.Context, in ack.TransferInput) (ack.TransferResult, error)
}

// ViewRecorder appends an in-app policy view. Satisfied by *store.PolicyViewStore.
type ViewRecorder interface {
	RecordView(ctx context.Context, userID, policyVersionID string) error
}

// AckHandler implements obligationsv1.AckServiceServer.
type AckHandler struct {
	obligationsv1.UnimplementedAckServiceServer
	svc   AckServiceBackend
	views ViewRecorder
}

// NewAckHandler returns an AckHandler. views may be nil only in tests that do
// not exercise RecordView.
func NewAckHandler(svc AckServiceBackend, views ViewRecorder) *AckHandler {
	return &AckHandler{svc: svc, views: views}
}

// RecordAck records an acknowledgment and returns a populated Acknowledgment
// proto so clients have the assigned id without an extra round-trip.
// The caller is the forwarded actor (go-grpc-actor); the request carries only
// the policy_version_id. During act-as the actor's subject is the target user,
// and the audit seam credits the admin.
func (h *AckHandler) RecordAck(ctx context.Context, req *obligationsv1.RecordAckRequest) (*obligationsv1.RecordAckResponse, error) {
	userID, ok := actorID(ctx)
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.AckAuthRequired())
	}

	result, err := h.svc.Record(ctx, ack.RecordInput{
		UserID:          userID,
		PolicyVersionID: req.PolicyVersionId,
	})
	if err != nil {
		return nil, storeUnavailable(ctx, "record_ack", err)
	}

	return &obligationsv1.RecordAckResponse{
		Acknowledgment: &obligationsv1.Acknowledgment{
			Id:              result.ID,
			UserId:          userID,
			PolicyVersionId: req.PolicyVersionId,
			AckedAt:         timestamppb.Now(),
		},
	}, nil
}

// GetAckStatus reports whether a given user has acknowledged a policy version.
// AckedAt is set to now on success when the user has acked; the timestamp is
// a placeholder until the store exposes the persisted acked_at value.
func (h *AckHandler) GetAckStatus(ctx context.Context, req *obligationsv1.GetAckStatusRequest) (*obligationsv1.GetAckStatusResponse, error) {
	ok, err := h.svc.GetStatus(ctx, req.UserId, req.PolicyVersionId)
	if err != nil {
		return nil, storeUnavailable(ctx, "get_ack_status", err)
	}
	resp := &obligationsv1.GetAckStatusResponse{Acknowledged: ok}
	if ok {
		resp.AckedAt = timestamppb.Now()
	}
	return resp, nil
}

// resolutionToProto maps a store/domain resolution string to the generated
// obligationsv1.AckTransferResolution enum, defaulting to UNSPECIFIED.
func resolutionToProto(r string) obligationsv1.AckTransferResolution {
	switch r {
	case store.ResolutionMoved:
		return obligationsv1.AckTransferResolution_ACK_TRANSFER_RESOLUTION_MOVED
	case store.ResolutionKeptEarliest:
		return obligationsv1.AckTransferResolution_ACK_TRANSFER_RESOLUTION_KEPT_EARLIEST
	case store.ResolutionTargetKept:
		return obligationsv1.AckTransferResolution_ACK_TRANSFER_RESOLUTION_TARGET_KEPT
	default:
		return obligationsv1.AckTransferResolution_ACK_TRANSFER_RESOLUTION_UNSPECIFIED
	}
}

// TransferAcknowledgments moves a SOURCE user's policy
// acknowledgments onto a TARGET user with dedupe, idempotently, with a dry-run
// preview mode. This is an admin/system RPC: source and target are taken from
// the request body (not JWT claims), matching the other cross-user admin RPCs
// (WelcomeService, reporting) which are callable in-cluster without a
// per-caller authz guard — there is no admin guard convention in this service.
func (h *AckHandler) TransferAcknowledgments(ctx context.Context, req *obligationsv1.TransferAcknowledgmentsRequest) (*obligationsv1.TransferAcknowledgmentsResponse, error) {
	if req.GetSourceUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_user_id is required")
	}
	if req.GetTargetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_user_id is required")
	}
	if req.GetSourceUserId() == req.GetTargetUserId() {
		return nil, status.Error(codes.InvalidArgument, "source_user_id and target_user_id must differ")
	}
	// An acknowledgment is a legal attestation, so moving one between accounts
	// has to record who authorised the move. ack.transferred takes its actor
	// from this field and nothing else — there is no claims fallback, because
	// this is a service-to-service admin RPC whose caller (identity's merge
	// orchestrator) is not the acting human. So a blank actor here does not
	// degrade the event, it produces one naming nobody. Reject instead.
	//
	// Trimmed because " " is indistinguishable from "" as evidence, and
	// actor_user_id is a plain proto3 string, so "unset" and "" cannot be told
	// apart on the wire — non-empty-after-trim is the strongest rule the
	// contract can support. Deliberately NOT a UUID check: on identity's
	// toolbox (mTLS) path the actor is an operator label such as
	// "alice@<fingerprint>", not a platform uuid, so requiring a uuid would
	// reject a legitimate caller.
	//
	// Dry-run previews are exempt: they mutate nothing and emit no event (see
	// ack.Service.Transfer), so there is no unattributed event to prevent, and
	// requiring an actor would break PreviewAccountMerge, which resolves an
	// admin but has no attestation to attribute.
	actor := strings.TrimSpace(req.GetActorUserId())
	if actor == "" && !req.GetDryRun() {
		return nil, errcodes.Error(ctx, errcodes.AckTransferActorRequired())
	}

	res, err := h.svc.Transfer(ctx, ack.TransferInput{
		SourceUserID:     req.GetSourceUserId(),
		TargetUserID:     req.GetTargetUserId(),
		ActorUserID:      actor,
		DryRun:           req.GetDryRun(),
		MergeOperationID: req.GetMergeOperationId(),
	})
	if err != nil {
		return nil, storeUnavailable(ctx, "transfer_acknowledgments", err)
	}

	items := make([]*obligationsv1.AckTransferItem, 0, len(res.Items))
	for _, it := range res.Items {
		item := &obligationsv1.AckTransferItem{
			PolicyVersionId: it.PolicyVersionID,
			SourceAckedAt:   timestamppb.New(it.SourceAckedAt),
			Resolution:      resolutionToProto(it.Resolution),
		}
		if it.TargetAckedAt != nil {
			item.TargetAckedAt = timestamppb.New(*it.TargetAckedAt)
		}
		items = append(items, item)
	}

	return &obligationsv1.TransferAcknowledgmentsResponse{
		Moved:   toInt32(res.Moved),
		Deduped: toInt32(res.Deduped),
		Items:   items,
	}, nil
}

// RecordView records that the authenticated caller viewed a policy version. The
// user is the forwarded actor; the request carries only the version.
func (h *AckHandler) RecordView(ctx context.Context, req *obligationsv1.RecordViewRequest) (*obligationsv1.RecordViewResponse, error) {
	userID, ok := actorID(ctx)
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.AckAuthRequired())
	}
	if err := h.views.RecordView(ctx, userID, req.GetPolicyVersionId()); err != nil {
		return nil, storeUnavailable(ctx, "record_view", err)
	}
	return &obligationsv1.RecordViewResponse{}, nil
}

// actorID is the forwarded actor's subject: the user the call acts for.
func actorID(ctx context.Context) (string, bool) {
	a, ok := grpcactor.FromContext(ctx)
	if !ok || a.Subject == "" {
		return "", false
	}
	return a.Subject, true
}
