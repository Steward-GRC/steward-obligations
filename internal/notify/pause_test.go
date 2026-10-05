// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// The dispatcher swallows email-channel failures so one bad recipient never
// starves the other channels. Phase 7 changes only the disposition of the
// swallow: a pause is surfaced (logged, held in outbox by the gate) while an
// ordinary per-recipient failure stays swallowed — either way, the other
// channels still fire and the send is never requeued through the dispatcher.

func TestDispatcher_PausedEmailDoesNotStarveOtherChannels(t *testing.T) {
	pref := store.NotifPref{UserID: "u1", Email: true, InApp: true, Push: false}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{err: mail.ErrMailPaused} // pause-gate held it in the outbox
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	d := notify.NewDispatcher(prefStore, email, inapp, push).WithClock(func() int { return 12 })
	d.SendAckReminder(context.Background(), notify.AckReminderPayload{UserID: "u1", Type: "initial"})

	require.Len(t, email.payloads, 1, "email channel was still invoked")
	require.Len(t, inapp.payloads, 1, "a paused email must not starve the in-app channel")
}

func TestDispatcher_OrdinaryEmailFailureStillSwallowed(t *testing.T) {
	pref := store.NotifPref{UserID: "u1", Email: true, InApp: true, Push: false}
	prefStore := &fakePrefStore{pref: pref}
	email := &recordingChannel{err: errors.New("one bad address")}
	inapp := &recordingChannel{}
	push := &recordingChannel{}

	d := notify.NewDispatcher(prefStore, email, inapp, push).WithClock(func() int { return 12 })
	// Must not panic or abort the audience; in-app still fires.
	d.SendPolicyPublished(context.Background(), notify.AckReminderPayload{UserID: "u1", Type: "policy_published"})

	require.Len(t, inapp.payloads, 1, "an ordinary per-recipient failure stays swallowed — other channels unaffected")
}
