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
	// gate cannot do: quoting {2id: 1} would yield valid JSON. A match inside
	// a string value can only break that string, so the gate rejects those
	// rewrites whole instead of repairing around them.
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
// holding [A-Za-z_][A-Za-z0-9_]* followed by ':' — in double quotes. Only
// quotes are inserted, so the caller's json.Valid gate stays the sole
// authority on whether the rewrite repairs the input: a match that landed in
// a string value breaks that string and the gate refuses the rewrite whole.
// Returns the input unchanged when no key is quoted.
func quoteBareObjectKeys(s string) (string, int) {
	locs := bareKeyRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s, 0
	}
	return bareKeyRe.ReplaceAllString(s, `${1}"${2}"${3}`), len(locs)
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
