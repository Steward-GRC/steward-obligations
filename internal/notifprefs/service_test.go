// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifprefs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notifprefs"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// --- in-memory fake stores ---

type fakeChannels struct{ p store.NotifPref }

func (f *fakeChannels) Get(_ context.Context, userID string) (store.NotifPref, error) {
	if f.p.UserID == userID {
		return f.p, nil
	}
	return store.NotifPref{UserID: userID, Email: true, InApp: true, Push: false}, nil
}
func (f *fakeChannels) Upsert(_ context.Context, p store.NotifPref) error { f.p = p; return nil }

type fakeCategories struct{ rows map[string]string }

func (f *fakeCategories) Get(_ context.Context, _, category string) (string, bool, error) {
	c, ok := f.rows[category]
	return c, ok, nil
}
func (f *fakeCategories) List(context.Context, string) ([]store.CategoryCadence, error) {
	var out []store.CategoryCadence
	for k, v := range f.rows {
		out = append(out, store.CategoryCadence{Category: k, Cadence: v})
	}
	return out, nil
}
func (f *fakeCategories) Upsert(_ context.Context, _, category, cadence string) error {
	if f.rows == nil {
		f.rows = map[string]string{}
	}
	f.rows[category] = cadence
	return nil
}

type fakeOverrides struct{ rows map[string]string }

func (f *fakeOverrides) Get(_ context.Context, _, kind string) (string, bool, error) {
	c, ok := f.rows[kind]
	return c, ok, nil
}
func (f *fakeOverrides) List(context.Context, string) ([]store.TypeOverride, error) {
	var out []store.TypeOverride
	for k, v := range f.rows {
		out = append(out, store.TypeOverride{Kind: k, Cadence: v})
	}
	return out, nil
}
func (f *fakeOverrides) Upsert(_ context.Context, _, kind, cadence string) error {
	if f.rows == nil {
		f.rows = map[string]string{}
	}
	f.rows[kind] = cadence
	return nil
}

type fakeWindows struct {
	w   store.DigestWindow
	set bool
}

func (f *fakeWindows) Get(context.Context, string) (store.DigestWindow, error) {
	if f.set {
		return f.w, nil
	}
	return store.DigestWindow{DailyHour: 8, WeeklyDOW: 1}, nil
}
func (f *fakeWindows) Upsert(_ context.Context, _ string, w store.DigestWindow) error {
	f.w, f.set = w, true
	return nil
}

func newService() *notifprefs.Service {
	return notifprefs.NewService(&fakeChannels{}, &fakeCategories{}, &fakeOverrides{}, &fakeWindows{})
}

// TestSetCategoryCadenceRejectsOffForMandatory proves the compliance floor is
// enforced at the WRITE: OFF is rejected for every mandatory category with
// ErrMandatoryOff, so a crafted API call can't silence a mandatory category.
func TestSetCategoryCadenceRejectsOffForMandatory(t *testing.T) {
	for _, cat := range []notifpolicy.Category{
		notifpolicy.CategoryCompliance, notifpolicy.CategorySecurity, notifpolicy.CategoryTransactional,
	} {
		s := newService()
		_, err := s.SetCategoryCadence(context.Background(), "u", cat, notifpolicy.CadenceOff)
		if !errors.Is(err, notifprefs.ErrMandatoryOff) {
			t.Errorf("category %s off: want ErrMandatoryOff, got %v", cat, err)
		}
	}
}

// TestSetCategoryCadenceAllowsOffForOptional proves optional categories accept
// OFF.
func TestSetCategoryCadenceAllowsOffForOptional(t *testing.T) {
	for _, cat := range []notifpolicy.Category{notifpolicy.CategoryWorkflow, notifpolicy.CategoryInformational} {
		s := newService()
		if _, err := s.SetCategoryCadence(context.Background(), "u", cat, notifpolicy.CadenceOff); err != nil {
			t.Errorf("category %s off: unexpected error %v", cat, err)
		}
	}
}

// TestSetCategoryCadenceAllowsDailyForMandatory proves a mandatory category can
// be narrowed to a digest (daily) -- the floor, not a hard immediate.
func TestSetCategoryCadenceAllowsDailyForMandatory(t *testing.T) {
	s := newService()
	got, err := s.SetCategoryCadence(context.Background(), "u", notifpolicy.CategoryCompliance, notifpolicy.CadenceDaily)
	if err != nil {
		t.Fatalf("compliance daily: %v", err)
	}
	for _, c := range got.Categories {
		if c.Category == notifpolicy.CategoryCompliance && c.Cadence != notifpolicy.CadenceDaily {
			t.Errorf("compliance cadence = %s, want daily", c.Cadence)
		}
	}
}

// TestSetTypeCadenceRejectsOffForMandatoryKind proves the floor at the type
// level too.
func TestSetTypeCadenceRejectsOffForMandatoryKind(t *testing.T) {
	s := newService()
	_, err := s.SetTypeCadence(context.Background(), "u", "ack-required", notifpolicy.CadenceOff)
	if !errors.Is(err, notifprefs.ErrMandatoryOff) {
		t.Fatalf("ack-required off: want ErrMandatoryOff, got %v", err)
	}
}

// TestSetTypeCadenceRejectsUnknownKind proves an unknown kind is rejected.
func TestSetTypeCadenceRejectsUnknownKind(t *testing.T) {
	s := newService()
	_, err := s.SetTypeCadence(context.Background(), "u", "not-a-real-kind", notifpolicy.CadenceDaily)
	if !errors.Is(err, notifprefs.ErrUnknownKind) {
		t.Fatalf("unknown kind: want ErrUnknownKind, got %v", err)
	}
}

// TestSetTypeCadenceAllowsOffForOptionalKind proves an optional kind accepts off.
func TestSetTypeCadenceAllowsOffForOptionalKind(t *testing.T) {
	s := newService()
	if _, err := s.SetTypeCadence(context.Background(), "u", "policy-published", notifpolicy.CadenceOff); err != nil {
		t.Fatalf("policy-published off: unexpected error %v", err)
	}
}

// TestSetDigestWindowValidatesRange proves out-of-range windows are rejected.
func TestSetDigestWindowValidatesRange(t *testing.T) {
	s := newService()
	for _, tc := range []struct{ hour, dow int }{{24, 1}, {-1, 1}, {8, 0}, {8, 8}} {
		if _, err := s.SetDigestWindow(context.Background(), "u", tc.hour, tc.dow); !errors.Is(err, notifprefs.ErrBadDigestWindow) {
			t.Errorf("window %d/%d: want ErrBadDigestWindow, got %v", tc.hour, tc.dow, err)
		}
	}
	if _, err := s.SetDigestWindow(context.Background(), "u", 9, 3); err != nil {
		t.Errorf("valid window 9/3: unexpected error %v", err)
	}
}

// TestGetSettingsAppliesDefaultsAndMandatoryFlags proves Get resolves every
// category (stored or default) and stamps the mandatory flag, never surfacing a
// mandatory compliance category as off.
func TestGetSettingsAppliesDefaultsAndMandatoryFlags(t *testing.T) {
	s := notifprefs.NewService(
		&fakeChannels{},
		&fakeCategories{rows: map[string]string{"informational": "off", "compliance": "off"}},
		&fakeOverrides{},
		&fakeWindows{},
	)
	got, err := s.Get(context.Background(), "u")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Categories) != 5 {
		t.Fatalf("want 5 categories, got %d", len(got.Categories))
	}
	byCat := map[notifpolicy.Category]notifprefs.CategoryPref{}
	for _, c := range got.Categories {
		byCat[c.Category] = c
	}
	if !byCat[notifpolicy.CategoryCompliance].Mandatory {
		t.Error("compliance must be flagged mandatory")
	}
	if byCat[notifpolicy.CategoryCompliance].Cadence == notifpolicy.CadenceOff {
		t.Error("compliance must never surface as off (floored to daily)")
	}
	if byCat[notifpolicy.CategoryInformational].Mandatory {
		t.Error("informational must not be mandatory")
	}
	if byCat[notifpolicy.CategoryInformational].Cadence != notifpolicy.CadenceOff {
		t.Error("informational off should be honored")
	}
	if got.Digest.DailyHour != 8 || got.Digest.WeeklyDOW != 1 {
		t.Errorf("digest default = %+v, want 8/1", got.Digest)
	}
}

// TestReaderReflectsStores proves the notifpolicy.PrefReader adapter reports
// stored channel/category/override values back to the resolver.
func TestReaderReflectsStores(t *testing.T) {
	r := notifprefs.NewReader(
		&fakeChannels{p: store.NotifPref{UserID: "u", Email: true, InApp: false, Push: true}},
		&fakeCategories{rows: map[string]string{"informational": "weekly"}},
		&fakeOverrides{rows: map[string]string{"policy-published": "daily"}},
	)
	ch, err := r.Channels(context.Background(), "u")
	if err != nil || !ch.Email || ch.InApp || !ch.Push {
		t.Fatalf("channels = %+v err=%v", ch, err)
	}
	cad, ok, _ := r.CategoryCadence(context.Background(), "u", notifpolicy.CategoryInformational)
	if !ok || cad != notifpolicy.CadenceWeekly {
		t.Errorf("category cadence = %s ok=%v, want weekly", cad, ok)
	}
	oc, ok, _ := r.TypeOverride(context.Background(), "u", "policy-published")
	if !ok || oc != notifpolicy.CadenceDaily {
		t.Errorf("override = %s ok=%v, want daily", oc, ok)
	}
}
