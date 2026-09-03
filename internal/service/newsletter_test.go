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

// TestCreateDraft_LayoutContentWithReservedPlaceholder_Rejected proves a
// writer-controlled block field cannot smuggle a send-time sentinel token
// (e.g. into an <Img src="...">) — which bind.go's scheme gate would let
// through unmodified as a whole-value sentinel match, and which
// substitutePlaceholders would then globally swap for the recipient's real
// per-recipient URL at send time, auto-firing an unsubscribe when the
// recipient's mail client loads the image.
func TestCreateDraft_LayoutContentWithReservedPlaceholder_Rejected(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	layout := &declarative.Layout{
		Blocks: []declarative.Block{
			{
				BlockType: "logo_header",
				Content:   map[string]any{"image_url": UnsubscribeURLPlaceholder},
			},
		},
	}
	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "Malicious image src",
		BodyLayout:    layout,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err == nil {
		t.Fatal("expected error for reserved placeholder in block content, got nil")
	}
	if !IsValidationError(err) {
		t.Errorf("expected validation (400) error, got %v", err)
	}
	repo.mu.Lock()
	n := len(repo.drafts)
	repo.mu.Unlock()
	if n != 0 {
		t.Errorf("expected nothing persisted, got %d drafts", n)
	}
}

// TestCreateDraft_LayoutContentWithSendScopeSentinel_Rejected proves the guard
// also rejects the send-SCOPE sentinels (%%SENDER_NAME%%/%%PROJECT_NAME%%), not
// just the per-recipient URL sentinels: substituteSendScope globally substitutes
// them too, and a whole-value token in a URL field bypasses bindAttrs' scheme
// gate, so writer content carrying one would be rewritten after the check.
func TestCreateDraft_LayoutContentWithSendScopeSentinel_Rejected(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	layout := &declarative.Layout{
		Blocks: []declarative.Block{
			{
				BlockType: "logo_header",
				Content:   map[string]any{"link": ProjectNamePlaceholder},
			},
		},
	}
	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "Send-scope sentinel in a URL field",
		BodyLayout:    layout,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err == nil {
		t.Fatal("expected error for send-scope sentinel in block content, got nil")
	}
	if !IsValidationError(err) {
		t.Errorf("expected validation (400) error, got %v", err)
	}
}

// TestCreateDraft_LayoutContentNestedArrayWithReservedPlaceholder_Rejected
// proves the reserved-placeholder check also inspects each= array items
// (bind.go's lookupSlice resolves each= against []any elements holding
// map[string]any, not just top-level string fields), so a writer cannot
// smuggle a sentinel into a nested field like news_items[].source_url.
func TestCreateDraft_LayoutContentNestedArrayWithReservedPlaceholder_Rejected(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	layout := &declarative.Layout{
		Blocks: []declarative.Block{
			{
				BlockType: "news_items",
				Content: map[string]any{
					"items": []any{
						map[string]any{
							"headline":   "Item",
							"source_url": UnsubscribeURLPlaceholder,
						},
					},
				},
			},
		},
	}
	_, err := svc.CreateDraft(context.Background(), CreateDraftInput{
		ProjectUID:    "p1",
		Subject:       "Malicious nested array field",
		BodyLayout:    layout,
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "user1",
	})
	if err == nil {
		t.Fatal("expected error for reserved placeholder in nested array content, got nil")
	}
	if !IsValidationError(err) {
		t.Errorf("expected validation (400) error, got %v", err)
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

	const marker = "Hello from the selected library"
	layout := introLayout(marker)
	layout.TemplateKey = "aaif-user-community" // intro_paragraph exists in this set

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
	if stored.TemplateKey != "aaif-user-community" {
		t.Errorf("template_key did not round-trip: got %q, want %q", stored.TemplateKey, "aaif-user-community")
	}
	if !strings.Contains(draft.BodyHTML, marker) {
		t.Errorf("derived body_html missing block content %q; got:\n%s", marker, draft.BodyHTML)
	}
}

// TestCreateDraft_TemplateKey_WhitespacePadded_PersistsTrimmed proves a
// whitespace-padded template_key persists trimmed, matching the key the
// render actually resolved against — otherwise the padded value would persist
// as-is and fail to match any catalog key on a later reload.
func TestCreateDraft_TemplateKey_WhitespacePadded_PersistsTrimmed(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	layout := introLayout("Padded key")
	layout.TemplateKey = "  aaif-user-community  "

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
	if stored.TemplateKey != "aaif-user-community" {
		t.Errorf("template_key did not persist trimmed: got %q, want %q", stored.TemplateKey, "aaif-user-community")
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

	// The manage sentinel is now bound at render-on-write (My Newsletters is a
	// standard archive link); the send path substitutes it to the Self-Serve
	// archive URL, so it must be present with the "My Newsletters" label.
	for _, want := range []string{SenderNamePlaceholder, ProjectNamePlaceholder, "Delivered by", ManageSubscriptionsURLPlaceholder, "My Newsletters"} {
		if !strings.Contains(draft.BodyHTML, want) {
			t.Errorf("derived body_html missing %q", want)
		}
	}
	// The manage link is the read-only archive, never the old "Manage your email
	// preferences" copy that aliased the one-click unsubscribe URL.
	if strings.Contains(draft.BodyHTML, "Manage your email preferences") {
		t.Errorf("derived body_html must not contain %q", "Manage your email preferences")
	}
}

// TestRenderLayout_UnsubscribeDisabled_RendersReplyFallback proves that with
// the unsubscribe service unconfigured, the wrapper's opt-out row renders the
// reply-based fallback copy (sendUnsubFooterMode(false) ->
// unsubFooterReplyFallback) instead of a working link — never omitting the
// opt-out row entirely.
func TestRenderLayout_UnsubscribeDisabled_RendersReplyFallback(t *testing.T) {
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
	if !strings.Contains(draft.BodyHTML, unsubscribeReplyFallbackText) {
		t.Errorf("unsubscribe disabled: derived body_html must contain the reply fallback copy %q; got:\n%s", unsubscribeReplyFallbackText, draft.BodyHTML)
	}
	// The rest of the footer still renders.
	if !strings.Contains(draft.BodyHTML, "Delivered by") {
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

// TestUpdateDraft_ExplicitNullLayout_EmptyBody_ClearsToEmpty pins the discard
// contract: clearing an existing layout draft back to the basic editor with no
// replacement body yet (explicit "body_layout": null AND an empty body_html) is
// allowed and persists as an empty html-only draft. Rejecting it would strand
// the stored layout and silently revert the switch on reload. A create still
// requires a body; only this existing-draft clear is exempt.
func TestUpdateDraft_ExplicitNullLayout_EmptyBody_ClearsToEmpty(t *testing.T) {
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
		BodyHTML:        "",
		BodyLayoutSet:   true,
		BodyLayout:      nil,
		EDReplyEmail:    "ed@example.com",
		CommitteeUIDs:   []string{"c1"},
	})
	if err != nil {
		t.Fatalf("UpdateDraft clearing to empty should succeed, got: %v", err)
	}
	if len(updated.BodyLayout) != 0 {
		t.Errorf("expected BodyLayout cleared, got %s", updated.BodyLayout)
	}
	if updated.BodyHTML != "" {
		t.Errorf("expected empty body_html, got %q", updated.BodyHTML)
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
