// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package notifpolicy is the obligations service's single source of truth for the
// notification-type taxonomy and the send-time
// preference resolver. It is imported by both enforcement points --
// internal/notify's Dispatcher and internal/mail's Suppress middleware -- so
// they share one classification of "what is this notification and how may the
// user's cadence choice be realized" instead of drifting apart. The package
// deliberately depends on neither internal/notify nor internal/mail (both
// depend on it) and defines its own narrow persistence/quiet-hours seams so it
// stays free of an internal/store import.
package notifpolicy

import (
	"slices"
	"strings"
)

// Category is the taxonomy category every notification kind rolls up to
// .
type Category string

const (
	// CategoryCompliance is audit-bearing obligations: mandatory, narrowable to
	// a digest but never silenced.
	CategoryCompliance Category = "compliance"
	// CategorySecurity is auth/account-integrity: mandatory, bypasses quiet hours.
	CategorySecurity Category = "security"
	// CategoryTransactional is a message the user explicitly requested (OTP,
	// recovery, welcome): mandatory, time-sensitive, bypasses quiet hours.
	CategoryTransactional Category = "transactional"
	// CategoryWorkflow is approvals/assignments directed at a specific actor:
	// optional but default-on.
	CategoryWorkflow Category = "workflow"
	// CategoryInformational is FYI / roll-up candidates: optional, digest-friendly.
	CategoryInformational Category = "informational"
)

// Severity drives default cadence, whether quiet hours apply, and digest
// ordering.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityNormal   Severity = "normal"
	SeverityLow      Severity = "low"
)

// DeliveryPolicy is the system-defined (not user-editable) description of how a
// chosen cadence is realized for a type.
type DeliveryPolicy string

const (
	// DeliveryImmediateOnly is always sent the instant the event fires and
	// never batched (transactional/security/critical, plus the LOCKED
	// first-demand ack-required).
	DeliveryImmediateOnly DeliveryPolicy = "immediate-only"
	// DeliveryImmediateOrDigest is sent immediately if the user's cadence is
	// immediate, otherwise rolled into the matching digest window.
	DeliveryImmediateOrDigest DeliveryPolicy = "immediate-or-digest"
	// DeliveryReminderSchedule is recurring compliance driven by the scheduler,
	// or folded into the pending-ack digest if the user chose a digest.
	DeliveryReminderSchedule DeliveryPolicy = "reminder-schedule"
	// DeliveryDigestPreferred defaults to the daily roll-up; immediate is
	// allowed but discouraged.
	DeliveryDigestPreferred DeliveryPolicy = "digest-preferred"
)

// Batchable reports whether a daily/weekly cadence may be realized as a batched
// (digest) send for this delivery policy. Everything except immediate-only can
// batch.
func (d DeliveryPolicy) Batchable() bool { return d != DeliveryImmediateOnly }

// Class is the full classification of one notification kind.
type Class struct {
	// Kind is the render-sidecar template kind (the taxonomy key).
	Kind string
	// Category is the type's taxonomy category.
	Category Category
	// Severity drives quiet-hours bypass (critical) and digest ordering.
	Severity Severity
	// Delivery is how a chosen cadence is realized.
	Delivery DeliveryPolicy
	// DefaultCadence is the fallback cadence when the user has neither a
	// per-type override nor a per-category preference AND it differs from the
	// category default; the empty value means "use the category default".
	DefaultCadence Cadence
}

// Mandatory reports whether the kind's category cannot be turned off
// (compliance, security, transactional). The mandatory set is exactly these
// three categories, so it is derived from the
// category rather than stored per row -- the invariant lives in one place.
func (c Class) Mandatory() bool { return CategoryMandatory(c.Category) }

// BypassQuietHours reports whether a send of this kind ignores the 22:00-07:00
// quiet-hours window. This generalizes today's hard-coded bypassSuppressionKinds
// list into the taxonomy: exactly the mandatory security/transactional set
// . Critical-severity compliance (policy-escalation)
// also bypasses, but that is applied in the resolver via severity, not here.
func (c Class) BypassQuietHours() bool {
	return c.Mandatory() && (c.Category == CategorySecurity || c.Category == CategoryTransactional)
}

// CategoryMandatory reports whether a category is non-disable-able.
func CategoryMandatory(cat Category) bool {
	switch cat {
	case CategoryCompliance, CategorySecurity, CategoryTransactional:
		return true
	default:
		return false
	}
}

// AllCategories is the fixed set of categories, in display order.
var AllCategories = []Category{
	CategoryCompliance,
	CategorySecurity,
	CategoryTransactional,
	CategoryWorkflow,
	CategoryInformational,
}

// taxonomy encodes: every notification kind the
// platform sends (or is designed to send) with its category, severity, and
// delivery policy. DefaultCadence is set only where a type's default differs
// from its category default (see DefaultCategoryCadence).
var taxonomy = map[string]Class{
	// --- Compliance (mandatory; cadence-narrowable, never off) ---
	"ack-required":        {Kind: "ack-required", Category: CategoryCompliance, Severity: SeverityHigh, Delivery: DeliveryImmediateOnly},           // LOCKED: first demand is ALWAYS immediate, never digest-foldable.
	"policy-ack-reminder": {Kind: "policy-ack-reminder", Category: CategoryCompliance, Severity: SeverityHigh, Delivery: DeliveryReminderSchedule}, // recurring reminder: digest-foldable.
	"policy-escalation":   {Kind: "policy-escalation", Category: CategoryCompliance, Severity: SeverityCritical, Delivery: DeliveryImmediateOnly},
	"review-due":          {Kind: "review-due", Category: CategoryCompliance, Severity: SeverityHigh, Delivery: DeliveryReminderSchedule},

	// --- Informational (optional; digest-friendly) ---
	"policy-published":        {Kind: "policy-published", Category: CategoryInformational, Severity: SeverityNormal, Delivery: DeliveryDigestPreferred, DefaultCadence: CadenceImmediate}, // digest-floored (D1) but defaults immediate as today.
	"policy-retired":          {Kind: "policy-retired", Category: CategoryInformational, Severity: SeverityNormal, Delivery: DeliveryDigestPreferred},
	"access-granted":          {Kind: "access-granted", Category: CategoryInformational, Severity: SeverityNormal, Delivery: DeliveryDigestPreferred},
	"raci-permission-granted": {Kind: "raci-permission-granted", Category: CategoryInformational, Severity: SeverityNormal, Delivery: DeliveryDigestPreferred},
	"sso-activated":           {Kind: "sso-activated", Category: CategoryInformational, Severity: SeverityNormal, Delivery: DeliveryImmediateOrDigest, DefaultCadence: CadenceImmediate},
	"sso-disabled":            {Kind: "sso-disabled", Category: CategoryInformational, Severity: SeverityNormal, Delivery: DeliveryImmediateOrDigest, DefaultCadence: CadenceImmediate},
	"idp-test-failed":         {Kind: "idp-test-failed", Category: CategoryInformational, Severity: SeverityHigh, Delivery: DeliveryImmediateOrDigest, DefaultCadence: CadenceImmediate},

	// --- Workflow (optional, default-on) ---
	"workflow-awaiting-approval": {Kind: "workflow-awaiting-approval", Category: CategoryWorkflow, Severity: SeverityHigh, Delivery: DeliveryImmediateOrDigest},
	"workflow-denied":            {Kind: "workflow-denied", Category: CategoryWorkflow, Severity: SeverityHigh, Delivery: DeliveryImmediateOrDigest},
	"workflow-assigned":          {Kind: "workflow-assigned", Category: CategoryWorkflow, Severity: SeverityHigh, Delivery: DeliveryImmediateOrDigest},
	"assigned-as-owner":          {Kind: "assigned-as-owner", Category: CategoryWorkflow, Severity: SeverityHigh, Delivery: DeliveryImmediateOrDigest},
	"workflow-started":           {Kind: "workflow-started", Category: CategoryWorkflow, Severity: SeverityNormal, Delivery: DeliveryDigestPreferred, DefaultCadence: CadenceDaily},

	// --- Transactional (mandatory; bypass quiet hours) ---
	"welcome-account":                  {Kind: "welcome-account", Category: CategoryTransactional, Severity: SeverityHigh, Delivery: DeliveryImmediateOnly},
	"sso-account-welcome":              {Kind: "sso-account-welcome", Category: CategoryTransactional, Severity: SeverityHigh, Delivery: DeliveryImmediateOnly},
	"email-verification":               {Kind: "email-verification", Category: CategoryTransactional, Severity: SeverityHigh, Delivery: DeliveryImmediateOnly},
	"domain-verification-instructions": {Kind: "domain-verification-instructions", Category: CategoryTransactional, Severity: SeverityNormal, Delivery: DeliveryImmediateOnly},
	"domain-verified":                  {Kind: "domain-verified", Category: CategoryTransactional, Severity: SeverityNormal, Delivery: DeliveryImmediateOnly},

	// --- Security (mandatory; bypass quiet hours) ---
	"otp":               {Kind: "otp", Category: CategorySecurity, Severity: SeverityCritical, Delivery: DeliveryImmediateOnly},
	"kratos-recovery":   {Kind: "kratos-recovery", Category: CategorySecurity, Severity: SeverityCritical, Delivery: DeliveryImmediateOnly},
	"break-glass-alert": {Kind: "break-glass-alert", Category: CategorySecurity, Severity: SeverityCritical, Delivery: DeliveryImmediateOnly},
	"mfa-setup":         {Kind: "mfa-setup", Category: CategorySecurity, Severity: SeverityHigh, Delivery: DeliveryImmediateOnly},
	"sp-cert-rotated":   {Kind: "sp-cert-rotated", Category: CategorySecurity, Severity: SeverityHigh, Delivery: DeliveryImmediateOnly},
}

// Classify returns the Class for a kind and whether it is known. Unknown kinds
// are the caller's cue to fall back to a permissive immediate delivery so a
// newly added template is never silently dropped before its taxonomy row lands.
func Classify(kind string) (Class, bool) {
	c, ok := taxonomy[kind]
	return c, ok
}

// AllClasses enumerates the whole taxonomy in stable display order: grouped by
// AllCategories (the category display order) and alphabetical by kind within a
// category. It derives from the same `taxonomy` map Classify reads, so the
// catalog can never drift from the classification the send-time resolver uses.
//
// This is the accessor behind NotifPrefService.ListNotifTypes: a client needs
// the full type list (not only the user's existing overrides) to render a
// per-type preferences surface, and it needs Delivery to know which types are
// immediate-only and must not be offered a digest cadence.
func AllClasses() []Class {
	byCategory := make(map[Category][]Class, len(AllCategories))
	for _, c := range taxonomy {
		byCategory[c.Category] = append(byCategory[c.Category], c)
	}
	out := make([]Class, 0, len(taxonomy))
	for _, cat := range AllCategories {
		group := byCategory[cat]
		slices.SortFunc(group, func(a, b Class) int { return strings.Compare(a.Kind, b.Kind) })
		out = append(out, group...)
	}
	return out
}
