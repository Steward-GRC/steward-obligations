// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	email "github.com/Bugs5382/go-email"
	log "github.com/Bugs5382/go-log"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// breakerClock is a settable clock for deterministic breaker tests.
type breakerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *breakerClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *breakerClock) fn() func() time.Time {
	return func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
}

func newTestBreaker(t *testing.T, opts ...mail.BreakerOption) *mail.Breaker {
	t.Helper()
	return mail.NewBreaker(log.New("breaker-test"), opts...)
}

func TestBreaker_OpensAtThreshold(t *testing.T) {
	b := newTestBreaker(t, mail.WithBreakerThreshold(3))
	require.Equal(t, mail.BreakerClosed, b.State())
	require.True(t, b.Allow())

	b.RecordFailure(fmt.Errorf("boom"))
	b.RecordFailure(fmt.Errorf("boom"))
	require.Equal(t, mail.BreakerClosed, b.State(), "below threshold stays closed")

	b.RecordFailure(fmt.Errorf("boom"))
	require.Equal(t, mail.BreakerOpen, b.State(), "third consecutive failure opens")
	require.False(t, b.Allow(), "an open breaker holds sends")
}

func TestBreaker_AuthFailureOpensImmediately(t *testing.T) {
	b := newTestBreaker(t, mail.WithBreakerThreshold(100))
	b.RecordFailure(fmt.Errorf("wrap: %w", mail.ErrMailgunAuth))
	require.Equal(t, mail.BreakerOpen, b.State(), "an auth failure opens immediately, ignoring the threshold")
}

func TestBreaker_SuccessResetsFailureRun(t *testing.T) {
	b := newTestBreaker(t, mail.WithBreakerThreshold(3))
	b.RecordFailure(fmt.Errorf("boom"))
	b.RecordFailure(fmt.Errorf("boom"))
	b.RecordSuccess() // resets the run
	b.RecordFailure(fmt.Errorf("boom"))
	b.RecordFailure(fmt.Errorf("boom"))
	require.Equal(t, mail.BreakerClosed, b.State(), "an interleaved success prevents the threshold from tripping")
}

func TestBreaker_HalfOpenProbeClosesAndTriggersDrain(t *testing.T) {
	clk := &breakerClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	var drained atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	once := sync.Once{}
	b := newTestBreaker(t,
		mail.WithBreakerThreshold(1),
		mail.WithBreakerCooldown(30*time.Second),
		mail.WithBreakerClock(clk.fn()),
		mail.WithBreakerOnClose(func() {
			drained.Add(1)
			once.Do(wg.Done)
		}),
	)

	b.RecordFailure(fmt.Errorf("boom"))
	require.Equal(t, mail.BreakerOpen, b.State())
	require.False(t, b.Allow(), "still within cooldown → held")

	clk.advance(31 * time.Second)
	require.True(t, b.Allow(), "after cooldown a half-open probe is allowed")
	require.Equal(t, mail.BreakerHalfOpen, b.State(), "cooldown elapsed → half-open")

	b.RecordSuccess() // probe succeeded
	require.Equal(t, mail.BreakerClosed, b.State(), "a successful probe closes the breaker")

	wg.Wait() // onClose (drain trigger) fired
	require.Equal(t, int32(1), drained.Load(), "closing must trigger exactly one drain")
}

func TestBreaker_HalfOpenProbeFailureReopens(t *testing.T) {
	clk := &breakerClock{now: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	b := newTestBreaker(t,
		mail.WithBreakerThreshold(1),
		mail.WithBreakerCooldown(30*time.Second),
		mail.WithBreakerClock(clk.fn()),
	)
	b.RecordFailure(fmt.Errorf("boom"))
	clk.advance(31 * time.Second)
	require.True(t, b.Allow(), "half-open probe allowed")

	b.RecordFailure(fmt.Errorf("still down"))
	require.Equal(t, mail.BreakerOpen, b.State(), "a failed probe re-opens")
	require.False(t, b.Allow(), "cooldown restarts after a failed probe")
}

// BreakerTransport observation tests.

func TestBreakerTransport_TransientFailuresDriveBreaker(t *testing.T) {
	b := newTestBreaker(t, mail.WithBreakerThreshold(2))
	inner := &countingTransport{err: email.TransientError{Err: fmt.Errorf("503")}}
	bt := mail.NewBreakerTransport(inner, b)

	_ = bt.Send(context.Background(), testMessage())
	_ = bt.Send(context.Background(), testMessage())
	require.Equal(t, mail.BreakerOpen, b.State(), "two transient failures open the breaker (threshold 2)")
	require.Equal(t, 2, inner.sends)
}

func TestBreakerTransport_PerMessage4xxKeepsBreakerClosed(t *testing.T) {
	b := newTestBreaker(t, mail.WithBreakerThreshold(1))
	// A permanent per-message error (bad recipient): NOT auth, NOT transient.
	inner := &countingTransport{err: fmt.Errorf("mailgun: status 422: permanent")}
	bt := mail.NewBreakerTransport(inner, b)

	_ = bt.Send(context.Background(), testMessage())
	_ = bt.Send(context.Background(), testMessage())
	require.Equal(t, mail.BreakerClosed, b.State(), "a bad address must never trip the platform-wide breaker")
}

func TestBreakerTransport_AuthFailureOpensBreaker(t *testing.T) {
	b := newTestBreaker(t, mail.WithBreakerThreshold(100))
	inner := &countingTransport{err: fmt.Errorf("mailgun: status 401: %w", mail.ErrMailgunAuth)}
	bt := mail.NewBreakerTransport(inner, b)
	_ = bt.Send(context.Background(), testMessage())
	require.Equal(t, mail.BreakerOpen, b.State())
}
