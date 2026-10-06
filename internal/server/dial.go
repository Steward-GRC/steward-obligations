// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	gootel "github.com/Bugs5382/go-otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Steward-GRC/steward-obligations/internal/workloadauth"
)

// DialOptions are the options for the outbound connections to core and
// identity. Each call carries obligations' projected service-account token,
// read from tokenFile on every call. Obligations asks both as itself, so no
// end-user actor is put on the call. An empty tokenFile is
// WORKLOAD_AUTH=disabled and sends no token. A token file that can't be read
// now fails, so a missing mount stops the boot instead of every call.
func DialOptions(tokenFile string) ([]grpc.DialOption, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(gootel.GRPCClientStatsHandler()),
	}
	if tokenFile == "" {
		return opts, nil
	}
	token, _, err := workloadauth.DialOptionFromEnv(func(k string) string {
		if k == workloadauth.EnvTokenFile {
			return tokenFile
		}
		return ""
	})
	if err != nil {
		return nil, err
	}
	return append(opts, token), nil
}
