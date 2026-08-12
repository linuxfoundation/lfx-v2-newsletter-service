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
		for _, want := range []string{`"daily_clicks":[]`, `"top_links":[]`} {
			if !strings.Contains(string(body), want) {
				t.Errorf("JSON: want %s (nil coerced to empty array), got %s", want, body)
			}
		}
	})

	t.Run("passes through click fields", func(t *testing.T) {
		dto := toAPIAnalytics(&model.Analytics{
			NewsletterID: id, Status: model.StatusSent,
			TotalClicks: 5, UniqueClicks: 3, ClickRate: 0.3, ClickToOpenRate: 0.6,
			DailyClicks: []model.DailyClicks{{Date: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Clicks: 5, UniqueClicks: 3}},
			TopLinks:    []model.LinkClicks{{URL: "https://example.com", Clicks: 5, UniqueClicks: 3}},
		})
		if dto.TotalClicks != 5 || dto.UniqueClicks != 3 || dto.ClickRate != 0.3 || dto.ClickToOpenRate != 0.6 {
			t.Errorf("click scalars: got %+v", dto)
		}
		if len(dto.DailyClicks) != 1 || dto.DailyClicks[0].Date != "2026-06-01" || dto.DailyClicks[0].Clicks != 5 {
			t.Errorf("DailyClicks: got %+v", dto.DailyClicks)
		}
		if len(dto.TopLinks) != 1 || dto.TopLinks[0].URL != "https://example.com" || dto.TopLinks[0].Clicks != 5 {
			t.Errorf("TopLinks: got %+v", dto.TopLinks)
		}
	})
}

// TestToAPIRecipientEngagement verifies the port→DTO mapping keeps the
// always-present-array contract for recipients and opened_at_list, and that
// optional timestamps are omitted (not null) when unset.
func TestToAPIRecipientEngagement(t *testing.T) {
	id := uuid.New()
	opened := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	clicked := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)

	t.Run("maps records with names, opens, and clicks", func(t *testing.T) {
		dto := toAPIRecipientEngagement(id.String(), &port.RecipientEngagementResult{
			TotalRecipients: 1,
			Complete:        true,
			Recipients: []port.RecipientEngagement{
				{
					Name: "Ann Ames",
					Record: port.EmailRecipientRecord{
						To: "ann@x.dev", Delivered: true, Opened: true, OpenCount: 1,
						LastOpened: &opened, OpenedAtList: []time.Time{opened},
						Clicked: true, ClickCount: 2,
						LastClicked: &clicked, ClickedAtList: []time.Time{clicked},
					},
				},
			},
		})
		if dto.NewsletterID != id.String() {
			t.Errorf("NewsletterID: got %q, want %q", dto.NewsletterID, id.String())
		}
		if dto.TotalRecipients != 1 || !dto.Complete {
			t.Errorf("completeness: got total=%d complete=%v, want 1/true", dto.TotalRecipients, dto.Complete)
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
		if !r.Clicked || r.ClickCount != 2 {
			t.Errorf("click fields: got clicked=%v click_count=%d, want true/2", r.Clicked, r.ClickCount)
		}
		if r.LastClickedAt == nil || !r.LastClickedAt.Equal(clicked) {
			t.Errorf("LastClickedAt: got %v, want %v", r.LastClickedAt, clicked)
		}
		if len(r.ClickedAtList) != 1 || !r.ClickedAtList[0].Equal(clicked) {
			t.Errorf("ClickedAtList: got %v, want [%v]", r.ClickedAtList, clicked)
		}
	})

	t.Run("empty inputs serialize as arrays, optional fields omitted", func(t *testing.T) {
		dto := toAPIRecipientEngagement(id.String(), &port.RecipientEngagementResult{
			TotalRecipients: 2,
			Complete:        false,
			Recipients: []port.RecipientEngagement{
				{Record: port.EmailRecipientRecord{To: "quiet@x.dev", Delivered: true}},
			},
		})
		body, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		s := string(body)
		if !strings.Contains(s, `"opened_at_list":[]`) {
			t.Errorf("JSON: want opened_at_list serialized as [], got %s", s)
		}
		if !strings.Contains(s, `"clicked_at_list":[]`) {
			t.Errorf("JSON: want clicked_at_list serialized as [], got %s", s)
		}
		if !strings.Contains(s, `"complete":false`) {
			t.Errorf("JSON: want complete always present (even when false), got %s", s)
		}
		for _, absent := range []string{`"name"`, `"delivered_at"`, `"failed_at"`, `"last_opened_at"`, `"last_clicked_at"`, `"sent_at"`} {
			if strings.Contains(s, absent) {
				t.Errorf("JSON: want %s omitted when unset, got %s", absent, s)
			}
		}

		empty := toAPIRecipientEngagement(id.String(), &port.RecipientEngagementResult{Complete: true})
		body, err = json.Marshal(empty)
		if err != nil {
			t.Fatalf("marshal empty: %v", err)
		}
		if !strings.Contains(string(body), `"recipients":[]`) {
			t.Errorf("JSON: want recipients serialized as [], got %s", body)
		}
	})
}
