# Memory pages say when to recall them

Date: 2026-10-11
Status: decided; index shows `when` instead of the description
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
what the page tells you; it stays on the page and is read when the page is opened.
`when` says when a future session needs the page, written like a skill's trigger, naming the topic
and the situation. The generated index shows `when` in place of the description; a page without
one shows its description, with today's fallback chain.

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

**Index line.** `memoryIndexLine` (agent/memory_index_render.go) renders a present `when` in place
of the description:

```text
- [Shop commit prefix](git/commit-prefix.md) — Writing a commit message in this repo [git] (updated 2026-10-11)
```

A page without `when` renders exactly as today, including the "(no description)" fallback.
Index-line parsing (`memoryIndexLineFor`) matches only the line's leading link, so it is
unaffected. A changed `when` changes the line, so the index delta reports it as an added line; a
changed description on a page that has a `when` changes no line.

**Index budget.** `when` has no separate length cap, matching `description`. Showing it instead of
the description keeps the 8 KiB projection as full as today.

**Writing.** `memory_write` already takes the whole page, frontmatter included, so the tool
schema doesn't change. `repairMemoryFrontmatter` (agent/memory_migrate.go) quotes an unparseable
free-text `when` line as it does `description` and `evidence` (a `when` like "Running `make: test`"
is as easy to get wrong). The write result adds a note when a page has a description but no
`when`, alongside the existing missing-description note, asking for one line saying when a future
session needs the page.

**Prompt.** The `## Memory` section of agent/prompts/system.md.tmpl adds `when` to the frontmatter
sentence: a one-line `when`, the line the index shows for the page, naming the topic and the
situation specifically enough that a future session recognizes it should open the page, with two
short examples ("Before changing Total's signature", "When writing a commit message in this
repo"). The description sentence stays: it is what the page tells you.

**Existing pages.** Nothing migrates. Pages without `when` keep today's line. An agent rewriting a
page adds one, following the prompt; nothing backfills them.

**Clients.** The web, phone and TUI render memory-context section text as recorded and don't
parse index-line internals, so no wire or client change. `agent.MemoryScopePage`, which the memory
lab reads, gains `When`.

## Docs

Update `docs/product/memory.md` (**Page format**, the example page and rendered index, the
frontmatter repair note) in the same change.

## Testing

- Scripted-provider and unit tests: parsing (present, missing, non-string, multi-line collapsed),
  index line with and without `when`, a changed `when` producing an index delta and a changed
  description under a `when` producing none, frontmatter
  repair of a colon-bearing `when`, the missing-`when` write note.
- Goldens: the system prompt golden and `agent/testdata/memorycontextwire` regenerate with their
  update flags.

## Eval results

The memory lab (spike/memory-when) compared description-only indexes with `when` indexes over three
rounds, main agent writing memory, no librarian.

- With good descriptions, description-only and `when`-only tie (description-only passed 10/10 to
  15/15 in pilots).
- With uninformative descriptions, `when`-only won: deepseek 16/30 to 29/30, glm 2/30 to 30/30.
- A mediocre `when` that names the topic did about as well as a careful one.
- `when`-only costs 1.5 to 5 times the memory_read calls.
- Describing pages near the 8 KiB cap, showing both fields cut the visible pages from about 51 to
  about 35, so showing both was rejected.
- Ranking the index (BM25 on the human message) didn't earn its place.

The decision: show `when` instead of the description, falling back to the description.

## Slim memory_write

`memory_write` takes `scope`, `file_path`, `when` (required, non-blank) and `body`, and nothing
else. Evener writes the frontmatter block holding `when`, stamps `updated` and `by`, then writes
the body, so the model never writes frontmatter. New pages carry no `description`; pages that have
one keep it and keep rendering. The guidance for each part lives in the parameter descriptions
(the `when` description asks for the moments the page matters and one page per fact; the `body`
description asks for a first heading naming the page's topic, not its rule), and the `## Memory`
prompt section points at the fields instead of describing frontmatter. A blank `when` and a body
that opens with its own frontmatter block are refused and write nothing. `memory_edit` stays a
plain string edit.

The memory lab compared this tool with the one that takes frontmatter inside a `content` string,
over 12-rep downstream tasks on deepseek and glm: recall tied and lines came out 5% to 18%
shorter. The tool is smaller and the model has less to get wrong, so it replaced the
`content` tool outright.

Write notes follow: a page that has a `when` gets no description note, a page left without a `when`
(an edit removed it) gets the missing-`when` note, and the unreadable-frontmatter note stays. The
`(no description)` marker never appears on a page that has a `when`.

## Out of scope

- Tool-call triggers derived from `when` text (paths, commands) and keyword ranking for indexes
  over the cap. Both could build on this field later.
- The memory librarian (spike/memory-librarian), which is on hold.
