# Compact realtime transcript-visualization proposals — slot `glm52-b`

Three compact, always-on companion visualizations for Evener session transcripts.
Each is a self-contained HTML file (inline CSS/JS, no network, works from `file://`),
driven by the **same synthetic session** so they can be compared honestly:

- session `s_4a9f…2c1` · branch `main` · model `glm-5.2-vision`
- 245 turns (0–244), ~2,034 tool calls, ~22–23 min wall
- 4 user prompts (turns 0, 61, 124, 215) + 2 steering inputs (150, 205)
- 3 compaction checkpoints (59, 122, 213) each followed by a carry-forward summary
- 8 delegates across 3 levels; `dlg_e5 verify-in-browser` ends errored
- 4 shell jobs (1 detached, 3 background), 2 watches (job-completion + fs)
- a rate-limit retry episode (turns 163–168, attempts 6/11 → 11/11)
- 1 model switch (turn 130), 4 hook completions, 1 env turn

All three share the same dark palette, the same seeded RNG (`_s=9173`), and the same
transport convention: **play/pause + speed + progress live in the page chrome, never
inside the component.**

## Provenance (read this first)

`idea-1.html` and `idea-2.html` were authored by the original `glm52-b` delegate,
which stalled mid-task and was retired. **Both files were non-functional as left**
(idea-1: its entire event dataset and two DOM elements were never defined, so the
script died on load; idea-2: an unbalanced brace was a hard SyntaxError, killing the
whole script). A **substitute delegate** minimally repaired them (exact repair list at
the bottom of this file) and authored **`idea-3.html` and this README**. Design intent
of ideas 1–2 belongs to the original delegate; any misjudgment in the repairs is the
substitute's.

---

## Idea 1 — Query-Rift — `idea-1.html`

**Form factor:** vertical rail, 160 px wide × full height (the always-on component);
paired in the demo page with a detail pane (facet filters + turn list) occupying the
`1fr` remainder — in production that pane would be a drawer/popover, not chrome.

**Comprehension question:** *“Which turns in this long session match an ad-hoc query —
delegates? failures? jobs > 30 s? compaction boundaries? — and where do those turns
cluster in time?”* It is a query-first atlas: you start from a clause, not from scrolling.

**Encoding:** one cell per turn on a horizontal strip inside the rail. x = turn index;
cell height = tool-call count (0 tools → 14 px, 25 tools → ~118 px; checkpoint/summary
fixed 100 px; plain assistant 22 px); cell color = turn kind (user cyan, steering teal,
assistant ink, checkpoint/summary violet, failure red, hook green, model-switch amber,
env slate). Ticks above cells: teal = delegate-linked, amber = job-linked, red = error;
violet outline = compaction. A cyan brush band with two draggable handles scopes the
detail list; matching cells get a glow, non-matches dim to 16 %.

**Update model:** the full strip is pre-rendered (final-state atlas); live mode advances
a cursor every `1000/speed` ms — it refreshes the current cell, moves the green `live`
glow, slides the brush to trail the last 5 turns, and recomputes facet counts, chip
counts, and the filtered turn list. Free-text query input maps keywords to clauses
(`429`/`retry` → retries, `delegate` + `error` → errored delegates, …); chips and facet
rows toggle clauses conjunctively; `Esc` clears.

**Scaling:** 245 turns × ≥2 px cells ⇒ ~490 px of cell field inside the 160 px rail —
the strip scrolls horizontally (`overflow-x:auto`); the turn list caps at 220 rows with
a “+N more in brush” note; brush + clauses are the narrowing mechanism. Counting is
O(turns) per tick — fine to ~10³ turns; beyond that it would need cell aggregation and
list virtualization (not implemented).

**Exact px footprint:** rail = 160 px × (100 vh − header ≈ 62 px − controls ≈ 62 px −
footer ≈ 37 px). Inside the rail: axis row ~18 px, strip field 148 px tall, summary row
~20 px. Detail pane cells: 2–4 px wide × 14–118 px tall.

**Prompt-jump mechanism:** none. Idea 1 has no transcript pane; user/steering turns are
queryable as the `user + steering` clause and appear as cyan/teal cells and list rows,
but clicking only selects within the list.

---

## Idea 2 — Delegate Cascade — `idea-2.html`

**Form factor:** its compact, always-on element is a **horizontal strip: full width ×
100 px** (`.timeline-overlay`) bottom-anchored over a demo tree canvas. (The mockup
surrounds it with a full-page delegation tree — that tree is demo real estate, not the
compact component.)

**Comprehension question:** *“How did the delegation tree actually unfold — who spawned
whom, when, for how long, at what cost, and which branch errored?”* It answers with
structure, not filters: lanes by depth, spans by lifetime, sibling compare for cost.

**Encoding:** lanes L0–L3 by delegation depth (y = 30/50/70/90 px); one node card per
delegate at x = spawn turn containing: level pill, name, outcome pill (✓/✕), a 12-bar
synthetic token-spend sparkline (red tail for the errored delegate), tools/tokens row,
spawn/done/wall row. Elbow edges connect parent → child. The bottom strip renders each
delegate’s lifetime as a span bar (10 px, blue ok / red err) and each shell job as a
thin amber bar (6 px). During playback, in-flight nodes and bars get a dashed flashing
outline.

**Update model:** the tree is rendered once at init (final state); live mode sweeps a
turn counter 0 → 244 every `1000/speed` ms, toggling `in-flight` on every node/bar whose
[spawn, done] span contains the current turn. Click a node → inspect panel (tools,
tokens, wall, span, jobs inside the span, IN-FLIGHT note when applicable); click a
second node with the same parent → side-by-side compare cells; `Esc` clears.

**Scaling:** node cards are fixed 148 px wide at x ∝ spawn turn, so densely-spawning
delegates overlap (visible in the mockup around t76–t172); `.tree` has
`min-width:900px` and the area scrolls (`overflow:auto`). Comfortable to ~10 delegates;
beyond that it needs lane stacking or collapse-to-parent (not implemented). The strip
scales to ~10³ spans before bars go sub-pixel.

**Exact px footprint:** compact strip = container width × 100 px (delegate bars 10 px
at y = 18 px, job bars 6 px at y = 10 px). Node cards 148 × ~92 px. Right panel 300 px
(demo only).

**Prompt-jump mechanism:** none. Selection is delegate-centric (inspect/compare panel);
user turns are not represented.

---

## Idea 3 — Session Lens — `idea-3.html`  *(authored by the substitute delegate)*

**Form factor:** **corner badge, 312 × 232 px**, `position:fixed` bottom-right beside a
dimmed mock transcript column. No scrolling anywhere inside the component; the whole
session-so-far plus the live-now cursor are always visible in the footprint. Only
popovers exceed the budget (a 284 px card floats above the badge), which the brief
explicitly allows. This is the one form factor ideas 1–2 did not use (vertical rail ≤
160 px and horizontal strip ≤ 100 px were taken).

**Comprehension question:** *“While it’s still running — where are the moments that
matter (my prompts, failures, compaction folds, delegate/job/watch spans), and can I
slice straight to any of them with one gesture?”* The badge is an **instrument**: the
field is the query surface, not just a picture.

**Encoding:** a canvas event field (296 × 150 CSS px, DPR-scaled), 8 lanes × turn on x:
**P** prompt (user+steering) · **A** assistant/env/hook/model-switch · **T** tools
(glyph height ∝ tool count) · **D** delegates · **J** jobs · **W** watches ·
**C** compaction · **F** failures. One 1-px glyph per turn per lane (~1.15 px/turn at
245 turns). In-flight delegate/job/watch spans render as translucent fills with
animated marching-ants brackets; the now-cursor is a pulsing green vertical line; a
LIVE pill sits in the badge header (dims when the stream is paused/complete). Every
user_input/steering turn gets a **permanent numbered diamond anchor** on the top track
(cyan = user, teal = steering) — real DOM buttons, never aggregated away.

**Interaction (the point of the idea):**
- **hover** the field → the lane under the cursor is isolated; all other lanes dim to 15 %
- **click** the field → jumps the transcript to that turn + opens a detail popover
  (turn #, kind, text, tools, delegate/job/watch links, wall clock, “jump ↗”)
- **drag** → brushes a turn band; the transcript dims outside the range and scrolls to
  its start; the status line reports prompts-in-range; double-click / `Esc` clears
- **type** in the `?` box → substring query over turn text/tool/entity haystack;
  non-matching turns dim to 15 %, status reports the match count
- **click a ♦ anchor** → transcript smooth-scrolls to *that exact prompt* and
  flash-highlights it (1.6 s); hover previews the prompt text in a popover

**Update model:** append-only reveal. Each transport tick (`1000/speed` ms, speeds
1×–16×, plus reset and a follow toggle) reveals the next turn on the field, appends its
transcript block (with a transient `● live` tag), and materializes anchors as their
prompts arrive. A rAF loop drives the now-line pulse and marching ants. Auto-scroll
follows the stream until any jump turns follow off.

**Scaling:** the field is fixed-width, so x-resolution degrades gracefully: 1 px/turn
holds to ~280 turns, sub-pixel thereafter; honest to ~10³ turns before lanes visually
saturate (canvas overplot is cheap; aggregation would be the next step and is not
implemented). Anchors are permanent DOM elements at fixed pixel positions — with 6
prompts they are widely separated; a pathological 60-prompt session would need
min-px-separation stacking, deliberately out of scope because the hard requirement is
that anchors are *never aggregated*.

**Exact px footprint:** 312 × 232 px (within the ≤ 320 × 240 budget). Internals: header
16 px, field 296 × 150 px (13 px anchor track + 8 lanes ≈ 17.1 px each), query row
18 px, status row 13 px, padding/gaps ~23 px. Popover 284 px wide, overflows upward.

**Prompt-jump mechanism (hard requirement — demonstrated):** anchors are `<button>`
elements positioned at `x = turn/244 × plot width`, one per user_input/steering turn,
numbered in arrival order. Click → `document.getElementById('t-'+turn)` →
`scrollIntoView({behavior:'smooth', block:'center'})` + flash class + pinned popover.
Verified in-browser: clicking anchor ♦2 scrolled the mock transcript to turn 61
(“The minimap idea feels generic…”), flash-highlighted it, opened its popover, and set
the status line to “jumped to turn 61 · follow off”.

---

## Repairs applied by the substitute delegate (minimal, functionality-restoring only)

**idea-1.html**
1. Added the missing `EVENTS` dataset: the seeded generator (`buildEvents()`) plus
   `KIND_COLOR`/`KIND_LABEL` maps, `USER_TURNS`/`STEER_TURNS`/`FAIL_TURNS`/`HOOK_TURNS`
   — the script referenced all of these but defined none, so nothing rendered.
2. Added the missing `#q` query input and `#chips` container to the controls row, with
   matching CSS — both were referenced by the JS and advertised in the footer.
3. Fixed a render crash: `d.name.split(':')[1].trim()` on colon-less delegate names.
4. Guarded the strip filter loop against the brush overlay div (out-of-range `m[i]`).
5. Fixed `.app` grid rows (`auto auto 1fr auto`) so the controls row no longer
   absorbs the flexible row; allowed the controls row to wrap.

**idea-2.html**
1. Closed the unbalanced brace in `buildTree()` (a SyntaxError that killed the entire
   script) and removed the dangling `userEl` reference inside it.
2. Fixed the sibling-compare predicate (it read `.parent` on an id *string*, so compare
   could never trigger).
3. Rewrote the dead `tick()`: it looped `idx` 0–7 while comparing against spawn values
   0–157, so playing did nothing and progress read “/ 8”. It now sweeps turns 0–244 and
   toggles `in-flight` on nodes/bars.
4. Added the layout/panel CSS the markup referenced but never defined (`main` grid,
   `.tree-area`, `.panel`, `.stat-grid`, `.cmp-row/.cmp-cell`, `.row2`, `.live-stat`,
   `.node.in-flight`), and the same `.app` grid-rows fix; progress label 245 → 244.

## Verification (one pass, Chrome, `file://`)

- idea-1: opened, played at 4× to turn 85/245 — strip, chips, facets, brush-following
  list all live. Re-checked after the grid-rows fix. OK.
- idea-2: opened, played to turn ~91/244 — tree, edges, timeline strip, in-flight
  flashing on `build-mockups` (spawn 76 ≤ 91 ≤ done 115) all render. First open exposed
  the clipped layout; fixed (grid rows) and re-verified. OK.
- idea-3: opened; play at 16× ran the stream to completion organically. Anchor jump
  demonstrated (click ♦2 → transcript at turn 61, flash, popover). Animation-advance
  check: two screenshots at different sim times — **t 050/244** (1 anchor; in-flight
  dashed spans on D/J/W; 51 turns revealed) and **t 150/244** (4 anchors; in-flight
  spans on J/W; 151 turns revealed; steering block live). Driven to those exact turns
  via in-page eval of the component’s own reveal path because headless tool round-trips
  (~10 s/action on this DOM) outrun the 16× clock. No network requests anywhere; all
  CSS/JS inline.
