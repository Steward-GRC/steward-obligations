// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	email "github.com/Bugs5382/go-email"
	log "github.com/Bugs5382/go-log"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// memRow is a stored outbox row for the in-memory fake store.
type memRow struct {
	it          mail.OutboxItem
	status      string // pending | sent | failed
	nextAttempt time.Time
}

// memOutbox is an in-memory OutboxStore for drainer tests.
type memOutbox struct {
	mu   sync.Mutex
	rows []*memRow
	seq  int
	now  func() time.Time
}

func newMemOutbox() *memOutbox { return &memOutbox{now: time.Now} }

func (m *memOutbox) Enqueue(_ context.Context, it mail.OutboxItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it.DedupKey != "" {
		for _, r := range m.rows {
			if r.it.DedupKey == it.DedupKey && r.status != "failed" {
				return nil // idempotent: already held
			}
		}
	}
	m.seq++
	it.ID = fmt.Sprintf("row-%d", m.seq)
	m.rows = append(m.rows, &memRow{it: it, status: "pending", nextAttempt: m.now()})
	return nil
}

func (m *memOutbox) ClaimPending(_ context.Context, limit int) ([]mail.OutboxItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []mail.OutboxItem
	for _, r := range m.rows {
		if r.status == "pending" && !r.nextAttempt.After(m.now()) {
			out = append(out, r.it)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *memOutbox) find(id string) *memRow {
	for _, r := range m.rows {
		if r.it.ID == id {
			return r
		}
	}
	return nil
}

func (m *memOutbox) MarkSent(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(id); r != nil {
		r.status = "sent"
	}
	return nil
}

func (m *memOutbox) MarkFailed(_ context.Context, id, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(id); r != nil {
		r.status = "failed"
		r.it.LastError = lastErr
	}
	return nil
}

func (m *memOutbox) Reschedule(_ context.Context, id, lastErr string, next time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(id); r != nil {
		r.it.Attempts++
		r.it.LastError = lastErr
		r.nextAttempt = next
	}
	return nil
}

func (m *memOutbox) statusOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(id); r != nil {
		return r.status
	}
	return ""
}

func (m *memOutbox) countStatus(status string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.rows {
		if r.status == status {
			n++
		}
	}
	return n
}

// scriptSender is a fake RenderedSender: it records every replay and returns a
// per-recipient scripted error (nil by default).
type scriptSender struct {
	mu    sync.Mutex
	calls []mail.OutboxItem
	errFn func(it mail.OutboxItem) error
}

func (s *scriptSender) SendRendered(_ context.Context, it mail.OutboxItem) error {
	s.mu.Lock()
	s.calls = append(s.calls, it)
	fn := s.errFn
	s.mu.Unlock()
	if fn != nil {
		return fn(it)
	}
	return nil
}

func (s *scriptSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func enqueueN(t *testing.T, ob *memOutbox, n int) {
	t.Helper()
	for i := range n {
		require.NoError(t, ob.Enqueue(context.Background(), mail.OutboxItem{
			Kind:      "policy-ack-reminder",
			Recipient: fmt.Sprintf("u%d@example.com", i),
			Subject:   "Hi",
			HTML:      "<p>hi</p>",
			DedupKey:  fmt.Sprintf("dk-%d", i),
		}))
	}
}

func TestDrainer_ReplaysAllAndRateCaps(t *testing.T) {
	ob := newMemOutbox()
	enqueueN(t, ob, 3)
	sender := &scriptSender{}

	var paced int
	d := mail.NewDrainer(ob, sender, log.New("drain-test"),
		mail.WithDrainPacer(func(context.Context) { paced++ }))

	d.DrainOnce(context.Background())

	require.Equal(t, 3, sender.count(), "every pending row is replayed")
	require.Equal(t, 3, ob.countStatus("sent"), "every replayed row is marked sent")
	require.Equal(t, 0, ob.countStatus("pending"))
	require.Equal(t, 3, paced, "the rate cap paces once per send")
}

func TestDrainer_IdempotentNoDoubleSend(t *testing.T) {
	ob := newMemOutbox()
	enqueueN(t, ob, 2)
	// A redelivery re-enqueues the same dedup keys: no new rows.
	enqueueN(t, ob, 2)
	require.Equal(t, 2, len(ob.rows), "dedup key keeps the outbox idempotent on re-enqueue")

	sender := &scriptSender{}
	d := mail.NewDrainer(ob, sender, log.New("drain-test"), mail.WithDrainPacer(func(context.Context) {}))

	d.DrainOnce(context.Background())
	require.Equal(t, 2, sender.count())

	// A second drain must send nothing (sent rows are never re-claimed).
	d.DrainOnce(context.Background())
	require.Equal(t, 2, sender.count(), "a sent row is never replayed again")
}

func TestDrainer_StopsOnPauseLeavesRemainingPending(t *testing.T) {
	ob := newMemOutbox()
	enqueueN(t, ob, 3) // row-1, row-2, row-3
	sender := &scriptSender{
		errFn: func(it mail.OutboxItem) error {
			if it.ID == "row-2" {
				return fmt.Errorf("held: %w", mail.ErrMailPaused)
			}
			return nil
		},
	}
	d := mail.NewDrainer(ob, sender, log.New("drain-test"), mail.WithDrainPacer(func(context.Context) {}))

	d.DrainOnce(context.Background())

	require.Equal(t, "sent", ob.statusOf("row-1"), "the row before the pause is delivered")
	require.Equal(t, "pending", ob.statusOf("row-2"), "the paused row stays held (not sent, not failed)")
	require.Equal(t, "pending", ob.statusOf("row-3"), "rows after the pause are deferred, not lost")
	require.Equal(t, 2, sender.count(), "drain stops at the pause — no further sends this round")
}

func TestDrainer_PermanentErrorMarksFailed(t *testing.T) {
	ob := newMemOutbox()
	enqueueN(t, ob, 1)
	sender := &scriptSender{errFn: func(mail.OutboxItem) error { return fmt.Errorf("mailgun: status 422: permanent") }}
	d := mail.NewDrainer(ob, sender, log.New("drain-test"), mail.WithDrainPacer(func(context.Context) {}))

	d.DrainOnce(context.Background())
	require.Equal(t, "failed", ob.statusOf("row-1"), "a permanent replay error is terminal")
}

func TestDrainer_TransientErrorReschedules(t *testing.T) {
	ob := newMemOutbox()
	enqueueN(t, ob, 1)
	sender := &scriptSender{errFn: func(mail.OutboxItem) error {
		return email.TransientError{Err: fmt.Errorf("503")}
	}}
	d := mail.NewDrainer(ob, sender, log.New("drain-test"),
		mail.WithDrainPacer(func(context.Context) {}),
		mail.WithDrainMaxAttempts(3))

	d.DrainOnce(context.Background())
	require.Equal(t, "pending", ob.statusOf("row-1"), "a transient error keeps the row pending for retry")
	require.Equal(t, 1, ob.rows[0].it.Attempts, "attempts bumped on a transient failure")
}

func TestDrainer_RunStopsOnContextCancel(t *testing.T) {
	ob := newMemOutbox()
	sender := &scriptSender{}
	d := mail.NewDrainer(ob, sender, log.New("drain-test"),
		mail.WithDrainPacer(func(context.Context) {}),
		mail.WithDrainInterval(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	// Trigger a round, then shut down; Run must return promptly (graceful stop).
	d.Trigger()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drainer Run did not stop on context cancel")
	}
}
