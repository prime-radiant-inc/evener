# Session Rail job lanes: live refresh and degraded history

**Issue:** [#451](https://github.com/prime-radiant-inc/evener/issues/451)
**Related:** [#338](https://github.com/prime-radiant-inc/evener/issues/338),
[#450](https://github.com/prime-radiant-inc/evener/issues/450),
[#457](https://github.com/prime-radiant-inc/evener/issues/457)
**Date:** 2026-10-24
**Base:** `92c6417a3e99072c9085ae2bec19198b1e930cf7`

## Base finding

The Session Rail runtime described by the issue is not in this base. The
complete P0-P4 rail was reverted by `8fec1b124`, and no re-land has landed:
`src/panes/session/rail/` is absent, `evener/thread/railSummary` is absent from
the protocol and the hub, and neither `useRailSummary` nor
`railModelFromSummaryAndTurns` exists in any ref of this checkout
(`git log --all -S` is empty for both).

The unmerged re-land contract lives only on the closed spec PR #466
(`task-7-session-rail-reconciliation-20260826`, commit `986ade39e8`):
`docs/superpowers/specs/2026-08-26-session-rail-reconciliation-design.md`. It
records #451 as a per-session job-lane requirement and assigns it to the P3
re-land phase. Issue #451's own triage says: "Cover #451 in the #338 re-land PR
rather than opening a standalone PR" and "No standalone main-tree fix is
possible."

This spec therefore freezes the #451 slice - the hook, the model merge, and
the degraded presentation - as the piece the #338 re-land mounts. The slice
has no rail to mount into at this base, so it is exercised by its own tests
and closes nothing. Landing it on main is a product decision, not a claim that
#451 is fixed for a user.

## Stale-cache path

The issue reports the unmerged rail's behavior (source: issue #451 and the
re-land contract above; the code is not in this base):

- the hook performs a one-shot request per thread ref and caches it forever;
- `railModelFromSummaryAndTurns` merges live turns into the rail but reuses
  `summary.jobs`, so turns advance while job lanes stay at the snapshot;
- every summary error is caught silently.

A job that starts after the snapshot has no lane. A job that finishes after it
stays running. A reconnect that replaces the thread model leaves the cache in
place. An incomplete or damaged job projection looks identical to complete
history.

## Live signals that already exist

The thread model carries the signals, so the hook takes them as inputs and
invents no subscription of its own:

| Signal | Source | Meaning |
| --- | --- | --- |
| `jobsUpdatedAt` | `ThreadModel.jobsUpdatedAt` | bumped by `evener/job/started`, `evener/job/finished`, `evener/delegate/updated`, and `evener/jobs/treeUpdated` |
| `jobsTreeRevision` | `ThreadModel.jobsTreeRevision` | monotonic revision from `evener/jobs/treeUpdated` when this thread is the root |
| `hydrations` | thread-store bookkeeping per ref | incremented by every wholesale hydration, including the reconnect `handleReady` path and targeted resyncs |

Two limits are known and carried, not hidden:

- `jobsTreeRevision` is emitted only for a root session, so a non-root rail's
  only job signal is `jobsUpdatedAt`, which is `Date.now()`; two job
  transitions inside one millisecond can be lost for a non-root rail. The
  re-land should add a monotonic per-thread job counter.
- `hydrations` is deleted when a ref is released (`dropUnpinnedModel`), so its
  counter is not monotonic across a release. It is a refresh signal, never an
  ordering fence.

No production consumer reads these signals today; the activity panel's old
freshness effect was deleted in `2076c81573` and the current panel reads the
independent `SessionActivityStore`. The deleted `useActivityRefresh` is the
pattern ported here; the rail hook owns its own new bookkeeping.

## Types

```ts
type RailJobHistoryStatus = "complete" | "incomplete" | "degraded" | "fatal";

interface RailJobHistory {
  status: RailJobHistoryStatus;
  /** The projection revision these jobs were read at, or null when the
   * source has none (the re-land maps Appwire JobActivityTree.Revision). */
  revision: number | null;
}

interface RailJobInterval {
  jobId: string;
  startedAt: number;          // epoch ms
  finishedAt: number | null;  // null while running
  exitCode?: number;
  status: string;
}

interface RailSummaryTurn {
  turnId?: string;            // wire turn id when the source carries one
  startedAt: number;
  userInput: boolean;
  steering: boolean;
  error: boolean;
  in: number;
  out: number;
  resultBytes: number;
}

interface RailSummary {
  turns: readonly RailSummaryTurn[];
  jobs: readonly RailJobInterval[];
  jobHistory: RailJobHistory;
}

type RailSummaryLoader = (ref: string, context: { signal: AbortSignal }) => Promise<RailSummary>;
```

The hook's own `jobHistory` is `RailJobHistory | null`: null means no
projection exists yet, and the notice renders nothing for it. A guessed
completeness at first paint is not allowed.

## Refresh contract

`useRailSummary({ ref, load, jobsUpdatedAt, jobsTreeRevision, hydration, terminal, cache })`
returns `{ summary, jobHistory, loading, failure, refresh }`.

1. **Cache by ref, preserved.** One cache entry per thread ref, shared across
   hook mounts, holding `{ summary, signal, hydration, terminal, jobHistory? }`.
   A remount whose entry matches the current signal, hydration and terminal
   flag renders the cached summary without a request. Entries live for the
   page session; `RailSummaryCache.clear()` is the teardown seam the re-land
   ties to the thread store's release path.
2. **Refresh only on that ref's job signals.** A change to the signal key
   (`jobsUpdatedAt` + `jobsTreeRevision`) or to `hydration` invalidates the
   entry and refetches. Live turn progress is not a signal: merging new turns
   into the model never calls the loader. A new loader or callback identity is
   not a signal either.
3. **Bursts coalesce; requests never overlap.** At most one request is in
   flight per mounted hook. A signal that arrives while it is open sets one
   trailing flag, and exactly one more request runs when it settles. Two
   mounted hooks for one ref are out of scope (the rail mounts one summary per
   session). Because hydration counters can reset on release, they cannot
   order writes across mounts; the revision guard below is the cross-mount
   ordering rule, and a source that returns null revisions falls back to
   last-write-wins.
4. **Reconnect invalidates.** Any `hydration` change bumps the hook's request
   generation and aborts the open request, even before a cache entry exists.
   Responses from a superseded generation are dropped, and the coalesced
   request runs after the aborted one settles.
5. **Ended sessions settle at their terminal revision.** The cache entry
   records whether it was captured live. An ended mount loads once on a miss,
   refreshes an entry captured live, and then holds the terminal entry across
   job signals and hydrations. A live session that inherits a
   terminal-captured entry refreshes it. A live-to-terminal transition while
  mounted refreshes once, so the last job's finish is captured. A terminal
  refresh queued behind an in-flight live request survives later job signals,
  so that final refresh is not discarded.
6. **Stale revisions never replace newer data.** A response whose
   `jobHistory.revision` is lower than the cached entry's, or null while the
   cached entry is revisioned, is dropped and asks once more (bounded). A
   repeated stale answer stops asking and records the cached rows as
   `degraded`, so the lane is usable but the rail shows that it could not
   reconcile the newer job signal and offers Retry. An equal or higher
   revision replaces the snapshot wholesale; snapshots are never merged page
   by page. The retry budget belongs to one invalidation cycle: a later job
   signal, hydration, or ref change starts a fresh budget, and a stale answer
   from an older cycle cannot spend the newer cycle's budget.
7. **Job-only failure preserves turns.** A failed refresh with a cached summary
   keeps it and records `degraded` in the cache entry, so a remount does not
   claim complete history; a failure with no cached summary reports `fatal`
   and no summary. Neither clears valid turn data. A loader that throws
   synchronously takes the same path.
8. **Lifecycle is defined.** A ref change fences the old ref's response,
   aborts its request, and starts the new ref's load. Unmount aborts the open
   request. A fenced or abandoned response clears `loading` when no request
   replaces it, so a reconnect during a terminal Retry leaves an honest state
   rather than a stuck spinner. Nothing refreshes on a timer, and nothing
   polls.

`refresh()` is the explicit user retry: it bypasses the terminal freeze,
abandons any response open from the previous attempt rather than waiting on a
loader that may ignore its abort signal, and requests once.

## Degraded-history contract

`jobHistory` is carried end to end, never collapsed into one error bit:

| Status | Meaning |
| --- | --- |
| `complete` | loaded at one revision, no unresolved damage |
| `incomplete` | a bounded page, continuation, or unavailable branch prevented a complete claim; retained rows stay usable |
| `degraded` | useful data renders but a lane or branch is damaged or unavailable |
| `fatal` | the summary itself could not be loaded; no partial result is presented as complete |

The loader returns the status already derived from its source (the re-land maps
the Appwire projection's complete counts, branch errors, truncated state, and
continuations onto these four). The hook adopts that status and may only
downgrade it: a failure with cached data records `degraded` (or keeps a cached
`fatal`), without cached data `fatal`. The model takes the hook's resolved
status as a required argument, so no caller can upgrade it.

The presentation is `RailJobHistoryNotice`: an `InlineNotice` (neutral tone,
polite live region) that renders nothing when the status is complete or absent.
`degraded` and `fatal` carry a `Retry` link button that calls `refresh`.
`incomplete` is a normal bounded result, not an error: it is announced without
a retry that could not advance the page. The notice is located by role and the
`data-job-history` attribute, not by copy.

## Model merge

`railModelFromSummaryAndTurns(summary, turns, jobHistory)`:

- derives events from the summary's turn tuples, then appends live turns the
  summary does not already hold. Identity is the summary's `turnId` when it
  carries one, otherwise that turn's start timestamp. Live turns are matched by
  `TurnModel.id` first, which needs no live timestamp because the summary
  supplies the placement. The timestamp fallback is one-to-one: a matched
  summary turn is consumed, and id-less summary turns are queued per
  timestamp so two of them each match one live turn. A live turn beyond the
  queue is a distinct turn and is appended. Two distinct turns that started
  in the same millisecond therefore stay distinct when their ids differ.
  Events are ordered by start time; the slice exposes no turn indices, so
  nothing is renumbered.
- reconciles a matched live turn with its summary event: the live turn's token
  usage and failure replace the summary's, while summary-only facts such as
  `resultBytes` and the prompt/steering flags stay. A turn the summary captured
  while it was still running therefore completes or fails on the rail without
  waiting for a job signal.
- reuses the summary's jobs, mapped to `RailJob` (`finishedAt: null` while
  running) and stable-sorted by `startedAt`, then `jobId`. A null summary
  yields no jobs, never fabricated lanes.
- computes `jobLanes` as the widest overlap of job intervals, where an
  interval ending exactly where the next starts does not overlap it; 0 when
  there are none. A renderer that needs a minimum lane count applies its own.
- carries `jobHistory` unchanged.
- computes `startMs`/`endMs` from events and jobs only, 0/0 when both are
  empty.

This is the job-lane projection, not the reverted renderer's `RailModel`
(`railModelFromTurns`/`railModelFromSummary`, absent here). It produces no
`live`, `prompts`, or axis state; the re-land composes those with this merge,
and that composition owns the renderer's `finishedAt` sentinel.

## Non-goals

- No socket-close repair and no change to the Appwire dispatch model.
- No rail layout, canvas rendering, scroll sync, axis, or action redesign; no
  mount is added by this slice.
- No backend RPC or protocol change; the loader is injected and no activity
  rows are copied. This is a rail projection over the same revisioned job
  source, not a second source of job truth.
- No polling and no fixed refresh interval.
- No full-suite local runs; CI is the source of truth.

## Verification

Focused frontend cases, each red before the implementation and green after:

- `useRailSummary.test.tsx`
  - a cached summary plus a later job-started signal refreshes the lane, and
    the merged model shows the new job; a later job-finished signal closes it;
  - a live-to-terminal transition refreshes once and then stays cached;
  - a terminal mount refreshes an entry captured live, then freezes;
  - a hydration bump refetches, the pre-reconnect response never becomes
    visible, and the refresh waits for the aborted request;
  - an older or unversioned revision is dropped, retried once, and the retry
    stop clears `loading` with the cached lane intact;
  - a burst of signals coalesces into one trailing request with no overlap;
  - a job-only failure keeps the cached turns, reports degraded, and a
    remount does not claim complete history; a first-load failure reports
    fatal and `refresh` retries it; a synchronously-throwing loader reports
    fatal;
  - a cold mount reports no lane status until the first projection;
  - a ref change fences the old ref's response and loads the new one;
  - unmount aborts the open request;
  - an unchanged rerender with a new loader identity does not refetch, and a
    terminal cache stays frozen across job signals and hydrations. Each
    negative case sits in a test that first proves its positive load.
- `railModel.test.ts`
  - live turns merge without duplicating a shared turn, including two turns
    with the same start millisecond and a summary turn with no id;
  - refreshed summary jobs replace stale lanes and sort stably;
  - `jobHistory` (complete, incomplete, degraded, fatal) survives the merge;
  - a null summary still renders live turns with no invented jobs;
  - `jobLanes` counts the widest interval overlap and touching intervals do
    not share a lane;
  - `startMs`/`endMs` follow turns and job intervals.
- `RailJobHistoryNotice.test.tsx`
  - complete and absent job history render no notice;
  - degraded and fatal render a polite status with a retry action that calls
    back;
  - incomplete renders a polite status with no retry.

Commands, from `cmd/evener-hub/frontend`, using the documented read-only
focused form (`docs/developing-evener/testing.md`):

```
EVENER_FRONTEND_VITE_CACHE_DIR=<absolute scratch outside the checkout> \
  npx vitest run src/panes/session/rail --configLoader runner --maxWorkers=2
npm run typecheck
npx biome check src/panes/session/rail
```

`make build` at the repository root proves the frontend still builds.
