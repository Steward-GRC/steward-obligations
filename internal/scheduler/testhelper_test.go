// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pg "github.com/Bugs5382/go-postgres"
)

// newTestDB returns a Postgres-backed pool for the calling test, mirroring the
// store package helper: DATABASE_TEST_DSN (CI) provisions a fresh database on a
// shared instance; otherwise an ephemeral testcontainer is launched (local).
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if dsn := os.Getenv("DATABASE_TEST_DSN"); dsn != "" {
		return newTestDBFromEnv(t, dsn)
	}
	return newTestDBFromContainer(t)
}

func newTestDBFromEnv(t *testing.T, baseDSN string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	defer admin.Close()

	dbName := uniqueDBName()
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		t.Fatalf("create db %s: %v", dbName, err)
	}

	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse base dsn: %v", err)
	}
	u.Path = "/" + dbName
	testDSN := u.String()

	migrationsDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	if err := pg.Migrate(testDSN, migrationsDir); err != nil {
		t.Fatalf("migrate %s: %v", dbName, err)
	}

	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		dropAdmin, err := pgxpool.New(dropCtx, baseDSN)
		if err != nil {
			return
		}
		defer dropAdmin.Close()
		_, _ = dropAdmin.Exec(dropCtx, fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, dbName))
	})
	return pool
}

func newTestDBFromContainer(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("compliance_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("postgres container unavailable (%v) — skipping integration test", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	migrationsDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	if err := pg.Migrate(dsn, migrationsDir); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func uniqueDBName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "test_" + hex.EncodeToString(b[:])
}
