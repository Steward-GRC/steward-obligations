// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"

	identityv1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/identity/v1"
	ackpkg "github.com/Steward-GRC/steward-obligations/internal/ack"
	"github.com/Steward-GRC/steward-obligations/internal/emailservice"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/store"
)

// identityGRPCAdapter wraps identityv1.IdentityReadServiceClient and
// implements notify.UserEmailResolver and notify.FCMTokenResolver so the
// email and push channels can use the real Identity service without importing
// proto types into the notify package.
type identityGRPCAdapter struct {
	client identityv1.IdentityReadServiceClient
}

func (a *identityGRPCAdapter) ResolveEmail(ctx context.Context, userID string) (string, error) {
	resp, err := a.client.ResolveEmail(ctx, &identityv1.ResolveEmailRequest{UserId: userID})
	if err != nil {
		return "", err
	}
	return resp.GetEmail(), nil
}

func (a *identityGRPCAdapter) ResolveFCMToken(ctx context.Context, userID string) (string, error) {
	resp, err := a.client.ResolveFCMToken(ctx, &identityv1.ResolveFCMTokenRequest{UserId: userID})
	if err != nil {
		return "", err
	}
	return resp.GetFcmToken(), nil
}

// ResolveWelcomeRecipient returns a user's display name and email address for
// the welcome-account email, implementing grpcsvc.WelcomeUserResolver. It uses
// GetUser (not ResolveEmail) so the greeting can be personalized with the name.
func (a *identityGRPCAdapter) ResolveWelcomeRecipient(ctx context.Context, userID string) (name, email string, err error) {
	resp, err := a.client.GetUser(ctx, &identityv1.GetUserRequest{UserId: userID})
	if err != nil {
		return "", "", err
	}
	u := resp.GetUser()
	return u.GetName(), u.GetEmail(), nil
}

// ResolveTimezone returns a user's IANA timezone (e.g. "America/New_York"),
// implementing notify.TZResolver. It reads the timezone field Identity exposes
// on the User message (contracts/identity v1.12.0). An unset timezone comes back
// as the empty string, which notify.QuietHours treats as "fall back to UTC" — so
// quiet-hours and digest windows degrade gracefully to UTC (no regression) for
// users who have not set a zone yet, and evaluate in the user's real local hour
// once they have.
func (a *identityGRPCAdapter) ResolveTimezone(ctx context.Context, userID string) (string, error) {
	resp, err := a.client.GetUser(ctx, &identityv1.GetUserRequest{UserId: userID})
	if err != nil {
		return "", err
	}
	return resp.GetUser().GetTimezone(), nil
}

// mailOutboxStore adapts *store.MailOutboxStore to mail.OutboxStore (and thus
// mail.OutboxWriter), mapping between mail.OutboxItem and store.MailOutboxRow so
// package store carries no dependency on internal/mail.
type mailOutboxStore struct{ store *store.MailOutboxStore }

func (a mailOutboxStore) Enqueue(ctx context.Context, it mail.OutboxItem) error {
	return a.store.Enqueue(ctx, toOutboxRow(it))
}

func (a mailOutboxStore) ClaimPending(ctx context.Context, limit int) ([]mail.OutboxItem, error) {
	rows, err := a.store.ClaimPending(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]mail.OutboxItem, len(rows))
	for i, r := range rows {
		out[i] = mail.OutboxItem{
			ID:        r.ID,
			Kind:      r.Kind,
			UserID:    r.UserID,
			Recipient: r.Recipient,
			From:      r.From,
			Subject:   r.Subject,
			HTML:      r.HTML,
			Text:      r.Text,
			DedupKey:  r.DedupKey,
			Attempts:  r.Attempts,
			LastError: r.LastError,
		}
	}
	return out, nil
}

func (a mailOutboxStore) MarkSent(ctx context.Context, id string) error {
	return a.store.MarkSent(ctx, id)
}

func (a mailOutboxStore) MarkFailed(ctx context.Context, id, lastErr string) error {
	return a.store.MarkFailed(ctx, id, lastErr)
}

func (a mailOutboxStore) Reschedule(ctx context.Context, id, lastErr string, next time.Time) error {
	return a.store.Reschedule(ctx, id, lastErr, next)
}

func toOutboxRow(it mail.OutboxItem) store.MailOutboxRow {
	return store.MailOutboxRow{
		ID:        it.ID,
		Kind:      it.Kind,
		Recipient: it.Recipient,
		From:      it.From,
		Subject:   it.Subject,
		HTML:      it.HTML,
		Text:      it.Text,
		UserID:    it.UserID,
		DedupKey:  it.DedupKey,
		Attempts:  it.Attempts,
		LastError: it.LastError,
	}
}

// notifOutboxWriter adapts *store.NotificationOutboxStore to notify.BatchWriter,
// mapping notify.BatchItem to store.OutboxRow so package notify carries no
// store-row coupling (mirroring mailOutboxStore).
type notifOutboxWriter struct {
	store *store.NotificationOutboxStore
}

func (a notifOutboxWriter) WriteBatch(ctx context.Context, it notify.BatchItem) error {
	return a.store.Enqueue(ctx, store.OutboxRow{
		UserID:     it.UserID,
		Kind:       it.Kind,
		Category:   it.Category,
		Severity:   it.Severity,
		DedupRef:   it.DedupRef,
		Vars:       it.Vars,
		WindowKind: it.WindowKind,
		DigestKey:  it.DigestKey,
	})
}

// pendingAckCandidates satisfies scheduler.PendingAckCandidates by unioning the
// users who fold compliance into a digest via the category cadence with those
// who only overrode a specific recurring-compliance type (policy-ack-reminder /
// review-due) to a digest.
type pendingAckCandidates struct {
	categories *store.NotificationCategoryPrefStore
	overrides  *store.NotificationTypeOverrideStore
}

func (c pendingAckCandidates) PendingAckDigestUsers(ctx context.Context) ([]string, error) {
	digestCadences := []string{"daily", "weekly"}
	byCat, err := c.categories.UsersWithCadence(ctx, "compliance", digestCadences)
	if err != nil {
		return nil, err
	}
	byType, err := c.overrides.UsersWithCadence(ctx, []string{"policy-ack-reminder", "review-due"}, digestCadences)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(byCat)+len(byType))
	var out []string
	for _, u := range append(byCat, byType...) {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, nil
}

// noManagerResolver satisfies scheduler.ManagerResolver in the interim: Identity
// exposes no manager RPC (v1.0.0), so no manager is resolvable yet. The sweep
// still records the escalation (stopping user reminders) and logs; the actual
// manager send lands when a manager resolver exists.
type noManagerResolver struct{}

func (noManagerResolver) ResolveManager(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// mailgunUsageReader resolves the live Mailgun credentials from the provider on
// each poll and reads usage over Mailgun's HTTP API. When Mailgun is not
// configured it returns an error, which the usage guard treats as fail-open (no
// pause) — the correct posture for dev/local where Mailgun is unset.
type emailServiceUsageReader struct{ provider *emailservice.Provider }

func (r emailServiceUsageReader) Read(ctx context.Context) (mail.Usage, error) {
	cfg, ok, err := r.provider.Get(ctx)
	if err != nil {
		return mail.Usage{}, err
	}
	if !ok {
		return mail.Usage{}, errors.New("the email service is not configured")
	}
	return mail.NewHTTPUsageReader(cfg.APIKey, cfg.Domain, cfg.Region).Read(ctx)
}

// staticSiteAdminResolver satisfies consumer.SiteAdminResolver from a fixed,
// config-supplied list of addresses (config.Config.SiteAdminEmails, env
// SITE_ADMIN_EMAILS). Interim: see its doc comment for why no dynamic
// resolver exists yet.
type staticSiteAdminResolver []string

func (r staticSiteAdminResolver) ListSiteAdminEmails(ctx context.Context) ([]string, error) {
	return r, nil
}

// rabbitLogger adapts the service's zerolog logger to the go-rabbitmq Logger
// interface so connection, reconnect, backoff, and per-message handler-error
// events surface in the service log.
type rabbitLogger struct{ logger zerolog.Logger }

func (l rabbitLogger) Debugf(format string, args ...any) { l.logger.Debug().Msgf(format, args...) }
func (l rabbitLogger) Infof(format string, args ...any)  { l.logger.Info().Msgf(format, args...) }
func (l rabbitLogger) Warnf(format string, args ...any)  { l.logger.Warn().Msgf(format, args...) }
func (l rabbitLogger) Errorf(format string, args ...any) { l.logger.Error().Msgf(format, args...) }

// ackAuditEmitter composes the audit emitter the ack service runs with:
// PlatformAuditAdapter over the impersonation-decorating sink.
//
// It is a function, rather than an inline expression at the call site, so
// main_test.go can assert the PRODUCTION composition attributes an impersonated
// ack to the real admin. That is not ceremony — it is the specific failure mode
// this class of bug keeps taking. hid behind tests that hand-rolled
// their own correct wiring while main.go supplied none, and
// itself was not a logic bug at all: authmw.ForwardedClaimsServerInterceptor was
// already installed here and had been exposing x-fwd-actor the whole time, and
// nothing consumed it. A test that builds its own decorated sink proves the
// decorator works and can never prove main.go uses it. So there is exactly one
// definition of this wiring and the tests drive it.
func ackAuditEmitter(sink ackpkg.AuditSink) ackpkg.AuditEmitter {
	return ackpkg.NewPlatformAuditAdapter(ackpkg.NewImpersonationSink(sink))
}
