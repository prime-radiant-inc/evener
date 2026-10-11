# Compact-context request renderer (web) — design

Date: 2026-10-08. Lane: `compact-context-renderer` (worktree), base
`ad265d610f`. Source: Jesse's request, 2026-10-08, with a screenshot of a
`compact_context` call rendering as a raw JSON dump.

## Problem

A `compact_context` tool call has no dedicated web renderer. It renders
through the default descriptor: the row summary is already good — the shared
client's housekeeping words ("Asked for a context compaction", "Cleared its
compaction note"), the same words the native tray and run lines use — but the
expanded body is the generic fallback: a raw pretty-printed JSON code block of
`argumentsJSON` plus raw output. The screenshot shows the result: quoted
field names (`"compaction_instructions"`, `"intent"`, `"note_to_self"`), long
quoted strings, and markdown checklists inside the note garbled into the raw
text.

## Scope

One new tool-renderer descriptor in the web frontend:

- New file
  `cmd/evener-hub/frontend/src/panes/session/transcript/tools/compactContext.tsx`,
  registered from `tools/index.ts` (the side-effect barrel).
- `match: "compact_context"`.
- **Summary unchanged**: `toolStepSummary(item, ctx)` — exactly what the
  default descriptor already produces, so web, native and run lines keep
  saying the same words (ground truth `appwire-client/typescript/
  housekeepingSteps.ts`, driven by `agent/session_tools_compact.go`).
- **Icon**: new `ToolIconKind "fold"` — two chevrons converging on a center
  point (a "compress/fold in" glyph), drawn on the 16x16 stroke-only grid the
  `widgets/toolicon` grammar requires. The widget's derived test enumerates
  kinds from `PATHS`, so union and record stay in lockstep. DECIDED (Jesse,
  2026-10-08): the fold glyph over keeping the default wrench, so the
  collapsed row's glyph visibly changes from wrench to fold.
- **fold**: unset — the row keeps breaking tool runs exactly as today. A
  compaction request is a high-signal event; we are not changing run
  grouping.
- **Body** pretty-prints the call's own fields, in this order, each an
  optional labeled block (label: sentence-case caption-size sans in
  `--ink-mid`, the meta-table label idiom — NOT an eyebrow; "Note to self" is
  three words and eyebrows cap at two):
  1. `note_to_self` — label "Note to self". Content renders through the
     shared `Markdown` widget (the note is authored prose carrying IDs, paths
     and decisions — the same treatment `memory_write` content gets,
     `tools/memoryTools.tsx`), clipped at the same 8000-char budget. An
     empty-string note is the tool's clearing request and renders nothing; a
     whitespace-only note is a real pinned note (`session_tools_compact.go`
     pins any non-empty string) and renders like any other.
  2. `compaction_instructions` — label "Compaction instructions", same
     Markdown treatment and clip.
  3. `reload_skills` — label "Skills to reload"; names in the mono face as a
     comma-separated line. An explicit empty array renders "None." (the
     tool's semantics: `[]` reloads none, `agent/session_tools_compact.go`);
     an absent/null selection renders no block at all.
  4. `intent` — the shared work-tool intent parameter, which the registry
     injects into every registered tool (`agent/internal/tool/registry.go`'s
     `WithIntentParameter`; compact_context does not opt out) and advertises
     as required (`agent/session_tools.go`'s wireToolDef), so it is the norm
     in `argumentsJSON`, not a per-session variant. The session promotes it
     into the tool-call event's `Description` field at emit
     (`agent/session_tools.go`'s `toolStartDescription`) — the projector does
     not mirror it — and the row renders that field as its stated intent
     line. The body therefore skips it whenever it matches the row's stated
     intent (the common case, see the screenshot); it renders labeled
     "Intent" only when it differs or the row shows no stated intent. Never
     duplicated, never dropped.
  5. Any other argument keys (the schema is closed, but recorded transcripts
     can carry anything): rendered in one small mono `CodeBlock` of the
     leftover JSON, so no registered-renderer body is ever less honest than
     the generic fallback. Known keys whose values are not of the expected
     type — a non-string `note_to_self`/`compaction_instructions`, a
     `reload_skills` that is not an array of strings — land in this block
     too, so a type mismatch is never silently dropped. A null value is the
     documented no-selection only for `reload_skills` (rule 3); any other
     null-valued key lands in this block as well, because the default
     renderer's raw dump shows it. Arguments that decode to no fields at
     all — malformed JSON, or well-formed but not an object, which parseArgs
     degrades to an empty map — render their raw text instead, so the row
     keeps the evidence the generic renderer shows. Absent leftovers render
     nothing.
  6. `item.output` — the tool's own sentence ("Note pinned. A compaction
     will run at the seam, …" / "Note cleared. No compaction requested.") as
     plain body text, like `write_file`'s confirmation body
     (`tools/bodies.tsx`'s `WriteFileBody`).
- Styling: a new `compactcontext.module.css` following the ask-user card's
  token usage (`var(--space-*)`, `var(--ink-*)`, `var(--font-size-caption)`);
  no boxes-in-boxes (design system §1: whitespace and fine rules, not nested
  enclosures — labels + content, like `memory_write`'s body).

## Non-goals

- No shared-client (`appwire-client/typescript`) changes: the wording
  already lives in `housekeepingSteps.ts` and both clients consume it.
- No native app changes: native already summarizes the call with the shared
  words; the phone shows step lines, and its rows are deliberately compact.
- No rendering of the compaction *result* — the summary/checkpoint scaffolding
  disclosure (`SystemNoticeItem`) already owns that surface.
- No run-grouping/fold change, no disclosure-default change (rows start
  collapsed as every tool row does).
- No descriptors for the other housekeeping tools (`notes_agent_set`,
  `notes_read`, `urls_add`, `urls_remove`, `update_goal`, `communicate`,
  `model_list`, `doctor_evener` — the rest of the shared
  `housekeepingSteps.ts` map) — this change is the compaction request Jesse
  asked for; those stay on the default renderer.

## Behavior, recovery and preservation

Read-only rendering; no mutation, no recovery surface, no user work at risk.
Rendering works from `argumentsJSON`, which the wire carries from call start,
so a live in-progress call renders the same body (fields, no output yet) and
settles by adding the output line. A row whose body would render nothing —
the live note-clearing call before its output exists — offers no disclosure
at all (the descriptor's `hasBody`, the registry's contract for a body that
would open to nothing). Malformed `argumentsJSON` parses to an
empty args map (`parseArgs`), so the body degrades to labels-for-what-parses
plus output; failures keep the generic failed-row treatment `ToolCallItem`
already owns (error text above the body, `data-attention`).

## Whole-journey test plan

`tools/compactContext.test.tsx`, following the `useSkillTool.test.tsx` /
`memoryTools.test.tsx` conventions (descriptor lookup via `toolRendererFor`,
inline-built `ItemModel`s, plus the wire fixture for the note-clearing call):

1. Summary: a compaction-requesting call reads "Asked for a context
   compaction"; the note-clearing call (`toolWireStep("call_compact_context")`)
   reads "Cleared its compaction note" — byte-parity with the shared words.
2. Body renders note and instructions as markdown (a note containing a
   heading renders as a real heading, not literal `#`), with their labels.
3. `reload_skills` renders the names; `[]` renders "None."; absent renders no
   "Skills to reload" label.
4. An `intent` argument equal to the item description renders nowhere in the
   body; with no description it renders under an "Intent" label.
5. An unexpected extra argument — or a known key with a wrong-typed value
   (a non-string note, a non-array reload_skills) — renders in the leftovers
   block, not dropped.
6. The output sentence renders as plain text; a live call renders the body
   without it.
7. The note-clearing fixture call's body shows the cleared-note output and no
   note block.
8. One `ToolCallItem`-level integration case renders a compact_context item
   through the real row (summary line, expandable body, label content) so the
   journey is proven through the shared row grammar, not just the descriptor.
9. The descriptor pins `icon: "fold"` and an unset `fold` (the row keeps
   breaking tool runs exactly as today), following the memoryTools.test.tsx
   convention of asserting descriptor.icon per tool.

Gates: Biome on touched files, `npm run typecheck`, targeted vitest on the
transcript tools directory, then `make test-web` from the repo root as the
canonical local gate before the push. No Go changes, so `make vet`/Go tests
are untouched. CI owns the full suite.

## Documentation

- This design doc is the spec of record (committed here, per the
  memory-renderers precedent `2026-10-06-memory-recording-renderers-design.md`).
- No evergreen product doc describes the compact_context row rendering today,
  so none needs amending: the design system's "Inline tools and delegates"
  contract is followed, not changed; the subsystem map's S02/S10 rows do not
  move.
- Adjacent pre-existing gap (flagged, not fixed here): the `compact_context`
  parameter table in `docs/design/context.md` omits `reload_skills`. Will be
  raised with Jesse and filed as a GitHub issue in the owning repo rather
  than folded into this change.

## Acceptance criteria

- A `compact_context` call with note + instructions + reload_skills renders
  as labeled, readable blocks in its expanded row; no raw JSON dump for
  known fields; nothing silently dropped (unknown or wrong-typed args
  surface in the leftovers block).
- The cleared-note call stays a one-line row whose body shows the tool's
  own sentence.
- The row's glyph changes from the generic wrench to the new fold icon;
  summary wording is unchanged from today (shared words), native parity
  preserved.
- All existing tests still pass; `make test-web` green.
