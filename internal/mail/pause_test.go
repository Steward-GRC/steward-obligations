// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"errors"
	"testing"

	email "github.com/Bugs5382/go-email"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// fakeDecider is a settable PauseDecider.
type fakeDecider struct{ pause bool }

func (f *fakeDecider) ShouldPause(context.Context) bool { return f.pause }

// fakeOutbox records enqueued items and can be told to fail.
type fakeOutbox struct {
	items []mail.OutboxItem
	err   error
}

func (f *fakeOutbox) Enqueue(_ context.Context, it mail.OutboxItem) error {
	if f.err != nil {
		return f.err
	}
	f.items = append(f.items, it)
	return nil
}

// buildGated composes the pause-gate around a terminal SendFunc that records
// whether it ran, mirroring how email.New chains a middleware over the
// transport.
func buildGated(dec mail.PauseDecider, ob mail.OutboxWriter) (send email.SendFunc, delivered *bool) {
	var ran bool
	terminal := email.SendFunc(func(context.Context, *email.Message) error {
		ran = true
		return nil
	})
	return mail.PauseGate(dec, ob)(terminal), &ran
}

func pauseTestMsg() *email.Message {
	return &email.Message{
		From:    "no-reply@example.org",
		To:      []string{"user@example.com"},
		Subject: "Hi",
		HTML:    "<p>hi</p>",
		Text:    "hi",
		Meta: map[string]any{
			"kind":      "policy-ack-reminder",
			"user_id":   "u-1",
			"dedup_key": "u-1:policy-ack-reminder:v-1",
		},
	}
}

func TestPauseGate_PassesThroughWhenHealthy(t *testing.T) {
	ob := &fakeOutbox{}
	send, delivered := buildGated(&fakeDecider{pause: false}, ob)

	require.NoError(t, send(context.Background(), pauseTestMsg()))
	require.True(t, *delivered, "a healthy send must reach the transport")
	require.Empty(t, ob.items, "nothing is held when a transport is working")
}

func TestPauseGate_EmitsErrMailPausedAndHoldsWhenNoTransport(t *testing.T) {
	ob := &fakeOutbox{}
	send, delivered := buildGated(&fakeDecider{pause: true}, ob)

	err := send(context.Background(), pauseTestMsg())
	require.True(t, mail.IsPaused(err), "no working transport must return ErrMailPaused")
	require.False(t, *delivered, "a paused send must NOT reach the transport")

	require.Len(t, ob.items, 1, "the rendered message must be held in the outbox")
	held := ob.items[0]
	require.Equal(t, "policy-ack-reminder", held.Kind)
	require.Equal(t, "user@example.com", held.Recipient)
	require.Equal(t, "Hi", held.Subject)
	require.Equal(t, "<p>hi</p>", held.HTML)
	require.Equal(t, "u-1:policy-ack-reminder:v-1", held.DedupKey)
}

func TestPauseGate_NotPausedAndNotTransient(t *testing.T) {
	// Guards the taxonomy: ErrMailPaused is neither a deliberate drop
	// (ErrSuppressed) nor a retry-me (TransientError).
	send, _ := buildGated(&fakeDecider{pause: true}, &fakeOutbox{})
	err := send(context.Background(), pauseTestMsg())

	require.True(t, mail.IsPaused(err))
	require.False(t, errors.Is(err, email.ErrSuppressed), "pause is not a suppression")
	var te email.TransientError
	require.False(t, errors.As(err, &te), "pause must not be transient (Retry would hot-spin)")
}

func TestPauseGate_ReplayDoesNotRePersist(t *testing.T) {
	ob := &fakeOutbox{}
	send, delivered := buildGated(&fakeDecider{pause: true}, ob)

	msg := pauseTestMsg()
	msg.Meta["outbox_replay"] = true // a drainer replay of an already-held row

	err := send(context.Background(), msg)
	require.True(t, mail.IsPaused(err), "a replay hitting an open breaker still holds")
	require.False(t, *delivered)
	require.Empty(t, ob.items, "a replay must not re-persist an already-held row")
}

func TestPauseGate_PersistFailureIsNotPaused(t *testing.T) {
	ob := &fakeOutbox{err: errors.New("db down")}
	send, delivered := buildGated(&fakeDecider{pause: true}, ob)

	err := send(context.Background(), pauseTestMsg())
	require.Error(t, err)
	require.False(t, mail.IsPaused(err), "a failed persist must NOT look like a successful hold (caller must not ack)")
	require.False(t, *delivered)
}

func TestPauseGate_NilOutboxStillPauses(t *testing.T) {
	// Pure decision mode (no persistence wired): still returns ErrMailPaused.
	send, delivered := buildGated(&fakeDecider{pause: true}, nil)
	require.True(t, mail.IsPaused(send(context.Background(), pauseTestMsg())))
	require.False(t, *delivered)
}
