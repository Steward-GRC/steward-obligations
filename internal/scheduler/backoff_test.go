// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/scheduler"
)

func TestDecideBackoffSchedule(t *testing.T) {
	anchor := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	cfg := scheduler.BackoffConfig{EscalationAfter: 30 * 24 * time.Hour}

	cases := []struct {
		name          string
		afterDays     float64
		reminderCount int
		escalated     bool
		want          scheduler.Action
	}{
		{"day0 nothing due yet", 0, 0, false, scheduler.ActionNone},
		{"before first reminder (12h)", 0.5, 0, false, scheduler.ActionNone},
		{"first reminder due at +1d", 1, 0, false, scheduler.ActionReminder},
		{"second not yet at +2d (count=1 wants +3d)", 2, 1, false, scheduler.ActionNone},
		{"second reminder due at +3d", 3, 1, false, scheduler.ActionReminder},
		{"third reminder due at +7d", 7, 2, false, scheduler.ActionReminder},
		{"weekly cadence: +14d with count=3", 14, 3, false, scheduler.ActionReminder},
		{"weekly not yet: +10d with count=3 (wants +14d)", 10, 3, false, scheduler.ActionNone},
		{"escalate past threshold", 31, 5, false, scheduler.ActionEscalate},
		{"already escalated stops", 40, 5, true, scheduler.ActionNone},
		{"escalated stops even before threshold reminder", 3, 1, true, scheduler.ActionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := anchor.Add(time.Duration(tc.afterDays * float64(24*time.Hour)))
			got := scheduler.Decide(now, anchor, tc.reminderCount, tc.escalated, cfg)
			if got != tc.want {
				t.Errorf("Decide(+%.1fd, count=%d, escalated=%v) = %s; want %s",
					tc.afterDays, tc.reminderCount, tc.escalated, got, tc.want)
			}
		})
	}
}

// TestDecideEscalationDisabled verifies EscalationAfter=0 keeps reminders going
// and never escalates.
func TestDecideEscalationDisabled(t *testing.T) {
	anchor := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	cfg := scheduler.BackoffConfig{EscalationAfter: 0}
	now := anchor.Add(100 * 24 * time.Hour)
	if got := scheduler.Decide(now, anchor, 8, false, cfg); got != scheduler.ActionReminder {
		t.Errorf("with escalation disabled, far-overdue = %s; want reminder", got)
	}
}
