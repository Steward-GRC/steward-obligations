// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// acctWelcomeSender satisfies consumer.Sender and records kind/to/dedupRef/vars.
type acctWelcomeSender struct {
	kinds     []string
	tos       []string
	dedupRefs []string
	lastVars  map[string]any
	err       error
	callCount int
}

func (s *acctWelcomeSender) Send(_ context.Context, kind, _ /*userID*/, to, dedupRef string, vars any) error {
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

// fakeWelcomeRecipients satisfies consumer.WelcomeRecipientResolver:
// userID -> (name, email).
type fakeWelcomeRecipients struct {
	byID map[string][2]string
	err  error
}

func (f fakeWelcomeRecipients) ResolveWelcomeRecipient(_ context.Context, userID string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	v := f.byID[userID]
	return v[0], v[1], nil
}

// fakeGate is an in-memory consumer.WelcomeGate. Claim returns true exactly
// once per user id; Release removes a claim. It records releases so tests can
// assert the send-failure rollback path.
type fakeGate struct {
	mu       sync.Mutex
	claimed  map[string]bool
	released []string
	claimErr error
}

func newFakeGate() *fakeGate { return &fakeGate{claimed: map[string]bool{}} }

func (g *fakeGate) Claim(_ context.Context, userID string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.claimErr != nil {
		return false, g.claimErr
	}
	if g.claimed[userID] {
		return false, nil
	}
	g.claimed[userID] = true
	return true, nil
}

func (g *fakeGate) Release(_ context.Context, userID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.claimed, userID)
	g.released = append(g.released, userID)
	return nil
}

func (g *fakeGate) isClaimed(userID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.claimed[userID]
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const acctURL = "https://policy.example.com/account"

func newAcctConsumer(sender consumer.Sender, users consumer.WelcomeRecipientResolver, gate consumer.WelcomeGate) *consumer.AccountCreatedConsumer {
	return consumer.NewAccountCreatedConsumer(users, sender, gate, acctURL)
}

// TestAccountCreated_SendsWelcome: a fresh account.created resolves the
// recipient and sends welcome-account with the account-URL call to action.
func TestAccountCreated_SendsWelcome(t *testing.T) {
	sender := &acctWelcomeSender{}
	users := fakeWelcomeRecipients{byID: map[string][2]string{"u1": {"Erin Example", "erin@example.org"}}}
	gate := newFakeGate()
	c := newAcctConsumer(sender, users, gate)

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.NoError(t, err)

	require.Equal(t, []string{"welcome-account"}, sender.kinds)
	require.Equal(t, []string{"erin@example.org"}, sender.tos)
	require.Equal(t, "Erin Example", sender.lastVars["recipientName"])
	require.Equal(t, acctURL, sender.lastVars["accountUrl"])
	require.True(t, gate.isClaimed("u1"))
}

// TestAccountCreated_DedupsPerAccount: a redelivered / repeated account.created
// for the same user sends the welcome only once (the durable gate).
func TestAccountCreated_DedupsPerAccount(t *testing.T) {
	sender := &acctWelcomeSender{}
	users := fakeWelcomeRecipients{byID: map[string][2]string{"u1": {"Erin", "erin@example.org"}}}
	gate := newFakeGate()
	c := newAcctConsumer(sender, users, gate)

	body := []byte(`{"event_type":"account.created","user_id":"u1"}`)
	require.NoError(t, c.Handle(context.Background(), body))
	require.NoError(t, c.Handle(context.Background(), body))

	require.Len(t, sender.tos, 1, "welcome-account must be sent exactly once per account")
}

// TestAccountCreated_FallbackName: a user with no display name gets the
// "there" greeting fallback so the template greeting is never empty.
func TestAccountCreated_FallbackName(t *testing.T) {
	sender := &acctWelcomeSender{}
	users := fakeWelcomeRecipients{byID: map[string][2]string{"u1": {"", "erin@example.org"}}}
	c := newAcctConsumer(sender, users, newFakeGate())

	require.NoError(t, c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`)))
	require.Equal(t, "there", sender.lastVars["recipientName"])
}

// TestAccountCreated_NoEmailSkips: a user with no email resolves but is not
// sent to, and the message is treated as done (no error, no requeue storm).
func TestAccountCreated_NoEmailSkips(t *testing.T) {
	sender := &acctWelcomeSender{}
	users := fakeWelcomeRecipients{byID: map[string][2]string{"u1": {"Erin", ""}}}
	c := newAcctConsumer(sender, users, newFakeGate())

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.NoError(t, err)
	require.Empty(t, sender.tos)
}

// TestAccountCreated_SendFailureReleasesClaim: when the send fails, the claim is
// released and an error is returned so the message is requeued and can retry.
func TestAccountCreated_SendFailureReleasesClaim(t *testing.T) {
	sender := &acctWelcomeSender{err: errors.New("smtp down")}
	users := fakeWelcomeRecipients{byID: map[string][2]string{"u1": {"Erin", "erin@example.org"}}}
	gate := newFakeGate()
	c := newAcctConsumer(sender, users, gate)

	err := c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":"u1"}`))
	require.Error(t, err)
	require.Equal(t, []string{"u1"}, gate.released, "a failed send must release the claim for retry")
	require.False(t, gate.isClaimed("u1"))
}

// TestAccountCreated_MissingUserIDSkips + malformed body: both are permanent
// errors that must be swallowed (nil) so they never requeue forever.
func TestAccountCreated_MissingUserIDSkips(t *testing.T) {
	sender := &acctWelcomeSender{}
	c := newAcctConsumer(sender, fakeWelcomeRecipients{}, newFakeGate())

	require.NoError(t, c.Handle(context.Background(), []byte(`{"event_type":"account.created","user_id":""}`)))
	require.NoError(t, c.Handle(context.Background(), []byte(`{not json`)))
	require.Zero(t, sender.callCount)
}
