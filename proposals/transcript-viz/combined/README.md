# Combined Rail — True-Time × Token Seismograph (convergence build, LIVE-faithful replay)

One self-contained `file://` page: `index.html` (450 KB, no network, inline CSS/JS, all three
real sessions from `proposals/transcript-viz/realdata/*.json` embedded verbatim).
Convergence of the three mockups the owner picked from. **Replay is live-faithful: at any replay
instant T the page shows exactly what a live observer at T could know — zero retrospective ink.**

| Reused from | What came over |
|---|---|
| `real/gpt-r3/idea-1.html` (True-Time Rail) | true-time y-axis + turn-index toggle; ASSISTANT→TOOL_RESULTS in-flight reconstruction; interval-partitioned job micro-lanes; fanned prompt-anchor diamonds with leaders; transcript row model; chrome transport; binary-search time↔index helpers |
| `real/gpt-r4/idea-1.html` (Token Seismograph) | per-turn log-width IN/OUT token texture; cumulative-burn step line; result-size cliffs; ranked top-cost diamonds; burn/result readouts |
| `real/k3-r3/idea-1.html` (Multi-Session Rail) | shared normalized clock with aligned now-lines; hatched idle voids and post-END voids; per-rail LIVE/ENDED chips; click-to-focus interaction; jobs that grow while running |
| `realdata/README.md` | honest-absence rules: no compactions/watches in this data → none can ever appear; session B is a leaf (stays quiet, never looks broken); `steer_src` honored |

## LIVE-faithful replay semantics (owner correction, implemented)

Retrospective replay is gone. The rules, and exactly how each is implemented:

1. **Sessions start blank.** At T=0 the rail is empty except the axis (verified: 0 non-background
   pixels, 0 anchors, burn 0 — only the T=0 HOOK_COMPLETED row exists, which a live observer at T=0
   does know). Nothing pre-drawn: no dim-future strata, no dim full burn path, no pre-known anchors
   or idle gaps. Everything appears only when its real timestamp arrives.
2. **No future knowledge in readouts.** Rail head: `104,249 tokens so far`, `results 20 KiB so far` —
   no denominators anywhere (verified: DOM contains no `813,765` before replay end; at the end the
   so-far number *is* the final number, which is then legitimately known). Turn counter is
   `N records so far`; clock is `HH:MM:SS UTC · +H:MM elapsed`; honesty line counts jobs/delegate
   calls *started so far*; Mode 2 foots show `#N turns · Σ burn · jobs live · prompts` — all so-far.
   The in-flight footer no longer names the next record (that would be foreknowledge).
3. **Dynamic axis (the crux).** A live tool cannot know the final span, so the true-time axis covers
   `[start, now]` with a 10-minute minimum window (`MIN_WINDOW`) and re-scales *continuously* as now
   advances — you watch the rail compress while a session runs long (verified: span 2.17h → 10h
   mid→late; a fixed event's y-fraction moves 0.027 → 0.006). During the first 10 minutes the
   now-line descends through the min window; after that it pins to the bottom edge and all history
   compresses upward. Turn-index mode normalizes by turns-so-far the same way. Axis tick labels are
   round absolute hours (19:04, 20:00, …) so labels never jump while the scale moves.
   The continuous policy was chosen over discrete (span-doubling) steps because the redraw loop
   already runs every frame; the smooth compression *is* the "what it really looks like" effect.
4. **Σ burn line auto-scales on x** (same live-chart policy on the horizontal): x is normalized to
   burn-*so-far*, so the line's head always touches the right edge of the strata band and past
   segments re-normalize leftward as burn accrues. Token-texture widths and result-cliff widths use
   running maxima-so-far. Top-5 cost ranks are ranks *so far*.
5. **Idle voids accrue.** A gap is not drawn from a precomputed gap list. When the silence since the
   last revealed event has *actually lasted* ≥10 min (`GAP_MIN`), hatch starts at that last event
   and grows down to the now-line with a live `Hh MMm idle` label; it freezes when the next event
   lands. END caps (Mode 2) appear only when replay passes the session's real end, with the
   post-END hatched void below.
6. **Jobs and intervals grow.** Jobs appear at their real start (bright dashed outline while
   running), lengthen until their real end, then freeze colored by outcome (outcome is unknowable
   before the end). In-flight tool strips do the same between an ASSISTANT turn and its
   TOOL_RESULTS, with a dashed live box over the strata.
7. **Transcript grows.** Only rows ≤ now exist (appended as they happen; removed on backward seek —
   seek is time travel, but nothing beyond now ever renders). No dim future rows. The viewport
   thumb sizes against existing rows only; drag/click math uses the live axis mapping.
8. **Mode 2 ordering is live.** Parent (A `034B9Kg7` — B's `parent` field) is always leftmost.
   Remaining rails sort by **most recent activity as of replay-now** (last revealed event's elapsed
   time), re-sorting with a FLIP animation when the order flips. Because the raw recency ordering
   flips 27 times in this data (early flicker), a **60 s hysteresis** (`REORDER_MARGIN_S`) requires
   the challenger to be clearly more recent before a swap — documented here because it is a
   deliberate deviation from a strict sort. Verified: T+1h → `[A,B,C]` (B active 4 min ago, C idle
   43 min), T+5.83h → `[A,C,B]` (C active 16 min ago, B ended).
9. **Transport unchanged in spirit.** Play/pause, 60–21,600×, restart, seek slider (time travel over
   the full real domain — transport chrome, not rail ink), Space shortcut, ESC exits Mode 2.

## The combined encoding (one 156px rail)

Left→right: live UTC/turn axis (28px) · fanned prompt ◆ anchors (DOM buttons, appear at their
instant) · **token strata band**: per-ASSISTANT-turn log-width IN (cyan) + OUT (amber) bars, with
the **white Σ cumulative-burn step line overlaid in the same band** and red ranked diamonds for the
costliest turns-so-far riding it · red result-size cliffs · in-flight strip + dashed live box ·
interval-partitioned job lanes · red × error ticks · violet delegate-spawn dots. Accruing idle
voids are hatched (true-time axes only). The white hairline is the live edge; rail head shows
burn/results so far; the foot shows in-flight tools, running jobs, jump readout, and the
so-far honesty line. Merging the burn line *into* the strata band is what lets the whole
seismograph fit the 156px budget.

## Mode 1 — the rail IS the transcript's scrollbar

The growing transcript has no native scrollbar — the rail replaces it: translucent **viewport
thumb** synced to scroll (sized against existing rows, mapped through the live axis); **drag the
thumb** to scroll (verified 1:1 in turn mode); **click anywhere** to jump the viewport there;
wheel over the rail scrolls. Prompt ◆ anchors and anomaly marks (top-cost diamonds, error ×) are
clickable to their exact revealed turns (row centered + flashed; replay clock untouched). Transcript
follows LIVE (FOLLOW LIVE toggle); any manual scroll/drag/jump disables follow.

## Mode 2 — comprehension view (multi-session, shared live clock)

`⤢ COMPREHENSION` / ESC. All three real sessions side-by-side on **one shared live clock**
(y = elapsed-since-start over `[0, now]`, 10-min minimum window; all rails re-scale together so
now-lines stay aligned by construction — verified 0px spread). Each rail is the Mode-1 combined
rail at its exact 156px width (owner constraint: no stretching; many rails overflow horizontally
with scrolling). Rail order: **parent leftmost, then most-recent-activity as of replay-now**
(60 s hysteresis, FLIP-animated re-sorts). END caps + ENDED chips appear only as each session's
real end passes. Click a rail's prompt ◆ / anomaly / any point → exits to Mode 1 focused on that
session+turn (replay parks at that exact real timestamp, row centered + flashed); rail-head click
exits focused on the session only.

## Prompt-jump mechanism

Anchors are DOM `<button>`s added to a fan-lane layout (≥11px separation, leader line to the exact
timestamp) only once their event's real instant arrives (verified: 1 → 2 anchors across prompt
#58's timestamp). Click → `jumpToStep(i)`: centers fixed-height row `#step-<i>`, flashes it, sets
the rail's jump readout + `data-last-jump`. Anomaly clicks hit-test the canvas (≤9px to a
top-cost-so-far diamond or error tick) and take the same path. In Mode 2 the same call first exits
the overlay and switches session (`focusFromComp`).

## Verification (Chrome, 1440×900 @100% — all passed)

1. **T=0 blank**: 0 non-background pixels in 360 canvas samples, 0 anchors, burn 0, 1 row (the T=0 hook).
2. **No future ink**: at +5:00 (10-min min window), nowY = 316 = exactly H/2; 0 non-background
   pixels in 1872 samples below the now-line.
3. **Axis re-scale**: span 2.17h → 10.0h between +2h10m and +10h; fixed event #16 y-fraction
   0.027 → 0.006; mid/late rail crops captured.
4. **No final totals**: `813,765` absent from the DOM before replay end (present only at END, where
   it is the current so-far value); all readouts show "so far".
5. **Mode 2 live order**: T+1h → `[A,B,C]`; T+5.83h → `[A,C,B]`; now-line spread 0px; widths 154px fixed.
6. **Scrollbar**: thumb drag ΔscrollTop 2023.5 vs axis-math 2023.7 (ratio 1.000, turn mode, live
   denominators); anchors appear only at/after their turn (1 → 2 across #58); backward seek shrinks
   the transcript (353 → 41 rows); replay reaches ENDED with full history only at the real end.

## Honest limitations

- **Continuous re-scaling means ink never sits still** while playing — that is the point (it is what
  live looks like), but pausing is the way to read a stable rail. Tick labels use round hours so the
  labels themselves don't jump.
- Token mass is log-scaled against running maxima-so-far, and burn x against burn-so-far: widths
  compare turns within the current view, not absolute cost across sessions or across time.
- Cache-read vs fresh-token split is not in the dataset; burn is total tokens only.
- `steer_src` is almost never populated → nearly all steering is daemon-classified (no anchors).
- Children/jobs have no per-event linkage (children[] lack timestamps) → delegate spawns are dots at
  the call turn, not edges to child rails; "children so far" is approximated by `delegate` calls.
- No compaction/watch glyphs exist to draw; encodings intentionally degrade to absence.
- Thumb mapping in true-time mode is nonlinear in scrollTop by design (time ≠ turn density); in turn
  mode it is exactly linear (verified 1:1).
- The seek slider's domain is the full real span — it is the operator's time-travel control
  (transport chrome), never rail ink; nothing beyond now renders regardless of where you seek.
- Transcript rows are fixed 29px, single-line elided — a minimap host, not a reader.
