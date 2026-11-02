// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// TestOpenCount_MatchesAnalyticsTotal checks the narrow open count used by GET
// one agrees with the full analytics aggregate, and is zero for an unopened issue.
func TestOpenCount_MatchesAnalyticsTotal(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	unopened := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(20))

	for _, hash := range []string{"recipient-a", "recipient-b", "recipient-a"} {
		if err := repo.RecordOpen(ctx, id, hash); err != nil {
			t.Fatalf("RecordOpen: %v", err)
		}
	}

	got, err := repo.OpenCount(ctx, id)
	if err != nil {
		t.Fatalf("OpenCount: %v", err)
	}
	full, err := repo.Analytics(ctx, id)
	if err != nil {
		t.Fatalf("Analytics: %v", err)
	}
	if got != full.TotalOpens || got == 0 {
		t.Errorf("OpenCount = %d, want the analytics total %d (non-zero)", got, full.TotalOpens)
	}

	if none, err := repo.OpenCount(ctx, unopened); err != nil || none != 0 {
		t.Errorf("OpenCount(unopened) = %d, %v; want 0, nil", none, err)
	}
}
