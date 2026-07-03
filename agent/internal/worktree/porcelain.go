package worktree

import "strings"

// PorcelainEntry is one worktree's record from `git worktree list
// --porcelain` output (spec §5 `list` step 1). Branch, when set, is the full
// ref (e.g. "refs/heads/main") as git prints it — callers that want the
// short name strip the "refs/heads/" prefix themselves. LockReason and
// PrunableReason are already C-unquoted (spec §5's C-quoting note: "reasons
// containing spaces/newlines are C-quoted in porcelain output — the parser
// must unquote before display/compare").
type PorcelainEntry struct {
	Path           string
	Head           string
	Branch         string
	Bare           bool
	Detached       bool
	Locked         bool
	LockReason     string
	Prunable       bool
	PrunableReason string
}

// ParsePorcelain parses the output of `git worktree list --porcelain` into
// one PorcelainEntry per worktree. Entries are separated by a blank line;
// within an entry each line is "<key>" or "<key> <value>" (the value is
// everything after the first space, so paths and reasons containing spaces
// survive intact). ParsePorcelain never panics and never returns an entry
// with an empty Path — a "worktree" line always starts an entry, and a
// stray line before the first "worktree" line (or an unrecognized key,
// tolerated for forward compatibility with newer git porcelain fields) is
// simply ignored rather than treated as a malformed-input error, since this
// parser only ever sees output git itself produced.
func ParsePorcelain(out string) []PorcelainEntry {
	var entries []PorcelainEntry
	var cur *PorcelainEntry

	flush := func() {
		if cur != nil && cur.Path != "" {
			entries = append(entries, *cur)
		}
		cur = nil
	}

	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			flush()
			continue
		}
		key, rest, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &PorcelainEntry{Path: rest}
		case "HEAD":
			if cur != nil {
				cur.Head = rest
			}
		case "branch":
			if cur != nil {
				cur.Branch = rest
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
				cur.LockReason = CUnquote(rest)
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
				cur.PrunableReason = CUnquote(rest)
			}
		}
	}
	flush()

	return entries
}

// cQuoteEscapes maps a C-style backslash escape letter (as git's
// quote_c_style emits, and as `git worktree lock --reason` round-trips
// through porcelain output) to its literal byte.
var cQuoteEscapes = map[byte]byte{
	'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v',
	'"': '"', '\\': '\\',
}

// CUnquote reverses git's C-style quoting of a porcelain field (spec §5's
// C-quoting note). A string git chose not to quote — anything without
// control characters, backslashes, or double quotes, which includes plain
// multi-word reasons like "reason with spaces" — passes through unchanged;
// CUnquote only has quoting to undo when s is wrapped in a leading and
// trailing '"'. Inside the quotes it recognizes the same escapes git's
// quote_c_style writes: \a \b \f \n \r \t \v \" \\, and three-digit octal
// byte escapes (\NNN, used for control bytes and non-ASCII bytes — each byte
// of a multi-byte UTF-8 character is escaped individually, and concatenating
// the decoded bytes reassembles the original character). An unrecognized
// escape sequence is left as-is (backslash and the following byte both
// copied through) rather than dropped, so unexpected input never silently
// loses data.
func CUnquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	body := s[1 : len(s)-1]

	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		next := body[i]
		if lit, ok := cQuoteEscapes[next]; ok {
			b.WriteByte(lit)
			continue
		}
		if next >= '0' && next <= '7' {
			val := int(next - '0')
			for digits := 1; digits < 3 && i+1 < len(body) && body[i+1] >= '0' && body[i+1] <= '7'; digits++ {
				i++
				val = val*8 + int(body[i]-'0')
			}
			b.WriteByte(byte(val))
			continue
		}
		// Unrecognized escape: preserve both bytes rather than guessing.
		b.WriteByte('\\')
		b.WriteByte(next)
	}
	return b.String()
}
