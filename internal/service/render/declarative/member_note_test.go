// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"strings"
	"testing"
)

// TestMemberSectionClosingNote covers the optional closing note on the
// aaif-member list sections: it renders after the items and keeps its links,
// and an empty note renders nothing (lfx-self-serve#4357).
func TestMemberSectionClosingNote(t *testing.T) {
	templates, err := LoadEmbeddedTemplateCached("aaif-member")
	if err != nil {
		t.Fatal(err)
	}
	item := map[string]any{"headline": "An item", "body": "<p>Item text</p><ul><li>sub one</li><li>sub two</li></ul>"}
	render := func(content map[string]any) string {
		t.Helper()
		layout := Layout{TemplateKey: "aaif-member", Blocks: []Block{{BlockType: "member_innovation", Content: content}}}
		out, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	with := render(map[string]any{"items": []any{item}, "note": `<p>Have an innovation to share? <a href="mailto:news@example.com">Send it to us</a>.</p>`})
	itemAt, noteAt := strings.Index(with, "Item text"), strings.Index(with, "Have an innovation to share")
	if itemAt < 0 || noteAt < 0 || noteAt < itemAt {
		t.Fatalf("closing note missing or before the items (item at %d, note at %d)", itemAt, noteAt)
	}
	if !strings.Contains(with, "mailto:news@example.com") {
		t.Error("closing note lost its link")
	}
	if !strings.Contains(with, "sub two") {
		t.Error("an item's sub-bullets did not render")
	}

	without := render(map[string]any{"items": []any{item}})
	if strings.Contains(without, "Have an innovation to share") {
		t.Error("an empty closing note rendered")
	}
}
