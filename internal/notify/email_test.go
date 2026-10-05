// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notify"
)

// fakeEmailResolver returns a canned email or an error.
type fakeEmailResolver struct {
	email string
	err   error
}

func (f fakeEmailResolver) ResolveEmail(_ context.Context, _ string) (string, error) {
	return f.email, f.err
}

// recordedSend captures one call to a fakeSender's Send method.
type recordedSend struct {
	kind     string
	userID   string
	to       string
	dedupRef string
	vars     any
}

// fakeSender is a notify.Sender stub that records every Send call instead of
// hitting a render sidecar or SMTP relay.
type fakeSender struct {
	calls []recordedSend
	err   error
}

func (f *fakeSender) Send(_ context.Context, kind, userID, to, dedupRef string, vars any) error {
	f.calls = append(f.calls, recordedSend{kind: kind, userID: userID, to: to, dedupRef: dedupRef, vars: vars})
	return f.err
}

func TestEmailChannelSendAckReminderUsesPolicyAckReminderKind(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.calls) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(fs.calls))
	}
	call := fs.calls[0]
	if call.kind != "policy-ack-reminder" {
		t.Errorf("kind: got %q, want policy-ack-reminder", call.kind)
	}
	if call.to != "alice@example.com" {
		t.Errorf("to: got %q", call.to)
	}
	if call.userID != "u1" {
		t.Errorf("userID: got %q, want u1 (feeds mail.Sender's Suppress hook)", call.userID)
	}
	if call.dedupRef != "c1" {
		t.Errorf("dedupRef: got %q, want the payload's CampaignID c1", call.dedupRef)
	}

	vars, ok := call.vars.(map[string]any)
	if !ok {
		t.Fatalf("vars: got %T, want map[string]any", call.vars)
	}
	if vars["portalUrl"] != "https://policy.example.org/portal/acknowledgements" {
		t.Errorf("portalUrl: got %v", vars["portalUrl"])
	}
	if vars["preferencesUrl"] != "https://policy.example.org/portal/preferences" {
		t.Errorf("preferencesUrl: got %v", vars["preferencesUrl"])
	}
	policies, ok := vars["policies"].([]map[string]any)
	if !ok {
		t.Fatalf("policies: got %T, want []map[string]any", vars["policies"])
	}
	if len(policies) != 1 {
		t.Fatalf("expected 1 policy (derived from the payload), got %d", len(policies))
	}
	// The visible reference must NEVER be the version UUID: with no enriched
	// backlog item it is empty; the version id rides only in the ack link.
	if policies[0]["ref"] != "" {
		t.Errorf("policies[0].ref: got %v, want empty (never the version UUID)", policies[0]["ref"])
	}
	if ack, _ := policies[0]["ackUrl"].(string); !strings.Contains(ack, "pv1") {
		t.Errorf("policies[0].ackUrl: got %v, want it to contain the version id pv1", policies[0]["ackUrl"])
	}
	assertNonEmptyRecipientName(t, vars)
}

// assertNonEmptyRecipientName fails the test if vars["recipientName"] is
// missing, empty, or the literal string "undefined". Both the
// policy-ack-reminder and policy-escalation templates interpolate
// recipientName directly into the greeting ("Hi {recipientName},"), so an
// absent/blank value would render "Hi," and an unset JS var would render
// "Hi undefined,".
func assertNonEmptyRecipientName(t *testing.T, vars map[string]any) {
	t.Helper()
	name, ok := vars["recipientName"]
	if !ok {
		t.Fatal("recipientName: missing from vars")
	}
	s, ok := name.(string)
	if !ok || s == "" || s == "undefined" {
		t.Errorf("recipientName: got %v (%T), want a non-empty, non-\"undefined\" string", name, name)
	}
}

// TestEmailChannelAckReminderRecipientNameFallsBackWhenUnresolved documents
// the interim behavior: with no
// resolved display name available, the ack-reminder vars must still carry a
// graceful, non-empty recipientName rather than an empty string.
func TestEmailChannelAckReminderRecipientNameFallsBackWhenUnresolved(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := fs.calls[0].dedupRef; got != "pv1" {
		t.Errorf("dedupRef: got %q, want PolicyVersionID fallback pv1 (CampaignID unset)", got)
	}

	vars := fs.calls[0].vars.(map[string]any)
	assertNonEmptyRecipientName(t, vars)
	if vars["recipientName"] != "there" {
		t.Errorf("recipientName: got %v, want the graceful fallback %q", vars["recipientName"], "there")
	}
}

func TestEmailChannelSendAckReminderListSendsAllPolicies(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	payload := notify.AckReminderPayload{
		UserID: "u1",
		Policies: []notify.AckItem{
			{Ref: "POL-014 v3", Title: "Remote Access & VPN Use Policy", DueBy: "August 15, 2026", AckURL: "https://policy.example.org/ack/pol-014"},
			{Ref: "POL-031 v1", Title: "Data Retention & Disposal Policy", DueBy: "August 18, 2026", AckURL: "https://policy.example.org/ack/pol-031"},
			{Ref: "POL-009 v2", Title: "Acceptable Use Policy", DueBy: "August 20, 2026", AckURL: "https://policy.example.org/ack/pol-009"},
		},
	}
	if err := ec.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.calls) != 1 {
		t.Fatalf("expected exactly 1 Send call for the whole list (one email), got %d", len(fs.calls))
	}
	vars := fs.calls[0].vars.(map[string]any)
	policies := vars["policies"].([]map[string]any)
	if len(policies) != 3 {
		t.Fatalf("expected 3 policies in the list, got %d", len(policies))
	}
	for i, want := range payload.Policies {
		got := policies[i]
		if got["ref"] != want.Ref || got["title"] != want.Title || got["dueBy"] != want.DueBy || got["ackUrl"] != want.AckURL {
			t.Errorf("policies[%d]: got %+v, want %+v", i, got, want)
		}
	}
}

func TestEmailChannelSendEscalationUsesPolicyEscalationKind(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", CampaignID: "c1", Type: "escalation",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.calls) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(fs.calls))
	}
	call := fs.calls[0]
	if call.kind != "policy-escalation" {
		t.Errorf("kind: got %q, want policy-escalation", call.kind)
	}
	if call.dedupRef != "c1" {
		t.Errorf("dedupRef: got %q, want the payload's CampaignID c1", call.dedupRef)
	}
	vars := call.vars.(map[string]any)
	// policyRef must never be the version UUID; with no enriched Policies it is
	// empty (escalation template renders no reference row), and the version id
	// appears only in the ack link.
	if vars["policyRef"] != "" {
		t.Errorf("policyRef: got %v, want empty (never the version UUID)", vars["policyRef"])
	}
	if ack, _ := vars["ackUrl"].(string); !strings.Contains(ack, "pv1") {
		t.Errorf("ackUrl: got %v, want it to contain the version id pv1", vars["ackUrl"])
	}
	if vars["preferencesUrl"] != "https://policy.example.org/portal/preferences" {
		t.Errorf("preferencesUrl: got %v", vars["preferencesUrl"])
	}
	assertNonEmptyRecipientName(t, vars)
	if title, _ := vars["policyTitle"].(string); title == "" {
		t.Errorf("policyTitle: got %q, want a non-empty fallback (blank renders a blank hero title)", title)
	}
}

// TestEmailChannelEscalationRecipientNameAndPolicyTitleFallBackWhenUnresolved
// documents the interim behavior (pending real-name/title
// enrichment): with no resolved display name or policy title available, the
// escalation vars must still carry graceful, non-empty values rather than
// blanks (a blank recipientName/policyTitle renders "Hi," and a blank hero
// title).
func TestEmailChannelEscalationRecipientNameAndPolicyTitleFallBackWhenUnresolved(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", Type: "escalation",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	assertNonEmptyRecipientName(t, vars)
	if vars["recipientName"] != "there" {
		t.Errorf("recipientName: got %v, want the graceful fallback %q", vars["recipientName"], "there")
	}
	if vars["policyTitle"] == "" || vars["policyTitle"] == nil {
		t.Errorf("policyTitle: got %v, want a non-empty fallback", vars["policyTitle"])
	}
}

// TestEmailChannelEscalationPolicyTitlePassesThroughWhenPoliciesPopulated
// covers the best-effort pass-through: if a caller (e.g. a future
// grouping change) already populated Policies on an escalation payload, its
// title is used verbatim instead of the generic fallback.
func TestEmailChannelEscalationPolicyTitlePassesThroughWhenPoliciesPopulated(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", Type: "escalation",
		Policies: []notify.AckItem{{Ref: "POL-014 v3", Title: "Remote Access & VPN Use Policy"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	if vars["policyTitle"] != "Remote Access & VPN Use Policy" {
		t.Errorf("policyTitle: got %v, want pass-through from Policies[0].Title", vars["policyTitle"])
	}
}

func TestEmailChannelSkipsWorkflowEscalation(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", Type: "workflow_escalation",
	}); err != nil {
		t.Fatalf("Send: unexpected error for deferred workflow_escalation type: %v", err)
	}
	if len(fs.calls) != 0 {
		t.Errorf("expected no Sender.Send call for workflow_escalation (no branded template yet), got %d", len(fs.calls))
	}
}

func TestEmailChannelReturnsErrorWhenResolveFails(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{err: errors.New("identity down")},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")
	if err := ec.Send(context.Background(), notify.AckReminderPayload{UserID: "u1"}); err == nil {
		t.Errorf("expected error when resolver fails")
	}
	if len(fs.calls) != 0 {
		t.Errorf("expected no Sender.Send call when the resolver fails, got %d", len(fs.calls))
	}
}

func TestEmailChannelPropagatesSenderError(t *testing.T) {
	fs := &fakeSender{err: errors.New("sidecar/smtp boom")}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "a@b.example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")
	if err := ec.Send(context.Background(), notify.AckReminderPayload{UserID: "u1"}); err == nil {
		t.Errorf("expected sender error to propagate")
	}
}

func TestEmailChannelSendAckRequiredUsesAckRequiredKind(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID:          "u1",
		PolicyVersionID: "pv1",
		Type:            "ack_required",
		Policies:        []notify.AckItem{{Ref: "POL-014 v3", Title: "Remote Access & VPN Use Policy", DueBy: "August 15, 2026", AckURL: "https://policy.example.org/portal/acknowledgements/pv1"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.calls) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(fs.calls))
	}
	call := fs.calls[0]
	if call.kind != "ack-required" {
		t.Errorf("kind: got %q, want ack-required", call.kind)
	}
	if call.dedupRef != "pv1" {
		t.Errorf("dedupRef: got %q, want pv1", call.dedupRef)
	}
	vars, ok := call.vars.(map[string]any)
	if !ok {
		t.Fatalf("vars: got %T, want map[string]any", call.vars)
	}
	for _, key := range []string{"ackUrl", "bodyText", "itemTitle", "preferencesUrl", "recipientName"} {
		if _, present := vars[key]; !present {
			t.Errorf("ack-required vars missing required key %q", key)
		}
	}
	if vars["itemTitle"] != "Remote Access & VPN Use Policy" {
		t.Errorf("itemTitle: got %v, want the policy title", vars["itemTitle"])
	}
	if vars["ackUrl"] != "https://policy.example.org/portal/acknowledgements/pv1" {
		t.Errorf("ackUrl: got %v", vars["ackUrl"])
	}
	if vars["dueBy"] != "August 15, 2026" {
		t.Errorf("dueBy: got %v", vars["dueBy"])
	}
}

// TestEmailChannelAckRequiredOmitsDueByWhenAbsent covers ackRequired.tsx's
// optional `dueBy?: string`, which the template gates its due-date callout on
// with `dueBy !== undefined`: when the payload carries no due date, the
// builder must OMIT the "dueBy" key entirely (not set it to ""), or the
// sidecar would render "Please acknowledge by.".
func TestEmailChannelAckRequiredOmitsDueByWhenAbsent(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID:          "u1",
		PolicyVersionID: "pv1",
		Type:            "ack_required",
		Policies:        []notify.AckItem{{Ref: "POL-014 v3", Title: "Remote Access & VPN Use Policy", AckURL: "https://policy.example.org/portal/acknowledgements/pv1"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	if _, present := vars["dueBy"]; present {
		t.Errorf("dueBy: got present with value %v, want the key omitted when no due date is known", vars["dueBy"])
	}
}

func TestEmailChannelSendPolicyPublishedUsesPolicyPublishedKind(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID:          "u1",
		PolicyVersionID: "pv1",
		Type:            "policy_published",
		Policies:        []notify.AckItem{{Ref: "POL-022 v1", Title: "Acceptable Use of AI Tools Policy", AckURL: "https://policy.example.org/portal/acknowledgements/pv1"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.calls) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(fs.calls))
	}
	call := fs.calls[0]
	if call.kind != "policy-published" {
		t.Errorf("kind: got %q, want policy-published", call.kind)
	}
	if call.dedupRef != "pv1" {
		t.Errorf("dedupRef: got %q, want pv1 (per version+user dedup)", call.dedupRef)
	}
	vars, ok := call.vars.(map[string]any)
	if !ok {
		t.Fatalf("vars: got %T, want map[string]any", call.vars)
	}
	for _, key := range []string{"policyRef", "policyTitle", "policyUrl", "effectiveDate", "preferencesUrl", "recipientName", "requiresAck", "summary"} {
		if _, present := vars[key]; !present {
			t.Errorf("policy-published vars missing required key %q", key)
		}
	}
	if vars["policyTitle"] != "Acceptable Use of AI Tools Policy" {
		t.Errorf("policyTitle: got %v", vars["policyTitle"])
	}
	if vars["requiresAck"] != true {
		t.Errorf("requiresAck: got %v, want true", vars["requiresAck"])
	}
	// policyPublished.tsx declares effectiveDate as a required string that's
	// always rendered in an "Effective" row -- with no domain source for it
	// yet, the builder must supply a non-empty fallback rather than "" (a
	// blank value would render an empty "Effective:" row).
	if effectiveDate, _ := vars["effectiveDate"].(string); effectiveDate == "" {
		t.Errorf("effectiveDate: got %q, want a non-empty fallback", effectiveDate)
	}
}

// TestEmailChannelPublishedEffectiveDateFallsBackToNonEmptyPlaceholder locks
// in the exact no-domain-source behavior: with nothing populating an
// effective date yet, publishedVars must emit the fixed fallback copy, not an
// empty string.
func TestEmailChannelPublishedEffectiveDateFallsBackToNonEmptyPlaceholder(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", PolicyVersionID: "pv1", Type: "policy_published",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	if vars["effectiveDate"] != "the publish date" {
		t.Errorf("effectiveDate: got %v, want the graceful fallback %q", vars["effectiveDate"], "the publish date")
	}
}

// TestEmailChannelPublishedUsesResolvedEffectiveDate proves the regression fix
// for: when the consumer resolves a real effective date
// and stamps it on the payload, publishedVars substitutes it into the
// "Effective" row instead of leaving the "the publish date" placeholder, and
// the resolved human reference/title ride through instead of the version UUID.
func TestEmailChannelPublishedUsesResolvedEffectiveDate(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID:          "u1",
		PolicyVersionID: "pv1",
		Type:            "policy_published",
		EffectiveDate:   "September 1, 2026",
		Policies:        []notify.AckItem{{Ref: "POL-014 v3", Title: "Disaster Recovery"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	if vars["effectiveDate"] != "September 1, 2026" {
		t.Errorf("effectiveDate: got %v, want the resolved date %q", vars["effectiveDate"], "September 1, 2026")
	}
	if vars["effectiveDate"] == "the publish date" {
		t.Error("effectiveDate: must never render the unresolved placeholder when a real date is supplied")
	}
	// The resolved policy reference/name must ride through too — never the UUID.
	if vars["policyRef"] != "POL-014 v3" {
		t.Errorf("policyRef: got %v, want the resolved human reference", vars["policyRef"])
	}
	if vars["policyTitle"] != "Disaster Recovery" {
		t.Errorf("policyTitle: got %v, want the resolved policy name", vars["policyTitle"])
	}
}

// TestEmailChannelSendPolicyRetiredUsesPolicyRetiredKind verifies a
// policy_retired-typed payload routes to the policy-retired template kind with
// the expected vars, carrying the consumer-stamped human ref/title (never a raw
// id).
func TestEmailChannelSendPolicyRetiredUsesPolicyRetiredKind(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://policy.example.org/portal/acknowledgements",
		"https://policy.example.org/portal/preferences")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID:   "u1",
		Type:     "policy_retired",
		Policies: []notify.AckItem{{Ref: "POL-022 v1", Title: "Acceptable Use of AI Tools Policy"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.calls) != 1 {
		t.Fatalf("expected 1 Send call, got %d", len(fs.calls))
	}
	call := fs.calls[0]
	if call.kind != "policy-retired" {
		t.Errorf("kind: got %q, want policy-retired", call.kind)
	}
	vars, ok := call.vars.(map[string]any)
	if !ok {
		t.Fatalf("vars: got %T, want map[string]any", call.vars)
	}
	for _, key := range []string{"policyRef", "policyTitle", "policyUrl", "retiredDate", "preferencesUrl", "recipientName", "summary"} {
		if _, present := vars[key]; !present {
			t.Errorf("policy-retired vars missing required key %q", key)
		}
	}
	if vars["policyRef"] != "POL-022 v1" {
		t.Errorf("policyRef: got %v, want the stamped human reference", vars["policyRef"])
	}
	if vars["policyTitle"] != "Acceptable Use of AI Tools Policy" {
		t.Errorf("policyTitle: got %v", vars["policyTitle"])
	}
	// retiredDate always renders a "Retired" row, so it must be non-empty even
	// when no date was supplied (falls back to a non-empty placeholder).
	if rd, _ := vars["retiredDate"].(string); rd == "" {
		t.Errorf("retiredDate: got %q, want a non-empty fallback", rd)
	}
}

// TestEmailChannelRetiredRefFallsBackToEmpty verifies that with no stamped
// reference, policyRef falls back to EMPTY (never a raw id) while the other
// vars still carry non-empty fallbacks.
func TestEmailChannelRetiredRefFallsBackToEmpty(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID: "u1", Type: "policy_retired",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	if vars["policyRef"] != "" {
		t.Errorf("policyRef: got %v, want empty fallback (never a raw id)", vars["policyRef"])
	}
	if vars["policyTitle"] == "" {
		t.Error("policyTitle must fall back to a non-empty placeholder")
	}
	if vars["summary"] == "" {
		t.Error("summary must fall back to a non-empty placeholder")
	}
	// With no date supplied, retiredDate uses the non-empty fallback, never "".
	if rd, _ := vars["retiredDate"].(string); rd == "" {
		t.Errorf("retiredDate: got %q, want a non-empty fallback", rd)
	}
}

// TestEmailChannelRetiredUsesResolvedRetiredDate verifies that when the consumer
// stamps a real retirement date on the payload (from the event's retired_at),
// retiredVars substitutes it into the "Retired" row instead of the fallback.
func TestEmailChannelRetiredUsesResolvedRetiredDate(t *testing.T) {
	fs := &fakeSender{}
	ec := notify.NewEmailChannel(fs, fakeEmailResolver{email: "alice@example.com"},
		"https://portal.example.com/acks", "https://portal.example.com/prefs")

	if err := ec.Send(context.Background(), notify.AckReminderPayload{
		UserID:        "u1",
		Type:          "policy_retired",
		EffectiveDate: "August 25, 2026",
		Policies:      []notify.AckItem{{Ref: "POL-014 v3", Title: "Legacy VPN Policy"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	vars := fs.calls[0].vars.(map[string]any)
	if vars["retiredDate"] != "August 25, 2026" {
		t.Errorf("retiredDate: got %v, want the resolved date %q", vars["retiredDate"], "August 25, 2026")
	}
	if vars["retiredDate"] == "recently" {
		t.Error("retiredDate: must never render the fallback when a real date is supplied")
	}
}
