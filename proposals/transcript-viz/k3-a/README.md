# Transcript-comprehension visualization proposals — k3-a (overview-first navigation)

Three self-contained mockups of the same synthetic very long evener session
(278 turns, ~1,670 tool calls, 5 delegates in a 3-level tree, 6 background/detached jobs,
2 watches incl. one runaway episode of 47 fires/4m, 3 compaction folds, a build-break +
edit_file retry loop, one retried provider-529 turn failure, 1.16M tokens, 6h42m wall clock).
Open each file directly (`file://`); no network needed. All are dark dev-tool aesthetic,
inline CSS/JS, deterministic seeded data — every number shown is computed from the model.

Shared scenario: session `0J8XKM` — "migrate jobstore to SQLite, repair the watcher
runaway loop, land regression tests", branch `main`, model k3 (one mid-session switch to
k3-fast). Breakage window ~4:16–4:38: failed `go build` job → three stale-context
`edit_file` errors → 529 turn failure → recovery, plus the watch_2 runaway stopped by
user steering at #232.

---

## Idea 1 — Stratigraphic Minimap (`idea-1.html`)

**Question answered:** "What is the shape of this whole session, and where are the
interesting faults?" This is the owner's minimap intuition taken seriously: five semantic
lanes (WHO / TOOL DENSITY / DELEGATES / JOBS / EVENTS) where every turn is one horizontal
stratum, tool-call volume is a teal alpha ramp, delegate spans are violet brackets indented
by tree depth, jobs are green (red = failed, dashed = detached), watch fires are amber ticks
(the runaway reads as a solid barcode), errors are red diamonds, and compactions are
full-width violet fault lines labeled with the token fold (186k→24k). **Scaling:** cost is
O(turns) in fixed width — 278 or 2,000 turns only changes stratum height, and the semantic
lanes mean structure survives compression instead of blurring into a colored scrollbar.
**Interactions (all working):** hover crosshair + tooltip per turn, click to jump the synced
transcript excerpt pane, drag the white viewport window or scroll over the map to navigate,
click a transcript row to re-center the map.

## Idea 2 — Mission Timeline (`idea-2.html`)

**Question answered:** "Who was active when, what ran in parallel, and what did waiting and
context pressure look like over 6h42m?" A wall-clock-proportional swimlane trace: a context-
pressure area band on top (the sawtooth makes all three compactions visible as literal
cliffs), one lane for ROOT (per-turn blocks colored by kind), one per delegate (nested
dlg_02a with a tree connector to its parent), one per job, one per watch. Because x is real
time, the 52-minute fuzz job, the delegate migration, stalls, and the runaway watch all show
as honest width rather than as counts. **Scaling:** the lane count grows with actors, not
turns; turn blocks aggregate visually into texture when zoomed out and resolve into
individual blocks on brush-zoom. **Interactions (all working):** drag a brush on the ruler
to zoom to a time window, jump buttons for known episodes, hover tooltips on every element,
click to pin a turn/delegate/job/watch dossier (tokens, % of session, tool mix, narrative)
into the inspector.

## Idea 3 — Anatomy Atlas (`idea-3.html`)

**Question answered:** "Where did this session's cost actually go — which actor, which
phase, which single turns?" A zoomable icicle that partitions the session hierarchically:
session → actors (mainline + each delegate, with dlg_02a nested out of dlg_02's range) →
6-turn phase clusters → individual turns, width proportional to a switchable metric
(TOKENS / WALL TIME / TOOL CALLS). Hue families trace ownership down the tree; semantic
overrides mark compaction writes (violet — the fold itself is a visible cost) and error/
failure turns (red). **Scaling:** the tree depth is fixed (4 levels) regardless of session
length, and aggregation happens at the cluster level, so a 2,000-turn session is the same
shape with finer leaves; zoom keeps any region legible. **Interactions (all working):**
click any block to zoom (breadcrumb to ascend), metric toggle that preserves zoom focus,
hover for exact per-node numbers, and an inspector with ranked cost-by-actor and hottest-
cluster lists that are themselves zoom shortcuts.

---

### How they differ

- **Idea 1** is ordinal (turn-index) and shape-first: best for "where are the faults" and
  transcript-synced navigation.
- **Idea 2** is temporal and actor-first: best for parallelism, waits, runaways, and
  correlating jobs/watches/delegates with main-line work.
- **Idea 3** is quantitative and hierarchy-first: best for cost attribution ("delegates own
  54% of this session") and finding the most expensive phase/turn.

They compose naturally: 3 answers *why expensive*, 2 answers *when/parallel*, 1 answers
*where in the transcript* — and either 1 or 2 could serve as the jump target provider for
the existing linear transcript pane.
