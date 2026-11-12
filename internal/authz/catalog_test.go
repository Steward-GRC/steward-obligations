// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package authz_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/Bugs5382/go-authz/rbac"

	"github.com/Steward-GRC/steward-obligations/internal/authz"
)

const (
	canonicalPath = "../../testdata/catalog.canonical.json"
	sourcePath    = "../../testdata/CATALOG_SOURCE"
)

// The embedded catalog is a copy of the canonical Steward catalog. rbac.Diff
// reports every difference, so the copy cannot drift without a test failing.
func TestEmbeddedCatalogMatchesCanonical(t *testing.T) {
	raw, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical catalog: %v", err)
	}
	canonical, err := rbac.Parse(raw)
	if err != nil {
		t.Fatalf("parse canonical catalog: %v", err)
	}
	if diff := rbac.Diff(authz.Catalog, canonical); len(diff) != 0 {
		t.Fatalf("embedded catalog differs from the canonical one (run: task catalog:refresh COMMIT=<sha>):\n%s", strings.Join(diff, "\n"))
	}
}

// The copy is also byte-identical, so a refresh is a plain file copy.
func TestEmbeddedCatalogIsByteIdentical(t *testing.T) {
	embedded, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, canonical) {
		t.Fatal("internal/authz/catalog.json is not byte-identical to testdata/catalog.canonical.json")
	}
}

func TestCatalogSourceNamesTheCommit(t *testing.T) {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("CATALOG_SOURCE line %q is not 'key: value'", line)
		}
		got[k] = v
	}
	if len(got["commit"]) != 40 {
		t.Errorf("commit must be a full sha, got %q", got["commit"])
	}
	if got["path"] != "internal/authz/catalog.json" || got["repo"] != "Steward-GRC/steward-gateway" {
		t.Errorf("unexpected source: %v", got)
	}
}

func TestPermissionsAndRolesTheServiceUses(t *testing.T) {
	if _, err := authz.Catalog.ParsePermission(string(authz.PolicyReadSensitive)); err != nil {
		t.Errorf("policy.read_sensitive is not in the catalog: %v", err)
	}
	if _, err := authz.Catalog.ParseRole(string(authz.RoleComplianceAdmin)); err != nil {
		t.Errorf("compliance-admin is not in the catalog: %v", err)
	}
}

// No role reads a sensitive policy; only the individual grant does.
func TestReadSensitiveIsAnIndividualGrant(t *testing.T) {
	for _, r := range authz.Catalog.RoleNames() {
		if authz.HasCapability(authz.Subject{Roles: []authz.Role{r}}, authz.PolicyReadSensitive) {
			t.Errorf("role %q holds policy.read_sensitive", r)
		}
	}
	if !authz.HasCapability(authz.Subject{ReadSensitive: true}, authz.PolicyReadSensitive) {
		t.Error("the individual grant must hold policy.read_sensitive")
	}
}
