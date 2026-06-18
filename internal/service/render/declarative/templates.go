// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
)

// Templates holds the parsed declarative templates needed for one render: the
// wrapper(s) and the block-template map keyed by block_type (filename without
// extension). Under the unified model, blocks/ and bricks/ load into the same
// Blocks map — a filename is a block_type regardless of which directory it
// came from.
type Templates struct {
	// Wrappers is keyed by wrapper key (filename without extension). The
	// "default" wrapper is used when a Layout's WrapperKey is empty.
	Wrappers map[string]string
	// Blocks is keyed by block_type. Values are the raw template source
	// (SCHEMA comment included; it is stripped at parse time).
	Blocks map[string]string
}

// schemaComment matches a leading "<!-- SCHEMA: ... -->" comment. SCHEMA is
// editor metadata only and is irrelevant to rendering, so it is stripped
// before the markup is parsed.
var schemaComment = regexp.MustCompile(`(?s)<!--\s*SCHEMA:.*?-->`)

// stripSchema removes the SCHEMA metadata comment from a template body.
func stripSchema(src string) string {
	return schemaComment.ReplaceAllString(src, "")
}

// wrapper returns the wrapper template for the given key, falling back to
// "default" when key is empty.
func (t Templates) wrapper(key string) (string, error) {
	if key == "" {
		key = "default"
	}
	w, ok := t.Wrappers[key]
	if !ok {
		return "", fmt.Errorf("declarative: wrapper %q not found", key)
	}
	return w, nil
}

// block returns the block template for the given block_type.
func (t Templates) block(blockType string) (string, error) {
	b, ok := t.Blocks[blockType]
	if !ok {
		return "", fmt.Errorf("declarative: block template %q not found", blockType)
	}
	return b, nil
}

// LoadTemplates reads a declarative template directory from the filesystem into
// a Templates value. It expects a "wrappers" subdirectory and any of "blocks"
// and "bricks" subdirectories; the latter two load into the same Blocks map
// (unified model). Only files with a ".html" extension are loaded.
//
// It delegates to loadFromFS over os.DirFS(root) so the parsing logic is shared
// with LoadEmbedded.
func LoadTemplates(root string) (Templates, error) {
	return loadFromFS(os.DirFS(root), ".")
}

// loadFromFS reads the declarative templates rooted at root within fsys into a
// Templates value. root is an fs-style (slash-separated) path; pass "." for the
// fs root. It is the shared core behind both LoadTemplates (os.DirFS) and
// LoadEmbedded (the embedded FS).
func loadFromFS(fsys fs.FS, root string) (Templates, error) {
	t := Templates{
		Wrappers: map[string]string{},
		Blocks:   map[string]string{},
	}

	if err := loadDir(fsys, path.Join(root, "wrappers"), t.Wrappers); err != nil {
		return Templates{}, err
	}
	// blocks/ and bricks/ both populate Blocks under the unified model. Both
	// are optional, but at least one must exist.
	loadedAny := false
	for _, sub := range []string{"blocks", "bricks"} {
		dir := path.Join(root, sub)
		if _, err := fs.Stat(fsys, dir); err != nil {
			continue
		}
		if err := loadDir(fsys, dir, t.Blocks); err != nil {
			return Templates{}, err
		}
		loadedAny = true
	}
	if !loadedAny {
		return Templates{}, fmt.Errorf("declarative: no blocks or bricks directory under %q", root)
	}
	if len(t.Wrappers) == 0 {
		return Templates{}, fmt.Errorf("declarative: no wrappers found under %q", root)
	}
	return t, nil
}

// loadDir reads every *.html file in dir (within fsys) into dst, keyed by
// filename without the extension.
func loadDir(fsys fs.FS, dir string, dst map[string]string) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("declarative: read template dir %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		p := path.Join(dir, e.Name())
		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return fmt.Errorf("declarative: read template %q: %w", p, readErr)
		}
		key := strings.TrimSuffix(e.Name(), ".html")
		dst[key] = string(data)
	}
	return nil
}
