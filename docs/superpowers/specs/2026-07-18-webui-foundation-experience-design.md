# Serf Web Hub — Foundation + Experience Pass

Status: approved (2026-07-18), revised after adversarial review (v2, same day).
Work branch: `webui-joy`.
Scope: `cmd/serf-hub` frontend (`assets/`, `templates/`, and `web.go` home rendering).

v2 changes: two adversarial reviewers each found 13 verified issues in v1
(reports in session transcripts `local:033rmJG0Qq602H7gRXkinK` and
`local:033rmJGtbkJZDf40au0ZnP`). This revision resolves all of them; each fix is
marked **[v2]**.

## Background

The owner reports the hub feels "clunky, unbalanced, and not very responsive" — both
interaction latency/jank and layout adaptiveness. Two read-only audits (2026-07-18)
produced the findings below; all line numbers refer to `cmd/serf-hub/assets/` on
`main` at `be125a62`.

### Jank findings (performance audit)

1. **Per-token full markdown re-parse.** `appendAssistantDelta` re-runs `marked.parse`
   on the entire accumulated message and replaces `innerHTML` on every delta
   (`renderer.js:2212-2222`). O(L²) per message; destroys selection/hover; full
   re-layout per token.
2. **Per-event layout thrash.** `handle()` reads `isNearBottom()` then calls
   `scrollToBottom()` around every event (`renderer.js:1062, 1402-1403`). During
   hydration replay (`renderer.js:823-826`) this runs per replayed event → O(N²)
   session open.
3. **Unbounded transcript DOM.** No windowing or pruning anywhere; every layout
   walks every node.
4. **Tool-output DOM rebuild per delta.** `setExpandableOutput`
   (`renderer-tools.js:135-172`, called from `:528-531` and `:225-227`) rebuilds the
   whole output subtree on every output delta.
5. **Unthrottled scroll handler** doing O(transcript) `querySelectorAll` and layout
   reads per scroll event (`renderer.js:4510-4521`, `4470-4477`).
6. Medium: diff DOM rebuild per delta (`renderer-tools.js:26-43, 580, 614`);
   reasoning full-buffer `textContent` replace per token (`renderer.js:2328-2338`);
   per-event `JSON.stringify`→`parse` round trip (`renderer.js:1047-1049, 1068`).

### Imbalance findings (layout audit)

1. **No wide-viewport strategy.** One 832px column + fixed 260px sidebar at every
   width ≥768px (`style.css:500, 511-516`). At 2560px the transcript occupies ~32%
   of the viewport. Shipped 832px also exceeds `design-system.md` §4's ~720px cap,
   and the capped dock contradicts §6 (dock spans the window).
2. **Home is an 82% void.** A ~150px cluster `margin: auto`-centered in 100vh
   (`web.go:374-385`; `style.css:5479-5541`). `/new` and settings use different,
   also-unbalanced strategies; five inconsistent content widths
   (832/880/920/720/640px).
3. **No tablet band (768–1200px).** Full sidebar + fixed 420px side panes persist
   to 768px (`style.css:477, 518, 539`); at 1024px with a side pane open the
   transcript gets ~344px.
4. **Composer.** 50vh textarea ceiling with no short-desktop rule
   (`style.css:2047`); phone rest row is two 44px discs flanking a dead gap
   (`:4561-4582`); desktop model pill is an ~18px hit target, violating
   `design-system.md` §6's ≥30px floor (`:2074-2082`).
5. **The stylesheet fights itself.** Dead `.composer-send`/`#send-btn` selectors
   (`:274-275`) **[v2 correction: the Send button's press feedback DOES fire via
   `.btn:active` in the same rule — only the two legacy selectors are dead]**;
   duplicate conflicting `.btn:active` blocks (`:270-275` vs `:5554-5562`) that
   silently cancel the scale-pop; undefined `--hair` (harmless today — has a
   `var(--rule)` fallback at `:4683`); 27 legacy-alias uses; ~600 uses of the
   shipped token names vs zero of the canonical ones; inverted state colors
   (shipped green=working/blue=awaiting vs doc blue=live/amber=needs-you — but see
   §3: this was a **documented deliberate decision**, `style.css:13-17`);
   `--text-dim` (3.4:1, sub-AA) used for words in ~37 places; 13 retired ALL-CAPS
   mono label treatments; 6-radius spread vs the documented two.

## Design decisions

Architecture is unchanged: no bundler, no framework, embedded assets, the
`window.SerfRendererInternal` module pattern. `docs/web-ui/design-system.md` is the
north star; where shipped code contradicts it, the code changes — and where this
spec extends the doc, the doc gets an addendum in the same commit (see §2).

### 1. Rendering pipeline

- **Frame batching — live events only [v2].** Only *live socket* events queue and
  flush once per animation frame. The two synchronous replay paths are exempt and
  stay synchronous: initial hydration replay (`renderer.js:823-826`) and
  `prependOlderTurns`' staging-div render + measure (`:4608-4658`), which measures
  the staged fragment immediately and would break under a queued flush.
  `isNearBottom()` is measured once per flushed frame (not per event); scroll
  settles once after.
- **Hidden-tab fallback [v2].** `requestAnimationFrame` never fires in hidden
  tabs, and a backgrounded hub tab is the common case. When `document.hidden`,
  flush on a 250ms `setTimeout` instead (state still applies, liveness clock and
  `serf-hub:thread-status` still dispatch, no monster refocus flush); also flush
  once on `visibilitychange → visible`.
- **Queue lifecycle [v2].** The queue is drained and invalidated on
  `resetTranscriptReplay` (reconnect) with a generation guard, so stale events
  never flush into a rehydrated transcript. Ordering with the existing
  `descriptionsReady` event buffer (`:1053-1056`) and the pending-reconcile
  invariant (`reconcilePendingFromNotification` must run *after* the reducer
  applies, `:673-686`) is preserved: reconcile runs at the end of the flush, not
  per event.
- **Existing jstest suite [v2].** ~50 test files drive `handleData` and assert
  synchronously. The batching increment ships a synchronous `SerfRenderer.flush()`
  test hook and migrates affected tests to call it; the suite must stay green at
  every commit. This migration is budgeted as part of the increment, not an
  afterthought.
- **Streaming text [v2].** Assistant deltas coalesce per frame. Short messages
  (<4KB accumulated, no open code fence) re-parse markdown at most once per frame;
  long or fenced messages switch — once, no flip-back — to a plain
  `.streaming-text` node (new component; there is no existing soft cursor — v1
  misspoke) with a CSS streaming caret whose lifecycle (added on first delta,
  removed on finalize/reset/interrupt) is specified in the component. Markdown
  parses once at finalization. **Finalization happens on `ASSISTANT_TEXT_END`,
  `TURN_COMPLETED`, or `SESSION_END`** — interrupt never emits
  `ASSISTANT_TEXT_END` (`agent/session_lifecycle.go:617-652`), so the renderer
  finalizes on turn end regardless of cause; an interrupted long message renders
  its partial markdown instead of staying raw. `TURN_COMPLETED` currently never
  calls `finalizeAssistantMessage` (`renderer.js:1104-1106`); it will.
- **Reasoning deltas** append text nodes instead of full-buffer replacement.
- **Tool output [v2].** During streaming, output is append-only `textContent` on a
  single `<pre>` that is max-height-clamped with overflow in CSS — the user
  watches the live tail without unbounded viewport growth (preserving the intent
  of the current 5-line fold). The 5-line fold + "expand · N more" chrome, binary
  detection, and error replacement (`data.error` at `:533-536`) are all applied
  once at `bodyEnd`, over the full clipped buffer. Diff rows coalesce per frame
  instead of rebuild-per-delta.
- **Windowing [v2].** `content-visibility: auto` with
  `contain-intrinsic-size: auto <estimate>` applied **per existing transcript
  entry** (`.assistant-message`, `.tool-call`, `.think`, banners — the flat
  children of `#conversation`). No DOM regrouping: v1's "turn containers" do not
  exist, and creating them would break clusters, sibling selectors, and the
  new-content pill's child counting. The `auto` keyword makes the browser reuse
  each element's last-rendered size, so only never-rendered prepended history
  uses estimates; minor scroll drift on first reveal of ancient turns is
  accepted and noted, since `prependOlderTurns` restores position by
  scrollHeight delta which remains approximately correct with remembered sizes.
- **Hydration.** Per-event scroll work suppressed during replay; one scroll
  settle at the end.
- **Scroll handler.** rAF-throttled; error anchors cached, invalidated on
  tool-end.
- Remove the per-event `JSON.stringify`→`parse` round trip (keep the
  `handleData(kind, obj)` signature tests use; pass objects through).

### 2. Layout system

- **One width scale.** `--measure: 720px` for the prose reading column (per
  design-system §4 — shipped 832px is non-compliant); machine rows (tool output,
  diffs, code blocks) may bleed right to `--measure-machine` (~1000px); left
  edges never move. Home, `/new`, settings, and transcript snap to this scale.
- **Breakpoint ladder.** Phone ≤767px (unchanged); **tablet 768–1199px**;
  desktop 1200–1799px (current); **wide ≥1800px**.
- **Wide ≥1800px [v2].** The prose measure stays 720px at every width (no
  contradiction with §4). What widens is the *machine* bleed (~1200px) — the
  content type (long commands, diffs, tool dumps) that actually benefits.
  design-system.md §4 gets a one-paragraph addendum recording this rule in the
  same commit. The composer dock spans the window per §6.
- **Tablet 768–1199px [v2].** Side panes are hidden (as at ≤767px today — no new
  overlay/focus-trap machinery). The sidebar auto-rails via a **tri-state**:
  `data-sidebar-mode="auto|rail|pane"` (auto is the default and media-query
  driven; ⌘B cycles force-rail/force-pane and persists; the settings radio and
  `panes.js:356` read the same attribute, replacing the binary
  `data-sidebar-rail`). Pickers go fluid width.
- **Composer.** Dock spans the window with the input card centered at measure;
  desktop model pill ≥30px hit target; phone control row rebalanced (no oversized
  send disc + dead gap); short-desktop rule (height <640px compacts header +
  status rail); textarea gets a px ceiling.
- **Home launchpad [v2].** Replace the empty state with a "Jump back in" column:
  up to ~8 recent sessions (title, project, relative age) plus the new/search
  actions, server-rendered from `/api/tree` data, on the shared width scale.
  **All interpolated strings are HTML-escaped** (titles are agent-controlled; the
  current handler is raw `fmt.Fprint`) with a Go test asserting escaping. Live
  status dots are hydrated client-side from the already-open appwire connection
  after load — the static markup carries no status that could go stale.
- **Sidebar.** One left-inset rhythm.

### 3. CSS consolidation

- **One token vocabulary [v2 — correctly scoped].** The canonical design-system
  names (`--ink`, `--ink-2/3/4`, `--surface`, `--line`, `--hair`, `--attention`,
  `--done`) have **zero** definitions today; the shipped namespace has ~600 uses
  (`--text` 158, `--text-muted` 256, `--rule` 104, `--text-dim` 43, …) across 4
  theme blocks (dark/light × default/override). Migration: (a) define canonical
  tokens as aliases of the shipped values in all 4 theme blocks; (b) mechanical
  rename per use site; (c) delete the 27 legacy-alias uses and the shipped names;
  (d) jstest asserts no legacy names remain. One vocabulary at the end.
- **State colors [v2].** Adopt the documented language (blue = live/active,
  amber = needs-you, red = error, neutral = done), explicitly reversing the
  recorded "accepted churn" decision (`style.css:13-17`) with the owner's
  approval from the design review. Consumer checklist, all updated in the same
  increment: 65 uses across 4 theme blocks; `data-state` setters (sidebar.js,
  search.js, renderer.js, workspace.html, credentials/plugins inline scripts);
  **`notifications.js:39-43` hardcoded favicon hex** (read the computed token at
  runtime instead of mirroring hex); the warning tier (`--state-warning` /
  diagnostics) keeps amber as a *diagnostic* hue distinct from needs-you via a
  separate `--diagnostic-warning` token, and `--diagnostic-hub` is re-hued so it
  no longer collides with `--diagnostic-ui`.
- **Delete dead CSS:** `.composer-send`/`#send-btn` selectors; the duplicate
  conflicting `.btn:active` blocks (keep the scale-pop, one rule); define
  `--hair`.
- **Contrast pass:** `--text-dim` stops coloring words (~37 sites → `--ink-3`
  minimum, once `--ink-3` exists); the short-think tier keeps its recede via
  size + italic.
- **Retire remaining ALL-CAPS mono labels** (13 sites); consolidate the radius
  scale to the documented two values.

## Error handling

- Frame batching: the flush drains the full queue every time; hidden-tab timer +
  `visibilitychange` flush means events never defer indefinitely; reconnect
  drains and generation-guards the queue; replay paths bypass it entirely. **[v2
  — replaces v1's incorrect "reconnect re-runs hydration through the same
  detached path": initial hydration renders live, only prepend stages detached.]**
- Streaming-text fallback: if `marked.parse` throws at finalization, the message
  stays as plain text (current behavior preserved). Interrupt/turn-end without
  `ASSISTANT_TEXT_END` still finalizes (see §1).
- `content-visibility` uses `auto`-remembered sizes so scroll geometry is exact
  for anything rendered at least once; residual drift is limited to first reveal
  of never-rendered ancient turns.
- The tablet rail is a tri-state; manual ⌘B always has a defined meaning;
  settings UI and `panes.js` read the same source of truth.

## Testing [v2 — calibrated to what the harness can actually do]

- `cmd/serf-hub/jstest/` (node + jsdom, no layout engine): behavioral tests for
  frame coalescing (N deltas in one frame → ≤1 parse), hidden-tab timer flush,
  queue drain on reset, `flush()` test hook, streaming-text switch + finalize on
  `TURN_COMPLETED` without `ASSISTANT_TEXT_END`, append-only tool output +
  bodyEnd chrome, hydration settle-once, per-entry `content-visibility` inline
  style, tri-state sidebar attribute logic, launchpad escaping (Go-side).
- **Stylesheet-text assertions** (new small helper loading `style.css` as text)
  for: no legacy token names, hit-target min-heights present, breakpoint rules
  present, dead selectors gone. These verify rules exist, not that they behave —
  behavior at real sizes is the playwright matrix below.
- The existing jstest suite is migrated to the `flush()` hook as part of the
  batching increment and stays green throughout.
- Go web tests for the launchpad markup **including an XSS-escaping case**
  (agent-controlled title containing HTML).
- **Visual verification matrix (playwright, dev-time tooling — new to this repo,
  not a CI gate):** 390/768/1100/1440/2560px × dark/light × home/session, before
  vs after, reviewed by eye.
- Gate per increment: `make build-hub` + `jstest/run-all.sh` +
  `go test ./cmd/serf-hub`. Small TDD commits on `webui-joy`.

## Out of scope

- Subagent-sidebar and multi-pane feature work owned by other efforts (side
  panes are only *hidden* at tablet widths, not redesigned).
- design-system.md *principles* (addenda recording new rules, like the wide-band
  machine bleed, are in scope and land with their increments).
- Backend/appwire protocol changes — all fixes are client-side except the
  server-rendered home launchpad.
