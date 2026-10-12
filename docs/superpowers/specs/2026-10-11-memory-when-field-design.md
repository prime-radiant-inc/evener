# Memory pages say when to recall them

Date: 2026-10-11
Status: proposed
Builds on: `docs/superpowers/specs/2026-10-08-generated-memory-index-design.md`

## Problem

A memory page's index line tells the agent what the page says. The agent has to work out on its
own that a page bears on what it is about to do, often many tool calls after it read the index.
The pages worth keeping are the ones that prevent a mistake that looks right locally, and that
mistake happens at a particular moment: writing a commit message, changing a signature,
formatting a price. Nothing in the index names that moment.

Skills solve the same problem with a description written as "use when …": the model reads a
short list and matches it against what it is doing. Memory pages should carry the same kind of
line.

## Decision

Pages get an optional `when` frontmatter field next to `description`. `description` keeps saying
what the page tells you (it often carries the fact itself, so the index delivers it with no read).
`when` says when a future session needs the page, written like a skill's trigger. The generated
index shows both.

```markdown
---
description: Commit messages in this repo start with the "shop:" prefix
when: Writing a commit message in this repo
tags: [git]
---
```

Recall stays model-driven: the agent reads `when` in the index, as it reads skill descriptions.
No tool-call pattern matching, no keyword ranking and no librarian are part of this design.

## Behavior

**Parsing.** `parseMemoryPage` (agent/memory_page.go) reads `when` like `description`: a YAML
string, whitespace runs collapsed to one space; any other type or an empty value counts as
missing. `memoryPage` gains `When string`. No fallback text when it is missing.

**Index line.** `memoryIndexLine` (agent/memory_index_render.go) renders a present `when` after
the description:

```text
- [Shop commit prefix](git/commit-prefix.md) — Commit messages in this repo start with the "shop:" prefix (when: writing a commit message in this repo) [git] (updated 2026-10-11)
```

A page without `when` renders exactly as today. The exact wording around the field (`(when: …)`)
is settled by the eval below; whatever it is, it is one fixed form. Index-line parsing
(`memoryIndexLineFor`) matches only the line's leading link, so it is unaffected. A changed `when`
changes the line, so the index delta reports it as an added line, like a changed description.

**Index budget.** `when` has no separate length cap, matching `description`. Lines get longer, so
the 8 KiB projection holds fewer pages; the eval reports bytes per page so we can see the cost.

**Writing.** `memory_write` already takes the whole page, frontmatter included, so the tool
schema doesn't change. `repairMemoryFrontmatter` (agent/memory_migrate.go) quotes an unparseable
free-text `when` line as it does `description` and `evidence` (a `when` like "Running `make: test`"
is as easy to get wrong). The write result adds a note when a page has a description but no
`when`, alongside the existing missing-description note, asking for one line saying when a future
session needs the page.

**Prompt.** The `## Memory` section of agent/prompts/system.md.tmpl adds `when` to the frontmatter
sentence: a one-line `when` saying when a future session will need the page, written the way a
skill says when to use it, with two short examples ("Before changing Total's signature", "When
writing a commit message in this repo"). The wording is tuned in the eval.

**Existing pages.** Nothing migrates. Pages without `when` keep today's line. An agent rewriting a
page adds one, following the prompt; nothing backfills them.

**Clients.** The web, phone and TUI render memory-context section text as recorded and don't
parse index-line internals, so no wire or client change.

## Docs

Update `docs/product/memory.md` (**Page format**, the example page and rendered index, the
frontmatter repair note) in the same change.

## Testing

- Scripted-provider and unit tests: parsing (present, missing, non-string, multi-line collapsed),
  index line with and without `when`, a changed `when` producing an index delta, frontmatter
  repair of a colon-bearing `when`, the missing-`when` write note.
- Goldens: the system prompt golden and `agent/testdata/memorycontextwire` regenerate with their
  update flags.

## Eval (running in parallel on spike/memory-when)

The decision to ship rests on the memory lab, comparing today's index (description only) with
description plus `when`, main agent writing memory, no librarian:

1. **Authoring:** on lunarouter deepseek-4.1-flash and glm-5.3-flash, how often pages get a
   `when`, and whether each names a real future situation, restates the fact, or is vague.
2. **Recall:** the same hand-written pages in stores where recall is hard (the facts among ~110
   distractor pages so the index sits at its cap; and a long task where a fact matters only near
   the end). Scored on the produced code and commits, plus memory_read/memory_search call counts.

Ship if recall improves and agents write useful `when` lines. If recall doesn't move, drop the
field. If recall improves but authored lines are poor, ship with prompt changes and re-run
authoring.

## Out of scope

- Tool-call triggers derived from `when` text (paths, commands) and keyword ranking for indexes
  over the cap. Both could build on this field later.
- The memory librarian (spike/memory-librarian), which is on hold.
