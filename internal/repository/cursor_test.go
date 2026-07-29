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
	for _, token := range []string{"not-base64!", "bm90LWpzb24", ""} {
		if _, err := decodeSentCursor(token); err == nil {
			t.Errorf("decodeSentCursor(%q): expected error, got nil", token)
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
