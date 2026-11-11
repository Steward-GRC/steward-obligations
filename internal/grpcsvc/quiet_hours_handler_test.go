// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/notifprefs"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

type memQuiet struct{ rows map[string]store.QuietWindow }

func (m *memQuiet) Get(_ context.Context, u string) (store.QuietWindow, bool, error) {
	w, ok := m.rows[u]
	return w, ok, nil
}
func (m *memQuiet) Set(_ context.Context, u string, w store.QuietWindow) error {
	if m.rows == nil {
		m.rows = map[string]store.QuietWindow{}
	}
	m.rows[u] = w
	return nil
}
func (m *memQuiet) Clear(_ context.Context, u string) error { delete(m.rows, u); return nil }

func quietHandler() *grpcsvc.NotifPrefHandler {
	return grpcsvc.NewNotifPrefHandler(nil).WithQuietHours(notifprefs.NewQuietService(&memQuiet{}))
}

func i32(v int32) *int32 { return &v }

func TestQuietHoursRPCs(t *testing.T) {
	h, ctx := quietHandler(), context.Background()

	got, err := h.GetQuietHours(ctx, &obligationsv1.GetQuietHoursRequest{UserId: "u-1"})
	if err != nil || got.GetQuietHours() != nil {
		t.Fatalf("before any save: %v %v, want unset", got, err)
	}

	set, err := h.SetQuietHours(ctx, &obligationsv1.SetQuietHoursRequest{
		UserId: "u-1", Start: i32(22), End: i32(7), Timezone: "America/New_York",
	})
	if err != nil {
		t.Fatalf("SetQuietHours: %v", err)
	}
	q := set.GetQuietHours()
	if q.GetStart() != 22 || q.GetEnd() != 7 || q.GetTimezone() != "America/New_York" {
		t.Fatalf("set returned %v", q)
	}

	got, _ = h.GetQuietHours(ctx, &obligationsv1.GetQuietHoursRequest{UserId: "u-1"})
	if got.GetQuietHours().GetStart() != 22 {
		t.Fatalf("get returned %v", got)
	}
	if other, _ := h.GetQuietHours(ctx, &obligationsv1.GetQuietHoursRequest{UserId: "u-2"}); other.GetQuietHours() != nil {
		t.Fatal("another user must have no window")
	}

	// UTC when no timezone, then cleared by no start and end.
	set, _ = h.SetQuietHours(ctx, &obligationsv1.SetQuietHoursRequest{UserId: "u-1", Start: i32(23), End: i32(6)})
	if set.GetQuietHours().GetTimezone() != "UTC" {
		t.Fatalf("timezone %q, want UTC", set.GetQuietHours().GetTimezone())
	}
	set, err = h.SetQuietHours(ctx, &obligationsv1.SetQuietHoursRequest{UserId: "u-1"})
	if err != nil || set.GetQuietHours() != nil {
		t.Fatalf("clear: %v %v", set, err)
	}
}

func TestSetQuietHoursRefusesBadInputCoded(t *testing.T) {
	h, ctx := quietHandler(), context.Background()
	for name, req := range map[string]*obligationsv1.SetQuietHoursRequest{
		"hour out of range": {UserId: "u", Start: i32(24), End: i32(7)},
		"negative hour":     {UserId: "u", Start: i32(22), End: i32(-1)},
		"start only":        {UserId: "u", Start: i32(22)},
		"end only":          {UserId: "u", End: i32(7)},
		"empty window":      {UserId: "u", Start: i32(22), End: i32(22)},
		"unknown timezone":  {UserId: "u", Start: i32(22), End: i32(7), Timezone: "Mars/Olympus"},
	} {
		_, err := h.SetQuietHours(ctx, req)
		st, _ := status.FromError(err)
		if st.Code() != codes.InvalidArgument {
			t.Errorf("%s: code %v, want InvalidArgument", name, st.Code())
			continue
		}
		if info, ok := apperrgrpc.FromStatus(st); !ok || info.Symbol != "NOTIFY_PREF_INVALID" {
			t.Errorf("%s: want NOTIFY_PREF_INVALID, got %+v", name, info)
		}
	}
}
