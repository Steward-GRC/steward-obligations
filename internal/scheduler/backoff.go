// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package scheduler holds the obligations service's wall-clock jobs (
// ): the reminder sweep ( back-off + escalate-once) and the digest drain
// ( outbox + pending-ack). The jobs are plain structs with a single
// entry-point method (Sweep.Run, Drain.DrainOnce) so they run identically from
// the in-service ticker (the approved D3 shape) OR a future Kubernetes CronJob
// main — the ticker just calls them under the advisory-lock leader (see
// leader.go, ticker.go), keeping D3 flippable without reworking the job logic.
package scheduler

import "time"

// Action is the sweep's decision for one outstanding obligation.
type Action int

const (
	// ActionNone means nothing is due this tick.
	ActionNone Action = iota
	// ActionReminder means the next recurring reminder is due.
	ActionReminder
	// ActionEscalate means the obligation is overdue past the escalation
	// threshold and the manager escalation should fire (once).
	ActionEscalate
)

// String renders an Action for logs/metrics.
func (a Action) String() string {
	switch a {
	case ActionReminder:
		return "reminder"
	case ActionEscalate:
		return "escalate"
	default:
		return "none"
	}
}

// BackoffConfig tunes the recurring-compliance schedule.
type BackoffConfig struct {
	// EscalationAfter is how long after the anchor (day 0 = first ack demand) an
	// obligation is considered overdue enough to escalate to the manager once.
	// Zero disables escalation (reminders continue on the back-off cadence).
	EscalationAfter time.Duration
}

// day is 24h; the schedule is expressed in whole days from the anchor.
const day = 24 * time.Hour

// reminderDueDay returns the number of days after the anchor at which the Nth
// recurring reminder (0-based; the first ack demand is not counted) is due,
// realizing the back-off: +1d, +3d, +7d, then weekly (+7d each).
//
//	count 0 -> day 1
//	count 1 -> day 3
//	count 2 -> day 7
//	count 3 -> day 14, count 4 -> day 21,... (weekly)
func reminderDueDay(count int) int {
	switch {
	case count <= 0:
		return 1
	case count == 1:
		return 3
	default:
		return 7 * (count - 1)
	}
}

// Decide computes the sweep action for one outstanding (user, version)
// obligation. The schedule is anchored at day 0 (the first ack demand,
// persisted as first_notified_at) rather than a per-policy due date, because
// core surfaces no ack due date to the obligations service today; the anchor is
// exactly the per-(user, version) state says to persist.
//
// - Already escalated -> ActionNone: the manager owns it; stop pestering the
// user directly.
// - Overdue past EscalationAfter -> ActionEscalate (fires once; the caller
// records escalated_at so the next tick returns None).
// - Otherwise, the next reminder is due when the elapsed time since the anchor
// reaches its back-off step (reminderDueDay(reminderCount)); before that,
// ActionNone.
func Decide(now, anchor time.Time, reminderCount int, escalated bool, cfg BackoffConfig) Action {
	if escalated {
		return ActionNone
	}
	elapsed := now.Sub(anchor)
	if elapsed < 0 {
		return ActionNone
	}
	if cfg.EscalationAfter > 0 && elapsed >= cfg.EscalationAfter {
		return ActionEscalate
	}
	if elapsed >= time.Duration(reminderDueDay(reminderCount))*day {
		return ActionReminder
	}
	return ActionNone
}
