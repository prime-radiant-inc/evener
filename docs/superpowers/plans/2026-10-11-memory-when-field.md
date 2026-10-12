# Memory `when` Field Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Memory pages carry an optional `when` frontmatter field saying when a future session needs them, and the generated index shows it after the description.

**Architecture:** `parseMemoryPage` reads `when` into `memoryPage.When`; `memoryIndexLine` renders `when` in place of the description; the write path quotes a broken `when` line and notes a missing one; the `## Memory` prompt asks for it. No wire, client or tool-schema change.

**Tech Stack:** Go (agent module), YAML frontmatter via `agent/internal/frontmatter`.

**Spec:** `docs/superpowers/specs/2026-10-11-memory-when-field-design.md`

## Global Constraints

- Evener prompts say "your human partner", never "the user".
- Prompt text is pinned only by whole-output goldens (`-update-prompt`), never by string-matching tests.
- Default tests are deterministic: scripted provider, no network or credentials.
- Gofmt with `$(go env GOROOT)/bin/gofmt`; `golangci-lint run --allow-serial-runners ./...` in `agent`; `go vet ./...` and `go vet -tags evenerfuzz ./...` in `agent`.
- No backward-compatibility shims; a page without `when` simply renders as today.
- Index line form for a page with `when` (decided after the eval; see the spec's Eval results): `- [Title](path) — <when> [tags] (updated YYYY-MM-DD)`; a page without one shows its description.

## Review Focus

1. A `when` value containing `: ` written unquoted: the write must still produce readable frontmatter (Task 2 test).
2. A non-string `when` (number, list, map) or an empty one: treated as missing, line renders as today (Task 1 test).
3. A multi-line `when` (YAML block scalar): collapses to one line so the index stays one line per page (Task 1 test).
4. A page with `when` but unreadable frontmatter: renders as today's unreadable line, shows no `when` (Task 1 test, falls out of parsing).
5. A changed `when` on an existing page: the index delta reports the new line (Task 1 test).

---

### Task 1: Parse and render `when`

**Files:**
- Modify: `agent/memory_page.go` (struct `memoryPage`, `parseMemoryPage`)
- Modify: `agent/memory_index_render.go` (`memoryIndexLine`)
- Test: `agent/memory_page_test.go` (`TestParseMemoryPage` table), `agent/memory_index_render_test.go` (`TestRenderMemoryIndex`)
- Test: the index-delta test that covers a changed description (find it in `agent/session_memory_refresh_test.go`; grep `changed description` / `description` there) — add a sibling case for `when`
- Docs: `docs/product/memory.md` (**Page format** example and key list, **The rendered index** example)

**Interfaces:**
- Produces: `memoryPage.When string` (one line, whitespace collapsed, "" when missing). Task 2 reads it.

- [ ] **Step 1: Add failing parse cases** to the `TestParseMemoryPage` table:

```go
{"when field", "a.md",
	"---\ndescription: d\nwhen: Writing a commit message in this repo\n---\n",
	memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, When: "Writing a commit message in this repo", Frontmatter: true, ModTime: mod}},
{"multi-line when collapses", "a.md",
	"---\ndescription: d\nwhen: |-\n  before changing\n  Total\n---\n",
	memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, When: "before changing Total", Frontmatter: true, ModTime: mod}},
{"non-string when counts as missing", "a.md",
	"---\ndescription: d\nwhen: [a, b]\n---\n",
	memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Frontmatter: true, ModTime: mod}},
{"empty when counts as missing", "a.md",
	"---\ndescription: d\nwhen: \"  \"\n---\n",
	memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Frontmatter: true, ModTime: mod}},
{"unreadable frontmatter drops when", "bad.md",
	"---\ndescription: Fix: use cents\nwhen: w\n---\n# Cents rule\n",
	memoryPage{Path: "bad.md", Title: "Cents rule", Description: "Cents rule " + memoryNoDescription, Unreadable: true, Frontmatter: true, ModTime: mod}},
```

- [ ] **Step 2: Run** `go test ./agent -run 'TestParseMemoryPage$' -count=1` from the repo root. Expected: compile failure, `unknown field When`.

- [ ] **Step 3: Implement parsing.** In `memoryPage` add, after `HasDescription`:

```go
	When           string // one line: when a future session needs the page; "" when there is none
```

In `parseMemoryPage`, after the description block:

```go
	if when, ok := doc.Meta["when"].(string); ok {
		p.When = collapseWhitespace(when)
	}
```

(`collapseWhitespace` trims, so a blank value becomes "".) Run Step 2's command: PASS.

- [ ] **Step 4: Add the failing render case.** In `TestRenderMemoryIndex`, give the `cents.md` page `When: "Formatting or storing a price"` and change its expected line to:

```go
"- [Integer cents](cents.md) — Formatting or storing a price [money, formatting] (updated 2026-10-08)\n" +
```

Run `go test ./agent -run 'TestRenderMemoryIndex$' -count=1`. Expected: FAIL, line still shows the description.

- [ ] **Step 5: Implement rendering.** In `memoryIndexLine`, choose the text after the link: the description, or `p.When` when present:

```go
	summary := p.Description
	if p.When != "" {
		summary = p.When
	}
```

Run Step 4's command: PASS. Then `go test ./agent -run 'Memory' -count=1` to catch other expectations; fix any that legitimately change.

- [ ] **Step 6: Index delta case.** Next to the existing test proving a changed description reaches the index delta, add the same test changing only `when` on an existing page; assert the delta carries `added "…<new>…"` for that page. Watch it pass (it should, since the line changed); if the delta machinery compares something other than the line, fix that.

- [ ] **Step 7: Docs.** In `docs/product/memory.md` **Page format**: add `when: Formatting or storing a price` to the example page, and a sentence after the `description` paragraph: "`when` is one line saying when a future session needs the page, written like a skill's trigger; the index line shows it instead of the description. A non-string or empty value counts as missing and renders nothing." Update **The rendered index** example's cents line to match Step 4.

- [ ] **Step 8: Regenerate goldens** whose index lines changed: `go test ./agent -run 'MemoryContextWireFixtures$' -count=1 -update-wire` (only if their fixtures gained `when`; otherwise they stay as is), read the diff.

- [ ] **Step 9: Commit**

```bash
git add agent/memory_page.go agent/memory_index_render.go agent/memory_page_test.go agent/memory_index_render_test.go agent/session_memory_refresh_test.go docs/product/memory.md
git diff --cached --stat
git commit -m "feat(memory): pages say when to recall them in the index"
```

### Task 2: Write path: quote a broken `when`, note a missing one

**Files:**
- Modify: `agent/memory_migrate.go` (`repairMemoryFrontmatter` key check, its doc comment)
- Modify: `agent/session_tools_memory.go` (notes const block, `stampMemoryPage` switch)
- Test: `agent/session_memory_writes_test.go` (`TestMemoryWriteQuotesAnUnquotedColonValue`, `TestMemoryWriteNotesMissingDescription`, plus every test that asserts a described write has no notes)
- Docs: `docs/product/memory.md` (the frontmatter-repair and write-note text; grep "quoted" and "no description")

**Interfaces:**
- Consumes: `memoryPage.When` from Task 1.
- Produces: `memoryMissingWhenNote` const.

- [ ] **Step 1: Failing quote case.** Add to the `TestMemoryWriteQuotesAnUnquotedColonValue` table:

```go
"when.md": {"description: d\nwhen: Running `make: test`\n", "description: d\nwhen: 'Running `make: test`'\n"},
```

That test also asserts the result has no notes (`strings.Contains(res.Output, "\n\n")`); Step 3 below adds a missing-`when` note, so give every other table entry a `when:` line in both `block` and `want` (e.g. append `when: w\n`) so the assertion keeps meaning "no repair notes".

- [ ] **Step 2: Run** `go test ./agent -run 'TestMemoryWriteQuotesAnUnquotedColonValue$' -count=1`. Expected: FAIL on `when.md` (frontmatter unreadable note or unquoted line).

- [ ] **Step 3: Implement quoting.** In `repairMemoryFrontmatter` change the key test to

```go
(key != "description" && key != "evidence" && key != "when")
```

and its doc comment's "each top-level description or evidence line" to "each top-level description, when or evidence line". Run Step 2: PASS.

- [ ] **Step 4: Failing note test.** Add to `agent/session_memory_writes_test.go`:

```go
func TestMemoryWriteNotesMissingWhen(t *testing.T) {
	t.Parallel()
	s, _ := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "a.md", "content": "---\ndescription: has one\n---\n"})
	if res.IsError || !strings.HasSuffix(res.Output, memoryMissingWhenNote) {
		t.Fatalf("%+v", res)
	}
	res = memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "b.md", "content": "---\ndescription: has one\nwhen: Writing a commit message\n---\n"})
	if strings.Contains(res.Output, "\n\n") {
		t.Fatalf("page with when noted: %q", res.Output)
	}
	res = memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "c.md", "content": "# A heading\n"})
	if !strings.HasSuffix(res.Output, memoryMissingDescriptionNote) || strings.Contains(res.Output, memoryMissingWhenNote) {
		t.Fatalf("undescribed page should get only the description note: %q", res.Output)
	}
}
```

Run `go test ./agent -run 'TestMemoryWriteNotesMissingWhen$' -count=1`. Expected: compile failure, `undefined: memoryMissingWhenNote`.

- [ ] **Step 5: Implement the note.** In the const block in `agent/session_tools_memory.go` add:

```go
	memoryMissingWhenNote = "\n\nThis page has no when in its frontmatter. Add when: <one line> saying when a future session will need this page, the way a skill says when to use it."
```

and in `stampMemoryPage`'s switch add a last case:

```go
	case page.When == "":
		notes = memoryMissingWhenNote
```

Run Step 4: PASS.

- [ ] **Step 6: Fix fallout.** Run `go test ./agent -run 'Memory|memory' -count=1`. Tests that write a described page and assert no notes now see the `when` note: add a `when:` line to their page content (preferred, keeps the assertion's meaning) rather than loosening assertions. Do not delete coverage.

- [ ] **Step 7: Docs.** In `docs/product/memory.md`, where the write notes and frontmatter repair are described, add `when` to the quoted keys and describe the missing-`when` note (fires only when the page has a readable description and no `when`).

- [ ] **Step 8: Commit**

```bash
git add agent/memory_migrate.go agent/session_tools_memory.go agent/session_memory_writes_test.go docs/product/memory.md
git diff --cached --stat
git commit -m "feat(memory): writes quote a broken when line and note a missing one"
```

### Task 3: Prompt guidance

**Files:**
- Modify: `agent/prompts/system.md.tmpl` (the `## Memory` paragraph starting "Write each page with memory_write")
- Regenerate: `agent/testdata/memoryprompt/*` (and any other prompt golden the change touches)

- [ ] **Step 1: Edit the frontmatter sentence.** Replace

```text
Start it with frontmatter: a one-line `description` of what the page tells you, never its status, and optionally `tags` naming topics (reuse the index's) and `evidence`.
```

with

```text
Start it with frontmatter: a one-line `description` of what the page tells you, never its status; a one-line `when` saying when a future session will need the page, the way a skill says when to use it ("Before changing Total's signature", "When writing a commit message in this repo"); and optionally `tags` naming topics (reuse the index's) and `evidence`.
```

Also extend "When a fact changes … rewrite the page, its description included" to "its description and when included".

- [ ] **Step 2: Regenerate goldens:** `go test ./agent -run 'Prompt' -count=1 -update-prompt`, then run `go test ./agent -run 'Prompt|Golden' -count=1` without the flag: PASS. Read the golden diff: only the edited sentences change.

- [ ] **Step 3: Commit**

```bash
git add agent/prompts/system.md.tmpl agent/testdata
git diff --cached --stat
git commit -m "feat(memory): the memory guidance asks for a when line"
```

### Task 4: Gates and PR

- [ ] In `agent`: `go build ./...`, `go vet ./...`, `go vet -tags evenerfuzz ./...`, `$(go env GOROOT)/bin/gofmt -l .` (empty), `golangci-lint run --allow-serial-runners ./...`, `go test -race -count=1 -run 'Memory|memory|Prompt' ./...`.
- [ ] `/simplify` on the branch diff; apply fixes; re-run the gates.
- [ ] Merge `origin/main`, push, open a regular PR (base main) whose body links the spec, lists the tests, states the /simplify outcome, and says merge waits on the spike/memory-when eval.
