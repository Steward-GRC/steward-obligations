// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Usage is a point-in-time snapshot of Mailgun consumption against the plan.
// Limit <= 0 means "unknown/unbounded" (the guard then only reports Sent and
// never hard-pauses, since it cannot know the ceiling).
type Usage struct {
	Sent  int64
	Limit int64
}

// UsageReader reads current Mailgun usage. The default is HTTPUsageReader
// (polls Mailgun's HTTP API); tests inject a fake. It never logs the api key.
type UsageReader interface {
	Read(ctx context.Context) (Usage, error)
}

// Default usage-guard tuning.
const (
	// DefaultUsageSoftThreshold warns when usage reaches 80% of the plan limit.
	DefaultUsageSoftThreshold = 0.80
	// DefaultUsagePollInterval is how often the guard polls Mailgun usage.
	DefaultUsagePollInterval = 5 * time.Minute
)

// UsageGuard polls Mailgun usage and enforces the approved policy: SOFT-ALERT
// as usage approaches the plan limit (a warn log + an OTEL gauge), and a
// CONFIGURABLE HARD CEILING that, once exceeded, makes the pause-gate route new
// sends to the outbox (OverCeiling) instead of overrunning the plan. A usage
// read error fails OPEN (does not pause) — a metrics hiccup must not stop mail.
type UsageGuard struct {
	reader   UsageReader
	soft     float64
	ceiling  int64 // hard ceiling in messages; <=0 disables the hard-pause
	interval time.Duration
	logger   zerolog.Logger

	overCeiling atomic.Bool
	lastSent    atomic.Int64
	lastLimit   atomic.Int64

	mu          sync.Mutex
	wasOverSoft bool // for edge-triggered soft alerts

	sentGauge  metric.Int64Gauge
	limitGauge metric.Int64Gauge
}

// UsageGuardOption customizes a UsageGuard.
type UsageGuardOption func(*UsageGuard)

// WithUsageSoftThreshold sets the fraction (0..1) of the plan limit at which a
// soft alert fires (out-of-range values fall back to the default).
func WithUsageSoftThreshold(f float64) UsageGuardOption {
	return func(g *UsageGuard) {
		if f > 0 && f < 1 {
			g.soft = f
		}
	}
}

// WithUsageHardCeiling sets the absolute message ceiling that pauses new sends
// once exceeded. Non-positive (the default) disables the hard-pause, leaving
// only the soft alert.
func WithUsageHardCeiling(n int64) UsageGuardOption {
	return func(g *UsageGuard) { g.ceiling = n }
}

// WithUsagePollInterval overrides the poll cadence (non-positive keeps the
// default).
func WithUsagePollInterval(d time.Duration) UsageGuardOption {
	return func(g *UsageGuard) {
		if d > 0 {
			g.interval = d
		}
	}
}

// NewUsageGuard builds a UsageGuard reading through reader and emitting metrics
// on meter (meter may be nil to skip metrics). logger is a go-log logger.
func NewUsageGuard(reader UsageReader, meter metric.Meter, logger zerolog.Logger, opts ...UsageGuardOption) *UsageGuard {
	g := &UsageGuard{
		reader:   reader,
		soft:     DefaultUsageSoftThreshold,
		interval: DefaultUsagePollInterval,
		logger:   logger,
	}
	for _, o := range opts {
		o(g)
	}
	if meter != nil {
		// Errors here are non-fatal: the guard still enforces the ceiling, it
		// just does not export the gauges.
		g.sentGauge, _ = meter.Int64Gauge("mailgun.usage.sent",
			metric.WithDescription("Mailgun messages sent in the current plan window"))
		g.limitGauge, _ = meter.Int64Gauge("mailgun.usage.limit",
			metric.WithDescription("Mailgun plan send limit for the current window"))
	}
	return g
}

// OverCeiling reports whether the last poll found usage at/above the configured
// hard ceiling. The pause-gate consults it to route new sends to the outbox.
func (g *UsageGuard) OverCeiling() bool { return g.overCeiling.Load() }

// LastUsage returns the most recent observed usage (for tests/diagnostics).
func (g *UsageGuard) LastUsage() Usage {
	return Usage{Sent: g.lastSent.Load(), Limit: g.lastLimit.Load()}
}

// Run polls usage every interval until ctx is cancelled, refreshing the
// OverCeiling flag, the OTEL gauges, and the soft alert. It performs one
// immediate refresh at start so the guard is populated before the first tick.
func (g *UsageGuard) Run(ctx context.Context) {
	g.Refresh(ctx)
	t := time.NewTicker(g.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Refresh(ctx)
		}
	}
}

// Refresh performs a single usage read and updates state, metrics, and alerts.
// A read error fails open (leaves OverCeiling unchanged) and is logged at debug
// — never with the api key.
func (g *UsageGuard) Refresh(ctx context.Context) {
	u, err := g.reader.Read(ctx)
	if err != nil {
		g.logger.Debug().Err(err).Msg("mailgun usage read failed — leaving limit guard unchanged (fail-open)")
		return
	}

	g.lastSent.Store(u.Sent)
	g.lastLimit.Store(u.Limit)
	if g.sentGauge != nil {
		g.sentGauge.Record(ctx, u.Sent, metric.WithAttributes(attribute.String("provider", "mailgun")))
	}
	if g.limitGauge != nil {
		g.limitGauge.Record(ctx, u.Limit, metric.WithAttributes(attribute.String("provider", "mailgun")))
	}

	g.evaluateSoft(u)
	g.evaluateCeiling(u)
}

// evaluateSoft fires an edge-triggered warn when usage first crosses the soft
// threshold (and an info when it recedes), so the log is not spammed every poll.
func (g *UsageGuard) evaluateSoft(u Usage) {
	if u.Limit <= 0 {
		return
	}
	ratio := float64(u.Sent) / float64(u.Limit)
	over := ratio >= g.soft

	g.mu.Lock()
	changed := over != g.wasOverSoft
	g.wasOverSoft = over
	g.mu.Unlock()

	if !changed {
		return
	}
	if over {
		g.logger.Warn().
			Int64("sent", u.Sent).
			Int64("limit", u.Limit).
			Float64("ratio", ratio).
			Float64("soft_threshold", g.soft).
			Msg("mailgun usage approaching plan limit")
	} else {
		g.logger.Info().
			Int64("sent", u.Sent).
			Int64("limit", u.Limit).
			Msg("mailgun usage back below soft threshold")
	}
}

// evaluateCeiling flips the OverCeiling flag and logs the transition. A
// non-positive ceiling disables the hard-pause entirely.
func (g *UsageGuard) evaluateCeiling(u Usage) {
	if g.ceiling <= 0 {
		g.overCeiling.Store(false)
		return
	}
	over := u.Sent >= g.ceiling
	if g.overCeiling.Swap(over) == over {
		return // no change
	}
	if over {
		g.logger.Warn().
			Int64("sent", u.Sent).
			Int64("ceiling", g.ceiling).
			Msg("mailgun hard ceiling exceeded — pausing new sends to outbox")
	} else {
		g.logger.Info().
			Int64("sent", u.Sent).
			Int64("ceiling", g.ceiling).
			Msg("mailgun usage back below hard ceiling — resuming sends")
	}
}

// HTTPUsageReader reads Mailgun usage from its HTTP API. It is best-effort and
// intentionally tolerant of shape differences across Mailgun plans/regions: it
// parses the fields it recognizes and returns Limit<=0 (unknown) when it cannot
// determine a plan limit, which keeps the guard in alert-only mode rather than
// hard-pausing on a parse it does not understand.
//
// The api key is used only as the HTTP Basic password and is NEVER logged.
type HTTPUsageReader struct {
	client  *http.Client
	baseURL string
	domain  string
	apiKey  string
}

// NewHTTPUsageReader builds an HTTPUsageReader for the given Mailgun coordinates.
func NewHTTPUsageReader(apiKey, domain, region string) *HTTPUsageReader {
	return &HTTPUsageReader{
		client:  &http.Client{Timeout: mailgunTimeout},
		baseURL: baseURLForRegion(region),
		domain:  domain,
		apiKey:  apiKey,
	}
}

// Read fetches the current usage snapshot. It queries Mailgun's per-domain
// limit endpoint; a non-2xx or unparseable response yields an error (the guard
// fails open on it).
func (r *HTTPUsageReader) Read(ctx context.Context) (Usage, error) {
	endpoint := fmt.Sprintf("%s/v3/%s/limits/tag", r.baseURL, r.domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Usage{}, fmt.Errorf("mailgun usage: build request: %w", err)
	}
	req.SetBasicAuth("api", r.apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("mailgun usage: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Usage{}, fmt.Errorf("mailgun usage: status %d", resp.StatusCode)
	}

	// Mailgun returns a small JSON object; parse the count/limit fields we know.
	var parsed struct {
		Count int64 `json:"count"`
		Limit int64 `json:"limit"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&parsed); err != nil {
		return Usage{}, fmt.Errorf("mailgun usage: decode: %w", err)
	}
	return Usage{Sent: parsed.Count, Limit: parsed.Limit}, nil
}

// interface guard.
var _ UsageReader = (*HTTPUsageReader)(nil)
