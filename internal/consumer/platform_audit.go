// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"strconv"

	"github.com/Steward-GRC/steward-obligations/internal/audit"
)

// PlatformAckAudienceAuditor wraps a *platform/audit.Emitter so it satisfies
// the consumer.AckAudienceAuditor interface. Constructed in cmd/server/main.go
// via the same audit.Emitter used for ack.recorded events. The summary lands on
// routing key "audit.audit" (audit.TierAudit).
//
// It emits ONE "policy.ack_required" audit-tier event per published policy
// version whose ack audience is materialized — never one row per user — so a
// large audience does not flood the append-only audit hash chain.
type PlatformAckAudienceAuditor struct{ emitter *audit.Emitter }

// NewPlatformAckAudienceAuditor returns an adapter that emits ack-audience
// summary events through the supplied platform audit emitter.
func NewPlatformAckAudienceAuditor(e *audit.Emitter) *PlatformAckAudienceAuditor {
	return &PlatformAckAudienceAuditor{emitter: e}
}

// EmitAckAudience publishes a single audit-tier summary event for the ack
// audience of a published policy version. The subject is
// "policy_version:<versionUUID>" so audit consumers can correlate the event
// back to the published version.
func (a *PlatformAckAudienceAuditor) EmitAckAudience(ctx context.Context, s AckAudienceSummary) error {
	return a.emitter.Emit(ctx, audit.Event{
		Tier:    audit.TierAudit,
		Action:  "policy.ack_required",
		Subject: "policy_version:" + s.PolicyVersionID,
		GroupID: s.GroupID,
		Attributes: map[string]string{
			"policy_version_id": s.PolicyVersionID,
			"user_count":        strconv.Itoa(s.UserCount),
		},
	})
}
