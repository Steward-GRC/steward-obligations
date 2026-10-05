// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"fmt"
	"testing"

	log "github.com/Bugs5382/go-log"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/emailservice"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

func usableMailgun() *fakeProvider {
	return &fakeProvider{
		cfg: emailservice.MailgunConfig{APIKey: "k", Domain: "mg.example.org", Region: "us"},
		ok:  true,
	}
}

func TestDecider_UnconfiguredWithSMTP_DoesNotPause(t *testing.T) {
	// The dev/local case: Mailgun unset, SMTP (maildev) available → never pause.
	dec := mail.NewPauseDecider(&fakeProvider{ok: false}, nil, nil, true)
	require.False(t, dec.ShouldPause(context.Background()))
}

func TestDecider_UnconfiguredWithoutSMTP_Pauses(t *testing.T) {
	dec := mail.NewPauseDecider(&fakeProvider{ok: false}, nil, nil, false)
	require.True(t, dec.ShouldPause(context.Background()), "no Mailgun and no SMTP → nothing can send → hold")
}

func TestDecider_ProviderErrorDegradesToSMTP(t *testing.T) {
	prov := &fakeProvider{ok: true, err: fmt.Errorf("core down")}
	require.False(t, mail.NewPauseDecider(prov, nil, nil, true).ShouldPause(context.Background()),
		"a provider error with SMTP available degrades to SMTP, not a pause")
	require.True(t, mail.NewPauseDecider(prov, nil, nil, false).ShouldPause(context.Background()),
		"a provider error with no SMTP fallback holds")
}

func TestDecider_MailgunHealthy_DoesNotPause(t *testing.T) {
	b := mail.NewBreaker(log.New("dec-test"))
	dec := mail.NewPauseDecider(usableMailgun(), b, nil, true)
	require.False(t, dec.ShouldPause(context.Background()), "configured + breaker closed → send via Mailgun")
}

func TestDecider_MailgunConfiguredButBreakerOpen_Pauses(t *testing.T) {
	b := mail.NewBreaker(log.New("dec-test"), mail.WithBreakerThreshold(1))
	b.RecordFailure(fmt.Errorf("boom")) // open it
	dec := mail.NewPauseDecider(usableMailgun(), b, nil, true)
	require.True(t, dec.ShouldPause(context.Background()),
		"a configured-but-open Mailgun holds even though SMTP exists (SMTP is not a Mailgun fallback)")
}

func TestDecider_OverUsageCeiling_Pauses(t *testing.T) {
	b := mail.NewBreaker(log.New("dec-test")) // closed/healthy
	reader := &fakeUsageReader{u: mail.Usage{Sent: 1000, Limit: 1000}}
	g := mail.NewUsageGuard(reader, nil, log.New("dec-test"), mail.WithUsageHardCeiling(900))
	g.Refresh(context.Background())

	dec := mail.NewPauseDecider(usableMailgun(), b, g, true)
	require.True(t, dec.ShouldPause(context.Background()), "over the hard ceiling holds even with a healthy breaker")
}
