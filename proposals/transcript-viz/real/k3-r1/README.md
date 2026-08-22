# REAL-1 · Micro Strata Rail — on real session data

Round 4 variant of the anchor `compact/k3-a` **Micro Strata Rail**, re-implemented
honestly on the three real sessions in `../realdata/`. One file: `idea-1.html`
(423 KB, self-contained, all three sessions embedded inline as one JS const,
`file://`-runnable, dark dev-tool aesthetic). Session switcher in the page chrome.

## Comprehension question

> **"What is the session doing *right now*, what did it cost to get here, and where
> did a human last change its mind?"** — answered at a glance, in 160 px, without
> scrolling: head line + animated in-flight stratum (now), real-duration strata +
> token micro-bars (cost), permanent prompt chevrons (human interventions), idle
> bands and red failure ticks (where time actually went).

## Encoding (per lane, x-offsets within 160 px)

| Lane | x | Glyph | Real source |
|---|---|---|---|
| Prompt gutter | 0–13 | solid **blue chevron** per `USER_INPUT`, **amber chevron** per `STEERING` with `steer_src==="user"` — permanent, individual, clickable; hollow until replay head passes | `stream[].kind/steer_src/ts/text` |
| Who | 14–22 | 1.5 px ticks: dim-amber = untagged/daemon steering, near-black = ENVIRONMENT/HOOK/SYSTEM | `stream[]` |
| Step strata | 24–70 | vertical span from ASSISTANT `ts` to its matching TOOL_RESULTS `ts` → **stratum height is real step duration**; teal brightness = # of tool calls; red left edge = errored call; amber right edge = ASSISTANT with no result (session A's final turn #352) | paired `stream[]` turns, `calls[].n/e`, `in/out` |
| Delegates | 72–87 | violet **diamonds** at spawn time (jittered within bursts), `∅` when none | `calls[]` where `n==="delegate"` |
| Jobs | 89–129 | **concurrency ribbon**: per pixel row, count of real job intervals overlapping that time slice; wider/brighter = more simultaneous; red slice while a failed job overlaps; full-width red tick at each failed job's end; grey tick = cancelled; dashed green edge = still-`running` job (1 in C, never ended) | `jobs[].started/ended/status` |
| Events | 131–152 | slate triangle = ATTENTION_RESOLUTION; red diamond = errored call (known at its result time) | `stream[]`, paired steps |
| Full width | — | hatched **⏸ idle bands** with duration labels (`idle 6.3h`); white head line + left chevron | gaps in real `ts` |
| Footer (inside budget) | — | context micro-bar (last assistant `in` ÷ session max), token micro-bar (cumulative ÷ total), `#turn/total` + UTC wall clock, verdict glyphs ⟳ (same tool erred ≥2/≥3 in a row) ⚠ (job failed within last 30 min) … (inside an idle gap or step in-flight >10 min) | derived from records |

**Y axis = gap-compressed real wall time.** Consecutive event timestamps more than
10 min apart close the active segment and insert an 18 px labeled band; everything
else maps linearly. Gaps are therefore *visible and labeled* (requirement) without
letting a 6.3 h idle eat the rail. Session A renders as ~14 bands; C's 6.3 h gap is
a single honest stripe.

## Update model (realtime reconstruction)

Replay is driven by the real timestamps: a 100 ms timer advances `simT` in wall-ms
(1×/120×/1200×/7200×, play/pause, seek slider over the full span, ⟲ restart). All
state is a pure function of `simT`, recomputed per frame (no event-pointer drift on
seek): turns with `ts ≤ simT` are revealed; a step is **in-flight between its
ASSISTANT turn and its TOOL_RESULTS turn** (animated dashed stratum, "RUNNING" in
tooltip); jobs run between their real `started`/`ended`; the never-ended `running`
job in C stays dashed to the head. LIVE chip: LIVE while playing, PAUSED, ENDED at
`tEnd`. Clicking the rail body seeks to that wall time; clicking a chevron never
disturbs the replay.

## Prompt anchors & jump mechanism

Every `USER_INPUT` and every user-sourced `STEERING` turn becomes a chevron, drawn
**from t=0** (hollow until reached) and never aggregated. Each chevron owns a
13×14 px hit rect (`data-prompt="<turnIndex>"`); click → `jumpTo(i)` → the mock
transcript (all rows pre-rendered from real turn text, dimmed until revealed)
scrolls `tr#i` into center and flashes it, independent of head position
(`jumpLock` suspends head-follow for 1.2 s). Verified in Chrome: clicking
`[data-prompt="228"]` in session A scrolled to the real row
*"ok. all of those suck. we need things that are wildly more compact"* (23:37:06Z)
and set `__viz.lastJump=228`.

## What the real data changed vs the synthetic round

1. **Y axis, the big one.** The anchor's uniform 1-px-per-turn strata erased time.
   Real sessions are mostly idle (A: 11.5 h span for 353 turns; C: 15.1 h / 932).
   Pure time scale compressed activity into unreadable clusters; pure index hid the
   gaps. Shipped: piecewise-linear **gap-compressed** axis with labeled ⏸ bands.
2. **Strata became real durations.** Anchor rows were uniform with a tool-count
   heat; real ASSISTANT→TOOL_RESULTS pairing makes each stratum's *height* the
   step's wall duration (median job 0.1 s, longest step minutes) — irregular turn
   sizes are now the texture, not noise.
3. **Job brackets → concurrency ribbon.** Anchor drew one bracket per job (6 jobs).
   Session C has **238** — individual spans are impossible. Completed jobs aggregate
   into the ribbon; the 27 failures stay individual red ticks; the 1 `running` job
   gets a dashed span.
4. **Delegate brackets → spawn diamonds.** `children[]` carries no timestamps, so
   the anchor's depth-indented lifespan brackets are unrepresentable; honest spawn
   events (41 in C, incl. A's 6-way fan-out in one turn) with task-snippet tooltips.
5. **Steering split into three tiers.** The anchor assumed binary user/daemon
   steering. In this data `steer_src` is **absent on 124 of 125** steering turns
   (the extractor only tagged one) — so only tagged turns earn the amber chevron;
   untagged steering degrades to dim ticks rather than being mislabeled as daemon
   or user.
6. **Fold / model-switch / watch encodings removed, not faked.** No CHECKPOINT,
   SUMMARY, or watch events exist in any session; the anchor's violet fold bars,
   93% fold-threshold tick, and watch-fire columns are simply absent (captioned so).
   The ⟳⚠… verdicts were rewired to real signals (repeat-error streaks, recent job
   failures, idle/in-flight stalls).
7. **ATTENTION_RESOLUTION got a glyph** (64 in C) — the anchor had none.
8. **Leaf session doesn't look broken.** Session B (0 jobs, 0 children) renders
   dense step strata with `∅` placeholders and a caption — absence is drawn, not
   hidden.

## Scaling

- Per-frame work ≈ turns + jobs×H (≈932 + 238×700 ≈ 167 k ops for C) — fine at
  10 fps. Revealed-turn classes are watermarked, not re-toggled.
- Axis pieces: ≤ ~2×#gaps+1 (≤ ~30 here). Prompt markers: ≤ 11 per session here;
  hit-testing is linear over prompts only.
- Known limits: chevrons would need collision stacking beyond ~50 prompts;
  sessions ≫10³ turns would want turn-tick aggregation in the who lane (prompts
  stay individual per the hard requirement).

## Exact footprint

**160 px wide × full viewport height** (`#rail`): SVG map (flex height) + 54 px
footer (context bar, token bar, live dot / turn# / wall clock / ⟳⚠… glyphs).
Transport, session switcher, legend and captions live in page chrome outside the
budget. No scrolling inside the component; the mock transcript column scrolls.

## Verification (Chrome, 1400×900, 100% zoom, one pass)

- Tight `#rail` crops: session A paused at 30 % (`#179/352 22:30:08Z`) vs 62 %
  (`#315/352 02:09:59Z`) — head line and revealed strata advance; live play
  separately observed advancing to ENDED.
- Prompt click test passed (details above).
- Session B at 55 %: `∅` job/delegate lanes, hollow unreached user-steering
  chevron at #372, footer `#204/561`; prompt list `[2,193,358,372]` matches raw
  records exactly.
- Session C at 50 %: concurrency ribbon with red failure ticks, 41 spawn diamonds,
  `idle 6.3h` band, footer `#250/931`; LIVE chip verified `LIVE` while playing.
- Console-message capture in this harness is an unimplemented stub; runtime
  health is instead evidenced by three full session switches, seeks, play/pause
  transitions and DOM assertions all succeeding with zero exceptions.
