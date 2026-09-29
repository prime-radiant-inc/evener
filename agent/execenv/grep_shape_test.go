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

// writeGrepContextTree lays out the fixtures the context-group parity tests
// search: adjacent matches whose -C windows overlap, matches a single skipped
// line apart, and matches on a file's last line, where the split's phantom
// trailing element must not leak into context.
func writeGrepContextTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"touch.txt": "a\nfoo 1\nfoo 2\nb\n",
		"gap.txt":   "1\n2\nfoo 3\n4\n5\n6\nfoo 7\n8\n",
		"eof.txt":   "pre\nfoo\n",
		"only.txt":  "foo\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGrepNativeMergesAdjacentContextGroupsLikeRipgrep pins the native
// fallback's -C shape to ripgrep's (#3284): overlapping and touching match
// windows collapse into one group so a match line never reappears as another
// window's context line, and a "--" row separates only groups with at least one
// skipped line between them.
func TestGrepNativeMergesAdjacentContextGroupsLikeRipgrep(t *testing.T) {
	root := writeGrepContextTree(t)
	env := NewLocalExecutionEnvironment(root)
	cases := []struct {
		file string
		want string
	}{
		// Windows [1,3] and [2,4] overlap: one group, both match lines use ":".
		{"touch.txt", "1-a\n2:foo 1\n3:foo 2\n4-b"},
		// Windows [2,4] and [6,8] leave line 5 unprinted: two groups, "--".
		{"gap.txt", "2-2\n3:foo 3\n4-4\n--\n6-6\n7:foo 7\n8-8"},
		// A match on the last real line must not print the split's phantom
		// trailing element as a context row (rg prints no such line).
		{"eof.txt", "1-pre\n2:foo"},
		{"only.txt", "1:foo"},
	}
	for _, tc := range cases {
		got, err := env.grepNative(context.Background(), "foo", filepath.Join(root, tc.file), "", false, 100, "content", 1)
		if err != nil {
			t.Fatalf("%s: grepNative: %v", tc.file, err)
		}
		if got != tc.want {
			t.Fatalf("%s context = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// TestGrepContextGroupsMatchWithOrWithoutRipgrep runs both arms over the same
// context fixtures and expects byte-identical -C output (#3284).
func TestGrepContextGroupsMatchWithOrWithoutRipgrep(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed; cannot compare the rg path with the fallback")
	}
	root := writeGrepContextTree(t)
	withRg := NewLocalExecutionEnvironment(root)
	defer withRg.Cleanup()
	withRg.lookPath = func(string) (string, error) { return rg, nil }
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	for _, name := range []string{"touch.txt", "gap.txt", "eof.txt", "only.txt"} {
		target := filepath.Join(root, name)
		gotRg, err := withRg.Grep(context.Background(), "foo", target, "", false, 100, "content", 1)
		if err != nil {
			t.Fatalf("%s: Grep (ripgrep): %v", name, err)
		}
		gotNative, err := fallback.Grep(context.Background(), "foo", target, "", false, 100, "content", 1)
		if err != nil {
			t.Fatalf("%s: Grep (fallback): %v", name, err)
		}
		if gotRg != gotNative {
			t.Fatalf("%s context differs:\n ripgrep = %q\n fallback = %q", name, gotRg, gotNative)
		}
	}

	// A directory search exercises the inter-file "--" separator and the
	// cross-file order --sort path pins.
	gotRg, err := withRg.Grep(context.Background(), "foo", root, "", false, 100, "content", 1)
	if err != nil {
		t.Fatalf("Grep (ripgrep, directory): %v", err)
	}
	gotNative, err := fallback.Grep(context.Background(), "foo", root, "", false, 100, "content", 1)
	if err != nil {
		t.Fatalf("Grep (fallback, directory): %v", err)
	}
	if gotRg == "" {
		t.Fatal("directory context search returned nothing")
	}
	if gotRg != gotNative {
		t.Fatalf("directory context differs:\n ripgrep = %q\n fallback = %q", gotRg, gotNative)
	}
}

// TestGrepContentCapCountsLines pins the result cap to output lines (#3284):
// maxResults counts ":" match rows, "-" context rows, and "--" separators alike,
// as the ripgrep arm's first-N-lines truncation does. Separate groups make a
// line cap and a match cap diverge.
func TestGrepContentCapCountsLines(t *testing.T) {
	root := t.TempDir()
	content := "hit\nx\ny\nz\nhit\nx\ny\nz\nhit\nx\ny\nz\n"
	if err := os.WriteFile(filepath.Join(root, "m.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	got, err := env.grepNative(context.Background(), "hit", filepath.Join(root, "m.txt"), "", false, 3, "content", 1)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if want := "1:hit\n2-x\n--"; got != want {
		t.Fatalf("capped context = %q, want %q", got, want)
	}

	if rg, err := exec.LookPath("rg"); err == nil {
		rgEnv := NewLocalExecutionEnvironment(root)
		defer rgEnv.Cleanup()
		rgEnv.lookPath = func(string) (string, error) { return rg, nil }
		gotRg, err := rgEnv.Grep(context.Background(), "hit", filepath.Join(root, "m.txt"), "", false, 3, "content", 1)
		if err != nil {
			t.Fatalf("Grep (ripgrep): %v", err)
		}
		if gotRg != got {
			t.Fatalf("capped context differs:\n ripgrep = %q\n fallback = %q", gotRg, got)
		}
	}
}

// TestBuildRipgrepArgsSortsByPath pins rg's deterministic cross-file order
// (#3284): the argv carries "--sort path" so a parallel search cannot report
// files in a different order from run to run.
func TestBuildRipgrepArgsSortsByPath(t *testing.T) {
	args := buildRipgrepArgs("content", false, "", "foo", "/root", 0)
	i := slices.Index(args, "--sort")
	if i < 0 || i+1 >= len(args) || args[i+1] != "path" {
		t.Fatalf("expected --sort path in args, got: %v", args)
	}
}

// TestGrepContextEmptyFileHasNoPhantomLine pins that a zero-byte file yields no
// context row (rg reports nothing), while a file holding one empty line still
// reports that line (#3284 review).
func TestGrepContextEmptyFileHasNoPhantomLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	got, err := env.grepNative(context.Background(), "^", filepath.Join(root, "empty.txt"), "", false, 100, "content", 1)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if got != "" {
		t.Fatalf("zero-byte file context = %q, want empty", got)
	}
	gotOne, err := env.grepNative(context.Background(), "^", filepath.Join(root, "one.txt"), "", false, 100, "content", 1)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if gotOne != "1:" {
		t.Fatalf("single empty line context = %q, want %q", gotOne, "1:")
	}
}
