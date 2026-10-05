// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// kindKratosRecovery is the branded template a Kratos password-recovery code
// is rendered + sent with. It always sends -- internal/mail's Suppress hook
// bypasses opt-out/quiet-hours for it (see internal/mail/suppressor.go) --
// because a user who asked to reset their password must receive the code
// regardless of their notification preferences.
const kindKratosRecovery = "kratos-recovery"

// authRecoveryEvent mirrors the payload the gateway publishes on the
// "auth.recovery" routing key: {"event":"auth.recovery","vars":{...}}. vars
// carries {email, recipientName, recoveryCode, expiresInMinutes} and is
// forwarded as-is to Sender.Send, so the render sidecar's template tolerates
// missing optional fields -- the same loosely-typed contract the SSO
// lifecycle consumer uses.
type authRecoveryEvent struct {
	Event string         `json:"event"`
	Vars  map[string]any `json:"vars"`
}

// AuthRecoveryConsumer handles AMQP messages from the "jobs" exchange on the
// "auth.recovery" routing key, published by the gateway after it mints a
// Kratos recovery code via the Kratos admin API. Each message renders the
// kratos-recovery template and delivers the code to the requesting user
// through our native email platform (mechanism B) -- the same
// consumer+Sender.Send idiom as SSOLifecycleConsumer, and the same pattern the
// Phase-6 mass force-reset will reuse.
type AuthRecoveryConsumer struct {
	sender Sender
	users  UserEmailResolver
}

// NewAuthRecoveryConsumer constructs an AuthRecoveryConsumer. users may be nil
// when every event is expected to carry the recipient email directly in vars
// (the gateway resolves it from the Kratos identity before publishing).
func NewAuthRecoveryConsumer(sender Sender, users UserEmailResolver) *AuthRecoveryConsumer {
	return &AuthRecoveryConsumer{sender: sender, users: users}
}

// Handle processes a single raw AMQP message body (JSON authRecoveryEvent).
// The recipient is taken from vars.email; if absent but vars.userId is present
// it falls back to UserEmailResolver. An unresolvable recipient or an
// unexpected event name is logged and swallowed (return nil) rather than
// requeued -- neither is the sort of transient failure a redelivery would
// fix, and requeuing a permanently-unroutable message would spin forever. A
// Sender failure IS propagated so the delivery is retried.
//
// dedupRef is UNIQUE per request: it includes the recovery code, so a user who
// requests several resets in quick succession receives every distinct code
// (internal/mail's Deduper never collapses two different codes into one send,
// and the bypass-suppression membership means opt-out/quiet-hours never drop
// it either), while a redelivery of the SAME message (same code) stays
// idempotent.
func (c *AuthRecoveryConsumer) Handle(ctx context.Context, body []byte) error {
	var evt authRecoveryEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/auth_recovery: unmarshal: %w", err)
	}

	logger := logctx.From(ctx)

	// Tolerate an absent event name (treat as auth.recovery), but skip any
	// other explicitly-named event so a mis-bound queue never mis-sends.
	if evt.Event != "" && evt.Event != "auth.recovery" {
		logger.Warn().Str("event", evt.Event).Msg("consumer/auth_recovery: unexpected event; skipping")
		return nil
	}

	to := stringVar(evt.Vars, "email")
	userID := stringVar(evt.Vars, "userId")
	if to == "" && userID != "" && c.users != nil {
		resolved, err := c.users.ResolveEmail(ctx, userID)
		if err != nil {
			logger.Warn().Err(err).Str("user_id", userID).
				Msg("consumer/auth_recovery: resolve email failed; skipping")
			return nil
		}
		to = resolved
	}
	if to == "" {
		logger.Warn().Msg("consumer/auth_recovery: no recipient email on event; skipping")
		return nil
	}

	code := stringVar(evt.Vars, "recoveryCode")
	dedupRef := dedupKey("auth-recovery", to, code)

	if err := c.sender.Send(ctx, kindKratosRecovery, userID, to, dedupRef, evt.Vars); err != nil {
		// Phase 7: a paused send is already HELD in the durable outbox by the
		// pause-gate and drains on recovery, so ACK it (return nil) rather than
		// requeue — a requeue would hot-loop through a multi-minute outage.
		if mail.IsPaused(err) {
			logger.Warn().Str("to", to).Msg("consumer/auth_recovery: send paused — held in outbox, ack")
			return nil
		}
		return fmt.Errorf("consumer/auth_recovery: send kind %q to %q: %w", kindKratosRecovery, to, err)
	}
	return nil
}
