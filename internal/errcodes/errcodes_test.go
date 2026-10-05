// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package errcodes_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Bugs5382/go-apperr/apperrgrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
)

func TestStoreUnavailableRoundTrip(t *testing.T) {
	st := status.Convert(errcodes.Error(context.Background(),
		errcodes.StoreUnavailable("record_ack", errors.New("dial tcp 192.0.2.10:5432: connect: connection refused"))))
	require.Equal(t, codes.Internal, st.Code())
	require.Equal(t, "Code 7001: Internal Error", st.Message(), "a store cause never reaches the wire")

	info, ok := apperrgrpc.FromStatus(st)
	require.True(t, ok, "status must carry ErrorInfo")
	require.Equal(t, "NOTIFY_STORE_UNAVAILABLE", info.Symbol)
	require.Equal(t, 7001, info.Code)
	require.Equal(t, "obligations", info.Domain)
	require.Equal(t, "record_ack", info.Metadata["op"])
}

func TestUserSafeCodesKeepTheirMessage(t *testing.T) {
	cases := []struct {
		err  error
		code codes.Code
		num  int
		meta map[string]string
	}{
		{errcodes.AckAuthRequired(), codes.Unauthenticated, 7003, nil},
		{errcodes.PrefMandatoryOff("compliance", nil), codes.InvalidArgument, 7004, map[string]string{"category": "compliance"}},
		{errcodes.PrefInvalid("daily_hour", nil), codes.InvalidArgument, 7005, map[string]string{"field": "daily_hour"}},
	}
	for _, c := range cases {
		st := status.Convert(errcodes.Error(context.Background(), c.err))
		require.Equal(t, c.code, st.Code())
		entry, ok := errcodes.Registry().Describe(c.num)
		require.True(t, ok)
		require.True(t, entry.UserSafe)
		require.Equal(t, entry.Message, st.Message())
		info, _ := apperrgrpc.FromStatus(st)
		require.Equal(t, c.num, info.Code)
		for k, v := range c.meta {
			require.Equal(t, v, info.Metadata[k])
		}
	}
}

func TestServiceFaultsAreNotUserSafe(t *testing.T) {
	for _, err := range []error{
		errcodes.WelcomeSendUnavailable("u-1", errors.New("smtp down")),
		errcodes.AckTransferActorRequired(),
	} {
		st := status.Convert(errcodes.Error(context.Background(), err))
		info, _ := apperrgrpc.FromStatus(st)
		require.Regexp(t, `^Code 700[26]: Internal Error$`, st.Message())
		require.NotContains(t, st.Message(), "smtp")
		require.Contains(t, []int{7002, 7006}, info.Code)
	}
}

func TestWelcomeSendCarriesTheUser(t *testing.T) {
	info, _ := apperrgrpc.FromError(errcodes.Error(context.Background(), errcodes.WelcomeSendUnavailable("u-1", errors.New("smtp down"))))
	require.Equal(t, "WELCOME_SEND_UNAVAILABLE", info.Symbol)
	require.Equal(t, "u-1", info.Metadata["user_id"])
}

func TestUncodedErrorsFallBackToInternal(t *testing.T) {
	info, _ := apperrgrpc.FromError(errcodes.Error(context.Background(), errors.New("boom")))
	require.Equal(t, errcodes.CodeInternal, info.Code)
	require.Equal(t, "INTERNAL", info.Symbol)
}

func TestRegistryBandAndDomain(t *testing.T) {
	require.NotEmpty(t, errcodes.Entries())
	for _, e := range errcodes.Entries() {
		require.Equal(t, 7, e.Code/1000, "code %d must be in band 7", e.Code)
		_, ok := errcodes.Registry().Describe(e.Code)
		require.True(t, ok)
	}
}

// docs/error-codes.md is generated from the registry; refresh it with
// UPDATE_DOCS=1 go test ./internal/errcodes.
func TestErrorCodesDocIsCurrent(t *testing.T) {
	const path = "../../docs/error-codes.md"
	want := errcodes.Doc()
	if os.Getenv("UPDATE_DOCS") == "1" {
		require.NoError(t, os.WriteFile(path, []byte(want), 0o600))
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, string(got), "docs/error-codes.md is stale; run UPDATE_DOCS=1 go test ./internal/errcodes")
}
