// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// TransportPauseDecider is the production PauseDecider: it fuses the live
// Mailgun config, the circuit-breaker, and the usage guard into the single
// "is there no working transport right now?" answer the pause-gate needs. It is
// consistent with RoutingTransport's own per-send routing so the gate and the
// router never disagree about which leg a send takes:
//
// - Mailgun NOT configured/disabled (or a provider error): SMTP is the path.
// Pause only if SMTP is not even available (SMTPAvailable=false) — otherwise
// never pause (this is the dev/local case: unset Mailgun → maildev via SMTP).
// - Mailgun configured+enabled: the send goes to Mailgun, so pause when the
// breaker will not allow a send (open, outside a probe window) OR the usage
// guard's hard ceiling is exceeded.
type TransportPauseDecider struct {
	provider      MailgunConfigProvider
	breaker       *Breaker
	usage         *UsageGuard
	smtpAvailable bool
}

// NewPauseDecider builds a TransportPauseDecider. breaker and usage may be nil
// (then they simply never force a pause). smtpAvailable reflects whether an SMTP
// fallback transport is wired and usable (true in this service whenever an SMTP
// host is configured — always the case in dev/local).
func NewPauseDecider(provider MailgunConfigProvider, breaker *Breaker, usage *UsageGuard, smtpAvailable bool) *TransportPauseDecider {
	return &TransportPauseDecider{
		provider:      provider,
		breaker:       breaker,
		usage:         usage,
		smtpAvailable: smtpAvailable,
	}
}

// ShouldPause implements PauseDecider. It resolves the live config (served from
// the provider's short cache, so this adds no per-send gRPC round-trip on the
// hot path) and applies the routing-consistent rules above. It never logs the
// api key.
func (d *TransportPauseDecider) ShouldPause(ctx context.Context) bool {
	_, ok, err := d.provider.Get(ctx)
	if err != nil {
		// Provider hiccup: RoutingTransport degrades to SMTP, so mirror it —
		// pause only when there is no SMTP fallback to degrade to.
		if !d.smtpAvailable {
			logger := logctx.From(ctx)
			logger.Warn().Err(err).Msg("mail: pause decider — config resolve failed and no SMTP fallback; holding")
		}
		return !d.smtpAvailable
	}
	if !ok {
		// Not configured / disabled → SMTP path. Pause only with no SMTP.
		return !d.smtpAvailable
	}

	// Mailgun is the selected transport.
	if d.breaker != nil && !d.breaker.Allow() {
		return true
	}
	if d.usage != nil && d.usage.OverCeiling() {
		return true
	}
	return false
}

// interface guard.
var _ PauseDecider = (*TransportPauseDecider)(nil)
