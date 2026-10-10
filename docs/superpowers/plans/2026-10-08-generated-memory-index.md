# Generated Memory Index Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace each memory scope's hand-written `MEMORY.md` with an index Evener renders from page frontmatter (description, tags, stamps), projected into context under the existing 8 KiB budget and per-turn change machinery.

**Architecture:** A pure page model (`memory_page.go`) turns each page's bytes into an index entry; a pure renderer (`memory_index_render.go`) builds the full index and its budgeted projection; a one-time migration (`memory_migrate.go`) moves old index lines into page frontmatter. `session_memory.go` swaps its single-file read for "migrate, list, render", keeps the full rendering as the change baseline, and `session_tools_memory.go` refuses writes to `MEMORY.md`, stamps written pages and serves `memory_read("MEMORY.md")` from the renderer.

**Tech Stack:** Go (agent module: `agent/`, `agent/execenv`, `agent/internal/frontmatter` over `gopkg.in/yaml.v3`), `internal/apptranscript` (root module), `cmd/evener-tui`, Python 3 for the memory lab.

**Spec:** `docs/superpowers/specs/2026-10-08-generated-memory-index-design.md`

## Global Constraints

- Land as a stack of small PRs, one per task (Tasks 1–7), each 80–150 non-test lines and one mechanism. Open every PR with base `main`. After a predecessor squash-merges, `git fetch origin main && git rebase --onto origin/main <predecessor-branch-tip> <branch>`, confirm your own lines are unchanged, `git push --force-with-lease`. Regular PRs, never drafts.
- Do not cut a release tag between Task 1 and Task 6. Tasks 4 and 5 leave the prompt still telling agents to edit `MEMORY.md` until Task 6 lands; that intermediate state must not ship.
- TDD per task: write the failing test, run it and see it fail for the stated reason, implement, see it pass. No test may string-match a source file. Prompt and tool-description text is pinned only by the whole-output golden snapshot Task 6 adds (`agent/testdata/memoryprompt/*.md`); the repo has no system-prompt golden today.
- Go gates, in every touched module (`agent/`, and the root module for `internal/apptranscript` and `cmd/evener-tui`), before every push:
  - `$(go env GOROOT)/bin/gofmt -l .` prints nothing (never the Homebrew gofmt on PATH)
  - `go vet ./...` and `go vet -tags evenerfuzz ./...`
  - `golangci-lint run --allow-serial-runners ./...`
  - the touched packages' tests: `go test -count=1 ./agent/...` from `agent/` scoped to the packages you touched, plus `go test -count=1 ./internal/apptranscript ./cmd/evener-tui ./cmd/evener-hub` from the root when Task 4 or 5 touches them.
- Any change to what the appwire projector emits needs a `cmd/evener-tui` test case in the same PR (Task 4).
- No task touches `make/*.mk`. If one ever does, run `go test -short -count=1 .` at the root.
- Evener prompt, tool-description and skill text says "your human partner", never "the user".
- No backward-compatibility shims beyond the spec's migration. The one exception that already exists stays: `internal/apptranscript` keeps decoding transcripts recorded by earlier builds (old size sentences), as it does today.
- Windows is unsupported: no Windows work, no Windows findings.
- Gate on piped commands with `set -o pipefail`. Check `git diff --cached --stat` before every commit.
- Never delete anything outside your own worktree or scratch files.

## Spec rulings this plan makes

The spec delegated these calls to the implementer, or left them open. Each is fixed here so tasks agree.

1. **No migration lock.** The spec says "under the scope's existing memory lock", but no cross-session memory lock exists (only the in-process `memoryMu`). Migration is made safe without one: two migrators read the same old index and write the same descriptions, a page that already has a description is skipped, and a rename that finds `MEMORY.md` gone (another migrator won) counts as done. A lock would only serialize migrators against each other, which idempotence already covers; agent writes would not take it either.
2. **The change baseline is the full rendering, not the budgeted projection.** Diffing the projection would report the oldest line falling off the budget as `- line`, which the spec reserves for a deleted page. `memoryProjection` gains an `Index` field (full rendering); the baseline and change blocks use it, `Content` stays what the model is shown.
3. **A scope with no pages has status `missing`.** The existing suppression of a first missing/empty observation carries over unchanged. `memory_read("MEMORY.md")` on such a scope returns `This scope has no pages yet.`
4. **`by` is the full session id** (`s.ID()`, a prefix-less UUIDv7). Evener names root sessions by that id; there is no `session-` prefix.
5. **Truncation sentence.** The "Not shown" line lives inside the quoted index content (the spec excludes it from diffs, so it is content). The envelope's size sentence becomes one fixed sentence, `memoryIndexPartial = " Not every page is shown; the index's last line counts the rest."`, decoded as `truncated: true`. The gardening-skill variant goes away from the producer; the parser keeps accepting both old sentences for recorded transcripts.
6. **"Not shown" tag order:** count descending, then tag name ascending, `untagged` last. `1 page` is singular.
7. **Sort date of an unstamped page** is its modification time's UTC date (`YYYY-MM-DD`); ties order by path. An `updated` that is neither a YAML date nor a `YYYY-MM-DD` string is ignored (no date shown).
8. **Migration targets:** only `.md` pages in the scope; links with `://` are skipped; `#anchor` and a leading `./` are stripped; a target that is not local is skipped; the first line naming a page wins; an empty remainder uses the link text; pages whose frontmatter does not parse are skipped (their index line already flags them). Separators are trimmed from both ends of the remainder.
9. **Migration failure never blocks rendering;** the next rendering retries.
10. **Only page paths are stamped:** `.md` files that count as pages (no dot segment, not the root `MEMORY.md`). The `MEMORY.md` refusal is case-insensitive (`memory.md` names the same file on macOS's default filesystem), and the error string drops the spec's final period (revive's error-strings rule).
11. **Stamps:** `updated: YYYY-MM-DD` is written unquoted (a YAML date, read back as `time.Time`); `by` and migrated `description` are YAML-encoded with `yaml.Marshal`, which quotes values that need it. A stamp that fails to write does not fail the tool call; its result ends with a note.
12. **Markers:** a page whose frontmatter does not parse renders `<fallback> (no description) (frontmatter unreadable)`. A non-`.md` file, or a page that cannot be read, renders with its filename as the description and no `(no description)` suffix. A `description` that is not a YAML string counts as missing. A written page whose frontmatter does not parse gets its own note (the spec's missing-description note would mislead).
13. **Over-budget header:** if the tag header plus a "Not shown" line for every page exceeds 8 KiB, that text is cut at a UTF-8 boundary at 8 KiB, still truncated.
14. **Memory lab seeds:** the lab has no per-version seeds, and this plan adds none. Existing seeded scenarios keep their hand-written `MEMORY.md` seeds: the base build reads them, the new build migrates them. `index-overflow` seeds frontmatter pages and a hand-written `MEMORY.md`, so the base build gets an index too.

## Review Focus

1. **An agent writes frontmatter YAML that does not parse** (`description: Fix: use cents`, unquoted colon). Expect the page still listed with its fallback and `(frontmatter unreadable)`, and the write result telling the agent to fix it. Tests: Task 1 `TestParseMemoryPage` row "unparseable frontmatter"; Task 5 `TestMemoryWriteNotesUnreadableFrontmatter`.
2. **One page whose description alone is larger than the budget** (an agent pastes a 9 KB description). Expect the projection to stay within 8 KiB, the page counted in "Not shown", never a cut mid-line. Test: Task 2 `TestProjectMemoryIndexHugeLine`.
3. **`MEMORY.md` named another way** (`./MEMORY.md`, `memory.md`), and `sub/MEMORY.md`. Expect the first two refused for write, edit and delete; `sub/MEMORY.md` an ordinary page. Test: Task 5 `TestMemoryIndexWritesRefused`.
4. **An old index with hostile or broken lines:** links to missing pages, `https://` links, `../escape.md`, anchors, duplicate links, lines with no link. Expect migration to skip them, describe only real pages, and still rename the file. Tests: Task 3 `TestParseLegacyMemoryIndex`, `TestMigrateMemoryScope`.
5. **Scopes holding non-page files:** dot directories, a symlink, `notes.txt`, an empty `.md`, a nested `sub/MEMORY.md`, the `.MEMORY.md.pre-generated` backup. Expect only pages listed, the symlink and dot paths skipped, the empty page described as `(no description)`. Test: Task 1 `TestListMemoryPages`.

---

## File map

| File | Task | Responsibility |
| --- | --- | --- |
| `agent/execenv/execenv.go`, `local.go`, `securepath_fdops_unix.go` | 1 | `DirEntry.ModTime` from both directory walks |
| `agent/memory_page.go` (new) | 1 | page entry from bytes; scope listing |
| `agent/memory_index_render.go` (new) | 2 | full index; budgeted projection; "Not shown" line |
| `agent/memory_migrate.go` (new) | 3 | frontmatter field setter; legacy index parse; migration run |
| `agent/session_memory.go` | 4 | projection, baseline and own-write recording on the rendered index |
| `internal/apptranscript/memory_context.go` | 4 | decode the new size sentence |
| `agent/memory_context_wire_fixture_test.go`, `agent/testdata/memorycontextwire/events.json`, `cmd/evener-tui/hub_transcript_widgets_test.go` | 4 | regenerated corpus and TUI case |
| `agent/session_tools_memory.go`, `agent/execenv/local.go` | 5 | refusals, stamping, notes, virtual read; `execenv.NumberLines` |
| `agent/prompts/system.md.tmpl`, `agent/internal/tool/definitions.go`, `internal/bundled/skills/gardening-memory/SKILL.md`, `docs/product/memory.md`, `docs/tools/memory.md` | 6 | guidance, tool text, skill, docs |
| `agent/memory_prompt_golden_test.go` (new), `agent/testdata/memoryprompt/` (new) | 6 | whole-output prompt golden |
| `tools/prompt-eval/memory-lab/scenarios/index-overflow/` (new), three scenario.json edits, `README.md` | 7 | eval scenario |

---

### Task 1: Page model and scope listing

**PR boundary:** PR 1, branch `claude/memory-index-1-pages`. Estimated non-test lines: ~140 (`memory_page.go` ~125, execenv ~15). Nothing calls the new code yet except tests.

**Files:**
- Modify: `agent/execenv/execenv.go:23-29` (`DirEntry`)
- Modify: `agent/execenv/local.go:1848-1916` (`ListDirectory` unsandboxed walk)
- Modify: `agent/execenv/securepath_fdops_unix.go:30,38-44,606` (`secureEntryInfo`, `fstatatEntryInfo`, `walkDirFd`)
- Modify: `agent/execenv/runtime_edges_fuzz_test.go:260,269` (stub signatures)
- Create: `agent/memory_page.go`
- Test: `agent/execenv/list_directory_modtime_test.go`, `agent/memory_page_test.go`

**Interfaces:**
- Consumes: `frontmatter.Parse(raw string) (frontmatter.Document, error)`; `(*execenv.LocalExecutionEnvironment).ListDirectory(path string, depth int) ([]execenv.DirEntry, error)`, `.ReadFileRaw(path string) ([]byte, error)`, `.WorkingDirectory() string`.
- Produces:
  - `execenv.DirEntry.ModTime time.Time` (`json:"-"`)
  - `type memoryPage struct { Path, Title, Description string; HasDescription, Unreadable bool; Tags []string; Updated string; ModTime time.Time }`
  - `func parseMemoryPage(rel string, raw []byte, modTime time.Time) memoryPage`
  - `func normalizeMemoryTags(v any) []string`
  - `func splitMemoryFrontmatter(text string) (block, body string, ok bool)`
  - `func isMemoryPagePath(rel string) bool`
  - `func listMemoryPages(env *execenv.LocalExecutionEnvironment) ([]memoryPage, error)`
  - constants `memoryNoDescription = "(no description)"`, `memoryFrontmatterUnreadable = "(frontmatter unreadable)"`

- [ ] **Step 1: Write the failing execenv test**

`agent/execenv/list_directory_modtime_test.go`:

```go
package execenv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Both directory walks report each file's modification time, which the
// memory index uses to order pages that carry no updated stamp.
func TestListDirectoryReportsModTime(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	seed := func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "sub", "page.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, entries []DirEntry) {
		t.Helper()
		for _, entry := range entries {
			if entry.Name == filepath.Join("sub", "page.md") {
				if !entry.ModTime.Equal(stamp) {
					t.Fatalf("ModTime=%v want %v", entry.ModTime, stamp)
				}
				return
			}
		}
		t.Fatalf("page missing from %+v", entries)
	}
	t.Run("confined", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		env, err := NewConfinedFileEnvironment(root, "scope")
		if err != nil {
			t.Fatal(err)
		}
		defer env.Cleanup()
		seed(t, filepath.Join(root, "scope"))
		entries, err := env.ListDirectory(env.WorkingDirectory(), 4)
		if err != nil {
			t.Fatal(err)
		}
		check(t, entries)
	})
	t.Run("unsandboxed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		seed(t, dir)
		entries, err := NewLocalExecutionEnvironment(dir).ListDirectory(dir, 4)
		if err != nil {
			t.Fatal(err)
		}
		check(t, entries)
	})
}
```

- [ ] **Step 2: Run it and see it fail**

Run (from `agent/`): `go test -count=1 -run TestListDirectoryReportsModTime ./execenv`
Expected: compile error `entry.ModTime undefined`.

- [ ] **Step 3: Add `ModTime` to both walks**

`execenv.go` `DirEntry` gains (import `time`):

```go
	// ModTime is the file's modification time. It is not part of list_dir's
	// output; the memory index orders unstamped pages by it.
	ModTime time.Time `json:"-"`
```

`securepath_fdops_unix.go`: change `fstatatEntryInfo` and the `secureEntryInfo` var to return the time as well (`unix.Stat_t` has `Mtim` on both darwin and linux; confirm with `GOOS=darwin go doc golang.org/x/sys/unix Stat_t` and `GOOS=linux ...`):

```go
func fstatatEntryInfo(dirFd int, name string) (int64, os.FileMode, time.Time, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dirFd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, 0, time.Time{}, err
	}
	return st.Size, os.FileMode(st.Mode & 0o777), time.Unix(st.Mtim.Unix()), nil
}
```

and in `walkDirFd`:

```go
			if size, mode, modTime, ierr := secureEntryInfo(dirFd, name); ierr == nil {
				de.Size = size
				de.ModTime = modTime
```

In `local.go`'s unsandboxed walk, beside `de.Size = info.Size()`: `de.ModTime = info.ModTime()`.

Update the two stubs in `runtime_edges_fuzz_test.go` to the new signature, e.g. `func(int, string) (int64, os.FileMode, time.Time, error) { return 0, 0, time.Time{}, fs.ErrPermission }`.

- [ ] **Step 4: Run it and see it pass**

Run: `go test -count=1 -run 'TestListDirectoryReportsModTime' ./execenv && go vet -tags evenerfuzz ./execenv`
Expected: PASS, vet clean.

- [ ] **Step 5: Write the failing page-model tests**

`agent/memory_page_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

func TestParseMemoryPage(t *testing.T) {
	t.Parallel()
	mod := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, rel, raw string
		want           memoryPage
	}{
		{"full frontmatter", "cents.md",
			"---\ndescription: Money is integer cents, never floats\ntags: [Money, formatting, money]\nevidence: file:shop/price.go:Format\nupdated: 2026-10-08\nby: s1\nextra: kept\n---\n# Integer cents\nPrices are cents.\n",
			memoryPage{Path: "cents.md", Title: "Integer cents", Description: "Money is integer cents, never floats", HasDescription: true, Tags: []string{"money", "formatting"}, Updated: "2026-10-08", ModTime: mod}},
		{"multi-line description collapses", "a.md",
			"---\ndescription: |-\n  line one\n  line two\n---\nbody\n",
			memoryPage{Path: "a.md", Title: "a", Description: "line one line two", HasDescription: true, ModTime: mod}},
		{"single string tag, whitespace runs", "a.md",
			"---\ndescription: d\ntags: \"  Read Only   Vitest \"\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Tags: []string{"read-only-vitest"}, ModTime: mod}},
		{"non-string tags formatted, nested dropped, empty dropped", "a.md",
			"---\ndescription: d\ntags: [2026, \"\", {x: 1}, true]\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Tags: []string{"2026", "true"}, ModTime: mod}},
		{"string updated", "a.md",
			"---\ndescription: d\nupdated: \"2026-10-07\"\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Updated: "2026-10-07", ModTime: mod}},
		{"non-date updated ignored", "a.md",
			"---\ndescription: d\nupdated: yesterday\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, ModTime: mod}},
		{"missing description falls back to heading", "sub/loader.md",
			"# Vitest read-only loader\n\nUse --configLoader runner.\n",
			memoryPage{Path: "sub/loader.md", Title: "Vitest read-only loader", Description: "Vitest read-only loader " + memoryNoDescription, ModTime: mod}},
		{"non-string description counts as missing", "a.md",
			"---\ndescription: 42\n---\nfirst line\n",
			memoryPage{Path: "a.md", Title: "a", Description: "first line " + memoryNoDescription, ModTime: mod}},
		{"fallback to first non-blank line", "a.md",
			"\n\n  first real line  \nsecond\n",
			memoryPage{Path: "a.md", Title: "a", Description: "first real line " + memoryNoDescription, ModTime: mod}},
		{"empty page", "empty.md", "",
			memoryPage{Path: "empty.md", Title: "empty", Description: memoryNoDescription, ModTime: mod}},
		{"unparseable frontmatter", "bad.md",
			"---\ndescription: Fix: use cents\n---\n# Cents rule\n",
			memoryPage{Path: "bad.md", Title: "Cents rule", Description: "Cents rule " + memoryNoDescription, Unreadable: true, ModTime: mod}},
		{"non-markdown file", "notes.txt", "anything",
			memoryPage{Path: "notes.txt", Title: "notes", Description: "notes.txt", ModTime: mod}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseMemoryPage(tc.rel, []byte(tc.raw), mod)
			if got.Path != tc.want.Path || got.Title != tc.want.Title || got.Description != tc.want.Description ||
				got.HasDescription != tc.want.HasDescription || got.Unreadable != tc.want.Unreadable ||
				!slices.Equal(got.Tags, tc.want.Tags) || got.Updated != tc.want.Updated || !got.ModTime.Equal(tc.want.ModTime) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// The fallback description is cut to 120 characters (runes, not bytes).
func TestParseMemoryPageFallbackCut(t *testing.T) {
	t.Parallel()
	line := ""
	for range 130 {
		line += "é"
	}
	got := parseMemoryPage("a.md", []byte(line+"\n"), time.Time{})
	want := string([]rune(line)[:120]) + " " + memoryNoDescription
	if got.Description != want {
		t.Fatalf("got %q want %q", got.Description, want)
	}
}

func TestListMemoryPages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory", "personal")
	for rel, body := range map[string]string{
		"a.md":                      "---\ndescription: alpha\n---\n",
		"sub/b.md":                  "# Bravo\n",
		"sub/MEMORY.md":             "nested index is a page\n",
		"empty.md":                  "",
		"notes.txt":                 "plain",
		"MEMORY.md":                 "root index is never a page\n",
		".MEMORY.md.pre-generated":  "backup\n",
		".hidden/c.md":              "hidden\n",
		"sub/.draft.md":             "hidden\n",
	} {
		path := filepath.Join(scope, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(scope, "a.md"), filepath.Join(scope, "link.md")); err != nil {
		t.Fatal(err)
	}
	pages, err := listMemoryPages(env)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range pages {
		paths = append(paths, p.Path)
	}
	slices.Sort(paths)
	want := []string{"a.md", "empty.md", "notes.txt", "sub/MEMORY.md", "sub/b.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("pages=%v want %v", paths, want)
	}
	for _, p := range pages {
		if p.ModTime.IsZero() {
			t.Fatalf("%s has no ModTime", p.Path)
		}
	}
}
```

- [ ] **Step 6: Run them and see them fail**

Run: `go test -count=1 -run 'TestParseMemoryPage|TestListMemoryPages' .`
Expected: compile errors, `undefined: parseMemoryPage` and friends.

- [ ] **Step 7: Implement `agent/memory_page.go`**

```go
package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/frontmatter"
)

// memoryPage is one page of a memory scope, as its generated index line needs it.
type memoryPage struct {
	Path           string // slash-separated, relative to the scope root
	Title          string
	Description    string // one line; the fallback when HasDescription is false
	HasDescription bool
	Unreadable     bool // the page's frontmatter did not parse
	Tags           []string
	Updated        string    // the YYYY-MM-DD stamp, "" when there is none
	ModTime        time.Time // orders a page that has no stamp
}

const (
	memoryNoDescription         = "(no description)"
	memoryFrontmatterUnreadable = "(frontmatter unreadable)"
	// memoryFallbackRunes bounds a fallback description, in characters.
	memoryFallbackRunes = 120
	// memoryPageWalkDepth bounds how deep a scope's pages are listed.
	memoryPageWalkDepth = 64
)

// isMemoryPagePath reports whether rel, a slash path relative to the scope
// root, is a page: no segment starts with "." (memory_search's rule) and it
// is not the root MEMORY.md, which is generated.
func isMemoryPagePath(rel string) bool {
	if rel == memoryIndexFile {
		return false
	}
	for segment := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}

// splitMemoryFrontmatter splits text into its frontmatter block and body
// exactly as frontmatter.Parse does, so a page whose YAML fails to parse
// still has a body to fall back on.
func splitMemoryFrontmatter(text string) (block, body string, ok bool) {
	rest, found := strings.CutPrefix(text, "---\n")
	if !found {
		return "", text, false
	}
	block, body, ok = strings.Cut(rest, "---\n")
	if !ok {
		return "", text, false
	}
	return block, body, true
}

// filenameMemoryPage is the entry for a file whose content says nothing: a
// non-Markdown file, or a page that could not be read.
func filenameMemoryPage(rel string, modTime time.Time) memoryPage {
	base := path.Base(rel)
	return memoryPage{Path: rel, Title: strings.TrimSuffix(base, path.Ext(base)), Description: base, ModTime: modTime}
}

// parseMemoryPage builds the index entry for the page at rel from its bytes.
func parseMemoryPage(rel string, raw []byte, modTime time.Time) memoryPage {
	p := filenameMemoryPage(rel, modTime)
	if path.Ext(rel) != ".md" {
		return p
	}
	text := string(raw)
	doc, err := frontmatter.Parse(text)
	body := doc.Body
	if err != nil {
		p.Unreadable = true
		_, body, _ = splitMemoryFrontmatter(text)
	}
	heading := firstMarkdownHeading(body)
	if heading != "" {
		p.Title = heading
	}
	if description, ok := doc.Meta["description"].(string); ok {
		p.Description = strings.Join(strings.Fields(description), " ")
		p.HasDescription = p.Description != ""
	}
	if !p.HasDescription {
		p.Description = memoryFallbackDescription(heading, body)
	}
	p.Tags = normalizeMemoryTags(doc.Meta["tags"])
	p.Updated = memoryStampDate(doc.Meta["updated"])
	return p
}

// firstMarkdownHeading returns the text of body's first ATX heading, or "".
func firstMarkdownHeading(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimLeft(line, "#")
		level := len(line) - len(trimmed)
		if level >= 1 && level <= 6 && (trimmed == "" || trimmed[0] == ' ' || trimmed[0] == '\t') {
			if text := strings.TrimSpace(trimmed); text != "" {
				return text
			}
		}
	}
	return ""
}

// memoryFallbackDescription is the description of a page without one: its
// first heading, else its first non-blank line, cut to memoryFallbackRunes
// characters and marked as having no description.
func memoryFallbackDescription(heading, body string) string {
	text := heading
	if text == "" {
		for line := range strings.SplitSeq(body, "\n") {
			if text = strings.TrimSpace(line); text != "" {
				break
			}
		}
	}
	if runes := []rune(text); len(runes) > memoryFallbackRunes {
		text = string(runes[:memoryFallbackRunes])
	}
	if text == "" {
		return memoryNoDescription
	}
	return text + " " + memoryNoDescription
}

// normalizeMemoryTags reads a tags value: a list, or a single string as one
// tag. Each tag is trimmed and lowercased, runs of whitespace become "-",
// and empty tags and duplicates are dropped.
func normalizeMemoryTags(v any) []string {
	var items []any
	switch value := v.(type) {
	case string:
		items = []any{value}
	case []any:
		items = value
	default:
		return nil
	}
	var tags []string
	for _, item := range items {
		var text string
		switch scalar := item.(type) {
		case string:
			text = scalar
		case int, float64, bool:
			text = fmt.Sprint(scalar)
		default:
			continue
		}
		tag := strings.Join(strings.Fields(strings.ToLower(text)), "-")
		if tag != "" && !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}

// memoryStampDate reads an updated value: a YAML date or a YYYY-MM-DD string.
func memoryStampDate(v any) string {
	switch value := v.(type) {
	case time.Time:
		return value.UTC().Format(time.DateOnly)
	case string:
		if _, err := time.Parse(time.DateOnly, value); err == nil {
			return value
		}
	}
	return ""
}

// listMemoryPages reads every page of env's scope. Directories, symlinks and
// non-page paths are skipped; a page removed since the listing is skipped,
// and one that cannot be read is listed by its filename.
func listMemoryPages(env *execenv.LocalExecutionEnvironment) ([]memoryPage, error) {
	root := env.WorkingDirectory()
	entries, err := env.ListDirectory(root, memoryPageWalkDepth)
	if err != nil {
		return nil, err
	}
	var pages []memoryPage
	for _, entry := range entries {
		rel := filepath.ToSlash(entry.Name)
		if entry.IsDir || entry.IsSymlink || !isMemoryPagePath(rel) {
			continue
		}
		raw, err := env.ReadFileRaw(filepath.Join(root, entry.Name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			pages = append(pages, filenameMemoryPage(rel, entry.ModTime))
		default:
			pages = append(pages, parseMemoryPage(rel, raw, entry.ModTime))
		}
	}
	return pages, nil
}
```

Add `"slices"` to the imports. If `ListDirectory` on the confined environment rejects the absolute root path, pass `"."` instead and note why in a comment; the existing memory code passes absolute paths built from `env.WorkingDirectory()` to every other confined call.

- [ ] **Step 8: Run the tests and gates**

Run: `go test -count=1 -run 'TestParseMemoryPage|TestListMemoryPages|TestListDirectoryReportsModTime' . ./execenv`, then the full Go gates from Global Constraints for the `agent` module.
Expected: PASS; gofmt, vet (both tag sets) and golangci-lint clean.

- [ ] **Step 9: Commit and open PR 1**

```bash
git add agent/execenv/execenv.go agent/execenv/local.go agent/execenv/securepath_fdops_unix.go agent/execenv/runtime_edges_fuzz_test.go agent/execenv/list_directory_modtime_test.go agent/memory_page.go agent/memory_page_test.go
git diff --cached --stat
git commit -m "feat(memory): parse a scope's pages into index entries"
```

---

### Task 2: Renderer and budgeted projection

**PR boundary:** PR 2, branch `claude/memory-index-2-render`, base `main` after PR 1 squashes. Estimated non-test lines: ~105. Pure functions; still unused outside tests.

**Files:**
- Create: `agent/memory_index_render.go`
- Test: `agent/memory_index_render_test.go`

**Interfaces:**
- Consumes: `memoryPage`, `memoryFrontmatterUnreadable` (Task 1); `runetrim.Cut(s string, maxBytes int) string`.
- Produces:
  - `const memoryProjectionCap = 8192`
  - `func renderMemoryIndex(pages []memoryPage) string` — full index, `""` for no pages
  - `func projectMemoryIndex(pages []memoryPage, limit int) (content string, truncated bool)`
  - `func memoryIndexLine(p memoryPage) string`
  - `func memoryNotShownLine(rest []memoryPage) string`

- [ ] **Step 1: Write the failing tests**

`agent/memory_index_render_test.go`:

```go
package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRenderMemoryIndex(t *testing.T) {
	t.Parallel()
	pages := []memoryPage{
		{Path: "vitest/loader.md", Title: "Vitest read-only loader", Description: "read-only Vitest needs --configLoader runner", HasDescription: true, Tags: []string{"vitest"}, Updated: "2026-10-07"},
		{Path: "cents.md", Title: "Integer cents", Description: "Money is integer cents, never floats", HasDescription: true, Tags: []string{"money", "formatting"}, Updated: "2026-10-08"},
		{Path: "b.md", Title: "b", Description: "unstamped, older mtime", HasDescription: true, ModTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Path: "a.md", Title: "a", Description: "unstamped, same day as loader", HasDescription: true, ModTime: time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC)},
		{Path: "bad.md", Title: "Bad", Description: "Bad " + memoryNoDescription, Unreadable: true, Updated: "2026-01-01"},
	}
	want := "Tags: formatting (1), money (1), vitest (1)\n" +
		"- [Integer cents](cents.md) — Money is integer cents, never floats [money, formatting] (updated 2026-10-08)\n" +
		"- [a](a.md) — unstamped, same day as loader\n" +
		"- [Vitest read-only loader](vitest/loader.md) — read-only Vitest needs --configLoader runner [vitest] (updated 2026-10-07)\n" +
		"- [Bad](bad.md) — Bad (no description) (frontmatter unreadable) (updated 2026-01-01)\n" +
		"- [b](b.md) — unstamped, older mtime\n"
	if got := renderMemoryIndex(pages); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := renderMemoryIndex(nil); got != "" {
		t.Fatalf("empty scope rendered %q", got)
	}
	untagged := []memoryPage{{Path: "x.md", Title: "x", Description: "d", HasDescription: true}}
	if got := renderMemoryIndex(untagged); strings.HasPrefix(got, "Tags:") {
		t.Fatalf("header present with no tags: %q", got)
	}
}

func overflowPages(n int) []memoryPage {
	var pages []memoryPage
	for i := range n {
		tags := []string{"vitest"}
		switch {
		case i%3 == 0:
			tags = []string{"indexeddb", "vitest"}
		case i%5 == 0:
			tags = nil
		}
		pages = append(pages, memoryPage{
			Path: fmt.Sprintf("p%03d.md", i), Title: fmt.Sprintf("Page %03d", i),
			Description: "opaque description " + strings.Repeat("x", 60), HasDescription: true,
			Tags: tags, Updated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly),
		})
	}
	return pages
}

func TestProjectMemoryIndexFitsWhole(t *testing.T) {
	t.Parallel()
	pages := overflowPages(3)
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if truncated || content != renderMemoryIndex(pages) {
		t.Fatalf("truncated=%t content=%q", truncated, content)
	}
}

func TestProjectMemoryIndexKeepsNewestAndCountsTheRest(t *testing.T) {
	t.Parallel()
	pages := overflowPages(200)
	full := renderMemoryIndex(pages)
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap {
		t.Fatalf("truncated=%t len=%d", truncated, len(content))
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	fullLines := strings.Split(strings.TrimSuffix(full, "\n"), "\n")
	shown := len(lines) - 1 // header ... last line is Not shown
	for i := range shown {
		if lines[i] != fullLines[i] {
			t.Fatalf("line %d %q is not the full index's %q", i, lines[i], fullLines[i])
		}
	}
	notShown := 200 - (shown - 1) // minus the header
	last := lines[len(lines)-1]
	sorted := sortedMemoryPages(pages)
	if want := memoryNotShownLine(sorted[200-notShown:]); last != want {
		t.Fatalf("last line %q want %q", last, want)
	}
	if !strings.HasPrefix(last, fmt.Sprintf("Not shown: %d pages (vitest ", notShown)) || !strings.Contains(last, "untagged") {
		t.Fatalf("last line %q", last)
	}
	// One more line would not have fit: the projection with the next page
	// shown and the rest counted is over the budget.
	k := 200 - notShown
	longer := strings.Join(fullLines[:shown+1], "\n") + "\n" + memoryNotShownLine(sorted[k+1:]) + "\n"
	if len(longer) <= memoryProjectionCap {
		t.Fatalf("showing page %d too would still fit (%d bytes)", k, len(longer))
	}
}

func TestMemoryNotShownLine(t *testing.T) {
	t.Parallel()
	got := memoryNotShownLine([]memoryPage{
		{Tags: []string{"vitest", "indexeddb"}}, {Tags: []string{"vitest"}}, {Tags: []string{"indexeddb"}},
		{Tags: []string{"vitest"}}, {}, {}, {Tags: []string{"alpha"}},
	})
	want := `Not shown: 7 pages (vitest 3, indexeddb 2, alpha 1, untagged 2). Read the full index with memory_read("MEMORY.md"), or find a tag's pages with memory_search.`
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if one := memoryNotShownLine([]memoryPage{{}}); !strings.HasPrefix(one, "Not shown: 1 page (untagged 1).") {
		t.Fatalf("singular: %q", one)
	}
}

// Review Focus 2: one page larger than the whole budget is never cut mid-line.
func TestProjectMemoryIndexHugeLine(t *testing.T) {
	t.Parallel()
	pages := []memoryPage{
		{Path: "huge.md", Title: "huge", Description: strings.Repeat("y", 9000), HasDescription: true, Tags: []string{"big"}, Updated: "2026-10-08"},
		{Path: "small.md", Title: "small", Description: "fits", HasDescription: true, Updated: "2026-10-01"},
	}
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || strings.Contains(content, "yyy") {
		t.Fatalf("truncated=%t len=%d content=%.200q", truncated, len(content), content)
	}
	if !strings.HasSuffix(content, memoryNotShownLine(sortedMemoryPages(pages))+"\n") {
		t.Fatalf("content %q does not end with the Not shown line for both pages", content)
	}
}

// Ruling 13: a header too large to fit is cut at the budget, still truncated.
func TestProjectMemoryIndexOversizedHeader(t *testing.T) {
	t.Parallel()
	var pages []memoryPage
	for i := range 600 {
		pages = append(pages, memoryPage{Path: fmt.Sprintf("p%03d.md", i), Title: "t", Description: "d", HasDescription: true, Tags: []string{fmt.Sprintf("tag-number-%03d", i)}})
	}
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || !strings.HasPrefix(content, "Tags: ") {
		t.Fatalf("truncated=%t len=%d", truncated, len(content))
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test -count=1 -run 'TestRenderMemoryIndex|TestProjectMemoryIndex|TestMemoryNotShownLine' .`
Expected: `undefined: renderMemoryIndex`.

- [ ] **Step 3: Implement `agent/memory_index_render.go`**

```go
package agent

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// memoryProjectionCap bounds, in bytes, the index a scope projects into context.
const memoryProjectionCap = 8192

// memoryPageSortDate is the date a page sorts by: its stamp, else its file's
// modification date.
func memoryPageSortDate(p memoryPage) string {
	if p.Updated != "" {
		return p.Updated
	}
	return p.ModTime.UTC().Format(time.DateOnly)
}

// sortedMemoryPages orders pages newest first, ties by path.
func sortedMemoryPages(pages []memoryPage) []memoryPage {
	sorted := slices.Clone(pages)
	slices.SortStableFunc(sorted, func(a, b memoryPage) int {
		return cmp.Or(strings.Compare(memoryPageSortDate(b), memoryPageSortDate(a)), strings.Compare(a.Path, b.Path))
	})
	return sorted
}

// memoryIndexLine is one page's index line.
func memoryIndexLine(p memoryPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s](%s) — %s", p.Title, p.Path, p.Description)
	if p.Unreadable {
		b.WriteString(" " + memoryFrontmatterUnreadable)
	}
	if len(p.Tags) > 0 {
		b.WriteString(" [" + strings.Join(p.Tags, ", ") + "]")
	}
	if p.Updated != "" {
		b.WriteString(" (updated " + p.Updated + ")")
	}
	return b.String()
}

// memoryTagCounts counts pages per tag; a page counts once under each tag.
func memoryTagCounts(pages []memoryPage) (counts map[string]int, untagged int) {
	counts = make(map[string]int)
	for _, p := range pages {
		if len(p.Tags) == 0 {
			untagged++
		}
		for _, tag := range p.Tags {
			counts[tag]++
		}
	}
	return counts, untagged
}

// memoryTagsHeader lists every tag with its page count, alphabetically, or
// is "" when no page has tags.
func memoryTagsHeader(pages []memoryPage) string {
	counts, _ := memoryTagCounts(pages)
	if len(counts) == 0 {
		return ""
	}
	var parts []string
	for _, tag := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s (%d)", tag, counts[tag]))
	}
	return "Tags: " + strings.Join(parts, ", ")
}

// memoryNotShownLine closes a projection that leaves rest out: how many pages
// and their tags, most common first, then the routes to them.
func memoryNotShownLine(rest []memoryPage) string {
	counts, untagged := memoryTagCounts(rest)
	tags := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
	})
	var parts []string
	for _, tag := range tags {
		parts = append(parts, fmt.Sprintf("%s %d", tag, counts[tag]))
	}
	if untagged > 0 {
		parts = append(parts, fmt.Sprintf("untagged %d", untagged))
	}
	noun := "pages"
	if len(rest) == 1 {
		noun = "page"
	}
	return fmt.Sprintf("Not shown: %d %s (%s). Read the full index with memory_read(\"MEMORY.md\"), or find a tag's pages with memory_search.", len(rest), noun, strings.Join(parts, ", "))
}

// renderMemoryIndex is a scope's whole generated index: the tag header, then
// one line per page, newest first. It is "" for a scope with no pages.
func renderMemoryIndex(pages []memoryPage) string {
	var b strings.Builder
	sorted := sortedMemoryPages(pages)
	if header := memoryTagsHeader(sorted); header != "" {
		b.WriteString(header + "\n")
	}
	for _, p := range sorted {
		b.WriteString(memoryIndexLine(p) + "\n")
	}
	return b.String()
}

// projectMemoryIndex is the index as projected into context within limit
// bytes: the whole index when it fits; otherwise the header, the newest lines
// that fit, and a closing line counting the pages left out.
func projectMemoryIndex(pages []memoryPage, limit int) (string, bool) {
	if full := renderMemoryIndex(pages); len(full) <= limit {
		return full, false
	}
	sorted := sortedMemoryPages(pages)
	prefix := ""
	if header := memoryTagsHeader(sorted); header != "" {
		prefix = header + "\n"
	}
	lines := make([]string, len(sorted))
	sizes := make([]int, len(sorted)+1) // sizes[k]: prefix plus the first k lines
	sizes[0] = len(prefix)
	for i, p := range sorted {
		lines[i] = memoryIndexLine(p) + "\n"
		sizes[i+1] = sizes[i] + len(lines[i])
	}
	for k := len(sorted) - 1; k >= 0; k-- {
		closing := memoryNotShownLine(sorted[k:]) + "\n"
		if sizes[k]+len(closing) <= limit {
			return prefix + strings.Join(lines[:k], "") + closing, true
		}
	}
	return runetrim.Cut(prefix+memoryNotShownLine(sorted)+"\n", limit), true
}
```

- [ ] **Step 4: Run the tests and gates**

Run: `go test -count=1 -run 'TestRenderMemoryIndex|TestProjectMemoryIndex|TestMemoryNotShownLine' .` then the Go gates for `agent/`.
Expected: PASS, gates clean.

- [ ] **Step 5: Commit and open PR 2**

```bash
git add agent/memory_index_render.go agent/memory_index_render_test.go
git diff --cached --stat
git commit -m "feat(memory): render a scope's index and its budgeted projection"
```

---

### Task 3: Frontmatter field setter and migration

**PR boundary:** PR 3, branch `claude/memory-index-3-migrate`, base `main` after PR 2 squashes. Estimated non-test lines: ~125. `migrateMemoryScope` is not called by the session until Task 4.

**Files:**
- Create: `agent/memory_migrate.go`
- Test: `agent/memory_migrate_test.go`

**Interfaces:**
- Consumes: `splitMemoryFrontmatter`, `parseMemoryPage`, `isMemoryPagePath` (Task 1); `memoryIndexFile` (`agent/session_memory.go:62`); `env.ReadFileRaw`, `env.WriteFileRaw(path string, data []byte, perm os.FileMode) error`, `env.RenamePath(oldPath, newPath string) error`.
- Produces:
  - `func memoryYAMLField(key, value string) string` — `"key: <yaml>\n"`
  - `func setMemoryFrontmatterField(raw []byte, key, line string) []byte`
  - `func parseLegacyMemoryIndex(index string) map[string]string` — page path → description
  - `func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error`
  - `const memoryLegacyIndexBackup = ".MEMORY.md.pre-generated"`

- [ ] **Step 1: Write the failing tests**

`agent/memory_migrate_test.go`:

```go
package agent

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

func TestSetMemoryFrontmatterField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw, key, line, want string }{
		{"no frontmatter gains a block", "# Title\nbody\n", "updated", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\n---\n# Title\nbody\n"},
		{"appends a missing key, keeps every other byte", "---\ndescription: d\nodd:   spacing  \n---\nbody", "by", "by: s1\n",
			"---\ndescription: d\nodd:   spacing  \nby: s1\n---\nbody"},
		{"replaces an existing key and its continuation lines", "---\nupdated: |\n  old\n  older\ntags:\n- a\n---\nb\n", "updated", "updated: 2026-10-08\n",
			"---\nupdated: 2026-10-08\ntags:\n- a\n---\nb\n"},
		{"does not match a longer key", "---\nbyline: x\n---\n", "by", "by: s1\n",
			"---\nbyline: x\nby: s1\n---\n"},
		{"empty block", "---\n---\nbody\n", "by", "by: s1\n", "---\nby: s1\n---\nbody\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(setMemoryFrontmatterField([]byte(tc.raw), tc.key, tc.line)); got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// yaml.v3 quotes what needs quoting and never wraps a long value.
func TestMemoryYAMLFieldRoundTrips(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"Money is: cents", "- dash", "#hash", "2026-10-08", strings.Repeat("word ", 40) + "end"} {
		raw := setMemoryFrontmatterField([]byte("body\n"), "description", memoryYAMLField("description", value))
		if got := parseMemoryPage("a.md", raw, time.Time{}); !got.HasDescription || got.Description != value {
			t.Fatalf("value %q read back as %+v from %q", value, got, raw)
		}
	}
}

// Review Focus 4.
func TestParseLegacyMemoryIndex(t *testing.T) {
	t.Parallel()
	index := "# Project memory\n\n" +
		"- [Running the real test suite](testing.md): plain `go test` silently skips everything\n" +
		"- [Integer cents](./money/cents.md#rule) — Money is integer cents\n" +
		"- [Logging rollout status](logging-rollout.md)\n" +
		"* see vitest.md for the loader quirk\n" +
		"- [again](testing.md): a second line for the same page loses\n" +
		"- [external](https://example.com/x.md): skipped\n" +
		"- [escape](../other/x.md): skipped\n" +
		"- [not markdown](notes.txt): skipped\n" +
		"- [hidden](.draft/x.md): skipped\n" +
		"- **no-link** — [feedback] a line with no page\n"
	want := map[string]string{
		"testing.md":         "plain `go test` silently skips everything",
		"money/cents.md":     "Money is integer cents",
		"logging-rollout.md": "Logging rollout status",
		"vitest.md":          "see for the loader quirk",
	}
	if got := parseLegacyMemoryIndex(index); !maps.Equal(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestMigrateMemoryScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory", "personal")
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(scope, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("MEMORY.md", "- [plain](plain.md) — plain page gets this\n- [kept](kept.md) — loses to the page's own\n- [bad](bad.md) — skipped, frontmatter unreadable\n- [gone](gone.md) — page missing\n")
	write("plain.md", "# Plain\nbody\n")
	write("kept.md", "---\ndescription: the page's own\n---\nbody\n")
	write("bad.md", "---\ndescription: a: b\n---\nbody\n")
	write(memoryLegacyIndexBackup, "an earlier backup is replaced\n")

	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(scope, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if got := read("plain.md"); got != "---\ndescription: plain page gets this\n---\n# Plain\nbody\n" {
		t.Fatalf("plain.md=%q", got)
	}
	if got := read("kept.md"); got != "---\ndescription: the page's own\n---\nbody\n" {
		t.Fatalf("kept.md=%q", got)
	}
	if got := read("bad.md"); got != "---\ndescription: a: b\n---\nbody\n" {
		t.Fatalf("bad.md=%q", got)
	}
	if _, err := os.Stat(filepath.Join(scope, "gone.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("migration created gone.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scope, "MEMORY.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MEMORY.md still present: %v", err)
	}
	if got := read(memoryLegacyIndexBackup); got[:9] != "- [plain]" {
		t.Fatalf("backup=%q", got)
	}
	// Idempotent: with no MEMORY.md, a second run changes nothing.
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	if got := read("plain.md"); got != "---\ndescription: plain page gets this\n---\n# Plain\nbody\n" {
		t.Fatalf("second run changed plain.md: %q", got)
	}
}

// A crash after the page writes but before the rename leaves MEMORY.md; the
// next run finishes the job without rewriting described pages.
func TestMigrateMemoryScopeResumesAfterACrash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory", "personal")
	if err := os.WriteFile(filepath.Join(scope, "MEMORY.md"), []byte("- [p](p.md) — from the index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "p.md"), []byte("---\ndescription: from the index\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "p.md"))
	if string(raw) != "---\ndescription: from the index\n---\n" {
		t.Fatalf("p.md=%q", raw)
	}
	if _, err := os.Stat(filepath.Join(scope, memoryLegacyIndexBackup)); err != nil {
		t.Fatal(err)
	}
}
```

The `vitest.md` row pins the bare-path form: the token is removed from the line and the separators trimmed, so the rest reads "see for the loader quirk". That wording is ugly but faithful to the spec's rule; it only matters for hand-written indexes that never linked their pages.

- [ ] **Step 2: Run them and see them fail**

Run: `go test -count=1 -run 'TestSetMemoryFrontmatterField|TestMemoryYAMLField|TestParseLegacyMemoryIndex|TestMigrateMemoryScope' .`
Expected: `undefined: setMemoryFrontmatterField`.

- [ ] **Step 3: Implement `agent/memory_migrate.go`**

```go
package agent

import (
	"errors"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"primeradiant.com/evener/agent/execenv"
)

// memoryLegacyIndexBackup is where migration keeps a scope's hand-written
// index. Its dot name keeps it out of the pages and out of memory_search.
const memoryLegacyIndexBackup = ".MEMORY.md.pre-generated"

// memoryYAMLField encodes one frontmatter line, quoting value as YAML needs.
func memoryYAMLField(key, value string) string {
	encoded, err := yaml.Marshal(map[string]string{key: value})
	if err != nil {
		// A map of two strings always encodes.
		panic(err)
	}
	return string(encoded)
}

// setMemoryFrontmatterField makes line (an encoded "key: value\n") the page's
// only top-level key entry, replacing an existing one and its continuation
// lines, or adding it at the end of the block, or adding a block. Every other
// byte of the page is kept.
func setMemoryFrontmatterField(raw []byte, key, line string) []byte {
	text := string(raw)
	block, body, ok := splitMemoryFrontmatter(text)
	if !ok {
		return []byte("---\n" + line + "---\n" + text)
	}
	var kept []string
	replaced, skipping := false, false
	for _, existing := range strings.SplitAfter(block, "\n") {
		if existing == "" {
			continue
		}
		if skipping && (strings.HasPrefix(existing, " ") || strings.HasPrefix(existing, "\t") || strings.HasPrefix(existing, "-")) {
			continue
		}
		skipping = false
		if strings.HasPrefix(existing, key+":") {
			if !replaced {
				kept = append(kept, line)
				replaced = true
			}
			skipping = true
			continue
		}
		kept = append(kept, existing)
	}
	if !replaced {
		if n := len(kept); n > 0 && !strings.HasSuffix(kept[n-1], "\n") {
			kept[n-1] += "\n"
		}
		kept = append(kept, line)
	}
	return []byte("---\n" + strings.Join(kept, "") + "---\n" + body)
}

var (
	legacyIndexLink     = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)
	legacyIndexBarePage = regexp.MustCompile(`[^\s\[\]()]+\.md\b`)
)

// legacyIndexSeparators are trimmed from both ends of a description.
const legacyIndexSeparators = " \t-*+—–:"

// legacyIndexPage turns a link target into a page path in the scope, or
// reports false for anything that is not a local Markdown page.
func legacyIndexPage(target string) (string, bool) {
	target, _, _ = strings.Cut(target, "#")
	if strings.Contains(target, "://") {
		return "", false
	}
	page := path.Clean(strings.TrimPrefix(target, "./"))
	if !filepath.IsLocal(page) || path.Ext(page) != ".md" || !isMemoryPagePath(page) {
		return "", false
	}
	return page, true
}

// parseLegacyMemoryIndex reads a hand-written MEMORY.md: for each line naming
// a page, by a Markdown link or a bare path ending in .md, the description
// the rest of the line gives it. The first line naming a page wins.
func parseLegacyMemoryIndex(index string) map[string]string {
	out := make(map[string]string)
	for line := range strings.SplitSeq(index, "\n") {
		var target, text, rest string
		if m := legacyIndexLink.FindStringSubmatchIndex(line); m != nil {
			text, target = line[m[2]:m[3]], line[m[4]:m[5]]
			rest = line[:m[0]] + line[m[1]:]
		} else if m := legacyIndexBarePage.FindStringIndex(line); m != nil {
			target = line[m[0]:m[1]]
			rest = line[:m[0]] + line[m[1]:]
		} else {
			continue
		}
		page, ok := legacyIndexPage(target)
		if _, seen := out[page]; !ok || seen {
			continue
		}
		description := strings.Join(strings.Fields(strings.Trim(rest, legacyIndexSeparators)), " ")
		if description == "" {
			description = strings.Join(strings.Fields(text), " ")
		}
		if description != "" {
			out[page] = description
		}
	}
	return out
}

// migrateMemoryScope moves a scope's hand-written index into its pages: each
// linked page with no description gets the one its index line gave, then the
// index is renamed to memoryLegacyIndexBackup. It is idempotent and needs no
// lock: concurrent runs write the same descriptions, skip pages that have
// one, and the run that finds the index already renamed is done. A failure
// leaves the index in place, so the next run finishes the job.
func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error {
	root := env.WorkingDirectory()
	legacy := filepath.Join(root, memoryIndexFile)
	raw, err := env.ReadFileRaw(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	descriptions := parseLegacyMemoryIndex(string(raw))
	for _, page := range slices.Sorted(maps.Keys(descriptions)) {
		abs := filepath.Join(root, filepath.FromSlash(page))
		body, err := env.ReadFileRaw(abs)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if parsed := parseMemoryPage(page, body, time.Time{}); parsed.HasDescription || parsed.Unreadable {
			continue
		}
		described := setMemoryFrontmatterField(body, "description", memoryYAMLField("description", descriptions[page]))
		if err := env.WriteFileRaw(abs, described, 0o644); err != nil {
			return err
		}
	}
	if err := env.RenamePath(legacy, filepath.Join(root, memoryLegacyIndexBackup)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
```

If `yaml.v3` is not yet a direct import of the `agent` module's non-test code, `go mod tidy` is not needed: `agent/internal/frontmatter` already requires it in `agent/go.mod`. Confirm with `grep yaml.v3 agent/go.mod`.

- [ ] **Step 4: Run the tests and gates**

Run: `go test -count=1 -run 'TestSetMemoryFrontmatterField|TestMemoryYAMLField|TestParseLegacyMemoryIndex|TestMigrateMemoryScope' .` then the Go gates for `agent/`.
Expected: PASS, gates clean. If `TestParseLegacyMemoryIndex`'s `vitest.md` row disagrees by whitespace only, fix the expectation, not the trimming rule.

- [ ] **Step 5: Commit and open PR 3**

```bash
git add agent/memory_migrate.go agent/memory_migrate_test.go
git diff --cached --stat
git commit -m "feat(memory): migrate a hand-written index into page frontmatter"
```

---

### Task 4: Project and diff the rendered index

**PR boundary:** PR 4, branch `claude/memory-index-4-project`, base `main` after PR 3 squashes. Estimated non-test lines: ~95 (`session_memory.go` ~85, `apptranscript` ~10). Heavy test churn: existing refresh tests seed a hand-written `MEMORY.md`, which the new projection migrates away.

**Files:**
- Modify: `agent/session_memory.go` (lines 61-67 index file helpers, 69-79 types, 114-120 `boundedMemoryIndex`, 302-347 `readMemoryScope`, 367-441 own-write recording, 543-618 projection, 664-721 deltas, 757 comment)
- Modify: `internal/apptranscript/memory_context.go:42-57,120-126`
- Modify: `agent/memory_context_wire_fixture_test.go`; regenerate `agent/testdata/memorycontextwire/events.json`
- Modify: `cmd/evener-tui/hub_transcript_widgets_test.go`
- Modify tests: `agent/session_memory_test.go`, `agent/session_memory_refresh_test.go`, `agent/session_memory_continuation_test.go`, `agent/session_memory_preservation_test.go`, `cmd/evener-hub/memory_preservation_test.go`, `internal/apptranscript/memory_context_test.go`

**Interfaces:**
- Consumes: `listMemoryPages` (Task 1); `renderMemoryIndex`, `projectMemoryIndex`, `memoryProjectionCap` (Task 2); `migrateMemoryScope` (Task 3).
- Produces:
  - `type renderedMemoryScope struct { status, full, projected string; truncated bool }`
  - `func renderMemoryScope(env *execenv.LocalExecutionEnvironment) (renderedMemoryScope, error)`
  - `memoryProjection.Index string` (full rendering)
  - `const memoryIndexPartial = " Not every page is shown; the index's last line counts the rest."` (agent) and `memoryContextPartial` (apptranscript, same text)
  - test helpers in `agent/session_memory_test.go`: `memorySeedPage(t, root, scope, name, description string) string` and `memoryExpectedIndex(t, root, scope string) string`

- [ ] **Step 1: Add the test helpers and convert the seeds**

In `agent/session_memory_test.go`, replace `memorySeed` (line 1117) with page seeding; keep `writeMemoryIndex` in the refresh tests for writing page files:

```go
// memorySeedPage writes one page into scope with a frontmatter description
// and a fixed stamp, so its index line is stable, and returns its path.
func memorySeedPage(t *testing.T, root, scope, name, description string) string {
	t.Helper()
	path := filepath.Join(root, "memory", scope, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: " + description + "\nupdated: 2026-10-01\n---\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// memoryExpectedIndex is scope's rendered index as the session would project
// it, read straight from disk.
func memoryExpectedIndex(t *testing.T, root, scope string) string {
	t.Helper()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", scope))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	pages, err := listMemoryPages(env)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := projectMemoryIndex(pages, memoryProjectionCap)
	return content
}
```

Convert every caller. The rule for each existing test:
- `memorySeed(t, root, scope, "opaque-a\nopaque-b\n")` becomes one `memorySeedPage` per line (`a.md` with description `opaque-a`, `b.md` with `opaque-b`). Opaque markers stay as descriptions, so `strings.Contains` assertions on projected text keep working.
- `writeMemoryIndex(t, path, newBody)` that adds a line becomes a new `memorySeedPage`; one that drops a line becomes `os.Remove` of that page; one that changes a line rewrites that page's description.
- Assertions that compare projected content to the seed bytes compare to `memoryExpectedIndex(t, root, scope)`.
- Tests that drive `memory_write`/`memory_edit`/`memory_delete` of `MEMORY.md` to exercise own-write baselines (`TestMemoryRefreshIgnoresOwnIndexEdit`, `...OwnIndexWriteWithoutIndex`, `...OwnIndexDelete`, `...OwnWriteDuringStalledReadIsNotEchoed`, `...DiscardsAStaleReadEvenWhenComplete`, `session_memory_preservation_test.go`) switch to writing, editing and deleting a page (`fact.md`): a page write is now what moves the baseline. Rename them to say "OwnPage" (e.g. `TestMemoryRefreshIgnoresOwnPageWrite`).
- `TestMemoryRefreshBaselineIsBoundedByTheProjectionCap` inverts (ruling 2): rename to `TestMemoryRefreshBaselineIsTheFullRenderedIndex`, seed 200 pages, assert `baseline.index == renderMemoryIndex(pages)` and longer than 8192.
- `TestMemoryRefreshIndexDeltaComparesTheProjectedIndex` inverts the same way: rename to `TestMemoryRefreshIndexDeltaComparesTheFullIndex`. Seed 200 pages (stamped `2026-01-01`). A new page stamped `2025-01-01` (outside the projected window) delivers a block naming it; a description change on the newest page delivers `-`/`+` lines; nothing about pages that merely moved past the budget.
- `TestMemoryProjectionReportsOnlyATooLongIndex` becomes `TestMemoryProjectionReportsOnlyAPartialIndex`: short (one page) decodes `truncated=false`; 200 pages decodes `truncated=true`, the text contains `memoryIndexPartial` and never `gardening-memory`, with or without `use_skill`.
- `session_memory_continuation_test.go` seeds a page with description `opaque-memory-personal-3730` instead of `MEMORY.md`.
- `cmd/evener-hub/memory_preservation_test.go` seeds `fact.md` pages (with the same opaque bytes) in personal and project, reads `fact.md` instead of `MEMORY.md`, and keeps the `memory/sessions/<id>/MEMORY.md` seed: that directory is no longer read, so its file must survive untouched. Assert both.

- [ ] **Step 2: Add the new behavior tests**

Add to `agent/session_memory_refresh_test.go`:

```go
// The first projection of a scope that still has a hand-written MEMORY.md
// migrates it: its descriptions move into the pages and the projection is
// the generated index.
func TestMemoryProjectionMigratesAHandWrittenIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scope := filepath.Join(root, "memory", "personal")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMemoryIndex(t, filepath.Join(scope, "MEMORY.md"), "- [Cents](cents.md) — opaque-migrated-description\n")
	writeMemoryIndex(t, filepath.Join(scope, "cents.md"), "# Cents\n")
	var text string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(func(req llm.Request) llm.Response {
		text = latestMemoryContext(req, "personal")
		return finalResponse("observed")
	}))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	display, ok := apptranscript.ParseMemoryContext(text, "memory_personal")
	if !ok || !strings.Contains(display.Content, "- [Cents](cents.md) — opaque-migrated-description") {
		t.Fatalf("projection=%+v ok=%t", display, ok)
	}
	if _, err := os.Stat(filepath.Join(scope, memoryLegacyIndexBackup)); err != nil {
		t.Fatal(err)
	}
}

// A page another session deletes reaches this session as a removed line; a
// description change as the old line removed and the new one added; the tag
// header never appears in a change block.
func TestMemoryRefreshDeltaListsPageLinesOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept")
	gone := memorySeedPage(t, root, "personal", "gone.md", "opaque-gone")
	changed := memorySeedPage(t, root, "personal", "changed.md", "opaque-before")
	var delta string
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response { return finalResponse("first") },
		func(req llm.Request) llm.Response {
			delta = latestMemoryContext(req, "personal")
			return finalResponse("second")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	writeMemoryIndex(t, changed, "---\ndescription: opaque-after\ntags: [newtag]\nupdated: 2026-10-01\n---\n")
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`- "- [gone](gone.md) — opaque-gone`, `- "- [changed](changed.md) — opaque-before`, `+ "- [changed](changed.md) — opaque-after [newtag]`} {
		if !strings.Contains(delta, want) {
			t.Fatalf("delta lacks %q: %s", want, delta)
		}
	}
	if strings.Contains(delta, "opaque-kept") || strings.Contains(delta, "Tags:") {
		t.Fatalf("delta lists an unchanged line or the header: %s", delta)
	}
}

// The session's own write of any page becomes its baseline: the next turn
// carries no change block for it.
func TestMemoryRefreshIgnoresOwnPageWriteOfAnyPage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memorySeedPage(t, root, "personal", "kept.md", "opaque-kept")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "new.md", "content": "---\ndescription: opaque-own-page\n---\n"})
		},
		func(llm.Request) llm.Response { return finalResponse("wrote") },
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("next turn carries %d memory contexts, want only the first projection", got)
			}
			return finalResponse("next")
		},
	))
	for range 2 {
		if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
}
```

Add to `internal/apptranscript/memory_context_test.go` a row decoding the new sentence as truncated (build it from the existing `memoryContextRecord`-style helper the file already uses at line 26, with `size = " Not every page is shown; the index's last line counts the rest."`), and keep the two existing too-long rows, now commented as earlier builds.

- [ ] **Step 3: Run them and see them fail**

Run (from `agent/`): `go test -count=1 -run 'TestMemory' .` and (root) `go test -count=1 ./internal/apptranscript`
Expected: the converted tests fail (the projection still reads `MEMORY.md`; seeded pages project nothing, status `missing`), the new tests fail, the apptranscript row fails to decode.

- [ ] **Step 4: Implement in `agent/session_memory.go`**

1. Comment on `memoryIndexFile` (line 61): `// memoryIndexFile is the scope's generated index: no file by this name is written, and memory_read of it renders the pages.` Delete `readMemoryIndexFile` (lines 64-67).
2. `memoryProjection` gains `Index`:

```go
type memoryProjection struct {
	Scope, Status string
	// Content is the index as the model is shown it, within the projection
	// budget; Index is the whole rendering, which baselines and change
	// blocks compare, so a page moving past the budget is never a change.
	Content, Index string
	Truncated      bool
}
```

Update `memoryIndexBaseline`'s comment: the baseline holds the whole rendered index.

3. Add beside `boundedMemoryIndex` (keep that function; `TestMemoryIndexUTF8Boundary` still pins it, and delete both only if nothing else calls it after this task, checked with a full `grep -rn boundedMemoryIndex agent/` without `head`):

```go
// renderedMemoryScope is one rendering of a scope's pages.
type renderedMemoryScope struct {
	status    string // "current", or "missing" when the scope has no pages
	full      string // the whole index
	projected string // the index within the projection budget
	truncated bool
}

// renderMemoryScope migrates a hand-written index if one remains, then
// renders the scope's pages. A failed migration never blocks the rendering;
// the next one retries it.
func renderMemoryScope(env *execenv.LocalExecutionEnvironment) (renderedMemoryScope, error) {
	_ = migrateMemoryScope(env)
	pages, err := listMemoryPages(env)
	if err != nil {
		return renderedMemoryScope{}, err
	}
	if len(pages) == 0 {
		return renderedMemoryScope{status: "missing"}, nil
	}
	projected, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	return renderedMemoryScope{status: "current", full: renderMemoryIndex(pages), projected: projected, truncated: truncated}, nil
}
```

4. In `readMemoryScope`, replace lines 332-339 with:

```go
	if rendered, err := renderMemoryScope(env); err == nil {
		p.Status, p.Content, p.Index, p.Truncated = rendered.status, rendered.projected, rendered.full, rendered.truncated
	}
```

5. Own writes: replace `recordOwnMemoryWrite` and drop the index branch from `recordMemoryContent`:

```go
// recordOwnMemoryWrite makes what the session wrote, edited or deleted what
// it knows: the scope's index is rendered again and becomes its baseline,
// and a page it read gets its new record. Neither is echoed back.
//
// Both are read back through env. Another session writing between this
// session's write and the read-back is folded in unseen until the file
// changes again or a compaction or resume delivers the index in full; the
// race is accepted as rare and cheap.
func (s *Session) recordOwnMemoryWrite(env *execenv.LocalExecutionEnvironment, scope, file string) {
	var rendered renderedMemoryScope
	err := s.beforeMemoryIO(scope, "record")
	if err == nil {
		rendered, err = renderMemoryScope(env)
	}
	s.recordOwnMemoryIndex(scope, rendered, err)
	s.memoryMu.Lock()
	_, tracked := s.memoryReadPages[scope][file]
	s.memoryMu.Unlock()
	if !tracked {
		return
	}
	var raw []byte
	if err = s.beforeMemoryIO(scope, "record"); err == nil {
		raw, err = env.ReadFileRaw(filepath.Join(env.WorkingDirectory(), file))
	}
	s.recordMemoryContent(scope, file, raw, err, false)
}

// recordOwnMemoryIndex makes rendered the index the session knows for scope
// after its own write; a failed rendering forgets the scope, so the next
// boundary delivers it in full. A read already in flight started before the
// write, so its result is discarded.
func (s *Session) recordOwnMemoryIndex(scope string, rendered renderedMemoryScope, err error) {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	if flight := s.memoryIndexFlights[scope]; flight != nil {
		flight.stale = true
	}
	switch {
	case err != nil:
		delete(s.memoryBaseline, scope)
	case rendered.status == "missing":
		s.setMemoryBaselineLocked(scope, memoryIndexBaseline{status: "missing"})
	default:
		s.setMemoryBaselineLocked(scope, memoryIndexBaseline{status: "current", index: rendered.full})
	}
}
```

In `recordMemoryContent`, delete `index := file == memoryIndexFile` and its `if index {...}` block; the rest is page recording. Keep the stale-flight marking.

6. `appendMemoryContext` (line 547): baseline `index: p.Index`.
7. Size sentence: replace the `memoryIndexTooLong`/`memoryIndexTooLongPlain` constants with

```go
// memoryIndexPartial follows the read route in a projection that does not
// show every page; the index's last line counts the rest. The projector
// (internal/apptranscript) decodes it as the truncated flag, so keep the
// sentence in step with memoryContextPartial there.
const memoryIndexPartial = " Not every page is shown; the index's last line counts the rest."
```

and in `appendMemoryProjection` drop the `tooLong` selection: `size := ""; if p.Truncated { size = memoryIndexPartial }`.

8. `memoryIndexDeltaBody`: `memoryIndexLineChanges(baseline, p.Index)`. `memoryIndexLines` keeps page lines only:

```go
// memoryIndexLines returns an index's page lines; the tag header and the
// "Not shown" line are not page lines and never appear in a change block.
func memoryIndexLines(index string) []string {
	var lines []string
	for line := range strings.SplitSeq(index, "\n") {
		if strings.HasPrefix(line, "- ") {
			lines = append(lines, line)
		}
	}
	return lines
}
```

Update `memoryIndexLineChanges`'s comment ("ignoring blank lines" becomes "comparing page lines only").

9. `maybeAppendMemoryContext`'s comment (line 757): `// maybeAppendMemoryContext projects each scope's generated index, never page contents.`

`internal/apptranscript/memory_context.go`: add `memoryContextPartial = " Not every page is shown; the index's last line counts the rest."` to the const block, reword the block comment (the current producer writes the partial sentence; earlier builds wrote one of the two too-long sentences or an explicit flag, and all still decode), and make the size switch `case memoryContextPartial, memoryContextTooLong, memoryContextTooLongPlain:`. Update `memorySessionProjectionReadOnlySuffix`'s neighbor comment only if it names the too-long sentence.

- [ ] **Step 5: Regenerate the wire corpus with a real partial projection**

In `agent/memory_context_wire_fixture_test.go`, replace the truncated case's input (line 86) with the real producer's output:

```go
	var overflow []memoryPage
	for i := range 200 {
		tags := []string{"alpha"}
		if i%4 == 0 {
			tags = nil
		}
		overflow = append(overflow, memoryPage{Path: fmt.Sprintf("opaque-%03d.md", i), Title: fmt.Sprintf("opaque %03d", i),
			Description: "opaque-description " + strings.Repeat("x", 64), HasDescription: true, Tags: tags, Updated: "2026-10-01"})
	}
	partial, truncated := projectMemoryIndex(overflow, memoryProjectionCap)
	s.appendMemoryProjection(memoryProjection{Scope: "project", Status: "current", Truncated: truncated, Content: partial, Index: renderMemoryIndex(overflow)})
	truncatedTurn, truncatedItem := capture(s, "project")
```

Set the case's `Note` to `"A current index whose pages do not all fit the 8192-byte projection: truncated true, the last line counts the pages not shown."` and `wantContent: partial`. The index-change case (line 113-115) must use page lines now: baseline index `"- opaque-change-kept\n- opaque-change-removed\n"`, projection `Content` and `Index` both `"- opaque-change-kept\n- opaque-change-added\n"`. Leave the legacy cases as they are.

Run: `go test ./agent -run 'MemoryContextWireFixtures$' -count=1 -update-wire` (from the root, or `cd agent && go test . -run ... -update-wire`), then the same without `-update-wire`. Inspect `git diff agent/testdata/memorycontextwire/events.json`: only the truncated and index-change cases change.

- [ ] **Step 6: Add the TUI case (same PR)**

In `cmd/evener-tui/hub_transcript_widgets_test.go`, next to `TestMemoryContextTruncatedIndexRendersInBothForms`:

```go
// A partial index refresh shows the line counting the pages it leaves out.
func TestMemoryContextPartialIndexRendersNotShownLine(t *testing.T) {
	rendered := renderDeliveredSystemItem(t, memoryContextWireItem(t, "truncated-project"))
	if !strings.Contains(rendered, "Not shown: ") || !strings.Contains(rendered, "untagged") {
		t.Fatalf("partial index lost its Not shown line: %.400s", rendered)
	}
}
```

The existing truncated test's `strings.Repeat("x", 64)` check still holds (each description carries 64 x's).

- [ ] **Step 7: Run everything this PR touches**

Run, from `agent/`: `go test -count=1 .` (whole package: the memory tests are spread across many files). From the root: `go test -count=1 ./internal/apptranscript ./cmd/evener-tui ./cmd/evener-hub -run 'Memory'`. Web: `cd cmd/evener-hub/frontend && npm test -- src/panes/session/transcript/messages/MemoryContextItem.wire.test.tsx`. Native: `cd mobile-native && npm test -- src/memoryContextRows.test.tsx src/projectedRows.test.ts`. Then the Go gates for `agent/` and the root module.
Expected: all PASS; the web and native suites read the regenerated corpus unchanged in shape.

- [ ] **Step 8: Commit and open PR 4**

```bash
git add agent/session_memory.go agent/session_memory_test.go agent/session_memory_refresh_test.go agent/session_memory_continuation_test.go agent/session_memory_preservation_test.go agent/memory_context_wire_fixture_test.go agent/testdata/memorycontextwire/events.json internal/apptranscript/memory_context.go internal/apptranscript/memory_context_test.go cmd/evener-tui/hub_transcript_widgets_test.go cmd/evener-hub/memory_preservation_test.go
git diff --cached --stat
git commit -m "feat(memory): project and diff the generated index"
```

---

### Task 5: Write path — refusals, stamps, notes, virtual read

**PR boundary:** PR 5, branch `claude/memory-index-5-writes`, base `main` after PR 4 squashes. Estimated non-test lines: ~110 (`session_tools_memory.go` ~95, `execenv.NumberLines` extraction ~15 net).

**Files:**
- Modify: `agent/session_tools_memory.go` (`execOwnMemoryWrite`, `execMemoryRead`)
- Modify: `agent/execenv/local.go:1520-1545` (extract `NumberLines`)
- Test: `agent/session_memory_writes_test.go` (new), `agent/execenv/number_lines_test.go` (new)
- Modify tests: `agent/memory_eval_test.go` (scripted episodes at lines ~670-700 that write `MEMORY.md`), any other test the refusal breaks (`go test` finds them)

**Interfaces:**
- Consumes: `renderMemoryScope` (Task 4); `setMemoryFrontmatterField`, `memoryYAMLField` (Task 3); `parseMemoryPage`, `isMemoryPagePath` (Task 1); `optionalIntArg(args, key) *int` (`agent/session_tools_file.go`).
- Produces:
  - `func NumberLines(text string, offsetLine, limitLines *int) string` (package `execenv`)
  - `var errMemoryIndexGenerated`
  - `func isMemoryIndexPath(file string) bool`
  - `func (s *Session) stampMemoryPage(env *execenv.LocalExecutionEnvironment, file string) string` (returns notes to append)
  - note constants `memoryMissingDescriptionNote`, `memoryUnreadableFrontmatterNote`

- [ ] **Step 1: Write the failing tests**

`agent/execenv/number_lines_test.go`:

```go
package execenv

import "testing"

func TestNumberLines(t *testing.T) {
	t.Parallel()
	two, one := 2, 1
	if got := NumberLines("a\r\nb\nc", &two, &one); got != "   2\tb\n" {
		t.Fatalf("got %q", got)
	}
	if got := NumberLines("a", nil, nil); got != "   1\ta\n" {
		t.Fatalf("got %q", got)
	}
	five := 5
	if got := NumberLines("a\nb", &five, nil); got != "" {
		t.Fatalf("past the end: %q", got)
	}
}
```

`agent/session_memory_writes_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

func memoryWritesSession(t *testing.T) (*Session, string, *agenttest.FakeClock) {
	t.Helper()
	root, clk := t.TempDir(), agenttest.NewFakeClock()
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, clock: clk}))
	return s, filepath.Join(root, "memory", "personal"), clk
}

// Review Focus 3.
func TestMemoryIndexWritesRefused(t *testing.T) {
	t.Parallel()
	s, scope, _ := memoryWritesSession(t)
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
	s, scope, clk := memoryWritesSession(t)
	date := clk.Now().UTC().Format(time.DateOnly)
	body := "---\ndescription: Money is integer cents\nodd:   kept  \n---\n# Cents\nbody\n"
	if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "cents.md", "content": body}); res.IsError {
		t.Fatal(res.Output)
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "cents.md"))
	want := "---\ndescription: Money is integer cents\nodd:   kept  \nupdated: " + date + "\nby: " + memoryYAMLField("by", s.ID())[len("by: "):] + "---\n# Cents\nbody\n"
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
	s, _, _ := memoryWritesSession(t)
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
	s, _, _ := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "bad.md", "content": "---\ndescription: Fix: use cents\n---\n# Cents\n"})
	if res.IsError || !strings.HasSuffix(res.Output, memoryUnreadableFrontmatterNote) {
		t.Fatalf("%+v", res)
	}
}

// memory_read of MEMORY.md renders the whole index, paged by offset and
// limit; with no pages it says so.
func TestMemoryReadRendersTheIndex(t *testing.T) {
	t.Parallel()
	s, scope, _ := memoryWritesSession(t)
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
```

Check the stamped `by:` expectation: `memoryYAMLField("by", s.ID())` is `"by: <id>\n"` (possibly quoted); the test slices off the key so it asserts exactly what the setter writes. If `memoryExec`'s result type is not `tool.ExecResult` with `IsError`/`Output`, follow `memoryExec` at `agent/session_memory_test.go:1687`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test -count=1 -run 'TestNumberLines' ./execenv` and `go test -count=1 -run 'TestMemoryIndexWritesRefused|TestMemoryWrite|TestMemoryReadRendersTheIndex' .`
Expected: compile errors (`NumberLines`, note constants undefined).

- [ ] **Step 3: Extract `execenv.NumberLines`**

In `agent/execenv/local.go`, move the tail of `ReadFileAndBytes` (from `s := strings.ReplaceAll(string(b), "\r\n", "\n")` to the final return) into:

```go
// NumberLines presents text as ReadFile presents a text file: CRLF
// normalized to LF, lines numbered from offsetLine (default 1), at most
// limitLines lines (default 2000), and "" past the end.
func NumberLines(text string, offsetLine, limitLines *int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	if start > len(lines) {
		return ""
	}
	end := min(start-1+limit, len(lines))
	var out strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&out, "%4d\t%s\n", i, lines[i-1])
	}
	return out.String()
}
```

and end `ReadFileAndBytes` with `return NumberLines(string(b), offsetLine, limitLines), b, nil`. The existing `ReadFile` tests pin that nothing changed.

The rendered index ends in `\n`, so `strings.Split` yields a final empty line; the full read therefore numbers one blank line after the last page line. If the test's line count is off by that one, trim the trailing newline before numbering in `execMemoryRead` (not in `NumberLines`, which must keep `ReadFile`'s behavior) and keep the test's count.

- [ ] **Step 4: Implement the write path in `agent/session_tools_memory.go`**

```go
// errMemoryIndexGenerated refuses a write, edit or delete of the index.
var errMemoryIndexGenerated = errors.New("MEMORY.md is generated from each page's frontmatter; edit a page's description or tags instead")

// isMemoryIndexPath reports whether file, cleaned and relative to the scope,
// names the generated index. Case is ignored: on a case-insensitive
// filesystem memory.md is the same file.
func isMemoryIndexPath(file string) bool {
	return strings.EqualFold(file, memoryIndexFile)
}

const (
	memoryMissingDescriptionNote    = "\n\nThis page has no description in its frontmatter, so its index line falls back to its first heading. Add description: <one line> to the frontmatter."
	memoryUnreadableFrontmatterNote = "\n\nThis page's frontmatter is not valid YAML, so its index line falls back to its first heading. Fix the frontmatter; quote a description that contains a colon."
)

// stampMemoryPage sets the updated and by stamps of the Markdown page the
// session just wrote, and returns the notes its tool result should end with.
// A stamp that cannot be written does not undo the write; the note says so.
func (s *Session) stampMemoryPage(env *execenv.LocalExecutionEnvironment, file string) string {
	rel := filepath.ToSlash(file)
	if path.Ext(rel) != ".md" || !isMemoryPagePath(rel) {
		return ""
	}
	abs := filepath.Join(env.WorkingDirectory(), file)
	raw, err := env.ReadFileRaw(abs)
	if err == nil {
		raw = setMemoryFrontmatterField(raw, "updated", "updated: "+s.sclock().Now().UTC().Format(time.DateOnly)+"\n")
		raw = setMemoryFrontmatterField(raw, "by", memoryYAMLField("by", s.ID()))
		err = env.WriteFileRaw(abs, raw, 0o644)
	}
	if err != nil {
		return "\n\nEvener could not stamp this page's updated date: " + err.Error()
	}
	switch page := parseMemoryPage(rel, raw, time.Time{}); {
	case page.Unreadable:
		return memoryUnreadableFrontmatterNote
	case !page.HasDescription:
		return memoryMissingDescriptionNote
	}
	return ""
}
```

`execOwnMemoryWrite` becomes:

```go
func (s *Session) execOwnMemoryWrite(args map[string]any, operation string, write func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any) (any, error)) (any, error) {
	scope, file := stringArg(args, "scope"), filepath.Clean(stringArg(args, "file_path"))
	if isMemoryIndexPath(file) {
		return nil, errMemoryIndexGenerated
	}
	env, forwarded, release, err := s.memoryFileArgs(args, "file_path", operation)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := write(env, forwarded)
	if err != nil {
		return out, err
	}
	if operation != "delete" {
		if notes := s.stampMemoryPage(env, file); notes != "" {
			if text, ok := out.(string); ok {
				out = text + notes
			}
		}
	}
	s.recordOwnMemoryWrite(env, scope, file)
	return out, nil
}
```

Update its doc comment: refuses the generated index; stamps a written page; records the result as the session's own. In `execMemoryRead`, right after `defer release()`:

```go
	if isMemoryIndexPath(file) {
		rendered, err := renderMemoryScope(env)
		if err != nil {
			return nil, err
		}
		if rendered.status == "missing" {
			return "This scope has no pages yet.", nil
		}
		return execenv.NumberLines(strings.TrimSuffix(rendered.full, "\n"), optionalIntArg(args, "offset"), optionalIntArg(args, "limit")), nil
	}
```

and change the later `file != memoryIndexFile` guard to `!isMemoryIndexPath(file)` (now unreachable for the index, but it keeps the page-tracking branch honest if the order changes; delete it instead if golangci flags it).

- [ ] **Step 5: Fix the tests the refusal breaks**

Run (from `agent/`): `go test -count=1 . 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` with `set -o pipefail`. Each failure that wrote, edited or deleted `MEMORY.md` through the tools moves to a page:
- `agent/memory_eval_test.go` scripted episodes (around lines 670-700): the `memory_write` of `MEMORY.md` with `"From the repository root run sh scripts/check.sh\n"` becomes a `memory_write` of `check.md` with content `"---\ndescription: From the repository root run sh scripts/check.sh\n---\nFrom the repository root run sh scripts/check.sh\n"`; the following `memory_edit` targets `check.md`. Update the episode's expected wiki files (`WikiAfter` keys such as `projects/fixture-project/MEMORY.md`) to the page path, and the grader branch at line ~1181 that special-cases `MEMORY.md` only if a run shows it now misclassifies the page write; if it does, report it in the PR body rather than redesigning the grader.
- Any remaining test still expecting an own `MEMORY.md` write to succeed: rewrite it against a page; never weaken the refusal.

- [ ] **Step 6: Run the tests and gates**

Run (from `agent/`): `go test -count=1 . ./execenv` and from the root `go test -count=1 ./cmd/evener-hub -run Memory`, then the Go gates for both modules.
Expected: PASS, gates clean.

- [ ] **Step 7: Commit and open PR 5**

```bash
git add agent/session_tools_memory.go agent/execenv/local.go agent/execenv/number_lines_test.go agent/session_memory_writes_test.go agent/memory_eval_test.go
git diff --cached --stat
git commit -m "feat(memory): refuse index writes, stamp pages, render memory_read of MEMORY.md"
```

---

### Task 6: Prompts, tool descriptions, gardening skill and docs

**PR boundary:** PR 6, branch `claude/memory-index-6-guidance`, base `main` after PR 5 squashes. Estimated non-test lines: ~130 (template ~12, definitions ~4, skill ~18, docs ~95). Two commits: the characterization golden first, then the change.

**Files:**
- Create: `agent/memory_prompt_golden_test.go`, `agent/testdata/memoryprompt/{enabled,personal-only,read-only}.md`
- Modify: `agent/prompts/system.md.tmpl:286,298,300`
- Modify: `agent/internal/tool/definitions.go:16,22`
- Modify: `internal/bundled/skills/gardening-memory/SKILL.md`
- Modify: `docs/product/memory.md`, `docs/tools/memory.md`

**Interfaces:**
- Consumes: `memoryGuidanceHeading`, `refreshModelFacingCaches` (`agent/session_memory_test.go`); `(*Session).renderSystemPrompt(env) (prompt, warning string)`; `nativeMemoryToolNames`; `s.reg.Get(name) *tool.RegisteredTool`.
- Produces: the golden files and `-update-prompt` flag.

- [ ] **Step 1: Pin today's guidance with a whole-output golden**

`agent/memory_prompt_golden_test.go`:

```go
package agent

// The memory guidance and the memory tools' descriptions are prompt text: they
// are pinned whole, per capability shape, never by substring. Regenerate after
// an intended wording change with
//
//	go test ./agent -run 'TestMemoryPromptGolden$' -count=1 -update-prompt
//
// and read the diff.

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updatePromptGoldens = flag.Bool("update-prompt", false,
	"rewrite agent/testdata/memoryprompt from the current memory guidance and tool descriptions")

func TestMemoryPromptGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cfg    SessionConfig
		revoke string
	}{
		{"enabled", SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}, ""},
		{"personal-only", SessionConfig{MemoryStateRoot: t.TempDir()}, ""},
		{"read-only", SessionConfig{MemoryStateRoot: t.TempDir(), MemoryProjectID: "fixture-project"}, "memory_write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withConfig(tc.cfg))
			if tc.revoke != "" {
				s.reg.Remove(tc.revoke)
				refreshModelFacingCaches(s)
			}
			prompt, warning := s.renderSystemPrompt(s.currentEnv())
			if warning != "" {
				t.Fatal(warning)
			}
			_, section, ok := strings.Cut(prompt, memoryGuidanceHeading)
			if !ok {
				t.Fatal("no memory section")
			}
			var out strings.Builder
			out.WriteString(strings.TrimPrefix(memoryGuidanceHeading, "\n\n") + section + "\n\n# Memory tool descriptions\n")
			for _, name := range nativeMemoryToolNames {
				if registered := s.reg.Get(name); registered != nil {
					fmt.Fprintf(&out, "\n## %s\n\n%s\n", name, registered.Definition.Description)
				}
			}
			path := filepath.Join("testdata", "memoryprompt", tc.name+".md")
			got := []byte(out.String())
			if *updatePromptGoldens {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (regenerate with -update-prompt)", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("memory prompt drifted from %s; regenerate with -update-prompt and read the diff.\ngot:\n%s", path, got)
			}
		})
	}
}
```

Run: `cd agent && go test . -run 'TestMemoryPromptGolden$' -count=1 -update-prompt`, then without the flag (PASS). Read the three files: they must hold today's text. Commit:

```bash
git add agent/memory_prompt_golden_test.go agent/testdata/memoryprompt
git diff --cached --stat
git commit -m "test(memory): pin the memory guidance and tool descriptions as goldens"
```

- [ ] **Step 2: Change the guidance (the golden now fails)**

`agent/prompts/system.md.tmpl` line 286: replace

`Each scope keeps an index, MEMORY.md, with one line per page; when an index has entries, it appears in the conversation. When an index line bears on what you are doing, or only gives a status or an id without saying what its page holds, read that page with memory_read{{ if .MemorySearch }}; use memory_search to look for a topic the index doesn't mention{{ end }}.`

with

`Each scope has an index, MEMORY.md, that Evener builds from its pages: one line per page with its description, tags and the date it last changed, under a list of the tags in use. When a scope has pages, its index appears in the conversation; when not every page fits, its last line counts the pages left out by tag. When an index line bears on what you are doing, or only gives a status or an id without saying what its page holds, read that page with memory_read{{ if .MemorySearch }}; use memory_search to find a tag's pages or a topic the index doesn't show{{ end }}.`

Line 298: replace

`Write each page with memory_write: one durable fact, led by the fact or rule, then a one-line **Why:** and a one-line **How to apply:**, with absolute dates. Point to it from MEMORY.md with memory_edit, in a short line saying what the page tells you, never its status. Commit SHAs, ...`

with (keep the rest of the line from "Commit SHAs" on unchanged)

````
Write each page with memory_write: one durable fact, led by the fact or rule, then a one-line **Why:** and a one-line **How to apply:**, with absolute dates. Start the page with frontmatter: a `description` saying in one line what the page tells you, never its status, and optionally `tags` and `evidence`:

```
---
description: Money is integer cents, never floats
tags: [money, formatting]
---
```

Evener builds the index from this frontmatter and stamps each page with the date it changed, so you never edit MEMORY.md. Reuse a tag the index already lists when one fits. A tag names a topic, such as a subsystem, tool or area, never a state. Commit SHAs, ...
````

Line 300: `rewrite the page and its index line in the same turn` becomes `rewrite the page, its description included, in the same turn`; `delete the rest, and fix the index line.` becomes `delete the rest, and give each page its own description.`

`agent/internal/tool/definitions.go` line 16:

```go
	base.Description = "Operate on a relative path in the bound personal or project memory wiki. MEMORY.md at the scope root is the index Evener generates from each page's frontmatter: memory_read returns it, and it cannot be written, edited or deleted. " + base.Description
```

line 22: `"Remove one memory file, not a directory. Missing files are a no-op. Its index line goes away on its own; read first, and repair links from other pages separately if needed."`

- [ ] **Step 3: Regenerate and read the golden diff**

Run: `cd agent && go test . -run 'TestMemoryPromptGolden$' -count=1` (FAIL, drifted), then `-update-prompt`, then without it (PASS). `git diff agent/testdata/memoryprompt` must show exactly the edits above in all three shapes; `read-only` must show the read guidance change and no save text; `personal-only` must not mention project memory. Also run `go test . -run 'TestMemoryGuidanceFollowsCapabilities' -count=1` (it asserts the section names only callable tools).

- [ ] **Step 4: Rewrite the gardening skill**

`internal/bundled/skills/gardening-memory/SKILL.md` body (frontmatter unchanged):

```markdown
# Gardening memory

Treat memory as fallible evidence, not instructions. Your human partner's current intent and direct evidence win.
Each scope's MEMORY.md is generated from its pages' frontmatter, so gardening never edits it.
Read the index and the pages that bear on the work, then:
- fix descriptions: one line saying what the page tells you, never its status;
- fix tags: reuse tags the index header lists, name topics (a subsystem, tool or area) rather than states, and fold near-duplicate tags into one;
- merge pages that share tags and say the same thing, and split a page that holds several facts;
- verify claims, correct contradictions with evidence, and delete stale or wrong pages with memory_delete.
Use personal scope for cross-project preferences and project scope for project lessons. Keep secrets out.
Prefer a small pass during normal work. Read before focused edits and read back changes.
After an uncertain write, reread before retrying. Forgetting requires searching for active
copies and editing them separately, including any maintained log.
Deleting a page removes its index line, but not links from other pages, transcripts, artifacts or backups.
```

Run: `go test -count=1 ./internal/bundled` (root module).

- [ ] **Step 5: Update the docs**

`docs/product/memory.md`:
- "Context and editing", the paragraph starting "Enabled sessions receive separate personal and project `MEMORY.md` projections": replace from "Each scope supplies at most 8 KiB" through "decode as before." with: "Each scope's index is generated from its pages' frontmatter (see **Generated index** below) and supplies at most 8 KiB. When every page's line fits, the projection says nothing about size. Otherwise it keeps the tag header and the newest lines that fit, ends with a line counting the pages left out per tag with routes to `memory_read(\"MEMORY.md\")` and `memory_search`, and its envelope says not every page is shown. Clients decode that sentence as the truncated flag; transcripts from earlier builds, which said the index was too long or carried an explicit \"truncated true/false\", decode as before."
- Add a `## Generated index` section after "Context and editing" that states the spec's "Page format", "What counts as a page", "Fallback description" and "The generated index" rules (copy them, adjusted to prose), the stamps, the `MEMORY.md` refusals, the virtual `memory_read`, the migration (including that it needs no lock and why), and this plan's rulings 3, 5, 6, 7, 10, 11 and 12.
- In the guidance paragraph: "under an index line that says what the page holds" becomes "with a frontmatter description that says what the page holds and topic tags that reuse the index's"; "A status-only index line is a reason to read its page." stays.
- The baseline paragraph: replace "A projected index with content becomes the baseline, held as projected (cut at the same 8 KiB cap)" with "A projected index with pages becomes the baseline, held whole (every page line, not only those the budget showed)"; replace "When the session itself writes, edits or deletes a `MEMORY.md` through the memory tools, the result, cut the same way, becomes its baseline" with "When the session itself writes, edits or deletes any page through the memory tools, the index is rendered again and becomes its baseline"; replace "the quoted lines added and removed since the baseline (blank lines ignored)" with "the quoted page lines added and removed since the baseline (the tag header and the not-shown line are never listed)"; delete "Both sides are compared as projected, within the 8 KiB cap, so a change past the cap appends nothing."
- "A long index is covered by its projection instead." becomes "The index is never noted as long; its projection counts what it leaves out."
- The "Content has no required schema" paragraph becomes: "Pages carry frontmatter (`description`, optional `tags` and `evidence`; Evener adds `updated` and `by`). A page without a description still appears, under its fallback description. Other content has no required schema, extension or link rule. An optional `log.md` is ordinary model-authored content, not a runtime-maintained change log."
- Gardening paragraph: "repair summaries and links" becomes "fix descriptions and tags, merge pages that share tags, and delete stale pages".
- "Page and index edits are separate calls, not a wiki transaction." becomes "Each page edit is its own call, not a wiki transaction; the index follows the pages."
- Forgetting: "including the index and any maintained log. Deleting a page removes one file; link repair is separate." becomes "including any maintained log. Deleting a page removes one file and its index line; repairing links from other pages is separate."

`docs/tools/memory.md`:
- `memory_read` row: append "`MEMORY.md` at the scope root is not a file: the read renders the generated index in full, with no size cap, paged by `offset`/`limit`, or says the scope has no pages yet."
- `memory_write` and `memory_edit` rows: append "`MEMORY.md` at the scope root (any case) is refused. After a successful write or edit of a Markdown page, Evener sets its `updated` and `by` frontmatter, keeping every other byte, and the result notes a missing or unparseable description."
- `memory_delete` row: replace "Repair links separately." with "`MEMORY.md` at the scope root is refused. The page's index line disappears with it; repair links from other pages separately."
- `memory_search` row: append "It never matches the generated index, which is not a file."
- "Focused correction": replace "Index and page changes require separate calls." with "The index follows the pages; there is no index edit."
- "Large results and recovery", final paragraph: replace from "An automatic index projection cut at the cap" to "returns the whole index." with "An automatic index projection that does not fit its budget ends with a line counting the pages it leaves out; `memory_read` of `MEMORY.md` returns the whole index."

Run the docs skill's checks only if the repo has a docs lint target already wired into the gate (`grep -n docs make/*.mk` without `head`); do not add one.

- [ ] **Step 6: Run gates and commit**

Run the Go gates for `agent/` and the root module; `go test -count=1 . -run 'Memory'` from `agent/`.

```bash
git add agent/prompts/system.md.tmpl agent/internal/tool/definitions.go agent/testdata/memoryprompt internal/bundled/skills/gardening-memory/SKILL.md docs/product/memory.md docs/tools/memory.md
git diff --cached --stat
git commit -m "prompt(memory): pages carry frontmatter; the index is generated"
```

PR 6 body: paste the golden diff for the `enabled` shape.

---

### Task 7: Memory lab `index-overflow` scenario and index checks

**PR boundary:** PR 7, branch `claude/memory-index-7-lab`, base `main` after PR 6 squashes (it can be reviewed in parallel; it touches only `tools/prompt-eval/memory-lab`). Estimated non-test lines: ~150 (generator ~110 Python, check edits ~10, README ~15, plus generated seed data which is not counted).

**Files:**
- Create: `tools/prompt-eval/memory-lab/scenarios/index-overflow/make_seed.py`
- Create (generated, committed): `tools/prompt-eval/memory-lab/scenarios/index-overflow/scenario.json`, `.../index-overflow/seed/*.md`
- Modify: `tools/prompt-eval/memory-lab/scenarios/{long-project,progress-log,progress-notes}/scenario.json` (the "index lines" checks)
- Modify: `tools/prompt-eval/memory-lab/README.md` (scenario table)

**Interfaces:**
- Consumes: the lab's scenario format (`memory-lab` docstring): `fixture_from`, `seed_project_memory`, `checks`, `trace`.
- Produces: scenario `index-overflow`.

- [ ] **Step 1: Write the generator**

`scenarios/index-overflow/make_seed.py`:

```python
#!/usr/bin/env python3
"""Generate the index-overflow scenario: a seeded project memory of about 120
tagged pages whose generated index exceeds the 8 KiB projection, with the one
fact the task needs on the oldest page, which the projection leaves out.

Why a generator: 120 pages by hand drift; this keeps the seed, its hand-written
MEMORY.md (so a build from before the generated index also gets an index) and
the scenario's tag-reuse check in step. Run it from anywhere after editing it:

    python3 scenarios/index-overflow/make_seed.py

It rewrites seed/ and scenario.json next to itself, then run ./memory-lab check.
"""
import datetime, json, os, shutil

HERE = os.path.dirname(os.path.abspath(__file__))
SEED = os.path.join(HERE, "seed")

TOPICS = {
    "vitest": "Vitest run {n} needs --pool=forks when worker {n} crashes on teardown",
    "indexeddb": "IndexedDB store {n} keeps records as plain JSON, never class instances",
    "release": "Release step {n} tags only after main CI is green on the merge commit",
    "ci": "CI job {n} caches modules by go.sum hash, never by branch name",
    "logging": "Log field {n} uses snake_case keys through log/slog",
    "catalog": "Catalog rule {n}: SKUs are lowercase with no spaces",
    "cart": "Cart rule {n}: line quantities are whole numbers",
    "tests": "Test helper {n} builds catalogs with prices in cents",
    "docs": "Doc comment rule {n}: start with the identifier's name",
    "api": "Exported API rule {n}: no new exported names without a doc comment",
    "pricing": "Pricing rule {n}: totals are computed in integer cents",
}
TARGET = {
    "path": "coupon-stacking.md",
    "title": "Coupons never stack",
    "description": "A cart's coupons never stack: only the largest percent applies",
    "tags": ["coupons", "pricing"],
    "updated": "2026-03-02",
    "body": "Only the largest coupon percent applies to a cart; coupons never stack or compound.\n\n"
            "**Why:** finance ruled on 2026-03-02 that stacked coupons let carts reach zero.\n\n"
            "**How to apply:** a function applying several coupons takes the largest percent and calls ApplyCoupon once.\n",
}


def new_pages_check(seed_tags, condition):
    """A shell check over the project pages this stage wrote (any page not
    marked by: seed-fixture). It passes only when the stage wrote at least one
    page and condition holds; condition sees `new`, a list of (tags, described)
    pairs, and `seed`, the seeded tag set."""
    return (
        "python3 - <<'EOF'\n"
        "import glob, os, re, sys\n"
        f"seed = set({seed_tags!r})\n"
        "new = []\n"
        "for f in glob.glob(os.environ['XDG_STATE_HOME'] + '/evener/memory/projects/*/**/*.md', recursive=True):\n"
        "    text = open(f).read()\n"
        "    if os.path.basename(f) == 'MEMORY.md' or 'by: seed-fixture' in text:\n"
        "        continue\n"
        "    m = re.match(r'---\\n(.*?)\\n---\\n', text, re.S)\n"
        "    block = m.group(1) if m else ''\n"
        "    flow = re.search(r'^tags:\\s*\\[(.*)\\]', block, re.M)\n"
        "    if flow:\n"
        "        tags = [t.strip().strip('\"\\'') for t in flow.group(1).split(',')]\n"
        "    elif re.search(r'^tags:\\s*$', block, re.M):\n"
        "        tags = re.findall(r'^- (.+)$', block.split('tags:', 1)[1], re.M)\n"
        "    else:\n"
        "        tags = []\n"
        "    new.append(([t.lower() for t in tags if t], 'description:' in block))\n"
        f"sys.exit(0 if new and {condition} else 1)\n"
        "EOF"
    )


def page(path, title, description, tags, updated, body):
    return (f"---\ndescription: {description}\ntags: [{', '.join(tags)}]\n"
            f"updated: {updated}\nby: seed-fixture\n---\n# {title}\n\n{body}")


def main():
    shutil.rmtree(SEED, ignore_errors=True)
    os.makedirs(SEED)
    pages = []
    day = datetime.date(2026, 9, 30)
    n = 0
    while len(pages) < 119:
        for tag, template in TOPICS.items():
            if len(pages) == 119:
                break
            n += 1
            tags = [tag] if n % 4 else [tag, "tests"]
            pages.append({"path": f"{tag}/{tag}-{n:03d}.md", "title": f"{tag.capitalize()} note {n}",
                          "description": template.format(n=n), "tags": tags, "updated": day.isoformat(),
                          "body": template.format(n=n) + ".\n"})
            day -= datetime.timedelta(days=1)
    pages.append(TARGET)
    for p in pages:
        dest = os.path.join(SEED, p["path"])
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        with open(dest, "w") as f:
            f.write(page(p["path"], p["title"], p["description"], p["tags"], p["updated"], p["body"]))
    # Newest first, as the generated index orders them; the target is last, past 8 KiB.
    with open(os.path.join(SEED, "MEMORY.md"), "w") as f:
        f.write("# Project memory\n\n")
        for p in pages:
            f.write(f"- [{p['title']}]({p['path']}) — {p['description']}\n")
    seed_tags = sorted({t for p in pages for t in p["tags"]})
    reuse = new_pages_check(seed_tags, "all(tags and set(tags) <= seed for tags, _ in new)")
    described = new_pages_check(seed_tags, "all(described for _, described in new)")
    heldout = (
        "cat > zz_heldout_test.go <<'EOF'\npackage shop\n\nimport \"testing\"\n\n"
        "func TestHeldoutApplyCouponsLargestOnly(t *testing.T) {\n"
        "\tfor _, c := range []struct {\n\t\ttotal    int\n\t\tpercents []int\n\t\twant     int\n\t}{\n"
        "\t\t{1000, []int{10, 20}, 800},\n\t\t{1000, []int{20, 10}, 800},\n\t\t{1000, nil, 1000},\n\t\t{999, []int{50}, 500},\n"
        "\t} {\n\t\tif got := ApplyCoupons(c.total, c.percents); got != c.want {\n"
        "\t\t\tt.Errorf(\"ApplyCoupons(%d, %v) = %d, want %d\", c.total, c.percents, got, c.want)\n\t\t}\n\t}\n}\nEOF\n"
        "go test -count=1 -run Heldout ./...; rc=$?; rm -f zz_heldout_test.go; exit $rc"
    )
    scenario = {
        "arms": ["on"],
        "fixture_from": "migration",
        "stages": [{
            "name": "B",
            "seed_project_memory": "seed",
            "prompt": "Add ApplyCoupons(total int, percents []int) int to the shop package: it applies a cart's coupons to its total, in cents. Add tests.",
            "checks": [
                {"name": "tests pass", "run": "go test ./..."},
                {"name": "held-out largest coupon only", "run": heldout},
                {"name": "new pages reuse seeded tags", "run": reuse},
                {"name": "new pages have a description", "run": described},
            ],
            "trace": [
                {"name": "read the coupon page", "tool": "memory_read", "regex": "coupon"},
                {"name": "searched memory for coupons", "tool": "memory_search", "regex": "(?i)coupon"},
            ],
        }],
    }
    with open(os.path.join(HERE, "scenario.json"), "w") as f:
        json.dump(scenario, f, indent=2)
        f.write("\n")


if __name__ == "__main__":
    main()
```

Both page checks fail when the stage writes no page, so they cannot pass on a run that ignored memory. Confirm `trace` checks match against the tool call's arguments by reading the lab's trace grader (`grep -n '"trace"' -A20 memory-lab` without `head`) before relying on the regexes.

- [ ] **Step 2: Generate and validate**

Run, from `tools/prompt-eval/memory-lab`:

```bash
python3 scenarios/index-overflow/make_seed.py
ls scenarios/index-overflow/seed | wc -l          # 11 topic dirs + coupon-stacking.md + MEMORY.md
wc -c scenarios/index-overflow/seed/MEMORY.md      # must exceed 8192
grep -n coupon-stacking scenarios/index-overflow/seed/MEMORY.md   # last line, past byte 8192
./memory-lab check
```

Expected: `MEMORY.md` over 8192 bytes with the coupon line last; `check` prints OK for every scenario. To prove the generated index also overflows, build the stack's `evener` and render once: `go build -o bin/evener-try ../../../cmd/evener`, then run the scenario with `--reps 1` (Task 8 does this at scale).

- [ ] **Step 3: Make the index-line checks work for both builds**

In `scenarios/long-project/scenario.json` (4 places), `progress-log` (2) and `progress-notes` (1), replace each check whose `run` is the `perl ... MEMORY.md ... length > 200` command with:

```
perl -e 'for $f (glob("$ENV{XDG_STATE_HOME}/evener/memory/{personal,projects/*}/*.md")) { open F, $f; my $idx = $f =~ m{/MEMORY\.md$}; while (<F>) { chomp; next unless $idx || s/^description:\s*//; exit 1 if length > 200 } }'
```

(JSON-escape the quotes as the existing entries do.) Under the base build it measures the hand-written index's lines; under the new build there is no `MEMORY.md` and it measures each page's description, which is what becomes the index line. Keep each check's name.

Run `./memory-lab check` again.

- [ ] **Step 4: README rows**

Add to the scenario table:

`| index-overflow | Seeded project memory of 120 tagged pages whose index overflows the 8 KiB projection. The fact the task needs (coupons never stack) is on the oldest page, which the projection leaves out. B adds ApplyCoupons | Measures finding a page through the "Not shown" tag counts or memory_search, and whether new pages reuse seeded tags and carry a description. The seed also has a hand-written MEMORY.md so a build without the generated index gets one; regenerate with make_seed.py. |`

and one sentence under "Scenario format": "Seeded `MEMORY.md` files are migrated into page frontmatter by builds with the generated index, so a seeded scenario compares the hand-written index (base) with the generated one (try) without per-version seeds."

- [ ] **Step 5: Commit and open PR 7**

```bash
git add tools/prompt-eval/memory-lab/scenarios/index-overflow tools/prompt-eval/memory-lab/scenarios/long-project/scenario.json tools/prompt-eval/memory-lab/scenarios/progress-log/scenario.json tools/prompt-eval/memory-lab/scenarios/progress-notes/scenario.json tools/prompt-eval/memory-lab/README.md
git diff --cached --stat
git commit -m "tools(memory-lab): index-overflow scenario; index-line checks read descriptions"
```

---

### Task 8: Comparison round (after PRs 1–7 land; no PR)

Run the spec's evaluation and report to Jesse. This produces results under the git-ignored `tools/prompt-eval/results/`, not code.

- [ ] **Step 1: Build both binaries**

```bash
cd tools/prompt-eval/memory-lab
mkdir -p bin
go build -o bin/projid ./projid
BASE=$(git log --format=%H -1 <PR-1 squash commit>^)   # main just before the stack
git worktree add ../../../.claude/worktrees/memlab-base "$BASE"
(cd ../../../.claude/worktrees/memlab-base && go build -o "$OLDPWD/bin/evener-base" ./cmd/evener)
git worktree remove ../../../.claude/worktrees/memlab-base
go build -o bin/evener-try ../../../cmd/evener
```

- [ ] **Step 2: Run the round** (lunarouter background pool: keep `--jobs` at or under 30 across concurrent runs)

```bash
./memory-lab run --version base=bin/evener-base --version try=bin/evener-try \
  --model lunarouter/deepseek-4.1-flash-background --work-root ~/Developer \
  --scenarios scenarios/feedback,scenarios/many-facts,scenarios/fact-changes,scenarios/polluted-seed,scenarios/long-project,scenarios/recall-seeded,scenarios/index-overflow \
  --reps 8 --jobs 10 --out $PWD/../results/memory-lab/generated-index-r1
```

`index-overflow` was added after `main`'s base build: the base binary still runs it (it reads the hand-written `MEMORY.md`).

- [ ] **Step 3: Report**

`./memory-lab report $PWD/../results/memory-lab/generated-index-r1`. For each arm report pass rates per scenario and check, tag reuse and description presence (the two `index-overflow` checks), and index-line quality (read 3 `try` trials' memory snapshots with `./memory-lab show` and quote their generated lines). Use `./memory-lab ask` on any `try` trial that missed the coupon fact before proposing prompt changes. Send Jesse the summary; no prompt change without his call.

---

## Self-review notes

- Spec coverage: Decisions 1–6 (Tasks 4, 1, 1, 3, 5, 4); page format and page rule (Task 1); fallback (Task 1); generated index and budget (Task 2); reading (Task 5); writes (Task 5); per-turn updates and own writes (Task 4); migration (Task 3, wired in Task 4); prompts, tools, skill, docs (Task 6); client decoding and regenerated fixture (Task 4); evaluation (Tasks 7, 8).
- Names used across tasks: `memoryPage`, `parseMemoryPage`, `listMemoryPages`, `isMemoryPagePath`, `splitMemoryFrontmatter` (1) → `renderMemoryIndex`, `projectMemoryIndex`, `sortedMemoryPages`, `memoryNotShownLine`, `memoryProjectionCap` (2) → `setMemoryFrontmatterField`, `memoryYAMLField`, `migrateMemoryScope`, `memoryLegacyIndexBackup` (3) → `renderMemoryScope`, `renderedMemoryScope`, `memoryProjection.Index`, `memoryIndexPartial` (4) → `isMemoryIndexPath`, `stampMemoryPage`, `execenv.NumberLines` (5).
