// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
	"github.com/Steward-GRC/steward-obligations/internal/inbox"
	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// InboxBackend is the narrow seam the Inbox handler depends on; *inbox.Service
// satisfies it in production.
type InboxBackend interface {
	List(ctx context.Context, userID string, f inbox.Filter, pageSize int, token string) (inbox.Page, error)
	CountUnread(ctx context.Context, userID string) (int64, error)
	MarkRead(ctx context.Context, userID, id string) (inbox.Notice, int64, error)
	MarkAllRead(ctx context.Context, userID string) (int64, error)
}

// InboxHandler implements obligationsv1.InboxServiceServer. The user is always
// the forwarded actor: no request names one, so a caller only reaches its own
// notices. The payloads are never logged; only ids are.
type InboxHandler struct {
	obligationsv1.UnimplementedInboxServiceServer
	svc InboxBackend
}

// NewInboxHandler returns a handler over the inbox service.
func NewInboxHandler(svc InboxBackend) *InboxHandler { return &InboxHandler{svc: svc} }

// ListNotices returns a page of the actor's notices, newest first.
func (h *InboxHandler) ListNotices(ctx context.Context, req *obligationsv1.ListNoticesRequest) (*obligationsv1.ListNoticesResponse, error) {
	userID, ok := actorID(ctx)
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.InboxAuthRequired())
	}
	f, ok := inboxFilterFromProto(req.GetFilter())
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.InboxPageInvalid(inbox.ErrInvalidToken))
	}
	page, err := h.svc.List(ctx, userID, f, int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		return nil, inboxErr(ctx, "inbox_list", err)
	}
	out := &obligationsv1.ListNoticesResponse{Notices: make([]*obligationsv1.Notice, 0, len(page.Notices)), NextPageToken: page.NextToken}
	for _, n := range page.Notices {
		pn, err := noticeToProto(n)
		if err != nil {
			lg := logctx.From(ctx)
			lg.Error().Err(err).Str("user_id", userID).Str("notice_id", n.ID).Msg("inbox list: notice payload not representable")
			return nil, storeUnavailable(ctx, "inbox_list", err)
		}
		out.Notices = append(out.Notices, pn)
	}
	return out, nil
}

// CountUnread returns the actor's unread count.
func (h *InboxHandler) CountUnread(ctx context.Context, _ *obligationsv1.CountUnreadRequest) (*obligationsv1.CountUnreadResponse, error) {
	userID, ok := actorID(ctx)
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.InboxAuthRequired())
	}
	n, err := h.svc.CountUnread(ctx, userID)
	if err != nil {
		return nil, inboxErr(ctx, "inbox_count", err)
	}
	return &obligationsv1.CountUnreadResponse{Unread: n}, nil
}

// MarkRead marks one of the actor's notices read.
func (h *InboxHandler) MarkRead(ctx context.Context, req *obligationsv1.MarkReadRequest) (*obligationsv1.MarkReadResponse, error) {
	userID, ok := actorID(ctx)
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.InboxAuthRequired())
	}
	n, unread, err := h.svc.MarkRead(ctx, userID, req.GetId())
	if err != nil {
		return nil, inboxErr(ctx, "inbox_mark_read", err)
	}
	pn, err := noticeToProto(n)
	if err != nil {
		lg := logctx.From(ctx)
		lg.Error().Err(err).Str("user_id", userID).Str("notice_id", n.ID).Msg("inbox mark read: notice payload not representable")
		return nil, storeUnavailable(ctx, "inbox_mark_read", err)
	}
	return &obligationsv1.MarkReadResponse{Notice: pn, Unread: unread}, nil
}

// MarkAllRead marks every unread notice of the actor read.
func (h *InboxHandler) MarkAllRead(ctx context.Context, _ *obligationsv1.MarkAllReadRequest) (*obligationsv1.MarkAllReadResponse, error) {
	userID, ok := actorID(ctx)
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.InboxAuthRequired())
	}
	n, err := h.svc.MarkAllRead(ctx, userID)
	if err != nil {
		return nil, inboxErr(ctx, "inbox_mark_all_read", err)
	}
	return &obligationsv1.MarkAllReadResponse{Marked: n}, nil
}

// inboxErr turns an inbox error into its coded status; anything else is a
// store fault.
func inboxErr(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, inbox.ErrNoUser):
		return errcodes.Error(ctx, errcodes.InboxAuthRequired())
	case errors.Is(err, inbox.ErrNotFound):
		return errcodes.Error(ctx, errcodes.InboxNoticeNotFound(err))
	case errors.Is(err, inbox.ErrInvalidToken):
		return errcodes.Error(ctx, errcodes.InboxPageInvalid(err))
	default:
		return storeUnavailable(ctx, op, err)
	}
}

func inboxFilterFromProto(f obligationsv1.InboxFilter) (inbox.Filter, bool) {
	switch f {
	case obligationsv1.InboxFilter_INBOX_FILTER_UNSPECIFIED, obligationsv1.InboxFilter_INBOX_FILTER_ALL:
		return inbox.FilterAll, true
	case obligationsv1.InboxFilter_INBOX_FILTER_NEEDS_ACTION:
		return inbox.FilterNeedsAction, true
	case obligationsv1.InboxFilter_INBOX_FILTER_UPDATES:
		return inbox.FilterUpdates, true
	default:
		return inbox.FilterAll, false
	}
}

func noticeToProto(n inbox.Notice) (*obligationsv1.Notice, error) {
	payload, err := structpb.NewStruct(n.Payload)
	if err != nil {
		return nil, err
	}
	out := &obligationsv1.Notice{
		Id:          n.ID,
		Kind:        n.Kind,
		Category:    categoryToProto(n.Category),
		NeedsAction: n.NeedsAction,
		Payload:     payload,
		CreatedAt:   timestamppb.New(n.CreatedAt),
	}
	if n.ReadAt != nil {
		out.ReadAt = timestamppb.New(*n.ReadAt)
	}
	return out, nil
}
