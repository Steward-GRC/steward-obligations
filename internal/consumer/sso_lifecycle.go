// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/Steward-GRC/steward-obligations/internal/logctx"
	"github.com/google/uuid"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// ---------------------------------------------------------------------------
// Wire-format types
// ---------------------------------------------------------------------------

// ssoLifecycleEvent mirrors the payload Identity publishes on the
// "sso.lifecycle" routing key: {"event": "<name>", "vars": {...}}. vars is
// left as a loosely-typed map -- each event carries a different shape, and
// forwarding it as-is to Sender.Send lets the render sidecar's template
// tolerate missing optional fields rather than this consumer having to know
// every template's exact schema.
type ssoLifecycleEvent struct {
	Event string         `json:"event"`
	Vars  map[string]any `json:"vars"`
}

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// Sender is the subset of *mail.Sender the SSO lifecycle consumer needs:
// render a template kind against vars and deliver it through the compliance
// hooks (audit/suppress/dedupe). Mirrors notify.Sender's signature so
// *mail.Sender satisfies it with no adapter, but is declared locally so this
// package has no compile-time dependency on internal/notify's own Sender
// type (following PolicyPublishedConsumer's pattern of locally-scoped
// dependency interfaces).
type Sender interface {
	Send(ctx context.Context, kind, userID, to, dedupRef string, vars any) error
}

// UserEmailResolver maps a user_id to an email address. Most sso.lifecycle
// events already carry the recipient's email directly in vars (identity
// resolves it before publishing), so this is only consulted when an event
// supplies a userID but no email.
type UserEmailResolver interface {
	ResolveEmail(ctx context.Context, userID string) (string, error)
}

// SiteAdminResolver returns every current site-admin's email address. It
// backs the break-glass fan-out (every site-admin AND the account) and the
// domain/IdP-lifecycle admin notices, which have no other recipient signal
// on the wire (no per-org "contact" concept exists yet upstream). No
// production-ready implementation exists in the obligations service or the
// current identity contract (no ListSiteAdmins-shaped RPC) -- see
// cmd/server's wiring comment for the interim stub.
type SiteAdminResolver interface {
	ListSiteAdminEmails(ctx context.Context) ([]string, error)
}

// ---------------------------------------------------------------------------
// Template kinds
// ---------------------------------------------------------------------------

const (
	kindSSOAccountWelcome       = "sso-account-welcome"
	kindAccessGranted           = "access-granted"
	kindBreakGlassAlert         = "break-glass-alert"
	kindDomainVerificationInstr = "domain-verification-instructions"
	kindDomainVerified          = "domain-verified"
	kindIdPTestFailed           = "idp-test-failed"
	kindSSOActivated            = "sso-activated"
	kindSSODisabled             = "sso-disabled"
	kindSPCertRotated           = "sp-cert-rotated"
)

// ---------------------------------------------------------------------------
// Consumer
// ---------------------------------------------------------------------------

// SSOLifecycleConsumer handles AMQP messages from the "jobs" exchange on the
// "sso.lifecycle" routing key, published by Identity as SAML/JIT
// SSO events occur. Each event maps to one branded template kind and one or
// more recipients:
//
// - sso.account_provisioned -> sso-account-welcome to the new user
// (delivered like welcome-account: internal/mail's Suppress hook must
// bypass opt-out/quiet-hours for this kind too). loginURL, when wired,
// is merged in as the template's "Sign in with SSO" CTA
//
// .
// - sso.access_granted -> access-granted to the user (respects prefs).
// - user.break_glass.login -> break-glass-alert fanned out to every
// site-admin AND the account itself; non-optional, bypasses suppression
// the same way welcome-account does, and MUST NOT be deduplicated across
// occurrences (every break-glass login is its own security alert).
// - sso.domain_verification_requested / domain_verified / idp_test_failed /
// activated / disabled / sp_cert_rotated -> the matching admin/org-contact
// kind, fanned out to every site-admin (no other recipient signal is on
// the wire for these).
//
// An event name outside this set is logged and skipped rather than treated
// as an error, so a future Identity emit does not wedge the queue on this
// consumer's redeploy lag.
type SSOLifecycleConsumer struct {
	sender     Sender
	users      UserEmailResolver
	siteAdmins SiteAdminResolver
	// welcome is the durable once-per-account guard shared with the
	// account-created consumer. When wired, an
	// sso.account_provisioned welcome claims through it so a federated
	// first-login never receives BOTH sso-account-welcome and welcome-account:
	// whichever signal is processed first wins the claim and sends its
	// template; the other is skipped. Nil preserves the original always-send
	// behavior.
	welcome WelcomeGate
	// loginURL is the app's login page, merged into sso-account-welcome's vars
	// as `loginUrl` so the template can render its "Sign in with SSO" CTA
	// . Empty (the default) omits the CTA -- the
	// template still renders the how-to text, just without a button.
	loginURL string
}

// NewSSOLifecycleConsumer constructs an SSOLifecycleConsumer.
func NewSSOLifecycleConsumer(sender Sender, users UserEmailResolver, siteAdmins SiteAdminResolver) *SSOLifecycleConsumer {
	return &SSOLifecycleConsumer{sender: sender, users: users, siteAdmins: siteAdmins}
}

// WithWelcomeGate wires the shared once-per-account welcome guard so the
// sso-account-welcome dedupes against the account-created consumer's
// welcome-account. Nil (the default) keeps the original always-send behavior.
// Returns the consumer for chaining.
func (c *SSOLifecycleConsumer) WithWelcomeGate(g WelcomeGate) *SSOLifecycleConsumer {
	c.welcome = g
	return c
}

// WithLoginURL wires the app's login page so sso-account-welcome can render
// its "Sign in with SSO" CTA. Unwired (the default,
// "") leaves the welcome's vars untouched and the template renders without a
// button. Returns the consumer for chaining.
func (c *SSOLifecycleConsumer) WithLoginURL(url string) *SSOLifecycleConsumer {
	c.loginURL = url
	return c
}

// Handle processes a single raw AMQP message body (JSON ssoLifecycleEvent).
// It is idempotent for every event except user.break_glass.login: the
// idempotent lifecycle kinds dedupe on (event, recipient), while break-glass
// deliberately uses a unique dedupRef per call so a redelivered message and a
// genuinely repeated break-glass login are indistinguishable to this
// consumer -- both must alert. Recipient resolution failures and unknown
// events are logged and swallowed (return nil) rather than propagated as
// handler errors: they are not the sort of transient failure a requeue would
// fix, and requeuing a permanently-unroutable event would spin forever.
func (c *SSOLifecycleConsumer) Handle(ctx context.Context, body []byte) error {
	var evt ssoLifecycleEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return fmt.Errorf("consumer/sso_lifecycle: unmarshal: %w", err)
	}

	logger := logctx.From(ctx)

	switch evt.Event {
	case "sso.account_provisioned":
		return c.sendAccountWelcome(ctx, evt)
	case "sso.access_granted":
		return c.sendToVarsEmail(ctx, evt, kindAccessGranted)
	case "user.break_glass.login":
		return c.handleBreakGlass(ctx, evt)
	case "sso.domain_verification_requested":
		return c.fanOutToSiteAdmins(ctx, evt, kindDomainVerificationInstr, domainRef(evt.Vars))
	case "sso.domain_verified":
		return c.fanOutToSiteAdmins(ctx, evt, kindDomainVerified, domainRef(evt.Vars))
	case "sso.idp_test_failed":
		return c.fanOutToSiteAdmins(ctx, evt, kindIdPTestFailed, stringVar(evt.Vars, "connectionId"))
	case "sso.activated":
		return c.fanOutToSiteAdmins(ctx, evt, kindSSOActivated, domainRef(evt.Vars))
	case "sso.disabled":
		return c.fanOutToSiteAdmins(ctx, evt, kindSSODisabled, domainRef(evt.Vars))
	case "sso.sp_cert_rotated":
		return c.fanOutToSiteAdmins(ctx, evt, kindSPCertRotated, stringVar(evt.Vars, "serial"))
	default:
		logger.Warn().Str("event", evt.Event).Msg("consumer/sso_lifecycle: unknown event; skipping")
		return nil
	}
}

// sendAccountWelcome sends the sso-account-welcome for a freshly provisioned
// federated account, deduped once-per-account against the account-created
// consumer's welcome-account via the shared WelcomeGate. With no gate wired (or
// no user id on the event) it is exactly sendToVarsEmail — the original
// always-send behavior. When a gate is wired it claims by user id first: a lost
// claim means the account was already welcomed (by welcome-account, the SSO
// welcome, or a redelivery) and is skipped; a send failure releases the claim
// so a redelivery can retry.
func (c *SSOLifecycleConsumer) sendAccountWelcome(ctx context.Context, evt ssoLifecycleEvent) error {
	userID := stringVar(evt.Vars, "userId")
	evt = withLoginURL(evt, c.loginURL)
	if c.welcome == nil || userID == "" {
		return c.sendToVarsEmail(ctx, evt, kindSSOAccountWelcome)
	}

	logger := logctx.From(ctx)
	claimed, err := c.welcome.Claim(ctx, userID)
	if err != nil {
		return fmt.Errorf("consumer/sso_lifecycle: claim welcome %q: %w", userID, err)
	}
	if !claimed {
		logger.Debug().Str("user_id", userID).Msg("consumer/sso_lifecycle: account already welcomed; skipping")
		return nil
	}
	if err := c.sendToVarsEmail(ctx, evt, kindSSOAccountWelcome); err != nil {
		if relErr := c.welcome.Release(ctx, userID); relErr != nil {
			logger.Error().Err(relErr).Str("user_id", userID).
				Msg("consumer/sso_lifecycle: release welcome claim after send failure")
		}
		return err
	}
	return nil
}

// sendToVarsEmail resolves the recipient directly from vars.email (identity
// resolves the address before publishing) and sends kind to them once,
// deduped per (event, recipient). If vars carries no email and a userID is
// present, it falls back to UserEmailResolver; an unresolvable recipient is
// logged and skipped rather than failing the whole handler.
func (c *SSOLifecycleConsumer) sendToVarsEmail(ctx context.Context, evt ssoLifecycleEvent, kind string) error {
	logger := logctx.From(ctx)

	to := stringVar(evt.Vars, "email")
	userID := stringVar(evt.Vars, "userId")
	if to == "" && userID != "" && c.users != nil {
		resolved, err := c.users.ResolveEmail(ctx, userID)
		if err != nil {
			logger.Warn().Err(err).Str("event", evt.Event).Str("user_id", userID).
				Msg("consumer/sso_lifecycle: resolve email failed; skipping")
			return nil
		}
		to = resolved
	}
	if to == "" {
		logger.Warn().Str("event", evt.Event).Msg("consumer/sso_lifecycle: no recipient email on event; skipping")
		return nil
	}

	if err := c.sender.Send(ctx, kind, userID, to, dedupKey(evt.Event, to), evt.Vars); err != nil {
		// Phase 7: a paused send is already held in the outbox; ACK it.
		if mail.IsPaused(err) {
			logger.Warn().Str("event", evt.Event).Str("to", to).Msg("consumer/sso_lifecycle: send paused — held in outbox, ack")
			return nil
		}
		return fmt.Errorf("consumer/sso_lifecycle: send kind %q to %q: %w", kind, to, err)
	}
	return nil
}

// handleBreakGlass fans user.break_glass.login out to every current
// site-admin AND the account whose break-glass login it was. It is
// non-optional: the caller (cmd/server wiring) must supply a Sender whose
// underlying *mail.Sender treats kindBreakGlassAlert as a bypass-suppression
// kind exactly like welcome-account (see internal/mail/suppressor.go),
// otherwise a recipient with email notifications off would silently miss a
// security alert they cannot opt out of. dedupRef is unique per call
// (occurrence id/timestamp from vars when identity supplies one, otherwise a
// generated UUID) so a genuinely repeated break-glass login is never
// collapsed into a single alert by the Deduper.
func (c *SSOLifecycleConsumer) handleBreakGlass(ctx context.Context, evt ssoLifecycleEvent) error {
	logger := logctx.From(ctx)

	accountEmail := stringVar(evt.Vars, "email")
	userID := stringVar(evt.Vars, "userId")
	if accountEmail == "" && userID != "" && c.users != nil {
		resolved, err := c.users.ResolveEmail(ctx, userID)
		if err != nil {
			logger.Warn().Err(err).Str("user_id", userID).
				Msg("consumer/sso_lifecycle: resolve break-glass account email failed")
		} else {
			accountEmail = resolved
		}
	}

	var admins []string
	if c.siteAdmins != nil {
		var err error
		admins, err = c.siteAdmins.ListSiteAdminEmails(ctx)
		if err != nil {
			logger.Error().Err(err).Msg("consumer/sso_lifecycle: list site admins failed; alerting account only")
		}
	}

	occurrence := breakGlassOccurrence(evt.Vars)

	recipients := make([]string, 0, len(admins)+1)
	recipients = append(recipients, admins...)
	if accountEmail != "" {
		recipients = append(recipients, accountEmail)
	}
	if len(recipients) == 0 {
		logger.Error().Msg("consumer/sso_lifecycle: break-glass alert has no resolvable recipients (no site-admins, no account email)")
		return nil
	}

	var firstErr error
	for _, to := range recipients {
		// dedupRef combines the occurrence with the recipient: unique per
		// occurrence (never collapses two distinct break-glass logins), and
		// keeps each fan-out recipient's own send independently idempotent
		// against redelivery of THIS message.
		dedupRef := fmt.Sprintf("break-glass:%s:%s", occurrence, to)
		if err := c.sender.Send(ctx, kindBreakGlassAlert, userID, to, dedupRef, evt.Vars); err != nil {
			// A paused send is held in the outbox and drains on recovery — not
			// a failure to requeue for; ACK it (no firstErr).
			if mail.IsPaused(err) {
				logger.Warn().Str("to", to).Msg("consumer/sso_lifecycle: break-glass alert paused — held in outbox")
				continue
			}
			logger.Error().Err(err).Str("to", to).Msg("consumer/sso_lifecycle: send break-glass alert failed")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("consumer/sso_lifecycle: break-glass alert: %w", firstErr)
	}
	return nil
}

// fanOutToSiteAdmins sends kind to every current site-admin, deduped per
// (event, ref, recipient). ref is the event's own identifying field (domain,
// connectionId, or serial) so re-verifying/re-rotating the same resource
// re-notifies instead of being permanently deduped by an empty/shared key.
// No site-admins resolver wired, or an empty admin list, is logged and
// swallowed -- there is no other recipient to fall back to for these
// org-level notices yet (see SiteAdminResolver's doc).
func (c *SSOLifecycleConsumer) fanOutToSiteAdmins(ctx context.Context, evt ssoLifecycleEvent, kind, ref string) error {
	logger := logctx.From(ctx)

	if c.siteAdmins == nil {
		logger.Warn().Str("event", evt.Event).Msg("consumer/sso_lifecycle: no site-admin resolver wired; skipping")
		return nil
	}
	admins, err := c.siteAdmins.ListSiteAdminEmails(ctx)
	if err != nil {
		return fmt.Errorf("consumer/sso_lifecycle: list site admins for %q: %w", evt.Event, err)
	}
	if len(admins) == 0 {
		logger.Warn().Str("event", evt.Event).Msg("consumer/sso_lifecycle: no site-admins to notify; skipping")
		return nil
	}

	var firstErr error
	for _, to := range admins {
		if err := c.sender.Send(ctx, kind, "", to, dedupKey(evt.Event, ref, to), evt.Vars); err != nil {
			if mail.IsPaused(err) {
				logger.Warn().Str("to", to).Str("event", evt.Event).Msg("consumer/sso_lifecycle: admin notice paused — held in outbox")
				continue
			}
			logger.Error().Err(err).Str("to", to).Str("event", evt.Event).Msg("consumer/sso_lifecycle: send admin notice failed")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("consumer/sso_lifecycle: admin fan-out for %q: %w", evt.Event, firstErr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stringVar reads a string field out of a loosely-typed vars map, returning
// "" for a missing/non-string field rather than panicking -- every optional
// template var must render gracefully when identity's emit doesn't carry it.
func stringVar(vars map[string]any, key string) string {
	if vars == nil {
		return ""
	}
	if v, ok := vars[key].(string); ok {
		return v
	}
	return ""
}

// domainRef reads the "domain" field common to the domain/IdP lifecycle
// events.
func domainRef(vars map[string]any) string {
	return stringVar(vars, "domain")
}

// withLoginURL returns evt with loginURL merged into a COPY of its vars map
// as "loginUrl" -- the original evt.Vars is never
// mutated, since other event kinds share the same underlying map shape and
// must not pick up a field meant only for sso-account-welcome. An empty
// loginURL (unwired) is a no-op, returning evt unchanged.
func withLoginURL(evt ssoLifecycleEvent, loginURL string) ssoLifecycleEvent {
	if loginURL == "" {
		return evt
	}
	vars := make(map[string]any, len(evt.Vars)+1)
	maps.Copy(vars, evt.Vars)
	vars["loginUrl"] = loginURL
	evt.Vars = vars
	return evt
}

// dedupKey joins an event's identifying parts into internal/mail's Deduper
// business key (combined with kind and userID by Sender.Send). Stable across
// redeliveries of the same logical event for the same recipient.
func dedupKey(parts ...string) string {
	var key strings.Builder
	for i, p := range parts {
		if i > 0 {
			key.WriteString(":")
		}
		key.WriteString(p)
	}
	return key.String()
}

// breakGlassOccurrence derives the unique-per-login identifier a break-glass
// alert's dedupRef is built from. It prefers an explicit occurrence id or
// timestamp from vars (were identity to add one -- see the Task-32 report's
// carry note: identity's current emit does not include either), falling
// back to a freshly generated UUID so two Handle calls for two distinct
// logins never collide even without that field.
func breakGlassOccurrence(vars map[string]any) string {
	if id := stringVar(vars, "occurrenceId"); id != "" {
		return id
	}
	if ts := stringVar(vars, "timestamp"); ts != "" {
		return ts
	}
	return uuid.NewString()
}
