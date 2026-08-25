// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	emailapi "github.com/linuxfoundation/lfx-v2-email-service/pkg/api"
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

// TestSendEmailWire_MatchesUpstreamContract guards the shared fields against
// silent drift from the pinned emailapi.SendEmailRequest. sendEmailWire is
// hand-copied rather than a type alias (see its doc comment), so a pin bump
// that renames or drops a field (e.g. reply_to, group_id) would otherwise
// compile clean and ship with that field silently missing from every send —
// email-service ignores unknown fields and there is no error path to surface
// the mismatch. This test marshals both shapes from the same input and diffs
// the JSON so that drift fails loudly instead of shipping quietly.
func TestSendEmailWire_MatchesUpstreamContract(t *testing.T) {
	in := port.SendEmailInput{
		To:              "a@example.com",
		Subject:         "s",
		HTML:            "h",
		Text:            "t",
		From:            "f@example.com",
		FromDisplayName: "F",
		ReplyTo:         "r@example.com",
		GroupID:         "g",
	}
	want, err := json.Marshal(emailapi.SendEmailRequest{
		To:              in.To,
		Subject:         in.Subject,
		HTML:            in.HTML,
		Text:            in.Text,
		From:            in.From,
		FromDisplayName: in.FromDisplayName,
		ReplyTo:         in.ReplyTo,
		GroupID:         in.GroupID,
	})
	if err != nil {
		t.Fatalf("marshal upstream contract: %v", err)
	}
	// newSendEmailWire also carries the unsubscribe fields, which are unset
	// here and therefore omitempty-dropped, so the shared-field JSON matches.
	got, err := json.Marshal(newSendEmailWire(in))
	if err != nil {
		t.Fatalf("marshal local wire: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("sendEmailWire drifted from emailapi.SendEmailRequest for the shared fields:\n got  %s\n want %s", got, want)
	}
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
