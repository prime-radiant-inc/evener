# Activity View Redesign — Dense Hierarchical Tree

Date: 2026-08-05
Status: Approved design (brainstormed with visual mockups; direction and interaction model chosen by user)

## Overview

Replace the card-based master-detail Activity view (`Activity · <session>` tab in
the serf-hub web UI) with a dense, hierarchical tree of everything a session has
spawned: subagents (delegates) and background shell jobs. One line per job or
agent showing status, tokens in/out, and — for live rows — time since last
output. Terminal items fold away behind a single "N inactive · M failed" row,
mirroring the left rail's "Inactive subagents (N)" pattern. Clicking a row opens
that job or agent's transcript in a new tab in the pane manager.

The tree component is built width-adaptive so the same markup can later be
pinned into the left sidebar; that reuse is a non-goal for this spec but
constrains layout decisions (no wide tabular columns, no hover-only affordances
that require full pane width).

## Goals

- One line per job/agent: status dot, kind glyph, name, right-aligned mono meta.
- Hierarchical: nested delegate subtrees render indented under their parents
  with guide lines.
- All terminal items (completed, failed, cancelled, stopped) fold into a single
  "N inactive · M failed" row per parent; failed count shown in danger color.
- Live rows show quiet time (time since last observed output); delegate rows
  show cumulative tokens in/out.
- Click row → open in new tab (existing `openTranscript` action).
- Chevron → expand inline detail strip without leaving the tree.

## Non-goals

- Pinning the tree into the left rail (later; the component must not preclude it).
- Changing what the pane manager does with opened transcripts.
- Reworking the left rail itself.
- Per-job cost accounting or historical analytics.

## Current state

- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx` — tab
  header with aggregate counts.
- `…/chrome/ActivityTree.tsx` — current tree of card rows.
- `…/chrome/ActivityInspector.tsx` — right-hand "Select activity" detail pane
  (removed by this redesign; inline detail replaces it).
- `…/chrome/activityData.ts` — builds the tree model from wire data.
- `src/stores/activityPanel.ts`, `src/stores/activitySummary.ts` — fetch and
  hold the `JobActivityTree` (revisioned).
- Wire types: `src/protocol/types.gen.ts` (`JobActivityTree`,
  `JobActivitySession`, `JobActivityEntry`, `JobActivityJob`,
  `JobActivityDelegate`, `JobActivityCounts`), generated from
  `appwire/types.go`; the tree is assembled in `agent/jobs_activity.go`.
- `openTranscript(ref, parentRef)` in
  `src/panes/session/transcript/openTranscript.ts` already opens a job/agent
  transcript in a new tab; `ActivityTree` already calls it.
- Left-rail fold reference: `src/shell/rail/railNodes.ts` (`InactiveFoldRailNode`)
  and `RailRow.tsx`.
- Design tokens: `src/styles/tokens.css` (Ledger light / Fjord dark; 4px grid;
  IBM Plex Sans for UI, IBM Plex Mono for numbers/timings).

### Data gap (must be closed by this work)

`JobActivityJob` today carries `jobId`, `type`, `status`, `outcome`, `terminal`,
`background`, `hasOutput`, `description`, `command`, `task`, `startedAt`,
`endedAt`, `exitCode`, `outputBytes` — but **no token counts and no last-output
timestamp**. Session-cumulative usage already exists daemon-side (`SerfUsage` in
`appwire/types.go`: `inputTokens`, `outputTokens`, …), and the jobstore records
output writes (`agent/internal/jobstore`), so both fields are obtainable without
new instrumentation plumbing.

## Design

### Row anatomy

```
▸  ●  ⌘  Align Inactive Subagents Chevrons          41.2k↑ 6.1k↓ · 12s
```

- **Chevron (▸/▾)** — toggles the inline detail strip; separate hit target from
  the row body. Rendered on every job/agent row. Fold rows have their own
  chevron, which toggles the fold rather than a detail strip.
- **Status dot** — 7px, colored strictly by the token contract:
  `--alive` (running/working), `--danger` (failed), `--attention` (blocked /
  needs-human only — amber has exactly one meaning in this app), neutral grey
  (`color-mix` of `--ink-low`) for completed/cancelled/stopped.
- **Kind glyph** — mono, `--ink-low`: `⌘` for delegates/agents, `$` for shell
  jobs.
- **Name** — `--font-size-ui`, medium weight when live, regular when terminal;
  single line, ellipsis overflow.
- **Meta** (right-aligned, `--font-mono`, `--font-size-caption`, `--ink-low`):
  - Live delegate: `<in>↑ <out>↓ · <quiet>` (e.g. `41.2k↑ 6.1k↓ · 12s`).
  - Live shell job: `— · <quiet>` (no tokens exist for shell jobs; render the
    dash rather than inventing a zero).
  - Terminal row: `<in>↑ <out>↓ · <duration>` for delegates, `<duration>` or
    `failed` (in `--danger`) for shell jobs. Age-style stamps (`13h`) remain the
    format for long-ended rows, matching the rail.
- Token formatting: compact SI (`900`, `4.2k`, `128k`), reusing whatever compact
  formatter the status row already uses for `SerfUsage` (extract a shared helper
  if none is exported).

### Hierarchy and folding

- The component consumes `JobActivityTree` directly: a session's `entries`
  render in order; a delegate entry with a `child` session renders that child's
  entries indented one level (12px per level) with a 1px `--edge` guide line on
  the left, exactly like the rail's project/session nesting.
- Per parent (root session or delegate child session), terminal entries fold
  into one row: `▸ N inactive` plus `· M failed` in `--danger` when M > 0.
  Default state is folded. Expanding reveals the terminal rows in their original
  order.
- Live entries are never folded.
- Fold state lives in the activity panel store keyed by `<sessionRef>:<path>`;
  in-memory only (no persistence) for this iteration.

### Interaction

- **Click row body** → `openTranscript(ref, parentRef)` opens the job/agent in a
  new tab (delegate rows pass `childRef`; job rows pass the job transcript ref
  `job:<jobId>`). This matches today's `ActivityTree` behavior.
- **Click chevron** → toggles an inline detail strip below the row (does not
  propagate to the row click). Strip contents: full command (shell) or mandate
  snippet (delegate) in mono, runtime or duration, `outputBytes`, started-at
  time, and a "Open in tab ↗" button for parity.
- **Hover** → row background `--surface-2` only.
- **Keyboard** — rows are focusable (`tabIndex=0`, `role="treeitem"` inside
  `role="tree"`): Enter/Space opens the tab, → expands detail, ← collapses,
  ↑/↓ move focus. The fold row participates as a treeitem whose expand/collapse
  toggles the fold.
- Only one detail strip open at a time per tree (opening one closes another);
  this keeps the tree from jumping in several places at once.

### Live updates

- The tree already refreshes through `activitySummary`'s revisioned fetch; new
  fields ride along with no new subscription channel.
- Quiet time is derived client-side: `now − lastOutputAt`. A 1s interval
  re-renders the tree while any live row is visible; browser timer throttling
  covers hidden tabs.
- While a job has produced no output yet (`hasOutput === false`), quiet time is
  measured from `startedAt`.

### Backend / wire changes

1. `appwire/types.go` — extend the activity job struct with:
   - `LastOutputAt string` (RFC3339, omitempty) — last time the jobstore
     observed output for the job.
   - `Usage *SerfUsage` (omitempty) — present on delegate entries only, sourced
     from the child session's cumulative usage; absent for shell jobs.
2. `agent/jobs_activity.go` — populate both: `LastOutputAt` from the jobstore
   record/output tracking for that job; `Usage` from the child session's
   existing usage accounting (the same source that feeds the status row's
   `SerfUsage`).
3. Regenerate `src/protocol/types.gen.ts` (existing appwirets codegen) and
   consume the fields in `activityData.ts`.
4. Old daemons omit both fields; the UI must degrade gracefully — no tokens
   column content, quiet time falls back to `startedAt`.

### Component structure

- Rewrite `ActivityTree.tsx` into the dense tree (keep the filename; the card
  rendering is replaced, not wrapped). New small components in the same
  directory: `ActivityTreeRow.tsx` (row + meta), `ActivityTreeDetail.tsx`
  (inline strip), `ActivityFoldRow.tsx` (the "N inactive" row).
- `activityData.ts` gains a pure function `buildActivityRows(tree, foldState)`
  returning a flat list of render-ready row models (visible rows only, fold
  rows synthesized) — fully unit-testable without rendering.
- `ActivityPanel.tsx` keeps the header/counts and drops the inspector split;
  `ActivityInspector.tsx` is deleted along with its tests, its useful detail
  fields moving into `ActivityTreeDetail.tsx`.

### Error and edge cases

- **Fetch states** (`activitySummary`'s `unsupported` / `ended` / `failed`)
  keep their current panel-level rendering; the tree only renders for `ready`.
- **Deep nesting** — nesting is unbounded; rows indent one level per nesting
  depth and containment is guarded by the `activity-tree-responsive`
  layoutguard case.
- **Long names/commands** — ellipsis everywhere; full text available in the
  detail strip and via `title` tooltips.
- **Old daemon** — missing `usage`/`lastOutputAt` degrades as described above;
  no broken glyphs.
- **Fold-all-terminal with zero terminal entries** — no fold row is rendered.

## Testing

Per `docs/testing.md`: deterministic, no provider credentials or network.

- Go (`agent/jobs_activity_test.go`): activity tree includes `lastOutputAt`
  when output exists, falls back to `startedAt` otherwise; delegate entries
  carry child-session usage; shell jobs omit usage.
- Frontend unit (`activityData.test.ts`, `ActivityTree.test.tsx`):
  - `buildActivityRows` folding: terminal items collapse into a fold row with
    correct counts; live items never fold; nested delegate children indent.
  - Row meta formatting: tokens ↑↓, quiet time for live, duration/outcome for
    terminal, `failed` in danger color.
  - Click row → `openTranscript` called with the right refs; chevron click
    toggles detail and does not call `openTranscript`; fold row toggles fold
    state in the store.
  - Missing usage/lastOutputAt (old-daemon shape) renders without tokens and
    with startedAt-based quiet time.
- Gates: `npx biome check --write` on touched files, then `make test-web`; on
  this Chrome-capable host also `make test-web-browser`. Token-contract test
  (`token-contract.test.ts`) must pass — no new hex literals outside
  `tokens.css`.

## Rollout

Single change: backend wire fields land first (additive, omitempty — safe
against older web builds), frontend tree rewrite in the same PR. No feature
flag; the old inspector UI is fully replaced.

## Mockups

Visual explorations (row layout A/B/C, interaction model 1/2) are preserved in
`.superpowers/brainstorm/83675-1785970344/content/` (gitignored). Decisions
taken from them: A-style sidebar rows (over tabular columns), inline detail via
chevron (over click-expands), click row opens tab.
