// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/Steward-GRC/steward-obligations/internal/notifytoken"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// verifySender satisfies consumer.Sender and records kind/to/dedupRef/vars.
type verifySender struct {
	kinds     []string
	tos       []string
	dedupRefs []string
	lastVars  map[string]any
	err       error
	callCount int
}

func (s *verifySender) Send(_ context.Context, kind, _ /*userID*/, to, dedupRef string, vars any) error {
	s.callCount++
	if s.err != nil {
		return s.err
	}
	s.kinds = append(s.kinds, kind)
	s.tos = append(s.tos, to)
	s.dedupRefs = append(s.dedupRefs, dedupRef)
	if m, ok := vars.(map[string]any); ok {
		s.lastVars = m
	}
	return nil
}

// fakeVerifyRecipients satisfies consumer.EmailVerificationRecipientResolver.
type fakeVerifyRecipients struct {
	byID map[string][2]string
	err  error
}

func (f fakeVerifyRecipients) ResolveWelcomeRecipient(_ context.Context, userID string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	v := f.byID[userID]
	return v[0], v[1], nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const verifyURL = "https://policy.example.com/notify/verify-email"

func mustVerifySigner(t *testing.T) *notifytoken.Signer {
	t.Helper()
	s, err := notifytoken.NewSigner("top-secret")
	require.NoError(t, err)
	return s
}

func newVerifyConsumer(sender consumer.Sender, users consumer.EmailVerificationRecipientResolver, signer *notifytoken.Signer) *consumer.EmailVerificationConsumer {
	return consumer.NewEmailVerificationConsumer(users, sender, signer, verifyURL, 48*time.Hour)
}

// TestEmailVerification_SendsVerification: a fresh account.created resolves
// the recipient, mints a token bound to (userID, email), and sends
// email-verification with a tokenized CTA link that verifies as that user's
// email under the same signer.
func TestEmailVerification_SendsVerification(t *testing.T) {
	sender := &verifySender{}
	users := fakeVerifyRecipients{byID: map[string][2]string{"u1": {"Erin Example", "erin@example.org"}}}
	signer := mustVerifySigner(t)
	c := newVerifyConsumer(sender, users, signer)

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.NoError(t, err)

	require.Equal(t, []string{"email-verification"}, sender.kinds)
	require.Equal(t, []string{"erin@example.org"}, sender.tos)
	require.Equal(t, "Erin Example", sender.lastVars["recipientName"])
	require.Equal(t, 48, sender.lastVars["ttlHours"])

	link, ok := sender.lastVars["verifyUrl"].(string)
	require.True(t, ok)
	require.Contains(t, link, verifyURL+"?token=")

	token := link[len(verifyURL+"?token="):]
	claims, err := signer.Verify(token, notifytoken.PurposeEmailVerify)
	require.NoError(t, err)
	require.Equal(t, "u1", claims.UserID)
	require.Equal(t, "erin@example.org", claims.Email)
}

// TestEmailVerification_NoEmailSkips: a user with no email resolves but is
// not sent to, and the message is treated as done (no error, no requeue
// storm).
func TestEmailVerification_NoEmailSkips(t *testing.T) {
	sender := &verifySender{}
	users := fakeVerifyRecipients{byID: map[string][2]string{"u1": {"Erin", ""}}}
	c := newVerifyConsumer(sender, users, mustVerifySigner(t))

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.NoError(t, err)
	require.Empty(t, sender.tos)
}

// TestEmailVerification_NoSignerSkips: with no signer configured (the
// NOTIFY_UNSUB_SECRET-unset case), the consumer must not panic or error --
// the whole feature is simply off.
func TestEmailVerification_NoSignerSkips(t *testing.T) {
	sender := &verifySender{}
	users := fakeVerifyRecipients{byID: map[string][2]string{"u1": {"Erin", "erin@example.org"}}}
	c := consumer.NewEmailVerificationConsumer(users, sender, nil, verifyURL, time.Hour)

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.NoError(t, err)
	require.Zero(t, sender.callCount)
}

// TestEmailVerification_MissingUserIDSkips + malformed body: both are
// permanent errors that must be swallowed (nil) so they never requeue
// forever.
func TestEmailVerification_MissingUserIDSkips(t *testing.T) {
	sender := &verifySender{}
	c := newVerifyConsumer(sender, fakeVerifyRecipients{}, mustVerifySigner(t))

	require.NoError(t, c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":""}`)))
	require.NoError(t, c.Handle(context.Background(), []byte(`{not json`)))
	require.Zero(t, sender.callCount)
}

// TestEmailVerification_SendFailureReturnsError: a transient send failure
// returns an error so the message is requeued and can retry.
func TestEmailVerification_SendFailureReturnsError(t *testing.T) {
	sender := &verifySender{err: errors.New("smtp down")}
	users := fakeVerifyRecipients{byID: map[string][2]string{"u1": {"Erin", "erin@example.org"}}}
	c := newVerifyConsumer(sender, users, mustVerifySigner(t))

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.Error(t, err)
}

// TestEmailVerification_ResolveFailureReturnsError: a transient identity
// resolve failure returns an error so the message is requeued.
func TestEmailVerification_ResolveFailureReturnsError(t *testing.T) {
	sender := &verifySender{}
	users := fakeVerifyRecipients{err: errors.New("identity unavailable")}
	c := newVerifyConsumer(sender, users, mustVerifySigner(t))

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.Error(t, err)
	require.Zero(t, sender.callCount)
}
