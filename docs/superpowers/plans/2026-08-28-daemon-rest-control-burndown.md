# Daemon REST Control Burndown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the daemon's retired REST control routes while retaining their typed AppWire behavior and leaving `/status` and `/clear` unchanged.

**Architecture:** `server.NewServer` will expose only `/rpc`, `/status`, and `/clear` from the current daemon control surface. The typed AppWire handlers and their callback seams remain; only HTTP-specific registration, decoding, response mapping, and route-only coverage are removed. Daemon lifecycle tests will use the real AppWire `thread/shutdown` method, and input-driving tests will use `turn/start`.

**Tech Stack:** Go, net/http, AppWire WebSocket client/server, repository Make gates.

**Spec:** Delegated request: daemon REST-to-AppWire migration workstream #1 (2026-08-28).

## Global Constraints

- Remove only `/interrupt`, `/steer`, `/queue`, `/drain-as-steer`, `/compact`, `/model`, `/input`, `/tasks`, and `/shutdown`.
- Keep `/rpc`, all typed AppWire handlers, `/status`, and `/clear` intact.
- Do not add compatibility routes, HTTP clients, or tests that merely assert a removed symbol or route is absent.
- Default tests must remain deterministic and provider-independent.
- The requested pre-implementation simplify-code review is an empty-diff review; do not invent a cleanup change.

---

### Task 1: Establish the behavioral baseline and audit deletion scope

**Files:**
- Modify: `docs/superpowers/plans/2026-08-28-daemon-rest-control-burndown.md`
- Inspect: `server/server.go:432-443`, `server/server_handlers.go`, `server/appwire_runtime.go:519-535`
- Inspect: `server/appwire_server_test.go`, `server/model_set_test.go`, `server/appwire_mutation_recovery_test.go`

**Interfaces:**
- Consumes: `server.NewServer(ServerConfig)` and existing typed AppWire registrations.
- Produces: an audited separation of HTTP-only handlers/tests from retained typed mutations.

- [ ] **Step 1: run the pre-implementation simplify-code review**

Run: `git diff @{upstream}...HEAD && git diff HEAD`

Expected: both diffs are empty. Record this as an empty-diff review.

- [ ] **Step 2: verify typed behavior before deletion**

Run: `go test ./server -run 'TestHandleAppThreadModelSet_|TestServerAppWireTasksList|TestAppWire.*(Interrupt|Queue|Drain|Compact|Shutdown)' -count=1`

Expected: PASS. These typed-handler tests cover model, task, turn-control, compact, and shutdown behavior, so deleting the obsolete transport requires no absence test.

- [ ] **Step 3: audit callers and route-only coverage**

Run: `rg -n --glob '!**/*_test.go' --glob '!docs/**' '"/(interrupt|steer|queue|drain-as-steer|compact|model|input|tasks|shutdown)"|restInterrupt' .`

Expected: no production HTTP caller. Classify `server/server_test.go`, `server/integration_test.go`, the handler-specific portion of `server/model_set_test.go`, and removed-route fuzz cases as HTTP-only; retain typed tests.

### Task 2: Delete the HTTP-only server surface

**Files:**
- Modify: `server/server.go:432-443,509-719`
- Modify: `server/server_handlers.go:1-420`
- Modify: `server/server_test.go:192-454,531-573,729-1009,1087-1328`
- Modify: `server/integration_test.go:15-88`
- Modify: `server/model_set_test.go:98-151`
- Modify: `server/server_surface_fuzz_test.go:350-362`
- Modify: `cmd/evener/runserve_exact_fuzz_test.go:108-109`

**Interfaces:**
- Consumes: existing `SetCancelFunc`, `SetSteerFunc`, `SetQueueFunc`, `SetDrainAsSteerFunc`, `SetCompactFunc`, `SetModelFunc`, `SetTasksFunc`, and `SetShutdownFunc` seams.
- Produces: the same callbacks stay available to `handleAppTurn*`, `handleAppThreadCompactStart`, `handleAppThreadModelSet`, `handleAppTasksList`, and `handleAppThreadShutdown`.

- [ ] **Step 1: confirm retained behavior is green before production deletion**

Run the Task 1 focused AppWire command again. Expected: PASS before any production edit. No new failing test is appropriate: the behavior is already specified at the typed boundary; the requested change is transport removal.

- [ ] **Step 2: remove only obsolete registrations and adapters**

In `server/server.go`, remove the nine named `s.mux.HandleFunc` registrations and retain `/rpc`, `/status`, and `/clear`. In `server/server_handlers.go`, delete `handleInterrupt`, `handleSteer`, `handleQueue`, `handleDrainAsSteer`, `handleCompact`, `handleModel`, `handleInput`, `handleTasks`, and `handleShutdown`, leaving `handleClear`. Remove `InputRequest` and `ModelRequest` only after their HTTP-only uses disappear. Rename callback comments to their typed AppWire method.

- [ ] **Step 3: remove only route-only tests and probes**

Delete direct REST-route/handler tests and their fuzz examples. Keep status/clear tests, typed AppWire handler/mutation tests, and callback behavior exercised through those handlers. Do not add static absence checks.

- [ ] **Step 4: verify the server package**

Run: `go test ./server -count=1`

Expected: PASS, including retained typed control contracts and untouched status/clear coverage.

- [ ] **Step 5: commit the server-surface deletion and plan**

Run:
```bash
git add docs/superpowers/plans/2026-08-28-daemon-rest-control-burndown.md server/server.go server/server_handlers.go server/server_test.go server/integration_test.go server/model_set_test.go server/server_surface_fuzz_test.go cmd/evener/runserve_exact_fuzz_test.go
git commit -m "refactor(server): remove retired daemon REST controls"
```

Commit body must enumerate the removed routes, retained AppWire contracts, and explicit `/status`/`/clear` exclusions.

### Task 3: Move daemon test control to AppWire

**Files:**
- Modify: `cmd/evener/serve_test.go`
- Modify: `cmd/evener/serve_ask_test.go`
- Modify: `cmd/evener/serve_delegate_send_interrupt_test.go`
- Modify: `cmd/evener/serve_goal_test.go`
- Modify: `cmd/evener/serve_model_switch_test.go`
- Modify: `cmd/evener/serve_state_test.go`
- Modify: `cmd/evener/serve_stop_parks_queued_message_test.go`
- Modify: `cmd/evener/serve_tool_result_test.go`

**Interfaces:**
- Consumes: `appwire.DialWebSocket`, `appwire.Client.Initialize`, `Client.ThreadShutdown`, and `Client.TurnStart`.
- Produces: daemon lifecycle tests terminate with `thread/shutdown`; the tool-result test submits input through `turn/start`.

- [ ] **Step 1: write the focused failing helper test**

Add a small test beside the existing serve test helpers. Start an existing test daemon, initialize an `appwire.Client`, call `ThreadShutdown` with `local:<sessionID>`, and wait on the existing serve completion channel.

Run: `go test ./cmd/evener -run Test.*ThreadShutdown -count=1`

Expected: FAIL because the shared helper does not provide the typed shutdown path, not from timing or provider behavior.

- [ ] **Step 2: implement the smallest test-only AppWire helper**

Add a helper that dials `/rpc`, initializes fixed test metadata, calls `ThreadShutdown` for the rendezvous session, and closes the client. Replace every test-only POST `/shutdown` with it. Replace POST `/input` in the tool-result test with `TurnStart` containing a fixed `ClientMutationID` and text input.

- [ ] **Step 3: complete the focused red-green cycle**

Run: `go test ./cmd/evener -run 'Test.*(ThreadShutdown|ToolResult)' -count=1`

Expected: PASS. Assert daemon shutdown and input-driven tool-result behavior, never HTTP status codes.

- [ ] **Step 4: run the affected command package**

Run: `go test ./cmd/evener -count=1`

Expected: PASS without an HTTP control-route client.

- [ ] **Step 5: commit the test transport migration**

Run:
```bash
git add cmd/evener/serve_test.go cmd/evener/serve_ask_test.go cmd/evener/serve_delegate_send_interrupt_test.go cmd/evener/serve_goal_test.go cmd/evener/serve_model_switch_test.go cmd/evener/serve_state_test.go cmd/evener/serve_stop_parks_queued_message_test.go cmd/evener/serve_tool_result_test.go
git commit -m "test(serve): use AppWire control methods"
```

Commit body must state that test transport now follows production AppWire ownership.

### Task 4: Review and verify the completed migration

**Files:**
- Inspect: full range `origin/main...HEAD`

**Interfaces:**
- Consumes: committed route deletion and typed test migration.
- Produces: evidence of no production HTTP control client/route and no unintended status/clear edit.

- [ ] **Step 1: run post-implementation simplify-code review**

Run the required four-angle review on `git diff origin/main...HEAD`. Apply only behavior-preserving quality findings. Record every skipped/reverted finding.

- [ ] **Step 2: run focused tests and repository gates**

Run:
```bash
go test ./server -count=1
go test ./cmd/evener -count=1
make vet
make test
make lint
```

Expected: each exits 0. State any omitted browser/live-provider gate explicitly.

- [ ] **Step 3: inspect the final range and whitespace**

Run:
```bash
git diff --check origin/main...HEAD
git diff --stat origin/main...HEAD
git diff origin/main...HEAD
rg -n --glob '!**/*_test.go' --glob '!docs/**' '"/(interrupt|steer|queue|drain-as-steer|compact|model|input|tasks|shutdown)"|restInterrupt' .
```

Expected: no whitespace error and no production HTTP control registration/caller. `/status`, `/clear`, `/rpc`, and typed AppWire handlers remain outside the deletion scope.

- [ ] **Step 4: commit review-only correction, then report**

Run `git status --short --branch` and `git log --oneline origin/main..HEAD`. Report branch, commits, exact command outcomes, review findings, and unresolved issues. Do not merge.

## Plan Self-Review

- Spec coverage: Task 2 removes every named route; Tasks 1 and 4 explicitly protect `/rpc`, `/status`, and `/clear`; Task 3 removes the test-only clients.
- Placeholder scan: no TBD/TODO marker or unnamed action remains.
- Type consistency: only existing `appwire.Client.ThreadShutdown`, `TurnStart`, and server typed handlers are used; no production interface is added.

