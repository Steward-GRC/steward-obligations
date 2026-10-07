// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifpolicy_test

import (
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
)

// TestAllClassesCoversEveryClassifiableKind asserts the enumerator is derived
// from the same map Classify reads: every row it returns round-trips through
// Classify with an identical Class, so a catalog served to the UI can never
// disagree with the send-time classification.
func TestAllClassesCoversEveryClassifiableKind(t *testing.T) {
	all := notifpolicy.AllClasses()
	if len(all) == 0 {
		t.Fatal("AllClasses returned no rows")
	}
	seen := make(map[string]bool, len(all))
	for _, c := range all {
		if seen[c.Kind] {
			t.Errorf("kind %q enumerated twice", c.Kind)
		}
		seen[c.Kind] = true

		got, ok := notifpolicy.Classify(c.Kind)
		if !ok {
			t.Errorf("kind %q enumerated but not classifiable", c.Kind)
			continue
		}
		if got != c {
			t.Errorf("kind %q: enumerated %+v, Classify %+v", c.Kind, c, got)
		}
		if c.Kind == "" || c.Category == "" || c.Severity == "" || c.Delivery == "" {
			t.Errorf("kind %q: incomplete class %+v", c.Kind, c)
		}
	}
}

// TestAllClassesTaxonomySize pins the taxonomy size so adding or removing a
// notification type is a deliberate, reviewed change.
func TestAllClassesTaxonomySize(t *testing.T) {
	if got, want := len(notifpolicy.AllClasses()), 27; got != want {
		t.Errorf("taxonomy size = %d, want %d (update this test deliberately)", got, want)
	}
}

// TestAllClassesGroupedByCategoryDisplayOrder asserts the stable ordering
// contract: rows are grouped by AllCategories order, alphabetical within a
// category, so the UI can render the catalog without re-sorting.
func TestAllClassesGroupedByCategoryDisplayOrder(t *testing.T) {
	all := notifpolicy.AllClasses()

	order := make(map[notifpolicy.Category]int, len(notifpolicy.AllCategories))
	for i, cat := range notifpolicy.AllCategories {
		order[cat] = i
	}

	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1], all[i]
		switch {
		case order[cur.Category] < order[prev.Category]:
			t.Fatalf("category order broken at %d: %q after %q", i, cur.Category, prev.Category)
		case cur.Category == prev.Category && cur.Kind <= prev.Kind:
			t.Fatalf("kind order broken within %q: %q after %q", cur.Category, cur.Kind, prev.Kind)
		}
	}

	// Every category in the display list must actually be represented; an empty
	// category would render as a header with nothing under it.
	present := make(map[notifpolicy.Category]bool, len(order))
	for _, c := range all {
		present[c.Category] = true
	}
	for _, cat := range notifpolicy.AllCategories {
		if !present[cat] {
			t.Errorf("category %q has no notification kinds", cat)
		}
	}
}

// TestAllClassesMandatoryAndDeliveryAreCoherent guards the two flags the UI
// relies on to compute the cadence floor client-side: the mandatory set is
// exactly compliance/security/transactional, and an immediate-only type is
// never reported as batchable.
func TestAllClassesMandatoryAndDeliveryAreCoherent(t *testing.T) {
	for _, c := range notifpolicy.AllClasses() {
		if got, want := c.Mandatory(), notifpolicy.CategoryMandatory(c.Category); got != want {
			t.Errorf("kind %q: Mandatory()=%v, CategoryMandatory(%q)=%v", c.Kind, got, c.Category, want)
		}
		if c.Delivery == notifpolicy.DeliveryImmediateOnly && c.Delivery.Batchable() {
			t.Errorf("kind %q: immediate-only reported batchable", c.Kind)
		}
	}
}

func TestBreakGlassReadAlertIsMandatorySecurity(t *testing.T) {
	c, ok := notifpolicy.Classify("break-glass-read-alert")
	if !ok || c.Category != notifpolicy.CategorySecurity || c.Severity != notifpolicy.SeverityCritical ||
		c.Delivery != notifpolicy.DeliveryImmediateOnly {
		t.Fatalf("break-glass-read-alert = %+v ok=%v, want critical immediate security", c, ok)
	}
}
