// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package emailservice provides the live Mailgun transport configuration to
// the obligations service sender. It is the single, internal-only path to the
// raw Mailgun sending api key: it reads the full config from core over the
// server-to-server EmailServiceSecretService.GetEmailServiceSecret RPC (never
// via the gateway/GraphQL).
//
// Caching: the non-secret fields (domain, region, from address, enabled) are
// cached in Redis with a short TTL. The api key never leaves this process: it
// is held in memory for the same TTL and refreshed from core when it expires
// or when the shared Redis entry is gone (so an Invalidate on any replica
// makes every replica re-read the key).
//
// Secret handling: the api key is treated as write-only. It is passed straight
// through to the transport constructor and is NEVER logged by this
// package, not on the success path, not on the error path.
package emailservice

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"google.golang.org/grpc"

	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	"github.com/Steward-GRC/steward-obligations/internal/cache"
)

// DefaultTTL is the approved cache lifetime for the Mailgun config. It bounds
// how stale a config change can be when the (audit-only) invalidation event is
// not wired to the jobs bus — see Provider.Invalidate for the follow-up. A
// non-positive ttl passed to NewProvider falls back to this value.
const DefaultTTL = 60 * time.Second

// cacheKey is the single Redis key holding the serialized Mailgun config. The
// config is a singleton (one row in core), so one entry serves every send.
const cacheKey = "mailgun:config"

// MailgunConfig is the resolved Mailgun configuration the transport-selection
// consumes to build a mail.MailgunTransport. It carries only the
// fields the transport needs; the enabled/usable decision is surfaced as the
// ok return of Get, not a field here.
//
// APIKey is a write-only secret: it is never logged and never serialized onto
// any gateway/GraphQL path.
type MailgunConfig struct {
	APIKey      string
	Domain      string
	Region      string // "us" | "eu"
	FromAddress string
}

// SecretClient is the narrow slice of corev1.EmailServiceSecretServiceClient this
// provider depends on. The generated client satisfies it directly; tests inject
// a fake. It is deliberately just the one internal read RPC.
type SecretClient interface {
	GetEmailServiceSecret(ctx context.Context, in *corev1.GetEmailServiceSecretRequest, opts ...grpc.CallOption) (*corev1.GetEmailServiceSecretResponse, error)
}

// Provider resolves the current Mailgun config: the non-secret fields
// cache-aside over Redis, the api key in process memory. A nil KV disables
// caching (every Get hits core), so the service runs with or without Redis,
// matching the obligation cache posture.
type Provider struct {
	client SecretClient
	kv     cache.KV
	ttl    time.Duration
	now    func() time.Time

	mu        sync.Mutex
	apiKey    string
	keyExpiry time.Time
}

// Option configures a Provider.
type Option func(*Provider)

// WithClock replaces the clock that times the in-memory api key.
func WithClock(now func() time.Time) Option {
	return func(p *Provider) { p.now = now }
}

// NewProvider builds a Provider. A non-positive ttl defaults to DefaultTTL
// (60s). kv may be nil to disable caching.
func NewProvider(client SecretClient, kv cache.KV, ttl time.Duration, opts ...Option) *Provider {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	p := &Provider{client: client, kv: kv, ttl: ttl, now: time.Now}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Get returns the current Mailgun config for the transport. ok reports whether
// Mailgun is actually usable (enabled AND carrying an api key and a sending
// domain); ok=false means "not configured / disabled", and falls back to
// SMTP. A Redis hit with an unexpired in-memory key is served without a gRPC
// call; otherwise Get reads core, repopulates the Redis entry and refreshes
// the in-memory key. Cache errors degrade to a direct core read; they never
// fail the call. The api key is never logged.
func (p *Provider) Get(ctx context.Context) (MailgunConfig, bool, error) {
	logger := logctx.From(ctx)
	if key, fresh := p.memKey(); fresh {
		if cc, hit := p.readCache(ctx); hit {
			logger.Trace().Msg("email service config: served from cache")
			return cc.toConfig(key), cc.usable(key), nil
		}
	}
	resp, err := p.client.GetEmailServiceSecret(ctx, &corev1.GetEmailServiceSecretRequest{})
	if err != nil {
		// No fields from resp are logged here, in particular never the key.
		logger.Error().Err(err).Msg("email service config: GetEmailServiceSecret failed")
		return MailgunConfig{}, false, err
	}
	cfg := resp.GetConfig()
	key := cfg.GetApiKey()
	cc := fromProto(cfg)
	p.setMemKey(key)
	p.writeCache(ctx, cc)
	logger.Debug().Bool("enabled", cc.Enabled).Msg("email service config: refreshed from core")
	return cc.toConfig(key), cc.usable(key), nil
}

func (p *Provider) memKey() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keyExpiry.IsZero() || !p.now().Before(p.keyExpiry) {
		return "", false
	}
	return p.apiKey, true
}

func (p *Provider) setMemKey(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.apiKey = key
	p.keyExpiry = p.now().Add(p.ttl)
}

// Invalidate drops the cached config and the in-memory key so the next Get
// re-reads them from core.
//
// It is exposed for near-instant propagation of admin config changes. Today
// core's SetMailgunConfig emits "mailgun_config.updated" only on the AUDIT tier
// (the audit topic exchange), NOT on the "jobs" exchange the obligations service
// consumes, so there is no bus event to hang a cache-bust consumer off — the
// 60s TTL is the freshness guarantee. FOLLOW-UP: when core also publishes
// "mailgun_config.updated" to the jobs exchange, add a small consumer here that
// calls Invalidate for sub-second propagation.
func (p *Provider) Invalidate(ctx context.Context) error {
	p.mu.Lock()
	p.apiKey, p.keyExpiry = "", time.Time{}
	p.mu.Unlock()
	if p.kv == nil {
		return nil
	}
	return p.kv.Del(ctx, cacheKey)
}

// readCache returns the cached config and whether it was a usable hit. A cache
// error, miss, or corrupt entry is reported as a miss so Get falls through to
// core.
func (p *Provider) readCache(ctx context.Context) (cachedConfig, bool) {
	if p.kv == nil {
		return cachedConfig{}, false
	}
	b, ok, err := p.kv.GetBytes(ctx, cacheKey)
	if err != nil || !ok {
		return cachedConfig{}, false
	}
	var cc cachedConfig
	if json.Unmarshal(b, &cc) != nil {
		return cachedConfig{}, false
	}
	return cc, true
}

// writeCache stores the config for the configured TTL. A marshal or Redis error
// is non-fatal: the next Get simply re-reads core.
func (p *Provider) writeCache(ctx context.Context, cc cachedConfig) {
	if p.kv == nil {
		return
	}
	b, err := json.Marshal(cc)
	if err != nil {
		return
	}
	_ = p.kv.SetBytes(ctx, cacheKey, b, p.ttl)
}

// cachedConfig is the JSON shape persisted in Redis. It carries only the
// non-secret fields; the api key stays in process memory (see Provider).
type cachedConfig struct {
	Domain      string `json:"domain"`
	Region      string `json:"region"`
	FromAddress string `json:"from_address"`
	Enabled     bool   `json:"enabled"`
}

// fromProto maps the internal RPC response into the cached shape. A nil config
// (defensive) yields the zero value, which is not usable.
func fromProto(c *corev1.EmailServiceConfig) cachedConfig {
	if c == nil {
		return cachedConfig{}
	}
	return cachedConfig{
		Domain:      c.GetDomain(),
		Region:      c.GetRegion(),
		FromAddress: c.GetFromAddress(),
		Enabled:     c.GetEnabled(),
	}
}

func (c cachedConfig) toConfig(apiKey string) MailgunConfig {
	return MailgunConfig{
		APIKey:      apiKey,
		Domain:      c.Domain,
		Region:      c.Region,
		FromAddress: c.FromAddress,
	}
}

// usable reports whether the config can actually send: it must be enabled and
// carry both an api key and a sending domain. Anything else is "not configured"
// and drives the SMTP fallback (Get returns ok=false).
func (c cachedConfig) usable(apiKey string) bool {
	return c.Enabled && apiKey != "" && c.Domain != ""
}
