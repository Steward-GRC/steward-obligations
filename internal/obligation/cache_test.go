// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation_test

import (
	"context"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// countingCore satisfies the (unexported) coreObligations interface and counts
// ListObligatingPolicies calls so tests can assert cache hits/misses.
type countingCore struct{ listCalls int }

func (c *countingCore) ListObligatingPolicies(context.Context) ([]obligation.ObligatingPolicy, error) {
	c.listCalls++
	return []obligation.ObligatingPolicy{{PolicyID: "p1", Number: "POL-1"}}, nil
}
func (c *countingCore) ResolvePolicyObligation(context.Context, string) (obligation.PolicyObligation, error) {
	return obligation.PolicyObligation{}, nil
}
func (c *countingCore) PolicyIDForVersion(context.Context, string) (string, error) { return "", nil }
func (c *countingCore) VersionPublishedAt(context.Context, string) (time.Time, error) {
	return time.Time{}, nil
}
func (c *countingCore) ListPolicyVersionIDs(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c *countingCore) PolicyHomeGroup(context.Context, string) (string, error) { return "", nil }
func (c *countingCore) PolicyNumber(context.Context, string) (string, error)    { return "", nil }
func (c *countingCore) PolicyDisplay(context.Context, string) (string, string, error) {
	return "", "", nil
}
func (c *countingCore) PolicySensitivity(context.Context, string) (bool, error) { return false, nil }
func (c *countingCore) PolicyDocumentType(context.Context, string) (string, error) {
	return "policy", nil
}

// memKV is an in-memory cache.KV.
type memKV struct{ m map[string][]byte }

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }
func (k *memKV) GetBytes(_ context.Context, key string) ([]byte, bool, error) {
	b, ok := k.m[key]
	return b, ok, nil
}
func (k *memKV) SetBytes(_ context.Context, key string, val []byte, _ time.Duration) error {
	k.m[key] = val
	return nil
}
func (k *memKV) Del(_ context.Context, key string) error { delete(k.m, key); return nil }

func TestCachedCore_MissThenHit(t *testing.T) {
	inner := &countingCore{}
	c := obligation.NewCachedCore(inner, newMemKV(), time.Minute)
	ctx := context.Background()

	if _, err := c.ListObligatingPolicies(ctx); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := c.ListObligatingPolicies(ctx); err != nil {
		t.Fatalf("second: %v", err)
	}
	if inner.listCalls != 1 {
		t.Fatalf("want inner called once (2nd served from cache), got %d", inner.listCalls)
	}
}

func TestCachedCore_InvalidateForcesReload(t *testing.T) {
	inner := &countingCore{}
	c := obligation.NewCachedCore(inner, newMemKV(), time.Minute)
	ctx := context.Background()

	_, _ = c.ListObligatingPolicies(ctx) // populate (call 1)
	if err := c.InvalidateObligating(ctx); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	_, _ = c.ListObligatingPolicies(ctx) // must hit inner again (call 2)
	if inner.listCalls != 2 {
		t.Fatalf("want inner called twice after invalidate, got %d", inner.listCalls)
	}
}

func TestCachedCore_NilKVPassthrough(t *testing.T) {
	inner := &countingCore{}
	c := obligation.NewCachedCore(inner, nil, time.Minute)
	ctx := context.Background()

	_, _ = c.ListObligatingPolicies(ctx)
	_, _ = c.ListObligatingPolicies(ctx)
	if inner.listCalls != 2 {
		t.Fatalf("want passthrough (inner each call) with nil KV, got %d", inner.listCalls)
	}
}
