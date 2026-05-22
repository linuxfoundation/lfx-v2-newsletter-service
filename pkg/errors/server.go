// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package errors

import "errors"

// Unexpected wraps unrecoverable internal failures that should surface as 5xx.
type Unexpected struct {
	base
}

// NewUnexpected constructs an Unexpected error with the given message and
// optional wrapped errors.
func NewUnexpected(message string, err ...error) Unexpected {
	return Unexpected{base: base{message: message, err: errors.Join(err...)}}
}

// ServiceUnavailable wraps transient upstream / dependency failures that
// should surface as 503.
type ServiceUnavailable struct {
	base
}

// NewServiceUnavailable constructs a ServiceUnavailable error with the given
// message and optional wrapped errors.
func NewServiceUnavailable(message string, err ...error) ServiceUnavailable {
	return ServiceUnavailable{base: base{message: message, err: errors.Join(err...)}}
}
