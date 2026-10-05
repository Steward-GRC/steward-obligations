// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// digestKind is the render-sidecar template kind both digests render.
const digestKind = "digest"

// windowDaily / windowWeekly are the outbox row window tokens and the pending-ack
// cadence tokens the drain matches against a user's digest window.
const (
	windowDaily  = "daily"
	windowWeekly = "weekly"
)

// OutboxDrainStore is the outbox read/drain seam. *store.NotificationOutboxStore
// satisfies it.
type OutboxDrainStore interface {
	PendingUsers(ctx context.Context) ([]string, error)
	ListPending(ctx context.Context, userID string) ([]store.OutboxRow, error)
	MarkSent(ctx context.Context, ids []string) error
}

// PendingAckLister reads a user's LIVE outstanding obligations for the
// pending-ack digest, so it is always current across restarts (never sourced
// from the outbox). *obligation.Resolver satisfies it via MyObligations.
type PendingAckLister interface {
	MyObligations(ctx context.Context, userID string) ([]obligation.ObligationItem, error)
}

// PendingAckCandidates lists users who fold compliance reminders into a digest
// (compliance category cadence daily/weekly, or a policy-ack-reminder/review-due
// override to a digest) — the pending-ack digest's candidate set. cmd/server
// unions the category + override store queries to satisfy it.
type PendingAckCandidates interface {
	PendingAckDigestUsers(ctx context.Context) ([]string, error)
}

// WindowReader reads a user's digest window. *store.NotificationDigestWindowStore
// satisfies it.
type WindowReader interface {
	Get(ctx context.Context, userID string) (store.DigestWindow, error)
}

// TZResolver resolves a user's IANA timezone so the digest window is evaluated
// in local time. It mirrors notify.TZResolver; nil (or an empty/errored result)
// degrades to UTC — the documented behavior until the Identity timezone slice
//
//	wires a real resolver.
type TZResolver interface {
	ResolveTimezone(ctx context.Context, userID string) (string, error)
}

// DigestCadenceResolver resolves a user's policy-ack-reminder disposition so the
// drain sends the pending-ack digest only to users on a batch (daily/weekly)
// cadence, using the resolved cadence to pick the window. *notifpolicy.Resolver
// satisfies it.
type DigestCadenceResolver interface {
	Resolve(ctx context.Context, userID, kind string) (notifpolicy.Decision, error)
}

// DigestSender renders and sends one digest email. *mail.Sender satisfies it
// (Send stamps the brand logo, runs the compliance middleware chain, and dedups
// via the durable deduper on the userID:digest:dedupRef key).
type DigestSender interface {
	Send(ctx context.Context, kind, userID, to, dedupRef string, vars any) error
}

// EmailResolver maps a user to their address. *identityGRPCAdapter satisfies it
// (mirrors notify.UserEmailResolver).
type EmailResolver interface {
	ResolveEmail(ctx context.Context, userID string) (string, error)
}

// OnceGuard is the durable once-per-window guard so a re-fired tick (the ticker
// runs every ~15 min while the local hour still matches) sends at most one
// digest of each kind per user per day. *store.NotificationSentStore satisfies
// it via AcquireOnce.
type OnceGuard interface {
	AcquireOnce(ctx context.Context, key string) (bool, error)
}

// Drain is the digest-drain job. At each user's
// digest window it assembles and sends TWO separate digests so a user can digest
// compliance but keep FYIs immediate (or vice-versa):
//
// - the GENERAL digest, drained from the notification_outbox (batched FYIs:
// new/updated policies, workflow notices, grants), then marks those rows sent;
// - the PENDING-ACK digest, assembled from LIVE outstanding obligations (never
// the outbox) so it is always current, for users who fold compliance into a
// digest.
//
// Digests send at the user's chosen window even inside quiet hours (§D5): the
// user picked the window, so the drain does not apply the 22:00-07:00 gate.
type Drain struct {
	outbox         OutboxDrainStore
	pendingAck     PendingAckLister
	candidates     PendingAckCandidates
	windows        WindowReader
	tz             TZResolver
	cadence        DigestCadenceResolver
	sender         DigestSender
	emails         EmailResolver
	guard          OnceGuard
	preferencesURL string
	portalURL      string
	nowFn          func() time.Time

	generalCtr metric.Int64Counter
	ackCtr     metric.Int64Counter
}

// DrainConfig collects the Drain's dependencies.
type DrainConfig struct {
	Outbox         OutboxDrainStore
	PendingAck     PendingAckLister
	Candidates     PendingAckCandidates
	Windows        WindowReader
	TZ             TZResolver // optional; nil ⇒ UTC
	Cadence        DigestCadenceResolver
	Sender         DigestSender
	Emails         EmailResolver
	Guard          OnceGuard
	PreferencesURL string
	PortalURL      string
}

const drainInstrumentation = "github.com/Steward-GRC/steward-obligations/internal/scheduler.Drain"

// NewDrain builds a Drain.
func NewDrain(cfg DrainConfig) *Drain {
	meter := otel.Meter(drainInstrumentation)
	gen, _ := meter.Int64Counter("cn_digest_general_sent_total", metric.WithDescription("general digests sent by the drain"))
	ack, _ := meter.Int64Counter("cn_digest_pending_ack_sent_total", metric.WithDescription("pending-ack digests sent by the drain"))
	return &Drain{
		outbox:         cfg.Outbox,
		pendingAck:     cfg.PendingAck,
		candidates:     cfg.Candidates,
		windows:        cfg.Windows,
		tz:             cfg.TZ,
		cadence:        cfg.Cadence,
		sender:         cfg.Sender,
		emails:         cfg.Emails,
		guard:          cfg.Guard,
		preferencesURL: cfg.PreferencesURL,
		portalURL:      cfg.PortalURL,
		nowFn:          time.Now,
		generalCtr:     gen,
		ackCtr:         ack,
	}
}

// WithClock injects the drain clock for deterministic tests.
func (d *Drain) WithClock(nowFn func() time.Time) *Drain {
	d.nowFn = nowFn
	return d
}

// DrainOnce performs one drain pass over both digests. Per-user errors are
// logged and skipped so one recipient never starves the rest; a top-level
// enumeration error is returned for the ticker to log.
func (d *Drain) DrainOnce(ctx context.Context) error {
	now := d.nowFn()
	logger := logctx.From(ctx)

	users, err := d.outbox.PendingUsers(ctx)
	if err != nil {
		return fmt.Errorf("scheduler/drain: list pending users: %w", err)
	}
	for _, u := range users {
		if err := d.drainGeneral(ctx, now, u); err != nil {
			logger.Warn().Err(err).Str("user_id", u).Msg("scheduler/drain: general digest skipped after error")
		}
	}

	cands, err := d.candidates.PendingAckDigestUsers(ctx)
	if err != nil {
		return fmt.Errorf("scheduler/drain: list pending-ack candidates: %w", err)
	}
	for _, u := range cands {
		if err := d.drainPendingAck(ctx, now, u); err != nil {
			logger.Warn().Err(err).Str("user_id", u).Msg("scheduler/drain: pending-ack digest skipped after error")
		}
	}
	return nil
}

// drainGeneral assembles and sends the general digest for one user from the
// outbox rows whose window is due now, then marks exactly those rows sent.
func (d *Drain) drainGeneral(ctx context.Context, now time.Time, userID string) error {
	win, err := d.windows.Get(ctx, userID)
	if err != nil {
		return fmt.Errorf("window: %w", err)
	}
	local := d.local(ctx, now, userID)

	rows, err := d.outbox.ListPending(ctx, userID)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	var due []store.OutboxRow
	for _, r := range rows {
		if dueNow(r.WindowKind, win, local) {
			due = append(due, r)
		}
	}
	if len(due) == 0 {
		return nil
	}

	// One general digest per user per day; a re-fired tick within the hour loses.
	ok, err := d.guard.AcquireOnce(ctx, fmt.Sprintf("digest:general:%s:%s", userID, local.Format("2006-01-02")))
	if err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	if !ok {
		return nil
	}

	to, err := d.emails.ResolveEmail(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolve email: %w", err)
	}

	vars := d.baseVars(local)
	ids := make([]string, 0, len(due))
	for _, r := range due {
		addSection(vars, r)
		ids = append(ids, r.ID)
	}

	if err := d.sender.Send(ctx, digestKind, userID, to, "general:"+local.Format("2006-01-02"), vars); err != nil {
		return fmt.Errorf("send general digest: %w", err)
	}
	if err := d.outbox.MarkSent(ctx, ids); err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	d.count(ctx, d.generalCtr)
	return nil
}

// drainPendingAck assembles and sends the pending-ack digest for one user from
// LIVE obligations, for users whose compliance cadence resolves to a digest.
func (d *Drain) drainPendingAck(ctx context.Context, now time.Time, userID string) error {
	dec, err := d.cadence.Resolve(ctx, userID, "policy-ack-reminder")
	if err != nil {
		return fmt.Errorf("resolve cadence: %w", err)
	}
	if dec.Mode != notifpolicy.ModeBatch {
		return nil // immediate users get sweep reminders, not a digest.
	}
	windowKind := string(dec.Cadence)

	win, err := d.windows.Get(ctx, userID)
	if err != nil {
		return fmt.Errorf("window: %w", err)
	}
	local := d.local(ctx, now, userID)
	if !dueNow(windowKind, win, local) {
		return nil
	}

	obligations, err := d.pendingAck.MyObligations(ctx, userID)
	if err != nil {
		return fmt.Errorf("my obligations: %w", err)
	}
	if len(obligations) == 0 {
		return nil
	}

	ok, err := d.guard.AcquireOnce(ctx, fmt.Sprintf("digest:ack:%s:%s", userID, local.Format("2006-01-02")))
	if err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	if !ok {
		return nil
	}

	to, err := d.emails.ResolveEmail(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolve email: %w", err)
	}

	vars := d.baseVars(local)
	awaiting := make([]map[string]any, 0, len(obligations))
	for _, o := range obligations {
		awaiting = append(awaiting, map[string]any{
			"title":       o.Title,
			"meta":        obligationMeta(o),
			"actionHref":  d.ackURL(o.PolicyVersionID),
			"actionLabel": "Acknowledge",
		})
	}
	vars["awaitingAck"] = awaiting

	if err := d.sender.Send(ctx, digestKind, userID, to, "ack:"+local.Format("2006-01-02"), vars); err != nil {
		return fmt.Errorf("send pending-ack digest: %w", err)
	}
	d.count(ctx, d.ackCtr)
	return nil
}

// baseVars builds the shared DigestProperties skeleton (all four sections empty,
// standard footer/period), matching render-sidecar/src/templates/digest.tsx.
func (d *Drain) baseVars(local time.Time) map[string]any {
	return map[string]any{
		"approvals":      []map[string]any{},
		"awaitingAck":    []map[string]any{},
		"newAndUpdated":  []map[string]any{},
		"other":          []map[string]any{},
		"periodLabel":    local.Format("Monday, January 2, 2006"),
		"portalUrl":      d.portalURL,
		"preferencesUrl": d.preferencesURL,
		"recipientName":  "there", // real display name lands with the Identity slice.
	}
}

// local resolves now in the user's timezone, degrading to UTC when no TZ
// resolver is wired or resolution fails.
func (d *Drain) local(ctx context.Context, now time.Time, userID string) time.Time {
	loc := time.UTC
	if d.tz != nil {
		if name, err := d.tz.ResolveTimezone(ctx, userID); err == nil && name != "" {
			if l, err := time.LoadLocation(name); err == nil {
				loc = l
			}
		}
	}
	return now.In(loc)
}

func (d *Drain) ackURL(versionID string) string {
	if d.portalURL == "" {
		return versionID
	}
	return fmt.Sprintf("%s/%s", d.portalURL, versionID)
}

func (d *Drain) count(ctx context.Context, c metric.Int64Counter) {
	if c != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("service", "compliance-notify")))
	}
}

// dueNow reports whether a row/candidate with the given window token is due at
// local time now: the daily hour must match, and a weekly window also requires
// the ISO weekday to match. A daily window is due every day at its hour, so on
// the weekly day both daily and weekly items drain into the same digest.
func dueNow(windowKind string, w store.DigestWindow, local time.Time) bool {
	if local.Hour() != w.DailyHour {
		return false
	}
	if windowKind == windowWeekly {
		return isoWeekday(local) == w.WeeklyDOW
	}
	return true
}

// isoWeekday maps Go's Sunday=0..Saturday=6 to ISO Monday=1..Sunday=7.
func isoWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

// addSection routes one outbox row into the correct digest section by its
// category/kind, mirroring the four fixed sections in digest.tsx.
func addSection(vars map[string]any, r store.OutboxRow) {
	item := map[string]any{
		"title":       str(r.Vars["title"]),
		"meta":        str(r.Vars["meta"]),
		"actionHref":  str(r.Vars["actionHref"]),
		"actionLabel": str(r.Vars["actionLabel"]),
	}
	var key string
	switch {
	case r.Category == string(notifpolicy.CategoryCompliance):
		key = "awaitingAck"
	case r.Category == string(notifpolicy.CategoryWorkflow):
		key = "approvals"
	case r.Kind == "policy-published" || r.Kind == "policy-retired":
		key = "newAndUpdated"
	default:
		key = "other"
	}
	vars[key] = append(vars[key].([]map[string]any), item)
}

// obligationMeta renders the secondary line for a pending-ack item, e.g.
// "POL-014 v3".
func obligationMeta(o obligation.ObligationItem) string {
	if o.VersionNo > 0 {
		return fmt.Sprintf("%s v%d", o.Number, o.VersionNo)
	}
	return o.Number
}

// str coerces a JSONB-decoded value to a string, tolerating a missing key.
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
