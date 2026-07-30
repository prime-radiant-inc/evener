# AppWire Authoritative Rejoin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Serf session panes converge without a page reload after
transient cellular/WebSocket RPC failures, while keeping one daemon-owned
snapshot authority and truthful retry-safe mutation receipts.

**Architecture:** Seed one full in-memory AppWire turn projection before the
session event bridge starts, advance it with the exact committed
notifications, and capture it under the existing subscription cut. Keep
frontend hydration alive for the full ref-ownership lifetime, retrying failed
reads on a still-ready client while fencing released and replaced generations.
Prepare session identity replacements before publication and use receipt
projection states to preserve accepted input until authoritative state carries
its identity.

**Tech Stack:** Go, AppWire JSON-RPC/WebSocket server, React/TypeScript,
Zustand, IndexedDB, Vitest with `FakeClient`, deterministic Go concurrency
seams.

## Global Constraints

- Read `AGENTS.md` and `docs/testing.md` before changing code or tests.
- Start in
  `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/appwire-authoritative-rejoin`
  on branch `wip/appwire-authoritative-rejoin`.
- The approved design is
  `docs/superpowers/specs/2026-07-29-appwire-authoritative-rejoin-design.md`.
  Re-read it before starting each task.
- Do not touch
  `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/appwire-v2-task7-integration`;
  it contains intentionally preserved WIP.
- Do not create or update Linear issues. Jesse explicitly excluded Linear from
  this work.
- This is a flag day. Do not add a compatibility reader, dual path, feature
  flag, protocol negotiation, transcript migration, or fallback to the old
  authority heuristic.
- Do not add transcript format v3, durable presentation IDs, public
  epoch/sequence fields, or a mutation-status RPC.
- A timeout or backoff delay controls resource use only. It must never decide
  whether input was accepted, reflected, removed, or lost.
- Every new test must produce a behavioral RED against the current
  implementation. A compile failure does not count as RED.
- Tests must use fake clients, scripted transports, temporary transcripts, or
  injected schedulers. Do not use sleeps, provider credentials, network
  access, or wall-clock race windows.
- Keep the real `CaptureSubscription` and `CommitProjection` path in
  integration tests. Do not mock Serf internals.
- Preserve the last published frontend model during refresh failure.
- Stage explicit files only after `git status --short`; never use
  `git add -A`, skip hooks, or mix unrelated changes into a task commit.
- After each task, run its focused package tests and request a subagent code
  review before moving to a dependent task.

## Starting Baseline

The planning worktree starts at design commit `7f5bc135d`, whose parent is
`b74a271ca62f86bc64afd3482c11c81342654501`.

Focused Go baseline:

```bash
go test ./server ./internal/appserver ./cmd/serf-hub/internal/appsource -count=1
```

Expected: PASS.

The frontend checkout initially has no `node_modules`; `npm test` reports
`vitest: command not found`. Before frontend RED/GREEN work, run:

```bash
cd cmd/serf-hub/frontend
npm ci
```

Do not interpret the repository-wide baseline as a RED for these tasks.
Before this plan changes production code, `go test ./...` has inherited
flag-day consistency failures:

- root `TestIdentifierAudit` does not yet inventory two `crypto/sha256`
  imports;
- `cmd/serf/internal/launchcheck` still expects AppWire v1 in one test;
- `cmd/serf-tui` callers do not yet supply required `clientMutationId` values;
  and
- `cmd/serf-tui/internal/hubstart` has stale v1/v2 fuzz expectations.

Those failures predate this design and are not authorization to expand this
plan into TUI or identifier-audit cleanup. Record whether their exact shape
changes, and report them separately at handoff.

## Dependency Order

- Tasks 1, 2, 3, and 6 are independent and may be delegated in parallel from
  isolated worktrees.
- Task 4 depends on Task 3.
- Task 5 depends on Task 4.
- Task 7 depends on Task 6 and should start after Task 4 fixes the daemon read
  authority it exercises.
- Task 8 runs after all implementation commits are integrated.

---

### Task 1: Carry Forward the Reviewed RelaySession and Codex Fixes

**Files brought in by existing commits:**

- Modify:
  `cmd/serf-hub/internal/appsource/relay_session_test.go`
- Modify:
  `cmd/serf-hub/internal/appsource/relay_session.go`
- Modify:
  `cmd/serf-hub/internal/appsource/codex_source_test.go`
- Modify:
  `cmd/serf-hub/internal/appsource/codex_live_thread.go`
- Modify:
  `cmd/serf-hub/internal/appsource/codex_source.go`

**Reviewed commits, in required order:**

1. `619b894fae3b2ee36c910d14b287e174f0f170f4` — RelaySession RED
2. `f0c3770e59f5d9f229392d2c02d8961a87300a6e` — RelaySession GREEN
3. `291342018561ae8254f6d584a5ace2a203f1c137` — Codex RED
4. `2969c4233c7f0beaf863bc1919f31c284a341829` — Codex GREEN

- [ ] **Step 1: Confirm none of the reviewed commits is already present.**

  Run:

  ```bash
  for sha in \
    619b894fae3b2ee36c910d14b287e174f0f170f4 \
    f0c3770e59f5d9f229392d2c02d8961a87300a6e \
    291342018561ae8254f6d584a5ace2a203f1c137 \
    2969c4233c7f0beaf863bc1919f31c284a341829
  do
    git merge-base --is-ancestor "$sha" HEAD && exit 1 || true
  done
  ```

  Expected: exit zero; all four commits still need integration.

- [ ] **Step 2: Cherry-pick the RelaySession RED and prove it fails
  behaviorally.**

  Run:

  ```bash
  git cherry-pick 619b894fae3b2ee36c910d14b287e174f0f170f4
  go test ./cmd/serf-hub/internal/appsource \
    -run 'TestRelaySession(SnapshotCutWaitsForQueuedPreCaptureNotification|EOFBeforeConnectionInstallForcesNextReadToRedial)$' \
    -count=1
  ```

  Expected: FAIL in both named assertions. One exposes a read response cut
  overtaking an already-accepted publish job; the other observes one dial
  where a dead pre-install connection should force two.

- [ ] **Step 3: Cherry-pick the RelaySession GREEN and stress it.**

  Run:

  ```bash
  git cherry-pick f0c3770e59f5d9f229392d2c02d8961a87300a6e
  go test ./cmd/serf-hub/internal/appsource \
    -run 'TestRelaySession(SnapshotCutWaitsForQueuedPreCaptureNotification|EOFBeforeConnectionInstallForcesNextReadToRedial)$' \
    -count=100
  go test -race ./cmd/serf-hub/internal/appsource \
    -run 'TestRelaySession(SnapshotCutWaitsForQueuedPreCaptureNotification|EOFBeforeConnectionInstallForcesNextReadToRedial)$' \
    -count=20
  ```

  Expected: PASS.

- [ ] **Step 4: Cherry-pick the Codex RED and prove dirty cache is currently
  accepted.**

  Run:

  ```bash
  git cherry-pick 291342018561ae8254f6d584a5ace2a203f1c137
  go test ./cmd/serf-hub/internal/appsource \
    -run '^TestCodexDirtyCacheIsNotAuthoritativeWhileRefreshRetries$' \
    -count=1
  ```

  Expected: FAIL because `ReadThread` returns the prior committed snapshot
  while a newer dirty generation is still retrying.

- [ ] **Step 5: Cherry-pick the Codex GREEN and stress it.**

  Run:

  ```bash
  git cherry-pick 2969c4233c7f0beaf863bc1919f31c284a341829
  go test ./cmd/serf-hub/internal/appsource \
    -run '^TestCodexDirtyCacheIsNotAuthoritativeWhileRefreshRetries$' \
    -count=100
  go test -race ./cmd/serf-hub/internal/appsource \
    -run '^TestCodexDirtyCacheIsNotAuthoritativeWhileRefreshRetries$' \
    -count=20
  go test ./cmd/serf-hub/internal/appsource -count=1
  ```

  Expected: PASS.

- [ ] **Step 6: Verify commit preservation.**

  Run:

  ```bash
  git log --oneline -6
  git diff --check HEAD~4..HEAD
  ```

  Expected: the four reviewed commits remain separate and ordered RED/GREEN;
  no squash or rewritten implementation is needed.

---

### Task 2: Make Mutation Receipts Describe Visible Projection Truth

**Files:**

- Modify: `agent/session_client_mutation.go`
- Modify: `agent/session_client_mutation_queue.go`
- Modify: `agent/session_client_mutation_test.go`
- Modify: `agent/session_client_mutation_queue_test.go`
- Verify: `agent/session_client_mutation_persist_test.go`
- Verify:
  `cmd/serf-hub/frontend/src/stores/mutationDispatcher.test.ts`
- Verify:
  `cmd/serf-hub/frontend/src/stores/mutationOutbox.test.ts`

**Interfaces:**

```go
func mutationReceipt(
	threadID string,
	record clientMutationRecord,
	disposition appwire.MutationDisposition,
	projectionState appwire.MutationProjectionState,
) appwire.MutationReceipt

func applyClientMutationRecord(
	record *clientMutationRecord,
	result json.RawMessage,
	projectionState appwire.MutationProjectionState,
)

func acceptedClientMutationProjection(
	method string,
) appwire.MutationProjectionState
```

The serialized response and durable record must receive the same
`projectionState`. The method-to-state mapping lives only in
`acceptedClientMutationProjection`; call sites do not repeat the switch.

- [ ] **Step 1: Add behavioral receipt-state tests at the public Session
  boundary.**

  Add `TestClientMutation_InitialReceiptProjectionState` using the existing
  real mutation-store/session fixtures. Cover all seven methods and assert:

  ```go
  {
    "start":     appwire.MutationProjectionPending,
    "steer":     appwire.MutationProjectionPending,
    "queue":     appwire.MutationProjectionPending,
    "drain":     appwire.MutationProjectionPending,
    "promote":   appwire.MutationProjectionPending,
    "interrupt": appwire.MutationProjectionReflected,
    "cancel":    appwire.MutationProjectionRemoved,
  }
  ```

  For each method, assert both the returned receipt and
  `ClientMutationProjection()` carry the expected state before transcript
  incorporation. Use distinct sessions/subtests so queue transforms do not
  share mutable setup.

  Add `TestClientMutation_PendingReceiptReplaysAfterRestart` so a restart
  before incorporation returns the same `pending` state for
  start/steer/queue/drain/promote. Keep existing incorporation tests asserting
  the later transition to `reflected` or `removed`.

- [ ] **Step 2: Run the new tests and confirm RED.**

  Run:

  ```bash
  go test ./agent \
    -run 'TestClientMutation_(InitialReceiptProjectionState|PendingReceiptReplaysAfterRestart)' \
    -count=1
  ```

  Expected: the five input-bearing methods report `reflected`, while interrupt
  and cancel already match their expected states.

- [ ] **Step 3: Thread the initial state through response serialization and
  persistence.**

  Add `acceptedClientMutationProjection` with the exact table from Step 1.
  Change `mutationReceipt` and `applyClientMutationRecord` to accept one value
  returned by that helper. At each acceptance site:

  - start, steer, queue, drain, and promote pass
    `appwire.MutationProjectionPending`;
  - interrupt passes `appwire.MutationProjectionReflected`; and
  - cancel continues to write `appwire.MutationProjectionRemoved`.

  Change `addPendingSteering` to initialize
  `PendingMutation.ProjectionState` as `pending`.

  Do not alter the existing incorporation and terminalization paths that later
  advance durable records to `reflected` or `removed`.

- [ ] **Step 4: Prove response-first frontend behavior already preserves
  accepted input.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm test -- \
    src/stores/mutationDispatcher.test.ts \
    src/stores/mutationOutbox.test.ts
  ```

  Expected: PASS, including:

  - `an applied pending receipt settles transport while preserving durable optimistic display`
  - identity reconciliation removes that accepted optimistic display later.

  No frontend production change belongs in this task unless one of those
  existing behavior tests exposes a real mismatch.

- [ ] **Step 5: Run focused persistence and recovery verification.**

  Run:

  ```bash
  go test ./agent \
    -run 'TestClientMutation_(InitialReceiptProjectionState|Start|Queue|Steer|Drain|Promote|Interrupt|Cancel)' \
    -count=20
  go test -race ./agent \
    -run 'TestClientMutation_(InitialReceiptProjectionState|Start|Queue|Steer|Drain|Promote|Interrupt|Cancel)' \
    -count=5
  go test ./server -run '^TestAppWireMutation' -count=20
  ```

  Expected: PASS.

- [ ] **Step 6: Commit receipt truth as one unit.**

  Run `git status --short`, stage only the four changed agent files, and
  commit with a detailed message explaining why response-before-notification
  is safe only when accepted input remains `pending`.

---

### Task 3: Turn `appTurnSnapshot` into a Complete State Reducer

**Files:**

- Modify: `server/appwire_turns.go`
- Modify: `server/appwire_runtime_test.go`
- Modify: `server/appwire_turns_paging_test.go`

**Interfaces added in this task:**

```go
func (s *appTurnSnapshot) Seed(turns []appwire.Turn)
func (s *appTurnSnapshot) Apply(records []appserver.SequencedNotification)
func (s *appTurnSnapshot) Snapshot() []appwire.Turn
```

This task adds complete reduction semantics. Task 4 removes the obsolete
retained-window storage and switches every read path to the reducer.

- [ ] **Step 1: Add a RED assistant-reset test without calling a nonexistent
  helper.**

  Construct `appTurnSnapshot` directly with one in-progress turn and one
  partial `agentMessage` item. Apply an
  `appwire.NotifyAgentMessageReset` record naming that turn/item. Assert the
  item is absent from `Snapshot()`.

  Name the test:
  `TestAppTurnSnapshotReducesAssistantMessageReset`.

- [ ] **Step 2: Add a RED steering test.**

  Drive an in-progress turn into the snapshot through
  `appwire.NotifyTurnStarted`, then apply two
  `appwire.NotifySerfSteeringInjected` records. Assert two completed steering
  items exist on that active turn with:

  ```text
  item_steering_live_turn_1_0
  item_steering_live_turn_1_1
  ```

  Also assert text, images, source, steering kind, and client mutation ID are
  preserved.

  Name the test:
  `TestAppTurnSnapshotReducesSteeringIntoActiveTurn`.

- [ ] **Step 3: Run both tests and confirm behavioral RED.**

  Run:

  ```bash
  go test ./server \
    -run 'TestAppTurnSnapshotReduces(AssistantMessageReset|SteeringIntoActiveTurn)$' \
    -count=1
  ```

  Expected: reset leaves the partial item present and steering produces no
  item.

- [ ] **Step 4: Implement `Seed` and the missing reducer cases.**

  `Seed` must:

  - deep-clone every turn/item using the existing clone helpers;
  - rebuild `turnIndex`;
  - select the last in-progress seeded turn as `activeTurnID`; and
  - leave the caller's slice and nested data unaliased.

  In `Apply`:

  - add `activeTurnID` to `appTurnSnapshot`;
  - `turn/started` sets `activeTurnID`;
  - matching `turn/completed` clears `activeTurnID`;
  - `item/agentMessage/reset` removes only the named item from the named turn;
  - `serf/steering/injected` appends to `activeTurnID` only; and
  - steering IDs and fields match the frontend reducer exactly.

  Do not invent a turn when no active turn exists. That case is recovered by
  the next authoritative snapshot.

- [ ] **Step 5: Add GREEN-only clone and active-turn coverage.**

  Add:

  - `TestAppTurnSnapshotSeedIsDeepDefensiveCopy`
  - `TestAppTurnSnapshotSeedFindsLastInProgressTurn`
  - `TestAppTurnSnapshotCompletedTurnClearsActiveSteeringTarget`

  Mutate the input slice, nested images, error cause, and raw JSON after
  `Seed`, and assert `Snapshot` is unchanged.

- [ ] **Step 6: Run reducer stress and mutation proof.**

  Run:

  ```bash
  go test ./server \
    -run 'Test(AppTurnSnapshot|AppTurnsFromNotifications)' \
    -count=100
  go test -race ./server \
    -run 'Test(AppTurnSnapshot|AppTurnsFromNotifications)' \
    -count=20
  ```

  Temporarily remove the reset switch arm and confirm the reset test fails;
  restore it and rerun GREEN.

- [ ] **Step 7: Commit the reducer semantics.**

  Stage only `server/appwire_turns.go`,
  `server/appwire_runtime_test.go`, and
  `server/appwire_turns_paging_test.go`. The commit message must state that
  retained-window authority is removed in Task 4, not claim it is already
  gone.

---

### Task 4: Install One Prepared Snapshot and Remove Read-Time Transcript Authority

**Files:**

- Modify: `server/appwire_turns.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/server.go`
- Modify: `server/appwire_turns_paging_test.go`
- Modify: `server/appwire_rejoin_test.go`
- Modify: `server/appwire_server_test.go`
- Modify: `server/server_surface_fuzz_test.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf/serve_residual_fuzz_test.go`

**Interfaces:**

```go
type PreparedAppIdentity struct {
	// fields remain package-owned
}

func PrepareAppIdentity(
	sourceID string,
	threadID string,
	transcriptPath string,
) (PreparedAppIdentity, error)

func (s *Server) ReplaceAppIdentity(
	prepared PreparedAppIdentity,
	activate func(),
)
```

`SetAppIdentity(sourceID, threadID)` may remain as a thin empty-transcript test
helper, but production serve must use `PrepareAppIdentity` and
`ReplaceAppIdentity`. `SetTranscriptPathFunc` leaves the interface entirely.

- [ ] **Step 1: Change the eviction contract test to the desired behavior and
  confirm RED.**

  Replace
  `TestServerAppWireNotificationSnapshotMatchesRetainedWindowAfterEviction`
  with
  `TestServerAppWireNotifierEvictionDoesNotTruncateMaterializedSnapshot`.

  Seed an early completed turn, emit enough later notifications to evict its
  notifier records, and assert the early turn and paging cursor remain in
  `thread/read`.

  Run:

  ```bash
  go test ./server \
    -run '^TestServerAppWireNotifierEvictionDoesNotTruncateMaterializedSnapshot$' \
    -count=1
  ```

  Expected: FAIL because `ReconcileAndSnapshot` rebuilds from the retained
  suffix.

- [ ] **Step 2: Add a no-read-time-I/O RED.**

  Seed a temporary transcript server using the current callback path. Install
  `apptranscript.InstallReadObserverForTesting` after server setup and before
  these calls:

  ```go
  thread/read(subscribe=true, includeTurns=true, turnLimit=40)
  thread/turns/list(cursor=..., limit=30)
  ```

  Assert the observer is never invoked by either call.

  Name the test:
  `TestServerAppWireInstalledSnapshotNeedsNoTranscriptReads`.

  Expected on current code: FAIL because both bounded read paths open/project
  the transcript.

- [ ] **Step 3: Add the persistence-ahead-of-event response-cut RED.**

  In `server/appwire_rejoin_test.go`, use the real connection and
  `beforeAppProjectionCommit` seam:

  1. install a snapshot from the transcript before the assistant entry exists;
  2. block the assistant live event before its projection commit;
  3. append the assistant entry to disk;
  4. issue subscribing `thread/read`;
  5. release the event; and
  6. seed a fresh `appTurnSnapshot` from `response.Thread.Turns`, apply the
     delivered notification records in wire order, and inspect its snapshot.

  Assert the assistant text appears once under one item.

  Name the test:
  `TestAtomicRejoinDoesNotReadTranscriptAheadOfBlockedEvent`.

  Expected: FAIL because current subscribing hydration rereads the newly
  appended transcript while the matching event remains after its cut.

- [ ] **Step 4: Implement preparation without publication.**

  `PrepareAppIdentity` must:

  - normalize empty source to `local`;
  - reject an empty thread ID;
  - treat an empty transcript path as an empty seed;
  - for a non-empty path, decode the leading header and reject a non-empty
    mismatched session ID;
  - project the full transcript once through the existing
    `apptranscript` projector;
  - `Seed` a fresh `appTurnSnapshot`; and
  - construct the fresh `AppEventProjector`.

  It must not mutate a `Server`, notifier, subscription, status, or callback.

- [ ] **Step 5: Implement infallible replacement under the projection cut.**

  `ReplaceAppIdentity` runs one `CommitProjection` callback. Inside it:

  - call `activate` when non-nil;
  - install source/thread identity, projector, and snapshot;
  - increment `appIdentityGeneration`;
  - set `status.SessionID` to the new thread ID;
  - clear active/reserved turn and last-failure-stamp fields; and
  - return one old-targeted `thread/closed` record when replacing a different,
    non-empty old identity.

  Use `appNotifier.Record` for the close record so it participates in the same
  ordered delivery path. Do not apply that old-thread notification to the new
  turn snapshot.

- [ ] **Step 6: Make every daemon turn read use only the installed snapshot.**

  Replace the transcript/notifier authority selection with:

  ```go
  all := snapshot.Snapshot()
  latest, cursor := appwire.WindowTurns(all, limit)
  page := appwire.PageTurns(all, cursor, limit)
  ```

  Remove:

  - `useTranscriptTurns`
  - replay records and replay limits from `appTurnSnapshot`
  - `Cursor`
  - `ReconcileAndSnapshot`
  - `appNotificationTurns`
  - `appNotificationTurnsForIdentity`
  - transcript-path callbacks and validation helpers
  - read-time `TurnCache` use
  - `SetTranscriptPathFunc`

  Keep the full transcript projection helper only at preparation time.

- [ ] **Step 7: Seed production serve before the bridge.**

  After constructing `srv` and before `bridgeSession(sess)`:

  ```go
  prepared, err := server.PrepareAppIdentity(
    "local",
    sess.ID(),
    sess.TranscriptPath(),
  )
  if err != nil {
    sess.Close()
    return fmt.Errorf("prepare app identity: %w", err)
  }
  srv.ReplaceAppIdentity(prepared, nil)
  ```

  Remove `SetTranscriptPathFunc` from `serveServer`,
  `residualServeServer`, and production setup. Update transcript-backed server
  fixtures to prepare before installing instead of setting a later callback.

- [ ] **Step 8: Add identity and paging GREEN coverage.**

  Cover:

  - missing/empty transcript seeds empty state;
  - mismatched transcript header fails preparation without server mutation;
  - full/latest/page results come from one installed slice;
  - old identity snapshots cannot publish after replacement;
  - restored `SessionStart.TranscriptEntries` keeps the next live turn above
    all seeded transcript turn IDs; and
  - notifier eviction changes replay availability but not snapshot state.

- [ ] **Step 9: Run focused GREEN and races.**

  Run:

  ```bash
  go test ./server \
    -run 'Test(ServerAppWire|AtomicRejoin|PreparedAppIdentity|AppTurnSnapshot)' \
    -count=20
  go test -race ./server ./internal/appserver \
    -run 'Test(ServerAppWire|AtomicRejoin|PreparedAppIdentity|AppTurnSnapshot|SnapshotCut)' \
    -count=10
  go test ./cmd/serf -run 'TestServe|TestRunServe' -count=1
  ```

  Expected: PASS.

- [ ] **Step 10: Prove the no-I/O guard catches regression.**

  Temporarily route `appLatestTurns` through the transcript helper. Confirm
  `TestServerAppWireInstalledSnapshotNeedsNoTranscriptReads` fails because its
  read observer runs. Restore the memory-only path and rerun GREEN.

- [ ] **Step 11: Commit the canonical daemon authority.**

  Run `git status --short`, stage only the files listed in this task, and
  commit with a detailed explanation of the startup memory cost, the removed
  notifier/transcript heuristic, and the snapshot/cut invariant.

---

### Task 5: Make Metadata and Clear Identity Replacement Atomic

**Files:**

- Modify: `server/bridge.go`
- Modify: `server/bridge_test.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/appwire_rejoin_test.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf/serve_state_test.go`
- Modify: `cmd/serf/serve_residual_fuzz_test.go`
- Modify: `cmd/serf/serve_test.go`

- [ ] **Step 1: Add a metadata-before-notification RED.**

  In `server/bridge_test.go`, install `beforeAppProjectionCommit` and send
  `EventSessionStart`. In the hook, read `GetStatus()` and assert session ID,
  model, profile, and state already match the event.

  Name the test:
  `TestBridgeUpdatesSessionMetadataBeforeProjectionCommit`.

  Run:

  ```bash
  go test ./server \
    -run '^TestBridgeUpdatesSessionMetadataBeforeProjectionCommit$' \
    -count=1
  ```

  Expected: FAIL because `RecordAppEvent` currently enters the hook before
  `UpdateSessionInfo` and `SetState`.

- [ ] **Step 2: Add clear failure/success RED cases.**

  Use injected `serveDeps` and a real `server.Server` observer.

  Add:

  - `TestRunServeClearPreparationFailureKeepsOldIdentity`
  - `TestRunServeClearRendezvousFailureKeepsOldIdentity`
  - `TestRunServeClearSuccessClosesOldStreamAndInstallsPreparedIdentity`

  Failure assertions:

  - `currentSess/currentEnv` still point to the old pair;
  - the old subscription receives no `thread/closed`;
  - the old session remains usable;
  - the new session/environment is closed; and
  - no rollback `ReplaceAppIdentity` call occurs.

  Success assertions:

  - rendezvous update happens before activation;
  - old subscribers receive exactly one old-targeted `thread/closed`;
  - a new `thread/read` returns only the prepared new snapshot;
  - the old session is closed after replacement; and
  - a late old-session event cannot mutate the new snapshot or status.

- [ ] **Step 3: Confirm RED.**

  Run:

  ```bash
  go test ./cmd/serf ./server \
    -run 'Test(RunServeClear|BridgeUpdatesSessionMetadataBeforeProjectionCommit)' \
    -count=1
  ```

  Expected: the metadata hook sees old fields, and the current clear sequence
  activates before rendezvous and lacks explicit old-stream closure.

- [ ] **Step 4: Reorder bridge state before event projection.**

  In `BridgeWithObserver`, after stale-session validation:

  1. apply `EventSessionStart`, `EventAssistantTextEnd`, and
     `EventSessionEnd` status effects;
  2. then call `RecordAppEvent`.

  Preserve interrupted-session semantics. Do not create a second projection
  lock or notify from setters.

- [ ] **Step 5: Prepare clear before changing shared state.**

  Add `prepareAppIdentity` to `serveDeps`, defaulting to
  `server.PrepareAppIdentity`, so preparation failure is deterministic in
  tests.

  The clear order must be:

  ```text
  create/provision new environment
  create new session
  prepare new AppWire identity
  update rendezvous to new session ID
  ReplaceAppIdentity(prepared, activate currentSess/currentEnv)
  close old session/environment
  bridge new session
  ```

  Delete the old activate-first/rollback sequence. Once preparation and
  rendezvous succeed, replacement is infallible.

- [ ] **Step 6: Run focused stress and race verification.**

  Run:

  ```bash
  go test ./server ./cmd/serf \
    -run 'Test(RunServeClear|Bridge|PreparedAppIdentity|AtomicRejoin)' \
    -count=50
  go test -race ./server ./cmd/serf \
    -run 'Test(RunServeClear|Bridge|PreparedAppIdentity|AtomicRejoin)' \
    -count=10
  ```

  Expected: PASS.

- [ ] **Step 7: Commit atomic identity replacement.**

  Stage only the files listed in this task and commit with a detailed message
  recording the prepare/rendezvous/activate order and why the old stream is
  closed inside the projection commit.

---

### Task 6: Detach and Fence Replaced Connection-State Callbacks

**Files:**

- Modify: `cmd/serf-hub/frontend/src/stores/connection.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.test.ts`

- [ ] **Step 1: Add the old-client callback RED.**

  In the existing `useConnectionStore` describe block:

  ```ts
  test("a replaced client's later state cannot overwrite the current client", () => {
    const a = connectFakeClient("ready");
    const b = new FakeClient("ready");
    connectionStore.getState().connect(b);

    a.emitStateChange("closed");

    expect(connectionStore.getState().client).toBe(b);
    expect(connectionStore.getState().state).toBe("ready");
  });
  ```

- [ ] **Step 2: Run and confirm RED.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm test -- src/stores/threads.test.ts \
    -t "a replaced client's later state cannot overwrite the current client"
  ```

  Expected: FAIL with global state `closed`.

- [ ] **Step 3: Retain and invoke the state-listener unsubscribe.**

  Add one module-private `unwireStateChange` slot. In `connect`:

  1. keep the same-client idempotent return;
  2. invoke and clear the prior unsubscribe;
  3. publish the new client and its current state; and
  4. register a callback that first verifies
     `connectionStore.getState().client === client`.

  Store the returned unsubscribe for the next replacement.

- [ ] **Step 4: Add a late-callback generation guard test.**

  Script a test client whose unsubscribe does not remove an already-captured
  callback, invoke that callback after replacement, and assert the client
  identity check still ignores it. This proves correctness does not depend
  solely on cooperative unsubscribe timing.

- [ ] **Step 5: Run frontend GREEN.**

  Run:

  ```bash
  npm test -- src/stores/threads.test.ts
  npm run typecheck
  npm run lint
  ```

  Expected: PASS.

- [ ] **Step 6: Commit client fencing.**

  Stage only `connection.ts` and `threads.test.ts`. The commit message must
  distinguish connection-state listener ownership from the notification/ready
  listener ownership already handled by `threads.ts`.

---

### Task 7: Keep Hydration Alive for the Entire Ref-Ownership Lifecycle

**Files:**

- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/Session.tsx`
- Verify:
  `cmd/serf-hub/frontend/src/stores/mutationDispatcher.test.ts`
- Verify:
  `cmd/serf-hub/frontend/src/stores/mutationOutbox.test.ts`

**Module-private interface:**

```ts
type HydrationRetryScheduler = (
  attempt: number,
  retry: () => void,
) => () => void;

export function installHydrationRetrySchedulerForTests(
  scheduler: HydrationRetryScheduler,
): () => void;
```

The production scheduler uses capped backoff. The returned function cancels
the scheduled callback. Tests install a manual queue and invoke retries
directly.

- [ ] **Step 1: Add the same-ready initial-hydration RED.**

  Script `thread/read` to reject once with `RequestTimeoutError` and succeed on
  the next call. Call `ensureThread` once, invoke the captured retry callback,
  and assert:

  - two reads occurred without `emitReady`, focus, remount, or client swap;
  - the original `ensureThread` promise resolves;
  - the model publishes once; and
  - one `releaseThread` fully untracks it.

  Name the test:
  `same-ready initial read failure retries while the pane still owns the ref`.

- [ ] **Step 2: Add a stale-model reconnect RED.**

  Hydrate version A, trigger a ready-generation refresh whose first read
  rejects, and assert version A stays in `threads`. Invoke the scheduler;
  resolve version B; assert B replaces A.

  Name the test:
  `same-ready refresh failure preserves stale model until retry succeeds`.

- [ ] **Step 3: Add release and generation RED cases.**

  Add:

  - `release cancels scheduled hydration retry and late response cannot resurrect`
  - `client swap fences an old client's scheduled retry and response`
  - `concurrent owners share one retrying read lifecycle`

  Assert exact request counts and map identity, not timer durations.

- [ ] **Step 4: Add watched-ref and pinned-outbox RED cases.**

  Add:

  - `same-ready watched read failure retries while watcher ownership remains`
  - `pinned mutation rejoin retries without focus or another ready transition`

  The pinned case must keep mutation dispatch closed until the successful
  authoritative response, then dispatch the original mutation ID once.

- [ ] **Step 5: Run all new cases and confirm behavioral RED.**

  Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm test -- src/stores/threads.test.ts \
    -t "same-ready|release cancels scheduled hydration|client swap fences|concurrent owners share|pinned mutation rejoin"
  ```

  Expected: current one-shot paths either reject/roll back, wait for a future
  ready/focus event, or let old callback ownership survive.

- [ ] **Step 6: Introduce one owned hydration controller per owner
  generation.**

  Add a module-private record for real-pane and watched lifecycles containing:

  ```ts
  {
    generation: number;
    retryAttempt: number;
    cancelRetry: (() => void) | null;
    firstHydration: Promise<ThreadModel | null>;
  }
  ```

  Reuse the existing pending response-cut buffers and client/epoch fencing.
  Do not add another Zustand store or another source of thread models.

  Required behavior:

  - the first owner creates the lifecycle;
  - concurrent owners increment the existing refcount and await/share its
    first hydration;
  - a rejection removes only that attempt's pending buffer;
  - same-ready rejection schedules the next attempt;
  - not-ready rejection waits for the current/new client ready trigger;
  - success resets retry attempt and publishes only if every generation fence
    still matches;
  - reconnect/resync requests enter the same controller rather than launching
    an independent retry loop; and
  - last release cancels the retry and retires the owner generation.

- [ ] **Step 7: Preserve stale state and newest-response ownership.**

  Do not delete `threads` or `watchedThreads` on refresh failure. Transfer
  buffered notifications only from an older attempt to its authoritative
  successor. Existing response-cut tests must remain green:

  - initial snapshot supersedes pre-response notifications;
  - same-client epoch rehydrate is newest-wins;
  - late A rejection cannot remove B; and
  - a released read cannot resurrect its ref.

- [ ] **Step 8: Make `Session.tsx` describe the new ownership contract.**

  Keep one `ensureThread(ref)` claim and one matching `releaseThread(ref)`.
  Remove comments that call the ensure attempt one-shot or say a failed
  hydrate leaves loading forever. Do not add a component timer, reload call,
  toast, or second retry loop.

- [ ] **Step 9: Mutation-test the self-heal guard.**

  Temporarily restore the old `throw err` branch for a same-client/same-epoch
  read rejection. Confirm the same-ready initial and refresh tests fail.
  Restore the owned retry path and rerun GREEN.

- [ ] **Step 10: Run complete frontend verification.**

  Run:

  ```bash
  npm test -- \
    src/stores/threads.test.ts \
    src/stores/mutationDispatcher.test.ts \
    src/stores/mutationOutbox.test.ts \
    src/panes/session/Session.test.tsx
  npm run typecheck
  npm run lint
  npm run build
  ```

  Expected: PASS.

- [ ] **Step 11: Commit owned hydration.**

  Stage only the changed frontend files. The detailed commit message must say
  that backoff paces retries but does not infer delivery, and that release,
  client identity, ready epoch, and owner generation are the correctness
  fences.

---

### Task 8: Integrated Verification and Adversarial Handoff

**Files:**

- No planned production changes.
- Any regression fix discovered here must be committed separately with its own
  behavioral test.

- [ ] **Step 1: Run focused Go suites.**

  ```bash
  go test ./agent ./appwire ./internal/appserver ./server ./cmd/serf ./cmd/serf-hub/internal/appsource -count=1
  ```

  Expected: PASS.

- [ ] **Step 2: Run focused Go stress and races.**

  ```bash
  go test ./agent ./server ./cmd/serf-hub/internal/appsource \
    -run 'Test(ClientMutation|AppTurnSnapshot|ServerAppWire|AtomicRejoin|Bridge|RelaySession|CodexDirty)' \
    -count=100
  go test -race ./agent ./internal/appserver ./server ./cmd/serf-hub/internal/appsource \
    -run 'Test(ClientMutation|AppTurnSnapshot|ServerAppWire|AtomicRejoin|SnapshotCut|Bridge|RelaySession|CodexDirty)' \
    -count=20
  ```

  Expected: PASS.

- [ ] **Step 3: Run full frontend verification.**

  ```bash
  cd cmd/serf-hub/frontend
  npm test
  npm run typecheck
  npm run lint
  npm run build
  ```

  Expected: PASS.

- [ ] **Step 4: Run repository static/build gates.**

  From the repository root:

  ```bash
  go vet ./agent ./appwire ./internal/appserver ./server ./cmd/serf ./cmd/serf-hub/internal/appsource
  golangci-lint run ./agent ./appwire ./internal/appserver ./server ./cmd/serf ./cmd/serf-hub/internal/appsource
  make build
  git diff --check
  ```

  Expected: PASS for the scoped packages and build.

- [ ] **Step 5: Re-run the repository-wide baseline and classify exactly.**

  ```bash
  go test ./...
  ```

  Expected on the current base: only the inherited failures listed in
  **Starting Baseline** may remain. Any new failure, changed failure shape, or
  failure in a touched package is a regression and must be fixed before
  handoff.

- [ ] **Step 6: Run two independent adversarial reviews.**

  Give each reviewer the design, this plan, the complete branch diff, and the
  explicit mandate below:

  > You are competing with one other reviewer. Whoever finds the largest
  > number of legitimate significant issues gets 5 points. Bullshit findings,
  > duplicate padding, and artificially inflated severity disqualify the
  > reviewer.

  Ask them to find:

  - a lock-order or response-cut violation;
  - an event the materialized reducer fails to retain;
  - an identity replacement partial-failure path;
  - a retry lifecycle that leaks, duplicates reads, or resurrects state;
  - a stale client callback that crosses generations; and
  - a receipt state that can hide accepted input.

  Require findings first, severity ordered, exact `file:line` evidence, and an
  explicit `APPROVED` or `CHANGES REQUIRED` verdict. Reject speculative
  findings without a reachable trace or failing test.

- [ ] **Step 7: Fix legitimate findings test-first and rerun the affected
  review.**

  Each fix gets its own deterministic RED, focused GREEN, and separate commit.
  Repeat until both reviewers return `APPROVED`.

- [ ] **Step 8: Verify final Git state and prepare the handoff.**

  Run:

  ```bash
  git status --short
  git log --oneline --decorate -20
  git diff --check
  ```

  Report:

  - branch and worktree;
  - commits by task;
  - focused test/race/frontend/build results;
  - repository-wide inherited failures, if still present;
  - both adversarial review verdicts; and
  - confirmation that the preserved Task 7 WIP worktree was untouched.
