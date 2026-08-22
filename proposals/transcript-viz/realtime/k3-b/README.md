# transcript-viz / realtime/k3-b — Causality & Structure, LIVE

Realtime re-interpretations of the three keeper ideas in `../k3-b/`, redesigned as an always-on
companion HUD for a *running* evener session. Each file embeds the same synthetic event stream
(session `evener-8f3k2`: 287 turns, 2,146 tool calls, 3-level delegation, 4 jobs, 3 watches, 2
compactions, one watch-storm → retry-cascade incident) and plays it back with play/pause, speed
(1×/8×/32×/128× = sim-minutes per second), seek slider, and replay. In-flight work pulses amber /
renders hatched; completed history is solid. LIVE chip flips to PAUSED / ENDED. All self-contained
inline CSS/JS, file://-safe.

## RT-1 — Upstream Live: the growing causal graph (`idea-1.html`)

**Realtime question:** *why is the agent doing this exact thing right now — what triggered it?* The
provenance DAG materializes node-by-node as work starts (pulsing halo = in flight), and causal edges
draw themselves the moment one thing causes another — steering→spawn at T63, job-output→watch-fire
at T129, and the satisfying moment the long red `bug written @T44` edge lands on the ROOT CAUSE node
at T147. **Update model:** event stream dispatches node lifecycle (active/done), live status lines,
edge formation, and a live alert rail ("WATCH RUNAWAY — 3 fires/90s" appears at the third fire, not
in hindsight); the WHY-NOW panel walks the current node's upstream chain over edges that exist *so
far*. **Scaling:** the graph accumulates ~21 entities for 287 turns — structure, not turns; completed
nodes freeze solid and dim slightly, so the eye is always drawn to the 1–2 pulsing in-flight nodes.

## RT-2 — Riverbed Live: swimlanes with a playhead (`idea-2.html`)

**Realtime question:** *who is active right now, what just fired, and is a runaway developing?* The
time axis extends only a little past NOW (the future doesn't exist); lanes fade in as actors join
(dlg_store at spawn, job lanes on start, WATCHES on first fire), density currents accumulate
bucket-by-bucket, running jobs are hatched bars that grow rightward until their exit event, and watch
fires drop triangles with dotted arcs to the handler. The CONTEXT lane climbs in real time, and when
it crosses threshold the fold band lands with a flash. A NOW panel pins the current action and flips
to a blinking red alert during the storm/cascade. **Update model:** per-frame reposition of every
time-anchored element against a growing viewport; seek/replay rebuild state by replaying the stream
quietly. **Scaling:** rendering cost is O(elapsed minutes × lanes), never O(turns) — 2,146 calls are
density, not rows; at 128× the full 6h12m session plays in ~3 s and stays legible.

## RT-3 — Case File Live: the ledger writes itself (`idea-3.html`)

**Realtime question:** *what episode is in flight, what did it just cause, and is the session still
healthy?* Episodes open as amber-pulsing cards with a live action line (elapsed, ~calls so far,
current tool); outcome panels read "pending — episode in flight…" until the seal event flips them
✓/✗. Causal link pills flash into existence when their trigger lands; the blast-radius heatmap cell
flashes on every edit and failure rings appear as they happen; the live alert strip badges anomalies
and their resolutions. Parallel work is honest: dlg_tests and the background-jobs episode are open
simultaneously. **Update model:** open/seal/link/alert events mutate card state; only the small live
line ticks per frame so animations aren't disturbed. **Scaling:** sealed episodes auto-collapse into
one-line custody rows once two newer episodes exist (click to re-open, double-click to re-collapse) —
at T287 the ledger is ~11 compact rows plus the 2 newest cards, a one-screen session regardless of
length.

---
Verified in Chrome: animation advances (start vs. mid-session vs. end screenshots differ
meaningfully), transport controls work, no console errors. Old retrospective versions in
`../k3-b/` untouched.
