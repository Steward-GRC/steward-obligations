// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package obligation_test

import (
	"context"
	"testing"
	"time"

	corev1 "github.com/Steward-GRC/steward-obligations/gen/go/thirdparty/core/v1"
	"github.com/Steward-GRC/steward-obligations/internal/obligation"
)

// An acknowledgement is due the configured number of days after the version
// the person must acknowledge was published.

func dueFixture(published map[string]time.Time) (*fakeCore, *fakeChains, *fakeIdentity) {
	core := &fakeCore{
		obligating: []obligation.ObligatingPolicy{
			{PolicyID: "p1", Number: "POL-001", Title: "One", VersionNo: 1, PublishedVersionID: "pv1"},
		},
		versionToPolicy: map[string]string{"pv1": "p1"},
		homeGroups:      map[string]string{"p1": "grp-a"},
		policyNumbers:   map[string]string{"p1": "POL-001"},
		resolutions: map[string]obligation.PolicyObligation{
			"p1": {RequiresAck: true, PublishedVersionID: "pv1"},
		},
		publishedAt: published,
	}
	chains := &fakeChains{chains: map[string][]*corev1.EffectiveLevel{"grp-a": groupAckChain("GroupA", "AD-A")}}
	idn := &fakeIdentity{adGroups: map[string][]string{"u1": {"AD-A"}, "u2": {"AD-A"}}}
	return core, chains, idn
}

func TestMyObligations_DueAtIsPublishPlusConfiguredDays(t *testing.T) {
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	core, chains, idn := dueFixture(map[string]time.Time{"pv1": published})
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil).WithAckDueDays(14)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 obligation, got %+v", got)
	}
	if want := published.AddDate(0, 0, 14); !got[0].DueAt.Equal(want) {
		t.Errorf("DueAt: want %v, got %v", want, got[0].DueAt)
	}
}

func TestMyObligations_PastDueKeepsItsDate(t *testing.T) {
	published := time.Now().UTC().AddDate(0, 0, -30)
	core, chains, idn := dueFixture(map[string]time.Time{"pv1": published})
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil).WithAckDueDays(14)

	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].DueAt.Before(time.Now()) {
		t.Fatalf("want one obligation already past due, got %+v", got)
	}
}

func TestMyObligations_NoDueDateWhenNoPublishTimeOrDaysOff(t *testing.T) {
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		pub  map[string]time.Time
		days int
	}{
		"unpublished version": {pub: nil, days: 14},
		"days set to zero":    {pub: map[string]time.Time{"pv1": published}, days: 0},
	} {
		t.Run(name, func(t *testing.T) {
			core, chains, idn := dueFixture(tc.pub)
			r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil).WithAckDueDays(tc.days)
			got, err := r.MyObligations(context.Background(), "u1")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !got[0].DueAt.IsZero() {
				t.Fatalf("want one obligation with no due date, got %+v", got)
			}
		})
	}
}

func TestMyObligations_DueDaysDefaultWhenNeverSet(t *testing.T) {
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	core, chains, idn := dueFixture(map[string]time.Time{"pv1": published})
	r := obligation.NewResolver(core, idn, chains, &fakeAcks{}, nil)
	got, err := r.MyObligations(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if want := published.AddDate(0, 0, obligation.DefaultAckDueDays); len(got) != 1 || !got[0].DueAt.Equal(want) {
		t.Fatalf("want due %v, got %+v", want, got)
	}
}

func TestCompletionAndRoster_DueAtOnUnacknowledgedOnly(t *testing.T) {
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	core, chains, idn := dueFixture(map[string]time.Time{"pv1": published})
	acks := &fakeAcks{ackedUsers: map[string][]string{"pv1": {"u1"}}, ackedAt: map[string]time.Time{"u1": published.AddDate(0, 0, 2)}}
	r := obligation.NewResolver(core, idn, chains, acks, nil).WithAckDueDays(14)
	want := published.AddDate(0, 0, 14)

	_, _, overdue, err := r.Completion(context.Background(), "pv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(overdue) != 1 || overdue[0].ID != "u2" || !overdue[0].DueAt.Equal(want) {
		t.Errorf("overdue: want u2 due %v, got %+v", want, overdue)
	}

	acked, pending, err := r.Roster(context.Background(), "pv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || !pending[0].DueAt.Equal(want) {
		t.Errorf("pending: want u2 due %v, got %+v", want, pending)
	}
	if len(acked) != 1 || acked[0].ID != "u1" {
		t.Errorf("acked: want u1, got %+v", acked)
	}
}
