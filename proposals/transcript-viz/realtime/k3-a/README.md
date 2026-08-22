# Realtime transcript visualizations — proposals/transcript-viz/realtime/k3-a

The earlier `proposals/transcript-viz/k3-a/` set was retrospective (archive browsing of a
finished session). This set re-works the same three visual ideas as **live HUDs for a
running session**: an always-on companion that updates incrementally as the event stream
arrives. All three embed the same deterministic synthetic realtime simulation of session
`0J8XKM` (278 turns, ~1,677 tool calls, 5 delegates over 3 levels, 6 jobs, 2 watches incl.
a 47-fire runaway, 3 compaction folds, a build-break → `edit_file` retry loop → retried
provider-529 failure, 1.19M tokens, 6h42m of compressed wall clock).

**Update model (all three):** a precomputed event log (`turn_start/end`, `tool_start/end`,
`dlg_spawn/report`, `job_start/end`, `watch_fire`, `fold`, `model_switch`) is replayed on a
simulated clock into a `world` state; every 100ms the UI re-renders from `world`. Transport:
play/pause, 1×/4×/16× speed, restart, and a seek slider that rebuilds state from the event
prefix. LIVE chip shows LIVE / PAUSED / ENDED. In-flight work (running turn, running tool
calls, active delegates, running jobs) is drawn with animated dashed/hatched treatment —
animation only ever means "this is happening right now", never decoration. Completed history
is static. Open from `file://`, no network.

---

## RT-1 — Live Strata HUD (`idea-1.html`)

**Realtime question:** "What is the agent doing right now, what just changed, and is
anything looping or about to fold?" The owner's minimap as a live HUD: the five semantic
lanes (WHO / TOOL DENSITY / DELEGATES / JOBS / EVENTS) fill in stratum-by-stratum as turns
complete, with a white head marker at the leading edge; the right rail carries a NOW card
(current action, current tool with elapsed seconds), context and token-budget gauges with a
fold threshold mark, live delegate/job/watch boards, an alert stack, and a "just changed"
event feed; the center shows the live transcript tail with a streaming caret and
in-flight tool chips. **Update model:** incremental — each event adds strata/extends
spans/moves the head; past strata never change, so the reading "fault lines stay put, the
present animates" holds. **Scaling:** at 278 turns the map is ~2px/stratum and still
separates folds, delegates, and the runaway barcode; longer sessions only shrink stratum
height, and the NOW/rail panels are length-independent.

## RT-2 — Live Mission Timeline (`idea-2.html`)

**Realtime question:** "What is running in parallel right now, and how long has it been
running?" A growing wall-clock swimlane trace: the x-axis extends as the session ages
(minimum 20 wall-minutes), a white NOW cursor rides the leading edge, the context-pressure
area band climbs live and drops off a cliff the moment a fold fires, delegate and job bars
lengthen in real time (dashed-animated while active, solid when done, red on failure), and
watch lanes accumulate fire ticks — the watch_2 runaway visibly packs into a dense barcode
as it happens. **Update model:** append-and-extend — completed blocks are immutable, only
the rightmost (in-flight) block per lane grows; the axis rescales when the session outgrows
it. **Scaling:** lane count grows with actors, not turns; at full length the 278 ROOT turn
blocks read as texture with anomalies (red diamonds, violet folds, model switch) still
standing out, and the status rail stays O(actors).

## RT-3 — Vitals & Verdict (`idea-3.html`)

**Realtime question:** "Is the session healthy right now — and how long until the next fold
or the budget wall?" An ops-console take: a NOW card with a big in-turn timer; four always-
visible **verdicts** (loop detector, watch-runaway detector, stall check, jobs healthy) that
flip OK → warn → bad with a one-line reason; accumulating throughput strip charts
(tokens/min, tools/min, cumulative errors) that make the breakage episode visible as a dip
plus a step; context and budget gauges with live **rate-based projections** ("next fold in
~N wall-min", "budget wall in ~N wall-min"); and a one-lane session strip (the whole
session-so-far as a density barcode with fold/error ticks and a head marker) that is
clickable to pin any past moment. **Update model:** world state plus a once-per-sim-second
sample buffer feeding the sparklines; verdicts recompute from sliding windows (8 watch fires
/ 5 sim-s, 3 consecutive same-tool failures, turn open > 8 sim-s). **Scaling:** every panel
is fixed-size — charts compress horizontally, the strip is one pixel-column per turn, and
verdicts are O(1) — so this is the idea that degrades least at thousands of turns, at the
price of the least per-turn detail.

---

### Verification evidence (Chrome, file://)

Each file was screenshotted at start and after simulated time; the frame pairs differ
meaningfully (e.g. RT-1: 0:12/12 turns vs 6:42/278 turns ENDED with full strata + alert
stack; RT-2: 0:11 NOW-cursor frame vs full 6:42 trace; RT-3: all-green verdicts vs runaway
amber + jobs red + 3-error step chart). RT-3's stall verdict was verified by seeking into
turn #206 ("turn #206 open 12s — long think or hang"). Bugs found and fixed during
verification: RNG drift dropping `edit_file` from the error turns (forced into the tool
list), runaway alert re-firing (latched once per watch), RT-2 canvas empty (helper created
SVG nodes but never appended them).
