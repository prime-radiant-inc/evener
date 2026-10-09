# Generated memory index

Status: approved direction (Jesse, 2026-10-08). Jesse decided the index is generated, virtual, tagged and stamped; the remaining calls below were delegated to the implementer ("you're going to build it and then you're going to do evals").

## Why

Each memory scope's `MEMORY.md` is written by hand today. Agents must add or fix an index line with `memory_edit` every time they write a page, and the two drift apart: magic-kingdom's cleanup on 2026-10-08 found pages with no index line, index lines naming status instead of the fact, and four hand-made topical sub-index pages in personal memory. The personal index also sat at 7.9 KB of an 8 KB projection cap, so descriptions had to be cut to fit. Nothing in a hand-written line says when a fact was written or by whom.

## Decisions

1. **The index is virtual.** `MEMORY.md` is a view Evener builds from the pages in a scope. No file named `MEMORY.md` is written. (Jesse)
2. **Pages carry frontmatter.** `description` (required, one line), `tags` (optional), `evidence` (optional). (Jesse)
3. **Tags are plain tags.** No primary tag, no reserved tags, no type field. (Jesse)
4. **Existing pages: migrate and fall back.** Old index lines move into their pages' frontmatter once; pages with no line fall back to their first heading. (Jesse, option C)
5. **Evener stamps `updated` and `by`** into the frontmatter on every successful write. (Jesse, option A)
6. **Removed and changed pages still reach running sessions** through the existing per-turn index update. (Jesse)

## Page format

```markdown
---
description: Money is integer cents, never floats
tags: [money, formatting]
evidence: file:shop/price.go:Format
updated: 2026-10-08
by: session-3f5637ba
---
# Integer cents
Prices are stored and computed as integer cents...
```

- `description`: one line. Newlines are collapsed to spaces when rendered.
- `tags`: a YAML list (a single string is accepted as one tag). Each tag is trimmed, lowercased, and runs of whitespace become `-`. Empty tags and duplicates are dropped.
- `evidence`: a free-form string, rendered nowhere in the index; it is for whoever reads the page.
- `updated`: `YYYY-MM-DD`, UTC. `by`: the writing session's id as Evener already names sessions in transcripts.
- Unknown frontmatter keys are kept untouched and ignored.
- Frontmatter is parsed with the existing `agent/internal/frontmatter` package. A page whose frontmatter fails to parse is still a page: it renders with the fallback description and a `(frontmatter unreadable)` marker.

**What counts as a page:** every regular file under the scope root, in subdirectories too, except any path with a segment starting with `.` (the same rule `memory_search` uses) and except a file named `MEMORY.md` at the root. Only `.md` files are parsed for frontmatter; any other file renders with its filename as the description. Symlinks are skipped, as scope confinement already refuses them.

**Fallback description**, when `description` is missing or empty: the page's first Markdown heading text, else its first non-blank body line, cut to 120 characters, followed by `(no description)`.

## The generated index

```
Tags: formatting (3), money (2), vitest (6)
- [Integer cents](cents.md) — Money is integer cents, never floats [money, formatting] (updated 2026-10-08)
- [Vitest read-only loader](vitest/loader.md) — read-only Vitest needs --configLoader runner [vitest] (updated 2026-10-07)
```

- Header: `Tags:` and every tag with its page count, alphabetical. Omitted when no page has tags. A projection the whole index does not fit caps it (see Budget).
- One line per page, newest `updated` first; ties and pages with no `updated` order by path. A page with no `updated` uses its file modification date for sorting and shows no date.
- Title: the first heading if there is one, else the filename without extension.
- Line shape: `- [Title](relative/path) — description [tags] (updated YYYY-MM-DD)`, omitting `[tags]` when there are none and `(updated …)` when there is no stamp.

**Budget.** The projection into context keeps today's 8 KiB cap. When the rendered index is larger, the projection keeps the header, then the newest lines that fit, then one closing line: `Not shown: N pages (vitest 4, indexeddb 3, untagged 2).` The per-tag counts in that line count a page once under each of its tags. In that projection the header and the closing line each list only their most-used tags (ties by name) that fit in 512 bytes, then `and M more tags`; the header shows the tags it keeps alphabetically, the closing line by count. A scope can drift into hundreds of tags, and listed in full they would push every page line out of the projection; the byte cap keeps both lists to about an eighth of the 8 KiB however many or long the tags are, so page lines are never dropped to make room for tag lists. The full rendering that `memory_read` returns has no cap and lists every tag. This replaces today's "the index is too long" sentence; the truncation flag in the projection envelope stays so clients keep decoding it.

**Reading.** `memory_read` of `MEMORY.md` returns the full rendered index with no cap, paged by the ordinary `offset`/`limit`. `memory_search` never matches the virtual index (it searches files, and there is no file).

## Writes

- After a successful `memory_write` or `memory_edit` of a `.md` page, Evener rewrites the page's frontmatter to set `updated` and `by`, creating a frontmatter block if there is none. Every other byte of the page is preserved. A failed write is not stamped.
- If the written page has no `description`, the tool result appends one line: `This page has no description in its frontmatter, so its index line falls back to its first heading. Add description: <one line> to the frontmatter.`
- `memory_write`, `memory_edit` and `memory_delete` of `MEMORY.md` at the scope root are refused: `MEMORY.md is generated from each page's frontmatter; edit a page's description or tags instead.`
- `memory_delete` of a page needs no index repair: its line disappears from the next rendering.

## Per-turn updates and own writes

The existing machinery (`memoryIndexBaseline`, `memoryIndexLineChanges`, the 2 KiB delta cap, first-model-call-of-turn refresh) keeps working on the rendered index instead of file bytes. The diff compares page lines only; the `Tags:` header and the "Not shown" line are excluded from the diff. So:
- a page another session added shows as `+ line`;
- a page another session deleted shows as `- line`;
- a description, tag or stamp change shows as `- old` and `+ new`.

A session's own `memory_write`, `memory_edit` or `memory_delete` of any page reads that page back and patches the session's baseline: the page's old line is replaced by its new one, or dropped for a delete, so its own change is never reported back to it. Only that line changes, so pages other sessions changed since the last boundary still reach the next one as changes.

Rendering reads every page in the scope. That cost replaces today's single-file read on the same paths (session start, resume, compaction, first model call of a turn) and runs under the same off-loop read and 250 ms wait. An own write reads back only the page it wrote.

## Migration

The first time a session with `memory_write`, `memory_edit` and `memory_delete` renders a scope that still has a real `MEMORY.md` file at its root (its name matched ignoring case, since the page listing excludes that name in any case; on a case-sensitive filesystem every such file is migrated, the exact `MEMORY.md` first), it migrates. Any other session never migrates, because migration edits pages and removes the root index; it renders the pages as they are, with fallback descriptions, until a writing session migrates. Migration takes no lock:
1. Parse each line of the old index for a Markdown link to a page in the scope (`[text](path)` or a bare `path.md`). The rest of the line, with the link and leading list markers and separators (`-`, `—`, `:`) stripped, is that page's description.
2. For each linked page that exists and has no `description` in its frontmatter, write that description into its frontmatter. Pages that already have a description keep it. Migration does not stamp `updated`/`by`.
3. Rename the old file to `.MEMORY.md.pre-generated` (a dot name, so it is never a page and never searched). An earlier backup is never replaced: when the name is taken, the file goes to the first free name of `.MEMORY.md.pre-generated.2`, `.3` and so on, because an older build sharing the scope can write `MEMORY.md` again after migration.

Migration is idempotent, so it needs no lock: once the root `MEMORY.md` is gone it never runs again, and a crash mid-way leaves pages with a description and the old file still present, so the next run finishes the job. A page that fails to write does not stop the others but keeps `MEMORY.md` in place for the next run. Two sessions racing read the same old index and write the same descriptions, skip pages that already have one, and the one whose rename finds `MEMORY.md` already gone counts as done.

## Prompts, tools, skill and docs

- `agent/prompts/system.md.tmpl` memory section: pages start with frontmatter (`description`, optional `tags`, optional `evidence`); Evener builds the index from it and stamps dates; reuse a tag already listed in the index header when one fits; a tag names a topic (a subsystem, tool or area), never a state. Drop the instructions to edit `MEMORY.md` with `memory_edit`.
- Memory tool descriptions: say the index is generated and read-only.
- `internal/bundled/skills/gardening-memory/SKILL.md`: gardening is now fixing descriptions and tags, merging pages that share tags, and deleting stale pages; no index upkeep.
- `docs/product/memory.md`, `docs/tools/memory.md`: replace the hand-written index contract with this one.
- Clients (web, phone, TUI, `apptranscript`) decode the projection envelope, not the index's content; the envelope's shape is unchanged apart from the truncation sentence. Any golden fixture that embeds a projection is regenerated.

## Out of scope

- Visible retraction history (a deleted page is gone; running sessions still see its line removed).
- A memory browser UI.
- Moving misfiled personal pages into project memory.

## Evaluation

After it lands, a memory-lab round comparing main against the new build:
- existing scenarios: `feedback`, `many-facts`, `fact-changes`, `polluted-seed`, `long-project`, `recall-seeded` (seeded pages get frontmatter in the try arm's fixture only if the scenario's point is recall, otherwise they exercise migration);
- one new scenario, `index-overflow`: a seeded scope of about 120 tagged pages, whose rendered index exceeds 8 KiB, where the task needs a fact on a page that is not shown. It measures whether the agent finds it through the tag counts, and whether pages it writes reuse existing tags.

Pass rates, tag reuse, description presence and index-line quality are reported per arm.
