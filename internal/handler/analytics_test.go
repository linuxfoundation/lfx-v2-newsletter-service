// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
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
