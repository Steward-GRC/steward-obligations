// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeObligation satisfies consumer.ObligationResolver. It answers both the
// obligation-metadata lookup and the RACI-resolved audience.
type fakeObligation struct {
	// resolutions maps policyID → PolicyObligation.
	resolutions map[string]obligation.PolicyObligation
	// audiences maps policyID → the RACI-resolved ack audience.
	audiences map[string][]obligation.User
	// audienceErr, when set for a policyID, is returned by AudienceUsers to
	// exercise the fail-loud path (nil/empty chain, missing home category).
	audienceErr map[string]error
	// myObligations maps userID → their full outstanding obligation set,
	// consumed by MyObligations to exercise per-persona consolidation. A nil
	// map (the zero value) makes MyObligations return (nil, nil) for every
	// user, which reproduces the pre-consolidation single-item payload.
	myObligations map[string][]obligation.ObligationItem
	// myObligationsErr, when set for a userID, is returned by MyObligations
	// to exercise the best-effort degrade-to-single-item path.
	myObligationsErr map[string]error
	// displays maps policyID → {number, title} returned by PolicyDisplay, the
	// human reference/title the consumer stamps onto email payloads.
	displays map[string]struct{ number, title string }
	// displayErr, when set for a policyID, makes PolicyDisplay fail (best-effort
	// path: the consumer proceeds with an empty reference).
	displayErr map[string]error
}

func (f *fakeObligation) PolicyDisplay(_ context.Context, policyID string) (string, string, error) {
	if err := f.displayErr[policyID]; err != nil {
		return "", "", err
	}
	d := f.displays[policyID]
	return d.number, d.title, nil
}

func (f *fakeObligation) ResolvePolicyObligation(ctx context.Context, policyID string) (obligation.PolicyObligation, error) {
	if o, ok := f.resolutions[policyID]; ok {
		return o, nil
	}
	return obligation.PolicyObligation{}, nil
}

func (f *fakeObligation) AudienceUsers(ctx context.Context, policyID string) ([]obligation.User, error) {
	if f.audienceErr != nil {
		if err, ok := f.audienceErr[policyID]; ok {
			return nil, err
		}
	}
	if f.audiences != nil {
		if users, ok := f.audiences[policyID]; ok {
			return users, nil
		}
	}
	return nil, nil
}

func (f *fakeObligation) MyObligations(ctx context.Context, userID string) ([]obligation.ObligationItem, error) {
	if f.myObligationsErr != nil {
		if err, ok := f.myObligationsErr[userID]; ok {
			return nil, err
		}
	}
	if f.myObligations != nil {
		return f.myObligations[userID], nil
	}
	return nil, nil
}

// fakeAcks satisfies consumer.AcksStore.
// ackedUsers maps policyVersionID → set of userIDs who have already acked.
type fakeAcks struct {
	ackedUsers map[string][]string // policyVersionID -> []userID
}

func (f *fakeAcks) AckedUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error) {
	if f.ackedUsers == nil {
		return nil, nil
	}
	return f.ackedUsers[policyVersionID], nil
}

// fakeNotifier satisfies consumer.Notifier and records every call, keeping
// ack (SendAckReminder) and informational (SendPolicyPublished) calls
// separate so tests can assert each path independently.
type fakeNotifier struct {
	calls     []notify.AckReminderPayload // SendAckReminder
	published []notify.AckReminderPayload // SendPolicyPublished
}

func (f *fakeNotifier) SendAckReminder(ctx context.Context, p notify.AckReminderPayload) {
	f.calls = append(f.calls, p)
}

func (f *fakeNotifier) SendPolicyPublished(ctx context.Context, p notify.AckReminderPayload) {
	f.published = append(f.published, p)
}

// fakeNotifiedTracker satisfies consumer.NotifiedTracker. seen records which
// (user, version) pairs have already been notified; the first call for a pair
// returns true (first), later calls return false (repeat).
type fakeNotifiedTracker struct {
	seen map[string]bool // key: user+"|"+version
	err  error
}

func (f *fakeNotifiedTracker) MarkNotifiedIfFirst(ctx context.Context, userID, versionID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	key := userID + "|" + versionID
	if f.seen[key] {
		return false, nil
	}
	f.seen[key] = true
	return true, nil
}

// fakeAuditor satisfies consumer.AckAudienceAuditor and records every summary.
type fakeAuditor struct {
	summaries []consumer.AckAudienceSummary
}

func (f *fakeAuditor) EmitAckAudience(ctx context.Context, s consumer.AckAudienceSummary) error {
	f.summaries = append(f.summaries, s)
	return nil
}

// fakeNewUserGate satisfies consumer.NewUserGate. skip maps userID → whether
// that user should be throttled this cycle.
type fakeNewUserGate struct {
	skip  map[string]bool
	err   map[string]error
	calls []string
}

func (f *fakeNewUserGate) ShouldSkipForNewUser(ctx context.Context, userID string) (bool, error) {
	f.calls = append(f.calls, userID)
	if f.err != nil {
		if err, ok := f.err[userID]; ok {
			return false, err
		}
	}
	return f.skip[userID], nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func publishedBody(t *testing.T, policyID, versionID string) []byte {
	t.Helper()
	evt := map[string]any{
		"event_type":   "policy.published",
		"published_at": time.Now().UTC(),
		"version": map[string]any{
			"PolicyID":  policyID,
			"VersionID": versionID,
		},
	}
	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal test event: %v", err)
	}
	return b
}

// publishedBodyWithDocType builds a policy.published event carrying an explicit
// version.DocumentType. An empty docType omits the key (the historical
// policy default); "procedure" marks a document excluded from the ack pipeline.
func publishedBodyWithDocType(t *testing.T, policyID, versionID, docType string) []byte {
	t.Helper()
	version := map[string]any{
		"PolicyID":  policyID,
		"VersionID": versionID,
	}
	if docType != "" {
		version["DocumentType"] = docType
	}
	b, err := json.Marshal(map[string]any{
		"event_type":   "policy.published",
		"published_at": time.Now().UTC(),
		"version":      version,
	})
	if err != nil {
		t.Fatalf("marshal test event: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPolicyPublishedConsumer_ProcedureGeneratesNoAcks is the P2 "no
// ack" boundary: a published PROCEDURE must produce ZERO ack obligations — no
// ack-required/reminder email, no informational fan-out, no audit summary — even
// when the (deliberately misconfigured) resolver WOULD obligate a full audience.
// The consumer must skip on the event's document_type BEFORE resolving any
// obligation, so none of the obligation machinery runs.
func TestPolicyPublishedConsumer_ProcedureGeneratesNoAcks(t *testing.T) {
	const (
		policyID  = "proc-1"
		versionID = "ver-9"
	)

	// The resolver is configured as if this were an obligating policy: if the
	// consumer failed to skip procedures, it WOULD send to the whole audience.
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}, {ID: "u2"}, {ID: "u3"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBodyWithDocType(t, policyID, versionID, "procedure")); err != nil {
		t.Fatalf("Handle(procedure): %v", err)
	}

	if len(notifier.calls) != 0 {
		t.Errorf("procedure must send 0 ack reminders, got %d: %+v", len(notifier.calls), notifier.calls)
	}
	if len(notifier.published) != 0 {
		t.Errorf("procedure must send 0 policy-published notices, got %d: %+v", len(notifier.published), notifier.published)
	}
	if len(auditor.summaries) != 0 {
		t.Errorf("procedure must emit 0 ack-audience summaries, got %d: %+v", len(auditor.summaries), auditor.summaries)
	}
}

// TestPolicyPublishedConsumer_PolicyDocTypeStillNotifies is the regression
// counterpart to the procedure gate: an event carrying an explicit
// document_type="policy" (and, by extension, the absent/default case) is
// unaffected and still obligates its RACI audience exactly as before.
func TestPolicyPublishedConsumer_PolicyDocTypeStillNotifies(t *testing.T) {
	const (
		policyID  = "pol-1"
		versionID = "ver-2"
	)

	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}, {ID: "u2"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBodyWithDocType(t, policyID, versionID, "policy")); err != nil {
		t.Fatalf("Handle(policy): %v", err)
	}

	if len(notifier.calls) != 2 {
		t.Fatalf("policy must still notify its 2-user audience, got %d: %+v", len(notifier.calls), notifier.calls)
	}
	if len(auditor.summaries) != 1 {
		t.Fatalf("policy must still emit 1 ack-audience summary, got %d", len(auditor.summaries))
	}
}

// TestPolicyPublishedConsumer_UppercaseProcedureDocTypeGeneratesNoAcks is the
//
//	defense-in-depth regression: stops core from emitting
//
// "policy.published" for procedures at all (procedures move to their own
// "procedure.published"/"procedure.retired" routing keys, which cn does not
// bind), and that new envelope stamps version.DocumentType as the uppercase
// "PROCEDURE" rather than 's lowercase "procedure". Should a
// procedure-shaped event ever land on the policy.published queue anyway
// (mis-route, legacy producer, redelivery from a stale binding), the
// case-insensitive guard in Handle must still catch it and produce ZERO
// notifications, exactly like the lowercase case.
func TestPolicyPublishedConsumer_UppercaseProcedureDocTypeGeneratesNoAcks(t *testing.T) {
	const (
		policyID  = "proc-2"
		versionID = "ver-10"
	)

	// Configured as if this WOULD obligate a full audience, so a failure to
	// gate on the uppercase spelling would be visible as unwanted sends.
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}, {ID: "u2"}, {ID: "u3"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBodyWithDocType(t, policyID, versionID, "PROCEDURE")); err != nil {
		t.Fatalf("Handle(PROCEDURE): %v", err)
	}

	if len(notifier.calls) != 0 {
		t.Errorf("uppercase PROCEDURE must send 0 ack reminders, got %d: %+v", len(notifier.calls), notifier.calls)
	}
	if len(notifier.published) != 0 {
		t.Errorf("uppercase PROCEDURE must send 0 policy-published notices, got %d: %+v", len(notifier.published), notifier.published)
	}
	if len(auditor.summaries) != 0 {
		t.Errorf("uppercase PROCEDURE must emit 0 ack-audience summaries, got %d: %+v", len(auditor.summaries), auditor.summaries)
	}
}

// TestPolicyPublishedConsumer_OnChangeNotifiesRACIAudience asserts that when a
// policy.published event arrives for a requires_ack+on_change policy, the
// consumer derives the audience from the RACI decision engine (AudienceUsers)
// and calls the notifier for every audience member who has NOT yet acked the
// current published version.
func TestPolicyPublishedConsumer_OnChangeNotifiesRACIAudience(t *testing.T) {
	const (
		policyID  = "pol-1"
		versionID = "ver-2"
	)

	// RACI audience: u1, u2, u3. u1 already acked; u2 + u3 are outstanding.
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {
				RequiresAck:        true,
				OnChange:           true,
				PublishedVersionID: versionID,
			},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}, {ID: "u2"}, {ID: "u3"}},
		},
	}
	acks := &fakeAcks{
		ackedUsers: map[string][]string{
			versionID: {"u1"}, // u1 already acked
		},
	}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Exactly ONE summary audit event describing the whole audience (not one
	// per user), regardless of how many are outstanding.
	if len(auditor.summaries) != 1 {
		t.Fatalf("expected exactly 1 ack-audience summary, got %d: %+v", len(auditor.summaries), auditor.summaries)
	}
	sum := auditor.summaries[0]
	if sum.PolicyVersionID != versionID {
		t.Errorf("summary PolicyVersionID: want %q, got %q", versionID, sum.PolicyVersionID)
	}
	if sum.UserCount != 3 {
		t.Errorf("summary UserCount: want 3, got %d", sum.UserCount)
	}

	// Expect exactly u2 and u3 to be notified (u1 already acked).
	if len(notifier.calls) != 2 {
		t.Fatalf("expected 2 notifier calls, got %d: %+v", len(notifier.calls), notifier.calls)
	}
	notified := make(map[string]bool)
	for _, c := range notifier.calls {
		notified[c.UserID] = true
		if c.PolicyVersionID != versionID {
			t.Errorf("payload PolicyVersionID: want %q, got %q", versionID, c.PolicyVersionID)
		}
		if c.Type != "bulk_sweep" {
			t.Errorf("payload Type: want %q, got %q", "bulk_sweep", c.Type)
		}
	}
	if !notified["u2"] {
		t.Error("expected u2 to be notified")
	}
	if !notified["u3"] {
		t.Error("expected u3 to be notified")
	}
}

// TestPolicyPublishedConsumer_StampsHumanPolicyRef verifies the consumer puts
// the policy's human display (number + title) onto BOTH the informational
// published fan-out and the ack-reminder fallback (when the persona backlog is
// empty), so emails never carry the raw policy version UUID as the reference.
func TestPolicyPublishedConsumer_StampsHumanPolicyRef(t *testing.T) {
	const (
		policyID  = "pol-1"
		versionID = "ver-2"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{policyID: {{ID: "u2"}}},
		// No myObligations for u2 → ack path hits the single-item fallback.
		displays: map[string]struct{ number, title string }{
			policyID: {number: "POL-9", title: "Data Retention"},
		},
	}
	notifier := &fakeNotifier{}
	c := consumer.NewPolicyPublishedConsumer(obl, &fakeAcks{}, notifier, &fakeAuditor{})
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.published) != 1 {
		t.Fatalf("expected 1 published send, got %d", len(notifier.published))
	}
	pub := notifier.published[0]
	if len(pub.Policies) != 1 || pub.Policies[0].Ref != "POL-9" || pub.Policies[0].Title != "Data Retention" {
		t.Fatalf("published payload should carry human ref/title, got %+v", pub.Policies)
	}
	if pub.Policies[0].Ref == versionID {
		t.Error("published ref must never be the version UUID")
	}

	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 ack-reminder send, got %d", len(notifier.calls))
	}
	ack := notifier.calls[0]
	if len(ack.Policies) != 1 || ack.Policies[0].Ref != "POL-9" {
		t.Errorf("ack-reminder fallback should carry the human ref POL-9, got %+v", ack.Policies)
	}
}

// TestPolicyPublishedConsumer_StampsEffectiveDate proves the regression fix for
// : the informational published send carries a real,
// human-readable effective date derived from the publish event's published_at
// timestamp — never the unresolved "the publish date" placeholder — alongside
// the resolved human ref/title (never the version UUID).
func TestPolicyPublishedConsumer_StampsEffectiveDate(t *testing.T) {
	const (
		policyID  = "pol-1"
		versionID = "ver-2"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{policyID: {{ID: "u2"}}},
		displays: map[string]struct{ number, title string }{
			policyID: {number: "POL-9", title: "Disaster Recovery"},
		},
	}
	notifier := &fakeNotifier{}
	c := consumer.NewPolicyPublishedConsumer(obl, &fakeAcks{}, notifier, &fakeAuditor{})

	// A publish event with a FIXED published_at so the formatted effective date
	// is deterministic.
	published := time.Date(2026, time.September, 1, 13, 30, 0, 0, time.UTC)
	evt := map[string]any{
		"event_type":   "policy.published",
		"published_at": published,
		"version": map[string]any{
			"PolicyID":  policyID,
			"VersionID": versionID,
		},
	}
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.published) != 1 {
		t.Fatalf("expected 1 published send, got %d", len(notifier.published))
	}
	pub := notifier.published[0]
	if pub.EffectiveDate != "September 1, 2026" {
		t.Errorf("published EffectiveDate: got %q, want %q (from event published_at)", pub.EffectiveDate, "September 1, 2026")
	}
	if pub.EffectiveDate == "the publish date" {
		t.Error("published EffectiveDate must never be the unresolved placeholder")
	}
	if len(pub.Policies) != 1 || pub.Policies[0].Ref != "POL-9" || pub.Policies[0].Title != "Disaster Recovery" {
		t.Fatalf("published payload should carry the resolved human ref/title, got %+v", pub.Policies)
	}
	if pub.Policies[0].Ref == versionID {
		t.Error("published ref must never be the version UUID")
	}
}

// TestPolicyPublishedConsumer_PrefersDistinctEffectiveDate proves the FULL
//
//	fix: when the publish event carries the policy's OWN
//
// distinct effective date, the published email renders THAT date in the
// "Effective" row, not the publish timestamp. The published_at here is a
// deliberately different day so a regression that used it instead would fail.
func TestPolicyPublishedConsumer_PrefersDistinctEffectiveDate(t *testing.T) {
	const (
		policyID  = "pol-1"
		versionID = "ver-2"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{policyID: {{ID: "u2"}}},
		displays: map[string]struct{ number, title string }{
			policyID: {number: "POL-9", title: "Disaster Recovery"},
		},
	}
	notifier := &fakeNotifier{}
	c := consumer.NewPolicyPublishedConsumer(obl, &fakeAcks{}, notifier, &fakeAuditor{})

	// published_at (the transition time) is January 5; the policy's DISTINCT
	// effective date is a different day (March 1) — the email must show March 1.
	published := time.Date(2026, time.January, 5, 13, 30, 0, 0, time.UTC)
	effective := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	evt := map[string]any{
		"event_type":   "policy.published",
		"published_at": published,
		"version": map[string]any{
			"PolicyID":      policyID,
			"VersionID":     versionID,
			"EffectiveDate": effective,
		},
	}
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := c.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.published) != 1 {
		t.Fatalf("expected 1 published send, got %d", len(notifier.published))
	}
	pub := notifier.published[0]
	if pub.EffectiveDate != "March 1, 2026" {
		t.Errorf("published EffectiveDate: got %q, want %q (the policy's distinct effective date, not published_at)", pub.EffectiveDate, "March 1, 2026")
	}
	if pub.EffectiveDate == "January 5, 2026" {
		t.Error("published EffectiveDate must be the distinct effective date, never the publish timestamp when a distinct date is present")
	}
}

// TestPolicyPublishedConsumer_NoAckRequired does NOT notify when requires_ack is
// false — and never even resolves the RACI audience.
func TestPolicyPublishedConsumer_NoAckRequired(t *testing.T) {
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			"pol-2": {RequiresAck: false, OnChange: true},
		},
		audiences: map[string][]obligation.User{
			"pol-2": {{ID: "u4"}, {ID: "u5"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBody(t, "pol-2", "ver-1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 0 {
		t.Errorf("expected 0 notifier calls for non-obligating policy, got %d", len(notifier.calls))
	}
	if len(auditor.summaries) != 0 {
		t.Errorf("expected 0 ack-audience summaries for non-obligating policy, got %d", len(auditor.summaries))
	}
}

// TestPolicyPublishedConsumer_NotOnChange does NOT notify when on_change is false.
func TestPolicyPublishedConsumer_NotOnChange(t *testing.T) {
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			"pol-3": {RequiresAck: true, OnChange: false},
		},
		audiences: map[string][]obligation.User{
			"pol-3": {{ID: "u6"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBody(t, "pol-3", "ver-3")); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 0 {
		t.Errorf("expected 0 notifier calls for on-publish (not on-change) policy, got %d", len(notifier.calls))
	}
	if len(auditor.summaries) != 0 {
		t.Errorf("expected 0 ack-audience summaries for on-publish policy, got %d", len(auditor.summaries))
	}
}

// TestPolicyPublishedConsumer_AllAlreadyAcked does NOT notify when everyone in
// the RACI audience has already acked.
func TestPolicyPublishedConsumer_AllAlreadyAcked(t *testing.T) {
	const versionID = "ver-4"
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			"pol-4": {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			"pol-4": {{ID: "u7"}, {ID: "u8"}},
		},
	}
	acks := &fakeAcks{
		ackedUsers: map[string][]string{
			versionID: {"u7", "u8"}, // everyone acked
		},
	}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBody(t, "pol-4", versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 0 {
		t.Errorf("expected 0 notifier calls when everyone already acked, got %d", len(notifier.calls))
	}
	// The summary is emitted per-publish once the audience is materialized, even
	// when every member has already acked (0 outstanding notifications).
	if len(auditor.summaries) != 1 {
		t.Fatalf("expected exactly 1 ack-audience summary, got %d", len(auditor.summaries))
	}
	if auditor.summaries[0].UserCount != 2 {
		t.Errorf("summary UserCount: want 2, got %d", auditor.summaries[0].UserCount)
	}
}

// TestPolicyPublishedConsumer_OverrideDenyExcluded verifies that a per-policy
// DENY override drops the user from the RACI audience — which the resolver's
// AudienceUsers applies — so the consumer never notifies them. The fake models
// this by returning an audience with the denied user already excluded.
func TestPolicyPublishedConsumer_OverrideDenyExcluded(t *testing.T) {
	const (
		policyID  = "pol-deny"
		versionID = "ver-deny"
	)
	// RACI audience excludes u-denied (deny override applied upstream in the
	// resolver): only u-keep is in scope.
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-keep"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].UserID != "u-keep" {
		t.Fatalf("expected exactly u-keep notified (deny override excluded), got %+v", notifier.calls)
	}
	if len(auditor.summaries) != 1 || auditor.summaries[0].UserCount != 1 {
		t.Fatalf("expected summary UserCount 1 (denied user excluded), got %+v", auditor.summaries)
	}
}

// TestPolicyPublishedConsumer_FailLoudOnAudienceError verifies that when the
// RACI audience cannot be resolved (nil/empty chain, missing home category),
// the consumer returns an error rather than silently materializing an empty
// audience. It must NOT emit a summary or notify anyone.
func TestPolicyPublishedConsumer_FailLoudOnAudienceError(t *testing.T) {
	const (
		policyID  = "pol-nochain"
		versionID = "ver-nochain"
	)
	wantErr := errors.New("no category chain available")
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audienceErr: map[string]error{policyID: wantErr},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	err := c.Handle(context.Background(), publishedBody(t, policyID, versionID))
	if err == nil {
		t.Fatal("want error when RACI audience cannot be resolved; got nil")
	}
	if len(notifier.calls) != 0 {
		t.Errorf("expected 0 notifier calls on audience error, got %d", len(notifier.calls))
	}
	if len(auditor.summaries) != 0 {
		t.Errorf("expected 0 ack-audience summaries on audience error, got %d", len(auditor.summaries))
	}
}

// TestPolicyPublishedConsumer_ConsolidatesPersonaBacklogIntoOneSend verifies
// the anti-spam consolidation rule: a user with N outstanding obligations
// (across multiple policies, not just the one just published) gets exactly
// ONE notifier call carrying all N in Policies -- never N separate sends.
func TestPolicyPublishedConsumer_ConsolidatesPersonaBacklogIntoOneSend(t *testing.T) {
	const (
		policyID  = "pol-consolidate"
		versionID = "ver-consolidate"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-persona"}},
		},
		myObligations: map[string][]obligation.ObligationItem{
			"u-persona": {
				{PolicyID: "p1", Number: "POL-014", Title: "Remote Access & VPN Use Policy", VersionNo: 3, PolicyVersionID: "pv-1"},
				{PolicyID: "p2", Number: "POL-031", Title: "Data Retention & Disposal Policy", VersionNo: 1, PolicyVersionID: "pv-2"},
				{PolicyID: "p3", Number: "POL-009", Title: "Acceptable Use Policy", VersionNo: 2, PolicyVersionID: "pv-3"},
			},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor).
		WithPortalURL("https://policy.example.org/portal/acknowledgements")
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 {
		t.Fatalf("expected exactly 1 notifier call (consolidated), got %d: %+v", len(notifier.calls), notifier.calls)
	}
	got := notifier.calls[0]
	if got.UserID != "u-persona" {
		t.Errorf("UserID: want %q, got %q", "u-persona", got.UserID)
	}
	if len(got.Policies) != 3 {
		t.Fatalf("Policies: want 3 items, got %d: %+v", len(got.Policies), got.Policies)
	}
	want := notify.AckItem{Ref: "POL-014 v3", Title: "Remote Access & VPN Use Policy", AckURL: "https://policy.example.org/portal/acknowledgements/pv-1"}
	if got.Policies[0] != want {
		t.Errorf("Policies[0]: want %+v, got %+v", want, got.Policies[0])
	}
}

// TestPolicyPublishedConsumer_MyObligationsErrorFallsBackToSingleItem
// verifies that a MyObligations lookup failure degrades gracefully to the
// pre-consolidation single-item payload (never drops the notification).
func TestPolicyPublishedConsumer_MyObligationsErrorFallsBackToSingleItem(t *testing.T) {
	const (
		policyID  = "pol-degrade"
		versionID = "ver-degrade"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-degrade"}},
		},
		myObligationsErr: map[string]error{"u-degrade": errors.New("core unavailable")},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 notifier call despite MyObligations error, got %d", len(notifier.calls))
	}
	got := notifier.calls[0]
	if got.PolicyVersionID != versionID {
		t.Errorf("PolicyVersionID: want %q, got %q", versionID, got.PolicyVersionID)
	}
	if len(got.Policies) != 0 {
		t.Errorf("Policies: want empty (single-item fallback derived downstream), got %+v", got.Policies)
	}
}

// TestPolicyPublishedConsumer_NewUserThrottleSkipsFirstCycle verifies the
// new-user anti-spam rule: a user the gate reports as still within its grace
// window gets NO ack-reminder email this cycle, while a normal (non-new)
// user in the same audience is notified as usual.
func TestPolicyPublishedConsumer_NewUserThrottleSkipsFirstCycle(t *testing.T) {
	const (
		policyID  = "pol-newuser"
		versionID = "ver-newuser"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-brand-new"}, {ID: "u-existing"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}
	gate := &fakeNewUserGate{skip: map[string]bool{"u-brand-new": true, "u-existing": false}}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor).
		WithNewUserGate(gate).
		WithNewUserThrottleEnabled(true)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].UserID != "u-existing" {
		t.Fatalf("expected only u-existing notified (u-brand-new throttled), got %+v", notifier.calls)
	}
}

// TestPolicyPublishedConsumer_NewUserThrottleDisabledByDefaultSendsAnyway
// verifies the cold-start safety gate: with the new-user throttle left at
// its default (disabled), an existing user the gate would otherwise report
// as "new" (e.g. because user_notify_first_seen is empty right after a
// fresh deploy) still receives their consolidated reminder -- the throttle
// must never fire when WithNewUserThrottleEnabled has not been called.
func TestPolicyPublishedConsumer_NewUserThrottleDisabledByDefaultSendsAnyway(t *testing.T) {
	const (
		policyID  = "pol-coldstart"
		versionID = "ver-coldstart"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-outstanding"}},
		},
		myObligations: map[string][]obligation.ObligationItem{
			"u-outstanding": {
				{PolicyID: "p1", Number: "POL-014", Title: "Remote Access & VPN Use Policy", VersionNo: 3, PolicyVersionID: "pv-1"},
			},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}
	// Gate would report every user as "new" (simulating an empty
	// user_notify_first_seen table right after a cold-start deploy).
	gate := &fakeNewUserGate{skip: map[string]bool{"u-outstanding": true}}

	// Throttle is NOT enabled (default false) -- WithNewUserThrottleEnabled
	// is deliberately not called.
	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor).WithNewUserGate(gate)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].UserID != "u-outstanding" {
		t.Fatalf("expected u-outstanding notified despite gate reporting new (throttle disabled), got %+v", notifier.calls)
	}
	if len(gate.calls) != 0 {
		t.Errorf("expected the gate to never be consulted while the throttle is disabled, got calls: %+v", gate.calls)
	}
}

// TestPolicyPublishedConsumer_NewUserThrottleEnabledSkipsBrandNewUser
// verifies that once WithNewUserThrottleEnabled(true) is set, the
// pre-existing throttle behavior is preserved: a brand-new user the
// gate reports as still within its grace window is skipped this cycle.
func TestPolicyPublishedConsumer_NewUserThrottleEnabledSkipsBrandNewUser(t *testing.T) {
	const (
		policyID  = "pol-throttleon"
		versionID = "ver-throttleon"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-brand-new"}, {ID: "u-existing"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}
	gate := &fakeNewUserGate{skip: map[string]bool{"u-brand-new": true, "u-existing": false}}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor).
		WithNewUserGate(gate).
		WithNewUserThrottleEnabled(true)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].UserID != "u-existing" {
		t.Fatalf("expected only u-existing notified (u-brand-new throttled with throttle enabled), got %+v", notifier.calls)
	}
}

// TestPolicyPublishedConsumer_NewUserGateErrorSendsAnyway verifies that a
// new-user gate failure degrades to sending the reminder rather than
// silently dropping it (a gate outage must never suppress delivery).
func TestPolicyPublishedConsumer_NewUserGateErrorSendsAnyway(t *testing.T) {
	const (
		policyID  = "pol-gateerr"
		versionID = "ver-gateerr"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-gateerr"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	auditor := &fakeAuditor{}
	gate := &fakeNewUserGate{err: map[string]error{"u-gateerr": errors.New("db unavailable")}}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, auditor).
		WithNewUserGate(gate).
		WithNewUserThrottleEnabled(true)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].UserID != "u-gateerr" {
		t.Fatalf("expected u-gateerr still notified despite gate error, got %+v", notifier.calls)
	}
}

// TestPolicyPublishedConsumer_FirstNotificationSendsAckRequired verifies that
// with a notified-tracker wired, a user's FIRST notification for a version is
// classified ack_required, and a subsequent publish delivery for the same
// still-un-acked (user, version) is classified bulk_sweep (repeat reminder).
func TestPolicyPublishedConsumer_FirstNotificationSendsAckRequired(t *testing.T) {
	const (
		policyID  = "pol-first"
		versionID = "ver-first"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	tracker := &fakeNotifiedTracker{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, nil).WithNotifiedTracker(tracker)

	// First publish: ack_required.
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle (first): %v", err)
	}
	if len(notifier.calls) != 1 || notifier.calls[0].Type != "ack_required" {
		t.Fatalf("first notification: want one ack_required, got %+v", notifier.calls)
	}

	// Second publish delivery, still un-acked: bulk_sweep (repeat).
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle (repeat): %v", err)
	}
	if len(notifier.calls) != 2 || notifier.calls[1].Type != "bulk_sweep" {
		t.Fatalf("repeat notification: want bulk_sweep, got %+v", notifier.calls)
	}
}

// TestPolicyPublishedConsumer_EmitsPolicyPublishedToWholeAudience verifies the
// informational fan-out: every audience member (including a user who already
// acked) receives a policy_published send, independent of ack state.
func TestPolicyPublishedConsumer_EmitsPolicyPublishedToWholeAudience(t *testing.T) {
	const (
		policyID  = "pol-info"
		versionID = "ver-info"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u1"}, {ID: "u2"}, {ID: "u3"}},
		},
	}
	acks := &fakeAcks{ackedUsers: map[string][]string{versionID: {"u1"}}}
	notifier := &fakeNotifier{}
	tracker := &fakeNotifiedTracker{}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, nil).WithNotifiedTracker(tracker)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// policy_published goes to ALL three (u1 already acked but still gets the notice).
	if len(notifier.published) != 3 {
		t.Fatalf("policy_published: want 3 sends (whole audience), got %d: %+v", len(notifier.published), notifier.published)
	}
	for _, p := range notifier.published {
		if p.Type != "policy_published" {
			t.Errorf("published payload Type: got %q, want policy_published", p.Type)
		}
		if p.PolicyVersionID != versionID {
			t.Errorf("published payload PolicyVersionID: got %q, want %q", p.PolicyVersionID, versionID)
		}
	}
	// ack sends still only go to the outstanding subset (u2, u3), classified first.
	if len(notifier.calls) != 2 {
		t.Fatalf("ack sends: want 2 (outstanding), got %d", len(notifier.calls))
	}
}

// TestPolicyPublishedConsumer_NotifiedTrackerErrorSendsAnyway verifies that a
// notified-tracker failure degrades to the repeat-reminder path rather than
// dropping the send: an outstanding recipient still receives an ack send, and
// since the classification could not be determined it is sent as bulk_sweep
// (never silently suppressed).
func TestPolicyPublishedConsumer_NotifiedTrackerErrorSendsAnyway(t *testing.T) {
	const (
		policyID  = "pol-trackererr"
		versionID = "ver-trackererr"
	)
	obl := &fakeObligation{
		resolutions: map[string]obligation.PolicyObligation{
			policyID: {RequiresAck: true, OnChange: true, PublishedVersionID: versionID},
		},
		audiences: map[string][]obligation.User{
			policyID: {{ID: "u-trackererr"}},
		},
	}
	acks := &fakeAcks{}
	notifier := &fakeNotifier{}
	tracker := &fakeNotifiedTracker{err: errors.New("db unavailable")}

	c := consumer.NewPolicyPublishedConsumer(obl, acks, notifier, nil).WithNotifiedTracker(tracker)
	if err := c.Handle(context.Background(), publishedBody(t, policyID, versionID)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(notifier.calls) != 1 || notifier.calls[0].UserID != "u-trackererr" {
		t.Fatalf("expected u-trackererr still notified despite tracker error, got %+v", notifier.calls)
	}
	if notifier.calls[0].Type != "bulk_sweep" {
		t.Errorf("Type: want bulk_sweep (degraded classification), got %q", notifier.calls[0].Type)
	}
}
