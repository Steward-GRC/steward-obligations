// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package authz is the service's copy of the canonical Steward permission
// catalog (catalog.json) and the few roles and permissions the service names.
// The mechanism is go-authz's rbac package; category rule decisions are not
// made here, they come from go-authz/ordered. The embedded catalog must equal
// testdata/catalog.canonical.json, which `task catalog:refresh` re-fetches.
package authz

import (
	_ "embed"

	"github.com/Bugs5382/go-authz/rbac"
)

//go:embed catalog.json
var catalogJSON []byte

// Catalog is the parsed canonical catalog. It is read-only; a catalog that does
// not parse stops the process at start-up.
var Catalog = rbac.MustParse(catalogJSON)

type (
	// Subject is the signed-in person a capability is checked for.
	Subject = rbac.Subject
	// Permission is a catalog permission such as policy.read.
	Permission = rbac.Permission
	// Role is a catalog role such as compliance-admin.
	Role = rbac.Role
)

// The permissions of catalog.json the service names.
const PolicyReadSensitive Permission = "policy.read_sensitive"

// The roles of catalog.json the service names.
const RoleComplianceAdmin Role = "compliance-admin"

// HasCapability reports whether s could hold p in at least one scope.
func HasCapability(s Subject, p Permission) bool {
	return Catalog.HasCapability(s, p)
}
