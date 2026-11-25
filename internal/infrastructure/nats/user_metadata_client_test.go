// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"errors"
	"testing"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

func TestUserEmailsRejectionSeparatesUnknownUsersFromFailures(t *testing.T) {
	cases := []struct {
		reply        string
		wantNotFound bool
	}{
		{"user not found", true},
		{"", true},
		{"auth_service_unavailable", false},
		{"failed to fetch user", false},
	}
	for _, tc := range cases {
		err := userEmailsRejection(tc.reply)
		var notFound pkgerrors.NotFound
		var unavailable pkgerrors.ServiceUnavailable
		if errors.As(err, &notFound) != tc.wantNotFound || errors.As(err, &unavailable) == tc.wantNotFound {
			t.Fatalf("userEmailsRejection(%q) = %T, wantNotFound %v", tc.reply, err, tc.wantNotFound)
		}
	}
}
