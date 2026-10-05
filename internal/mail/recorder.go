// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"strings"

	email "github.com/Bugs5382/go-email"

	"github.com/Steward-GRC/steward-obligations/internal/audit"
)

// AuditEmitter is the narrow seam AuditRecorder depends on for publishing
// audit events. *platform/audit.Emitter (already built in cmd/server's main
// for the ack service's own audit trail) satisfies it directly; tests inject
// a fake instead of standing up RabbitMQ.
type AuditEmitter interface {
	Emit(ctx context.Context, ev audit.Event) error
}

// AuditRecorder implements go-email's Recorder by emitting one audit-tier
// event per logical send. NewSender wires Record outside Retry (see its doc
// comment), so this fires exactly once per send with the final outcome, not
// once per delivery attempt, and never at all for a send the Suppress hook
// skipped (Record is nested inside Suppress in the chain).
type AuditRecorder struct {
	emitter AuditEmitter
}

// NewAuditRecorder returns an AuditRecorder that emits through e.
func NewAuditRecorder(e AuditEmitter) *AuditRecorder {
	return &AuditRecorder{emitter: e}
}

// actionSent/actionFailed are the audit Action values a Record call emits,
// mirroring the ack.recorded / ack-style naming already used elsewhere in
// this service's audit trail (see internal/ack/platform_audit.go).
const (
	actionEmailSent   = "email.sent"
	actionEmailFailed = "email.failed"
)

// Record implements email.Recorder. It reads kind, user_id, and message_id
// back off m.Meta (all stamped by Sender.Send) and the real recipient off
// the X-Dev-Original-Recipients header when devCatchAll has rewritten it (so
// a dev-environment send is still audited against its real, intended
// recipient rather than the catch-all mailbox). The original sendErr from
// the chain is not altered or swallowed by this method's own return -- Record
// (the go-email middleware) always returns the original sendErr to the
// caller regardless of what this returns.
func (r *AuditRecorder) Record(ctx context.Context, m *email.Message, sendErr error) error {
	kind, _ := m.Meta["kind"].(string)
	userID, _ := m.Meta["user_id"].(string)
	messageID, _ := m.Meta["message_id"].(string)

	action := actionEmailSent
	if sendErr != nil {
		action = actionEmailFailed
	}

	attrs := map[string]string{
		"kind":      kind,
		"recipient": strings.Join(auditRecipients(m), ","),
	}
	if messageID != "" {
		attrs["message_id"] = messageID
	}
	if sendErr != nil {
		attrs["error"] = sendErr.Error()
	}

	return r.emitter.Emit(ctx, audit.Event{
		Tier:        audit.TierAudit,
		Action:      action,
		ActorUserID: userID,
		Subject:     "email:" + kind,
		Attributes:  attrs,
	})
}

// auditRecipients returns the recipient(s) a send should be audited against:
// the pre-redirect, real recipients stamped into devCatchAllHeader by
// devCatchAll when it has rewritten To/Cc/Bcc to a dev catch-all mailbox, or
// m.Recipients otherwise.
func auditRecipients(m *email.Message) []string {
	if m.Headers != nil {
		if original, ok := m.Headers[devCatchAllHeader]; ok && original != "" {
			return strings.Split(original, ", ")
		}
	}
	return m.Recipients()
}
