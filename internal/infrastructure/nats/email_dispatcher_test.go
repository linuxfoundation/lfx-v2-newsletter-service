// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// TestNewSendEmailWire_UnsubscribeFields pins the NATS wire contract for the
// unsubscribe intent: the send input's list_unsubscribe fields must reach
// email-service with the right JSON keys and values, and omitempty must drop
// them when unset. A typo in a tag or a missed assignment would silently strip
// the unsubscribe headers on the default provider, so this is asserted directly
// on the conversion rather than only through a fake dispatcher.
func TestNewSendEmailWire_UnsubscribeFields(t *testing.T) {
	t.Run("populated fields serialize with the expected keys and values", func(t *testing.T) {
		wire := newSendEmailWire(port.SendEmailInput{
			To:                  "a@example.com",
			Subject:             "Hi",
			ListUnsubscribeURL:  "https://host/newsletters/unsubscribe?t=abc",
			ListUnsubscribePost: true,
		})
		if wire.ListUnsubscribeURL != "https://host/newsletters/unsubscribe?t=abc" || !wire.ListUnsubscribePost {
			t.Fatalf("input fields not carried onto the wire: %+v", wire)
		}
		data, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got := string(data)
		for _, want := range []string{`"list_unsubscribe_url":"https://host/newsletters/unsubscribe?t=abc"`, `"list_unsubscribe_post":true`} {
			if !strings.Contains(got, want) {
				t.Errorf("wire JSON missing %s:\n%s", want, got)
			}
		}
	})

	t.Run("unset fields are omitted", func(t *testing.T) {
		data, err := json.Marshal(newSendEmailWire(port.SendEmailInput{To: "a@example.com", Subject: "Hi"}))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got := string(data)
		if strings.Contains(got, "list_unsubscribe_url") || strings.Contains(got, "list_unsubscribe_post") {
			t.Errorf("expected unsubscribe fields omitted when unset:\n%s", got)
		}
	})
}

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
