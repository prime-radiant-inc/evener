# Sidebar watches: design exploration

Design-only exploration for surfacing **active watches** in the evener web UI.
No production code is changed by this directory.

## The ask

- Sidebar: show the number of active watches on a session's **summary line**.
- Sidebar: show individual watches in the session's **fold-out** (the expanded
  children of a session row).
- Activity panel: show **details of active watches**.

## What exists today

**Sidebar** (`cmd/evener-hub/frontend/src/shell/rail/`)

- `RailRow.tsx` renders one row. A session row is title line + optional second
  line. The second line (`rail-row-activity`, built by `activityGloss`) glosses
  *why* the row wants attention: `"2 subagents working · 1 job running · branch"`.
  It only renders for signal states (working / needs-you / failed) or when a
  depth-0 row needs its project named.
- `railNodes.ts` shapes the tree. A session's expanded children are built by
  `splitChildren`: current subagent rows, active `JobRailNode`s, then an
  `inactiveFold` ("Inactive subagents (N)") and a `completedJobsFold`
  ("Completed jobs (N)") disclosure.
- `activeWorkSummary(session)` rolls up `{ workingSubagents, runningJobs }`
  through the whole subtree. This is the natural home for a watch count.
- The visual language is deliberately neutral: the rail's only hues are the
  Cadence dot + tinted gloss (alive / attention / danger). A quiet row is one
  line.

**Activity panel** (`cmd/evener-hub/frontend/src/panes/session/chrome/`)

- `ActivityPanel.tsx` loads a retained `ActivityTree` (jobs + delegates) and
  renders `ActivityTree.tsx`, a flat row list built by `activityRows.ts`.
- Row kinds today: `job` (glyph `$`), `delegate` (glyph `⌘`), `fold`
  ("N inactive"). Dense rows carry name + right-aligned meta; a chevron reveals
  `ActivityRowDetail` (full command/mandate, a mono meta line, output tail).
- Panel header trigger reads `Activity · <active count>`.
- Live rows tick once a second via `TreeTickProvider`.

**Watch data today**

- The frontend wire has no active-watch model. The only watch-adjacent fields are
  `fromWatch` on a job and `parentWatchGranted` on a delegate.
- Backend truth lives in the jobstore folds and is exposed by
  `agent/doctor/watches.go` (`WatchView`): `watch_id`, `target`, `send_to`,
  `condition`, `active`, `end_reason`, receiver session/delegate, the joined
  target job, delivery accounting (`pending_lines`, `distinct_deliveries`,
  `delivered`, `dropped`, `evicted`, `still_pending`, `coalesced`), `end_notices`,
  and breaker telemetry (`max_self_influence_depth`, `runaway_drops`).
- A watch created by `job_watch` is one of: a one-shot timer (`after_seconds`),
  a repeating timer (`repeat_seconds`), an event watch (`events` +
  `event_filter`), an output match (`output_match`), or a periodic progress
  tick (`progress_interval_ms`). Every watch carries a human `note`, the prose
  reason it was armed.
- So a watch is **scheduled/pending work**, not running work. That distinction
  is the central design problem: the rail and the activity tree both currently
  model "things happening now", and a watch is "a thing that will happen".

## Design directions

Three deliberately different information designs live in `mockups/`. They differ
in how much prominence and structure watches get, and in where they sit relative
to running work:

- **A — Inline glint**: watches are supporting cast. A count joins the summary
  line; fold-out watch rows are one quiet line; the Activity panel gets a slim
  "Watches" group above retained activity.
- **B — Scheduled work**: watches are a peer of jobs, ordered by *when they will
  next do something*. The fold-out interleaves watches with running jobs under a
  shared "when" column; the activity tree gains a watch row kind sorted by next
  fire.
- **C — Watchboard**: watches get their own grammar. The summary line leads with
  the soonest fire; fold-out rows carry a cadence micro-visual; the Activity
  panel leads with an "Armed" band of watch cards.

## Non-negotiables carried into every direction

- Watches must not spend one of the four attention hues (attention / alive /
  danger / accent). They are not a call for a human, not a failure, and not a
  success. Danger is reserved for runaway drops.
- The rail is a triage surface; a row that can be one line stays one line.
- Every claim in the UI must be backed by a field that exists or is cheap to add.
- Watch detail must answer the three questions an operator actually asks:
  *what will it do*, *when*, and *did it already fire, and how*.

## Review

Four independent reviews of the three directions are consolidated in
`reviews/synthesis.md`. The round-2 proposal, the decision list, and the verified
data plan are in `round-2.md`; the round-2 mockups are
`mockups/round-2-primary.html` and `mockups/round-2-variant.html`.
