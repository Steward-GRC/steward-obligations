// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package cache provides a minimal byte cache used by cache-aside decorators
// (e.g. the obligating-policy set). Redis is optional: when no URL is
// configured, callers pass a nil KV and degrade to direct passthrough.
package cache

import (
	"context"
	"errors"
	"sync"
	"time"

	redis "github.com/Bugs5382/go-redis"
)

// KV is the minimal byte cache the decorators depend on.
type KV interface {
	GetBytes(ctx context.Context, key string) ([]byte, bool, error)
	SetBytes(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
}

// breaker is a tiny circuit breaker: after breakerThreshold consecutive Redis
// errors it "opens" for breakerCooldown, during which ops skip Redis entirely
// (instant fall-through to the DB) instead of each paying the dial/read timeout.
// A single success closes it. This keeps a Redis outage from adding per-request
// latency under load.
const (
	breakerThreshold = 3
	breakerCooldown  = 3 * time.Second
)

type breaker struct {
	mu        sync.Mutex
	fails     int
	openUntil time.Time
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().After(b.openUntil)
}

func (b *breaker) record(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.fails = 0
		b.openUntil = time.Time{}
		return
	}
	b.fails++
	if b.fails >= breakerThreshold {
		b.openUntil = time.Now().Add(breakerCooldown)
	}
}

// Redis is a go-redis-backed KV with a circuit breaker.
type Redis struct {
	c  redis.UniversalClient
	br breaker
}

// New returns the cache on c. The client's own timeouts should be short: a
// slow cache must never be slower than the read it saves.
func New(c *redis.Client) *Redis { return &Redis{c: c.Redis()} }

// Ping checks the cache is reachable.
func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

// GetBytes returns (value, found, error); a cache miss is (nil, false, nil).
// When the breaker is open it returns a miss immediately (no Redis call).
func (r *Redis) GetBytes(ctx context.Context, key string) ([]byte, bool, error) {
	if !r.br.allow() {
		return nil, false, nil
	}
	b, err := r.c.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		r.br.record(nil)
		return nil, false, nil
	}
	if err != nil {
		r.br.record(err)
		return nil, false, err
	}
	r.br.record(nil)
	return b, true, nil
}

// SetBytes stores value with a TTL (skipped when the breaker is open).
func (r *Redis) SetBytes(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if !r.br.allow() {
		return nil
	}
	err := r.c.Set(ctx, key, val, ttl).Err()
	r.br.record(err)
	return err
}

// Del removes a key (skipped when the breaker is open; no error if absent).
func (r *Redis) Del(ctx context.Context, key string) error {
	if !r.br.allow() {
		return nil
	}
	err := r.c.Del(ctx, key).Err()
	r.br.record(err)
	return err
}
