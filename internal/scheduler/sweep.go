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
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// OutstandingLister enumerates the live (user, version) obligations the sweep
// re-evaluates. *obligation.Resolver satisfies it.
type OutstandingLister interface {
	OutstandingObligations(ctx context.Context) ([]obligation.Outstanding, error)
}

// BackoffStore persists the per-(user, version) back-off + escalate-once state.
// *store.UserVersionNotifiedStore satisfies it.
type BackoffStore interface {
	EnsureAnchor(ctx context.Context, userID, versionID string, at time.Time) (store.BackoffState, error)
	RecordReminder(ctx context.Context, userID, versionID string, at time.Time) error
	RecordEscalation(ctx context.Context, userID, versionID string, at time.Time) error
}

// ReminderSender delivers a recurring ack reminder. *notify.Dispatcher
// satisfies it.
type ReminderSender interface {
	SendAckReminder(ctx context.Context, p notify.AckReminderPayload)
}

// Escalator delivers the manager escalation — the long-dead
// Dispatcher.SendEscalationToManager path the sweep finally
// wires. *notify.Dispatcher satisfies it.
type Escalator interface {
	SendEscalationToManager(ctx context.Context, p notify.EscalationPayload)
}

// CadenceResolver reports how a user's policy-ack-reminder cadence is realized so
// the sweep sends an immediate reminder only when the user is on the immediate
// cadence; a user who chose a digest has their reminders folded into the
// pending-ack digest by the drain instead. *notifpolicy.Resolver satisfies it.
type CadenceResolver interface {
	Resolve(ctx context.Context, userID, kind string) (notifpolicy.Decision, error)
}

// ManagerResolver maps an obligated user to the manager who receives the
// escalation. Identity exposes no manager RPC today, so cmd/server wires a
// resolver that reports ok=false (the escalation is still recorded so the sweep
// stops pestering the user; the actual manager send lands when a manager
// resolver exists — a follow-up). Tests inject a fake that returns
// a manager to prove the escalation path fires exactly once.
type ManagerResolver interface {
	ResolveManager(ctx context.Context, userID string) (managerUserID string, ok bool, err error)
}

// Sweep is the reminder-sweep job. Each pass it
// re-evaluates every outstanding obligation and, per (user, policy_version),
// applies the back-off schedule: send the next reminder when due (immediate
// users only), or escalate once to the manager when overdue past the threshold
// and then stop reminding the user. Digest users' reminders are handled by the
// drain, so the sweep skips their immediate reminder but still escalates them.
type Sweep struct {
	lister    OutstandingLister
	state     BackoffStore
	reminders ReminderSender
	escalator Escalator
	cadence   CadenceResolver
	managers  ManagerResolver
	cfg       BackoffConfig
	portalURL string
	nowFn     func() time.Time

	sweptCtr  metric.Int64Counter
	remindCtr metric.Int64Counter
	escCtr    metric.Int64Counter
	skipCtr   metric.Int64Counter
}

// SweepConfig collects the Sweep's dependencies.
type SweepConfig struct {
	Lister    OutstandingLister
	State     BackoffStore
	Reminders ReminderSender
	Escalator Escalator
	Cadence   CadenceResolver
	Managers  ManagerResolver
	Backoff   BackoffConfig
	// PortalURL builds each reminder's ack link (base + "/" + versionID),
	// matching the consumer/EmailChannel derivation.
	PortalURL string
}

const sweepInstrumentation = "github.com/Steward-GRC/steward-obligations/internal/scheduler.Sweep"

// NewSweep builds a Sweep. The otel meter is resolved from the global provider
// (same posture as the rest of the service).
func NewSweep(cfg SweepConfig) *Sweep {
	meter := otel.Meter(sweepInstrumentation)
	swept, _ := meter.Int64Counter("cn_sweep_obligations_total", metric.WithDescription("outstanding obligations evaluated by the reminder sweep"))
	remind, _ := meter.Int64Counter("cn_sweep_reminders_total", metric.WithDescription("recurring ack reminders sent by the sweep"))
	esc, _ := meter.Int64Counter("cn_sweep_escalations_total", metric.WithDescription("manager escalations fired by the sweep"))
	skip, _ := meter.Int64Counter("cn_sweep_digest_deferred_total", metric.WithDescription("reminders deferred to the digest for digest-cadence users"))
	return &Sweep{
		lister:    cfg.Lister,
		state:     cfg.State,
		reminders: cfg.Reminders,
		escalator: cfg.Escalator,
		cadence:   cfg.Cadence,
		managers:  cfg.Managers,
		cfg:       cfg.Backoff,
		portalURL: cfg.PortalURL,
		nowFn:     time.Now,
		sweptCtr:  swept,
		remindCtr: remind,
		escCtr:    esc,
		skipCtr:   skip,
	}
}

// WithClock injects the sweep clock for deterministic tests.
func (s *Sweep) WithClock(nowFn func() time.Time) *Sweep {
	s.nowFn = nowFn
	return s
}

// Run performs one sweep pass. It never returns partway on a per-obligation
// error — one bad recipient must not starve the rest — but propagates a
// top-level enumeration error so the ticker logs it.
func (s *Sweep) Run(ctx context.Context) error {
	now := s.nowFn()
	logger := logctx.From(ctx)

	items, err := s.lister.OutstandingObligations(ctx)
	if err != nil {
		return fmt.Errorf("scheduler/sweep: list outstanding: %w", err)
	}

	for _, o := range items {
		s.count(ctx, s.sweptCtr)
		if err := s.handle(ctx, now, o); err != nil {
			logger.Warn().Err(err).
				Str("user_id", o.UserID).Str("policy_version_id", o.PolicyVersionID).
				Msg("scheduler/sweep: obligation skipped after error")
		}
	}
	return nil
}

// handle applies the back-off decision for one obligation.
func (s *Sweep) handle(ctx context.Context, now time.Time, o obligation.Outstanding) error {
	// Ensure a stable anchor exists (day 0) and read the current back-off state.
	st, err := s.state.EnsureAnchor(ctx, o.UserID, o.PolicyVersionID, now)
	if err != nil {
		return fmt.Errorf("ensure anchor: %w", err)
	}

	switch Decide(now, st.FirstNotifiedAt, st.ReminderCount, st.Escalated, s.cfg) {
	case ActionEscalate:
		return s.escalate(ctx, now, o)
	case ActionReminder:
		return s.remind(ctx, now, o)
	default:
		return nil
	}
}

// remind sends the next recurring reminder — but only for users on the immediate
// cadence. A user who folded compliance into a digest has this reminder
// delivered by the drain (from live obligations), so the sweep neither sends nor
// advances the immediate back-off count for them.
func (s *Sweep) remind(ctx context.Context, now time.Time, o obligation.Outstanding) error {
	if s.cadence != nil {
		dec, err := s.cadence.Resolve(ctx, o.UserID, "policy-ack-reminder")
		if err != nil {
			return fmt.Errorf("resolve cadence: %w", err)
		}
		if dec.Mode == notifpolicy.ModeBatch {
			s.count(ctx, s.skipCtr)
			return nil
		}
	}

	s.reminders.SendAckReminder(ctx, notify.AckReminderPayload{
		UserID:          o.UserID,
		PolicyVersionID: o.PolicyVersionID,
		Type:            "reminder",
		Policies: []notify.AckItem{{
			Ref:    ackRef(o),
			Title:  o.Title,
			AckURL: s.ackURL(o.PolicyVersionID),
		}},
	})
	if err := s.state.RecordReminder(ctx, o.UserID, o.PolicyVersionID, now); err != nil {
		return fmt.Errorf("record reminder: %w", err)
	}
	s.count(ctx, s.remindCtr)
	return nil
}

// escalate fires the manager escalation once (the T3 path) and
// records escalated_at so the user is no longer pestered directly. The
// escalation is recorded even when no manager is resolvable — the schedule must
// stop regardless — but the send only happens when a manager is known.
func (s *Sweep) escalate(ctx context.Context, now time.Time, o obligation.Outstanding) error {
	logger := logctx.From(ctx)
	mgr, ok, err := s.managers.ResolveManager(ctx, o.UserID)
	if err != nil {
		return fmt.Errorf("resolve manager: %w", err)
	}
	if ok && mgr != "" {
		s.escalator.SendEscalationToManager(ctx, notify.EscalationPayload{
			UserID:          mgr,
			PolicyVersionID: o.PolicyVersionID,
		})
		s.count(ctx, s.escCtr)
	} else {
		logger.Warn().Str("user_id", o.UserID).Str("policy_version_id", o.PolicyVersionID).
			Msg("scheduler/sweep: overdue past escalation threshold but no manager resolved; recording escalation and stopping user reminders")
	}
	if err := s.state.RecordEscalation(ctx, o.UserID, o.PolicyVersionID, now); err != nil {
		return fmt.Errorf("record escalation: %w", err)
	}
	return nil
}

func (s *Sweep) ackURL(versionID string) string {
	if s.portalURL == "" {
		return versionID
	}
	return fmt.Sprintf("%s/%s", s.portalURL, versionID)
}

// ackRef formats the human policy reference shown in a reminder, e.g. "POL-014 v3".
func ackRef(o obligation.Outstanding) string {
	if o.VersionNo > 0 {
		return fmt.Sprintf("%s v%d", o.Number, o.VersionNo)
	}
	return o.Number
}

func (s *Sweep) count(ctx context.Context, c metric.Int64Counter) {
	if c != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("service", "compliance-notify")))
	}
}
