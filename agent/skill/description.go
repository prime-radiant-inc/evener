package skill

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"primeradiant.com/evener/agent/internal/frontmatter"
)

// parseSkillDocument recovers only an unquoted, single-line description whose
// colon-space can be read as prose without changing the rest of the mapping.
func parseSkillDocument(data []byte) (frontmatter.Document, bool, error) {
	raw := string(data)
	document, err := frontmatter.Parse(raw)
	if err == nil {
		return document, false, nil
	}
	block, body, framed := frontmatter.Split(raw)
	if !framed {
		return document, false, err
	}
	lines := strings.Split(block, "\n")
	candidate := -1
	for i, line := range lines {
		value, found := strings.CutPrefix(line, "description:")
		if !found || len(value) == 0 || (value[0] != ' ' && value[0] != '\t') {
			continue
		}
		if candidate >= 0 {
			return document, false, err
		}
		candidate = i
		value = strings.Trim(value, " \t")
		if value == "" || strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(value[0])) {
			return document, false, err
		}
		comment := ""
		for j := 1; j < len(value); j++ {
			if value[j] == '#' && (value[j-1] == ' ' || value[j-1] == '\t') {
				comment, value = value[j:], strings.TrimRight(value[:j], " \t")
				break
			}
		}
		if !strings.Contains(value, ": ") {
			return document, false, err
		}
		// JSON quoting must not repair invalid UTF-8, escape forbidden control
		// characters, or fold YAML's other line separators into a single line.
		if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool {
			return (unicode.IsControl(r) && r != '\t') || r == '\u2028' || r == '\u2029'
		}) {
			return document, false, err
		}
		// An indented next entry might continue the scalar. Never consume or
		// reinterpret it as part of a one-line recovery.
		for _, next := range lines[i+1:] {
			trimmed := strings.TrimSpace(next)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if next[0] == ' ' || next[0] == '\t' {
				return document, false, err
			}
			break
		}
		quoted, _ := json.Marshal(value)
		lines[i] = "description: " + string(quoted) + " " + comment
	}
	if candidate < 0 {
		return document, false, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines, "\n")))
	var tree yaml.Node
	if decodeErr := decoder.Decode(&tree); decodeErr != nil || len(tree.Content) != 1 {
		return document, false, err
	}
	root := tree.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return document, false, err
	}
	// Column zero alone does not imply a top-level key inside flow collections.
	topLevelDescription := false
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "description" && key.Line == candidate+1 {
			topLevelDescription = true
		}
	}
	if !topLevelDescription {
		return document, false, err
	}
	// Unmarshal accepts the first document without checking its suffix. Require
	// EOF so a terminator cannot hide controls or malformed trailing content.
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return document, false, err
	}
	if !recoveryMappingKeysUnique(root, true) {
		return document, false, err
	}
	var meta map[string]any
	if decodeErr := tree.Decode(&meta); decodeErr != nil {
		return document, false, err
	}
	return frontmatter.Document{Meta: meta, Body: body, Framing: frontmatter.Framed}, true, nil
}

// Compare decoded keys without rewriting nodes, which could turn an alias into
// a merge directive. The root decodes to map[string]any, while nested mappings
// retain their scalar key types. Visit contained nodes only, since aliases can
// recurse.
func recoveryMappingKeysUnique(node *yaml.Node, stringKeys bool) bool {
	if node.Kind == yaml.MappingNode {
		seen := make(map[any]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind == yaml.AliasNode {
				key = key.Alias
			}
			if key == nil || key.Kind != yaml.ScalarNode {
				return false
			}
			var decoded any
			if stringKeys {
				var text string
				if err := key.Decode(&text); err != nil {
					return false
				}
				decoded = text
			} else if err := key.Decode(&decoded); err != nil {
				return false
			}
			if seen[decoded] {
				return false
			}
			seen[decoded] = true
		}
	}
	for _, child := range node.Content {
		if !recoveryMappingKeysUnique(child, false) {
			return false
		}
	}
	return true
}
