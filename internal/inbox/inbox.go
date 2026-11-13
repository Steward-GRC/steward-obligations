// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package inbox is the rules of the signed-in user's in-app notification
// inbox: which notices a person sees, what category each is and whether it
// waits on them, paging, and marking read. It holds no transport or database
// types; the store and the gRPC handler plug in around it.
//
// Every method takes the user the call acts for and nothing else names a user,
// so a person can only list, count or mark their own notices.
package inbox

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
)

// Page sizes: a request for 0 gets DefaultPageSize, and none gets more than
// MaxPageSize.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

var (
	// ErrNoUser is a call with no user to act for.
	ErrNoUser = errors.New("inbox: no user")
	// ErrInvalidToken is a page token this service did not issue.
	ErrInvalidToken = errors.New("inbox: invalid page token")
	// ErrNotFound is a notice that does not exist for the user. Someone else's
	// notice is reported the same way, so its existence is not revealed.
	ErrNotFound = errors.New("inbox: notice not found")
)

// Filter narrows a listing to a tab of the bell.
type Filter int

const (
	// FilterAll lists every notice.
	FilterAll Filter = iota
	// FilterNeedsAction lists the notices that wait on the person.
	FilterNeedsAction
	// FilterUpdates lists every notice that does not need action.
	FilterUpdates
)

// legacyTypes maps a type the in-app channel stored before the notice kinds
// followed the taxonomy to its taxonomy kind. The in-app writer still stores
// "ack_reminder" for the recurring acknowledgement reminder.
var legacyTypes = map[string]string{"ack_reminder": "policy-ack-reminder"}

// needsActionKinds are the workflow kinds that wait on the person. Every
// compliance kind waits on them too (an acknowledgement, a review), so those
// follow from the category.
var needsActionKinds = map[string]bool{
	"workflow-awaiting-approval": true,
	"workflow-assigned":          true,
	"workflow-reminder":          true,
	"assigned-as-owner":          true,
}

// Description is what the inbox derives from a stored notice type.
type Description struct {
	Kind        string
	Category    notifpolicy.Category
	NeedsAction bool
}

// Describe gives the taxonomy kind, category and needs-action flag of a stored
// notice type. A type the taxonomy does not know is an informational update, so
// a newly added kind is listed rather than dropped.
func Describe(storedType string) Description {
	kind := storedType
	if k, ok := legacyTypes[storedType]; ok {
		kind = k
	}
	c, ok := notifpolicy.Classify(kind)
	if !ok {
		return Description{Kind: kind, Category: notifpolicy.CategoryInformational}
	}
	return Description{
		Kind:        kind,
		Category:    c.Category,
		NeedsAction: c.Category == notifpolicy.CategoryCompliance || needsActionKinds[kind],
	}
}

// NeedsActionTypes lists every stored type that Describe says needs action:
// the taxonomy kinds and the legacy stored types. The store filters on it.
func NeedsActionTypes() []string {
	var out []string
	for _, c := range notifpolicy.AllClasses() {
		if Describe(c.Kind).NeedsAction {
			out = append(out, c.Kind)
		}
	}
	for legacy := range legacyTypes {
		if Describe(legacy).NeedsAction {
			out = append(out, legacy)
		}
	}
	return out
}

// Row is a stored notice as the store hands it over.
type Row struct {
	ID        string
	Type      string
	Payload   map[string]any
	CreatedAt time.Time
	ReadAt    *time.Time
}

// Cursor is the position after the last notice of a page.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Query is one listing: a user's notices, optionally limited to some stored
// types or without some, newest first, strictly after a cursor.
type Query struct {
	UserID       string
	Types        []string
	ExcludeTypes []string
	After        *Cursor
	Limit        int
}

// Store is what the inbox needs from persistence. Every method is scoped to
// the user it is given.
type Store interface {
	List(ctx context.Context, q Query) ([]Row, error)
	CountUnread(ctx context.Context, userID string) (int64, error)
	// MarkRead sets read_at on the user's notice when it is unset and returns
	// the notice, or false when the user has no such notice.
	MarkRead(ctx context.Context, userID, id string) (Row, bool, error)
	// MarkAllRead marks the user's unread notices and returns how many.
	MarkAllRead(ctx context.Context, userID string) (int64, error)
}

// Notice is a stored notice with what the inbox derives from its type.
type Notice struct {
	ID          string
	Kind        string
	Category    notifpolicy.Category
	NeedsAction bool
	Payload     map[string]any
	CreatedAt   time.Time
	ReadAt      *time.Time
}

// Page is one page of a listing; NextToken is empty on the last page.
type Page struct {
	Notices   []Notice
	NextToken string
}

// Service is the inbox.
type Service struct{ store Store }

// NewService returns an inbox over store.
func NewService(store Store) *Service { return &Service{store: store} }

func toNotice(r Row) Notice {
	d := Describe(r.Type)
	return Notice{ID: r.ID, Kind: d.Kind, Category: d.Category, NeedsAction: d.NeedsAction,
		Payload: r.Payload, CreatedAt: r.CreatedAt, ReadAt: r.ReadAt}
}

// List returns one page of the user's notices, newest first. pageSize 0 means
// DefaultPageSize; token is the previous page's NextToken.
func (s *Service) List(ctx context.Context, userID string, f Filter, pageSize int, token string) (Page, error) {
	lg := logctx.From(ctx)
	if userID == "" {
		return Page{}, ErrNoUser
	}
	size := pageSize
	switch {
	case size <= 0:
		size = DefaultPageSize
	case size > MaxPageSize:
		size = MaxPageSize
	}
	q := Query{UserID: userID, Limit: size + 1}
	switch f {
	case FilterNeedsAction:
		q.Types = NeedsActionTypes()
	case FilterUpdates:
		q.ExcludeTypes = NeedsActionTypes()
	}
	if token != "" {
		cur, err := decodeCursor(token)
		if err != nil {
			lg.Debug().Str("user_id", userID).Msg("inbox list: page token refused")
			return Page{}, err
		}
		q.After = &cur
	}
	lg.Trace().Str("user_id", userID).Int("filter", int(f)).Int("page_size", size).Bool("has_token", token != "").Msg("inbox list")
	rows, err := s.store.List(ctx, q)
	if errors.Is(err, ErrInvalidToken) {
		return Page{}, err
	}
	if err != nil {
		lg.Error().Err(err).Str("user_id", userID).Msg("inbox list: store failed")
		return Page{}, fmt.Errorf("inbox list: %w", err)
	}
	var next string
	if len(rows) > size {
		rows = rows[:size]
		last := rows[size-1]
		next = encodeCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	page := Page{Notices: make([]Notice, 0, len(rows)), NextToken: next}
	for _, r := range rows {
		page.Notices = append(page.Notices, toNotice(r))
	}
	lg.Debug().Str("user_id", userID).Int("returned", len(page.Notices)).Bool("more", next != "").Msg("inbox list done")
	return page, nil
}

// CountUnread returns how many of the user's notices are unread.
func (s *Service) CountUnread(ctx context.Context, userID string) (int64, error) {
	lg := logctx.From(ctx)
	if userID == "" {
		return 0, ErrNoUser
	}
	n, err := s.store.CountUnread(ctx, userID)
	if err != nil {
		lg.Error().Err(err).Str("user_id", userID).Msg("inbox count: store failed")
		return 0, fmt.Errorf("inbox count: %w", err)
	}
	lg.Trace().Str("user_id", userID).Int64("unread", n).Msg("inbox count")
	return n, nil
}

// MarkRead marks one of the user's notices read and returns it with the user's
// unread count after the change. A notice already read keeps its first read
// time. A notice that is missing or someone else's is ErrNotFound.
func (s *Service) MarkRead(ctx context.Context, userID, id string) (Notice, int64, error) {
	lg := logctx.From(ctx)
	if userID == "" {
		return Notice{}, 0, ErrNoUser
	}
	row, found, err := s.store.MarkRead(ctx, userID, id)
	if err != nil {
		lg.Error().Err(err).Str("user_id", userID).Str("notice_id", id).Msg("inbox mark read: store failed")
		return Notice{}, 0, fmt.Errorf("inbox mark read: %w", err)
	}
	if !found {
		lg.Debug().Str("user_id", userID).Str("notice_id", id).Msg("inbox mark read: no such notice for the user")
		return Notice{}, 0, ErrNotFound
	}
	unread, err := s.store.CountUnread(ctx, userID)
	if err != nil {
		lg.Error().Err(err).Str("user_id", userID).Msg("inbox mark read: count failed")
		return Notice{}, 0, fmt.Errorf("inbox count: %w", err)
	}
	lg.Debug().Str("user_id", userID).Str("notice_id", id).Int64("unread", unread).Msg("inbox notice read")
	return toNotice(row), unread, nil
}

// MarkAllRead marks every unread notice of the user read and returns how many.
func (s *Service) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	lg := logctx.From(ctx)
	if userID == "" {
		return 0, ErrNoUser
	}
	n, err := s.store.MarkAllRead(ctx, userID)
	if err != nil {
		lg.Error().Err(err).Str("user_id", userID).Msg("inbox mark all read: store failed")
		return 0, fmt.Errorf("inbox mark all read: %w", err)
	}
	lg.Debug().Str("user_id", userID).Int64("marked", n).Msg("inbox all read")
	return n, nil
}

// The token is the last notice's created_at (microseconds, what Postgres
// keeps) and id, so a page continues exactly after it even as new notices
// arrive at the front.
func encodeCursor(c Cursor) string {
	raw := strconv.FormatInt(c.CreatedAt.UnixMicro(), 10) + ":" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(token string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, ErrInvalidToken
	}
	micros, id, ok := strings.Cut(string(b), ":")
	if !ok || id == "" {
		return Cursor{}, ErrInvalidToken
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return Cursor{}, ErrInvalidToken
	}
	return Cursor{CreatedAt: time.UnixMicro(us).UTC(), ID: id}, nil
}
