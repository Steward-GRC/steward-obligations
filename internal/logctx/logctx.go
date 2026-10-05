// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

// Package logctx gives code that holds no logger the request's zerolog
// logger, with its trace ids.
package logctx

import (
	"context"

	log "github.com/Bugs5382/go-log"
	"github.com/rs/zerolog"
)

// From returns the service logger, correlated with the trace in ctx.
//
// go-log deprecates its package-level Ctx because a process with several
// base loggers gets the last one. This service creates exactly one (main's
// log.New), so it is the right logger here.
func From(ctx context.Context) zerolog.Logger {
	return log.Ctx(ctx) //nolint:staticcheck // one base logger per process, see above
}
