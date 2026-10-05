// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package mail_test

import (
	"context"
	"errors"
	"testing"

	log "github.com/Bugs5382/go-log"
	"github.com/stretchr/testify/require"

	"github.com/Steward-GRC/steward-obligations/internal/mail"
)

// fakeUsageReader returns a settable usage/err.
type fakeUsageReader struct {
	u   mail.Usage
	err error
}

func (f *fakeUsageReader) Read(context.Context) (mail.Usage, error) { return f.u, f.err }

func TestUsageGuard_HardCeilingPausesWhenExceeded(t *testing.T) {
	r := &fakeUsageReader{u: mail.Usage{Sent: 100, Limit: 1000}}
	g := mail.NewUsageGuard(r, nil, log.New("usage-test"),
		mail.WithUsageHardCeiling(500))

	g.Refresh(context.Background())
	require.False(t, g.OverCeiling(), "under the ceiling → sends flow")

	r.u = mail.Usage{Sent: 500, Limit: 1000}
	g.Refresh(context.Background())
	require.True(t, g.OverCeiling(), "at/over the ceiling → pause new sends")

	r.u = mail.Usage{Sent: 400, Limit: 1000}
	g.Refresh(context.Background())
	require.False(t, g.OverCeiling(), "back under the ceiling → resume")
}

func TestUsageGuard_NoCeilingNeverPauses(t *testing.T) {
	// Default: soft-alert only, no hard ceiling configured.
	r := &fakeUsageReader{u: mail.Usage{Sent: 999999, Limit: 1000}}
	g := mail.NewUsageGuard(r, nil, log.New("usage-test"))
	g.Refresh(context.Background())
	require.False(t, g.OverCeiling(), "with no hard ceiling, over-limit alerts but never hard-pauses")
}

func TestUsageGuard_ReadErrorFailsOpen(t *testing.T) {
	r := &fakeUsageReader{u: mail.Usage{Sent: 900, Limit: 1000}}
	g := mail.NewUsageGuard(r, nil, log.New("usage-test"), mail.WithUsageHardCeiling(800))
	g.Refresh(context.Background())
	require.True(t, g.OverCeiling())

	// A subsequent read failure must NOT change the flag (fail-open): mail
	// keeps flowing based on the last-known-good state rather than pausing on a
	// metrics hiccup.
	r.err = errors.New("mailgun usage unreachable")
	g.Refresh(context.Background())
	require.True(t, g.OverCeiling(), "a read error leaves the guard state unchanged")
}

func TestUsageGuard_SoftThresholdDoesNotPause(t *testing.T) {
	// Crossing the soft threshold alerts but never pauses on its own.
	r := &fakeUsageReader{u: mail.Usage{Sent: 850, Limit: 1000}}
	g := mail.NewUsageGuard(r, nil, log.New("usage-test"),
		mail.WithUsageSoftThreshold(0.8)) // no hard ceiling
	g.Refresh(context.Background())
	require.False(t, g.OverCeiling())
	require.Equal(t, int64(850), g.LastUsage().Sent)
}
