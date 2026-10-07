// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package config reads the obligations service's settings from the
// environment.
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/Steward-GRC/steward-obligations/internal/workloadauth"
)

// TLS is the server certificate and the CA client certificates must chain
// to. Empty serves plain gRPC.
type TLS struct {
	CertFile     string
	KeyFile      string
	ClientCAFile string
}

// Config holds every setting the service runs with.
type Config struct {
	DatabaseDSN string
	// MigrateDSN is a direct connection for migrations; it defaults to
	// DatabaseDSN.
	MigrateDSN    string
	MigrationsDir string
	RabbitURL     string
	GRPCPort      string
	// ProbePort serves /livez and /readyz over plain HTTP.
	ProbePort    string
	OTLPEndpoint string
	TLS          TLS
	// WorkloadAuth verifies the callers' workload tokens. It is set when
	// WorkloadAuthEnabled; WORKLOAD_AUTH=disabled is the only way to turn it
	// off.
	WorkloadAuth        workloadauth.Config
	WorkloadAuthEnabled bool
	// TokenFile is obligations' own projected token, sent on every call to
	// core and identity. Empty (WORKLOAD_AUTH=disabled) sends none.
	TokenFile string

	// CoreGRPCAddr and IdentityAddr are the services obligations calls.
	CoreGRPCAddr string
	IdentityAddr string

	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPStartTLS bool
	// SMTPTLSInsecure keeps STARTTLS but skips verifying the relay's
	// certificate, for relays whose certificate has no SANs.
	SMTPTLSInsecure bool

	// AppEnv "dev" sends every email to DevMailCatchAllTo instead of its
	// recipient; an empty DevMailCatchAllTo turns that off.
	AppEnv            string
	DevMailCatchAllTo string
	// RenderSidecarURL is the render sidecar (render/) the sender calls.
	RenderSidecarURL string

	// The links stamped into emails, all under the staff app.
	PortalAckQueueURL    string
	PortalPreferencesURL string
	PortalAccountURL     string
	PortalApprovalsURL   string
	PortalWorkflowsURL   string

	// Adopter branding for emails, empty by default: an absolute logo URL,
	// the product name, and the footer's legal or postal text.
	EmailLogoSrc     string
	EmailProductName string
	EmailLegalText   string

	// UnsubscribeSecret signs the unsubscribe, preferences and
	// email-verification links; empty turns all three off.
	UnsubscribeSecret       string
	UnsubscribeEndpointURL  string
	UnsubscribeLinkTTLHours int
	VerifyEmailEndpointURL  string
	VerifyEmailLinkTTLHours int

	// FCMProjectID turns push on; empty leaves it off.
	FCMProjectID string

	// RedisAddr turns the read caches on; empty leaves them off.
	RedisAddr             string
	RedisPassword         string
	ObligatingCacheTTLSec int

	NewUserAckGraceHours      int
	NewUserAckThrottleEnabled bool

	// The email-service transport's outage breaker, the held-mail drain
	// rate, and the plan-usage guard.
	EmailServiceBreakerThreshold   int
	EmailServiceBreakerCooldownSec int
	MailOutboxDrainRatePerSec      int
	EmailServiceUsageSoftThreshold float64
	EmailServiceUsageHardCeiling   int64
	EmailServiceUsagePollSec       int

	SchedulerEnabled    bool
	SchedulerTickSec    int
	EscalationAfterDays int

	// SiteAdminEmails receive the site-wide SSO notices.
	SiteAdminEmails []string
}

// Load reads the settings from the environment.
func Load() (Config, error) {
	internalBase := strings.TrimRight(getOr("INTERNAL_BASE_URL", "https://policies.example.org"), "/")
	publicBase := strings.TrimRight(getOr("PUBLIC_BASE_URL", internalBase), "/")

	c := Config{
		DatabaseDSN:   os.Getenv("DATABASE_DSN"),
		MigrationsDir: getOr("MIGRATIONS_DIR", "migrations"),
		RabbitURL:     os.Getenv("RABBITMQ_URL"),
		GRPCPort:      getOr("GRPC_PORT", "9090"),
		ProbePort:     getOr("PROBE_PORT", "8080"),
		OTLPEndpoint:  getOr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		TLS: TLS{
			CertFile: os.Getenv("GRPC_TLS_CERT_FILE"), KeyFile: os.Getenv("GRPC_TLS_KEY_FILE"),
			ClientCAFile: os.Getenv("GRPC_TLS_CLIENT_CA_FILE"),
		},

		CoreGRPCAddr: os.Getenv("CORE_GRPC_ADDR"),
		IdentityAddr: os.Getenv("IDENTITY_GRPC_ADDR"),

		SMTPHost:        os.Getenv("SMTP_HOST"),
		SMTPPort:        intOr("SMTP_PORT", 587),
		SMTPUsername:    os.Getenv("SMTP_USERNAME"),
		SMTPPassword:    os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:        getOr("SMTP_FROM", "no-reply@policies.example.org"),
		SMTPStartTLS:    boolOr("SMTP_STARTTLS", true),
		SMTPTLSInsecure: boolOr("SMTP_TLS_INSECURE", false),

		AppEnv:            os.Getenv("APP_ENV"),
		RenderSidecarURL:  getOr("RENDER_SIDECAR_URL", "http://127.0.0.1:8091"),
		DevMailCatchAllTo: os.Getenv("DEV_MAIL_CATCHALL_TO"),

		PortalAccountURL:     internalBase,
		PortalAckQueueURL:    internalBase + "/acknowledgements",
		PortalPreferencesURL: internalBase + "/settings/notifications",
		PortalApprovalsURL:   internalBase + "/workflows/approvals",
		PortalWorkflowsURL:   internalBase + "/workflows",

		EmailLogoSrc:     os.Getenv("EMAIL_LOGO_URL"),
		EmailProductName: os.Getenv("EMAIL_PRODUCT_NAME"),
		EmailLegalText:   os.Getenv("EMAIL_LEGAL_TEXT"),

		UnsubscribeSecret:       os.Getenv("NOTIFY_UNSUB_SECRET"),
		UnsubscribeEndpointURL:  getOr("NOTIFY_UNSUB_ENDPOINT_URL", publicBase+"/notify/unsubscribe"),
		UnsubscribeLinkTTLHours: intOr("NOTIFY_UNSUB_LINK_TTL_HOURS", 720),
		VerifyEmailEndpointURL:  getOr("NOTIFY_VERIFY_EMAIL_ENDPOINT_URL", publicBase+"/notify/verify-email"),
		VerifyEmailLinkTTLHours: intOr("NOTIFY_VERIFY_EMAIL_LINK_TTL_HOURS", 48),

		FCMProjectID: os.Getenv("FCM_PROJECT_ID"),

		RedisAddr:             os.Getenv("REDIS_ADDR"),
		RedisPassword:         os.Getenv("REDIS_PASSWORD"),
		ObligatingCacheTTLSec: intOr("OBLIGATING_CACHE_TTL_SEC", 60),

		NewUserAckGraceHours:      intOr("NEW_USER_ACK_GRACE_HOURS", 24),
		NewUserAckThrottleEnabled: boolOr("NEW_USER_ACK_THROTTLE_ENABLED", false),

		EmailServiceBreakerThreshold:   intOr("EMAIL_SERVICE_BREAKER_THRESHOLD", 5),
		EmailServiceBreakerCooldownSec: intOr("EMAIL_SERVICE_BREAKER_COOLDOWN_SEC", 30),
		MailOutboxDrainRatePerSec:      intOr("MAIL_OUTBOX_DRAIN_RATE", 10),
		EmailServiceUsageSoftThreshold: floatOr("EMAIL_SERVICE_USAGE_SOFT_THRESHOLD", 0.80),
		EmailServiceUsageHardCeiling:   int64(intOr("EMAIL_SERVICE_USAGE_HARD_CEILING", 0)),
		EmailServiceUsagePollSec:       intOr("EMAIL_SERVICE_USAGE_POLL_SEC", 300),

		SchedulerEnabled:    boolOr("SCHEDULER_ENABLED", true),
		SchedulerTickSec:    intOr("SCHEDULER_TICK_SEC", 900),
		EscalationAfterDays: intOr("ESCALATION_AFTER_DAYS", 30),

		SiteAdminEmails: splitCSV(os.Getenv("SITE_ADMIN_EMAILS")),
	}
	c.MigrateDSN = getOr("MIGRATE_DSN", c.DatabaseDSN)

	var errs []error
	var err error
	if c.WorkloadAuth, c.WorkloadAuthEnabled, err = workloadauth.ServerConfigFromEnv(os.Getenv); err != nil {
		errs = append(errs, err)
	}
	if c.WorkloadAuthEnabled {
		c.TokenFile = getOr(workloadauth.EnvTokenFile, workloadauth.DefaultTokenFile)
	}
	if c.DatabaseDSN == "" {
		errs = append(errs, errors.New("DATABASE_DSN is required"))
	}
	if c.CoreGRPCAddr == "" {
		errs = append(errs, errors.New("CORE_GRPC_ADDR is required"))
	}
	if c.IdentityAddr == "" {
		errs = append(errs, errors.New("IDENTITY_GRPC_ADDR is required"))
	}
	tlsSet := c.TLS.CertFile != "" || c.TLS.KeyFile != "" || c.TLS.ClientCAFile != ""
	if tlsSet && (c.TLS.CertFile == "" || c.TLS.KeyFile == "" || c.TLS.ClientCAFile == "") {
		errs = append(errs, errors.New("GRPC_TLS_CERT_FILE, GRPC_TLS_KEY_FILE and GRPC_TLS_CLIENT_CA_FILE are set together"))
	}
	return c, errors.Join(errs...)
}

func getOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func intOr(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

func boolOr(k string, d bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return d
}

func floatOr(k string, d float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return d
}

func splitCSV(v string) []string {
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
