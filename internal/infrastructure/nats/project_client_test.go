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
		name    string
		data    []byte
		wantErr string
	}{
		{name: "not_found code", data: []byte(`{"error":"not_found"}`), wantErr: "not_found"},
		{name: "internal code", data: []byte(`{"error":"internal"}`), wantErr: "internal"},
		{name: "unknown code", data: []byte(`{"error":"foo"}`), wantErr: "foo"},
		{name: "success plain string", data: []byte("my-slug"), wantErr: ""},
		{name: "success json without error key", data: []byte(`{"slug":"k8s"}`), wantErr: ""},
		{name: "empty body", data: []byte{}, wantErr: ""},
		{name: "nil body", data: nil, wantErr: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectServiceErrorCode(tt.data)
			if got != tt.wantErr {
				t.Errorf("projectServiceErrorCode(%q) = %q, want %q", tt.data, got, tt.wantErr)
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
			// Structural disjointness: a JSON object without an "error" key is NOT
			// treated as an error envelope — it is returned as a success string.
			// The '{' prefix guard is necessary but not sufficient for error detection;
			// both the '{' prefix AND a non-empty "error" key are required.
			name:      "JSON object without error key → success value, not an error",
			reply:     []byte(`{"name":"Kubernetes"}`),
			wantValue: `{"name":"Kubernetes"}`,
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
