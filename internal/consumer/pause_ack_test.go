// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// A paused send is durably held by the pause-gate in the outbox, so the
// direct-send consumers must ACK (Handle returns nil) rather than requeue —
// requeuing would hot-loop through a multi-minute outage.

func TestAuthRecovery_PauseIsAckedNotRequeued(t *testing.T) {
	snd := &capturingSender{err: mail.ErrMailPaused}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	body := []byte(`{"event":"auth.recovery","vars":{"email":"erin@example.org","recoveryCode":"483920"}}`)

	require.NoError(t, c.Handle(context.Background(), body),
		"a paused send is held in the outbox → ACK, do not requeue")
}

func TestAuthRecovery_OrdinaryErrorStillRequeues(t *testing.T) {
	snd := &capturingSender{err: errors.New("sidecar 500")}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	body := []byte(`{"event":"auth.recovery","vars":{"email":"erin@example.org","recoveryCode":"483920"}}`)

	require.Error(t, c.Handle(context.Background(), body),
		"an ordinary send failure still propagates so the delivery is retried")
}

func TestSSOLifecycle_PauseIsAckedNotRequeued(t *testing.T) {
	snd := &fakeSender{err: mail.ErrMailPaused}
	c := consumer.NewSSOLifecycleConsumer(snd, fakeUsers{"u1": "bob@example.org"}, fakeSiteAdmins{"admin@example.org"})

	// A fan-out event (break-glass) where every recipient pauses: all are held
	// individually by the pause-gate, so the whole handler ACKs.
	require.NoError(t, c.Handle(context.Background(),
		[]byte(`{"event":"user.break_glass.login","vars":{"userId":"u1","email":"bob@example.org"}}`)),
		"paused fan-out sends are held in the outbox → ACK")
}

func TestSSOLifecycle_OrdinaryErrorStillRequeues(t *testing.T) {
	snd := &fakeSender{err: errors.New("sidecar 500")}
	c := consumer.NewSSOLifecycleConsumer(snd, fakeUsers{"u1": "bob@example.org"}, fakeSiteAdmins{"admin@example.org"})

	require.Error(t, c.Handle(context.Background(),
		[]byte(`{"event":"user.break_glass.login","vars":{"userId":"u1","email":"bob@example.org"}}`)),
		"an ordinary fan-out failure still surfaces for retry")
}
