// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import "embed"

// embeddedTemplates carries the declarative wrapper and block templates baked
// into the binary at build time. Embedding them means the emitter has its
// templates at runtime with no filesystem dependency — production never reads a
// templates directory off disk.
//
//go:embed templates
var embeddedTemplates embed.FS

// LoadEmbedded parses the templates compiled into the binary via go:embed into
// a Templates value. It is the production entry point for obtaining templates;
// LoadTemplates (filesystem) remains available for tests and local iteration.
func LoadEmbedded() (Templates, error) {
	return loadFromFS(embeddedTemplates, "templates")
}
