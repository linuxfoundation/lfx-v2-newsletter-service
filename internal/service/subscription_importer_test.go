// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// fakeSubscriptionRepo is an in-memory port.SubscriptionRepository, keyed by
// (list_id, email), that mimics the real repository's ON CONFLICT DO NOTHING
// semantics: a row already present is never overwritten.
type fakeSubscriptionRepo struct {
	rows map[string]map[string]model.Subscription // listID -> email -> row
	// batches records the size of each call to ImportBatch, in order, so
	// tests can assert on batching behavior.
	batches []int
}

func newFakeSubscriptionRepo() *fakeSubscriptionRepo {
	return &fakeSubscriptionRepo{rows: make(map[string]map[string]model.Subscription)}
}

func (f *fakeSubscriptionRepo) ImportBatch(_ context.Context, listID string, rows []model.Subscription) (int, error) {
	f.batches = append(f.batches, len(rows))
	byEmail, ok := f.rows[listID]
	if !ok {
		byEmail = make(map[string]model.Subscription)
		f.rows[listID] = byEmail
	}
	inserted := 0
	for _, row := range rows {
		if _, exists := byEmail[row.Email]; exists {
			continue
		}
		byEmail[row.Email] = row
		inserted++
	}
	return inserted, nil
}

func TestImport_AcceptsValidRows(t *testing.T) {
	csv := "email,subscribed\n" +
		"a@example.com,true\n" +
		"b@example.com,false\n"
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	var rejected bytes.Buffer
	summary, err := imp.Import(context.Background(), strings.NewReader(csv), &rejected, ImportOptions{ListID: model.ListAAIFUserCommunity})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if summary.TotalRows != 2 || summary.Valid != 2 || summary.Invalid != 0 {
		t.Fatalf("summary = %+v, want TotalRows=2 Valid=2 Invalid=0", summary)
	}
	if summary.Inserted != 2 || summary.AlreadyPresent != 0 {
		t.Fatalf("summary = %+v, want Inserted=2 AlreadyPresent=0", summary)
	}

	got := repo.rows[model.ListAAIFUserCommunity]
	if !got["a@example.com"].Subscribed {
		t.Errorf("a@example.com subscribed = false, want true")
	}
	if got["b@example.com"].Subscribed {
		t.Errorf("b@example.com subscribed = true, want false")
	}
}

func TestImport_RejectsMalformedEmail(t *testing.T) {
	csv := "email,subscribed\n" +
		"not-an-email,true\n" +
		"good@example.com,true\n"
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	var rejected bytes.Buffer
	summary, err := imp.Import(context.Background(), strings.NewReader(csv), &rejected, ImportOptions{ListID: model.ListAAIFUserCommunity})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if summary.Valid != 1 || summary.Invalid != 1 {
		t.Fatalf("summary = %+v, want Valid=1 Invalid=1", summary)
	}
	if !strings.Contains(rejected.String(), "not-an-email") {
		t.Errorf("rejected CSV = %q, want it to contain the malformed row", rejected.String())
	}
	if !strings.Contains(rejected.String(), "reason") {
		t.Errorf("rejected CSV = %q, want a reason header/column", rejected.String())
	}
}

func TestImport_RejectsUnrecognizedSubscribedValue(t *testing.T) {
	csv := "email,subscribed\n" +
		"a@example.com,maybe\n"
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	var rejected bytes.Buffer
	summary, err := imp.Import(context.Background(), strings.NewReader(csv), &rejected, ImportOptions{ListID: model.ListAAIFUserCommunity})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if summary.Invalid != 1 || summary.Valid != 0 {
		t.Fatalf("summary = %+v, want Invalid=1 Valid=0", summary)
	}
}

func TestImport_MissingSubscribedColumnFails(t *testing.T) {
	// The subscribed column must round-trip Gatewaze's own opt-out state
	// verbatim; this importer refuses to guess it by defaulting to true.
	csv := "email\n" +
		"a@example.com\n"
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	var rejected bytes.Buffer
	_, err := imp.Import(context.Background(), strings.NewReader(csv), &rejected, ImportOptions{ListID: model.ListAAIFUserCommunity})
	if err == nil {
		t.Fatal("Import: want error for missing subscribed column, got nil")
	}
}

func TestImport_DedupesCaseInsensitiveEmailLastOccurrenceWins(t *testing.T) {
	csv := "email,subscribed\n" +
		"Dup@Example.com,true\n" +
		"other@example.com,true\n" +
		"dup@example.com,false\n"
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	var rejected bytes.Buffer
	summary, err := imp.Import(context.Background(), strings.NewReader(csv), &rejected, ImportOptions{ListID: model.ListAAIFUserCommunity})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if summary.TotalRows != 3 || summary.Valid != 3 {
		t.Fatalf("summary = %+v, want TotalRows=3 Valid=3", summary)
	}
	if summary.DedupedInFile != 1 {
		t.Fatalf("summary.DedupedInFile = %d, want 1", summary.DedupedInFile)
	}
	if summary.Inserted != 2 {
		t.Fatalf("summary.Inserted = %d, want 2 (dup@example.com counted once)", summary.Inserted)
	}

	got := repo.rows[model.ListAAIFUserCommunity]["dup@example.com"]
	if got.Subscribed {
		t.Errorf("dup@example.com subscribed = true, want false (last occurrence must win)")
	}
}

func TestImport_AlreadyPresentRowsAreNotOverwritten(t *testing.T) {
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	first := "email,subscribed\n" + "a@example.com,false\n"
	if _, err := imp.Import(context.Background(), strings.NewReader(first), &bytes.Buffer{}, ImportOptions{ListID: model.ListAAIFUserCommunity}); err != nil {
		t.Fatalf("first Import: %v", err)
	}

	second := "email,subscribed\n" + "a@example.com,true\n"
	summary, err := imp.Import(context.Background(), strings.NewReader(second), &bytes.Buffer{}, ImportOptions{ListID: model.ListAAIFUserCommunity})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if summary.Inserted != 0 || summary.AlreadyPresent != 1 {
		t.Fatalf("summary = %+v, want Inserted=0 AlreadyPresent=1", summary)
	}
	if repo.rows[model.ListAAIFUserCommunity]["a@example.com"].Subscribed {
		t.Errorf("subscribed = true, want false (a re-import must not resubscribe an opted-out row)")
	}
}

func TestImport_BatchesRows(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("email,subscribed\n")
	for i := 0; i < 5; i++ {
		csv.WriteString("user")
		csv.WriteString(strings.Repeat("x", i+1))
		csv.WriteString("@example.com,true\n")
	}
	repo := newFakeSubscriptionRepo()
	imp := NewSubscriptionImporter(repo)

	summary, err := imp.Import(context.Background(), strings.NewReader(csv.String()), &bytes.Buffer{}, ImportOptions{ListID: model.ListAAIFUserCommunity, BatchSize: 2})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if summary.Valid != 5 || summary.Inserted != 5 {
		t.Fatalf("summary = %+v, want Valid=5 Inserted=5", summary)
	}
	if want := []int{2, 2, 1}; !equalInts(repo.batches, want) {
		t.Fatalf("batches = %v, want %v", repo.batches, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
