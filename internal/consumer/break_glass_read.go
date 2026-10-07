// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// kindBreakGlassReadAlert is the security notice for a document read under a
// break-glass grant. It always sends (see internal/mail's suppressor).
const kindBreakGlassReadAlert = "break-glass-read-alert"

// breakGlassReadTimeLayout renders read_at for the email, always in UTC.
const breakGlassReadTimeLayout = "January 2, 2006 at 3:04 PM UTC"

// breakGlassReadEvent mirrors core's policy.break_glass_read event on the
// "jobs" exchange. It is kept local so this package has no core import.
type breakGlassReadEvent struct {
	EventID          string    `json:"event_id"`
	ReadAt           time.Time `json:"read_at"`
	PolicyID         string    `json:"policy_id"`
	PolicyVersionID  string    `json:"policy_version_id"`
	Number           string    `json:"number"`
	Title            string    `json:"title"`
	DocumentType     string    `json:"document_type"`
	OwnerUserID      string    `json:"owner_user_id"`
	ReaderUserID     string    `json:"reader_user_id"`
	ActAsAdminUserID string    `json:"act_as_admin_user_id"`
}

// ComplianceAdminResolver returns the email address of every enabled user
// holding the compliance-admin role.
type ComplianceAdminResolver interface {
	ListComplianceAdminEmails(ctx context.Context) ([]string, error)
}

// BreakGlassReadConsumer handles core's "policy.break_glass_read" events: one
// alert per read to the document's owner and every compliance admin. Each
// read is its own alert (the dedup key is the event id plus the recipient), so
// a redelivery never sends twice and two reads are never folded into one.
type BreakGlassReadConsumer struct {
	sender     Sender
	users      UserEmailResolver
	compliance ComplianceAdminResolver
}

// NewBreakGlassReadConsumer returns a BreakGlassReadConsumer.
func NewBreakGlassReadConsumer(sender Sender, users UserEmailResolver, compliance ComplianceAdminResolver) *BreakGlassReadConsumer {
	return &BreakGlassReadConsumer{sender: sender, users: users, compliance: compliance}
}

// Handle processes one event. A failed compliance-admin lookup or send is
// returned so the message is retried; the dedup key keeps a retry from
// alerting anyone twice.
func (c *BreakGlassReadConsumer) Handle(ctx context.Context, body []byte) error {
	var evt breakGlassReadEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/break_glass_read: unmarshal: %w", err)
	}
	if evt.EventID == "" || evt.PolicyID == "" {
		return errors.New("consumer/break_glass_read: event_id and policy_id are required")
	}
	logger := logctx.From(ctx)
	logger.Info().Str("event_id", evt.EventID).Str("policy_id", evt.PolicyID).
		Bool("act_as", evt.ActAsAdminUserID != "").Msg("consumer/break_glass_read: alerting")

	var lookupErr error
	recipients := []string{}
	owner, err := c.email(ctx, evt.OwnerUserID)
	if err != nil {
		lookupErr = err
	}
	if owner != "" {
		recipients = append(recipients, owner)
	}
	admins, err := c.compliance.ListComplianceAdminEmails(ctx)
	if err != nil {
		logger.Error().Err(err).Str("event_id", evt.EventID).Msg("consumer/break_glass_read: list compliance admins failed; will retry")
		if lookupErr == nil {
			lookupErr = err
		}
	}
	for _, a := range admins {
		if a != "" && !slices.Contains(recipients, a) {
			recipients = append(recipients, a)
		}
	}
	if len(recipients) == 0 {
		logger.Error().Str("event_id", evt.EventID).Msg("consumer/break_glass_read: no resolvable recipients")
		return lookupErr
	}

	vars := map[string]any{
		"number": evt.Number,
		"title":  evt.Title,
		"at":     evt.ReadAt.UTC().Format(breakGlassReadTimeLayout),
	}
	if evt.ReaderUserID != "" {
		vars["reader"] = c.nameOrID(ctx, evt.ReaderUserID)
	}
	if evt.ActAsAdminUserID != "" {
		vars["actAsAdmin"] = c.nameOrID(ctx, evt.ActAsAdminUserID)
	}

	firstErr := lookupErr
	for _, to := range recipients {
		dedupRef := fmt.Sprintf("break-glass-read:%s:%s", evt.EventID, to)
		if err := c.sender.Send(ctx, kindBreakGlassReadAlert, "", to, dedupRef, vars); err != nil {
			if mail.IsPaused(err) {
				logger.Warn().Str("event_id", evt.EventID).Msg("consumer/break_glass_read: alert paused, held in outbox")
				continue
			}
			logger.Error().Err(err).Str("event_id", evt.EventID).Msg("consumer/break_glass_read: send failed")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("consumer/break_glass_read: %w", firstErr)
	}
	return nil
}

// email resolves a recipient's address; "" with no error when there is no
// user id.
func (c *BreakGlassReadConsumer) email(ctx context.Context, userID string) (string, error) {
	if userID == "" || c.users == nil {
		return "", nil
	}
	e, err := c.users.ResolveEmail(ctx, userID)
	if err != nil {
		logger := logctx.From(ctx)
		logger.Error().Err(err).Str("user_id", userID).Msg("consumer/break_glass_read: resolve owner email failed; will retry")
		return "", err
	}
	return e, nil
}

// nameOrID is the email of a user named in the alert, or the user id when it
// can't be resolved, so the alert always says who read the document.
func (c *BreakGlassReadConsumer) nameOrID(ctx context.Context, userID string) string {
	if c.users == nil {
		return userID
	}
	e, err := c.users.ResolveEmail(ctx, userID)
	if err != nil || e == "" {
		return userID
	}
	return e
}
