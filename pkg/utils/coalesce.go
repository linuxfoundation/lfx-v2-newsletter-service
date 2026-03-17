// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package utils

// Coalesce returns the first non-empty value from the given arguments.
func Coalesce[T ~string](values ...T) T {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// CastSlice converts a slice of one ~string type to another.
func CastSlice[To ~string, From ~string](ss []From) []To {
	if ss == nil {
		return nil
	}
	out := make([]To, len(ss))
	for i, s := range ss {
		out[i] = To(s)
	}
	return out
}
