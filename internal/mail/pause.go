// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	email "github.com/Bugs5382/go-email"
)

// ErrMailPaused signals that a send could not be delivered because there is no
// working transport right now (Mailgun selected but unhealthy/over-limit, or
// unconfigured with no SMTP fallback), so the message has been HELD in the
// durable outbox for replay on recovery — not dropped.
//
// It is deliberately a new sentinel with a disposition of its own, distinct
// from the two go-email outcomes it sits between:
// - NOT email.ErrSuppressed: Suppress means "deliberately drop this recipient"
// (opt-out / quiet-hours). ErrMailPaused means the opposite — "hold and
// retry later, lose nothing."
// - NOT an email.TransientError: Retry only backs off and re-runs a
// TransientError. A multi-minute Mailgun outage would make Retry hot-spin;
// ErrMailPaused breaks out of the chain immediately so the caller can ACK
// the AMQP message (the outbox, not RabbitMQ requeue, is the hold).
//
// Callers test for it with errors.Is; IsPaused is the convenience helper.
var ErrMailPaused = errors.New("mail: sending paused — held in outbox for retry")

// IsPaused reports whether err (or anything it wraps) is ErrMailPaused. Consumers
// use it to ACK-and-hold a paused send instead of requeuing it.
func IsPaused(err error) bool { return errors.Is(err, ErrMailPaused) }

// PauseDecider answers the one question the pause-gate asks per send: is there
// no working transport right now? True → the send is held in the outbox and
// ErrMailPaused is returned; false → the send proceeds down the chain.
type PauseDecider interface {
	ShouldPause(ctx context.Context) bool
}

// PauseGate returns the OUTERMOST middleware (installed before devCatchAll):
// before any other hook runs it asks decider whether a working transport
// exists. When it does not, the fully-rendered message is persisted to outbox
// (if non-nil) and ErrMailPaused is returned; the chain's later hooks (dedupe,
// suppress, record, retry, transport) never run, so a paused send is neither
// audited as delivered nor marked seen by Dedupe, and a later replay still goes
// out.
//
// A drainer replay (Meta["outbox_replay"]) that re-hits a paused gate returns
// ErrMailPaused WITHOUT re-persisting: the row is already held, so a breaker
// that reopens mid-drain never duplicates the hold.
//
// A persist failure returns a plain (non-paused) error rather than ErrMailPaused
// so the caller does NOT ack a message that was never actually held — it stays
// on the queue (or surfaces) instead of being silently lost.
func PauseGate(decider PauseDecider, outbox OutboxWriter) email.Middleware {
	return func(next email.SendFunc) email.SendFunc {
		return func(ctx context.Context, m *email.Message) error {
			if decider == nil || !decider.ShouldPause(ctx) {
				return next(ctx, m)
			}

			logger := logctx.From(ctx)
			kind, _ := m.Meta["kind"].(string)

			if isOutboxReplay(m) {
				// Already a held row; breaker reopened mid-drain. Signal hold
				// without persisting a duplicate.
				logger.Debug().Str("kind", kind).Msg("mail: pause-gate held drain replay (breaker still open)")
				return ErrMailPaused
			}

			if outbox != nil {
				if err := outbox.Enqueue(ctx, outboxItemFromMessage(m)); err != nil {
					logger.Error().Err(err).Str("kind", kind).Msg("mail: pause-gate could not persist to outbox — send not held")
					return fmt.Errorf("mail: pause-gate persist to outbox: %w", err)
				}
			}
			logger.Warn().Str("kind", kind).Msg("mail: sending paused — message held in outbox for retry")
			return ErrMailPaused
		}
	}
}
