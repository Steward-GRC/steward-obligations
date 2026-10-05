// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail

import (
	"context"
	"fmt"

	email "github.com/Bugs5382/go-email"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
)

// kindWelcomeAccount is the one template kind that always sends regardless
// of the recipient's email preference or quiet hours. Co-design's rule: a
// fresh account's welcome email is the one message a brand-new user actually
// wants immediately, and they haven't had a chance to set a preference yet.
const kindWelcomeAccount = "welcome-account"

// kindSSOAccountWelcome is sso-account-welcome (internal/consumer's SSO
// lifecycle consumer, ): a JIT-provisioned SSO account's first email,
// delivered the same way welcome-account is -- the recipient hasn't had a
// chance to set a preference yet.
const kindSSOAccountWelcome = "sso-account-welcome"

// kindBreakGlassAlert is break-glass-alert (internal/consumer's SSO
// lifecycle consumer, ): a non-optional security notice for a
// break-glass login. It always sends -- a recipient must never be able to
// opt out of, or quiet-hours-delay, an alert about a break-glass credential
// being used.
const kindBreakGlassAlert = "break-glass-alert"

// kindKratosRecovery is kratos-recovery (internal/consumer's auth-recovery
// consumer): the password-recovery code a user explicitly requested. It
// always sends -- a recipient who asked to reset their password must receive
// the code regardless of email preference or quiet hours (and the code
// expires, so a quiet-hours delay would defeat the request). Same treatment
// as a login OTP.
const kindKratosRecovery = "kratos-recovery"

// bypassSuppressionKinds are every template kind the Suppress hook always
// delivers regardless of the recipient's email preference or quiet hours.
// Membership here means "security/transactional, not a discretionary
// notification" -- see each kind's own doc comment for why.
var bypassSuppressionKinds = map[string]struct{}{
	kindWelcomeAccount:    {},
	kindSSOAccountWelcome: {},
	kindBreakGlassAlert:   {},
	kindKratosRecovery:    {},
}

// PrefSource reports whether userID currently has the email channel enabled.
// It is satisfied in production by an adapter over the obligations service's
// NotifPref store (cmd/server wiring); tests supply a fake. Implementations
// should apply the same default-on (opt-out) semantics the store already
// does -- a user with no explicit preference is enabled -- so this package
// never needs to special-case "no row".
type PrefSource interface {
	EmailEnabled(ctx context.Context, userID string) (bool, error)
}

// QuietHoursSource reports whether "now" falls in userID's quiet-hours
// window. internal/notify.QuietHours (the same type internal/notify's
// Dispatcher uses for its own pref/quiet-hours gate) satisfies this
// interface, so both enforcement points share one definition of "quiet
// hours" instead of drifting apart.
type QuietHoursSource interface {
	InWindow(ctx context.Context, userID string) bool
}

// suppressByPrefsAndQuietHours returns a Middleware enforcing the SP-3
// compliance rule: skip (return email.ErrSuppressed) a send whose recipient
// has email notifications off, or who is in quiet hours -- except the kinds
// in bypassSuppressionKinds ("welcome-account", "sso-account-welcome",
// "break-glass-alert", "kratos-recovery"), which always send.
//
// Unlike go-email's own address-oriented Suppress, this reads kind and
// user_id from m.Meta (stamped by Sender.Send): the welcome bypass is keyed
// on kind, and the preference/quiet-hours lookup is keyed on the user, not
// the destination address, so go-email's per-address Suppressor interface
// does not fit here.
//
// prefs and quiet may each be nil (NewSender's default when WithSuppressor
// is not supplied): a nil prefs skips the preference check, a nil quiet
// skips the quiet-hours check, and a Message with no user_id in Meta (an
// empty userID passed to Sender.Send) always sends -- there is no user to
// evaluate either check against.
func suppressByPrefsAndQuietHours(prefs PrefSource, quiet QuietHoursSource) email.Middleware {
	return func(next email.SendFunc) email.SendFunc {
		return func(ctx context.Context, m *email.Message) error {
			kind, _ := m.Meta["kind"].(string)
			if _, bypass := bypassSuppressionKinds[kind]; bypass {
				return next(ctx, m)
			}

			userID, _ := m.Meta["user_id"].(string)
			if userID == "" {
				return next(ctx, m)
			}

			if prefs != nil {
				enabled, err := prefs.EmailEnabled(ctx, userID)
				if err != nil {
					return fmt.Errorf("mail: check email pref for %q: %w", userID, err)
				}
				if !enabled {
					return email.ErrSuppressed
				}
			}

			if quiet != nil && quiet.InWindow(ctx, userID) {
				return email.ErrSuppressed
			}

			return next(ctx, m)
		}
	}
}

// suppressByResolver is the send-time backstop built on the
// shared notifpolicy.Resolver: it suppresses (returns email.ErrSuppressed) any
// email send the resolver does not clear for the email channel -- the email
// channel switch is off, the recipient is in quiet hours (unless bypassed), or
// the type's category cadence is off (only reachable for optional types; the
// compliance floor and the mandatory security/transactional force-deliver live
// in the resolver). It reads kind and user_id from m.Meta exactly like
// suppressByPrefsAndQuietHours, and preserves that hook's two guards: a message
// with no user_id always sends (no user to evaluate), and the mandatory
// security/transactional set is force-delivered by the resolver so a recovery
// code or break-glass alert never gets suppressed. This supersedes the
// hard-coded bypassSuppressionKinds list, now generalized in the taxonomy.
func suppressByResolver(r *notifpolicy.Resolver) email.Middleware {
	return func(next email.SendFunc) email.SendFunc {
		return func(ctx context.Context, m *email.Message) error {
			userID, _ := m.Meta["user_id"].(string)
			if userID == "" {
				return next(ctx, m)
			}
			kind, _ := m.Meta["kind"].(string)
			dec, err := r.Resolve(ctx, userID, kind)
			if err != nil {
				return fmt.Errorf("mail: resolve prefs for %q: %w", userID, err)
			}
			if !dec.Email {
				return email.ErrSuppressed
			}
			return next(ctx, m)
		}
	}
}
