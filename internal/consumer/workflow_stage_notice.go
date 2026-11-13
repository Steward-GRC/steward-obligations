// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// Notification kinds for the two stage notices the workflow sweeper publishes
// when a stage is past its due date. Both are ordinary workflow-category kinds
// in the notifpolicy taxonomy: the reminder is normal severity and the
// escalation to the category owners is high severity, never critical, so
// neither bypasses quiet hours (the in-app notice is exempt from quiet hours
// anyway and is always written).
const (
	kindWorkflowReminder  = "workflow-reminder"
	kindWorkflowEscalated = "workflow-escalated"

	eventApprovalReminder = "workflow.approval_reminder"
	eventStageEscalated   = "workflow.stage_escalated"
)

// approvalReminderEvent is the workflow.approval_reminder payload: one event per
// still-pending approver. N is the reminder sequence number for the seat (1 for
// the first reminder at the due date, then 2, 3, ... every remind_every_days).
// Ids only; names are resolved here.
type approvalReminderEvent struct {
	EventType       string `json:"event_type"`
	TaskID          string `json:"task_id"`
	PolicyID        string `json:"policy_id"`
	PolicyVersionID string `json:"policy_version_id"`
	StageIndex      int    `json:"stage_index"`
	StageName       string `json:"stage_name"`
	ApproverUserID  string `json:"approver_user_id"`
	WorkflowName    string `json:"workflow_name"`
	DueBy           string `json:"due_by"` // RFC3339, empty when unknown
	N               int    `json:"n"`
}

// stageEscalatedEvent is the workflow.stage_escalated payload: one event per
// category owner, sent once per stage. PendingCount is how many approvers are
// still undecided; no voter names travel in the event.
type stageEscalatedEvent struct {
	EventType       string `json:"event_type"`
	TaskID          string `json:"task_id"`
	PolicyID        string `json:"policy_id"`
	PolicyVersionID string `json:"policy_version_id"`
	StageIndex      int    `json:"stage_index"`
	StageName       string `json:"stage_name"`
	OwnerUserID     string `json:"owner_user_id"`
	WorkflowName    string `json:"workflow_name"`
	DueBy           string `json:"due_by"`
	PendingCount    int    `json:"pending_count"`
}

// InAppNoticeWriter persists one in-app notice (a row the inbox lists) of the
// given notification kind for a user. The notification store, through a small
// adapter in the composition root, satisfies it. nil disables the in-app write.
type InAppNoticeWriter interface {
	WriteNotice(ctx context.Context, userID, kind string, payload map[string]any) error
}

// stageNotice is one notice to one person, already reduced from either event.
type stageNotice struct {
	logPrefix string
	kind      string
	userID    string
	dedupID   string // email marker key; the in-app marker is dedupID + ":inapp"
	taskID    string
	policyID  string
	// vars builds the email template vars given the resolved recipient name and
	// policy title; payload builds the in-app notice given the policy title.
	vars    func(recipientName, policyTitle string) map[string]any
	payload func(policyTitle string) map[string]any
}

// stageNoticeDeps are the collaborators both consumers share.
type stageNoticeDeps struct {
	sender     Sender
	recipients RecipientResolver
	policies   PolicyTitleResolver // optional
	dedup      ApprovalDedupStore  // optional
	inApp      InAppNoticeWriter   // optional
}

// deliver runs the shared flow: durable dedup, the in-app notice (always,
// independent of the email outcome), then the email. The in-app write has its
// own marker so a retry after a failed email send does not write a second
// notice; the email marker is recorded only after a successful send, so a failed
// send is retried on redelivery. Permanent conditions (no resolvable recipient,
// no email address) are logged and swallowed; a transient send failure is
// returned so the delivery is retried.
func (d stageNoticeDeps) deliver(ctx context.Context, n stageNotice) error {
	logger := logctx.From(ctx)
	log := func(msg string) {
		logger.Debug().Str("task_id", n.taskID).Str("user_id", n.userID).Str("kind", n.kind).Msg(n.logPrefix + ": " + msg)
	}

	if d.dedup != nil {
		done, err := d.dedup.AlreadyNotified(ctx, n.dedupID, n.userID)
		if err != nil {
			logger.Warn().Err(err).Str("dedup_id", n.dedupID).Str("user_id", n.userID).
				Msg(n.logPrefix + ": dedup check failed; sending anyway")
		} else if done {
			log("already notified; skipping")
			return nil
		}
	}

	policyTitle := "a policy"
	if d.policies != nil && n.policyID != "" {
		if _, title, err := d.policies.PolicyDisplay(ctx, n.policyID); err != nil {
			logger.Debug().Err(err).Str("policy_id", n.policyID).Msg(n.logPrefix + ": policy display lookup failed; omitting title")
		} else if title != "" {
			policyTitle = title
		}
	}

	d.writeInApp(ctx, n, policyTitle)

	name, to, err := d.recipients.ResolveWelcomeRecipient(ctx, n.userID)
	if err != nil {
		logger.Warn().Err(err).Str("user_id", n.userID).Msg(n.logPrefix + ": resolve recipient failed; email skipped")
		return nil
	}
	if to == "" {
		logger.Warn().Str("user_id", n.userID).Msg(n.logPrefix + ": recipient has no email; email skipped")
		return nil
	}
	if name == "" {
		name = "there"
	}

	if err := d.sender.Send(ctx, n.kind, n.userID, to, n.dedupID, n.vars(name, policyTitle)); err != nil {
		logger.Error().Err(err).Str("task_id", n.taskID).Str("kind", n.kind).Msg(n.logPrefix + ": send failed")
		return fmt.Errorf("%s: send %q: %w", n.logPrefix, n.kind, err)
	}
	log("email sent")

	if d.dedup != nil {
		if err := d.dedup.MarkNotified(ctx, n.dedupID, n.userID); err != nil {
			logger.Warn().Err(err).Str("dedup_id", n.dedupID).Str("user_id", n.userID).
				Msg(n.logPrefix + ": mark-notified failed; a redelivery may re-send (mail Deduper backstops)")
		}
	}
	return nil
}

// writeInApp writes the in-app notice once per notice key. A failure is logged
// and never blocks the email.
func (d stageNoticeDeps) writeInApp(ctx context.Context, n stageNotice, policyTitle string) {
	if d.inApp == nil {
		return
	}
	logger := logctx.From(ctx)
	marker := n.dedupID + ":inapp"
	if d.dedup != nil {
		if done, err := d.dedup.AlreadyNotified(ctx, marker, n.userID); err == nil && done {
			logger.Trace().Str("dedup_id", marker).Str("user_id", n.userID).Msg(n.logPrefix + ": in-app notice already written")
			return
		}
	}
	if err := d.inApp.WriteNotice(ctx, n.userID, n.kind, n.payload(policyTitle)); err != nil {
		logger.Warn().Err(err).Str("task_id", n.taskID).Str("user_id", n.userID).Str("kind", n.kind).
			Msg(n.logPrefix + ": in-app notice write failed")
		return
	}
	logger.Debug().Str("task_id", n.taskID).Str("user_id", n.userID).Str("kind", n.kind).Msg(n.logPrefix + ": in-app notice written")
	if d.dedup != nil {
		if err := d.dedup.MarkNotified(ctx, marker, n.userID); err != nil {
			logger.Warn().Err(err).Str("dedup_id", marker).Msg(n.logPrefix + ": in-app marker failed; a redelivery may write a second notice")
		}
	}
}

// stageLabel falls back to a generic phrase so a notice never shows a blank stage.
func stageLabel(name string) string {
	if name == "" {
		return "the current step"
	}
	return name
}

func workflowLabel(name string) string {
	if name == "" {
		return "Policy approval"
	}
	return name
}

// ---------------------------------------------------------------------------
// workflow.approval_reminder consumer
// ---------------------------------------------------------------------------

// WorkflowReminderConsumer handles workflow.approval_reminder events and tells
// one pending approver that a decision is overdue ("workflow-reminder"). It is
// deduped per (task, approver, reminder number) so each numbered reminder goes
// out once per seat and a redelivery never repeats it.
type WorkflowReminderConsumer struct {
	deps           stageNoticeDeps
	approveURL     string
	preferencesURL string
}

// NewWorkflowReminderConsumer constructs a WorkflowReminderConsumer. policies,
// dedup and inApp may be nil.
func NewWorkflowReminderConsumer(sender Sender, recipients RecipientResolver, policies PolicyTitleResolver, dedup ApprovalDedupStore, inApp InAppNoticeWriter, approveURL, preferencesURL string) *WorkflowReminderConsumer {
	return &WorkflowReminderConsumer{
		deps:           stageNoticeDeps{sender: sender, recipients: recipients, policies: policies, dedup: dedup, inApp: inApp},
		approveURL:     approveURL,
		preferencesURL: preferencesURL,
	}
}

// Handle processes a single raw AMQP message body (JSON approvalReminderEvent).
func (c *WorkflowReminderConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)
	const prefix = "consumer/workflow_reminder"

	var evt approvalReminderEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("%s: unmarshal: %w", prefix, err)
	}
	if evt.EventType != "" && evt.EventType != eventApprovalReminder {
		logger.Warn().Str("event", evt.EventType).Msg(prefix + ": unexpected event; skipping")
		return nil
	}
	if evt.ApproverUserID == "" || evt.TaskID == "" || evt.N < 1 {
		logger.Warn().Str("task_id", evt.TaskID).Str("approver_user_id", evt.ApproverUserID).Int("n", evt.N).
			Msg(prefix + ": missing task_id or approver_user_id, or n below 1; skipping")
		return nil
	}
	logger.Debug().Str("task_id", evt.TaskID).Str("approver_user_id", evt.ApproverUserID).Int("n", evt.N).
		Msg(prefix + ": handling reminder")

	due := formatDueBy(evt.DueBy)
	return c.deps.deliver(ctx, stageNotice{
		logPrefix: prefix,
		kind:      kindWorkflowReminder,
		userID:    evt.ApproverUserID,
		dedupID:   fmt.Sprintf("%s:r%d", evt.TaskID, evt.N),
		taskID:    evt.TaskID,
		policyID:  evt.PolicyID,
		vars: func(recipientName, policyTitle string) map[string]any {
			vars := map[string]any{
				"recipientName":  recipientName,
				"policyTitle":    policyTitle,
				"workflowName":   workflowLabel(evt.WorkflowName),
				"stageName":      stageLabel(evt.StageName),
				"approveUrl":     c.approveURL,
				"preferencesUrl": c.preferencesURL,
			}
			if due != "" {
				vars["dueBy"] = due
			}
			return vars
		},
		payload: func(policyTitle string) map[string]any {
			meta := stageLabel(evt.StageName)
			if due != "" {
				meta += ", due " + due
			}
			return map[string]any{
				"title":             "Your approval is overdue: " + policyTitle,
				"meta":              meta,
				"actionHref":        c.approveURL,
				"actionLabel":       "Review",
				"task_id":           evt.TaskID,
				"policy_version_id": evt.PolicyVersionID,
				"stage_index":       evt.StageIndex,
				"reminder_number":   evt.N,
			}
		},
	})
}

// ---------------------------------------------------------------------------
// workflow.stage_escalated consumer
// ---------------------------------------------------------------------------

// WorkflowEscalationConsumer handles workflow.stage_escalated events and tells
// one category owner that a stage is still undecided well past its due date
// ("workflow-escalated"). It only notifies; it changes no seat and decides
// nothing. Deduped per (task, owner), so a stage escalates once per owner even
// if the event is published again.
type WorkflowEscalationConsumer struct {
	deps           stageNoticeDeps
	workflowURL    string
	preferencesURL string
}

// NewWorkflowEscalationConsumer constructs a WorkflowEscalationConsumer.
// policies, dedup and inApp may be nil.
func NewWorkflowEscalationConsumer(sender Sender, recipients RecipientResolver, policies PolicyTitleResolver, dedup ApprovalDedupStore, inApp InAppNoticeWriter, workflowURL, preferencesURL string) *WorkflowEscalationConsumer {
	return &WorkflowEscalationConsumer{
		deps:           stageNoticeDeps{sender: sender, recipients: recipients, policies: policies, dedup: dedup, inApp: inApp},
		workflowURL:    workflowURL,
		preferencesURL: preferencesURL,
	}
}

// Handle processes a single raw AMQP message body (JSON stageEscalatedEvent).
func (c *WorkflowEscalationConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)
	const prefix = "consumer/workflow_escalation"

	var evt stageEscalatedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("%s: unmarshal: %w", prefix, err)
	}
	if evt.EventType != "" && evt.EventType != eventStageEscalated {
		logger.Warn().Str("event", evt.EventType).Msg(prefix + ": unexpected event; skipping")
		return nil
	}
	if evt.OwnerUserID == "" || evt.TaskID == "" {
		logger.Warn().Str("task_id", evt.TaskID).Str("owner_user_id", evt.OwnerUserID).
			Msg(prefix + ": missing task_id or owner_user_id; skipping")
		return nil
	}
	logger.Debug().Str("task_id", evt.TaskID).Str("owner_user_id", evt.OwnerUserID).Int("pending_count", evt.PendingCount).
		Msg(prefix + ": handling escalation")

	due := formatDueBy(evt.DueBy)
	return c.deps.deliver(ctx, stageNotice{
		logPrefix: prefix,
		kind:      kindWorkflowEscalated,
		userID:    evt.OwnerUserID,
		dedupID:   evt.TaskID + ":escalated",
		taskID:    evt.TaskID,
		policyID:  evt.PolicyID,
		vars: func(recipientName, policyTitle string) map[string]any {
			vars := map[string]any{
				"recipientName":  recipientName,
				"policyTitle":    policyTitle,
				"workflowName":   workflowLabel(evt.WorkflowName),
				"stageName":      stageLabel(evt.StageName),
				"pendingCount":   evt.PendingCount,
				"workflowUrl":    c.workflowURL,
				"preferencesUrl": c.preferencesURL,
			}
			if due != "" {
				vars["dueBy"] = due
			}
			return vars
		},
		payload: func(policyTitle string) map[string]any {
			meta := stageLabel(evt.StageName)
			if due != "" {
				meta += ", was due " + due
			}
			return map[string]any{
				"title":             "Approval overdue: " + policyTitle,
				"meta":              meta,
				"actionHref":        c.workflowURL,
				"actionLabel":       "View",
				"task_id":           evt.TaskID,
				"policy_version_id": evt.PolicyVersionID,
				"stage_index":       evt.StageIndex,
				"pending_count":     evt.PendingCount,
			}
		},
	})
}
