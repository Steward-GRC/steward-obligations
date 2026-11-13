// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/stretchr/testify/require"
)

// fakeInApp records the in-app notices written, keyed by user and kind.
type fakeInApp struct {
	notices []inAppNotice
	err     error
}

type inAppNotice struct {
	userID  string
	kind    string
	payload map[string]any
}

func (f *fakeInApp) WriteNotice(_ context.Context, userID, kind string, payload map[string]any) error {
	if f.err != nil {
		return f.err
	}
	f.notices = append(f.notices, inAppNotice{userID, kind, payload})
	return nil
}

const reminderEventBody = `{
  "event_type":"workflow.approval_reminder",
  "task_id":"pv-1:0",
  "policy_id":"pol-9",
  "policy_version_id":"pv-1",
  "stage_index":0,
  "stage_name":"Legal review",
  "approver_user_id":"u1",
  "workflow_name":"Policy review & publish",
  "due_by":"2026-08-04T00:00:00Z",
  "n":1
}`

const escalationEventBody = `{
  "event_type":"workflow.stage_escalated",
  "task_id":"pv-1:0",
  "policy_id":"pol-9",
  "policy_version_id":"pv-1",
  "stage_index":0,
  "stage_name":"Legal review",
  "owner_user_id":"own1",
  "workflow_name":"Policy review & publish",
  "due_by":"2026-08-04T00:00:00Z",
  "pending_count":2
}`

func newReminder(sender consumer.Sender, recips consumer.RecipientResolver, dedup consumer.ApprovalDedupStore, inApp consumer.InAppNoticeWriter) *consumer.WorkflowReminderConsumer {
	return consumer.NewWorkflowReminderConsumer(sender, recips, fakePolicies{"pol-9": "Data Retention Policy"}, dedup, inApp,
		"https://policy.example.org/workflows/approvals", "https://policy.example.org/prefs")
}

func newEscalation(sender consumer.Sender, recips consumer.RecipientResolver, dedup consumer.ApprovalDedupStore, inApp consumer.InAppNoticeWriter) *consumer.WorkflowEscalationConsumer {
	return consumer.NewWorkflowEscalationConsumer(sender, recips, fakePolicies{"pol-9": "Data Retention Policy"}, dedup, inApp,
		"https://policy.example.org/workflows", "https://policy.example.org/prefs")
}

func TestWorkflowReminder_SendsEmailAndInAppNotice(t *testing.T) {
	sender := &varsCapturingSender{}
	inApp := &fakeInApp{}
	recips := fakeRecipients{"u1": {name: "Carol Example", email: "carol@example.org"}}
	c := newReminder(sender, recips, newFakeDedup(), inApp)

	require.NoError(t, c.Handle(context.Background(), []byte(reminderEventBody)))

	require.Equal(t, 1, sender.calls)
	require.Equal(t, "workflow-reminder", sender.kind)
	require.Equal(t, "carol@example.org", sender.to)
	require.Equal(t, "pv-1:0:r1", sender.dedup, "dedupRef must carry the task and the reminder number")
	require.Equal(t, "Carol Example", sender.vars["recipientName"])
	require.Equal(t, "Data Retention Policy", sender.vars["policyTitle"])
	require.Equal(t, "Policy review & publish", sender.vars["workflowName"])
	require.Equal(t, "Legal review", sender.vars["stageName"])
	require.Equal(t, "August 4, 2026", sender.vars["dueBy"])
	require.Equal(t, "https://policy.example.org/workflows/approvals", sender.vars["approveUrl"])
	require.Equal(t, "https://policy.example.org/prefs", sender.vars["preferencesUrl"])

	require.Len(t, inApp.notices, 1)
	require.Equal(t, "u1", inApp.notices[0].userID)
	require.Equal(t, "workflow-reminder", inApp.notices[0].kind)
	require.Equal(t, "pv-1:0", inApp.notices[0].payload["task_id"])
	require.Equal(t, "https://policy.example.org/workflows/approvals", inApp.notices[0].payload["actionHref"])
}

func TestWorkflowReminder_OncePerSeatAndNumber(t *testing.T) {
	sender := &varsCapturingSender{}
	inApp := &fakeInApp{}
	recips := fakeRecipients{
		"u1": {name: "Carol", email: "carol@example.org"},
		"u2": {name: "Dana", email: "dana@example.org"},
	}
	c := newReminder(sender, recips, newFakeDedup(), inApp)
	ctx := context.Background()

	require.NoError(t, c.Handle(ctx, []byte(reminderEventBody)))
	require.NoError(t, c.Handle(ctx, []byte(reminderEventBody)), "redelivery of the same reminder")
	require.Equal(t, 1, sender.calls, "a redelivery must not re-email")
	require.Len(t, inApp.notices, 1, "a redelivery must not write a second notice")

	// The next reminder for the same seat is a new send.
	next := `{"event_type":"workflow.approval_reminder","task_id":"pv-1:0","approver_user_id":"u1","n":2}`
	require.NoError(t, c.Handle(ctx, []byte(next)))
	require.Equal(t, 2, sender.calls)
	require.Equal(t, "pv-1:0:r2", sender.dedup)
	require.Len(t, inApp.notices, 2)

	// Another seat on the same stage and number is independent.
	other := `{"event_type":"workflow.approval_reminder","task_id":"pv-1:0","approver_user_id":"u2","n":1}`
	require.NoError(t, c.Handle(ctx, []byte(other)))
	require.Equal(t, 3, sender.calls)
	require.Equal(t, "dana@example.org", sender.to)

	// Another stage of the same version is independent.
	stage := `{"event_type":"workflow.approval_reminder","task_id":"pv-1:1","approver_user_id":"u1","n":1}`
	require.NoError(t, c.Handle(ctx, []byte(stage)))
	require.Equal(t, 4, sender.calls)
}

func TestWorkflowReminder_SkipsBadEvents(t *testing.T) {
	cases := map[string]string{
		"wrong event":       `{"event_type":"workflow.stage_escalated","task_id":"t","approver_user_id":"u1","n":1}`,
		"missing task":      `{"event_type":"workflow.approval_reminder","approver_user_id":"u1","n":1}`,
		"missing approver":  `{"event_type":"workflow.approval_reminder","task_id":"t","n":1}`,
		"zero sequence":     `{"event_type":"workflow.approval_reminder","task_id":"t","approver_user_id":"u1","n":0}`,
		"missing sequence":  `{"event_type":"workflow.approval_reminder","task_id":"t","approver_user_id":"u1"}`,
		"negative sequence": `{"event_type":"workflow.approval_reminder","task_id":"t","approver_user_id":"u1","n":-1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sender := &varsCapturingSender{}
			inApp := &fakeInApp{}
			c := newReminder(sender, fakeRecipients{"u1": {name: "C", email: "c@example.org"}}, newFakeDedup(), inApp)
			require.NoError(t, c.Handle(context.Background(), []byte(body)))
			require.Zero(t, sender.calls)
			require.Empty(t, inApp.notices)
		})
	}
}

func TestWorkflowReminder_MalformedJSONIsAnError(t *testing.T) {
	c := newReminder(&varsCapturingSender{}, fakeRecipients{}, newFakeDedup(), &fakeInApp{})
	require.Error(t, c.Handle(context.Background(), []byte("{not json")))
}

// A missing email skips the email but never the in-app notice.
func TestWorkflowReminder_NoEmailStillWritesInAppNotice(t *testing.T) {
	for name, recips := range map[string]fakeRecipients{
		"unresolvable": {},
		"no email":     {"u1": {name: "Carol", email: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			sender := &varsCapturingSender{}
			inApp := &fakeInApp{}
			c := newReminder(sender, recips, newFakeDedup(), inApp)
			require.NoError(t, c.Handle(context.Background(), []byte(reminderEventBody)))
			require.Zero(t, sender.calls)
			require.Len(t, inApp.notices, 1)
		})
	}
}

// A transient send failure is propagated for retry, and the retry neither
// loses the email nor writes a second in-app notice.
func TestWorkflowReminder_SendFailureRetriesWithoutDuplicateNotice(t *testing.T) {
	sender := &varsCapturingSender{err: errors.New("smtp down")}
	inApp := &fakeInApp{}
	dedup := newFakeDedup()
	c := newReminder(sender, fakeRecipients{"u1": {name: "Carol", email: "carol@example.org"}}, dedup, inApp)
	ctx := context.Background()

	require.Error(t, c.Handle(ctx, []byte(reminderEventBody)))
	require.Len(t, inApp.notices, 1)
	done, _ := dedup.AlreadyNotified(ctx, "pv-1:0:r1", "u1")
	require.False(t, done, "a failed send must leave no marker")

	sender.err = nil
	require.NoError(t, c.Handle(ctx, []byte(reminderEventBody)))
	require.Equal(t, 1, sender.calls, "the retry sends the email")
	require.Len(t, inApp.notices, 1, "the retry must not write a second notice")
}

func TestWorkflowReminder_InAppFailureDoesNotBlockEmail(t *testing.T) {
	sender := &varsCapturingSender{}
	inApp := &fakeInApp{err: errors.New("db down")}
	c := newReminder(sender, fakeRecipients{"u1": {name: "Carol", email: "carol@example.org"}}, newFakeDedup(), inApp)
	require.NoError(t, c.Handle(context.Background(), []byte(reminderEventBody)))
	require.Equal(t, 1, sender.calls)
}

func TestWorkflowReminder_NilDedupAndInAppAreAllowed(t *testing.T) {
	sender := &varsCapturingSender{}
	c := consumer.NewWorkflowReminderConsumer(sender, fakeRecipients{"u1": {name: "C", email: "c@example.org"}}, nil, nil, nil, "a", "p")
	require.NoError(t, c.Handle(context.Background(), []byte(reminderEventBody)))
	require.Equal(t, 1, sender.calls)
	require.Equal(t, "a policy", sender.vars["policyTitle"])
}

func TestWorkflowReminder_NoDueDateOmitsDueBy(t *testing.T) {
	sender := &varsCapturingSender{}
	c := newReminder(sender, fakeRecipients{"u1": {name: "C", email: "c@example.org"}}, newFakeDedup(), &fakeInApp{})
	body := `{"event_type":"workflow.approval_reminder","task_id":"t","approver_user_id":"u1","n":1}`
	require.NoError(t, c.Handle(context.Background(), []byte(body)))
	_, has := sender.vars["dueBy"]
	require.False(t, has)
}

func TestWorkflowEscalation_SendsToOwnerWithPendingCount(t *testing.T) {
	sender := &varsCapturingSender{}
	inApp := &fakeInApp{}
	recips := fakeRecipients{"own1": {name: "Olive Owner", email: "olive@example.org"}}
	c := newEscalation(sender, recips, newFakeDedup(), inApp)

	require.NoError(t, c.Handle(context.Background(), []byte(escalationEventBody)))

	require.Equal(t, 1, sender.calls)
	require.Equal(t, "workflow-escalated", sender.kind)
	require.Equal(t, "olive@example.org", sender.to)
	require.Equal(t, "pv-1:0:escalated", sender.dedup)
	require.Equal(t, "Olive Owner", sender.vars["recipientName"])
	require.Equal(t, "Legal review", sender.vars["stageName"])
	require.Equal(t, "Data Retention Policy", sender.vars["policyTitle"])
	require.Equal(t, 2, sender.vars["pendingCount"])
	require.Equal(t, "August 4, 2026", sender.vars["dueBy"])
	require.Equal(t, "https://policy.example.org/workflows", sender.vars["workflowUrl"])

	require.Len(t, inApp.notices, 1)
	require.Equal(t, "own1", inApp.notices[0].userID)
	require.Equal(t, "workflow-escalated", inApp.notices[0].kind)
}

func TestWorkflowEscalation_OncePerOwnerAndStage(t *testing.T) {
	sender := &varsCapturingSender{}
	inApp := &fakeInApp{}
	recips := fakeRecipients{
		"own1": {name: "Olive", email: "olive@example.org"},
		"own2": {name: "Omar", email: "omar@example.org"},
	}
	c := newEscalation(sender, recips, newFakeDedup(), inApp)
	ctx := context.Background()

	require.NoError(t, c.Handle(ctx, []byte(escalationEventBody)))
	require.NoError(t, c.Handle(ctx, []byte(escalationEventBody)), "redelivery or a re-published escalation")
	require.Equal(t, 1, sender.calls)
	require.Len(t, inApp.notices, 1)

	// A changed pending count on a re-publish must not escalate again.
	again := `{"event_type":"workflow.stage_escalated","task_id":"pv-1:0","owner_user_id":"own1","pending_count":1}`
	require.NoError(t, c.Handle(ctx, []byte(again)))
	require.Equal(t, 1, sender.calls)

	// A second owner of the same stage is told once.
	second := `{"event_type":"workflow.stage_escalated","task_id":"pv-1:0","owner_user_id":"own2","pending_count":2}`
	require.NoError(t, c.Handle(ctx, []byte(second)))
	require.Equal(t, 2, sender.calls)
	require.Equal(t, "omar@example.org", sender.to)

	// Another stage escalates independently.
	stage := `{"event_type":"workflow.stage_escalated","task_id":"pv-1:1","owner_user_id":"own1","pending_count":1}`
	require.NoError(t, c.Handle(ctx, []byte(stage)))
	require.Equal(t, 3, sender.calls)
}

func TestWorkflowEscalation_SkipsBadEventsAndHandlesMissingEmail(t *testing.T) {
	cases := map[string]string{
		"wrong event":   `{"event_type":"workflow.approval_reminder","task_id":"t","owner_user_id":"own1"}`,
		"missing task":  `{"event_type":"workflow.stage_escalated","owner_user_id":"own1"}`,
		"missing owner": `{"event_type":"workflow.stage_escalated","task_id":"t"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sender := &varsCapturingSender{}
			inApp := &fakeInApp{}
			c := newEscalation(sender, fakeRecipients{"own1": {name: "O", email: "o@example.org"}}, newFakeDedup(), inApp)
			require.NoError(t, c.Handle(context.Background(), []byte(body)))
			require.Zero(t, sender.calls)
			require.Empty(t, inApp.notices)
		})
	}

	sender := &varsCapturingSender{}
	inApp := &fakeInApp{}
	c := newEscalation(sender, fakeRecipients{}, newFakeDedup(), inApp)
	require.NoError(t, c.Handle(context.Background(), []byte(escalationEventBody)))
	require.Zero(t, sender.calls)
	require.Len(t, inApp.notices, 1, "the in-app notice does not depend on an email address")

	require.Error(t, c.Handle(context.Background(), []byte("{")))
}

func TestWorkflowEscalation_SendFailureRetries(t *testing.T) {
	sender := &varsCapturingSender{err: errors.New("smtp down")}
	inApp := &fakeInApp{}
	c := newEscalation(sender, fakeRecipients{"own1": {name: "O", email: "o@example.org"}}, newFakeDedup(), inApp)
	ctx := context.Background()
	require.Error(t, c.Handle(ctx, []byte(escalationEventBody)))
	sender.err = nil
	require.NoError(t, c.Handle(ctx, []byte(escalationEventBody)))
	require.Equal(t, 1, sender.calls)
	require.Len(t, inApp.notices, 1)
}
