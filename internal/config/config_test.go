// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/config"
	"github.com/Steward-GRC/steward-obligations/internal/workloadauth"
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
	t.Setenv("WORKLOAD_AUTH", "disabled")

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

// required sets Load's own required values and a complete workload-auth
// block, so each test changes only what it is about.
func required(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"DATABASE_DSN": "postgres://test/test", "CORE_GRPC_ADDR": "core:9090", "IDENTITY_GRPC_ADDR": "identity:9090",
		"WORKLOAD_OIDC_ISSUER": "https://issuer.example.org", "WORKLOAD_ALLOWED_SERVICEACCOUNTS": "steward/steward-gateway",
	} {
		t.Setenv(k, v)
	}
}

func TestLoadReadsWorkloadAuth(t *testing.T) {
	required(t)
	t.Setenv("WORKLOAD_OIDC_JWKS_URL", "https://issuer.example.org/openid/v1/jwks")
	t.Setenv("WORKLOAD_OIDC_CA_FILE", "/oidc/ca.crt")
	t.Setenv("WORKLOAD_OIDC_BEARER_FILE", "/oidc/token")
	t.Setenv("WORKLOAD_AUDIENCE", "steward")
	t.Setenv("WORKLOAD_ALLOWED_SERVICEACCOUNTS", "steward/steward-gateway, steward/steward-reporting")
	c, err := config.Load()
	require.NoError(t, err)
	require.True(t, c.WorkloadAuthEnabled)
	require.Equal(t, workloadauth.Config{
		Issuer: "https://issuer.example.org", JWKSURL: "https://issuer.example.org/openid/v1/jwks", CAFile: "/oidc/ca.crt",
		BearerFile: "/oidc/token", Audience: "steward", AllowedServiceAccounts: []string{"steward/steward-gateway", "steward/steward-reporting"},
	}, c.WorkloadAuth)
	require.Equal(t, workloadauth.DefaultTokenFile, c.TokenFile, "with auth on, outbound calls carry the mounted token")
}

func TestLoadTakesTheTokenFileFromTheEnvironment(t *testing.T) {
	required(t)
	t.Setenv("WORKLOAD_TOKEN_FILE", "/elsewhere/token")
	c, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "/elsewhere/token", c.TokenFile)
}

func TestLoadFailsClosedWithoutWorkloadAuth(t *testing.T) {
	required(t)
	t.Setenv("WORKLOAD_OIDC_ISSUER", "")
	_, err := config.Load()
	require.ErrorIs(t, err, workloadauth.ErrNotConfigured, "no issuer and no explicit off switch stops the boot")
}

func TestLoadTurnsWorkloadAuthOffOnlyWhenDisabled(t *testing.T) {
	required(t)
	t.Setenv("WORKLOAD_OIDC_ISSUER", "")
	t.Setenv("WORKLOAD_ALLOWED_SERVICEACCOUNTS", "")
	t.Setenv("WORKLOAD_AUTH", "disabled")
	c, err := config.Load()
	require.NoError(t, err)
	require.False(t, c.WorkloadAuthEnabled)
	require.Empty(t, c.TokenFile, "with auth off, outbound calls carry no token")
}

func TestLoadRejectsABadWorkloadAuthSetting(t *testing.T) {
	for name, kv := range map[string][2]string{
		"auth mode typo":         {"WORKLOAD_AUTH", "off"},
		"disabled and an issuer": {"WORKLOAD_AUTH", "disabled"},
		"plain http issuer":      {"WORKLOAD_OIDC_ISSUER", "http://issuer.example.org"},
		"no allow-list":          {"WORKLOAD_ALLOWED_SERVICEACCOUNTS", ""},
		"bad allow-list entry":   {"WORKLOAD_ALLOWED_SERVICEACCOUNTS", "steward-gateway"},
	} {
		t.Run(name, func(t *testing.T) {
			required(t)
			t.Setenv(kv[0], kv[1])
			_, err := config.Load()
			require.Error(t, err)
		})
	}
}
