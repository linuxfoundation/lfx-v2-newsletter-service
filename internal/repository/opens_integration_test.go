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

// recipientA and recipientB are well-formed recipient hashes. The
// newsletter_opens_recipient_hash_format check only accepts 64 lowercase hex
// characters, the shape the open pixel sends.
var (
	recipientA = strings.Repeat("a", 64)
	recipientB = strings.Repeat("b", 64)
)

// TestOpenCount_MatchesAnalyticsTotal checks the narrow open count used by GET
// one agrees with the full analytics aggregate, and is zero for an unopened issue.
func TestOpenCount_MatchesAnalyticsTotal(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	unopened := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(20))

	for _, hash := range []string{recipientA, recipientB, recipientA} {
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

// TestRecordOpen_CollapsesRepeatWithinHour pins the per-recipient-per-hour
// dedup: a repeat open by the same recipient in the same hour is a no-op, not
// an error and not a second row.
func TestRecordOpen_CollapsesRepeatWithinHour(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()
	id := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	waitClearOfHourBoundary(t, repo)

	for _, hash := range []string{recipientA, recipientA, recipientB} {
		if err := repo.RecordOpen(ctx, id, hash); err != nil {
			t.Fatalf("RecordOpen(%.8s…): %v", hash, err)
		}
	}

	got, err := repo.OpenCount(ctx, id)
	if err != nil {
		t.Fatalf("OpenCount: %v", err)
	}
	if got != 2 {
		t.Errorf("OpenCount = %d, want 2 (the repeat by recipientA in the same hour collapsed)", got)
	}
}

// waitClearOfHourBoundary sleeps past the next UTC hour boundary when it is
// less than hourBoundaryMargin away on the database clock. RecordOpen takes
// opened_at from the column default, so inserts that straddle the boundary
// land in different opened_at_hour buckets and are correctly kept as
// separate rows, which would make a dedup assertion fail for no code reason.
func waitClearOfHourBoundary(t *testing.T, repo *PostgresNewsletterRepo) {
	t.Helper()
	const hourBoundaryMargin = 10 * time.Second
	var secondsLeft float64
	if err := repo.db.NewRaw(
		"SELECT 3600 - mod(extract(epoch FROM clock_timestamp())::numeric, 3600)",
	).Scan(context.Background(), &secondsLeft); err != nil {
		t.Fatalf("read database clock: %v", err)
	}
	if left := time.Duration(secondsLeft * float64(time.Second)); left < hourBoundaryMargin {
		time.Sleep(left + time.Second)
	}
}
