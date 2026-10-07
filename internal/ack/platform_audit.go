// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package ack

import (
	"context"
	"strconv"

	"github.com/Steward-GRC/steward-obligations/internal/audit"
)

// AuditSink is the narrow seam PlatformAuditAdapter publishes through.
// *audit.Emitter satisfies it directly; tests inject a fake instead of
// standing up RabbitMQ, mirroring the same seam used by internal/mail's
// AuditRecorder. Holding the interface (rather than *audit.Emitter) is what
// makes the emitted audit.Event assertable in a unit test.
type AuditSink interface {
	Emit(ctx context.Context, ev audit.Event) error
}

// PlatformAuditAdapter wraps an audit emitter so it satisfies the
// ack.AuditEmitter interface. In production the emitter writes to the audit
// outbox in the acknowledgement's own transaction. Ack events land on routing key
// "audit.audit" (audit.TierAudit) so the audit log captures
// first-time acknowledgments as audit-tier evidence.
type PlatformAuditAdapter struct{ emitter AuditSink }

// NewPlatformAuditAdapter returns an adapter that emits ack.recorded events
// through the supplied audit emitter.
func NewPlatformAuditAdapter(e AuditSink) *PlatformAuditAdapter {
	return &PlatformAuditAdapter{emitter: e}
}

// EmitAck publishes an audit-tier event for a first-time acknowledgment. The
// subject is "acknowledgment:<id>" so audit consumers can correlate the
// emitted event back to the persisted ack row without an extra round-trip.
//
// ActorUserID names the user who acknowledged, sourced from the verified
// session claims via RecordInput -> RecordResult. This event previously carried
// no actor at all, which left the acknowledgment — the attestation that a named
// person read and accepted a policy — unattributable in the audit trail
// .
func (a *PlatformAuditAdapter) EmitAck(ctx context.Context, r RecordResult) error {
	return a.emitter.Emit(ctx, audit.Event{
		Tier:        audit.TierAudit,
		Action:      "ack.recorded",
		ActorUserID: r.UserID,
		Subject:     "acknowledgment:" + r.ID,
		Attributes: map[string]string{
			"policy_version_id": r.PolicyVersionID,
		},
	})
}

// EmitTransfer publishes a single audit-tier event recording an account-merge
// ack transfer. The subject is the target user ("user:<target>"); the actor is
// the admin who ran the merge, and the counts, the merge operation id and the
// calling service are carried as attributes so audit consumers can correlate
// the transfer back to the originating merge.
func (a *PlatformAuditAdapter) EmitTransfer(ctx context.Context, in TransferInput, res TransferResult) error {
	attrs := map[string]string{
		"source_user_id":     in.SourceUserID,
		"target_user_id":     in.TargetUserID,
		"moved":              strconv.Itoa(res.Moved),
		"deduped":            strconv.Itoa(res.Deduped),
		"merge_operation_id": in.MergeOperationID,
	}
	if in.Caller != "" {
		attrs["caller"] = in.Caller
	}
	return a.emitter.Emit(ctx, audit.Event{
		Tier:        audit.TierAudit,
		Action:      "ack.transferred",
		ActorUserID: in.ActorUserID,
		Subject:     "user:" + in.TargetUserID,
		Attributes:  attrs,
	})
}
