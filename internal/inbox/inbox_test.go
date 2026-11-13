// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/inbox"
	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
)

func TestDescribeMapsTheLegacyAckReminderToItsTaxonomyKind(t *testing.T) {
	d := inbox.Describe("ack_reminder")
	require.Equal(t, "policy-ack-reminder", d.Kind)
	require.Equal(t, notifpolicy.CategoryCompliance, d.Category)
	require.True(t, d.NeedsAction)
}

func TestDescribeDerivesCategoryAndNeedsActionFromTheTaxonomy(t *testing.T) {
	cases := []struct {
		typ    string
		cat    notifpolicy.Category
		action bool
	}{
		{"ack-required", notifpolicy.CategoryCompliance, true},
		{"policy-escalation", notifpolicy.CategoryCompliance, true},
		{"review-due", notifpolicy.CategoryCompliance, true},
		{"workflow-awaiting-approval", notifpolicy.CategoryWorkflow, true},
		{"workflow-assigned", notifpolicy.CategoryWorkflow, true},
		{"assigned-as-owner", notifpolicy.CategoryWorkflow, true},
		{"workflow-denied", notifpolicy.CategoryWorkflow, false},
		{"workflow-reminder", notifpolicy.CategoryWorkflow, true},
		{"workflow-escalated", notifpolicy.CategoryWorkflow, false},
		{"workflow-started", notifpolicy.CategoryWorkflow, false},
		{"policy-published", notifpolicy.CategoryInformational, false},
		{"break-glass-read-alert", notifpolicy.CategorySecurity, false},
	}
	for _, c := range cases {
		d := inbox.Describe(c.typ)
		require.Equal(t, c.typ, d.Kind)
		require.Equal(t, c.cat, d.Category, c.typ)
		require.Equal(t, c.action, d.NeedsAction, c.typ)
	}
}

func TestDescribeTreatsAnUnknownTypeAsAnInformationalUpdate(t *testing.T) {
	d := inbox.Describe("something-new")
	require.Equal(t, "something-new", d.Kind)
	require.Equal(t, notifpolicy.CategoryInformational, d.Category)
	require.False(t, d.NeedsAction)
}

func TestNeedsActionTypesAreExactlyTheKindsDescribeCallsNeedsAction(t *testing.T) {
	set := map[string]bool{}
	for _, ty := range inbox.NeedsActionTypes() {
		set[ty] = true
		require.True(t, inbox.Describe(ty).NeedsAction, ty)
	}
	require.True(t, set["ack_reminder"], "the stored legacy type is included")
	for _, c := range notifpolicy.AllClasses() {
		require.Equal(t, inbox.Describe(c.Kind).NeedsAction, set[c.Kind], c.Kind)
	}
}

// fakeStore keeps rows per user, newest first, and honours the query like the
// real store: scoped to the user, type filters, keyset paging.
type fakeStore struct {
	rows  map[string][]inbox.Row
	calls []inbox.Query
}

func (f *fakeStore) List(_ context.Context, q inbox.Query) ([]inbox.Row, error) {
	f.calls = append(f.calls, q)
	var out []inbox.Row
	for _, r := range f.rows[q.UserID] {
		if len(q.Types) > 0 && !contains(q.Types, r.Type) {
			continue
		}
		if contains(q.ExcludeTypes, r.Type) {
			continue
		}
		if q.After != nil && !r.CreatedAt.Before(q.After.CreatedAt) && (!r.CreatedAt.Equal(q.After.CreatedAt) || r.ID >= q.After.ID) {
			continue
		}
		out = append(out, r)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) CountUnread(_ context.Context, user string) (int64, error) {
	var n int64
	for _, r := range f.rows[user] {
		if r.ReadAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) MarkRead(_ context.Context, user, id string) (inbox.Row, bool, error) {
	for i, r := range f.rows[user] {
		if r.ID == id {
			if r.ReadAt == nil {
				now := time.Now()
				f.rows[user][i].ReadAt = &now
			}
			return f.rows[user][i], true, nil
		}
	}
	return inbox.Row{}, false, nil
}

func (f *fakeStore) MarkAllRead(_ context.Context, user string) (int64, error) {
	var n int64
	for i, r := range f.rows[user] {
		if r.ReadAt == nil {
			now := time.Now()
			f.rows[user][i].ReadAt = &now
			n++
		}
	}
	return n, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func row(id, typ string, age int) inbox.Row {
	return inbox.Row{ID: id, Type: typ, Payload: map[string]any{"policy_version_id": "pv-" + id},
		CreatedAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Add(-time.Duration(age) * time.Minute)}
}

func seeded() (*fakeStore, *inbox.Service) {
	f := &fakeStore{rows: map[string][]inbox.Row{
		"u1": {row("a", "ack_reminder", 1), row("b", "policy-published", 2), row("c", "workflow-awaiting-approval", 3),
			row("d", "workflow-denied", 4), row("e", "policy-retired", 5)},
		"u2": {row("z", "ack_reminder", 1)},
	}}
	return f, inbox.NewService(f)
}

func ids(p inbox.Page) []string {
	var out []string
	for _, n := range p.Notices {
		out = append(out, n.ID)
	}
	return out
}

func TestListReturnsOnlyTheUsersNoticesNewestFirstWithDerivedFields(t *testing.T) {
	_, svc := seeded()
	p, err := svc.List(context.Background(), "u1", inbox.FilterAll, 0, "")
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c", "d", "e"}, ids(p))
	require.Empty(t, p.NextToken)
	require.Equal(t, "policy-ack-reminder", p.Notices[0].Kind)
	require.Equal(t, notifpolicy.CategoryCompliance, p.Notices[0].Category)
	require.True(t, p.Notices[0].NeedsAction)
	require.Equal(t, "pv-a", p.Notices[0].Payload["policy_version_id"])
	require.Nil(t, p.Notices[0].ReadAt)
}

func TestListFiltersNeedsActionAndUpdates(t *testing.T) {
	_, svc := seeded()
	na, err := svc.List(context.Background(), "u1", inbox.FilterNeedsAction, 0, "")
	require.NoError(t, err)
	require.Equal(t, []string{"a", "c"}, ids(na))
	up, err := svc.List(context.Background(), "u1", inbox.FilterUpdates, 0, "")
	require.NoError(t, err)
	require.Equal(t, []string{"b", "d", "e"}, ids(up))
}

func TestListPagesWithAnOpaqueTokenAndNoGapsOrRepeats(t *testing.T) {
	_, svc := seeded()
	p1, err := svc.List(context.Background(), "u1", inbox.FilterAll, 2, "")
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ids(p1))
	require.NotEmpty(t, p1.NextToken)
	p2, err := svc.List(context.Background(), "u1", inbox.FilterAll, 2, p1.NextToken)
	require.NoError(t, err)
	require.Equal(t, []string{"c", "d"}, ids(p2))
	p3, err := svc.List(context.Background(), "u1", inbox.FilterAll, 2, p2.NextToken)
	require.NoError(t, err)
	require.Equal(t, []string{"e"}, ids(p3))
	require.Empty(t, p3.NextToken)
}

func TestListRefusesAMalformedToken(t *testing.T) {
	_, svc := seeded()
	_, err := svc.List(context.Background(), "u1", inbox.FilterAll, 2, "not-a-token")
	require.ErrorIs(t, err, inbox.ErrInvalidToken)
}

func TestListClampsThePageSize(t *testing.T) {
	f, svc := seeded()
	_, err := svc.List(context.Background(), "u1", inbox.FilterAll, 0, "")
	require.NoError(t, err)
	_, err = svc.List(context.Background(), "u1", inbox.FilterAll, 5000, "")
	require.NoError(t, err)
	require.Equal(t, inbox.DefaultPageSize+1, f.calls[0].Limit, "one extra row tells whether a next page exists")
	require.Equal(t, inbox.MaxPageSize+1, f.calls[1].Limit)
}

func TestCountUnreadIsPerUser(t *testing.T) {
	_, svc := seeded()
	n, err := svc.CountUnread(context.Background(), "u1")
	require.NoError(t, err)
	require.EqualValues(t, 5, n)
	n, err = svc.CountUnread(context.Background(), "u2")
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func TestMarkReadMarksOneAndReportsTheNewCount(t *testing.T) {
	_, svc := seeded()
	got, unread, err := svc.MarkRead(context.Background(), "u1", "a")
	require.NoError(t, err)
	require.NotNil(t, got.ReadAt)
	require.EqualValues(t, 4, unread)
	// Idempotent: the first read time stays.
	again, unread, err := svc.MarkRead(context.Background(), "u1", "a")
	require.NoError(t, err)
	require.Equal(t, got.ReadAt, again.ReadAt)
	require.EqualValues(t, 4, unread)
}

func TestMarkReadOfSomeoneElsesOrMissingNoticeIsNotFound(t *testing.T) {
	f, svc := seeded()
	_, _, err := svc.MarkRead(context.Background(), "u1", "z") // u2's notice
	require.True(t, errors.Is(err, inbox.ErrNotFound))
	_, _, err = svc.MarkRead(context.Background(), "u1", "nope")
	require.True(t, errors.Is(err, inbox.ErrNotFound))
	require.Nil(t, f.rows["u2"][0].ReadAt, "u2's notice is untouched")
}

func TestMarkAllReadTouchesOnlyTheUsersNotices(t *testing.T) {
	f, svc := seeded()
	n, err := svc.MarkAllRead(context.Background(), "u1")
	require.NoError(t, err)
	require.EqualValues(t, 5, n)
	require.Nil(t, f.rows["u2"][0].ReadAt, "u2's notice is untouched")
	n, err = svc.MarkAllRead(context.Background(), "u1")
	require.NoError(t, err)
	require.EqualValues(t, 0, n)
}

func TestEveryMethodNeedsAUser(t *testing.T) {
	_, svc := seeded()
	_, err := svc.List(context.Background(), "", inbox.FilterAll, 0, "")
	require.ErrorIs(t, err, inbox.ErrNoUser)
	_, err = svc.CountUnread(context.Background(), "")
	require.ErrorIs(t, err, inbox.ErrNoUser)
	_, _, err = svc.MarkRead(context.Background(), "", "a")
	require.ErrorIs(t, err, inbox.ErrNoUser)
	_, err = svc.MarkAllRead(context.Background(), "")
	require.ErrorIs(t, err, inbox.ErrNoUser)
}
