// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"errors"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notifprefs"
	"github.com/Steward-GRC/steward-obligations/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NotifPrefBackend is the narrow persistence seam the NotifPref handler
// depends on; *store.NotificationPrefStore satisfies it in production.
type NotifPrefBackend interface {
	Upsert(ctx context.Context, p store.NotifPref) error
	Get(ctx context.Context, userID string) (store.NotifPref, error)
}

// NotifSettingsService is the read/write model behind the cadence RPCs
// (category/type/digest). *notifprefs.Service satisfies it in production.
type NotifSettingsService interface {
	Get(ctx context.Context, userID string) (notifprefs.Settings, error)
	SetCategoryCadence(ctx context.Context, userID string, cat notifpolicy.Category, cad notifpolicy.Cadence) (notifprefs.Settings, error)
	SetTypeCadence(ctx context.Context, userID, kind string, cad notifpolicy.Cadence) (notifprefs.Settings, error)
	SetDigestWindow(ctx context.Context, userID string, dailyHour, weeklyDow int) (notifprefs.Settings, error)
}

// NotifPrefHandler implements obligationsv1.NotifPrefServiceServer.
type NotifPrefHandler struct {
	obligationsv1.UnimplementedNotifPrefServiceServer
	backend  NotifPrefBackend
	settings NotifSettingsService
}

// NewNotifPrefHandler returns a handler that delegates to the supplied backend.
// The cadence RPCs require a settings service wired via WithSettings.
func NewNotifPrefHandler(b NotifPrefBackend) *NotifPrefHandler {
	return &NotifPrefHandler{backend: b}
}

// WithSettings wires the cadence (category/type/digest) settings service that
// backs GetNotificationSettings/SetCategoryCadence/SetTypeCadence/
// SetDigestWindow. Returns the receiver for fluent construction.
func (h *NotifPrefHandler) WithSettings(s NotifSettingsService) *NotifPrefHandler {
	h.settings = s
	return h
}

// UpsertNotifPref inserts or replaces a user's per-channel notification
// preferences. Returns InvalidArgument if the request omits the Pref message.
func (h *NotifPrefHandler) UpsertNotifPref(ctx context.Context, req *obligationsv1.UpsertNotifPrefRequest) (*obligationsv1.UpsertNotifPrefResponse, error) {
	if req.Pref == nil {
		return nil, status.Error(codes.InvalidArgument, "pref is required")
	}
	p := store.NotifPref{
		UserID: req.Pref.UserId,
		Email:  req.Pref.Email,
		InApp:  req.Pref.InApp,
		Push:   req.Pref.Push,
	}
	if err := h.backend.Upsert(ctx, p); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert notif pref: %v", err)
	}
	return &obligationsv1.UpsertNotifPrefResponse{Pref: req.Pref}, nil
}

// GetNotifPref returns the user's notification preferences (with system
// defaults applied by the backend when no row exists).
func (h *NotifPrefHandler) GetNotifPref(ctx context.Context, req *obligationsv1.GetNotifPrefRequest) (*obligationsv1.GetNotifPrefResponse, error) {
	p, err := h.backend.Get(ctx, req.UserId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get notif pref: %v", err)
	}
	return &obligationsv1.GetNotifPrefResponse{Pref: &obligationsv1.NotificationPref{
		UserId: p.UserID, Email: p.Email, InApp: p.InApp, Push: p.Push,
	}}, nil
}

// ListNotifTypes returns the notification-type taxonomy: every kind the
// platform sends with its category, mandatory flag, and delivery policy. It is
// user-independent reference data served straight from notifpolicy.AllClasses,
// the same map the send-time resolver classifies against, so a catalog a client
// renders can never drift from what is actually enforced.
//
// A client needs this in addition to GetNotificationSettings because
// `overrides` only carries the per-type choices a user has already saved (empty
// for a new user), and because `delivery` is the only reliable per-type cadence
// floor: SetTypeCadence rejects OFF for a mandatory kind but still accepts a
// digest cadence for an immediate-only kind and clamps it at send time.
func (h *NotifPrefHandler) ListNotifTypes(_ context.Context, _ *obligationsv1.ListNotifTypesRequest) (*obligationsv1.ListNotifTypesResponse, error) {
	classes := notifpolicy.AllClasses()
	out := &obligationsv1.ListNotifTypesResponse{Types: make([]*obligationsv1.NotifTypeDef, 0, len(classes))}
	for _, c := range classes {
		out.Types = append(out.Types, &obligationsv1.NotifTypeDef{
			Kind:      c.Kind,
			Category:  categoryToProto(c.Category),
			Mandatory: c.Mandatory(),
			Delivery:  deliveryToProto(c.Delivery),
		})
	}
	return out, nil
}

// GetNotificationSettings returns the full per-user notification preference
// surface: channel switches, resolved category cadences (with the mandatory
// flag), advanced per-type overrides, and the digest window.
func (h *NotifPrefHandler) GetNotificationSettings(ctx context.Context, req *obligationsv1.GetNotificationSettingsRequest) (*obligationsv1.GetNotificationSettingsResponse, error) {
	s, err := h.settings.Get(ctx, req.GetUserId())
	if err != nil {
		return nil, storeUnavailable(ctx, "get_notification_settings", err)
	}
	return &obligationsv1.GetNotificationSettingsResponse{Settings: settingsToProto(s)}, nil
}

// SetCategoryCadence sets the cadence for one category. It rejects OFF for a
// mandatory category with a coded InvalidArgument (the compliance floor is
// enforced at the write, not only at send time).
func (h *NotifPrefHandler) SetCategoryCadence(ctx context.Context, req *obligationsv1.SetCategoryCadenceRequest) (*obligationsv1.SetCategoryCadenceResponse, error) {
	cat, ok := categoryFromProto(req.GetCategory())
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.PrefInvalid("category", nil))
	}
	cad, ok := cadenceFromProto(req.GetCadence())
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.PrefInvalid("cadence", nil))
	}
	s, err := h.settings.SetCategoryCadence(ctx, req.GetUserId(), cat, cad)
	if err != nil {
		return nil, mapSettingsErr(ctx, "set_category_cadence", cat.String(), err)
	}
	return &obligationsv1.SetCategoryCadenceResponse{Settings: settingsToProto(s)}, nil
}

// SetTypeCadence sets an advanced per-type override. It rejects OFF for a
// mandatory kind and an unknown kind with a coded InvalidArgument.
func (h *NotifPrefHandler) SetTypeCadence(ctx context.Context, req *obligationsv1.SetTypeCadenceRequest) (*obligationsv1.SetTypeCadenceResponse, error) {
	cad, ok := cadenceFromProto(req.GetCadence())
	if !ok {
		return nil, errcodes.Error(ctx, errcodes.PrefInvalid("cadence", nil))
	}
	s, err := h.settings.SetTypeCadence(ctx, req.GetUserId(), req.GetKind(), cad)
	if err != nil {
		return nil, mapSettingsErr(ctx, "set_type_cadence", req.GetKind(), err)
	}
	return &obligationsv1.SetTypeCadenceResponse{Settings: settingsToProto(s)}, nil
}

// SetDigestWindow sets the user's daily/weekly digest anchor. It range-validates
// the hour [0,23] and day-of-week [1,7].
func (h *NotifPrefHandler) SetDigestWindow(ctx context.Context, req *obligationsv1.SetDigestWindowRequest) (*obligationsv1.SetDigestWindowResponse, error) {
	s, err := h.settings.SetDigestWindow(ctx, req.GetUserId(), int(req.GetDailyHour()), int(req.GetWeeklyDow()))
	if err != nil {
		return nil, mapSettingsErr(ctx, "set_digest_window", "digest_window", err)
	}
	return &obligationsv1.SetDigestWindowResponse{Settings: settingsToProto(s)}, nil
}

// mapSettingsErr routes a settings-service error to the right coded status: the
// compliance floor and validation faults become user-safe InvalidArgument
// codes, anything else is a store fault.
func mapSettingsErr(ctx context.Context, op, field string, err error) error {
	switch {
	case errors.Is(err, notifprefs.ErrMandatoryOff):
		return errcodes.Error(ctx, errcodes.PrefMandatoryOff(field, err))
	case errors.Is(err, notifprefs.ErrUnknownKind),
		errors.Is(err, notifprefs.ErrUnknownCategory),
		errors.Is(err, notifprefs.ErrUnknownCadence),
		errors.Is(err, notifprefs.ErrBadDigestWindow):
		return errcodes.Error(ctx, errcodes.PrefInvalid(field, nil))
	default:
		return storeUnavailable(ctx, op, err)
	}
}

// --- proto <-> notifpolicy conversions ---

func cadenceFromProto(c obligationsv1.NotifCadence) (notifpolicy.Cadence, bool) {
	switch c {
	case obligationsv1.NotifCadence_NOTIF_CADENCE_IMMEDIATE:
		return notifpolicy.CadenceImmediate, true
	case obligationsv1.NotifCadence_NOTIF_CADENCE_DAILY:
		return notifpolicy.CadenceDaily, true
	case obligationsv1.NotifCadence_NOTIF_CADENCE_WEEKLY:
		return notifpolicy.CadenceWeekly, true
	case obligationsv1.NotifCadence_NOTIF_CADENCE_OFF:
		return notifpolicy.CadenceOff, true
	default:
		return notifpolicy.CadenceUnspecified, false
	}
}

func cadenceToProto(c notifpolicy.Cadence) obligationsv1.NotifCadence {
	switch c {
	case notifpolicy.CadenceImmediate:
		return obligationsv1.NotifCadence_NOTIF_CADENCE_IMMEDIATE
	case notifpolicy.CadenceDaily:
		return obligationsv1.NotifCadence_NOTIF_CADENCE_DAILY
	case notifpolicy.CadenceWeekly:
		return obligationsv1.NotifCadence_NOTIF_CADENCE_WEEKLY
	case notifpolicy.CadenceOff:
		return obligationsv1.NotifCadence_NOTIF_CADENCE_OFF
	default:
		return obligationsv1.NotifCadence_NOTIF_CADENCE_UNSPECIFIED
	}
}

func categoryFromProto(c obligationsv1.NotifCategory) (notifpolicy.Category, bool) {
	switch c {
	case obligationsv1.NotifCategory_NOTIF_CATEGORY_COMPLIANCE:
		return notifpolicy.CategoryCompliance, true
	case obligationsv1.NotifCategory_NOTIF_CATEGORY_SECURITY:
		return notifpolicy.CategorySecurity, true
	case obligationsv1.NotifCategory_NOTIF_CATEGORY_TRANSACTIONAL:
		return notifpolicy.CategoryTransactional, true
	case obligationsv1.NotifCategory_NOTIF_CATEGORY_WORKFLOW:
		return notifpolicy.CategoryWorkflow, true
	case obligationsv1.NotifCategory_NOTIF_CATEGORY_INFORMATIONAL:
		return notifpolicy.CategoryInformational, true
	default:
		return "", false
	}
}

func categoryToProto(c notifpolicy.Category) obligationsv1.NotifCategory {
	switch c {
	case notifpolicy.CategoryCompliance:
		return obligationsv1.NotifCategory_NOTIF_CATEGORY_COMPLIANCE
	case notifpolicy.CategorySecurity:
		return obligationsv1.NotifCategory_NOTIF_CATEGORY_SECURITY
	case notifpolicy.CategoryTransactional:
		return obligationsv1.NotifCategory_NOTIF_CATEGORY_TRANSACTIONAL
	case notifpolicy.CategoryWorkflow:
		return obligationsv1.NotifCategory_NOTIF_CATEGORY_WORKFLOW
	case notifpolicy.CategoryInformational:
		return obligationsv1.NotifCategory_NOTIF_CATEGORY_INFORMATIONAL
	default:
		return obligationsv1.NotifCategory_NOTIF_CATEGORY_UNSPECIFIED
	}
}

// deliveryToProto maps a taxonomy delivery policy to its wire enum. An unknown
// kind carries no delivery policy, which maps to UNSPECIFIED; a client must
// treat that as "no digest guarantee" rather than assume batchability.
func deliveryToProto(d notifpolicy.DeliveryPolicy) obligationsv1.NotifDelivery {
	switch d {
	case notifpolicy.DeliveryImmediateOnly:
		return obligationsv1.NotifDelivery_NOTIF_DELIVERY_IMMEDIATE_ONLY
	case notifpolicy.DeliveryImmediateOrDigest:
		return obligationsv1.NotifDelivery_NOTIF_DELIVERY_IMMEDIATE_OR_DIGEST
	case notifpolicy.DeliveryReminderSchedule:
		return obligationsv1.NotifDelivery_NOTIF_DELIVERY_REMINDER_SCHEDULE
	case notifpolicy.DeliveryDigestPreferred:
		return obligationsv1.NotifDelivery_NOTIF_DELIVERY_DIGEST_PREFERRED
	default:
		return obligationsv1.NotifDelivery_NOTIF_DELIVERY_UNSPECIFIED
	}
}

func settingsToProto(s notifprefs.Settings) *obligationsv1.NotificationSettings {
	out := &obligationsv1.NotificationSettings{
		Channels: &obligationsv1.NotificationPref{
			UserId: s.Channels.UserID,
			Email:  s.Channels.Email,
			InApp:  s.Channels.InApp,
			Push:   s.Channels.Push,
		},
		Digest: &obligationsv1.DigestWindow{
			DailyHour: toInt32(s.Digest.DailyHour),
			WeeklyDow: toInt32(s.Digest.WeeklyDOW),
		},
	}
	for _, c := range s.Categories {
		out.Categories = append(out.Categories, &obligationsv1.CategoryPref{
			Category:  categoryToProto(c.Category),
			Cadence:   cadenceToProto(c.Cadence),
			Mandatory: c.Mandatory,
		})
	}
	for _, o := range s.Overrides {
		out.Overrides = append(out.Overrides, &obligationsv1.TypePref{
			Kind:      o.Kind,
			Category:  categoryToProto(o.Category),
			Cadence:   cadenceToProto(o.Cadence),
			Mandatory: o.Mandatory,
			Delivery:  deliveryToProto(o.Delivery),
		})
	}
	return out
}
