// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package errors defines typed error wrappers used across the newsletter
// service and shared with related LFX v2 services. The package mirrors the
// canonical pattern in lfx-v2-committee-service/pkg/errors.
package errors

import "fmt"

// base is the common state for typed error wrappers. Embedded by each
// exported error type to inherit the Error() and Unwrap() methods.
type base struct {
	message string
	err     error
}

// Error renders the wrapped error. Promoted to each exported error type via
// struct embedding.
func (b base) Error() string {
	if b.err == nil {
		return b.message
	}
	return fmt.Sprintf("%s: %v", b.message, b.err)
}

// Unwrap exposes the wrapped error for use with errors.Is / errors.As.
func (b base) Unwrap() error { return b.err }
