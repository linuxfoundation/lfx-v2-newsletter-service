// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"strings"
	"testing"
)

func TestCreateDraftLengthLimitsCountCharactersNotBytes(t *testing.T) {
	tests := []struct {
		name      string
		subject   string
		bodyHTML  string
		wantValid bool
	}{
		{
			name:      "ascii subject at the limit",
			subject:   strings.Repeat("a", maxSubjectLength),
			bodyHTML:  "<p>Body</p>",
			wantValid: true,
		},
		{
			name:      "ascii subject over the limit",
			subject:   strings.Repeat("a", maxSubjectLength+1),
			bodyHTML:  "<p>Body</p>",
			wantValid: false,
		},
		{
			// Each of these is 3 bytes, so a byte-counting check would reject a
			// subject that is well under the documented character limit.
			name:      "multibyte subject at the limit",
			subject:   strings.Repeat("界", maxSubjectLength),
			bodyHTML:  "<p>Body</p>",
			wantValid: true,
		},
		{
			name:      "multibyte subject over the limit",
			subject:   strings.Repeat("界", maxSubjectLength+1),
			bodyHTML:  "<p>Body</p>",
			wantValid: false,
		},
		{
			name:      "multibyte body at the limit",
			subject:   "Hello",
			bodyHTML:  strings.Repeat("界", maxBodyHTMLLength),
			wantValid: true,
		},
		{
			name:      "multibyte body over the limit",
			subject:   "Hello",
			bodyHTML:  strings.Repeat("界", maxBodyHTMLLength+1),
			wantValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewNewsletterService(newFakeRepo())
			_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
				ProjectUID:    "proj-1",
				Subject:       tc.subject,
				BodyHTML:      tc.bodyHTML,
				EDReplyEmail:  "ed@example.com",
				CommitteeUIDs: []string{"cmte-1"},
				CreatedBy:     "user-1",
			})
			if tc.wantValid {
				if err != nil {
					t.Fatalf("CreateDraft: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("CreateDraft: expected a validation error, got nil")
			}
			if !IsValidationError(err) {
				t.Fatalf("CreateDraft: expected ErrInvalidRequest, got %v", err)
			}
		})
	}
}
