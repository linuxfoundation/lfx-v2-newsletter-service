// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// TestOpenCount_MatchesAnalyticsTotal checks the narrow open count used by GET
// one agrees with the full analytics aggregate, and is zero for an unopened issue.
//
// Opens are inserted directly rather than through RecordOpen: they need valid
// 64-char lowercase hex hashes (newsletter_opens_recipient_hash_format) and
// distinct hour buckets so each row lands (uq_opens_newsletter_recipient_hour).
func TestOpenCount_MatchesAnalyticsTotal(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	unopened := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(20))

	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	base := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	opens := []*model.NewsletterOpen{
		{NewsletterID: id, RecipientHash: hashA, OpenedAt: base},
		{NewsletterID: id, RecipientHash: hashB, OpenedAt: base},
		{NewsletterID: id, RecipientHash: hashA, OpenedAt: base.Add(2 * time.Hour)}, // a repeat open, a later hour
	}
	if _, err := repo.db.NewInsert().Model(&opens).Exec(ctx); err != nil {
		t.Fatalf("insert opens: %v", err)
	}

	got, err := repo.OpenCount(ctx, id)
	if err != nil {
		t.Fatalf("OpenCount: %v", err)
	}
	full, err := repo.Analytics(ctx, id)
	if err != nil {
		t.Fatalf("Analytics: %v", err)
	}
	if got != 3 || got != full.TotalOpens {
		t.Errorf("OpenCount = %d, want 3 and equal to the analytics total %d", got, full.TotalOpens)
	}

	if none, err := repo.OpenCount(ctx, unopened); err != nil || none != 0 {
		t.Errorf("OpenCount(unopened) = %d, %v; want 0, nil", none, err)
	}
}
