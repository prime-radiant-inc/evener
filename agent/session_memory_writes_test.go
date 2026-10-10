package agent

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// Writing, editing or deleting the root MEMORY.md, in any letter case, is
// refused; sub/MEMORY.md is an ordinary page.
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

// memory_write and memory_edit refuse a path holding an ASCII control
// character other than tab, in any segment, and change nothing; memory_delete
// still removes such a file, which can arrive from outside the tools, and
// memory_search names it as a JSON string on one line. A tab is an ordinary
// path character.
func TestMemoryControlCharacterPathsRefused(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	for name, searched := range map[string]string{
		"bad\nname.md": `"bad\nname.md"`,
		"dir\r/x.md":   `"dir\r/x.md"`,
		"bell\a.txt":   `"bell\u0007.txt"`,
		"del\x7f.md":   `"del\u007f.md"`,
	} {
		path := filepath.Join(scope, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		for tool, args := range map[string]map[string]any{
			"memory_write": {"scope": "personal", "file_path": name, "content": "y"},
			"memory_edit":  {"scope": "personal", "file_path": name, "old_string": "x", "new_string": "y"},
		} {
			res := memoryExec(t, s, tool, args)
			if !res.IsError || res.Output != errMemoryPathControlCharacter.Error() {
				t.Fatalf("%s %q: %+v", tool, name, res)
			}
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "x" {
			t.Fatalf("%q changed: %q, %v", name, raw, err)
		}
		for mode, want := range map[string]string{"files_with_matches": searched, "content": searched + ":1:x", "count": searched + ":1"} {
			if res := memoryExec(t, s, "memory_search", map[string]any{"scope": "personal", "pattern": "^x$", "output_mode": mode}); res.IsError || res.Output != want {
				t.Fatalf("search %s for %q: %+v, want %q", mode, name, res, want)
			}
		}
		if res := memoryExec(t, s, "memory_delete", map[string]any{"scope": "personal", "file_path": name}); res.IsError {
			t.Fatalf("delete %q: %+v", name, res)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%q still present after delete: %v", name, err)
		}
	}
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "tab\there.md", "content": "---\ndescription: tabbed\n---\n"}); res.IsError {
		t.Fatalf("tab refused: %+v", res)
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

// A written page whose frontmatter is not valid YAML gets a note asking to
// fix it.
func TestMemoryWriteNotesUnreadableFrontmatter(t *testing.T) {
	t.Parallel()
	s, _ := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "bad.md", "content": "---\ndescription: Fix: use cents\n---\n# Cents\n"})
	if res.IsError || !strings.HasSuffix(res.Output, memoryUnreadableFrontmatterNote) {
		t.Fatalf("%+v", res)
	}
}

// A page whose valid frontmatter can't take another key line (a flow
// mapping, or a block closed by "...") is left unstamped rather than made
// unreadable, and keeps its description.
func TestMemoryWriteLeavesUnextendableFrontmatterUnstamped(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	for name, body := range map[string]string{
		"flow.md":  "---\n{description: Flow page}\n---\n# Flow\n",
		"ended.md": "---\ndescription: Ended page\n...\n---\n# Ended\n",
	} {
		res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": name, "content": body})
		if res.IsError || strings.Contains(res.Output, "\n\n") {
			t.Fatalf("%s: %+v", name, res)
		}
		if raw, err := os.ReadFile(filepath.Join(scope, name)); err != nil || string(raw) != body {
			t.Fatalf("%s: got %q, %v", name, raw, err)
		}
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

// memory_edit applies its edit and sets the stamps in one write, and notes a
// page the edit leaves with no description.
func TestMemoryEditStampsInItsOwnWrite(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	page := filepath.Join(scope, "a.md")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("---\ndescription: old fact\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := memoryExec(t, s, "memory_edit", map[string]any{"scope": "personal", "file_path": "a.md", "old_string": "old fact", "new_string": "new fact"})
	if res.IsError || strings.Contains(res.Output, "no description") {
		t.Fatalf("%+v", res)
	}
	want := "---\ndescription: new fact\n" + memoryOwnStamps(s) + "---\nbody\n"
	if raw, _ := os.ReadFile(page); string(raw) != want {
		t.Fatalf("got %q\nwant %q", raw, want)
	}
	res = memoryExec(t, s, "memory_edit", map[string]any{"scope": "personal", "file_path": "a.md", "old_string": "description: new fact\n", "new_string": ""})
	if res.IsError || !strings.HasSuffix(res.Output, memoryMissingDescriptionNote) {
		t.Fatalf("%+v", res)
	}
}

// memory_search never reports the physical hand-written root index that a
// session without memory_write leaves unmigrated: memory_read renders the
// generated index under that name instead. Files whose names merely start
// like the index's are still searched, and the index never uses up a
// search's result cap.
func TestMemorySearchSkipsTheLegacyRootIndex(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	s.reg.Remove("memory_write")
	refreshModelFacingCaches(s)
	if err := os.MkdirAll(filepath.Join(scope, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"MEMORY.md":            "- [p](p.md) — opaque-needle\n",
		"MEMORY.md-1-notes.md": "opaque-needle\n",
		"memory.md-notes.md":   "opaque-needle\n",
		"p.md":                 "---\ndescription: d\n---\nopaque-needle\n",
		"sub/MEMORY.md":        "opaque-needle\n",
	} {
		if err := os.WriteFile(filepath.Join(scope, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	search := func(extra map[string]any) string {
		t.Helper()
		args := map[string]any{"scope": "personal", "pattern": "opaque-needle"}
		maps.Copy(args, extra)
		res := memoryExec(t, s, "memory_search", args)
		if res.IsError {
			t.Fatalf("%v: %+v", extra, res)
		}
		return res.Output
	}
	files := []string{"MEMORY.md-1-notes.md", "memory.md-notes.md", "p.md", filepath.Join("sub", "MEMORY.md")}
	if got := strings.Split(search(map[string]any{"output_mode": "files_with_matches"}), "\n"); !slices.Equal(got, files) {
		t.Fatalf("files: got %q, want %q", got, files)
	}
	if got := search(map[string]any{"output_mode": "count"}); got != strings.Join(files, ":1\n")+":1" {
		t.Fatalf("count: %q", got)
	}
	if got := search(map[string]any{"output_mode": "content", "context_lines": 1}); strings.Contains(got, "[p](p.md)") {
		t.Fatalf("content: root index reported: %q", got)
	}
	if got := search(map[string]any{"output_mode": "files_with_matches", "max_results": float64(1)}); got != files[0] {
		t.Fatalf("capped: got %q, want %q", got, files[0])
	}
	if got := search(map[string]any{"path": "MEMORY.md"}); got != "" {
		t.Fatalf("search of the root index itself: %q", got)
	}
	// A confined scope searches one named page (#4137).
	if got := search(map[string]any{"path": "p.md", "output_mode": "files_with_matches"}); got != "." {
		t.Fatalf("search of one named page: %q", got)
	}
	// Naming the index still checks the pattern and the glob's braces.
	for _, bad := range []map[string]any{{"pattern": "["}, {"glob_filter": "{a,b"}} {
		args := map[string]any{"scope": "personal", "pattern": "opaque-needle", "path": "memory.md"}
		maps.Copy(args, bad)
		if res := memoryExec(t, s, "memory_search", args); !res.IsError {
			t.Fatalf("%v over the root index answered %+v", bad, res)
		}
	}
	if err := os.Remove(filepath.Join(scope, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	if got := search(map[string]any{"path": "Memory.md"}); got != "" {
		t.Fatalf("search of a missing root index: %q", got)
	}
}
