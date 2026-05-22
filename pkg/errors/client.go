// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package errors

import "errors"

// Validation wraps client-supplied input that fails validation; surfaces as 400.
type Validation struct {
	base
}

// NewValidation constructs a Validation error with the given message and
// optional wrapped errors.
func NewValidation(message string, err ...error) Validation {
	return Validation{base: base{message: message, err: errors.Join(err...)}}
}

// NotFound wraps a missing resource lookup; surfaces as 404.
type NotFound struct {
	base
}

// NewNotFound constructs a NotFound error with the given message and optional
// wrapped errors.
func NewNotFound(message string, err ...error) NotFound {
	return NotFound{base: base{message: message, err: errors.Join(err...)}}
}

// Conflict wraps a state conflict (e.g. duplicate, version mismatch); surfaces as 409.
type Conflict struct {
	base
}

// NewConflict constructs a Conflict error with the given message and optional
// wrapped errors.
func NewConflict(message string, err ...error) Conflict {
	return Conflict{base: base{message: message, err: errors.Join(err...)}}
}
