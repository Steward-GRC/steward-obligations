// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
)

// ---------------------------------------------------------------------------
// Wire-format types
// ---------------------------------------------------------------------------

// accountCreatedEvent mirrors the payload Identity publishes on the
// "account.created" routing key: {"event_type": "account.created", "user_id":
// "<uuid>"}. Identity emits it from every account-creation route (/setup
// bootstrap, admin local create, SAML/JIT federated first login), so a
// single consumer covers them all. The event intentionally carries no PII — the
// recipient's name and email are resolved from Identity by user id, exactly
// like the welcome-resend RPC.
type accountCreatedEvent struct {
	EventType string `json:"event_type"`
	UserID    string `json:"user_id"`
}

// welcomeAccountKind is the branded welcome template. It is a bypass-suppression
// kind (see internal/mail/suppressor.go): a brand-new account's welcome always
// sends regardless of email preference or quiet hours, since the user has not
// had a chance to set a preference yet.
const welcomeAccountKind = "welcome-account"

// fallbackWelcomeName is stamped as recipientName when Identity has no display
// name; the welcome-account template interpolates it into the greeting and must
// never be empty. Mirrors grpcsvc.fallbackWelcomeName.
const fallbackWelcomeName = "there"

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// WelcomeRecipientResolver resolves a user's display name and email address.
// The identity gRPC adapter in cmd/server satisfies it via GetUser (the same
// adapter the welcome-resend RPC uses).
type WelcomeRecipientResolver interface {
	ResolveWelcomeRecipient(ctx context.Context, userID string) (name, email string, err error)
}

// WelcomeGate is the durable, once-per-account claim the consumer uses so an
// account is welcomed exactly once even under create retries, event
// redelivery, or repeated JIT. *store.WelcomeSentStore satisfies it. Claim
// returns true for the first caller only; Release rolls a claim back so a
// failed send can be retried on redelivery.
type WelcomeGate interface {
	Claim(ctx context.Context, userID string) (bool, error)
	Release(ctx context.Context, userID string) error
}

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// AccountCreatedConsumer handles AMQP messages from the "jobs" exchange on the
// "account.created" routing key. On each event it claims the per-account
// welcome guard and, if it won the claim, resolves the recipient and sends the
// welcome-account email through the branded mail Sender. A losing claim
// (already welcomed — including by the SSO lifecycle consumer's
// sso-account-welcome, which shares the same guard) is skipped, so a user
// never receives more than one welcome. A resolution/send failure releases the
// claim and returns an error so the message is requeued and retried.
type AccountCreatedConsumer struct {
	users      WelcomeRecipientResolver
	sender     Sender
	gate       WelcomeGate
	accountURL string
}

// NewAccountCreatedConsumer constructs an AccountCreatedConsumer. sender is the
// shared *mail.Sender (satisfying the local Sender interface); accountURL is the
// portal landing page stamped as the welcome email's call-to-action.
func NewAccountCreatedConsumer(users WelcomeRecipientResolver, sender Sender, gate WelcomeGate, accountURL string) *AccountCreatedConsumer {
	return &AccountCreatedConsumer{users: users, sender: sender, gate: gate, accountURL: accountURL}
}

// Handle processes a single raw AMQP message body (JSON accountCreatedEvent).
// It is idempotent: the durable WelcomeGate makes repeated deliveries (or a
// second account.created for the same user from a create retry) a no-op after
// the first welcome. A malformed body or missing user id is a permanent error
// that must not requeue forever, so it is logged and swallowed (return nil); a
// transient resolve/send failure returns an error so the message is requeued.
func (c *AccountCreatedConsumer) Handle(ctx context.Context, body []byte) error {
	logger := logctx.From(ctx)

	var evt accountCreatedEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		logger.Warn().Err(err).Msg("consumer/account_created: unmarshal; skipping")
		return nil
	}
	if evt.UserID == "" {
		logger.Warn().Msg("consumer/account_created: missing user_id; skipping")
		return nil
	}

	// Durable once-per-account claim. A losing claim means this account was
	// already welcomed (by a redelivery, a create retry, or the SSO welcome
	// that shares this guard) — nothing to do.
	claimed, err := c.gate.Claim(ctx, evt.UserID)
	if err != nil {
		return fmt.Errorf("consumer/account_created: claim welcome %q: %w", evt.UserID, err)
	}
	if !claimed {
		logger.Debug().Str("user_id", evt.UserID).Msg("consumer/account_created: already welcomed; skipping")
		return nil
	}

	if err := c.sendWelcome(ctx, evt.UserID); err != nil {
		// Roll the claim back so a redelivery can re-claim and retry; otherwise
		// the account would be marked welcomed with no email ever sent.
		if relErr := c.gate.Release(ctx, evt.UserID); relErr != nil {
			logger.Error().Err(relErr).Str("user_id", evt.UserID).
				Msg("consumer/account_created: release welcome claim after send failure")
		}
		return err
	}
	return nil
}

// sendWelcome resolves the recipient from Identity and sends the welcome-account
// email. A user with no email address is not an error the broker can fix, so it
// is logged and treated as done (the claim stands — there is no address to ever
// deliver to, and an admin can resend once the profile is fixed).
func (c *AccountCreatedConsumer) sendWelcome(ctx context.Context, userID string) error {
	logger := logctx.From(ctx)

	name, email, err := c.users.ResolveWelcomeRecipient(ctx, userID)
	if err != nil {
		return fmt.Errorf("consumer/account_created: resolve recipient %q: %w", userID, err)
	}
	if email == "" {
		logger.Warn().Str("user_id", userID).Msg("consumer/account_created: user has no email; skipping welcome")
		return nil
	}
	if name == "" {
		name = fallbackWelcomeName
	}

	vars := map[string]any{
		"recipientName": name,
		"accountUrl":    c.accountURL,
	}
	// Stable dedupRef so the Sender's own TTL deduper is a second, short-window
	// line of defense against a near-simultaneous duplicate; the durable
	// WelcomeGate is the primary once-per-account guarantee.
	if err := c.sender.Send(ctx, welcomeAccountKind, userID, email, "account-created", vars); err != nil {
		return fmt.Errorf("consumer/account_created: send welcome to %q: %w", email, err)
	}
	logger.Info().Str("user_id", userID).Msg("consumer/account_created: welcome-account sent")
	return nil
}
