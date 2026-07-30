# Crash Marker Transcript Hydration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hydrate saved transcripts through subscribed WebUI reads when their retained rendezvous record belongs to a confirmed-dead daemon, without weakening authoritative live rejoin.

**Architecture:** Give roster crash markers explicit provenance, retain them for workspace diagnostics, and exclude them only from local-daemon routing. Allow persisted transcript fallback for the precise no-live-entry error while preserving errors from real live actors, dials, reads, and handoffs.

**Tech Stack:** Go, AppWire WebSocket integration tests, `hubcore.Roster`, `appsource.LocalDaemonSource`, persisted transcript fixtures

## Global Constraints

- Preserve crash markers in the tree as `errored`.
- Do not infer deadness from `Status == "errored"`; reachable daemons can report that state.
- Do not fall back to persisted state after a live actor, dial, read, or handoff failure.
- Use deterministic local fixtures without provider credentials or ambient processes.
- Make the smallest coherent change and retain the current AppWire protocol.

---

### Task 1: Distinguish crash markers from routable daemons

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/roster.go`
- Modify: `cmd/serf-hub/internal/hubcore/roster_test.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/app_threadlifecycle.go`

**Interfaces:**
- Consumes: `Roster.Refresh`, `Roster.List`, `newHubSourceRegistry`, and `hubThreadResume`
- Produces: `hubcore.LiveEntry.Crashed bool`, true only for a retained record whose PID is confirmed gone

- [x] **Step 1: Extend the existing crash regression**

In both the watched-crash and fresh-roster scenarios, assert that the retained
record carries explicit crash provenance:

```go
if !got.Crashed {
	t.Fatal("retained dead-process entry is not marked crashed")
}
```

Also assert after the initially successful probe that a reachable record is not
marked crashed:

```go
if live, ok := r.Find("01CRASHED"); !ok || live.Crashed {
	t.Fatalf("reachable entry = %+v, want present and not crashed", live)
}
```

- [x] **Step 2: Run the roster regression and verify RED**

Run:

```bash
go test ./cmd/serf-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
```

Expected after declaring the inert field: FAIL in the watched-crash and
fresh-roster seeds because the retained records do not set `Crashed`.

- [x] **Step 3: Add explicit crash provenance**

Add the field to `LiveEntry`:

```go
type LiveEntry struct {
	rendezvous.Entry
	SessionID          string
	Status             string
	Crashed            bool
	PendingAsk         bool
	PendingEscalation  bool
	RunningSubagentIDs []string
	Project            identifier.Project
}
```

Set it only in the confirmed-dead branch:

```go
crashed.Status = "errored"
crashed.Crashed = true
```

- [x] **Step 4: Exclude crash markers from routing and resume reuse**

In `newHubSourceRegistry`, skip only explicit crash markers:

```go
for _, item := range live {
	if item.Crashed {
		continue
	}
	entries = append(entries, appsource.LocalDaemonEntry{
		Entry:      item.Entry,
		SessionID:  item.SessionID,
		Status:     item.Status,
		PendingAsk: item.PendingAsk,
	})
}
```

In the under-lock resume double-check, replace the status proxy with the
provenance field:

```go
if le, ok := cfg.Roster.Find(sessionID); ok &&
	!le.Crashed &&
	le.Protocol == appwire.ProtocolVersion {
	return hubResumedThreadResponse(ctx, sources, le.SessionID, le.ThreadID)
}
```

- [x] **Step 5: Run the roster regression and verify GREEN**

Run:

```bash
go test ./cmd/serf-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
```

Expected: PASS.

### Task 2: Hydrate saved transcripts only when no live daemon exists

**Files:**
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/app_rpc_test.go`
- Modify: `cmd/serf-hub/app_tasks.go`
- Modify: `cmd/serf-hub/app_tasks_test.go`

**Interfaces:**
- Consumes: the local source's `SessionUnavailable("thread not found: ...")` error and `pastThreadReadResponse`
- Produces: `isDeadSessionError(error) bool` shared by task and transcript persisted fallbacks

- [ ] **Step 1: Add the subscribed crash-marker regression**

Create a real saved transcript with `buildRPCParentSession`, index it with
`PastIndex`, and configure the Hub with this roster entry:

```go
roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
	Entry: rendezvous.Entry{
		PID:       53841,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	},
	SessionID: sessionID,
	Status:    "errored",
	Crashed:   true,
})
```

Read it through the real Hub WebSocket with the WebUI contract:

```go
response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
	Ref:          "local:" + sessionID,
	IncludeTurns: true,
	ItemsView:    "full",
	Subscribe:    true,
	TurnLimit:    40,
})
if err != nil {
	t.Fatalf("subscribed thread/read: %v", err)
}
if response.Thread.ID != sessionID || len(response.Thread.Turns) != 3 {
	t.Fatalf("saved thread = %+v", response.Thread)
}
```

- [ ] **Step 2: Run the integration regression and verify RED**

Run:

```bash
go test ./cmd/serf-hub -run '^TestHubRPCSubscribedReadReturnsPastForCrashMarker$' -count=1
```

Expected: FAIL. Before the routing change it reports a refused stale endpoint;
with Task 1 applied it reports `thread not found` because the generic atomic
fallback guard still rejects the saved transcript.

- [ ] **Step 3: Generalize the precise dead-session predicate**

Rename `isDeadSessionTasksError` to `isDeadSessionError`, keeping its strict
wire-code and message-prefix checks:

```go
func isDeadSessionError(err error) bool {
	if !isSessionUnavailableError(err) {
		return false
	}
	var wire appwire.WireError
	errors.As(err, &wire)
	return strings.HasPrefix(wire.Message, threadNotFoundMessagePrefix)
}
```

Update `hubTasksList` and its tests to use the shared name.

- [ ] **Step 4: Narrow the atomic fallback guard**

Pass the live-read error into the policy:

```go
func allowsPastFallbackAfterLiveReadFailure(source appsource.Source, params appwire.ThreadReadParams, err error) bool {
	if !params.Subscribe {
		return true
	}
	if _, requiresLiveHandoff := source.(appsource.RelaySessionSource); !requiresLiveHandoff {
		return true
	}
	return isDeadSessionError(err)
}
```

Call it with the actual read error:

```go
if allowsPastFallbackAfterLiveReadFailure(source, params, err) {
```

This permits fallback before a relay exists and continues rejecting every
failure after a live entry was selected.

- [ ] **Step 5: Prove the boundary is GREEN**

Run:

```bash
go test ./cmd/serf-hub -run '^(TestHubRPCSubscribedReadReturnsPastForCrashMarker|TestHubRPCSubscribedAtomicFailuresDoNotFallBackToPastAndCanRetry|TestHubRPCNonSubscribedAtomicReadFailureCanReturnPastTranscript|TestHubRPCSubscribedNonAtomicReadFailureCanReturnPastTranscript)$' -count=1
go test ./cmd/serf-hub -run '^TestHubTasksList_' -count=1
```

Expected: PASS.

### Task 3: Verify integration and identifier-writing boundaries

**Files:**
- Verify: `cmd/serf-hub`
- Verify: `cmd/serf-hub/internal/hubcore`
- Verify: `cmd/serf-namingcheck`
- Verify: live Hub session state

**Interfaces:**
- Consumes: the production Hub launch path and transcript writer
- Produces: evidence that the repaired session and a fresh session hydrate without stale routing or non-AppWire camelCase persistence

- [ ] **Step 1: Run package and static verification**

Run:

```bash
gofmt -w cmd/serf-hub/app_rpc.go \
  cmd/serf-hub/app_rpc_test.go \
  cmd/serf-hub/app_tasks.go \
  cmd/serf-hub/app_tasks_test.go \
  cmd/serf-hub/app_threadlifecycle.go \
  cmd/serf-hub/internal/hubcore/roster.go \
  cmd/serf-hub/internal/hubcore/roster_test.go
go test ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore
go test ./cmd/serf-namingcheck
go run ./cmd/serf-namingcheck .
git diff --check
```

Expected: all commands pass.

- [ ] **Step 2: Exercise a fresh session**

Launch a fresh local session through the production Hub path, verify an exact
`thread/read` with `includeTurns:true`, `itemsView:"full"`,
`subscribe:true`, and `turnLimit:40`, then stop its daemon without graceful
rendezvous removal. Refresh or restart the Hub and repeat the subscribed read.

Expected: the session remains visible as failed, its persisted transcript
hydrates, and the dead endpoint is not dialed.

- [ ] **Step 3: Audit newly persisted identifier fields**

Inspect the fresh transcript and adjacent durable state with the strict
transcript reader and the repository naming checker. AppWire JSON remains
camelCase by protocol; Serf-owned transcript and durable-state JSON keys remain
snake_case.

- [ ] **Step 4: Commit the scoped implementation**

Review `git status` and stage only:

```bash
git add docs/superpowers/specs/2026-07-30-crash-marker-transcript-hydration-design.md \
  docs/superpowers/plans/2026-07-30-crash-marker-transcript-hydration.md \
  cmd/serf-hub/app_rpc.go \
  cmd/serf-hub/app_rpc_test.go \
  cmd/serf-hub/app_tasks.go \
  cmd/serf-hub/app_tasks_test.go \
  cmd/serf-hub/app_threadlifecycle.go \
  cmd/serf-hub/internal/hubcore/roster.go \
  cmd/serf-hub/internal/hubcore/roster_test.go
```

Commit with a detailed message describing the dead-PID crash marker,
subscribed-read RED case, authoritative-rejoin boundary, and verification.
