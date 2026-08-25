// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

// sendEmailWireKnownExtraJSONTags are the json tags sendEmailWire is allowed
// to carry beyond emailapi.SendEmailRequest: the RFC 8058 unsubscribe fields
// the pinned email-service version does not yet expose (see sendEmailWire's
// doc comment). Update this alongside sendEmailWire when it gains a field
// that is deliberately ahead of the pin.
var sendEmailWireKnownExtraJSONTags = map[string]bool{
	"list_unsubscribe_url":  true,
	"list_unsubscribe_post": true,
}

// TestSendEmailWire_MatchesUpstreamContract guards sendEmailWire against
// silent drift from the pinned emailapi.SendEmailRequest. sendEmailWire is
// hand-copied rather than a type alias (see its doc comment), so a pin bump
// that adds, drops, or renames a field would otherwise compile clean and
// ship with that field silently missing (or never collapsed back) — email-
// service ignores unknown fields and there is no error path to surface the
// mismatch. This compares the two structs' json tag sets by reflection
// rather than diffing marshalled JSON, so it catches an upstream addition
// (the case a byte-diff of only-set fields would miss) as well as a drop or
// rename, and does not false-positive on an upstream field reorder.
func TestSendEmailWire_MatchesUpstreamContract(t *testing.T) {
	upstreamTags := jsonTagSet(reflect.TypeOf(emailapi.SendEmailRequest{}))
	localTags := jsonTagSet(reflect.TypeOf(sendEmailWire{}))

	for tag := range upstreamTags {
		if !localTags[tag] {
			t.Errorf("sendEmailWire is missing upstream field %q — emailapi.SendEmailRequest gained a field that isn't mirrored locally", tag)
		}
	}
	for tag := range localTags {
		if upstreamTags[tag] {
			continue
		}
		if !sendEmailWireKnownExtraJSONTags[tag] {
			t.Errorf("sendEmailWire has field %q that is neither in emailapi.SendEmailRequest nor sendEmailWireKnownExtraJSONTags — if the pin gained this field, collapse sendEmailWire back onto emailapi.SendEmailRequest per its doc comment; otherwise add it to sendEmailWireKnownExtraJSONTags", tag)
		}
	}
}

// jsonTagSet returns the set of json tag names (the part before any comma,
// e.g. "omitempty") declared on t's exported fields. Fields tagged `json:"-"`
// or untagged are excluded.
func jsonTagSet(t reflect.Type) map[string]bool {
	tags := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		tags[name] = true
	}
	return tags
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
