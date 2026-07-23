# Web UI — UX Round 2 Design

**Date:** 2026-07-23
**Status:** approved (brainstorm), pending implementation plan
**Scope:** the round-2 UX defects Jesse reported clicking around the built SPA,
plus a new "subagent tree in the sidebar" capability. Twelve kata refs:
c8gt, 3w2p, vbh8, 4y12, yhmh, 9ct0, qb8e, yd16, tv5k, 677w, yt2q, zrzr.

This spec covers **what** each change is and the decisions made during the
visual brainstorm. Exact call sites and root-cause anchors already live on the
kata issues (each carries a `recon-done` root-cause comment); this document is
the design of record, not a re-derivation of the recon.

---

## Global constraint: the token contract governs every color here

`src/styles/tokens.css` is the single source of truth for color, and
`src/styles/token-contract.test.ts` fails CI on any hex/rgb/hsl/oklch literal
outside it, on any non-allowlisted widget reaching for the attention-family
hues, and on dark/light token drift. Every visual in this spec MUST resolve to
`var(--…)` — never a literal. The one-meaning-per-hue rule is load-bearing:

| Hue token | The one meaning it carries |
|---|---|
| `--alive` (green) | agent working / streaming / running |
| `--attention` (amber) | **a human is needed** — nothing else may be amber |
| `--danger` (red) | failure / destructive |
| `--accent` (steel-blue) | focus ring, selection, links |
| `--ink-low` (grey) | idle / ended / timestamps / placeholders |

Motion budget is equally strict: the only animations that exist are the 200ms
attention-onset color transition and the 120ms overlay fade-scale. **No idle
pulses, no shimmer, no faked liveness.** Any "running" indicator shows honest
state, not a heartbeat.

Two shipped widgets are the canonical status vocabulary and MUST be reused
rather than reinvented:

- **`StatusDot`** (`widgets/statusdot`) — a standalone 8px state dot. States:
  `working | needs-you | failed | idle | ended`.
- **`Cadence`** (`widgets/cadence`) — the signature indicator: a state dot plus
  a 64×10px SVG trace of the last ~60s of frame arrivals as vertical ticks,
  **now** at the right edge, oldest at the left, fading with age across four
  15s buckets. It is a **pure** render (no internal clock): the caller passes
  `frameTimes[]` and `now`, so a stalled agent's trace honestly ages out. Its
  own docstring intends it for "tree row, pane header, mobile card."

---

## 1. Shell frame (c8gt, 3w2p)

### 1.1 The sidebar owns all chrome — no full-width top bar

The reported "missing header" (search / new / settings) is **not** a missing
top bar. Those controls belong **inside the sidebar**, and no full-width chrome
row should eat a row of vertical space. The content area runs floor-to-ceiling.

Sidebar header zone, top to bottom:

1. **Brand** (`serf`) + a home icon, on one row.
2. **Search** — a full-width field showing the `⌘K` chord; opens the existing
   command palette. This is the home for the `[data-search-trigger]` handler
   that currently has no rendered trigger.
3. **"+ New session"** — a full-width primary button (New is a first-class
   action; it earns a real button, not an icon).

Sidebar footer zone (pinned to the bottom): **identity + settings gear.**

### 1.2 Docks on any desktop width ≥900px

Today the sidebar only auto-docks at ≥1200px, leaving a 900–1200px dead-zone
where it collapses to the ☰ overlay; a saved `rail` mode overlays at every
width. Both produce the "won't dock on a big screen" surprise.

**Decision:** drop the 1200px auto-collapse threshold entirely. Any desktop
width (≥900px, i.e. above the mobile/stack breakpoint) **docks** the sidebar
inline, like an app. The user collapses it themselves via ⌘B or the hide
control, and that choice persists. Mobile (<900px) is unchanged — StackHost
still owns that layout with the rail in its drawer.

This changes `useSidebarMode`'s resolution (the `auto` mode docks across the
whole desktop range instead of gating on 1200px). ⌘B cycling and the persisted
preference semantics otherwise stay; help copy that references the 1200px
behavior gets updated to match.

---

## 2. Sidebar tree: projects → sessions → subagents (vbh8 + new capability)

### 2.1 The hierarchy is project → session → subagent(→…)

The sidebar groups by **project** — the existing model (`TreeProject.sessions[]`
→ `TreeNode` session → `TreeNode.children[]` subagents, recursive). The
archived-projects section and favorites are retained.

**New:** a session that spawned subagents shows a twisty and expands to its
**subagent tree, nested inline**, fully recursive (subagent → sub-subagent → …)
and **independently foldable at every level**. Leaves show no twisty. Depth is
conveyed by indentation plus a hairline guide rail (`--edge`). Each subagent
node is a row with its state dot / Cadence trace, name · agent-type/status, and
clicking it opens that child's transcript.

### 2.2 Attention is inline, never a duplicate group

The **old attention grouping is removed.** Its defect was duplication: a session
that needed you appeared twice — once under "Needs you" and once in its project.

Instead, attention surfaces **within the single project hierarchy**:

- the session's own dot goes amber (`--attention`) when it needs you;
- an amber count badge rides the session row and bubbles up to its project row;
- needs-you sessions **sort to the top within their project**.

Nothing appears twice. "Needs you" is unmissable (amber is reserved for exactly
this) without a parallel list.

### 2.3 Row anatomy (the "polish")

Each **session row**: twisty (if it has subagents) · state dot/Cadence · name
(bolder when active) · a short second line (activity/status, e.g. "running ·
make test", "waiting on your input", "3 unread") · right-aligned relative
timestamp on non-attention rows, or an amber count badge when it needs you.
Active row gets an `--accent` left-bar + `--accent-bg` tint; hover gets a
`--surface-2` fill.

The second activity line is confirmed in-scope (Jesse: "it's worth it").

---

## 3. Spawn form — `/new` (4y12, yhmh, 9ct0)

Field order (top to bottom): **Harness · Model** (side by side) → **Working
directory** → **Branch · Reasoning** (side by side) → **Prompt** → **Advanced
options** (disclosure) → **Launch**.

### 3.1 Model picker → click-the-chip combobox (4y12)

Replace the read-only chip + separate "Change model" button with a **single
chip-as-button trigger**: click the model → a combobox opens. This reshapes the
shared closed-state in `widgets/modelCatalog` so **both** consumers — spawn and
Settings — inherit the fix. Mirror the in-session `ModelSwitch` trigger over the
already-shared `ModelCatalogPanel`.

### 3.2 Directory picker → one field (yhmh)

Collapse to a **single** working-directory input with a browse affordance. The
browse popover shows only **recents / `../` / subfolders** — remove the
duplicate second path input and the redundant "Use this directory" button
(Enter already commits).

### 3.3 One scroll surface; Advanced below the prompt (9ct0)

Remove the inner `max-height: 280px` scroller on the advanced-options block so
the form scrolls as **one** surface inside the pane. The Advanced options
disclosure sits **below the prompt box** (Jesse's placement call). Access mode
and schema-driven fields live inside it.

### 3.4 Global rule: popovers float, never reflow

Every combobox / picker / popover in the SPA is an **overlay** (absolutely
positioned, out of flow): opening one **must not push page content down**. This
is how the model combobox, the directory browse popover, and any listbox behave.
Same mechanism as the merged menu portal fix. This is a standing rule, not a
spawn-only detail.

---

## 4. Subagent card in the transcript (qb8e, tv5k, yd16)

One component fixes three defects: the card had no real disclosure affordance,
no inline summary, and a stale status pill.

### 4.1 Real disclosure + live status (qb8e, yd16)

- A **rotating chevron** makes the disclosure obvious (today only a faint native
  `<details>` triangle hints at it).
- The **status pill tracks the live child status** (working → done → failed),
  not just the settled tool-output value it froze at.

### 4.2 Expanded body: Mandate → Activity → Summary (tv5k)

When expanded, the card shows three layers:

1. **Mandate** — the delegation's `purpose` (why the subagent was spawned).
2. **Activity** — a **live feed** of the child's tool-call `purpose` fields, in
   order, latest highlighted while running. This reads the child's transcript
   turns via the existing watch path with `includeTurns: true` (today
   `WatchedChildIndicator` watches with `includeTurns: false`). Fed **live while
   the card is expanded.**
3. **Summary** — the child's final report.

Collapsed state stays a one-liner: title + status pill (with a step count).

### 4.3 The summary rides a new wire field

**Decision (Option A over on-demand fetch):** carry the subagent summary as a
**field on the delegate item** (Go `appwire`/`hubapi` → `types.gen.ts` →
`ItemModel`). Rationale: it's the same mechanism that already carries status, it
survives reload trivially, and threading status through the same live-updated
item **also fixes the stale-pill defect (yd16)** in one stroke. The rejected
alternative (fetch the child's last `agentMessage` on expand) needs no Go change
but adds a per-expand fetch and shows nothing after reload if the child thread
is gone.

### 4.4 "Open full transcript" works while running

The link to pop the child's full transcript is available **while the subagent is
still running**, not only when done — the opened pane watches the live child
thread and keeps updating.

---

## 5. Composer: pasted-image preview (677w)

A pasted image is already fully staged (base64 + decoded W×H); today it renders
as a text-only chip (often just "(1024×768)" since pasted images have no name).

**Decision:** render each attachment as a **thumbnail tile** — the actual image,
with dimensions overlaid and an ✕ to remove — reusing `flow/ImageGallery`.
**Clicking a thumbnail opens a full-image lightbox.** Non-image attachments fall
back to a labeled file tile.

---

## 6. Disclosure state survives remount (yt2q)

**Premise correction:** there is no transcript-width control in the SPA; `76rem`
is a hardcoded measure. What resets disclosures is a **pane-splitter or window
resize** (or a dockview layout change) that remounts transcript rows: disclosure
open/closed state is component-local (`useState` / uncontrolled `<details>`) and
dies on remount, because the `VirtualList` unmounts off-window rows and dockview
unmounts the whole pane tree on layout changes.

**Fix:** move disclosure open/closed state into a **store keyed by a stable id**
(mirror the `subagentModuleStore` per-item pattern), consumed by every
disclosure — the tool-call rows, the subagent "+N more" fold, and the native
`<details>` renderers (ThinkBlock, SteeringItem, SystemNotice). State then
survives VirtualList re-windowing and dockview remounts. (A shared Disclosure
widget, which does not exist today, is the natural home for this store-backed
state and pairs with §4.1's chevron affordance.)

---

## 7. Tool call renders twice after reload (zrzr)

**Distinct duplication path — not a regression of the merged
`b7eba6880`/`4e6936fcf` dedup**, which live in the legacy `assets/renderer.js`
and no longer participate. The live path mints one item per CallID; the **reload
path** (`apptranscript.ProjectTurn`) projects the CALL (from the assistant
entry) and the RESULT (from the tool-results entry) as **two items with
different ids but the same CallID, in separate wire turns** — and the Go side's
own comment states the contract: "the client merges the two by call id." The SPA
reducer never does: `wireItemToModel` maps every wire item 1:1, with no CallID
reconciliation anywhere. Result: two cards (and two usage/separator lines) for
one call. Not shell-specific — every non-`communicate` tool reload-doubles.

**Fix:** implement **call↔result merge-by-CallID in the SPA reducer's hydration
path** (`wireToTurnModel` / `wireItemToModel`): collapse `item_tool_*` and
`item_tool_result_*` sharing a CallID into one `ItemModel` — the call supplies
args + `startedAt`, the result supplies output + exit code + `completedAt` +
terminal status — porting the reconciliation the legacy renderer hardened.

---

## Testing posture

Per Jesse's standing steer for this phase: **UI test failures are not merge
blockers while the UI/UX is being made right** (the vitest baseline is tracked
in kata 4wgg). Compile gates still hold — `tsc`, `biome`, `build`, and, for any
change touching the Go `agent` module, `make test` + `make lint` (the root
`./...` does not cover that module under go.work). New behavior gets tests per
TDD; the token-contract test in particular must stay green (it enforces §0).

## Out of scope / deferred

- General theme audit beyond these components (kata xay0 dark-theme check
  remains its own item).
- The two P1s that were purely product/design questions are now decided here
  (c8gt, 3w2p); no separate sign-off pending.

## Decision log (from the brainstorm)

- App frame: **sidebar owns chrome, no full-width top bar** (Jesse corrected an
  initial full-width-header mock).
- Dock: **≥900px, drop 1200px threshold** (Option A over lowered-threshold B).
- Model/dir/all pickers: **float, never reflow**; Advanced **below prompt**.
- Subagent summary: **wire field** (Option A over on-demand fetch); card body
  **Mandate → live Activity → Summary**; transcript link **live while running**.
- Paste: **thumbnail tiles + lightbox** (over chip-with-swatch).
- Sidebar: **project hierarchy retained**, attention **inline (no duplicate
  group)** — the old attention grouping's duplication was the defect; deep
  foldable subagent tree added; **Cadence** kept; second activity line kept.
- All color/motion bound to the **token contract**; reuse `StatusDot`/`Cadence`.
