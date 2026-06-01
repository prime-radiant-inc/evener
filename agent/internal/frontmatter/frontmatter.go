// Package frontmatter parses YAML frontmatter from Markdown documents.
package frontmatter

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document holds the parsed frontmatter metadata and the remaining Markdown body.
type Document struct {
	Meta map[string]any // parsed YAML frontmatter (nil if none present)
	Body string         // markdown body after the closing ---
}

const delimiter = "---\n"

// Parse splits a YAML-frontmattered Markdown document into metadata and body.
// If no frontmatter is present (no leading ---), Meta is nil and Body is the full input.
func Parse(raw string) (Document, error) {
	if !strings.HasPrefix(raw, delimiter) {
		return Document{Body: raw}, nil
	}

	rest := raw[len(delimiter):]
	idx := strings.Index(rest, delimiter)
	if idx < 0 {
		// Opening delimiter but no closing delimiter — treat as no frontmatter.
		return Document{Body: raw}, nil
	}

	yamlStr := rest[:idx]
	body := rest[idx+len(delimiter):]

	meta := make(map[string]any)
	if strings.TrimSpace(yamlStr) != "" {
		if err := yaml.Unmarshal([]byte(yamlStr), &meta); err != nil {
			return Document{}, fmt.Errorf("parsing frontmatter YAML: %w", err)
		}
	}

	return Document{Meta: meta, Body: body}, nil
}
