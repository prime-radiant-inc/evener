# Serf Web Hub — Foundation + Experience Pass

Status: approved (2026-07-18), revised after two adversarial-review rounds (v3).
Work branch: `webui-joy`.
Scope: `cmd/serf-hub` frontend (`assets/`, `templates/`, and `web.go` home rendering).

v2 resolved 26 round-1 findings (reviewers 1–2, 13 each, tie). v3 resolves 26
round-2 findings (reviewer 3: 14 — round winner; reviewer 4: 12). Round-2 reports
in session transcripts `local:033rn6RYwumQSzT0WOLYT8` and
`local:033rn6SS7dzJfyGLde5AIz`. Substantive v3 changes are marked **[v3]**;
round-1 resolutions retained from v2 are unmarked.

## Background

The owner reports the hub feels "clunky, unbalanced, and not very responsive" — both
interaction latency/jank and layout adaptiveness. Two read-only audits (2026-07-18)
produced the findings below; line numbers refer to `cmd/serf-hub/assets/` on `main`
at `be125a62`.

### Jank findings (performance audit)

1. **Per-token full markdown re-parse** (`renderer.js:2212-2222`): O(L²) per
   message, destroys selection, full re-layout per token.
2. **Per-event layout thrash** (`renderer.js:1062, 1402-1403`); hydration replay
   (`:823-826`) makes session open O(N²).
3. **Unbounded transcript DOM** — no windowing anywhere.
4. **Tool-output DOM rebuild per delta** (`renderer-tools.js:135-172`, called at
   `:528-531, 225-227`).
5. **Unthrottled scroll handler** with O(transcript) queries + layout reads
   (`renderer.js:4510-4521, 4470-4477`).
6. Medium: diff rebuild per delta (`renderer-tools.js:26-43, 580, 614`); reasoning
   full-buffer replace (`renderer.js:2328-2338`) **and an O(n²) preview-tail regex
   per delta (`:2337`) [v3]**; per-event `JSON.stringify`→`parse` round trip
   (`:1047-1049, 1068`).

### Imbalance findings (layout audit)

1. **No wide-viewport strategy** (`style.css:500, 511-516`): 32% width usage at
   2560px; shipped 832px exceeds design-system §4's ~720px; capped dock
   contradicts §6.
2. **Home is an 82% void** (`web.go:374-385`; `style.css:5479-5541`); five
   inconsistent content widths.
3. **No tablet band (768–1200px)** (`style.css:477, 518, 539`): transcript ~344px
   at 1024px with a side pane.
4. **Composer** (`:2047, 4561-4582, 2074-2082`): 50vh ceiling, phone dead gap,
   ~18px model-pill hit target (doc floor: ≥30px).
5. **The stylesheet fights itself**: dead `.composer-send`/`#send-btn` selectors
   (`:274-275` — dead, though Send's press feedback DOES fire via `.btn:active`);
   duplicate conflicting `.btn:active` blocks that cancel the scale-pop
   (`:270-275` vs `:5554-5562`); `--hair` referenced only via fallback (`:4683`);
   27 legacy-alias uses; ~600 shipped-token uses vs zero canonical definitions;
   shipped state colors (green=working/blue=awaiting) contradict design-system §3
   but were a documented "accepted churn" decision (`:13-17`); `--text-dim`
   (2.9:1 measured — sub-AA) used for words in ~37 places; 13 retired ALL-CAPS
   mono labels; 6-radius spread vs two.

## Design decisions

Architecture unchanged: no bundler, no framework, embedded assets, the
`window.SerfRendererInternal` module pattern. `docs/web-ui/design-system.md` is the
north star; where shipped code contradicts it the code changes, and where this spec
extends it the doc gets an addendum in the same commit.

### 1. Rendering pipeline

- **Frame batching — live events only.** Only live socket events queue and flush
  once per animation frame. The synchronous replay paths (initial hydration,
  `prependOlderTurns`' staging render+measure) are exempt. `isNearBottom()` is
  measured once per flushed frame; scroll settles once after.
- **Hidden-tab fallback [v3].** rAF never fires in hidden tabs. The flush
  scheduler is: if `document.hidden`, a best-effort `setTimeout` (browsers clamp
  background timers to ≥1s; correctness does not depend on the interval — the
  queue always drains fully); else rAF. Additionally, any pending queue is
  flushed immediately on `visibilitychange` in BOTH directions — hiding a tab
  with an in-flight rAF must not strand events until refocus. The rAF call is
  feature-guarded (`typeof requestAnimationFrame === "function"` → else 16ms
  timeout) because plain jsdom (6 jstest files) has no rAF.
- **Queue lifecycle [v3].** The queue stores ordered `(kind, data)` events plus
  the ordered `(method, params)` of each originating notification. On flush:
  apply all events, then replay `reconcilePendingFromNotification` once **per
  queued notification, in order** — the existing per-notification invariant
  (`renderer.js:677-684`) is preserved exactly, and optimistic placeholders can't
  leak or cross-match. The queue is drained and generation-guarded on
  `resetTranscriptReplay`.
- **jstest migration.** The batching increment ships a synchronous
  `SerfRenderer.flush()` test hook and migrates affected tests in the same
  commits; the suite stays green at every commit.
- **Streaming text [v3 — frozen head, raw tail].** Assistant deltas coalesce per
  frame. While accumulated length ≤4KB: markdown re-parse at most once per frame.
  Past 4KB the message switches — once, on length only (never on fence state),
  no flip-back: the current parsed DOM is **frozen in place** and subsequent
  deltas append as plain text in a `.streaming-tail` node below it, with a CSS
  streaming caret. The user keeps the formatted head; the raw tail is bounded to
  long messages. Finalization re-parses the whole buffer. The message element
  keeps `.assistant-message` + `data-turn-id` at all times (the turn-meta badge
  query at `:1122` and windowing depend on it). The communicate dedup
  (`:2195-2210`) compares against the accumulated **source buffer** while a
  streaming message is active, not DOM `textContent` (a raw tail breaks the
  rendered-text comparison → duplicate communicate messages).
- **Idempotent finalization [v3 — the critical round-2 catch].** Appwire can
  emit `TURN_COMPLETED` *and then* a synthesized `ASSISTANT_TEXT_END` in the same
  notification (codex turn/completed-with-items path, `appwire.js:992-998,
  734-740`). Therefore: finalization runs on `ASSISTANT_TEXT_END`,
  `TURN_COMPLETED`, or `SESSION_END`, **whichever arrives first**, and is
  idempotent per turn: a later `ASSISTANT_TEXT_END` for an already-finalized
  message **replaces the finalized block's content in place** (never
  `appendAssistantBlock` a duplicate). Turn-meta is (re)appended **after** any
  (re)parse, since re-parsing destroys children. A jstest covers the
  codex-shape `TURN_COMPLETED → ASSISTANT_TEXT_END` sequence, not just the
  serf-source shape.
- **Reasoning deltas** append text nodes; the 200-char preview tail is
  recomputed at most once per frame, not per delta.
- **Tool output.** During streaming: append-only `textContent` on a single `<pre>`
  that is max-height-clamped in CSS (live tail visible, no viewport flood). The
  5-line fold chrome, binary detection, and `data.error` replacement apply once
  at `bodyEnd`. Diff rows coalesce per frame.
- **Windowing [v3].** `content-visibility: auto` with
  `contain-intrinsic-size: auto <estimate>` on **every direct child of
  `#conversation`** — `.assistant-message`, `.user-message`, `.tool-call`,
  `.tool-call-cluster` (the flat child after cheap-tool regrouping — the most
  numerous entry in heavy sessions), `.think`, banners, job cards, subagent
  modules, system lines. No DOM regrouping. `auto` gives exact remembered sizes
  for anything rendered once. For never-rendered prepended history (estimates
  only), `prependOlderTurns` does a **two-phase scroll settle**: restore
  `scrollTop` from the estimate-based scrollHeight immediately, then re-measure
  and correct on the next frame once the browser has rendered the
  near-viewport entries. Residual drift is bounded to deep-history paging and
  verified in the playwright matrix.
- **Hydration.** Per-event scroll work suppressed during replay; one scroll
  settle at the end.
- **Scroll handler.** rAF-throttled; error anchors cached, invalidated on
  tool-end **and on prepend/hydration** (errored rows enter via history paging
  too).
- Remove the per-event `JSON.stringify`→`parse` round trip (keep the
  `handleData(kind, obj)` signature tests use).

### 2. Layout system

- **One width scale.** `--measure: 720px` prose column (per design-system §4);
  machine rows bleed right to `--measure-machine` (~1000px, clamped by the
  container); left edges never move. All pages snap to the scale.
- **Breakpoint ladder.** Phone ≤767px (unchanged); tablet 768–1199px; desktop
  1200–1799px; wide ≥1800px.
- **Wide ≥1800px.** Prose measure stays 720px at every width; the machine bleed
  widens to ~1200px. design-system.md §4 gets an addendum recording this. The
  composer dock spans the window per §6.
- **Tablet 768–1199px [v3].** Side panes hidden (as ≤767px today). Sidebar
  auto-rails via tri-state `data-sidebar-mode="auto|rail|pane"`:
  - localStorage migration: `"true"`→`rail`, `"false"`→`pane`, absent→`auto`
    (auto is new; before this band existed the sidebar simply stayed full, so
    this is a deliberate, documented improvement, not a silent regression).
  - Effective state is computed in one JS helper (attribute + `matchMedia`);
    `panes.js`'s resizer-disable and the settings radio consume that helper —
    no split-brain in auto mode.
  - ⌘B cycles `rail → pane → auto`; every press produces a visible or
    state-visible change.
  - **Pickers:** `dir-picker.js:7-20` and the mirrored copy in `spawn.js`
    position fixed 480–520px panels via JS at a hardcoded 767px branch — both
    move to the shared helper and clamp inside the viewport at tablet widths
    (or use the bottom-sheet path).
- **Composer.** Dock spans the window, input card centered at measure; desktop
  model pill ≥30px hit target; phone control row rebalanced; short-desktop rule
  (height <640px compacts chrome — the playwright matrix gains a short-height
  case); textarea px ceiling.
- **Home launchpad [v3].** Server-rendered "Jump back in" column: up to 8
  sessions across projects, sorted by `UpdatedAt` desc, from Current+Recent
  tiers of the `/api/tree` data (title, project, relative age). **No live status
  dots** — the home page has no appwire connection (it connects lazily per
  renderer RPC only), so v2's client hydration would deliver nothing; age-only
  markup can't go stale. **All interpolated strings HTML-escaped** with a Go XSS
  test. Empty cases: zero sessions (first run) → the quiet wordmark welcome
  (that state is honest); sessions all archived → welcome + a "search all
  sessions" affordance.
- **Sidebar.** One left-inset rhythm.

### 3. CSS consolidation

- **One color-token vocabulary [v3 — correctly scoped and mapped].** Today:
  ~600 uses of shipped color names (`--text` 158, `--text-muted` 256, `--rule`
  104, `--text-dim` 43, …) across 4 theme blocks; zero canonical definitions.
  Plan: (a) define canonical tokens in all 4 theme blocks; (b) rename use sites
  per an explicit **mapping table** (in the implementation plan — e.g.
  `--text`→`--ink`, `--text-muted`→`ink-2`, `--text-dim`→`--ink-4`,
  `--rule`→`--line`), word-boundary-safe because the canonical `--text-*` **type
  scale** (`--text-2xs…--text-2xl`, kept per design-system §3 and redefined by
  `body[data-font-size]`) shares the prefix — the "no legacy names" assertion
  carries that documented carve-out; (c) delete the 27 legacy aliases and old
  names. Values: aliases adopt **shipped** values (no visual change) **except**
  `--ink-3`, which takes the doc's `#7e8593` — shipped `--text-muted #7a7a86`
  measures 4.24:1 on raised surfaces (sub-AA); the doc value passes on both
  [v3 — R4's computed-contrast catch]. The doc §3 hex table gets an addendum
  aligning it to shipped values where they differ.
- **State colors [v3 — full surface].** Adopt the documented language
  (blue=live, amber=needs-you, red=error, neutral=done), reversing the recorded
  "accepted churn" decision with owner approval, and fixing the
  `style.css:13-17` comment's misattribution (it cites design-system §3, which
  documents the opposite). Verified true surface — 24 `--state-*` definitions
  (6 tokens × 4 blocks), **125** `var(--state-*)` uses, ~49 `data-state` CSS
  selectors, ~25 JS/template setters (sidebar.js, search.js, renderer.js,
  workspace.html, credentials/plugins inline scripts), `thread.html:8` hardcoded
  favicon, and these existing tests that hard-assert the old world and are
  rewritten in the same commit: `test-color-system-css.js`,
  `test-notifications-palette.js`, `test-style-palette.js`,
  `test-context-pressure-css.js`, `test-subagents.js`. The warning tier (31
  `--state-warning` uses) becomes `--diagnostic-warning`; `--diagnostic-hub` is
  re-hued to not collide with `--diagnostic-ui`. **Favicon [v3]:** the hardcoded
  hexes are deliberate — the favicon renders against dark browser chrome
  regardless of page theme (`notifications.js:35-38`). Keep theme-independent
  pinned constants (comment updated to reference the dark-theme tokens) but
  update all three sites (`:32-33` PLAIN_FAVICON, `:39-43` STATE_COLORS, `:129`)
  to the new language: base favicon goes neutral (post-recolor blue = working,
  so a blue base favicon would read as "working" at rest).
- **Delete dead CSS:** `.composer-send`/`#send-btn`; the duplicate `.btn:active`
  blocks (keep the scale-pop, one rule); define `--hair`.
- **Contrast pass:** `--text-dim` stops coloring words (~37 sites → `--ink-3`);
  short-think tier keeps its recede via size + italic.
- **Retire ALL-CAPS mono labels** (13 sites); radius scale → the documented two.
- **Contract tests [v3]:** every existing stylesheet-asserting jstest
  (`test-font-size-presets.js`, `test-mobile-css.js`,
  `test-pane-and-sidebar-css.js`, etc.) is migrated in the **same commit** as
  the change it covers — the per-commit gate never breaks.

## Error handling

- Batching: full-drain flush every time; flush on hide AND on visible; rAF
  feature-guarded; reconnect drains and generation-guards; replay paths bypass;
  reconcile replays per notification in order.
- Streaming text: finalization is idempotent per turn; a late
  `ASSISTANT_TEXT_END` replaces in place; `marked.parse` failure leaves plain
  text (current behavior); turn-meta re-appended after any reparse.
- Windowing: two-phase prepend settle; estimates only ever affect
  never-rendered deep history.
- Sidebar: tri-state with migration; one effective-state helper for all JS
  consumers.

## Testing

- **jstest (node + jsdom):** frame coalescing; rAF-absence fallback; hidden-tab
  timer flush; flush-on-hide; queue drain on reset; per-notification reconcile
  replay; `flush()` hook; streaming-text switch at 4KB with frozen head +
  `.assistant-message`/`data-turn-id` preserved; **idempotent finalization on
  the codex-shape `TURN_COMPLETED → ASSISTANT_TEXT_END` sequence**; communicate
  dedup against the source buffer; append-only tool output + bodyEnd chrome;
  hydration settle-once; per-child `content-visibility`; tri-state helper logic
  (attribute + stubbed matchMedia); renderer-file mirror test
  (`jstest/load-renderer.js` RENDERER_FILES ≡ `templates/app.html` script
  order).
- **Stylesheet-text assertions:** no legacy color-token names (with the
  `--text-*` type-scale carve-out), hit-target min-heights, breakpoint rules,
  dead selectors gone.
- **Go tests:** launchpad markup incl. XSS-escaping case; empty/archived
  variants.
- **Playwright matrix (dev-time, not CI):** 390/768/1100/1440/2560px **plus a
  short-height (1100×600) case** × dark/light × home/session, before/after,
  reviewed by eye; deep-history prepend scroll-stability spot check.
- Gate per increment: `make build-hub` + `jstest/run-all.sh` +
  `go test ./cmd/serf-hub`.

## Increments [v3 — dependency order]

1. **Test groundwork:** `flush()` hook (initially no-op passthrough), rAF
   feature-guard, renderer-file mirror test. No behavior change.
2. **Frame batching** + jstest migration to `flush()`.
3. **Streaming text** (frozen head/raw tail, idempotent finalize, dedup fix) +
   reasoning append + tool-output append-only + diff coalescing.
4. **Windowing + hydration settle + scroll-handler throttle/cache.**
5. **Layout scale + breakpoint ladder** (width tokens, tablet band, wide band,
   sidebar tri-state incl. picker JS) + composer fixes.
6. **Home launchpad** (Go + CSS).
7. **Token migration** (alias → rename → delete) + contract-test migration.
8. **State colors** (full surface incl. favicon + tests).
9. **Contrast + retired treatments** (uppercase labels, radius, `--text-dim`
   words).

Each lands green per the gate; 7 must precede 8 (state colors reference
canonical tokens); 2 precedes 3; 5 precedes 6.

## Out of scope

- Subagent-sidebar and multi-pane feature work owned by other efforts (side
  panes are only hidden at tablet widths).
- design-system.md *principles* (addenda recording new rules are in scope).
- Backend/appwire protocol changes — all fixes are client-side except the
  server-rendered launchpad.
