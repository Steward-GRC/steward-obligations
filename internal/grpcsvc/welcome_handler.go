// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"
	"fmt"
	"time"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// welcomeKind is the template kind rendered + sent for a welcome email. It is
// the one kind that always sends (the Suppressor bypasses opt-out/quiet-hours
// and the new-user throttle for it), so an admin resend is never silently
// swallowed. See internal/mail/suppressor.go.
const welcomeKind = "welcome-account"

// fallbackWelcomeName is stamped as recipientName when identity has no display
// name — the welcome-account template interpolates it into the greeting and
// must never be empty.
const fallbackWelcomeName = "there"

// WelcomeUserResolver resolves a user's display name and email address. The
// identity gRPC adapter in cmd/server satisfies it via GetUser.
type WelcomeUserResolver interface {
	ResolveWelcomeRecipient(ctx context.Context, userID string) (name, email string, err error)
}

// WelcomeSender renders a template kind against vars and sends it — the subset
// of *mail.Sender the welcome handler needs.
type WelcomeSender interface {
	Send(ctx context.Context, kind, userID, to, dedupRef string, vars any) error
}

// WelcomeHandler implements obligationsv1.WelcomeServiceServer: an admin action
// that re-sends the welcome-account onboarding email to a single user.
type WelcomeHandler struct {
	obligationsv1.UnimplementedWelcomeServiceServer
	users      WelcomeUserResolver
	sender     WelcomeSender
	accountURL string
}

// NewWelcomeHandler returns a handler that resolves the recipient via users,
// renders/sends via sender, and stamps accountURL as the email's call-to-action.
func NewWelcomeHandler(users WelcomeUserResolver, sender WelcomeSender, accountURL string) *WelcomeHandler {
	return &WelcomeHandler{users: users, sender: sender, accountURL: accountURL}
}

// ResendWelcome re-sends the welcome-account email to req.UserId. It resolves
// the recipient's name + email from identity, then hands the render+send to the
// mail Sender. Because it is an explicit admin action, it uses a unique dedup
// reference so the Deduper never suppresses a repeat send.
func (h *WelcomeHandler) ResendWelcome(ctx context.Context, req *obligationsv1.ResendWelcomeRequest) (*obligationsv1.ResendWelcomeResponse, error) {
	userID := req.GetUserId()
	if userID == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	name, email, err := h.users.ResolveWelcomeRecipient(ctx, userID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve welcome recipient: %v", err)
	}
	if email == "" {
		return nil, status.Error(codes.FailedPrecondition, "user has no email address to send to")
	}
	if name == "" {
		name = fallbackWelcomeName
	}

	vars := map[string]any{
		"recipientName": name,
		"accountUrl":    h.accountURL,
	}

	// Unique per-invocation ref: an admin resend must go out even if this user
	// already received a welcome email (kind welcome-account already bypasses
	// the opt-out suppressor + new-user throttle in the Sender's hook chain).
	dedupRef := fmt.Sprintf("resend-welcome:%s:%d", userID, time.Now().UnixNano())

	if err := h.sender.Send(ctx, welcomeKind, userID, email, dedupRef, vars); err != nil {
		// Phase 7: when there is no working transport the pause-gate durably
		// holds the rendered welcome email in the outbox and it drains on
		// recovery. Report success (it is guaranteed to send) rather than an
		// error the admin would read as "lost".
		if mail.IsPaused(err) {
			logger := logctx.From(ctx)
			logger.Warn().Str("user_id", userID).Msg("grpcsvc/welcome: send paused — held in outbox, will drain on recovery")
			return &obligationsv1.ResendWelcomeResponse{Sent: true}, nil
		}
		// The cause is logged by the registry, never sent: the caller gets
		// WELCOME_SEND_UNAVAILABLE with the user id.
		return nil, errcodes.Error(ctx, errcodes.WelcomeSendUnavailable(userID, err))
	}
	return &obligationsv1.ResendWelcomeResponse{Sent: true}, nil
}
