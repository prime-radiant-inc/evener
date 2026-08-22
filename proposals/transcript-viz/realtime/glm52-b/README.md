# Transcript Visualization Proposals — Real-Time Interaction-Centric Exploration

Three distinct, self-contained HTML mockups of one coherent synthetic evener session (~245 turns, ~1,600 tool calls, 3-level delegation tree, 3 compaction boundaries, 4 background/detached shell jobs, 1 error/retry episode). All mockups render from `file://` with inline CSS/JS only — no CDN/network. Dark dev-tool aesthetic throughout. Interaction-centric: the user actively filters, queries, brushes, and compares live streams while the session is actively running.

---

## Idea 1 — Query-Rift Live: live query-driven transcript atlas

**Real-time comprehension question.** "What is the agent doing right now, and what just changed?" — filtered to the structures I care about right now while it streams in.

**Visual encoding.** A live strip with constant cell width, color = turn kind, height = tool-call volume. Overlaid markers (teal delegate, yellow job, red error, violet checkpoint) for cross-turn structures. Facet sidebar + query chips + free-text query box compose an instrument panel. A linked detail pane below lists the turns in the brushed span. The strip dims non-matching turns and highlights matches, and a live badge indicates the stream is running. A brush follows the live turn as new turns arrive, even when paused.

**Update model.** Incremental — events are added live as they arrive; the strip grows horizontally; brushing works while the stream is live; the live turn is highlighted and the brush follows.

**Scaling.** The strip's constant-width x-axis maps any turn count into the same horizontal space, so 245 turns stay legible. Tool-type filter toggles keep the strip from drowning in tool-call volume. The brush scopes the detail list to a turn range so the detail never overflows.

**Interactions.** Type or click a query clause (chips/facets) to filter; brush a span on the strip to scope the detail list; drag the cyan brush handles to resize the scope; play/pause and speed controls; Esc clears.

---

## Idea 2 — Delegate Cascade Live: live interactive delegation tree explorer

**Real-time comprehension question.** "Who spawned whom, when, and at what cost right now — and how do sibling delegates compare side-by-side while it streams in?"

**Visual encoding.** A live 3-level delegation tree laid out by spawn time on the x-axis. Each node is colored by level (blue/teal/violet) and shows a token-spend sparkline, tool count, wall time, and outcome. The root timeline lane at the bottom overlays each delegate's active span. Clicking a node inspects it in the right panel; clicking a second sibling compares two side-by-side. In-flight states (running tool calls, active delegates, pending jobs) render distinctly from completed history. The root timeline lane at the bottom overlays each delegate's active span.

**Update model.** Incremental — delegate spawns and reports (and job spawn/done) are shown live as they arrive; in-flight states render distinctly from completed history; live indicator shows the stream is running.

**Scaling.** Delegation forms a bounded tree, not a linear list. A 3-level tree with 7 nodes (root + 2 L1 + 2 L2 + 1 L3) stays legible even when the session spans hundreds of turns and thousands of tool calls, because the tree structure compresses the linear list into a compact hierarchy.

**Interactions.** Click a node to inspect it (stats, shell jobs in span). Click a second sibling to compare side-by-side. Esc clears.

---

## Idea 3 — Cost & Compaction Atlas Live: live cost-over-time explorer

**Real-time comprehension question.** "How does the session's token/context cost evolve over time right now, and how do compaction boundaries re-partition the session into phases I can compare while it streams in?"

**Visual encoding.** A two-axis stacked bar chart showing token/context cost over turns. Each turn is a vertical bar whose height = total token cost, stacked by tool type and colored by kind. Compaction boundaries are drawn as vertical phase walls (labeled "ckpt #1", #2, #3). Error markers (red dots) and job spans (yellow bars) are overlaid. A scrubber at the bottom lets the user scope phases (compaction 1→2, compaction 2→3) to compare pre/post-compaction phases side-by-side. In-flight states render distinctly from completed history. The scrubber moves live as the stream progresses. Live indicator shows the stream is running.

**Update model.** Incremental — events are added live as they arrive; compaction boundaries must fire live; live indicator shows the stream is running.

**Scaling.** Compaction partitions the long session into legible phases. The chart's constant-width x-axis maps any turn count into the same horizontal space, so 245 turns stay legible. Tool-type filter toggles keep the chart from drowning in tool-call volume.

**Interactions.** Click a filter toggle to isolate a tool type. Drag the scrubber to scope a phase or compare phases. Esc clears.
