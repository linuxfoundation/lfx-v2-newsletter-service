// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"errors"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// TestRequestErrIsAmbiguous verifies which failed email-service send requests
// are treated as ambiguous (the message may have been delivered) versus
// definitive (nothing was delivered, so a retry is safe). Client.Request wraps
// the raw error in a ServiceUnavailable, so the classification must see through
// that wrapper.
func TestRequestErrIsAmbiguous(t *testing.T) {
	// wrap mirrors Client.Request, which wraps the underlying error.
	wrap := func(err error) error {
		return pkgerrors.NewServiceUnavailable("NATS request failed", err)
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"timeout is ambiguous", natsgo.ErrTimeout, true},
		{"deadline exceeded is ambiguous", context.DeadlineExceeded, true},
		{"cancelled is ambiguous", context.Canceled, true},
		{"no responders is definitive", natsgo.ErrNoResponders, false},
		{"other error is definitive", errors.New("boom"), false},
		{"wrapped timeout is ambiguous", wrap(context.DeadlineExceeded), true},
		{"wrapped no responders is definitive", wrap(natsgo.ErrNoResponders), false},
	}
	for _, tc := range cases {
		if got := requestErrIsAmbiguous(tc.err); got != tc.want {
			t.Errorf("%s: requestErrIsAmbiguous = %v, want %v", tc.name, got, tc.want)
		}
	}
}
