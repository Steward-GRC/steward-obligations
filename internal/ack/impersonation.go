// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package ack

import (
	"context"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	grpcactor "github.com/Bugs5382/go-grpc-actor"

	"github.com/Steward-GRC/steward-obligations/internal/audit"
)

// applyImpersonation credits an audit event to the real admin during act-as.
// An acknowledgement is an attestation, so the trail has to say both who acted
// and whom they acted as: the admin becomes ActorUserID and the target, who
// stays the subject everywhere else, is kept in Attributes["impersonated_user_id"]
// (the key core, workflow and identity use). Without act-as the event is
// unchanged.
//
// It only rewrites ActorUserID, never fills it: the target is read from what
// the emit site put there. An emit site that leaves the actor empty would lose
// the target silently, so both emit sites guarantee one: ack.recorded takes it
// from the forwarded actor (ACK_AUTH_REQUIRED, 7003, otherwise) and
// ack.transferred from the request, which TransferAcknowledgments refuses when
// blank (ACK_TRANSFER_ACTOR_REQUIRED, 7006). The empty case is logged, so a
// new emit site that forgets is visible.
func applyImpersonation(ctx context.Context, ev audit.Event) audit.Event {
	a, ok := grpcactor.FromContext(ctx)
	if !ok || !a.Impersonated() {
		return ev
	}
	adminID := a.Impersonator
	target := ev.ActorUserID
	ev.ActorUserID = adminID
	if ev.Attributes == nil {
		ev.Attributes = map[string]string{}
	}
	ev.Attributes["impersonated_user_id"] = target
	if target == "" {
		l := logctx.From(ctx)
		l.Warn().
			Str("action", ev.Action).
			Str("admin_user_id", adminID).
			Msg("obligations: impersonated audit event had no actor to preserve; " +
				"impersonated_user_id is empty (populate ActorUserID at the emit site)")
	}
	return ev
}

// impersonationSink decorates an AuditSink so every emitted event passes
// through applyImpersonation. Wired once at the server seam (cmd/server), it is
// the single place impersonation attribution is applied, so both ack events —
// and any added later — inherit it without per-emit-site changes.
type impersonationSink struct{ inner AuditSink }

// NewImpersonationSink wraps inner so emitted events are attributed to the real
// admin when the forwarded actor is an impersonation. A nil
// inner returns nil so callers' existing nil-sink guards keep working.
func NewImpersonationSink(inner AuditSink) AuditSink {
	if inner == nil {
		return nil
	}
	return impersonationSink{inner: inner}
}

func (s impersonationSink) Emit(ctx context.Context, ev audit.Event) error {
	return s.inner.Emit(ctx, applyImpersonation(ctx, ev))
}
