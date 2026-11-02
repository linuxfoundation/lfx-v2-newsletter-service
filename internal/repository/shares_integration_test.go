// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// Postgres-backed share tests (gated on NEWSLETTER_TEST_DATABASE_URL like the
// rest of this package's integration tests).

func userHash(c byte) string { return strings.Repeat(string(c), 64) }

func TestShares_DedupePerUserAndChannel(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	other := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(20))

	shares := []*model.NewsletterShare{
		{NewsletterID: id, UserHash: userHash('a'), Channel: model.ShareChannelLinkedIn},
		{NewsletterID: id, UserHash: userHash('a'), Channel: model.ShareChannelLinkedIn}, // repeat: no-op
		{NewsletterID: id, UserHash: userHash('a'), Channel: model.ShareChannelCopyLink},
		{NewsletterID: id, UserHash: userHash('b'), Channel: model.ShareChannelLinkedIn},
	}
	for _, s := range shares {
		if err := repo.RecordShare(ctx, s); err != nil {
			t.Fatalf("RecordShare: %v", err)
		}
	}

	counts, err := repo.ShareCounts(ctx, []uuid.UUID{id, other})
	if err != nil {
		t.Fatalf("ShareCounts: %v", err)
	}
	if counts[id] != 3 {
		t.Errorf("count = %d, want 3 (repeat collapsed)", counts[id])
	}
	if _, ok := counts[other]; ok {
		t.Errorf("unshared newsletter present in counts: %v", counts)
	}

	stats, err := repo.ShareStats(ctx, id)
	if err != nil {
		t.Fatalf("ShareStats: %v", err)
	}
	if stats.Total != 3 || stats.ByChannel[model.ShareChannelLinkedIn] != 2 || stats.ByChannel[model.ShareChannelCopyLink] != 1 {
		t.Errorf("stats = %+v, want total 3, linkedin 2, copy_link 1", stats)
	}

	if counts, err := repo.ShareCounts(ctx, nil); err != nil || len(counts) != 0 {
		t.Errorf("ShareCounts(nil) = %v, %v; want empty, nil", counts, err)
	}
}

func TestShares_CheckConstraints(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))

	if err := repo.RecordShare(ctx, &model.NewsletterShare{NewsletterID: id, UserHash: userHash('a'), Channel: "myspace"}); err == nil {
		t.Error("unknown channel accepted, want CHECK violation")
	}
	if err := repo.RecordShare(ctx, &model.NewsletterShare{NewsletterID: id, UserHash: "alice", Channel: model.ShareChannelX}); err == nil {
		t.Error("raw user identifier accepted, want CHECK violation")
	}
}
