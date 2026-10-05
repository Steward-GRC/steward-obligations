// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderLockKey is the fixed Postgres advisory-lock key the scheduler contends
// on so at most one replica runs the sweep/drain at a time (
// ). It is an arbitrary service-unique constant; any other advisory-lock user
// in the same database must avoid it.
const LeaderLockKey int64 = 0x636E5F736368 // "cn_sch"

// Leader provides single-writer semantics across replicas via a Postgres
// advisory lock — the "advisory-lock leader election" with no new dependency.
// WithLock acquires the lock on a dedicated pooled connection for the duration
// of one job pass and releases it immediately after, so exactly one replica's
// tick runs the work while the others no-op that tick. Per-tick acquire/release
// (rather than a long-held session lock) keeps the connection returned to the
// pool between ticks and lets leadership move freely if a pod restarts.
type Leader struct {
	pool *pgxpool.Pool
	key  int64
}

// NewLeader builds a Leader contending on LeaderLockKey.
func NewLeader(pool *pgxpool.Pool) *Leader {
	return &Leader{pool: pool, key: LeaderLockKey}
}

// NewLeaderWithKey builds a Leader contending on a caller-chosen key. It exists
// so a future second scheduler (or a test) can elect leadership on an isolated
// advisory-lock key instead of the shared LeaderLockKey.
func NewLeaderWithKey(pool *pgxpool.Pool, key int64) *Leader {
	return &Leader{pool: pool, key: key}
}

// WithLock runs fn iff this replica wins the advisory lock. ran reports whether
// fn was invoked (false means another replica holds the lock this tick — a
// normal, non-error outcome). The lock is held on a single connection for the
// whole of fn and released in a deferred unlock even if fn panics/errs.
func (l *Leader) WithLock(ctx context.Context, fn func(ctx context.Context) error) (ran bool, err error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("scheduler: acquire leader conn: %w", err)
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&got); err != nil {
		return false, fmt.Errorf("scheduler: try advisory lock: %w", err)
	}
	if !got {
		return false, nil
	}
	defer func() {
		// Release on the same connection that holds it; best-effort (the lock also
		// clears when the session ends if this ever fails).
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, l.key)
	}()

	return true, fn(ctx)
}
