// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"strings"
	"time"

	email "github.com/Bugs5382/go-email"
)

// OutboxItem is one durable, held email: a fully-rendered message captured by
// the pause-gate (see PauseGate) when there is no working transport, plus the
// replay bookkeeping the drainer needs. It is deliberately the RENDERED message
// (subject/html/text already through the render sidecar) rather than the render
// inputs, so a replay re-SENDS the exact bytes the recipient would have gotten
// — no dependency on the sidecar still rendering the same output later.
//
// Recipient is the primary To address captured BEFORE devCatchAll rewrites it
// (the pause-gate is outermost), so the outbox always holds the real intended
// recipient; a dev replay re-applies the catch-all on its own way back out.
type OutboxItem struct {
	// ID is the store-assigned row id. Empty on a fresh Enqueue; populated by
	// ClaimPending so the drainer can mark the row sent/failed/rescheduled.
	ID string
	// Kind is the render kind (e.g. "policy-ack-reminder"), read back off
	// Meta["kind"] and stamped onto the replay's audit event.
	Kind string
	// UserID is the send's subject user (Meta["user_id"]); replayed onto the
	// audit event, empty for admin/site-wide sends.
	UserID string
	// Recipient is the primary To address (pre-catch-all).
	Recipient string
	// From is the envelope/header From the message carried.
	From string
	// Subject/HTML/Text are the rendered bodies.
	Subject string
	HTML    string
	Text    string
	// DedupKey mirrors go-email's Meta["dedup_key"] (userID:kind:dedupRef). It
	// is the durable idempotency key: the outbox keeps at most one non-failed
	// row per key, so a redelivered trigger never double-holds and a replay
	// never double-sends. Empty when the original send disabled dedup.
	DedupKey string
	// Attempts counts replay attempts (populated by ClaimPending).
	Attempts int
	// LastError is the most recent replay failure reason (populated by
	// ClaimPending; never carries the api key).
	LastError string
}

// OutboxWriter persists a held message. The pause-gate depends on only this
// narrow slice so it can be unit-tested with a tiny fake and so a nil writer
// (pure decision test) is a valid "don't persist" mode.
type OutboxWriter interface {
	// Enqueue durably holds it. It MUST be idempotent on DedupKey: a second
	// Enqueue for a key that already has a non-failed row is a no-op (no
	// duplicate hold), so a redelivered AMQP message never double-holds.
	Enqueue(ctx context.Context, it OutboxItem) error
}

// OutboxStore is the full durable outbox the drainer drives: the writer plus
// the claim/mark operations that replay a backlog exactly once.
type OutboxStore interface {
	OutboxWriter
	// ClaimPending returns up to limit pending rows whose next_attempt_at has
	// elapsed, oldest first (FIFO replay). Each carries its ID/Attempts so the
	// drainer can finalize it.
	ClaimPending(ctx context.Context, limit int) ([]OutboxItem, error)
	// MarkSent flips a row to sent (terminal). A sent row is never re-claimed,
	// so a replay delivers each held message exactly once.
	MarkSent(ctx context.Context, id string) error
	// MarkFailed flips a row to failed (terminal) after a permanent replay
	// error, recording lastErr.
	MarkFailed(ctx context.Context, id, lastErr string) error
	// Reschedule keeps a row pending after a transient replay error, bumping
	// attempts and holding it until nextAttempt so a blip does not spin.
	Reschedule(ctx context.Context, id, lastErr string, nextAttempt time.Time) error
}

// outboxReplayMetaKey marks a Message as a drainer replay of an existing outbox
// row. The pause-gate honors it: a replay that re-hits an open breaker returns
// ErrMailPaused WITHOUT re-persisting (the row is already held), so a breaker
// that reopens mid-drain never duplicates a hold.
const outboxReplayMetaKey = "outbox_replay"

// outboxItemFromMessage captures a rendered *email.Message into an OutboxItem.
// The recipient is the primary To joined (this service sends one recipient per
// message; cc/bcc are not used on the notification paths).
func outboxItemFromMessage(m *email.Message) OutboxItem {
	kind, _ := m.Meta["kind"].(string)
	userID, _ := m.Meta["user_id"].(string)
	dedup, _ := m.Meta["dedup_key"].(string)
	return OutboxItem{
		Kind:      kind,
		UserID:    userID,
		Recipient: strings.Join(m.To, ","),
		From:      m.From,
		Subject:   m.Subject,
		HTML:      m.HTML,
		Text:      m.Text,
		DedupKey:  dedup,
	}
}

// isOutboxReplay reports whether m is a drainer replay (see outboxReplayMetaKey).
func isOutboxReplay(m *email.Message) bool {
	if m == nil || m.Meta == nil {
		return false
	}
	v, _ := m.Meta[outboxReplayMetaKey].(bool)
	return v
}
