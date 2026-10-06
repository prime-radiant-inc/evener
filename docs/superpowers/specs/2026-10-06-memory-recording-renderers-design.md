# Memory-recording renderers — web and native

Approved by Jesse in chat on 2026-10-06 (bounded path: in-chat design approved,
this file records it and pins it against the lane's base). This spec extends
that approval with two facts discovered when the lane was rebased onto current
main (`7760e3e0c2`): memory has a third scope (`session`, #3751), and
`docs/product/memory.md` carries a now-stale sentence about generic rendering.
Amended after competing review (Astra: 5 findings; Luna: 2): descriptor
registration through the side-effect barrel, outcome-honest verbs, exact
search strings, part-within-run wording, image retention, and pinned
fallbacks and budgets.

## Problem

The five memory tools — `memory_read`, `memory_write`, `memory_edit`,
`memory_search`, `memory_delete` — are registered in no renderer table:

- `appwire-client/typescript/toolSummaries.ts`'s `TOOLS` table and
  `housekeepingSteps.ts` both lack them, so both clients' step lines read the
  fallback: "Used memory write", tray "Using memory write".
- The web's tool-renderer registry
  (`cmd/evener-hub/frontend/src/panes/session/transcript/toolRenderers.ts`) has
  no memory descriptors, so a row falls to `DEFAULT_DESCRIPTOR`: wrench icon,
  fallback summary, and a body that dumps the arguments as raw JSON — including
  the entire saved page for a write — plus raw output.
- The app's evidence (`mobile-native/src/session/evidence.ts`) routes them to
  `jsonEvidence` (pretty-printed arguments and result) for the same reason.

## Scope

Dedicated, content-forward rendering of the five memory tools on the two
AppWire clients, in the three code trees that own it:

1. **Shared words** — `appwire-client/typescript`: a new `memorySteps.ts`
   (following `housekeepingSteps.ts`'s module pattern), registered in
   `toolSummaries.ts`'s `TOOLS` table; one new `memory` member of
   `ToolFamily`.
2. **Web** — `cmd/evener-hub/frontend`: a new
   `panes/session/transcript/tools/memoryTools.tsx` (five descriptors, per the
   `fsTools`/`editTools` patterns) and one new `memory` icon kind in
   `widgets/toolicon`.
3. **App** — `mobile-native/src/session/evidence.ts`: one memory case in
   `stepEvidence`, plus the `partText` family phrase for run lines.

## Non-goals

- TUI renderers (`cmd/evener-tui/internal/msgrender/tool_renderers.go` has the
  same gap; separate follow-up).
- "Open beside" controls for memory rows: memory files live under the memory
  state root, outside session cwd, so the open-beside gate would dead-end.
- New wire fields, protocol changes, projector changes, or ActivityDetail
  changes: everything renders from `argumentsJSON` and `output` the rows
  already carry.
- Any memory UI, RPC, or automatic memory work (the product guide's promise on
  that stands).

## Behavior

### Words (shared package; both clients read them)

The `scope` argument is a required enum `personal | project | session`
(`agent/internal/tool/definitions.go`'s `MemoryDefinition`), so the words carry
`scope/path` as the target and never misread a memory path as a workspace
path. A call whose recorded arguments lack the scope (or name no file) falls
back the same way the file tools' words do: a bare-verb line, never an
exception or an empty target.

| tool | settled words (detail from output) | running (tray/progress) |
| --- | --- | --- |
| `memory_write` | "Wrote memory personal/implementation-delegation.md" | "Writing memory personal/implementation-delegation.md" |
| `memory_edit` | "Edited memory project/MEMORY.md · +1 -1" (diff stats from `old_string`/`new_string`, the `editFileWords` pattern) | "Editing memory project/MEMORY.md" |
| `memory_read` | "Read memory personal/foo.md · lines 1-40" (`readLineRange` pattern) | "Reading memory personal/foo.md" |
| `memory_search` | "Searched memory for \"pattern\" in personal · 3 hits" (the `grepWords` target/after pattern; `in <scope>` with `/<path>` appended when the `path` arg names more than `.`) | "Searching memory for \"pattern\" in personal" |
| `memory_delete` | "Removed memory personal/foo.md" | "Removing memory personal/foo.md" |

Family: all five are family `memory`. `progressFor`'s exhaustive switch gains
the case (the compiler forces it — that switch has no default). The
`session` scope words identically ("Wrote memory session/notes.md").

Fallbacks, exactly one per missing piece, all tested: a call whose recorded
arguments lack the scope degrades the target to the bare path; a write/edit/
read/delete that names no path falls to the bare-verb line ("Wrote memory",
"Edited memory", "Read memory", "Removed memory"); a search without a
pattern reads "Searched memory"; a search whose `path` is `.` names the scope
alone. Never an empty target, never an exception.

The settled words are intent-shaped exactly like the file tools' ("Wrote a.go"
for a write that can still fail); the generic failure treatment carries the
outcome. Session memory is read-only for delegates
(`agent/session_memory.go`'s `sessionMemoryReadOnly`), so a delegate's
session-scope write/edit/delete is rejected with "session memory belongs to
the root session" — that rejected row must render as a failed row with its
error on both surfaces (web failure glyph + error body; app "failed" state +
error evidence), pinned as a test on each.

### Web descriptors (`memoryTools.tsx`)

Registration is part of the unit: add `import "./memoryTools";` to
`tools/index.ts` — the side-effect barrel the production transcript imports;
a descriptor module missing from the barrel never registers, and a test that
imports the module directly would pass while production stays broken. The
descriptor test must import the barrel and assert `toolRendererFor` resolves
each memory tool to the memory descriptor.

- `memory_write`: icon `memory`, fold `consequential`, body renders
  `args.content` through the existing `Markdown` widget (the one
  `useSkillTool`'s body uses), clipped to the same 8,000-character budget the
  output bodies use (`bodies.tsx`) with the established truncation marker,
  boundary tested; if the call carries no `content`, the body falls back to
  the tool's output text.
- `memory_edit`: body `DiffBlock(editDiffText(path, old, new))` — the exact
  `EditFileBody` pattern, so `MEMORY.md` index updates render as one-line
  diffs. Fold `consequential`.
- `memory_read`: `TailFoldedOutputBody`, fold `quiet`.
- `memory_search`: `HeadClippedOutputBody`, fold `quiet`.
- `memory_delete`: small output-text body (the `WriteFileBody` pattern), fold
  `consequential`.
- New icon kind `memory` in `widgets/toolicon` (`ToolIconKind` union +
  `PATHS`); the existing `ALL_KINDS` icon test covers it automatically. A
  bookmark-style glyph, matching the set's stroke style.
- Summaries come from the shared package, so web and app never word a step
  differently.

### App evidence (`evidence.ts`)

One `memory` case in `stepEvidence` (before the `isFileTool` block, which is
`edit`-family and must not catch these):

- `memory_write` → `{ kind: "markdown", title: <scope/path>, markdown: content }`
  with images retained — markdown evidence keeps images by Jesse's #3696
  ruling for delegate replies ("the phone shows them as it does an agent's"),
  and a memory page is the same class of agent-authored markdown. The title is
  the same target the words build: `<scope>/<path>`, degrading to the bare
  path, then "Memory page" when the arguments name neither.
- `memory_edit` → `diff(editDiffText(...))` evidence.
- `memory_read`, `memory_search`, `memory_delete` → `rawOutput` (the page
  text, the hits, or the removal line).
- Malformed arguments fall back to `rawOutput`/`jsonEvidence`, never crash;
  tested for each shape: unparseable JSON, non-object JSON, mistyped fields
  (content not a string), missing scope with valid content, and empty content.

Memory steps join a completed tool run like every tool
(`projectedRows.ts`'s `clusterFamilyFor` merges completed tools under
`"tool"`); what the family buys is their own part in the run's line
(`transcriptRows.ts`'s `partOf`/`partText`). `partText` gains a `memory` case:
a part that
contains any mutation (write/edit/delete) reads "updated memory" (with the
count, the tasks-family pattern); an all-reads part reads "read memory".

### Preservation and recovery

Display-only: no daemon, wire, storage or projector change. The
`DEFAULT_DESCRIPTOR` fallback and `jsonEvidence` paths stay exactly as they
are for every unregistered tool, so an unknown tool or a future sixth memory
tool renders today's way, never blank. Recorded rows improve on deploy with
no migration: nothing about a row's rendering is persisted, so old transcripts
with memory calls immediately read the new words.

## Documentation (evergreen, same change)

`docs/product/memory.md` says "CLI transcripts and AppWire clients use the
existing dynamic-context and generic tool-result paths, not a memory UI or
RPC." Update that sentence: AppWire web and native clients render memory tool
calls with dedicated step renderers (words, saved-page body, edit diff); the
TUI still reads the generic tool-result path; still no memory UI or RPC.

## Whole-journey test plan

TDD, red first, per surface:

1. **Package** (`memorySteps.test.ts` + `toolSummaries.test.ts` additions):
   each tool's words, detail, progress and family; the three scopes; the
   missing-scope, missing-path, missing-pattern, and omitted-search-path
   fallbacks. Gate: `make test-api-package`.
2. **Web** (`memoryTools.test.tsx`, per the `fsTools`/`editTools` test
   pattern): each descriptor's summary, icon, fold class; registration
   through the barrel; write body renders the content through `Markdown`,
   clips at the budget boundary with the truncation marker, and falls back to
   output; edit body renders the diff; no `openBesidePath`; a rejected
   session-scope write renders as a failed row with its error. Gate:
   `make test-web` (+ Biome autofix on touched files from the frontend
   directory).
3. **App** (`evidence.test.ts`, `transcriptRows.test.ts`, `trayLine.test.ts`,
   `projectedRows.test.ts` additions): the evidence case per tool (images
   retained, title fallback); the run part phrase; tray progress; the
   malformed-argument matrix; a rejected session-scope write renders as a
   failed row with its error.
   Gate: `make test-native`.
4. **Journey check**: a recorded `memory_write` + `memory_edit` + `MEMORY.md`
   sequence rendered in the web transcript test fixture and the app's row
   projection shows the words agreed by the package, a diff for the edit, the
   written page for the write, and the app run line carries the memory part
   ("...and updated memory twice") inside the shared tool run.

Implementation is delegated to Luna subagents (Jesse's standing preference),
one unit at a time with independent review before acceptance; the package
lands first because both clients read it.
