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
)

// TestToAPICommitteeNewsletter verifies the member-facing DTO mapping: the
// identifying/list fields carry through, and neither manager-only fields
// (body_html, ed_reply_email, group_id, created_by) nor the full committee
// audience ever appear in the serialized shape.
func TestToAPICommitteeNewsletter(t *testing.T) {
	id := uuid.New()
	sentAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	groupID := "group-123"

	dto := toAPICommitteeNewsletter(&model.Newsletter{
		ID:            id,
		ProjectUID:    "project-1",
		Subject:       "Quarterly update",
		BodyHTML:      "<p>secret-draft-content</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"committee-a", "committee-b"},
		Status:        model.StatusSent,
		SentAt:        &sentAt,
		GroupID:       &groupID,
		CreatedBy:     "sender-user",
	})

	if dto.ID != id.String() {
		t.Errorf("ID: got %s, want %s", dto.ID, id)
	}
	if dto.ProjectUID != "project-1" || dto.Subject != "Quarterly update" {
		t.Errorf("got (%s, %s), want (project-1, Quarterly update)", dto.ProjectUID, dto.Subject)
	}
	if dto.SentAt == nil || !dto.SentAt.Equal(sentAt) {
		t.Errorf("SentAt: got %v, want %v", dto.SentAt, sentAt)
	}

	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// committee-a / committee-b: a member of one committee must not be able to
	// enumerate the newsletter's other audience committees from this DTO.
	for _, leak := range []string{"secret-draft-content", "ed@example.com", "group-123", "sender-user", "committee-a", "committee-b"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("member-facing DTO leaked %q: %s", leak, body)
		}
	}
}
