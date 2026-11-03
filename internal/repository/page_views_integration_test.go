// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// Postgres-backed page-view tests (gated on NEWSLETTER_TEST_DATABASE_URL like
// the rest of this package's integration tests).

func TestPageViews_DedupePerUser(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	other := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(20))

	for _, v := range []*model.NewsletterPageView{
		{NewsletterID: id, UserHash: userHash('a')},
		{NewsletterID: id, UserHash: userHash('a')}, // repeat: no-op
		{NewsletterID: id, UserHash: userHash('b')},
	} {
		if err := repo.RecordPageView(ctx, v); err != nil {
			t.Fatalf("RecordPageView: %v", err)
		}
	}

	counts, err := repo.PageViewCounts(ctx, []uuid.UUID{id, other})
	if err != nil {
		t.Fatalf("PageViewCounts: %v", err)
	}
	if counts[id] != 2 {
		t.Errorf("count = %d, want 2 (repeat collapsed)", counts[id])
	}
	if _, ok := counts[other]; ok {
		t.Errorf("unviewed newsletter present in counts: %v", counts)
	}
	if counts, err := repo.PageViewCounts(ctx, nil); err != nil || len(counts) != 0 {
		t.Errorf("PageViewCounts(nil) = %v, %v; want empty, nil", counts, err)
	}

	if err := repo.RecordPageView(ctx, &model.NewsletterPageView{NewsletterID: id, UserHash: "alice"}); err == nil {
		t.Error("raw user identifier accepted, want CHECK violation")
	}
}

func TestPageViews_DeletePageViewsByUser(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	first := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	second := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(20))

	for _, v := range []*model.NewsletterPageView{
		{NewsletterID: first, UserHash: userHash('a')},
		{NewsletterID: second, UserHash: userHash('a')},
		{NewsletterID: first, UserHash: userHash('b')},
	} {
		if err := repo.RecordPageView(ctx, v); err != nil {
			t.Fatalf("RecordPageView: %v", err)
		}
	}

	n, err := repo.DeletePageViewsByUser(ctx, userHash('a'))
	if err != nil || n != 2 {
		t.Fatalf("DeletePageViewsByUser = %d, %v; want 2 rows across both newsletters", n, err)
	}
	counts, err := repo.PageViewCounts(ctx, []uuid.UUID{first, second})
	if err != nil {
		t.Fatalf("PageViewCounts: %v", err)
	}
	if counts[first] != 1 || counts[second] != 0 {
		t.Errorf("counts after erase = %v, want only user b's view on the first newsletter", counts)
	}
}
