// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
)

// introLayout builds a minimal valid layout: a single intro_paragraph block
// carrying distinctive text we can assert on in the rendered HTML.
func introLayout(text string) *declarative.Layout {
	return &declarative.Layout{
		Blocks: []declarative.Block{
			{
				BlockType: "intro_paragraph",
				Content:   map[string]any{"text": text},
			},
		},
	}
}

func TestCreateDraft_WithLayout_PersistsLayoutAndDerivedHTML(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo)

	const marker = "Welcome to the June edition"
	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID: "p1",
		Subject:    "June news",
		// body_html intentionally empty: the layout is the source of truth and
		// should drive the derived HTML.
		BodyHTML:      "",
		BodyLayout:    introLayout(marker),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	// The raw layout must be persisted and round-trip back to the same block.
	if len(draft.BodyLayout) == 0 {
		t.Fatal("expected BodyLayout to be persisted, got empty")
	}
	var stored declarative.Layout
	if err := json.Unmarshal(draft.BodyLayout, &stored); err != nil {
		t.Fatalf("unmarshal stored layout: %v", err)
	}
	if len(stored.Blocks) != 1 || stored.Blocks[0].BlockType != "intro_paragraph" {
		t.Fatalf("stored layout did not round-trip: %+v", stored)
	}

	// The derived body_html must contain the block content and the unsubscribe
	// placeholder so the send path can substitute the per-recipient URL later.
	if !strings.Contains(draft.BodyHTML, marker) {
		t.Errorf("derived body_html missing block content %q; got:\n%s", marker, draft.BodyHTML)
	}
	if !strings.Contains(draft.BodyHTML, UnsubscribeURLPlaceholder) {
		t.Errorf("derived body_html missing unsubscribe placeholder %q", UnsubscribeURLPlaceholder)
	}
}

func TestCreateDraft_WithBodyHTMLOnly_Unchanged(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo)

	const html = "<p>Hand-written body</p>"
	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "Legacy",
		BodyHTML:      html,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if draft.BodyHTML != html {
		t.Errorf("body_html mutated on legacy path: got %q want %q", draft.BodyHTML, html)
	}
	if len(draft.BodyLayout) != 0 {
		t.Errorf("expected no BodyLayout on legacy path, got %s", draft.BodyLayout)
	}
}

func TestCreateDraft_UnrenderableLayout_ErrorsAndPersistsNothing(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo)

	bad := &declarative.Layout{
		Blocks: []declarative.Block{{BlockType: "no_such_block_type"}},
	}
	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "Broken",
		BodyLayout:    bad,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err == nil {
		t.Fatal("expected error for unrenderable layout, got nil")
	}
	if !IsUnprocessableError(err) {
		t.Errorf("expected unprocessable (422) error, got %v", err)
	}
	repo.mu.Lock()
	n := len(repo.drafts)
	repo.mu.Unlock()
	if n != 0 {
		t.Errorf("expected nothing persisted, got %d drafts", n)
	}
}

func TestUpdateDraft_WithLayout_PersistsLayoutAndDerivedHTML(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo)

	existing := &model.Newsletter{
		ID:            uuid.New(),
		ProjectUID:    "p1",
		Subject:       "Old",
		BodyHTML:      "<p>old</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		Status:        model.StatusDraft,
		Version:       1,
	}
	repo.drafts[existing.ID] = existing

	const marker = "Updated edition content"
	updated, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
		ID:              existing.ID,
		ExpectedVersion: 1,
		Subject:         "New",
		BodyLayout:      introLayout(marker),
		EDReplyEmail:    "ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if len(updated.BodyLayout) == 0 {
		t.Fatal("expected BodyLayout persisted on update")
	}
	if !strings.Contains(updated.BodyHTML, marker) {
		t.Errorf("derived body_html missing %q; got:\n%s", marker, updated.BodyHTML)
	}
	if !strings.Contains(updated.BodyHTML, UnsubscribeURLPlaceholder) {
		t.Errorf("derived body_html missing unsubscribe placeholder")
	}
}

func TestUpdateDraft_OmittingLayout_KeepsExistingLayout(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo)

	// A saved LAYOUT newsletter: body_layout present, body_html is the derived
	// emitter document (wrapper + blocks + %%…%% sentinels).
	const derivedHTML = "<html><body>derived emitter document</body></html>"
	existing := &model.Newsletter{
		ID:            uuid.New(),
		ProjectUID:    "p1",
		Subject:       "Old",
		BodyHTML:      derivedHTML,
		BodyLayout:    json.RawMessage(`{"wrapper_key":"default","blocks":[{"block_type":"intro_paragraph","content":{"text":"hi"}}]}`),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		Status:        model.StatusDraft,
		Version:       1,
	}
	repo.drafts[existing.ID] = existing

	// Update WITHOUT body_layout — e.g. an autosave that round-trips only body_html.
	// The stored layout must be KEPT, not silently cleared (which would route the
	// next send down the legacy path and re-wrap the full emitter document).
	updated, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
		ID:              existing.ID,
		ExpectedVersion: 1,
		Subject:         "New subject",
		BodyHTML:        "<p>just html</p>",
		EDReplyEmail:    "ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if len(updated.BodyLayout) == 0 {
		t.Fatal("expected the existing body_layout to be kept when the update omits it")
	}
	if updated.BodyHTML != derivedHTML {
		t.Errorf("expected the derived body_html kept, not overwritten; got %q", updated.BodyHTML)
	}
	if updated.Subject != "New subject" {
		t.Errorf("expected other fields to still update; subject = %q", updated.Subject)
	}
}

func TestUpdateDraft_UnrenderableLayout_ErrorsAndPersistsNothing(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo)

	existing := &model.Newsletter{
		ID:            uuid.New(),
		ProjectUID:    "p1",
		Subject:       "Old",
		BodyHTML:      "<p>original body</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		Status:        model.StatusDraft,
		Version:       1,
	}
	repo.drafts[existing.ID] = existing

	_, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
		ID:              existing.ID,
		ExpectedVersion: 1,
		Subject:         "New",
		BodyLayout:      &declarative.Layout{Blocks: []declarative.Block{{BlockType: "nope"}}},
		EDReplyEmail:    "ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if err == nil {
		t.Fatal("expected error for unrenderable layout")
	}
	if !IsUnprocessableError(err) {
		t.Errorf("expected unprocessable (422) error, got %v", err)
	}
	// The existing row must be untouched: the update is only applied after a
	// successful render, so a render failure leaves the loaded row unmodified.
	if existing.BodyHTML != "<p>original body</p>" || existing.Subject != "Old" {
		t.Errorf("existing draft mutated despite render failure: %+v", existing)
	}
}
