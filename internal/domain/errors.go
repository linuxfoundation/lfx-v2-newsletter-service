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

	// ErrEmailNotDispatched marks a per-recipient email dispatch failure where
	// the email is known NOT to have gone out: a documented pre-acceptance
	// rejection reply from email-service, or a NATS no-responders condition
	// (the request was dropped before any service saw it). Only failures carrying
	// this sentinel are safe to retry — ambiguous transport failures (request
	// timeout, cancelled context) may have been accepted upstream, and
	// email-service has no idempotency key, so retrying them risks sending a
	// recipient the same newsletter twice. The send orchestrator matches this
	// sentinel with errors.Is to gate its bounded per-recipient retry.
	ErrEmailNotDispatched = errors.New("email was not dispatched")
)
