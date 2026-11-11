// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"math"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestampOrNil is the protobuf timestamp for t, or nil for the zero time so
// an unset date stays unset on the wire.
func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// toInt32 converts a non-negative domain int (audience sizes, ack/view counts —
// bounded far below int32 max by construction) to the int32 used by the
// protobuf API, clamping defensively so a corrupt value can never wrap. Keeps
// gosec G115 (integer-overflow) satisfied in one audited place.
func toInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n) //nosec G115 -- bounded by the guards above
	}
}
