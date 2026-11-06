// Package frontmatter parses and edits YAML frontmatter in Markdown documents.
package frontmatter

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
	"primeradiant.com/evener/agent/internal/lineend"
)

// Document holds the parsed frontmatter metadata and the remaining Markdown body.
type Document struct {
	Meta    map[string]any // parsed YAML frontmatter (nil if none present)
	Body    string         // markdown body after the closing ---
	Framing Framing        // how the input was framed (see Framing)
}

// Framing describes how a document's frontmatter is framed.
type Framing int

const (
	// FramingUnknown is the zero value: no framing was reported, which is what
	// a document Parse returned with an error carries.
	FramingUnknown Framing = iota
	// NoOpeningDelimiter means raw does not open with the frontmatter delimiter.
	NoOpeningDelimiter
	// NoClosingDelimiter means raw opens with the delimiter but the closing
	// delimiter line never follows.
	NoClosingDelimiter
	// Framed means a complete frontmatter block was found.
	Framed
)

const delimiter = "---\n"

// Split cuts raw into its frontmatter block and the body after the closing
// delimiter, the first line that is exactly "---". It reads raw with its line
// endings normalized and a single leading UTF-8 byte-order mark dropped, so a
// CRLF or BOM-prefixed document has the same frontmatter, and block and body
// end their lines in "\n". A second byte-order mark is content, not framing.
// ok is false, with body set to all of the normalized raw, when raw has no
// complete frontmatter.
func Split(raw string) (block, body string, ok bool) {
	block, body, framing := split(raw)
	return block, body, framing == Framed
}

// split is Split's body, also reporting which framing it found, so Parse does
// not have to re-derive the rule.
func split(raw string) (block, body string, framing Framing) {
	raw = strings.TrimPrefix(lineend.Normalize(raw), "\ufeff")
	rest, found := strings.CutPrefix(raw, delimiter)
	if !found {
		return "", raw, NoOpeningDelimiter
	}
	// The closing delimiter starts a line, so "a---\n" ending a value is not
	// one. Searching from the opening delimiter's "\n" lets a delimiter right
	// after it match; end is then where the delimiter starts in rest.
	end := strings.Index(raw[len(delimiter)-1:], "\n"+delimiter)
	if end < 0 {
		// Opening delimiter but no closing delimiter: treat as no frontmatter.
		return "", raw, NoClosingDelimiter
	}
	return rest[:end], rest[end+len(delimiter):], Framed
}

// Parse splits a YAML-frontmattered Markdown document into metadata and body,
// as Split reads it. If no frontmatter is present (no leading ---), Meta is
// nil and Body is the full input with its line endings normalized and a leading
// UTF-8 byte-order mark dropped.
func Parse(raw string) (Document, error) {
	yamlStr, body, framing := split(raw)
	if framing != Framed {
		return Document{Body: body, Framing: framing}, nil
	}

	meta := make(map[string]any)
	if strings.TrimSpace(yamlStr) != "" {
		if err := parseBlock(yamlStr, &meta); err != nil {
			return Document{}, fmt.Errorf("parsing frontmatter YAML: %w", err)
		}
	}

	return Document{Meta: meta, Body: body, Framing: Framed}, nil
}

// parseBlock decodes block as exactly one YAML document, requiring that
// nothing follows it but the document-end marker, comments and blank lines.
// yaml.Unmarshal reads only the first document and silently ignores the rest,
// so a control field written after "..." would vanish; a document end that
// closes the block is harmless, anything after it is an error.
func parseBlock(block string, meta *map[string]any) error {
	decoder := yaml.NewDecoder(strings.NewReader(block))
	var tree yaml.Node
	if err := decoder.Decode(&tree); err != nil {
		return err
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return fmt.Errorf("frontmatter has content after its YAML document end: %w", err)
	default:
		return errors.New("frontmatter has content after its YAML document end")
	}
	if len(tree.Content) == 0 {
		// A comment-only block leaves meta empty, as yaml.Unmarshal did.
		return nil
	}
	// tree is a document node; its first child is the mapping that decodes into
	// map[string]any, so that mapping's keys are compared as strings.
	if err := mappingKeysUnique(tree.Content[0], true); err != nil {
		return err
	}
	return tree.Decode(meta)
}

// mappingKeysUnique reports an error when a mapping in node declares the same
// key twice, comparing keys as the decode does: aliases resolved to the scalar
// they name, strings at the root where meta is map[string]any, and decoded
// scalar values in nested mappings. yaml.v3's own duplicate check compares an
// alias node's anchor name instead of what it resolves to, so
// "*key: 'false'" beside "user-invocable: true" slips through and the decode
// silently keeps one value. Merge keys are just keys here; only keys written
// in this mapping are compared, so a merged mapping's keys do not count as
// duplicates of the explicit ones that override them.
func mappingKeysUnique(node *yaml.Node, stringKeys bool) error {
	if node.Kind == yaml.MappingNode {
		seen := make(map[any]*yaml.Node, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind == yaml.AliasNode {
				key = key.Alias
			}
			if key == nil || key.Kind != yaml.ScalarNode {
				// The decoder rejects keys that are not scalars.
				continue
			}
			var decoded any
			if stringKeys {
				var text string
				if err := key.Decode(&text); err != nil {
					return err
				}
				decoded = text
			} else if err := key.Decode(&decoded); err != nil {
				return err
			}
			if first, ok := seen[decoded]; ok {
				return fmt.Errorf("frontmatter mapping key %v is already defined at line %d", decoded, first.Line)
			}
			seen[decoded] = key
		}
	}
	for _, child := range node.Content {
		if err := mappingKeysUnique(child, false); err != nil {
			return err
		}
	}
	return nil
}
