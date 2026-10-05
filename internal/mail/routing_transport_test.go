// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"errors"
	"testing"

	email "github.com/Bugs5382/go-email"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/emailservice"
	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// fakeProvider is a stand-in for emailservice.Provider: it returns a canned
// config/ok/err and records how many times Get was called. cfg is read on each
// Get so a test can mutate it between sends to simulate an admin toggling the
// Mailgun settings mid-flight.
type fakeProvider struct {
	cfg   emailservice.MailgunConfig
	ok    bool
	err   error
	calls int
}

func (f *fakeProvider) Get(context.Context) (emailservice.MailgunConfig, bool, error) {
	f.calls++
	return f.cfg, f.ok, f.err
}

// countingTransport is a fake email.Transport that records the messages it was
// asked to send and can be told to fail.
type countingTransport struct {
	name  string
	sends int
	last  email.Message
	err   error
}

func (c *countingTransport) Send(_ context.Context, m email.Message) error {
	c.sends++
	c.last = m
	return c.err
}

func testMessage() email.Message {
	return email.Message{
		From:    "no-reply@example.org",
		To:      []string{"user@example.com"},
		Subject: "Hi",
		Text:    "body",
	}
}

func TestRoutingTransport_RoutesToMailgunWhenConfigured(t *testing.T) {
	smtpT := &countingTransport{name: "smtp"}
	mgT := &countingTransport{name: "mailgun"}

	builds := 0
	prov := &fakeProvider{
		cfg: emailservice.MailgunConfig{APIKey: "key-1", Domain: "mg.example.org", Region: "us", FromAddress: "no-reply@example.org"},
		ok:  true,
	}
	rt := mail.NewRoutingTransport(prov, smtpT, mail.WithMailgunFactory(func(emailservice.MailgunConfig) email.Transport {
		builds++
		return mgT
	}))

	require.NoError(t, rt.Send(context.Background(), testMessage()))

	require.Equal(t, 1, mgT.sends, "should route to Mailgun when configured")
	require.Equal(t, 0, smtpT.sends, "SMTP must not be used when Mailgun is configured")
	require.Equal(t, 1, builds, "Mailgun transport built once")
}

func TestRoutingTransport_FallsBackToSMTPWhenNotConfigured(t *testing.T) {
	smtpT := &countingTransport{name: "smtp"}
	prov := &fakeProvider{ok: false} // not configured / disabled

	built := false
	rt := mail.NewRoutingTransport(prov, smtpT, mail.WithMailgunFactory(func(emailservice.MailgunConfig) email.Transport {
		built = true
		return &countingTransport{name: "mailgun"}
	}))

	require.NoError(t, rt.Send(context.Background(), testMessage()))

	require.Equal(t, 1, smtpT.sends, "should fall back to SMTP when Mailgun is not configured")
	require.False(t, built, "must not build a Mailgun transport when not configured")
}

func TestRoutingTransport_FallsBackToSMTPOnProviderError(t *testing.T) {
	smtpT := &countingTransport{name: "smtp"}
	prov := &fakeProvider{ok: true, err: errors.New("core unavailable")}

	rt := mail.NewRoutingTransport(prov, smtpT, mail.WithMailgunFactory(func(emailservice.MailgunConfig) email.Transport {
		t.Fatal("must not build Mailgun transport on provider error")
		return nil
	}))

	// Degrade to SMTP rather than hard-fail: the real pause logic is Slice B.
	require.NoError(t, rt.Send(context.Background(), testMessage()))
	require.Equal(t, 1, smtpT.sends, "provider error must degrade to SMTP")
}

func TestRoutingTransport_ReusesMailgunForStableConfig(t *testing.T) {
	smtpT := &countingTransport{name: "smtp"}
	mgT := &countingTransport{name: "mailgun"}

	builds := 0
	prov := &fakeProvider{
		cfg: emailservice.MailgunConfig{APIKey: "key-1", Domain: "mg.example.org", Region: "us", FromAddress: "from@example.org"},
		ok:  true,
	}
	rt := mail.NewRoutingTransport(prov, smtpT, mail.WithMailgunFactory(func(emailservice.MailgunConfig) email.Transport {
		builds++
		return mgT
	}))

	for range 3 {
		require.NoError(t, rt.Send(context.Background(), testMessage()))
	}

	require.Equal(t, 3, mgT.sends, "all sends routed to Mailgun")
	require.Equal(t, 1, builds, "stable config reuses the cached Mailgun transport — no rebuild per send")
	require.Equal(t, 3, prov.calls, "config is resolved per-send (dynamic), not once at boot")
}

func TestRoutingTransport_RebuildsMailgunWhenConfigChanges(t *testing.T) {
	smtpT := &countingTransport{name: "smtp"}
	mgT := &countingTransport{name: "mailgun"}

	builds := 0
	prov := &fakeProvider{
		cfg: emailservice.MailgunConfig{APIKey: "key-1", Domain: "mg.example.org", Region: "us", FromAddress: "from@example.org"},
		ok:  true,
	}
	rt := mail.NewRoutingTransport(prov, smtpT, mail.WithMailgunFactory(func(emailservice.MailgunConfig) email.Transport {
		builds++
		return mgT
	}))

	require.NoError(t, rt.Send(context.Background(), testMessage()))
	require.Equal(t, 1, builds)

	// Admin rotates the API key via the settings UI; within the cache window the
	// provider now returns a different config → the transport must be rebuilt.
	prov.cfg.APIKey = "key-2"
	require.NoError(t, rt.Send(context.Background(), testMessage()))
	require.Equal(t, 2, builds, "changed config (fingerprint) rebuilds the Mailgun transport")

	// Changing only the domain also rebuilds.
	prov.cfg.Domain = "mg2.example.org"
	require.NoError(t, rt.Send(context.Background(), testMessage()))
	require.Equal(t, 3, builds, "changed domain rebuilds the Mailgun transport")
}

func TestRoutingTransport_TogglesBackToSMTP(t *testing.T) {
	smtpT := &countingTransport{name: "smtp"}
	mgT := &countingTransport{name: "mailgun"}

	prov := &fakeProvider{
		cfg: emailservice.MailgunConfig{APIKey: "key-1", Domain: "mg.example.org", Region: "us"},
		ok:  true,
	}
	rt := mail.NewRoutingTransport(prov, smtpT, mail.WithMailgunFactory(func(emailservice.MailgunConfig) email.Transport {
		return mgT
	}))

	require.NoError(t, rt.Send(context.Background(), testMessage()))
	require.Equal(t, 1, mgT.sends)

	// Admin disables Mailgun → next send (dynamic) goes to SMTP without a restart.
	prov.ok = false
	require.NoError(t, rt.Send(context.Background(), testMessage()))
	require.Equal(t, 1, smtpT.sends, "disabling Mailgun routes the next send to SMTP")
	require.Equal(t, 1, mgT.sends, "no further Mailgun sends after disable")
}

func TestRoutingTransport_PropagatesTransportError(t *testing.T) {
	smtpT := &countingTransport{name: "smtp", err: errors.New("smtp boom")}
	prov := &fakeProvider{ok: false}
	rt := mail.NewRoutingTransport(prov, smtpT)

	err := rt.Send(context.Background(), testMessage())
	require.Error(t, err, "a real send error from the chosen transport must propagate")
}

func TestRoutingTransport_SatisfiesTransport(t *testing.T) {
	var _ email.Transport = (*mail.RoutingTransport)(nil)
}
