// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/consumer"
)

type fakeComplianceAdmins struct {
	emails []string
	err    error
}

func (f fakeComplianceAdmins) ListComplianceAdminEmails(context.Context) ([]string, error) {
	return f.emails, f.err
}

const breakGlassReadBody = `{"event_type":"policy.break_glass_read","event_id":"ev-1","read_at":"2026-08-05T15:14:00Z",
"policy_id":"p1","policy_version_id":"v1","number":"POL-HR-000042","title":"Sensitive Matter","document_type":"policy",
"owner_user_id":"owner","reader_user_id":"reader","act_as_admin_user_id":"admin"}`

func breakGlassUsers() fakeUsers {
	return fakeUsers{"owner": "owner@example.org", "reader": "reader@example.org", "admin": "admin@example.org"}
}

func TestBreakGlassReadAlertsTheOwnerAndEveryComplianceAdmin(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewBreakGlassReadConsumer(sender, breakGlassUsers(),
		fakeComplianceAdmins{emails: []string{"grace@example.org", "owner@example.org"}})

	require.NoError(t, c.Handle(context.Background(), []byte(breakGlassReadBody)))
	require.ElementsMatch(t, []string{"owner@example.org", "grace@example.org"}, sender.sentTo)
	require.Equal(t, "break-glass-read-alert", sender.lastKind)
	require.ElementsMatch(t, []string{"break-glass-read:ev-1:owner@example.org", "break-glass-read:ev-1:grace@example.org"}, sender.dedupRefs)

	vars, ok := sender.lastVars.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "POL-HR-000042", vars["number"])
	require.Equal(t, "Sensitive Matter", vars["title"])
	require.Equal(t, "reader@example.org", vars["reader"])
	require.Equal(t, "admin@example.org", vars["actAsAdmin"])
	require.Equal(t, "August 5, 2026 at 3:14 PM UTC", vars["at"])
}

func TestBreakGlassReadWithoutActAsOmitsTheAdmin(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewBreakGlassReadConsumer(sender, breakGlassUsers(), fakeComplianceAdmins{})
	body := `{"event_type":"policy.break_glass_read","event_id":"ev-2","read_at":"2026-08-05T15:14:00Z",
"policy_id":"p1","number":"POL-HR-000042","title":"T","owner_user_id":"owner","reader_user_id":"reader"}`

	require.NoError(t, c.Handle(context.Background(), []byte(body)))
	require.Equal(t, []string{"owner@example.org"}, sender.sentTo)
	_, has := sender.lastVars.(map[string]any)["actAsAdmin"]
	require.False(t, has)
}

func TestBreakGlassReadEachReadIsItsOwnAlert(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewBreakGlassReadConsumer(sender, breakGlassUsers(), fakeComplianceAdmins{})
	second := `{"event_type":"policy.break_glass_read","event_id":"ev-3","read_at":"2026-08-05T15:20:00Z",
"policy_id":"p1","number":"POL-HR-000042","title":"T","owner_user_id":"owner","reader_user_id":"reader"}`

	require.NoError(t, c.Handle(context.Background(), []byte(breakGlassReadBody)))
	require.NoError(t, c.Handle(context.Background(), []byte(second)))
	require.Len(t, sender.dedupRefs, 2)
	require.NotEqual(t, sender.dedupRefs[0], sender.dedupRefs[1])
}

func TestBreakGlassReadComplianceListFailureStillAlertsTheOwnerAndRetries(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewBreakGlassReadConsumer(sender, breakGlassUsers(), fakeComplianceAdmins{err: errors.New("identity down")})

	require.Error(t, c.Handle(context.Background(), []byte(breakGlassReadBody)))
	require.Equal(t, []string{"owner@example.org"}, sender.sentTo)
}

func TestBreakGlassReadRejectsAnEventWithoutIDs(t *testing.T) {
	c := consumer.NewBreakGlassReadConsumer(&fakeSender{}, breakGlassUsers(), fakeComplianceAdmins{})
	require.Error(t, c.Handle(context.Background(), []byte(`{"event_type":"policy.break_glass_read","policy_id":"p1"}`)))
	require.Error(t, c.Handle(context.Background(), []byte(`not json`)))
}

type failingUsers struct{ fakeUsers }

func (f failingUsers) ResolveEmail(ctx context.Context, userID string) (string, error) {
	if userID == "owner" {
		return "", errors.New("identity down")
	}
	return f.fakeUsers.ResolveEmail(ctx, userID)
}

func TestBreakGlassReadOwnerLookupFailureAlertsComplianceAndRetries(t *testing.T) {
	sender := &fakeSender{}
	c := consumer.NewBreakGlassReadConsumer(sender, failingUsers{breakGlassUsers()},
		fakeComplianceAdmins{emails: []string{"grace@example.org"}})

	require.Error(t, c.Handle(context.Background(), []byte(breakGlassReadBody)))
	require.Equal(t, []string{"grace@example.org"}, sender.sentTo)
}
