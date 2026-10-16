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
	document, err := frontmatter.Parse(string(data))
	if err == nil {
		return document, false, nil
	}
	block, body, framed := frontmatter.Split(string(data))
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
	var meta map[string]any
	if decodeErr := decoder.Decode(&meta); decodeErr != nil {
		return document, false, err
	}
	// Unmarshal accepts the first document without checking its suffix. Require
	// EOF so a terminator cannot hide controls or malformed trailing content.
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return document, false, err
	}
	return frontmatter.Document{Meta: meta, Body: body, Framing: frontmatter.Framed}, true, nil
}
