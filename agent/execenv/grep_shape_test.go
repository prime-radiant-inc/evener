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
// match "hit" (never "foo", but for ctx/deep, which pins that a directory's
// files come before a sibling file extending its name) to pin how context windows join: overlapping,
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
		"ctx/empty":    "",
		"ctx/deep":     "foo deep\n",
		"ctx/blank":    "\n",
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
// relative to the workspace root when searching under it (#6176 supersedes #3259), and no trailing newline; context
// windows that overlap or touch joined into one group, and a "--" only between
// groups apart; and in content mode a cap on output lines, separators and
// context included (#3284); and files in the same order. Without ripgrep
// installed only the fallback is checked.
func TestGrepEmitsOneShapeWithOrWithoutRipgrep(t *testing.T) {
	root := writeGrepShapeTree(t)
	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	type grepArm struct {
		name string
		env  *LocalExecutionEnvironment
	}
	arms := []grepArm{{"fallback", fallback}}
	if rg, err := exec.LookPath("rg"); err == nil {
		withRg := NewLocalExecutionEnvironment(root)
		defer withRg.Cleanup()
		withRg.lookPath = func(string) (string, error) { return rg, nil }
		arms = append(arms, grepArm{"ripgrep", withRg})
	}

	cases := []struct {
		name, path, mode, pattern string
		context, maxResults       int
		want                      []string
	}{
		{name: "content from the root", mode: "content", want: []string{
			filepath.FromSlash("a.go") + ":2:foo one",
			filepath.FromSlash("ctx/deep") + ":1:foo deep",
			filepath.FromSlash("ctx.txt") + ":3:foo ctx",
			filepath.FromSlash("sub/b.go") + ":1:foo two",
			filepath.FromSlash("sub/b.go") + ":2:foo three",
		}},
		{name: "content from a subdirectory", path: "sub", mode: "content", want: []string{
			filepath.FromSlash("sub/b.go") + ":1:foo two",
			filepath.FromSlash("sub/b.go") + ":2:foo three",
		}},
		{name: "files with matches", mode: "files_with_matches", want: []string{
			"a.go", filepath.FromSlash("ctx/deep"), "ctx.txt", filepath.FromSlash("sub/b.go"),
		}},
		{name: "count", mode: "count", want: []string{
			"a.go:1", filepath.FromSlash("ctx/deep") + ":1", "ctx.txt:1", filepath.FromSlash("sub/b.go") + ":2",
		}},
		{name: "content with context in one file", path: "ctx.txt", mode: "content", context: 1, want: []string{
			"2-before1", "3:foo ctx", "4-after1",
		}},
		{name: "one named file", path: "sub/b.go", mode: "content", want: []string{
			"1:foo two", "2:foo three",
		}},
		{name: "one named file's matches", path: "a.go", mode: "files_with_matches", want: []string{"a.go"}},
		{name: "one named file's count", path: "sub/b.go", mode: "count", want: []string{"2"}},
		{name: "overlapping context windows join", path: "ctx/overlap", pattern: "hit", context: 1, want: []string{
			"1-a", "2:hit 1", "3:hit 2", "4-b",
		}},
		{name: "touching context windows join", path: "ctx/touching", pattern: "hit", context: 1, want: []string{
			"1:hit 1", "2-x", "3-y", "4:hit 2", "5-z",
		}},
		{name: "context windows apart get a separator", path: "ctx/apart", pattern: "hit", context: 1, want: []string{
			"1:hit 1", "2-x", "--", "4-z", "5:hit 2",
		}},
		{name: "context stops at the last line", path: "ctx/eof", pattern: "hit", context: 1, want: []string{
			"1-before", "2:hit one", "3-after", "4:hit two",
		}},
		{name: "no empty line past the last one", path: "ctx/eof", pattern: "^$", want: []string{""}},
		{name: "an empty file has no lines", path: "ctx/empty", pattern: "^", context: 1, want: []string{""}},
		{name: "a lone newline is one empty line", path: "ctx/blank", pattern: "^", context: 1, want: []string{"1:"}},
		{name: "the cap counts context lines and separators", path: "ctx/apart", pattern: "hit", context: 1, maxResults: 4, want: []string{
			"1:hit 1", "2-x", "--", "4-z", "", grepTruncationNote(4),
		}},
		{name: "output exactly at the cap has no note", path: "ctx/apart", pattern: "hit", context: 1, maxResults: 5, want: []string{
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
				if !slices.Equal(lines, tc.want) {
					t.Errorf("%s: lines = %q, want %q", arm.name, lines, tc.want)
				}
			}
		})
	}
}

// TestGrepSubdirectoryPathsRoundTripIntoReadFile tests that paths returned from
// subdirectory and single-file searches resolve against the environment root
// and round-trip into ReadFile without hitting same-named root decoys (#6176).
func TestGrepSubdirectoryPathsRoundTripIntoReadFile(t *testing.T) {
	root := t.TempDir()
	rootDecoy := filepath.Join(root, "b.go")
	if err := os.WriteFile(rootDecoy, []byte("package main\n// decoy root b.go: target pattern\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subTarget := filepath.Join(subDir, "b.go")
	if err := os.WriteFile(subTarget, []byte("package sub\n// sub/b.go target pattern\n// second line target pattern\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fallback := NewLocalExecutionEnvironment(root)
	defer fallback.Cleanup()
	fallback.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	type grepArm struct {
		name string
		env  *LocalExecutionEnvironment
	}
	arms := []grepArm{{"fallback", fallback}}
	if rg, err := exec.LookPath("rg"); err == nil {
		withRg := NewLocalExecutionEnvironment(root)
		defer withRg.Cleanup()
		withRg.lookPath = func(string) (string, error) { return rg, nil }
		arms = append(arms, grepArm{"ripgrep", withRg})
	}

	expectedSubB := filepath.FromSlash("sub/b.go")

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			// 1. Directory search with path="sub", output_mode="content"
			contentOut, err := arm.env.Grep(context.Background(), "target pattern", "sub", "", false, 100, "content")
			if err != nil {
				t.Fatalf("%s: Grep content: %v", arm.name, err)
			}
			wantContent := expectedSubB + ":2:// sub/b.go target pattern\n" + expectedSubB + ":3:// second line target pattern"
			if contentOut != wantContent {
				t.Fatalf("%s: content = %q, want %q", arm.name, contentOut, wantContent)
			}
			readContent, err := arm.env.ReadFile(expectedSubB, nil, nil)
			if err != nil || !strings.Contains(readContent, "sub/b.go target pattern") || strings.Contains(readContent, "decoy") {
				t.Fatalf("%s: ReadFile round-trip failed: content=%q, err=%v", arm.name, readContent, err)
			}

			// 2. Directory search with path="sub", output_mode="files_with_matches"
			filesOut, err := arm.env.Grep(context.Background(), "target pattern", "sub", "", false, 100, "files_with_matches")
			if err != nil {
				t.Fatalf("%s: Grep files_with_matches: %v", arm.name, err)
			}
			if filesOut != expectedSubB {
				t.Fatalf("%s: files_with_matches = %q, want %q", arm.name, filesOut, expectedSubB)
			}
			readFiles, err := arm.env.ReadFile(filesOut, nil, nil)
			if err != nil || !strings.Contains(readFiles, "sub/b.go target pattern") || strings.Contains(readFiles, "decoy") {
				t.Fatalf("%s: ReadFile round-trip for files_with_matches failed: content=%q, err=%v", arm.name, readFiles, err)
			}

			// 3. Directory search with path="sub", output_mode="count"
			countOut, err := arm.env.Grep(context.Background(), "target pattern", "sub", "", false, 100, "count")
			if err != nil {
				t.Fatalf("%s: Grep count: %v", arm.name, err)
			}
			wantCount := expectedSubB + ":2"
			if countOut != wantCount {
				t.Fatalf("%s: count = %q, want %q", arm.name, countOut, wantCount)
			}

			// 4. Single-file search path="sub/b.go" in all 3 modes
			// 4a. content: prefix-free line numbers
			sfContent, err := arm.env.Grep(context.Background(), "target pattern", "sub/b.go", "", false, 100, "content")
			if err != nil || sfContent != "2:// sub/b.go target pattern\n3:// second line target pattern" {
				t.Fatalf("%s: single-file content = %q, want prefix-free line numbers, err=%v", arm.name, sfContent, err)
			}
			// 4b. count: bare count
			sfCount, err := arm.env.Grep(context.Background(), "target pattern", "sub/b.go", "", false, 100, "count")
			if err != nil || sfCount != "2" {
				t.Fatalf("%s: single-file count = %q, want \"2\", err=%v", arm.name, sfCount, err)
			}
			// 4c. files_with_matches: returns workspace-relative path
			sfFiles, err := arm.env.Grep(context.Background(), "target pattern", "sub/b.go", "", false, 100, "files_with_matches")
			if err != nil || sfFiles != expectedSubB {
				t.Fatalf("%s: single-file files_with_matches = %q, want %q, err=%v", arm.name, sfFiles, expectedSubB, err)
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
		target    string
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
		// rg searches files in parallel and prints them in the order they
		// finish; the lines come back in path order, so the cap keeps the
		// same lines on every run (#3284).
		// The native walk lists a directory's files before a sibling file
		// whose name extends the directory's ("ctx/" before "ctx.txt"), so
		// paths compare one segment at a time.
		{name: "a directory before a sibling file sharing its prefix", stdout: dir + sep + "ctx.txt\x00" + dir + sep + "ctx" + sep + "m.txt\x00", filesOnly: true, want: []string{"ctx" + sep + "m.txt", "ctx.txt"}},
		{name: "files in the order rg finished them", stdout: dir + sep + "b.go\x00" + dir + sep + "a.go\x00", filesOnly: true, want: []string{"a.go", "b.go"}},
		{
			name:   "context groups in the order rg finished their files",
			stdout: dir + sep + "b.go\x001:foo\n" + dir + sep + "b.go\x002-x\n--\n" + dir + sep + "b.go\x009:foo\n--\n" + dir + sep + "a.go\x003:foo\n",
			want:   []string{"a.go:3:foo", "--", "b.go:1:foo", "b.go-2-x", "--", "b.go:9:foo"},
		},
		{name: "count", stdout: dir + sep + "a.go\x0012\n", want: []string{"a.go:12"}},
		{
			name:   "a name holding a newline",
			stdout: dir + sep + "a\nb.md\x001-x\n" + dir + sep + "a\nb.md\x002:foo\n--\n" + dir + sep + "c.md\x009:foo\n",
			want:   []string{`"a\nb.md"-1-x`, `"a\nb.md":2:foo`, "--", "c.md:9:foo"},
		},
		{name: "a name holding a newline, files with matches", stdout: dir + sep + "a\nb.md\x00", filesOnly: true, want: []string{`"a\nb.md"`}},
		{name: "a named file's matches", stdout: filepath.Join(dir, "a.go") + "\x00", target: filepath.Join(dir, "a.go"), oneFile: true, filesOnly: true, want: []string{"a.go"}},
		{name: "a named file's lines", stdout: "1:foo\n2:foo\n", oneFile: true, want: []string{"1:foo", "2:foo"}},
		{name: "a named file's count", stdout: "2\n", oneFile: true, want: []string{"2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := cmp.Or(tc.target, dir)
			if got := ripgrepOutputLines(tc.stdout, target, dir, tc.oneFile, tc.filesOnly); !slices.Equal(got, tc.want) {
				t.Fatalf("ripgrepOutputLines = %q, want %q", got, tc.want)
			}
		})
	}
}
