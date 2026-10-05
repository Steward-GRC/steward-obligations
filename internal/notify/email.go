// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// UserEmailResolver maps a user_id to an email address. In production this
// hits the Identity gRPC; tests inject a fake.
type UserEmailResolver interface {
	ResolveEmail(ctx context.Context, userID string) (string, error)
}

// Sender is the subset of *mail.Sender (internal/mail, ) the email
// channel needs: render a template kind against vars via the render sidecar
// and deliver the result through the compliance hooks (audit/suppress/
// dedupe). Extracting this interface keeps package notify free of a
// concrete internal/mail import, so channel tests can supply a fake instead
// of standing up a sidecar.
//
// userID and dedupRef exist purely to feed internal/mail's Suppress and
// Dedupe hooks (see mail.Sender.Send's doc comment): userID is who the send
// is for (its email preference/quiet-hours are checked against it), and
// dedupRef is the business key -- here, the campaign or policy version this
// notification is about -- that keeps a redelivered event from double-
// sending.
type Sender interface {
	Send(ctx context.Context, kind, userID, to, dedupRef string, vars any) error
}

const (
	// kindPolicyAckReminder is the render-sidecar template kind for a
	// persona's pending policy acknowledgements (1..N policies per email).
	kindPolicyAckReminder = "policy-ack-reminder"
	// kindPolicyEscalation is the render-sidecar template kind for a single
	// overdue acknowledgement escalated to the recipient.
	kindPolicyEscalation = "policy-escalation"
	// kindAckRequired is the render-sidecar template kind for a recipient's
	// FIRST acknowledgement demand for a (user, version): a single-item
	// "action required" ask, distinct from the recurring policy-ack-reminder.
	kindAckRequired = "ack-required"
	// kindPolicyPublished is the render-sidecar template kind for the
	// informational "a new policy was published" notice sent to the whole
	// resolved audience once per (version, user), independent of ack state.
	kindPolicyPublished = "policy-published"
	// kindPolicyRetired is the render-sidecar template kind for the
	// informational "a policy was retired" notice sent to the full ack
	// audience once per (policy, user). A retired policy obligates no one, so
	// the notice simply informs the audience that no acknowledgement is
	// required anymore.
	kindPolicyRetired = "policy-retired"

	// fallbackRecipientName is stamped into every send's recipientName var
	// until a real display name is wired in. Both
	// templates interpolate it directly into the greeting ("Hi
	// {recipientName},"), so it must never be empty -- an empty/undefined
	// value would render "Hi undefined," or "Hi,".
	fallbackRecipientName = "there"
	// fallbackPolicyTitle is used for policy-escalation's hero title/preview
	// text when no title is known yet (enrichment supplies the real
	// one). It must never be empty -- an empty title renders a blank hero.
	fallbackPolicyTitle = "Policy acknowledgement"

	// fallbackAckRequiredBody is the ack-required body copy used until
	// per-policy enrichment is wired. It must never be empty.
	fallbackAckRequiredBody = "Please review the policy below and confirm your acknowledgement."
	// fallbackPublishedSummary is the policy-published body copy used until
	// per-policy summary enrichment is wired. It must never be empty.
	fallbackPublishedSummary = "A new policy has been published. Please review it at your earliest convenience."
	// fallbackRetiredSummary is the policy-retired body copy used until
	// per-policy summary enrichment is wired. It must never be empty.
	fallbackRetiredSummary = "This policy has been retired. You no longer need to acknowledge it."
	// fallbackRetiredDate is the policy-retired "Retired" row copy used when the
	// caller supplies no retirement date. policyRetired.tsx always renders a
	// "Retired" row, so this must never be empty -- an empty value renders a
	// blank row.
	fallbackRetiredDate = "recently"
	// fallbackEffectiveDate is the policy-published effective-date copy used
	// until the real effective date is wired in. policyPublished.tsx declares
	// effectiveDate as a required string it always renders in an "Effective"
	// row, so this must never be empty -- an empty value renders a blank row.
	fallbackEffectiveDate = "the publish date"
)

// EmailChannel sends branded ack-reminder and escalation emails via the
// go-email Sender: it resolves the recipient's address, maps the payload
// onto a render-sidecar template kind + vars, and hands off to Sender.Send.
// There is no plaintext fallback -- a sidecar/SMTP failure surfaces as an
// error rather than shipping an unbranded email.
type EmailChannel struct {
	sender         Sender
	resolver       UserEmailResolver
	portalURL      string
	preferencesURL string
}

// NewEmailChannel builds an EmailChannel. portalURL points at the portal's
// acknowledgement queue and preferencesURL at the recipient's email
// preferences page; both are stamped into every ack-reminder/escalation send
// .
func NewEmailChannel(sender Sender, r UserEmailResolver, portalURL, preferencesURL string) *EmailChannel {
	return &EmailChannel{
		sender:         sender,
		resolver:       r,
		portalURL:      portalURL,
		preferencesURL: preferencesURL,
	}
}

// Send resolves the recipient's address and ships a branded email for p.
// Type "escalation" maps to kind policy-escalation; every other known ack
// type ("", "initial", "reminder", "bulk_sweep") maps to kind
// policy-ack-reminder carrying a 1..N policies list (owns grouping a
// persona's outstanding acks into that list -- today, absent a populated
// p.Policies, a single item is derived from p.PolicyVersionID).
// "workflow_escalation" (Phase 3 approval escalations) has no branded
// template yet, so it is intentionally skipped rather than mis-rendered as a
// policy-ack email. Type "ack_required" maps to kind ack-required (a
// recipient's first acknowledgement demand for a version) and
// "policy_published" maps to kind policy-published (the informational
// new-policy notice); both are SP-5 additions.
func (e *EmailChannel) Send(ctx context.Context, p AckReminderPayload) error {
	to, err := e.resolver.ResolveEmail(ctx, p.UserID)
	if err != nil {
		return err
	}

	switch p.Type {
	case "workflow_escalation":
		logger := logctx.From(ctx)
		logger.Debug().Str("user_id", p.UserID).Msg("notify/email: workflow_escalation has no branded template yet (SP-5); skipping")
		return nil
	case "escalation":
		return e.sender.Send(ctx, kindPolicyEscalation, p.UserID, to, dedupRef(p), e.escalationVars(p))
	case "ack_required":
		return e.sender.Send(ctx, kindAckRequired, p.UserID, to, dedupRef(p), e.ackRequiredVars(p))
	case "policy_published":
		return e.sender.Send(ctx, kindPolicyPublished, p.UserID, to, dedupRef(p), e.publishedVars(p))
	case "policy_retired":
		return e.sender.Send(ctx, kindPolicyRetired, p.UserID, to, dedupRef(p), e.retiredVars(p))
	default:
		return e.sender.Send(ctx, kindPolicyAckReminder, p.UserID, to, dedupRef(p), e.ackReminderVars(p))
	}
}

// dedupRef derives the business key internal/mail's Deduper combines with
// UserID and kind to form its idempotency key: the campaign this reminder
// wave belongs to, or (for a payload with no campaign, e.g. a single-policy
// escalation) the policy version it's about. An empty result (both fields
// unset) disables deduplication for that send rather than erroring.
func dedupRef(p AckReminderPayload) string {
	if p.CampaignID != "" {
		return p.CampaignID
	}
	return p.PolicyVersionID
}

// ackReminderVars builds the policy-ack-reminder template vars: the
// recipient's pending policies (1..N) plus the shared portal/preferences
// links. When p.Policies is populated it is
// used as-is; otherwise a single item is derived from the payload — its ack
// link carries the version id but its visible Ref is left EMPTY (the consumer
// stamps a human policy number upstream; a UUID is never shown).
func (e *EmailChannel) ackReminderVars(p AckReminderPayload) map[string]any {
	policies := p.Policies
	if len(policies) == 0 {
		// No enriched backlog item: derive a single item from the payload. Ref
		// is left EMPTY (never the version UUID — the consumer stamps a human
		// number when it can); only the ack link carries the version id.
		policies = []AckItem{{
			Ref:    "",
			AckURL: fmt.Sprintf("%s/%s", e.portalURL, p.PolicyVersionID),
		}}
	}

	rendered := make([]map[string]any, len(policies))
	for i, item := range policies {
		rendered[i] = map[string]any{
			"ref":    item.Ref,
			"title":  item.Title,
			"dueBy":  item.DueBy,
			"ackUrl": item.AckURL,
		}
	}

	return map[string]any{
		"policies":       rendered,
		"portalUrl":      e.portalURL,
		"preferencesUrl": e.preferencesURL,
		// recipientName is always non-empty: the template interpolates it
		// directly into the greeting, so an empty/unresolved name must fall
		// back to fallbackRecipientName rather than render "Hi undefined,".
		// wires in the real resolved name.
		"recipientName": fallbackRecipientName,
	}
}

// escalationVars builds the policy-escalation template vars from an
// escalation-typed AckReminderPayload. The template's escalationNote is
// fixed copy for now (who gets notified on an overdue ack); per-policy
// due-date enrichment lands with grouping work. recipientName and
// policyTitle always fall back to a non-empty placeholder so the greeting
// and hero title are never blank/"undefined" in the interim.
func (e *EmailChannel) escalationVars(p AckReminderPayload) map[string]any {
	// Best-effort pass-through: if a caller already populated Policies (Task
	// 7 may do this even for escalations), use its title; otherwise fall
	// back to a generic, non-empty placeholder.
	policyTitle := fallbackPolicyTitle
	policyRef := ""
	if len(p.Policies) > 0 {
		if p.Policies[0].Title != "" {
			policyTitle = p.Policies[0].Title
		}
		// Human reference (policy number) when the consumer stamped one; never
		// the version UUID. policyEscalation.tsx renders no reference row when
		// policyRef is empty.
		policyRef = p.Policies[0].Ref
	}

	return map[string]any{
		"ackUrl":         fmt.Sprintf("%s/%s", e.portalURL, p.PolicyVersionID),
		"dueBy":          "",
		"escalationNote": "Your manager and Compliance have been notified.",
		"policyRef":      policyRef,
		"policyTitle":    policyTitle,
		"preferencesUrl": e.preferencesURL,
		"recipientName":  fallbackRecipientName,
	}
}

// ackRequiredVars builds the ack-required template vars for a recipient's
// FIRST acknowledgement demand for a (user, version). It renders the single
// just-published policy: itemTitle/dueBy come from p.Policies[0] when the
// consumer populated it, and ackUrl is the per-version portal link
// (portalURL + "/" + PolicyVersionID) matching EmailChannel's other
// single-item derivations. recipientName/itemTitle always fall back to a
// non-empty placeholder so the greeting and card are never blank. dueBy is
// optional in ackRequired.tsx (`dueBy?: string`), which gates its due-date
// callout on `dueBy !== undefined` -- so when there's no due date, the key is
// omitted from the map entirely rather than set to "" (an empty string is
// still "present" in the rendered JSON and would render "Please acknowledge
// by.").
func (e *EmailChannel) ackRequiredVars(p AckReminderPayload) map[string]any {
	itemTitle := fallbackPolicyTitle
	dueBy := ""
	if len(p.Policies) > 0 {
		if p.Policies[0].Title != "" {
			itemTitle = p.Policies[0].Title
		}
		dueBy = p.Policies[0].DueBy
	}
	vars := map[string]any{
		"ackUrl":         fmt.Sprintf("%s/%s", e.portalURL, p.PolicyVersionID),
		"bodyText":       fallbackAckRequiredBody,
		"itemTitle":      itemTitle,
		"preferencesUrl": e.preferencesURL,
		"recipientName":  fallbackRecipientName,
	}
	if dueBy != "" {
		vars["dueBy"] = dueBy
	}
	return vars
}

// publishedVars builds the policy-published informational template vars. The
// notice always renders as requires-ack for (the only path that
// resolves an audience is requires_ack+on_change), with a per-version ack CTA
// and read link both derived from portalURL. policyTitle/policyRef come from
// p.Policies[0] when populated; policyTitle/recipientName/summary fall back to
// a non-empty placeholder, and policyRef falls back to EMPTY (never the version
// UUID) so no field renders blank/"undefined" or leaks an id.
func (e *EmailChannel) publishedVars(p AckReminderPayload) map[string]any {
	policyTitle := fallbackPolicyTitle
	// Ref is the human policy number the consumer stamps; default EMPTY so a
	// missing ref renders no reference (never the raw version UUID).
	policyRef := ""
	if len(p.Policies) > 0 {
		if p.Policies[0].Title != "" {
			policyTitle = p.Policies[0].Title
		}
		if p.Policies[0].Ref != "" {
			policyRef = p.Policies[0].Ref
		}
	}
	// Effective date: use the real date the consumer resolved from the publish
	// event when present; otherwise fall back to the non-empty placeholder so
	// the "Effective" row is never blank. A populated value must never be
	// overridden by the placeholder -- that was.
	effectiveDate := fallbackEffectiveDate
	if p.EffectiveDate != "" {
		effectiveDate = p.EffectiveDate
	}
	link := fmt.Sprintf("%s/%s", e.portalURL, p.PolicyVersionID)
	return map[string]any{
		"ackUrl":         link,
		"effectiveDate":  effectiveDate,
		"policyRef":      policyRef,
		"policyTitle":    policyTitle,
		"policyUrl":      link,
		"preferencesUrl": e.preferencesURL,
		"recipientName":  fallbackRecipientName,
		"requiresAck":    true,
		"summary":        fallbackPublishedSummary,
	}
}

// retiredVars builds the policy-retired informational template vars, mirroring
// publishedVars. The notice tells the full ack audience the policy has been
// retired and no acknowledgement is required anymore. policyTitle/policyRef come
// from p.Policies[0] when populated; policyTitle/recipientName/summary fall back
// to a non-empty placeholder, and policyRef falls back to EMPTY (never the raw
// policy UUID) so no field renders blank/"undefined" or leaks an id.
func (e *EmailChannel) retiredVars(p AckReminderPayload) map[string]any {
	policyTitle := fallbackPolicyTitle
	// Ref is the human policy number the consumer stamps; default EMPTY so a
	// missing ref renders no reference (never a raw id).
	policyRef := ""
	if len(p.Policies) > 0 {
		if p.Policies[0].Title != "" {
			policyTitle = p.Policies[0].Title
		}
		if p.Policies[0].Ref != "" {
			policyRef = p.Policies[0].Ref
		}
	}
	// Retirement date: use the real date the consumer resolved when present;
	// otherwise fall back to the non-empty placeholder so the "Retired" row is
	// never blank. A populated value must never be overridden by the placeholder.
	retiredDate := fallbackRetiredDate
	if p.EffectiveDate != "" {
		retiredDate = p.EffectiveDate
	}
	return map[string]any{
		"policyRef":      policyRef,
		"policyTitle":    policyTitle,
		"policyUrl":      e.portalURL,
		"preferencesUrl": e.preferencesURL,
		"recipientName":  fallbackRecipientName,
		"retiredDate":    retiredDate,
		"summary":        fallbackRetiredSummary,
	}
}
