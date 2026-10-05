// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	email "github.com/Bugs5382/go-email"

	"github.com/Steward-GRC/steward-obligations/internal/emailservice"
)

// MailgunConfigProvider resolves the current Mailgun config for a send. It is
// the seam onto emailservice.Provider (whose *Provider satisfies it directly);
// tests inject a fake. ok reports whether Mailgun is usable right now — ok=false
// means "not configured / disabled", and the RoutingTransport falls back to
// SMTP.
type MailgunConfigProvider interface {
	Get(ctx context.Context) (emailservice.MailgunConfig, bool, error)
}

// mailgunFactory builds an email.Transport for a resolved Mailgun config. The
// default constructs a real MailgunTransport; tests override it via
// WithMailgunFactory to inject a fake and count rebuilds.
type mailgunFactory func(emailservice.MailgunConfig) email.Transport

// RoutingTransport is a go-email email.Transport that chooses, per send,
// between Mailgun and SMTP based on the live admin config. On each Send it asks
// the provider for the current config: if Mailgun is usable it routes to a
// MailgunTransport, otherwise (not configured/disabled, or a provider error) it
// routes to the SMTP fallback.
//
// Selection is dynamic (per-send), not boot-time: an admin enabling or
// disabling Mailgun through the settings UI takes effect on the next send once
// the provider's config cache refreshes (the ~60s TTL window), with no cn
// restart. The Mailgun transport itself is cached and only rebuilt when the
// resolved config changes — detected by a fingerprint over
// APIKey/Domain/Region/FromAddress — so a stable config does not construct a
// fresh HTTP transport on every message.
//
// A provider error degrades to SMTP rather than hard-failing the send (the
// pause-on-failure gate is Slice B); the error is logged via go-log and the api
// key is never logged.
type RoutingTransport struct {
	provider   MailgunConfigProvider
	smtp       email.Transport
	newMailgun mailgunFactory

	mu     sync.Mutex
	cached email.Transport // last-built Mailgun transport
	fp     string          // fingerprint of the config `cached` was built from
}

// RoutingOption customizes a RoutingTransport at construction.
type RoutingOption func(*RoutingTransport)

// WithMailgunFactory overrides how a MailgunTransport is built from a resolved
// config. Primarily a test seam; production uses the default factory
// (NewMailgunTransport).
func WithMailgunFactory(f mailgunFactory) RoutingOption {
	return func(rt *RoutingTransport) {
		if f != nil {
			rt.newMailgun = f
		}
	}
}

// NewRoutingTransport builds a RoutingTransport that routes between Mailgun
// (resolved live from provider) and the supplied smtp fallback transport.
func NewRoutingTransport(provider MailgunConfigProvider, smtp email.Transport, opts ...RoutingOption) *RoutingTransport {
	rt := &RoutingTransport{
		provider: provider,
		smtp:     smtp,
		newMailgun: func(c emailservice.MailgunConfig) email.Transport {
			return NewMailgunTransport(MailgunConfig{
				APIKey:      c.APIKey,
				Domain:      c.Domain,
				Region:      c.Region,
				FromAddress: c.FromAddress,
			})
		},
	}
	for _, o := range opts {
		o(rt)
	}
	return rt
}

// Send implements email.Transport. It resolves the current config and routes
// the message to Mailgun or SMTP accordingly.
func (rt *RoutingTransport) Send(ctx context.Context, m email.Message) error {
	logger := logctx.From(ctx)

	cfg, ok, err := rt.provider.Get(ctx)
	if err != nil {
		// Degrade to SMTP so a transient core/cache hiccup does not drop mail.
		// The real pause-on-failure gate is Slice B. Never log the api key.
		logger.Warn().Err(err).Msg("mailgun config resolve failed — falling back to SMTP")
		return rt.smtp.Send(ctx, m)
	}
	if !ok {
		// Not configured / disabled: SMTP is the path (and the only path in
		// dev/local where Mailgun is unset → maildev still works).
		return rt.smtp.Send(ctx, m)
	}

	return rt.mailgunFor(cfg).Send(ctx, m)
}

// mailgunFor returns the cached MailgunTransport for cfg, rebuilding it only
// when the config's fingerprint has changed since the last build.
func (rt *RoutingTransport) mailgunFor(cfg emailservice.MailgunConfig) email.Transport {
	fp := fingerprint(cfg)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cached == nil || rt.fp != fp {
		rt.cached = rt.newMailgun(cfg)
		rt.fp = fp
	}
	return rt.cached
}

// fingerprint hashes the sending-relevant config fields so a change to any of
// them (including the api key, e.g. a rotation) forces a transport rebuild. It
// hashes rather than concatenates so the raw api key is never held in the
// comparison field (belt-and-suspenders — the fingerprint is never logged
// either).
func fingerprint(c emailservice.MailgunConfig) string {
	h := sha256.New()
	for _, s := range []string{c.APIKey, c.Domain, c.Region, c.FromAddress} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0}) // domain-separate fields so "ab"+"c" != "a"+"bc"
	}
	return hex.EncodeToString(h.Sum(nil))
}

// interface guard: RoutingTransport must satisfy go-email's Transport.
var _ email.Transport = (*RoutingTransport)(nil)
