// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifpolicy

// This file holds the string<->typed conversions shared by the persistence
// adapter and the gRPC handler so neither re-implements the mapping. The stored
// representation (Postgres text columns) is the lowercase constant value of
// each Cadence/Category.

// ParseCadence maps a stored string to a Cadence. An empty or unrecognized
// value yields CadenceUnspecified, ok=false so callers treat it as "no choice".
func ParseCadence(s string) (Cadence, bool) {
	switch Cadence(s) {
	case CadenceImmediate:
		return CadenceImmediate, true
	case CadenceDaily:
		return CadenceDaily, true
	case CadenceWeekly:
		return CadenceWeekly, true
	case CadenceOff:
		return CadenceOff, true
	default:
		return CadenceUnspecified, false
	}
}

// ParseCategory maps a stored string to a Category. An unrecognized value
// yields ok=false.
func ParseCategory(s string) (Category, bool) {
	switch Category(s) {
	case CategoryCompliance:
		return CategoryCompliance, true
	case CategorySecurity:
		return CategorySecurity, true
	case CategoryTransactional:
		return CategoryTransactional, true
	case CategoryWorkflow:
		return CategoryWorkflow, true
	case CategoryInformational:
		return CategoryInformational, true
	default:
		return "", false
	}
}

// String returns the stored text representation of a Cadence.
func (c Cadence) String() string { return string(c) }

// String returns the stored text representation of a Category.
func (c Category) String() string { return string(c) }

// String returns the text representation of a Severity.
func (s Severity) String() string { return string(s) }

// String returns the text representation of a DeliveryPolicy.
func (d DeliveryPolicy) String() string { return string(d) }
