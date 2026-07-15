// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// TestClassifySendRequestError pins the retry-safety boundary at the transport
// layer: only NATS no-responders — proof the request was dropped before any
// service saw it — is tagged domain.ErrEmailNotDispatched. Ambiguous failures
// (timeout, cancelled context, generic connection errors) pass through
// untagged so the send orchestrator does not retry a request email-service may
// already have accepted.
func TestClassifySendRequestError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{
			name:      "no responders is tagged retry-safe",
			err:       pkgerrors.NewServiceUnavailable("NATS request failed", nats.ErrNoResponders),
			wantRetry: true,
		},
		{
			name:      "request timeout stays ambiguous",
			err:       pkgerrors.NewServiceUnavailable("NATS request failed", context.DeadlineExceeded),
			wantRetry: false,
		},
		{
			name:      "cancelled context stays ambiguous",
			err:       pkgerrors.NewServiceUnavailable("NATS request failed", context.Canceled),
			wantRetry: false,
		},
		{
			name:      "nats timeout stays ambiguous",
			err:       pkgerrors.NewServiceUnavailable("NATS request failed", nats.ErrTimeout),
			wantRetry: false,
		},
		{
			name:      "generic transport error stays ambiguous",
			err:       errors.New("connection reset"),
			wantRetry: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySendRequestError(tc.err)
			if errors.Is(got, domain.ErrEmailNotDispatched) != tc.wantRetry {
				t.Fatalf("classifySendRequestError(%v) retry-safe=%v, want %v", tc.err, !tc.wantRetry, tc.wantRetry)
			}
			// The original error must survive classification in the chain so
			// the HTTP error mapper still sees the typed ServiceUnavailable
			// and callers can match the underlying cause.
			if !errors.Is(got, tc.err) {
				t.Fatalf("classification dropped the original error from the chain: %v", got)
			}
		})
	}
}
