# Realtime transcript-comprehension visualization proposals

Each mockup opens on a running session that already contains more than 250 durable turns, then advances through a deterministic synthetic stream of tool starts/completions, turns, delegates, jobs, watch callbacks, phase changes, and compaction. All controls and simulation logic are inline and work from `file://`; each page defaults to live playback at 4× and provides pause, reset, 1×, 4×, and 12× controls.

## Idea 1 — Pulse Deck

**Realtime question:** What is moving right now, and is the current pace a productive burst, a stall, or a runaway loop? A rolling 90-second strip chart separates assistant, main tools, tempo, delegates, jobs, watches, and compaction; unfinished spans are dashed and keep extending against a fixed “NOW” edge, while a compact tape preserves all 250+ earlier turns. Each simulator event mutates the active-span set, completes or appends history, updates the event feed, and recalculates tempo, target diversity, oldest in-flight age, latency, burn, and a health diagnosis. The rolling window remains constant-size indefinitely, while the full-session tape aggregates one narrow mark per turn.

## Idea 2 — Conductor Orbit

**Realtime question:** What is the system’s current center of gravity, and are its parallel actors still inside healthy time envelopes? The center names and times the current action; live arcs on separate orbits represent main tools, delegates, jobs, and watches, while a pulse ring ages recent events and an outer ring retains one tick per durable turn. New events create arcs, completion cools them into history ticks, elapsed time grows arc length, context pressure advances around a threshold ring, and repeating watch callbacks accumulate into a visible red cluster. The “now” detail uses fixed radial space, and older work collapses into phase mass plus hundreds of fine outer ticks rather than competing with active actors.

## Idea 3 — Cadence Loom

**Realtime question:** Is the live event pattern making progress across different consequences, or repeating the same tool, target, and callback without new evidence? Semantic rows form a 120-second loom: events weave toward the right edge, running operations remain arrow-ended threads, and the detector brackets repeated same-row/same-target motifs while scoring pathology from repetition, outcomes, watch rate, and pending age. Every synthetic event appends a block or starts/finishes a thread; steering can break a loop, compaction cuts across all rows, and the lower 288-cell tapestry fills one durable-turn cell at a time. The expanded loom is a bounded rolling window, while the tapestry keeps the entire 250+ turn session spatially stable and legible.
