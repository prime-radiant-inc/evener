package execenv

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PathHasControl reports whether p holds an ASCII control character other
// than tab (a C0 control or DEL). A line ending in such a name splits any
// output that gives each entry one line, and a Markdown link destination can
// hold no other control character either. A tab is an ordinary character.
func PathHasControl(p string) bool {
	return strings.ContainsFunc(p, func(r rune) bool { return (r < ' ' && r != '\t') || r == 0x7f })
}

// QuoteControlPath writes p on one line as a JSON string, the form a tool
// call passes back as a path, without HTML escaping and with DEL escaped
// (JSON leaves it bare). No JSON string holds invalid UTF-8, which JSON
// encoding would replace with U+FFFD, so such a name is Go-quoted with \x
// escapes instead and each name keeps a form of its own.
func QuoteControlPath(p string) string {
	if !utf8.ValidString(p) {
		return strconv.Quote(p)
	}
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(p) // a string always encodes
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "\x7f", `\u007f`)
}

// OneLinePath is how a tool's output names the path p on a line of its own
// (a grep result, a glob match, a directory entry): p as it is, or, when p
// holds a control character, quoted (QuoteControlPath), so a newline in a
// name never splits a line and every tool names such a file the same way.
func OneLinePath(p string) string {
	if PathHasControl(p) {
		return QuoteControlPath(p)
	}
	return p
}
