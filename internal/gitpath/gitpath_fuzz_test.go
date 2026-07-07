//go:build serffuzz

package gitpath

import (
	"strings"
	"testing"
)

// FuzzParseGitdirPointer drives ParseGitdirPointer with arbitrary ".git"
// pointer-file content. Two invariants hold regardless of input: it never
// panics, and its (path, ok) result is self-consistent — ok==false always
// carries an empty path, and ok==true always carries a non-empty, fully
// whitespace-trimmed token (the parser returns strings.TrimSpace of the text
// after "gitdir:").
func FuzzParseGitdirPointer(f *testing.F) {
	for _, s := range []string{
		"gitdir: /path/to/.git/worktrees/x\n",
		"gitdir: ../relative\n",
		"gitdir:no-space-after-colon",
		"",
		"gitdir:",
		"gitdir:   ",
		"# a comment line\ngitdir: /x\n",
		"garbage\nmore garbage\n",
		"GITDIR: /uppercase-ignored",
		"\n\n\ngitdir: /y\n",
		"gitdir: with trailing spaces   \n",
		"prefix gitdir: /not-at-line-start",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		p, ok := ParseGitdirPointer(content) // must not panic
		if !ok {
			if p != "" {
				t.Fatalf("ParseGitdirPointer(%q) = (%q, false); path must be empty when ok is false", content, p)
			}
			return
		}
		if p == "" {
			t.Fatalf("ParseGitdirPointer(%q) reported ok with an empty path", content)
		}
		if strings.TrimSpace(p) != p {
			t.Fatalf("ParseGitdirPointer(%q) = %q; a genuine pointer path is fully trimmed", content, p)
		}
	})
}
