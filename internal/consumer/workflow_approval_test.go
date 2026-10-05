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

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// varsCapturingSender records the kind, recipient, dedupRef, and vars of the last
// Send so the workflow-approval tests can assert on the rendered payload.
type varsCapturingSender struct {
	calls int
	kind  string
	to    string
	dedup string
	vars  map[string]any
	err   error
}

func (s *varsCapturingSender) Send(_ context.Context, kind, _ /*userID*/, to, dedupRef string, vars any) error {
	if s.err != nil {
		return s.err
	}
	s.calls++
	s.kind = kind
	s.to = to
	s.dedup = dedupRef
	if m, ok := vars.(map[string]any); ok {
		s.vars = m
	}
	return nil
}

type fakePerson struct{ name, email string }

// fakeRecipients satisfies consumer.RecipientResolver.
type fakeRecipients map[string]fakePerson

func (f fakeRecipients) ResolveWelcomeRecipient(_ context.Context, userID string) (string, string, error) {
	p, ok := f[userID]
	if !ok {
		return "", "", errors.New("unknown user")
	}
	return p.name, p.email, nil
}

// fakePolicies satisfies consumer.PolicyTitleResolver.
type fakePolicies map[string]string // policyID -> title

func (f fakePolicies) PolicyDisplay(_ context.Context, policyID string) (string, string, error) {
	return "POL-1", f[policyID], nil
}

// fakeDedup satisfies consumer.ApprovalDedupStore with an in-memory set.
type fakeDedup struct {
	seen     map[string]bool
	markErr  error
	checkErr error
}

func newFakeDedup() *fakeDedup { return &fakeDedup{seen: map[string]bool{}} }

func (d *fakeDedup) key(taskID, uid string) string { return taskID + "|" + uid }

func (d *fakeDedup) AlreadyNotified(_ context.Context, taskID, uid string) (bool, error) {
	if d.checkErr != nil {
		return false, d.checkErr
	}
	return d.seen[d.key(taskID, uid)], nil
}

func (d *fakeDedup) MarkNotified(_ context.Context, taskID, uid string) error {
	if d.markErr != nil {
		return d.markErr
	}
	d.seen[d.key(taskID, uid)] = true
	return nil
}

const approvalEventBody = `{
  "event_type":"workflow.approval_requested",
  "task_id":"pv-1:0",
  "policy_id":"pol-9",
  "policy_version_id":"pv-1",
  "stage_index":0,
  "approver_user_id":"u1",
  "requested_by_user_id":"boss",
  "workflow_name":"Policy review & publish",
  "due_by":"2026-08-04T00:00:00Z"
}`

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestWorkflowApproval_ResolvesApproverAndSends is the core path: the consumer
// resolves the approver's email via the identity adapter and sends
// workflow-awaiting-approval with the fully-resolved template vars.
func TestWorkflowApproval_ResolvesApproverAndSends(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{
		"u1":   {name: "Carol Example", email: "carol@example.org"},
		"boss": {name: "Bob Example", email: "bob@example.org"},
	}
	pols := fakePolicies{"pol-9": "Data Retention & Disposal Policy"}
	dedup := newFakeDedup()

	c := consumer.NewWorkflowApprovalConsumer(sender, recips, pols, dedup,
		"https://policy.example.org/workflows/approvals", "https://policy.example.org/prefs")

	require.NoError(t, c.Handle(context.Background(), []byte(approvalEventBody)))

	require.Equal(t, 1, sender.calls, "expected exactly one send")
	require.Equal(t, "workflow-awaiting-approval", sender.kind)
	require.Equal(t, "carol@example.org", sender.to)
	require.Equal(t, "pv-1:0", sender.dedup, "dedupRef must be the task id")

	require.Equal(t, "Carol Example", sender.vars["recipientName"])
	require.Equal(t, "Bob Example", sender.vars["requestedBy"])
	require.Equal(t, "Data Retention & Disposal Policy", sender.vars["policyTitle"])
	require.Equal(t, "Policy review & publish", sender.vars["workflowName"])
	require.Equal(t, "https://policy.example.org/workflows/approvals", sender.vars["approveUrl"])
	require.Equal(t, "https://policy.example.org/prefs", sender.vars["preferencesUrl"])
	require.Equal(t, "August 4, 2026", sender.vars["dueBy"], "RFC3339 due_by must render to a human date")
}

// TestWorkflowApproval_DedupsPerTaskAndApprover asserts a second delivery of the
// same (task, approver) — a re-emit / re-submit / redelivery — does NOT re-send.
func TestWorkflowApproval_DedupsPerTaskAndApprover(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{"u1": {name: "Jordan", email: "carol@example.org"}, "boss": {name: "Morgan", email: "m@example.org"}}
	dedup := newFakeDedup()
	c := consumer.NewWorkflowApprovalConsumer(sender, recips, nil, dedup, "a", "p")

	require.NoError(t, c.Handle(context.Background(), []byte(approvalEventBody)))
	require.NoError(t, c.Handle(context.Background(), []byte(approvalEventBody)))
	require.Equal(t, 1, sender.calls, "second delivery for same task+approver must be deduped")
}

// TestWorkflowApproval_MissingRecipientSkips asserts an unresolvable approver is
// skipped (no send) without erroring — a requeue would not help.
func TestWorkflowApproval_MissingRecipientSkips(t *testing.T) {
	sender := &varsCapturingSender{}
	c := consumer.NewWorkflowApprovalConsumer(sender, fakeRecipients{}, nil, newFakeDedup(), "a", "p")
	require.NoError(t, c.Handle(context.Background(), []byte(approvalEventBody)))
	require.Equal(t, 0, sender.calls)
}

// TestWorkflowApproval_SendFailureIsRetryable asserts a send failure is
// propagated (so the delivery is retried) AND the durable marker is NOT set, so
// the retry actually re-sends rather than being suppressed.
func TestWorkflowApproval_SendFailureIsRetryable(t *testing.T) {
	sender := &varsCapturingSender{err: errors.New("smtp down")}
	recips := fakeRecipients{"u1": {name: "Jordan", email: "carol@example.org"}, "boss": {name: "M", email: "m@example.org"}}
	dedup := newFakeDedup()
	c := consumer.NewWorkflowApprovalConsumer(sender, recips, nil, dedup, "a", "p")

	err := c.Handle(context.Background(), []byte(approvalEventBody))
	require.Error(t, err)
	already, _ := dedup.AlreadyNotified(context.Background(), "pv-1:0", "u1")
	require.False(t, already, "a failed send must not leave a dedup marker (would suppress the retry)")
}

// TestWorkflowApproval_NoDueByOmitsCallout asserts an event with no SLA deadline
// omits the dueBy var (the template then renders no deadline callout).
func TestWorkflowApproval_NoDueByOmitsCallout(t *testing.T) {
	sender := &varsCapturingSender{}
	recips := fakeRecipients{"u1": {name: "Jordan", email: "carol@example.org"}, "boss": {name: "M", email: "m@example.org"}}
	body := `{"event_type":"workflow.approval_requested","task_id":"pv-2:1","policy_id":"p","policy_version_id":"pv-2","approver_user_id":"u1","requested_by_user_id":"boss","workflow_name":"WF"}`
	c := consumer.NewWorkflowApprovalConsumer(sender, recips, nil, newFakeDedup(), "a", "p")

	require.NoError(t, c.Handle(context.Background(), []byte(body)))
	require.Equal(t, 1, sender.calls)
	_, hasDueBy := sender.vars["dueBy"]
	require.False(t, hasDueBy, "no SLA deadline → dueBy var must be omitted")
}
