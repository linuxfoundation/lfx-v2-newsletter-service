// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package domain

import "errors"

var (
	// ErrNewsletterNotFound indicates the requested newsletter was not found
	ErrNewsletterNotFound = errors.New("newsletter not found")
	// ErrInvalidTag indicates an invalid tag was provided
	ErrInvalidTag = errors.New("invalid tag")
	// ErrRateLimitExceeded indicates the API rate limit was exceeded
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
	// ErrUnauthorized indicates unauthorized access
	ErrUnauthorized = errors.New("unauthorized")
)
