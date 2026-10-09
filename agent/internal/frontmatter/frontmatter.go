// Package frontmatter parses YAML frontmatter from Markdown documents.
package frontmatter

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// closingLineDelimiter returns the byte offset in rest at which a closing "---"
// line begins, or -1 when rest holds none. The delimiter must start a line —
// the very start of rest (empty frontmatter) or right after a newline — so a
// "---\n" glued to the end of a value or indented inside a block scalar is not
// mistaken for the close.
func closingLineDelimiter(rest string) int {
	offset := 0
	for {
		at := strings.Index(rest[offset:], delimiter)
		if at < 0 {
			return -1
		}
		at += offset
		if at == 0 || rest[at-1] == '\n' {
			return at
		}
		offset = at + 1
	}
}

// Document holds the parsed frontmatter metadata and the remaining Markdown body.
type Document struct {
	Meta map[string]any // parsed YAML frontmatter (nil if none present)
	Body string         // markdown body after the closing ---
}

const delimiter = "---\n"

// Parse splits a YAML-frontmattered Markdown document into metadata and body.
// If no frontmatter is present (no leading ---), Meta is nil and Body is the full input.
// The closing delimiter must be a whole "---" line: a "---" glued to the end of
// a value or indented inside a block scalar does not close the block.
func Parse(raw string) (Document, error) {
	if !strings.HasPrefix(raw, delimiter) {
		return Document{Body: raw}, nil
	}

	rest := raw[len(delimiter):]
	closing := closingLineDelimiter(rest)
	if closing < 0 {
		// Opening delimiter but no closing delimiter — treat as no frontmatter.
		return Document{Body: raw}, nil
	}
	yamlStr := rest[:closing]
	body := rest[closing+len(delimiter):]

	meta := make(map[string]any)
	if strings.TrimSpace(yamlStr) != "" {
		if err := yaml.Unmarshal([]byte(yamlStr), &meta); err != nil {
			return Document{}, fmt.Errorf("parsing frontmatter YAML: %w", err)
		}
	}

	return Document{Meta: meta, Body: body}, nil
}
