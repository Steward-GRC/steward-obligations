// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"errors"
	"sync"
	"time"

	email "github.com/Bugs5382/go-email"
	"github.com/rs/zerolog"
)

// BreakerState is the circuit-breaker's coarse health state feeding the
// pause-gate: Closed (send normally), Open (hold — Mailgun is down), or
// HalfOpen (allow a single probe to test recovery).
type BreakerState int

const (
	// BreakerClosed: Mailgun looks healthy; sends flow.
	BreakerClosed BreakerState = iota
	// BreakerOpen: Mailgun is failing; the pause-gate holds every send until a
	// cooldown elapses and a probe is allowed.
	BreakerOpen
	// BreakerHalfOpen: cooldown elapsed; one probe send is permitted. Its
	// outcome closes (success) or re-opens (failure) the breaker.
	BreakerHalfOpen
)

// String renders a BreakerState for logs.
func (s BreakerState) String() string {
	switch s {
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// Default breaker tuning. Threshold is consecutive transient/5xx failures
// before opening; an auth failure opens immediately regardless. Cooldown is how
// long Open holds before a half-open probe is allowed.
const (
	DefaultBreakerThreshold = 5
	DefaultBreakerCooldown  = 30 * time.Second
)

// Breaker is the Mailgun circuit-breaker. It is driven by send outcomes
// (RecordSuccess / RecordFailure, wired through BreakerTransport) and queried
// by the pause-gate (via Allow / State). On a successful half-open probe it
// closes AND fires onClose so the drainer replays the held backlog. All state
// transitions are logged via the injected go-log logger.
type Breaker struct {
	mu          sync.Mutex
	state       BreakerState
	consecFails int
	threshold   int
	cooldown    time.Duration
	openedAt    time.Time

	nowFn   func() time.Time
	onClose func()
	logger  zerolog.Logger
}

// BreakerOption customizes a Breaker.
type BreakerOption func(*Breaker)

// WithBreakerThreshold sets the consecutive-failure count that opens the
// breaker (non-positive falls back to the default).
func WithBreakerThreshold(n int) BreakerOption {
	return func(b *Breaker) {
		if n > 0 {
			b.threshold = n
		}
	}
}

// WithBreakerCooldown sets how long Open holds before a half-open probe
// (non-positive falls back to the default).
func WithBreakerCooldown(d time.Duration) BreakerOption {
	return func(b *Breaker) {
		if d > 0 {
			b.cooldown = d
		}
	}
}

// WithBreakerClock injects a clock for deterministic tests.
func WithBreakerClock(nowFn func() time.Time) BreakerOption {
	return func(b *Breaker) {
		if nowFn != nil {
			b.nowFn = nowFn
		}
	}
}

// WithBreakerOnClose registers a callback fired (in its own goroutine) each
// time the breaker closes after recovery — wired to the drainer's trigger so a
// backlog drains the moment Mailgun comes back.
func WithBreakerOnClose(fn func()) BreakerOption {
	return func(b *Breaker) { b.onClose = fn }
}

// NewBreaker builds a Breaker starting Closed. logger receives the state
// transition log lines (pass a go-log logger, e.g. log.New(service)).
func NewBreaker(logger zerolog.Logger, opts ...BreakerOption) *Breaker {
	b := &Breaker{
		state:     BreakerClosed,
		threshold: DefaultBreakerThreshold,
		cooldown:  DefaultBreakerCooldown,
		nowFn:     time.Now,
		logger:    logger,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// State returns the current state (transitioning Open→HalfOpen if the cooldown
// has elapsed, so a caller reading State sees recovery availability). It does
// NOT reserve a probe; use Allow for that.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()
	return b.state
}

// Allow reports whether a send may proceed now. Closed allows; Open denies
// until the cooldown elapses, at which point it promotes to HalfOpen and
// allows recovery probes through (a probe's outcome, reported via
// RecordSuccess/RecordFailure, then closes or re-opens the breaker). Allowing
// more than one send during HalfOpen is intentional — the first success
// closes, and it avoids wedging on a probe whose outcome never lands (e.g. the
// send was deduped/suppressed downstream).
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()
	return b.state != BreakerOpen
}

// maybeHalfOpenLocked promotes Open→HalfOpen once the cooldown has elapsed.
// Caller holds b.mu.
func (b *Breaker) maybeHalfOpenLocked() {
	if b.state == BreakerOpen && !b.nowFn().Before(b.openedAt.Add(b.cooldown)) {
		b.transitionLocked(BreakerHalfOpen, "cooldown elapsed")
	}
}

// RecordSuccess reports a delivered send. In HalfOpen it closes the breaker
// (and fires onClose); in any state it clears the consecutive-failure count.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	var fireClose bool
	if b.state != BreakerClosed {
		b.transitionLocked(BreakerClosed, "probe succeeded")
		fireClose = true
	}
	b.consecFails = 0
	onClose := b.onClose
	b.mu.Unlock()

	if fireClose && onClose != nil {
		go onClose()
	}
}

// RecordFailure reports a failed send. An auth failure (ErrMailgunAuth) opens
// the breaker immediately (a bad/absent key will not fix itself per message);
// otherwise it opens once consecutive failures reach the threshold. A failure
// during a half-open probe re-opens immediately.
func (b *Breaker) RecordFailure(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecFails++
	hardAuth := errors.Is(err, ErrMailgunAuth)

	switch b.state {
	case BreakerHalfOpen:
		b.openLocked("probe failed")
	case BreakerClosed:
		if hardAuth {
			b.openLocked("auth failure")
		} else if b.consecFails >= b.threshold {
			b.openLocked("failure threshold reached")
		}
	default: // already Open
		if hardAuth {
			b.openedAt = b.nowFn() // extend cooldown on a fresh auth failure
		}
	}
}

// openLocked transitions to Open and stamps openedAt. Caller holds b.mu.
func (b *Breaker) openLocked(reason string) {
	b.openedAt = b.nowFn()
	b.transitionLocked(BreakerOpen, reason)
}

// transitionLocked records + logs a state change. Caller holds b.mu.
func (b *Breaker) transitionLocked(to BreakerState, reason string) {
	from := b.state
	if from == to {
		return
	}
	b.state = to
	b.logger.Warn().
		Str("component", "mailgun-breaker").
		Str("from", from.String()).
		Str("to", to.String()).
		Str("reason", reason).
		Int("consecutive_failures", b.consecFails).
		Msg("mailgun circuit-breaker state change")
}

// BreakerTransport wraps the Mailgun leg to feed the Breaker: it records each
// send's outcome (success / auth / transient) so the breaker can open, and
// closes on a healthy probe. It does NOT gate sends itself — the pause-gate is
// the gate; this only observes. A permanent per-message 4xx (bad recipient) is
// recorded as a SUCCESS for breaker purposes: the provider answered fine, so
// one bad address must never trip the platform-wide breaker.
type BreakerTransport struct {
	inner email.Transport
	b     *Breaker
}

// NewBreakerTransport wraps inner so its outcomes drive b.
func NewBreakerTransport(inner email.Transport, b *Breaker) *BreakerTransport {
	return &BreakerTransport{inner: inner, b: b}
}

// Send delivers via the inner transport and records the outcome with the
// breaker.
func (t *BreakerTransport) Send(ctx context.Context, m email.Message) error {
	err := t.inner.Send(ctx, m)
	switch {
	case err == nil:
		t.b.RecordSuccess()
	case errors.Is(err, ErrMailgunAuth):
		t.b.RecordFailure(err)
	case isTransientError(err):
		t.b.RecordFailure(err)
	default:
		// Permanent per-message (e.g. bad recipient 4xx): provider is healthy.
		t.b.RecordSuccess()
	}
	return err
}

// isTransientError reports whether err (or anything it wraps) is a go-email
// TransientError.
func isTransientError(err error) bool {
	var te email.TransientError
	return errors.As(err, &te)
}

// interface guard.
var _ email.Transport = (*BreakerTransport)(nil)
