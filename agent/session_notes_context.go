package agent

import (
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/schema"
)

// notesContextBlock renders the current notes plus URL list for agent context
// injection: beside the goal continuation-prompt rendering at turn start and
// on resume, refreshed from note events before the next round. Empty state
// renders the explicit cleared marker (see notesClearedBlock), except that a
// session whose store was NEVER non-empty this process renders nothing, so a
// fresh session's context is byte-identical to today.
func (s *Session) notesContextBlock() string {
	return s.renderNotesContextBlock()
}

// notesContextBlockForModel renders the model-facing copy of the block: the
// framing tags stay literal and everything between them is escaped, so a note or
// label carrying the closing tag cannot terminate the block and make the model
// read attacker-chosen text as harness-authored context (persistent indirect
// prompt injection). Only this copy escapes — the raw block is what persists,
// displays, and feeds tool output, so real URLs and text survive.
func (s *Session) notesContextBlockForModel() string {
	return escapeNotesContextBlock(s.renderNotesContextBlock())
}

// notesAngleBrackets escapes the literal spelling of the two framing characters.
var notesAngleBrackets = strings.NewReplacer("<", "&lt;", ">", "&gt;")

// neutralizeNotesFraming removes every way content could forge the block's framing
// or terminate it from inside: the literal angle brackets, and the character
// references that decode back into them. Every other byte passes through untouched
// — html.EscapeString would also rewrite "&" and the quote characters, handing the
// model a URL like "...?a=1&amp;b=2" that neither notes_read nor the UI would ever
// show it.
//
// References are canonicalized rather than escaped one layer deeper. Escaping the
// leading "&" only ever buys the next decode layer, so a note could always spell a
// tag behind one layer more than the escaper had seen; canonicalizing gives the
// copy exactly one spelling per angle bracket, whatever the input spelling and
// however many "amp;" layers it carried, and makes the pass idempotent. A reader
// that decodes one layer still sees what it saw before for a literal "<", which is
// the property this escape has always had.
func neutralizeNotesFraming(content string) string {
	// References first: canonicalizing them leaves nothing that decodes to an angle
	// bracket, and this runs before the literal pass so its own "&lt;" output is
	// not escaped a second time.
	return notesAngleBrackets.Replace(canonicalizeNotesAngleBracketReferences(content))
}

// canonicalizeNotesAngleBracketReferences rewrites every complete character
// reference that resolves to an angle bracket into the canonical escaped spelling
// of that angle bracket ("&lt;" or "&gt;"). Text that is not such a reference is
// copied byte for byte, so an innocent "?a=1&ltd=2" in a URL reaches the model
// exactly as it was stored.
func canonicalizeNotesAngleBracketReferences(content string) string {
	if !strings.Contains(content, "&") {
		return content
	}
	var canonical strings.Builder
	canonical.Grow(len(content))
	for i := 0; i < len(content); {
		if content[i] == '&' {
			if bracket, size, ok := parseNotesAngleBracketReference(content[i:]); ok {
				if bracket == '<' {
					canonical.WriteString("&lt;")
				} else {
					canonical.WriteString("&gt;")
				}
				i += size
				continue
			}
		}
		canonical.WriteByte(content[i])
		i++
	}
	return canonical.String()
}

// parseNotesAngleBracketReference parses one complete character reference at the
// start of s, which must begin with "&", and reports the angle bracket it resolves
// to together with the number of bytes the reference occupied.
//
// A reference is "&", any number of ampersand layers, then a base followed by the
// terminating ";": the named references lt/gt (HTML named references are
// case-insensitive, so "&LT;" and "&Lt;" count too), or a numeric reference of
// decimal or "x"/"X" hex digits ("&#60;", "&#060;", "&#x3c;", "&#X3C;"). A layer
// may itself be spelled numerically, which is why notesAmpLayerWidth exists. The
// terminator is required, the digits have to parse and stay in range, and the
// resolved value has to be an angle bracket: anything else is ordinary text that
// happens to contain an "&", and rewriting it would hand the model text the user
// never wrote.
func parseNotesAngleBracketReference(s string) (bracket rune, size int, ok bool) {
	rest := s[1:]
	size = 1
	for {
		width, layer := notesAmpLayerWidth(rest)
		if !layer {
			break
		}
		rest = rest[width:]
		size += width
	}
	switch {
	case len(rest) >= 3 && strings.EqualFold(rest[:3], "lt;"):
		return '<', size + 3, true
	case len(rest) >= 3 && strings.EqualFold(rest[:3], "gt;"):
		return '>', size + 3, true
	case strings.HasPrefix(rest, "#"):
		if value, width, valid := parseNotesNumericReference(rest[1:], size+1); valid && (value == '<' || value == '>') {
			return value, width, true
		}
	}
	return 0, 0, false
}

// notesAmpLayerWidth reports the width of an ampersand-encoding layer at the
// start of s: the named reference "amp;" in any case, or a complete numeric
// reference resolving to "&" ("&#38;", "&#038;", "&#x26;", "&#X26;"). The
// numeric spelling counts because "&#38;lt;" decodes to "&lt;" and then to "<",
// so following only the named spelling left a whole spelling of a framing tag
// passing through untouched (roborev's finding on the round that introduced
// this parser).
func notesAmpLayerWidth(s string) (int, bool) {
	if len(s) >= 4 && strings.EqualFold(s[:4], "amp;") {
		return 4, true
	}
	if strings.HasPrefix(s, "#") {
		if value, width, ok := parseNotesNumericReference(s[1:], 1); ok && value == '&' {
			return width, true
		}
	}
	return 0, false
}

// parseNotesNumericReference parses the digits of a numeric character reference
// and its required terminator, afterHash pointing just past the "#" and size
// counting the bytes consumed before them. It resolves any in-range value and
// leaves which values are in scope to the caller.
func parseNotesNumericReference(afterHash string, size int) (value rune, consumed int, ok bool) {
	base := 10
	digits := afterHash
	if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
		base = 16
		digits = digits[1:]
		size++
	}
	end := 0
	for end < len(digits) && isNotesReferenceDigit(digits[end], base) {
		end++
	}
	if end == 0 || end >= len(digits) || digits[end] != ';' {
		return 0, 0, false
	}
	parsed, err := strconv.ParseUint(digits[:end], base, 32)
	if err != nil {
		return 0, 0, false
	}
	return rune(parsed), size + end + 1, true
}

// isNotesReferenceDigit reports whether c is a digit of base 10 or 16.
func isNotesReferenceDigit(c byte, base int) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case base == 16 && c >= 'a' && c <= 'f':
		return true
	case base == 16 && c >= 'A' && c <= 'F':
		return true
	}
	return false
}

// escapeNotesContextBlock returns the model-facing copy of a rendered
// shared-notes block. It is the single definition of the framing-escape contract:
// the renderer applies it to the live copy, and every path that puts a persisted
// (raw) block back into model context applies it to that turn — a restored
// transcript turn or a forked delegate's inherited prefix. The framing tags sit at
// fixed ends of the block and the content between them is neutralized (see
// neutralizeNotesFraming), so the block can be neither terminated nor re-opened
// from inside. Text that is not a rendered block is neutralized whole rather than
// passed through raw.
func escapeNotesContextBlock(block string) string {
	inner, ok := strings.CutPrefix(block, notesBlockOpen)
	if ok {
		if body, closed := strings.CutSuffix(inner, notesBlockClose); closed {
			return notesBlockOpen + neutralizeNotesFraming(body) + notesBlockClose
		}
	}
	return neutralizeNotesFraming(block)
}

func (s *Session) renderNotesContextBlock() string {
	human, agentNote, urls, everProjected := s.notesProjectionSnapshot()
	if human == "" && agentNote == "" && len(urls) == 0 {
		if !everProjected {
			return ""
		}
		return notesClearedBlock
	}
	var b strings.Builder
	b.WriteString(notesBlockOpen)
	if human != "" {
		b.WriteString(formatNotesField("Human:", human) + "\n")
	}
	if agentNote != "" {
		b.WriteString(formatNotesField("Agent:", agentNote) + "\n")
	}
	for _, u := range urls {
		b.WriteString(formatNotesLinkLine(u) + "\n")
	}
	b.WriteString(notesBlockClose)
	return b.String()
}

// notesClearedBlock is the explicit empty snapshot maybeAppendNotesContext
// appends when the store transitions to empty after having held content. A
// removal that empties the last populated field (final URL removed while both
// notes are empty, a note cleared while nothing else is set) must still reach
// the next model request as a turn: history is append-only and the model
// never re-reads the store, so without this the stale pre-removal snapshot
// would stand as the model's latest truth. Distinct from "" (never
// populated: project nothing) so the fill→remove-all→next-request sequence
// shows empty rather than stale rows.
const notesClearedBlock = notesBlockOpen + "(empty — all shared notes and links cleared)\n" + notesBlockClose

// formatNotesField renders a whiteboard under its label ("Human:", "Agent:")
// for the model context block and the notes_read tool output. A whiteboard
// keeps its lines, so each continuation line is indented under the label: a
// line the human writes as "Agent: ..." or "Link: ..." then cannot read as a
// separate field. Blank lines stay empty rather than carrying the indent.
func formatNotesField(label, note string) string {
	lines := strings.Split(note, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return label + " " + strings.Join(lines, "\n")
}

// formatNotesLinkLine renders one session URL list entry for the model
// context block and the notes_read tool output. The entry id rides alongside
// the human-readable URL/label because the model never receives the State
// side-channel — only Output — yet urls_remove requires the id.
func formatNotesLinkLine(u schema.SessionURL) string {
	base := u.URL
	if u.Label != "" {
		base = fmt.Sprintf("%s (%s)", u.Label, u.URL)
	}
	if u.ID != "" {
		return fmt.Sprintf("Link: %s [id: %s]", base, u.ID)
	}
	return "Link: " + base
}
