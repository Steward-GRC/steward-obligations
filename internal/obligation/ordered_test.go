// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
	"google.golang.org/grpc"
)

// The ack audience is decided by go-authz's ordered model: per action, the
// first non-blank cell wins, a category's own rows before its parents'; owners
// never owe an acknowledgement by ownership; Ack requires Read; root is exempt
// for everything but Ack; a site admin who is not root follows the rows.

func audienceOf(t *testing.T, chain []*corev1.EffectiveLevel, idn *fakeIdentity) []string {
	t.Helper()
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "cat"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	r := obligation.NewResolver(core, idn, &fakeChains{chains: map[string][]*corev1.EffectiveLevel{"cat": chain}}, &fakeAcks{}, nil)
	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AudienceUsers: %v", err)
	}
	return audienceIDs(t, users)
}

func TestOrdered_AckRequiresRead(t *testing.T) {
	// u-blocked is allowed Ack by a row above, but denied Read: the ack is
	// not allowed because Ack requires Read.
	chain := []*corev1.EffectiveLevel{level("cat", nil,
		userRule("u-blocked", deny, allow),
		everyoneRule(allow, allow),
	)}
	idn := &fakeIdentity{adGroups: map[string][]string{"u-blocked": nil, "u-ok": nil}}
	if got, want := audienceOf(t, chain, idn), []string{"u-ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestOrdered_OwnerNotAskedToAck(t *testing.T) {
	// The owner holds Read by ownership but has no Ack cell anywhere.
	chain := []*corev1.EffectiveLevel{level("cat", []string{"u-owner"}, groupRule("AD-A", allow, allow))}
	idn := &fakeIdentity{adGroups: map[string][]string{"u-owner": nil, "u-member": {"AD-A"}}}
	if got, want := audienceOf(t, chain, idn), []string{"u-member"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestOrdered_InheritedOwnerNotAskedToAck(t *testing.T) {
	chain := []*corev1.EffectiveLevel{
		level("child", nil, groupRule("AD-A", allow, allow)),
		level("parent", []string{"u-owner"}),
	}
	idn := &fakeIdentity{adGroups: map[string][]string{"u-owner": nil, "u-member": {"AD-A"}}}
	if got, want := audienceOf(t, chain, idn), []string{"u-member"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestOrdered_RootAckFollowsTheRule(t *testing.T) {
	// Root is exempt for Read but not for Ack: with no Ack cell for it, root
	// is not asked; with one, root is, even though a row denies it Read.
	noAck := []*corev1.EffectiveLevel{level("cat", nil, groupRule("AD-A", allow, allow))}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u-root": nil, "u-member": {"AD-A"}},
		root:     map[string]bool{"u-root": true},
	}
	if got, want := audienceOf(t, noAck, idn), []string{"u-member"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root without an ack cell: want %v, got %v", want, got)
	}

	withAck := []*corev1.EffectiveLevel{level("cat", nil,
		userRule("u-root", deny, allow),
		groupRule("AD-A", allow, allow),
	)}
	got := audienceOf(t, withAck, idn)
	if !reflect.DeepEqual(got, []string{"u-member", "u-root"}) {
		t.Fatalf("root with an ack allow (Read deny is ignored for root): got %v", got)
	}

	denyAck := []*corev1.EffectiveLevel{level("cat", nil,
		userRule("u-root", allow, deny),
		everyoneRule(allow, allow),
	)}
	if got, want := audienceOf(t, denyAck, idn), []string{"u-member"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root with an ack deny: want %v, got %v", want, got)
	}
}

func TestOrdered_StrictPerAction(t *testing.T) {
	// u1's group row has only a Read cell; its blank Ack cell is skipped and
	// the parent's Everyone Ack decides. A child Ack deny beats the parent's
	// allow for u2.
	chain := []*corev1.EffectiveLevel{
		level("child", nil,
			userRule("u2", allow, deny),
			groupRule("AD-A", allow, blank),
		),
		level("parent", nil, everyoneRule(allow, allow)),
	}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}, "u2": nil, "u3": nil}}
	if got, want := audienceOf(t, chain, idn), []string{"u1", "u3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestOrdered_ReadOnlyRowWithNoAckAnywhereIsNotAsked(t *testing.T) {
	chain := []*corev1.EffectiveLevel{level("cat", nil, groupRule("AD-A", allow, blank))}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}}}
	if got := audienceOf(t, chain, idn); len(got) != 0 {
		t.Fatalf("a Read cell alone does not oblige an ack, got %v", got)
	}
}

func TestOrdered_NonRootSiteAdminFollowsTheRules(t *testing.T) {
	chain := []*corev1.EffectiveLevel{level("cat", nil,
		userRule("u-admin", deny, deny),
		everyoneRule(allow, allow),
	)}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u-admin": nil, "u-other": nil},
		roles:    map[string][]string{"u-admin": {"site-admin"}},
	}
	if got, want := audienceOf(t, chain, idn), []string{"u-other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a non-root site admin follows the deny: want %v, got %v", want, got)
	}
	// And the Everyone row applies to a site admin like anyone else.
	everyone := []*corev1.EffectiveLevel{level("cat", nil, everyoneRule(allow, allow))}
	if got := audienceOf(t, everyone, idn); !reflect.DeepEqual(got, []string{"u-admin", "u-other"}) {
		t.Fatalf("Everyone includes the site admin, got %v", got)
	}
}

// Root is exempt for Read, so a Read deny against it is ignored and does not
// remove it from the audience when an Ack allow applies. A site admin who is not
// root has no such exemption: the same Read deny removes it, because Ack
// requires Read.
func TestOrdered_ReadDenyRootStaysNonRootSiteAdminDrops(t *testing.T) {
	chain := []*corev1.EffectiveLevel{level("cat", nil,
		userRule("u-root", deny, allow),
		userRule("u-admin", deny, allow),
		everyoneRule(allow, allow),
	)}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u-root": nil, "u-admin": nil, "u-other": nil},
		roles:    map[string][]string{"u-admin": {"site-admin"}},
		root:     map[string]bool{"u-root": true},
	}
	if got, want := audienceOf(t, chain, idn), []string{"u-other", "u-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root stays (Read exempt, Ack allowed), non-root site admin drops (Read deny): want %v, got %v", want, got)
	}
}

func TestOrdered_ManageCellConfersReadForAck(t *testing.T) {
	// Manage implies Read, so a Manage allow lets a blank-Read row's Ack stand.
	chain := []*corev1.EffectiveLevel{level("cat", nil,
		&corev1.CategoryRule{
			SubjectKind: corev1.RuleSubjectKind_RULE_SUBJECT_KIND_USER, SubjectRef: "u-mgr",
			Manage: allow, Ack: allow,
		},
	)}
	idn := &fakeIdentity{adGroups: map[string][]string{"u-mgr": nil, "u-x": nil}}
	if got, want := audienceOf(t, chain, idn), []string{"u-mgr"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestOrdered_SensitiveGateStillApplies_RootPasses(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "cat"},
		policyNumbers: map[string]string{"p1": "POL-001"},
		sensitive:     map[string]bool{"p1": true},
	}
	chain := []*corev1.EffectiveLevel{level("cat", nil, everyoneRule(allow, allow))}
	idn := &fakeIdentity{
		adGroups:      map[string][]string{"u-plain": nil, "u-granted": nil, "u-root": nil},
		readSensitive: map[string]bool{"u-granted": true},
		root:          map[string]bool{"u-root": true},
	}
	r := obligation.NewResolver(core, idn, &fakeChains{chains: map[string][]*corev1.EffectiveLevel{"cat": chain}}, &fakeAcks{}, nil)
	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := audienceIDs(t, users), []string{"u-granted", "u-root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// The override keeps its meaning (D6): deny drops the person, allow lifts only
// the Read requirement, and the Ack cell must still allow.
func TestOrdered_OverridesKept(t *testing.T) {
	core := &fakeCore{
		homeGroups:    map[string]string{"p1": "cat"},
		policyNumbers: map[string]string{"p1": "POL-001"},
	}
	chain := []*corev1.EffectiveLevel{level("cat", nil,
		userRule("u-allow", deny, allow),
		everyoneRule(allow, allow),
	)}
	idn := &fakeIdentity{
		adGroups: map[string][]string{"u-allow": nil, "u-deny": nil, "u-plain": nil},
		overrides: map[string]map[string]string{
			"u-allow": {"POL-001": "allow"},
			"u-deny":  {"POL-001": "deny"},
		},
	}
	r := obligation.NewResolver(core, idn, &fakeChains{chains: map[string][]*corev1.EffectiveLevel{"cat": chain}}, &fakeAcks{}, nil)
	users, err := r.AudienceUsers(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := audienceIDs(t, users), []string{"u-allow", "u-plain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// fakeCategoryClient answers GetEffectiveRuleset only.
type fakeCategoryClient struct {
	corev1.CategoryServiceClient
	levels []*corev1.EffectiveLevel
	err    error
	calls  []string
}

func (f *fakeCategoryClient) GetEffectiveRuleset(_ context.Context, in *corev1.GetEffectiveRulesetRequest, _ ...grpc.CallOption) (*corev1.GetEffectiveRulesetResponse, error) {
	f.calls = append(f.calls, in.GetCategoryId())
	if f.err != nil {
		return nil, f.err
	}
	return &corev1.GetEffectiveRulesetResponse{Levels: f.levels}, nil
}

func TestChainAdapter_ReadsTheChainWithOneCall(t *testing.T) {
	want := []*corev1.EffectiveLevel{level("child", nil), level("parent", nil)}
	cc := &fakeCategoryClient{levels: want}
	got, err := obligation.NewChainAdapter(cc).EffectiveLevels(context.Background(), "child")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(cc.calls, []string{"child"}) {
		t.Fatalf("want 2 levels from one GetEffectiveRuleset(child), got %d levels, calls %v", len(got), cc.calls)
	}
}

func TestChainAdapter_NoLevelsAndErrorsFailLoud(t *testing.T) {
	if _, err := obligation.NewChainAdapter(&fakeCategoryClient{}).EffectiveLevels(context.Background(), "c"); err == nil {
		t.Fatal("a chain with no levels must be an error")
	}
	boom := errors.New("boom")
	if _, err := obligation.NewChainAdapter(&fakeCategoryClient{err: boom}).EffectiveLevels(context.Background(), "c"); !errors.Is(err, boom) {
		t.Fatalf("want the RPC error, got %v", err)
	}
}
