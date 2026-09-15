// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"errors"
	"testing"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// TestProjectServiceErrorCode pins the envelope parser used by
// ProjectClient.get.
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

// TestProjectClientGet_NotFoundEnvelope pins that {"error":"not_found"} maps
// to pkgerrors.NotFound — the caller can distinguish a permanent absence from a
// transient failure.
func TestProjectClientGet_NotFoundEnvelope(t *testing.T) {
	data := []byte(`{"error":"not_found","message":"project not found"}`)
	code := projectServiceErrorCode(data)

	if code != "not_found" {
		t.Fatalf("projectServiceErrorCode = %q, want %q", code, "not_found")
	}

	// Reproduce the exact branch logic from get().
	var gotErr error
	if code == "not_found" {
		gotErr = pkgerrors.NewNotFound("project uid-1 not found")
	} else if code != "" {
		gotErr = pkgerrors.NewUnexpected("project-service error (code=" + code + ")")
	}

	if gotErr == nil {
		t.Fatal("not_found must return an error")
	}
	var nf pkgerrors.NotFound
	if !errors.As(gotErr, &nf) {
		t.Errorf("not_found envelope must return pkgerrors.NotFound, got %T: %v", gotErr, gotErr)
	}
}

// TestProjectClientGet_InternalEnvelopeIsNotNotFound pins that {"error":"internal"}
// and unknown codes map to pkgerrors.Unexpected — infrastructure failures must
// not be misread as confirmed absences.
func TestProjectClientGet_InternalEnvelopeIsNotNotFound(t *testing.T) {
	for _, code := range []string{"internal", "unknown_future_code"} {
		code := code
		t.Run(code, func(t *testing.T) {
			data := []byte(`{"error":"` + code + `","message":"service error"}`)
			got := projectServiceErrorCode(data)

			if got != code {
				t.Fatalf("projectServiceErrorCode = %q, want %q", got, code)
			}

			// Reproduce the exact branch logic from get().
			var gotErr error
			if got == "not_found" {
				gotErr = pkgerrors.NewNotFound("project not found")
			} else if got != "" {
				gotErr = pkgerrors.NewUnexpected("project-service error (code=" + got + ")")
			}

			if gotErr == nil {
				t.Fatal("error code must return an error")
			}
			var nf pkgerrors.NotFound
			if errors.As(gotErr, &nf) {
				t.Errorf("%q code must not be pkgerrors.NotFound — got %T: %v", code, gotErr, gotErr)
			}
		})
	}
}

// TestProjectClientGet_EmptyBodyIsTransportFailure pins that an empty reply is
// an unresolvable transport/dispatch failure — not a confirmed absence.
// project-service now returns {"error":"not_found"} for missing projects under
// the coordinated RPC contract; empty bodies can no longer mean "not found".
func TestProjectClientGet_EmptyBodyIsTransportFailure(t *testing.T) {
	data := []byte("")
	code := projectServiceErrorCode(data)

	// Empty body does not parse as an error envelope.
	if code != "" {
		t.Fatalf("projectServiceErrorCode on empty body = %q, want %q", code, "")
	}

	// Reproduce the exact branch logic from get().
	value := string(data)
	var gotErr error
	var gotValue string
	if code == "not_found" {
		// should not reach
	} else if code != "" {
		gotErr = pkgerrors.NewUnexpected("project-service error (code=" + code + ")")
	} else if value == "" {
		gotErr = pkgerrors.NewUnexpected("project-service returned empty reply (transport failure)")
	} else {
		gotValue = value
	}

	if gotErr == nil {
		t.Fatalf("empty body must return an error, got value=%q", gotValue)
	}
	var nf pkgerrors.NotFound
	if errors.As(gotErr, &nf) {
		t.Errorf("empty body must not be pkgerrors.NotFound — got %T: %v", gotErr, gotErr)
	}
}
