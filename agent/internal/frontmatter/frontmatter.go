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

// Split cuts raw into its frontmatter block and the body after the closing
// delimiter. ok is false, with body set to all of raw, when raw has no
// complete frontmatter.
func Split(raw string) (block, body string, ok bool) {
	rest, found := strings.CutPrefix(raw, delimiter)
	if !found {
		return "", raw, false
	}
	block, body, ok = strings.Cut(rest, delimiter)
	if !ok {
		// Opening delimiter but no closing delimiter: treat as no frontmatter.
		return "", raw, false
	}
	return block, body, true
}

// Parse splits a YAML-frontmattered Markdown document into metadata and body.
// If no frontmatter is present (no leading ---), Meta is nil and Body is the full input.
func Parse(raw string) (Document, error) {
	yamlStr, body, found := Split(raw)
	if !found {
		return Document{Body: raw}, nil
	}

	meta := make(map[string]any)
	if strings.TrimSpace(yamlStr) != "" {
		if err := yaml.Unmarshal([]byte(yamlStr), &meta); err != nil {
			return Document{}, fmt.Errorf("parsing frontmatter YAML: %w", err)
		}
	}

	return Document{Meta: meta, Body: body}, nil
}
