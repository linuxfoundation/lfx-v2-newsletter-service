// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The cursor codecs are the only pure logic in this package (the SQL paths are
// exercised against a real database elsewhere, not unit-tested — repo norm).
// A regression here silently truncates or corrupts pagination, so lock in the
// encode→decode identity and the malformed-token failure mode.

func TestSentCursorRoundTrip(t *testing.T) {
	in := sentListCursor{SentAt: time.Date(2026, 7, 1, 12, 30, 45, 0, time.UTC), ID: uuid.New()}

	out, err := decodeSentCursor(encodeSentCursor(in))
	if err != nil {
		t.Fatalf("decodeSentCursor: %v", err)
	}
	if !out.SentAt.Equal(in.SentAt) || out.ID != in.ID {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

func TestSentCursorRejectsMalformedTokens(t *testing.T) {
	// "e30" is base64url("{}") and "bnVsbA" is base64url("null"): both decode
	// and unmarshal cleanly but leave zero cursor fields, which must be
	// rejected rather than silently produce an empty/mispositioned page.
	for _, token := range []string{"not-base64!", "bm90LWpzb24", "", "e30", "bnVsbA"} {
		if _, err := decodeSentCursor(token); err == nil {
			t.Errorf("decodeSentCursor(%q): expected error, got nil", token)
		}
	}
}

func TestSentCursorRejectsPartiallyZeroCursors(t *testing.T) {
	sentAt := time.Date(2026, 7, 1, 12, 30, 45, 0, time.UTC)
	for name, c := range map[string]sentListCursor{
		"zero id":      {SentAt: sentAt},
		"zero sent_at": {ID: uuid.New()},
	} {
		if _, err := decodeSentCursor(encodeSentCursor(c)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestPublicationCursorRoundTrip(t *testing.T) {
	in := publicationListCursor{CreatedAt: time.Date(2026, 8, 2, 9, 15, 0, 0, time.UTC), ID: uuid.New()}

	out, err := decodePublicationCursor(encodePublicationCursor(in))
	if err != nil {
		t.Fatalf("decodePublicationCursor: %v", err)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}

func TestPublicationCursorRejectsMalformedTokens(t *testing.T) {
	// "e30" is base64url("{}") and "bnVsbA" is base64url("null"): both decode
	// and unmarshal cleanly but leave zero cursor fields. Used as a filter they
	// would return an empty page for a project that has publications, so they
	// must be rejected as invalid tokens.
	for _, token := range []string{"not-base64!", "bm90LWpzb24", "e30", "bnVsbA"} {
		if _, err := decodePublicationCursor(token); err == nil {
			t.Errorf("decodePublicationCursor(%q): expected error, got nil", token)
		}
	}
}

func TestPublicationCursorRejectsPartiallyZeroCursors(t *testing.T) {
	createdAt := time.Date(2026, 8, 2, 9, 15, 0, 0, time.UTC)
	for name, c := range map[string]publicationListCursor{
		"zero id":         {CreatedAt: createdAt},
		"zero created_at": {ID: uuid.New()},
	} {
		if _, err := decodePublicationCursor(encodePublicationCursor(c)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestListCursorRoundTrip(t *testing.T) {
	in := listCursor{UpdatedAt: time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC), ID: uuid.New()}

	out, err := decodeCursor(encodeCursor(in))
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !out.UpdatedAt.Equal(in.UpdatedAt) || out.ID != in.ID {
		t.Errorf("round trip: got %+v, want %+v", out, in)
	}
}
