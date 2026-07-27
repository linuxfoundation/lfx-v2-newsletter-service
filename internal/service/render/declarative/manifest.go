// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// ManifestEntry is one palette entry in the editor manifest. The JSON shape
// mirrors NewsletterBlockManifestEntry in @lfx-one/shared, which is what the
// block composer consumes.
type ManifestEntry struct {
	BlockType   string          `json:"block_type"`
	Label       string          `json:"label"`
	Category    string          `json:"category"`
	Schema      json.RawMessage `json:"schema"`
	IsContainer bool            `json:"is_container,omitempty"`
	// Template is the raw element tree with all HTML comments (license,
	// CATEGORY, SCHEMA) stripped — the client-side renderer walks it to draw
	// the canvas preview.
	Template string `json:"template"`
}

// Manifest is the editor manifest for one template set: the palette of block
// types plus the page-chrome wrapper. The JSON shape mirrors
// NewsletterTemplateManifest in @lfx-one/shared.
type Manifest struct {
	WrapperKey string          `json:"wrapper_key"`
	Blocks     []ManifestEntry `json:"blocks"`
	Wrapper    string          `json:"wrapper,omitempty"`
}

var (
	// schemaCapture extracts the JSON body of the leading SCHEMA comment.
	schemaCapture = regexp.MustCompile(`(?s)<!--\s*SCHEMA:\s*(.*?)-->`)
	// categoryCapture extracts an optional CATEGORY comment. Blocks default to
	// the "block" category; platform blocks (e.g. logo_header) override it.
	categoryCapture = regexp.MustCompile(`<!--\s*CATEGORY:\s*([a-z0-9_-]+)\s*-->`)
	// anyComment matches every HTML comment, for template-body stripping.
	anyComment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// buildManifestFromFS reads the template set rooted at root within fsys and
// builds its editor manifest. It walks blocks/ and bricks/ into one palette
// (unified model) and reads wrappers/default.html as the page chrome. Every
// block file must carry a SCHEMA comment — a missing or malformed one is a
// packaging defect, not a runtime condition.
func buildManifestFromFS(fsys fs.FS, root string) (Manifest, error) {
	m := Manifest{WrapperKey: "default"}

	for _, sub := range []string{"blocks", "bricks"} {
		dir := path.Join(root, sub)
		if _, err := fs.Stat(fsys, dir); err != nil {
			continue
		}
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return Manifest{}, fmt.Errorf("declarative: read manifest dir %q: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
				continue
			}
			p := path.Join(dir, e.Name())
			src, readErr := fs.ReadFile(fsys, p)
			if readErr != nil {
				return Manifest{}, fmt.Errorf("declarative: read template %q: %w", p, readErr)
			}
			entry, entryErr := buildManifestEntry(strings.TrimSuffix(e.Name(), ".html"), string(src))
			if entryErr != nil {
				return Manifest{}, fmt.Errorf("declarative: %q: %w", p, entryErr)
			}
			m.Blocks = append(m.Blocks, entry)
		}
	}
	if len(m.Blocks) == 0 {
		return Manifest{}, fmt.Errorf("declarative: no manifest blocks under %q", root)
	}
	// Deterministic order so responses are stable across restarts.
	sort.Slice(m.Blocks, func(i, j int) bool { return m.Blocks[i].BlockType < m.Blocks[j].BlockType })

	// The default wrapper is required, matching the render loader's contract
	// (loadFromFS fails a set with no wrappers): a template set without page
	// chrome is a packaging defect, and silently degrading the editor preview
	// while rendering hard-fails would be an inconsistent contract for the
	// same defect.
	wrapperPath := path.Join(root, "wrappers", "default.html")
	wrapperSrc, err := fs.ReadFile(fsys, wrapperPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("declarative: read wrapper %q: %w", wrapperPath, err)
	}
	m.Wrapper = stripAllComments(string(wrapperSrc))

	return m, nil
}

// buildManifestEntry parses one block template source into its palette entry.
func buildManifestEntry(blockType, src string) (ManifestEntry, error) {
	schemaMatch := schemaCapture.FindStringSubmatch(src)
	if schemaMatch == nil {
		return ManifestEntry{}, fmt.Errorf("no SCHEMA comment")
	}
	raw := strings.TrimSpace(schemaMatch[1])

	// Validate the schema JSON and detect container blocks (any top-level
	// field declaring type "slot") in one unmarshal.
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return ManifestEntry{}, fmt.Errorf("invalid SCHEMA JSON: %w", err)
	}
	isContainer := false
	for _, v := range fields {
		if f, ok := v.(map[string]any); ok && f["type"] == "slot" {
			isContainer = true
			break
		}
	}

	entry := ManifestEntry{
		BlockType:   blockType,
		Label:       HumanizeKey(blockType),
		Category:    "block",
		Schema:      json.RawMessage(raw),
		IsContainer: isContainer,
		Template:    stripAllComments(src),
	}
	if catMatch := categoryCapture.FindStringSubmatch(src); catMatch != nil {
		entry.Category = catMatch[1]
	}
	return entry, nil
}

// knownAcronyms maps a lowercase segment to its canonical capitalization so
// HumanizeKey renders acronyms correctly (e.g. "AAIF") instead of title-casing
// them ("Aaif"). Extend as new acronyms appear in keys.
var knownAcronyms = map[string]string{
	"aaif": "AAIF",
}

// HumanizeKey turns a snake_case or kebab-case identifier into a Title Case
// label, e.g. "job_of_week" -> "Job Of Week", "aaif-user-community" ->
// "AAIF User Community". Known acronyms (see knownAcronyms) keep their casing.
// Used for block labels and template-set labels.
func HumanizeKey(t string) string {
	words := strings.FieldsFunc(t, func(r rune) bool { return r == '_' || r == '-' })
	for i, w := range words {
		if ac, ok := knownAcronyms[strings.ToLower(w)]; ok {
			words[i] = ac
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// stripAllComments removes every HTML comment from a template source and trims
// the result — license headers, CATEGORY, and SCHEMA metadata are editor and
// packaging concerns, not part of the renderable element tree.
func stripAllComments(src string) string {
	return strings.TrimSpace(anyComment.ReplaceAllString(src, ""))
}
