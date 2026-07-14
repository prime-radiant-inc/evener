# Session Status Ownership Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Show running in-process subagents as active without incorrectly showing their settled parent session as working.

**Architecture:** Treat the parent daemon's `/status` payload as the authoritative observation point for in-process delegates. Extract running local delegate transcript IDs into the probe result and carry them as non-routable metadata on the parent's `LiveEntry`. Tree and saved-thread projections consume that metadata for child status, while `Session.WireState` reports only work owned by the parent itself.

**Tech Stack:** Go, Serf daemon `/status`, hub roster/tree projection, appwire thread projection, deterministic `go test` fixtures.

---

### Task 1: Pin parent wire-state ownership

**Files:**
- Modify: `agent/session_awaiting_test.go`
- Modify: `agent/session_state.go`

**Step 1: Write the failing test**

Replace the misleading notification-only `TestWireState_ChildrenInFlightReadsActive` coverage with explicit contracts:

```go
func TestWireState_LiveChildDoesNotMakeIdleParentActive(t *testing.T) {
    parent := newTestSessionForState(t)
    child := newTestSessionForState(t)
    parent.subagents.mu.Lock()
    parent.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child}
    parent.subagents.mu.Unlock()

    if got := parent.WireState(); got != string(SessionIdle) {
        t.Fatalf("WireState with only live child = %q, want %q", got, SessionIdle)
    }
    if !parent.autonomyInFlight() {
        t.Fatal("live child must remain autonomy in flight for settle and restore")
    }
}

func TestWireState_PendingParentWorkMakesIdleParentActive(t *testing.T) {
    sess := newTestSessionForState(t)
    sess.enqueueJobNotification(jobNotification{})
    if got := sess.WireState(); got != string(SessionProcessing) {
        t.Fatalf("WireState with pending notification = %q, want %q", got, SessionProcessing)
    }
}
```

**Step 2: Run the test to verify it fails**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestWireState_(LiveChildDoesNotMakeIdleParentActive|PendingParentWorkMakesIdleParentActive)$' -count=1
```

Expected: the live-child test fails because `WireState` calls `autonomyInFlight`.

**Step 3: Implement the smallest behavior split**

Add a helper for parent-owned queued work only:

```go
func (s *Session) sessionWorkPending() bool {
    return s.peekNotifications() > 0 || s.QueueDepth() > 0
}
```

Use `sessionWorkPending` in `WireState`. Keep `autonomyInFlight` as `sessionWorkPending() || len(s.liveSubagentSessions()) > 0` so settle, restore, and drain behavior do not change.

**Step 4: Run focused tests**

Run the command from Step 2, then:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'Test(WireState|SettleTerminalState|Restore.*Awaiting)' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add agent/session_state.go agent/session_awaiting_test.go
git commit -m "fix(agent): keep child activity off idle parent state" -m "Report an idle parent as idle when its only autonomous work is owned by a live child, while retaining active projection for notifications and queued parent input. Keep child activity in the broader autonomy check used by settle and restore behavior."
```

### Task 2: Carry running-child evidence through the roster

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/prober.go`
- Modify: `cmd/serf-hub/internal/hubcore/prober_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster_test.go`
- Modify: test Prober implementations under `cmd/serf-hub/`

**Step 1: Write the failing probe test**

Serve a deterministic `/status` body with these `detailed.jobs` rows:

```json
[
  {"job_type":"delegate","status":"running","transcript_ref":"local:child-running"},
  {"job_type":"delegate","status":"completed","transcript_ref":"local:child-done"},
  {"job_type":"shell","status":"running","transcript_ref":"local:not-a-child"},
  {"job_type":"delegate","status":"running","transcript_ref":"codex:remote-child"},
  {"job_type":"delegate","status":"running","transcript_ref":"invalid"}
]
```

Assert that the result contains only `child-running`.

**Step 2: Run the test to verify it fails**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/hubcore -run FuzzHubcoreScenarios -count=1
```

Expected: compile failure because the structured result and running-child field do not exist.

**Step 3: Introduce a structured probe contract**

Replace the five-value `Prober.Probe` result with:

```go
type ProbeResult struct {
    SessionID          string
    Status             string
    PendingAsk         bool
    PendingEscalation  bool
    RunningSubagentIDs []string
    OK                 bool
}
```

Partially decode `detailed.jobs` in `StatusProber`. Include only rows whose type is `delegate`, status is `running`, and whose `appwire.ParseRef` result has `SourceID == "local"`. Adapt existing deterministic fake Probers to the structured contract.

**Step 4: Propagate and fingerprint the metadata**

Add `RunningSubagentIDs []string` to `LiveEntry`, copy it from `ProbeResult` in `Roster.Refresh`, and include a sorted copy in `rosterFingerprint` so child start/stop transitions trigger `onChange`. Add:

```go
func (r *Roster) IsSubagentActive(sessionID string) bool
```

This method scans the nested ID metadata under the roster lock. It must not insert child IDs into `bySess`; `Find(childID)` must remain false so actions cannot be routed to the parent daemon as if it were the child.

**Step 5: Run focused tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/hubcore -run FuzzHubcoreScenarios -count=1
```

Expected: PASS, including `IsSubagentActive(child)==true` and `Find(child)==false`.

**Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/prober.go cmd/serf-hub/internal/hubcore/prober_test.go cmd/serf-hub/internal/hubcore/roster.go cmd/serf-hub/internal/hubcore/roster_test.go cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go cmd/serf-hub/testmain_test.go cmd/serf-hub/web_test.go cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go
git commit -m "feat(hub): retain running in-process subagent status" -m "Decode running local delegate transcript IDs from the existing daemon status response and retain them as non-routable metadata on the parent roster entry. Fingerprint child lifecycle changes so the UI refreshes without registering child IDs as daemon endpoints."
```

### Task 3: Project child activity consistently

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Modify: `cmd/serf-hub/internal/hubcore/tree_test.go`
- Modify: `cmd/serf-hub/app_threadread.go`
- Modify: `cmd/serf-hub/app_threadread_test.go`
- Modify: `cmd/serf-hub/app_threadlist.go`
- Modify: `cmd/serf-hub/app_rpc_test.go`
- Modify: direct `pastEntryThread` call sites under `cmd/serf-hub/`

**Step 1: Write failing tree ownership test**

Build a parent/child meta tree with one parent `LiveEntry` whose status is idle and whose `RunningSubagentIDs` contains the child. Assert:

```text
parent.State          idle
child.State           active
project.RollupState   active
project.RollupLive    1
project.Expanded      true
```

Then remove the child ID and assert the child is ended and the project has no live rollup.

**Step 2: Write failing thread read/list tests**

Build a `PastIndex` containing the child plus a roster containing only the parent entry and nested child ID. Assert both `pastThreadForRead` and `hubThreadList` return the child with `ThreadStatusActive`. Also assert `Roster.Find(childID)` remains false.

**Step 3: Run tests to verify they fail**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/hubcore -run FuzzHubcoreScenarios -count=1
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub -run 'Test(PastThreadRead.*RunningSubagent|HubThreadList.*RunningSubagent)' -count=1
```

Expected: the child is `ended` / `notLoaded` and the rollup is idle.

**Step 4: Implement tree projection**

Index all `RunningSubagentIDs` separately from the routable live map. `stateFor` returns active for a nested running child. For each top-level tree, derive its rollup from the highest-ranked state of the parent and its children, then increment `RollupLive` or `RollupAttn` once. Do not change the parent's own `TreeNode.State`.

**Step 5: Implement saved-thread projection**

Pass `WebConfig` into `pastEntryThread` and set `ThreadStatusActive` only when `cfg.Roster.IsSubagentActive(entry.Meta.ID)` is true. Update all direct callers. This single builder keeps list, read, transcript listing, and previews consistent.

**Step 6: Run focused package tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub/internal/hubcore -run FuzzHubcoreScenarios -count=1
GOCACHE=/tmp/serf-gocache go test ./cmd/serf-hub -run 'Test(PastThread|HubThreadList|HubRPCThread)' -count=1
```

Expected: PASS.

**Step 7: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_test.go cmd/serf-hub/app_threadlist.go cmd/serf-hub/app_rpc_test.go cmd/serf-hub/app_transcripts.go cmd/serf-hub/cov_threadread_images_fuzz_test.go
git commit -m "fix(hub): display running subagents on their own rows" -m "Use the parent roster entry's running-child evidence to render saved subagent sessions active in tree, read, and list projections. Keep the parent row idle and count its task tree once in the project working rollup."
```

### Task 4: Verify the complete timeout and status change

**Files:**
- Verify all committed implementation and test files

**Step 1: Format and inspect**

```bash
gofmt -w $(git diff --name-only -- '*.go')
git diff --check
git status --short
```

**Step 2: Run affected packages**

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai ./agent ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1
```

Expected: PASS.

**Step 3: Run focused race tests**

```bash
GOCACHE=/tmp/serf-gocache go test -race ./agent ./cmd/serf-hub/internal/hubcore -run 'Test(WireState|StatusProber|Roster|BuildTree)' -count=1
```

Expected: PASS.

**Step 4: Run the complete deterministic suite**

```bash
GOCACHE=/tmp/serf-gocache go test ./... -count=1
```

Expected: no new failures. Compare any failures exactly with the recorded baseline fuzz failures in `cmd/serf-fuzz-harvest` and `cmd/serf-hub/internal/fspaths`.

**Step 5: Review branch evidence**

```bash
git diff --check
git status --short --branch
git log --oneline --decorate origin/main..HEAD
```

Report commit IDs, exact test commands and outputs, the unchanged baseline failures if present, and whether the branch has been pushed or merged.
