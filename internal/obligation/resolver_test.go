// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	authz "github.com/Steward-GRC/steward-authz"
	"github.com/Steward-GRC/steward-obligations/internal/consumer"
	"github.com/Steward-GRC/steward-obligations/internal/notify"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// ---------------------------------------------------------------------------
// Hand-written fakes
// ---------------------------------------------------------------------------

// fakeCore fakes the coreObligations interface.
type fakeCore struct {
	obligating []obligation.ObligatingPolicy
	// resolvePolicyObligation keyed by policyID
	resolutions map[string]obligation.PolicyObligation
	// versionToPolicy maps policyVersionID -> owning policyID (GetPolicyVersion)
	versionToPolicy map[string]string
	// policyVersions maps policyID -> version IDs (ListPolicyVersions)
	policyVersions map[string][]string
	// homeGroups maps policyID -> home group id (PolicyHomeGroup)
	homeGroups map[string]string
	// policyNumbers maps policyID -> policy number string
	policyNumbers map[string]string
	// policyTitles maps policyID -> policy title string (for PolicyDisplay)
	policyTitles map[string]string
	// sensitive maps policyID -> SENSITIVE classification (PolicySensitivity)
	sensitive map[string]bool
	// docTypes maps policyID -> document type ("procedure"/"policy"). An absent
	// entry defaults to "policy" (PolicyDocumentType), the back-compat default.
	docTypes map[string]string
	// docTypeErr, when non-nil, is returned by PolicyDocumentType to exercise the
	// fail-loud path (an errored lookup must propagate, never default to policy).
	docTypeErr error
}

func (f *fakeCore) PolicySensitivity(ctx context.Context, policyID string) (bool, error) {
	return f.sensitive[policyID], nil
}

func (f *fakeCore) PolicyDocumentType(ctx context.Context, policyID string) (string, error) {
	if f.docTypeErr != nil {
		return "", f.docTypeErr
	}
	if dt, ok := f.docTypes[policyID]; ok {
		return dt, nil
	}
	return "policy", nil
}

func (f *fakeCore) PolicyHomeGroup(ctx context.Context, policyID string) (string, error) {
	return f.homeGroups[policyID], nil
}

func (f *fakeCore) PolicyNumber(ctx context.Context, policyID string) (string, error) {
	return f.policyNumbers[policyID], nil
}

func (f *fakeCore) PolicyDisplay(ctx context.Context, policyID string) (string, string, error) {
	return f.policyNumbers[policyID], f.policyTitles[policyID], nil
}

func (f *fakeCore) ListObligatingPolicies(ctx context.Context) ([]obligation.ObligatingPolicy, error) {
	return f.obligating, nil
}

func (f *fakeCore) ResolvePolicyObligation(ctx context.Context, policyID string) (obligation.PolicyObligation, error) {
	if r, ok := f.resolutions[policyID]; ok {
		return r, nil
	}
	return obligation.PolicyObligation{}, nil
}

func (f *fakeCore) PolicyIDForVersion(ctx context.Context, policyVersionID string) (string, error) {
	return f.versionToPolicy[policyVersionID], nil
}

func (f *fakeCore) VersionPublishedAt(ctx context.Context, policyVersionID string) (time.Time, error) {
	return time.Time{}, nil
}

func (f *fakeCore) ListPolicyVersionIDs(ctx context.Context, policyID string) ([]string, error) {
	return f.policyVersions[policyID], nil
}

// fakeIdentity fakes the identityUsers interface.
// adGroups maps userID → []adGroupName
// overrides maps userID -> {policyNumber -> "allow"|"deny"}.
type fakeIdentity struct {
	adGroups map[string][]string // userID -> []adGroupName
	// overrides maps userID -> {policyNumber -> "allow"|"deny"}.
	overrides map[string]map[string]string
	// roles maps userID -> global roles (UserReadClearance).
	roles map[string][]string
	// readSensitive maps userID -> individual read_sensitive grant.
	readSensitive map[string]bool
}

func (f *fakeIdentity) UserReadClearance(ctx context.Context, userID string) ([]string, bool, error) {
	return f.roles[userID], f.readSensitive[userID], nil
}

func (f *fakeIdentity) UserAdGroups(ctx context.Context, userID string) ([]string, error) {
	return f.adGroups[userID], nil
}

func (f *fakeIdentity) UserPolicyOverrides(ctx context.Context, userID string) (map[string]string, error) {
	return f.overrides[userID], nil
}

// ListAllUsers returns every known user (the RACI audience candidate set),
// independent of AD-group membership.
func (f *fakeIdentity) ListAllUsers(ctx context.Context) ([]obligation.User, error) {
	users := make([]obligation.User, 0, len(f.adGroups))
	for uid := range f.adGroups {
		users = append(users, obligation.User{ID: uid})
	}
	return users, nil
}

// fakeChains fakes the categoryChains interface.
// chains maps categoryID -> []authz.CategoryRuleset (the pre-built chain for
// that category, as if BuildCategoryChain walked the lineage).
type fakeChains struct {
	chains map[string][]authz.CategoryRuleset
}

func (f *fakeChains) BuildCategoryChain(ctx context.Context, categoryID string) ([]authz.CategoryRuleset, error) {
	if f.chains == nil {
		return nil, nil
	}
	return f.chains[categoryID], nil
}

// fakeAcks fakes the acksStore interface.
// acked maps userID -> set of acked policyVersionIDs
// ackedUserIDs maps policyVersionID -> set of userIDs
type fakeAcks struct {
	acked      map[string]map[string]bool // userID -> versionID -> bool
	ackedUsers map[string][]string        // policyVersionID -> []userID
}

func (f *fakeAcks) Acked(ctx context.Context, userID string, versionIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(versionIDs))
	for _, id := range versionIDs {
		result[id] = false
	}
	if byUser, ok := f.acked[userID]; ok {
		for _, id := range versionIDs {
			if byUser[id] {
				result[id] = true
			}
		}
	}
	return result, nil
}

func (f *fakeAcks) AckedUserIDsForVersion(ctx context.Context, policyVersionID string) ([]string, error) {
	if f.ackedUsers == nil {
		return nil, nil
	}
	return f.ackedUsers[policyVersionID], nil
}

func (f *fakeAcks) AckedAtForVersion(ctx context.Context, policyVersionID string) (map[string]time.Time, error) {
	return nil, nil
}

func (f *fakeAcks) DailyAckCounts(ctx context.Context, policyVersionID string, since time.Time) (map[string]int, error) {
	return nil, nil
}

func (f *fakeAcks) DeleteAcksForUsers(ctx context.Context, policyVersionID string, userIDs []string) (int, error) {
	if f.ackedUsers == nil {
		return 0, nil
	}
	del := make(map[string]struct{}, len(userIDs))
	for _, u := range userIDs {
		del[u] = struct{}{}
	}
	var remaining []string
	n := 0
	for _, u := range f.ackedUsers[policyVersionID] {
		if _, ok := del[u]; ok {
			n++
		} else {
			remaining = append(remaining, u)
		}
	}
	f.ackedUsers[policyVersionID] = remaining
	for _, u := range userIDs {
		if f.acked != nil && f.acked[u] != nil {
			delete(f.acked[u], policyVersionID)
		}
	}
	return n, nil
}

func (f *fakeAcks) VersionsAckedByUser(ctx context.Context, userID string) ([]string, error) {
	var out []string
	for v, users := range f.ackedUsers {
		if slices.Contains(users, userID) {
			out = append(out, v)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// RACI chain helpers — build authz.CategoryRuleset slices for common scenarios.
// ---------------------------------------------------------------------------

// everyoneAckChain builds a single-category chain with an everyone rule that
// allows both read and ack — the "all users must ack" scenario.
func everyoneAckChain(name string) []authz.CategoryRuleset {
	return []authz.CategoryRuleset{{
		Name: name,
		Rules: []authz.Rule{{
			Subject: authz.RuleSubject{Kind: authz.SubjectEveryone},
			Grants: map[authz.Action]authz.Grant{
				authz.ActionRead:        authz.GrantAllow,
				authz.ActionAcknowledge: authz.GrantAllow,
			},
		}},
	}}
}

// groupAckChain builds a single-category chain where group g gets read+ack.
func groupAckChain(name, g string) []authz.CategoryRuleset {
	return []authz.CategoryRuleset{{
		Name: name,
		Rules: []authz.Rule{{
			Subject: authz.RuleSubject{Kind: authz.SubjectGroup, Name: g},
			Grants: map[authz.Action]authz.Grant{
				authz.ActionRead:        authz.GrantAllow,
				authz.ActionAcknowledge: authz.GrantAllow,
			},
		}},
	}}
}

// userDenyChain builds a single-category chain where user u is absolutely
// denied read and ack (the RACI replacement for group exclusion).
func userDenyChain(name, userID string) []authz.CategoryRuleset {
	return []authz.CategoryRuleset{{
		Name: name,
		Rules: []authz.Rule{
			{
				Subject: authz.RuleSubject{Kind: authz.SubjectUser, Name: userID},
				Grants: map[authz.Action]authz.Grant{
					authz.ActionRead:        authz.GrantDeny,
					authz.ActionAcknowledge: authz.GrantDeny,
				},
			},
			{
				Subject: authz.RuleSubject{Kind: authz.SubjectEveryone},
				Grants: map[authz.Action]authz.Grant{
					authz.ActionRead:        authz.GrantAllow,
					authz.ActionAcknowledge: authz.GrantAllow,
				},
			},
		},
	}}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestMyObligations_MembershipAndAckKeying is the primary test.
// User u1 is in AD-A.
// policy p1: group AD-A rule (read+ack allow), on_change=true, current published = v1b
//
//	→ user acked OLD version v1a → still obligated (on-change requires exact version)
//
// policy p2: group AD-Z rule → no match → not returned
func TestMyObligations_MembershipAndAckKeying(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "Policy One", VersionNo: 2, PublishedVersionID: "v1b", OnChange: true},
			{PolicyID: "p2", Number: "POL-002", Title: "Policy Two", VersionNo: 1, PublishedVersionID: "v2", OnChange: false},
		},
		homeGroups:    map[string]string{"p1": "grp-a", "p2": "grp-z"},
		policyNumbers: map[string]string{"p1": "POL-001", "p2": "POL-002"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
		"grp-z": groupAckChain("GroupZ", "AD-Z"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	// u1 acked OLD version v1a of p1, but not v1b (current)
	acks := &fakeAcks{
		acked: map[string]map[string]bool{"u1": {"v1a": true}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 obligation (p1 - on-change, old ack doesn't count), got %d: %+v", len(got), got)
	}
	if got[0].PolicyID != "p1" {
		t.Errorf("want PolicyID p1, got %q", got[0].PolicyID)
	}
}

// TestMyObligations_EveryoneObligatesNonMember verifies that an everyone-ack
// RACI rule includes a user who is in NO audience AD group: the everyone rule
// matches regardless of group membership. A comparison policy with a group-only
// rule (non-matching) is NOT returned.
func TestMyObligations_EveryoneObligatesNonMember(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			// Everyone policy: everyone rule matches u1 even though u1 has no groups.
			{PolicyID: "pe", Number: "POL-E", Title: "Everyone Policy", VersionNo: 1, PublishedVersionID: "ve", OnChange: true},
			// Non-everyone policy with a group rule u1 doesn't match: excluded.
			{PolicyID: "pn", Number: "POL-N", Title: "Normal Policy", VersionNo: 1, PublishedVersionID: "vn", OnChange: true},
		},
		homeGroups:    map[string]string{"pe": "grp-everyone", "pn": "grp-x"},
		policyNumbers: map[string]string{"pe": "POL-E", "pn": "POL-N"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-everyone": everyoneAckChain("Everyone"),
		"grp-x":        groupAckChain("GroupX", "AD-X"),
	}}
	// u1 belongs to NO AD group (empty slice, but a known user).
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {}}}
	acks := &fakeAcks{}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 obligation (the Everyone policy), got %d: %+v", len(got), got)
	}
	if got[0].PolicyID != "pe" {
		t.Errorf("want PolicyID pe (Everyone), got %q", got[0].PolicyID)
	}

	// The same flag must feed the compliance summary: required counts the
	// everyone policy for a non-member.
	required, done, err := r.MyAckSummary(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if required != 1 || done != 0 {
		t.Fatalf("MyAckSummary: want required=1 done=0 for Everyone non-member, got required=%d done=%d", required, done)
	}
}

// TestMyObligations_SensitivePolicyNotObligatedWithoutClearance is the primary
// regression for the obligate-the-uncleared bug: a SENSITIVE policy with an
// everyone-ack rule must NOT be obligated to a groupless user who lacks
// policy.read_sensitive — they can never open it, so obligating them strands
// the /acknowledgements list on a policy every read path hides. A STANDARD
// everyone policy in the same set remains obligated (the gate only drops the
// sensitive one).
func TestMyObligations_SensitivePolicyNotObligatedWithoutClearance(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "ps", Number: "POL-INFOSE-000002", Title: "Data Classification Standard", VersionNo: 1, PublishedVersionID: "vs", OnChange: true},
			{PolicyID: "pn", Number: "POL-STD-1", Title: "Standard Everyone", VersionNo: 1, PublishedVersionID: "vn", OnChange: true},
		},
		homeGroups:    map[string]string{"ps": "grp-everyone", "pn": "grp-everyone"},
		policyNumbers: map[string]string{"ps": "POL-INFOSE-000002", "pn": "POL-STD-1"},
		sensitive:     map[string]bool{"ps": true}, // pn defaults to false (standard)
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-everyone": everyoneAckChain("Everyone"),
	}}
	// u1: groupless, plain reader (no roles), no individual read_sensitive grant.
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {}}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PolicyID != "pn" {
		t.Fatalf("uncleared user must be obligated ONLY to the standard policy pn, not the sensitive ps; got %+v", got)
	}

	// The summary must move in lockstep: only the standard policy is required.
	required, done, err := r.MyAckSummary(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if required != 1 || done != 0 {
		t.Fatalf("MyAckSummary: want required=1 done=0 (sensitive excluded), got required=%d done=%d", required, done)
	}
}

// TestMyObligations_SensitivePolicyObligatedWithClearance verifies the same
// SENSITIVE everyone-ack policy IS obligated once the subject is cleared to read
// it, which only the individual read_sensitive grant does: no role reads a
// sensitive policy, compliance-admin included.
func TestMyObligations_SensitivePolicyObligatedWithClearance(t *testing.T) {
	newCore := func() *fakeCore {
		return &fakeCore{
			obligating: []obligation.ObligatingPolicy{
				{PolicyID: "ps", Number: "POL-INFOSE-000002", Title: "Data Classification Standard", VersionNo: 1, PublishedVersionID: "vs", OnChange: true},
			},
			homeGroups:    map[string]string{"ps": "grp-everyone"},
			policyNumbers: map[string]string{"ps": "POL-INFOSE-000002"},
			sensitive:     map[string]bool{"ps": true},
		}
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-everyone": everyoneAckChain("Everyone"),
	}}

	// Case A: the compliance-admin role alone does not clear a sensitive policy.
	idnRole := &fakeIdentity{
		adGroups: map[string][]string{"u1": {}},
		roles:    map[string][]string{"u1": {"compliance-admin"}},
	}
	rRole := obligation.NewResolver(newCore(), idnRole, chains, &fakeAcks{}, nil)
	got, err := rRole.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("compliance-admin without the grant must not be obligated to the sensitive policy; got %+v", got)
	}

	// Case B: clearance via the individual read_sensitive grant.
	idnGrant := &fakeIdentity{
		adGroups:      map[string][]string{"u1": {}},
		readSensitive: map[string]bool{"u1": true},
	}
	rGrant := obligation.NewResolver(newCore(), idnGrant, chains, &fakeAcks{}, nil)
	got, err = rGrant.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PolicyID != "ps" {
		t.Fatalf("read_sensitive-granted user should be obligated to the sensitive policy; got %+v", got)
	}
}

// TestAudienceUsers_SensitivePolicyGatesUnclearedReaders verifies the reminder /
// notification path (AudienceUsers) applies the SAME sensitivity gate: for a
// SENSITIVE everyone-ack policy the audience is exactly the cleared users, so
// ack-reminder emails never go to a user who cannot open the policy.
func TestAudienceUsers_SensitivePolicyGatesUnclearedReaders(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"ps": "grp-everyone"},
		policyNumbers: map[string]string{"ps": "POL-INFOSE-000002"},
		sensitive:     map[string]bool{"ps": true},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-everyone": everyoneAckChain("Everyone"),
	}}
	idn := &fakeIdentity{
		adGroups:      map[string][]string{"u-clear": {}, "u-uncleared": {}},
		readSensitive: map[string]bool{"u-clear": true}, // u-uncleared has no clearance
	}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "ps")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	if got, want := audienceIDs(t, users), []string{"u-clear"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sensitive-policy reminder audience: want %v (uncleared excluded), got %v", want, got)
	}
}

// TestMyObligations_ExcludedFromGroupDropsObligation verifies that a user
// with an explicit RACI deny rule (read+ack deny) is dropped from the audience
// even for a policy where everyone else is obligated.
func TestMyObligations_ExcludedFromGroupDropsObligation(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "pe", Number: "POL-FACILITIES-1", Title: "Facilities", VersionNo: 1, PublishedVersionID: "ve", OnChange: true},
		},
		homeGroups:    map[string]string{"pe": "grp-facilities"},
		policyNumbers: map[string]string{"pe": "POL-FACILITIES-1"},
	}
	// Chain: u1 is explicitly denied read+ack; everyone else gets read+ack.
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-facilities": userDenyChain("Facilities", "u1"),
	}}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u1": {}},
	}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("excluded user (RACI deny) should have NO obligations, got %d: %+v", len(got), got)
	}
	required, done, err := r.MyAckSummary(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if required != 0 || done != 0 {
		t.Fatalf("excluded user: want required=0 done=0, got required=%d done=%d", required, done)
	}

	// Control: swap to everyone-ack chain (no deny rule for u1) → u1 is obligated.
	chains.chains["grp-facilities"] = everyoneAckChain("Facilities")
	got, _ = r.MyObligations(context.Background(), "u1")
	if len(got) != 1 {
		t.Fatalf("non-excluded user should have 1 obligation, got %d", len(got))
	}
}

// TestMyObligations_PolicyOverridePrecedence_Deny verifies that a DENY override
// drops an otherwise-obligated user regardless of the RACI chain.
func TestMyObligations_PolicyOverridePrecedence_Deny(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "pe", Number: "POL-FACILITIES-1", VersionNo: 1, PublishedVersionID: "ve", OnChange: true},
		},
		homeGroups:    map[string]string{"pe": "grp-facilities"},
		policyNumbers: map[string]string{"pe": "POL-FACILITIES-1"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-facilities": everyoneAckChain("Facilities"),
	}}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u1": {}},
		overrides: map[string]map[string]string{
			"u1": {"POL-FACILITIES-1": "deny"},
		},
	}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	// DENY override drops the obligation even though the chain has an everyone-ack rule.
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("deny-override should drop the obligation: want 0, got %d", len(got))
	}
}

// TestMyObligations_PolicyOverridePrecedence_AllowWithAckRule verifies that an
// ALLOW override re-includes a user who has a deny rule in the chain, PROVIDED
// there is also an ack rule that matches them (AckGrant check). The allow
// override establishes read out-of-band, but the ack rule must also match.
func TestMyObligations_PolicyOverridePrecedence_AllowWithAckRule(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "pe", Number: "POL-FACILITIES-1", VersionNo: 1, PublishedVersionID: "ve", OnChange: true},
		},
		homeGroups:    map[string]string{"pe": "grp-facilities"},
		policyNumbers: map[string]string{"pe": "POL-FACILITIES-1"},
	}
	// Chain: u1 is explicitly denied. With allow override, AckGrant is used.
	// But the deny rule would stop the ack too. So we need an everyone ack rule
	// AFTER the user deny to ensure AckGrant evaluates it.
	// Actually per spec: override-allow → obligated iff AckGrant(merit, chain).Allowed
	// AckGrant runs decideAction(u, chain, ActAck), which hits the user-deny first
	// → not allowed. So a user with a deny rule AND an allow override has NO obligation
	// (the deny rule blocks AckGrant). This is the correct behavior.
	// For the allow-with-matching-ack-rule case, we use a chain with only everyone-ack.
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-facilities": userDenyChain("Facilities", "u1"),
	}}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u1": {}},
		overrides: map[string]map[string]string{
			"u1": {"POL-FACILITIES-1": "allow"},
		},
	}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	// ALLOW override with a deny chain: AckGrant hits the user deny → NOT obligated.
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("allow-override with ack-deny rule: AckGrant denied → want 0, got %d", len(got))
	}

	// Now swap to everyone-ack chain. AckGrant hits everyone-ack → obligated.
	chains.chains["grp-facilities"] = everyoneAckChain("Facilities")
	got, err = r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("allow-override with everyone-ack rule: AckGrant allows → want 1, got %d", len(got))
	}
}

// TestMyObligations_OverrideAllow_NoMatchingAckRule verifies that an allow
// override WITHOUT any matching ack rule in the chain yields NO obligation
// (as per spec: AckGrant must also match).
func TestMyObligations_OverrideAllow_NoMatchingAckRule(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "pe", Number: "POL-IT-1", VersionNo: 1, PublishedVersionID: "ve", OnChange: true},
		},
		homeGroups:    map[string]string{"pe": "grp-it"},
		policyNumbers: map[string]string{"pe": "POL-IT-1"},
	}
	// Chain: read-only rule for group AD-IT. No ack grant for anyone.
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": {{
			Name: "IT",
			Rules: []authz.Rule{{
				Subject: authz.RuleSubject{Kind: authz.SubjectGroup, Name: "AD-IT"},
				Grants: map[authz.Action]authz.Grant{
					authz.ActionRead: authz.GrantAllow,
					// no ack grant
				},
			}},
		}},
	}}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u1": {}}, // not in AD-IT
		overrides: map[string]map[string]string{
			"u1": {"POL-IT-1": "allow"},
		},
	}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("allow-override with no ack rule: want 0 obligations, got %d", len(got))
	}
}

// TestMyObligations_OverrideAllow_EveryoneAckRule verifies that an allow
// override WITH an everyone-ack rule (but no read rule granting the user) DOES
// yield an obligation — the read is established by the override, AckGrant
// checks only the ack rule.
func TestMyObligations_OverrideAllow_EveryoneAckRule(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "pe", Number: "POL-IT-1", VersionNo: 1, PublishedVersionID: "ve", OnChange: true},
		},
		homeGroups:    map[string]string{"pe": "grp-it"},
		policyNumbers: map[string]string{"pe": "POL-IT-1"},
	}
	// Chain: only ack grant for everyone, no read grant for u1.
	// Resolve(u1).Ack would fail (no read → requires read gate fails).
	// But AckGrant(u1) succeeds because the everyone-ack rule matches.
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": {{
			Name: "IT",
			Rules: []authz.Rule{{
				Subject: authz.RuleSubject{Kind: authz.SubjectEveryone},
				Grants: map[authz.Action]authz.Grant{
					authz.ActionAcknowledge: authz.GrantAllow,
					// no read grant
				},
			}},
		}},
	}}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u1": {}},
		overrides: map[string]map[string]string{
			"u1": {"POL-IT-1": "allow"},
		},
	}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("allow-override + everyone-ack rule (no read): AckGrant passes → want 1, got %d", len(got))
	}
}

// TestMyObligations_OwnerDoesNotAutoAck verifies that being listed as an owner
// in the category chain does NOT create an ack obligation (owners auto-read/approve/
// author, but ack is NOT auto-granted to owners).
func TestMyObligations_OwnerDoesNotAutoAck(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", VersionNo: 1, PublishedVersionID: "v1", OnChange: true},
		},
		homeGroups:    map[string]string{"p1": "grp-it"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	// Chain: u1 is an owner. No ack rule for u1 or everyone.
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": {{
			Name:   "IT",
			Owners: []string{"u1"},
			Rules:  nil, // no ack rule
		}},
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {}}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("owner should have no ack obligation (owners do not auto-ack): want 0, got %d", len(got))
	}
}

// TestMyObligations_SiteAdminMeritOnlyBehavior verifies that site-admin status
// does NOT create an ack obligation unless the RACI chain explicitly includes them.
// The merit subject always uses IsSiteAdmin=false, IsRoot=false for obligation evaluation.
func TestMyObligations_SiteAdminMeritOnly(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			// Chain has only an ack rule for group AD-STAFF, which u1 is NOT in.
			{PolicyID: "p1", Number: "POL-001", VersionNo: 1, PublishedVersionID: "v1", OnChange: true},
		},
		homeGroups:    map[string]string{"p1": "grp-it"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": groupAckChain("IT", "AD-STAFF"),
	}}
	// u1 has no AD-STAFF membership — if site-admin bypass were applied they
	// would be auto-read but NOT auto-ack. Merit evaluation (IsSiteAdmin=false)
	// means they also don't get the read bypass, so Resolve.Ack is false.
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {}}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("site-admin (merit eval) without ack rule: want 0 obligations, got %d", len(got))
	}
}

// TestMyObligations_OnPublishKeyingClearsWithAnyVersion tests that when a
// policy uses on-publish keying (onChange=false), a user who has acked ANY
// version of that policy is considered cleared.
func TestMyObligations_OnPublishKeyingClearsWithAnyVersion(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "Policy One", VersionNo: 2, PublishedVersionID: "v1b", OnChange: false},
		},
		homeGroups:    map[string]string{"p1": "grp-a"},
		policyNumbers: map[string]string{"p1": "POL-001"},
		// core knows p1 has two versions; v1b is the currently published one.
		policyVersions: map[string][]string{"p1": {"v1a", "v1b"}},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	// u1 acked an old version v1a — on-publish keying clears via any version.
	acks := &fakeAcks{
		acked: map[string]map[string]bool{"u1": {"v1a": true}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 obligations (on-publish, any version acked clears), got %d: %+v", len(got), got)
	}
}

// TestMyObligations_OnPublishNewVersionDoesNotReObligate proves that publishing
// a NEW version of an on-publish policy does not re-obligate a user who already
// acked an older version.
func TestMyObligations_OnPublishNewVersionDoesNotReObligate(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "Policy One", VersionNo: 3, PublishedVersionID: "v1c", OnChange: false},
		},
		homeGroups:     map[string]string{"p1": "grp-a"},
		policyNumbers:  map[string]string{"p1": "POL-001"},
		policyVersions: map[string][]string{"p1": {"v1a", "v1b", "v1c"}},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	// u1 only ever acked the oldest version v1a — must NOT be re-obligated by v1c.
	acks := &fakeAcks{
		acked: map[string]map[string]bool{"u1": {"v1a": true}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 obligations (on-publish, older-version ack still clears after new publish), got %d: %+v", len(got), got)
	}
}

// TestMyObligations_NoMembershipMatch tests that a user with no matching
// AD groups (and no everyone rule) has no obligations returned.
func TestMyObligations_NoMembershipMatch(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "Policy One", VersionNo: 1, PublishedVersionID: "v1", OnChange: true},
		},
		homeGroups:    map[string]string{"p1": "grp-z"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-z": groupAckChain("GroupZ", "AD-Z"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	acks := &fakeAcks{acked: map[string]map[string]bool{}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 obligations (no membership match), got %d", len(got))
	}
}

// TestMyObligations_CaseInsensitiveAudienceMatch verifies that group-name
// matching is case-insensitive: the authz engine's subjectMatches uses
// strings.EqualFold so a rule subject "Facilities team" matches a user whose AD group
// name is "facilities team" (and vice-versa).
func TestMyObligations_CaseInsensitiveAudienceMatch(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "Security Policy", VersionNo: 1, PublishedVersionID: "v1", OnChange: false},
		},
		homeGroups:    map[string]string{"p1": "grp-sec"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	// Rule subject uses title-case "Facilities team"; user's directory group
	// federation is lower-case "facilities team". The authz engine matches via EqualFold.
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-sec": groupAckChain("Security", "Facilities team"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"facilities team"},
	}}
	acks := &fakeAcks{acked: map[string]map[string]bool{}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 obligation (case-insensitive group match), got %d: %+v", len(got), got)
	}
	if got[0].PolicyID != "p1" {
		t.Errorf("want PolicyID p1, got %q", got[0].PolicyID)
	}
}

// TestMyObligations_OnChangeCurrentVersionAcked tests that when a user has
// acked the CURRENT published version with on-change keying, they are cleared.
func TestMyObligations_OnChangeCurrentVersionAcked(t *testing.T) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "Policy One", VersionNo: 2, PublishedVersionID: "v1b", OnChange: true},
		},
		homeGroups:    map[string]string{"p1": "grp-a"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	// u1 acked the CURRENT version v1b
	acks := &fakeAcks{
		acked: map[string]map[string]bool{"u1": {"v1b": true}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 obligations (on-change, current version acked), got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Invariant: MyAckSummary <-> MyObligations must agree
// ---------------------------------------------------------------------------

// mixedFixture builds a single shared fixture with a MIX of policy kinds for
// user u1 (member of AD-A). It returns a resolver plus the acks fake (so a test
// can mutate the acked map and re-run to prove the two methods move together).
//
//	p1 on-change, group AD-A chain, current v1b, u1 acked OLD v1a -> OUTSTANDING
//	p2 on-change, group AD-A chain, current v2b, u1 acked v2b -> cleared (done)
//	p3 on-publish, group AD-A chain, current v3b, u1 acked OLD v3a -> cleared (done, ackedAnyVersion)
//	p4 on-publish, group AD-A chain, current v4a, u1 never acked -> OUTSTANDING
//	p5 on-change, group AD-Z chain (no match) -> neither required nor listed
//
// => required = 4 (p1..p4 match; p5 excluded)
//
//	done = 2 (p2, p3)
//	outstanding (MyObligations) = 2 (p1, p4)
func mixedFixture() (*obligation.Resolver, *fakeAcks) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "On-change outstanding", VersionNo: 2, PublishedVersionID: "v1b", OnChange: true},
			{PolicyID: "p2", Number: "POL-002", Title: "On-change acked", VersionNo: 2, PublishedVersionID: "v2b", OnChange: true},
			{PolicyID: "p3", Number: "POL-003", Title: "On-publish old-ack cleared", VersionNo: 2, PublishedVersionID: "v3b", OnChange: false},
			{PolicyID: "p4", Number: "POL-004", Title: "On-publish never acked", VersionNo: 1, PublishedVersionID: "v4a", OnChange: false},
			{PolicyID: "p5", Number: "POL-005", Title: "Out of audience", VersionNo: 1, PublishedVersionID: "v5a", OnChange: true},
		},
		homeGroups: map[string]string{
			"p1": "grp-a", "p2": "grp-a", "p3": "grp-a", "p4": "grp-a", "p5": "grp-z",
		},
		policyNumbers: map[string]string{
			"p1": "POL-001", "p2": "POL-002", "p3": "POL-003", "p4": "POL-004", "p5": "POL-005",
		},
		policyVersions: map[string][]string{
			"p3": {"v3a", "v3b"},
			"p4": {"v4a"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
		"grp-z": groupAckChain("GroupZ", "AD-Z"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	acks := &fakeAcks{
		acked: map[string]map[string]bool{
			"u1": {
				"v1a": true, // p1: OLD version only -> on-change NOT cleared
				"v2b": true, // p2: current version -> on-change cleared
				"v3a": true, // p3: OLD version -> on-publish cleared (any version)
				// p4: nothing -> outstanding
			},
		},
	}
	return obligation.NewResolver(core, idn, chains, acks, nil), acks
}

// TestSummaryObligationsInvariant is the primary lock: over one shared mixed
// fixture, MyAckSummary and MyObligations must satisfy required-done==len(list).
func TestSummaryObligationsInvariant(t *testing.T) {
	r, _ := mixedFixture()
	ctx := context.Background()

	obligations, err := r.MyObligations(ctx, "u1")
	if err != nil {
		t.Fatalf("MyObligations: %v", err)
	}
	required, done, err := r.MyAckSummary(ctx, "u1")
	if err != nil {
		t.Fatalf("MyAckSummary: %v", err)
	}

	// Core invariant the UI card depends on.
	if int(required)-int(done) != len(obligations) {
		t.Fatalf("invariant broken: required(%d) - done(%d) = %d, but len(MyObligations)=%d",
			required, done, int(required)-int(done), len(obligations))
	}

	// required == all audience-matching obligating policies (p1..p4; p5 excluded).
	if required != 4 {
		t.Errorf("required: want 4 (p1..p4 in audience, p5 excluded), got %d", required)
	}
	// done == cleared policies (p2 on-change current, p3 on-publish any-version).
	if done != 2 {
		t.Errorf("done: want 2 (p2, p3 cleared), got %d", done)
	}
	// outstanding list is exactly p1 and p4.
	if len(obligations) != 2 {
		t.Fatalf("obligations: want 2 (p1, p4), got %d: %+v", len(obligations), obligations)
	}
	gotIDs := map[string]bool{}
	for _, o := range obligations {
		gotIDs[o.PolicyID] = true
	}
	if !gotIDs["p1"] || !gotIDs["p4"] {
		t.Errorf("obligations: want {p1, p4}, got %+v", gotIDs)
	}
}

// TestSummaryObligationsMoveTogether proves the two methods move in lockstep.
func TestSummaryObligationsMoveTogether(t *testing.T) {
	r, acks := mixedFixture()
	ctx := context.Background()

	beforeList, err := r.MyObligations(ctx, "u1")
	if err != nil {
		t.Fatalf("MyObligations before: %v", err)
	}
	_, beforeDone, err := r.MyAckSummary(ctx, "u1")
	if err != nil {
		t.Fatalf("MyAckSummary before: %v", err)
	}

	// u1 now acks the current published version of the outstanding on-publish
	// policy p4.
	acks.acked["u1"]["v4a"] = true

	afterList, err := r.MyObligations(ctx, "u1")
	if err != nil {
		t.Fatalf("MyObligations after: %v", err)
	}
	afterRequired, afterDone, err := r.MyAckSummary(ctx, "u1")
	if err != nil {
		t.Fatalf("MyAckSummary after: %v", err)
	}

	if afterDone != beforeDone+1 {
		t.Errorf("done: want %d (before+1), got %d", beforeDone+1, afterDone)
	}
	if len(afterList) != len(beforeList)-1 {
		t.Errorf("obligations: want %d (before-1), got %d", len(beforeList)-1, len(afterList))
	}
	// Invariant still holds after the ack.
	if int(afterRequired)-int(afterDone) != len(afterList) {
		t.Errorf("invariant broken after ack: required(%d) - done(%d) != len(list)=%d",
			afterRequired, afterDone, len(afterList))
	}
	// p4 is no longer outstanding; only p1 remains.
	for _, o := range afterList {
		if o.PolicyID == "p4" {
			t.Errorf("p4 should be cleared after ack, but still outstanding: %+v", afterList)
		}
	}
}

// ---------------------------------------------------------------------------
// Completion tests
// ---------------------------------------------------------------------------

// TestCompletion_ResolvesPolicyFromVersion is the regression test:
// groupID is EMPTY — audience must come from the RACI chain via ListAllUsers,
// not from an ad-group-based audience path.
func TestCompletion_ResolvesPolicyFromVersion(t *testing.T) {
	core := &fakeCore{
		versionToPolicy: map[string]string{"pv1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-a"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true, PublishedVersionID: "pv1"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"},
		"u2": {"AD-A"},
	}}
	acks := &fakeAcks{
		acked:      map[string]map[string]bool{},
		ackedUsers: map[string][]string{"pv1": {"u1"}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	// groupID is EMPTY — the production "no filter" case.
	total, acked, overdue, err := r.Completion(context.Background(), "pv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total: want 2 (audience resolved from version), got %d", total)
	}
	if acked != 1 {
		t.Errorf("acked: want 1, got %d", acked)
	}
	if len(overdue) != 1 || overdue[0].ID != "u2" {
		t.Errorf("overdue: want [u2], got %+v", overdue)
	}
}

// TestCompletion_PartialAck tests Completion with an audience of 3, where 2
// have acked, leaving 1 overdue.
func TestCompletion_PartialAck(t *testing.T) {
	core := &fakeCore{
		versionToPolicy: map[string]string{"pv1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-a"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true, PublishedVersionID: "pv1"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"},
		"u2": {"AD-A"},
		"u3": {"AD-A"},
	}}
	acks := &fakeAcks{
		acked:      map[string]map[string]bool{},
		ackedUsers: map[string][]string{"pv1": {"u1", "u2"}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	total, acked, overdue, err := r.Completion(context.Background(), "pv1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("total: want 3, got %d", total)
	}
	if acked != 2 {
		t.Errorf("acked: want 2, got %d", acked)
	}
	if len(overdue) != 1 {
		t.Fatalf("overdue: want 1 entry, got %d: %+v", len(overdue), overdue)
	}
	if overdue[0].ID != "u3" {
		t.Errorf("overdue user: want u3, got %q", overdue[0].ID)
	}
}

// TestCompletion_AllAcked tests that when every audience member has acked,
// overdue is empty.
func TestCompletion_AllAcked(t *testing.T) {
	core := &fakeCore{
		versionToPolicy: map[string]string{"pv1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-a"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true, PublishedVersionID: "pv1"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"},
		"u2": {"AD-A"},
	}}
	acks := &fakeAcks{
		acked:      map[string]map[string]bool{},
		ackedUsers: map[string][]string{"pv1": {"u1", "u2"}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	total, acked, overdue, err := r.Completion(context.Background(), "pv1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total: want 2, got %d", total)
	}
	if acked != 2 {
		t.Errorf("acked: want 2, got %d", acked)
	}
	if len(overdue) != 0 {
		t.Errorf("overdue: want empty, got %+v", overdue)
	}
}

// TestCompletion_AckedUserLeftAudience tests that a user who acked but is no
// longer in the current audience does NOT inflate acked above total (pct ≤ 100%).
func TestCompletion_AckedUserLeftAudience(t *testing.T) {
	core := &fakeCore{
		versionToPolicy: map[string]string{"pv1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-a"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true, PublishedVersionID: "pv1"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	// Only u1 is in AD-A; u_gone has left (not in adGroups map).
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"},
		// u_gone is NOT listed — they have left the audience.
	}}
	acks := &fakeAcks{
		acked:      map[string]map[string]bool{},
		ackedUsers: map[string][]string{"pv1": {"u1", "u_gone"}},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	total, acked, overdue, err := r.Completion(context.Background(), "pv1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("total: want 1, got %d", total)
	}
	if acked > total {
		t.Errorf("acked (%d) must not exceed total (%d): pct would exceed 100%%", acked, total)
	}
	if acked != 1 {
		t.Errorf("acked: want 1 (only current audience member counts), got %d", acked)
	}
	if len(overdue) != 0 {
		t.Errorf("overdue: want empty (u1 acked), got %+v", overdue)
	}
}

// TestCompletion_NoneAcked tests that when no one has acked, all users are overdue.
func TestCompletion_NoneAcked(t *testing.T) {
	core := &fakeCore{
		versionToPolicy: map[string]string{"pv2": "p1"},
		homeGroups:      map[string]string{"p1": "grp-b"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true, PublishedVersionID: "pv2"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-b": groupAckChain("GroupB", "AD-B"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u4": {"AD-B"},
		"u5": {"AD-B"},
	}}
	acks := &fakeAcks{
		acked:      map[string]map[string]bool{},
		ackedUsers: map[string][]string{}, // no acks for pv2
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)
	total, acked, overdue, err := r.Completion(context.Background(), "pv2", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total: want 2, got %d", total)
	}
	if acked != 0 {
		t.Errorf("acked: want 0, got %d", acked)
	}
	if len(overdue) != 2 {
		t.Errorf("overdue: want 2, got %d: %+v", len(overdue), overdue)
	}
}

// ---------------------------------------------------------------------------
// Roster / Activity / CompletionMetrics tests
// ---------------------------------------------------------------------------

func TestResolver_RosterActivityMetrics(t *testing.T) {
	const ver = "ver-1"
	published := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Use relative dates so this test doesn't break as time advances.
	now := time.Now().UTC()
	dayMinus1 := now.AddDate(0, 0, -1).Format("2006-01-02")
	dayMinus2 := now.AddDate(0, 0, -2).Format("2006-01-02")

	core := &mCore{policyID: "pol-1", homeGroup: "grp-all", policyNum: "POL-M1", obl: obligation.PolicyObligation{
		RequiresAck: true, PublishedVersionID: ver,
	}, publishedAt: published}
	idn := &mIdentity{users: []obligation.User{
		{ID: "u1", Email: "u1@example.org"}, {ID: "u2", Email: "u2@example.org"}, {ID: "u3", Email: "u3@example.org"},
	}}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	acks := &mAcks{ackedAt: map[string]time.Time{
		"u1": published.AddDate(0, 0, 2), // acked 2 days after publish
	}, daily: map[string]int{dayMinus1: 1}}
	views := &mViews{distinct: []string{"u1", "u2"}, daily: map[string]int{dayMinus2: 2}}

	r := obligation.NewResolver(core, idn, chains, acks, views)
	ctx := context.Background()

	acked, pending, err := r.Roster(ctx, ver, "")
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(acked) != 1 || acked[0].ID != "u1" || acked[0].AckedAt.IsZero() {
		t.Fatalf("acked roster: %+v", acked)
	}
	if len(pending) != 2 {
		t.Fatalf("pending roster: got %d want 2", len(pending))
	}

	avg, vna, err := r.CompletionMetrics(ctx, ver, "")
	if err != nil {
		t.Fatalf("CompletionMetrics: %v", err)
	}
	if avg < 1.9 || avg > 2.1 {
		t.Fatalf("avgDaysToAck: got %v want ~2", avg)
	}
	// u2 viewed (in audience) but did not ack -> 1; u1 viewed but acked -> excluded.
	if vna != 1 {
		t.Fatalf("viewedNotAcked: got %d want 1", vna)
	}

	days, err := r.Activity(ctx, ver, "", 7)
	if err != nil {
		t.Fatalf("Activity: %v", err)
	}
	if len(days) != 7 {
		t.Fatalf("activity len: got %d want 7", len(days))
	}
	var sumA, sumV int
	for _, d := range days {
		sumA += d.Acks
		sumV += d.Views
	}
	if sumA != 1 || sumV != 2 {
		t.Fatalf("activity sums: acks=%d views=%d want 1/2", sumA, sumV)
	}
}

// --- fakes for the metrics tests ---

type mCore struct {
	policyID    string
	homeGroup   string
	policyNum   string
	obl         obligation.PolicyObligation
	publishedAt time.Time
}

func (m *mCore) ListObligatingPolicies(context.Context) ([]obligation.ObligatingPolicy, error) {
	return nil, nil
}
func (m *mCore) ResolvePolicyObligation(context.Context, string) (obligation.PolicyObligation, error) {
	return m.obl, nil
}
func (m *mCore) PolicyIDForVersion(context.Context, string) (string, error) { return m.policyID, nil }
func (m *mCore) VersionPublishedAt(context.Context, string) (time.Time, error) {
	return m.publishedAt, nil
}
func (m *mCore) ListPolicyVersionIDs(context.Context, string) ([]string, error) { return nil, nil }
func (m *mCore) PolicyHomeGroup(context.Context, string) (string, error)        { return m.homeGroup, nil }
func (m *mCore) PolicyNumber(context.Context, string) (string, error)           { return m.policyNum, nil }
func (m *mCore) PolicyDisplay(context.Context, string) (string, string, error) {
	return m.policyNum, "", nil
}
func (m *mCore) PolicySensitivity(context.Context, string) (bool, error)    { return false, nil }
func (m *mCore) PolicyDocumentType(context.Context, string) (string, error) { return "policy", nil }

type mIdentity struct{ users []obligation.User }

func (m *mIdentity) UserAdGroups(context.Context, string) ([]string, error) { return nil, nil }
func (m *mIdentity) UserPolicyOverrides(context.Context, string) (map[string]string, error) {
	return nil, nil
}
func (m *mIdentity) ListAllUsers(context.Context) ([]obligation.User, error) { return m.users, nil }
func (m *mIdentity) UserReadClearance(context.Context, string) ([]string, bool, error) {
	return nil, false, nil
}

type mAcks struct {
	ackedAt map[string]time.Time
	daily   map[string]int
}

func (m *mAcks) Acked(context.Context, string, []string) (map[string]bool, error) { return nil, nil }
func (m *mAcks) AckedUserIDsForVersion(context.Context, string) ([]string, error) {
	ids := make([]string, 0, len(m.ackedAt))
	for id := range m.ackedAt {
		ids = append(ids, id)
	}
	return ids, nil
}
func (m *mAcks) AckedAtForVersion(context.Context, string) (map[string]time.Time, error) {
	return m.ackedAt, nil
}
func (m *mAcks) DailyAckCounts(context.Context, string, time.Time) (map[string]int, error) {
	return m.daily, nil
}
func (m *mAcks) DeleteAcksForUsers(context.Context, string, []string) (int, error) { return 0, nil }
func (m *mAcks) VersionsAckedByUser(context.Context, string) ([]string, error)     { return nil, nil }

// ---------------------------------------------------------------------------
// PurgeOrphanedAcks / ReconcilePolicyAcks (obligation-removal purge)
// ---------------------------------------------------------------------------

// When a policy's ack requirement is turned OFF, the audience is empty, so
// EVERY existing ack for the version is orphaned and purged.
func TestPurgeOrphanedAcks_RequirementOff_PurgesAll(t *testing.T) {
	core := &fakeCore{
		resolutions:     map[string]obligation.PolicyObligation{"p1": {RequiresAck: false}},
		versionToPolicy: map[string]string{"v1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-it"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": groupAckChain("IT", "AD-IT"),
	}}
	acks := &fakeAcks{ackedUsers: map[string][]string{"v1": {"u1", "u2"}}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-IT"}, "u2": {"AD-IT"}}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	n, err := r.PurgeOrphanedAcks(context.Background(), "v1")
	if err != nil {
		t.Fatalf("PurgeOrphanedAcks: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 acks purged, got %d", n)
	}
	if len(acks.ackedUsers["v1"]) != 0 {
		t.Fatalf("want no acks remaining, got %v", acks.ackedUsers["v1"])
	}
}

// A user who is no longer in the audience (RACI chain gives no ack) is purged;
// a still-obligated member is kept.
func TestPurgeOrphanedAcks_LeftAudience_PurgesOnlyOrphan(t *testing.T) {
	core := &fakeCore{
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true},
		},
		versionToPolicy: map[string]string{"v1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-it"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": groupAckChain("IT", "AD-IT"),
	}}
	// u1 is still in AD-IT; u2 has left (no groups) but still has a stale ack.
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-IT"}, "u2": {}}}
	acks := &fakeAcks{ackedUsers: map[string][]string{"v1": {"u1", "u2"}}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	n, err := r.PurgeOrphanedAcks(context.Background(), "v1")
	if err != nil {
		t.Fatalf("PurgeOrphanedAcks: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 ack purged (u2), got %d", n)
	}
	if got := acks.ackedUsers["v1"]; len(got) != 1 || got[0] != "u1" {
		t.Fatalf("want only u1 remaining, got %v", got)
	}
}

// An everyone-ack chain keeps every user's ack (all users are obligated), so
// nothing is purged.
func TestPurgeOrphanedAcks_Everyone_PurgesNothing(t *testing.T) {
	core := &fakeCore{
		resolutions:     map[string]obligation.PolicyObligation{"p1": {RequiresAck: true}},
		versionToPolicy: map[string]string{"v1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-all"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {}, "u2": {}}}
	acks := &fakeAcks{ackedUsers: map[string][]string{"v1": {"u1", "u2"}}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	n, err := r.PurgeOrphanedAcks(context.Background(), "v1")
	if err != nil {
		t.Fatalf("PurgeOrphanedAcks: %v", err)
	}
	if n != 0 {
		t.Fatalf("want 0 purged for Everyone chain, got %d", n)
	}
}

// ReconcileUserAcks purges only the user's orphaned acks: v1 (AD-IT, user not a
// member) is purged; v2 (everyone chain, user still obligated) is kept.
func TestReconcileUserAcks_PurgesOnlyOrphaned(t *testing.T) {
	core := &fakeCore{
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true},
			"p2": {RequiresAck: true},
		},
		versionToPolicy: map[string]string{"v1": "p1", "v2": "p2"},
		homeGroups:      map[string]string{"p1": "grp-it", "p2": "grp-all"},
		policyNumbers:   map[string]string{"p1": "POL-001", "p2": "POL-002"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it":  groupAckChain("IT", "AD-IT"),
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-IT"}, "u2": {}}}
	acks := &fakeAcks{ackedUsers: map[string][]string{"v1": {"u2"}, "v2": {"u2"}}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	n, err := r.ReconcileUserAcks(context.Background(), "u2")
	if err != nil {
		t.Fatalf("ReconcileUserAcks: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 purged (u2's v1 ack), got %d", n)
	}
	if len(acks.ackedUsers["v1"]) != 0 {
		t.Fatalf("want v1 ack purged, got %v", acks.ackedUsers["v1"])
	}
	if len(acks.ackedUsers["v2"]) != 1 {
		t.Fatalf("want v2 ack kept (everyone chain), got %v", acks.ackedUsers["v2"])
	}
}

// ReconcilePolicyAcks purges orphaned acks across all versions of the policy.
func TestReconcilePolicyAcks_AllVersions(t *testing.T) {
	core := &fakeCore{
		resolutions:     map[string]obligation.PolicyObligation{"p1": {RequiresAck: false}},
		versionToPolicy: map[string]string{"v1": "p1", "v2": "p1"},
		policyVersions:  map[string][]string{"p1": {"v1", "v2"}},
		homeGroups:      map[string]string{"p1": "grp-it"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-it": groupAckChain("IT", "AD-IT"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-IT"}, "u2": {"AD-IT"}, "u3": {"AD-IT"}}}
	acks := &fakeAcks{ackedUsers: map[string][]string{"v1": {"u1"}, "v2": {"u2", "u3"}}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	n, err := r.ReconcilePolicyAcks(context.Background(), "p1")
	if err != nil {
		t.Fatalf("ReconcilePolicyAcks: %v", err)
	}
	if n != 3 {
		t.Fatalf("want 3 acks purged across versions, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// PurgeOrphanedAcks — nil-chain / empty-homeGroup fail-safe
// ---------------------------------------------------------------------------

// trackingAcks wraps fakeAcks and records whether DeleteAcksForUsers was called.
type trackingAcks struct {
	fakeAcks
	deleteCalled bool
}

func (t *trackingAcks) DeleteAcksForUsers(ctx context.Context, policyVersionID string, userIDs []string) (int, error) {
	t.deleteCalled = true
	return t.fakeAcks.DeleteAcksForUsers(ctx, policyVersionID, userIDs)
}

// TestPurgeOrphanedAcks_EmptyHomeGroup_ErrorsNotDeletes verifies that when a
// policy has no home group (hg == ""), audienceUsers returns an error and the
// purge path does NOT call DeleteAcksForUsers — guarding against silent
// mass-delete of valid acks.
func TestPurgeOrphanedAcks_EmptyHomeGroup_ErrorsNotDeletes(t *testing.T) {
	core := &fakeCore{
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true},
		},
		versionToPolicy: map[string]string{"v1": "p1"},
		// homeGroups deliberately absent for p1 — PolicyHomeGroup returns ""
		homeGroups:    map[string]string{},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-IT"}, "u2": {"AD-IT"}}}
	tr := &trackingAcks{
		fakeAcks: fakeAcks{ackedUsers: map[string][]string{"v1": {"u1", "u2"}}},
	}
	r := obligation.NewResolver(core, idn, chains, tr, nil)

	_, err := r.PurgeOrphanedAcks(context.Background(), "v1")
	if err == nil {
		t.Fatal("want error when policy has no home group; got nil — audience is empty and would mass-delete acks")
	}
	if tr.deleteCalled {
		t.Error("DeleteAcksForUsers must NOT be called when audience resolution errors")
	}
}

// TestPurgeOrphanedAcks_NilChains_ErrorsNotDeletes verifies that when r.chains
// is nil, audienceUsers returns an error rather than producing an empty audience
// that would cause all existing acks to be treated as orphaned.
func TestPurgeOrphanedAcks_NilChains_ErrorsNotDeletes(t *testing.T) {
	core := &fakeCore{
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true},
		},
		versionToPolicy: map[string]string{"v1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-it"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
	}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-IT"}, "u2": {"AD-IT"}}}
	tr := &trackingAcks{
		fakeAcks: fakeAcks{ackedUsers: map[string][]string{"v1": {"u1", "u2"}}},
	}
	// chains == nil — simulates a misconfigured resolver
	r := obligation.NewResolver(core, idn, nil, tr, nil)

	_, err := r.PurgeOrphanedAcks(context.Background(), "v1")
	if err == nil {
		t.Fatal("want error when chains is nil; got nil — audience is empty and would mass-delete acks")
	}
	if tr.deleteCalled {
		t.Error("DeleteAcksForUsers must NOT be called when audience resolution errors")
	}
}

// ---------------------------------------------------------------------------
// AudienceUsers — the publish-path entry point (policy-published consumer).
// These lock the contract that the publish sweep obligates exactly the users
// the read-time ack resolver would, via the same RACI chain.
// ---------------------------------------------------------------------------

// audienceIDs collects the sorted user IDs from an AudienceUsers result.
func audienceIDs(t *testing.T, users []obligation.User) []string {
	t.Helper()
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestAudienceUsers_EveryoneAckObligatesAllReaders verifies that an everyone-ack
// RACI rule obligates every enabled user — including a user in NO audience AD
// group — so the publish sweep notifies the whole readership.
func TestAudienceUsers_EveryoneAckObligatesAllReaders(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "grp-all"},
		policyNumbers: map[string]string{"p1": "POL-ALL"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	// u3 belongs to no group at all — the everyone rule must still obligate it.
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"}, "u2": {"AD-B"}, "u3": nil,
	}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	if got, want := audienceIDs(t, users), []string{"u1", "u2", "u3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("everyone-ack audience: want %v, got %v", want, got)
	}
}

// TestAudienceUsers_DeniedReadExcluded verifies that a user explicitly denied
// read+ack in the chain (RACI replacement for group exclusion) is NOT in the
// publish audience, while everyone else is — read-gates-ack.
func TestAudienceUsers_DeniedReadExcluded(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "grp-med"},
		policyNumbers: map[string]string{"p1": "POL-MED"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-med": userDenyChain("Facilities", "u-denied"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u-keep": {"AD-A"}, "u-denied": {"AD-A"},
	}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	if got, want := audienceIDs(t, users), []string{"u-keep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("denied-read audience: want %v (u-denied excluded), got %v", want, got)
	}
}

// TestAudienceUsers_OwnerNotAutoAcked verifies that an owner listed in the chain
// (auto read/approve/author) is NOT obligated to ack purely by ownership — the
// publish audience excludes an owner who has no ack grant.
func TestAudienceUsers_OwnerNotAutoAcked(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "grp-it"},
		policyNumbers: map[string]string{"p1": "POL-IT"},
	}
	// Chain grants ack only to AD-A; u-owner is an owner but not in AD-A.
	chain := []authz.CategoryRuleset{{
		Name:   "IT",
		Owners: []string{"u-owner"},
		Rules: []authz.Rule{{
			Subject: authz.RuleSubject{Kind: authz.SubjectGroup, Name: "AD-A"},
			Grants: map[authz.Action]authz.Grant{
				authz.ActionRead:        authz.GrantAllow,
				authz.ActionAcknowledge: authz.GrantAllow,
			},
		}},
	}}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{"grp-it": chain}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u-member": {"AD-A"}, "u-owner": nil,
	}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	if got, want := audienceIDs(t, users), []string{"u-member"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("owner-not-auto-acked audience: want %v (owner excluded), got %v", want, got)
	}
}

// TestAudienceUsers_FailsLoudOnEmptyHomeGroup verifies AudienceUsers returns an
// error (never a silent empty slice) when the policy has no home group — the
// same fail-loud guardrail the purge path relies on, so a publish can never
// silently suppress every notification.
func TestAudienceUsers_FailsLoudOnEmptyHomeGroup(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{}, // p1 absent → PolicyHomeGroup returns ""
		policyNumbers: map[string]string{"p1": "POL-1"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "p1")
	if err == nil {
		t.Fatalf("want error for empty home group; got nil with audience %v", users)
	}
	if users != nil {
		t.Errorf("audience must be nil on fail-loud, got %v", users)
	}
}

type mViews struct {
	distinct []string
	daily    map[string]int
}

func (m *mViews) DistinctViewersForVersion(context.Context, string) ([]string, error) {
	return m.distinct, nil
}
func (m *mViews) DailyDistinctViewers(context.Context, string, time.Time, []string) (map[string]int, error) {
	return m.daily, nil
}

// ---------------------------------------------------------------------------
// P3 — PROCEDURES are never ack-bearing. The audienceUsers gate closes
// the P2-flagged gap for the audience paths that BYPASS ResolvePolicyObligation
// (policy_retired consumer via AudienceUsers, reporting Completion), and — via
// audienceFor — for Roster/purge/reconcile. For POLICIES behavior is unchanged.
// ---------------------------------------------------------------------------

// TestAudienceUsers_ProcedureExcludedFromAckAudience proves a PROCEDURE resolves
// an EMPTY ack audience even under an everyone-ack chain that would obligate all
// users for a POLICY. This is the direct closure of the retired-procedure /
// Completion-denominator gap on the audienceUsers-driven paths.
func TestAudienceUsers_ProcedureExcludedFromAckAudience(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"proc1": "grp-all"},
		policyNumbers: map[string]string{"proc1": "PROC-1"},
		docTypes:      map[string]string{"proc1": "procedure"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"}, "u2": {"AD-B"}, "u3": nil,
	}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "proc1")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("procedure must obligate no one; got audience %v", audienceIDs(t, users))
	}
}

// TestAudienceUsers_PolicyDocTypeUnchanged is the regression twin: the SAME
// everyone-ack chain and users, but the document is a POLICY (default doc type),
// so the full audience is resolved exactly as before the P3 change.
func TestAudienceUsers_PolicyDocTypeUnchanged(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "grp-all"},
		policyNumbers: map[string]string{"p1": "POL-ALL"},
		docTypes:      map[string]string{"p1": "policy"},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"}, "u2": {"AD-B"}, "u3": nil,
	}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	if got, want := audienceIDs(t, users), []string{"u1", "u2", "u3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("policy audience unchanged: want %v, got %v", want, got)
	}
}

// TestCompletion_ProcedureZeroDenominator proves the reporting path (which calls
// audienceUsers directly, bypassing ResolvePolicyObligation) yields total=0 and
// no overdue for a PROCEDURE, so a procedure can never show a non-zero ack
// denominator even if ack rows somehow exist.
func TestCompletion_ProcedureZeroDenominator(t *testing.T) {
	core := &fakeCore{
		versionToPolicy: map[string]string{"pv1": "proc1"},
		homeGroups:      map[string]string{"proc1": "grp-all"},
		policyNumbers:   map[string]string{"proc1": "PROC-1"},
		docTypes:        map[string]string{"proc1": "procedure"},
		resolutions: map[string]obligation.PolicyObligation{
			"proc1": {RequiresAck: true, PublishedVersionID: "pv1"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}, "u2": {"AD-B"}}}
	// Stray ack rows must NOT inflate the denominator for a procedure.
	acks := &fakeAcks{ackedUsers: map[string][]string{"pv1": {"u1"}}}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	total, acked, overdue, err := r.Completion(context.Background(), "pv1", "")
	if err != nil {
		t.Fatalf("Completion: %v", err)
	}
	if total != 0 || acked != 0 || len(overdue) != 0 {
		t.Fatalf("procedure completion: want total=0 acked=0 overdue=0, got total=%d acked=%d overdue=%v", total, acked, overdue)
	}
}

// TestAudienceUsers_DocTypeLookupErrorPropagates proves the gate fails loud: an
// errored PolicyDocumentType lookup propagates (nil audience, non-nil error) and
// is NEVER silently treated as a policy — matching the helper's fail-loud
// contract so an error can't accidentally resolve a full audience.
func TestAudienceUsers_DocTypeLookupErrorPropagates(t *testing.T) {
	wantErr := errors.New("core GetPolicy unavailable")
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "grp-all"},
		policyNumbers: map[string]string{"p1": "POL-ALL"},
		docTypeErr:    wantErr,
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)

	users, err := r.AudienceUsers(context.Background(), "p1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("want doc-type lookup error propagated, got err=%v users=%v", err, users)
	}
	if users != nil {
		t.Errorf("audience must be nil when the doc-type lookup errors, got %v", users)
	}
}

// recordingRetireNotifier satisfies consumer.RetireNotifier and records sends.
type recordingRetireNotifier struct {
	sent []notify.AckReminderPayload
}

func (r *recordingRetireNotifier) SendPolicyRetired(_ context.Context, p notify.AckReminderPayload) {
	r.sent = append(r.sent, p)
}

func retiredEventBody(t *testing.T, policyID string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"event_type": "policy.retired",
		"retired_at": time.Now().UTC(),
		"policy_id":  policyID,
		"number":     "REF-1",
		"title":      "Doc",
	})
	if err != nil {
		t.Fatalf("marshal retired event: %v", err)
	}
	return b
}

// TestPolicyRetiredConsumer_ProcedureNotifiesNoOne wires a REAL resolver into the
// REAL policy_retired consumer and proves the end-to-end path: a retired
// PROCEDURE notifies NO ONE (the gate empties the audience), while a retired
// POLICY under the same everyone-ack chain notifies the full audience.
func TestPolicyRetiredConsumer_ProcedureNotifiesNoOne(t *testing.T) {
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-all": everyoneAckChain("All"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}, "u2": {"AD-B"}}}

	// Retired PROCEDURE → no notices.
	procCore := &fakeCore{
		homeGroups:    map[string]string{"proc1": "grp-all"},
		policyNumbers: map[string]string{"proc1": "PROC-1"},
		policyTitles:  map[string]string{"proc1": "A Procedure"},
		docTypes:      map[string]string{"proc1": "procedure"},
	}
	procResolver := obligation.NewResolver(procCore, idn, chains, &fakeAcks{}, nil)
	procNotifier := &recordingRetireNotifier{}
	procConsumer := consumer.NewPolicyRetiredConsumer(procResolver, procNotifier)
	if err := procConsumer.Handle(context.Background(), retiredEventBody(t, "proc1")); err != nil {
		t.Fatalf("Handle (procedure): %v", err)
	}
	if len(procNotifier.sent) != 0 {
		t.Fatalf("retired PROCEDURE must notify no one, got %d notices", len(procNotifier.sent))
	}

	// Retired POLICY → full audience notified (regression).
	polCore := &fakeCore{
		homeGroups:    map[string]string{"p1": "grp-all"},
		policyNumbers: map[string]string{"p1": "POL-1"},
		policyTitles:  map[string]string{"p1": "A Policy"},
		docTypes:      map[string]string{"p1": "policy"},
	}
	polResolver := obligation.NewResolver(polCore, idn, chains, &fakeAcks{}, nil)
	polNotifier := &recordingRetireNotifier{}
	polConsumer := consumer.NewPolicyRetiredConsumer(polResolver, polNotifier)
	if err := polConsumer.Handle(context.Background(), retiredEventBody(t, "p1")); err != nil {
		t.Fatalf("Handle (policy): %v", err)
	}
	if len(polNotifier.sent) != 2 {
		t.Fatalf("retired POLICY must notify the full audience (2), got %d", len(polNotifier.sent))
	}
}

// TestOutstandingObligationsAcrossUsers verifies the sweep's enumeration source:
// it returns exactly the (user, version) pairs that are still un-acked across
// all obligating policies, using the same on-change (exact version) vs
// on-publish (any version) ack-keying as MyObligations.
func TestOutstandingObligationsAcrossUsers(t *testing.T) {
	ctx := context.Background()
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "On-change", VersionNo: 2, PublishedVersionID: "v1b", OnChange: true},
			{PolicyID: "p2", Number: "POL-002", Title: "On-publish", VersionNo: 2, PublishedVersionID: "v2a", OnChange: false},
		},
		homeGroups:    map[string]string{"p1": "grp-a", "p2": "grp-a"},
		policyNumbers: map[string]string{"p1": "POL-001", "p2": "POL-002"},
		policyVersions: map[string][]string{
			"p2": {"v2z", "v2a"},
		},
	}
	chains := &fakeChains{chains: map[string][]authz.CategoryRuleset{
		"grp-a": groupAckChain("GroupA", "AD-A"),
	}}
	idn := &fakeIdentity{adGroups: map[string][]string{
		"u1": {"AD-A"},
		"u2": {"AD-A"},
	}}
	acks := &fakeAcks{
		ackedUsers: map[string][]string{
			"v1b": {"u1"}, // p1 on-change: u1 acked current version, u2 did not
		},
		acked: map[string]map[string]bool{
			"u2": {"v2z": true}, // p2 on-publish: u2 acked an OLD version -> cleared
		},
	}
	r := obligation.NewResolver(core, idn, chains, acks, nil)

	out, err := r.OutstandingObligations(ctx)
	if err != nil {
		t.Fatalf("OutstandingObligations: %v", err)
	}

	// Expect: p1/u2 (not acked current), p1/u1 cleared; p2/u1 (never acked any),
	// p2/u2 cleared (old-version ack).
	got := map[string]bool{}
	for _, o := range out {
		got[o.PolicyID+"|"+o.UserID] = true
	}
	want := []string{"p1|u2", "p2|u1"}
	if len(out) != len(want) {
		t.Fatalf("outstanding = %d (%+v); want %d (%v)", len(out), out, len(want), want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing outstanding %q; got %+v", w, got)
		}
	}
	// A cleared pair must never appear.
	if got["p1|u1"] || got["p2|u2"] {
		t.Errorf("cleared obligation surfaced as outstanding: %+v", got)
	}
}
