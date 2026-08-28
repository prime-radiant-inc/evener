# AppWire Daemon Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace daemon `GET /status` reads with bounded typed AppWire snapshots, project running non-agent jobs, and remove the legacy HTTP route.

**Architecture:** The daemon's existing `thread/read` response supplies root state, diagnostics, and hydration metrics while `thread/list` supplies descendant identity/state. The hub filters the existing `EvenerDiagnostics.Jobs` into a roster-backed running non-agent projection and keeps delegate identity exclusively in descendant threads.

**Tech Stack:** Go, AppWire JSON-RPC over WebSocket, `httptest`, existing hub roster/source projections.

**Spec:** `docs/superpowers/specs/2026-08-28-appwire-status-design.md`

## Global Constraints

- Work only on daemon status migration; do not remove other legacy control routes.
- Use existing `thread/read`, `thread/list`, and `EvenerDiagnostics.Jobs`; do not add a status RPC or second job model.
- Keep all default tests deterministic and provider-free.
- Preserve fail-soft liveness and old/partial typed-data behavior.
- Do not add REST fallback or any other backward compatibility without Jesse's approval.
- Remove `/status` only after all production callers and behavioral test seams are migrated.
- Do not add route-absence or source-text tests.

## Pre-implementation simplify-code review

`git diff origin/main...HEAD` and `git diff HEAD` were both empty before design
and implementation work began. The requested review was therefore recorded as
an empty-diff review with no findings and no invented changes.

---

### Task 1: Extend the existing thread snapshot with bounded hydration count

**Files:**
- Modify: `appwire/types.go`
- Modify: `server/appwire_runtime.go`
- Test: `server/appwire_server_test.go`
- Regenerate: `docs/appwire-protocol.md`
- Regenerate: `cmd/evener-hub/frontend/src/appwireTypes.ts`

**Interfaces:**
- Consumes: `server.StatusInfo.Turns` from the daemon's materialized status snapshot.
- Produces: `appwire.EvenerThread.TurnCount int` serialized as `turnCount`.

- [ ] **Step 1: Write the failing typed snapshot test**

Add a server AppWire test that sets `StatusInfo{Turns: 37}`, reads the root with
`ThreadReadParams{IncludeTurns: false}`, and asserts both
`response.Thread.Evener.TurnCount == 37` and `len(response.Thread.Turns) == 0`.
The production mutation this catches is omitting the materialized count or
loading transcript turns to derive it.

- [ ] **Step 2: Run the test and confirm red**

Run:

```bash
go test ./server -run TestServerAppWireThreadReadCarriesTurnCountWithoutTurns -count=1
```

Expected: compile failure because `EvenerThread.TurnCount` does not exist.

- [ ] **Step 3: Add the minimal typed field and server projection**

Add:

```go
TurnCount int `json:"turnCount,omitempty"`
```

to `EvenerThread`, and set it from the copied `status.Turns` in
`Server.appThread`.

- [ ] **Step 4: Regenerate and verify green**

Run:

```bash
make generate
go test ./appwire ./server -run 'TestServerAppWireThreadReadCarriesTurnCountWithoutTurns|TestProtocol' -count=1
```

- [ ] **Step 5: Commit**

Stage only the typed field, server projection, focused test, generated protocol
reference, generated TypeScript declarations, and the two design documents.
Commit with a detailed message explaining why a typed count avoids an unbounded
turn read.

### Task 2: Read and project daemon status through real AppWire

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/prober.go`
- Modify: `cmd/evener-hub/internal/hubcore/prober_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/prober_wire_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/roster.go`
- Modify: `cmd/evener-hub/internal/hubcore/roster_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` only if a registered scenario is added or renamed.

**Interfaces:**
- Consumes: root `appwire.ThreadReadResponse` and descendant-inclusive `appwire.ThreadListResponse` from one initialized daemon connection.
- Produces: `ProbeResult.RunningJobs []appwire.EvenerJobInfo` and `LiveEntry.RunningJobs []appwire.EvenerJobInfo`.

- [ ] **Step 1: Convert the real wire test to the desired AppWire contract**

Serve `server.NewServer` through its real HTTP handler, set an AppWire endpoint
on the rendezvous entry, and seed:

```go
server.DetailedStatus{Jobs: []server.JobStatusInfo{
    {JobID: "job_shell", JobType: "shell", Status: "running"},
    {JobID: "job_delegate", JobType: "delegate", Status: "running", TranscriptRef: "local:child-1"},
}}
```

Also seed active, idle, and closed descendants through real descendant events.
Assert root identity/state, pending ask/escalation, sorted non-closed descendant
IDs and states, and exactly one running job with ID `job_shell`, type `shell`,
and status `running`.

- [ ] **Step 2: Run the integration test and confirm red**

Run:

```bash
go test ./cmd/evener-hub/internal/hubcore -run TestStatusProberReadsAppWireStatusIncludingNonAgentJobs -count=1 -v
```

Expected: failure because the prober still calls `/status` and has no typed
running-job projection.

- [ ] **Step 3: Implement the minimal bounded AppWire probe**

Replace HTTP JSON decoding with:

```go
transport, err := appwire.DialWebSocketWithHeaders(ctx, entry.Endpoint, client, header)
client := appwire.NewClient(transport)
client.Start(ctx)
_, err = client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "evener-hub"}})
root, err := client.ThreadRead(ctx, appwire.ThreadReadParams{})
threads, err := client.ThreadList(ctx, appwire.ThreadListParams{IncludeSubagents: true})
```

Fail the probe if the root is empty or absent from the list. Project descendants
from every non-root thread. Project running jobs from root diagnostics by
excluding `delegate` and the five known terminal statuses.

- [ ] **Step 4: Carry jobs through roster snapshots**

Add defensive cloning for `[]appwire.EvenerJobInfo`, copy it through refresh,
`List`, `Find`, and last-good crash retention, and include job ID/type/status in
`rosterFingerprint`. Add a roster test that mutates a returned job and its
pointer fields without changing the stored snapshot, and proves a running-job
status change changes the fingerprint.

- [ ] **Step 5: Run focused tests and confirm green**

Run:

```bash
go test ./cmd/evener-hub/internal/hubcore -run 'TestStatusProber|TestProbe|TestRoster.*Job' -count=1
```

- [ ] **Step 6: Commit**

Stage only the prober, roster, and tests. Commit with the root/list consistency
rule and non-agent filtering contract in the message body.

### Task 3: Expose running jobs and web hydration through existing hub consumers

**Files:**
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/evener-hub/internal/appsource/local_daemon_test.go`
- Modify: `cmd/evener-hub/web_session.go`
- Modify: `cmd/evener-hub/web_types.go`
- Modify: `cmd/evener-hub/web_test.go`

**Interfaces:**
- Consumes: `LiveEntry.RunningJobs` and root `appwire.Thread`.
- Produces: roster-backed local `thread/list` diagnostics and the existing internal `daemonStatus` hydration shape.

- [ ] **Step 1: Write the failing local-source consumer test**

Create a `LocalDaemonEntry` with a running shell `EvenerJobInfo`, call the real
`LocalDaemonSource.ListThreads`, and assert the root thread contains the same
job ID/type/status in `Evener.Diagnostics.Jobs`. Include a delegate row in the
fixture and assert the upstream prober contract, not the local source, is what
prevents it from arriving here.

- [ ] **Step 2: Run the local-source test and confirm red**

Run:

```bash
go test ./cmd/evener-hub/internal/appsource -run TestLocalDaemonListThreadsCarriesRunningJobs -count=1
```

Expected: compile failure because `LocalDaemonEntry` has no running-jobs field.

- [ ] **Step 3: Add the minimal roster-to-thread mapping**

Copy `LiveEntry.RunningJobs` into `LocalDaemonEntry.RunningJobs`, and set
`Evener.Diagnostics` to a diagnostics object containing a defensive job copy
when the slice is non-nil.

- [ ] **Step 4: Write the failing web hydration test**

Run a real daemon AppWire server with root status and envelope metrics, construct
a `WebServer` whose roster points at that endpoint, call `fetchStatus`, and
assert model, profile, state, working directory, turn count, context metrics,
usage, work milliseconds, and active-turn start time. Assert the server's
returned thread has no turns.

- [ ] **Step 5: Run the web test and confirm red**

Run:

```bash
go test ./cmd/evener-hub -run TestFetchStatusReadsBoundedAppWireThread -count=1
```

Expected: failure because `fetchStatus` still performs HTTP `GET /status`.

- [ ] **Step 6: Map the typed root thread into workspace hydration**

Resolve the local source from `WebServer.sources`, call `ReadThread` with the
live root ref and `IncludeTurns: false` under a one-second context, and map the
existing thread fields plus `Evener.TurnCount` into `daemonStatus`. Return nil
on every error.

- [ ] **Step 7: Run focused tests and commit**

Run:

```bash
go test ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -run 'TestLocalDaemonListThreadsCarriesRunningJobs|TestFetchStatusReadsBoundedAppWireThread|TestWorkspaceData' -count=1
```

Commit the consumer projection and hydration migration with a detailed message.

### Task 4: Remove the legacy HTTP status surface after AppWire coverage exists

**Files:**
- Modify: `server/server.go`
- Modify: `server/server_handlers.go`
- Modify/remove endpoint-only cases in `server/server_test.go`, `server/integration_test.go`, `server/security_test.go`, `server/awaiting_status_test.go`, and `server/thread_envelope_test.go`
- Modify live observation helpers in `cmd/evener/serve_ask_test.go`, `cmd/evener/serve_state_test.go`, and `install_test.go`
- Modify registered fuzz/surface cases in `server/server_surface_fuzz_test.go`, `cmd/evener/runserve_exact_fuzz_test.go`, and related manifests only where the removed route is currently registered.

**Interfaces:**
- Consumes: the AppWire observation helpers completed in Tasks 1-3.
- Produces: a daemon HTTP surface without `GET /status`; `/rpc` and unrelated legacy routes remain.

- [ ] **Step 1: Migrate behavior tests that use status as an observation seam**

Replace live `GET /status` helpers with initialized real AppWire clients and
typed `thread/read` calls. Preserve their event/barrier synchronization; do not
replace status polling with fixed sleeps or wider timeouts.

- [ ] **Step 2: Run the migrated behavior tests**

Run the exact affected test names from the edited files with `-count=1`; all
must pass before deleting the route.

- [ ] **Step 3: Remove the route and handler**

Delete `s.mux.HandleFunc("/status", s.handleStatus)` and the now-unused HTTP
handler. Remove endpoint-only method/auth/JSON tests and route registrations.
Keep internal status/envelope types used by AppWire.

- [ ] **Step 4: Run affected packages and confirm green**

Run:

```bash
go test ./server ./cmd/evener ./cmd/evener-hub/internal/hubcore ./cmd/evener-hub/internal/appsource ./cmd/evener-hub -count=1
```

- [ ] **Step 5: Commit**

Stage only the status-route cleanup and migrated behavior tests. Commit with a
detailed inventory of the retained unrelated routes.

### Task 5: Simplify and verify the complete branch

**Files:**
- Review: every file changed from `origin/main...HEAD`

**Interfaces:**
- Consumes: the completed migration.
- Produces: a behavior-preserving cleanup and verification record.

- [ ] **Step 1: Run the post-implementation simplify-code review**

Review the full range diff for reuse, simplification, efficiency, and altitude.
Apply only behavior-preserving changes; skip any finding that changes the typed
contract, removes tests, or reaches outside this workstream.

- [ ] **Step 2: Format and inspect**

Run:

```bash
gofmt -w <touched Go files>
git diff --check
git diff --stat origin/main...HEAD
git diff origin/main...HEAD
```

Read the complete diff, including generated files and removed route tests.

- [ ] **Step 3: Run focused and repository gates**

Run:

```bash
go test ./cmd/evener-hub/internal/hubcore ./cmd/evener-hub/internal/appsource ./cmd/evener-hub ./server ./cmd/evener -count=1
make lint
make vet
make test
make merge-approval-gate
```

If a gate fails, root-cause it; do not widen timeouts or bypass hooks.

- [ ] **Step 4: Final commit and handoff**

Commit any simplify-only changes separately. Report the branch, commit hashes,
exact commands and results, the empty pre-implementation review, the final
simplify findings/actions, and any unresolved limitation. Do not merge.

