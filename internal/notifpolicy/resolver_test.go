// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package notifpolicy_test

import (
	"context"
	"testing"

	"github.com/Steward-GRC/steward-obligations/internal/notifpolicy"
)

// fakeReader is an in-memory notifpolicy.PrefReader.
type fakeReader struct {
	channels notifpolicy.Channels
	cat      map[notifpolicy.Category]notifpolicy.Cadence
	override map[string]notifpolicy.Cadence
	err      error
}

func (f fakeReader) Channels(context.Context, string) (notifpolicy.Channels, error) {
	return f.channels, f.err
}
func (f fakeReader) CategoryCadence(_ context.Context, _ string, cat notifpolicy.Category) (notifpolicy.Cadence, bool, error) {
	c, ok := f.cat[cat]
	return c, ok, nil
}
func (f fakeReader) TypeOverride(_ context.Context, _, kind string) (notifpolicy.Cadence, bool, error) {
	c, ok := f.override[kind]
	return c, ok, nil
}

type fakeQuiet struct{ in bool }

func (f fakeQuiet) InWindow(context.Context, string) bool { return f.in }

// allChannelsOn is the default opt-out channel set (email+in-app on, push off).
var allChannelsOn = notifpolicy.Channels{Email: true, InApp: true, Push: true}

func newReader(cat map[notifpolicy.Category]notifpolicy.Cadence, override map[string]notifpolicy.Cadence) fakeReader {
	return fakeReader{channels: allChannelsOn, cat: cat, override: override}
}

func resolve(t *testing.T, r fakeReader, quiet bool, kind string) notifpolicy.Decision {
	t.Helper()
	dec, err := notifpolicy.NewResolver(r, fakeQuiet{in: quiet}).Resolve(context.Background(), "u-1", kind)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", kind, err)
	}
	return dec
}

// TestAckRequiredAlwaysImmediate is the LOCKED decision: the first-demand
// ack-required is ALWAYS immediate and never digest-foldable, regardless of the
// user's compliance-category cadence.
func TestAckRequiredAlwaysImmediate(t *testing.T) {
	for _, cad := range []notifpolicy.Cadence{
		notifpolicy.CadenceImmediate, notifpolicy.CadenceDaily,
		notifpolicy.CadenceWeekly, notifpolicy.CadenceOff,
	} {
		r := newReader(map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryCompliance: cad}, nil)
		dec := resolve(t, r, false, "ack-required")
		if !dec.Deliver {
			t.Fatalf("compliance=%s: ack-required must always deliver", cad)
		}
		if dec.Mode != notifpolicy.ModeImmediate {
			t.Errorf("compliance=%s: ack-required must be immediate, got %s", cad, dec.Mode)
		}
	}
}

// TestAckReminderIsDigestable proves the recurring reminder folds into a digest
// when the user chooses a daily/weekly compliance cadence.
func TestAckReminderIsDigestable(t *testing.T) {
	r := newReader(map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryCompliance: notifpolicy.CadenceDaily}, nil)
	dec := resolve(t, r, false, "policy-ack-reminder")
	if !dec.Deliver || dec.Mode != notifpolicy.ModeBatch {
		t.Fatalf("policy-ack-reminder daily: want deliver+batch, got deliver=%v mode=%s", dec.Deliver, dec.Mode)
	}
}

// TestComplianceFloorClampsOffToDaily proves the compliance floor: a mandatory
// compliance type can never be silenced -- off clamps up to a daily digest, so
// mandatory compliance mail still sends.
func TestComplianceFloorClampsOffToDaily(t *testing.T) {
	r := newReader(map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryCompliance: notifpolicy.CadenceOff}, nil)
	dec := resolve(t, r, false, "policy-ack-reminder")
	if !dec.Deliver {
		t.Fatal("compliance off must clamp to daily and still deliver, not suppress")
	}
	if dec.Mode != notifpolicy.ModeBatch {
		t.Errorf("clamped-to-daily reminder should batch, got %s", dec.Mode)
	}
}

// TestSecurityForceDeliveredPastChannelAndQuietHours proves the mandatory
// security set bypasses BOTH the channel switch and quiet hours (today's
// bypassSuppressionKinds, generalized): a recovery code reaches the user even
// with email off and inside quiet hours.
func TestSecurityForceDeliveredPastChannelAndQuietHours(t *testing.T) {
	r := fakeReader{channels: notifpolicy.Channels{Email: false, InApp: false, Push: false}}
	dec := resolve(t, r, true /* quiet */, "kratos-recovery")
	if !dec.Deliver || !dec.Email {
		t.Fatalf("kratos-recovery must force-deliver email past channel-off + quiet hours, got deliver=%v email=%v", dec.Deliver, dec.Email)
	}
	if !dec.BypassQuietHours {
		t.Error("kratos-recovery must report BypassQuietHours")
	}
}

// TestTransactionalForceDelivered proves welcome-account (transactional,
// mandatory) is force-delivered like the security set.
func TestTransactionalForceDelivered(t *testing.T) {
	r := fakeReader{channels: notifpolicy.Channels{Email: false}}
	dec := resolve(t, r, true, "welcome-account")
	if !dec.Email {
		t.Fatal("welcome-account must force-deliver email past channel-off + quiet hours")
	}
}

// TestCriticalComplianceBypassesQuietHoursButNotChannel proves policy-escalation
// (compliance, critical) bypasses quiet hours (critical) but still respects the
// email channel switch (it is not in the security/transactional force set).
func TestCriticalComplianceBypassesQuietHoursButNotChannel(t *testing.T) {
	// Email on + quiet hours -> still sends (critical bypass).
	on := resolve(t, newReader(nil, nil), true, "policy-escalation")
	if !on.Email {
		t.Error("policy-escalation should bypass quiet hours (critical) with email on")
	}
	if !on.BypassQuietHours {
		t.Error("policy-escalation should report BypassQuietHours")
	}
	// Email off -> suppressed on email (channel respected).
	off := resolve(t, fakeReader{channels: notifpolicy.Channels{Email: false, InApp: true}}, false, "policy-escalation")
	if off.Email {
		t.Error("policy-escalation must respect the email channel switch (not in the force set)")
	}
}

// TestQuietHoursSuppressEmailNotInApp proves a normal immediate compliance send
// (ack-required) is email-suppressed in quiet hours while in-app still delivers.
func TestQuietHoursSuppressEmailNotInApp(t *testing.T) {
	dec := resolve(t, newReader(nil, nil), true, "ack-required")
	if dec.Email {
		t.Error("ack-required email must be suppressed in quiet hours (not critical, not bypass)")
	}
	if !dec.InApp {
		t.Error("in-app is quiet-hours exempt and must still deliver")
	}
	if dec.Push {
		t.Error("push default-off channel should be false")
	}
}

// TestOptionalOffSuppresses proves an optional type set to off is fully
// suppressed on every channel.
func TestOptionalOffSuppresses(t *testing.T) {
	r := newReader(map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryInformational: notifpolicy.CadenceOff}, nil)
	dec := resolve(t, r, false, "policy-published")
	if dec.Deliver || dec.Email || dec.InApp || dec.Push {
		t.Fatalf("informational off must suppress all channels, got %+v", dec)
	}
}

// TestChannelSwitchesIntersect proves the channel master switches gate delivery.
func TestChannelSwitchesIntersect(t *testing.T) {
	r := fakeReader{channels: notifpolicy.Channels{Email: true, InApp: false, Push: true}}
	dec := resolve(t, r, false, "policy-published")
	if !dec.Email || dec.InApp || !dec.Push {
		t.Fatalf("channels must intersect: want email+push only, got %+v", dec)
	}
}

// TestTypeOverrideWinsOverCategory proves an advanced per-type override takes
// precedence over the category cadence.
func TestTypeOverrideWinsOverCategory(t *testing.T) {
	// Category immediate, but override this one type off -> suppressed.
	r := newReader(
		map[notifpolicy.Category]notifpolicy.Cadence{notifpolicy.CategoryInformational: notifpolicy.CadenceImmediate},
		map[string]notifpolicy.Cadence{"policy-published": notifpolicy.CadenceOff},
	)
	dec := resolve(t, r, false, "policy-published")
	if dec.Deliver {
		t.Error("type override off must win over category immediate")
	}
}

// TestPolicyPublishedDefaultsImmediate proves the informational new-policy notice
// defaults to an immediate send (its per-type default), preserving today's
// behavior even though the informational category default is daily.
func TestPolicyPublishedDefaultsImmediate(t *testing.T) {
	dec := resolve(t, newReader(nil, nil), false, "policy-published")
	if !dec.Deliver || dec.Mode != notifpolicy.ModeImmediate {
		t.Fatalf("policy-published default: want immediate deliver, got deliver=%v mode=%s", dec.Deliver, dec.Mode)
	}
}

// TestUnknownKindIsPermissiveImmediate proves a kind with no taxonomy row is not
// silently dropped: it defaults to an immediate delivery honoring channels.
func TestUnknownKindIsPermissiveImmediate(t *testing.T) {
	dec := resolve(t, newReader(nil, nil), false, "some-future-template")
	if !dec.Deliver || dec.Mode != notifpolicy.ModeImmediate || !dec.Email {
		t.Fatalf("unknown kind must default to permissive immediate delivery, got %+v", dec)
	}
}

// TestResolverErrorPropagates proves a channel-store error surfaces rather than
// silently delivering.
func TestResolverErrorPropagates(t *testing.T) {
	r := fakeReader{err: context.DeadlineExceeded}
	_, err := notifpolicy.NewResolver(r, fakeQuiet{}).Resolve(context.Background(), "u", "ack-required")
	if err == nil {
		t.Fatal("expected the reader error to propagate")
	}
}

// TestTaxonomyClassification spot-checks the encoded table.
func TestTaxonomyClassification(t *testing.T) {
	cases := []struct {
		kind      string
		cat       notifpolicy.Category
		mandatory bool
		batchable bool
	}{
		{"ack-required", notifpolicy.CategoryCompliance, true, false},
		{"policy-ack-reminder", notifpolicy.CategoryCompliance, true, true},
		{"policy-escalation", notifpolicy.CategoryCompliance, true, false},
		{"policy-published", notifpolicy.CategoryInformational, false, true},
		{"otp", notifpolicy.CategorySecurity, true, false},
		{"welcome-account", notifpolicy.CategoryTransactional, true, false},
		{"workflow-awaiting-approval", notifpolicy.CategoryWorkflow, false, true},
	}
	for _, c := range cases {
		class, ok := notifpolicy.Classify(c.kind)
		if !ok {
			t.Errorf("%s: not classified", c.kind)
			continue
		}
		if class.Category != c.cat {
			t.Errorf("%s: category=%s want %s", c.kind, class.Category, c.cat)
		}
		if class.Mandatory() != c.mandatory {
			t.Errorf("%s: mandatory=%v want %v", c.kind, class.Mandatory(), c.mandatory)
		}
		if class.Delivery.Batchable() != c.batchable {
			t.Errorf("%s: batchable=%v want %v", c.kind, class.Delivery.Batchable(), c.batchable)
		}
	}
}
