package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The returned filename must be directly usable by memory_read, even when
// another page at the scope root has the same basename and search pattern.
func TestMemorySearchPathsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"personal", "project"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}))
			pages := map[string]string{
				"page.md":         "opaque-search-hit root-decoy",
				"sub/page.md":     "opaque-search-hit nested-target",
				"sibling/page.md": "opaque-search-hit sibling-decoy",
			}
			for name, body := range pages {
				memorySeedSearchPath(t, s, scope, name, body+"\n")
			}
			for _, tc := range []struct {
				path string
				want []string
			}{
				{"sub", []string{"sub/page.md"}},
				{"./sub", []string{"sub/page.md"}},
				{"sub/page.md", []string{"sub/page.md"}},
				{"./sub/page.md", []string{"sub/page.md"}},
				{"page.md", []string{"page.md"}},
				{"./page.md", []string{"page.md"}},
				{"", []string{"page.md", "sibling/page.md", "sub/page.md"}},
				{".", []string{"page.md", "sibling/page.md", "sub/page.md"}},
			} {
				t.Run(tc.path, func(t *testing.T) {
					res := memoryExec(t, s, "memory_search", map[string]any{"scope": scope, "pattern": "opaque-search-hit", "path": tc.path, "output_mode": "files_with_matches"})
					if res.IsError {
						t.Fatal(res.Output)
					}
					got := strings.Split(res.Output, "\n")
					if !slices.Equal(got, tc.want) {
						t.Fatalf("search path=%q filenames=%q, want %q", tc.path, got, tc.want)
					}
					for _, name := range got {
						read := memoryExec(t, s, "memory_read", map[string]any{"scope": scope, "file_path": name})
						if read.IsError || !strings.Contains(read.Output, pages[name]) {
							t.Fatalf("reading returned filename %q: %+v", name, read)
						}
					}
				})
			}
		})
	}
}

// These are grep's machine-readable path, line-number and count fields, not
// prose snapshots. Page text deliberately quotes a host path and a grep-like
// prefix so a whole-result string replacement cannot satisfy the test.
func TestMemorySearchPathsAllModes(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	quoted := "see " + filepath.Join(s.memoryScopeRoot("personal"), "sub", "page.md")
	body := "opaque-before\nopaque-search-hit " + quoted + "\nopaque-after\n"
	memorySeedSearchPath(t, s, "personal", "sub/page.md", body)
	memorySeedSearchPath(t, s, "personal", "page.md", "opaque-search-hit root-decoy\n")
	for _, path := range []string{"sub", "./sub", "sub/page.md", "./sub/page.md"} {
		for mode, want := range map[string]string{
			"files_with_matches": "sub/page.md",
			"count":              "sub/page.md:1",
			"content":            "sub/page.md-1-opaque-before\nsub/page.md:2:opaque-search-hit " + quoted + "\nsub/page.md-3-opaque-after",
		} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "path": path, "pattern": "opaque-search-hit", "output_mode": mode, "context_lines": 1})
				if res.IsError || res.Output != want {
					t.Fatalf("search=%+v, want %q", res, want)
				}
			})
		}
	}
	for mode, want := range map[string]string{
		"files_with_matches": "page.md",
		"count":              "page.md:1",
		"content":            "page.md:1:opaque-search-hit root-decoy",
	} {
		res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "path": "./page.md", "pattern": "opaque-search-hit", "output_mode": mode})
		if res.IsError || res.Output != want {
			t.Fatalf("root file %s search=%+v, want %q", mode, res, want)
		}
	}
}

func TestMemorySearchPathsQuotedFilenames(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	name := "sub\nquoted/page\n.md"
	memorySeedSearchPath(t, s, "personal", name, "opaque-quoted-hit\n")
	for _, path := range []string{"sub\nquoted", name} {
		res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "path": path, "pattern": "opaque-quoted-hit", "output_mode": "files_with_matches"})
		var got string
		if res.IsError || json.Unmarshal([]byte(res.Output), &got) != nil || got != name {
			t.Fatalf("quoted search path=%q: %+v decoded=%q", path, res, got)
		}
		read := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": got})
		if read.IsError || !strings.Contains(read.Output, "opaque-quoted-hit") {
			t.Fatalf("read quoted result: %+v", read)
		}
	}
}

func TestMemorySearchPathsPreserveSelectionAndLimits(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	memorySeedSearchPath(t, s, "personal", "sub/page.md", "opaque-hit\npadding\npadding\npadding\nopaque-hit\n")
	memorySeedSearchPath(t, s, "personal", "sub/other.txt", "opaque-hit\n")
	memorySeedSearchPath(t, s, "personal", "page.md", "opaque-hit root-decoy\n")
	args := map[string]any{"scope": "personal", "path": "sub", "pattern": "opaque-hit", "glob_filter": "*.md", "context_lines": 1}
	full := memoryExec(t, s, "memory_search", args)
	want := "sub/page.md:1:opaque-hit\nsub/page.md-2-padding\n--\nsub/page.md-4-padding\nsub/page.md:5:opaque-hit"
	if full.IsError || full.Output != want {
		t.Fatalf("filtered context search=%+v, want %q", full, want)
	}
	args["max_results"] = 2
	limited := memoryExec(t, s, "memory_search", args)
	rows, note, separated := strings.Cut(limited.Output, "\n\n")
	if limited.IsError || rows != strings.Join(strings.Split(full.Output, "\n")[:2], "\n") || !separated || note == "" {
		t.Fatalf("limited search=%+v", limited)
	}
}

// A file above the 16 MiB directory read cap takes a separate streaming
// renderer. Its filenames must follow the same memory-only contract.
func TestMemorySearchPathsStreamedFile(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: t.TempDir()}))
	body := "opaque-before\nopaque-stream-hit\nopaque-after\n" + strings.Repeat("padding\n", (17<<20)/len("padding\n"))
	memorySeedSearchPath(t, s, "personal", "sub/large.txt", body)
	for mode, want := range map[string]string{
		"files_with_matches": "sub/large.txt",
		"count":              "sub/large.txt:1",
		"content":            "sub/large.txt-1-opaque-before\nsub/large.txt:2:opaque-stream-hit\nsub/large.txt-3-opaque-after",
	} {
		t.Run(mode, func(t *testing.T) {
			res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "path": "./sub/large.txt", "pattern": "opaque-stream-hit", "output_mode": mode, "context_lines": 1})
			if res.IsError || res.Output != want {
				t.Fatalf("streamed search=%+v, want %q", res, want)
			}
		})
	}
}

func memorySeedSearchPath(t *testing.T, s *Session, scope, name, body string) {
	t.Helper()
	memoryEvalWrite(t, filepath.Join(s.memoryScopeRoot(scope), name), body)
}
