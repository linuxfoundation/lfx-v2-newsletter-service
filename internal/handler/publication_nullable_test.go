// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"testing"
)

// TestParseNullableStringTriState pins the three JSON states a nullable
// publication field must tell apart. Without the presence flag, "absent" and
// "null" both decode to nil, so a caller can set template_set_id,
// sender_email or view_online_base but never clear one.
func TestParseNullableStringTriState(t *testing.T) {
	tests := []struct {
		name      string
		body      string // the whole request body
		wantSet   bool
		wantValue *string
		wantErr   bool
	}{
		{
			name:    "absent preserves the stored value",
			body:    `{"name":"unchanged"}`,
			wantSet: false,
		},
		{
			name:    "explicit null clears the column",
			body:    `{"template_set_id":null}`,
			wantSet: true,
		},
		{
			name:    "empty string clears the column",
			body:    `{"template_set_id":""}`,
			wantSet: true,
		},
		{
			name:      "value sets the column",
			body:      `{"template_set_id":"aaif-user-community"}`,
			wantSet:   true,
			wantValue: strPtr("aaif-user-community"),
		},
		{
			name:      "value is trimmed",
			body:      `{"template_set_id":"  aaif  "}`,
			wantSet:   true,
			wantValue: strPtr("aaif"),
		},
		{
			name:    "non-string is a client error",
			body:    `{"template_set_id":42}`,
			wantSet: true,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Decode through the real request struct so the test covers the
			// JSON wiring, not just the helper in isolation.
			var req struct {
				TemplateSetID json.RawMessage `json:"template_set_id,omitempty"`
			}
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}

			value, set, err := parseNullableString(req.TemplateSetID, "template_set_id")

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got value=%v set=%v", value, set)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if set != tc.wantSet {
				t.Errorf("set = %v, want %v", set, tc.wantSet)
			}
			switch {
			case tc.wantValue == nil && value != nil:
				t.Errorf("value = %q, want nil (clear)", *value)
			case tc.wantValue != nil && value == nil:
				t.Errorf("value = nil, want %q", *tc.wantValue)
			case tc.wantValue != nil && *value != *tc.wantValue:
				t.Errorf("value = %q, want %q", *value, *tc.wantValue)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
