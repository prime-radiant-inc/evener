# WebUI Jobs Panel — Design

Date: 2026-07-31
Status: approved (pending spec review)
Branch: webui-jobs-panel

## Summary

The session pane gains a **Jobs** panel beside the existing Details and Tasks
panels. It lists every job the current session owns — background shell jobs
and delegate (subagent) jobs — with live updates driven by server push. Rows
show a summary; a disclosure expands into full details plus a lazily fetched
output tail for jobs that have output.

The design mirrors the tasks panel's pipeline end to end: a fetch-on-open
list call, a push notification that refreshes an open panel, a hub handler
with a dead-session fallback to persisted state, and the same failure
taxonomy (unsupported / daemon-gone / stale-with-retry).

## Decisions

1. **Push-driven live updates.** The event subscription machinery already
   exists: the session emits `EventJobStarted` / `EventJobFinished`
   (`agent/events/payloads.go`), and the projector→hub→reducer path already
   carries `serf/task/updated`. We add one projection case and one reducer
   case; no new plumbing layers.
2. **Summary rows with expandable detail.** Row grammar follows the tasks
   panel: status glyph + description, disclosure expands to details.
3. **Lazy output tail on expand.** Job output lives in the durable jobstore
   OutputStore (`agent/internal/jobstore/output.go`). The panel fetches a
   tail only when the reader expands a job that has output, via a separate
   wire call. The list payload stays small.

## Architecture

```
session jobstore ──► daemon appwire handlers ──► hub handler ──► frontend store ──► JobsPanel
       │                      ▲                        ▲
       └── EventJobStarted/ ──┘ (projection)           └── dead-session fallback
          Finished push            (serf/job/updated)      (past index + jobs.jsonl)
```

## 1. Wire types (appwire/types.go)

New methods and types:

- `MethodSerfJobsList = "serf/jobs/list"`
  - `JobsListParams{ Ref string }`
  - `JobsListResponse{ Data any }` — same envelope style as
    `TaskListResponse`: the daemon returns its native job-summary shape and
    the frontend parser owns interpretation.
- `MethodSerfJobsOutput = "serf/jobs/output"`
  - `JobsOutputParams{ Ref string; JobID string; MaxBytes int64 }`
  - `JobsOutputResponse{ Data any }` carrying
    `{ tail: string, totalBytes: int64, retainedStart: int64, truncated: bool }`.
- `NotifySerfJobUpdated = "serf/job/updated"`
  - `JobUpdatedParams{ ThreadID string; Ref string; JobID string; Status string }`
  — mirrors `TaskUpdatedParams`; no count fields, because the panel
  re-fetches the list rather than maintaining an aggregate.

### Job summary shape (daemon → frontend)

One entry per job, projected from `jobstore.JobRecord`:

| Field | Source | Notes |
|---|---|---|
| `jobId` | `JobID` | |
| `type` | `Type` | `"shell"` or `"delegate"` |
| `status` | `Status` | running/completed/failed/cancelled/stopped/exhausted |
| `reason` | `Reason` | omitted when empty |
| `description` | `Description`, else `Command`, else `Task` | first non-empty |
| `command` | `Command` | shell jobs; omitted when empty |
| `task` | `Task` | delegate jobs; omitted when empty |
| `background` | `Background` | |
| `startedAt` | `StartedAt` | RFC3339 |
| `endedAt` | `EndedAt` | omitted while running |
| `exitCode` | `ExitCode` | omitted when nil |
| `outputBytes` | `OutputBytes` | |
| `hasOutput` | `OutputPath != ""` | drives the lazy tail affordance |

Internal fields (provenance, restore descriptors, transcript refs, working
dir, notify state) stay out of the wire shape.

## 2. Daemon (server/ + agent/)

**server/appwire_runtime.go**

- Two function fields registered beside `tasksFn`:
  `jobsFn func() any` and
  `jobOutputFn func(jobID string, maxBytes int64) (any, error)`.
- `handleAppJobsList` mirrors `handleAppTasksList`: nil `jobsFn` returns an
  empty response (old-daemon capability gap), never an error.
- `handleAppJobsOutput`: nil `jobOutputFn` returns
  `appwire.Unavailable("job output not available")`; unknown job id returns
  `appwire.InvalidParams`.

**agent/**

- The session registers a jobs function that projects
  `jobManager.store.LoadOrdered()` into the summary shape above.
- The output function resolves the job's `OutputPath` and reads the last
  `maxBytes` (default 4 KiB, capped at 64 KiB) of the OutputStore, reporting
  `totalBytes`/`retainedStart`/`truncated` from `OutputFileStats`.

**Projection (internal/appprojector/appwire_projection.go)**

- `case events.EventJobStarted, events.EventJobFinished:` → one
  `serf/job/updated` notification per event, mirroring the
  `EventTaskUpdated` case. Payload comes from `JobStartedData` /
  `JobFinishedData` (job id + status); no new session events.

## 3. Hub (cmd/serf-hub/)

**app_jobs.go** (new), mirroring `app_tasks.go`:

- `hubJobsList`: live daemon first via
  `sourceForThreadWithManagedLaunch`; on `isDeadSessionError`, fall back to
  the past index: resolve ref → local past thread id → require
  `cfg.Past.Find(threadID)` → open the session's durable jobstore
  (`jobstore.Open` + `LoadOrdered`) and project the same summary shape. The
  `cfg.Past.Find` gate keeps the hub from serving job data for sessions it
  cannot otherwise account for, same as `pastTasksListResponse`.
- `hubJobsOutput`: live daemon first; dead-session fallback resolves the
  job's `OutputPath` from the persisted store and reads the tail. A job id
  not in the persisted store is `InvalidParams`.
- Route registration beside the tasks handler in `app_rpc.go` (the
  `hubTasksList` dispatch site).
- `serf/job/updated` relays through the existing notification bridge with no
  hub changes (same as `serf/task/updated`).

The projection code that turns a `JobRecord` into a summary lives in one
place shared by the daemon jobs function and the hub fallback — a small
exported helper — so the wire shape cannot drift between the live and past
paths.

## 4. Frontend (cmd/serf-hub/frontend/src/)

**protocol**

- `types.gen.ts`: generated types for the new methods. `appwire/doc.go`
  drives this; `make generate` (`go generate ./appwire/...`) regenerates the
  protocol doc and the frontend types together.
- `reducer.ts`: case for `serf/job/updated` setting
  `model.jobsUpdatedAt` (per-thread monotonic timestamp), analogous to the
  `tasks` aggregate case. `protocol/model.ts` gains the field.

**stores/threads.ts**

- `listJobs(ref): Promise<unknown>` — `client.request("serf/jobs/list", { ref })`.
- `jobOutput(ref, jobId, maxBytes?): Promise<unknown>` —
  `client.request("serf/jobs/output", { ref, jobId, maxBytes })`.

**panes/session/chrome/jobData.ts** (new)

- `parseJobListData(data: unknown): JobRow[] | null` — wire-true parser
  against the daemon summary shape; null means uninterpretable (capability
  gap), same contract as `parseTaskListData`.
- `parseJobOutputData(data: unknown): JobOutput | null`.

**panes/session/chrome/JobsPanel.tsx** (new) + `jobspanel.module.css`

Mirrors `TasksPanel.tsx`:

- Trigger button + `Sheet`; imperative `open()` handle for the collapsed
  overflow menu. Trigger label shows a running badge: `Jobs` normally,
  `Jobs ●2` when two jobs are non-terminal (badge data comes from the last
  fetched list; hidden until the panel has fetched once — the tasks panel's
  aggregate has no jobs equivalent, and inventing one adds push-payload
  churn for little value).
- Fetch on open; re-fetch when `model.jobsUpdatedAt` changes while open.
- Rows: type glyph (shell `›`, delegate `◈`), description, status `Chip`,
  elapsed time. Status→tone mapping: running → `alive`, terminal →
  `neutral`, failed → `danger` only for `failed`/`exhausted` (the
  color-is-attention rule; cancelled/stopped read as settled, mirroring the
  tasks panel's cancelled reasoning).
- `Disclosure` expands to a detail list: status, type, started, ended, exit
  code, output size, full command or task text. When `hasOutput`, the
  disclosure also lazily fetches the tail on first expand and re-fetches it
  when `jobsUpdatedAt` bumps while expanded. Tail renders in a `<pre>`,
  capped at 4 KiB, with a "showing last N of M bytes" note when truncated.
- Failure taxonomy identical to TasksPanel: `actionUnavailable` → inline
  "isn't available" state; `isThreadNotFound` + never-fetched → empty list
  or daemon-gone terminal state; any other rejection → toast + inline stale
  notice above the retained list + Try again.
- Disclosure ids are scoped per session with the same NUL-separator idiom
  (`${sessionRef}\0${jobId}`).

**panes/session/chrome/SessionChrome.tsx**

- `Jobs` trigger mounts beside Details and Tasks in the trailing cluster;
  below the existing 640px collapse breakpoint it moves into the "⋯" menu as
  a third overflow item (`Details`, `Tasks`, `Jobs`), opened through the
  same imperative-handle pattern.

## 5. Tests

Per `docs/testing.md`: deterministic, no provider credentials or network.

**Go**

- appwire round-trip tests for `serf/jobs/list` and `serf/jobs/output`.
- Projector tests for `EventJobStarted`/`EventJobFinished` →
  `serf/job/updated` (mirrors `TestProject_TaskUpdated`).
- Daemon handler tests: nil-fn capability gaps; happy path against a real
  jobstore fixture.
- Hub handler tests: live path, dead-session fallback, past-index gate
  (unknown thread → error), unknown job id → `InvalidParams`.

**Frontend**

- `jobData.test.ts`: parser coverage with wire fixtures.
- `JobsPanel.test.tsx`: mirrors the TasksPanel suite — fetch on open,
  push-driven re-fetch while open, unsupported state, daemon-gone state,
  stale-on-refetch-failure with Try again, lazy tail fetch on expand.
- `reducer.test.ts`: `serf/job/updated` case.
- `SessionChrome.test.tsx`: trigger placement and overflow collapse item.

## Out of scope

- Job control actions (stop/cancel a job from the panel).
- Delegate transcript navigation from a row.
- A running-jobs aggregate on the thread model header/status row.
- Watch and watch-send records in the panel (jobs only: shell + delegate).
