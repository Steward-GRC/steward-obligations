// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	grpcactor "github.com/Bugs5382/go-grpc-actor"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	obligationsv1 "github.com/Steward-GRC/steward-obligations/gen/go/steward/obligations/v1"
)

func tokenFile(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(p, []byte(tok), 0o600))
	return p
}

// The token rides every outbound call to core and identity, re-read each
// time. Obligations calls them as itself, so no end-user actor goes along.
func TestDialOptionsSendTheTokenAndNoActor(t *testing.T) {
	got := make(chan metadata.MD, 2)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, _ any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		got <- md
		return &obligationsv1.GetMyObligationsResponse{}, nil
	}))
	obligationsv1.RegisterObligationServiceServer(s, obligationsv1.UnimplementedObligationServiceServer{})
	go func() { _ = s.Serve(lis) }()
	defer s.Stop()

	file := tokenFile(t, "first")
	opts, err := DialOptions(file)
	require.NoError(t, err)
	conn, err := grpc.NewClient(lis.Addr().String(), opts...)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	client := obligationsv1.NewObligationServiceClient(conn)
	_, err = client.GetMyObligations(grpcactor.WithActor(t.Context(), grpcactor.Actor{Subject: "erin"}), &obligationsv1.GetMyObligationsRequest{})
	require.NoError(t, err)
	md := <-got
	require.Equal(t, []string{"Bearer first"}, md.Get("authorization"))
	require.Empty(t, md.Get(grpcactor.MetadataKey), "no end-user actor goes to core or identity")

	require.NoError(t, os.WriteFile(file, []byte("rotated"), 0o600))
	_, err = client.GetMyObligations(t.Context(), &obligationsv1.GetMyObligationsRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"Bearer rotated"}, (<-got).Get("authorization"), "a rotated token is picked up")
}

func TestDialOptionsWithoutATokenFileSendNoToken(t *testing.T) {
	opts, err := DialOptions("")
	require.NoError(t, err)
	require.NotEmpty(t, opts)
}

// A token file that isn't there fails closed at start-up.
func TestDialOptionsFailClosedOnAMissingTokenFile(t *testing.T) {
	_, err := DialOptions(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
