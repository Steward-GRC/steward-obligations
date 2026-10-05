// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package readiness registers the service's dependencies with go-buildinfo's
// health checker. Postgres and RabbitMQ are required: without them no
// acknowledgement, preference or audit event can be written, and no event is
// consumed. The rest are optional and degrade the service instead of draining
// it: the Valkey caches fall back to direct reads; core and identity are
// needed for obligation lookups and sends, but acknowledgements, preferences
// and reports keep working without them; and with the render sidecar down
// email waits in the mail outbox.
package readiness

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Bugs5382/go-buildinfo/health"
	postgres "github.com/Bugs5382/go-postgres"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Dependency names, as they appear in the report and the
// steward-depstate-<name> headers.
const (
	Postgres = "postgres"
	RabbitMQ = "rabbitmq"
	Valkey   = "valkey"
	Core     = "core"
	Identity = "identity"
	Render   = "render"
)

// Database is the Postgres the service runs on.
type Database interface {
	Ping(ctx context.Context) error
	ServerVersion(ctx context.Context) (string, error)
}

// Broker is the RabbitMQ connection; go-rabbitmq's Conn reports it.
type Broker interface{ Healthy() bool }

// Deps are the dependencies to report. A nil optional check is a feature that
// is off, and is not reported.
type Deps struct {
	Postgres Database
	Broker   Broker
	Cache    func(ctx context.Context) error
	Core     func(ctx context.Context) error
	Identity func(ctx context.Context) error
	Render   func(ctx context.Context) error
}

var errBrokerDown = errors.New("rabbitmq connection is down")

// New returns a checker with deps registered.
func New(d Deps, opts ...health.Option) (*health.Checker, error) {
	deps := []health.Dependency{
		{Name: Postgres, Required: true, Check: d.Postgres.Ping, Version: d.Postgres.ServerVersion},
		{Name: RabbitMQ, Required: true, Check: func(context.Context) error {
			if !d.Broker.Healthy() {
				return errBrokerDown
			}
			return nil
		}},
	}
	for _, o := range []struct {
		name  string
		check func(context.Context) error
	}{{Valkey, d.Cache}, {Core, d.Core}, {Identity, d.Identity}, {Render, d.Render}} {
		if o.check != nil {
			deps = append(deps, health.Dependency{Name: o.name, Check: o.check})
		}
	}
	c := health.New(opts...)
	return c, c.Register(deps...)
}

// GRPCServing checks a callee through its standard gRPC health service.
func GRPCServing(conn grpc.ClientConnInterface) func(context.Context) error {
	hc := healthpb.NewHealthClient(conn)
	return func(ctx context.Context) error {
		r, err := hc.Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			return err
		}
		if r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return fmt.Errorf("health status %s", r.GetStatus())
		}
		return nil
	}
}

// HTTPReady checks a component through its /readyz endpoint.
func HTTPReady(client *http.Client, baseURL string) func(context.Context) error {
	url := strings.TrimRight(baseURL, "/") + "/readyz"
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("readyz answered %d", res.StatusCode)
		}
		return nil
	}
}

// PostgresDB adapts go-postgres's DB.
func PostgresDB(db *postgres.DB) Database { return pgDB{db} }

type pgDB struct{ db *postgres.DB }

func (p pgDB) Ping(ctx context.Context) error { return p.db.Ping(ctx) }

// ServerVersion drops the build suffix ("16.4 (Debian 16.4-1)"), which the
// header would redact.
func (p pgDB) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := p.db.Pool().QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", err
	}
	if f := strings.Fields(v); len(f) > 0 {
		return f[0], nil
	}
	return v, nil
}
