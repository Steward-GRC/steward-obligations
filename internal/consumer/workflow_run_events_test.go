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

// These tests reuse the fakes defined in workflow_approval_test.go
// (varsCapturingSender, fakeRecipients, fakePolicies, fakeDedup) — same package.

const startedEventBody = `{
  "event_type":"workflow.started",
  "run_id":"run-1",
  "policy_id":"pol-9",
  "policy_version_id":"pv-1",
  "submitted_by_user_id":"sub-1",
  "workflow_name":"Policy review & publish",
  "current_step":"Legal review"
}`

const deniedEventBody = `{
  "event_type":"workflow.denied",
  "run_id":"run-1",
  "policy_id":"pol-9",
  "policy_version_id":"pv-1",
  "submitted_by_user_id":"sub-1",
  "reviewed_by_user_id":"rev-1",
  "workflow_name":"Policy review & publish",
  "reason":"Section 4 conflicts with the retention schedule."
}`

// ---------------------------------------------------------------------------
// workflow.started
// ---------------------------------------------------------------------------

func TestWorkflowStarted_ResolvesSubmitterAndSends(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{"sub-1": {name: "Carol Example", email: "carol@example.org"}}
	pols := fakePolicies{"pol-9": "Data Retention & Disposal Policy"}
	dedup := newFakeDedup()

	c := consumer.NewWorkflowStartedConsumer(sender, recips, pols, dedup,
		"https://policy.example.org/workflows", "https://policy.example.org/prefs")

	require.NoError(t, c.Handle(context.Background(), []byte(startedEventBody)))

	require.Equal(t, 1, sender.calls)
	require.Equal(t, "workflow-started", sender.kind)
	require.Equal(t, "carol@example.org", sender.to)
	require.Equal(t, "run-1:started", sender.dedup, "dedupRef must be run_id:started")

	require.Equal(t, "Carol Example", sender.vars["recipientName"])
	require.Equal(t, "Carol Example", sender.vars["initiatedBy"], "submitter both initiated and receives")
	require.Equal(t, "Data Retention & Disposal Policy", sender.vars["policyTitle"])
	require.Equal(t, "Policy review & publish", sender.vars["workflowName"])
	require.Equal(t, "https://policy.example.org/workflows", sender.vars["workflowUrl"])
	require.Equal(t, "https://policy.example.org/prefs", sender.vars["preferencesUrl"])
	require.Equal(t, "Legal review", sender.vars["currentStep"])
}

func TestWorkflowStarted_NoCurrentStepOmitsRow(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{"sub-1": {name: "Jordan", email: "carol@example.org"}}
	body := `{"event_type":"workflow.started","run_id":"run-2","policy_id":"p","policy_version_id":"pv","submitted_by_user_id":"sub-1","workflow_name":"WF"}`
	c := consumer.NewWorkflowStartedConsumer(sender, recips, nil, newFakeDedup(), "a", "p")

	require.NoError(t, c.Handle(context.Background(), []byte(body)))
	require.Equal(t, 1, sender.calls)
	_, has := sender.vars["currentStep"]
	require.False(t, has, "no current_step → currentStep var must be omitted")
	require.Equal(t, "a policy", sender.vars["policyTitle"], "nil policy resolver → generic label, never a UUID")
}

func TestWorkflowStarted_DedupsPerRunAndRecipient(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{"sub-1": {name: "Jordan", email: "carol@example.org"}}
	dedup := newFakeDedup()
	c := consumer.NewWorkflowStartedConsumer(sender, recips, nil, dedup, "a", "p")

	require.NoError(t, c.Handle(context.Background(), []byte(startedEventBody)))
	require.NoError(t, c.Handle(context.Background(), []byte(startedEventBody)))
	require.Equal(t, 1, sender.calls, "a redelivery for the same run must be deduped")
}

func TestWorkflowStarted_MissingRecipientSkips(t *testing.T) {
	sender := &varsCapturingSender{}
	c := consumer.NewWorkflowStartedConsumer(sender, fakeRecipients{}, nil, newFakeDedup(), "a", "p")
	require.NoError(t, c.Handle(context.Background(), []byte(startedEventBody)))
	require.Equal(t, 0, sender.calls)
}

// ---------------------------------------------------------------------------
// workflow.denied
// ---------------------------------------------------------------------------

func TestWorkflowDenied_ResolvesSubmitterAndReviewerAndSends(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{
		"sub-1": {name: "Carol Example", email: "carol@example.org"},
		"rev-1": {name: "Bob Example", email: "bob@example.org"},
	}
	pols := fakePolicies{"pol-9": "Data Retention & Disposal Policy"}
	dedup := newFakeDedup()

	c := consumer.NewWorkflowDeniedConsumer(sender, recips, pols, dedup,
		"https://policy.example.org/workflows", "https://policy.example.org/prefs")

	require.NoError(t, c.Handle(context.Background(), []byte(deniedEventBody)))

	require.Equal(t, 1, sender.calls)
	require.Equal(t, "workflow-denied", sender.kind)
	require.Equal(t, "carol@example.org", sender.to, "denied email goes to the SUBMITTER")
	require.Equal(t, "run-1:denied", sender.dedup)

	require.Equal(t, "Carol Example", sender.vars["authorName"])
	require.Equal(t, "Bob Example", sender.vars["reviewedBy"])
	require.Equal(t, "Data Retention & Disposal Policy", sender.vars["policyTitle"])
	require.Equal(t, "Section 4 conflicts with the retention schedule.", sender.vars["reason"])
	require.Equal(t, "Policy review & publish", sender.vars["workflowName"])
	require.Equal(t, "https://policy.example.org/workflows", sender.vars["reviseUrl"])
}

func TestWorkflowDenied_EmptyReasonFallsBack(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{"sub-1": {name: "Jordan", email: "carol@example.org"}}
	body := `{"event_type":"workflow.denied","run_id":"run-3","policy_id":"p","policy_version_id":"pv","submitted_by_user_id":"sub-1","reviewed_by_user_id":"","workflow_name":"WF"}`
	c := consumer.NewWorkflowDeniedConsumer(sender, recips, nil, newFakeDedup(), "a", "p")

	require.NoError(t, c.Handle(context.Background(), []byte(body)))
	require.Equal(t, 1, sender.calls)
	require.NotEmpty(t, sender.vars["reason"], "empty reason must fall back to a non-empty message")
	require.Equal(t, "a reviewer", sender.vars["reviewedBy"], "unresolvable reviewer → generic label, never a UUID")
}

func TestWorkflowDenied_SendFailureIsRetryable(t *testing.T) {
	sender := &varsCapturingSender{err: errors.New("smtp down")}
	recips := fakeRecipients{"sub-1": {name: "Jordan", email: "carol@example.org"}, "rev-1": {name: "M", email: "m@example.org"}}
	dedup := newFakeDedup()
	c := consumer.NewWorkflowDeniedConsumer(sender, recips, nil, dedup, "a", "p")

	err := c.Handle(context.Background(), []byte(deniedEventBody))
	require.Error(t, err)
	already, _ := dedup.AlreadyNotified(context.Background(), "run-1:denied", "sub-1")
	require.False(t, already, "a failed send must not leave a dedup marker (would suppress the retry)")
}

// ---------------------------------------------------------------------------
// cross-kind: started + denied for the same run must NOT collide in the shared
// approval_notified dedup table.
// ---------------------------------------------------------------------------

func TestWorkflowRunEvents_StartedAndDeniedDoNotCollide(t *testing.T) {
	dedup := newFakeDedup()
	recips := fakeRecipients{"sub-1": {name: "Jordan", email: "carol@example.org"}, "rev-1": {name: "M", email: "m@example.org"}}

	startedSender := &varsCapturingSender{}
	started := consumer.NewWorkflowStartedConsumer(startedSender, recips, nil, dedup, "a", "p")
	require.NoError(t, started.Handle(context.Background(), []byte(startedEventBody)))

	deniedSender := &varsCapturingSender{}
	denied := consumer.NewWorkflowDeniedConsumer(deniedSender, recips, nil, dedup, "a", "p")
	require.NoError(t, denied.Handle(context.Background(), []byte(deniedEventBody)))

	require.Equal(t, 1, startedSender.calls, "started must send")
	require.Equal(t, 1, deniedSender.calls, "denied must still send for the same run (distinct dedup key)")
}
