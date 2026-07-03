package worktree

import (
	"strconv"
	"testing"
)

// FuzzParsePorcelain drives ParsePorcelain with arbitrary strings. Two
// invariants hold regardless of input: ParsePorcelain never panics, and
// every entry it returns has a non-empty Path — the parser only starts an
// entry on a "worktree <path>" line and only keeps entries with a non-empty
// Path (see the flush() guard in porcelain.go), so no malformed or
// truncated fuzz input should ever slip a Path-less entry through.
func FuzzParsePorcelain(f *testing.F) {
	seeds := []string{
		"",
		"worktree /a\nHEAD abc123\nbranch refs/heads/main\n",
		"worktree /a\nbare\n\nworktree /b\nHEAD abc123\ndetached\n",
		"worktree /a\nHEAD abc123\nbranch refs/heads/x\nlocked \"line one\\nline two\"\n\n",
		"worktree /a\nHEAD abc123\nbranch refs/heads/x\nprunable gitdir file points to non-existent location\n\n",
		"worktree\nHEAD\nbranch\n\n",
		"HEAD abc123\nbranch refs/heads/main\n\nworktree /a\n",
		"worktree /a\n\n\nworktree /b\n",
		"worktree /a\r\nHEAD abc\r\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, out string) {
		entries := ParsePorcelain(out) // must not panic regardless of input
		for _, e := range entries {
			if e.Path == "" {
				t.Fatalf("ParsePorcelain(%q) produced an entry with an empty Path: %+v", out, e)
			}
		}
	})
}

// FuzzCUnquote drives CUnquote with arbitrary strings. It never panics
// regardless of input. For the subset of inputs that are printable ASCII
// (byte range 0x20-0x7E, so also valid UTF-8 and unambiguous under both
// git's and Go's escaping rules), it also checks a round-trip oracle:
// strconv.Quote uses the same escape letters as git's quote_c_style for the
// characters a printable-ASCII string can actually contain (only \\ and \"
// ever fire in that range — the other escape letters like \n \t are for
// control bytes, which printable ASCII excludes by construction), so
// CUnquote(strconv.Quote(s)) must reproduce s exactly.
func FuzzCUnquote(f *testing.F) {
	seeds := []string{
		"",
		"reason with spaces",
		`"hello"`,
		`"line one\nline two"`,
		`"a\tb"`,
		`"he said \"hi\""`,
		`"a\\b"`,
		`"bell\001end"`,
		`"caf\303\251"`,
		`"unterminated`,
		`"`,
		`""`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		_ = CUnquote(s) // must not panic regardless of input

		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] > 0x7e {
				return // oracle below is only defined for printable ASCII
			}
		}
		quoted := strconv.Quote(s)
		got := CUnquote(quoted)
		if got != s {
			t.Fatalf("CUnquote(strconv.Quote(%q)) = %q, want %q (quoted form: %q)", s, got, s, quoted)
		}
	})
}
