// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifprefs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notifprefs"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

type fakeQuietStore struct{ rows map[string]store.QuietWindow }

func (f *fakeQuietStore) Get(_ context.Context, u string) (store.QuietWindow, bool, error) {
	w, ok := f.rows[u]
	return w, ok, nil
}
func (f *fakeQuietStore) Set(_ context.Context, u string, w store.QuietWindow) error {
	if f.rows == nil {
		f.rows = map[string]store.QuietWindow{}
	}
	f.rows[u] = w
	return nil
}
func (f *fakeQuietStore) Clear(_ context.Context, u string) error { delete(f.rows, u); return nil }

func ip(v int) *int { return &v }

func TestQuietHoursIsOffUntilSaved(t *testing.T) {
	svc := notifprefs.NewQuietService(&fakeQuietStore{})
	if _, found, err := svc.Get(context.Background(), "u"); err != nil || found {
		t.Fatalf("found=%v err=%v, want none", found, err)
	}
}

func TestQuietHoursStoresWindowInGivenTimezone(t *testing.T) {
	st := &fakeQuietStore{}
	svc := notifprefs.NewQuietService(st)
	w, found, err := svc.Set(context.Background(), "u", ip(22), ip(7), "America/New_York")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := store.QuietWindow{Start: 22, End: 7, Timezone: "America/New_York"}
	if w != want || st.rows["u"] != want {
		t.Fatalf("returned %+v stored %+v, want %+v", w, st.rows["u"], want)
	}
}

func TestQuietHoursTakesUTCWithNoTimezone(t *testing.T) {
	svc := notifprefs.NewQuietService(&fakeQuietStore{})
	w, _, err := svc.Set(context.Background(), "u", ip(23), ip(6), "")
	if err != nil || w.Timezone != "UTC" {
		t.Fatalf("w=%+v err=%v, want UTC", w, err)
	}
}

func TestQuietHoursClearsWithNoStartAndEnd(t *testing.T) {
	st := &fakeQuietStore{}
	svc := notifprefs.NewQuietService(st)
	_, _, _ = svc.Set(context.Background(), "u", ip(23), ip(6), "")
	_, found, err := svc.Set(context.Background(), "u", nil, nil, "")
	if err != nil || found {
		t.Fatalf("found=%v err=%v, want cleared", found, err)
	}
	if _, ok := st.rows["u"]; ok {
		t.Fatal("clear must delete the stored window")
	}
}

func TestQuietHoursRefusesInvalidInput(t *testing.T) {
	cases := map[string]struct {
		start, end *int
		tz         string
	}{
		"start out of range": {ip(24), ip(7), ""},
		"end negative":       {ip(22), ip(-1), ""},
		"start only":         {ip(22), nil, ""},
		"end only":           {nil, ip(7), ""},
		"empty window":       {ip(22), ip(22), ""},
		"unknown timezone":   {ip(22), ip(7), "Mars/Olympus"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeQuietStore{}
			svc := notifprefs.NewQuietService(st)
			_, _, err := svc.Set(context.Background(), "u", c.start, c.end, c.tz)
			if !errors.Is(err, notifprefs.ErrBadQuietHours) {
				t.Fatalf("err=%v, want ErrBadQuietHours", err)
			}
			if len(st.rows) != 0 {
				t.Fatal("a refused write must store nothing")
			}
		})
	}
}
