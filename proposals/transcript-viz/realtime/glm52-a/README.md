# Transcript-viz proposals · realtime · glm52-a · DENSE PATTERN SURFACES

Three self-contained realtime visual mockups for evener session transcript comprehension, each answering a different realtime comprehension question during a running session (~250+ turns, 1,500–2,500 tool calls, 3-level delegation tree, 2–3 compaction boundaries, several background jobs, one error/retry episode). Dark dev-tool aesthetic, inline CSS/JS, work from `file://`. Synthetic data with embedded realtime event stream, play/pause/speed controls, and a LIVE indicator. The pattern surface updates incrementally as events arrive; the reviewer must SEE new columns/cells/glyphs appear.

---

## realtime idea 1 — Realtime Event Tape (a live per-turn glyph ribbon)

**File:** `idea-1.html`

**Realtime comprehension question:** "What pattern is forming right now (burst, stall, retry loop, runaway), what just changed, and is current activity normal for this session?" The answer is a fit-to-width ribbon where each turn is a column, colored by turn kind, with inner bar height encoding tool-call intensity, amber dividers for compaction, red corner glyphs for failures, and amber dots for steering.

**Update model:** Columns stream in from the right as turns arrive; older columns compress toward the left to keep the full-session-so-far pattern on one screen. A green-glow border marks the live cell. The streaming ticker narrates each event as it arrives (steering, compaction, tool_results, failure).

**Scaling:** One column per turn means a 263-turn session fits in 2 rows × 132 columns; a 1,000-turn session becomes ~8 rows — always one screen, never scroll. Bursts, stalls, failure clusters, and compaction boundaries read as patterns before any clicking.

**Interactions:** Play/pause/speed controls drive the simulation; hover shows a title tooltip; click selects a column and populates a detail card (kind, index, wall-clock, tokens, tool calls, synthetic content, error diagnostic). A background-jobs lane and a 3-level delegate-lane strip beneath the ribbon share the time axis.

---

## realtime idea 2 — Realtime Tool-Mix Timeline (a live activity heatmap)

**File:** `idea-2.html`

**Realtime comprehension question:** "How is the tool mix shifting across the session — which tools dominate each phase, where did cost concentrate by tool, and where is the error episode?" Rows are tool types, columns are time-buckets, and cell intensity encodes call count. A vertical amber band marks each compaction boundary and a mini-icicle on top shows the 3-level delegate tree along the same axis.

**Update model:** Columns grow column-by-column from the right; the active bucket's column pulses with a glowing green outline to mark where the session is right now. The active-row label highlights the current tool type. The streaming ticker narrates each event as it arrives (tool_results, steering, compaction, failure).

**Scaling:** 50 buckets × 10 tool types keeps the grid to one screen regardless of session length; adding buckets or rows only changes cell size, not layout. Phase transitions (exploration → delegation → integration) and tool-mix shifts read as spatial patterns — no clicking needed to read them.

**Interactions:** Play/pause/speed controls drive the simulation; hover shows a tooltip (tool type, bucket, calls, row total); click selects a cell and populates a detail card (tool type, bucket, calls, phase, reading). Row labels are color-glyphed and sorted by total calls so the dominant tool is always at the top.

---

## realtime idea 3 — Realtime Delegate Cost Matrix (a live icicle + ribbon tree)

**File:** `idea-3.html`

**Realtime comprehension question:** "How is cost (tokens, wall-clock, tool calls) distributed across the delegation tree, and which branch owns most of the session's tokens and time?" The 3-level tree is an icicle: node width = turn span, depth = delegation level; inside each node, stacked micro-ribbons show tool-call density (green), token cost (blue), and wall-clock duration (violet) — a red ribbon marks an error.

**Update model:** As turns stream in, the root bar grows rightward; below it, new child delegate nodes spawn into the tree and grow rightward. Active nodes pulse/glow with a green outline; an error ribbon appears the instant a failure fires (the build-failure retry) and a taller violet duration ribbon, showing it took wall-clock time to recover.

**Scaling:** One node per session element, width proportional to turn span, so a 3-level tree with 7 nodes fits one screen regardless of depth; a fat node's tall ribbons immediately show where tokens and time concentrate; the only branch that errored carries a red ribbon.

**Interactions:** Play/pause/speed controls drive the simulation; hover or click a node to populate a detail card (name, depth, parent, turn span, tool calls, tokens, wall-clock, errors with meter bars); the error node auto-selects on load to show the build-failure retry.
