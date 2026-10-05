// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifpolicy

import "context"

// Cadence is the per-category (or per-type-override) delivery frequency a user
// may choose.
type Cadence string

const (
	// CadenceUnspecified is the zero value; treated as "no explicit choice".
	CadenceUnspecified Cadence = ""
	CadenceImmediate   Cadence = "immediate"
	CadenceDaily       Cadence = "daily"
	CadenceWeekly      Cadence = "weekly"
	CadenceOff         Cadence = "off"
)

// Mode is how a resolved send is realized right now.
type Mode string

const (
	// ModeImmediate means send at once (subject to channels + quiet hours).
	ModeImmediate Mode = "immediate"
	// ModeBatch means the send belongs in the user's digest window. This is the
	// resolver's contract for digest-bound events; the actual outbox/drain is a
	// later slice, so callers without a batch
	// sink fall back to an immediate send (documented at the call sites).
	ModeBatch Mode = "batch"
)

// Channels are a user's channel master switches.
type Channels struct {
	Email bool
	InApp bool
	Push  bool
}

// DefaultCategoryCadence returns the fallback cadence for a category when the
// user has set no preference. Mandatory
// security/transactional are forced immediate; workflow is immediate;
// informational defaults to a daily roll-up; compliance defaults to immediate
// (the floor is daily, applied only when a user narrows it).
func DefaultCategoryCadence(cat Category) Cadence {
	switch cat {
	case CategoryInformational:
		return CadenceDaily
	default:
		return CadenceImmediate
	}
}

// PrefReader is the narrow persistence seam the resolver reads at send time.
// An adapter over the obligations service's stores satisfies it in production; tests
// supply a fake. All lookups are opt-out: a missing row means "no explicit
// choice" (found=false), never an error.
type PrefReader interface {
	// Channels returns the user's channel master switches, defaulting to
	// email/in-app on, push off when no row exists.
	Channels(ctx context.Context, userID string) (Channels, error)
	// CategoryCadence returns the user's cadence for a category; found is false
	// when the user has set none.
	CategoryCadence(ctx context.Context, userID string, cat Category) (cadence Cadence, found bool, err error)
	// TypeOverride returns the user's advanced per-type override; found is false
	// when the user has set none.
	TypeOverride(ctx context.Context, userID, kind string) (cadence Cadence, found bool, err error)
}

// QuietHours reports whether "now" falls in userID's quiet-hours window.
// internal/notify.QuietHours satisfies it structurally, so both the Dispatcher
// and the mail Suppress middleware feed the resolver the same definition of
// quiet hours they already share.
type QuietHours interface {
	InWindow(ctx context.Context, userID string) bool
}

// Decision is the resolved send disposition for one (user, kind).
type Decision struct {
	// Deliver is false when the notification is fully suppressed -- only
	// reachable for OFF on an optional (non-mandatory) type.
	Deliver bool
	// Mode is immediate vs batch. Batch is the contract for digest-bound events;
	// see ModeBatch.
	Mode Mode
	// Cadence is the effective cadence after override→category→default resolution
	// and the compliance floor. For a batch decision it is daily or weekly, which
	// the scheduler uses to pick the digest window (a daily row drains at the
	// daily hour; a weekly row also waits for the weekly day). Empty when the
	// kind is force-immediate (mandatory security/transactional).
	Cadence Cadence
	// Email/InApp/Push are the per-channel send decisions for the immediate
	// path. Email and Push already fold in quiet hours (false in-window unless
	// bypassed); InApp is quiet-hours exempt (the inbox always receives it) but
	// still honors the channel switch and the OFF suppression.
	Email bool
	InApp bool
	Push  bool
	// BypassQuietHours records whether quiet hours were bypassed for this
	// decision (mandatory security/transactional, or critical severity).
	BypassQuietHours bool
	// Class is the resolved classification (empty Kind when the kind is unknown).
	Class Class
}

// Resolver is the one shared send-time preference resolver
// . It classifies a kind, resolves the user's cadence
// within the mandatory floor, maps cadence to a delivery mode, and intersects
// channel switches and quiet hours.
type Resolver struct {
	prefs PrefReader
	quiet QuietHours
}

// NewResolver builds a Resolver. quiet may be nil (then quiet hours never
// suppress -- matching the dispatcher/suppressor's own nil-quiet behavior).
func NewResolver(prefs PrefReader, quiet QuietHours) *Resolver {
	return &Resolver{prefs: prefs, quiet: quiet}
}

// Resolve computes the send disposition for a (user, kind). It follows
// :
//
// 1. classify kind -> (category, severity, mandatory, deliveryPolicy)
// 2. mandatory security/transactional -> immediate + bypass quiet hours
// 3. type override -> else category cadence -> else default
// 4. clamp: mandatory compliance + off => daily (the compliance floor)
// 5. map cadence -> mode (immediate; or batch iff the delivery policy allows,
// else immediate); off => not delivered
// 6. intersect channel switches + quiet hours (email/push gated unless bypass;
// in-app is quiet-hours exempt)
//
// An unknown kind falls back to a permissive immediate delivery so a template
// added before its taxonomy row is never silently dropped.
func (r *Resolver) Resolve(ctx context.Context, userID, kind string) (Decision, error) {
	ch, err := r.prefs.Channels(ctx, userID)
	if err != nil {
		return Decision{}, err
	}

	class, known := Classify(kind)

	// The mandatory security/transactional set is force-delivered: it ignores
	// both the quiet-hours window AND the channel master switches, exactly as
	// today's bypassSuppressionKinds does (a password-recovery code or
	// break-glass alert must reach the user even with email turned off). This
	// generalizes that hard-coded list into the taxonomy.
	if known && class.BypassQuietHours() {
		return Decision{
			Deliver:          true,
			Mode:             ModeImmediate,
			Email:            true,
			InApp:            true,
			Push:             true,
			BypassQuietHours: true,
			Class:            class,
		}, nil
	}

	// Bypass only the quiet-hours window (not the channel switches) for critical
	// severity (: "immediate still quiet-hours-gated
	// unless severity==critical") -- e.g. policy-escalation.
	bypass := known && class.Severity == SeverityCritical

	// Resolve the effective cadence.
	cadence, err := r.effectiveCadence(ctx, userID, class, known)
	if err != nil {
		return Decision{}, err
	}

	dec := Decision{Class: class, BypassQuietHours: bypass, Cadence: cadence}

	// OFF is only reachable for optional types (the floor clamped it away for
	// mandatory compliance; security/transactional are forced immediate). A
	// fully-off optional type delivers nothing on any channel.
	if cadence == CadenceOff {
		return dec, nil
	}

	dec.Deliver = true

	// Map cadence -> mode. daily/weekly batch only when the delivery policy
	// permits; an immediate-only type can never be batched (e.g. the LOCKED
	// ack-required first demand), so it stays immediate regardless of the
	// user's category cadence.
	dec.Mode = ModeImmediate
	if cadence == CadenceDaily || cadence == CadenceWeekly {
		if !known || class.Delivery.Batchable() {
			dec.Mode = ModeBatch
		}
	}

	// Intersect channel switches and quiet hours. Email and push are suppressed
	// in the quiet window unless bypassed; in-app is exempt.
	inQuiet := !bypass && r.quiet != nil && r.quiet.InWindow(ctx, userID)
	dec.InApp = ch.InApp
	dec.Email = ch.Email && !inQuiet
	dec.Push = ch.Push && !inQuiet
	return dec, nil
}

// effectiveCadence resolves the cadence for a (user, kind) applying the
// override -> category -> default precedence and the mandatory floor.
func (r *Resolver) effectiveCadence(ctx context.Context, userID string, class Class, known bool) (Cadence, error) {
	// Mandatory security/transactional are forced immediate regardless of any
	// stored preference.
	if known && class.Mandatory() && (class.Category == CategorySecurity || class.Category == CategoryTransactional) {
		return CadenceImmediate, nil
	}

	// Per-type override wins.
	if known {
		if c, found, err := r.prefs.TypeOverride(ctx, userID, class.Kind); err != nil {
			return CadenceUnspecified, err
		} else if found && c != CadenceUnspecified {
			return r.clampFloor(class, c), nil
		}
	}

	// Category cadence next. For an unknown kind we have no category, so fall
	// straight through to a permissive immediate.
	if !known {
		return CadenceImmediate, nil
	}
	if c, found, err := r.prefs.CategoryCadence(ctx, userID, class.Category); err != nil {
		return CadenceUnspecified, err
	} else if found && c != CadenceUnspecified {
		return r.clampFloor(class, c), nil
	}

	// Default: the type's own default when set, else the category default.
	def := class.DefaultCadence
	if def == CadenceUnspecified {
		def = DefaultCategoryCadence(class.Category)
	}
	return r.clampFloor(class, def), nil
}

// clampFloor enforces the compliance floor in the resolver (not the UI): a
// mandatory compliance type may be narrowed to a daily digest but never set to
// off, so off is clamped up to daily.
func (r *Resolver) clampFloor(class Class, c Cadence) Cadence {
	if c == CadenceOff && class.Mandatory() && class.Category == CategoryCompliance {
		return CadenceDaily
	}
	return c
}
