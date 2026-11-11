// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

type fakeWindows struct {
	w     store.QuietWindow
	found bool
	err   error
}

func (f fakeWindows) Get(context.Context, string) (store.QuietWindow, bool, error) {
	return f.w, f.found, f.err
}

func utcAt(h int) func() time.Time {
	return func() time.Time { return time.Date(2026, 11, 11, h, 30, 0, 0, time.UTC) }
}

func TestStoredWindowInsideADay(t *testing.T) {
	q := notify.QuietHours{Windows: fakeWindows{w: store.QuietWindow{Start: 9, End: 17, Timezone: "UTC"}, found: true}}
	for h, want := range map[int]bool{8: false, 9: true, 16: true, 17: false, 23: false} {
		q.NowFn = utcAt(h)
		if got := q.InWindow(context.Background(), "u"); got != want {
			t.Errorf("hour %d: got %v, want %v", h, got, want)
		}
	}
}

func TestStoredWindowAcrossMidnight(t *testing.T) {
	q := notify.QuietHours{Windows: fakeWindows{w: store.QuietWindow{Start: 23, End: 6, Timezone: "UTC"}, found: true}}
	for h, want := range map[int]bool{22: false, 23: true, 2: true, 5: true, 6: false, 12: false} {
		q.NowFn = utcAt(h)
		if got := q.InWindow(context.Background(), "u"); got != want {
			t.Errorf("hour %d: got %v, want %v", h, got, want)
		}
	}
}

func TestStoredWindowReadsTheHourInItsOwnTimezone(t *testing.T) {
	// Identity says Tokyo; the stored window says New York and wins.
	q := notify.QuietHours{
		Resolver: fixedTZResolver{tz: "Asia/Tokyo"},
		Windows:  fakeWindows{w: store.QuietWindow{Start: 22, End: 7, Timezone: "America/New_York"}, found: true},
		// 03:00 UTC is 22:00 in New York (EST).
		NowFn: func() time.Time { return time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC) },
	}
	if !q.InWindow(context.Background(), "u") {
		t.Fatal("22:00 New York is inside the stored window")
	}
	q.NowFn = func() time.Time { return time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC) }
	if q.InWindow(context.Background(), "u") {
		t.Fatal("08:00 New York is outside the stored window")
	}
}

func TestNoStoredWindowKeepsTheDefault(t *testing.T) {
	q := notify.QuietHours{Windows: fakeWindows{}, Resolver: fixedTZResolver{tz: "UTC"}}
	for h, want := range map[int]bool{21: false, 22: true, 3: true, 7: false, 12: false} {
		q.NowFn = utcAt(h)
		if got := q.InWindow(context.Background(), "u"); got != want {
			t.Errorf("hour %d: got %v, want %v", h, got, want)
		}
	}
}

func TestWindowLookupFailureKeepsTheDefault(t *testing.T) {
	q := notify.QuietHours{Windows: fakeWindows{err: errors.New("db down")}, NowFn: utcAt(23)}
	if !q.InWindow(context.Background(), "u") {
		t.Fatal("a failed lookup falls back to 22:00-07:00, so 23:00 is held")
	}
	q.NowFn = utcAt(12)
	if q.InWindow(context.Background(), "u") {
		t.Fatal("a failed lookup falls back to the default, so noon is not held")
	}
}

func TestStoredWindowDrivesTheDispatcherForEmailAndPushOnly(t *testing.T) {
	prefs := &fakePrefStore{pref: store.NotifPref{UserID: "u", Email: true, InApp: true, Push: true}}
	email, inapp, push := &recordingChannel{}, &recordingChannel{}, &recordingChannel{}
	// Noon UTC: the default window would not hold, the stored 9-17 window does.
	d := notify.NewDispatcher(prefs, email, inapp, push).WithQuietWindows(
		fakeWindows{w: store.QuietWindow{Start: 9, End: 17, Timezone: "UTC"}, found: true}).
		WithNow(utcAt(12))
	d.SendAckReminder(context.Background(), notify.AckReminderPayload{UserID: "u", PolicyVersionID: "pv", Type: "initial"})
	if len(email.payloads) != 0 || len(push.payloads) != 0 {
		t.Errorf("email=%d push=%d, want both held", len(email.payloads), len(push.payloads))
	}
	if len(inapp.payloads) != 1 {
		t.Errorf("in-app=%d, want 1: it is never held", len(inapp.payloads))
	}

	// Outside the window everything goes out, and nothing held earlier is replayed.
	d = d.WithNow(utcAt(20))
	d.SendAckReminder(context.Background(), notify.AckReminderPayload{UserID: "u", PolicyVersionID: "pv2", Type: "initial"})
	if len(email.payloads) != 1 || len(push.payloads) != 1 || len(inapp.payloads) != 2 {
		t.Errorf("email=%d push=%d inapp=%d, want 1/1/2", len(email.payloads), len(push.payloads), len(inapp.payloads))
	}
}
