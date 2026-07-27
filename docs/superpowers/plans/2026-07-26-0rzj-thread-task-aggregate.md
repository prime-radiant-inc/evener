# Thread Task Aggregate Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional authoritative task aggregate to `appwire.SerfThread` so live, cold, and reconnect snapshots preserve task counts and the frontend never loses its Tasks badge.

**Architecture:** Define a nullable Go wire aggregate with only `total` and `done`, wire a live callback from the session's real task store into the server snapshot, and project persisted task-store data in the hub's past-thread projector. Missing or unsupported sources leave the pointer nil; a source that has an authoritative empty task file emits a present zero aggregate. Regenerate TypeScript and consume the field in `hydrateThread`, preserving it through websocket reconnect replacement.

**Tech Stack:** Go AppWire catalog and server/hub projectors, persistent `agent/task.TaskStore`, generated TypeScript protocol types, reducer/Vitest frontend tests.

## Global Constraints

- Do not change `src/shell/rail/**` or any `Steering*` renderer/behavior.
- The aggregate must have the same `total`/`done` semantics as `serf/task/updated`.
- Preserve absent-versus-zero exactly: unsupported or missing task data is `null`/unknown; an authoritative empty task store is `{total: 0, done: 0}`.
- Read and follow `docs/testing.md`, including round-trip/drift guidance; default tests remain deterministic and use real task stores.
- Regenerate `cmd/serf-hub/frontend/src/protocol/types.gen.ts` and `docs/appwire-protocol.md` with `make generate`; never hand-edit generated output.
- Do not modify unrelated work or close `0rzj`; add a substantive ready-for-controller-review comment only after complete verification.

---

### Task 1: Add the nullable AppWire aggregate and live snapshot producer

**Files:**
- Modify: `appwire/types.go:235-285,384-392`
- Modify: `server/server.go:220-240,666-672`
- Modify: `server/appwire_runtime.go:846-1030`
- Modify: `cmd/serf/serve.go:590-605`
- Modify: `server/appwire_server_test.go:1024-1115`

**Interfaces:**
- Consumes: the existing `serf/task/updated` `total`/`done` contract and `Session.Tasks()` callback.
- Produces: `appwire.TaskAggregate`, `SerfThread.Tasks *TaskAggregate`, and `Server.SetTaskAggregateFunc(func() *appwire.TaskAggregate)` used only by authoritative live thread reads.

- [ ] **Step 1: Write the failing Go wire and live-snapshot tests**

  Add a server appwire thread-read test that installs `SetTaskAggregateFunc` returning `{Total: 4, Done: 2}` and asserts the returned `Thread.Serf.Tasks` pointer has those values. Add adjacent cases with no callback (nil/unknown) and with a callback returning `{Total: 0, Done: 0}` (present zero). Add a JSON assertion that the aggregate keys are `tasks`, `total`, and `done`, matching the existing notification vocabulary.

- [ ] **Step 2: Run the focused tests and verify red**

  Run:

  ```bash
  go test ./server -run 'TestServerAppWireThreadRead.*Task|TestTaskAggregate' -count=1 -v
  ```

  Expected: the tests do not compile because the aggregate type and setter do not yet exist, or the returned snapshot has no field.

- [ ] **Step 3: Implement the smallest Go wire/live change**

  Add:

  ```go
  type TaskAggregate struct {
      Total int `json:"total"`
      Done  int `json:"done"`
  }
  ```

  Add `Tasks *TaskAggregate `json:"tasks,omitempty"`` to `SerfThread`. Add a server callback field and setter. In `Server.appThread`, call the callback if wired and assign its pointer; leave it nil when the callback is absent. In `cmd/serf/serve.go`, wire the callback to `getSession().Tasks()`, counting `TaskDone` exactly as `TaskStore.Progress` does and returning a non-nil aggregate even when the real store is empty.

- [ ] **Step 4: Run focused server/appwire tests**

  Run the focused tests again, then:

  ```bash
  go test ./server ./appwire -count=1
  ```

  Expected: live snapshots distinguish nil from present zero and preserve the notification aggregate semantics.

- [ ] **Step 5: Commit the live wire change**

  ```bash
  git add appwire/types.go server/server.go server/appwire_runtime.go cmd/serf/serve.go server/appwire_server_test.go
  git commit -m "feat(appwire): include task progress in thread snapshots"
  ```

### Task 2: Project persisted task aggregates for cold and past reads

**Files:**
- Modify: `cmd/serf-hub/app_threadread.go:133-290`
- Create or modify: `cmd/serf-hub/app_threadread_tasks_test.go`

**Interfaces:**
- Consumes: `agent/task.TaskStore`, `loadPersistedTasks`, `pastEntryThread`, and `mergePastThreadForRead`.
- Produces: past-thread `Serf.Tasks` from the real persisted task file, with missing-file and empty-file semantics that cannot be confused.

- [ ] **Step 1: Write the failing past-read behavior tests**

  Seed a past-indexed session with a real `task.TaskStore` file containing two tasks and one `done` status; assert `pastThreadForRead` returns `{Total: 2, Done: 1}`. Seed a known session with no task file; assert `Serf.Tasks == nil`. Seed a known session with an authoritative empty task file; assert `Serf.Tasks != nil` and equals `{Total: 0, Done: 0}`. Exercise both a normal read and a windowed read so the aggregate does not depend on transcript inclusion or turn paging.

- [ ] **Step 2: Run the focused hub tests and verify red**

  Run:

  ```bash
  go test ./cmd/serf-hub -run 'TestPastThreadRead.*Task|TestPastEntryThread.*Task' -count=1 -v
  ```

  Expected: all aggregate assertions fail because the past projector currently leaves `Serf.Tasks` nil.

- [ ] **Step 3: Implement persisted aggregate projection**

  Add a helper beside `loadPersistedTasks` that first checks `<stateDir>/tasks/<sessionID>.json`: a missing file returns nil, a present file is loaded through `task.NewTaskStore(...).Load()` and `View()`, and read/decode errors return nil for the aggregate without failing an otherwise readable thread. Count total and `TaskDone` from the loaded tasks. Stamp the pointer on `pastEntryThread`. In `mergePastThreadForRead`, copy the past aggregate only when the live snapshot has nil; never replace a live present zero or other authoritative value. Reuse the same helper in every `pastEntryThread` invocation so normal, cold, and windowed snapshot paths agree.

- [ ] **Step 4: Run focused hub tests and relevant package tests**

  Run the focused command again, then:

  ```bash
  go test ./cmd/serf-hub -run 'Test(PastThread|PastEntry|HubTasksList).*' -count=1
  ```

  Expected: missing remains nil, an empty persisted store is present zero, and nonzero counts are read from real task-store JSON.

- [ ] **Step 5: Commit the cold/past projection**

  ```bash
  git add cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_tasks_test.go
  git commit -m "fix(hub): project persisted task counts in thread reads"
  ```

### Task 3: Regenerate protocol types and hydrate the frontend aggregate

**Files:**
- Modify through repository generation only: `cmd/serf-hub/frontend/src/protocol/types.gen.ts`
- Modify through repository generation only: `docs/appwire-protocol.md`
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts:245-275`
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts:2400-2500`

**Interfaces:**
- Consumes: generated `SerfThread.tasks?: TaskAggregate` and existing `serf/task/updated` reducer behavior.
- Produces: `ThreadModel.tasks` initialized from every authoritative snapshot, with absent wire data mapped to null and `{total: 0, done: 0}` preserved as a real value.

- [ ] **Step 1: Write the failing reducer reconnect test**

  Build a valid thread-read fixture with `serf.tasks: {total: 7, done: 6}`. Hydrate it, apply a `serf/task/updated` notification changing the aggregate to `{total: 7, done: 7}`, then hydrate a fresh snapshot carrying `{total: 7, done: 7}`. Assert the final model remains `{total: 7, done: 7}` and the Tasks trigger renders `Tasks 7/7` when given that model. Add assertions that a snapshot omitting `tasks` yields `null` and a snapshot carrying `{total: 0, done: 0}` yields a non-null zero aggregate.

- [ ] **Step 2: Run the focused reducer test and verify red**

  Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/protocol/reducer.test.ts src/panes/session/chrome/TasksPanel.test.tsx
  ```

  Expected: hydrateThread currently sets `tasks` to null, so the fresh snapshot loses the aggregate and the zero/absent assertions fail.

- [ ] **Step 3: Regenerate the Go-derived protocol files**

  After the Go field exists, run:

  ```bash
  make generate
  ```

  Confirm the generated `SerfThread` interface contains optional `tasks?: TaskAggregate` and the generated docs describe the field. Do not edit either generated file by hand.

- [ ] **Step 4: Implement snapshot hydration and run the reducer test**

  Replace `tasks: null` in `hydrateThread` with `tasks: thread.serf.tasks ?? null`. Keep the existing reducer notification case unchanged so live updates and fresh snapshots use the same `{total, done}` model shape. Run the focused frontend tests and typecheck.

- [ ] **Step 5: Commit generated and frontend changes**

  ```bash
  git add appwire/types.go docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts cmd/serf-hub/frontend/src/protocol/reducer.ts cmd/serf-hub/frontend/src/protocol/reducer.test.ts
  git commit -m "fix(webui): hydrate task badge from thread snapshots"
  ```

### Task 4: Verify `0rzj` and prepare the controller handoff

**Files:**
- No additional production files.

**Interfaces:**
- Consumes: live, past, generated-wire, reducer, and reconnect behavior from Tasks 1-3.
- Produces: fresh evidence, a clean focused diff, and a substantive open-kata review comment.

- [ ] **Step 1: Run the complete relevant verification matrix**

  Run focused Go/frontend tests throughout, relevant package tests, appwire drift/round-trip tests, the full relevant frontend suite, typecheck, lint, production build, `make build-runtime`, `git diff --check`, formatting/codegen drift checks, and a complete self-review of `base..HEAD`.

- [ ] **Step 2: Confirm the absence/zero invariant line by line**

  Verify no task-file source, unsupported source, old daemon, or missing persisted file becomes `{0,0}`; verify a live callback and present persisted empty file do become `{0,0}`; verify a live present zero is not overwritten by a past nil or past value.

- [ ] **Step 3: Add the ready-for-controller-review kata comment**

  Keep `0rzj` open and add a comment naming the aggregate field, live and past producers, generated files, reconnect test, exact commits, exact verification commands/results, and any separate new kata IDs created for real discovered issues.
