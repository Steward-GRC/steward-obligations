// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package notifprefs bridges the obligations service's persistence stores and the
// notifpolicy taxonomy/resolver. It provides two things:
//
// - Reader: the notifpolicy.PrefReader the send-time resolver reads (channels,
// category cadences, per-type overrides), keeping notifpolicy free of any
// store import.
// - Service: the read/write model behind the NotifPrefService cadence RPCs
// (build the full NotificationSettings; set category/type cadence and digest
// window, enforcing the compliance floor at the write).
package notifprefs

import (
	"context"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// ChannelStore is the channel master-switch persistence seam.
type ChannelStore interface {
	Get(ctx context.Context, userID string) (store.NotifPref, error)
	Upsert(ctx context.Context, p store.NotifPref) error
}

// CategoryStore is the per-category cadence persistence seam.
type CategoryStore interface {
	Get(ctx context.Context, userID, category string) (cadence string, found bool, err error)
	List(ctx context.Context, userID string) ([]store.CategoryCadence, error)
	Upsert(ctx context.Context, userID, category, cadence string) error
}

// OverrideStore is the per-type override persistence seam.
type OverrideStore interface {
	Get(ctx context.Context, userID, kind string) (cadence string, found bool, err error)
	List(ctx context.Context, userID string) ([]store.TypeOverride, error)
	Upsert(ctx context.Context, userID, kind, cadence string) error
}

// WindowStore is the digest-window persistence seam.
type WindowStore interface {
	Get(ctx context.Context, userID string) (store.DigestWindow, error)
	Upsert(ctx context.Context, userID string, w store.DigestWindow) error
}

// Reader implements notifpolicy.PrefReader over the channel/category/override
// stores.
type Reader struct {
	channels   ChannelStore
	categories CategoryStore
	overrides  OverrideStore
}

// NewReader builds a Reader.
func NewReader(channels ChannelStore, categories CategoryStore, overrides OverrideStore) *Reader {
	return &Reader{channels: channels, categories: categories, overrides: overrides}
}

// Channels implements notifpolicy.PrefReader.
func (r *Reader) Channels(ctx context.Context, userID string) (notifpolicy.Channels, error) {
	p, err := r.channels.Get(ctx, userID)
	if err != nil {
		return notifpolicy.Channels{}, err
	}
	return notifpolicy.Channels{Email: p.Email, InApp: p.InApp, Push: p.Push}, nil
}

// CategoryCadence implements notifpolicy.PrefReader.
func (r *Reader) CategoryCadence(ctx context.Context, userID string, cat notifpolicy.Category) (notifpolicy.Cadence, bool, error) {
	s, found, err := r.categories.Get(ctx, userID, cat.String())
	if err != nil || !found {
		return notifpolicy.CadenceUnspecified, false, err
	}
	c, ok := notifpolicy.ParseCadence(s)
	return c, ok, nil
}

// TypeOverride implements notifpolicy.PrefReader.
func (r *Reader) TypeOverride(ctx context.Context, userID, kind string) (notifpolicy.Cadence, bool, error) {
	s, found, err := r.overrides.Get(ctx, userID, kind)
	if err != nil || !found {
		return notifpolicy.CadenceUnspecified, false, err
	}
	c, ok := notifpolicy.ParseCadence(s)
	return c, ok, nil
}

// interface guard.
var _ notifpolicy.PrefReader = (*Reader)(nil)

// CategoryPref is one category's resolved cadence + mandatory flag.
type CategoryPref struct {
	Category  notifpolicy.Category
	Cadence   notifpolicy.Cadence
	Mandatory bool
}

// TypePref is one per-type override, enriched with its category + mandatory
// flag and the taxonomy's delivery policy so the row is self-describing: a
// client can tell an immediate-only kind from a digest-capable one without a
// second catalog lookup.
type TypePref struct {
	Kind      string
	Category  notifpolicy.Category
	Cadence   notifpolicy.Cadence
	Mandatory bool
	Delivery  notifpolicy.DeliveryPolicy
}

// Settings is the full per-user notification preference surface.
type Settings struct {
	Channels   store.NotifPref
	Categories []CategoryPref
	Overrides  []TypePref
	Digest     store.DigestWindow
}

// Service is the read/write model behind the cadence RPCs.
type Service struct {
	channels   ChannelStore
	categories CategoryStore
	overrides  OverrideStore
	windows    WindowStore
}

// NewService builds a Service over the four stores.
func NewService(channels ChannelStore, categories CategoryStore, overrides OverrideStore, windows WindowStore) *Service {
	return &Service{channels: channels, categories: categories, overrides: overrides, windows: windows}
}

// Get assembles the full NotificationSettings for a user: channel switches, a
// resolved cadence for every category (stored choice or default, floored),
// the advanced per-type overrides, and the digest window.
func (s *Service) Get(ctx context.Context, userID string) (Settings, error) {
	out := Settings{}

	ch, err := s.channels.Get(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	out.Channels = ch

	stored, err := s.categories.List(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	storedByCat := make(map[notifpolicy.Category]notifpolicy.Cadence, len(stored))
	for _, c := range stored {
		if cat, ok := notifpolicy.ParseCategory(c.Category); ok {
			if cad, ok := notifpolicy.ParseCadence(c.Cadence); ok {
				storedByCat[cat] = cad
			}
		}
	}
	for _, cat := range notifpolicy.AllCategories {
		cad, ok := storedByCat[cat]
		if !ok {
			cad = notifpolicy.DefaultCategoryCadence(cat)
		}
		// The compliance floor also governs what we report: a mandatory
		// compliance category never surfaces as off.
		cad = floorCategory(cat, cad)
		out.Categories = append(out.Categories, CategoryPref{
			Category:  cat,
			Cadence:   cad,
			Mandatory: notifpolicy.CategoryMandatory(cat),
		})
	}

	ovr, err := s.overrides.List(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	for _, o := range ovr {
		cad, ok := notifpolicy.ParseCadence(o.Cadence)
		if !ok {
			continue
		}
		class, known := notifpolicy.Classify(o.Kind)
		tp := TypePref{Kind: o.Kind, Cadence: cad}
		if known {
			tp.Category = class.Category
			tp.Mandatory = class.Mandatory()
			tp.Delivery = class.Delivery
		}
		out.Overrides = append(out.Overrides, tp)
	}

	w, err := s.windows.Get(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	out.Digest = w

	return out, nil
}

// ErrMandatoryOff is returned when a caller tries to set OFF for a mandatory
// category or type. The handler maps it to codes.InvalidArgument.
var ErrMandatoryOff = fmt.Errorf("cannot set OFF for a mandatory notification category")

// ErrUnknownCategory is returned for an unrecognized category.
var ErrUnknownCategory = fmt.Errorf("unknown notification category")

// ErrUnknownCadence is returned for an unrecognized cadence.
var ErrUnknownCadence = fmt.Errorf("unknown notification cadence")

// ErrUnknownKind is returned when a per-type override names an unknown kind.
var ErrUnknownKind = fmt.Errorf("unknown notification kind")

// ErrBadDigestWindow is returned for an out-of-range digest window.
var ErrBadDigestWindow = fmt.Errorf("digest window out of range")

// SetCategoryCadence persists a category cadence after enforcing the compliance
// floor: OFF is rejected for mandatory categories (compliance/security/
// transactional) with ErrMandatoryOff, so a crafted API call cannot silence a
// mandatory category even though the resolver would also clamp it at send time.
func (s *Service) SetCategoryCadence(ctx context.Context, userID string, cat notifpolicy.Category, cad notifpolicy.Cadence) (Settings, error) {
	if _, ok := notifpolicy.ParseCategory(cat.String()); !ok {
		return Settings{}, ErrUnknownCategory
	}
	if cad == notifpolicy.CadenceOff && notifpolicy.CategoryMandatory(cat) {
		return Settings{}, ErrMandatoryOff
	}
	if err := s.categories.Upsert(ctx, userID, cat.String(), cad.String()); err != nil {
		return Settings{}, err
	}
	return s.Get(ctx, userID)
}

// SetTypeCadence persists a per-type override after enforcing the floor: OFF is
// rejected for a mandatory kind's category with ErrMandatoryOff, and an unknown
// kind is rejected with ErrUnknownKind.
func (s *Service) SetTypeCadence(ctx context.Context, userID, kind string, cad notifpolicy.Cadence) (Settings, error) {
	class, known := notifpolicy.Classify(kind)
	if !known {
		return Settings{}, ErrUnknownKind
	}
	if cad == notifpolicy.CadenceOff && class.Mandatory() {
		return Settings{}, ErrMandatoryOff
	}
	if err := s.overrides.Upsert(ctx, userID, kind, cad.String()); err != nil {
		return Settings{}, err
	}
	return s.Get(ctx, userID)
}

// SetDigestWindow persists the digest window after range-validating dailyHour
// [0,23] and weeklyDow [1,7].
func (s *Service) SetDigestWindow(ctx context.Context, userID string, dailyHour, weeklyDow int) (Settings, error) {
	if dailyHour < 0 || dailyHour > 23 || weeklyDow < 1 || weeklyDow > 7 {
		return Settings{}, ErrBadDigestWindow
	}
	if err := s.windows.Upsert(ctx, userID, store.DigestWindow{DailyHour: dailyHour, WeeklyDOW: weeklyDow}); err != nil {
		return Settings{}, err
	}
	return s.Get(ctx, userID)
}

// floorCategory reports the display cadence for a category, applying the
// compliance floor (a mandatory compliance category is never off).
func floorCategory(cat notifpolicy.Category, cad notifpolicy.Cadence) notifpolicy.Cadence {
	if cad == notifpolicy.CadenceOff && cat == notifpolicy.CategoryCompliance {
		return notifpolicy.CadenceDaily
	}
	return cad
}
