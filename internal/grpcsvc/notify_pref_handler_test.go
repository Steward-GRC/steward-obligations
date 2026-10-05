// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/grpcsvc"
	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
	"github.com/Steward-GRC/steward-obligations/internal/notifprefs"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

type fakeNotifPrefBackend struct{ stored store.NotifPref }

func (f *fakeNotifPrefBackend) Upsert(_ context.Context, p store.NotifPref) error {
	f.stored = p
	return nil
}
func (f *fakeNotifPrefBackend) Get(_ context.Context, userID string) (store.NotifPref, error) {
	if f.stored.UserID == userID {
		return f.stored, nil
	}
	return store.NotifPref{UserID: userID, Email: true, InApp: true, Push: false}, nil
}

func startNotifPrefTestServer(t *testing.T, b grpcsvc.NotifPrefBackend) obligationsv1.NotifPrefServiceClient {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()
	obligationsv1.RegisterNotifPrefServiceServer(s, grpcsvc.NewNotifPrefHandler(b))
	go s.Serve(lis) //nolint:errcheck
	t.Cleanup(s.GracefulStop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return obligationsv1.NewNotifPrefServiceClient(conn)
}

func TestUpsertAndGetNotifPref(t *testing.T) {
	backend := &fakeNotifPrefBackend{}
	client := startNotifPrefTestServer(t, backend)

	_, err := client.UpsertNotifPref(context.Background(), &obligationsv1.UpsertNotifPrefRequest{
		Pref: &obligationsv1.NotificationPref{UserId: "u-pref-1", Email: true, InApp: false, Push: true},
	})
	if err != nil {
		t.Fatalf("UpsertNotifPref: %v", err)
	}

	resp, err := client.GetNotifPref(context.Background(), &obligationsv1.GetNotifPrefRequest{UserId: "u-pref-1"})
	if err != nil {
		t.Fatalf("GetNotifPref: %v", err)
	}
	if !resp.Pref.Email {
		t.Error("expected Email=true")
	}
	if resp.Pref.InApp {
		t.Error("expected InApp=false")
	}
	if !resp.Pref.Push {
		t.Error("expected Push=true")
	}
}

func TestUpsertNotifPref_NilPref(t *testing.T) {
	backend := &fakeNotifPrefBackend{}
	client := startNotifPrefTestServer(t, backend)

	_, err := client.UpsertNotifPref(context.Background(), &obligationsv1.UpsertNotifPrefRequest{Pref: nil})
	if err == nil {
		t.Fatal("expected error when Pref is nil")
	}
}

// fakeSettings is an in-memory NotifSettingsService for the cadence RPCs. It
// can be primed to return a canned Settings or error.
type fakeSettings struct {
	ret notifprefs.Settings
	err error
}

func (f *fakeSettings) Get(context.Context, string) (notifprefs.Settings, error) {
	return f.ret, f.err
}
func (f *fakeSettings) SetCategoryCadence(context.Context, string, notifpolicy.Category, notifpolicy.Cadence) (notifprefs.Settings, error) {
	return f.ret, f.err
}
func (f *fakeSettings) SetTypeCadence(context.Context, string, string, notifpolicy.Cadence) (notifprefs.Settings, error) {
	return f.ret, f.err
}
func (f *fakeSettings) SetDigestWindow(context.Context, string, int, int) (notifprefs.Settings, error) {
	return f.ret, f.err
}

// TestSetCategoryCadenceRejectsMandatoryOffCoded proves the write-side floor
// surfaces as a coded InvalidArgument (NOTIFY_PREF_MANDATORY_OFF / 7004).
func TestSetCategoryCadenceRejectsMandatoryOffCoded(t *testing.T) {
	h := grpcsvc.NewNotifPrefHandler(nil).WithSettings(&fakeSettings{err: notifprefs.ErrMandatoryOff})
	_, err := h.SetCategoryCadence(context.Background(), &obligationsv1.SetCategoryCadenceRequest{
		UserId:   "u-1",
		Category: obligationsv1.NotifCategory_NOTIF_CATEGORY_COMPLIANCE,
		Cadence:  obligationsv1.NotifCadence_NOTIF_CADENCE_OFF,
	})
	if err == nil {
		t.Fatal("expected error setting mandatory category off")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	info, ok := apperrgrpc.FromStatus(st)
	if !ok || info.Symbol != "NOTIFY_PREF_MANDATORY_OFF" || info.Code != 7004 {
		t.Fatalf("want NOTIFY_PREF_MANDATORY_OFF/7004, got %+v (ok=%v)", info, ok)
	}
}

// TestSetCategoryCadenceRejectsUnspecifiedCoded proves an unspecified category
// enum is a coded InvalidArgument (NOTIFY_PREF_INVALID / 7005).
func TestSetCategoryCadenceRejectsUnspecifiedCoded(t *testing.T) {
	h := grpcsvc.NewNotifPrefHandler(nil).WithSettings(&fakeSettings{})
	_, err := h.SetCategoryCadence(context.Background(), &obligationsv1.SetCategoryCadenceRequest{
		UserId:   "u-1",
		Category: obligationsv1.NotifCategory_NOTIF_CATEGORY_UNSPECIFIED,
		Cadence:  obligationsv1.NotifCadence_NOTIF_CADENCE_DAILY,
	})
	st, _ := status.FromError(err)
	if st == nil || st.Code() != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	info, ok := apperrgrpc.FromStatus(st)
	if !ok || info.Code != 7005 {
		t.Fatalf("want NOTIFY_PREF_INVALID/7005, got %+v", info)
	}
}

// TestGetNotificationSettingsMapsToProto proves the handler maps the domain
// Settings to the proto surface (channels + categories + digest).
func TestGetNotificationSettingsMapsToProto(t *testing.T) {
	ret := notifprefs.Settings{
		Channels: store.NotifPref{UserID: "u-1", Email: true, InApp: false, Push: true},
		Categories: []notifprefs.CategoryPref{
			{Category: notifpolicy.CategoryCompliance, Cadence: notifpolicy.CadenceDaily, Mandatory: true},
		},
		Digest: store.DigestWindow{DailyHour: 9, WeeklyDOW: 3},
	}
	h := grpcsvc.NewNotifPrefHandler(nil).WithSettings(&fakeSettings{ret: ret})
	resp, err := h.GetNotificationSettings(context.Background(), &obligationsv1.GetNotificationSettingsRequest{UserId: "u-1"})
	if err != nil {
		t.Fatalf("GetNotificationSettings: %v", err)
	}
	s := resp.GetSettings()
	if !s.GetChannels().GetEmail() || s.GetChannels().GetInApp() || !s.GetChannels().GetPush() {
		t.Errorf("channels mismapped: %+v", s.GetChannels())
	}
	if len(s.GetCategories()) != 1 ||
		s.GetCategories()[0].GetCategory() != obligationsv1.NotifCategory_NOTIF_CATEGORY_COMPLIANCE ||
		s.GetCategories()[0].GetCadence() != obligationsv1.NotifCadence_NOTIF_CADENCE_DAILY ||
		!s.GetCategories()[0].GetMandatory() {
		t.Errorf("category mismapped: %+v", s.GetCategories())
	}
	if s.GetDigest().GetDailyHour() != 9 || s.GetDigest().GetWeeklyDow() != 3 {
		t.Errorf("digest mismapped: %+v", s.GetDigest())
	}
}

// TestListNotifTypesReturnsWholeTaxonomy asserts the catalog RPC serves every
// taxonomy row -- the whole point of the RPC is that a client can enumerate the
// types before the user has saved a single override.
func TestListNotifTypesReturnsWholeTaxonomy(t *testing.T) {
	h := grpcsvc.NewNotifPrefHandler(nil)
	resp, err := h.ListNotifTypes(context.Background(), &obligationsv1.ListNotifTypesRequest{})
	if err != nil {
		t.Fatalf("ListNotifTypes: %v", err)
	}
	want := notifpolicy.AllClasses()
	got := resp.GetTypes()
	if len(got) != len(want) {
		t.Fatalf("got %d types, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.GetKind() != w.Kind {
			t.Errorf("row %d: kind %q, want %q (order must match AllClasses)", i, g.GetKind(), w.Kind)
		}
		if g.GetMandatory() != w.Mandatory() {
			t.Errorf("kind %q: mandatory %v, want %v", w.Kind, g.GetMandatory(), w.Mandatory())
		}
		if g.GetCategory() == obligationsv1.NotifCategory_NOTIF_CATEGORY_UNSPECIFIED {
			t.Errorf("kind %q: category is UNSPECIFIED", w.Kind)
		}
		if g.GetDelivery() == obligationsv1.NotifDelivery_NOTIF_DELIVERY_UNSPECIFIED {
			t.Errorf("kind %q: delivery is UNSPECIFIED", w.Kind)
		}
	}
}

// TestListNotifTypesNeedsNoSettingsService pins that the catalog is
// user-independent reference data: it must answer on a handler with neither a
// store backend nor a settings service wired.
func TestListNotifTypesNeedsNoSettingsService(t *testing.T) {
	h := grpcsvc.NewNotifPrefHandler(nil)
	resp, err := h.ListNotifTypes(context.Background(), &obligationsv1.ListNotifTypesRequest{})
	if err != nil {
		t.Fatalf("ListNotifTypes without settings service: %v", err)
	}
	if len(resp.GetTypes()) == 0 {
		t.Fatal("ListNotifTypes returned no types")
	}
}

// TestListNotifTypesMarksImmediateOnlyKinds guards the signal the UI computes
// its per-type cadence floor from. SetTypeCadence accepts a digest cadence for
// these kinds and clamps at send time, so `delivery` is the only way a client
// can know not to offer one.
func TestListNotifTypesMarksImmediateOnlyKinds(t *testing.T) {
	h := grpcsvc.NewNotifPrefHandler(nil)
	resp, err := h.ListNotifTypes(context.Background(), &obligationsv1.ListNotifTypesRequest{})
	if err != nil {
		t.Fatalf("ListNotifTypes: %v", err)
	}
	byKind := make(map[string]*obligationsv1.NotifTypeDef, len(resp.GetTypes()))
	for _, d := range resp.GetTypes() {
		byKind[d.GetKind()] = d
	}
	for kind, want := range map[string]obligationsv1.NotifDelivery{
		// LOCKED: the first ack demand is never digest-foldable.
		"ack-required": obligationsv1.NotifDelivery_NOTIF_DELIVERY_IMMEDIATE_ONLY,
		"otp":          obligationsv1.NotifDelivery_NOTIF_DELIVERY_IMMEDIATE_ONLY,
		// The recurring reminder, by contrast, IS digest-foldable.
		"policy-ack-reminder": obligationsv1.NotifDelivery_NOTIF_DELIVERY_REMINDER_SCHEDULE,
		"policy-retired":      obligationsv1.NotifDelivery_NOTIF_DELIVERY_DIGEST_PREFERRED,
		"workflow-assigned":   obligationsv1.NotifDelivery_NOTIF_DELIVERY_IMMEDIATE_OR_DIGEST,
	} {
		d, ok := byKind[kind]
		if !ok {
			t.Errorf("kind %q missing from catalog", kind)
			continue
		}
		if d.GetDelivery() != want {
			t.Errorf("kind %q: delivery %v, want %v", kind, d.GetDelivery(), want)
		}
	}
}

// TestGetNotificationSettingsCarriesOverrideDelivery asserts an override row is
// self-describing, so the UI does not have to join it back to the catalog to
// know whether the kind can be batched.
func TestGetNotificationSettingsCarriesOverrideDelivery(t *testing.T) {
	ret := notifprefs.Settings{
		Channels: store.NotifPref{UserID: "u-1", Email: true},
		Overrides: []notifprefs.TypePref{{
			Kind:      "ack-required",
			Category:  notifpolicy.CategoryCompliance,
			Cadence:   notifpolicy.CadenceImmediate,
			Mandatory: true,
			Delivery:  notifpolicy.DeliveryImmediateOnly,
		}},
	}
	h := grpcsvc.NewNotifPrefHandler(nil).WithSettings(&fakeSettings{ret: ret})
	resp, err := h.GetNotificationSettings(context.Background(), &obligationsv1.GetNotificationSettingsRequest{UserId: "u-1"})
	if err != nil {
		t.Fatalf("GetNotificationSettings: %v", err)
	}
	ovr := resp.GetSettings().GetOverrides()
	if len(ovr) != 1 {
		t.Fatalf("got %d overrides, want 1", len(ovr))
	}
	if ovr[0].GetDelivery() != obligationsv1.NotifDelivery_NOTIF_DELIVERY_IMMEDIATE_ONLY {
		t.Errorf("override delivery = %v, want IMMEDIATE_ONLY", ovr[0].GetDelivery())
	}
}
