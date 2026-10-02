// Copyright 2026 The Sneakers-PAM Authors
// SPDX-License-Identifier: Apache-2.0

// Package safeconv provides audited, overflow-safe narrowing of wide integers
// (int/int64/uint64) into the fixed-width protobuf scalar types (int32/uint32).
// Counts, indexes and version numbers are computed as int but must cross the
// wire as int32; a raw int32(...) at the call site trips the gosec G115
// (CWE-190) gate. Centralizing the one clamped, //nosec-justified conversion
// here keeps every service consistent and avoids N hand-rolled copies that each
// risk the gate.
package safeconv

import "math"

// Int32 clamps n into the int32 range: negatives to 0, oversized to MaxInt32.
func Int32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n) // #nosec G115 -- bounds-checked immediately above
}
