// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation

import (
	"context"
	"fmt"
	"time"

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
// evaluation for Completion / Roster / ReleaseDroppedPending.
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

// VersionRef returns the owning policy ID and the version number of a
// policyVersionID via core's GetPolicyVersion. An unknown version answers an
// empty policy ID.
func (a *CoreAdapter) VersionRef(ctx context.Context, policyVersionID string) (string, int32, error) {
	resp, err := a.client.GetPolicyVersion(ctx, &corev1.GetPolicyVersionRequest{Id: policyVersionID})
	if err != nil {
		return "", 0, err
	}
	return resp.GetVersion().GetPolicyId(), resp.GetVersion().GetVersionNo(), nil
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

// UserReadClearance returns the user's GLOBAL roles, their individual
// read_sensitive grant and whether they hold the root seat, via identity
// GetUser: the inputs to the obligation sensitivity gate and the root
// exemption. No role carries policy.read_sensitive; the grant is the per-user
// override.
func (a *IdentityAdapter) UserReadClearance(ctx context.Context, userID string) (roles []string, readSensitive, root bool, err error) {
	resp, err := a.client.GetUser(ctx, &identityv1.GetUserRequest{UserId: userID})
	if err != nil {
		return nil, false, false, err
	}
	u := resp.GetUser()
	return u.GetRoles(), u.GetReadSensitiveGrant(), u.GetIsRoot(), nil
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
// ChainAdapter reads a category's effective ruleset from core's
// CategoryService: the category's own levels and its ancestors', in one call.
// ---------------------------------------------------------------------------

// ChainAdapter adapts the generated CategoryServiceClient to categoryChains.
type ChainAdapter struct {
	cc corev1.CategoryServiceClient
}

// NewChainAdapter returns a ChainAdapter on cc.
func NewChainAdapter(cc corev1.CategoryServiceClient) *ChainAdapter {
	return &ChainAdapter{cc: cc}
}

// EffectiveLevels returns categoryID's chain, nearest level first, from one
// GetEffectiveRuleset call. A chain with no levels is an error, never an empty
// rule set, so a lookup that went wrong cannot read as "nobody is asked".
func (a *ChainAdapter) EffectiveLevels(ctx context.Context, categoryID string) ([]*corev1.EffectiveLevel, error) {
	resp, err := a.cc.GetEffectiveRuleset(ctx, &corev1.GetEffectiveRulesetRequest{CategoryId: categoryID})
	if err != nil {
		return nil, fmt.Errorf("get effective ruleset %s: %w", categoryID, err)
	}
	if len(resp.GetLevels()) == 0 {
		return nil, fmt.Errorf("get effective ruleset %s: no levels", categoryID)
	}
	return resp.GetLevels(), nil
}
