# Transcript Visualization Proposals — Interaction-Centric Exploration

Three distinct, self-contained HTML mockups of one coherent synthetic evener session (~245 turns, ~1,600 tool calls, 3-level delegation tree, 3 compactions, 4 background/detached shell jobs, and 1 error/retry episode). All mockups render from `file://` with inline CSS/JS only — no CDN/network. Dark dev-tool aesthetic throughout. Interaction-centric: the user actively filters, queries, brushes, and compares to interrogate the session.

---

## Idea 1 — Query-Rift: query-driven transcript atlas

**Comprehension question.** "What happened in this session, filtered to the structures I care about right now?" — delegates, jobs, compactions, errors, tool types, anything, and the user steers mid-run.

**Visual encoding.** A horizontal strip, one cell per turn, color = turn kind, height = tool-call volume. Overlaid markers (teal delegate, yellow job, red error, violet checkpoint) for cross-turn structures. Facet sidebar + query chips + free-text query box compose an instrument panel. A linked detail pane below lists the turns in the brushed span.

**Why it scales.** The strip is constant-width regardless of turn count. A query collapses the irrelevant turns (dimmed) and keeps the relevant (highlighted), so a very long session's 245 turns, 1,600 tools, and 3 compactions become navigable. The brush scopes the detail list to a turn range so the detail never overflows.

**Interactions.** Type or click a query clause (chips/facets) to filter; brush a span on the strip to scope the detail list; drag the cyan brush handles to resize the scope; Esc clears.

---

## Idea 2 — Delegate Cascade: interactive delegation tree explorer

**Comprehension question.** "Who spawned whom, when, and at what cost — and how do sibling delegates compare side-by-side?" — the 3-level delegation tree, spawning/done spans, token cost, outcome.

**Visual encoding.** A left-to-right hierarchical tree (root→L1→L2→L3), laid out by spawn time on the x-axis. Each node is colored by level (blue/teal/violet) and shows a token-spend sparkline, tool count, wall time, and outcome. The root timeline lane at the bottom overlays each delegate's active span. Clicking a node inspects it in the right panel; clicking a sibling compares two side-by-side.

**Why it scales.** Delegation forms a bounded tree, not a linear list. A 3-level tree with 7 nodes (root + 2 L1 + 2 L2 + 1 L3) stays legible even when the session spans hundreds of turns and thousands of tool calls, because the tree structure compresses the linear list into a compact hierarchy.

**Interactions.** Click a node to inspect it (stats, shell jobs in span). Click a second sibling to compare side-by-side. Esc clears.

---

## Idea 3 — Cost & Compaction Atlas: cost-over-time explorer

**Comprehension question.** "How does the session's token/context cost evolve over time, and how do compaction boundaries re-partition the session into phases I can compare?" — token/context cost over turns, tool-type cost spend, compaction phase walls.

**Visual encoding.** A two-axis stacked bar chart: y-axis = token cost (in k), x-axis = turn index. Each turn is a vertical bar whose height = total token cost, stacked by tool type (exec_command, read_file, apply_patch, write_file, grep, web_fetch, delegate_send) and colored by kind. Compaction boundaries are drawn as vertical phase walls (labeled "ckpt #1", #2, #3). Error markers (red dots) and job spans (yellow bars) are overlaid. A scrubber at the bottom lets the user scope phases (compaction 1→2, compaction 2→3) to compare pre/post-compaction phases side-by-side.

**Why it scales.** Compaction partitions the long session into legible phases. The chart's constant-width x-axis maps any turn count into the same horizontal space, so 245 turns stay legible. Tool-type filter toggles isolate tool types by recoloring/dimming, so the chart never drowns in tool-call volume.

**Interactions.** Click a filter toggle to isolate a tool type. Drag the scrubber to scope a phase or compare phases. Esc clears.
