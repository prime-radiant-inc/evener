package repair

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var (
	// brokenEscapeRe matches a \u escape with fewer than 4 hex digits followed
	// by a non-hex char or end of string. It captures the trailing char so the
	// replacement can preserve it. Valid \uXXXX (4 hex) never matches.
	brokenEscapeRe = regexp.MustCompile(`\\u([0-9a-fA-F]{0,3})([^0-9a-fA-F]|$)`)
	// uEscapeRe matches a complete \uXXXX escape.
	uEscapeRe = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
	// bareKeyRe matches a bare identifier object key — an identifier directly
	// after '{' or ',' (with optional JSON whitespace) followed by ':'. The
	// anchor keeps value-position identifiers out; starting the identifier at
	// [A-Za-z_] keeps digit-leading names out, which the caller's json.Valid
	// gate cannot do: quoting {2id: 1} would yield valid JSON. Matches that
	// start inside a string value are skipped against stringSpans, so
	// key-like text in a value never poisons a legitimate key's repair.
	bareKeyRe = regexp.MustCompile(`([{,][ \t\n\r]*)([A-Za-z_][A-Za-z0-9_]*)([ \t\n\r]*:)`)
)

// RepairJSON makes unparseable tool-argument bytes parseable by fixing broken
// \u escapes and lone UTF-16 surrogates in string values, quoting bare
// identifier object keys, or appending a missing outer-object brace. The brace
// repair never combines with the others; the final json.Valid gate rejects any
// rewrite that does not yield valid JSON. Deliberately narrow: it does not
// attempt general JSON slop repair (trailing commas, etc.). Returns
// (raw, nil) when it changes nothing.
func RepairJSON(raw []byte) ([]byte, []Change) {
	// A nonempty object that becomes valid with exactly one appended brace
	// already has complete members. Let the JSON parser reject unfinished
	// values, trailing commas, and missing nested closers; never invent fields.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 1 && trimmed[0] == '{' {
		candidate := append(bytes.Clone(raw), '}')
		if json.Valid(candidate) {
			return candidate, []Change{{Kind: ChangeMissingOuterBrace, Detail: "appended missing outer-object }"}}
		}
	}

	s := string(raw)
	var changes []Change

	if brokenEscapeRe.MatchString(s) {
		s = brokenEscapeRe.ReplaceAllString(s, `�$2`)
		changes = append(changes, Change{Kind: ChangeUnicodeRepair, Detail: `invalid \u escape → �`})
	}

	fixed, surr := fixLoneSurrogates(s)
	s = fixed
	changes = append(changes, surr...)

	if quoted, n := quoteBareObjectKeys(s); n > 0 {
		s = quoted
		changes = append(changes, Change{Kind: ChangeQuoteObjectKey,
			Detail: strconv.Itoa(n) + " bare object key(s) quoted"})
	}

	// Only claim a repair when it actually produced valid JSON that differs
	// from the input. RE2 has no lookbehind, so the broken-escape pass isn't
	// escape-parity-aware and can either (a) leave adjacent broken escapes
	// still invalid, or (b) corrupt a valid `\\u` (escaped backslash + u)
	// sequence into invalid JSON. In either case, reporting a change would
	// mislead a caller into treating a still-broken or newly-corrupted
	// result as a successful repair.
	candidate := []byte(s)
	if len(changes) == 0 || !json.Valid(candidate) || bytes.Equal(candidate, raw) {
		return raw, nil
	}
	return candidate, changes
}

// quoteBareObjectKeys wraps bare identifier object keys — a key position
// holding [A-Za-z_][A-Za-z0-9_]* followed by ':' — in double quotes. Matches
// that start inside a string value (key-like text the model quoted as data)
// are skipped, so only real keys are rewritten and the caller's json.Valid
// gate stays the sole authority on whether the result repairs the input.
// Returns the input unchanged when no key is quoted.
func quoteBareObjectKeys(s string) (string, int) {
	locs := bareKeyRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s, 0
	}
	spans := stringSpans(s)
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	quoted := 0
	si := 0
	for _, loc := range locs {
		// Both spans and matches are position-sorted, so one forward walk
		// classifies every match: skip ones that start inside a string value.
		for si < len(spans) && spans[si][1] <= loc[0] {
			si++
		}
		if si < len(spans) && spans[si][0] <= loc[0] {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(s[loc[2]:loc[3]]) // '{' or ',' plus whitespace
		b.WriteByte('"')
		b.WriteString(s[loc[4]:loc[5]]) // the bare identifier
		b.WriteByte('"')
		b.WriteString(s[loc[6]:loc[7]]) // whitespace plus ':'
		last = loc[1]
		quoted++
	}
	if quoted == 0 {
		return s, 0
	}
	b.WriteString(s[last:])
	return b.String(), quoted
}

// stringSpans returns the [lo, hi) byte ranges of string literals in s.
// Escape-aware: a backslash inside a string skips the next byte, so an
// escaped quote never closes it. An unterminated string runs to the end of
// the input — conservative, since the caller's json.Valid gate is the final
// authority and nothing after the opening quote is treated as structural.
func stringSpans(s string) [][2]int {
	var spans [][2]int
	start := -1
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case start < 0:
			if c == '"' {
				start = i
			}
			i++
		case c == '\\':
			i += 2
		case c == '"':
			spans = append(spans, [2]int{start, i + 1})
			start = -1
			i++
		default:
			i++
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(s)})
	}
	return spans
}

func fixLoneSurrogates(s string) (string, []Change) {
	locs := uEscapeRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s, nil
	}
	code := func(i int) int64 {
		v, _ := strconv.ParseInt(s[locs[i][2]:locs[i][3]], 16, 32)
		return v
	}
	adjacent := func(i, j int) bool { return locs[j][0] == locs[i][1] }

	var changes []Change
	var b strings.Builder
	last := 0
	for i := range locs {
		c := code(i)
		lone := false
		switch {
		case c >= 0xD800 && c <= 0xDBFF: // high surrogate
			paired := i+1 < len(locs) && adjacent(i, i+1) && code(i+1) >= 0xDC00 && code(i+1) <= 0xDFFF
			lone = !paired
		case c >= 0xDC00 && c <= 0xDFFF: // low surrogate
			pairedPrev := i > 0 && adjacent(i-1, i) && code(i-1) >= 0xD800 && code(i-1) <= 0xDBFF
			lone = !pairedPrev
		}
		if !lone {
			continue
		}
		b.WriteString(s[last:locs[i][0]])
		b.WriteString(`�`)
		last = locs[i][1]
		changes = append(changes, Change{Kind: ChangeUnicodeRepair, Detail: `lone surrogate → �`})
	}
	if len(changes) == 0 {
		return s, nil
	}
	b.WriteString(s[last:])
	return b.String(), changes
}
