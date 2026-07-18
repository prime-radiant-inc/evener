# Serf Web Hub — Foundation + Experience Pass

Status: approved (2026-07-18). Work branch: `webui-joy`.
Scope: `cmd/serf-hub` frontend (`assets/`, `templates/`, and `web.go` home rendering).

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
   of the viewport. Also contradicts `design-system.md` §4 (~720px cap) and §6
   (dock spans the window).
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
   (`:274-275` — Send's press feedback never fires); duplicate conflicting
   `.btn:active` blocks (`:270-275` vs `:5554-5562`); undefined `--hair` (`:4683`);
   27 legacy-alias uses; three parallel token namespaces; inverted state colors
   (shipped green=working/blue=awaiting vs doc blue=live/amber=needs-you);
   `--text-dim` (3.4:1, sub-AA) used for words in ~37 places; 13 retired ALL-CAPS
   mono label treatments; 6-radius spread vs the documented two.

## Design decisions

Architecture is unchanged: no bundler, no framework, embedded assets, the
`window.SerfRendererInternal` module pattern. `docs/web-ui/design-system.md` is the
north star; where shipped code contradicts it, the code changes.

### 1. Rendering pipeline

- **Frame batching.** Socket events queue and flush once per
  `requestAnimationFrame`. `isNearBottom()` is measured once per frame before
  mutations; scroll settles once after.
- **Streaming text.** Assistant deltas coalesce per frame. Short messages
  (<4KB, no open code fence) re-parse markdown at most once per frame; long or
  complex messages stream into a plain `.streaming-text` node with the existing
  soft cursor, and markdown parses once at `ASSISTANT_TEXT_END`. Reasoning deltas
  append text nodes instead of full-buffer replacement.
- **Tool output.** Streaming output is append-only `textContent` on a single
  `<pre>`; expand/collapse chrome is built once at `bodyEnd`. Diff rows coalesce
  per frame.
- **Windowing.** `content-visibility: auto` with `contain-intrinsic-size` on turn
  containers. No hand-rolled virtualization; DOM stays intact for find-in-page.
- **Hydration.** Per-event scroll work suppressed during replay; one scroll
  settle at the end.
- **Scroll handler.** rAF-throttled; error anchors cached, invalidated on
  tool-end.
- Remove the per-event `JSON.stringify`→`parse` round trip.

### 2. Layout system

- **One width scale.** `--measure: 720px` (reading column, per design-system §4);
  machine rows (tool output, diffs) may bleed right to `--measure-machine`
  (~1000px); left edges never move. Home, `/new`, settings, and transcript snap to
  this scale.
- **Breakpoint ladder.** Phone ≤767px (unchanged); **tablet 768–1199px** (sidebar
  auto-collapses to the 56px rail, side panes become overlays, pickers fluid);
  desktop 1200–1799px (current); **wide ≥1800px** (measure ~880px, machine rows
  ~1200px, composer dock spans the window per §6).
- **Composer.** Dock spans the window with the input card centered at measure;
  desktop model pill ≥30px hit target; phone control row rebalanced (no oversized
  send disc + dead gap); short-desktop rule (height <640px compacts header +
  status rail); textarea gets a px ceiling.
- **Home launchpad.** Replace the empty state with a "Jump back in" column:
  recent sessions (title, project, relative time, status) plus new/search
  actions, server-rendered from roster data, on the shared width scale.
- **Sidebar.** One left-inset rhythm.

### 3. CSS consolidation

- **One token vocabulary:** canonical `design-system.md` names (`--ink`,
  `--surface`, `--line`, `--hair`, `--attention`, `--done`); delete the 27
  legacy-alias uses and the third namespace.
- **State colors per the doc:** blue = live/active, amber = needs-you, red =
  error, neutral = done.
- **Delete dead CSS:** `.composer-send`/`#send-btn` selectors, duplicate
  `.btn:active` blocks (keep the scale-pop), define `--hair`.
- **Contrast pass:** `--text-dim` stops coloring words (~37 sites → `--ink-3`
  minimum); the short-think tier keeps its recede via size + italic.
- **Retire remaining ALL-CAPS mono labels** (13 sites); consolidate the radius
  scale to the documented two values.

## Error handling

- Frame batching must not drop events: queue overflow is impossible (unbounded
  array), and a flush always drains the full queue. A socket reconnect mid-frame
  re-runs hydration through the same detached path.
- `content-visibility` gets a `contain-intrinsic-size` estimate so scrollbar
  geometry stays stable; elements near the viewport render normally.
- Streaming-text fallback: if `marked.parse` throws at `ASSISTANT_TEXT_END`,
  the message stays as plain text (current behavior on parse failure is
  preserved).
- The tablet rail collapse is CSS/container-query driven; manual ⌘B toggle
  continues to override it.

## Testing

- Extend `cmd/serf-hub/jstest/`: frame batching coalesces parses (N deltas → ≤1
  parse per frame), streaming-text path for long messages, append-only tool
  output, rAF-throttled scroll handler, hydration settle-once,
  `content-visibility` attributes present, breakpoint rules, no legacy token
  aliases, hit-target floors. Mirror `test-renderer.js` patterns.
- Go web tests for the launchpad markup (`web.go`).
- Visual verification matrix (playwright): 390/768/1100/1440/2560px × dark/light
  × home/session, before vs after, reviewed by eye.
- Gate per increment: `make build-hub` + `jstest/run-all.sh` +
  `go test ./cmd/serf-hub`. Small TDD commits on `webui-joy`.

## Out of scope

- Subagent-sidebar and multi-pane feature work owned by other efforts.
- The design-system doc itself (no principle changes).
- Backend/appwire protocol changes — all fixes are client-side except the
  server-rendered home launchpad.
