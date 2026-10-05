// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// Job is one unit of scheduled work run under the leader lock each tick. Sweep
// and Drain each expose a method that satisfies it, and a CronJob main could
// call the same methods directly — the ticker is just one host for them (D3).
type Job struct {
	// Name labels the job in logs.
	Name string
	// Run performs one pass. An error is logged; it never stops the ticker.
	Run func(ctx context.Context) error
}

// Ticker runs its jobs on a fixed wall-clock interval, but only on the replica
// that holds the advisory-lock leadership for that tick (
// ). All jobs of a tick run inside a single leader lock so the sweep and drain
// see a consistent single-writer window.
type Ticker struct {
	interval time.Duration
	leader   *Leader
	jobs     []Job
}

// NewTicker builds a Ticker. interval is the poll cadence (~15 min per).
func NewTicker(interval time.Duration, leader *Leader, jobs ...Job) *Ticker {
	return &Ticker{interval: interval, leader: leader, jobs: jobs}
}

// Run blocks until ctx is cancelled, firing the jobs each interval under the
// leader lock. It runs one pass immediately on start (so a freshly-elected
// leader doesn't wait a full interval) and then on the interval.
func (t *Ticker) Run(ctx context.Context) {
	t.tick(ctx)
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.tick(ctx)
		}
	}
}

// tick runs every job under one leader lock. A non-leader tick is a quiet no-op.
func (t *Ticker) tick(ctx context.Context) {
	logger := logctx.From(ctx)
	ran, err := t.leader.WithLock(ctx, func(ctx context.Context) error {
		for _, j := range t.jobs {
			if err := j.Run(ctx); err != nil {
				logger.Error().Err(err).Str("job", j.Name).Msg("scheduler: job failed")
			}
		}
		return nil
	})
	if err != nil {
		logger.Warn().Err(err).Msg("scheduler: leader lock error; skipping tick")
		return
	}
	if !ran {
		logger.Debug().Msg("scheduler: not leader this tick; skipping")
	}
}
