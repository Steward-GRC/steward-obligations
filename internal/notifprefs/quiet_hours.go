// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifprefs

import (
	"context"
	"errors"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// ErrBadQuietHours is returned for a quiet-hours write that is not valid: an
// hour outside 0 to 23, only one of start and end, start equal to end, or an
// unknown timezone. The handler codes it NOTIFY_PREF_INVALID.
var ErrBadQuietHours = errors.New("quiet hours not valid")

// QuietStore is the quiet-hours persistence seam.
type QuietStore interface {
	Get(ctx context.Context, userID string) (store.QuietWindow, bool, error)
	Set(ctx context.Context, userID string, w store.QuietWindow) error
	Clear(ctx context.Context, userID string) error
}

// QuietService is the read/write model behind GetQuietHours and
// SetQuietHours. It owns the validation rules, which mirror the mock server's
// setQuietHours.
type QuietService struct{ store QuietStore }

// NewQuietService builds a QuietService.
func NewQuietService(s QuietStore) *QuietService { return &QuietService{store: s} }

// Get returns the user's window; found is false when none is stored.
func (s *QuietService) Get(ctx context.Context, userID string) (store.QuietWindow, bool, error) {
	return s.store.Get(ctx, userID)
}

// Set validates and stores a window, or clears it when neither start nor end is
// given. An empty timezone means UTC. The returned bool is false after a clear.
func (s *QuietService) Set(ctx context.Context, userID string, start, end *int, timezone string) (store.QuietWindow, bool, error) {
	logger := logctx.From(ctx)
	if start == nil && end == nil {
		if err := s.store.Clear(ctx, userID); err != nil {
			logger.Error().Err(err).Str("user_id", userID).Msg("notifprefs: clear quiet hours failed")
			return store.QuietWindow{}, false, err
		}
		logger.Debug().Str("user_id", userID).Msg("notifprefs: quiet hours cleared")
		return store.QuietWindow{}, false, nil
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if start == nil || end == nil || !validHour(*start) || !validHour(*end) || *start == *end || !knownZone(timezone) {
		logger.Debug().Str("user_id", userID).Msg("notifprefs: quiet hours refused as not valid")
		return store.QuietWindow{}, false, ErrBadQuietHours
	}
	w := store.QuietWindow{Start: *start, End: *end, Timezone: timezone}
	if err := s.store.Set(ctx, userID, w); err != nil {
		logger.Error().Err(err).Str("user_id", userID).Msg("notifprefs: save quiet hours failed")
		return store.QuietWindow{}, false, err
	}
	logger.Debug().Str("user_id", userID).Int("start", w.Start).Int("end", w.End).Str("timezone", w.Timezone).
		Msg("notifprefs: quiet hours saved")
	return w, true, nil
}

func validHour(h int) bool { return h >= 0 && h <= 23 }

func knownZone(name string) bool {
	_, err := time.LoadLocation(name)
	return err == nil
}
