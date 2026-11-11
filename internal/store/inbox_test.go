// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"testing"
	"time"

	pg "github.com/Bugs5382/go-postgres"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/inbox"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

const (
	inboxAlice = "cccccccc-0000-0000-0000-00000000000a"
	inboxBob   = "cccccccc-0000-0000-0000-00000000000b"
)

// addNotice inserts one notice with a fixed created_at so ordering is exact.
func addNotice(t *testing.T, pool *pg.DB, s *store.NotificationStore, user, typ, channel string, minutesAgo int) string {
	t.Helper()
	n, err := s.Insert(context.Background(), store.Notification{
		UserID: user, Type: typ, Channel: channel, Payload: map[string]any{"policy_version_id": "pv-1"},
	})
	require.NoError(t, err)
	_, err = pool.Querier().Exec(context.Background(),
		`UPDATE notifications SET created_at = now() - make_interval(mins => $2) WHERE id = $1`, n.ID, minutesAgo)
	require.NoError(t, err)
	return n.ID
}

func TestInboxListIsScopedToTheUserAndTheInAppChannelNewestFirst(t *testing.T) {
	pool := newTestDB(t)
	ns := store.NewNotificationStore(pool)
	in := store.NewInboxStore(pool)
	ctx := context.Background()
	old := addNotice(t, pool, ns, inboxAlice, "ack_reminder", "in_app", 10)
	recent := addNotice(t, pool, ns, inboxAlice, "policy-published", "in_app", 1)
	addNotice(t, pool, ns, inboxAlice, "ack_reminder", "email", 2) // not an inbox notice
	addNotice(t, pool, ns, inboxBob, "ack_reminder", "in_app", 3)  // someone else's

	rows, err := in.List(ctx, inbox.Query{UserID: inboxAlice, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, recent, rows[0].ID)
	require.Equal(t, old, rows[1].ID)
	require.Equal(t, "ack_reminder", rows[1].Type)
	require.Equal(t, "pv-1", rows[1].Payload["policy_version_id"])
	require.Nil(t, rows[0].ReadAt)
}

func TestInboxListFiltersByTypeAndPagesByCursor(t *testing.T) {
	pool := newTestDB(t)
	ns := store.NewNotificationStore(pool)
	in := store.NewInboxStore(pool)
	ctx := context.Background()
	a := addNotice(t, pool, ns, inboxAlice, "ack_reminder", "in_app", 1)
	b := addNotice(t, pool, ns, inboxAlice, "policy-published", "in_app", 2)
	c := addNotice(t, pool, ns, inboxAlice, "ack_reminder", "in_app", 3)

	only, err := in.List(ctx, inbox.Query{UserID: inboxAlice, Types: []string{"ack_reminder"}, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{a, c}, rowIDs(only))
	without, err := in.List(ctx, inbox.Query{UserID: inboxAlice, ExcludeTypes: []string{"ack_reminder"}, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{b}, rowIDs(without))

	first, err := in.List(ctx, inbox.Query{UserID: inboxAlice, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{a, b}, rowIDs(first))
	rest, err := in.List(ctx, inbox.Query{UserID: inboxAlice, Limit: 2,
		After: &inbox.Cursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}})
	require.NoError(t, err)
	require.Equal(t, []string{c}, rowIDs(rest))
}

func TestInboxCursorWithoutAUUIDIsAnInvalidToken(t *testing.T) {
	pool := newTestDB(t)
	in := store.NewInboxStore(pool)
	_, err := in.List(context.Background(), inbox.Query{UserID: inboxAlice, Limit: 2,
		After: &inbox.Cursor{CreatedAt: time.Now(), ID: "not-a-uuid"}})
	require.ErrorIs(t, err, inbox.ErrInvalidToken)
}

func TestInboxCountAndMarkReadStayWithTheUser(t *testing.T) {
	pool := newTestDB(t)
	ns := store.NewNotificationStore(pool)
	in := store.NewInboxStore(pool)
	ctx := context.Background()
	a1 := addNotice(t, pool, ns, inboxAlice, "ack_reminder", "in_app", 1)
	addNotice(t, pool, ns, inboxAlice, "policy-published", "in_app", 2)
	addNotice(t, pool, ns, inboxAlice, "ack_reminder", "email", 2)
	b1 := addNotice(t, pool, ns, inboxBob, "ack_reminder", "in_app", 3)

	n, err := in.CountUnread(ctx, inboxAlice)
	require.NoError(t, err)
	require.EqualValues(t, 2, n, "the email row is not an inbox notice")

	row, found, err := in.MarkRead(ctx, inboxAlice, a1)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, row.ReadAt)
	first := *row.ReadAt
	again, found, err := in.MarkRead(ctx, inboxAlice, a1)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, first.Equal(*again.ReadAt), "the first read time is kept")

	// Bob's notice is not Alice's to read.
	_, found, err = in.MarkRead(ctx, inboxAlice, b1)
	require.NoError(t, err)
	require.False(t, found)
	bobN, err := in.CountUnread(ctx, inboxBob)
	require.NoError(t, err)
	require.EqualValues(t, 1, bobN)

	// Neither a missing id nor a non-UUID user or id errors.
	_, found, err = in.MarkRead(ctx, inboxAlice, "cccccccc-0000-0000-0000-0000000000ff")
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = in.MarkRead(ctx, inboxAlice, "not-a-uuid")
	require.NoError(t, err)
	require.False(t, found)
	n, err = in.CountUnread(ctx, "not-a-uuid")
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestInboxMarkAllReadMarksOnlyTheUsersUnreadInAppNotices(t *testing.T) {
	pool := newTestDB(t)
	ns := store.NewNotificationStore(pool)
	in := store.NewInboxStore(pool)
	ctx := context.Background()
	a1 := addNotice(t, pool, ns, inboxAlice, "ack_reminder", "in_app", 1)
	addNotice(t, pool, ns, inboxAlice, "policy-published", "in_app", 2)
	addNotice(t, pool, ns, inboxAlice, "ack_reminder", "email", 2)
	addNotice(t, pool, ns, inboxBob, "ack_reminder", "in_app", 3)
	_, _, err := in.MarkRead(ctx, inboxAlice, a1)
	require.NoError(t, err)

	marked, err := in.MarkAllRead(ctx, inboxAlice)
	require.NoError(t, err)
	require.EqualValues(t, 1, marked, "only the one still unread")
	n, err := in.CountUnread(ctx, inboxAlice)
	require.NoError(t, err)
	require.Zero(t, n)
	bobN, err := in.CountUnread(ctx, inboxBob)
	require.NoError(t, err)
	require.EqualValues(t, 1, bobN)
	var emailRead *time.Time
	require.NoError(t, pool.Querier().QueryRow(ctx,
		`SELECT read_at FROM notifications WHERE user_id=$1 AND channel='email'`, inboxAlice).Scan(&emailRead))
	require.Nil(t, emailRead, "a non-inbox row is left alone")

	marked, err = in.MarkAllRead(ctx, "not-a-uuid")
	require.NoError(t, err)
	require.Zero(t, marked)
}

func rowIDs(rows []inbox.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}
