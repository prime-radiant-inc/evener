# REAL·K3-R2 — Prompt-Spine Rail

**File:** `idea-1.html` — self-contained `file://` page, inline CSS/JS, all three real
sessions from `proposals/transcript-viz/realdata/*.json` embedded verbatim (422 KB total).
Session switcher (A/B/C) in the page chrome. No synthetic events anywhere.

Variation of the **Micro Strata Rail** anchor (`compact/k3-a/idea-1.html`): the organizing
structure is **what the human asked**, not time. The owner's core navigation need —
*"get to any user prompt step"* — is the spine of the component.

## Comprehension question

> *"What did the human ask for, in order — and what did the agent actually do about each
> ask, and which ask is it working on right now?"*

Every user prompt and user steering turn is a permanent, labeled node on a vertical spine;
the agent's work hangs beneath its prompt as dense strata. The session reads as a list of
**asks → responses**, not as an undifferentiated event stream.

## Encoding

- **Spine nodes (the anchors).** One per `USER_INPUT` turn and per `STEERING` turn with
  `steer_src === "user"` (8 / 4 / 11 nodes in sessions A / B / C — all real). Blue chevron
  = user prompt, amber chevron = user steering. Each node shows a meta line
  (`P3 · PROMPT · 14:56:41 · 24t · 11m`) and a **2-line real prompt-text preview**
  (full text in the hover tooltip). Nodes never collapse and are visible from t=0
  (future nodes ghosted at 42% opacity — the roadmap of the session is visible before
  the replay reaches it).
- **Sections.** The rail height is partitioned into one section per node, spanning from
  that prompt to the next. Section height ∝ its real turn count, so long answers get
  long sections. The section containing the replay head is tinted blue with a left accent
  bar and a pulsing ring on its node — **the current ask is always visible**.
- **Work strata (per section, hanging under the node).** One row per turn, uniform scale
  (0.15–1 px, adaptive): a 4 px kind-color tick (teal assistant, dim-teal results,
  dim-amber daemon steering, gray env/hook/attention) plus a **work bar whose width is
  log-scaled real `out` tokens** (assistant) or log `res_bytes` (results). Reads as a heat
  strip: wide = the agent said/did a lot here.
- **Delegate lane (violet).** Spawn diamonds at real `delegate` tool-call turns; brackets
  close at the real `<delegate-notification> "kind":"reported"` steering turn (FIFO
  pairing — the extract's `children[]` carries no timestamps; noted honestly in the
  tooltip/README). Dashed-animated while open.
- **Job lane (green).** Real `jobs[].started/ended` wall-clock brackets. Solid =
  completed, red + ✕ = failed (27 real failures in session C), amber = cancelled,
  dashed-animated = still running (incl. the one job that was `running` at extraction —
  its bracket simply never closes).
- **Event lane.** Red ◆ on real errored tool calls (`calls[].e`); gray ticks for
  `ATTENTION_RESOLUTION`.
- **Idle gaps.** Real timestamp gaps ≥ 2 min render as hatched **gap bands** with the real
  duration (`idle 4.2h`); band height ∝ log₁₀(duration), so bigger silences are visibly
  bigger without eating the rail. The adaptive threshold (120 s → 300 → 900 → 2700 s)
  raises only as needed to fit — smallest gaps collapse first, the big ones always show.
- **Head.** Full-width white line + right arrow + wall-clock tag, on the exact current
  turn; it **interpolates through gap bands in real time**, so during a 4.2 h idle the
  head visibly crawls through the hatched band (the "…" stall glyph lights amber).
- **In-flight reconstruction.** Between an `ASSISTANT` turn and its `TOOL_RESULTS`, the
  turn's bar renders as an animated dashed blue overlay with the running tool's name —
  you can see a tool call *running*, not just completed history.
- **Footer (inside the rail).** Two micro-bars: cumulative **real** tokens in (blue) and
  out (teal), normalized to session totals; live dot; `#turn/N`; `Pk/Pn` (current prompt
  of total); verdict glyphs `⟳` (same tool erroring consecutively), `⚠` (any real failure
  seen so far), `…` (head inside a real idle gap ≥ 5 min).

## Update model

World state is a **pure function of one wall-clock cursor `simT`** — no event queue, no
accumulated mutation. `headIdx = last turn with ts ≤ simT` (binary search); jobs and
delegate brackets derive open/closed from their real timestamps vs `simT`; the token
micro-bars read precomputed cumulative arrays at `headIdx`. A 100 ms tick advances
`simT += 0.1 × speed` (1× / 60× / 600× / 3600× — 600× crosses the 11.5 h session in
~69 s). Turn-boundary crossings trigger the static layer (strata/nodes/closed brackets)
and transcript append; the dynamic layer (head line, open brackets, in-flight overlay,
footer, transport) re-renders every tick. Seek/switch/jump all just set `simT` and
re-derive — **scrubbing backwards is exact**, because nothing was ever applied
incrementally. LIVE → PAUSED → ENDED chip reflects `playing` and `simT ≥ T1`.

## Scaling (with the real numbers)

- **Turns:** row scale is solved per session to fit exactly — no scrolling inside the
  component, ever. A: 353 turns → ~0.9 px/turn (crisp rows). C: 932 turns → 0.36 px/turn
  (rows anti-alias into a continuous heat strip — the right degradation at density).
- **Prompts:** node block is 35 px (≤5 prompts) → 31 px (≤9) → 27 px (≥10, session C).
  Beyond ~20 prompts the honest fallback would be collapsing preview to the meta line
  (17 px) — not needed by any real session here.
- **Gaps:** threshold adapts (above). Session C fits 932 turns + 11 nodes + 8 shown gap
  bands in ~750 px at scale 0.36.
- **Absences degrade gracefully:** session B (leaf worker) renders zero jobs / zero
  delegates as simply quiet lanes — guides remain, nothing errors, nothing looks broken.
- **No compactions / no watches in any session:** the violet fold bars and watch ticks of
  the anchor design are simply absent (the encodings exist in `KC` and would render if
  `CHECKPOINT`/`SUMMARY` turns ever appeared). No fake folds were invented.
- **Cost:** ≤ ~2.5 k SVG nodes at worst (session C mid-replay); static layer re-renders
  only on turn-boundary, so 10 fps ticking is cheap. Transcript appends incrementally
  (`insertAdjacentHTML`), full rebuild only on seek/switch.

## Exact footprint

**160 px wide × full stage height** (viewport − 37 px chrome − 24 px caption; 863 px at
1440×900). That includes the in-component footer (2 micro-bars + status row ≈ 92 px).
Transport (play/pause, speed, restart, seek, clock), session switcher, and LIVE chip live
in the page chrome **outside** the budget, as does the legend/caption strip. The mock
transcript beside the rail is dimmed (opacity .66) and scrolls independently; the rail
itself never scrolls.

## Prompt-jump mechanism (the point of this variation)

Every spine node carries a full-width transparent hit rect (`data-anchor=<turnIndex>`).
One click:

1. sets `simT = ts[anchor]` — the replay **seeks on the real timeline** to that prompt
   (exact, because state is a pure function of `simT`);
2. re-derives the world, rebuilds the transcript up to exactly that turn;
3. `scrollIntoView({block:'center'})` the transcript row `#tr<anchor>` and flashes it;
4. outlines the node white (selection) and highlights its section as current.

Verified in Chrome (1440×900, 100% zoom): clicking P3 (`data-anchor=106`) produced
`selAnchor=106, headIdx=106, simT==ts[106]` exactly, row `#tr106` in viewport
(y=795 px inside the transcript box) with the flash row visible in the click-moment
screenshot, real prompt text ("hang onto all those prototypes, maybe let's put them on
the github wiki…") centered in the dimmed transcript. Works while playing (transcript
resumes live-tail following) and while paused (stable for inspection). Follow-mode: the
transcript auto-follows the tail only while the user hasn't scrolled up; any jump/seek
re-centers explicitly.

## What real data changed vs the synthetic anchor

- **Prompt count is small and text is real** (8/4/11 prompts, ≤ 400 chars) — this is what
  makes a *prompt spine* viable at all; synthetic rounds had no real text to preview.
  Node blocks with genuine previews replaced the anchor's bare chevrons.
- **Timestamps are wall-clock with multi-hour holes.** The synthetic sim had uniform
  1–2 s turns; real gaps forced the hatched gap-band encoding, the adaptive threshold,
  gap-interpolating head, and speed up to 3600× (1×/4×/16× is meaningless on 15 h of
  real time).
- **`steer_src` is mostly absent** (only ONE user-steering in the whole corpus, session B
  turn 372). Daemon nudges are sub-classified by real text prefix (sys·reminder /
  dlg·notify / job·notify / img·desc) and demoted to dim strata ticks instead of
  pretending to be user input. The spine stays honest: only genuine human turns.
- **Tool-call structure differs per harness.** Session C (gpt-5.6-luna) has zero
  assistant text and a different tool vocabulary (`exec_command`, `grep_files`,
  `ask_user`), so assistant rows/bars derive from `calls` + real token counts, never from
  assumed text. Work-bar heat had to use `out` tokens (call counts are ~always 1).
- **Jobs have real start/end and honest statuses** — including one job still `running`
  at extraction (renders as a never-closing bracket) and 2 `cancelled` (amber).
- **Children carry no timing**, so delegate brackets pair real `delegate` calls with real
  `"reported"` notifications FIFO — documented, not hidden.
- **No compactions, no watches, one all-quiet leaf session** — all handled by absence,
  not by invention.
- **Errors are rare and isolated** (2/0/10 errored calls) — so `⚠` is a seen-so-far
  history glyph rather than the anchor's synthetic three-error loop drama.

## Verification evidence (Chrome, file://, 1440×900 @100%, one pass)

- Zero JS errors (`node --check` on the extracted script; all runtime evals clean).
- **Before/after animation proof:** `proof-before.png` (LIVE, #315/353, +8:13:00) →
  `proof-after.png` (ENDED, #352/353, +11:27:01/11:27:01) — head, strata, footer all
  advanced on the real timeline.
- **Tight crops:** `crop-rail.png` (A), `crop-rail-C.png` (932-turn density, failed-job
  brackets, 6.3 h gap band), `crop-rail-B.png` (leaf session, quiet lanes, 4 anchors).
- **Click test:** assertions above + `008-click.png` (flashed row #106 + tooltip with
  full real prompt text).
- Chrome capture dir:
  `/Users/jesse/Library/Caches/superpowers/browser/2026-08-22/session-1787380558296/`
