// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// Postgres-backed repository tests. The production SQL (status predicate,
// array containment, tuple keyset) is the sole enforcement of the sent-only
// member-visibility invariant, so it gets exercised against a real database
// rather than only through fakes. Gated on NEWSLETTER_TEST_DATABASE_URL: unset
// (e.g. a plain local `go test ./...`) skips; CI provides a postgres service
// and sets it (see .github/workflows/newsletter-api-build.yml).
//
// Each helper call TRUNCATEs the newsletters table, so these tests must not
// run against a database holding data anyone cares about.

// newIntegrationRepo connects, applies the embedded schema, and returns a
// repository over a clean newsletters table.
func newIntegrationRepo(t *testing.T) *PostgresNewsletterRepo {
	t.Helper()
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set — skipping Postgres-backed repository tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := schema.Apply(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("apply schema: %v", err)
	}

	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	t.Cleanup(func() {
		_ = db.Close()
		pool.Close()
	})

	if _, err := db.ExecContext(ctx, "TRUNCATE newsletters CASCADE"); err != nil {
		t.Fatalf("truncate newsletters: %v", err)
	}
	return NewPostgresNewsletterRepo(db)
}

// seed inserts a newsletter row directly with the given status/audience;
// sentAt may be nil for drafts.
func seed(t *testing.T, repo *PostgresNewsletterRepo, status model.Status, committeeUIDs []string, sentAt *time.Time) uuid.UUID {
	t.Helper()
	n := &model.Newsletter{
		ProjectUID:    "project-1",
		Subject:       fmt.Sprintf("Subject %s", uuid.New()),
		BodyHTML:      "<p>body</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: committeeUIDs,
		Status:        status,
		SentAt:        sentAt,
		CreatedBy:     "seeder",
	}
	// The newsletters_sent_requires_group_id CHECK forbids sent rows without a
	// group_id (normally minted by MarkSending), and
	// newsletters_group_id_uuid_format requires it to be a UUID.
	if status == model.StatusSent {
		groupID := uuid.NewString()
		n.GroupID = &groupID
	}
	if err := repo.Create(context.Background(), n); err != nil {
		t.Fatalf("seed newsletter: %v", err)
	}
	return n.ID
}

func ts(minutesAgo int) *time.Time {
	v := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Duration(minutesAgo) * time.Minute)
	return &v
}

func TestListSentByCommittee_ExcludesDraftsAndSending(t *testing.T) {
	repo := newIntegrationRepo(t)

	sentID := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)
	seed(t, repo, model.StatusSending, []string{"committee-a"}, ts(5))

	page, err := repo.ListSentByCommittee(context.Background(), "committee-a", "")
	if err != nil {
		t.Fatalf("ListSentByCommittee: %v", err)
	}
	if len(page.Newsletters) != 1 || page.Newsletters[0].ID != sentID {
		t.Fatalf("got %d rows, want only the sent row %s", len(page.Newsletters), sentID)
	}
}

func TestListSentByCommittee_ArrayContainment(t *testing.T) {
	repo := newIntegrationRepo(t)

	multi := seed(t, repo, model.StatusSent, []string{"committee-a", "committee-b"}, ts(10))
	bOnly := seed(t, repo, model.StatusSent, []string{"committee-b"}, ts(20))
	seed(t, repo, model.StatusSent, []string{"committee-c"}, ts(30))

	pageA, err := repo.ListSentByCommittee(context.Background(), "committee-a", "")
	if err != nil {
		t.Fatalf("committee-a: %v", err)
	}
	if len(pageA.Newsletters) != 1 || pageA.Newsletters[0].ID != multi {
		t.Fatalf("committee-a: got %d rows, want only %s", len(pageA.Newsletters), multi)
	}

	pageB, err := repo.ListSentByCommittee(context.Background(), "committee-b", "")
	if err != nil {
		t.Fatalf("committee-b: %v", err)
	}
	if len(pageB.Newsletters) != 2 || pageB.Newsletters[0].ID != multi || pageB.Newsletters[1].ID != bOnly {
		t.Fatalf("committee-b: got %v, want [%s %s] (sent_at desc)", ids(pageB.Newsletters), multi, bOnly)
	}
}

func TestListSentByCommittee_PaginatesAcrossEqualSentAt(t *testing.T) {
	repo := newIntegrationRepo(t)

	// defaultListLimit+5 rows sharing ONE sent_at: ordering degenerates to the
	// id tiebreak, so this exercises the tuple cursor exactly where a naive
	// single-column cursor would skip or duplicate rows.
	same := ts(60)
	total := defaultListLimit + 5
	want := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		want[seed(t, repo, model.StatusSent, []string{"committee-a"}, same)] = true
	}

	first, err := repo.ListSentByCommittee(context.Background(), "committee-a", "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first.Newsletters) != defaultListLimit || first.NextPageToken == "" {
		t.Fatalf("page 1: got %d rows (token %q), want %d rows with token", len(first.Newsletters), first.NextPageToken, defaultListLimit)
	}

	second, err := repo.ListSentByCommittee(context.Background(), "committee-a", first.NextPageToken)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(second.Newsletters) != total-defaultListLimit || second.NextPageToken != "" {
		t.Fatalf("page 2: got %d rows (token %q), want %d rows and no token", len(second.Newsletters), second.NextPageToken, total-defaultListLimit)
	}

	seen := make(map[uuid.UUID]bool, total)
	for _, n := range append(first.Newsletters, second.Newsletters...) {
		if seen[n.ID] {
			t.Fatalf("row %s appeared on both pages", n.ID)
		}
		seen[n.ID] = true
		if !want[n.ID] {
			t.Fatalf("unexpected row %s", n.ID)
		}
	}
	if len(seen) != total {
		t.Fatalf("pages covered %d of %d rows", len(seen), total)
	}
}

func TestListSentByCommittee_RejectsMalformedToken(t *testing.T) {
	repo := newIntegrationRepo(t)

	for _, token := range []string{"not-base64!", "e30"} {
		_, err := repo.ListSentByCommittee(context.Background(), "committee-a", token)
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("token %q: got %v, want ErrInvalidRequest", token, err)
		}
	}
}

func ids(rows []*model.Newsletter) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(rows))
	for _, n := range rows {
		out = append(out, n.ID)
	}
	return out
}
