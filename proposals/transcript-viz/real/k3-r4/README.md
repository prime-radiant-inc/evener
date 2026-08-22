# R4 · Health Strata Rail — real-data round

**File:** `index.html` (self-contained, file://, ~420 KB — all three real sessions embedded inline, unmodified)
**Form factor:** vertical rail, **148 px × full height** (inside the ≤160 px budget). Transport, session switcher, and legend live in the page chrome, outside the footprint.

## The comprehension question

> **"Is this session healthy RIGHT NOW — and if not, where did it hurt?"**

The rail is a health monitor, not a map. Its primary content is anomaly and verdict
encoding; the session's shape is only the substrate anomalies are painted on.

## Encoding

**Verdict head (top of rail).** A 26 px glyph + one-word verdict computed *only from
turns whose real timestamp ≤ replay head* (no peeking):

| verdict | meaning |
|---|---|
| `● HEALTHY` | no active signal |
| `⌛ STALLED` (pulsing) | we are *inside* a real >20 min wall-clock gap right now; reason line counts up live (`idle 2.1h…`) |
| `✕ HURT` | an errored tool call within the last 12 seen turns |
| `⟳ LOOPING` | the trailing run of identical-signature assistant turns is ≥3 right now |
| `◈ CHURNING` | ≥4 ATTENTION_RESOLUTION turns in the last 40 seen turns |
| `▲ STRAINED` | health score < 70 from accumulated damage |

Below it: two reason lines with concrete anchors (`✕ web_fetch #147 · ×2`,
`⌛ worst stall 4.2h @#316`, `⟳ edit_file ×30 @#434`), and an in-flight line naming the
head turn and its tool histogram.

**Turn strata (main body, x 22–147).** One hairline row per stream turn (y = turn
index). Baseline is deliberately quiet (dark slate; blue = USER_INPUT, amber =
user-steering, violet wash = ATTENTION_RESOLUTION). Anomalies are loud:

- **red wash + red ◆** — turn containing a real errored call (`calls[].e`)
- **amber wash** — member of a repetition run (same `(tool, target[:40])` signature
  in ≥3 consecutive assistant turns)
- **full-width amber band + notch** — the turn that *ends* a real >20 min wall-clock gap
- **white head line + arrow**, animated dashed blue outline on the in-flight turn

**Wall-clock strip (x 12–21).** The same session on the honest time axis: teal ticks =
activity, **voids = real idle gaps** (the 4.2 h / 6.3 h holes are immediately visible),
amber fill = recorded stall regions, a growing amber band while a stall is in progress,
and job intervals overlaid (green done / **red failed** / gray cancelled / amber running).
Session B has zero jobs — the strip simply shows activity ticks and its two stalls; a
leaf does not look broken.

**Anomaly pills (bottom).** `✕ errors · ⟳ loops · ⌛ stalls · ◈ churn` with live counts.
Clicking a pill cycles the transcript through that anomaly's locations ("next hit").

**Health bar (foot).** `hp = 100 − min(6·errors,48) − min(4·stalls,32) − min(3·loops,40)
− min(3·failedJobs,24)`, green → amber → red. Caps keep long sessions comparable instead
of saturating at zero.

## Update model (realtime)

Replay runs on **real timestamps**: `simT` advances in wall-clock seconds (×60 / ×600 /
×3600; a 15 h session replays in ~90 s at ×600). Each 100 ms tick: `upto = #{ts ≤ T0+simT}`;
the whole health state is a **pure function of the seen prefix** — recomputed every tick,
so seeking is trivially consistent and nothing can peek ahead. A stall is *recorded* only
when the turn ending it arrives; while the head sits inside a gap, the verdict flips to
STALLED with a live-counting elapsed timer. Transcript and strata re-render only when
`upto` changes; the verdict head updates every tick. LIVE / PAUSED / ENDED chip in the
chrome mirrors transport state.

## Scaling

932 turns (session C) render as sub-pixel strata — hairlines merge into texture while
anomaly overlays (2 px bands, 3.4 px diamonds, 9 px-min chevrons) stay legible, which is
exactly the intent: health signal survives compression, baseline does not need to.
Per-tick cost is O(N) prefix scan + one SVG rebuild on turn change; fine at this scale.
The honest wall-clock strip is what keeps a 15 h / 238-job session readable: most of the
height is void, and that void *is* the information. Beyond ~5–10 k turns the per-tick
prefix scan would want incremental accumulation; the SVG itself (one node per turn)
scales to ~10 k nodes before you'd bin rows.

## Prompt anchors

Every `USER_INPUT` and every user-sourced `STEERING` (`steer_src === "user"`) is a
permanent ≥9 px chevron on the rail's left gutter (blue / amber), drawn last, never
collapsed: **8** in A, **4** in B (3 prompts + the user steering @#372), **11** in C.
Chevrons are dimmed until the replay head reaches them; clicking a reached one scrolls
the mock transcript to that exact row (centered + flash + white selection ring on the
rail); clicking an unreached one seeks the replay to its timestamp first. Verified in
Chrome: clicked `rect.prompt-hit[data-pi="317"]` on session A → row #317
("the micro strata rail is closest…") centered at viewport y=390 with flash.

## What real data changed vs the synthetic rounds

1. **Time became load-bearing.** Synthetic rounds spread events uniformly over sim-time;
   real timestamps are clumped around multi-hour voids. The wall-clock strip and the
   STALLED verdict exist because of this — synthetic data never produced a live stall.
2. **The fold lane died.** No CHECKPOINT/SUMMARY turns exist in any session, so the
   anchor design's fold bars and context micro-bar were removed rather than faked.
3. **Watch-runaway detection died** (`watches` empty everywhere); repetition detection on
   real call signatures replaced it — and immediately paid off (below).
4. **Verdicts got humbler.** The synthetic verdicts fired on scripted beats; the real
   detector needed caps and recency windows or everything long degraded to noise.
5. **Steering lost its blue chips:** 125 of 126 steering turns across all three sessions
   are daemon-sourced, so daemon steering shrank to a 4 px tick and only *user* steering
   earns a chevron.

## Anomalies the detector actually surfaces (per session)

**A · k3 orchestrator (353 t, 11.5 h)** — final: STRAINED, hp 44
- ✕ 2 tool errors: `write_file` @#50, `web_fetch` @#147
- ⌛ 8 stalls >20 m; worst **4.2 h** before #316 (also 76 m @#57)
- ⟳ 4 repetition runs: `communicate` ×6 @#37, ×4 @#185, ×4 @#247; `shell` ×3 @#285
- ◈ 22 attention-resolutions · ▪ 2 cancelled jobs (stopped_by_parent)
- Live proof: at +9:10:00 the verdict reads **STALLED — idle 2.1 h**, hp 48, *before* the
  4.2 h stall is recorded (causality check passed)

**B · k3 leaf worker (562 t, 5.5 h)** — final: STRAINED, hp 52
- ✕ 0 errors — the rail correctly never cries wolf
- ⟳ **32 repetition runs**: worst `edit_file` **×30** @#434, `edit_file` ×7 @#478,
  `use_browser` ×7 @#312 — the edit→verify churn is the dominant texture of the strata
- ⌛ 2 stalls: **1.9 h** before #192, 48 m before #357
- 0 jobs / 0 children / 0 attention events — leaf renders clean, not broken

**C · gpt-5.6-luna big-tree (932 t, 15 h)** — final: CHURNING, hp 0
- ✕ 10 tool errors: `read_transcript` @#60/#101, `ask_user` @#66/#143/#145,
  `exec_command` @#107, `delegate` @#240/#306, `task_list` @#500, `grep_files` @#590
- ⟳ 7 runs incl. the **`ask_user` ×3 loop @#147 that contains two of the errors** and
  `read_transcript` ×4 @#105, `job_stop` ×3 @#639
- ⌛ 6 stalls; worst **6.3 h** before #251 (at 62 % of wall time only 251/932 turns have
  happened — the honest strip shows this instantly)
- ◈ 64 attention-resolutions with a churn burst @#288–299 · ▪ **27 failed jobs**
  (red ticks on the time strip), 2 cancelled, 1 still running at end

## Verification evidence (Chrome, file://, 1280×800 @100%)

- Before/after: `v1-before-rail.png` (HEALTHY, hp 100, #19/353) → `v2-after-rail.png` /
  `v2-after-full.png` (STALLED, hp 48, counters live) — plus LIVE replay shot at +12:00.
- Prompt click: `012-click.png` (flash + tooltip on #317), DOM-verified centered.
- Session switcher: `v5-sessC-stalled.png` (C inside the 6.3 h gap, hp 21),
  `v6-sessB-ended.png` (B's amber loop texture, hp 52).
