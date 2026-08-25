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
// silent drift from the pinned emailapi.SendEmailRequest, for every
// top-level tagged field (neither struct embeds another today, so promoted
// fields are out of scope — revisit this test if one ever does), since
// email-service ignores unknown fields and there is no error path to
// surface a mismatch:
//   - upstream adds a field this struct doesn't mirror
//   - upstream drops, renames, or retypes a field this struct still claims
//   - a known local-only extra (an unsubscribe field) appears upstream too,
//     which is the doc comment's "collapse this back" trigger
//   - newSendEmailWire's value mapping for a shared field is dropped or
//     crossed with another field
//
// The first three are checked structurally (by reflection, independent of
// field order); the last needs an actual conversion, so it's checked by
// marshalling a fully-populated input through both types and comparing the
// decoded values — not the raw JSON bytes, so an upstream field reorder
// (a no-op on the wire; encoding/json decodes by key, not position) can't
// fail this the way a byte-diff would.
func TestSendEmailWire_MatchesUpstreamContract(t *testing.T) {
	upstream := jsonFieldSet(reflect.TypeOf(emailapi.SendEmailRequest{}))
	local := jsonFieldSet(reflect.TypeOf(sendEmailWire{}))

	for tag, upstreamType := range upstream {
		localType, ok := local[tag]
		if !ok {
			t.Errorf("sendEmailWire is missing upstream field %q — emailapi.SendEmailRequest gained a field that isn't mirrored locally", tag)
			continue
		}
		if localType != upstreamType {
			t.Errorf("field %q type drifted: emailapi.SendEmailRequest has %s, sendEmailWire has %s", tag, upstreamType, localType)
		}
	}
	for tag := range local {
		if _, ok := upstream[tag]; ok {
			continue
		}
		if !sendEmailWireKnownExtraJSONTags[tag] {
			t.Errorf("sendEmailWire has field %q that is neither in emailapi.SendEmailRequest nor sendEmailWireKnownExtraJSONTags — if the pin gained this field, collapse sendEmailWire back onto emailapi.SendEmailRequest per its doc comment; otherwise add it to sendEmailWireKnownExtraJSONTags", tag)
		}
	}
	for tag := range sendEmailWireKnownExtraJSONTags {
		if _, ok := upstream[tag]; ok {
			t.Errorf("emailapi.SendEmailRequest now exposes %q — collapse sendEmailWire back onto it per its doc comment instead of carrying it as a local-only extra", tag)
		}
	}

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
	// Decode both sides before comparing, rather than diffing the raw bytes:
	// json.Marshal emits struct fields in declaration order, so a byte
	// compare would fail on a harmless upstream field reorder (a no-op on
	// the wire — email-service decodes by key) and blame the wrong thing.
	var wantFields, gotFields map[string]any
	if err := json.Unmarshal(want, &wantFields); err != nil {
		t.Fatalf("decode upstream contract JSON: %v", err)
	}
	if err := json.Unmarshal(got, &gotFields); err != nil {
		t.Fatalf("decode local wire JSON: %v", err)
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Errorf("newSendEmailWire dropped or crossed a shared field:\n got  %v\n want %v", gotFields, wantFields)
	}
}

// jsonFieldSet maps, for each exported field of t carrying a json tag, the
// tag name (the part before any comma, e.g. "omitempty") to the field's Go
// type. Fields tagged `json:"-"`, untagged, or unexported (encoding/json
// never serializes these) are excluded.
func jsonFieldSet(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = f.Type
	}
	return fields
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
