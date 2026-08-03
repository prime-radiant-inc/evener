# Current-Session Activity Tree Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat Jobs sheet and `serf/jobs/list` payload with a recursive, paginated Activity tree that shows every retained shell job beneath its authoritative session or subagent owner.

**Architecture:** The agent package builds one authoritative recursive tree from durable job/delegate records with live overlays, explicit partial-branch errors, stable IDs, aggregate counts, and bounded continuation. The existing `serf/jobs/list` method returns that tree for live and exited local sessions; a root-scoped tree revision notification invalidates nested activity. The Web UI parses the recursive payload, reconciles it by stable IDs, and renders a responsive accessible master-detail Activity sheet.

**Tech Stack:** Go, AppWire JSON-RPC, jobstore event logs, Serf session metadata, React 19, TypeScript 6, Zustand, CSS modules, Vitest/Testing Library, headless-Chrome layout guards.

## Global Constraints

- Read `docs/testing.md` before changing tests; all default tests must be deterministic and credential-, network-, quota-, model-, and ambient-state-independent.
- Replace the existing `serf/jobs/list` payload in place. Do not add a second tree endpoint.
- Keep `serf/jobs/output`; select descendant output by the owner session ref supplied in the tree.
- The local activity-tree capability must never cross source or state-directory boundaries.
- Count work units, not visible rows: each shell job and each delegate turn counts once; session and subagent rows do not count.
- Aggregate precedence is `working` → `failed` → `unavailable` → `ended` → `idle`, using the exact rules in the design spec.
- Bound each response to 2,000 work units, 32 newly expanded delegation levels, and 4 MiB encoded JSON; expose truncated branches through continuation on `serf/jobs/list`.
- Unknown statuses retain their exact label, authoritative terminal bit, and neutral visual tone.
- Green means working, red means failure, gray means terminal/idle, blue means focus/selection/link, and amber only means human input is required.
- Generated files `docs/appwire-protocol.md` and `cmd/serf-hub/frontend/src/protocol/types.gen.ts` are regenerated with `make generate`, never hand-edited.
- Do not stage or modify unrelated workspace changes, including `docs/superpowers/plans/2026-08-02-all-open-katas.md` and `docs/superpowers/specs/2026-08-03-named-pinned-session-sections-design.md` if they remain dirty.

## File Structure

**Create**

- `agent/jobs_activity.go` — pure activity-tree projection, aggregation, bounds, continuation token validation, and live-session traversal.
- `agent/jobs_activity_test.go` — deterministic live-tree, ordering, aggregation, error, and continuation tests.
- `agent/jobs_activity_past.go` — exited-local-session traversal from one state directory.
- `agent/jobs_activity_past_test.go` — durable traversal and source-boundary tests.
- `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts` — recursive runtime parser, IDs, default expansion, and reconciliation helpers.
- `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts` — parser and reconciliation tests.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx` — fetch/refresh state and responsive master-detail shell.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx` — recursive accessible tree and keyboard navigation.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityInspector.tsx` — root, subagent, and shell-job inspection plus lazy output.
- `cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css` — Activity sheet geometry and tree styling.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx` — panel behavior, refresh, failures, selection, and narrow-screen tests.
- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx` — disclosure and keyboard tests.
- `cmd/serf-hub/frontend/scripts/layoutguard/cases/activity-tree-responsive/{case.json,harness.html,assert.mjs}` — real-browser depth and narrow-master-detail guard.

**Modify**

- `appwire/types.go`, `appwire/protocol.go`, `appwire/protocol_test.go` — replacement tree types, continuation parameter, and tree-update notification.
- `docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts` — generated outputs.
- `agent/jobs_panel.go` and tests — retain output-tail projection; remove flat-list projection after callers move.
- `agent/events/payloads.go`, `agent/events/eventdata.go`, and event tests — carry root tree identity/revision on job lifecycle events.
- `agent/session.go`, `agent/session_init.go`, `agent/subagents.go` — share one monotonic tree revision and root session identity across descendants.
- `internal/appprojector/appwire_projection.go` and tests — emit `serf/jobs/treeUpdated` beside child job lifecycle notifications.
- `server/server.go`, `server/appwire_runtime.go`, `server/server_test.go` — pass `JobsListParams` to the new tree provider.
- `cmd/serf/serve.go`, `cmd/serf/serve_residual_fuzz_test.go` — wire the live session tree provider.
- `cmd/serf-hub/app_jobs.go`, `cmd/serf-hub/app_jobs_test.go` — live-first tree routing and exited-local fallback.
- `cmd/serf-hub/internal/appsource/local_daemon.go` and tests — forward the replacement payload unchanged.
- `cmd/serf-hub/frontend/src/protocol/model.ts`, `reducer.ts`, `reducer.test.ts` — store root tree revisions.
- `cmd/serf-hub/frontend/src/stores/threads.ts`, `threads.test.ts` — route tree invalidation and expose list/output calls.
- `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`, `SessionChrome.test.tsx` — replace Jobs with Activity and preserve collapsed-menu opening.

**Delete after replacement tests pass**

- `cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.test.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/jobData.ts`
- `cmd/serf-hub/frontend/src/panes/session/chrome/jobData.test.ts`
- `cmd/serf-hub/frontend/src/panes/session/chrome/jobspanel.module.css`

---

### Task 1: Define the replacement AppWire contract

**Files:**
- Modify: `appwire/types.go:1179-1229`
- Modify: `appwire/protocol.go`
- Modify: `appwire/protocol_test.go`
- Generate: `docs/appwire-protocol.md`
- Generate: `cmd/serf-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Produces: `JobsListParams{Ref, Continuation}` and `JobsListResponse{Data any}`.
- Produces: `JobActivityTree`, `JobActivitySession`, `JobActivityEntry`, `JobActivityJob`, `JobActivityDelegate`, `JobActivityCounts`, `JobActivityBranchState`.
- Produces: `NotifySerfJobsTreeUpdated` and `JobsTreeUpdatedParams{ThreadID, Ref, Revision}`.
- Keeps: `JobsOutputParams{Ref, JobID, MaxBytes}` and `JobOutputTail` unchanged.

- [ ] **Step 1: Write failing AppWire shape and catalog tests**

Add tests that marshal a two-level tree and assert exact discriminators and IDs:

```go
func TestJobsListReplacementTreeWireShape(t *testing.T) {
	payload := appwire.JobActivityTree{
		Revision: 7,
		Root: appwire.JobActivitySession{
			SessionID: "root", Ref: "local:root", Label: "Root",
			Counts: appwire.JobActivityCounts{Active: 2, Failed: 0, Completed: 1, Complete: true},
			Entries: []appwire.JobActivityEntry{
				{Kind: "shell", Job: &appwire.JobActivityJob{JobID: "job_shell", OwnerSessionID: "root", OwnerRef: "local:root", Type: "shell", Status: "running", Terminal: false}},
				{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{DelegateID: "dlg_1", ChildSessionID: "child", ChildRef: "local:child"}},
			},
		},
	}
	got, err := json.Marshal(payload)
	if err != nil { t.Fatal(err) }
	for _, want := range []string{`"revision":7`, `"kind":"shell"`, `"delegateId":"dlg_1"`, `"ownerRef":"local:root"`} {
		if !bytes.Contains(got, []byte(want)) { t.Fatalf("wire %s missing %s", got, want) }
	}
}
```

Also extend notification-catalog tests to require `serf/jobs/treeUpdated` with `JobsTreeUpdatedParams`.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./appwire -run 'TestJobsListReplacementTreeWireShape|TestNotificationCatalog' -count=1
```

Expected: compile failure because the activity-tree types and notification constant do not exist.

- [ ] **Step 3: Add the exact protocol types**

Use pointer payloads to keep the tagged union unambiguous:

```go
type JobsListParams struct {
	Ref          string `json:"ref,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

type JobActivityCounts struct {
	Active    int  `json:"active"`
	Failed    int  `json:"failed"`
	Completed int  `json:"completed"`
	Complete  bool `json:"complete"`
}

type JobActivityBranchState struct {
	Error        string `json:"error,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

type JobActivityJob struct {
	JobID          string `json:"jobId"`
	OwnerSessionID string `json:"ownerSessionId"`
	OwnerRef       string `json:"ownerRef"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	Outcome        string `json:"outcome,omitempty"`
	Terminal       bool   `json:"terminal"`
	Background     bool   `json:"background"`
	HasOutput      bool   `json:"hasOutput"`
	Description    string `json:"description"`
	Command        string `json:"command,omitempty"`
	Task           string `json:"task,omitempty"`
	Reason         string `json:"reason,omitempty"`
	StartedAt      string `json:"startedAt"`
	EndedAt        string `json:"endedAt,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	OutputBytes    int64  `json:"outputBytes"`
}

type JobActivityDelegate struct {
	DelegateID    string                 `json:"delegateId"`
	ChildSessionID string                `json:"childSessionId"`
	ChildRef      string                 `json:"childRef"`
	Mandate       string                 `json:"mandate,omitempty"`
	Turns         []JobActivityJob       `json:"turns"`
	Child         *JobActivitySession    `json:"child,omitempty"`
	Branch        JobActivityBranchState `json:"branch"`
}

type JobActivityEntry struct {
	Kind     string               `json:"kind"` // shell | delegate
	Job      *JobActivityJob      `json:"job,omitempty"`
	Delegate *JobActivityDelegate `json:"delegate,omitempty"`
}

type JobActivitySession struct {
	SessionID string                 `json:"sessionId"`
	Ref       string                 `json:"ref"`
	Label     string                 `json:"label"`
	Aggregate string                 `json:"aggregate"`
	Counts    JobActivityCounts      `json:"counts"`
	Entries   []JobActivityEntry     `json:"entries"`
	Branch    JobActivityBranchState `json:"branch"`
}

type JobActivityTree struct {
	Revision uint64             `json:"revision"`
	Root     JobActivitySession `json:"root"`
}
```

Write explicit JSON tags for every field in `appwire/types.go`; do not rely on Go field-name defaults. Keep `JobsListResponse.Data any` so an old daemon's flat array reaches the runtime parser and becomes the capability-gap state instead of failing transport decoding.

- [ ] **Step 4: Register the notification and regenerate**

Add `NotifySerfJobsTreeUpdated = "serf/jobs/treeUpdated"`, its params type, and the notification catalog entry. Then run:

```bash
make generate
```

Expected: only the generated protocol Markdown and TypeScript files change in addition to hand-written AppWire files.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test ./appwire -count=1
make lint-generated
git diff --check
```

Commit only Task 1 files:

```bash
git add appwire/types.go appwire/protocol.go appwire/protocol_test.go docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(appwire): replace jobs list with activity tree contract"
```

---

### Task 2: Build the pure job/delegate activity projection

**Files:**
- Create: `agent/jobs_activity.go`
- Create: `agent/jobs_activity_test.go`
- Modify: `agent/jobs_panel.go`
- Modify: `agent/jobs_panel_test.go`

**Interfaces:**
- Consumes: AppWire activity types from Task 1.
- Produces: `projectActivitySession(snapshot activitySessionSnapshot, budget *activityBudget) appwire.JobActivitySession`.
- Produces: `activityOutcome(status jobstore.Status) (terminal bool, outcome string)` and `aggregateActivity(entries []appwire.JobActivityEntry, branch appwire.JobActivityBranchState) (appwire.JobActivityCounts, string)`.
- Produces: a union keyed by job ID where durable order wins and live-only jobs use `(StartedAt, JobID)` ordering.

- [ ] **Step 1: Write failing projection tests**

Create table tests for these exact cases:

```go
func TestProjectActivitySession_GroupsDelegateTurnsOnce(t *testing.T) {
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", Label: "Root",
		Jobs: []*jobstore.JobRecord{
			{JobID: "job_a", Type: jobstore.JobShell, OwnerSessionID: "root", Status: jobstore.StatusCompleted, StartedAt: time.Unix(1, 0)},
			{JobID: "job_d1", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusCompleted, StartedAt: time.Unix(2, 0)},
			{JobID: "job_d2", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusRunning, StartedAt: time.Unix(3, 0)},
		},
		Delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_1", ChildSessionID: "child", TranscriptRef: "local:child"}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 2 { t.Fatalf("entries=%d, want shell + one delegate", len(got.Entries)) }
	if turns := got.Entries[1].Delegate.Turns; len(turns) != 2 || turns[1].JobID != "job_d2" { t.Fatalf("turns=%+v", turns) }
}
```

Add separate tests for active/failed/completed counts, unknown non-terminal status, cancelled/stopped completion, unavailable precedence, owner refs, live-only insertion, and no duplicate after durable reconciliation.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./agent -run 'TestProjectActivitySession|TestAggregateActivity|TestMergeActivityRecords' -count=1
```

Expected: compile failure because `activitySessionSnapshot` and projection functions do not exist.

- [ ] **Step 3: Implement immutable snapshot projection**

Define an internal snapshot that contains no locks or live `Session` pointers:

```go
type activitySessionSnapshot struct {
	SessionID string
	Ref       string
	Label     string
	Jobs      []*jobstore.JobRecord
	LiveJobs  map[string]*jobstore.JobRecord
	Delegates map[string]*jobstore.DelegateRecord
	Children  map[string]*activitySessionSnapshot // child session ID
	Errors    map[string]error                    // child session ID
}
```

Clone records before merging. Build delegate groups by `DelegateID`, anchor each group at its earliest turn, and emit shell entries directly. Reject malformed delegate links as branch errors; never attach a job by task text or timestamp alone.

Implement the outcome mapping exactly:

```go
func activityOutcome(status jobstore.Status) (bool, string) {
	switch status {
	case jobstore.StatusRunning:
		return false, ""
	case jobstore.StatusFailed, jobstore.StatusExhausted:
		return true, "failure"
	case jobstore.StatusCompleted:
		return true, "success"
	case jobstore.StatusCancelled, jobstore.StatusStopped:
		return true, "neutral"
	default:
		return status.IsTerminal(), ""
	}
}
```

Aggregate work units from shell jobs and delegate turns, recursively including loaded child branches but never counting the subagent row itself.

- [ ] **Step 4: Keep output-tail code and remove only obsolete flat projection**

Move reusable job-to-wire field projection into:

```go
func projectActivityJob(rec *jobstore.JobRecord, ownerRef string) appwire.JobActivityJob
```

Keep `JobOutputTail`, `jobOutputTailFrom`, `Session.JobOutputTail`, and `LoadSessionJobOutputTail`. Remove `JobSummary`, `JobSummaries`, and `LoadSessionJobList` only after Task 4 moves every caller.

- [ ] **Step 5: Verify and commit**

```bash
go test ./agent -run 'Activity|JobOutputTail' -count=1
git diff --check
git add agent/jobs_activity.go agent/jobs_activity_test.go agent/jobs_panel.go agent/jobs_panel_test.go
git commit -m "feat(agent): project session activity trees"
```

---

### Task 3: Add live and durable traversal, bounds, and continuation

**Files:**
- Modify: `agent/jobs_activity.go`
- Modify: `agent/jobs_activity_test.go`
- Create: `agent/jobs_activity_past.go`
- Create: `agent/jobs_activity_past_test.go`
- Read/reuse: `agent/jobs_nested.go:48-215`
- Read/reuse: `agent/internal/jobstore/store.go:151-193`

**Interfaces:**
- Produces: `func (s *Session) JobActivityTree(params appwire.JobsListParams) (appwire.JobActivityTree, error)`.
- Produces: `func LoadSessionJobActivityTree(stateDir, sessionID string, params appwire.JobsListParams) (appwire.JobActivityTree, error)`.
- Produces: `encodeActivityContinuation(activityContinuation) string` and `decodeActivityContinuation(token, expectedRoot string) (activityContinuation, error)`.
- Bounds: 2,000 work units, 32 new levels, 4 MiB encoded JSON.

- [ ] **Step 1: Write failing live traversal tests**

Build a parent, child, and grandchild with real temporary jobstores. Assert shell jobs appear under their owner, repeated delegate turns form one row, and a closed child remains available through durable records. Reuse existing test helpers from `jobs_nested_test.go`; do not mock jobstore internals.

Add this bound assertion:

```go
func TestJobActivityTree_TruncatesWithScopedContinuation(t *testing.T) {
	s := buildActivityTreeWithJobs(t, activityMaxWorkUnits+1)
	got, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil { t.Fatal(err) }
	branch := firstTruncatedBranch(t, got.Root)
	if !branch.Truncated || branch.Continuation == "" { t.Fatalf("branch=%+v", branch) }
	if _, err := decodeActivityContinuation(branch.Continuation, "other-root"); err == nil {
		t.Fatal("continuation accepted for another root")
	}
}
```

Define `activityMaxWorkUnits = 2000`, `activityMaxNewDepth = 32`, and
`activityMaxEncodedBytes = 4 << 20` in `jobs_activity.go`. In the test file,
define `buildActivityTreeWithJobs(t *testing.T, count int) *Session` by creating
one real temporary session/jobstore and appending `count` shell starts, and
define `firstTruncatedBranch(t *testing.T, root appwire.JobActivitySession)
appwire.JobActivityBranchState` as a recursive test-only search that fails the
test when no truncated branch exists.

Also test depth 33, 4 MiB payload pressure, cycles, malformed refs, and unavailable descendants.

- [ ] **Step 2: Write failing exited-tree tests**

Persist session metadata plus parent/child job logs beneath one temporary state directory. Assert `LoadSessionJobActivityTree` follows only child IDs named by durable delegate records. Add a test where a child path outside the supplied state directory is encoded into metadata; the loader must return an unavailable branch and never read it.

- [ ] **Step 3: Run and verify RED**

```bash
go test ./agent -run 'TestJobActivityTree|TestLoadSessionJobActivityTree' -count=1
```

Expected: compile failure because both public loaders and continuation helpers are absent.

- [ ] **Step 4: Implement leaf-lock live snapshots**

Follow `collectDescendantJobs` lock discipline: read one jobstore and its delegate fold, release it, then enumerate direct children. Use `LoadOrdered`, `LoadDelegates`, and `liveJobRecords`; never hold one manager lock while reading a child store.

For live-only records, union by job ID and insert with `(StartedAt, JobID)`. For closed or absent children, call the durable loader using the same `s.stateDir`. Cycle detection uses a recursion-path set of session IDs and yields `Branch.Error = "cycle detected"`.

- [ ] **Step 5: Implement durable traversal and safe continuation**

Define the opaque payload:

```go
type activityContinuation struct {
	Version   int      `json:"v"`
	RootID    string   `json:"root"`
	SessionID string   `json:"session"`
	Path      []string `json:"path"` // delegate IDs from root
}
```

Base64url-encode JSON without padding. Decode with strict version/root checks, validate every path hop against durable delegate records, cap token length at 16 KiB, and reject duplicate/cyclic path IDs. A continuation response returns the same root envelope with only the requested branch populated so the client can graft it by stable typed ID.

Enforce node/depth budgets during projection. After projection, marshal once; if it exceeds 4 MiB, back out complete trailing branch entries until it fits and place continuation on the first omitted branch. Never split one job object.

- [ ] **Step 6: Verify and commit**

```bash
go test ./agent -run 'Activity|JobOutputTail' -count=1
go test ./agent/internal/jobstore -count=1
git diff --check
git add agent/jobs_activity.go agent/jobs_activity_test.go agent/jobs_activity_past.go agent/jobs_activity_past_test.go
git commit -m "feat(agent): traverse bounded activity history"
```

---

### Task 4: Replace daemon and hub jobs-list providers

**Files:**
- Modify: `server/server.go:555-563`
- Modify: `server/appwire_runtime.go:893-905`
- Modify: `server/server_test.go`
- Modify: `cmd/serf/serve.go:80-100,831-834`
- Modify: `cmd/serf/serve_residual_fuzz_test.go`
- Modify: `cmd/serf-hub/app_jobs.go`
- Modify: `cmd/serf-hub/app_jobs_test.go`
- Modify: `cmd/serf-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/serf-hub/internal/appsource/local_daemon_test.go`

**Interfaces:**
- Consumes: `Session.JobActivityTree` and `LoadSessionJobActivityTree` from Task 3.
- Produces: `SetJobsFunc(func(appwire.JobsListParams) (any, error))`.
- Keeps: `Source.ListJobs(context.Context, appwire.JobsListParams)` unchanged.

- [ ] **Step 1: Write failing daemon handler tests**

Change the server test provider to assert params reach the callback:

```go
srv.SetJobsFunc(func(got appwire.JobsListParams) (any, error) {
	if got.Ref != "local:root" || got.Continuation != "next" { t.Fatalf("params=%+v", got) }
	return appwire.JobActivityTree{Root: appwire.JobActivitySession{SessionID: "root"}}, nil
})
```

Call `serf/jobs/list` with both fields and assert the tree response.

- [ ] **Step 2: Write failing hub fallback tests**

Replace flat fallback fixtures with persisted parent/child activity. Prove:

1. live local source wins;
2. only the dead-session error triggers durable fallback;
3. fallback calls `LoadSessionJobActivityTree` with the indexed entry's state dir;
4. non-local refs never read local state;
5. continuation reaches live and past providers unchanged;
6. unreadable root store is an error, not an empty tree.

- [ ] **Step 3: Run and verify RED**

```bash
go test ./server ./cmd/serf ./cmd/serf-hub/... -run 'JobsList|ActivityTree|JobsOutput' -count=1
```

Expected: compile failures at the old no-argument `SetJobsFunc` interface and flat fallback.

- [ ] **Step 4: Replace provider signatures and callers**

Implement:

```go
func (s *Server) SetJobsFunc(fn func(appwire.JobsListParams) (any, error))

func (s *Server) handleAppJobsList(_ context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	// read fn under RLock, call outside lock
	data, err := fn(params)
	return appwire.JobsListResponse{Data: data}, err
}
```

In `serve.go`, ignore the incoming ref only after validating that it names the current local session, then call `getSession().JobActivityTree(params)`. Keep nil provider behavior as the capability gap.

In `pastJobsListResponse`, call `agent.LoadSessionJobActivityTree(entry.StateDir, entry.Meta.ID, params)`. Preserve the existing past-index gate.

- [ ] **Step 5: Delete obsolete flat projection callers and verify**

Search must return no production callers:

```bash
rg 'JobSummaries|LoadSessionJobList|\[\]JobSummary' --glob '*.go'
```

Remove the obsolete functions/types left in Task 2 only when the search contains tests or declarations alone.

Run:

```bash
go test ./server ./cmd/serf ./cmd/serf-hub/... -run 'JobsList|ActivityTree|JobsOutput' -count=1
git diff --check
```

- [ ] **Step 6: Commit**

```bash
git add server/server.go server/appwire_runtime.go server/server_test.go cmd/serf/serve.go cmd/serf/serve_residual_fuzz_test.go cmd/serf-hub/app_jobs.go cmd/serf-hub/app_jobs_test.go cmd/serf-hub/internal/appsource/local_daemon.go cmd/serf-hub/internal/appsource/local_daemon_test.go agent/jobs_panel.go agent/jobs_panel_test.go
git commit -m "feat(hub): serve recursive session activity"
```

---

### Task 5: Invalidate the root tree for descendant lifecycle changes

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/session_init.go`
- Modify: `agent/subagents.go`
- Modify: `agent/events/payloads.go`
- Modify: `agent/events/eventdata.go`
- Modify: relevant event/session lifecycle tests
- Modify: `internal/appprojector/appwire_projection.go:931-977`
- Modify: `internal/appprojector/appwire_projection_test.go`
- Modify: `cmd/serf-hub/frontend/src/protocol/model.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts:861-865`
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts:1052-1068`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.test.ts`

**Interfaces:**
- Produces: one `jobTreeClock` shared by root and descendants: `{rootSessionID string; revision atomic.Uint64}`.
- Adds to `JobStartedData` and `JobFinishedData`: `RootSessionID string`, `TreeRevision uint64`.
- Emits: `serf/jobs/treeUpdated` targeted at `local:<rootSessionID>`.
- Produces: `ThreadModel.jobsTreeRevision: number | null`.

- [ ] **Step 1: Write failing shared-clock agent tests**

Create a root, child, and grandchild. Emit one shell start from each and assert revisions `1,2,3` all carry the root ID. Restore a child and assert it shares the root clock instead of starting at zero.

```go
if child.jobTreeClock != root.jobTreeClock { t.Fatal("child did not inherit root tree clock") }
```

- [ ] **Step 2: Write failing projector tests**

For each job start/finish event with `RootSessionID: "root", TreeRevision: 9`, require exactly the existing job notification plus one tree-update notification:

```go
params := notificationParams[appwire.JobsTreeUpdatedParams](t, out, appwire.NotifySerfJobsTreeUpdated)
if params.Ref != "local:root" || params.Revision != 9 { t.Fatalf("params=%+v", params) }
```

Do not emit tree updates when root identity/revision is absent in old persisted fixtures.

- [ ] **Step 3: Write failing frontend reducer/store tests**

Route a `serf/jobs/treeUpdated` frame for `local:root` while the currently open event source is a child. Assert only the root model's `jobsTreeRevision` and `jobsUpdatedAt` change; `lastFrameAt` on the root must not claim conversational activity from a nested job.

- [ ] **Step 4: Run and verify RED**

```bash
go test ./agent ./internal/appprojector -run 'TreeRevision|JobsTreeUpdated' -count=1
cd cmd/serf-hub/frontend && npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts
```

Expected: missing clock fields, notification type, and model field.

- [ ] **Step 5: Implement root clock and projector notification**

Initialize the root clock once; pass the same pointer in subagent spawn/restore config. At the durable job-start and job-finish emission points, increment after the jobstore event succeeds and stamp the emitted session event. Never bump for output bytes alone because the tree output body is lazy.

In the projector, append:

```go
appwire.JobsTreeUpdatedParams{
	ThreadID: data.RootSessionID,
	Ref:      "local:" + data.RootSessionID,
	Revision: data.TreeRevision,
}
```

Keep job started/finished payloads and transcript-card routing intact.

- [ ] **Step 6: Implement root routing in the frontend**

Add `jobsTreeRevision` to model initialization and fixtures. In the store notification ingress, locate the model by `params.ref`, ignore revisions `<=` the stored revision, and apply the reducer. The reducer sets `jobsTreeRevision` and `jobsUpdatedAt` but leaves `lastFrameAt` unchanged.

- [ ] **Step 7: Regenerate, verify, and commit**

```bash
make generate
go test ./agent ./internal/appprojector -run 'TreeRevision|JobsTreeUpdated|JobStarted|JobFinished' -count=1
cd cmd/serf-hub/frontend && npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts
cd ../../.. && make lint-generated
git diff --check
```

Commit all Task 5 hand-written and regenerated files:

```bash
git add agent/session.go agent/session_init.go agent/subagents.go agent/events internal/appprojector appwire docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol cmd/serf-hub/frontend/src/stores/threads.ts cmd/serf-hub/frontend/src/stores/threads.test.ts
git commit -m "feat(activity): invalidate roots for descendant jobs"
```

---

### Task 6: Parse and reconcile recursive activity data in the client

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.test.ts`

**Interfaces:**
- Produces: `ActivityTree`, `ActivitySessionNode`, `ActivityShellEntry`, `ActivityDelegateEntry`, `ActivityCounts`, `ActivityBranchState`.
- Produces: `parseActivityTree(data: unknown): ActivityTree | null`.
- Produces: `activityNodeID`, `defaultExpandedIDs`, and `reconcileActivityState`.
- Consumes: store calls `listJobs(ref, continuation?)` and `jobOutput(ownerRef, jobID)`.

- [ ] **Step 1: Write failing parser tests with wire-true fixtures**

Use camelCase fixtures copied from Task 1's JSON tags. Cover a root shell, repeated delegate turns, nested child shell, unknown status, unavailable child, truncated branch, malformed sibling, and old flat array.

```ts
expect(parseActivityTree([{ jobId: "old-flat-row" }])).toBeNull();
expect(parseActivityTree(validTree)?.root.entries[1].kind).toBe("delegate");
```

A malformed sibling is omitted and marks its owning session incomplete; it must not erase valid siblings.

- [ ] **Step 2: Write failing identity/default/reconcile tests**

Assert exact IDs:

```ts
expect(activityNodeID({ kind: "session", sessionId: "root" })).toBe("session:root");
expect(activityNodeID({ kind: "delegate", delegateId: "dlg_1" })).toBe("delegate:dlg_1");
expect(activityNodeID({ kind: "shell", jobId: "job_1" })).toBe("job:job_1");
```

Assert default expansion includes the root and every ancestor of active work, excludes completed-only branches, preserves explicit user choices on refresh, and falls selection back to the nearest surviving owner.

- [ ] **Step 3: Run and verify RED**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/activityData.test.ts src/stores/threads.test.ts
```

Expected: module-not-found and missing continuation argument failures.

- [ ] **Step 4: Implement strict recursive parsing**

Use `isPlainObject`, `readString`, `readBoolean`, and `readNumber` helpers. Require the non-optional identity/discriminator fields; keep unknown status text. Parse recursively with an explicit depth counter capped at 64 client-side to defend against hostile payloads. At depth 65, return a truncated/unavailable branch rather than overflowing the JS stack.

Use discriminated unions:

```ts
export type ActivityEntry = ActivityShellEntry | ActivityDelegateEntry;
export interface ActivityShellEntry { kind: "shell"; job: ActivityJob; }
export interface ActivityDelegateEntry { kind: "delegate"; delegate: ActivityDelegate; }
```

- [ ] **Step 5: Implement reconciliation and store calls**

`defaultExpandedIDs(tree)` walks active branches. `reconcileActivityState(previous, next)` preserves explicit disclosure IDs that still exist, auto-opens a newly active ancestor path, preserves surviving selection, and returns a fallback owner ID plus `selectionPruned: true` when needed.

Keep the AppWire method name in the store:

```ts
listJobs: async (ref: string, continuation?: string) =>
  (await requireClient().request("serf/jobs/list", { ref, ...(continuation ? { continuation } : {}) })).data
```

- [ ] **Step 6: Verify and commit**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/activityData.test.ts src/stores/threads.test.ts
npm run typecheck
npm run lint
cd ../../..
git add cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts cmd/serf-hub/frontend/src/stores/threads.ts cmd/serf-hub/frontend/src/stores/threads.test.ts
git commit -m "feat(webui): parse and reconcile activity trees"
```

---

### Task 7: Build the Activity tree and in-place inspector

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityInspector.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css`

**Interfaces:**
- Produces: `ActivityPanelProps` equal to the old panel props and `ActivityPanelHandle{open()}`.
- `ActivityTree` consumes parsed tree, expanded IDs, selected ID, and callbacks.
- `ActivityInspector` consumes the selected node and calls `jobOutput(ownerRef, jobId)` lazily.

- [ ] **Step 1: Write failing ActivityPanel state tests**

Copy the existing JobsPanel harness, replace flat fixtures with tree fixtures, and cover:

1. trigger text `Activity` before first load and `Activity · 3` after a complete active count;
2. open fetch, lifecycle refetch, established closed-trigger refetch, and hidden-trigger suppression;
3. initial, stale-refresh, empty, capability-gap, exited, and partial-branch states;
4. active paths open by default and completed branches collapsed;
5. refresh preserves disclosure and selection;
6. continuation grafts a branch without resetting selection;
7. removed selection falls back and shows “This activity is no longer retained.”

- [ ] **Step 2: Write failing tree accessibility tests**

Render root → delegate → shell and assert `role="tree"`, `role="treeitem"`, levels, `aria-expanded`, visible-row roving `tabIndex`, and keyboard behavior:

```ts
await user.keyboard("{ArrowRight}{ArrowDown}{Enter}");
expect(onSelect).toHaveBeenCalledWith("delegate:dlg_1");
await user.keyboard("{ArrowLeft}");
expect(onExpandedChange).toHaveBeenCalledWith(expect.not.arrayContaining(["delegate:dlg_1"]));
```

Test status text alongside every color indicator and neutral unknown status.

- [ ] **Step 3: Write failing inspector tests**

For a shell job, assert metadata renders immediately and output fetch starts only after selection. For a delegate row, assert mandate, ordered turns, latest output/report availability, child aggregate, branch error, and **Open transcript**. Test output retry and **Refresh output** without collapsing the tree.

- [ ] **Step 4: Run and verify RED**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/ActivityTree.test.tsx
```

Expected: module-not-found failures.

- [ ] **Step 5: Implement the panel state machine**

Use one reducer with explicit states instead of interdependent booleans:

```ts
type ActivityLoadState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; tree: ActivityTree; staleError?: LoadFailure }
  | { kind: "unsupported" }
  | { kind: "failed"; error: LoadFailure }
  | { kind: "ended"; tree?: ActivityTree };
```

On `jobsUpdatedAt`, refetch the loaded root page, reconcile by stable ID, and keep last-good data on failure. A continuation request updates only the targeted branch. Abort or ignore all stale promises on session-ref change/unmount.

- [ ] **Step 6: Implement recursive tree keyboard behavior**

Flatten visible nodes for focus movement while rendering recursively for semantics. Use one focused ID with roving `tabIndex`. Right expands or enters the first child; Left collapses or moves to parent; Up/Down move visible siblings in document order; Enter/Space select. Restore focus by stable ID after refresh.

Use existing `StatusDot`, `Chip`, `Button`, `EmptyState`, `Sheet`, and token variables. Do not introduce color literals.

- [ ] **Step 7: Implement lazy inspectors and responsive master-detail state**

Desktop renders tree and inspector side by side. Under the existing mobile breakpoint, selection switches to inspector-only and a **Back to activity** button restores tree and focus to the selected row. Reuse the focus-return pattern from `panes/backToParentAction.tsx`, not its navigation behavior.

Subagent report content comes from the latest delegate turn's `ownerRef`/`jobId` via `jobOutput`; tree data supplies availability, mandate, and transcript link.

- [ ] **Step 8: Verify and commit**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/ActivityTree.test.tsx
npm run typecheck
npm run lint
cd ../../..
git add cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityInspector.tsx cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css
git commit -m "feat(webui): add session activity inspector"
```

---

### Task 8: Replace Jobs integration and delete the flat UI

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
- Delete: `cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.tsx`
- Delete: `cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.test.tsx`
- Delete: `cmd/serf-hub/frontend/src/panes/session/chrome/jobData.ts`
- Delete: `cmd/serf-hub/frontend/src/panes/session/chrome/jobData.test.ts`
- Delete: `cmd/serf-hub/frontend/src/panes/session/chrome/jobspanel.module.css`

**Interfaces:**
- Consumes: `ActivityPanel` and `ActivityPanelHandle` from Task 7.
- Keeps: the same wide-trigger and narrow-overflow opening contract.

- [ ] **Step 1: Change integration tests first**

Replace mocks and assertions so wide chrome renders **Activity**, narrow chrome places **Activity** in overflow, and selecting the overflow item calls `ActivityPanelHandle.open()`. Assert no **Jobs** label remains.

- [ ] **Step 2: Run and verify RED**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/SessionChrome.test.tsx
```

Expected: failures because SessionChrome still imports and labels JobsPanel.

- [ ] **Step 3: Replace the integration**

Make only the focused substitutions:

```tsx
import { ActivityPanel, type ActivityPanelHandle } from "./ActivityPanel";
const activityRef = useRef<ActivityPanelHandle>(null);
// overflow item: { label: "Activity", onSelect: () => activityRef.current?.open() }
<ActivityPanel ref={activityRef} sessionRef={sessionRef} model={model} now={now} hideTrigger={collapsed} />
```

Do not change Tasks, Details, menu ordering, cadence, or unrelated chrome geometry.

- [ ] **Step 4: Delete flat UI files and prove no references remain**

```bash
rg 'JobsPanel|jobData|jobspanel\.module|>Jobs<|"Jobs"' cmd/serf-hub/frontend/src
```

Expected: no production references; update any test names or comments that still describe the old panel.

- [ ] **Step 5: Verify and commit**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/SessionChrome.test.tsx src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/chrome/activityData.test.ts
npm run typecheck
npm run lint
cd ../../..
git add -- cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/jobData.ts cmd/serf-hub/frontend/src/panes/session/chrome/jobData.test.ts cmd/serf-hub/frontend/src/panes/session/chrome/jobspanel.module.css
git commit -m "refactor(webui): replace jobs sheet with activity tree"
```

The explicit path list stages the Task 8 edits and deletions without touching
unrelated files. Inspect `git diff --cached --name-only` before committing.

---

### Task 9: Add real-browser depth and responsive guards

**Files:**
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/activity-tree-responsive/case.json`
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/activity-tree-responsive/harness.html`
- Create: `cmd/serf-hub/frontend/scripts/layoutguard/cases/activity-tree-responsive/assert.mjs`
- Modify if necessary: `cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css`

**Interfaces:**
- Proves: deep rows do not create a sheet-level horizontal scroller; mobile shows exactly one readable pane; output alone may scroll horizontally.

- [ ] **Step 1: Create a wire-representative static harness**

Render eight nested delegate rows, one long shell command, desktop tree+inspector, and mobile tree/inspector variants. Include `styles/global.css`, `styles/tokens.css`, Sheet styles, and Activity styles. Use `data-role` markers for measurement.

- [ ] **Step 2: Write geometry assertions**

In `assert.mjs`, measure computed overflow rather than raw `scrollWidth` alone:

```js
assert.equal(getComputedStyle(treePane).overflowX, "hidden");
assert.ok(treePane.scrollWidth <= treePane.clientWidth, "tree pane must not scroll sideways");
assert.equal(desktopVisiblePanes, 2);
assert.equal(mobileVisiblePanes, 1);
assert.match(getComputedStyle(outputPre).overflowX, /auto|scroll/);
```

Also assert deep labels use ellipsis/overflow clipping and the mobile Back control stays inside the viewport.

- [ ] **Step 3: Run RED by temporarily removing one required rule**

Run:

```bash
cd cmd/serf-hub/frontend
npm run layoutguard -- --case activity-tree-responsive
```

Temporarily remove the tree-pane `min-width: 0` or overflow rule and confirm the guard fails naming the tree pane. Restore the mutation.

- [ ] **Step 4: Run the real guard and overflow integration**

```bash
npm run layoutguard -- --case activity-tree-responsive
npm run overflowguard
```

Expected: PASS at all configured widths.

- [ ] **Step 5: Commit**

```bash
cd ../../..
git add cmd/serf-hub/frontend/scripts/layoutguard/cases/activity-tree-responsive cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css
git commit -m "test(webui): guard activity tree responsiveness"
```

---

### Task 10: End-to-end deterministic verification and cleanup

**Files:**
- Modify only if failures expose a root cause in files already covered above.
- Do not modify the design spec unless implementation reveals a genuine contract contradiction; report that before changing it.

**Interfaces:**
- Verifies the complete replacement and leaves no obsolete symbols or scratch artifacts.

- [ ] **Step 1: Run focused Go tests**

```bash
go test ./appwire ./agent ./server ./internal/appprojector ./cmd/serf ./cmd/serf-hub/... -run 'Activity|JobsList|JobsOutput|TreeUpdated|JobStarted|JobFinished' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run focused frontend tests**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/chrome/activityData.test.ts src/panes/session/chrome/SessionChrome.test.tsx src/protocol/reducer.test.ts src/stores/threads.test.ts
npm run typecheck
npm run lint
npm run layoutguard -- --case activity-tree-responsive
npm run overflowguard
```

Expected: PASS.

- [ ] **Step 3: Run generated, build, and repository gates**

From repository root:

```bash
make lint-generated
make build
make test
```

Expected: PASS. If a failure repeats after a focused fix, stop and investigate the upstream root cause; do not mute, skip, or reduce coverage.

- [ ] **Step 4: Audit the replacement and workspace**

```bash
rg 'JobSummary|JobSummaries|LoadSessionJobList|JobsPanel|jobData|jobspanel\.module' --glob '!docs/superpowers/**'
rg 'serf/jobs/treeUpdated|JobActivityTree|ActivityPanel' appwire agent internal server cmd/serf cmd/serf-hub
git status --short
git diff --check
```

Expected: the first search has no production results; the second shows every intended layer; status contains no scratch files. Preserve unrelated user changes exactly as found.

- [ ] **Step 5: Request code review**

Invoke `superpowers:requesting-code-review`. Give the reviewer the design spec, this plan, commit range, acceptance criteria, and commands/results from Steps 1–4. Fix every confirmed issue with a focused failing test before implementation.

- [ ] **Step 6: Commit any review fixes and record final evidence**

Stage only reviewed activity-tree files, use a focused message such as:

```bash
git commit -m "fix(activity): address final tree review"
```

If review requires no code changes, do not create an empty commit. Report the final commit range, exact passing commands, generated-file status, and any unrelated dirty paths left untouched.
