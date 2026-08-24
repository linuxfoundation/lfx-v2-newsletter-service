// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// TestParseUpdatePublicationIDTriState pins the three JSON states the update
// request must tell apart. Every other field on UpdateNewsletterRequest is
// full-replace, so without this distinction a client PUT that omits the key
// would unfile the edition rather than leave it alone.
func TestParseUpdatePublicationIDTriState(t *testing.T) {
	target := uuid.New()

	tests := []struct {
		name    string
		raw     *string // nil = field absent from the JSON body
		wantSet bool
		wantID  *uuid.UUID
		wantErr bool
	}{
		{
			name:    "absent preserves the current publication",
			raw:     nil,
			wantSet: false,
			wantID:  nil,
		},
		{
			name:    "explicit null unfiles",
			raw:     strPtr(`null`),
			wantSet: true,
			wantID:  nil,
		},
		{
			name:    "empty string unfiles",
			raw:     strPtr(`""`),
			wantSet: true,
			wantID:  nil,
		},
		{
			name:    "whitespace-only string unfiles",
			raw:     strPtr(`"   "`),
			wantSet: true,
			wantID:  nil,
		},
		{
			name:    "uuid moves the edition",
			raw:     strPtr(`"` + target.String() + `"`),
			wantSet: true,
			wantID:  &target,
		},
		{
			name:    "malformed uuid is a client error",
			raw:     strPtr(`"not-a-uuid"`),
			wantSet: true,
			wantErr: true,
		},
		{
			name:    "non-string json is a client error",
			raw:     strPtr(`42`),
			wantSet: true,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var field *json.RawMessage
			if tc.raw != nil {
				msg := json.RawMessage(*tc.raw)
				field = &msg
			}

			id, set, err := parseUpdatePublicationID(field)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got id=%v set=%v", id, set)
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
			case tc.wantID == nil && id != nil:
				t.Errorf("id = %v, want nil", id)
			case tc.wantID != nil && id == nil:
				t.Errorf("id = nil, want %v", tc.wantID)
			case tc.wantID != nil && *id != *tc.wantID:
				t.Errorf("id = %v, want %v", id, tc.wantID)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
