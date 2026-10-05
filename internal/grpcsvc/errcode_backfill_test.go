// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"errors"
	"testing"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
)

// errStoreDown stands in for a genuine store/dependency fault (dial / timeout /
// query error) so the handler routes it to the coded store/send path.
var errStoreDown = errors.New("dial tcp cn-pg:5432: connect: connection refused")

// recordFailAckSvc satisfies grpcsvc.AckServiceBackend and fails Record with a
// store fault, exercising NOTIFY_STORE_UNAVAILABLE on the ack write path.
type recordFailAckSvc struct{}

func (recordFailAckSvc) Record(context.Context, ack.RecordInput) (ack.RecordResult, error) {
	return ack.RecordResult{}, errStoreDown
}
func (recordFailAckSvc) GetStatus(context.Context, string, string) (bool, error) {
	return false, errStoreDown
}
func (recordFailAckSvc) Transfer(context.Context, ack.TransferInput) (ack.TransferResult, error) {
	return ack.TransferResult{}, errStoreDown
}

// TestRecordAckStoreFaultCarriesCode proves the ack write path: an authenticated
// caller whose ack store write fails gets an Internal status carrying
// ErrorInfo{Reason: NOTIFY_STORE_UNAVAILABLE, codeNum: 7001, Domain:
// the obligations service} with op=record_ack in metadata — the raw cause stays off
// the wire (debug-only) while the stable Code 7001 rides for the gateway.
func TestRecordAckStoreFaultCarriesCode(t *testing.T) {
	h := grpcsvc.NewAckHandler(recordFailAckSvc{}, nil)
	ctx := grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: "u-1"})

	_, err := h.RecordAck(ctx, &obligationsv1.RecordAckRequest{PolicyVersionId: "pv-1"})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok, "error must be a gRPC status")
	require.Equal(t, codes.Internal, st.Code(), "must preserve the Internal gRPC code")

	info, ok := apperrgrpc.FromStatus(st)
	require.True(t, ok, "status must carry ErrorInfo")
	require.Equal(t, "NOTIFY_STORE_UNAVAILABLE", info.Symbol)
	require.Equal(t, 7001, info.Code)
	require.Equal(t, "obligations", info.Domain)
	require.Equal(t, "record_ack", info.Metadata["op"])
}

// TestRecordAckUnauthenticatedCarriesCode proves the authn gate: a caller with
// no forwarded claims gets an Unauthenticated status carrying
// ErrorInfo{Reason: ACK_AUTH_REQUIRED, codeNum: 7003} and the user-safe message.
func TestRecordAckUnauthenticatedCarriesCode(t *testing.T) {
	h := grpcsvc.NewAckHandler(recordFailAckSvc{}, nil)

	_, err := h.RecordAck(context.Background(), &obligationsv1.RecordAckRequest{PolicyVersionId: "pv-1"})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok, "error must be a gRPC status")
	require.Equal(t, codes.Unauthenticated, st.Code(), "must preserve the Unauthenticated gRPC code")

	info, ok := apperrgrpc.FromStatus(st)
	require.True(t, ok, "status must carry ErrorInfo")
	require.Equal(t, "ACK_AUTH_REQUIRED", info.Symbol)
	require.Equal(t, 7003, info.Code)
	require.Equal(t, "obligations", info.Domain)
}

// TestResendWelcomeSendFaultCarriesCode proves the marquee send site: a genuine
// (non-paused) mail send failure returns an Internal status carrying
// ErrorInfo{Reason: WELCOME_SEND_UNAVAILABLE, codeNum: 7002} with the target
// user_id in metadata — the raw sender cause stays off the wire (debug-only).
func TestResendWelcomeSendFaultCarriesCode(t *testing.T) {
	res := &fakeWelcomeResolver{name: "Erin", email: "erin@example.org"}
	snd := &fakeWelcomeSender{err: errors.New("smtp down")}
	h := grpcsvc.NewWelcomeHandler(res, snd, "https://portal")

	_, err := h.ResendWelcome(context.Background(), &obligationsv1.ResendWelcomeRequest{UserId: "u-1"})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok, "error must be a gRPC status")
	require.Equal(t, codes.Internal, st.Code(), "must preserve the Internal gRPC code")

	info, ok := apperrgrpc.FromStatus(st)
	require.True(t, ok, "status must carry ErrorInfo")
	require.Equal(t, "WELCOME_SEND_UNAVAILABLE", info.Symbol)
	require.Equal(t, 7002, info.Code)
	require.Equal(t, "obligations", info.Domain)
	require.Equal(t, "u-1", info.Metadata["user_id"])
}
