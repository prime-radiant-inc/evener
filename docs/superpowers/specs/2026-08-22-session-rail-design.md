# Session Rail: Live-Faithful Transcript Scrollbar + Comprehension View

**Issue:** [#338](https://github.com/prime-radiant-inc/evener/issues/338)
**Date:** 2026-08-22
**Status:** Approved

## Summary

Add a 156px vertical "Session Rail" that replaces the session transcript's
native scrollbar in the evener-hub web UI, plus a full-screen Comprehension
View that composes the same rail across a session tree (parent + subagents).
The rail encodes, at once: when things happened (true elapsed-time axis,
idle voids literal), what they cost (per-turn token strata + cumulative burn
line), and what went wrong (error ticks, failed jobs) — and because it is
the scrollbar, every glance is one drag or click away from the exact turn.

A complete, verified reference implementation exists on branch
`wip/session-rail` (`proposals/transcript-viz/combined/index.html`):
self-contained, three real sessions embedded, ported to this design.

## Live-faithful semantics (non-negotiable)

The rail shows only what a live observer at this instant could know. Zero
retrospective ink:

1. Sessions start blank; turns, anchors, gaps, and totals draw in only
   when their real timestamp arrives.
2. The true-time axis spans `[session start, max(now, start+10min)]` and
   re-scales continuously — the rail visibly compresses as a session
   outlives its history. Turn-index mode normalizes by turns-so-far.
3. No final-total denominators: "Σ 421,121 tokens so far", never
   "421,121 / 813,765". Clock shows "+5h 09m elapsed", never "of 11h 27m".
4. Idle voids hatch only after 10 real silent minutes and grow down to the
   now-line; END caps appear only when the end actually passes.

## Owner decisions (locked)

1. Real `last_activity_at` field on tree nodes (backend work), not the
   `updated_at` proxy.
2. Phones (<560px) get a 40px minimal strip (burn line + errors + anchors,
   drag still works), not hidden.
3. Read-only transcript panes get the rail in v1 (they share VirtualList —
   cheap).
4. Feature flag default-ON on desktop (`settings.rail`).

## Architecture

### Backend (`cmd/evener-hub`, Go)

- `evener/thread/railSummary` RPC: per session ref, computed from
  transcript JSONL — per-turn tuples `(startedAt, inTok, outTok,
  resultBytes, flags{error, userInput, steering})`, job intervals
  `(jobId, startedAt, finishedAt, exitCode)`, totals. ~60B/turn (the
  932-turn fixture session ≈ 56KB). One bounded response instead of
  paging N windows of full turn text.
- `last_activity_at` (epoch ms) added to tree rows (`web_api_tree.go` →
  `stores/tree.ts`), sourced from last event/turn timestamp.
- `TurnModel.usage` type tightening: `unknown` → `EvenerUsage | null`.

### Frontend (new, under `src/panes/session/rail/`)

- `railModel.ts` — pure deriver: `TurnModel[]` (+ railSummary) →
  `RailModel { events, jobs, anchors, totals, span }`. No React.
  Live-faithful by construction (only ever sees revealed data).
- `axis.ts` — pure axis math: `[start, max(now, start+10min)]` mapping,
  round-hour tick generation, turn-index mode. Port of the reference's
  verified math.
- `ordering.ts` — comprehension ordering: parent leftmost, then
  most-recent-activity with 60s hysteresis + FLIP animation.
- `SessionRail.tsx` — canvas component + DOM anchor buttons (real hit
  targets); pointer handling (drag thumb / click-to-jump / anchor click /
  anomaly hit-test); tooltips.
- `useRailScrollSync.ts` — bidirectional sync with `VirtualListHandle`;
  follow-live arbitration (manual drag/jump disables follow; scroll-to-
  bottom re-enables), reusing `useTranscriptScroll`/`scrollMetrics`.
- `useRailTheme.ts` — reads `getComputedStyle` for resolved RGB token
  values, caches per theme change (observes `data-theme` attribute). No
  hex literals.
- `ComprehensionView.tsx` — Mode 2 overlay following
  `widgets/dialog/OverlayPanel.tsx` (scrim, aria-modal, Escape,
  FocusScope).
- `rail.module.css` — tokens only.

## Theme mapping (token-contract compliant)

| Encoding                 | Token                     | Semantics            |
|--------------------------|---------------------------|----------------------|
| IN token strata          | `--accent` (blue)         | neutral information  |
| OUT strata               | `--alive` (green)         | agent producing work |
| Prompt anchors (◆)       | `--attention` (orange)    | human input          |
| Errors / result cliffs   | `--danger` (red)          | failure              |
| Σ burn line              | `--ink-hi`                | primary foreground   |
| Idle void hatch          | `--ink-low` over `--surface-inset` | absence       |
| Jobs ok/failed           | `--alive` / `--danger`    |                      |
| Thumb                    | `--surface-2` + `--accent` edge |                |

Canvas resolves tokens via `getComputedStyle` in `useRailTheme()`. No new
hex literals.

## Responsive bands

Container-query driven rail width (not window media queries — a narrow
dockview split on a wide monitor gets the tablet treatment):

- ≥900px pane → 156px full encoding
- 560–899px → 96px (2 axis ticks, ≥44px tap targets)
- <560px → 40px minimal strip (Σ burn + errors + anchors, drag works,
  comprehension entry hidden)

## Phases

### P0 — Foundations (backend + pure frontend), no UI

- `railSummary` RPC + Go tests (fixtures: 932-turn session, job
  intervals, ended + live). Bounded payload (~60B/turn).
- `last_activity_at` on tree rows.
- `TurnModel.usage` type tightening; fix fallout.
- `railModel.ts` + `axis.ts` + `ordering.ts` with vitest suites:
  live-faithful invariants as property tests — *for any t, model(t)
  contains no event with timestamp > t*; axis monotonic; ordering rule
  (parent first, then `last_activity_at` desc with 60s hysteresis).
- Gates: `make test`, `make lint`, `make test-web`.

### P1 — SessionRail in both transcript panes, flag default-on desktop

- Mount `SessionRail.tsx` beside `VirtualList` in `Session.tsx` and in
  read-only `panes/transcript/Transcript.tsx`. Hide native scrollbar on
  those containers; rail subscribes to `getScrollElement()` scroll events
  + `getVisibleRange()`.
- Thumb drag / click-jump / anchor buttons; `useNowTick` (liveness.ts)
  drives the live axis.
- Feature flag: `settings.rail`, default ON on desktop.
- Acceptance: drag delta ratio 1.000 (turn-index mode, jsdom test with
  mocked scroll element); anchor click scrolls to exact turn (including
  paging via `olderCursor`); no horizontal overflow (overflowguard);
  token-contract passes; axis never shows future.

### P2 — Responsive bands + a11y

- Container-query width: ≥900px → 156px; 560–899px → 96px; <560px → 40px
  minimal strip.
- Tooltips (hover desktop / long-press coarse pointer), keyboard focus
  order for anchors, `prefers-reduced-motion` (disable FLIP + follow
  animations), aria-labels.
- spawnguard-style tap-target case at 768px; layoutguard geometry case at
  1440/1024/768/390.

### P3 — ComprehensionView (Mode 2)

- Full-screen overlay via `OverlayPanel` pattern (Escape, FocusScope,
  aria-modal); entry: button in session pane chrome + command palette
  action + `⌘⇧R`.
- Hydrate parent + subagent sessions through refcounted
  `threadsStore.ensureThread`/`releaseThread`; ended sessions via
  `railSummary`.
- Shared live clock `[0, max(10min, now-start)]`; aligned now-lines;
  ordering: parent leftmost, then most-recent-activity with 60s
  hysteresis + FLIP; rails at exact 156px, horizontal overflow scrolls.
- Click-through: exit overlay, `openTranscript` at exact session+turn.
- Acceptance: ordering unit tests; overlay a11y test; browser guards.

### P4 — Graduation

- Docs (user-facing rail legend), settings copy.

## Data mapping (reference → wire)

| Reference field       | Wire source                                              |
|-----------------------|----------------------------------------------------------|
| turn strata + timestamps | `Turn.items[].type`, `Turn.startedAt/completedAt`, usage |
| Σ burn                | Σ `Turn.usage.totalTokens` (live); railSummary (ended)    |
| result cliffs         | byte length of settled `ItemModel.output`                |
| error ticks           | `TurnModel.error`, item `error`, `exitCode != 0`         |
| prompt anchors        | `userMessage` items + provable steering kinds            |
| job lanes             | live: notification arrival stamps; railSummary (ended)   |
| session ordering      | `TreeNode.last_activity_at` (new field)                   |
| in-flight tools       | `observedStartedAt` live stamps (existing pattern)       |

## Risks & mitigations

1. **History skew:** rail shows full summary while transcript pages →
   anchor clicks page `olderCursor` first, then scroll. Test with
   900-turn fixture.
2. **Follow-fight:** rail jumps vs `anchorToEnd` → explicit follow state
   machine (manual → follow off; scroll-to-bottom → follow on).
3. **Canvas perf** at 900+ turns with continuous re-scale: single canvas,
   dirty-flag rendering, per-pixel column aggregation above ~2k events;
   measure on the 932-turn session before P1 sign-off.
4. **Ended-session job lanes** depend on railSummary: if it slips, ship
   P1 with live-only lanes and degrade honestly.
5. **Reasoning items have no wire timestamps:** they don't get strata;
   tool-call items carry timestamps and are sufficient for rhythm.
