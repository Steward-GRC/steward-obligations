// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"strings"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/config"
)

// TestPortalPreferencesURLTargetsNotificationsRoute locks the email
// "manage preferences" deep-link to the staff app's notification-preferences
// route. Every branded email footer and List-Unsubscribe hint stamps this URL,
// so a change here silently sends recipients to the wrong page: the link is
// only exercised out-of-band (in a mail client), never by the app's own
// navigation, so nothing else would catch a regression.
func TestPortalPreferencesURLTargetsNotificationsRoute(t *testing.T) {
	// Load's own required values, so the assertion below actually runs rather
	// than skipping.
	t.Setenv("DATABASE_DSN", "postgres://test/test")
	t.Setenv("CORE_GRPC_ADDR", "core:9090")
	t.Setenv("IDENTITY_GRPC_ADDR", "identity:9090")
	t.Setenv("INTERNAL_BASE_URL", "https://policy.example.test")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	if got, want := cfg.PortalPreferencesURL, "https://policy.example.test/settings/notifications"; got != want {
		t.Errorf("PortalPreferencesURL = %q, want %q", got, want)
	}
	// It must be a dedicated page, not the app root as it was before the
	// preferences page existed.
	if !strings.HasSuffix(cfg.PortalPreferencesURL, "/settings/notifications") {
		t.Errorf("PortalPreferencesURL %q does not point at the preferences page", cfg.PortalPreferencesURL)
	}
}
