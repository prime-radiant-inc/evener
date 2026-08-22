# Transcript-viz proposals · glm52-a · DENSE PATTERN SURFACES

Three self-contained visual mockups for evener session transcript comprehension, each answering a different comprehension question for a very long session (~250+ turns, 1,500–2,500 tool calls, 3-level delegation tree, 2–3 compaction boundaries, several background jobs, one error/retry episode). Dark dev-tool aesthetic, inline CSS/JS, work from `file://`. Synthetic data; hover/click for detail.

---

## idea-1 — Event Tape (a per-turn glyph ribbon)

**File:** `idea-1.html`

**Comprehension question:** "What happened across the whole session, turn by turn, and where are the phase boundaries, failures, and compaction folds?" The answer is a fit-to-width ribbon where each turn is a column, colored by turn kind, with inner bar height encoding tool-call intensity, amber dividers for compaction, red corner glyphs for failures, and amber dots for steering.

**Scaling:** One column per turn means a 263-turn session fits in 2 rows × 132 columns; a 1,000-turn session becomes ~8 rows — always one screen, never scroll. Bursts, stalls, failure clusters, and compaction boundaries read as patterns before any clicking.

**Interactions:** Hover shows a title tooltip; click selects a column and populates a detail card (kind, index, wall-clock, tokens, tool calls, synthetic content, error diagnostic). A background-jobs lane and a 3-level delegate-lane strip beneath the ribbon share the time axis.

---

## idea-2 — Tool-Mix Timeline (a dense activity heatmap)

**File:** `idea-2.html`

**Comprehension question:** "How did the tool mix shift across the session — which tools dominated each phase, where did cost concentrate by tool, and where is the error episode?" Rows are tool types (ordered by total calls), columns are time-buckets (~5 turns/bucket), and cell intensity encodes call count; a vertical amber band marks each compaction boundary and a mini-icicle on top shows the 3-level delegate tree along the same axis.

**Scaling:** 50 buckets × 10 tool types keeps the grid to one screen regardless of session length; adding buckets or rows only changes cell size, not layout. Phase transitions (exploration → delegation → integration) and tool-mix shifts read as spatial patterns — no clicking needed to read them.

**Interactions:** Hover shows a tooltip (tool type, bucket, calls, row total); click selects a cell and populates a detail card (tool type, bucket, calls, phase, reading). Row labels are color-glyphed and sorted by total calls so the dominant tool is always at the top.

---

## idea-3 — Delegate Cost Matrix (an icicle + ribbon tree)

**File:** `idea-3.html`

**Comprehension question:** "How is cost (tokens, wall-clock, tool calls) distributed across the delegation tree, and which branch owns most of the session's tokens and time?" The 3-level tree is an icicle: node width = turn span, depth = delegation level; inside each node, stacked micro-ribbons show tool-call density (green), token cost (blue), and wall-clock duration (violet) — a red ribbon marks an error.

**Scaling:** One node per session element, width proportional to turn span, so a 3-level tree with 7 nodes fits one screen regardless of depth; adding levels only adds rows. A fat node's tall ribbons immediately show where tokens and time concentrate; the only branch that errored carries a red ribbon.

**Interactions:** Hover or click a node to populate a detail card (name, depth, parent, turn span, tool calls, tokens, wall-clock, errors with meter bars); the error node auto-selects on load to show the build-failure retry.
