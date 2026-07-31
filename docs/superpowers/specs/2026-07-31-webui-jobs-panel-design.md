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
list call, a push notification that refreshes the list, a hub handler with a
dead-session fallback to persisted state, and the same failure taxonomy
(unsupported / daemon-gone / stale-with-retry).

## Decisions

1. **Push-driven live updates.** The event subscription machinery already
   exists: the session emits `EventJobStarted` / `EventJobFinished`
   (`agent/events/payloads.go`), and the projector already turns that pair
   into `serf/job/started` / `serf/job/finished`. Those two notifications are
   the panel's refetch trigger — the reducer gains one case covering both,
   and nothing new goes on the wire. A third, lighter notification fired at
   exactly those two instants with a strict subset of the same payload, so
   it was folded away (kata j7y6).
2. **Summary rows with expandable detail.** Row grammar follows the tasks
   panel: status glyph + description, disclosure expands to details.
3. **Lazy output tail on expand.** Job output lives in the durable jobstore
   OutputStore (`agent/internal/jobstore/output.go`). The panel fetches a
   tail only when the reader expands a job that has output, via a separate
   wire call, and holds that tail still for as long as the row stays
   expanded. The list payload stays small.

## Architecture

```
session jobstore ──► daemon appwire handlers ──► hub handler ──► frontend store ──► JobsPanel
       │                      ▲                          ▲
       └── EventJobStarted/ ──┘ (projection)             └── dead-session fallback
          Finished push       (serf/job/started|finished)    (past index + jobs.jsonl)
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

No new notification. The panel's refetch trigger is the pair the daemon
already pushes — `NotifySerfJobStarted` / `NotifySerfJobFinished`, both
carrying `SerfJobParams`, whose `Job.JobID` and `Job.Status` are everything a
refetch trigger reads.

Both response payloads are declared here rather than in `agent`, because a
wire payload carries this package's camelCase tag convention: `JobSummary`
and `JobOutputTail` live in `appwire/types.go`, and `agent` keeps aliases so
its own producers stay named in domain terms.

### Job summary shape (daemon → frontend)

One entry per job, projected from `jobstore.JobRecord`:

| Field | Source | Notes |
|---|---|---|
| `jobId` | `JobID` | |
| `type` | `Type` | `"shell"` or `"delegate"` |
| `status` | `Status` | the jobstore's own word, verbatim: running/completed/failed/cancelled/stopped/exhausted today, and whatever a newer daemon names |
| `reason` | `Reason` | omitted when empty |
| `description` | `Description`, else `Command`, else `Task` | first non-empty |
| `command` | `Command` | shell jobs; omitted when empty |
| `task` | `Task` | delegate jobs; omitted when empty |
| `background` | `Background` | |
| `startedAt` | `StartedAt` | RFC3339 |
| `endedAt` | `EndedAt` | omitted while running |
| `exitCode` | `ExitCode` | omitted when nil |
| `outputBytes` | `OutputBytes` | |
| `hasOutput` | `OutputPath != "" \|\| OutputBytes > 0` | drives the lazy tail affordance: a tail read is worth attempting |

Internal fields (provenance, restore descriptors, transcript refs, working
dir, notify state) stay out of the wire shape.

## 2. Daemon (server/ + agent/)

**server/appwire_runtime.go**

- Two function fields registered beside `tasksFn`:
  `jobsFn func() (any, error)` and
  `jobOutputFn func(jobID string, maxBytes int64) (data any, found bool, err error)`.
- `handleAppJobsList` mirrors `handleAppTasksList` on the capability gap: a
  nil `jobsFn` returns an empty response (old daemon), never an error. A
  registered `jobsFn` that fails is a different answer entirely, and its
  error propagates — "no jobs ran" and "I can't tell you what ran" must not
  reach the panel as the same sentence.
- `handleAppJobsOutput`: nil `jobOutputFn` returns
  `appwire.Unavailable("job output not available")`; `found=false` returns
  `appwire.InvalidParams`.

**agent/**

- The session registers a jobs function that projects
  `jobManager.store.LoadOrdered()` into the summary shape above. A store it
  cannot read is an error, never an empty list — the reason `JobSummaries`
  returns `([]JobSummary, error)` at all.
- The output function resolves the job's `OutputPath` and reads the last
  `maxBytes` (default 4 KiB, capped at 64 KiB) of the OutputStore.
  `OutputFileStats` supplies the lifetime `totalBytes`, validated against the
  record's own `OutputBytes` once the job is terminal; `retainedStart` is
  then the *tail's* start offset, `totalBytes - len(tail)` in Go bytes, so
  the difference of the two is exactly the byte span the payload carries.

**Projection (internal/appprojector/appwire_projection.go)**

- Nothing to add. `case events.EventJobStarted:` and
  `case events.EventJobFinished:` already project `serf/job/started` and
  `serf/job/finished` from `JobStartedData` / `JobFinishedData`, and that
  pair is the only job push on the wire — it serves the webui's subagent
  rows, the TUI's transcript reducer, and this panel's refetch trigger
  alike.

## 3. Hub (cmd/serf-hub/)

**app_jobs.go** (new), mirroring `app_tasks.go`:

- `hubJobsList`: live daemon first via
  `sourceForThreadWithManagedLaunch`; on `isDeadSessionError`, fall back to
  the past index: resolve ref → local past thread id → require
  `cfg.Past.Find(threadID)` → open the session's durable jobstore
  (`jobstore.Open` + `LoadOrdered`) and project the same summary shape.
  Those three steps are the shared `pastEntryForRead` helper, which both
  jobs fallbacks call. The gate keeps the hub from serving job data for
  sessions it cannot otherwise account for, same as `pastTasksListResponse`.
- `hubJobsOutput`: live daemon first; dead-session fallback resolves the
  job's `OutputPath` from the persisted store and reads the tail. A job id
  not in the persisted store is `InvalidParams`.
- Route registration beside the tasks handler in `app_rpc.go` (the
  `hubTasksList` dispatch site).
- `serf/job/started` and `serf/job/finished` relay through the existing
  notification bridge with no hub changes (same as `serf/task/updated`).

The projection code that turns a `JobRecord` into a summary lives in one
place shared by the daemon jobs function and the hub fallback, so the wire
shape cannot drift between the live and past paths. It stays *unexported*:
the hub reaches it only through `agent.LoadSessionJobList` and
`agent.LoadSessionJobOutputTail`, which is what keeps `jobstore.JobRecord`
from crossing the library boundary into `cmd/serf-hub`.

## 4. Frontend (cmd/serf-hub/frontend/src/)

**protocol**

- `types.gen.ts`: generated types for the new methods. `appwire/doc.go`
  drives this; `make generate` (`go generate ./appwire/...`) regenerates the
  protocol doc and the frontend types together.
- `reducer.ts`: one case over `serf/job/started` and `serf/job/finished`
  setting `model.jobsUpdatedAt` (per-thread monotonic timestamp), analogous
  to the `tasks` aggregate case. Both ends bump it, because starting and
  finishing both change what `serf/jobs/list` returns.
  `protocol/model.ts` gains the field.

**stores/threads.ts**

- `listJobs(ref): Promise<unknown>` — `client.request("serf/jobs/list", { ref })`.
- `jobOutput(ref, jobId): Promise<unknown>` —
  `client.request("serf/jobs/output", { ref, jobId })`. No `maxBytes`: the
  panel wants the daemon's default tail, and a client-chosen size would be a
  second place for the 4 KiB number to live.

**panes/session/chrome/jobData.ts** (new)

- `parseJobListData(data: unknown): JobRow[] | null` — wire-true parser
  against the daemon summary shape; null means uninterpretable (capability
  gap), same contract as `parseTaskListData`.
- `parseJobOutputData(data: unknown): JobOutput | null`.
- `JobRow.status` is the wire's string, deliberately not narrowed to the
  known `JobStatus` union. Casting would tell every consumer an unrecognised
  status had been handled when it had not; keeping it a string makes the
  compiler ask each consumer what it does with one it doesn't know.

**panes/session/chrome/JobsPanel.tsx** (new) + `jobspanel.module.css`

Mirrors `TasksPanel.tsx`:

- Trigger button + `Sheet`; imperative `open()` handle for the collapsed
  overflow menu. Trigger label shows a running badge: `Jobs` normally,
  `Jobs ●2` when two rows report status `running` (badge data comes from the
  last fetched list; hidden until the panel has fetched once — the tasks
  panel's aggregate has no jobs equivalent, and inventing one adds
  push-payload churn for little value).
- Fetch on open, on Try again, and on every `model.jobsUpdatedAt` bump while
  open. A bump while the panel is **closed** also refetches, but only to keep
  that badge honest: only for a bump the panel has not already fetched for,
  only once a first open has given the trigger a count worth keeping honest,
  and only while a trigger is rendered at all (`hideTrigger` puts no count on
  screen, so a push there costs no round trip). A closed-panel refresh that
  fails stays silent — no toast for a fetch the reader cannot see; the badge
  simply holds its last known count.
- Rows: type glyph (shell `›`, delegate `◈`), description, status `Chip`,
  elapsed time. Status→tone mapping: running → `alive`, terminal →
  `neutral`, failed → `danger` only for `failed`/`exhausted` (the
  color-is-attention rule; cancelled/stopped read as settled, mirroring the
  tasks panel's cancelled reasoning). A status this bundle does not know is
  neither known-alive nor known-failed, so it recedes `neutral` and the row
  is labelled with the wire's own word.
- The clock is shown only when it is knowable: a running job ticks against
  the shared `now`, a settled one shows its wall duration. A settled status
  beats a missing `endedAt` (an elapsed time must not climb forever on a
  finished job), and Go's zero time on the wire
  (`0001-01-01T00:00:00Z`) shows no clock rather than two millennia.
- `Disclosure` expands to a detail list: status, type, started, ended, exit
  code, output size, full command or task text. When `hasOutput`, the
  disclosure also lazily fetches the tail — `Disclosure` mounts its body only
  while open, so mounting the tail view inside it *is* the lazy trigger. The
  tail does **not** re-fire on a `jobsUpdatedAt` bump: a collapse/expand
  re-mounts and refetches, and holding it still while open keeps text from
  jumping under the reader. Tail renders in a `<pre>`, at the daemon's
  4 KiB default, with a "showing last N of M bytes" note when truncated —
  where N is `totalBytes - retainedStart`, the daemon's own byte measurement,
  not `tail.length` (UTF-16 code units, which mis-count every non-ASCII
  character).
- Failure taxonomy follows TasksPanel with one divergence: `actionUnavailable`
  → inline "isn't available" state, no retry; `isThreadNotFound` is **always**
  terminal daemon-gone, because there is no live-pushed aggregate here to
  disambiguate "never had jobs" from "can't ask any more" — with no rows it
  renders "This session has ended", with retained rows it keeps them under a
  terminal `role=alert` notice instead of blanking them, and neither offers
  Try again. Any other rejection splits on first-fetch versus re-fetch: a
  first fetch that fails has nothing to keep and gets the error state alone;
  a re-fetch that fails keeps the list on screen under a `role=alert` stale
  notice. Both carry Try again, and both toast — while the panel is open.
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

- appwire round-trip tests for `serf/jobs/list` and `serf/jobs/output`,
  asserting decoded field content and not merely that decoding succeeded.
- Projector tests pinning that `EventJobStarted`/`EventJobFinished` each
  produce exactly one notification, the lifecycle one — the guard against a
  second, duplicate job push growing back.
- Daemon handler tests: nil-fn capability gaps; a `jobsFn` error surfacing as
  an error rather than an empty list; happy path against a real jobstore
  fixture.
- Hub handler tests: live path, dead-session fallback, past-index gate
  (unknown thread → error), unknown job id → `InvalidParams`, and a corrupt
  `jobs.jsonl` → error rather than empty success.

**Frontend**

- `jobData.test.ts`: parser coverage with wire fixtures.
- `JobsPanel.test.tsx`: mirrors the TasksPanel suite — fetch on open,
  push-driven re-fetch while open, unsupported state, daemon-gone state,
  stale-on-refetch-failure with Try again, lazy tail fetch on expand — plus
  this panel's own edges: the closed-panel badge refresh and its three
  guards, the silent background failure, and the two no-clock cases.
- `reducer.test.ts`: `serf/job/started` and `serf/job/finished` both bump
  `jobsUpdatedAt`, and neither touches another thread.
- `SessionChrome.test.tsx`: trigger placement and overflow collapse item.

## Out of scope

- Job control actions (stop/cancel a job from the panel).
- Delegate transcript navigation from a row.
- A running-jobs aggregate on the thread model header/status row.
- Watch and watch-send records in the panel (jobs only: shell + delegate).
