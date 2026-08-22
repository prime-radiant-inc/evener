# transcript-viz / compact/k3-b — Causality & Structure, COMPACT + LIVE

Round-3 redesign under hard pixel budgets: encodings native to tiny always-on surfaces, not shrunken
dashboards. Each page embeds the same synthetic realtime event stream (session `evener-8f3k2`: 287
turns, ~2,146 tool calls, 3-level delegation, 4 jobs, 3 watches, 2 compactions, **7 user prompts**,
a watch-storm → retry-cascade incident) with transport (play/pause, 1×/8×/32×/128×, seek, replay,
LIVE chip) in page chrome **outside** the component's budget. Every component sits beside a dimmed,
live-appending mock transcript. Earlier rounds in `../../k3-b/` and `../../realtime/k3-b/` are
untouched.

**User-prompt guarantee (all three):** every user_input/steering turn gets a permanent marker inside
the component (U1 goal · U2 in-place-rewrite · U3 tombstones steering · U4 keep-fuzz-detached · U5
find-root-cause · U6 update-docs · U7 wrap-up). Markers live on dedicated fixed lanes/rows that are
exempt from all compression and never scroll; hovering shows the full prompt text and turn; clicking
calls `scrollIntoView` on the matching `u-prompt-N` block in the mock transcript and flashes it. One
click reaches any prompt, at any session length.

## RT-1 — Trigger Rail (`idea-1.html`) — vertical rail, 160px × 100% viewport height

**Realtime question:** *what is the session doing now, what chain of causes led here, and is
something running away?* **Encoding:** a vertical spine of episode rows (dot = type: violet
delegate, blue steering/jobs, orange ▲ watch, red ▪ cascade, purple fold) — pulsing amber = in
flight; non-adjacent causality draws as left-edge brackets (red `caused failure`, teal `reported`,
blue `steered`, green `gate`) that land live (E2→E8 "bug @T44", U5→E8 user pivot); a right-edge
mercury gauge is context pressure (green→amber→red, purple nicks at folds); a mini-tray shows
running jobs/watches; the bottom zone is the current action. **Update model:** event stream opens /
seals rows; the signature move — when a compaction fires, every row above it collapses to a 5px
sliver, so the rail literally compacts like the context did. **Scaling:** 287 turns → 20 rows; rows
compress behind folds; even 10× sessions fit one viewport. **Prompt markers:** green ◆ U-rows in the
spine flow, exempt from slivering — U1–U7 always clickable. **Footprint:** 160px wide × full
viewport height.

## RT-2 — Causal Tape (`idea-2.html`) — horizontal strip, full width × 100px, bottom-docked

**Realtime question:** *who is running, what just fired, what triggered the current action?*
**Encoding:** four micro-lanes in 100px — (1) top hairline: context pressure filling left→right,
resetting at each fold nick; (2) permanent user-prompt pip lane (green diamonds); (3) actor tokens in
order of appearance with tiny spawn/trigger arcs drawn above them (pulsing = running, dim = done,
red = failed); (4) the causal ribbon: episodes as contiguous segments (hatched amber = in flight,
red = cascade), red ▾ failures and orange ▾ watch-fires underneath, dashed purple cuts at
compactions, a playhead at NOW; (5) bottom fuse: breadcrumb chips of the live cause chain
(`E7·⚠storm → E8·✱bug@44 → E8 ▶ now`). **Update model:** fixed full-session time scale; elements
pop in as events arrive; fuse recomputes on every link event. **Scaling:** the tape is O(session
duration), not O(turns) — 2,146 calls are invisible as aggregate ribbon color, only causally
salient events earn pixels. **Prompt markers:** dedicated 11px lane under the pressure bar; pips
never collapse; dense prompt traffic would stack as overlapping diamonds with hover spread.
**Footprint:** full width × 100px, docked below the transcript.

## RT-3 — Causality Badge (`idea-3.html`) — corner element, exactly 320 × 240px

**Realtime question:** *who spawned whom and what's still running? what triggered the current
action?* **Encoding:** three micro-zones — left: a constellation of actor nodes (pulsing amber ring
= running, green = done, red = failed; violet solid = spawn, teal dashed = report-back, red zigzag =
the watch-storm interrupt) — right: the trigger fuse, the current action at the bottom with its
causes stacked as rungs above, connector color = relation, the fuse literally burns red during a
cascade — bottom: session progress bar with notch glyphs (purple fold, orange fire, red failure, blue
model-switch) plus last-event micro-caption; top: live dot, turn, calls, cost, and a context-pressure
hairline. During a runaway/cascade the whole badge border pulses red (color propagation at component
level). **Update model:** nodes/links materialize at spawn/report/fire events; fuse redraws on focus
change; notches accumulate. **Scaling:** O(actors + causal-chain depth), both tiny even at 2,000
turns; the progress bar compresses everything else to notches. **Prompt markers:** a permanent
`user` pip row between mid and bottom zones — U1–U7, always on screen, click jumps the transcript.
**Footprint:** 320 × 240px fixed.

---
Chrome-verified at 100% zoom: in-situ full-page shots, tight component crops, before/after animation
proofs (start vs storm vs end), and the prompt-marker → transcript jump exercised by synthetic clicks
(scroll position + flash verified). No git commands; no files outside `proposals/transcript-viz/compact/k3-b/`.
