// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package domain

import "errors"

// Domain-level sentinel errors. Repository, service, and handler layers wrap and
// translate these via errors.Is to keep transport-specific knowledge out of the
// domain layer.
var (
	// ErrNotFound indicates a record with the requested identifier does not exist.
	ErrNotFound = errors.New("not found")

	// ErrVersionMismatch indicates an optimistic-locking conflict: the caller's
	// expected version does not match the row's current version.
	ErrVersionMismatch = errors.New("version mismatch")

	// ErrInvalidRequest indicates the caller's input failed validation.
	ErrInvalidRequest = errors.New("invalid request")

	// ErrAlreadySent indicates a draft has already been sent and cannot be re-sent
	// or modified.
	ErrAlreadySent = errors.New("newsletter already sent")

	// ErrSendInProgress indicates a send has been accepted for this newsletter
	// and its fan-out is still running. The newsletter cannot be re-sent,
	// edited, or deleted until the send settles (sent, or reverted to draft on
	// total failure).
	ErrSendInProgress = errors.New("newsletter send in progress")

	// ErrForbidden indicates the caller does not have permission to access
	// the requested resource (e.g., not a member of any committee the newsletter
	// was sent to).
	ErrForbidden = errors.New("forbidden")
)
