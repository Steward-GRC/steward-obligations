// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package notify owns the WRITE half of the notification delivery layer: it
// fans out user-targeted notifications across email, in-app, and push channels.
//
// Quiet hours (22:00–07:00 in the user's local timezone) suppress email and
// push but never in-app, which always lands in the user's notification inbox.
// The TZResolver is optional; when absent or returning an error the
// dispatcher falls back to UTC.
package notify

import (
	"context"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// AckReminderPayload is the message body for a user-targeted ack reminder.
// Type discriminates "initial", "reminder", "bulk_sweep", and "escalation"
// so downstream channels can adjust copy/severity.
type AckReminderPayload struct {
	UserID          string
	PolicyVersionID string
	CampaignID      string
	Type            string // "initial" | "reminder" | "bulk_sweep" | "escalation" | "workflow_escalation"

	// Policies carries the persona's full set of pending acknowledgements
	// for the branded email channel's policy-ack-reminder template (1..N).
	// It is optional: today's callers (the policy.published consumer) only
	// populate PolicyVersionID for a single policy, so EmailChannel derives
	// a one-item list from that field when Policies is empty. grouping
	// populates this directly with a persona's full outstanding set before
	// SendAckReminder is called, at which point PolicyVersionID/CampaignID
	// become the "primary" policy (used by non-email channels).
	Policies []AckItem

	// EffectiveDate is the human-readable date the published policy takes
	// effect (e.g. "September 1, 2026"), used only by the policy-published
	// template's "Effective" row. The policy.published consumer resolves and
	// formats it from the publish event's timestamp before dispatch. When
	// empty (no caller populated it), publishedVars falls back to a non-empty
	// placeholder so the row is never blank -- but a populated value must be
	// used verbatim so the email shows the real date, not the placeholder.
	EffectiveDate string
}

// AckItem is one pending acknowledgement in an AckReminderPayload's Policies
// list: the data the policy-ack-reminder/policy-escalation email templates
// need to render a row for a single policy.
type AckItem struct {
	// Ref is the short internal reference/version shown to the recipient,
	// e.g. "POL-014 v3". Today this is populated from the policy version id.
	Ref string
	// Title is the policy's display title.
	Title string
	// DueBy is the human-readable due date, e.g. "August 15, 2026".
	DueBy string
	// AckURL is the absolute URL to this policy's acknowledgement action.
	AckURL string
}

// EscalationPayload carries the source user, policy version, campaign, and the
// resolved manager destination for a manager-escalation notification.
type EscalationPayload struct {
	UserID          string
	PolicyVersionID string
	CampaignID      string
	ManagerUserID   string
}

// WorkflowEscalationPayload is delivered when Phase 3 asks the obligations service
// to escalate an overdue approval to a Phase-3 approver.
type WorkflowEscalationPayload struct {
	ApproverUserID  string
	PolicyVersionID string
	WorkflowRunID   string
	StageID         string
}

// Channel is the send interface for a single notification channel. Email,
// in-app, and push each provide one implementation.
type Channel interface {
	Send(ctx context.Context, p AckReminderPayload) error
}

// PrefStore retrieves per-user notification preferences. *store.NotificationPrefStore
// satisfies it; tests use a small fake.
type PrefStore interface {
	Get(ctx context.Context, userID string) (store.NotifPref, error)
}

// TZResolver resolves a user's IANA timezone string (e.g. "America/New_York")
// so the dispatcher can apply quiet hours in local time. A stub returning
// "UTC" is acceptable until the Identity service surfaces a real timezone.
type TZResolver interface {
	ResolveTimezone(ctx context.Context, userID string) (string, error)
}

// QuietHours determines whether "now", in a user's local timezone, falls in
// the 22:00-07:00 window that suppresses email and push notifications (but
// never in-app). Both the Dispatcher below and internal/mail's Suppress
// middleware (send-time backstop, which needs the identical rule to
// avoid drifting from the dispatcher's own gate) share this one type instead
// of each defining their own version of "quiet hours".
type QuietHours struct {
	// Resolver looks up a user's IANA timezone; nil, an error, or a blank
	// result all fall back to UTC.
	Resolver TZResolver
	// HourFn overrides the current-hour lookup for tests; nil means
	// time.Now.In(loc).Hour. When set it short-circuits timezone resolution
	// entirely (the hour is taken as already-local).
	HourFn func() int
	// NowFn overrides the base instant for tests while STILL resolving the
	// user's timezone (unlike HourFn, which bypasses the Resolver). nil means
	// time.Now. This lets a test pin a UTC instant and assert that the same
	// moment lands in/out of the window depending on the resolved local zone.
	NowFn func() time.Time
}

// InWindow reports whether userID is currently in the 22:00-07:00 quiet
// window.
func (q QuietHours) InWindow(ctx context.Context, userID string) bool {
	if q.HourFn != nil {
		h := q.HourFn()
		return h >= 22 || h < 7
	}
	loc := time.UTC
	if q.Resolver != nil {
		if tzName, err := q.Resolver.ResolveTimezone(ctx, userID); err == nil && tzName != "" {
			if l, err := time.LoadLocation(tzName); err == nil {
				loc = l
			}
		}
	}
	now := time.Now
	if q.NowFn != nil {
		now = q.NowFn
	}
	h := now().In(loc).Hour()
	return h >= 22 || h < 7
}

// Dispatcher fans notifications to every channel a user has enabled. Quiet
// hours (22:00–07:00 in the user's local tz) suppress email and push but not
// in-app. The clock hook is injectable to keep tests deterministic.
//
// When a PrefResolver is wired (WithResolver, done by cmd/server), the
// resolver is the single decision point: it folds the
// channel switches, the 22:00-07:00 quiet-hours window (bypassed for mandatory
// security/transactional and critical severity), and the per-category cadence
// (off suppresses an optional type; the compliance floor is applied in the
// resolver) into one Decision. Without a resolver the dispatcher falls back to
// its channel-pref + quiet-hours gate — the same optional-wiring seam the mail
// Sender uses for its Suppress hook.
type Dispatcher struct {
	prefs PrefStore
	email Channel
	inApp Channel
	push  Channel
	// quietHours holds the (optional) TZResolver and clock hook; see
	// QuietHours.InWindow for the 22:00-07:00 rule it applies (legacy gate).
	quietHours QuietHours
	// resolver, when non-nil, is the send-time preference resolver that
	// supersedes the channel-pref + quiet-hours gate above.
	resolver *notifpolicy.Resolver
	// batch, when non-nil, is the digest outbox: a resolver mode=batch decision
	// writes the event here instead of sending, and the scheduler drains it at
	// the user's digest window. Without it a
	// batch decision falls back to an immediate send (the pre-slice-6 behavior).
	batch BatchWriter
}

// BatchItem is one digest-bound event handed to the outbox: enough to render a
// row in the digest (Vars) plus the routing keys the drain groups by.
type BatchItem struct {
	UserID     string
	Kind       string
	Category   string
	Severity   string
	DedupRef   string
	WindowKind string // daily|weekly
	DigestKey  string // user_id:category:window
	Vars       map[string]any
}

// BatchWriter persists a digest-bound event. cmd/server adapts it to
// *store.NotificationOutboxStore so package notify carries no store-row coupling
// (mirroring the mail outbox adapter).
type BatchWriter interface {
	WriteBatch(ctx context.Context, it BatchItem) error
}

// NewDispatcher returns a Dispatcher wired to the supplied stores and
// channels. The TZResolver and clock hook are optional and default to UTC
// time.Now.
func NewDispatcher(prefs PrefStore, email, inApp, push Channel) *Dispatcher {
	return &Dispatcher{prefs: prefs, email: email, inApp: inApp, push: push}
}

// WithResolver returns a copy of the dispatcher whose send-time gating goes
// through the shared PrefResolver (channel switches + quiet hours + category
// cadence). cmd/server wires the production resolver here so immediate types
// honor the user's category cadence and optional types can be turned off, while
// mandatory compliance mail keeps sending (floored in the resolver).
func (d *Dispatcher) WithResolver(r *notifpolicy.Resolver) *Dispatcher {
	d2 := *d
	d2.resolver = r
	return &d2
}

// WithOutbox returns a copy of the dispatcher that writes a resolver mode=batch
// decision to the digest outbox instead of sending immediately (
// ). cmd/server wires the production outbox here; without it a batch
// decision still sends immediately, preserving pre-slice-6 behavior for callers
// (and tests) that do not wire an outbox.
func (d *Dispatcher) WithOutbox(w BatchWriter) *Dispatcher {
	d2 := *d
	d2.batch = w
	return &d2
}

// WithTZResolver returns a copy of the dispatcher with a timezone resolver
// attached. The original is unchanged so callers can keep a UTC-only
// dispatcher around if Identity is unavailable.
func (d *Dispatcher) WithTZResolver(r TZResolver) *Dispatcher {
	d2 := *d
	d2.quietHours.Resolver = r
	return &d2
}

// WithClock returns a copy of the dispatcher with an injected hour-of-day
// function; tests use this to drive deterministic quiet-hour behavior. The
// function receives no input and returns an hour in [0, 23].
func (d *Dispatcher) WithClock(hourFn func() int) *Dispatcher {
	d2 := *d
	d2.quietHours.HourFn = hourFn
	return &d2
}

// recordEmailOutcome handles the result of an email-channel send from the
// swallow-per-recipient dispatcher paths, distinguishing the Phase-7 pause
// disposition from an ordinary per-recipient failure:
//
// - ErrMailPaused: there is no working transport, and the pause-gate has
// already HELD the fully-rendered message in the durable outbox (it drains
// on recovery). We surface it as a warn rather than swallowing it silently,
// but we do NOT requeue/abort the audience — the hold is the retry.
// - any other error: an ordinary per-recipient failure (e.g. one bad
// address); it stays swallowed so a single recipient never starves the
// others or re-blasts the whole audience.
func (d *Dispatcher) recordEmailOutcome(ctx context.Context, p AckReminderPayload, err error) {
	if err == nil {
		return
	}
	logger := logctx.From(ctx)
	if mail.IsPaused(err) {
		logger.Warn().Str("user_id", p.UserID).Str("type", p.Type).
			Msg("notify: email send paused — held in outbox for retry")
		return
	}
	logger.Debug().Err(err).Str("user_id", p.UserID).Str("type", p.Type).
		Msg("notify: email send failed — dropped for this recipient")
}

// channelGate is the per-send delivery decision after preferences, quiet
// hours, and (when a resolver is wired) category cadence have been applied.
type channelGate struct {
	deliver bool
	inApp   bool
	email   bool
	push    bool
}

// dispatch is the single entry every Send* method funnels through. With a
// resolver wired it is the one decision point (channels + quiet hours +
// cadence): an off/suppressed or errored decision sends nothing; a mode=batch
// decision is written to the digest outbox when one is wired, else it
// falls back to an immediate send; an immediate decision fans out to the
// resolved channels. Without a resolver it uses the legacy channel-pref +
// quiet-hours gate. A resolver/pref lookup failure suppresses all channels,
// matching the prior "lookup failed → suppress" behavior.
func (d *Dispatcher) dispatch(ctx context.Context, p AckReminderPayload) {
	if d.resolver != nil {
		kind := kindForType(p.Type)
		dec, err := d.resolver.Resolve(ctx, p.UserID, kind)
		if err != nil || !dec.Deliver {
			return
		}
		if dec.Mode == notifpolicy.ModeBatch && d.batch != nil {
			d.writeBatch(ctx, p, kind, dec)
			return
		}
		d.fanOut(ctx, channelGate{deliver: true, inApp: dec.InApp, email: dec.Email, push: dec.Push}, p)
		return
	}
	pref, err := d.prefs.Get(ctx, p.UserID)
	if err != nil {
		return
	}
	inQuiet := d.userQuietHours(ctx, p.UserID)
	d.fanOut(ctx, channelGate{deliver: true, inApp: pref.InApp, email: pref.Email && !inQuiet, push: pref.Push && !inQuiet}, p)
}

// writeBatch records a digest-bound event in the outbox. On a write failure it
// falls back to an immediate send so a batched notice is never silently lost.
func (d *Dispatcher) writeBatch(ctx context.Context, p AckReminderPayload, kind string, dec notifpolicy.Decision) {
	item := BatchItem{
		UserID:     p.UserID,
		Kind:       kind,
		Category:   string(dec.Class.Category),
		Severity:   string(dec.Class.Severity),
		DedupRef:   dedupRef(p),
		WindowKind: string(dec.Cadence),
		DigestKey:  p.UserID + ":" + string(dec.Class.Category) + ":" + string(dec.Cadence),
		Vars:       batchVars(kind, p),
	}
	if err := d.batch.WriteBatch(ctx, item); err != nil {
		logger := logctx.From(ctx)
		logger.Warn().Err(err).Str("user_id", p.UserID).Str("kind", kind).
			Msg("notify: outbox write failed; sending immediately instead")
		d.fanOut(ctx, channelGate{deliver: true, inApp: dec.InApp, email: dec.Email, push: dec.Push}, p)
	}
}

// batchVars projects an AckReminderPayload into the DigestItem fields the drain
// renders (title/meta/actionHref/actionLabel), with a per-kind CTA label.
func batchVars(kind string, p AckReminderPayload) map[string]any {
	var title, meta, href string
	if len(p.Policies) > 0 {
		title = p.Policies[0].Title
		meta = p.Policies[0].DueBy
		href = p.Policies[0].AckURL
	}
	label := "View"
	switch kind {
	case "policy-published":
		if p.EffectiveDate != "" {
			meta = "Effective " + p.EffectiveDate
		}
		label = "Read"
	case "workflow-awaiting-approval", "workflow-denied", "workflow-assigned", "assigned-as-owner":
		label = "Open"
	}
	return map[string]any{"title": title, "meta": meta, "actionHref": href, "actionLabel": label}
}

// fanOut sends the payload to each channel the gate allows. In-app is delivered
// whenever its switch is on (it is the persistent inbox); email routes through
// recordEmailOutcome so a pause is held in the outbox and an ordinary
// per-recipient failure is swallowed.
func (d *Dispatcher) fanOut(ctx context.Context, g channelGate, p AckReminderPayload) {
	if !g.deliver {
		return
	}
	if g.inApp {
		_ = d.inApp.Send(ctx, p)
	}
	if g.email {
		d.recordEmailOutcome(ctx, p, d.email.Send(ctx, p))
	}
	if g.push {
		_ = d.push.Send(ctx, p)
	}
}

// kindForType maps an AckReminderPayload.Type to its render-sidecar template
// kind, the taxonomy key the resolver classifies. It mirrors EmailChannel.Send's
// own type→kind switch so the resolver and the email channel agree on what a
// payload is.
func kindForType(payloadType string) string {
	switch payloadType {
	case "escalation":
		return "policy-escalation"
	case "workflow_escalation":
		// No branded template yet (EmailChannel skips it); classify it as a
		// workflow approval so its cadence/channels resolve sensibly for
		// in-app/push.
		return "workflow-awaiting-approval"
	case "ack_required":
		return "ack-required"
	case "policy_published":
		return "policy-published"
	case "policy_retired":
		return "policy-retired"
	default: // "", "initial", "reminder", "bulk_sweep"
		return "policy-ack-reminder"
	}
}

// SendAckReminder dispatches a reminder to each enabled channel. The gate
// (channels + quiet hours + cadence when a resolver is wired) quietly
// suppresses delivery on lookup failure or an off decision; per-channel errors
// are handled by recordEmailOutcome (pause → held in outbox, ordinary →
// swallowed) so one bad channel/recipient does not starve the others.
func (d *Dispatcher) SendAckReminder(ctx context.Context, p AckReminderPayload) {
	d.dispatch(ctx, p)
}

// SendPolicyPublished dispatches an informational "new policy published"
// notice to each enabled channel. Like SendAckReminder it honors the user's
// per-channel preferences and the 22:00-07:00 quiet-hours window (email/push
// suppressed in-window, in-app always delivered). The payload is delivered
// as-is; the caller sets Type:"policy_published" so EmailChannel selects the
// policy-published template kind. Per-send dedup (once per version+user) is
// enforced downstream by internal/mail's Deduper hook.
func (d *Dispatcher) SendPolicyPublished(ctx context.Context, p AckReminderPayload) {
	d.dispatch(ctx, p)
}

// SendPolicyRetired dispatches an informational "policy retired" notice to
// each enabled channel. Like SendPolicyPublished it honors the user's
// per-channel preferences and the 22:00-07:00 quiet-hours window (email/push
// suppressed in-window, in-app always delivered). The payload is delivered
// as-is; the caller sets Type:"policy_retired" so EmailChannel selects the
// policy-retired template kind.
func (d *Dispatcher) SendPolicyRetired(ctx context.Context, p AckReminderPayload) {
	d.dispatch(ctx, p)
}

// SendEscalationToManager delivers an escalation notification. The caller
// passes the resolved manager's UserID as the destination; UserID in the
// payload is treated as the notification recipient.
func (d *Dispatcher) SendEscalationToManager(ctx context.Context, p EscalationPayload) {
	d.deliverEscalation(ctx, p.UserID, p.PolicyVersionID, p.CampaignID)
}

// SendEscalationToManagerExplicit is the notify-native variant: callers that
// resolve the manager themselves (e.g. command handlers wiring directly to
// notify without going through the saga) hand over both source and manager
// ids in one struct.
func (d *Dispatcher) SendEscalationToManagerExplicit(ctx context.Context, p EscalationPayload) {
	d.deliverEscalation(ctx, p.ManagerUserID, p.PolicyVersionID, p.CampaignID)
}

func (d *Dispatcher) deliverEscalation(ctx context.Context, target, policyVersionID, campaignID string) {
	payload := AckReminderPayload{
		UserID:          target,
		PolicyVersionID: policyVersionID,
		CampaignID:      campaignID,
		Type:            "escalation",
	}
	d.dispatch(ctx, payload)
}

// SendWorkflowEscalation delivers a Phase 3 workflow approval escalation
// notification. The WorkflowRunID is mapped to the CampaignID slot so all
// downstream channels (in particular in-app) carry a single correlation id.
func (d *Dispatcher) SendWorkflowEscalation(ctx context.Context, p WorkflowEscalationPayload) {
	payload := AckReminderPayload{
		UserID:          p.ApproverUserID,
		PolicyVersionID: p.PolicyVersionID,
		CampaignID:      p.WorkflowRunID,
		Type:            "workflow_escalation",
	}
	d.dispatch(ctx, payload)
}

// userQuietHours returns true if the current time falls in the 22:00–07:00
// quiet window in the user's local timezone. Resolution errors and missing
// resolvers both degrade to UTC; the clock hook (WithClock) overrides the
// real time.Now lookup for tests. See QuietHours.InWindow for the shared
// implementation (also used by internal/mail's Suppress middleware).
func (d *Dispatcher) userQuietHours(ctx context.Context, userID string) bool {
	return d.quietHours.InWindow(ctx, userID)
}
