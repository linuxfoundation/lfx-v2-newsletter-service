// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// TestUpdateNewsletterPublicationIDTriState pins the three JSON states the
// update request must tell apart. Every other field on UpdateNewsletterRequest
// is full-replace, so without this distinction a client PUT that omits the key
// would unfile the edition rather than leave it alone.
//
// The bodies go through encoding/json into the real request DTO rather than
// being handed to the parser as pre-built raw messages. The bug this guards
// against lives in the decoding step: a pointer field is set to nil both for an
// absent key and for an explicit null, so a parser test that builds the raw
// message itself passes while the endpoint still cannot tell null from absent.
func TestUpdateNewsletterPublicationIDTriState(t *testing.T) {
	target := uuid.New()

	tests := []struct {
		name    string
		body    string
		wantSet bool
		wantID  *uuid.UUID
		wantErr bool
	}{
		{
			name:    "absent preserves the current publication",
			body:    `{"subject":"s"}`,
			wantSet: false,
			wantID:  nil,
		},
		{
			name:    "explicit null unfiles",
			body:    `{"subject":"s","publication_id":null}`,
			wantSet: true,
			wantID:  nil,
		},
		{
			name:    "empty string unfiles",
			body:    `{"subject":"s","publication_id":""}`,
			wantSet: true,
			wantID:  nil,
		},
		{
			name:    "whitespace-only string unfiles",
			body:    `{"subject":"s","publication_id":"   "}`,
			wantSet: true,
			wantID:  nil,
		},
		{
			name:    "uuid moves the edition",
			body:    `{"subject":"s","publication_id":"` + target.String() + `"}`,
			wantSet: true,
			wantID:  &target,
		},
		{
			name:    "malformed uuid is a client error",
			body:    `{"subject":"s","publication_id":"not-a-uuid"}`,
			wantSet: true,
			wantErr: true,
		},
		{
			name:    "non-string json is a client error",
			body:    `{"subject":"s","publication_id":42}`,
			wantSet: true,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req publicapi.UpdateNewsletterRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("decode request body: %v", err)
			}

			id, set, err := parseUpdatePublicationID(req.PublicationID)

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

// TestUpdateNewsletterPublicationIDPersistence drives the whole PUT path with
// real JSON bodies and asserts what ends up on the stored row. The parser test
// above pins the decode; this one pins that the decoded state reaches the
// repository, so an absent key cannot quietly unfile an edition.
func TestUpdateNewsletterPublicationIDPersistence(t *testing.T) {
	pubA := uuid.New()
	pubB := uuid.New()

	// put seeds one draft filed under pubA, PUTs the given body, and returns the
	// stored row plus the response status.
	put := func(t *testing.T, body string) (*model.Newsletter, int) {
		t.Helper()

		newsletters := NewMockNewsletterRepository()
		publications := NewMockPublicationRepository()
		for _, id := range []uuid.UUID{pubA, pubB} {
			pub := &model.NewsletterPublication{ID: id, ProjectUID: "project-1", Slug: id.String(), Name: "Pub"}
			if err := publications.Create(t.Context(), pub); err != nil {
				t.Fatalf("seed publication: %v", err)
			}
		}
		draft := &model.Newsletter{
			ID:            uuid.New(),
			ProjectUID:    "project-1",
			Subject:       "Original",
			BodyHTML:      "<p>x</p>",
			EDReplyEmail:  "ed@example.org",
			CommitteeUIDs: []string{"c1"},
			Status:        model.StatusDraft,
			PublicationID: &pubA,
			CreatedBy:     "user",
		}
		if err := newsletters.Create(t.Context(), draft); err != nil {
			t.Fatalf("seed draft: %v", err)
		}

		h := &Handler{newsletter: service.NewNewsletterService(newsletters, publications)}
		req := httptest.NewRequest(http.MethodPut,
			"/projects/project-1/newsletters/"+draft.ID.String(), bytes.NewBufferString(body))
		req.SetPathValue("project_uid", "project-1")
		req.SetPathValue("newsletter_uid", draft.ID.String())
		req.Header.Set("If-Match", `"1"`)
		rec := httptest.NewRecorder()
		h.UpdateNewsletter(rec, req)

		stored, err := newsletters.Get(t.Context(), draft.ID)
		if err != nil {
			t.Fatalf("read stored draft: %v", err)
		}
		return stored, rec.Code
	}

	// The fields every body below carries, so only publication_id varies.
	const fields = `"subject":"Updated","body_html":"<p>y</p>","ed_reply_email":"ed@example.org","committee_uids":["c1"]`

	t.Run("absent key preserves the publication", func(t *testing.T) {
		stored, code := put(t, `{`+fields+`}`)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if stored.PublicationID == nil || *stored.PublicationID != pubA {
			t.Errorf("stored publication_id = %v, want %v — an omitted key unfiled the edition", stored.PublicationID, pubA)
		}
	})

	t.Run("explicit null unfiles the edition", func(t *testing.T) {
		stored, code := put(t, `{`+fields+`,"publication_id":null}`)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if stored.PublicationID != nil {
			t.Errorf("stored publication_id = %v, want nil — an explicit null did not unfile the edition", stored.PublicationID)
		}
	})

	t.Run("uuid moves the edition", func(t *testing.T) {
		stored, code := put(t, `{`+fields+`,"publication_id":"`+pubB.String()+`"}`)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if stored.PublicationID == nil || *stored.PublicationID != pubB {
			t.Errorf("stored publication_id = %v, want %v", stored.PublicationID, pubB)
		}
	})

	t.Run("a publication from another project is rejected", func(t *testing.T) {
		other := uuid.New()
		stored, code := put(t, `{`+fields+`,"publication_id":"`+other.String()+`"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", code)
		}
		if stored.PublicationID == nil || *stored.PublicationID != pubA {
			t.Errorf("stored publication_id = %v, want %v — a rejected update changed the row", stored.PublicationID, pubA)
		}
	})
}
