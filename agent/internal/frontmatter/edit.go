package frontmatter

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLField encodes one frontmatter line, quoting value as YAML needs.
func YAMLField(key, value string) string {
	encoded, err := yaml.Marshal(map[string]string{key: value})
	if err != nil {
		// a string-to-string map always encodes.
		panic(err)
	}
	return string(encoded)
}

// SetField makes line (an encoded "key: value\n") the page's
// only top-level entry for its key, replacing an existing one and the lines of
// its value, or adding it at the end of the block, or adding a block. Every
// other byte of the page is kept, except that its line endings become "\n"
// (Split). Keys are found as YAML reads them (quoted,
// or with a space before the colon); a block that does not parse gains the
// line at its end and stays as unreadable as it was (Parse
// reads only a mapping). Readable frontmatter that would not read the line
// back as its key, such as a flow mapping or a block
// with a "..." document end, is returned as it is.
func SetField(raw []byte, line string) []byte {
	key, _, _ := strings.Cut(line, ":")
	text := string(raw)
	block, body, ok := Split(text)
	if !ok {
		return []byte("---\n" + line + "---\n" + body)
	}
	keys := keyLines(block)
	lines := slices.Collect(strings.Lines(block))
	var kept []string
	replaced := false
	for i := 0; i < len(lines); i++ {
		if k, isKey := keys[i]; !isKey || k != key {
			kept = append(kept, lines[i])
			continue
		}
		if !replaced {
			kept = append(kept, line)
			replaced = true
		}
		// The value runs to the next key; blank and comment lines ending it
		// belong to what follows and are kept.
		next := i + 1
		for ; next < len(lines); next++ {
			if _, isKey := keys[next]; isKey {
				break
			}
		}
		spacers := next
		for spacers > i+1 && (strings.TrimSpace(lines[spacers-1]) == "" || strings.HasPrefix(lines[spacers-1], "#")) {
			spacers--
		}
		kept = append(kept, lines[spacers:next]...)
		i = next - 1
	}
	// Split's block is empty or ends in a newline, so the
	// line can be appended as it is.
	if !replaced {
		kept = append(kept, line)
	}
	out := "---\n" + strings.Join(kept, "") + "---\n" + body
	// Checked by reading the page back as pages are read, rather than
	// predicted: the line can break a flow mapping, or land after a document
	// end YAML never reads past.
	if _, err := Parse(text); err == nil {
		doc, err := Parse(out)
		if _, set := doc.Meta[key]; err != nil || !set {
			return raw
		}
	}
	return []byte(out)
}

// keyLines maps the 0-based line of each top-level key in a
// block-style frontmatter mapping to the key as YAML reads it, or is nil when
// the block is not one.
func keyLines(block string) map[int]string {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(block), &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode || mapping.Style&yaml.FlowStyle != 0 {
		return nil
	}
	keys := make(map[int]string, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keys[mapping.Content[i].Line-1] = mapping.Content[i].Value
	}
	return keys
}
