package execenv

import (
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeGrepShapeTree lays out a small tree both grep implementations search
// the same way: two matching files at different depths, a file with no match,
// and a file whose match sits alone with context around it. The ctx/ files
// match "hit" (never "foo") to pin how context windows join: overlapping,
// touching, or apart, and at the end of a file that ends in a newline.
func writeGrepShapeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.go":         "package a\nfoo one\nbar\n",
		"sub/b.go":     "foo two\nfoo three\n",
		"sub/none.txt": "nothing here\n",
		"ctx.txt":      "before2\nbefore1\nfoo ctx\nafter1\nafter2\n",
		"ctx/overlap":  "a\nhit 1\nhit 2\nb\n",
		"ctx/touching": "hit 1\nx\ny\nhit 2\nz\n",
		"ctx/apart":    "hit 1\nx\ny\nz\nhit 2\n",
		"ctx/eof":      "before\nhit one\nafter\nhit two\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGrepEmitsOneShapeWithOrWithoutRipgrep runs the ripgrep arm and the
// native fallback against the same tree and asks for the same text: paths
// relative to the searched directory, and no trailing newline (#3259); context
// windows that overlap or touch joined into one group, and a "--" only between
// groups apart; and in content mode a cap on output lines, separators and
// context included (#3284). rg searches files in parallel, so across several
// files only the set of lines is compared; a single file's output is compared
// exactly. Without ripgrep installed only the fallback is checked.
func TestGrepEmitsOneShapeWithOrWithoutRipgrep(t *testing.T) {
	root := writeGrepShapeTree(t)
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	arms := []struct {
		name string
		env  *LocalExecutionEnvironment
	}{{"fallback", fallback}}
	if rg, err := exec.LookPath("rg"); err == nil {
		withRg := NewLocalExecutionEnvironment(root)
		defer withRg.Cleanup()
		withRg.lookPath = func(string) (string, error) { return rg, nil }
		arms = append(arms, struct {
			name string
			env  *LocalExecutionEnvironment
		}{"ripgrep", withRg})
	}

	cases := []struct {
		name, path, mode, pattern string
		context, maxResults       int
		ordered                   bool
		want                      []string
	}{
		{name: "content from the root", mode: "content", want: []string{
			filepath.FromSlash("a.go") + ":2:foo one",
			filepath.FromSlash("ctx.txt") + ":3:foo ctx",
			filepath.FromSlash("sub/b.go") + ":1:foo two",
			filepath.FromSlash("sub/b.go") + ":2:foo three",
		}},
		{name: "content from a subdirectory", path: "sub", mode: "content", want: []string{
			"b.go:1:foo two",
			"b.go:2:foo three",
		}},
		{name: "files with matches", mode: "files_with_matches", want: []string{
			"a.go", "ctx.txt", filepath.FromSlash("sub/b.go"),
		}},
		{name: "count", mode: "count", want: []string{
			"a.go:1", "ctx.txt:1", filepath.FromSlash("sub/b.go") + ":2",
		}},
		{name: "content with context in one file", path: "ctx.txt", mode: "content", context: 1, ordered: true, want: []string{
			"2-before1", "3:foo ctx", "4-after1",
		}},
		{name: "one named file", path: "sub/b.go", mode: "content", ordered: true, want: []string{
			"1:foo two", "2:foo three",
		}},
		{name: "one named file's matches", path: "a.go", mode: "files_with_matches", ordered: true, want: []string{"."}},
		{name: "one named file's count", path: "sub/b.go", mode: "count", ordered: true, want: []string{"2"}},
		{name: "overlapping context windows join", path: "ctx/overlap", pattern: "hit", context: 1, ordered: true, want: []string{
			"1-a", "2:hit 1", "3:hit 2", "4-b",
		}},
		{name: "touching context windows join", path: "ctx/touching", pattern: "hit", context: 1, ordered: true, want: []string{
			"1:hit 1", "2-x", "3-y", "4:hit 2", "5-z",
		}},
		{name: "context windows apart get a separator", path: "ctx/apart", pattern: "hit", context: 1, ordered: true, want: []string{
			"1:hit 1", "2-x", "--", "4-z", "5:hit 2",
		}},
		{name: "context stops at the last line", path: "ctx/eof", pattern: "hit", context: 1, ordered: true, want: []string{
			"1-before", "2:hit one", "3-after", "4:hit two",
		}},
		{name: "no empty line past the last one", path: "ctx/eof", pattern: "^$", ordered: true, want: []string{""}},
		{name: "the cap counts context lines and separators", path: "ctx/apart", pattern: "hit", context: 1, maxResults: 4, ordered: true, want: []string{
			"1:hit 1", "2-x", "--", "4-z", "", grepTruncationNote(4),
		}},
		{name: "output exactly at the cap has no note", path: "ctx/apart", pattern: "hit", context: 1, maxResults: 5, ordered: true, want: []string{
			"1:hit 1", "2-x", "--", "4-z", "5:hit 2",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pattern := cmp.Or(tc.pattern, "foo")
			maxResults := cmp.Or(tc.maxResults, 100)
			for _, arm := range arms {
				got, err := arm.env.Grep(context.Background(), pattern, tc.path, "", false, maxResults, tc.mode, tc.context)
				if err != nil {
					t.Fatalf("%s: Grep: %v", arm.name, err)
				}
				if strings.HasSuffix(got, "\n") {
					t.Errorf("%s: output ends with a newline: %q", arm.name, got)
				}
				lines := strings.Split(got, "\n")
				if !tc.ordered {
					slices.Sort(lines)
				}
				if !slices.Equal(lines, tc.want) {
					t.Errorf("%s: lines = %q, want %q", arm.name, lines, tc.want)
				}
			}
		})
	}
}

// TestRipgrepOutputLinesTakesTheFallbacksShape pins the reshaping on recorded
// rg output, so hosts without ripgrep cover it too.
func TestRipgrepOutputLinesTakesTheFallbacksShape(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"work", "repo")
	sep := string(filepath.Separator)
	cases := []struct {
		name      string
		stdout    string
		oneFile   bool
		filesOnly bool
		want      []string
	}{
		{
			name:   "content under a directory, with a context group",
			stdout: dir + sep + "a.go\x002:foo\n--\n" + dir + sep + "sub" + sep + "b.go\x001-before\n" + dir + sep + "sub" + sep + "b.go\x002:foo\n",
			want:   []string{"a.go:2:foo", "--", "sub" + sep + "b.go-1-before", "sub" + sep + "b.go:2:foo"},
		},
		{name: "files with matches", stdout: dir + sep + "a.go\x00" + dir + sep + "b.go\x00", filesOnly: true, want: []string{"a.go", "b.go"}},
		{name: "count", stdout: dir + sep + "a.go\x0012\n", want: []string{"a.go:12"}},
		{
			name:   "a name holding a newline",
			stdout: dir + sep + "a\nb.md\x001-x\n" + dir + sep + "a\nb.md\x002:foo\n--\n" + dir + sep + "c.md\x009:foo\n",
			want:   []string{`"a\nb.md"-1-x`, `"a\nb.md":2:foo`, "--", "c.md:9:foo"},
		},
		{name: "a name holding a newline, files with matches", stdout: dir + sep + "a\nb.md\x00", filesOnly: true, want: []string{`"a\nb.md"`}},
		{name: "a named file's matches", stdout: dir + "\x00", oneFile: true, filesOnly: true, want: []string{"."}},
		{name: "a named file's lines", stdout: "1:foo\n2:foo\n", oneFile: true, want: []string{"1:foo", "2:foo"}},
		{name: "a named file's count", stdout: "2\n", oneFile: true, want: []string{"2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ripgrepOutputLines(tc.stdout, dir, tc.oneFile, tc.filesOnly); !slices.Equal(got, tc.want) {
				t.Fatalf("ripgrepOutputLines = %q, want %q", got, tc.want)
			}
		})
	}
}
