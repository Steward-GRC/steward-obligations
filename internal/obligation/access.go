// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation

import (
	"github.com/Bugs5382/go-authz/ordered"

	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
)

// The ack audience is decided by go-authz's ordered model. Per top-level
// category, ordered rows (a user, a group or Everyone) carry a cell for each of
// the actions Read, Author, Approve, Manage and Ack. For one action the rows are
// read from the top, the category's own before each parent's, and the first row
// that matches the person and has a non-blank cell for that action decides.
// Core stores the rows and the owners and evaluates nothing; this file turns
// what core returns into an ordered.RuleSet and asks the engine.

// The five actions of the model, as the engine spells them.
const (
	actRead    = "read"
	actAuthor  = "author"
	actApprove = "approve"
	actManage  = "manage"
	actAck     = "ack"
)

// rootRole is the engine role of the one root seat. It comes from identity's
// is_root flag, never from a user name or id.
const rootRole = "root"

// ackModel is the engine configuration: the five default actions (Manage
// implies Read and Author, Ack requires Read, owners hold Read, Author, Approve
// and Manage but not Ack) and the root seat, exempt for every action except
// Ack. A site admin who is not root is decided by the rows like anyone else.
var ackModel = func() ordered.Model {
	m := ordered.DefaultModel()
	m.Superusers = &ordered.Superusers{
		Subjects: []string{"role:" + rootRole},
		Actions:  []string{actRead, actAuthor, actApprove, actManage},
	}
	return m
}()

// ackUngatedModel is ackModel with Ack's Read requirement lifted. It answers the
// acknowledge rule alone, for a document a per-document override allows the
// reader to open outside the rules.
var ackUngatedModel = func() ordered.Model {
	m := ackModel
	acts := ordered.DefaultActions()
	for i := range acts {
		if acts[i].ID == actAck {
			acts[i].Requires = nil
		}
	}
	m.Actions = acts
	return m
}()

// categoryAccess is one category's rules and its ancestors', ready to decide.
type categoryAccess struct {
	rs   ordered.RuleSet
	leaf string
}

// newCategoryAccess builds the rule set from core's chain, nearest level first.
// Owners are people: core stores their user ids and the engine matches
// "user:<id>". No levels is an empty rule set, which allows nothing.
func newCategoryAccess(levels []*corev1.EffectiveLevel) *categoryAccess {
	a := &categoryAccess{rs: ordered.RuleSet{
		Categories: map[string]ordered.Category{},
		Parents:    map[string]string{},
	}}
	if len(levels) == 0 {
		return a
	}
	a.leaf = levels[0].GetCategoryId()
	for i, l := range levels {
		id := l.GetCategoryId()
		owners := make([]string, 0, len(l.GetOwners()))
		for _, o := range l.GetOwners() {
			owners = append(owners, "user:"+o)
		}
		a.rs.Categories[id] = ordered.Category{Entries: rulesEntries(l.GetRules()), Owners: owners}
		if i+1 < len(levels) {
			a.rs.Parents[id] = levels[i+1].GetCategoryId()
		}
	}
	return a
}

// ackAllowed reports whether the person's Ack answers allowed. Ack requires
// Read, so a person who may not read the document is not asked; ungated lifts
// that requirement for a document an override opens to them.
func (a *categoryAccess) ackAllowed(p ordered.Person, ungated bool) bool {
	m := ackModel
	if ungated {
		m = ackUngatedModel
	}
	return m.Decide(a.rs, p, a.leaf, actAck).Allowed
}

// ruleSubject is the engine subject of a row: "user:<id>", "group:<name>" or
// the Everyone subject.
func ruleSubject(kind corev1.RuleSubjectKind, ref string) string {
	switch kind {
	case corev1.RuleSubjectKind_RULE_SUBJECT_KIND_GROUP:
		return "group:" + ref
	case corev1.RuleSubjectKind_RULE_SUBJECT_KIND_USER:
		return "user:" + ref
	default:
		return ordered.Everyone
	}
}

// grantEffect is a core grant as an engine effect; blank has none.
func grantEffect(e corev1.GrantEffect) (ordered.Effect, bool) {
	switch e {
	case corev1.GrantEffect_GRANT_EFFECT_ALLOW:
		return ordered.Allow, true
	case corev1.GrantEffect_GRANT_EFFECT_DENY:
		return ordered.Deny, true
	default:
		return "", false
	}
}

// rulesEntries flattens rows into the engine's entries: one entry per non-blank
// cell, in the order of the five actions, keeping row order.
func rulesEntries(rules []*corev1.CategoryRule) []ordered.Entry {
	var out []ordered.Entry
	for _, r := range rules {
		subject := ruleSubject(r.GetSubjectKind(), r.GetSubjectRef())
		cells := []struct {
			action string
			grant  corev1.GrantEffect
		}{
			{actRead, r.GetRead()},
			{actAuthor, r.GetAuthor()},
			{actApprove, r.GetApprove()},
			{actManage, r.GetManage()},
			{actAck, r.GetAck()},
		}
		for _, c := range cells {
			if eff, ok := grantEffect(c.grant); ok {
				out = append(out, ordered.Entry{Subject: subject, Action: c.action, Effect: eff})
			}
		}
	}
	return out
}

// personFor is the person the engine decides for. The rows name users, groups
// and Everyone, so roles do not matter to them; the one role the engine knows
// is root, set from identity's is_root flag.
func personFor(userID string, groups []string, root bool) ordered.Person {
	p := ordered.Person{ID: userID, Groups: groups}
	if root {
		p.Roles = []string{rootRole}
	}
	return p
}
