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

// TestClassifyErrorReply pins the retry-safety boundary for explicit
// email-service error replies: every error string the email-service contract
// documents as a pre-acceptance rejection is tagged retry-safe — the literal
// rows double as a typo guard on the sendRejectedBeforeAcceptance allowlist.
// The post-acceptance "email delivery failed" (SMTP failure after the service
// accepted the request — the remote MTA may already hold the message) and any
// unrecognized string stay ambiguous and untagged.
func TestClassifyErrorReply(t *testing.T) {
	cases := []struct {
		name      string
		reply     string
		wantRetry bool
	}{
		// All six contract-documented pre-acceptance rejections
		// (lfx-v2-email-service/docs/email-service-contract.md § send_email
		// error reply), spelled as literals so an allowlist typo fails here.
		{"pre-acceptance malformed payload rejection", "invalid request payload", true},
		{"pre-acceptance required-fields rejection", "to, subject, html, and text are required", true},
		{"pre-acceptance from-address rejection", "invalid from address", true},
		{"pre-acceptance from-domain rejection", "from address domain not allowed", true},
		{"pre-acceptance reply_to-address rejection", "invalid reply_to address", true},
		{"pre-acceptance reply_to-domain rejection", "reply_to address domain not allowed", true},
		{"post-acceptance delivery failure stays ambiguous", "email delivery failed", false},
		{"unrecognized error string stays ambiguous", "smtp connection lost", false},
	}
	// Completeness guard: every allowlist entry must have a positive row
	// above, so growing sendRejectedBeforeAcceptance without pinning the new
	// string here fails the test.
	positive := 0
	for _, tc := range cases {
		if tc.wantRetry {
			positive++
			if _, ok := sendRejectedBeforeAcceptance[tc.reply]; !ok {
				t.Fatalf("positive case %q is not in sendRejectedBeforeAcceptance — allowlist typo?", tc.reply)
			}
		}
	}
	if positive != len(sendRejectedBeforeAcceptance) {
		t.Fatalf("test covers %d pre-acceptance strings, allowlist has %d — add a row for every allowlist entry", positive, len(sendRejectedBeforeAcceptance))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyErrorReply(tc.reply)
			if errors.Is(got, domain.ErrEmailNotDispatched) != tc.wantRetry {
				t.Fatalf("classifyErrorReply(%q) retry-safe=%v, want %v", tc.reply, !tc.wantRetry, tc.wantRetry)
			}
			// Every reply keeps the typed ServiceUnavailable so the HTTP
			// mapper (test-send path) still resolves 503.
			var su pkgerrors.ServiceUnavailable
			if !errors.As(got, &su) {
				t.Fatalf("classifyErrorReply(%q) lost the typed ServiceUnavailable: %v", tc.reply, got)
			}
		})
	}
}
