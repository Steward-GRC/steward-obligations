// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	ackpkg "github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/audit"
	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
)

// These tests drive the production act-as wiring over a real gRPC connection
// carrying real forwarding metadata:
//
//	grpcactor.UnaryClientInterceptor (what the gateway dials with)
//	 -> grpcactor.UnaryServerInterceptor (what cmd/server installs)
//	 -> grpcsvc.NewAckHandler
//	 -> ack.NewService
//	 -> ack.NewPlatformAuditAdapter(ack.NewImpersonationSink(sink))
//
// Only the store and the final audit sink are fakes, so the tests see whether
// the forwarded admin that actually arrives on the wire reaches the audit
// event. Whether an admin may acknowledge on a user's behalf at all is a
// product decision; these tests assert only that the trail is truthful.

const (
	attrTarget = "u-erin"  // the impersonated account
	attrAdmin  = "u-alice" // the real site admin
	attrPVID   = "pv-attr-1"
	attrSource = "u-source"
)

// recordingSink is the terminal audit sink; it captures events AFTER the
// production impersonation decorator has run.
type recordingSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingSink) Emit(_ context.Context, ev audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingSink) only(t *testing.T) audit.Event {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 1 {
		t.Fatalf("got %d audit events, want exactly 1", len(r.events))
	}
	return r.events[0]
}

func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// attrStore is a minimal ack.Store: nothing is acked yet, and a transfer always
// reconciles one policy version so the emit gate (Moved+Deduped > 0) opens.
type attrStore struct{}

func (attrStore) Insert(_ context.Context, _, _ string) (ackpkg.RecordResult, error) {
	return ackpkg.RecordResult{ID: "ack-1"}, nil
}

func (attrStore) HasAcked(_ context.Context, _, _ string) (bool, error) { return false, nil }

func (attrStore) TransferAcks(_ context.Context, _, _ string, dryRun bool) (int, int, []ackpkg.TransferItem, error) {
	items := []ackpkg.TransferItem{{
		PolicyVersionID: attrPVID,
		SourceAckedAt:   time.Unix(0, 0).UTC(),
		Resolution:      "source_moved",
	}}
	if dryRun {
		return 1, 0, items, nil
	}
	return 1, 0, items, nil
}

// startAckWithProductionAudit stands the AckService up exactly as cmd/server
// does — the real claims interceptor, the real ack.Service, and the real
// PlatformAuditAdapter wrapped in the real impersonation sink — behind a real
// gRPC connection whose client carries the gateway's forwarding interceptor.
func startAckWithProductionAudit(t *testing.T) (obligationsv1.AckServiceClient, *recordingSink) {
	t.Helper()

	sink := &recordingSink{}
	auditAdapter := ackpkg.NewPlatformAuditAdapter(ackpkg.NewImpersonationSink(sink))
	svc := ackpkg.NewService(attrStore{}, auditAdapter)

	// The in-process connection has no mTLS identity to check, so this
	// server trusts every caller's forwarded actor.
	trustAll := func(context.Context, string) bool { return true }
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcactor.UnaryServerInterceptor(grpcactor.WithTrust(trustAll))))
	obligationsv1.RegisterAckServiceServer(srv, grpcsvc.NewAckHandler(svc, nil))

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///ackattr",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(grpcactor.UnaryClientInterceptor()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return obligationsv1.NewAckServiceClient(conn), sink
}

// gatewayCtx is the call context the gateway holds: the subject is the
// TARGET and, when impersonating, the real admin rides along as the
// impersonator, which the client interceptor puts on the wire.
func gatewayCtx(impersonating bool) context.Context {
	a := grpcactor.Actor{Subject: attrTarget}
	if impersonating {
		a.Impersonator = attrAdmin
	}
	return grpcactor.WithActor(context.Background(), a)
}

func transferReq(actor string, dryRun bool) *obligationsv1.TransferAcknowledgmentsRequest {
	return &obligationsv1.TransferAcknowledgmentsRequest{
		SourceUserId:     attrSource,
		TargetUserId:     attrTarget,
		ActorUserId:      actor,
		DryRun:           dryRun,
		MergeOperationId: "merge-1",
	}
}

// codedEntry is what a coded rejection must carry on the wire.
type codedEntry struct {
	Code     int
	Symbol   string
	GRPCCode codes.Code
}

var ackTransferActorRequired = codedEntry{errcodes.CodeAckTransferActorRequired, "ACK_TRANSFER_ACTOR_REQUIRED", codes.InvalidArgument}

// assertCode asserts err carries the given registry entry, read back through
// the real ErrorInfo transport rather than by string-matching the message.
func assertCode(t *testing.T, err error, want codedEntry) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s (Code %d), got nil error", want.Symbol, want.Code)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != want.GRPCCode {
		t.Errorf("gRPC code = %v, want %v (err: %v)", st.Code(), want.GRPCCode, err)
	}
	info, ok := apperrgrpc.FromStatus(st)
	if !ok {
		t.Fatalf("status carries no coded ErrorInfo, so ops cannot correlate it: %v", err)
	}
	if info.Symbol != want.Symbol {
		t.Errorf("symbol = %q, want %q", info.Symbol, want.Symbol)
	}
	if info.Code != want.Code {
		t.Errorf("codeNum = %d, want %d", info.Code, want.Code)
	}
}

// --- Part 1: ack.transferred must never emit an unattributed event ---

// TestTransferRejectsMissingActor: a real transfer
// with no actor must be REJECTED, not emitted: an ack.transferred naming nobody
// is unusable as evidence, and an acknowledgment is a legal attestation.
func TestTransferRejectsMissingActor(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	_, err := client.TransferAcknowledgments(gatewayCtx(false), transferReq("", false))
	assertCode(t, err, ackTransferActorRequired)

	if n := sink.count(); n != 0 {
		t.Errorf("%d audit events emitted on a rejected transfer, want 0 — "+
			"the point of the rejection is that no unattributed ack.transferred exists", n)
	}
}

// TestTransferRejectsBlankActor pins the whitespace case. " " is no more
// attributable than "", and actor_user_id is a plain proto3 string so "unset"
// and "" are indistinguishable on the wire — non-empty-after-trim is the
// strongest rule the contract supports.
func TestTransferRejectsBlankActor(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	_, err := client.TransferAcknowledgments(gatewayCtx(false), transferReq("   ", false))
	assertCode(t, err, ackTransferActorRequired)

	if n := sink.count(); n != 0 {
		t.Errorf("%d audit events emitted on a rejected transfer, want 0", n)
	}
}

// TestTransferDryRunExemptFromActor pins the deliberate exemption. A preview
// mutates nothing and emits no event, so there is no unattributed event to
// prevent — and identity's PreviewAccountMerge legitimately has no attestation
// to attribute. If this ever starts failing, the validation has been tightened
// in a way that breaks merge previews.
func TestTransferDryRunExemptFromActor(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	if _, err := client.TransferAcknowledgments(gatewayCtx(false), transferReq("", true)); err != nil {
		t.Fatalf("dry-run preview with no actor must be allowed, got: %v", err)
	}
	if n := sink.count(); n != 0 {
		t.Errorf("dry-run emitted %d audit events, want 0", n)
	}
}

// TestTransferWithActorIsAttributed is the positive case: a real transfer with
// an actor emits exactly one ack.transferred naming that actor.
func TestTransferWithActorIsAttributed(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	if _, err := client.TransferAcknowledgments(gatewayCtx(false), transferReq(attrAdmin, false)); err != nil {
		t.Fatalf("TransferAcknowledgments: %v", err)
	}

	ev := sink.only(t)
	if ev.Action != "ack.transferred" {
		t.Errorf("action = %q, want %q", ev.Action, "ack.transferred")
	}
	if ev.ActorUserID != attrAdmin {
		t.Errorf("actor = %q, want %q", ev.ActorUserID, attrAdmin)
	}
}

// TestTransferNonUUIDActorAccepted pins that the rule is non-empty and NOT a
// uuid check. On identity's toolbox (mTLS) path the actor is an operator label
// like "alice@<fingerprint>", not a platform uuid; rejecting it would break a
// legitimate caller while adding nothing to attributability.
func TestTransferNonUUIDActorAccepted(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	const operator = "alice@ab12cd34"
	if _, err := client.TransferAcknowledgments(gatewayCtx(false), transferReq(operator, false)); err != nil {
		t.Fatalf("operator-label actor must be accepted, got: %v", err)
	}
	if ev := sink.only(t); ev.ActorUserID != operator {
		t.Errorf("actor = %q, want %q", ev.ActorUserID, operator)
	}
}

// --- Part 2: the service must consume the forwarded actor ---

// TestRecordAckUnderImpersonationAttributesToAdmin: the admin's id exists
// ONLY in the client's call context, crosses a real gRPC connection as the
// forwarded actor, and must come back out on the audit event.
func TestRecordAckUnderImpersonationAttributesToAdmin(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	if _, err := client.RecordAck(gatewayCtx(true), &obligationsv1.RecordAckRequest{
		PolicyVersionId: attrPVID,
	}); err != nil {
		t.Fatalf("impersonated RecordAck: %v", err)
	}

	ev := sink.only(t)
	if ev.ActorUserID != attrAdmin {
		t.Fatalf("actor = %q, want the acting admin %q: an acknowledgment "+
			"recorded while an admin impersonates a user is attributed to the target, "+
			"with no record that an admin acted, and an acknowledgment is a legal attestation",
			ev.ActorUserID, attrAdmin)
	}
	// The ordering trap: applyImpersonation only REWRITES ActorUserID,
	// so if the emit site had left it empty the admin would be set correctly and
	// the impersonated TARGET would be silently dropped. This assertion is what
	// proves the actor was populated BEFORE the decorator ran.
	if got := ev.Attributes["impersonated_user_id"]; got != attrTarget {
		t.Fatalf("impersonated_user_id = %q, want the target %q — an empty value here "+
			"means the emit site did not populate ActorUserID before the impersonation "+
			"decorator ran, so the target was lost", got, attrTarget)
	}
}

// TestRecordAckWithoutImpersonationNamesTheUser guards the ordinary path: no
// impersonation means the acknowledging user owns the event and no
// impersonated_user_id is invented. Without this, the test above would still
// pass if the decorator attributed everything to an admin.
func TestRecordAckWithoutImpersonationNamesTheUser(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	if _, err := client.RecordAck(gatewayCtx(false), &obligationsv1.RecordAckRequest{
		PolicyVersionId: attrPVID,
	}); err != nil {
		t.Fatalf("RecordAck: %v", err)
	}

	ev := sink.only(t)
	if ev.ActorUserID != attrTarget {
		t.Errorf("actor = %q, want the acknowledging user %q", ev.ActorUserID, attrTarget)
	}
	if got, present := ev.Attributes["impersonated_user_id"]; present {
		t.Errorf("impersonated_user_id = %q on a non-impersonated ack, want absent", got)
	}
}

// TestTransferUnderImpersonationAttributesToAdmin covers the transfer path
// through the same seam. The request-body actor stays on the event's
// impersonated_user_id and the forwarded admin takes over as the actor, so both
// parties survive — which only works because the actor was validated non-empty.
func TestTransferUnderImpersonationAttributesToAdmin(t *testing.T) {
	client, sink := startAckWithProductionAudit(t)

	const bodyActor = "u-merge-operator"
	if _, err := client.TransferAcknowledgments(gatewayCtx(true), transferReq(bodyActor, false)); err != nil {
		t.Fatalf("impersonated TransferAcknowledgments: %v", err)
	}

	ev := sink.only(t)
	if ev.ActorUserID != attrAdmin {
		t.Errorf("actor = %q, want the acting admin %q", ev.ActorUserID, attrAdmin)
	}
	if got := ev.Attributes["impersonated_user_id"]; got != bodyActor {
		t.Errorf("impersonated_user_id = %q, want %q — the actor the caller supplied must "+
			"be preserved, not dropped", got, bodyActor)
	}
	// The transfer's own attributes must survive the rewrite.
	if got := ev.Attributes["merge_operation_id"]; got != "merge-1" {
		t.Errorf("merge_operation_id = %q, want %q", got, "merge-1")
	}
}

// TestImpersonationSinkIsWiredAtTheSeam is the blunt wiring guard: an
// undecorated sink passes the actor straight through and loses the admin, so
// assert the decorator actually changes attribution.
func TestImpersonationSinkIsWiredAtTheSeam(t *testing.T) {
	ctx := grpcactor.WithActor(context.Background(), grpcactor.Actor{Subject: attrTarget, Impersonator: attrAdmin})

	bare := &recordingSink{}
	if err := bare.Emit(ctx, audit.Event{Action: "ack.recorded", ActorUserID: attrTarget}); err != nil {
		t.Fatalf("bare sink: %v", err)
	}
	if got := bare.only(t).ActorUserID; got != attrTarget {
		t.Fatalf("precondition: an undecorated sink must pass the actor through, got %q", got)
	}

	decorated := &recordingSink{}
	if err := ackpkg.NewImpersonationSink(decorated).Emit(ctx, audit.Event{
		Action: "ack.recorded", ActorUserID: attrTarget,
	}); err != nil {
		t.Fatalf("decorated sink: %v", err)
	}
	ev := decorated.only(t)
	if ev.ActorUserID != attrAdmin || ev.Attributes["impersonated_user_id"] != attrTarget {
		t.Errorf("decorated sink gave actor=%q impersonated=%q, want %q / %q",
			ev.ActorUserID, ev.Attributes["impersonated_user_id"], attrAdmin, attrTarget)
	}
}

// TestNewImpersonationSinkNilPassthrough keeps the nil-sink contract the
// existing nil-auditor guards in cmd/server rely on.
func TestNewImpersonationSinkNilPassthrough(t *testing.T) {
	if got := ackpkg.NewImpersonationSink(nil); got != nil {
		t.Errorf("NewImpersonationSink(nil) = %v, want nil", got)
	}
}

// TestTransferRejectionUsesRegistryNotBareStatus pins that the new validation
// speaks the coded-error registry rather than a bare status string, so a
// support report of "Code 7006" is greppable to this site.
func TestTransferRejectionUsesRegistryNotBareStatus(t *testing.T) {
	client, _ := startAckWithProductionAudit(t)

	_, err := client.TransferAcknowledgments(gatewayCtx(false), transferReq("", false))
	if err == nil {
		t.Fatal("want a rejection")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("gRPC code = %v, want InvalidArgument", st.Code())
	}
	if _, ok := apperrgrpc.FromStatus(st); !ok {
		t.Error("rejection carries no coded ErrorInfo: ops cannot correlate it to a code")
	}
}
