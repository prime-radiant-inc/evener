# Integration Plan — Session Rail (scrollbar) + Comprehension View → evener-hub web UI

Reference implementation: `proposals/transcript-viz/combined/index.html` (+ README) — the live-faithful
combined True-Time × Token Seismograph rail. Scout findings (2026-08-22, sourced file:line) are
quoted throughout. Target surfaces: **desktop (dockview, ≥900px)**, **tabletish (560–899px)**,
and **phones (<560px, 40px minimal strip)**.

## Owner decisions (locked 2026-08-22)

1. **Real last-activity field** on TreeNode (no `updated_at` proxy) — backend work in P0.
2. **Phones get a 40px minimal strip**, not hidden.
3. **Read-only transcript panes get the rail in v1** (they share VirtualList — cheap).
4. **Feature flag default-ON on desktop.**

## 0. What we are shipping

1. **Mode 1 — SessionRail**: a 156px canvas rail that *replaces the native scrollbar* of the
   session transcript pane. True-elapsed-time axis, per-turn token strata, cumulative burn line,
   job lanes, error ticks, prompt anchors; draggable viewport thumb, click-to-jump, anchor clicks
   to exact turns. **Live-faithful**: axis spans [session start, now] and re-scales; nothing is
   drawn before its time; readouts are "so far" only.
2. **Mode 2 — ComprehensionView**: full-screen overlay over the session pane: the same rail
   repeated for the open session's tree (parent leftmost, subagents by most-recent-activity),
   one shared live clock, aligned now-lines, click-through to any session+turn.

## 1. Key findings that shape the design

- Both transcript surfaces — live `panes/session/Session.tsx:386-482` and read-only
  `panes/transcript/Transcript.tsx:63-168` — share `VirtualList`
  (`src/widgets/virtuallist/index.tsx`). It exposes `getScrollElement()` and `getVisibleRange()`
  (index.tsx:16,28): **the exact sync seam** for thumb ↔ scroll. No custom scrollbar exists today
  (grep "minimap" = 0 hits).
- **The wire already carries almost everything**: per-turn `EvenerUsage {inputTokens, outputTokens,
  cacheReadTokens, totalTokens}` on every `Turn` (types.gen.ts:355-360, 1335-1346), turn/item
  timestamps (epoch ms), delegate lifecycle (`evener/delegate/updated`), user/steering/error items.
  Live sessions push `turn/started` / `turn/completed` (types.gen.ts:1556-1557).
- **Gaps** (must be closed): ① `EvenerJobInfo` has **no start/end timestamps** (types.gen.ts:240-260);
  ② thread/read is **windowed** (`app_threadread.go:416-422`, TurnLimit + olderCursor) — a
  full-history rail cannot come from one read; ③ `TurnModel.usage` is typed `unknown`
  (model.ts:134) though the reducer passes real `EvenerUsage` (reducer.ts:250); ④ session-cumulative
  usage is snapshot-only, no live push (model.ts:280-291) — but derivable by summing per-turn usage.
- Session tree with `children` + `updated_at` proxy for ordering: `src/stores/tree.ts:30-58`.
- Theme is **contract-enforced**: any hex/rgb literal outside `src/styles/tokens.css` fails CI
  (`token-contract.test.ts`). All rail colors must be `var(--*)` tokens.
- Breakpoint reality: `@media (max-width: 899px)` switches AppShell to StackHost
  (AppShell.tsx:56-60, AppShell.module.css:45). "Tabletish" = the 560–899px StackHost band plus
  narrow dockview splits at 900–1100px. spawnguard enforces 44px tap targets at ≤899px.
- Overlay precedent for Mode 2: `src/widgets/dialog/OverlayPanel.tsx` (scrim, aria-modal, Escape,
  FocusScope); `FlowOverlay` is the transcript-local absolute-overlay precedent.
- Guards: `make test-web` (typecheck + vitest + biome), `make test-web-browser` (layoutguard /
  overflowguard / spawnguard in real Chrome). overflowguard renders the real Session pane and
  asserts no horizontal scroll — directly relevant to a scrollbar-replacing rail.

## 2. Architecture

```
Go backend                          Frontend
─────────────────────────────────   ─────────────────────────────────────────────
appwire Turn/usage/timestamps  ──►  threadsStore (existing) ──► useRailModel(thread)  (P1)
(new) railSummary endpoint     ──►  railSummaryStore        ──► useRailModel merges     (P0)
tree.ts (children, updated_at) ──►  useSessionTreeRails()  ──► ComprehensionView       (P3)
                                    SessionRail (canvas) ◄── VirtualListHandle sync    (P1)
```

**New frontend modules** (all under `src/panes/session/rail/`):
- `railModel.ts` — pure deriver: `TurnModel[]` (+ railSummary) → `RailModel { events, jobs,
  anchors, totals, span }`. No React. Unit-testable. Live-faithful by construction: it only ever
  sees revealed data.
- `axis.ts` — pure axis math: `[start, max(now, start+10min)]` mapping, tick generation (round
  absolute hours), turn-index mode; port of the mockup's verified math.
- `SessionRail.tsx` — canvas component + DOM anchor buttons (real hit targets, as in the mockup),
  pointer handling (drag thumb / click-to-jump / anchor click), tooltips.
- `useRailScrollSync.ts` — bidirectional sync with `VirtualListHandle`; follow-live arbitration
  (manual drag/jump disables follow; transcript's existing `anchorToEnd` re-enables it).
- `ComprehensionView.tsx` — Mode 2 overlay (P3).
- `rail.module.css` — tokens only.

**Backend work** (`cmd/evener-hub`):
- `evener/thread/railSummary` RPC: given a session ref, return a compact full-history summary
  computed from the transcript JSONL: per-turn tuples `(startedAt, inTok, outTok, resultBytes,
  flags{error,userInput,steering})`, job intervals `(jobId, startedAt, finishedAt, exitCode)`,
  and totals. This closes gaps ①②④ for ended sessions in one bounded response instead of paging
  N windows of full turn text. Live sessions need no endpoint: data arrives via existing push.
- **Real last-activity field on TreeNode** (owner decision): add `last_activity_at` (epoch ms)
  to the tree response rows (`web_api_tree.go`, `stores/tree.ts:30-58`), sourced from the session
  store's last event/turn timestamp rather than the file-write `updated_at` proxy; the
  comprehension ordering consumes this field.
- Type tightening: `TurnModel.usage: unknown` → `EvenerUsage` (model.ts:134) — small, safe.

## 3. Theme mapping (token-contract compliant)

| Mockup encoding        | Token                     | Semantics match                          |
|------------------------|---------------------------|------------------------------------------|
| IN token strata (cyan) | `--accent` (blue)         | neutral information                      |
| OUT strata (amber)     | `--alive` (green)         | agent producing work                     |
| Prompt anchors (◆)     | `--attention` (orange)    | human input = human attention point      |
| Errors / result cliffs | `--danger` (red)          | failure                                  |
| Σ burn line (white)    | `--ink-hi`                | primary foreground                       |
| Idle void hatch        | `--ink-low` over `--surface-inset` | absence                          |
| Jobs ok/failed         | `--alive` / `--danger`    |                                          |
| Thumb                  | `--surface-2` + `--accent` edge |                                    |

Canvas needs resolved RGB values: read `getComputedStyle(document.documentElement).getPropertyValue`
once per theme change (observe `data-theme` attribute), cache in a `useRailTheme()` hook. Add the
hook's unit test; do NOT add new hex literals.

## 4. Phases, files, acceptance criteria

### P0 — Foundations (backend + pure frontend), no UI
- `railSummary` RPC + `rail_summary.go` (+ `_test.go` with fixture transcripts incl. a 900-turn
  session, jobs with intervals, ended + live). Bounded payload (~60B/turn → 932 turns ≈ 56KB).
- `TurnModel.usage` type tightening; fix any fallout (reducer, tests).
- `railModel.ts` + `axis.ts` with vitest suites: live-faithful invariants as property tests —
  *for any t, model(t) contains no event with timestamp > t*; axis monotonic; ordering rule
  (parent first, then updated_at desc with 60s hysteresis).
- Gates: `make test`, `make lint`, `make test-web`.

### P1 — SessionRail in both transcript panes (v1 scope, owner decision), feature-flagged
- `SessionRail.tsx` mounted beside `VirtualList` in `Session.tsx` **and in the read-only
  `panes/transcript/Transcript.tsx`** (both share VirtualList; read-only gets the same rail minus
  follow-live). Native scrollbar hidden on those containers only; rail subscribes to
  `getScrollElement()` scroll events + `getVisibleRange()`.
- Thumb drag / click-jump / anchor buttons; `useNowTick` (liveness.ts) drives the live axis.
- Feature flag: `settings.rail`, **default ON on desktop** (owner decision); off is a settings
  toggle away.
- Acceptance: drag delta ratio 1.000 (turn-index mode, jsdom test with mocked scroll element);
  anchor click scrolls to the exact turn; no horizontal overflow (overflowguard case added);
  token-contract passes; axis never shows future (unit).

### P2 — Tablet band + phone strip + polish
- Container-query driven rail width: ≥900px pane → 156px full encoding; 560–899px → 96px
  (drop axis labels to 2 ticks, anchors become ≥44px tap targets per spawnguard); **<560px →
  40px minimal strip (owner decision)**: Σ burn line + error ticks + prompt anchors only, no
  strata detail, drag-to-scroll still functional, full comprehension entry hidden (phone trees
  are read via the parent instead).
- Tooltips (hover on desktop / long-press on coarse pointer), keyboard focus order for anchors,
  `prefers-reduced-motion` (disable FLIP + follow animations), aria-label on the rail region.
- spawnguard-style tap-target case for the rail at 768px; layoutguard geometry case at
  1440/1024/768/390.

### P3 — ComprehensionView (Mode 2)
- Full-screen overlay via `OverlayPanel` pattern (Escape, FocusScope, aria-modal); entry: button
  in session pane chrome + command palette action + `⌘⇧R` (check binding conflicts).
- Hydrate parent + subagent sessions through the existing refcounted
  `threadsStore.ensureThread/releaseThread` and `openTranscript`/`stableDelegate` plumbing; ended
  sessions via `railSummary`.
- Shared live clock `[0, max(10min, now-start)]`; aligned now-lines; ordering: parent leftmost,
  then most-recent-activity (`updated_at` proxy) with 60s hysteresis + FLIP; rails at exact 156px,
  horizontal overflow scrolls (many-rail future).
- Click-through: exit overlay, `openTranscript` (or focus the live pane) at the exact session+turn.
- Acceptance: ordering unit tests; overlay a11y test (focus trap, Escape); browser-guard pass.

### P4 — Graduation
- Docs (user-facing rail legend page), settings copy.

## 5. Data mapping (mockup → wire)

| Mockup field              | Wire source                                              |
|---------------------------|----------------------------------------------------------|
| turn strata + timestamps  | `Turn.items[].type`, `Turn.startedAt/completedAt`, usage |
| Σ burn                    | Σ `Turn.usage.totalTokens` (live); railSummary totals (ended) |
| result cliffs             | byte length of settled `ItemModel.output`                |
| error ticks               | `TurnModel.error`, item `error`, `exitCode != 0`         |
| prompt anchors            | `userMessage` items + provable steering kinds            |
| job lanes                 | live: notification arrival stamps (v1); railSummary intervals (ended) |
| session ordering          | **`TreeNode.last_activity_at` (new field, P0)**          |
| in-flight tools           | `observedStartedAt` live stamps (existing pattern)       |

## 6. Test plan

- **Unit (vitest)**: railModel deriver, axis math, ordering/hysteresis, no-future-ink property,
  totals-so-far, theme hook.
- **Component (jsdom + testing-library)**: scroll sync both directions, drag ratio, anchor click →
  exact turn, follow-live arbitration, overlay focus/Escape.
- **Browser guards**: overflowguard (no h-scroll with rail at 3 widths), layoutguard (rail
  geometry + thumb position vs scrollTop), spawnguard-style tap targets at 768px.
- **Contract**: token-contract.test.ts must stay green (no literals).
- **Backend**: Go tests for railSummary (fixtures, windowing bypass, job intervals).
- **Live smoke**: run the real hub against this session's own transcript at 1440/1024/768.

## 7. Risks & mitigations

1. **History skew**: transcript is windowed but rail shows full summary → clicking an anchor for an
   unpaged turn must trigger `olderCursor` paging then scroll. Mitigation: P1 anchor handler calls
   the existing paging path first; test with a 900-turn fixture.
2. **Follow-fight**: rail jumps vs `anchorToEnd`. Mitigation: explicit follow state machine
   (manual interaction → follow off; scroll-to-bottom → follow on), reusing `useTranscriptScroll`.
3. **Canvas perf** on 1000+ turns at 60fps re-scale: single canvas, dirty-flag rendering (mockup
   pattern), event downsampling (per-pixel column aggregation) above ~2k events; measure on C
   session (932 turns) before P1 sign-off.
4. **Job lanes for ended sessions** need backend intervals — if railSummary slips, ship P1 with
   live-only job lanes and degrade honestly (no lanes on ended sessions) rather than faking.
5. **Dockview narrow splits** at desktop window widths: rail reads pane width via container query,
   not window width (a 700px dock split inside a 1600px window gets the tablet treatment).
