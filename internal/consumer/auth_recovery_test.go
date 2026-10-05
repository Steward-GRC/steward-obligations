// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/stretchr/testify/require"
)

// capturingSender records the full Send call (kind, userID, to, dedupRef,
// vars) so auth-recovery tests can assert on the rendered vars, not just the
// recipient. (The shared fakeSender in sso_lifecycle_test.go does not capture
// vars.)
type capturingSender struct {
	calls []capturedRecoverySend
	err   error
}

type capturedRecoverySend struct {
	kind, userID, to, dedupRef string
	vars                       any
}

func (s *capturingSender) Send(_ context.Context, kind, userID, to, dedupRef string, vars any) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, capturedRecoverySend{kind: kind, userID: userID, to: to, dedupRef: dedupRef, vars: vars})
	return nil
}

func TestAuthRecovery_RendersKratosRecoveryToVarsEmail(t *testing.T) {
	snd := &capturingSender{}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	body := []byte(`{"event":"auth.recovery","vars":{"email":"erin@example.org","recipientName":"Erin Example","recoveryCode":"483920","expiresInMinutes":60}}`)
	require.NoError(t, c.Handle(context.Background(), body))

	require.Len(t, snd.calls, 1)
	call := snd.calls[0]
	require.Equal(t, "kratos-recovery", call.kind)
	require.Equal(t, "erin@example.org", call.to)
	vars, ok := call.vars.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "483920", vars["recoveryCode"])
	require.Equal(t, "Erin Example", vars["recipientName"])
	require.Contains(t, call.dedupRef, "483920", "dedupRef must include the code so repeated resets aren't deduped/suppressed away")
}

func TestAuthRecovery_FallsBackToUserResolver(t *testing.T) {
	snd := &capturingSender{}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{"u1": "bob@example.org"})
	body := []byte(`{"event":"auth.recovery","vars":{"userId":"u1","recoveryCode":"111111"}}`)
	require.NoError(t, c.Handle(context.Background(), body))
	require.Len(t, snd.calls, 1)
	require.Equal(t, "bob@example.org", snd.calls[0].to)
	require.Equal(t, "u1", snd.calls[0].userID)
}

func TestAuthRecovery_NoRecipient_Skips(t *testing.T) {
	snd := &capturingSender{}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	body := []byte(`{"event":"auth.recovery","vars":{"recoveryCode":"111111"}}`)
	require.NoError(t, c.Handle(context.Background(), body))
	require.Empty(t, snd.calls, "no recipient -> nothing sent, no error (not requeued)")
}

func TestAuthRecovery_UnexpectedEvent_Skips(t *testing.T) {
	snd := &capturingSender{}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	body := []byte(`{"event":"something.else","vars":{"email":"x@y.z","recoveryCode":"1"}}`)
	require.NoError(t, c.Handle(context.Background(), body))
	require.Empty(t, snd.calls)
}

func TestAuthRecovery_DistinctCodesDistinctDedupRefs(t *testing.T) {
	snd := &capturingSender{}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	require.NoError(t, c.Handle(context.Background(), []byte(`{"event":"auth.recovery","vars":{"email":"a@b.c","recoveryCode":"111111"}}`)))
	require.NoError(t, c.Handle(context.Background(), []byte(`{"event":"auth.recovery","vars":{"email":"a@b.c","recoveryCode":"222222"}}`)))
	require.Len(t, snd.calls, 2)
	require.NotEqual(t, snd.calls[0].dedupRef, snd.calls[1].dedupRef, "two distinct codes must produce distinct dedupRefs")
}

func TestAuthRecovery_SenderError_Propagates(t *testing.T) {
	snd := &capturingSender{err: errors.New("smtp down")}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	body := []byte(`{"event":"auth.recovery","vars":{"email":"a@b.c","recoveryCode":"1"}}`)
	require.Error(t, c.Handle(context.Background(), body))
}

func TestAuthRecovery_InvalidJSON_Errors(t *testing.T) {
	snd := &capturingSender{}
	c := consumer.NewAuthRecoveryConsumer(snd, fakeUsers{})
	require.Error(t, c.Handle(context.Background(), []byte("not json")))
}
