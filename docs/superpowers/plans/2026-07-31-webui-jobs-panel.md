# WebUI Jobs Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Jobs panel to the serf webui session chrome listing all shell and delegate jobs for the current session, push-updated live, with lazy output tails.

**Architecture:** Mirrors the tasks panel pipeline end to end: daemon appwire handlers (`serf/jobs/list`, `serf/jobs/output`) backed by the session's jobstore, a `serf/job/updated` push via the existing `EventJobStarted`/`EventJobFinished` projection path, hub handlers with a dead-session fallback to the durable `jobs.jsonl`, and a `JobsPanel.tsx` mirroring `TasksPanel.tsx`.

**Tech Stack:** Go (appwire, server, agent, internal/appprojector, cmd/serf-hub), React + TypeScript + CSS modules + vitest (cmd/serf-hub/frontend).

**Spec:** `docs/superpowers/specs/2026-07-31-webui-jobs-panel-design.md`

**Status:** implemented and merged; superseded in places. Read the
post-implementation addendum at the end of this file before treating any task
below as current — `serf/job/updated` does not exist, and several task
interfaces changed shape after the branch merged. The spec has been
reconciled in place and describes what shipped.

## Global Constraints

- Tests are deterministic: no provider credentials, no network (per `docs/testing.md` / AGENTS.md).
- Follow the existing tasks-panel templates exactly: naming, error taxonomy, comment style.
- Internal jobstore types (`agent/internal/jobstore`) must not leak into `cmd/serf-hub` — the hub reaches durable job data only through exported `agent` package functions (the `LoadSessionObserverGrants` pattern in `agent/observer_grants.go`).
- Go verification: `go test -count=1 <changed packages>` per task; `go build ./...` before each commit.
- Frontend verification: `cd cmd/serf-hub/frontend && npx vitest run <changed test files>` per task; `npm run lint` (biome) must stay clean.
- Generated files (`docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts`) are regenerated with `make generate`, never hand-edited; `make lint-generated` must pass.

---

### Task 1: appwire types, protocol catalog, client methods, codegen

**Files:**
- Modify: `appwire/types.go` (method constants near line 31; notification constants near line 110; params types near line 1138)
- Modify: `appwire/protocol.go` (request catalog near line 115; notification catalog near line 236)
- Modify: `appwire/client.go` (near `func (c *Client) TasksList`, line 442)
- Regenerate: `docs/appwire-protocol.md`, `cmd/serf-hub/frontend/src/protocol/types.gen.ts`
- Test: `appwire/` round-trip test (extend the existing typed-request test table; find via `rg "MethodSerfTasksList" appwire/*_test.go`)

**Interfaces:**
- Produces (everything downstream uses these exact names):
  - `appwire.MethodSerfJobsList = "serf/jobs/list"`
  - `appwire.MethodSerfJobsOutput = "serf/jobs/output"`
  - `appwire.NotifySerfJobUpdated = "serf/job/updated"`
  - `appwire.JobsListParams{ Ref string }`, `appwire.JobsListResponse{ Data any }`
  - `appwire.JobsOutputParams{ Ref string; JobID string; MaxBytes int64 }`, `appwire.JobsOutputResponse{ Data any }`
  - `appwire.JobUpdatedParams{ ThreadID string; Ref string; JobID string; Status string }`
  - `func (c *Client) JobsList(ctx context.Context, params JobsListParams) (JobsListResponse, error)`
  - `func (c *Client) JobOutput(ctx context.Context, params JobsOutputParams) (JobsOutputResponse, error)`

- [ ] **Step 1: Add the failing catalog/coverage test**

The appwire package has tests that walk the protocol catalog (find them: `rg "catalog|Catalog" appwire/*_test.go`). Add a test pinning the new entries if none auto-covers new methods:

```go
func TestJobsCatalogEntries(t *testing.T) {
	methods := map[string]bool{}
	for _, e := range RequestCatalog { // use the real catalog variable name from appwire/protocol.go
		methods[e.Method] = true
	}
	for _, m := range []string{MethodSerfJobsList, MethodSerfJobsOutput} {
		if !methods[m] {
			t.Errorf("request catalog missing %s", m)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 -run TestJobsCatalogEntries ./appwire/`
Expected: FAIL — `undefined: MethodSerfJobsList`

- [ ] **Step 3: Add the wire types**

In `appwire/types.go`, beside `MethodSerfTasksList` (line 31):

```go
MethodSerfJobsList               = "serf/jobs/list"
MethodSerfJobsOutput             = "serf/jobs/output"
```

Beside `NotifySerfTaskUpdated` (line 110):

```go
NotifySerfJobUpdated             = "serf/job/updated"
```

Beside `TaskListParams`/`TaskListResponse` (line 1138):

```go
type JobsListParams struct {
	Ref string `json:"ref,omitempty"`
}

type JobsListResponse struct {
	Data any `json:"data"`
}

// JobsOutputParams reads a byte tail of one job's durable output. MaxBytes
// defaults server-side (4 KiB) and is capped (64 KiB).
type JobsOutputParams struct {
	Ref      string `json:"ref,omitempty"`
	JobID    string `json:"jobId"`
	MaxBytes int64  `json:"maxBytes,omitempty"`
}

type JobsOutputResponse struct {
	Data any `json:"data"`
}
```

Beside `TaskUpdatedParams` (line 407):

```go
// JobUpdatedParams is the params shape for serf/job/updated: one job's
// lifecycle state changed, so a client with an open jobs panel re-fetches
// serf/jobs/list event-driven instead of polling.
type JobUpdatedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	JobID    string `json:"jobId"`
	Status   string `json:"status"`
}
```

- [ ] **Step 4: Add the protocol catalog entries**

In `appwire/protocol.go` beside the `MethodSerfTasksList` entry (line 115):

```go
{MethodSerfJobsList, JobsListParams{}, JobsListResponse{}, ScopeBoth, "Lists the session's jobs (shell and delegate)."},
{MethodSerfJobsOutput, JobsOutputParams{}, JobsOutputResponse{}, ScopeBoth, "Reads a byte tail of one job's output."},
```

Beside the `NotifySerfTaskUpdated` entry (line 236):

```go
{NotifySerfJobUpdated, JobUpdatedParams{}, "A job's lifecycle state changed (started or finished)."},
```

- [ ] **Step 5: Add the client methods**

In `appwire/client.go` beside `TasksList` (line 442; read it first and mirror its body exactly):

```go
func (c *Client) JobsList(ctx context.Context, params JobsListParams) (JobsListResponse, error) {
	// mirror TasksList's body with MethodSerfJobsList / JobsListResponse
}

func (c *Client) JobOutput(ctx context.Context, params JobsOutputParams) (JobsOutputResponse, error) {
	// same shape with MethodSerfJobsOutput / JobsOutputResponse
}
```

- [ ] **Step 6: Run tests and codegen**

Run: `go test -count=1 ./appwire/`
Expected: PASS

Run: `make generate && make lint-generated`
Expected: PASS; `git status` shows regenerated `docs/appwire-protocol.md` and `cmd/serf-hub/frontend/src/protocol/types.gen.ts`.

- [ ] **Step 7: Commit**

```bash
git add appwire/ docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts
git commit -m "appwire: serf/jobs/list, serf/jobs/output, serf/job/updated wire types"
```

---

### Task 2: agent job summary projection + live/past readers

**Files:**
- Create: `agent/jobs_panel.go`
- Test: `agent/jobs_panel_test.go`
- Reference: `agent/observer_grants.go` (the `historicalJobsStat`/`historicalJobsOpen` read-only past-store pattern), `agent/jobs.go:441` (`jobsDir`), `agent/jobs.go:970-992` (`readOutput`, `errJobNotFound`), `agent/internal/jobstore/record.go` (`JobRecord` fields)

**Interfaces:**
- Consumes: `jobstore.Store.LoadOrdered()` (`agent/internal/jobstore/store.go:169`), `jobsDir(stateDir, sessionID)`, `jobstore.Open`
- Produces:
  - `type JobSummary struct` (exact JSON tags below — the frontend parser in Task 8 depends on them)
  - `func SummarizeJobRecord(rec *jobstore.JobRecord) JobSummary`
  - `func (s *Session) JobSummaries() []JobSummary`
  - `type JobOutputTail struct { Tail string; TotalBytes int64; RetainedStart int64; Truncated bool }`
  - `func (s *Session) JobOutputTail(jobID string, maxBytes int64) (JobOutputTail, bool, error)` — bool = job found
  - `func LoadSessionJobList(stateDir, sessionID string) ([]JobSummary, error)` — hub past fallback
  - `func LoadSessionJobOutputTail(stateDir, sessionID, jobID string, maxBytes int64) (JobOutputTail, bool, error)` — hub past fallback

**Defaults:** `maxBytes <= 0` → 4096; `maxBytes > 65536` → 65536. Missing output file → empty tail, `TotalBytes 0`, no error. Unknown job id → `found=false`.

- [ ] **Step 1: Write the failing tests**

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestSummarizeJobRecordShell(t *testing.T) {
	started := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	exit := 0
	rec := &jobstore.JobRecord{
		JobID:       "job_1",
		Type:        jobstore.JobShell,
		Status:      jobstore.StatusCompleted,
		Description: "run tests",
		Command:     "go test ./...",
		Background:  true,
		StartedAt:   started,
		ExitCode:    &exit,
		OutputBytes: 123,
		OutputPath:  "/tmp/out.log",
	}
	got := SummarizeJobRecord(rec)
	if got.JobID != "job_1" || got.Type != "shell" || got.Status != "completed" {
		t.Errorf("identity fields: %+v", got)
	}
	if got.Description != "run tests" || got.Command != "go test ./..." {
		t.Errorf("description/command: %+v", got)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("exit code: %+v", got)
	}
	if !got.HasOutput {
		t.Error("HasOutput should be true when OutputPath is set")
	}
	if got.EndedAt != "" {
		t.Errorf("EndedAt should be empty for nil EndedAt, got %q", got.EndedAt)
	}
}

func TestSummarizeJobRecordDescriptionFallback(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "job_2", Type: jobstore.JobDelegate, Status: jobstore.StatusRunning, Task: "scout the repo"}
	if got := SummarizeJobRecord(rec); got.Description != "scout the repo" {
		t.Errorf("description should fall back to Task, got %q", got.Description)
	}
	rec2 := &jobstore.JobRecord{JobID: "job_3", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Command: "make build"}
	if got := SummarizeJobRecord(rec2); got.Description != "make build" {
		t.Errorf("description should fall back to Command, got %q", got.Description)
	}
}

func TestLoadSessionJobListEmptyWhenNoLog(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadSessionJobList(dir, "01KXXXXXXXXXXXXXXXXXXXXXXX") // any valid-format id; see identifier.MustNewSessionID in tests nearby
	if err != nil {
		t.Fatalf("LoadSessionJobList: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %+v", got)
	}
}

func TestLoadSessionJobListOrdersAndProjects(t *testing.T) {
	dir := t.TempDir()
	sessionID := newTestSessionID(t) // reuse the helper neighboring job tests use to mint session ids; see agent/job_watch_send_test.go
	path := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, id := range []string{"job_a", "job_b"} {
		ts := now.Add(time.Duration(i) * time.Second)
		if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: ts, JobID: id, Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &ts}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionJobList(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSessionJobList: %v", err)
	}
	if len(got) != 2 || got[0].JobID != "job_a" || got[1].JobID != "job_b" {
		t.Errorf("ordered projection: %+v", got)
	}
}

func TestLoadSessionJobOutputTail(t *testing.T) {
	dir := t.TempDir()
	sessionID := newTestSessionID(t)
	// Write a store with one finished job whose OutputPath points at a file.
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(logDir, "job_x.log")
	if err := os.WriteFile(outPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_x", Status: jobstore.StatusCompleted, OutputBytes: 10, TerminalGen: "tg-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_x", 4)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if tail.Tail != "6789" || tail.TotalBytes != 10 || !tail.Truncated {
		t.Errorf("tail: %+v", tail)
	}
	if _, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_nope", 4); err != nil || found {
		t.Errorf("unknown job: found=%v err=%v", found, err)
	}
}
```

(`newTestSessionID` — check how neighboring tests mint session ids, e.g. `rg "MustNewSessionID|newTestSession" agent/*_test.go | head`; use the same idiom.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 -run 'TestSummarizeJobRecord|TestLoadSessionJob' ./agent/`
Expected: FAIL — `undefined: SummarizeJobRecord`

- [ ] **Step 3: Implement `agent/jobs_panel.go`**

```go
package agent

import (
	"os"
	"path/filepath"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// JobSummary is the UI wire projection of one jobstore.JobRecord — the
// shape serf/jobs/list returns and the webui jobs panel renders. Internal
// fields (provenance, restore descriptors, transcript refs, working dir,
// notify state) deliberately stay out.
type JobSummary struct {
	JobID       string `json:"jobId"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	Task        string `json:"task,omitempty"`
	Background  bool   `json:"background"`
	StartedAt   string `json:"startedAt"`
	EndedAt     string `json:"endedAt,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	OutputBytes int64  `json:"outputBytes"`
	HasOutput   bool   `json:"hasOutput"`
}

// JobOutputTail is the serf/jobs/output payload: the last bytes of a job's
// durable output plus the bookkeeping a client needs to say "showing last N
// of M bytes".
type JobOutputTail struct {
	Tail          string `json:"tail"`
	TotalBytes    int64  `json:"totalBytes"`
	RetainedStart int64  `json:"retainedStart"`
	Truncated     bool   `json:"truncated"`
}

const (
	jobOutputTailDefaultBytes = 4096
	jobOutputTailMaxBytes     = 65536
)

func clampJobTailBytes(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return jobOutputTailDefaultBytes
	}
	if maxBytes > jobOutputTailMaxBytes {
		return jobOutputTailMaxBytes
	}
	return maxBytes
}

// SummarizeJobRecord projects one record. Description is the first non-empty
// of Description, Command, Task. HasOutput means a tail read is worth
// attempting: an output path is recorded or bytes were counted.
func SummarizeJobRecord(rec *jobstore.JobRecord) JobSummary {
	if rec == nil {
		return JobSummary{}
	}
	desc := rec.Description
	if desc == "" {
		desc = rec.Command
	}
	if desc == "" {
		desc = rec.Task
	}
	s := JobSummary{
		JobID:       rec.JobID,
		Type:        string(rec.Type),
		Status:      string(rec.Status),
		Reason:      rec.Reason,
		Description: desc,
		Command:     rec.Command,
		Task:        rec.Task,
		Background:  rec.Background,
		StartedAt:   rec.StartedAt.UTC().Format(time.RFC3339),
		ExitCode:    rec.ExitCode,
		OutputBytes: rec.OutputBytes,
		HasOutput:   rec.OutputPath != "" || rec.OutputBytes > 0,
	}
	if rec.EndedAt != nil {
		s.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	return s
}

func summarizeJobRecords(ordered []*jobstore.JobRecord) []JobSummary {
	out := make([]JobSummary, 0, len(ordered))
	for _, rec := range ordered {
		if rec == nil {
			continue
		}
		out = append(out, SummarizeJobRecord(rec))
	}
	return out
}

// JobSummaries is the live-daemon serf/jobs/list payload: every job in the
// session's durable store, in append order. A nil jobManager (a session that
// never started job infrastructure) yields an empty, non-nil slice.
func (s *Session) JobSummaries() []JobSummary {
	if s == nil || s.jobManager == nil {
		return []JobSummary{}
	}
	ordered, err := s.jobManager.store.LoadOrdered()
	if err != nil {
		return []JobSummary{}
	}
	return summarizeJobRecords(ordered)
}

// JobOutputTail is the live-daemon serf/jobs/output payload. found=false
// means no job with that id exists; a found job with no output file yet is
// an empty tail, not an error.
func (s *Session) JobOutputTail(jobID string, maxBytes int64) (JobOutputTail, bool, error) {
	if s == nil || s.jobManager == nil {
		return JobOutputTail{}, false, nil
	}
	content, total, truncated, err := s.jobManager.readOutput(jobID, int(clampJobTailBytes(maxBytes)))
	if err != nil {
		if isJobNotFoundErr(err) { // see note below
			return JobOutputTail{}, false, nil
		}
		return JobOutputTail{}, true, err
	}
	retainedStart := total - int64(len(content))
	if retainedStart < 0 {
		retainedStart = 0
	}
	return JobOutputTail{Tail: content, TotalBytes: total, RetainedStart: retainedStart, Truncated: truncated}, true, nil
}
```

Note for the implementer: `errJobNotFound` (`agent/jobs.go:966`) builds with `fmt.Errorf`, so there is no sentinel to `errors.Is` against. Add one minimally: declare `var errJobNotFoundSentinel = errors.New("job not found")` in `agent/jobs.go` and wrap it in `errJobNotFound` (`fmt.Errorf("job %q not found: %w — use job_list ...", ...)`, preserving the existing message text because job-list tool tests assert on it — run `rg "not found" agent/*jobs*_test.go` and keep them green). Then `isJobNotFoundErr` is `errors.Is(err, errJobNotFoundSentinel)`.

Past readers (same file), mirroring `LoadSessionObserverGrants` (`agent/observer_grants.go:102`) — read-only, no file creation, missing log → empty result:

```go
// LoadSessionJobList reads one local session's durable jobs.jsonl and
// returns every job in append order, projected for the webui jobs panel. It
// is read-only: a session with no jobs.jsonl yields an empty slice and
// creates no file.
func LoadSessionJobList(stateDir, sessionID string) ([]JobSummary, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return []JobSummary{}, nil
		}
		return nil, err
	}
	store, err := historicalJobsOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	ordered, err := store.LoadOrdered()
	if err != nil {
		return nil, err
	}
	return summarizeJobRecords(ordered), nil
}
```

`historicalJobStore` (`agent/observer_grants.go:12-16`) needs `LoadOrdered` added to its interface — it already wraps `jobstore.Store`, which has it (`store.go:169`).

For `LoadSessionJobOutputTail`: open the store the same way, `store.Load()` (map), find the record (missing → `found=false`), resolve the output path with the same rule as `outputPathForJob` (record's `OutputPath` if set, else `filepath.Join(jobsDir(stateDir, sessionID), "jobs", jobID+".log")` — verify against `outputPathForJob`'s real body before writing this), then read the tail with the same helpers `readOutput` uses for the cold path (`validatedOutputStatsForRecord` + `tailOutputFile`, `agent/jobs.go:986-991`). Missing output file → empty tail, found=true, no error.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -run 'TestSummarizeJobRecord|TestLoadSessionJob' ./agent/`
Expected: PASS

Run: `go test -count=1 -short ./agent/` and `go build ./...`
Expected: PASS (the `errJobNotFound` message change must not break existing assertions)

- [ ] **Step 5: Commit**

```bash
git add agent/jobs_panel.go agent/jobs_panel_test.go agent/jobs.go agent/observer_grants.go
git commit -m "agent: job summary projection and live/past job readers for the webui jobs panel"
```

---

### Task 3: daemon server handlers + serve.go wiring

**Files:**
- Modify: `server/server.go` (fields near `tasksFn` line 231; setters near `SetTasksFunc` line 547)
- Modify: `server/appwire_runtime.go` (router registration line 374; handlers near `handleAppTasksList` line 827)
- Modify: `cmd/serf/serve.go` (server interface near line 90; wiring near line 828)
- Modify: `cmd/serf/serve_residual_fuzz_test.go` (fake server near line 87)
- Test: `server/server_test.go` (mirror the tasks tests near lines 1120, 1384)

**Interfaces:**
- Consumes: `agent.Session.JobSummaries()`, `agent.Session.JobOutputTail(jobID, maxBytes) (JobOutputTail, bool, error)` (Task 2); `appwire.JobsList*`/`JobsOutput*` (Task 1)
- Produces:
  - `func (s *Server) SetJobsFunc(fn func() any)`
  - `func (s *Server) SetJobOutputFunc(fn func(jobID string, maxBytes int64) (data any, found bool, err error))`
  - Daemon answers `serf/jobs/list` and `serf/jobs/output` (Task 5's source layer calls these through the appwire client)

**Semantics:** nil `jobsFn` → empty `JobsListResponse{}` and no error (old-daemon capability gap, same as `handleAppTasksList`). Nil `jobOutputFn` → `appwire.Unavailable("job output not available")`. `found=false` → `appwire.InvalidParams("job not found: " + jobID)`. Any other fn error propagates.

- [ ] **Step 1: Write the failing tests**

In `server/server_test.go`, mirroring the tasks-list tests (read the tests at lines 1120-1170 and 1384-1400 first and match their fixture setup):

```go
func TestHandleAppJobsListNilFunc(t *testing.T) {
	// same server fixture as the tasks nil-fn test
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodSerfJobsList, appwire.JobsListParams{}))
	// want: result response, data null, no error
}

func TestHandleAppJobsList(t *testing.T) {
	srv.SetJobsFunc(func() any {
		return []agent.JobSummary{{JobID: "job_1", Type: "shell", Status: "running", Description: "make build", StartedAt: "2026-07-31T12:00:00Z"}}
	})
	// want: data carries one summary with jobId job_1
}

func TestHandleAppJobsOutputNilFunc(t *testing.T) {
	// no SetJobOutputFunc call
	// want: error response, serfErrorInfo actionUnavailable
}

func TestHandleAppJobsOutputNotFound(t *testing.T) {
	srv.SetJobOutputFunc(func(string, int64) (any, bool, error) { return nil, false, nil })
	// want: error response, invalid params, message carries the job id
}

func TestHandleAppJobsOutput(t *testing.T) {
	srv.SetJobOutputFunc(func(jobID string, maxBytes int64) (any, bool, error) {
		if jobID != "job_1" {
			t.Errorf("jobID = %q", jobID)
		}
		if maxBytes != 99 {
			t.Errorf("maxBytes = %d", maxBytes)
		}
		return agent.JobOutputTail{Tail: "hi", TotalBytes: 2}, true, nil
	})
	// request with params {jobId: "job_1", maxBytes: 99}; want data.tail == "hi"
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 -run 'TestHandleAppJobs' ./server/`
Expected: FAIL — `srv.SetJobsFunc undefined`

- [ ] **Step 3: Implement the server side**

In `server/server.go` beside `tasksFn`:

```go
jobsFn                        func() any
jobOutputFn                   func(jobID string, maxBytes int64) (data any, found bool, err error)
```

Beside `SetTasksFunc`:

```go
// SetJobsFunc sets the function backing serf/jobs/list. The function should
// return a JSON-serializable slice (typically []agent.JobSummary).
func (s *Server) SetJobsFunc(fn func() any) {
	s.mu.Lock()
	s.jobsFn = fn
	s.mu.Unlock()
}

// SetJobOutputFunc sets the function backing serf/jobs/output. found=false
// maps to an invalid-params wire error (the caller guessed a job id).
func (s *Server) SetJobOutputFunc(fn func(jobID string, maxBytes int64) (data any, found bool, err error)) {
	s.mu.Lock()
	s.jobOutputFn = fn
	s.mu.Unlock()
}
```

In `server/appwire_runtime.go` beside the `MethodSerfTasksList` router registration (line 374):

```go
appserver.HandleTyped(router, appwire.MethodSerfJobsList, s.handleAppJobsList)
appserver.HandleTyped(router, appwire.MethodSerfJobsOutput, s.handleAppJobsOutput)
```

Beside `handleAppTasksList` (line 827):

```go
func (s *Server) handleAppJobsList(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	s.mu.RLock()
	fn := s.jobsFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsListResponse{}, nil
	}
	return appwire.JobsListResponse{Data: fn()}, nil
}

func (s *Server) handleAppJobsOutput(_ context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	s.mu.RLock()
	fn := s.jobOutputFn
	s.mu.RUnlock()
	if fn == nil {
		return appwire.JobsOutputResponse{}, appwire.Unavailable("job output not available")
	}
	data, found, err := fn(params.JobID, params.MaxBytes)
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	if !found {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsOutputResponse{Data: data}, nil
}
```

- [ ] **Step 4: Wire the daemon in cmd/serf/serve.go**

The serve server interface (line 90 area) gains:

```go
SetJobsFunc(func() any)
SetJobOutputFunc(func(string, int64) (any, bool, error))
```

Beside `srv.SetTasksFunc(func() any { return getSession().Tasks() })` (line 828):

```go
srv.SetJobsFunc(func() any { return getSession().JobSummaries() })
srv.SetJobOutputFunc(func(jobID string, maxBytes int64) (any, bool, error) {
	return getSession().JobOutputTail(jobID, maxBytes)
})
```

Add matching methods to `residualServeServer` in `cmd/serf/serve_residual_fuzz_test.go` (line 87 area):

```go
func (s *residualServeServer) SetJobsFunc(f func() any)                                       { s.jobs = f }
func (s *residualServeServer) SetJobOutputFunc(f func(string, int64) (any, bool, error))      { s.jobOutput = f }
```

(plus the two struct fields; `rg "residualServeServer struct" cmd/serf/` to find the struct.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -count=1 -run 'TestHandleAppJobs' ./server/` && `go test -count=1 -short ./server/ ./cmd/serf/` && `go build ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add server/server.go server/appwire_runtime.go server/server_test.go cmd/serf/serve.go cmd/serf/serve_residual_fuzz_test.go
git commit -m "server: serf/jobs/list and serf/jobs/output daemon handlers"
```

---

### Task 4: projector serf/job/updated case

**Files:**
- Modify: `internal/appprojector/appwire_projection.go` (near the `EventTaskUpdated` case, line 776)
- Test: `internal/appprojector/appwire_projection_test.go` (mirror `TestProject_TaskUpdated`, line 153)

**Interfaces:**
- Consumes: `events.EventJobStarted`/`events.EventJobFinished` with `events.JobStartedData`/`events.JobFinishedData` payloads (`agent/events/payloads.go:477-505`; both carry `JobID` and `Status`), `appwire.NotifySerfJobUpdated`/`JobUpdatedParams` (Task 1)
- Produces: a `serf/job/updated` appwire notification per job lifecycle event; the frontend reducer (Task 7) handles it

**Fact check before writing:** the session already emits these events on every job start/finish (`agent/jobs.go:864` and `agent/jobs.go:940`).

- [ ] **Step 1: Write the failing test**

```go
func TestProject_JobStartedUpdated(t *testing.T) {
	out := testProject(t, events.Event{ // use the same projector-fixture helper TestProject_TaskUpdated uses
		Kind: events.EventJobStarted,
		Data: events.JobStartedData{JobID: "job_1", JobType: "shell", Status: "running"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfJobUpdated {
		t.Fatalf("want one serf/job/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.JobUpdatedParams)
	if !ok {
		t.Fatalf("params type %T", out[0].Params)
	}
	if params.JobID != "job_1" || params.Status != "running" {
		t.Errorf("params: %+v", params)
	}
}

func TestProject_JobFinishedUpdated(t *testing.T) {
	// same, with EventJobFinished / JobFinishedData{JobID: "job_1", Status: "completed"}
}
```

Also register both in the fuzz replay table (`internal/appprojector/project_fuzz_test.go:143`, beside `{"task_updated", TestProject_TaskUpdated}`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 -run 'TestProject_Job' ./internal/appprojector/`
Expected: FAIL — zero notifications (no case yet)

- [ ] **Step 3: Add the projection cases**

Beside the `EventTaskUpdated` case (`appwire_projection.go:776`):

```go
case events.EventJobStarted:
	p.clearSkillCandidate()
	data := eventData[events.JobStartedData](event.Data)
	return []AppNotification{p.notification(appwire.NotifySerfJobUpdated, appwire.JobUpdatedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		JobID:    data.JobID,
		Status:   data.Status,
	})}
case events.EventJobFinished:
	p.clearSkillCandidate()
	data := eventData[events.JobFinishedData](event.Data)
	return []AppNotification{p.notification(appwire.NotifySerfJobUpdated, appwire.JobUpdatedParams{
		ThreadID: p.threadID,
		Ref:      p.ref,
		JobID:    data.JobID,
		Status:   data.Status,
	})}
```

(Check `clearSkillCandidate` usage in neighboring cases first — copy whatever the `EventTaskUpdated` case does; if it does not call it, omit it.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/appprojector/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appprojector/
git commit -m "appprojector: project job lifecycle events as serf/job/updated"
```

---

### Task 5: hub appsource JobsList/JobOutput

**Files:**
- Modify: `cmd/serf-hub/internal/appsource/source.go` (interface, line 33 area)
- Modify: `cmd/serf-hub/internal/appsource/local_daemon.go` (near `ListTasks`, line 466)
- Modify: `cmd/serf-hub/internal/appsource/codex_source.go` (near `ListTasks`, line 364)
- Modify: `cmd/serf-hub/internal/appsource/registry_test.go` (fakeSource, line 81 area)
- Modify: `cmd/serf-hub/internal/appsource/coverage_completion_test.go` (call-table, line 232 area) and `cov_rhub_appsource_test.go` (line 424 area) if they assert interface coverage
- Test: extend the local-daemon source tests that cover `ListTasks` (find via `rg "ListTasks" cmd/serf-hub/internal/appsource/*_test.go`)

**Interfaces:**
- Consumes: `client.JobsList`/`client.JobOutput` (Task 1)
- Produces (hub handlers in Task 6 call these):
  - `Source.ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error)`
  - `Source.JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error)`

- [ ] **Step 1: Write the failing tests**

Mirror the existing `ListTasks` local-daemon test (read it first). Core assertions:

```go
// local daemon: JobsList forwards through the appwire client and returns its data
// codex source: JobsList and JobOutput reject with appwire.Unavailable (actionUnavailable)
func TestCodexSourceJobsUnavailable(t *testing.T) {
	// same fixture as the ListTasks unavailability test (cov_rhub_appsource_test.go:424)
	if _, err := s.ListJobs(ctx, appwire.JobsListParams{}); err == nil {
		t.Error("ListJobs should be unavailable")
	}
	if _, err := s.JobOutput(ctx, appwire.JobsOutputParams{JobID: "job_1"}); err == nil {
		t.Error("JobOutput should be unavailable")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 -run 'Jobs' ./cmd/serf-hub/internal/appsource/`
Expected: FAIL — `s.ListJobs undefined`

- [ ] **Step 3: Implement**

`source.go` interface, beside `ListTasks`:

```go
ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error)
JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error)
```

`local_daemon.go`, mirroring `ListTasks` exactly:

```go
func (s *LocalDaemonSource) ListJobs(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.JobsListResponse{}, err
	}
	var out appwire.JobsListResponse
	err = s.withClient(ctx, entry, func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.JobsList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *LocalDaemonSource) JobOutput(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	entry, err := s.entryForRef(params.Ref, "")
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	var out appwire.JobsOutputResponse
	err = s.withClient(ctx, entry, func(client *appwire.Client) error {
		var callErr error
		out, callErr = client.JobOutput(ctx, params)
		return callErr
	})
	return out, err
}
```

`codex_source.go`:

```go
func (s *CodexSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return appwire.JobsListResponse{}, appwire.Unavailable("codex source does not expose serf jobs")
}

func (s *CodexSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return appwire.JobsOutputResponse{}, appwire.Unavailable("codex source does not expose serf jobs")
}
```

`registry_test.go` fakeSource: add both methods returning zero values.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./cmd/serf-hub/internal/appsource/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/appsource/
git commit -m "appsource: ListJobs/JobOutput across local and codex sources"
```

---

### Task 6: hub handlers + dead-session fallback

**Files:**
- Create: `cmd/serf-hub/app_jobs.go`
- Modify: `cmd/serf-hub/app_rpc.go` (registration beside `MethodSerfTasksList`, line 743)
- Test: `cmd/serf-hub/app_jobs_test.go` (mirror `app_tasks_test.go`; shared past-fixture helpers live in `web_workspace_test.go`)

**Interfaces:**
- Consumes: `Source.ListJobs`/`JobOutput` (Task 5); `agent.LoadSessionJobList`/`agent.LoadSessionJobOutputTail` (Task 2); `sourceForThreadWithManagedLaunch`, `isDeadSessionError`, `localPastThreadID`, `cfg.Past.Find` (all from `app_tasks.go`)
- Produces:
  - `func hubJobsList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsListParams) (appwire.JobsListResponse, error)`
  - `func hubJobsOutput(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error)`
  - Hub answers `serf/jobs/list` and `serf/jobs/output` (the frontend store, Task 8, calls these)

- [ ] **Step 1: Write the failing tests**

Mirror `app_tasks_test.go` — read it first and reuse its fixtures (`web_workspace_test.go` builds the past-index test harness the comment at its line 21 describes). Cases:

```go
func TestHubJobsListLiveDaemon(t *testing.T) {
	// fake source whose ListJobs returns one summary; want it passed through
}

func TestHubJobsListDeadSessionFallsBackToPast(t *testing.T) {
	// source rejects with the dead-session error (isDeadSessionError-shaped:
	// SessionUnavailable + "thread not found: " prefix, see app_tasks_test.go);
	// past index has the session with a jobs.jsonl fixture; want the persisted jobs
}

func TestHubJobsListDeadSessionNotInPastIndex(t *testing.T) {
	// dead-session error, but cfg.Past.Find misses; want the ORIGINAL error, not an empty list
}

func TestHubJobsListLiveErrorPropagates(t *testing.T) {
	// source rejects with a transient sessionUnavailable ("local daemon unavailable: ...");
	// want that error, no fallback
}

func TestHubJobsOutputPastUnknownJob(t *testing.T) {
	// fallback path, job id absent from the persisted store; want invalid params
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 -run 'TestHubJobs' ./cmd/serf-hub/`
Expected: FAIL — `undefined: hubJobsList`

- [ ] **Step 3: Implement `cmd/serf-hub/app_jobs.go`**

Mirror `app_tasks.go` (read it again before writing; the structure below is exact):

```go
package main

import (
	"context"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// hubJobsList answers serf/jobs/list. A running daemon's jobstore is
// authoritative, so it is always tried first; only the specific dead-session
// condition (isDeadSessionError, app_tasks.go) falls back to the persisted
// jobs.jsonl through agent.LoadSessionJobList, behind the same past-index
// gate pastTasksListResponse uses.
func hubJobsList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	var resp appwire.JobsListResponse
	if err == nil {
		resp, err = source.ListJobs(ctx, params)
	}
	if err == nil {
		return resp, nil
	}
	if !isDeadSessionError(err) {
		return appwire.JobsListResponse{}, err
	}
	pastResp, ok, pastErr := pastJobsListResponse(cfg, params)
	if pastErr != nil {
		return appwire.JobsListResponse{}, pastErr
	}
	if ok {
		return pastResp, nil
	}
	return appwire.JobsListResponse{}, err
}

func pastJobsListResponse(cfg hubcore.WebConfig, params appwire.JobsListParams) (appwire.JobsListResponse, bool, error) {
	if cfg.Past == nil {
		return appwire.JobsListResponse{}, false, nil
	}
	threadID, ok := localPastThreadID(appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsListResponse{}, false, nil
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.JobsListResponse{}, false, nil
	}
	jobs, err := agent.LoadSessionJobList(entry.StateDir, entry.Meta.ID)
	if err != nil {
		return appwire.JobsListResponse{}, true, err
	}
	return appwire.JobsListResponse{Data: jobs}, true, nil
}

// hubJobsOutput answers serf/jobs/output with the same live-first /
// dead-session-fallback split. A job id absent from the persisted store is
// invalid params — the caller guessed.
func hubJobsOutput(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	var resp appwire.JobsOutputResponse
	if err == nil {
		resp, err = source.JobOutput(ctx, params)
	}
	if err == nil {
		return resp, nil
	}
	if !isDeadSessionError(err) {
		return appwire.JobsOutputResponse{}, err
	}
	if cfg.Past == nil {
		return appwire.JobsOutputResponse{}, err
	}
	threadID, ok := localPastThreadID(appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsOutputResponse{}, err
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.JobsOutputResponse{}, err
	}
	tail, found, tailErr := agent.LoadSessionJobOutputTail(entry.StateDir, entry.Meta.ID, params.JobID, params.MaxBytes)
	if tailErr != nil {
		return appwire.JobsOutputResponse{}, tailErr
	}
	if !found {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsOutputResponse{Data: tail}, nil
}
```

Register in `app_rpc.go` beside the tasks registration (line 743):

```go
appserver.HandleTyped(server.Router(), appwire.MethodSerfJobsList, func(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return hubJobsList(ctx, cfg, sources, params)
})
appserver.HandleTyped(server.Router(), appwire.MethodSerfJobsOutput, func(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return hubJobsOutput(ctx, cfg, sources, params)
})
```

No hub changes are needed for `serf/job/updated` relay — daemon notifications reach the frontend through the existing bridge (same as `serf/task/updated`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -run 'TestHubJobs' ./cmd/serf-hub/` && `go test -count=1 -short ./cmd/serf-hub/` && `go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_jobs.go cmd/serf-hub/app_jobs_test.go cmd/serf-hub/app_rpc.go
git commit -m "serf-hub: serf/jobs/list and serf/jobs/output handlers with past fallback"
```

---

### Task 7: frontend protocol — model field + reducer case

**Files:**
- Modify: `cmd/serf-hub/frontend/src/protocol/model.ts` (`tasks` field is at line 156)
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts` (beside the `serf/task/updated` case, line 696)
- Test: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts` (mirror the `serf/task/updated` case test; find via `rg "task/updated" src/protocol/reducer.test.ts`)
- Note: `types.gen.ts` was regenerated in Task 1 and already carries the new notification's params type — if the reducer switch keys params types off generated types, use them; otherwise follow whatever the `serf/task/updated` case does.

**Interfaces:**
- Produces: `ThreadModel.jobsUpdatedAt: number | null` — bumped to the frame's `now` on every `serf/job/updated` for the thread. The JobsPanel (Task 9) re-fetches when this changes.

- [ ] **Step 1: Write the failing test**

```ts
it("serf/job/updated bumps jobsUpdatedAt for the targeted thread", () => {
	// same fixture shape as the serf/task/updated test: reduce a notification
	// { method: "serf/job/updated", params: { threadId, ref, jobId: "job_1", status: "running" } }
	// want: model.jobsUpdatedAt === <the reducer's now>, model unchanged otherwise
});

it("serf/job/updated for another thread leaves the model untouched", () => {
	// notificationTargetsThread miss → same model object identity
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/protocol/reducer.test.ts`
Expected: FAIL — `jobsUpdatedAt` undefined

- [ ] **Step 3: Implement**

`model.ts` beside `tasks`:

```ts
  // Bumped (to the reducer's frame time) by every serf/job/updated for this
  // thread; the jobs panel re-fetches its list when this changes. null until
  // the first push arrives.
  jobsUpdatedAt: number | null;
```

Find where `tasks: null` is initialized for a fresh model (`rg "tasks: null" src/`) and add `jobsUpdatedAt: null` there.

`reducer.ts` beside the `serf/task/updated` case:

```ts
    case "serf/job/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/protocol/reducer.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/protocol/model.ts cmd/serf-hub/frontend/src/protocol/reducer.ts cmd/serf-hub/frontend/src/protocol/reducer.test.ts
git commit -m "webui: thread model jobsUpdatedAt + serf/job/updated reducer case"
```

---

### Task 8: frontend store methods + jobData parser

**Files:**
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts` (interface line 191 area; implementation beside `listTasks`, line 2000)
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/jobData.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/jobData.test.ts`; extend `cmd/serf-hub/frontend/src/stores/threads.test.ts` (mirror the listTasks test near line 3128)

**Interfaces:**
- Consumes: `model.jobsUpdatedAt` (Task 7); the daemon/hub wire shapes (Tasks 2, 6)
- Produces (JobsPanel, Task 9, consumes exactly these):
  - `threadsStore.getState().listJobs(ref: string): Promise<unknown>`
  - `threadsStore.getState().jobOutput(ref: string, jobId: string): Promise<unknown>`
  - `type JobStatus = "running" | "completed" | "failed" | "cancelled" | "stopped" | "exhausted"`
  - `interface JobRow { jobId: string; type: string; status: JobStatus; reason?: string; description: string; command?: string; task?: string; background: boolean; startedAt: string; endedAt?: string; exitCode?: number; outputBytes: number; hasOutput: boolean }`
  - `parseJobListData(data: unknown): JobRow[] | null`
  - `interface JobOutput { tail: string; totalBytes: number; retainedStart: number; truncated: boolean }`
  - `parseJobOutputData(data: unknown): JobOutput | null`

- [ ] **Step 1: Write the failing tests**

`jobData.test.ts`, mirroring `taskData.test.ts`'s structure (read it first):

```ts
import { describe, expect, it } from "vitest";
import { parseJobListData, parseJobOutputData } from "./jobData";

describe("parseJobListData", () => {
	it("parses a full shell row", () => {
		const rows = parseJobListData([
			{
				jobId: "job_1", type: "shell", status: "completed", reason: "",
				description: "run tests", command: "go test ./...", background: true,
				startedAt: "2026-07-31T12:00:00Z", endedAt: "2026-07-31T12:01:00Z",
				exitCode: 0, outputBytes: 123, hasOutput: true,
			},
		]);
		expect(rows).toHaveLength(1);
		expect(rows![0]).toMatchObject({ jobId: "job_1", status: "completed", exitCode: 0, hasOutput: true });
	});

	it("omits optional fields when absent", () => {
		const rows = parseJobListData([
			{ jobId: "job_2", type: "delegate", status: "running", description: "scout", background: true, startedAt: "2026-07-31T12:00:00Z", outputBytes: 0, hasOutput: false },
		]);
		expect(rows![0].endedAt).toBeUndefined();
		expect(rows![0].exitCode).toBeUndefined();
	});

	it("returns null for null data (old daemon capability gap)", () => {
		expect(parseJobListData(null)).toBeNull();
		expect(parseJobListData(undefined)).toBeNull();
		expect(parseJobListData({})).toBeNull();
	});

	it("returns an empty list for a real empty list", () => {
		expect(parseJobListData([])).toEqual([]);
	});

	it("drops malformed entries but keeps parseable ones", () => {
		const rows = parseJobListData([
			"garbage",
			{ jobId: "job_3", type: "shell", status: "running", description: "ok", background: false, startedAt: "2026-07-31T12:00:00Z", outputBytes: 0, hasOutput: false },
		]);
		expect(rows).toHaveLength(1);
		expect(rows![0].jobId).toBe("job_3");
	});
});

describe("parseJobOutputData", () => {
	it("parses a tail payload", () => {
		const out = parseJobOutputData({ tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true });
		expect(out).toEqual({ tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true });
	});

	it("returns null for uninterpretable data", () => {
		expect(parseJobOutputData(null)).toBeNull();
		expect(parseJobOutputData({ tail: 5 })).toBeNull();
	});
});
```

`threads.test.ts`: mirror the `listTasks` test — fake client, assert `client.request("serf/jobs/list", { ref })` / `client.request("serf/jobs/output", { ref, jobId })` and that `resp.data` is returned.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/jobData.test.ts`
Expected: FAIL — module not found

- [ ] **Step 3: Implement `jobData.ts`**

Mirror `taskData.ts` (read it again first; the comment header should document the wire-truth source — `agent/jobs_panel.go`'s `JobSummary`/`JobOutputTail` json tags — the same way taskData.ts documents `agent/task/task_store.go`):

```ts
// Narrows JobsListResponse.data / JobsOutputResponse.data (both `Data any`
// in appwire/types.go, so types.gen.ts types them `unknown`) into
// display-ready shapes. Wire truth: agent/jobs_panel.go's JobSummary and
// JobOutputTail structs. `data` is `null`/`undefined` only when no jobsFn
// is registered server-side (an old daemon), reported as `null` — distinct
// from a real empty list (`[]`, "zero jobs").

export type JobStatus = "running" | "completed" | "failed" | "cancelled" | "stopped" | "exhausted";

export interface JobRow {
	jobId: string;
	type: string;
	status: JobStatus;
	reason?: string;
	description: string;
	command?: string;
	task?: string;
	background: boolean;
	startedAt: string;
	endedAt?: string;
	exitCode?: number;
	outputBytes: number;
	hasOutput: boolean;
}

export interface JobOutput {
	tail: string;
	totalBytes: number;
	retainedStart: number;
	truncated: boolean;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

// A row is usable once it carries the wire's non-omitempty fields with the
// right primitive types (jobId/type/description/background/startedAt/
// outputBytes/hasOutput are never omitted by the Go struct's json tags) —
// anything else is dropped rather than crashing the whole parse.
function parseRow(raw: unknown): JobRow | null {
	if (!isPlainObject(raw)) return null;
	const { jobId, type, status, reason, description, command, task, background, startedAt, endedAt, exitCode, outputBytes, hasOutput } = raw;
	if (typeof jobId !== "string" || typeof type !== "string" || typeof status !== "string") return null;
	if (typeof description !== "string" || typeof background !== "boolean" || typeof startedAt !== "string") return null;
	if (typeof outputBytes !== "number" || typeof hasOutput !== "boolean") return null;

	const row: JobRow = { jobId, type, status: status as JobStatus, description, background, startedAt, outputBytes, hasOutput };
	if (typeof reason === "string" && reason !== "") row.reason = reason;
	if (typeof command === "string" && command !== "") row.command = command;
	if (typeof task === "string" && task !== "") row.task = task;
	if (typeof endedAt === "string" && endedAt !== "") row.endedAt = endedAt;
	if (typeof exitCode === "number") row.exitCode = exitCode;
	return row;
}

export function parseJobListData(data: unknown): JobRow[] | null {
	if (!Array.isArray(data)) return null;
	const rows: JobRow[] = [];
	for (const raw of data) {
		const row = parseRow(raw);
		if (row) rows.push(row);
	}
	return rows;
}

export function parseJobOutputData(data: unknown): JobOutput | null {
	if (!isPlainObject(data)) return null;
	const { tail, totalBytes, retainedStart, truncated } = data;
	if (typeof tail !== "string" || typeof totalBytes !== "number" || typeof retainedStart !== "number" || typeof truncated !== "boolean") {
		return null;
	}
	return { tail, totalBytes, retainedStart, truncated };
}
```

`threads.ts` interface, beside `listTasks(ref: string): Promise<unknown>;`:

```ts
  listJobs(ref: string): Promise<unknown>;
  jobOutput(ref: string, jobId: string): Promise<unknown>;
```

Implementation, beside `listTasks`:

```ts
  async listJobs(ref) {
    const client = requireClient();
    // No mapConflict here either, same reasoning as listModels/listTasks above.
    const resp = await client.request("serf/jobs/list", { ref });
    return resp.data;
  },

  async jobOutput(ref, jobId) {
    const client = requireClient();
    const resp = await client.request("serf/jobs/output", { ref, jobId });
    return resp.data;
  },
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/jobData.test.ts src/stores/threads.test.ts && npm run lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/stores/threads.ts cmd/serf-hub/frontend/src/stores/threads.test.ts cmd/serf-hub/frontend/src/panes/session/chrome/jobData.ts cmd/serf-hub/frontend/src/panes/session/chrome/jobData.test.ts
git commit -m "webui: listJobs/jobOutput store methods and jobData wire parser"
```

---

### Task 9: JobsPanel component + CSS

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/jobspanel.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.test.tsx`
- Template: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx` and `TasksPanel.test.tsx` — read both fully before writing anything; this component copies their structure (failure taxonomy, stale notice, Try again, imperative handle, `data-tasks-trigger`-style trigger hook is NOT needed — no palette command for jobs)

**Interfaces:**
- Consumes: `parseJobListData`/`parseJobOutputData`/`JobRow`/`JobOutput` (Task 8); `model.jobsUpdatedAt` (Task 7); `threadsStore.listJobs`/`jobOutput` (Task 8); widgets `Button, Chip, EmptyState, Sheet, useToasts`, `Disclosure`, `requireClass`
- Produces (SessionChrome, Task 10, mounts this):
  - `JobsPanelProps { sessionRef: string; model: ThreadModel; now: number; hideTrigger?: boolean }`
  - `JobsPanelHandle { open: () => void }`

**Behavior summary (the test list derives from this):**

- Trigger: quiet sm `Button`, label `Jobs` or `Jobs ●N` when N jobs in the last fetched list are non-terminal; omitted while `hideTrigger`.
- Sheet fetch: on open, on Try again, and whenever `model.jobsUpdatedAt` changes while open. Same effect-dep pattern as TasksPanel (`[open, model.jobsUpdatedAt, sessionRef, reloads]`, `toasts` deliberately excluded with the same biome-ignore rationale).
- Failure taxonomy (identical shape to TasksPanel): `isActionUnavailable` → "Job list isn't available" (no retry); `isThreadNotFound` + no prior rows → "This session has ended"; first-fetch error → EmptyState + Try again + toast; refetch error → stale notice above retained rows + Try again + toast. Reuse `sessionActionHeadline`/`sessionActionError`/`errorText` with `LOAD_FAILURE = "Couldn't load jobs"`. Copy the `isActionUnavailable` and `isThreadNotFound` helpers from TasksPanel (they are generic; if lint flags duplication, extract both panels' copies into a small shared module in the same directory — do not leave three copies).
- Rows: `<li data-testid="job-row">` with a `Disclosure`; summary = status `Chip` + type glyph + description + elapsed/duration. Glyph: `›` shell, `◈` delegate. Tone map: running→`alive`; completed/cancelled/stopped→`neutral`; failed/exhausted→`danger`.
- Detail (Disclosure body): `<dl>` rows for status, type, started, ended (when set), exit code (when set), output size, reason (when set), and full command or task text in a `<pre>` — the `TaskDetailField` grammar from TasksPanel.
- Output tail: when `row.hasOutput`, the detail also renders a `JobOutputTailView` that fetches `jobOutput(sessionRef, row.jobId)` on mount. Disclosure mounts its body only when open (`widgets/disclosure/index.tsx` line 49: `{open && <div>…}`), so mounting inside the body IS lazy-on-expand. States: loading ("Loading output…"), error (inline line + toast, with its own Try again), loaded (`<pre>` + a `Showing last N of M bytes` caption when `truncated`). The tail fetch does NOT re-fire on `jobsUpdatedAt` — the next collapse/expand re-mounts and refetches; keeping the tail static while open avoids text jumping under the reader.
- Disclosure id: `${sessionRef}\0${jobId}` (same NUL idiom as `taskDisclosureId`).
- Elapsed time: receive `now: number` as a prop from SessionChrome (it already owns `useNowTick`); format `now - Date.parse(startedAt)` as a compact duration for running jobs, `Date.parse(endedAt) - Date.parse(startedAt)` for finished ones. Reuse any existing duration formatter (`rg "formatDuration|formatElapsed" src/` — StatusRow/detailsAccounting have one; use it rather than writing a new one).

- [ ] **Step 1: Write the failing tests**

`JobsPanel.test.tsx` — mirror `TasksPanel.test.tsx`'s harness (it mocks `threadsStore.getState().listTasks`; mock `listJobs`/`jobOutput` the same way). Cases, one `it` each:

```ts
// 1. closed by default: no listJobs call until the trigger is clicked
// 2. opening fetches and renders one row per job (glyph, description, status chip)
// 3. empty list → "No jobs yet" empty state
// 4. null data → "Job list isn't available" unsupported state, no Try again
// 5. actionUnavailable rejection → same unsupported state
// 6. "thread not found: " sessionUnavailable rejection with no prior rows → "This session has ended"
// 7. first-fetch failure → error EmptyState + toast + Try again; clicking Try again refetches
// 8. refetch failure with retained rows → stale notice ("Showing the last list that loaded.") above the rows
// 9. jobsUpdatedAt bump while open → refetches (advance model.jobsUpdatedAt, assert listJobs called twice)
// 10. jobsUpdatedAt bump while closed → no fetch
// 11. expanding a row with hasOutput fetches jobOutput and renders the tail in a <pre>
// 12. expanding a row without hasOutput never calls jobOutput
// 13. truncated tail renders the "Showing last N of M bytes" caption
// 14. jobOutput rejection renders an inline error line (and does not clear the detail fields)
// 15. trigger label shows the running count after a fetch with running jobs ("Jobs ●2")
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/JobsPanel.test.tsx`
Expected: FAIL — module not found

- [ ] **Step 3: Implement `JobsPanel.tsx` + `jobspanel.module.css`**

Copy `TasksPanel.tsx`'s skeleton and adapt: rename, swap the fetch to `listJobs`, swap rows to `JobRowView`, add `JobOutputTailView`, swap the trigger label, drop the `data-tasks-trigger` attribute. The CSS module copies `taskspanel.module.css`'s class set (`state, list, description, stale, staleMessage, staleHint, detailList, detailRow, detailLabel, detailValue, detailPrompt`) verbatim, plus:

```css
.outputTail {
	/* mono, pre-wrap off with horizontal scroll; same tokens detailPrompt uses */
}
.outputCaption {
	/* caption size/tone tokens, same as detailLabel */
}
```

`JobOutputTailView` sketch:

```tsx
function JobOutputTailView({ sessionRef, jobId }: { sessionRef: string; jobId: string }) {
	const toasts = useToasts();
	const [output, setOutput] = useState<JobOutput | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [reloads, setReloads] = useState(0);
	// biome-ignore lint/correctness/useExhaustiveDependencies: toasts wrapper is fresh every render; toasts.push is stable
	useEffect(() => {
		let cancelled = false;
		setError(null);
		threadsStore.getState().jobOutput(sessionRef, jobId)
			.then((data) => { if (!cancelled) setOutput(parseJobOutputData(data) ?? { tail: "", totalBytes: 0, retainedStart: 0, truncated: false }); })
			.catch((err) => {
				if (cancelled) return;
				const sentence = sessionActionError("Couldn't load job output", err);
				setError(sentence);
				toasts.push("error", sentence);
			});
		return () => { cancelled = true; };
	}, [sessionRef, jobId, reloads]);
	if (error) {
		return (
			<div data-testid="job-output-error">
				<p role="alert">{error}</p>
				<Button variant="quiet" size="sm" onClick={() => setReloads((n) => n + 1)}>Try again</Button>
			</div>
		);
	}
	if (!output) return <p data-testid="job-output-loading">Loading output…</p>;
	return (
		<div data-testid="job-output">
			{output.truncated && (
				<p className={CLASS.outputCaption}>Showing last {output.tail.length} of {output.totalBytes} bytes</p>
			)}
			<pre className={CLASS.outputTail}>{output.tail}</pre>
		</div>
	);
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/JobsPanel.test.tsx && npm run lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/JobsPanel.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/jobspanel.module.css
git commit -m "webui: jobs panel component"
```

---

### Task 10: SessionChrome integration + final verification

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx` (imports lines 21-26; refs lines 112-113; overflow items lines 119-124; trailing cluster lines 138-147)
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx` (mirror the Details/Tasks overflow tests; find via `rg "overflowItems|collapsed" src/panes/session/chrome/SessionChrome.test.tsx`)

**Interfaces:**
- Consumes: `JobsPanel`, `JobsPanelHandle` (Task 9)
- Produces: the shipped feature — a Jobs trigger in the session chrome

- [ ] **Step 1: Write the failing tests**

```ts
// 1. wide chrome renders a "Jobs" trigger beside Details and Tasks
// 2. narrow chrome (ResizeObserver stub below 640px — copy the existing
//    Details/Tasks collapse test's stub) hides the inline trigger and puts a
//    "Jobs" item in the "⋯" menu after Details and Tasks
// 3. selecting the menu item opens the Jobs sheet (title "Jobs" visible)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx`
Expected: FAIL — no Jobs trigger

- [ ] **Step 3: Wire SessionChrome**

In `SessionChrome.tsx`:

```tsx
import { JobsPanel, type JobsPanelHandle } from "./JobsPanel";
```

Beside `detailsRef`/`tasksRef`:

```tsx
const jobsRef = useRef<JobsPanelHandle>(null);
```

Overflow items:

```tsx
const overflowItems: MenuItem[] = collapsed
  ? [
      { id: "details", label: "Details", onSelect: () => detailsRef.current?.open() },
      { id: "tasks", label: "Tasks", onSelect: () => tasksRef.current?.open() },
      { id: "jobs", label: "Jobs", onSelect: () => jobsRef.current?.open() },
    ]
  : [];
```

Trailing cluster, after `TasksPanel`:

```tsx
<JobsPanel ref={jobsRef} sessionRef={sessionRef} model={model} now={now} hideTrigger={collapsed} />
```

Also update the `SessionChrome` header comment ("Details, Tasks and the session "⋯" menu" → "Details, Tasks, Jobs and the session "⋯" menu") and the `NARROW_CHROME_WIDTH_PX` comment if the measured breakpoint needs to move — verify in a real browser build that three triggers still fit at 640px with a typical model name; if not, raise the constant and note the measurement in the commit message (same discipline the existing comment describes).

- [ ] **Step 4: Run the full verification suite**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ && npm run lint && npm run build`
Expected: PASS (typecheck via `tsc --noEmit` runs inside `npm run build`)

Run (repo root): `go build ./... && go test -count=1 -short ./appwire/ ./server/ ./agent/ ./internal/appprojector/ ./cmd/serf/ ./cmd/serf-hub/... && make lint-generated`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx
git commit -m "webui: mount the jobs panel in session chrome"
```

---

## Final acceptance

- `go build ./...` and `go test -short ./...` clean (or no new failures vs. the pre-feature baseline).
- `cd cmd/serf-hub/frontend && npm test && npm run lint && npm run build` clean.
- `make lint-generated` clean.
- Manual smoke: `make build-hub`, open a session, run a background shell job and a delegate; the Jobs trigger lists both, the panel refreshes on start/finish without reopening, and expanding a job shows its output tail.

---

## Post-implementation addendum — 2026-07-31

The plan above is the record of what was planned, and it is left as written.
This section records where the shipped code differs, after the branch merged
(`f99baf14e`) and a night of follow-up katas landed on top of it. The design
spec has been reconciled in place and describes what is there now.

**`serf/job/updated` does not exist** — kata j7y6, `ae4ff7d9f`. Task 1's
`NotifySerfJobUpdated`/`JobUpdatedParams` and Task 4's projection case both
landed and were then folded away. The notification fired at exactly the two
instants `serf/job/started` and `serf/job/finished` fire, from the same two
projector cases, to the same audience, and its payload was a strict subset of
`SerfJobParams` — `Job.JobID` plus `Job.Status` is all a refetch trigger
reads. Task 7's reducer case now hangs off that lifecycle pair, bumping
`model.jobsUpdatedAt` on both ends. Task 4 is empty in hindsight: both
projector cases predate this branch, so the projector never needed a change
at all.

**The output-tail refetch policy shipped exactly as Task 9 wrote it.** The
tail is fetched when `Disclosure` mounts its body, and does not re-fire on a
`jobsUpdatedAt` bump; the next collapse/expand re-mounts and refetches. What
did not survive is the caption in the same sketch: `output.tail.length` is
UTF-16 code units, which mis-counts every non-ASCII character, so the shipped
caption counts `totalBytes - retainedStart` — the byte span the daemon itself
measured over the file (kata e95r, `8fa05d583`).

**The list refetch grew a closed-panel case** — kata e95r, `8fa05d583`. Task
9 fetched on open, on Try again, and on a bump while open; its test 10 pinned
"bump while closed → no fetch". But a closed panel still shows the trigger's
`●N`, and that count must not keep claiming a job is running after the push
that says it finished. A bump while closed now refetches, under three guards:
only a bump this panel has not already fetched for, only after a first open,
and only while a trigger is rendered at all (`hideTrigger` puts no count on
screen). Such a refresh fails silently — no toast for a fetch the reader
cannot see; the badge holds its last known count. Test 10 survives, narrowed
to a panel that has never been opened.

**Three more panel edges** — same kata and commit. The terminal daemon-gone
notice carries `role=alert`, like the stale-refetch notice beside it. A
settled status beats a missing `endedAt`: only a running job's clock ticks
against `now`, so a finished job whose record carried no end timestamp shows
no clock rather than an elapsed time that climbs forever. A zero `startedAt`
(Go's zero time, `0001-01-01T00:00:00Z`) shows no clock rather than two
millennia.

**`JobRow.status` is a plain string, not the `JobStatus` union** — kata ddah,
`c0e3883e2`. Task 8's `status: status as JobStatus` cast told every consumer
an unrecognised status had been handled when it had not. Dropping the cast
made the compiler name the consumer that had been falling through —
`STATUS_TONE[row.status]`, `undefined` for anything outside the union, and
invisible only because `Chip` defaults its tone. `statusTone` now answers
`neutral` for a status this bundle does not know.

**`SetJobsFunc` returns an error** — kata 1fkq, `d2e7557a3`. Task 5's
`SetJobsFunc(fn func() any)` and `Session.JobSummaries() []JobSummary`
swallowed `LoadOrdered`'s error and answered the same empty list a job-less
session answers, so a damaged `jobs.jsonl` reported "No jobs yet" to the
panel — the most reassuring thing it could have said. Both grew an `error`
return. The nil-fn capability gap is still an empty response with no error,
because that one really is "this daemon has no job list".

**The wire payloads live in `appwire`, and the projection helper stayed
unexported** — `2cdb01997`. Task 2 declared `JobSummary`/`JobOutputTail` in
`agent/jobs_panel.go` with an exported `SummarizeJobRecord`. Wire payloads
carry `appwire`'s camelCase tag convention (namingcheck's carve-out), so both
structs moved to `appwire/types.go` with `agent` keeping aliases; and
exporting the projector would have leaked `jobstore.JobRecord` past the
library-boundary audit, so it is `summarizeJobRecord` and the hub reaches it
only through `agent.LoadSessionJobList`/`agent.LoadSessionJobOutputTail`.

**Both hub fallbacks share one past-index gate** — kata qqf6, `383e1c89c`.
Task 6's two open-coded `cfg.Past.Find(threadID)` sites are now the shared
`pastEntryForRead` helper (`cmd/serf-hub/app_threadread.go`), which performs
the same three steps: parse the ref, require source `local`, and require the
past index to know the thread.
