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
	svc := NewNewsletterService(repo, true)

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
	svc := NewNewsletterService(repo, true)

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
	svc := NewNewsletterService(repo, true)

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

// TestCreateDraft_TemplateKey_RoundTripsAndRendersFromSelectedLibrary asserts a
// layout carrying an explicit template_key persists that key and derives its
// body_html from the named library.
func TestCreateDraft_TemplateKey_RoundTripsAndRendersFromSelectedLibrary(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	const marker = "Hello from the default library"
	layout := introLayout(marker)
	layout.TemplateKey = "default" // intro_paragraph exists in the "default" set

	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "News",
		BodyLayout:    layout,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	var stored declarative.Layout
	if err := json.Unmarshal(draft.BodyLayout, &stored); err != nil {
		t.Fatalf("unmarshal stored layout: %v", err)
	}
	if stored.TemplateKey != "default" {
		t.Errorf("template_key did not round-trip: got %q, want %q", stored.TemplateKey, "default")
	}
	if !strings.Contains(draft.BodyHTML, marker) {
		t.Errorf("derived body_html missing block content %q; got:\n%s", marker, draft.BodyHTML)
	}
}

// TestCreateDraft_TemplateKey_SelectsLibrary proves the key actually routes the
// render library: hidden_gems exists in the default render library
// (aaif-user-community) but NOT in the smaller "default" set, so a layout tagged
// template_key="default" with a hidden_gems block is unrenderable (422) — it is
// bound against the selected library, not the fallback superset.
func TestCreateDraft_TemplateKey_SelectsLibrary(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	layout := &declarative.Layout{
		TemplateKey: "default",
		Blocks:      []declarative.Block{{BlockType: "hidden_gems", Content: map[string]any{}}},
	}
	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "News",
		BodyLayout:    layout,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if !IsUnprocessableError(err) {
		t.Fatalf("expected 422 for a block absent from the selected library, got %v", err)
	}
}

// TestCreateDraft_UnknownTemplateKey_Is422 asserts a layout naming a library the
// binary does not embed is unrenderable (422), not a 500.
func TestCreateDraft_UnknownTemplateKey_Is422(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	layout := introLayout("hi")
	layout.TemplateKey = "no-such-library"
	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "News",
		BodyLayout:    layout,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if !IsUnprocessableError(err) {
		t.Fatalf("expected 422 for unknown template_key, got %v", err)
	}
}

func TestUpdateDraft_WithLayout_PersistsLayoutAndDerivedHTML(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

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
		BodyLayoutSet:   true,
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
	svc := NewNewsletterService(repo, true)

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

	// Update WITHOUT body_layout — e.g. an autosave that round-trips only
	// body_html — while also changing the reply address. The stored layout must
	// be KEPT, not silently cleared (which would route the next send down the
	// legacy path and re-wrap the full emitter document), and body_html must be
	// RE-DERIVED from that layout so the wrapper footer carries the CURRENT
	// reply address rather than the one baked in at the previous save.
	updated, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
		ID:              existing.ID,
		ExpectedVersion: 1,
		Subject:         "New subject",
		BodyHTML:        "<p>just html</p>",
		EDReplyEmail:    "new-ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if len(updated.BodyLayout) == 0 {
		t.Fatal("expected the existing body_layout to be kept when the update omits it")
	}
	if updated.BodyHTML == derivedHTML {
		t.Error("expected body_html re-derived from the stored layout, not the stale copy")
	}
	if !strings.Contains(updated.BodyHTML, "hi") {
		t.Errorf("re-derived body_html must contain the stored layout's block content; got %q", updated.BodyHTML)
	}
	if !strings.Contains(updated.BodyHTML, "mailto:new-ed@example.com") {
		t.Error("re-derived body_html must carry the update's reply address in the footer")
	}
	if strings.Contains(updated.BodyHTML, "mailto:ed@example.com") {
		t.Error("re-derived body_html must not keep the previous reply address")
	}
	if strings.Contains(updated.BodyHTML, "just html") {
		t.Error("the request's body_html must still be ignored on the preserve branch")
	}
	if updated.Subject != "New subject" {
		t.Errorf("expected other fields to still update; subject = %q", updated.Subject)
	}
}

func TestUpdateDraft_UnrenderableLayout_ErrorsAndPersistsNothing(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

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
		BodyLayoutSet:   true,
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

// TestRenderLayout_WrapperRuntimeFields pins the wrapper's runtime-field
// contract after the review fixes: the compliance footer sentinels and the
// bound reply email are present, the manage-preferences link is dropped (no
// preferences surface exists, and aliasing it to the one-click unsubscribe
// would opt recipients out on GET), and no tenant-specific URL leaks from the
// default wrapper.
func TestRenderLayout_WrapperRuntimeFields(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "June news",
		BodyLayout:    introLayout("hello"),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	for _, want := range []string{SenderNamePlaceholder, ProjectNamePlaceholder, "mailto:ed@example.com", "Delivered by LFX"} {
		if !strings.Contains(draft.BodyHTML, want) {
			t.Errorf("derived body_html missing %q", want)
		}
	}
	// The render key on this branch is the aaif-user-community set, whose
	// brand-keyed wrapper legitimately carries its own subscribe URL - the
	// no-tenant-leak invariant for the NEUTRAL wrapper is pinned per key in
	// TestDefaultKeyWrapperIsProjectNeutral instead.
	for _, reject := range []string{"Manage your email preferences", ManageSubscriptionsURLPlaceholder} {
		if strings.Contains(draft.BodyHTML, reject) {
			t.Errorf("derived body_html must not contain %q", reject)
		}
	}
}

// TestRenderLayout_UnsubscribeDisabled_DropsOptOutRow proves the blocking
// review finding is fixed: with the unsubscribe service unconfigured, the
// wrapper's opt-out row is dropped at render time instead of shipping a link
// whose href would substitute to an empty string.
func TestRenderLayout_UnsubscribeDisabled_DropsOptOutRow(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, false)

	draft, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "June news",
		BodyLayout:    introLayout("hello"),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	for _, reject := range []string{UnsubscribeURLPlaceholder, ">Unsubscribe<"} {
		if strings.Contains(draft.BodyHTML, reject) {
			t.Errorf("unsubscribe disabled: derived body_html must not contain %q; got:\n%s", reject, draft.BodyHTML)
		}
	}
	// The rest of the footer still renders.
	if !strings.Contains(draft.BodyHTML, "Delivered by LFX") {
		t.Error("unsubscribe disabled: footer should still carry the Delivered by LFX line")
	}
}

// TestUpdateDraft_ExplicitNullLayout_ClearsToHTMLOnly pins the tri-state
// contract's escape hatch: an update with an explicit "body_layout": null
// (BodyLayoutSet true, BodyLayout nil) clears the stored layout and the
// newsletter becomes html-only, taking body_html from the request.
func TestUpdateDraft_ExplicitNullLayout_ClearsToHTMLOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	created, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "June news",
		BodyLayout:    introLayout("layout content"),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	updated, err := svc.UpdateDraft(context.Background(), "p1", UpdateDraftInput{
		ID:              created.ID,
		ExpectedVersion: created.Version,
		Subject:         "June news",
		BodyHTML:        "<p>plain html now</p>",
		BodyLayoutSet:   true,
		BodyLayout:      nil,
		EDReplyEmail:    "ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if len(updated.BodyLayout) != 0 {
		t.Errorf("expected BodyLayout cleared, got %s", updated.BodyLayout)
	}
	if updated.BodyHTML != "<p>plain html now</p>" {
		t.Errorf("expected request body_html to be taken as-is, got %q", updated.BodyHTML)
	}
}

// TestCreateDraft_OversizedDerivedHTML_Is422 pins the status-code contract:
// an oversized DERIVED (layout-rendered) document is 422 unprocessable, the
// same classification render-preview gives the identical output — not a 400
// blaming a body_html field the request never supplied.
func TestCreateDraft_OversizedDerivedHTML_Is422(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "big",
		BodyLayout:    introLayout(strings.Repeat("x", 100_001)),
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err == nil {
		t.Fatal("expected an error for oversized derived HTML")
	}
	if !IsUnprocessableError(err) {
		t.Errorf("expected 422 unprocessable classification, got: %v", err)
	}
	if IsValidationError(err) {
		t.Errorf("must not classify as 400 invalid_request: %v", err)
	}
}
