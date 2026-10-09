package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

func memoryWritesSession(t *testing.T) (*Session, string) {
	t.Helper()
	root := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, clock: agenttest.NewFakeClock()}))
	return s, filepath.Join(root, "memory", "personal")
}

// memoryOwnStamps is the frontmatter a session's own write of a page sets:
// today's updated date and the session's id.
func memoryOwnStamps(s *Session) string {
	return "updated: " + s.sclock().Now().UTC().Format(time.DateOnly) + "\n" + memoryYAMLField("by", s.ID())
}

// Review Focus 3.
func TestMemoryIndexWritesRefused(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	for _, name := range []string{"MEMORY.md", "./MEMORY.md", "memory.md"} {
		for tool, args := range map[string]map[string]any{
			"memory_write":  {"scope": "personal", "file_path": name, "content": "x"},
			"memory_edit":   {"scope": "personal", "file_path": name, "old_string": "x", "new_string": "y"},
			"memory_delete": {"scope": "personal", "file_path": name},
		} {
			res := memoryExec(t, s, tool, args)
			if !res.IsError || !strings.Contains(res.Output, "MEMORY.md is generated from each page's frontmatter; edit a page's description or tags instead") {
				t.Fatalf("%s %s: %+v", tool, name, res)
			}
		}
	}
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "sub/MEMORY.md", "content": "---\ndescription: nested\n---\n"}); res.IsError {
		t.Fatalf("sub/MEMORY.md is a page: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(scope, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("root MEMORY.md exists: %v", err)
	}
}

// A successful write or edit of a page stamps updated and by and keeps every
// other byte; a failed edit stamps nothing.
func TestMemoryWriteStampsThePage(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	body := "---\ndescription: Money is integer cents\nodd:   kept  \n---\n# Cents\nbody\n"
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "cents.md", "content": body}); res.IsError {
		t.Fatal(res.Output)
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "cents.md"))
	want := "---\ndescription: Money is integer cents\nodd:   kept  \n" + memoryOwnStamps(s) + "---\n# Cents\nbody\n"
	if string(raw) != want {
		t.Fatalf("got %q\nwant %q", raw, want)
	}
	before := string(raw)
	if res := memoryExec(t, s, "memory_edit", map[string]any{"scope": "personal", "file_path": "cents.md", "old_string": "absent", "new_string": "y"}); !res.IsError {
		t.Fatal("edit of an absent string succeeded")
	}
	if raw, _ := os.ReadFile(filepath.Join(scope, "cents.md")); string(raw) != before {
		t.Fatalf("failed edit changed the page: %q", raw)
	}
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "notes.txt", "content": "plain"}); res.IsError {
		t.Fatal(res.Output)
	}
	if raw, _ := os.ReadFile(filepath.Join(scope, "notes.txt")); string(raw) != "plain" {
		t.Fatalf("non-Markdown file stamped: %q", raw)
	}
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": ".drafts/x.md", "content": "draft"}); res.IsError {
		t.Fatal(res.Output)
	}
	if raw, _ := os.ReadFile(filepath.Join(scope, ".drafts", "x.md")); string(raw) != "draft" {
		t.Fatalf("dot path stamped: %q", raw)
	}
}

func TestMemoryWriteNotesMissingDescription(t *testing.T) {
	t.Parallel()
	s, _ := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "a.md", "content": "# A heading\n"})
	if res.IsError || !strings.HasSuffix(res.Output, memoryMissingDescriptionNote) {
		t.Fatalf("%+v", res)
	}
	res = memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "b.md", "content": "---\ndescription: has one\n---\n"})
	if strings.Contains(res.Output, "no description") {
		t.Fatalf("described page noted: %q", res.Output)
	}
}

// Review Focus 1.
func TestMemoryWriteNotesUnreadableFrontmatter(t *testing.T) {
	t.Parallel()
	s, _ := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "bad.md", "content": "---\ndescription: Fix: use cents\n---\n# Cents\n"})
	if res.IsError || !strings.HasSuffix(res.Output, memoryUnreadableFrontmatterNote) {
		t.Fatalf("%+v", res)
	}
}

// memory_read of MEMORY.md renders the whole index, paged by offset and
// limit; with no pages it says so.
func TestMemoryReadRendersTheIndex(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	if res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"}); res.IsError || res.Output != "This scope has no pages yet." {
		t.Fatalf("%+v", res)
	}
	for i := range 300 {
		path := filepath.Join(scope, "p", string(rune('a'+i%26))+strings.Repeat("x", i/26)+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\ndescription: opaque page\ntags: [t]\nupdated: 2026-10-01\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"})
	if res.IsError || !strings.HasPrefix(res.Output, "   1\tTags: t (300)\n") || strings.Count(res.Output, "\n") != 301 || strings.Contains(res.Output, "Not shown") {
		t.Fatalf("full index: %d lines, %.200q", strings.Count(res.Output, "\n"), res.Output)
	}
	paged := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "offset": 300, "limit": 5})
	if paged.IsError || !strings.HasPrefix(paged.Output, " 300\t- [") || strings.Count(paged.Output, "\n") != 2 {
		t.Fatalf("paged: %q", paged.Output)
	}
	// memory_search never matches the virtual index.
	if res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "pattern": `\(updated 2026`}); strings.Contains(res.Output, "MEMORY.md") || strings.Contains(res.Output, "(updated") {
		t.Fatalf("search matched the virtual index: %q", res.Output)
	}
}

// Stamping a page that already carries this session's stamps for today leaves
// the file untouched, so its modification time stays.
func TestMemoryStampSkipsAnUnchangedPage(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "a.md", "content": "---\ndescription: d\n---\n"}); res.IsError {
		t.Fatal(res.Output)
	}
	page := filepath.Join(scope, "a.md")
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(page, old, old); err != nil {
		t.Fatal(err)
	}
	env, release, err := s.acquireMemoryEnvironment("personal")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if notes := s.stampMemoryPage(env, "a.md"); notes != "" {
		t.Fatalf("notes=%q", notes)
	}
	if info, err := os.Stat(page); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("page rewritten: %v, %v", info.ModTime(), err)
	}
}

// memory_search never reports the physical hand-written root index that a
// session without memory_write leaves unmigrated: memory_read renders the
// generated index under that name instead.
func TestMemorySearchSkipsTheLegacyRootIndex(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	s.reg.Remove("memory_write")
	refreshModelFacingCaches(s)
	if err := os.MkdirAll(filepath.Join(scope, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"MEMORY.md":          "- [p](p.md) — opaque-needle\n",
		"p.md":               "---\ndescription: d\n---\nopaque-needle\n",
		"sub/MEMORY.md":      "opaque-needle\n",
		"memory.md-notes.md": "opaque-needle\n",
	} {
		if err := os.WriteFile(filepath.Join(scope, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"content", "files_with_matches", "count"} {
		res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "pattern": "opaque-needle", "output_mode": mode, "context_lines": 1})
		if res.IsError {
			t.Fatalf("%s: %+v", mode, res)
		}
		for line := range strings.Lines(res.Output) {
			if strings.HasPrefix(strings.ToLower(line), "memory.md:") || strings.HasPrefix(strings.ToLower(line), "memory.md-1") || strings.TrimSpace(line) == "MEMORY.md" {
				t.Fatalf("%s: root index reported: %q", mode, res.Output)
			}
		}
		for _, want := range []string{"p.md", filepath.Join("sub", "MEMORY.md"), "memory.md-notes.md"} {
			if !strings.Contains(res.Output, want) {
				t.Fatalf("%s: %s missing from %q", mode, want, res.Output)
			}
		}
	}
	if res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "path": "MEMORY.md", "pattern": "opaque-needle"}); res.IsError || res.Output != "" {
		t.Fatalf("search of the root index itself: %+v", res)
	}
}

// Dropping the root index's lines from a search leaves no "--" group
// separator leading, trailing or doubled, and keeps every other file's
// lines, including names that start like the index's.
func TestWithoutLegacyIndexLines(t *testing.T) {
	t.Parallel()
	for out, want := range map[string]string{
		"MEMORY.md:1:x\nMEMORY.md-2-\n--\na.md:1:x":                "a.md:1:x",
		"a.md:1:x\n--\nmemory.md-4-y\nmemory.md:5:x\n--\nb.md:2:x": "a.md:1:x\n--\nb.md:2:x",
		"a.md:1:x\n--\nMemory.md:9:x":                              "a.md:1:x",
		"MEMORY.md\nMEMORY.md-notes.md\nsub/MEMORY.md":             "MEMORY.md-notes.md\nsub/MEMORY.md",
		"MEMORY.md:3\np.md:1":                                      "p.md:1",
		"":                                                         "",
	} {
		if got := withoutLegacyIndexLines(out); got != want {
			t.Fatalf("%q: got %q, want %q", out, got, want)
		}
	}
}
