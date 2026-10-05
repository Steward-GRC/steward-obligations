// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"context"

	"github.com/Steward-GRC/steward-obligations/internal/errcodes"
)

// storeUnavailable codes a failed store call as NOTIFY_STORE_UNAVAILABLE (7001)
// with op naming it. The cause goes to the debug log, never on the wire.
func storeUnavailable(ctx context.Context, op string, cause error) error {
	return errcodes.Error(ctx, errcodes.StoreUnavailable(op, cause))
}
