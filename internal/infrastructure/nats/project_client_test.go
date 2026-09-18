// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"errors"
	"testing"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// TestProjectServiceErrorCode pins the envelope parser used by parseProjectReply.
func TestProjectServiceErrorCode(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantCode string
		wantErr  bool
	}{
		{name: "not_found code", data: []byte(`{"error":"not_found"}`), wantCode: "not_found"},
		{name: "internal code", data: []byte(`{"error":"internal"}`), wantCode: "internal"},
		{name: "unknown code", data: []byte(`{"error":"foo"}`), wantCode: "foo"},
		{name: "success plain string", data: []byte("my-slug"), wantCode: ""},
		// JSON without "error" key: code is "", no error (caller classifies).
		{name: "success json without error key — caller must classify", data: []byte(`{"slug":"k8s"}`), wantCode: ""},
		{name: "empty body", data: []byte{}, wantCode: ""},
		{name: "nil body", data: nil, wantCode: ""},
		// Malformed JSON starting with '{': non-nil error is returned.
		{name: "malformed JSON — unmarshal error propagated", data: []byte(`{not-json`), wantCode: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectServiceErrorCode(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Errorf("projectServiceErrorCode(%q) error = nil, want non-nil", tt.data)
				}
				return
			}
			if err != nil {
				t.Errorf("projectServiceErrorCode(%q) unexpected error: %v", tt.data, err)
			}
			if got != tt.wantCode {
				t.Errorf("projectServiceErrorCode(%q) = %q, want %q", tt.data, got, tt.wantCode)
			}
		})
	}
}

// TestParseProjectReply exercises the production parseProjectReply helper that
// get() delegates to, covering all classification branches.
func TestParseProjectReply(t *testing.T) {
	const subject = "lfx.projects-api.get_name"
	const uid = "00000000-0000-0000-0000-000000000001"

	tests := []struct {
		name           string
		reply          []byte
		wantValue      string
		wantNotFound   bool
		wantUnexpected bool // non-not-found error cases must produce pkgerrors.Unexpected
		wantErr        bool
	}{
		{
			name:      "success plain string",
			reply:     []byte("Test Project"),
			wantValue: "Test Project",
		},
		{
			// Embedded braces are valid in display names; only the prefix matters.
			// A mistaken bytes.Contains(reply, '{') guard would fail this case.
			name:      "success — brace in middle of name is accepted",
			reply:     []byte("Foo {Bar} Working Group"),
			wantValue: "Foo {Bar} Working Group",
		},
		{
			// Structural disjointness: a '{'-prefixed payload that has no recognised
			// error code is Unexpected, not a success. This enforces the guarantee
			// that parseProjectReply never returns a success value starting with '{'.
			name:           "JSON object without error key → pkgerrors.Unexpected (ambiguous)",
			reply:          []byte(`{"name":"Kubernetes"}`),
			wantUnexpected: true,
			wantErr:        true,
		},
		{
			name:         "not_found envelope → pkgerrors.NotFound",
			reply:        []byte(`{"error":"not_found","message":"project not found"}`),
			wantNotFound: true,
			wantErr:      true,
		},
		{
			name:           "internal envelope → pkgerrors.Unexpected",
			reply:          []byte(`{"error":"internal","message":"service error"}`),
			wantUnexpected: true,
			wantErr:        true,
		},
		{
			name:           "unknown future code → pkgerrors.Unexpected",
			reply:          []byte(`{"error":"unknown_code"}`),
			wantUnexpected: true,
			wantErr:        true,
		},
		{
			name:           "empty body → pkgerrors.Unexpected (transport failure)",
			reply:          []byte(""),
			wantUnexpected: true,
			wantErr:        true,
		},
		{
			name:           "nil body → pkgerrors.Unexpected (transport failure)",
			reply:          nil,
			wantUnexpected: true,
			wantErr:        true,
		},
		{
			// Malformed JSON starting with '{': projectServiceErrorCode returns an
			// error that parseProjectReply propagates as Unexpected.
			name:           "malformed JSON starting with '{' → pkgerrors.Unexpected",
			reply:          []byte(`{not valid json`),
			wantUnexpected: true,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProjectReply(subject, uid, tt.reply)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseProjectReply() = %q, want an error", got)
				}
				var nf pkgerrors.NotFound
				isNotFound := errors.As(err, &nf)
				if tt.wantNotFound && !isNotFound {
					t.Errorf("parseProjectReply() error = %T (%v), want pkgerrors.NotFound", err, err)
				}
				if !tt.wantNotFound && isNotFound {
					t.Errorf("parseProjectReply() error = pkgerrors.NotFound, must NOT be NotFound for this case")
				}
				if tt.wantUnexpected {
					var ux pkgerrors.Unexpected
					if !errors.As(err, &ux) {
						t.Errorf("parseProjectReply() error = %T (%v), want pkgerrors.Unexpected", err, err)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("parseProjectReply() unexpected error: %v", err)
			}
			if got != tt.wantValue {
				t.Errorf("parseProjectReply() = %q, want %q", got, tt.wantValue)
			}
		})
	}
}
