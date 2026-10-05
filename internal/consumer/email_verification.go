// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	"github.com/Steward-GRC/steward-obligations/internal/notifytoken"
)

// ---------------------------------------------------------------------------
// Wire-format types
// ---------------------------------------------------------------------------

// emailVerificationEvent mirrors the same "account.created" payload
// AccountCreatedConsumer consumes: {"event_type": "account.created",
// "user_id": "<uuid>"}. This consumer binds a SECOND queue to the same "jobs"
// exchange / "account.created" routing key (a distinct queue name -- see
// cmd/server's runEmailVerificationConsumer), so both the welcome send and the
// verification send fire independently off the one signal Identity already
// emits on every account-creation route -- no new event, no Identity change.
type emailVerificationEvent struct {
	EventType string `json:"event_type"`
	UserID    string `json:"user_id"`
}

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// EmailVerificationRecipientResolver resolves a user's display name and email
// address. Structurally identical to WelcomeRecipientResolver (and satisfied
// by the same identity gRPC adapter via ResolveWelcomeRecipient) but declared
// locally so this consumer's dependency is legible on its own and independent
// of the welcome flow's naming.
type EmailVerificationRecipientResolver interface {
	ResolveWelcomeRecipient(ctx context.Context, userID string) (name, email string, err error)
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// emailVerificationKind is the branded template rendered + sent. See
// internal/notifpolicy/taxonomy.go: CategoryTransactional + DeliveryImmediateOnly
// makes it mandatory (never suppressible by preference or quiet hours), the
// same treatment as welcome-account.
const emailVerificationKind = "email-verification"

// EmailVerificationConsumer handles AMQP messages from the "jobs" exchange on
// the "account.created" routing key (the same signal AccountCreatedConsumer
// handles, on its own queue). On each event it resolves the recipient, mints a
// PurposeEmailVerify token bound to (userID, email) via signer, and sends the
// email-verification email with a tokenized CTA link. Unlike the welcome send,
// this has no durable once-per-account gate: a redelivered event that lands
// outside the mail Sender's own dedup window simply issues a fresh,
// independently valid token -- re-verifying (or re-requesting a link) is
// idempotent downstream, so at-least-once delivery is an acceptable default
// for a first cut.
type EmailVerificationConsumer struct {
	users     EmailVerificationRecipientResolver
	sender    Sender
	signer    *notifytoken.Signer
	verifyURL string
	ttl       time.Duration
}

// NewEmailVerificationConsumer constructs an EmailVerificationConsumer.
// verifyURL is the PUBLIC verify-email endpoint the CTA button targets
// (cfg.VerifyEmailEndpointURL); ttl bounds how long a minted link stays valid
// (cfg.VerifyEmailLinkTTLHours). signer is nil-checked by Handle so a service
// with NOTIFY_UNSUB_SECRET unset (dev/local) simply never wires this consumer
// (see cmd/server's wiring, matching the unsubscribe-links feature's own
// empty-secret-disables-the-feature convention).
func NewEmailVerificationConsumer(users EmailVerificationRecipientResolver, sender Sender, signer *notifytoken.Signer, verifyURL string, ttl time.Duration) *EmailVerificationConsumer {
	return &EmailVerificationConsumer{users: users, sender: sender, signer: signer, verifyURL: verifyURL, ttl: ttl}
}

// Handle processes a single raw AMQP message body (JSON
// emailVerificationEvent). A malformed body or missing user id is a permanent
// error that must not requeue forever, so it is logged and swallowed (return
// nil); a transient resolve/send failure returns an error so the message is
// requeued and retried.
func (c *EmailVerificationConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)

	if c.signer == nil {
		logger.Warn().Msg("consumer/email_verification: no signer configured (NOTIFY_UNSUB_SECRET unset); skipping")
		return nil
	}

	var evt emailVerificationEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		logger.Warn().Err(err).Msg("consumer/email_verification: unmarshal; skipping")
		return nil
	}
	if evt.UserID == "" {
		logger.Warn().Msg("consumer/email_verification: missing user_id; skipping")
		return nil
	}

	name, email, err := c.users.ResolveWelcomeRecipient(ctx, evt.UserID)
	if err != nil {
		return fmt.Errorf("consumer/email_verification: resolve recipient %q: %w", evt.UserID, err)
	}
	if email == "" {
		logger.Warn().Str("user_id", evt.UserID).Msg("consumer/email_verification: user has no email; skipping")
		return nil
	}

	token, err := c.signer.MintEmailVerify(evt.UserID, email, c.ttl)
	if err != nil {
		return fmt.Errorf("consumer/email_verification: mint token for %q: %w", evt.UserID, err)
	}

	vars := map[string]any{
		"verifyUrl": appendToken(c.verifyURL, token),
		"ttlHours":  int(c.ttl.Hours()),
	}
	if name != "" {
		vars["recipientName"] = name
	}

	// Stable per-user dedupRef: the Sender's own TTL deduper (mail.WithDeduper)
	// collapses a near-simultaneous redelivery of this same event into a single
	// send; see the type doc for why no stronger, durable per-account gate is
	// applied here.
	dedupRef := "email-verification:" + evt.UserID
	if err := c.sender.Send(ctx, emailVerificationKind, evt.UserID, email, dedupRef, vars); err != nil {
		return fmt.Errorf("consumer/email_verification: send to %q: %w", email, err)
	}
	logger.Info().Str("user_id", evt.UserID).Msg("consumer/email_verification: email-verification sent")
	return nil
}

// appendToken appends "?token=<token>" (or "&token=<token>" if base already
// has a query string) to base. Mirrors internal/mail/sender.go's own
// appendToken helper (unexported there, so duplicated here rather than
// exported solely for this one caller).
func appendToken(base, token string) string {
	sep := "?"
	if u, err := url.Parse(base); err == nil && u.RawQuery != "" {
		sep = "&"
	}
	return base + sep + "token=" + url.QueryEscape(token)
}
