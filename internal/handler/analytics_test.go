// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// TestToAPIAnalytics_FailedRecipients verifies the domain→DTO mapping carries
// the failed-recipient list through and normalizes a nil list to an empty JSON
// array (never null) so the UI can rely on the field always being present.
func TestToAPIAnalytics_FailedRecipients(t *testing.T) {
	id := uuid.New()

	t.Run("passes through populated list", func(t *testing.T) {
		dto := toAPIAnalytics(&model.Analytics{
			NewsletterID:     id,
			Status:           model.StatusSent,
			FailedRecipients: []string{"bounce@x", "complaint@x"},
		})
		if len(dto.FailedRecipients) != 2 || dto.FailedRecipients[0] != "bounce@x" || dto.FailedRecipients[1] != "complaint@x" {
			t.Fatalf("FailedRecipients: got %v, want [bounce@x complaint@x]", dto.FailedRecipients)
		}
	})

	t.Run("nil normalizes to empty JSON array", func(t *testing.T) {
		dto := toAPIAnalytics(&model.Analytics{NewsletterID: id, Status: model.StatusSent})
		if dto.FailedRecipients == nil {
			t.Fatal("FailedRecipients: got nil, want non-nil empty slice")
		}
		body, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(body), `"failed_recipients":[]`) {
			t.Errorf("JSON: want failed_recipients serialized as [], got %s", body)
		}
	})
}

// TestToAPIRecipientEngagement verifies the port→DTO mapping keeps the
// always-present-array contract for recipients and opened_at_list, and that
// optional timestamps are omitted (not null) when unset.
func TestToAPIRecipientEngagement(t *testing.T) {
	id := uuid.New()
	opened := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	t.Run("maps records with names and opens", func(t *testing.T) {
		dto := toAPIRecipientEngagement(id.String(), []port.RecipientEngagement{
			{
				Name: "Ann Ames",
				Record: port.EmailRecipientRecord{
					To: "ann@x.dev", Delivered: true, Opened: true, OpenCount: 1,
					LastOpened: &opened, OpenedAtList: []time.Time{opened},
				},
			},
		})
		if dto.NewsletterID != id.String() {
			t.Errorf("NewsletterID: got %q, want %q", dto.NewsletterID, id.String())
		}
		if len(dto.Recipients) != 1 {
			t.Fatalf("Recipients: got %d, want 1", len(dto.Recipients))
		}
		r := dto.Recipients[0]
		if r.Name != "Ann Ames" || r.Email != "ann@x.dev" || !r.Opened || r.OpenCount != 1 {
			t.Errorf("record: got %+v", r)
		}
		if len(r.OpenedAtList) != 1 || !r.OpenedAtList[0].Equal(opened) {
			t.Errorf("OpenedAtList: got %v, want [%v]", r.OpenedAtList, opened)
		}
	})

	t.Run("empty inputs serialize as arrays, optional fields omitted", func(t *testing.T) {
		dto := toAPIRecipientEngagement(id.String(), []port.RecipientEngagement{
			{Record: port.EmailRecipientRecord{To: "quiet@x.dev", Delivered: true}},
		})
		body, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(body)
		if !strings.Contains(s, `"opened_at_list":[]`) {
			t.Errorf("JSON: want opened_at_list serialized as [], got %s", s)
		}
		for _, absent := range []string{`"name"`, `"delivered_at"`, `"failed_at"`, `"last_opened_at"`, `"sent_at"`} {
			if strings.Contains(s, absent) {
				t.Errorf("JSON: want %s omitted when unset, got %s", absent, s)
			}
		}

		empty := toAPIRecipientEngagement(id.String(), nil)
		body, err = json.Marshal(empty)
		if err != nil {
			t.Fatalf("marshal empty: %v", err)
		}
		if !strings.Contains(string(body), `"recipients":[]`) {
			t.Errorf("JSON: want recipients serialized as [], got %s", body)
		}
	})
}
