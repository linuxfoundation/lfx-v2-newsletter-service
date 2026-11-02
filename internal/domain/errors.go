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

	// ErrScheduled indicates a newsletter has an armed schedule (status
	// scheduled) and cannot be edited, deleted, or sent/scheduled again until
	// it is cancelled back to draft or settles to sent.
	ErrScheduled = errors.New("newsletter is scheduled")

	// ErrCancelWindowClosed indicates a cancel-schedule request arrived too
	// close to the newsletter's scheduled_at — inside the configured buffer
	// where the provider's cancel is no longer reliable.
	ErrCancelWindowClosed = errors.New("cancel window closed")
)

// ErrAudienceNotReady indicates a newsletter has no usable audience snapshot.
// The snapshot is missing, still being resolved, failed, or was built from a
// different committee list than the newsletter has now. Callers get the
// details through *AudienceNotReadyError.
var ErrAudienceNotReady = errors.New("audience not ready")

// Audience states reported by AudienceNotReadyError.Status in addition to the
// persisted 'processing' and 'failed' audience statuses.
const (
	// AudienceStateNone means no audience has been resolved for the newsletter.
	AudienceStateNone = "none"
	// AudienceStateStale means a ready audience exists but was built from a
	// different committee list than the newsletter has now.
	AudienceStateStale = "stale"
)

// AudienceNotReadyError is the ErrAudienceNotReady error with the audience
// state, so the handler can tell the client what to do.
type AudienceNotReadyError struct {
	// Status is AudienceStateNone, AudienceStateStale, "processing" or "failed".
	Status string
}

func (e *AudienceNotReadyError) Error() string {
	return "audience not ready: " + e.Status
}

// Is makes errors.Is(err, ErrAudienceNotReady) true.
func (e *AudienceNotReadyError) Is(target error) bool { return target == ErrAudienceNotReady }
