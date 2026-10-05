// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// RenderedSender replays a held OutboxItem back through the send path. *Sender
// satisfies it via SendRendered; the drainer depends on only this method.
type RenderedSender interface {
	SendRendered(ctx context.Context, it OutboxItem) error
}

// Drainer tuning defaults.
const (
	// DefaultDrainRatePerSec caps replay throughput so flushing a backlog after
	// recovery does not spike Mailgun or immediately re-trip the usage limit.
	DefaultDrainRatePerSec = 10
	// DefaultDrainBatchSize is how many rows a claim pulls per round.
	DefaultDrainBatchSize = 100
	// DefaultDrainInterval is the periodic sweep cadence (a safety net beside
	// the breaker-close trigger).
	DefaultDrainInterval = 30 * time.Second
	// DefaultDrainMaxAttempts is how many transient replay failures a row
	// tolerates before it is marked failed.
	DefaultDrainMaxAttempts = 8
	// drainRetryBase is the base backoff for a transient replay failure.
	drainRetryBase = 30 * time.Second
	// drainRetryCap bounds the backoff.
	drainRetryCap = 30 * time.Minute
)

// Drainer replays the durable mail_outbox when Mailgun recovers. It wakes on an
// explicit trigger (fired by the circuit-breaker closing) and on a periodic
// tick, claims pending rows oldest-first, and replays each through the send
// path at a configurable rate cap. Each row is marked sent, failed (permanent),
// or rescheduled (transient) exactly once; a redelivery never double-sends
// because a sent row is never re-claimed and the outbox is dedup-idempotent on
// its dedup key. If a replay returns ErrMailPaused (the breaker reopened
// mid-drain), the drainer stops the round and leaves the remaining rows pending
// for the next recovery.
type Drainer struct {
	store       OutboxStore
	sender      RenderedSender
	ratePerSec  int
	batchSize   int
	interval    time.Duration
	maxAttempts int
	logger      zerolog.Logger

	trigger chan struct{}
	pace    func(ctx context.Context)
	nowFn   func() time.Time
}

// DrainerOption customizes a Drainer.
type DrainerOption func(*Drainer)

// WithDrainRate sets the replay rate cap in messages/second (non-positive keeps
// the default).
func WithDrainRate(n int) DrainerOption {
	return func(d *Drainer) {
		if n > 0 {
			d.ratePerSec = n
		}
	}
}

// WithDrainBatchSize sets the per-claim batch size (non-positive keeps default).
func WithDrainBatchSize(n int) DrainerOption {
	return func(d *Drainer) {
		if n > 0 {
			d.batchSize = n
		}
	}
}

// WithDrainInterval sets the periodic sweep cadence (non-positive keeps default).
func WithDrainInterval(dur time.Duration) DrainerOption {
	return func(d *Drainer) {
		if dur > 0 {
			d.interval = dur
		}
	}
}

// WithDrainMaxAttempts sets the transient-failure tolerance before a row is
// marked failed (non-positive keeps default).
func WithDrainMaxAttempts(n int) DrainerOption {
	return func(d *Drainer) {
		if n > 0 {
			d.maxAttempts = n
		}
	}
}

// WithDrainClock injects a clock (tests).
func WithDrainClock(nowFn func() time.Time) DrainerOption {
	return func(d *Drainer) {
		if nowFn != nil {
			d.nowFn = nowFn
		}
	}
}

// WithDrainPacer overrides the inter-send rate limiter (tests inject a
// non-sleeping counter to assert the cap deterministically).
func WithDrainPacer(pace func(ctx context.Context)) DrainerOption {
	return func(d *Drainer) {
		if pace != nil {
			d.pace = pace
		}
	}
}

// NewDrainer builds a Drainer over store + sender. logger is a go-log logger.
func NewDrainer(store OutboxStore, sender RenderedSender, logger zerolog.Logger, opts ...DrainerOption) *Drainer {
	d := &Drainer{
		store:       store,
		sender:      sender,
		ratePerSec:  DefaultDrainRatePerSec,
		batchSize:   DefaultDrainBatchSize,
		interval:    DefaultDrainInterval,
		maxAttempts: DefaultDrainMaxAttempts,
		logger:      logger,
		trigger:     make(chan struct{}, 1),
		nowFn:       time.Now,
	}
	for _, o := range opts {
		o(d)
	}
	if d.pace == nil {
		d.pace = d.sleepPacer
	}
	return d
}

// Trigger wakes the drainer to run a round now (coalesced: a pending trigger is
// not queued twice). Safe to call from the breaker's onClose callback.
func (d *Drainer) Trigger() {
	select {
	case d.trigger <- struct{}{}:
	default:
	}
}

// Run drives the drainer until ctx is cancelled: it drains on each trigger and
// on each periodic tick, and returns cleanly on shutdown (graceful stop).
func (d *Drainer) Run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	d.logger.Info().Int("rate_per_sec", d.ratePerSec).Dur("interval", d.interval).Msg("mail outbox drainer started")
	for {
		select {
		case <-ctx.Done():
			d.logger.Info().Msg("mail outbox drainer stopped")
			return
		case <-d.trigger:
			d.DrainOnce(ctx)
		case <-t.C:
			d.DrainOnce(ctx)
		}
	}
}

// DrainOnce claims and replays pending rows until the backlog is empty, the
// breaker reopens (ErrMailPaused), or ctx is cancelled. It is exported so tests
// can drive a single deterministic round.
func (d *Drainer) DrainOnce(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		items, err := d.store.ClaimPending(ctx, d.batchSize)
		if err != nil {
			d.logger.Error().Err(err).Msg("mail outbox drainer: claim pending failed")
			return
		}
		if len(items) == 0 {
			return
		}
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			d.pace(ctx)
			if d.replay(ctx, it) == stopDrain {
				return
			}
		}
		if len(items) < d.batchSize {
			return // last (partial) batch drained
		}
	}
}

// drainOutcome tells the batch loop whether to keep going or stop this round.
type drainOutcome int

const (
	continueDrain drainOutcome = iota
	stopDrain
)

// replay sends one held row and finalizes it. It returns stopDrain when the
// breaker has reopened (the row stays pending for the next recovery); otherwise
// continueDrain.
func (d *Drainer) replay(ctx context.Context, it OutboxItem) drainOutcome {
	err := d.sender.SendRendered(ctx, it)
	switch {
	case err == nil:
		if merr := d.store.MarkSent(ctx, it.ID); merr != nil {
			d.logger.Error().Err(merr).Str("id", it.ID).Msg("mail outbox drainer: mark sent failed")
		}
		d.logger.Debug().Str("id", it.ID).Str("kind", it.Kind).Msg("mail outbox drainer: replayed")
		return continueDrain

	case IsPaused(err):
		// Breaker reopened mid-drain: stop, leave this and the rest pending.
		d.logger.Warn().Str("id", it.ID).Msg("mail outbox drainer: paused again — deferring remaining backlog")
		return stopDrain

	case isTransientError(err):
		attempts := it.Attempts + 1
		if attempts >= d.maxAttempts {
			d.markFailed(ctx, it, err)
			return continueDrain
		}
		next := d.nowFn().Add(backoff(attempts))
		if rerr := d.store.Reschedule(ctx, it.ID, err.Error(), next); rerr != nil {
			d.logger.Error().Err(rerr).Str("id", it.ID).Msg("mail outbox drainer: reschedule failed")
		}
		d.logger.Warn().Str("id", it.ID).Int("attempts", attempts).Msg("mail outbox drainer: transient replay failure — rescheduled")
		return continueDrain

	default:
		// Permanent per-message error (e.g. bad recipient): terminal.
		d.markFailed(ctx, it, err)
		return continueDrain
	}
}

func (d *Drainer) markFailed(ctx context.Context, it OutboxItem, err error) {
	if ferr := d.store.MarkFailed(ctx, it.ID, err.Error()); ferr != nil {
		d.logger.Error().Err(ferr).Str("id", it.ID).Msg("mail outbox drainer: mark failed failed")
	}
	d.logger.Warn().Str("id", it.ID).Str("kind", it.Kind).Msg("mail outbox drainer: replay permanently failed")
}

// backoff returns the exponential backoff for the nth transient attempt,
// capped at drainRetryCap.
func backoff(attempt int) time.Duration {
	d := drainRetryBase << uint(attempt-1)
	if d > drainRetryCap || d <= 0 {
		return drainRetryCap
	}
	return d
}

// sleepPacer enforces the rate cap by sleeping 1/rate between sends, honoring
// ctx cancellation.
func (d *Drainer) sleepPacer(ctx context.Context) {
	if d.ratePerSec <= 0 {
		return
	}
	wait := time.Second / time.Duration(d.ratePerSec)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
