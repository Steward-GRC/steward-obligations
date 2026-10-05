// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// Branded templates sent to the SUBMITTER when their approval run starts or is
// returned. Both already exist in the render sidecar (registry.tsx).
const (
	kindWorkflowStarted = "workflow-started"
	kindWorkflowDenied  = "workflow-denied"
)

// ---------------------------------------------------------------------------
// Wire-format types (mirror grpcsvc.RunStartedEvent / RunDeniedEvent published
// by the workflow service on the "jobs" exchange). Raw-JSON events, no
// proto/contracts type — the policy.published / workflow.approval_requested
// convention. The workflow carries only ids plus the display fields it already
// knows; the obligations service resolves the submitter's email/name, the reviewer's
// name, and the policy title itself.
// ---------------------------------------------------------------------------

// runStartedEvent is the workflow.started payload.
type runStartedEvent struct {
	EventType         string `json:"event_type"`
	RunID             string `json:"run_id"`
	PolicyID          string `json:"policy_id"`
	PolicyVersionID   string `json:"policy_version_id"`
	SubmittedByUserID string `json:"submitted_by_user_id"`
	WorkflowName      string `json:"workflow_name"`
	CurrentStep       string `json:"current_step"`
}

// runDeniedEvent is the workflow.denied payload.
type runDeniedEvent struct {
	EventType         string `json:"event_type"`
	RunID             string `json:"run_id"`
	PolicyID          string `json:"policy_id"`
	PolicyVersionID   string `json:"policy_version_id"`
	SubmittedByUserID string `json:"submitted_by_user_id"`
	ReviewedByUserID  string `json:"reviewed_by_user_id"`
	WorkflowName      string `json:"workflow_name"`
	Reason            string `json:"reason"`
}

// ---------------------------------------------------------------------------
// Shared resolution helpers (used by both run-event consumers). They mirror the
// best-effort resolution in the workflow.approval_requested consumer: never
// surface a raw UUID — fall back to a generic phrase.
// ---------------------------------------------------------------------------

// resolveDisplayName resolves a user id to a display name, returning fallback on
// any error / empty name. logCtx names the field for the debug log.
func resolveDisplayName(ctx context.Context, r RecipientResolver, userID, fallback, field string) string {
	if userID == "" {
		return fallback
	}
	name, _, err := r.ResolveWelcomeRecipient(ctx, userID)
	if err != nil {
		logger := logctx.From(ctx)
		logger.Debug().Err(err).Str(field, userID).Msg("consumer/workflow_run_events: resolve name failed; using generic label")
		return fallback
	}
	if name == "" {
		return fallback
	}
	return name
}

// resolvePolicyTitle resolves a policy id to its title, returning fallback on
// any error / empty title (never the raw UUID). nil resolver -> fallback.
func resolvePolicyTitle(ctx context.Context, p PolicyTitleResolver, policyID, fallback string) string {
	if p == nil || policyID == "" {
		return fallback
	}
	_, title, err := p.PolicyDisplay(ctx, policyID)
	if err != nil {
		logger := logctx.From(ctx)
		logger.Debug().Err(err).Str("policy_id", policyID).Msg("consumer/workflow_run_events: policy display lookup failed; using generic label")
		return fallback
	}
	if title == "" {
		return fallback
	}
	return title
}

// ---------------------------------------------------------------------------
// workflow.started consumer
// ---------------------------------------------------------------------------

// WorkflowStartedConsumer handles workflow.started events and sends the
// "workflow-started" email to the SUBMITTER. Modelled on
// WorkflowApprovalConsumer: resolve the recipient + display fields, send one
// template kind, dedup durably per (run, kind, recipient). Permanent,
// un-retryable conditions (malformed event, unresolvable recipient) are logged
// and swallowed; only a transient send failure is propagated for retry.
type WorkflowStartedConsumer struct {
	sender         Sender
	recipients     RecipientResolver
	policies       PolicyTitleResolver // optional; nil omits the policy title
	dedup          ApprovalDedupStore  // optional; nil disables durable dedup
	workflowURL    string              // "View workflow" CTA target
	preferencesURL string
}

// NewWorkflowStartedConsumer constructs a WorkflowStartedConsumer. policies and
// dedup may be nil.
func NewWorkflowStartedConsumer(sender Sender, recipients RecipientResolver, policies PolicyTitleResolver, dedup ApprovalDedupStore, workflowURL, preferencesURL string) *WorkflowStartedConsumer {
	return &WorkflowStartedConsumer{
		sender:         sender,
		recipients:     recipients,
		policies:       policies,
		dedup:          dedup,
		workflowURL:    workflowURL,
		preferencesURL: preferencesURL,
	}
}

// Handle processes a single raw AMQP message body (JSON runStartedEvent).
func (c *WorkflowStartedConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)

	var evt runStartedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/workflow_started: unmarshal: %w", err)
	}
	if evt.EventType != "" && evt.EventType != "workflow.started" {
		logger.Warn().Str("event", evt.EventType).Msg("consumer/workflow_started: unexpected event; skipping")
		return nil
	}
	if evt.SubmittedByUserID == "" || evt.RunID == "" {
		logger.Warn().Str("run_id", evt.RunID).Str("submitted_by", evt.SubmittedByUserID).
			Msg("consumer/workflow_started: missing run_id or submitted_by; skipping")
		return nil
	}

	// dedupID embeds the kind so the started + denied markers for one run never
	// collide in the shared approval_notified table (grain: run, kind, recipient).
	dedupID := evt.RunID + ":started"
	if c.dedup != nil {
		done, err := c.dedup.AlreadyNotified(ctx, dedupID, evt.SubmittedByUserID)
		if err != nil {
			logger.Warn().Err(err).Str("dedup_id", dedupID).Msg("consumer/workflow_started: dedup check failed; sending anyway")
		} else if done {
			return nil
		}
	}

	recipientName, to, err := c.recipients.ResolveWelcomeRecipient(ctx, evt.SubmittedByUserID)
	if err != nil {
		logger.Warn().Err(err).Str("submitted_by", evt.SubmittedByUserID).Msg("consumer/workflow_started: resolve submitter failed; skipping")
		return nil
	}
	if to == "" {
		logger.Warn().Str("submitted_by", evt.SubmittedByUserID).Msg("consumer/workflow_started: submitter has no email; skipping")
		return nil
	}
	if recipientName == "" {
		recipientName = "there"
	}

	workflowName := evt.WorkflowName
	if workflowName == "" {
		workflowName = "Policy approval"
	}

	vars := map[string]any{
		"recipientName": recipientName,
		// The submitter both initiated and receives the notice.
		"initiatedBy":    recipientName,
		"policyTitle":    resolvePolicyTitle(ctx, c.policies, evt.PolicyID, "a policy"),
		"workflowName":   workflowName,
		"workflowUrl":    c.workflowURL,
		"preferencesUrl": c.preferencesURL,
	}
	// Render the "Current step" row only when the workflow supplied one.
	if evt.CurrentStep != "" {
		vars["currentStep"] = evt.CurrentStep
	}

	if err := c.sender.Send(ctx, kindWorkflowStarted, evt.SubmittedByUserID, to, dedupID, vars); err != nil {
		return fmt.Errorf("consumer/workflow_started: send %q to %q: %w", kindWorkflowStarted, to, err)
	}
	if c.dedup != nil {
		if err := c.dedup.MarkNotified(ctx, dedupID, evt.SubmittedByUserID); err != nil {
			logger.Warn().Err(err).Str("dedup_id", dedupID).Msg("consumer/workflow_started: mark-notified failed; a redelivery may re-send (mail Deduper backstops)")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// workflow.denied consumer
// ---------------------------------------------------------------------------

// WorkflowDeniedConsumer handles workflow.denied events and sends the
// "workflow-denied" email back to the SUBMITTER with the reviewer's reason.
// Same shape/guarantees as WorkflowStartedConsumer.
type WorkflowDeniedConsumer struct {
	sender         Sender
	recipients     RecipientResolver
	policies       PolicyTitleResolver // optional; nil omits the policy title
	dedup          ApprovalDedupStore  // optional; nil disables durable dedup
	reviseURL      string              // "Revise & resubmit" CTA target
	preferencesURL string
}

// NewWorkflowDeniedConsumer constructs a WorkflowDeniedConsumer. policies and
// dedup may be nil.
func NewWorkflowDeniedConsumer(sender Sender, recipients RecipientResolver, policies PolicyTitleResolver, dedup ApprovalDedupStore, reviseURL, preferencesURL string) *WorkflowDeniedConsumer {
	return &WorkflowDeniedConsumer{
		sender:         sender,
		recipients:     recipients,
		policies:       policies,
		dedup:          dedup,
		reviseURL:      reviseURL,
		preferencesURL: preferencesURL,
	}
}

// Handle processes a single raw AMQP message body (JSON runDeniedEvent).
func (c *WorkflowDeniedConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)

	var evt runDeniedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/workflow_denied: unmarshal: %w", err)
	}
	if evt.EventType != "" && evt.EventType != "workflow.denied" {
		logger.Warn().Str("event", evt.EventType).Msg("consumer/workflow_denied: unexpected event; skipping")
		return nil
	}
	if evt.SubmittedByUserID == "" || evt.RunID == "" {
		logger.Warn().Str("run_id", evt.RunID).Str("submitted_by", evt.SubmittedByUserID).
			Msg("consumer/workflow_denied: missing run_id or submitted_by; skipping")
		return nil
	}

	dedupID := evt.RunID + ":denied"
	if c.dedup != nil {
		done, err := c.dedup.AlreadyNotified(ctx, dedupID, evt.SubmittedByUserID)
		if err != nil {
			logger.Warn().Err(err).Str("dedup_id", dedupID).Msg("consumer/workflow_denied: dedup check failed; sending anyway")
		} else if done {
			return nil
		}
	}

	authorName, to, err := c.recipients.ResolveWelcomeRecipient(ctx, evt.SubmittedByUserID)
	if err != nil {
		logger.Warn().Err(err).Str("submitted_by", evt.SubmittedByUserID).Msg("consumer/workflow_denied: resolve submitter failed; skipping")
		return nil
	}
	if to == "" {
		logger.Warn().Str("submitted_by", evt.SubmittedByUserID).Msg("consumer/workflow_denied: submitter has no email; skipping")
		return nil
	}
	if authorName == "" {
		authorName = "there"
	}

	workflowName := evt.WorkflowName
	if workflowName == "" {
		workflowName = "Policy approval"
	}
	reason := evt.Reason
	if reason == "" {
		reason = "No reason was provided. Please contact the reviewer for details."
	}

	vars := map[string]any{
		"authorName":     authorName,
		"policyTitle":    resolvePolicyTitle(ctx, c.policies, evt.PolicyID, "a policy"),
		"reviewedBy":     resolveDisplayName(ctx, c.recipients, evt.ReviewedByUserID, "a reviewer", "reviewed_by"),
		"reason":         reason,
		"reviseUrl":      c.reviseURL,
		"workflowName":   workflowName,
		"preferencesUrl": c.preferencesURL,
	}

	if err := c.sender.Send(ctx, kindWorkflowDenied, evt.SubmittedByUserID, to, dedupID, vars); err != nil {
		return fmt.Errorf("consumer/workflow_denied: send %q to %q: %w", kindWorkflowDenied, to, err)
	}
	if c.dedup != nil {
		if err := c.dedup.MarkNotified(ctx, dedupID, evt.SubmittedByUserID); err != nil {
			logger.Warn().Err(err).Str("dedup_id", dedupID).Msg("consumer/workflow_denied: mark-notified failed; a redelivery may re-send (mail Deduper backstops)")
		}
	}
	return nil
}
