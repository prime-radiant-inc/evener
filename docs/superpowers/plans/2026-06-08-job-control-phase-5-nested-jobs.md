# Job Control — Phase 5: nested shell jobs (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make background **shell** jobs started by a subagent (a delegate child session) visible to and controllable by the **parent** session. A nested job is created in the child's own `jobManager` with `ParentJobID` set; the child forwards its `job_started`/`job_finished` (and notification) events into the parent's job store as parent-visible durable records keyed by the same globally-unique `job_id`. The parent surfaces them via `job_list(include_nested=true)`, reads/stops them via the parent-visible `job_id` (routing the control call to the live owner runtime, else finalizing on restart loss), and `job_stop(parent_delegate, include_children=true)` stops the visible active nested jobs.

**Architecture:** The forwarding seam is a single callback. Each `jobManager` gains a `forward func(jobstore.Event)` hook (nil for the root). When a subagent's `jobManager` writes a `job_started`/`job_session_assigned`/`job_finished`/notification event for a job whose `ParentJobID` is non-empty, it also calls `forward(event)` with the **verbatim** event (same `job_id`, same `terminal_generation`); the parent appends it to its own `jobstore.Store`, producing a forwarded durable record that folds (via the existing `Fold`) into a `JobRecord` carrying `ParentJobID`. The seam is established at spawn: `spawnAgent` threads the parent's forward closure and the spawning delegate's `job_id` into the child's `spawnConfig`, and `NewSession` installs them on the child `jobManager`. `job_list` filters forwarded records (non-empty `ParentJobID`) behind `include_nested`. `job_read_output`/`job_stop` on a forwarded `job_id` route to the owner child's `jobManager` (reached through the parent's `subagentManager`) when the owner runtime is live; after restart with no live owner the forwarded `running` record reconciles to `stopped/runtime_lost` (the existing Phase 1 `Reconcile` + Phase 2 wiring). `terminal_generation` is minted **once by the owner** and copied verbatim into the parent store, so the dedupe key `(visible_session_id, job_id, terminal_generation)` matches in both stores.

**Tech Stack:** Go, `agent/internal/jobstore` (Phase 1), the Phase 2 `jobManager` (`agent/jobs.go`: `jobManager`, `runningJob`, `jobNotification`, `createShell`, `list`/`listFilter`, `readOutput`, `stop`, `finalize`, `reconcileLostJobs`, `now`, `enqueue`), the Phase 2 streaming-shell path (`agent/job_shell.go`: `runShell`/`shellArgs`/`shellResult`), the existing subagent spawn machinery (`agent/subagents.go`, `agent/session_config.go` `spawnConfig`). Module: `primeradiant.com/serf/agent`.

This is **Phase 5 of 6**, implementing spec `docs/superpowers/specs/2026-06-08-job-control-design.md` §10 (nested jobs), §5.7 (`job_list include_nested`), §3.2/§3.3 (`parent_job_id`, `terminal_generation`), §3.4 (output routing), §5.8 (`job_stop include_children`), §7 (forwarded nested-job reconciliation), and the reference contract `docs/job-control.md` §"Nested jobs" (lines 1007–1023). It depends on **Phases 1–4** being merged: Phase 1 (`agent/internal/jobstore`), Phase 2 (the `jobManager`, the job-capable `shell`, `job_read_output`/`job_list`/`job_stop`, the durable notification bridge, restart reconciliation), Phase 3 (`delegate` + `job_send_message`, the `spawnAgent` `communicateOutputSchema` parameter), Phase 4 (watches). The behavior here registers/extends **alongside** the legacy subagent surface — **Phase 6 deletes the legacy surface**; this phase adds no new model-facing tools (it activates `include_nested`/`include_children`, which already exist as parameters on `DefJobList`/`DefJobStop` from Phase 2).

**Conventions for every task below:**
- Work in the `agent` module: run Go commands from `/Users/jesse/prime-radiant/toil-suite/serf/agent`.
- TDD: write the failing test first, watch it fail, write the minimal implementation, watch it pass, commit.
- Commit messages use the repo's `type(scope): subject` style, e.g. `feat(agent): ...`.
- Full `make test` + `make lint` from repo root (`/Users/jesse/prime-radiant/toil-suite/serf`) before the final task.

---

## What Phase 5 reuses / builds on (verify EVERY name against the merged Phase 1–4 code before using it)

Phases 2–4 are the prerequisites that **create** these symbols; their exact names are fixed by those merges, not by this plan. Each task below carries a `grep`-to-confirm note. The names this plan assumes, with the source of truth:

- **`spawnConfig`** (`agent/session_config.go:189`): the `json:"-"`, never-persisted struct `spawnAgent` populates on `subCfg.spawn` before `NewSession`. Today it has `parentSessionID`, `parentToolCallID`, `subagentTask`, `depth`, `sharedTaskStore`, and the prompt-shaping fields. **VERIFIED against current code.** Phase 5 adds two fields here (Task 1). Because the struct is `json:"-"`, the new fields are correctly absent from restored sessions (a restored subagent reconstructs nothing from `spawn`; forwarding is a live-runtime concern only).
- **`spawnAgent`** (`agent/subagents.go:155`): `func (s *Session) spawnAgent(ctx, task, model, workingDir string, maxTurns int, agentType, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string) (any, error)`. **VERIFIED.** Phase 3 adds a trailing `communicateOutputSchema map[string]any` parameter (Phase 3 Task 5). It sets `subCfg.spawn.parentSessionID = s.id` (line 209), `subCfg.spawn.depth = depth + 1` (line 211), and reads the spawning tool-call id from context — `if callID, ok := ctx.Value(ctxToolCallID).(string); ok { subCfg.spawn.parentToolCallID = callID }` (line 217). **This `ctx.Value(ctxToolCallID)` pattern is exactly how spawn-time data already reaches `spawnAgent`** — Phase 5 reuses it to carry the spawning delegate's `job_id`.
- **`ctxToolCallID`** (`agent/session_tools.go:28`, set at `:313`): the context key the tool dispatcher stamps with `call.ID` before running a tool handler. **VERIFIED.** Phase 5 adds a sibling `ctxParentJobID` key (Task 1) that the `createDelegate` handler stamps so `spawnAgent` can read the spawning delegate `job_id` the same way.
- **`NewSession`** (`agent/session_init.go`, called from `spawnAgent:302`): constructs the child `*Session`. Phase 2 (Task 6) wires `s.jobs = newJobManager(s.stateDir, s.id, s.enqueueJobNotification)` here. **Confirm the exact construction line with `grep -n "newJobManager(" agent/session_init.go`.** Phase 5 (Task 1) installs the forward hook + parent job id onto the child `jobManager` right after that construction, reading them from `cfg.spawn`.
- **`jobManager`** (`agent/jobs.go`, Phase 2): struct `{mu, store *jobstore.Store, dir, running map[string]*runningJob, enqueue func(jobNotification), now func() time.Time, sessionID, watches …}`. **Phase 4 uses `jm.sessionID` (see phase-4 Task 6).** Confirm the field name for the session id (`jm.sessionID` vs `jm.id`) with `grep -n "sessionID\|func newJobManager" agent/jobs.go`. Phase 5 adds `forward func(jobstore.Event)` and `parentJobID string` (Task 2).
- **`jobManager.createShell`** (`agent/jobs.go`, Phase 2 Task 3): `createShell(opts createShellOpts) (*jobstore.JobRecord, error)`. **Confirm `createShellOpts`'s fields with `grep -n "type createShellOpts" agent/jobs.go`** — Phase 2 Task 3 lists `Command`, `Description`; Phase 5 (Task 2) adds `ParentJobID` so a subagent's shell job records its parent. The owner/visible session ids are set by `createShell` from the manager's `sessionID` (Phase 2).
- **`runShell` / `shellArgs` / `shellResult`** (`agent/job_shell.go`, Phase 2 Task 4): `runShell(ctx, jm, se, args shellArgs) shellResult`. **Phase 5 does not change the shell lifecycle.** The subagent already reaches `runShell` through its own `shell` tool handler (Phase 2 Task 8 registered the shell tool on every session, including children). The only Phase-5 change to the shell path is that the child's `jm.finalize`/`createShell` now also `forward(...)` (Task 2/3) — `runShell` itself is untouched.
- **`jobManager.list` / `listFilter`** (`agent/jobs.go`, Phase 2 Task 3/9): `list(f listFilter) []*jobstore.JobRecord`. **Phase 2 Task 9 already passes `listFilter{Status, Type, Limit, IncludeNested:false}`** — so `IncludeNested` is already a field on `listFilter`; Phase 5 (Task 4) makes it functional (currently `list` ignores it / the handler hard-codes false). Confirm with `grep -n "type listFilter\|IncludeNested" agent/jobs.go agent/session_tools_jobs.go`.
- **`jobManager.readOutput`** (`agent/jobs.go`, Phase 2 Task 3): `readOutput(jobID string, tailBytes int) (content string, total int64, truncated bool, err error)` (Phase 4 also threads `grep`/`block`; confirm the real signature with `grep -n "func (jm \*jobManager) readOutput" agent/jobs.go`). Phase 5 (Task 5) adds nested routing in the **handler/wrapper**, not in this low-level method.
- **`jobManager.stop`** (`agent/jobs.go`, Phase 2 Task 3/4): `stop(jobID string) (*jobstore.JobRecord, error)` — signals the runtime and finalizes (`cancelled` confirmed / `stopped` unconfirmed). Confirm signature with `grep -n "func (jm \*jobManager) stop" agent/jobs.go`. Phase 5 (Task 6) adds nested routing + `include_children` in the **handler**.
- **`jobManager.finalize`** (`agent/jobs.go`, Phase 2 Task 3): writes `EventJobFinished` (minting `jobstore.NewTerminalGeneration()` once), `EventJobNotificationPending`, and calls `jm.enqueue`. Phase 5 (Task 3) makes finalize `forward` the `job_finished` + `job_notification_pending` events for nested jobs. Confirm with `grep -n "func (jm \*jobManager) finalize\|NewTerminalGeneration" agent/jobs.go`.
- **`jobManager.reconcileLostJobs`** (`agent/jobs.go`, Phase 2 Task 6): folds the store, runs `jobstore.Reconcile`, appends `job_finished`/`job_notification_pending`, arms notifications. **The parent's `reconcileLostJobs` already reconciles forwarded records** — a forwarded `running` record with no live owner is just a `running` record in the parent store with no entry in `jm.running`, which `jobstore.Reconcile` finalizes as `stopped/runtime_lost` (Phase 5 Task 7 proves this end-to-end; the only code change, if any, is excluding forwarded records from the parent's live set correctly). Confirm with `grep -n "func (jm \*jobManager) reconcileLostJobs" agent/jobs.go`.
- **`subagentManager`** (`agent/subagent_manager.go:22`): the parent's child map. `get(id) *subagent` (`:62`), `track` (`:53`), `infos() []SubagentInfo` (`:227`). **VERIFIED.** A `*subagent` exposes `sess *Session` (the child Session, `subagents.go:60`). The child's `jobManager` is `sub.sess.jobs` (Phase 2 field on `Session`). Phase 5 reaches the owner runtime for control routing through this manager. Phase 5 adds one read-only enumeration helper (Task 6) if `subagentManager` lacks a "list live children" accessor — confirm with `grep -n "func (m \*subagentManager)" agent/subagent_manager.go`.
- **`Session.jobs`** (`agent/session.go`, Phase 2 Task 6): the `*jobManager` field on `Session`. Confirm with `grep -n "jobs \+\*jobManager\|jobs *\*jobManager" agent/session.go`.
- **`Session.depth`** (`agent/session.go:108`): subagent nesting depth (0 = root). **VERIFIED.** A nested shell job exists exactly when the owning session has `depth > 0`.
- **`jobstore.Event` / `jobstore.Store.Append` / `Fold` / `Reconcile`** (`agent/internal/jobstore/`, Phase 1): `Append(Event) error` assigns `Seq` at write; `Fold` applies `ParentJobID` from `job_started` (`fold.go:34` — **VERIFIED**, already handled); `Reconcile(records, liveJobIDs, now)` (Phase 1 Task 7). **The forwarded event's own `Seq` is reassigned by the parent's `Append`** — forwarding copies the *payload* fields (kind, job_id, parent/owner/visible session, parent_job_id, terminal_generation, status, etc.), NOT the child's `seq`; the parent store stamps its own monotonic `seq`. This is correct: `seq` is store-local; `terminal_generation` is the cross-store stable identity.

**The single load-bearing invariant of this phase:** the forwarded `job_finished` copies `terminal_generation` **verbatim** from the owner's event. Never call `jobstore.NewTerminalGeneration()` on the parent side for a forwarded job. The owner mints once (Phase 1/2 `finalize`); the parent copies. This keeps `(visible_session_id, job_id, terminal_generation)` identical in both stores so the parent's terminal notification dedupes across a restart (spec §10, §6 dedupe).

---

## File structure

```
agent/
  jobs.go                   EDIT — add forward hook + parentJobID to jobManager; forward in createShell/finalize;
                                   make listFilter.IncludeNested functional in list(); nested routing helpers
                                   (ownerJobManagerFor, nested-aware readOutput/stop wrappers)
  jobs_nested.go            NEW  — the nested-job seam kept separate from the core jobManager:
                                   forwardEvent, isForwarded predicate, ownerJobManagerFor (parent→owner via subagentManager),
                                   stopChildren (include_children)
  job_nested_test.go        NEW  — all Phase 5 tests
  subagents.go              EDIT — spawnAgent threads the parent forward closure + spawning delegate job_id into
                                   subCfg.spawn (read from ctxParentJobID, mirroring ctxToolCallID at :217)
  session_config.go         EDIT — spawnConfig gains forwardJobEvent func(jobstore.Event) + parentJobID string
  session_init.go           EDIT — after newJobManager(...) for a child, install cfg.spawn.forwardJobEvent +
                                   cfg.spawn.parentJobID onto the child jobManager
  session_tools.go          EDIT — add ctxParentJobID context key (sibling of ctxToolCallID)
  job_delegate.go           EDIT — createDelegate stamps ctxParentJobID with the delegate's job_id before spawnAgent,
                                   so the child's nested shell jobs record this delegate as their parent_job_id
  session_tools_jobs.go     EDIT — job_list handler passes include_nested through to listFilter.IncludeNested;
                                   job_read_output / job_stop handlers route a forwarded job_id to the owner runtime;
                                   job_stop handler honors include_children
```

`jobs_nested.go` keeps the nested mechanism in its own file (the core `jobManager` create/list/read/stop stays in `jobs.go`); the few one-line touch points in `jobs.go` (`createShell`/`finalize`/`list` calling the nested helpers) are the only edits to the core file.

> **Seam I'm least sure of — flag for the implementer.** The exact Phase 2 *field name for the session id* on `jobManager` (`jm.sessionID` vs `jm.id`) and whether Phase 2's `readOutput`/`stop` are **methods on `jobManager`** or **package functions** (`runShell(ctx, jm, …)`-style) is fixed by the Phase 2 merge. Phase 4 uses `jm.sessionID` and method-style `jm.readOutput`, so this plan assumes the same; **every task that touches them says `grep -n … agent/jobs.go` first**. If Phase 2 chose function-style, adapt the call site — do not invent a parallel path.

---

## Task 1: spawn seam — carry the parent forward hook + delegate job_id to the child

**Files:**
- Modify: `agent/session_config.go` (`spawnConfig`: add `forwardJobEvent func(jobstore.Event)` + `parentJobID string`)
- Modify: `agent/session_tools.go` (add `ctxParentJobID` context key)
- Modify: `agent/subagents.go` (`spawnAgent`: read `ctxParentJobID` from context, set `subCfg.spawn.parentJobID`; set `subCfg.spawn.forwardJobEvent` to the parent's forward closure)
- Modify: `agent/session_init.go` (after the child's `newJobManager`, install `cfg.spawn.forwardJobEvent` + `cfg.spawn.parentJobID` onto `s.jobs`)
- Test: `agent/job_nested_test.go`

This task establishes the seam **without** any forwarding logic yet — just the plumbing that a child `jobManager` ends up with a non-nil `forward` and a `parentJobID` when (and only when) it was spawned by a delegate. The forwarding behavior is Task 2/3.

- [ ] **Step 0: Confirm the prerequisite names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "newJobManager(" agent/session_init.go            # where the child jobManager is built (Phase 2 Task 6)
grep -n "type spawnConfig" agent/session_config.go        # VERIFIED :189
grep -n "ctxToolCallID" agent/session_tools.go            # VERIFIED key def :28, set :313
grep -n "subCfg.spawn.parentToolCallID" agent/subagents.go # VERIFIED :218 — the pattern to mirror
grep -n "forward\b\|parentJobID" agent/jobs.go            # confirm Phase 2/4 did NOT already add these
```

If `newJobManager(` is absent from `session_init.go`, STOP — Phase 2 is the prerequisite and its child-jobManager wiring must be present.

- [ ] **Step 1: Write the failing test** — `agent/job_nested_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// A delegate child session must end up with a jobManager whose forward hook is
// wired to the parent and whose parentJobID is the spawning delegate's job_id.
func TestChildJobManagerHasForwardSeam(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Child blocks on bare "done" text so the delegate stays running while we inspect it.
	c.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	res := parent.createDelegate(context.Background(), delegateArgs{Task: "long", Background: true, BlockTimeoutMS: 120000})
	if res.JobID == "" {
		t.Fatalf("no delegate job_id: %+v", res)
	}
	defer parent.jobs.stop(res.JobID)

	_, childSessID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref %q: %v", res.TranscriptRef, err)
	}
	sub := parent.subagents.get(childSessID)
	if sub == nil {
		t.Fatalf("child subagent %q not tracked", childSessID)
	}
	childJM := sub.sess.jobs
	if childJM == nil {
		t.Fatal("child session has no jobManager")
	}
	if childJM.forward == nil {
		t.Error("child jobManager.forward must be wired to the parent")
	}
	if childJM.parentJobID != res.JobID {
		t.Errorf("child jobManager.parentJobID = %q, want the delegate job_id %q", childJM.parentJobID, res.JobID)
	}

	// The root parent's own jobManager has no forward hook and no parent job id.
	if parent.jobs.forward != nil {
		t.Error("root jobManager.forward must be nil")
	}
	if parent.jobs.parentJobID != "" {
		t.Errorf("root jobManager.parentJobID = %q, want empty", parent.jobs.parentJobID)
	}
}

// Compile-time guard: the forward hook type matches jobstore.Event.
var _ = func(jm *jobManager) func(jobstore.Event) { return jm.forward }
```

NOTE for the implementer: `createDelegate`/`delegateArgs`/`decodeRef`/`fakeAdapter`/`NewOpenAIProfile` are Phase 3 + existing helpers. `decodeRef` is `agent/transcript_ref.go:20` (VERIFIED). `sub.sess.jobs` reads the child's Phase 2 jobManager field — confirm the field name with `grep -n "jobs " agent/session.go`.

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestChildJobManagerHasForwardSeam -v`. Expected: FAIL to compile (`jobManager has no field forward`/`parentJobID`).

- [ ] **Step 3: Implement.**

In `agent/session_config.go`, add to `spawnConfig` (after `sharedTaskStore`, before the prompt-shaping fields — keep the `json:"-"` parent comment accurate; the new fields are runtime-only, never persisted, exactly like the rest of `spawnConfig`):

```go
	// forwardJobEvent, when non-nil, is the parent jobManager's forwarding hook:
	// a child session calls it to mirror a nested job's events into the parent's
	// durable job store (Phase 5). nil for the root session. Set by spawnAgent;
	// never persisted (the whole spawnConfig is json:"-").
	forwardJobEvent func(jobstore.Event)

	// parentJobID is the delegate job_id that spawned this child session. A
	// shell job the child starts records it as parent_job_id, making the job
	// parent-visible (Phase 5). Empty for the root session.
	parentJobID string
```

Add the import `"primeradiant.com/serf/agent/internal/jobstore"` to `session_config.go` (confirm it is not already imported).

In `agent/session_tools.go`, next to `ctxToolCallID` (`:27-28`), add:

```go
// ctxParentJobID carries the spawning delegate job_id into spawnAgent so the
// child's nested jobs record it as parent_job_id (Phase 5).
const ctxParentJobID ctxKey = "parentJobID"
```

In `agent/subagents.go`, in `spawnAgent`, right after the existing `parentToolCallID` capture (`:217-219`), add the parent-job-id + forward-hook plumbing. The forward closure appends to the **parent's** jobManager store (the seam itself is Task 2; here just capture it):

```go
	if jobID, ok := ctx.Value(ctxParentJobID).(string); ok {
		subCfg.spawn.parentJobID = jobID
	}
	if s.jobs != nil {
		subCfg.spawn.forwardJobEvent = s.jobs.forwardEvent
	}
```

(`s.jobs.forwardEvent` is the parent-side append method added in Task 2. To keep Task 1 building before Task 2 exists, you may temporarily set `subCfg.spawn.forwardJobEvent = nil` here and switch to `s.jobs.forwardEvent` in Task 2 — but the cleaner path is to land Task 2's `forwardEvent` method in the same edit. Prefer implementing `forwardEvent` now, as Task 2 specifies, so this line is final.)

In `agent/session_init.go`, immediately after the child's `s.jobs = newJobManager(...)` line (Phase 2 Task 6), install the seam from the spawn config:

```go
	s.jobs.forward = cfg.spawn.forwardJobEvent
	s.jobs.parentJobID = cfg.spawn.parentJobID
```

(For a root/fresh/restored session, `cfg.spawn` is the zero value, so `forward` is nil and `parentJobID` is empty — correct.)

**The `createDelegate` ordering change (load-bearing — the test's `childJM.parentJobID == res.JobID` assertion depends on it).** Phase 3's `createDelegate` mints the delegate `job_id` **after** `spawnAgent` returns (Phase 3 Task 5, step 4: spawn the child, then `jobstore.NewJobID()`). But the child's nested shell jobs must record **this delegate's** `job_id` as their `parent_job_id`, and the child's `jobManager` is built **inside** `spawnAgent`/`NewSession` — before Phase 3 mints the id. So `createDelegate` must **mint the delegate `job_id` first**, then stamp it on the context it passes to `spawnAgent`, so `spawnAgent` reads it via `ctxParentJobID` and lands it on `subCfg.spawn.parentJobID` before `NewSession`.

In `agent/job_delegate.go` `createDelegate`, reorder so the job id is minted before the spawn and threaded through context:

```go
	jobID := jobstore.NewJobID()                       // mint FIRST (was after spawnAgent in Phase 3)
	spawnCtx := context.WithValue(ctx, ctxParentJobID, jobID)
	result, err := s.spawnAgent(spawnCtx, args.Task, args.Model, "", 0, args.AgentType, args.ReasoningEffort, nil, nil, args.ResultSchema)
	// ... Phase 3 step 4 onward, but use the already-minted `jobID` instead of minting a new one ...
```

This is a one-line reorder of an existing Phase 3 mint plus a `context.WithValue`. It does NOT change the delegate's own record (the delegate job is owned by the parent and has no `parent_job_id`; only the child's nested shell jobs read `ctxParentJobID`). Verify the Phase 3 mint site with `grep -n "NewJobID()" agent/job_delegate.go` and move it above the `spawnAgent` call; if Phase 3 named the spawned-result var differently, keep that name.

The `jobManager` struct fields `forward func(jobstore.Event)` and `parentJobID string` are added in Task 2 (the struct edit). If you are landing Task 1 and Task 2 as one build, add them now; otherwise this task's compile depends on Task 2's struct fields, so **do Task 2's struct-field + `forwardEvent` edit first if the build complains** — the two tasks are adjacent and the split is only for test granularity.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestChildJobManagerHasForwardSeam -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_config.go agent/session_tools.go agent/subagents.go agent/session_init.go agent/job_delegate.go agent/job_nested_test.go
git commit -m "feat(agent): spawn seam to forward nested job events to the parent"
```

---

## Task 2: forward hook + parent_job_id on the child's shell job

**Files:**
- Modify: `agent/jobs.go` (add `forward func(jobstore.Event)` + `parentJobID string` fields to `jobManager`; `createShell` sets `ParentJobID` from `jm.parentJobID` and forwards `job_started`)
- Create: `agent/jobs_nested.go` (`forwardEvent` parent-side append; `forwardLocked` child-side mirror helper)
- Test: `agent/job_nested_test.go` (extend)

Now the child's `createShell` records `parent_job_id` and forwards the `job_started` event into the parent store. After this task, a subagent's background shell job appears as a forwarded `running` record in the parent's store (Task 4 surfaces it through `job_list`).

- [ ] **Step 0: Confirm names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "type jobManager\|func newJobManager\|sessionID\|type createShellOpts\|func (jm \*jobManager) createShell\|EventJobStarted" agent/jobs.go
```

Note the session-id field name (`jm.sessionID` per Phase 4) and the `createShellOpts` fields.

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`:

```go
// A shell job created in a child jobManager (with a parentJobID + forward hook)
// records parent_job_id AND forwards a job_started into the parent store.
func TestNestedShellForwardsJobStarted(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()

	parentJM, err := newJobManager(parentDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	// Wire the child to forward into the parent, as the spawn seam does (Task 1).
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	rec, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	if rec.ParentJobID != "job_PARENTDELEGATE" {
		t.Errorf("child record parent_job_id = %q, want job_PARENTDELEGATE", rec.ParentJobID)
	}

	// The parent store now holds a forwarded record under the SAME job_id.
	precs, err := parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	pr := precs[rec.JobID]
	if pr == nil {
		t.Fatalf("forwarded record not in parent store; have %v", keysOf(precs))
	}
	if pr.ParentJobID != "job_PARENTDELEGATE" || pr.OwnerSessionID != "CHILD" || pr.VisibleToSession != "PARENT" {
		t.Errorf("forwarded record fields wrong: %+v", pr)
	}
	if pr.Status != jobstore.StatusRunning {
		t.Errorf("forwarded record status = %q, want running", pr.Status)
	}
}

func keysOf(m map[string]*jobstore.JobRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

NOTE: this test depends on the §3.4 routing decision for `visible_to_session_id`. The forwarded record's `VisibleToSession` must be the **parent** session id (`PARENT`), not the child's. The owner's own `job_started` carries `OwnerSessionID = VisibleToSession = CHILD` (Phase 2 `createShell` sets both from `jm.sessionID`); forwarding **rewrites `VisibleToSession` to the parent** so the parent's `Fold` produces a record visible to the parent and the dedupe key is parent-scoped. This rewrite is the one field the forward path changes (everything else verbatim) — see step 3.

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestNestedShellForwardsJobStarted -v`. Expected: FAIL to compile (`jobManager has no field forward`/`forwardEvent`/`parentJobID`, or `createShellOpts` has no `ParentJobID` wiring).

- [ ] **Step 3: Implement.**

In `agent/jobs.go`, add to the `jobManager` struct:

```go
	// forward, when non-nil, mirrors a nested job's events into a parent
	// (visible) session's store. Set at spawn for delegate child sessions
	// (Phase 5). nil for the root.
	forward func(jobstore.Event)
	// parentJobID is the delegate job_id that owns this session's nested jobs
	// (their parent_job_id). Empty for the root.
	parentJobID string
```

In `agent/jobs.go`, in `createShell` (Phase 2 Task 3), set `ParentJobID` on the record/`job_started` event from `jm.parentJobID`, and forward the `job_started`. The minimal change: after the record + `EventJobStarted` are built and the event is appended to `jm.store`, mirror it:

```go
	// (Phase 2 builds rec + the started event, then jm.store.Append(started).)
	rec.ParentJobID = jm.parentJobID
	started.ParentJobID = jm.parentJobID   // the EventJobStarted being appended
	// ... existing: jm.store.Append(started); open OutputStore; register runningJob ...
	jm.forwardLocked(started)              // mirror to the parent if this is a nested job
```

(Match the Phase 2 variable names — `rec`, `started`, or however Phase 2 named the record and the `EventJobStarted`. If Phase 2 builds the event inline inside `Append`, hoist it into a named `started := jobstore.Event{...}` so it can be both appended and forwarded.)

Create `agent/jobs_nested.go`:

```go
package agent

import "primeradiant.com/serf/agent/internal/jobstore"

// forwardLocked mirrors a nested job's event into the parent (visible) session's
// store via the forward hook, rewriting visible_to_session_id to the parent so
// the parent's fold yields a parent-visible record and the terminal dedupe key
// is parent-scoped. It is a no-op for the root (forward == nil) and for events
// of non-nested jobs (parentJobID == ""). The event payload — including
// terminal_generation — is copied VERBATIM; only visible_to_session_id is
// rewritten. The parent's Append reassigns its own seq.
//
// Named *Locked because callers (createShell/finalize) invoke it while holding
// jm.mu; the hook itself must not re-enter jm.mu (it targets a DIFFERENT
// manager's store, which has its own lock).
func (jm *jobManager) forwardLocked(e jobstore.Event) {
	if jm.forward == nil || jm.parentJobID == "" {
		return
	}
	jm.forward(e) // e is passed by value; the parent endpoint stamps visibility
}

// forwardEvent is the parent-side endpoint installed onto a child jobManager's
// forward hook. It rewrites the event's visible_to_session_id to this (parent)
// manager's session and appends it to the parent store. terminal_generation and
// job_id ride through unchanged, so the dedupe key matches the owner's.
func (jm *jobManager) forwardEvent(e jobstore.Event) {
	e.VisibleToSession = jm.sessionID
	_ = jm.store.Append(e) // append is internally locked; a forward failure must not crash the child
}
```

DESIGN NOTE for the implementer: the rewrite of `VisibleToSession` is done **on the parent side** (`forwardEvent`), not the child side, so the child's own record keeps `VisibleToSession = childID` while the parent's forwarded copy gets `VisibleToSession = parentID`. This is the simplest correct split: the child forwards verbatim; the receiving parent stamps its own visibility. Do NOT also rewrite `OwnerSessionID` — the owner stays the child (that is how routing finds the owner runtime in Task 5/6). Confirm `jm.sessionID` is the real field name (Phase 4 uses it); if Phase 2 named it `jm.id`, use that.

LOCK NOTE: `forwardEvent` runs on the child's goroutine but touches the **parent's** `store` (a different `*jobstore.Store` with its own mutex via `Append`). It must NOT take the parent `jobManager.mu`. Appending to the parent store is sufficient for visibility (the parent reconstructs records by folding the store on `list`/reconcile); the parent's in-memory `running` overlay deliberately does **not** track forwarded jobs (the owner runtime lives in the child). This is consistent with the parent reconciling a forwarded `running` record as `runtime_lost` when no live owner exists (Task 7).

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestNestedShellForwardsJobStarted -v`. Expected: PASS. Then `cd agent && go test ./ -run TestChildJobManagerHasForwardSeam -v` (Task 1 still green).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/jobs_nested.go agent/job_nested_test.go
git commit -m "feat(agent): forward nested shell job_started + record parent_job_id"
```

---

## Task 3: forward the terminal — `job_finished` + notification carry the owner's `terminal_generation` verbatim

**Files:**
- Modify: `agent/jobs.go` (`finalize`: forward the `EventJobFinished` and `EventJobNotificationPending` for nested jobs, after the owner mints `terminal_generation`)
- Test: `agent/job_nested_test.go` (extend)

The owner's `finalize` mints `terminal_generation` once (Phase 2) and writes `job_finished` + `job_notification_pending`. For a nested job, those same events must be forwarded **verbatim** (same `terminal_generation`) so the parent store's forwarded record goes terminal with the identical dedupe key, and the parent arms the terminal notification.

- [ ] **Step 0: Confirm names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "func (jm \*jobManager) finalize\|NewTerminalGeneration\|EventJobFinished\|EventJobNotificationPending\|jm.enqueue" agent/jobs.go
```

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`:

```go
// When a nested job finalizes, the owner's job_finished (with its minted
// terminal_generation) is forwarded verbatim into the parent store, and a
// terminal notification is armed in the parent — NOT re-minted.
func TestNestedTerminalForwardsGenerationVerbatim(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()

	var parentNotifs []jobNotification
	parentJM, err := newJobManager(parentDir, "PARENT", func(n jobNotification) { parentNotifs = append(parentNotifs, n) })
	if err != nil {
		t.Fatal(err)
	}
	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	rec, err := childJM.createShell(createShellOpts{Command: "true", Description: "nested"})
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)

	// Owner store: terminal with a minted generation.
	crecs, _ := childJM.store.Load()
	ownerGen := crecs[rec.JobID].TerminalGen
	if ownerGen == "" || crecs[rec.JobID].Status != jobstore.StatusCompleted {
		t.Fatalf("owner record not terminal with a generation: %+v", crecs[rec.JobID])
	}

	// Parent store: forwarded terminal with the SAME generation (verbatim, not re-minted).
	precs, _ := parentJM.store.Load()
	pr := precs[rec.JobID]
	if pr == nil || pr.Status != jobstore.StatusCompleted {
		t.Fatalf("parent forwarded record not terminal: %+v", pr)
	}
	if pr.TerminalGen != ownerGen {
		t.Errorf("parent terminal_generation = %q, want owner's %q (verbatim)", pr.TerminalGen, ownerGen)
	}

	// The dedupe key matches in both stores except for the visible session, which
	// is correctly parent-scoped on the parent side and child-scoped on the owner.
	if pr.DedupeKey().TerminalGen != crecs[rec.JobID].DedupeKey().TerminalGen {
		t.Errorf("dedupe generation diverged: parent=%v owner=%v", pr.DedupeKey(), crecs[rec.JobID].DedupeKey())
	}
	if pr.VisibleToSession != "PARENT" {
		t.Errorf("parent forwarded record visible_to = %q, want PARENT", pr.VisibleToSession)
	}

	// The parent armed exactly one terminal notification for the nested job.
	if len(parentNotifs) != 1 || parentNotifs[0].JobID != rec.JobID {
		t.Fatalf("parent notifications = %+v, want one for %s", parentNotifs, rec.JobID)
	}
}
```

NOTE: `JobRecord.DedupeKey()` is Phase 1 (`agent/internal/jobstore/notify.go`, Phase 1 Task 6). The parent arming a notification for the forwarded terminal is what makes the parent inject a `<job-notification>` for the nested job (spec §10: "A parent-visible nested background job follows the same notification rules as a top-level background job").

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestNestedTerminalForwardsGenerationVerbatim -v`. Expected: FAIL (the parent store has no terminal forwarded record / no armed notification — `finalize` does not forward yet).

- [ ] **Step 3: Implement.**

In `agent/jobs.go` `finalize`, after the owner mints `terminal_generation` and builds the `EventJobFinished` (call it `finished`) and `EventJobNotificationPending` (call it `pending`) and appends them + calls `jm.enqueue`, forward both for a nested job. The key constraint: forward the **already-built** events so `terminal_generation` is the owner's minted value, not a new one:

```go
	// ... Phase 2 finalize: mint gen; build `finished` (EventJobFinished w/ gen);
	//     jm.store.Append(finished); build `pending` (EventJobNotificationPending w/ gen);
	//     jm.store.Append(pending); jm.enqueue(jobNotification{...}) ...
	jm.forwardLocked(finished)
	jm.forwardLocked(pending)
```

That is the entire change — two lines. `forwardLocked` (Task 2) no-ops for the root and for non-nested jobs, and copies the event verbatim (the owner's `terminal_generation` rides through). The parent's `forwardEvent` appends them; the parent's forwarded record folds to terminal (via the existing `applyEvent` first-terminal-wins, `fold.go:47-57`), and the `EventJobNotificationPending` fold sets the parent record's `NotifyState = pending` (`fold.go:60-63`).

**Arming the parent notification.** The forwarded `EventJobNotificationPending` only marks the parent's *durable* state pending; the in-memory `jm.enqueue` (which actually injects the `<job-notification>`) fires on the **owner** side, not the parent. So the parent must also enqueue. Two options — choose the simpler that the Phase 2 notification bridge supports:

- **(A) Forward an "arm" signal.** Have `forwardEvent`, when it appends an `EventJobNotificationPending`, also call the parent's `enqueue` with a `jobNotification` built from the forwarded terminal `job_finished` it just stored. This requires `forwardEvent` to either receive the `job_finished` first (it does — `finished` is forwarded before `pending`) and remember the terminal fields, or to reload the parent record. Simplest: in `forwardEvent`, when `e.Kind == jobstore.EventJobNotificationPending`, load the just-appended parent record for `e.JobID` and `jm.enqueue(jobNotificationFromRecord(rec))`.

  ```go
  func (jm *jobManager) forwardEvent(e jobstore.Event) {
      e.VisibleToSession = jm.sessionID
      _ = jm.store.Append(e)
      if e.Kind == jobstore.EventJobNotificationPending {
          if recs, err := jm.store.Load(); err == nil {
              if rec := recs[e.JobID]; rec != nil {
                  jm.enqueue(jobNotificationFromRecord(rec)) // parent injects the <job-notification>
              }
          }
      }
  }
  ```

  `jobNotificationFromRecord(rec *jobstore.JobRecord) jobNotification` builds `{JobID, JobType: string(rec.Type), Status: string(rec.Status), Reason: rec.Reason, OutputBytes: rec.OutputBytes, ExitCode: rec.ExitCode, TranscriptRef: rec.TranscriptRef}` — the §6 "payload from the durable JobRecord" rule. If Phase 2 already has such a helper (it builds the same struct in `finalize`/`reconcileLostJobs`), reuse it — `grep -n "jobNotification{" agent/jobs.go` and extract a shared `jobNotificationFromRecord` if it is duplicated.

Wire option (A). It keeps the parent's notification path identical to a top-level job (durable `pending` + an `enqueue`), and the parent's own delivery filter (Phase 2 §6: deliverable iff a durable record exists and `NotifyState == pending`) handles dedupe across restart using the verbatim generation.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestNestedTerminalForwardsGenerationVerbatim -v`. Expected: PASS. Re-run Tasks 1–2 to confirm no regression: `cd agent && go test ./ -run 'TestChildJobManagerHasForwardSeam|TestNestedShellForwardsJobStarted' -v`.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/jobs_nested.go agent/job_nested_test.go
git commit -m "feat(agent): forward nested terminal + notification with verbatim terminal_generation"
```

---

## Task 4: `job_list(include_nested=true)` surfaces forwarded records

**Files:**
- Modify: `agent/jobs.go` (`list`: honor `listFilter.IncludeNested` — include records with a non-empty `ParentJobID` only when set)
- Modify: `agent/session_tools_jobs.go` (the `job_list` handler passes `include_nested` through to `listFilter.IncludeNested`)
- Test: `agent/job_nested_test.go` (extend)

Phase 2's `job_list` handler hard-coded `IncludeNested:false` (Phase 2 Task 9). This task makes both the manager filter and the handler honor the parameter. Default stays `false` (forwarded nested records are hidden unless asked for).

- [ ] **Step 0: Confirm names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "type listFilter\|IncludeNested\|func (jm \*jobManager) list" agent/jobs.go
grep -n "include_nested\|listFilter{" agent/session_tools_jobs.go
```

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`:

```go
// The parent's list hides forwarded nested records by default and shows them
// under include_nested=true.
func TestJobListIncludeNestedFilter(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()
	parentJM, _ := newJobManager(parentDir, "PARENT", func(jobNotification) {})
	childJM, _ := newJobManager(childDir, "CHILD", func(jobNotification) {})
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	// A top-level parent job (no parent_job_id) + a forwarded nested job.
	top, _ := parentJM.createShell(createShellOpts{Command: "top", Description: "parent-own"})
	_ = top
	nested, _ := childJM.createShell(createShellOpts{Command: "nested", Description: "child-own"})

	def := parentJM.list(listFilter{}) // IncludeNested false
	for _, r := range def {
		if r.JobID == nested.JobID {
			t.Errorf("default list must hide the forwarded nested job %s", nested.JobID)
		}
	}

	withNested := parentJM.list(listFilter{IncludeNested: true})
	var sawNested bool
	for _, r := range withNested {
		if r.JobID == nested.JobID {
			sawNested = true
			if r.ParentJobID != "job_PARENTDELEGATE" {
				t.Errorf("nested record missing parent_job_id: %+v", r)
			}
		}
	}
	if !sawNested {
		t.Errorf("include_nested=true must surface the forwarded nested job %s", nested.JobID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestJobListIncludeNestedFilter -v`. Expected: FAIL (default list already includes the nested record because `list` ignores `IncludeNested`).

- [ ] **Step 3: Implement.**

In `agent/jobs.go` `list`, after loading + overlaying records and before the status/type filter (or folded into the same predicate), drop forwarded nested records unless `IncludeNested`:

```go
	for _, r := range candidates {
		if r.ParentJobID != "" && !f.IncludeNested {
			continue // forwarded nested job; hidden unless include_nested
		}
		// ... existing status/type filter + append ...
	}
```

In `agent/session_tools_jobs.go`, the `job_list` handler: parse `include_nested` and pass it through (Phase 2 hard-coded `false`):

```go
	f := listFilter{ /* Status, Type, Limit from Phase 2 */ }
	if v, ok := args["include_nested"].(bool); ok {
		f.IncludeNested = v
	}
	jobs := s.jobs.list(f)
```

The `include_nested` parameter is already on `DefJobList` (Phase 2 §5.7); no tool-definition change. The §5.7 return projection (emit `parent_job_id` as `null` when empty, present when set) is already the Phase 2 handler's job — confirm forwarded records emit a non-null `parent_job_id` (they will, since `ParentJobID` is set). If the Phase 2 projection omitted `parent_job_id`, add it to the projected shape now.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestJobListIncludeNestedFilter -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/session_tools_jobs.go agent/job_nested_test.go
git commit -m "feat(agent): job_list include_nested surfaces forwarded nested jobs"
```

---

## Task 5: `job_read_output` on a nested `job_id` routes to the owner runtime

**Files:**
- Create/extend: `agent/jobs_nested.go` (`ownerJobManagerFor(jobID) *jobManager` — parent→owner via `subagentManager`)
- Modify: `agent/session_tools_jobs.go` (the `job_read_output` handler routes a forwarded `job_id` to the owner's `readOutput`; falls back to the forwarded record when the owner runtime is gone)
- Test: `agent/job_nested_test.go` (extend)

Per spec §3.4/§10: the parent reads a nested job's output via the parent-visible `job_id`, by routing to the owner runtime if live (output lives in the **child's** `OutputStore`), else from the forwarded durable record (which after restart finalization reports its state; the bytes are routed, not mirrored, in v1 — Task 7 covers the restart case).

- [ ] **Step 0: Confirm names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "func (jm \*jobManager) readOutput\|func (m \*subagentManager) get\|\.sess.jobs\|func (s \*Session) jobs" agent/jobs.go agent/subagent_manager.go agent/session.go
grep -n "job_read_output\|readOutput(" agent/session_tools_jobs.go
```

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`. Use real sessions so the parent→child link is the production `subagentManager`:

```go
// Reading a forwarded nested job_id through the PARENT routes to the owner
// child's live output store.
func TestParentReadsNestedOutputViaOwnerRuntime(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	// Start a background delegate so a live child session + jobManager exists.
	del := parent.createDelegate(context.Background(), delegateArgs{Task: "host", Background: true, BlockTimeoutMS: 120000})
	if del.JobID == "" {
		t.Fatalf("no delegate job: %+v", del)
	}
	defer parent.jobs.stop(del.JobID)
	_, childSessID, _ := decodeRef(del.TranscriptRef)
	sub := parent.subagents.get(childSessID)
	if sub == nil {
		t.Fatal("child not tracked")
	}
	childJM := sub.sess.jobs

	// The child starts a nested background shell job and writes output.
	nested, err := childJM.createShell(createShellOpts{Command: "noop", Description: "nested"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = childJM.running[nested.JobID].output.Append([]byte("nested output line\n"))

	// The parent reads via the parent-visible job_id and routes to the owner.
	content, err := parent.readNestedOrLocalOutput(nested.JobID, 1<<16)
	if err != nil {
		t.Fatalf("parent read of nested job: %v", err)
	}
	if content != "nested output line\n" {
		t.Errorf("parent read = %q, want the child's output", content)
	}
}
```

NOTE: `childJM.running[nested.JobID].output` reaches the child's `OutputStore` directly (the Phase 2 `runningJob.output` field) — mirrors the Phase 2 `TestJobManagerReadOutput` test. `readNestedOrLocalOutput` is the parent-side router this task adds (a thin wrapper the handler calls; named to make the routing explicit and unit-testable).

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestParentReadsNestedOutputViaOwnerRuntime -v`. Expected: FAIL (`readNestedOrLocalOutput`/`ownerJobManagerFor` undefined).

- [ ] **Step 3: Implement** in `agent/jobs_nested.go`:

```go
// ownerJobManagerFor returns the live jobManager that OWNS the given job_id when
// it is a forwarded nested job whose owner child session is still tracked, plus
// the owner record; (nil, nil) when the job_id is not a forwarded nested job
// owned by a live child. The parent reaches the owner through its subagentManager
// (s.subagents): the forwarded record's OwnerSessionID is the child session id.
func (s *Session) ownerJobManagerFor(jobID string) (*jobManager, *jobstore.JobRecord) {
	recs, err := s.jobs.store.Load()
	if err != nil {
		return nil, nil
	}
	rec := recs[jobID]
	if rec == nil || rec.ParentJobID == "" {
		return nil, nil // not a forwarded nested job
	}
	if rec.OwnerSessionID == s.id {
		return nil, rec // owned by this session, not nested-routed
	}
	sub := s.subagents.get(rec.OwnerSessionID)
	if sub == nil || sub.sess == nil || sub.sess.jobs == nil {
		return nil, rec // owner runtime gone; caller falls back to the forwarded record
	}
	return sub.sess.jobs, rec
}

// readNestedOrLocalOutput reads a job's output, routing a forwarded nested job to
// its owner child runtime when live; otherwise it reads this session's own store
// (Phase 2 readOutput) — which for a forwarded-but-owner-gone job returns the
// durable record's state (bytes are owner-routed, not mirrored, in v1).
func (s *Session) readNestedOrLocalOutput(jobID string, tailBytes int) (string, error) {
	if ownerJM, _ := s.ownerJobManagerFor(jobID); ownerJM != nil {
		content, _, _, err := ownerJM.readOutput(jobID, tailBytes)
		return content, err
	}
	content, _, _, err := s.jobs.readOutput(jobID, tailBytes)
	return content, err
}
```

(Match the real `readOutput` signature — Phase 4 may thread `grep`/`block`. The wrapper here uses the minimal `(jobID, tailBytes)` form; thread the extra params through if Phase 2/4 added them. Confirm `s.id` is the Session id field — VERIFIED `agent/session.go:34`.)

In `agent/session_tools_jobs.go`, the `job_read_output` handler: replace the direct `s.jobs.readOutput(...)` call with `s.readNestedOrLocalOutput(...)` (preserve the rest of the §5.6 return-shape marshaling — `status`/`grep`/`matches`/`total_bytes`/`exit_code` still come from the routed manager's record; the simplest is to make the router return the record too, or have the handler call `ownerJobManagerFor` once and use the chosen manager for both the read and the record lookup). Keep it minimal: route the read; the status/exit_code fields are read from whichever manager owns the job.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestParentReadsNestedOutputViaOwnerRuntime -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs_nested.go agent/session_tools_jobs.go agent/job_nested_test.go
git commit -m "feat(agent): job_read_output routes a nested job_id to the owner runtime"
```

---

## Task 6: `job_stop` on a nested `job_id`, and `job_stop(parent, include_children=true)`

**Files:**
- Extend: `agent/jobs_nested.go` (`stopChildren(delegateJobID) ([]*jobstore.JobRecord, error)` — stop visible active nested jobs of a delegate)
- Modify: `agent/session_tools_jobs.go` (the `job_stop` handler routes a forwarded `job_id` to the owner's `stop`; honors `include_children` for a delegate target)
- Test: `agent/job_nested_test.go` (extend)

Two cases (spec §5.8/§10):
1. `job_stop(nested_job_id)` — route to the owner runtime if live (the child's `stop` signals the process group and finalizes; the owner's `job_finished` is forwarded to the parent by Task 3). If the owner runtime is gone (restart), the forwarded record finalizes `stopped/runtime_lost` via reconciliation (Task 7), not here.
2. `job_stop(delegate_job_id, include_children=true)` — stop the delegate AND its visible active nested jobs.

- [ ] **Step 0: Confirm names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "func (jm \*jobManager) stop\|func (m \*subagentManager) get\|include_children\|func (s \*Session) ownerJobManagerFor" agent/jobs.go agent/subagent_manager.go agent/session_tools_jobs.go agent/jobs_nested.go
```

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`:

```go
// Stopping a forwarded nested job_id through the PARENT routes to the owner and
// cancels the child's job.
func TestParentStopsNestedJobViaOwner(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	del := parent.createDelegate(context.Background(), delegateArgs{Task: "host", Background: true, BlockTimeoutMS: 120000})
	defer parent.jobs.stop(del.JobID)
	_, childSessID, _ := decodeRef(del.TranscriptRef)
	sub := parent.subagents.get(childSessID)
	childJM := sub.sess.jobs

	se := mustStreamingExec(t, dir)               // LocalExecutionEnvironment as StreamingExecutor (Phase 2 helper)
	nestedRes := runShell(context.Background(), childJM, se, shellArgs{Command: "sleep 30", Background: true, BlockTimeoutMS: 120000})
	if nestedRes.JobID == "" {
		t.Fatal("no nested shell job")
	}

	rec, err := parent.stopNestedOrLocal(nestedRes.JobID, false)
	if err != nil {
		t.Fatalf("parent stop of nested job: %v", err)
	}
	if rec.Status != jobstore.StatusCancelled && rec.Status != jobstore.StatusStopped {
		t.Errorf("nested job status after stop = %q, want cancelled/stopped", rec.Status)
	}
}

// job_stop(delegate, include_children=true) stops visible active nested jobs.
func TestStopDelegateIncludeChildrenStopsNested(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	del := parent.createDelegate(context.Background(), delegateArgs{Task: "host", Background: true, BlockTimeoutMS: 120000})
	_, childSessID, _ := decodeRef(del.TranscriptRef)
	childJM := parent.subagents.get(childSessID).sess.jobs
	se := mustStreamingExec(t, dir)
	nested := runShell(context.Background(), childJM, se, shellArgs{Command: "sleep 30", Background: true, BlockTimeoutMS: 120000})

	stopped, err := parent.stopChildren(del.JobID)
	if err != nil {
		t.Fatalf("stopChildren: %v", err)
	}
	var sawNested bool
	for _, r := range stopped {
		if r.JobID == nested.JobID {
			sawNested = true
		}
	}
	if !sawNested {
		t.Errorf("include_children must stop the visible nested job %s; stopped=%v", nested.JobID, stopped)
	}
	_, _ = parent.jobs.stop(del.JobID) // cleanup the delegate itself
}
```

NOTE: `mustStreamingExec(t, dir)` builds a `LocalExecutionEnvironment` and asserts `StreamingExecutor` — Phase 2's `newShellTestRig` does this inline (`env.(execenv.StreamingExecutor)`); reuse that helper or its body. Confirm with `grep -n "newShellTestRig\|StreamingExecutor)" agent/job_shell_test.go`.

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run 'TestParentStopsNestedJobViaOwner|TestStopDelegateIncludeChildrenStopsNested' -v`. Expected: FAIL (`stopNestedOrLocal`/`stopChildren` undefined).

- [ ] **Step 3: Implement** in `agent/jobs_nested.go`:

```go
// stopNestedOrLocal stops a job by its parent-visible job_id, routing a forwarded
// nested job to its owner child runtime when live; otherwise it stops this
// session's own job (Phase 2 stop). It returns the (parent-visible) terminal
// record. When the owner runtime is gone, the local stop on a forwarded record
// finalizes it stopped/runtime_lost (the parent owns the forwarded durable
// record), per spec §10.
func (s *Session) stopNestedOrLocal(jobID string, includeChildren bool) (*jobstore.JobRecord, error) {
	if ownerJM, _ := s.ownerJobManagerFor(jobID); ownerJM != nil {
		rec, err := ownerJM.stop(jobID)
		// The owner's finalize forwards job_finished to the parent (Task 3); the
		// parent-visible record converges. Return the owner's terminal record.
		return rec, err
	}
	// include_children only applies to a delegate target (handled by the caller
	// before this point); here it is a plain local/forwarded-owner-gone stop.
	return s.jobs.stop(jobID)
}

// stopChildren stops the visible active nested jobs whose parent_job_id is the
// given delegate job_id, routing each to its owner runtime. It returns the
// stopped records. Errors stopping an individual child are collected but do not
// abort the rest (best-effort, spec §5.8 include_children).
func (s *Session) stopChildren(delegateJobID string) ([]*jobstore.JobRecord, error) {
	recs, err := s.jobs.store.Load()
	if err != nil {
		return nil, err
	}
	var stopped []*jobstore.JobRecord
	for id, r := range recs {
		if r.ParentJobID != delegateJobID || r.Status.IsTerminal() {
			continue
		}
		sr, serr := s.stopNestedOrLocal(id, false)
		if serr != nil || sr == nil {
			continue
		}
		stopped = append(stopped, sr)
	}
	return stopped, nil
}
```

In `agent/session_tools_jobs.go`, the `job_stop` handler:
- Parse `include_children` (already a `DefJobStop` param, Phase 2 §5.8).
- Replace the direct `s.jobs.stop(job_id)` with `s.stopNestedOrLocal(job_id, includeChildren)` for the primary target.
- When `include_children == true`, after stopping the primary target, also call `s.stopChildren(job_id)` (the children of the targeted delegate). The return shape stays the §5.8 `{job_id, status, reason}` for the primary target — the child stops are durable-and-notified, not enumerated in the primary return (the parent learns of each child terminal via its forwarded notification). If a richer return is desired, this is where it would go; v1 keeps the §5.8 shape.

```go
	includeChildren, _ := args["include_children"].(bool)
	rec, err := s.stopNestedOrLocal(jobID, includeChildren)
	if err != nil {
		return nil, err
	}
	if includeChildren {
		_, _ = s.stopChildren(jobID) // best-effort; each child's terminal is forwarded + notified
	}
	// ... marshal {job_id, status, reason} from rec (Phase 2 §5.8 shape) ...
```

**`not_controllable` note (spec §5.8/§10).** If the owner runtime is believed live but the routed `stop` cannot perform the op, that surfaces as a synchronous `not_controllable` tool error — but in this implementation `ownerJobManagerFor` returns the owner `jobManager` only when the child session + its jobManager are live, and `jm.stop` always either signals or finalizes, so the `not_controllable` path is not reachable in v1 (it is reserved for a future cross-process owner). Leave a one-line code comment citing §5.8 so the reviewer knows the omission is deliberate, not a gap.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run 'TestParentStopsNestedJobViaOwner|TestStopDelegateIncludeChildrenStopsNested' -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs_nested.go agent/session_tools_jobs.go agent/job_nested_test.go
git commit -m "feat(agent): job_stop routes nested job_id to owner; include_children stops nested jobs"
```

---

## Task 7: restart — a forwarded `running` nested job with no live owner reconciles to `stopped/runtime_lost`

**Files:**
- Test: `agent/job_nested_test.go` (extend)
- Modify (only if the test reveals a real gap): `agent/jobs.go` (`reconcileLostJobs` must treat a forwarded record with no live owner as lost)

Per spec §7/§10: after a restart, the parent reconstructs its store (including forwarded records). A forwarded record whose latest state is `running` and which has no live owner runtime finalizes **exactly once** as `stopped/runtime_lost`, using the same parent-visible `job_id` and dedupe key. This reuses the Phase 1 `Reconcile` + Phase 2 `reconcileLostJobs` directly — a forwarded `running` record is a `running` record in the parent store with no entry in the parent's `jm.running` overlay, so `Reconcile` already finalizes it. This task PROVES that and adds the guard only if Phase 2's live-set logic accidentally treats forwarded records as live.

- [ ] **Step 0: Confirm names.**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "func (jm \*jobManager) reconcileLostJobs\|jobstore.Reconcile\|jm.running" agent/jobs.go
```

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`. Seed a parent store with a forwarded `running` nested record (no live owner), then reconcile:

```go
// On restart, a forwarded nested job stuck running (no live owner runtime)
// finalizes stopped/runtime_lost in the parent store, exactly once.
func TestForwardedNestedReconcilesRuntimeLostOnRestart(t *testing.T) {
	parentDir := t.TempDir()

	// Simulate a pre-restart parent store: a forwarded job_started arrived, but the
	// owner runtime (child session) is gone and no job_finished was forwarded.
	seed, err := newJobManager(parentDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1, 0).UTC()
	_ = seed.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		JobID:            "job_NESTED",
		Type:             jobstore.JobShell,
		Command:          "sleep 999",
		ParentJobID:      "job_DELEGATE",
		OwnerSessionID:   "CHILDGONE",
		VisibleToSession: "PARENT",
		StartedAt:        &start,
	})
	_ = seed.store.Close()

	var queued []jobNotification
	jm, err := newJobManager(parentDir, "PARENT", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }
	// No subagents tracked, no running overlay → the owner runtime is absent.

	jm.reconcileLostJobs()

	recs, _ := jm.store.Load()
	pr := recs["job_NESTED"]
	if pr == nil || pr.Status != jobstore.StatusStopped || pr.Reason != "runtime_lost" {
		t.Fatalf("forwarded nested record = %+v, want stopped/runtime_lost", pr)
	}
	if pr.ParentJobID != "job_DELEGATE" {
		t.Errorf("parent_job_id lost during reconcile: %+v", pr)
	}
	if len(queued) != 1 || queued[0].JobID != "job_NESTED" {
		t.Fatalf("expected one runtime_lost notification for the nested job, got %+v", queued)
	}

	// Idempotent: a second reconcile is a no-op (already terminal).
	jm.reconcileLostJobs()
	recs2, _ := jm.store.Load()
	if recs2["job_NESTED"].TerminalGen != pr.TerminalGen {
		t.Errorf("reconcile re-minted terminal_generation: %q -> %q", pr.TerminalGen, recs2["job_NESTED"].TerminalGen)
	}
}
```

NOTE: `reconcileLostJobs` is Phase 2 Task 6; it builds the live set from `jm.running`. Forwarded records are never in the parent's `jm.running` (Task 2 deliberately does not register them there), so this should pass with no `jobs.go` change. If it FAILS because the parent erroneously skips records it does not "own" (e.g. a Phase 2 `reconcileLostJobs` that filters `r.OwnerSessionID == jm.sessionID`), that is the bug to fix: reconciliation must cover **every** `running` record visible in this store with no live runtime, forwarded or not.

- [ ] **Step 2: Run test to verify it fails (or passes immediately).** `cd agent && go test ./ -run TestForwardedNestedReconcilesRuntimeLostOnRestart -v`. If it passes immediately, the Phase 1/2 reconciliation already covers forwarded records — record that and proceed. If it fails, inspect why.

- [ ] **Step 3: Implement / fix** only what the failure reveals. The likely fix (if needed): ensure `reconcileLostJobs` does NOT restrict reconciliation to owner-equals-self records — `jobstore.Reconcile(recs, live, now)` should be called over ALL folded records, with `live` = the parent's `jm.running` keys. Do not special-case forwarded records; the generic `running`-without-live-runtime rule already covers them. If the parent's live set must additionally exclude any nested job whose owner child is still tracked-and-live (so a still-running forwarded job is NOT reconciled away while its owner lives), add that: build `live` to also include forwarded job ids whose `OwnerSessionID` is a currently-tracked live child (`s.subagents.get(rec.OwnerSessionID) != nil`). Reconciliation runs at restore when no children are tracked yet, so in practice the forwarded-but-owner-live case does not arise at reconcile time — but document the reasoning in a comment.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestForwardedNestedReconcilesRuntimeLostOnRestart -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/job_nested_test.go
git commit -m "test(agent): forwarded nested job reconciles to runtime_lost on restart"
```

---

## Task 8: end-to-end through the tools + full-suite green + live smoke

**Files:**
- Test: `agent/job_nested_test.go` (extend — drive the real `shell` (child) and parent `job_list`/`job_read_output`/`job_stop` tools)
- Verification only otherwise.

This task proves the whole nested path through the **registered tools** (not just the manager methods), then runs the full suite and a live smoke.

- [ ] **Step 1: Write the failing test** — extend `agent/job_nested_test.go`. Start a background delegate, have the child call its `shell` tool with `background=true`, then from the parent call `job_list(include_nested=true)`, `job_read_output`, and `job_stop` on the nested `job_id` through the registered tools:

```go
func TestNestedShellEndToEndThroughTools(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	del := parent.createDelegate(context.Background(), delegateArgs{Task: "host", Background: true, BlockTimeoutMS: 120000})
	defer parent.jobs.stop(del.JobID)
	_, childSessID, _ := decodeRef(del.TranscriptRef)
	child := parent.subagents.get(childSessID).sess

	// The child starts a nested background shell job THROUGH ITS shell tool.
	shellOut, err := child.reg.Get("shell").Exec(context.Background(), child.env, map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	var sj map[string]any
	_ = json.Unmarshal([]byte(shellOut.(string)), &sj)
	nestedID, _ := sj["job_id"].(string)
	if nestedID == "" {
		t.Fatal("child shell did not return a nested job_id")
	}

	// Parent job_list hides it by default, shows it with include_nested.
	defOut, _ := parent.reg.Get("job_list").Exec(context.Background(), parent.env, map[string]any{})
	if strings.Contains(defOut.(string), nestedID) {
		t.Errorf("default job_list leaked the nested job")
	}
	nestedOut, _ := parent.reg.Get("job_list").Exec(context.Background(), parent.env, map[string]any{"include_nested": true})
	if !strings.Contains(nestedOut.(string), nestedID) {
		t.Errorf("job_list include_nested missing nested job %s:\n%s", nestedID, nestedOut)
	}

	// Parent reads + stops the nested job by the parent-visible job_id.
	if _, err := parent.reg.Get("job_read_output").Exec(context.Background(), parent.env, map[string]any{"job_id": nestedID}); err != nil {
		t.Errorf("parent job_read_output of nested job: %v", err)
	}
	stopOut, err := parent.reg.Get("job_stop").Exec(context.Background(), parent.env, map[string]any{"job_id": nestedID})
	if err != nil {
		t.Fatalf("parent job_stop of nested job: %v", err)
	}
	if !strings.Contains(stopOut.(string), "cancelled") && !strings.Contains(stopOut.(string), "stopped") {
		t.Errorf("nested job_stop result = %s, want cancelled/stopped", stopOut)
	}
}
```

NOTE: `child.reg.Get("shell")` — the child session has the job-capable `shell` tool from Phase 2 (registered on every session). `child.env` / `parent.env` are the Session env field (`agent/session.go:39`). Confirm the `reg`/`env` field names with `grep -n "reg \+\*tool.Registry\|env \+execenv" agent/session.go`. This test depends on Tasks 1–6 all landing.

- [ ] **Step 2: Run test to verify it fails, then passes after fixes** — `cd agent && go test ./ -run TestNestedShellEndToEndThroughTools -v`. If a handler still hard-codes `include_nested=false` or does not route, fix per Tasks 4–6.

- [ ] **Step 3: Run the full module test + lint**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
make test && make lint
```
Expected: all modules PASS; lint clean (golangci ×4 + `serf-namingcheck`/`internalcheck`/`docscheck`). Fix any fallout. Likely touch points: the new `spawnConfig` fields (ensure they do not break the converter/round-trip tests — they are `json:"-"`, so they should not appear in `schema.ConfigSnapshot`; if a snapshot/parity test enumerates `spawnConfig`, it should still pass since these are runtime-only); any nested-visibility change to `job_list`'s projected shape (update the expected JSON in Phase 2's `job_list` tests if `parent_job_id` projection changed).

- [ ] **Step 4: Live smoke** (per `reference_serf_live_run` recipe — build a standalone binary, do NOT touch a running serve):

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go build -o /tmp/serf ./cmd/serf
. "$PWD/.env"
# In a scratch dir, run serf with a real model (--model oai-work/<model>) and ask it to:
#  1. delegate a task that itself starts a background shell job (e.g. "delegate: start a background `sleep 60` shell job and report its job_id");
#  2. from the parent, job_list(include_nested=true) → confirm the nested job appears with a parent_job_id;
#  3. job_read_output on the nested job_id from the parent → confirm it routes (returns the child's output);
#  4. job_stop on the nested job_id from the parent → confirm cancelled/stopped;
#  5. confirm the nested job's terminal <job-notification> is injected into the parent on completion (or after stop).
```
Expected: the nested shell job is parent-visible only under `include_nested`, controllable by the parent-visible `job_id`, and its terminal notification reaches the parent.

- [ ] **Step 5: Commit any test/lint fixups**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git status   # review before adding
git add -A
git commit -m "test(job-control): phase 5 nested shell jobs suite green"
```

---

## Phase 5 self-review (run against the spec)

- **Spec coverage:**
  - §10 (nested jobs: subagents start shell jobs; `parent_job_id`, not a separate control plane; forwarded into parent-visible records; parent reads/stops via the parent-visible `job_id`; cross-store `terminal_generation` verbatim) → Task 1 (spawn seam), Task 2 (forward `job_started` + record `parent_job_id`, parent stamps `visible_to`), Task 3 (forward terminal + notification verbatim generation), Tasks 4–6 (list/read/stop), Task 7 (restart reconciliation). The single load-bearing invariant — the owner mints `terminal_generation`, the parent copies it verbatim — is asserted directly in Task 3 (`pr.TerminalGen == ownerGen`).
  - §5.7 (`job_list include_nested`, default false) → Task 4 (manager filter + handler passthrough).
  - §3.2/§3.3 (`parent_job_id`, `terminal_generation` minted once, dedupe key) → Tasks 2/3; the dedupe key matches across stores (Task 3 asserts `DedupeKey().TerminalGen` parity).
  - §3.4 (output routing — "mirroring or durable routing metadata"; not model-visible) → Task 5 chooses **durable routing** (the parent routes `job_read_output` to the owner's `OutputStore`; bytes are not mirrored into the parent store). The routing is not model-visible (the model uses only the parent-visible `job_id`).
  - §5.8 (`job_stop include_children`; non-recursive default; confirmed→cancelled / unconfirmed→stopped) → Task 6 (`stopNestedOrLocal` routes; `stopChildren` for `include_children`; `not_controllable` documented as unreachable-in-v1).
  - §7 (forwarded nested jobs reconcile to `stopped/runtime_lost` exactly once, same `job_id` + dedupe key) → Task 7 (reuses Phase 1 `Reconcile` + Phase 2 `reconcileLostJobs`; idempotence + generation-stability asserted).
  - Reference contract `docs/job-control.md` §"Nested jobs" (1007–1023): parent-visible `job_id` is the only control handle (Tasks 5/6 route by that id); forwarded durable events (Tasks 2/3); `include_nested`/`include_children` (Tasks 4/6); terminal notifications for armed forwarded jobs (Task 3); parent `job_stop` routes to the owner if live (Task 6).
- **Builds on the EXACT Phase 1–4 APIs (no invention):** every Phase 2/3/4 symbol carries a `grep -n … agent/jobs.go` (or equivalent) confirm-first note, because those names are fixed by the prerequisite merges, not by this plan. The grounded-against-current-code items are the spawn machinery (`spawnConfig:189`, `spawnAgent:155`, `ctxToolCallID:28/313`, `subCfg.spawn.parentToolCallID:218`, `subagentManager.get:62`, `Session.depth:108`, `decodeRef`/`encodeRef`, `fold.go:34` already folding `ParentJobID`), each cited by file:line and VERIFIED above.
- **New behavior registers/extends alongside legacy (Phase 6 deletes legacy):** this phase adds NO model-facing tool. It activates `include_nested`/`include_children` (already parameters on `DefJobList`/`DefJobStop` from Phase 2) and the forwarding seam. The legacy subagent control plane (`spawn_agent`/`wait`/…) is untouched; the `delegate` host is the Phase 3 runtime. Phase 6 removes the legacy surface.
- **Seam I flagged as uncertain (and how the plan de-risks it):** the Phase 2 `jobManager` session-id field name (`jm.sessionID` vs `jm.id`) and whether `readOutput`/`stop` are methods vs package functions — Phase 4 uses `jm.sessionID` + method style, so this plan assumes the same and every task says grep-first. The `createShell`/`finalize` internal variable names (`rec`/`started`/`finished`/`pending`) are Phase 2's — each touch point says "match the Phase 2 variable names." If Phase 2 builds events inline inside `Append`, the implementer must hoist them into named values to forward — called out in Tasks 2/3.
- **Generic `parent_job_id` machinery, no nested delegates (v1 non-goal):** nothing in this phase implements nested **delegate** jobs. `parentJobID`/`ParentJobID`/the forward seam are type-agnostic (a delegate job created in a child jobManager would forward identically), so delegates can be added later without reworking the seam — but `createDelegate` is NOT given a forward path here, and `spawnAgent` remains depth-gated (`subagents.go:160`), so a subagent cannot spawn a further delegate. Only the child's **shell** path (`createShell`) forwards.
- **Lock discipline:** `forwardEvent` touches the *parent's* `store` (its own `Append` lock), never the parent `jobManager.mu` — no cross-manager lock cycle. `forwardLocked` is called from `createShell`/`finalize` while the child holds `jm.mu`; it only calls the hook, which targets a different manager. Documented in `jobs_nested.go`.
- **Determinism:** tests script the child LLM via `fakeAdapter` (no real provider), seed stores directly for the restart test, inject `jm.now`, and assert structural outcomes (status/reason/generation parity), never wall-clock equality. The stop tests tolerate `cancelled` OR `stopped` (timing of confirmation is not asserted), matching the §5.8 confirmed/unconfirmed split.
- **Placeholder scan:** every task has complete Go and an exact run command + commit. The only deferred-by-design items (documented in code comments, not silent): `not_controllable` is unreachable in v1 (Task 6); output bytes are owner-routed not mirrored (Task 5); nested delegate jobs are out of scope (kept generic).
```