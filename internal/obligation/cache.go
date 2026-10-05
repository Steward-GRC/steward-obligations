// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/cache"
)

// obligatingKey is the single Redis key holding the global obligating-policy set.
// It is the same for every user (the set does not depend on the caller), so one
// cached entry serves every myObligations / myAckSummary request.
const obligatingKey = "policy:obligating"

// CachedCore wraps a coreObligations with cache-aside for ListObligatingPolicies
// — the global set queried on every obligation/summary call and the single
// biggest read hotspot found in the stress test. Every other method passes
// through. A nil KV disables caching (direct passthrough), so the service runs
// with or without Redis.
type CachedCore struct {
	inner coreObligations
	kv    cache.KV
	ttl   time.Duration
}

// NewCachedCore wraps inner. ttl bounds staleness; event-driven invalidation
// (InvalidateObligating, called by the publish / obligation-change consumers)
// keeps it fresh in normal operation.
func NewCachedCore(inner coreObligations, kv cache.KV, ttl time.Duration) *CachedCore {
	return &CachedCore{inner: inner, kv: kv, ttl: ttl}
}

// ListObligatingPolicies serves from Redis on a hit, else queries core and
// populates the cache. Cache errors never fail the call — they fall through to
// core so a Redis outage degrades to (slower) correctness, never an error.
func (c *CachedCore) ListObligatingPolicies(ctx context.Context) ([]ObligatingPolicy, error) {
	if c.kv == nil {
		return c.inner.ListObligatingPolicies(ctx)
	}
	if b, ok, err := c.kv.GetBytes(ctx, obligatingKey); err == nil && ok {
		var out []ObligatingPolicy
		if json.Unmarshal(b, &out) == nil {
			return out, nil
		}
	}
	out, err := c.inner.ListObligatingPolicies(ctx)
	if err != nil {
		return nil, err
	}
	if b, mErr := json.Marshal(out); mErr == nil {
		_ = c.kv.SetBytes(ctx, obligatingKey, b, c.ttl)
	}
	return out, nil
}

// InvalidateObligating drops the cached set so the next read repopulates it.
// Called when a publish or governance/audience change alters what obligates ack.
func (c *CachedCore) InvalidateObligating(ctx context.Context) error {
	if c.kv == nil {
		return nil
	}
	return c.kv.Del(ctx, obligatingKey)
}

// --- pass-throughs (unchanged behavior) -----------------------------------

func (c *CachedCore) ResolvePolicyObligation(ctx context.Context, policyID string) (PolicyObligation, error) {
	return c.inner.ResolvePolicyObligation(ctx, policyID)
}

func (c *CachedCore) PolicyIDForVersion(ctx context.Context, policyVersionID string) (string, error) {
	return c.inner.PolicyIDForVersion(ctx, policyVersionID)
}

func (c *CachedCore) VersionPublishedAt(ctx context.Context, policyVersionID string) (time.Time, error) {
	return c.inner.VersionPublishedAt(ctx, policyVersionID)
}

func (c *CachedCore) ListPolicyVersionIDs(ctx context.Context, policyID string) ([]string, error) {
	return c.inner.ListPolicyVersionIDs(ctx, policyID)
}

func (c *CachedCore) PolicyHomeGroup(ctx context.Context, policyID string) (string, error) {
	return c.inner.PolicyHomeGroup(ctx, policyID)
}

func (c *CachedCore) PolicyNumber(ctx context.Context, policyID string) (string, error) {
	return c.inner.PolicyNumber(ctx, policyID)
}

func (c *CachedCore) PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error) {
	return c.inner.PolicyDisplay(ctx, policyID)
}

func (c *CachedCore) PolicySensitivity(ctx context.Context, policyID string) (bool, error) {
	return c.inner.PolicySensitivity(ctx, policyID)
}

func (c *CachedCore) PolicyDocumentType(ctx context.Context, policyID string) (string, error) {
	return c.inner.PolicyDocumentType(ctx, policyID)
}

// Compile-time check that the cache decorator still satisfies the core interface.
var _ coreObligations = (*CachedCore)(nil)
