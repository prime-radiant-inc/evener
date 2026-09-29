package execenv

import (
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
// and a file whose match sits alone with context around it.
func writeGrepShapeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.go":         "package a\nfoo one\nbar\n",
		"sub/b.go":     "foo two\nfoo three\n",
		"sub/none.txt": "nothing here\n",
		"ctx.txt":      "before2\nbefore1\nfoo ctx\nafter1\nafter2\n",
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
// relative to the searched directory, and no trailing newline (#3259). rg
// searches files in parallel, so across several files only the set of lines
// is compared; a single file's output is compared exactly.
func TestGrepEmitsOneShapeWithOrWithoutRipgrep(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed; cannot compare the rg path with the fallback")
	}
	root := writeGrepShapeTree(t)
	withRg := NewLocalExecutionEnvironment(root)
	defer withRg.Cleanup()
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	cases := []struct {
		name, path, mode string
		context          int
		ordered          bool
		want             []string
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, arm := range []struct {
				name string
				env  *LocalExecutionEnvironment
			}{{"ripgrep", withRg}, {"fallback", fallback}} {
				got, err := arm.env.Grep(context.Background(), "foo", tc.path, "", false, 100, tc.mode, tc.context)
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
		name    string
		stdout  string
		oneFile bool
		want    []string
	}{
		{
			name:   "content under a directory, with a context group",
			stdout: dir + sep + "a.go:2:foo\n--\n" + dir + sep + "sub" + sep + "b.go-1-before\n" + dir + sep + "sub" + sep + "b.go:2:foo\n",
			want:   []string{"a.go:2:foo", "--", "sub" + sep + "b.go-1-before", "sub" + sep + "b.go:2:foo"},
		},
		{name: "files with matches", stdout: dir + sep + "a.go\n", want: []string{"a.go"}},
		{name: "a named file's matches", stdout: dir + "\n", oneFile: true, want: []string{"."}},
		{name: "a named file's lines", stdout: "1:foo\n2:foo\n", oneFile: true, want: []string{"1:foo", "2:foo"}},
		{name: "a named file's count", stdout: "2\n", oneFile: true, want: []string{"2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ripgrepOutputLines(tc.stdout, dir, tc.oneFile); !slices.Equal(got, tc.want) {
				t.Fatalf("ripgrepOutputLines = %q, want %q", got, tc.want)
			}
		})
	}
}
