// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// kindWorkflowAwaitingApproval is the branded template sent to an approver when
// a decision is waiting on them (render sidecar subject "Your approval is
// needed").
const kindWorkflowAwaitingApproval = "workflow-awaiting-approval"

// ---------------------------------------------------------------------------
// Wire-format types
// ---------------------------------------------------------------------------

// approvalRequestedEvent mirrors saga.ApprovalRequestedEvent published by the
// workflow service on the "jobs" exchange, routing key
// "workflow.approval_requested". It is a raw-JSON event (no proto/contracts
// type), matching the policy.published / membership.changed convention. The
// workflow carries only IDs plus the couple of display fields it already knows
// (workflow name, SLA deadline); the obligations service resolves the approver's
// email/name, the requester's name, and the policy title itself — the same
// division of labour as the policy.published consumer.
type approvalRequestedEvent struct {
	EventType         string `json:"event_type"`
	TaskID            string `json:"task_id"`
	PolicyID          string `json:"policy_id"`
	PolicyVersionID   string `json:"policy_version_id"`
	StageIndex        int    `json:"stage_index"`
	ApproverUserID    string `json:"approver_user_id"`
	RequestedByUserID string `json:"requested_by_user_id"`
	WorkflowName      string `json:"workflow_name"`
	DueBy             string `json:"due_by"` // RFC3339, empty when the stage has no SLA
}

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// RecipientResolver resolves a user_id to a display name + email address. The
// service's identity adapter (*identityGRPCAdapter.ResolveWelcomeRecipient)
// satisfies it — the SAME adapter the welcome-account send uses, so the
// approver's email comes from the one identity source of truth.
type RecipientResolver interface {
	ResolveWelcomeRecipient(ctx context.Context, userID string) (name, email string, err error)
}

// PolicyTitleResolver resolves a policy id to its human display number + title.
// *obligation.Resolver satisfies it (PolicyDisplay), reused as-is from the
// policy.published path.
type PolicyTitleResolver interface {
	PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error)
}

// ApprovalDedupStore is the durable per-(task, approver) dedup marker.
// *store.ApprovalNotifiedStore satisfies it. nil disables durable dedup (the
// mail Sender's own in-process Deduper still backstops within a replica).
type ApprovalDedupStore interface {
	AlreadyNotified(ctx context.Context, taskID, approverUserID string) (bool, error)
	MarkNotified(ctx context.Context, taskID, approverUserID string) error
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// WorkflowApprovalConsumer handles AMQP messages from the "jobs" exchange on
// the "workflow.approval_requested" routing key. For each event it resolves the
// assigned approver's email (via RecipientResolver), builds the branded
// template vars, and sends "workflow-awaiting-approval" to them — deduped per
// (task_id, approver_user_id) so the workflow's re-entrant assign_stage action,
// an idempotent re-submit, or a broker redelivery never re-emails the same
// approver for the same task.
//
// Modelled on PolicyPublishedConsumer (the obligations service resolves the recipient
// + display fields) and on the SSO/auth-recovery consumers (event → one
// template kind → Sender.Send). Permanent, un-retryable conditions (malformed
// event, no resolvable recipient) are logged and swallowed (return nil) rather
// than requeued forever; only a transient send failure is propagated so the
// delivery is retried.
type WorkflowApprovalConsumer struct {
	sender         Sender
	recipients     RecipientResolver
	policies       PolicyTitleResolver // optional; nil omits the policy title
	dedup          ApprovalDedupStore  // optional; nil disables durable dedup
	approveURL     string              // portal approvals-inbox URL (the CTA target)
	preferencesURL string              // portal email-preferences URL
}

// NewWorkflowApprovalConsumer constructs a WorkflowApprovalConsumer. policies
// and dedup may be nil.
func NewWorkflowApprovalConsumer(sender Sender, recipients RecipientResolver, policies PolicyTitleResolver, dedup ApprovalDedupStore, approveURL, preferencesURL string) *WorkflowApprovalConsumer {
	return &WorkflowApprovalConsumer{
		sender:         sender,
		recipients:     recipients,
		policies:       policies,
		dedup:          dedup,
		approveURL:     approveURL,
		preferencesURL: preferencesURL,
	}
}

// Handle processes a single raw AMQP message body (JSON approvalRequestedEvent).
func (c *WorkflowApprovalConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)

	var evt approvalRequestedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/workflow_approval: unmarshal: %w", err)
	}
	if evt.EventType != "" && evt.EventType != "workflow.approval_requested" {
		logger.Warn().Str("event", evt.EventType).Msg("consumer/workflow_approval: unexpected event; skipping")
		return nil
	}
	if evt.ApproverUserID == "" || evt.TaskID == "" {
		logger.Warn().Str("task_id", evt.TaskID).Str("approver_user_id", evt.ApproverUserID).
			Msg("consumer/workflow_approval: missing task_id or approver_user_id; skipping")
		return nil
	}

	// Durable dedup FIRST: cheapest gate, and it short-circuits a re-emit before
	// any identity/core lookup runs.
	if c.dedup != nil {
		done, err := c.dedup.AlreadyNotified(ctx, evt.TaskID, evt.ApproverUserID)
		if err != nil {
			// A dedup-store read failure must not drop a legitimate notice;
			// fall through and send (the mail Sender's Deduper still backstops
			// a duplicate within this replica).
			logger.Warn().Err(err).Str("task_id", evt.TaskID).Str("approver_user_id", evt.ApproverUserID).
				Msg("consumer/workflow_approval: dedup check failed; sending anyway")
		} else if done {
			return nil
		}
	}

	// Resolve the approver's name + email from identity (the send target).
	recipientName, to, err := c.recipients.ResolveWelcomeRecipient(ctx, evt.ApproverUserID)
	if err != nil {
		logger.Warn().Err(err).Str("approver_user_id", evt.ApproverUserID).
			Msg("consumer/workflow_approval: resolve approver failed; skipping")
		return nil
	}
	if to == "" {
		logger.Warn().Str("approver_user_id", evt.ApproverUserID).
			Msg("consumer/workflow_approval: approver has no email; skipping")
		return nil
	}
	if recipientName == "" {
		recipientName = "there"
	}

	// Requester display name (best-effort). Falls back to a generic phrase so
	// the email never shows a raw user UUID.
	requestedBy := "a colleague"
	if evt.RequestedByUserID != "" {
		if name, _, err := c.recipients.ResolveWelcomeRecipient(ctx, evt.RequestedByUserID); err != nil {
			logger.Debug().Err(err).Str("user_id", evt.RequestedByUserID).
				Msg("consumer/workflow_approval: resolve requester failed; using generic label")
		} else if name != "" {
			requestedBy = name
		}
	}

	// Policy title (best-effort). Falls back to a generic phrase, never the UUID.
	policyTitle := "a policy"
	if c.policies != nil && evt.PolicyID != "" {
		if _, title, err := c.policies.PolicyDisplay(ctx, evt.PolicyID); err != nil {
			logger.Debug().Err(err).Str("policy_id", evt.PolicyID).
				Msg("consumer/workflow_approval: policy display lookup failed; omitting title")
		} else if title != "" {
			policyTitle = title
		}
	}

	workflowName := evt.WorkflowName
	if workflowName == "" {
		workflowName = "Policy approval"
	}

	vars := map[string]any{
		"recipientName":  recipientName,
		"requestedBy":    requestedBy,
		"policyTitle":    policyTitle,
		"workflowName":   workflowName,
		"approveUrl":     c.approveURL,
		"preferencesUrl": c.preferencesURL,
	}
	// Render the deadline callout only when the stage carries an SLA.
	if due := formatDueBy(evt.DueBy); due != "" {
		vars["dueBy"] = due
	}

	// dedupRef = task_id, so the Sender's own Deduper key
	// (approver:kind:task_id) matches the durable marker's grain.
	if err := c.sender.Send(ctx, kindWorkflowAwaitingApproval, evt.ApproverUserID, to, evt.TaskID, vars); err != nil {
		return fmt.Errorf("consumer/workflow_approval: send %q to %q: %w", kindWorkflowAwaitingApproval, to, err)
	}

	// Record the durable marker only AFTER a successful send, so a failed send
	// (propagated above) is retried on redelivery rather than suppressed.
	if c.dedup != nil {
		if err := c.dedup.MarkNotified(ctx, evt.TaskID, evt.ApproverUserID); err != nil {
			logger.Warn().Err(err).Str("task_id", evt.TaskID).Str("approver_user_id", evt.ApproverUserID).
				Msg("consumer/workflow_approval: mark-notified failed; a redelivery may re-send (mail Deduper backstops)")
		}
	}
	return nil
}

// formatDueBy renders an RFC3339 deadline into the human date the template
// expects ("August 4, 2026"). An empty or unparseable value yields "" so the
// deadline callout is simply omitted.
func formatDueBy(rfc3339 string) string {
	if rfc3339 == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	return t.Format("January 2, 2006")
}
