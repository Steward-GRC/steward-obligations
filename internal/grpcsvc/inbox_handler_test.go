// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/inbox"
)

// memInbox is an in-memory inbox store scoped per user, like the real one.
type memInbox struct {
	mu   sync.Mutex
	rows map[string][]inbox.Row
}

func (m *memInbox) List(_ context.Context, q inbox.Query) ([]inbox.Row, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []inbox.Row
	for _, r := range m.rows[q.UserID] {
		if len(q.Types) > 0 && !has(q.Types, r.Type) || has(q.ExcludeTypes, r.Type) {
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

func (m *memInbox) CountUnread(_ context.Context, u string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, r := range m.rows[u] {
		if r.ReadAt == nil {
			n++
		}
	}
	return n, nil
}

func (m *memInbox) MarkRead(_ context.Context, u, id string) (inbox.Row, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rows[u] {
		if r.ID == id {
			if r.ReadAt == nil {
				now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
				m.rows[u][i].ReadAt = &now
			}
			return m.rows[u][i], true, nil
		}
	}
	return inbox.Row{}, false, nil
}

func (m *memInbox) MarkAllRead(_ context.Context, u string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for i, r := range m.rows[u] {
		if r.ReadAt == nil {
			now := time.Now()
			m.rows[u][i].ReadAt = &now
			n++
		}
	}
	return n, nil
}

func has(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func notice(id, typ string, minsAgo int) inbox.Row {
	return inbox.Row{ID: id, Type: typ, Payload: map[string]any{"policy_version_id": "pv-" + id, "campaign_id": "c1"},
		CreatedAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Add(-time.Duration(minsAgo) * time.Minute)}
}

func newMemInbox() *memInbox {
	return &memInbox{rows: map[string][]inbox.Row{
		"u-erin": {notice("n1", "ack_reminder", 1), notice("n2", "policy-published", 2), notice("n3", "workflow-awaiting-approval", 3)},
		"u-sam":  {notice("s1", "ack_reminder", 1)},
	}}
}

// startInbox serves the InboxService with the given actor (empty for none).
func startInbox(t *testing.T, store inbox.Store, actor string) obligationsv1.InboxServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	var opts []grpc.ServerOption
	if actor != "" {
		opts = append(opts, grpc.UnaryInterceptor(actorInterceptor(actor)))
	}
	s := grpc.NewServer(opts...)
	obligationsv1.RegisterInboxServiceServer(s, grpcsvc.NewInboxHandler(inbox.NewService(store)))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewInboxServiceClient(conn)
}

func noticeIDs(ns []*obligationsv1.Notice) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.GetId())
	}
	return out
}

func symbolOf(t *testing.T, err error) string {
	t.Helper()
	info, ok := apperrgrpc.FromStatus(status.Convert(err))
	require.True(t, ok, "status must carry ErrorInfo")
	return info.Symbol
}

func TestListNoticesReturnsTheActorsNoticesWithTheDerivedFields(t *testing.T) {
	c := startInbox(t, newMemInbox(), "u-erin")
	resp, err := c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"n1", "n2", "n3"}, noticeIDs(resp.Notices))
	n := resp.Notices[0]
	require.Equal(t, "policy-ack-reminder", n.Kind)
	require.Equal(t, obligationsv1.NotifCategory_NOTIF_CATEGORY_COMPLIANCE, n.Category)
	require.True(t, n.NeedsAction)
	require.Equal(t, "pv-n1", n.Payload.AsMap()["policy_version_id"])
	require.True(t, n.CreatedAt.IsValid())
	require.Nil(t, n.ReadAt)
	require.False(t, resp.Notices[1].NeedsAction)
	require.Equal(t, obligationsv1.NotifCategory_NOTIF_CATEGORY_INFORMATIONAL, resp.Notices[1].Category)
	require.Empty(t, resp.NextPageToken)
}

func TestListNoticesFiltersAndPages(t *testing.T) {
	c := startInbox(t, newMemInbox(), "u-erin")
	na, err := c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{Filter: obligationsv1.InboxFilter_INBOX_FILTER_NEEDS_ACTION})
	require.NoError(t, err)
	require.Equal(t, []string{"n1", "n3"}, noticeIDs(na.Notices))
	up, err := c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{Filter: obligationsv1.InboxFilter_INBOX_FILTER_UPDATES})
	require.NoError(t, err)
	require.Equal(t, []string{"n2"}, noticeIDs(up.Notices))

	p1, err := c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"n1", "n2"}, noticeIDs(p1.Notices))
	require.NotEmpty(t, p1.NextPageToken)
	p2, err := c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{PageSize: 2, PageToken: p1.NextPageToken})
	require.NoError(t, err)
	require.Equal(t, []string{"n3"}, noticeIDs(p2.Notices))
	require.Empty(t, p2.NextPageToken)
}

func TestListNoticesRefusesABadTokenAndAnUnknownFilter(t *testing.T) {
	c := startInbox(t, newMemInbox(), "u-erin")
	_, err := c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{PageToken: "garbage"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "INBOX_PAGE_INVALID", symbolOf(t, err))
	_, err = c.ListNotices(context.Background(), &obligationsv1.ListNoticesRequest{Filter: 99})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestEveryInboxMethodNeedsAForwardedActor(t *testing.T) {
	c := startInbox(t, newMemInbox(), "")
	ctx := context.Background()
	_, err := c.ListNotices(ctx, &obligationsv1.ListNoticesRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Equal(t, "INBOX_AUTH_REQUIRED", symbolOf(t, err))
	_, err = c.CountUnread(ctx, &obligationsv1.CountUnreadRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = c.MarkRead(ctx, &obligationsv1.MarkReadRequest{Id: "n1"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = c.MarkAllRead(ctx, &obligationsv1.MarkAllReadRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestCountUnreadIsTheActorsOwn(t *testing.T) {
	store := newMemInbox()
	erin := startInbox(t, store, "u-erin")
	sam := startInbox(t, store, "u-sam")
	e, err := erin.CountUnread(context.Background(), &obligationsv1.CountUnreadRequest{})
	require.NoError(t, err)
	require.EqualValues(t, 3, e.Unread)
	s, err := sam.CountUnread(context.Background(), &obligationsv1.CountUnreadRequest{})
	require.NoError(t, err)
	require.EqualValues(t, 1, s.Unread)
}

func TestMarkReadMarksTheActorsNoticeAndReturnsTheNewCount(t *testing.T) {
	c := startInbox(t, newMemInbox(), "u-erin")
	resp, err := c.MarkRead(context.Background(), &obligationsv1.MarkReadRequest{Id: "n1"})
	require.NoError(t, err)
	require.Equal(t, "n1", resp.Notice.Id)
	require.True(t, resp.Notice.ReadAt.IsValid())
	require.EqualValues(t, 2, resp.Unread)
	again, err := c.MarkRead(context.Background(), &obligationsv1.MarkReadRequest{Id: "n1"})
	require.NoError(t, err)
	require.True(t, resp.Notice.ReadAt.AsTime().Equal(again.Notice.ReadAt.AsTime()))
}

func TestMarkReadOfSomeoneElsesNoticeIsNotFoundAndLeavesItUnread(t *testing.T) {
	store := newMemInbox()
	erin := startInbox(t, store, "u-erin")
	_, err := erin.MarkRead(context.Background(), &obligationsv1.MarkReadRequest{Id: "s1"}) // Sam's
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Equal(t, "INBOX_NOTICE_NOT_FOUND", symbolOf(t, err))
	_, err = erin.MarkRead(context.Background(), &obligationsv1.MarkReadRequest{Id: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Nil(t, store.rows["u-sam"][0].ReadAt)
}

func TestMarkAllReadMarksOnlyTheActorsNotices(t *testing.T) {
	store := newMemInbox()
	erin := startInbox(t, store, "u-erin")
	resp, err := erin.MarkAllRead(context.Background(), &obligationsv1.MarkAllReadRequest{})
	require.NoError(t, err)
	require.EqualValues(t, 3, resp.Marked)
	require.Nil(t, store.rows["u-sam"][0].ReadAt)
	resp, err = erin.MarkAllRead(context.Background(), &obligationsv1.MarkAllReadRequest{})
	require.NoError(t, err)
	require.Zero(t, resp.Marked)
}
