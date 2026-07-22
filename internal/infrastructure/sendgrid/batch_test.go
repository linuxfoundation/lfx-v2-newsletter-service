// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSendBatch_BuildsPersonalizations(t *testing.T) {
	store := &fakeStore{}
	var gotBody mailSendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{
		APIKey: "k", DefaultFrom: "newsletter@lfx.aaif.io", DefaultFromName: "AAIF",
		BaseURL: srv.URL, HTTPClient: srv.Client(), Store: store,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	at := time.Unix(1700000000, 0)
	results, err := d.SendBatch(context.Background(), BatchInput{
		Subject: "Weekly", HTML: "<p>Hi %%UNSUB%%</p>", Text: "Hi %%UNSUB%%",
		GroupID: "grp-1", BatchID: "batch-xyz",
		Recipients: []BatchRecipient{
			{To: "a@x.io", SendAt: at, Substitutions: map[string]string{"%%UNSUB%%": "https://u/a"}},
			{To: "b@x.io"}, // no send_at, no substitutions -> send now
		},
	})
	if err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if gotBody.BatchID != "batch-xyz" {
		t.Errorf("batch_id = %q, want batch-xyz", gotBody.BatchID)
	}
	if gotBody.From.Email != "newsletter@lfx.aaif.io" {
		t.Errorf("from = %+v, want the default", gotBody.From)
	}
	if len(gotBody.Personalizations) != 2 {
		t.Fatalf("personalizations = %d, want 2", len(gotBody.Personalizations))
	}
	p0 := gotBody.Personalizations[0]
	if p0.To[0].Email != "a@x.io" {
		t.Errorf("p0 to = %q", p0.To[0].Email)
	}
	if p0.SendAt != at.Unix() {
		t.Errorf("p0 send_at = %d, want %d", p0.SendAt, at.Unix())
	}
	if p0.Substitutions["%%UNSUB%%"] != "https://u/a" {
		t.Errorf("p0 substitutions = %v", p0.Substitutions)
	}
	if p0.CustomArgs[customArgGroupID] != "grp-1" || p0.CustomArgs[customArgEmailID] != results[0].EmailID {
		t.Errorf("p0 custom_args = %v, want group grp-1 + email_id %s", p0.CustomArgs, results[0].EmailID)
	}
	// Second recipient sends immediately (send_at omitted) with no substitutions.
	if gotBody.Personalizations[1].SendAt != 0 {
		t.Errorf("p1 send_at = %d, want 0 (immediate)", gotBody.Personalizations[1].SendAt)
	}
	// Distinct minted email_ids, each recorded to the store.
	if results[0].EmailID == results[1].EmailID {
		t.Errorf("email_ids must be distinct per recipient")
	}
	if len(store.sent) != 2 {
		t.Errorf("store RecordSent calls = %d, want 2", len(store.sent))
	}
}

func TestSendBatch_ChunksAtLimit(t *testing.T) {
	var mu sync.Mutex
	var chunkSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body mailSendRequest
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		chunkSizes = append(chunkSizes, len(body.Personalizations))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io", BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	recipients := make([]BatchRecipient, 0, 1500)
	for i := range 1500 {
		recipients = append(recipients, BatchRecipient{To: fmt.Sprintf("r%d@x.io", i)})
	}
	results, err := d.SendBatch(context.Background(), BatchInput{Subject: "s", Text: "t", Recipients: recipients})
	if err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if len(results) != 1500 {
		t.Errorf("results = %d, want 1500", len(results))
	}
	// 1500 recipients -> two calls of 1000 + 500.
	if len(chunkSizes) != 2 {
		t.Fatalf("mail/send calls = %d, want 2", len(chunkSizes))
	}
	total := 0
	for _, n := range chunkSizes {
		if n > maxPersonalizationsPerCall {
			t.Errorf("a chunk had %d personalizations, over the %d cap", n, maxPersonalizationsPerCall)
		}
		total += n
	}
	if total != 1500 {
		t.Errorf("total personalizations = %d, want 1500", total)
	}
}

func TestSendBatch_Validation(t *testing.T) {
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io"})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.SendBatch(context.Background(), BatchInput{Subject: "s", Recipients: []BatchRecipient{{To: "a@x.io"}}}); err == nil {
		t.Errorf("expected a validation error for an empty body")
	}
	res, err := d.SendBatch(context.Background(), BatchInput{Subject: "s", Text: "t"})
	if err != nil || res != nil {
		t.Errorf("empty recipients should be a no-op (nil, nil), got %v, %v", res, err)
	}
}

func TestSendBatch_ErrorPerChunk(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bad"}]}`))
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io", BaseURL: srv.URL, HTTPClient: srv.Client(), Store: store})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	results, err := d.SendBatch(context.Background(), BatchInput{
		Subject: "s", Text: "t",
		Recipients: []BatchRecipient{{To: "a@x.io"}, {To: "b@x.io"}},
	})
	if err != nil {
		t.Fatalf("SendBatch returned a fatal error, want per-recipient errors: %v", err)
	}
	for _, r := range results {
		if r.Err == nil {
			t.Errorf("recipient %s should carry the chunk error", r.To)
		}
	}
	if len(store.sent) != 0 {
		t.Errorf("a failed chunk must not record sends, got %d", len(store.sent))
	}
}
