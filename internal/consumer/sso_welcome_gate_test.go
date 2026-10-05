// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
)

// TestSSOWelcome_GatedSkipsWhenAlreadyWelcomed: when the shared welcome gate is
// wired, an sso.account_provisioned whose account was already welcomed (e.g. by
// the account-created consumer's welcome-account) is skipped — a federated
// first-login never receives two welcomes.
func TestSSOWelcome_GatedSkipsWhenAlreadyWelcomed(t *testing.T) {
	sender := &fakeSender{}
	gate := newFakeGate()
	// Simulate the account-created welcome having already claimed this user.
	claimed, err := gate.Claim(context.Background(), "u1")
	require.NoError(t, err)
	require.True(t, claimed)

	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{}).WithWelcomeGate(gate)
	err = c.Handle(context.Background(), []byte(`{"event":"sso.account_provisioned","vars":{"email":"newhire@example.org","userId":"u1"}}`))
	require.NoError(t, err)
	require.Empty(t, sender.sentTo, "already-welcomed account must not receive sso-account-welcome")
}

// TestSSOWelcome_GatedSendsAndClaimsWhenFirst: when the account has NOT been
// welcomed, the SSO welcome sends and claims the gate so a later
// account-created welcome-account is deduped.
func TestSSOWelcome_GatedSendsAndClaimsWhenFirst(t *testing.T) {
	sender := &fakeSender{}
	gate := newFakeGate()
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{}).WithWelcomeGate(gate)

	err := c.Handle(context.Background(), []byte(`{"event":"sso.account_provisioned","vars":{"email":"newhire@example.org","userId":"u1"}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"newhire@example.org"}, sender.sentTo)
	require.Equal(t, "sso-account-welcome", sender.lastKind)
	require.True(t, gate.isClaimed("u1"), "SSO welcome must claim the shared gate so welcome-account dedupes")
}
