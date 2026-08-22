# transcript-viz / k3-b — Causality & Structure

Three novel visualization proposals for comprehending very long evener sessions. All three render
the **same synthetic long session** (`evener-8f3k2`: 287 turns, 2,146 tool calls, a 3-level
delegation tree, 4 shell jobs, 3 watches, 2 compaction boundaries, one watch-storm → retry-cascade
incident) so the encodings can be compared side-by-side. Self-contained HTML, no network, dark
dev-tool aesthetic.

## Idea 1 — Upstream: causal provenance graph (`idea-1.html`)

**Question answered:** *"Why did the session end up here?"* — for any node, what is its upstream
causal chain and what did it cause downstream. The session is encoded as a provenance DAG: nodes are
key entities (episodes, delegates, jobs, watches, compactions, steering, goal), edges are typed
causal relations (spawned, steered, triggered, reported, caused-failure, folded, gate-passed), with
time flowing left→right. It scales because 287 turns collapse to ~16 entities — the graph shows
structure, not text; semantic zoom (clicking the retry cascade expands its 6-node internal
attempt→failure→root-cause chain) keeps detail available without polluting the macro view.
Interactions: click-to-trace (amber = ancestors, teal = descendants), semantic zoom, evidence
tooltips, sidebar chain-of-causation readout.

## Idea 2 — Riverbed: actor-lane causal timeline (`idea-2.html`)

**Question answered:** *"Who was doing what when, and which events crossed actor boundaries to cause
others?"* Each actor (user, root agent, each delegate, each shell job, the watch subsystem, plus a
context-pressure lane) owns a horizontal swimlane over wall-clock time; all 2,146 tool calls render
as per-lane density currents, file edits as colored ticks, failures as red diamonds, and cross-lane
bezier arcs carry the causality (spawn / report / trigger / steer / notify). Compaction folds are
vertical bands, and the top CONTEXT lane makes *why* each fold happened legible (pressure climbs to
92%, then drops). It scales because rendering cost is O(minutes × lanes), independent of turn count,
and the overview-strip brush zoom resolves a 90-second watch storm inside a 6-hour session.
Interactions: drag-to-zoom brush, hover evidence tooltips, click-to-pin causal arcs.

## Idea 3 — Case File: chain-of-custody episode ledger (`idea-3.html`)

**Question answered:** *"What were the ~14 things that actually happened, what caused each, and what
did the session touch?"* A blast-radius heatmap (files × time buckets, edit intensity + failure
rings + fold columns) sits above a forensic ledger of auto-clustered episodes, each rendered as a
CAUSE → MECHANISM → OUTCOME strip with evidence chips (files, actors, tools, cost) and explicit
↖caused-by / ↘caused link pills for non-adjacent causality (e.g. the retry cascade tracing back to
a diff written three episodes earlier). It scales because the ledger grows with the number of
*episodes*, not turns — a 2,000-turn session stays a one-screen scroll. Interactions: click an
episode to flood-highlight its causal ancestors/descendants across both the ledger and the heatmap,
file-chip → heatmap-row hover linking, sticky "why did it end up here" rail.

---
Files: `idea-1.html`, `idea-2.html`, `idea-3.html` — open directly from `file://`. Verified in
Chrome. No repo files outside this directory were touched.
