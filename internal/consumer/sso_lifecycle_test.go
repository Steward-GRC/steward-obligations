// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeSender satisfies consumer.Sender and records every call so tests can
// assert on recipients, kind, and dedupRef.
type fakeSender struct {
	sentTo    []string
	lastKind  string
	lastVars  any
	dedupRefs []string
	err       error
}

func (f *fakeSender) Send(ctx context.Context, kind, userID, to, dedupRef string, vars any) error {
	if f.err != nil {
		return f.err
	}
	f.sentTo = append(f.sentTo, to)
	f.lastKind = kind
	f.lastVars = vars
	f.dedupRefs = append(f.dedupRefs, dedupRef)
	return nil
}

// fakeUsers satisfies consumer.UserEmailResolver.
type fakeUsers map[string]string

func (f fakeUsers) ResolveEmail(ctx context.Context, userID string) (string, error) {
	return f[userID], nil
}

// fakeSiteAdmins satisfies consumer.SiteAdminResolver: a fixed list of
// site-admin email addresses.
type fakeSiteAdmins []string

func (f fakeSiteAdmins) ListSiteAdminEmails(ctx context.Context) ([]string, error) {
	return f, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSSOConsumer_BreakGlassAlertsAdminsAndAccount is the Task-32 brief's
// verbatim Step 1 test: a break-glass event fans out to every site-admin AND
// the account, as kind break-glass-alert.
func TestSSOConsumer_BreakGlassAlertsAdminsAndAccount(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{"u1": "bob@example.org"}, fakeSiteAdmins{"admin@example.org"})
	err := c.Handle(context.Background(), []byte(`{"event":"user.break_glass.login","vars":{"userId":"u1","email":"bob@example.org"}}`))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"bob@example.org", "admin@example.org"}, sender.sentTo)
	require.Equal(t, "break-glass-alert", sender.lastKind)
}

// TestSSOConsumer_BreakGlassDedupRefUniquePerOccurrence verifies that two
// separate break-glass logins for the same user never share a dedupRef --
// every occurrence is a distinct security alert and must never be collapsed
// by internal/mail's Deduper.
func TestSSOConsumer_BreakGlassDedupRefUniquePerOccurrence(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{"u1": "bob@example.org"}, fakeSiteAdmins{"admin@example.org"})
	body := []byte(`{"event":"user.break_glass.login","vars":{"userId":"u1","email":"bob@example.org"}}`)

	require.NoError(t, c.Handle(context.Background(), body))
	require.NoError(t, c.Handle(context.Background(), body))

	require.Len(t, sender.dedupRefs, 4) // 2 recipients x 2 occurrences
	first := sender.dedupRefs[:2]
	second := sender.dedupRefs[2:]
	require.NotEqual(t, first, second, "two distinct break-glass occurrences must not share dedupRefs")
}

// TestSSOConsumer_AccountProvisionedWelcomesNewUser verifies
// sso.account_provisioned sends kind sso-account-welcome to the new user's
// email, forwarding the event's vars.
func TestSSOConsumer_AccountProvisionedWelcomesNewUser(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{})
	err := c.Handle(context.Background(), []byte(`{"event":"sso.account_provisioned","vars":{"email":"newhire@example.org"}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"newhire@example.org"}, sender.sentTo)
	require.Equal(t, "sso-account-welcome", sender.lastKind)
}

// TestSSOConsumer_AccountProvisionedIncludesLoginURL verifies
// : with WithLoginURL wired, sso.account_provisioned's
// vars gain a "loginUrl" field (driving the welcome template's "Sign in with
// SSO" CTA) without losing the event's own vars.
func TestSSOConsumer_AccountProvisionedIncludesLoginURL(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{}).
		WithLoginURL("https://policy.example.org")
	err := c.Handle(context.Background(), []byte(`{"event":"sso.account_provisioned","vars":{"email":"newhire@example.org"}}`))
	require.NoError(t, err)
	require.Equal(t, "sso-account-welcome", sender.lastKind)
	vars, ok := sender.lastVars.(map[string]any)
	require.True(t, ok, "expected vars to be a map[string]any")
	require.Equal(t, "https://policy.example.org", vars["loginUrl"])
	require.Equal(t, "newhire@example.org", vars["email"], "original event vars must survive the merge")
}

// TestSSOConsumer_AccountProvisionedOmitsLoginURLWhenUnwired verifies the
// default (WithLoginURL never called) leaves vars untouched -- no "loginUrl"
// key is injected, matching the template's optional prop (no CTA renders).
func TestSSOConsumer_AccountProvisionedOmitsLoginURLWhenUnwired(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{})
	err := c.Handle(context.Background(), []byte(`{"event":"sso.account_provisioned","vars":{"email":"newhire@example.org"}}`))
	require.NoError(t, err)
	vars, ok := sender.lastVars.(map[string]any)
	require.True(t, ok, "expected vars to be a map[string]any")
	_, present := vars["loginUrl"]
	require.False(t, present, "loginUrl must not be injected when WithLoginURL was never called")
}

// TestSSOConsumer_AccessGrantedNotifiesUser verifies sso.access_granted sends
// kind access-granted to the user's email.
func TestSSOConsumer_AccessGrantedNotifiesUser(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{})
	err := c.Handle(context.Background(), []byte(`{"event":"sso.access_granted","vars":{"email":"alice@example.org","groups":["engineering"]}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"alice@example.org"}, sender.sentTo)
	require.Equal(t, "access-granted", sender.lastKind)
}

// TestSSOConsumer_DomainEventsFanOutToSiteAdmins verifies the domain/IdP
// lifecycle events (no user recipient on the wire) fan out to every current
// site-admin with the matching template kind.
func TestSSOConsumer_DomainEventsFanOutToSiteAdmins(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantKind string
	}{
		{"domain_verification_requested", `{"event":"sso.domain_verification_requested","vars":{"domain":"example.org"}}`, "domain-verification-instructions"},
		{"domain_verified", `{"event":"sso.domain_verified","vars":{"domain":"example.org"}}`, "domain-verified"},
		{"idp_test_failed", `{"event":"sso.idp_test_failed","vars":{"connectionId":"conn-1","detail":"timeout"}}`, "idp-test-failed"},
		{"activated", `{"event":"sso.activated","vars":{"domain":"example.org"}}`, "sso-activated"},
		{"disabled", `{"event":"sso.disabled","vars":{"domain":"example.org"}}`, "sso-disabled"},
		{"sp_cert_rotated", `{"event":"sso.sp_cert_rotated","vars":{"serial":"abc123"}}`, "sp-cert-rotated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeSender{}
			c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{"admin1@example.org", "admin2@example.org"})
			err := c.Handle(context.Background(), []byte(tc.body))
			require.NoError(t, err)
			require.ElementsMatch(t, []string{"admin1@example.org", "admin2@example.org"}, sender.sentTo)
			require.Equal(t, tc.wantKind, sender.lastKind)
		})
	}
}

// TestSSOConsumer_UnknownEventIsNoOp verifies an event name outside the known
// set is logged and skipped -- no error, no panic, no send.
func TestSSOConsumer_UnknownEventIsNoOp(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{})
	err := c.Handle(context.Background(), []byte(`{"event":"sso.something_unrecognized","vars":{}}`))
	require.NoError(t, err)
	require.Empty(t, sender.sentTo)
}

// TestSSOConsumer_MalformedBodyErrors verifies a genuinely malformed message
// body (not just an unknown event name) returns an error so the broker can
// requeue/dead-letter it rather than silently dropping it.
func TestSSOConsumer_MalformedBodyErrors(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewSSOLifecycleConsumer(sender, fakeUsers{}, fakeSiteAdmins{})
	err := c.Handle(context.Background(), []byte(`not-json`))
	require.Error(t, err)
}
