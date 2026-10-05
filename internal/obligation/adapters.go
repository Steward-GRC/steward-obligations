// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation

import (
	"context"
	"fmt"
	"time"

	authz "github.com/Steward-GRC/steward-authz"
	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	identityv1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/identity/v1"
)

// ---------------------------------------------------------------------------
// CoreAdapter: wraps the corev1.PolicyServiceClient to satisfy coreObligations.
// ---------------------------------------------------------------------------

// CoreAdapter adapts the generated corev1.PolicyServiceClient to the narrow
// coreObligations interface consumed by the Resolver.
type CoreAdapter struct {
	client corev1.PolicyServiceClient
}

// NewCoreAdapter returns a CoreAdapter wrapping the given PolicyServiceClient.
func NewCoreAdapter(c corev1.PolicyServiceClient) *CoreAdapter { return &CoreAdapter{client: c} }

// ListObligatingPolicies calls core and projects to the local ObligatingPolicy
// shape so the resolver has no proto import dependency.
func (a *CoreAdapter) ListObligatingPolicies(ctx context.Context) ([]ObligatingPolicy, error) {
	resp, err := a.client.ListObligatingPolicies(ctx, &corev1.ListObligatingPoliciesRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]ObligatingPolicy, len(resp.GetPolicies()))
	for i, p := range resp.GetPolicies() {
		out[i] = ObligatingPolicy{
			PolicyID:           p.GetPolicyId(),
			Number:             p.GetNumber(),
			Title:              p.GetTitle(),
			VersionNo:          p.GetVersionNo(),
			PublishedVersionID: p.GetPublishedVersionId(),
			OnChange:           p.GetOnChange(),
		}
	}
	return out, nil
}

// ListPolicyVersionIDs returns the version IDs for a policy via core's
// ListPolicyVersions RPC. The resolver uses these for on-publish keying: a
// user who has acked ANY of a policy's versions has satisfied the obligation.
func (a *CoreAdapter) ListPolicyVersionIDs(ctx context.Context, policyID string) ([]string, error) {
	resp, err := a.client.ListPolicyVersions(ctx, &corev1.ListPolicyVersionsRequest{PolicyId: policyID})
	if err != nil {
		return nil, err
	}
	versions := resp.GetVersions()
	ids := make([]string, 0, len(versions))
	for _, v := range versions {
		ids = append(ids, v.GetId())
	}
	return ids, nil
}

// PolicyHomeGroup returns the policy's home group id (core GetPolicy).
func (a *CoreAdapter) PolicyHomeGroup(ctx context.Context, policyID string) (string, error) {
	resp, err := a.client.GetPolicy(ctx, &corev1.GetPolicyRequest{Id: policyID})
	if err != nil {
		return "", err
	}
	return resp.GetPolicy().GetHomeCategoryId(), nil
}

// PolicyNumber returns the policy's display number (e.g. "POL-001") for the
// given policy ID. Used to look up per-policy overrides during RACI chain
// evaluation for Completion / Roster / PurgeOrphanedAcks.
func (a *CoreAdapter) PolicyNumber(ctx context.Context, policyID string) (string, error) {
	resp, err := a.client.GetPolicy(ctx, &corev1.GetPolicyRequest{Id: policyID})
	if err != nil {
		return "", err
	}
	return resp.GetPolicy().GetNumber(), nil
}

// PolicyDisplay returns the policy's human-facing display number (e.g.
// "POL-001") and title for the given policy ID, in one GetPolicy call. The
// notify consumer uses these to populate the reference/title shown in emails,
// so recipients never see the raw policy version UUID.
func (a *CoreAdapter) PolicyDisplay(ctx context.Context, policyID string) (number, title string, err error) {
	resp, err := a.client.GetPolicy(ctx, &corev1.GetPolicyRequest{Id: policyID})
	if err != nil {
		return "", "", err
	}
	return resp.GetPolicy().GetNumber(), resp.GetPolicy().GetTitle(), nil
}

// PolicySensitivity reports whether policyID is classified SENSITIVE, via core
// GetPolicy. The obligation resolver's sensitivity gate uses it so a user
// without read_sensitive clearance is not obligated to ack a policy they can
// never open (mirrors the gateway read path — see resolver.go obligated).
func (a *CoreAdapter) PolicySensitivity(ctx context.Context, policyID string) (bool, error) {
	resp, err := a.client.GetPolicy(ctx, &corev1.GetPolicyRequest{Id: policyID})
	if err != nil {
		return false, err
	}
	return resp.GetPolicy().GetSensitivity() == corev1.Sensitivity_SENSITIVITY_SENSITIVE, nil
}

// PolicyDocumentType returns the policy's document-type discriminator via core
// GetPolicy, as a stable lowercase string: "procedure" for a document typed
// DOCUMENT_TYPE_PROCEDURE, else "policy" (UNSPECIFIED and POLICY both map to
// "policy", the back-compat default). The resolver's audienceUsers gate uses
// this to exclude procedures from ack-bearing audiences: a
// procedure obligates/notifies no one.
func (a *CoreAdapter) PolicyDocumentType(ctx context.Context, policyID string) (string, error) {
	resp, err := a.client.GetPolicy(ctx, &corev1.GetPolicyRequest{Id: policyID})
	if err != nil {
		return "", err
	}
	if resp.GetPolicy().GetDocumentType() == corev1.DocumentType_DOCUMENT_TYPE_PROCEDURE {
		return "procedure", nil
	}
	return "policy", nil
}

// ResolvePolicyObligation calls core and projects to PolicyObligation.
func (a *CoreAdapter) ResolvePolicyObligation(ctx context.Context, policyID string) (PolicyObligation, error) {
	resp, err := a.client.ResolvePolicyObligation(ctx, &corev1.ResolvePolicyObligationRequest{PolicyId: policyID})
	if err != nil {
		return PolicyObligation{}, err
	}
	return PolicyObligation{
		RequiresAck:        resp.GetRequiresAck(),
		OnChange:           resp.GetOnChange(),
		PublishedVersionID: resp.GetPublishedVersionId(),
	}, nil
}

// PolicyIDForVersion resolves the owning policy ID for a policyVersionID via
// core's GetPolicyVersion. Completion needs the policy ID to resolve the
// audience obligation, but the gateway only forwards a policyVersionID.
func (a *CoreAdapter) PolicyIDForVersion(ctx context.Context, policyVersionID string) (string, error) {
	resp, err := a.client.GetPolicyVersion(ctx, &corev1.GetPolicyVersionRequest{Id: policyVersionID})
	if err != nil {
		return "", err
	}
	return resp.GetVersion().GetPolicyId(), nil
}

// VersionPublishedAt returns the publish timestamp for a policy version, the
// baseline for avg-days-to-ack. Zero time if the version is not yet published.
func (a *CoreAdapter) VersionPublishedAt(ctx context.Context, policyVersionID string) (time.Time, error) {
	resp, err := a.client.GetPolicyVersion(ctx, &corev1.GetPolicyVersionRequest{Id: policyVersionID})
	if err != nil {
		return time.Time{}, err
	}
	if ts := resp.GetVersion().GetPublishedAt(); ts != nil {
		return ts.AsTime().UTC(), nil
	}
	return time.Time{}, nil
}

// ---------------------------------------------------------------------------
// IdentityAdapter: wraps identityv1.IdentityReadServiceClient to satisfy identityUsers.
// ---------------------------------------------------------------------------

// IdentityAdapter adapts the generated identityv1.IdentityReadServiceClient to
// the narrow identityUsers interface consumed by the Resolver.
type IdentityAdapter struct {
	client identityv1.IdentityReadServiceClient
}

// NewIdentityAdapter returns an IdentityAdapter wrapping the given client.
func NewIdentityAdapter(c identityv1.IdentityReadServiceClient) *IdentityAdapter {
	return &IdentityAdapter{client: c}
}

// UserAdGroups returns the directory group names a user's identity provider
// asserted, which group rules match on.
func (a *IdentityAdapter) UserAdGroups(ctx context.Context, userID string) ([]string, error) {
	resp, err := a.client.GetUser(ctx, &identityv1.GetUserRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	return resp.GetUser().GetIdpGroups(), nil
}

// UserPolicyOverrides returns the user's per-policy allow/deny overrides keyed
// by policy NUMBER, via identity GetUser. "allow" forces the policy into the
// user's ack audience (beats a group exclusion / non-membership); "deny" forces
// it out.
func (a *IdentityAdapter) UserPolicyOverrides(ctx context.Context, userID string) (map[string]string, error) {
	resp, err := a.client.GetUser(ctx, &identityv1.GetUserRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, o := range resp.GetUser().GetPolicyOverrides() {
		switch o.GetEffect() {
		case identityv1.OverrideEffect_OVERRIDE_EFFECT_ALLOW:
			out[o.GetPolicyNumber()] = "allow"
		case identityv1.OverrideEffect_OVERRIDE_EFFECT_DENY:
			out[o.GetPolicyNumber()] = "deny"
		}
	}
	return out, nil
}

// UserReadClearance returns the user's GLOBAL roles and their individual
// read_sensitive grant via identity GetUser — the inputs to the obligation
// sensitivity gate. Roles feed authz.PermissionsForRoles (compliance-admin /
// site-admin carry policy.read_sensitive); the grant is the per-user override.
func (a *IdentityAdapter) UserReadClearance(ctx context.Context, userID string) (roles []string, readSensitive bool, err error) {
	resp, err := a.client.GetUser(ctx, &identityv1.GetUserRequest{UserId: userID})
	if err != nil {
		return nil, false, err
	}
	u := resp.GetUser()
	return u.GetRoles(), u.GetReadSensitiveGrant(), nil
}

// ListAllUsers returns all ENABLED platform users — the candidate set the RACI
// chain is evaluated against to resolve a policy's ack audience.
func (a *IdentityAdapter) ListAllUsers(ctx context.Context) ([]User, error) {
	resp, err := a.client.ListAllUsers(ctx, &identityv1.ListAllUsersRequest{})
	if err != nil {
		return nil, err
	}
	users := make([]User, len(resp.GetUsers()))
	for i, u := range resp.GetUsers() {
		users[i] = User{ID: u.GetId(), Email: u.GetEmail()}
	}
	return users, nil
}

// ---------------------------------------------------------------------------
// ChainAdapter builds a category's access-rule chain from core's
// CategoryService.
type ChainAdapter struct {
	cc corev1.CategoryServiceClient
}

// NewChainAdapter returns a ChainAdapter on cc.
func NewChainAdapter(cc corev1.CategoryServiceClient) *ChainAdapter {
	return &ChainAdapter{cc: cc}
}

// maxChainDepth bounds the walk to the root, as a guard against a cycle.
const maxChainDepth = 20

// BuildCategoryChain walks from categoryID up to the root: chain[0] is the
// category itself, then each ancestor.
func (a *ChainAdapter) BuildCategoryChain(ctx context.Context, categoryID string) ([]authz.CategoryRuleset, error) {
	var chain []authz.CategoryRuleset
	for id := categoryID; id != "" && len(chain) < maxChainDepth; {
		c, err := a.cc.GetCategory(ctx, &corev1.GetCategoryRequest{Id: id})
		if err != nil {
			return nil, fmt.Errorf("get category %s: %w", id, err)
		}
		rs, err := a.cc.GetCategoryRuleset(ctx, &corev1.GetCategoryRulesetRequest{CategoryId: id})
		if err != nil {
			return nil, fmt.Errorf("get category ruleset %s: %w", id, err)
		}
		rules := make([]authz.Rule, 0, len(rs.GetRules()))
		for _, r := range rs.GetRules() {
			rules = append(rules, ruleFromProto(r))
		}
		cat := c.GetCategory()
		chain = append(chain, authz.CategoryRuleset{Name: cat.GetName(), Owners: cat.GetOwners(), Rules: rules})
		id = cat.GetParentId()
	}
	return chain, nil
}

func ruleFromProto(r *corev1.CategoryRule) authz.Rule {
	return authz.Rule{
		Subject: authz.RuleSubject{Kind: kindFromProto(r.GetSubjectKind()), Name: r.GetSubjectRef()},
		Grants: map[authz.Action]authz.Grant{
			authz.ActionRead:        grantFromProto(r.GetRead()),
			authz.ActionAcknowledge: grantFromProto(r.GetAck()),
			authz.ActionApprove:     grantFromProto(r.GetApprove()),
			authz.ActionAuthor:      grantFromProto(r.GetAuthor()),
		},
	}
}

func grantFromProto(e corev1.GrantEffect) authz.Grant {
	switch e {
	case corev1.GrantEffect_GRANT_EFFECT_ALLOW:
		return authz.GrantAllow
	case corev1.GrantEffect_GRANT_EFFECT_DENY:
		return authz.GrantDeny
	default:
		return authz.GrantBlank
	}
}

func kindFromProto(k corev1.RuleSubjectKind) authz.SubjectKind {
	switch k {
	case corev1.RuleSubjectKind_RULE_SUBJECT_KIND_GROUP:
		return authz.SubjectGroup
	case corev1.RuleSubjectKind_RULE_SUBJECT_KIND_USER:
		return authz.SubjectUser
	default:
		return authz.SubjectEveryone
	}
}
