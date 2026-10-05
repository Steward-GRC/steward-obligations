// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

//go:build smoke

// This smoke file extends the mail smoke suite with the Phase-7
// pause-on-failure path: with no working transport a send must land in
// mail_outbox and be ACKed (not delivered, not dropped); once a transport is
// available again the drainer must replay the held row and deliver it to
// maildev EXACTLY once. It uses a real Postgres (testcontainers) for the
// outbox, the real render sidecar, and a real maildev SMTP catcher — the same
// dev stack as the other smoke tests. No real Mailgun is ever contacted: the
// "no working transport" condition is driven by a toggleable pause decider,
// which is exactly what the circuit-breaker feeds the gate in production.
//
//	go test -tags smoke./internal/mail/... -run SmokePause -v
package mail_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	log "github.com/Bugs5382/go-log"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pg "github.com/Bugs5382/go-postgres"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// toggleDecider is a PauseDecider a smoke test flips: pause=true simulates "no
// working transport" (breaker open), pause=false simulates recovery.
type toggleDecider struct {
	mu    sync.Mutex
	pause bool
}

func (d *toggleDecider) set(v bool) { d.mu.Lock(); d.pause = v; d.mu.Unlock() }
func (d *toggleDecider) ShouldPause(context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pause
}

// smokeOutbox adapts *store.MailOutboxStore to mail.OutboxStore for the smoke
// test (mirrors cmd/server's production adapter).
type smokeOutbox struct{ s *store.MailOutboxStore }

func (a smokeOutbox) Enqueue(ctx context.Context, it mail.OutboxItem) error {
	return a.s.Enqueue(ctx, store.MailOutboxRow{
		Kind: it.Kind, Recipient: it.Recipient, From: it.From, Subject: it.Subject,
		HTML: it.HTML, Text: it.Text, UserID: it.UserID, DedupKey: it.DedupKey,
	})
}

func (a smokeOutbox) ClaimPending(ctx context.Context, limit int) ([]mail.OutboxItem, error) {
	rows, err := a.s.ClaimPending(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]mail.OutboxItem, len(rows))
	for i, r := range rows {
		out[i] = mail.OutboxItem{
			ID: r.ID, Kind: r.Kind, UserID: r.UserID, Recipient: r.Recipient, From: r.From,
			Subject: r.Subject, HTML: r.HTML, Text: r.Text, DedupKey: r.DedupKey, Attempts: r.Attempts,
		}
	}
	return out, nil
}

func (a smokeOutbox) MarkSent(ctx context.Context, id string) error { return a.s.MarkSent(ctx, id) }
func (a smokeOutbox) MarkFailed(ctx context.Context, id, e string) error {
	return a.s.MarkFailed(ctx, id, e)
}
func (a smokeOutbox) Reschedule(ctx context.Context, id, e string, next time.Time) error {
	return a.s.Reschedule(ctx, id, e, next)
}

func TestSmokePauseHoldsThenDrainsToMaildev(t *testing.T) {
	ctx := context.Background()

	smtpHost, smtpPort, httpBase := startMaildev(t, ctx)
	startRenderSidecar(t)
	pool := startSmokePostgres(t, ctx)
	outbox := smokeOutbox{s: store.NewMailOutboxStore(pool)}

	dec := &toggleDecider{pause: true} // start with no working transport

	cfg := mail.SenderConfig{
		SMTPHost:     smtpHost,
		SMTPPort:     smtpPort,
		From:         "no-reply@example.org",
		SidecarURL:   "http://127.0.0.1:" + sidecarPort,
		AppEnv:       "dev",
		SMTPStartTLS: false,
	}
	sender, err := mail.NewSender(cfg, mail.WithPauseGate(dec, outbox))
	if err != nil {
		t.Fatalf("mail.NewSender: %v", err)
	}

	const to = "held@example.org"
	vars := map[string]any{
		"ackUrl":         "https://policy.example.org/portal/acknowledgements/pv-smoke",
		"bodyText":       "Please review the policy below and confirm your acknowledgement.",
		"itemTitle":      "Acceptable Use of AI Tools Policy",
		"preferencesUrl": "https://policy.example.org/portal/preferences",
		"recipientName":  "Carol Example",
	}

	// 1) No working transport → held in outbox, ErrMailPaused, nothing delivered.
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = sender.Send(sendCtx, "ack-required", "smoke-pause-1", to, "smoke-pause-ref", vars)
	if !mail.IsPaused(err) {
		t.Fatalf("Send: expected ErrMailPaused, got %v", err)
	}
	if n, e := store.NewMailOutboxStore(pool).CountByStatus(ctx, "pending"); e != nil || n != 1 {
		t.Fatalf("expected 1 pending outbox row (err=%v), got %d", e, n)
	}
	if msgs := pollMaildevMessages(t, httpBase, 1); len(msgs) != 0 {
		t.Fatalf("maildev should be empty while paused, got %d messages", len(msgs))
	}

	// 2) Transport recovers → drainer replays the held row to maildev exactly once.
	dec.set(false)
	drainer := mail.NewDrainer(outbox, sender, log.New("smoke-drain"),
		mail.WithDrainPacer(func(context.Context) {}))
	drainer.DrainOnce(ctx)

	msgs := pollMaildevMessages(t, httpBase, 1)
	if len(msgs) != 1 {
		t.Fatalf("maildev: got %d messages after drain, want exactly 1", len(msgs))
	}
	if len(msgs[0].To) != 1 || msgs[0].To[0].Address != to {
		t.Fatalf("drained message To = %+v, want [%s]", msgs[0].To, to)
	}
	if n, e := store.NewMailOutboxStore(pool).CountByStatus(ctx, "sent"); e != nil || n != 1 {
		t.Fatalf("expected 1 sent outbox row (err=%v), got %d", e, n)
	}

	// 3) A second drain must not re-deliver (replay-once).
	drainer.DrainOnce(ctx)
	if msgs := pollMaildevMessages(t, httpBase, 2); len(msgs) != 1 {
		t.Fatalf("re-drain double-sent: maildev has %d messages, want 1", len(msgs))
	}
}

// startSmokePostgres launches a Postgres container and runs the service
// migrations (including 0004_mail_outbox), returning a pool. Mirrors the
// store test helper's container-unavailable skip.
func startSmokePostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	container, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("compliance_smoke"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("postgres container unavailable (%v) — skipping smoke test", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	migrationsDir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	if err := pg.Migrate(dsn, migrationsDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
