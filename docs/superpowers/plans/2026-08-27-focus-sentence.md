# Focus Sentence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render the current in-progress task and optional goal objective immediately above the session compose card on desktop and narrow panes.

**Architecture:** Extend the existing additive AppWire task aggregate and goal state rather than introducing a polling request. Live and persisted snapshots carry both values; existing task updates carry the current summary; a new structured goal update keeps every client current. `ThreadModel` remains the sole frontend state source, and a focused composer child renders the approved one-line/two-line treatment.

**Tech Stack:** Go 1.27, AppWire JSON-RPC and its Go-to-TypeScript generator, React 19, TypeScript 6, CSS modules, Vitest, Testing Library, and Chrome overflowguard.

**Spec:** `docs/superpowers/specs/2026-08-27-focus-sentence-design.md`

## Global Constraints

- Preserve old-producer compatibility: every new JSON field is optional and additive.
- Use `TaskStore.CurrentInProgress` semantics: the first `in_progress` task is current.
- Do not fetch task details merely to render the footer.
- Keep `ThreadModel` as the only frontend source for current task and goal.
- Use the composer’s existing 559px container boundary, not viewport width.
- Hide the current-work line whenever AskDock hides the compose form.
- Run `npx biome check --write` on touched files under `frontend/src/` before the frontend gate.
- Keep default tests deterministic and offline.

---

### Task 1: Carry current task and goal objective in live and persisted snapshots

**Files:**
- Modify: `appwire/types.go`
- Modify: `appwire/clone.go`
- Test: `appwire/clone_test.go`
- Modify: `agent/session_goal.go`
- Test: `agent/session_goal_test.go`
- Modify: `server/thread_envelope.go`
- Modify: `server/thread_envelope_test_helpers_test.go`
- Test: `server/thread_envelope_test.go`
- Modify: `cmd/evener/serve.go`
- Modify: `cmd/evener-hub/app_threadread.go`
- Test: `cmd/evener-hub/app_threadread_tasks_test.go`
- Regenerate: `appwire/protocol.md`
- Regenerate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Produces: `appwire.TaskSummary{ID int, Description string}`.
- Produces: `TaskAggregate.Current *TaskSummary` and `GoalState.Objective string` as optional JSON fields.
- Produces: `Session.GoalStatus() (objective, status string, iterations int, ok bool)` as one atomic snapshot.
- Consumes: `TaskStore.CurrentInProgress` ordering through the loaded task slices in live and persisted producers.

- [ ] **Step 1: Write failing live and persisted snapshot tests**

Extend the goal status test:

```go
objective, status, iterations, ok := sess.GoalStatus()
if !ok || objective != "ship the footer" || status != "active" || iterations != 0 {
    t.Fatalf("GoalStatus() = %q, %q, %d, %v", objective, status, iterations, ok)
}
```

Add a thread-envelope fixture with:

```go
source.tasks = &appwire.TaskAggregate{
    Total: 2,
    Current: &appwire.TaskSummary{ID: 2, Description: "wire current work"},
}
source.goalObjective = "wire goal objective"
source.goalStatus = "active"
source.goalSet = true
```

Assert `thread.Evener.Tasks.Current` and `thread.Evener.Goal.Objective`. Extend the persisted-task tests with two in-progress rows and assert the first row wins. Seed `entry.Meta.Goal` in a past-thread test and assert objective, status, and iterations survive.

Add a clone test that mutates the source `TaskAggregate.Current` after cloning and proves the clone does not change.

- [ ] **Step 2: Run focused tests and confirm red**

```bash
go test ./appwire -run 'Test.*Clone.*Task' -count=1
go test ./agent -run 'Test.*GoalStatus' -count=1
go test ./server -run 'Test.*ThreadEnvelope|Test.*Goal' -count=1
go test ./cmd/evener-hub -run 'Test.*Past.*(Task|Goal)|Test.*ThreadRead.*Task' -count=1
```

Expected: compile failures for `TaskSummary`, `Current`, `Objective`, and the expanded `GoalStatus` signature.

- [ ] **Step 3: Implement additive snapshot fields**

Add:

```go
type TaskSummary struct {
    ID          int    `json:"id"`
    Description string `json:"description"`
}

type TaskAggregate struct {
    Total   int          `json:"total"`
    Done    int          `json:"done"`
    Current *TaskSummary `json:"current,omitempty"`
}

type GoalState struct {
    Objective string `json:"objective,omitempty"`
    Status     string `json:"status"`
    Iterations int    `json:"iterations"`
}
```

Deep-copy `TaskAggregate.Current` in `cloneTaskAggregate`. Return objective with status and iterations from one `goal.Store.Snapshot`. Update `ThreadEnvelopeSource`, its test doubles, and `refreshFacets` to construct `GoalState{Objective, Status, Iterations}`.

In `liveThreadEnvelopeSource.TaskAggregate` and `persistedTaskAggregate`, retain the first task whose status is `taskpkg.TaskInProgress`:

```go
if aggregate.Current == nil && task.Status == taskpkg.TaskInProgress {
    aggregate.Current = &appwire.TaskSummary{ID: task.ID, Description: task.Description}
}
```

Map `entry.Meta.Goal` into `pastEntryThread.Evener.Goal` when the metadata carries one.

- [ ] **Step 4: Regenerate, format, and rerun focused tests**

```bash
make generate
gofmt -w appwire/types.go appwire/clone.go appwire/clone_test.go agent/session_goal.go agent/session_goal_test.go server/thread_envelope.go server/thread_envelope_test_helpers_test.go server/thread_envelope_test.go cmd/evener/serve.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_tasks_test.go
go test ./appwire -run 'Test.*Clone.*Task' -count=1
go test ./agent -run 'Test.*GoalStatus' -count=1
go test ./server -run 'Test.*ThreadEnvelope|Test.*Goal' -count=1
go test ./cmd/evener-hub -run 'Test.*Past.*(Task|Goal)|Test.*ThreadRead.*Task' -count=1
```

Expected: all focused tests pass; generated TypeScript contains optional `current` and `objective` fields.

- [ ] **Step 5: Commit snapshot projection**

```bash
git add appwire/types.go appwire/clone.go appwire/clone_test.go appwire/protocol.md agent/session_goal.go agent/session_goal_test.go server/thread_envelope.go server/thread_envelope_test_helpers_test.go server/thread_envelope_test.go cmd/evener/serve.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_tasks_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/superpowers/specs/2026-08-27-focus-sentence-design.md docs/superpowers/plans/2026-08-27-focus-sentence.md
git commit -m "feat: project current work in thread snapshots"
```

### Task 2: Push the current task through existing task updates

**Files:**
- Modify: `agent/events/payloads.go`
- Test: `agent/events/payloads_test.go`
- Modify: `agent/session_tools_task.go`
- Test: `agent/cov_task_updated_test.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Test: `internal/appprojector/appwire_projection_test.go`
- Modify: `appwire/types.go`
- Regenerate: `appwire/protocol.md`
- Regenerate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/model.ts`
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.ts`
- Test: `cmd/evener-hub/frontend/src/protocol/reducer.test.ts`

**Interfaces:**
- Consumes: `appwire.TaskSummary` and `TaskAggregate.Current` from Task 1.
- Produces: `events.TaskUpdatedData.Current *events.TaskSummaryData`.
- Produces: `appwire.TaskUpdatedParams.Current *TaskSummary`.
- Produces: `ThreadModel.tasks: TaskAggregate | null`.

- [ ] **Step 1: Write failing event, projector, and reducer tests**

Observe `EventTaskUpdated` after starting task 2:

```go
data, ok := event.Data.(events.TaskUpdatedData)
if !ok || data.Current == nil || data.Current.ID != 2 || data.Current.Description != "live current task" {
    t.Fatalf("TASK_UPDATED = %#v", event.Data)
}
```

Extend `TestProject_TaskUpdated` with a current summary and assert the projected `TaskUpdatedParams.Current`. In `reducer.test.ts`, hydrate a current task, apply a replacement notification, then apply one without `current` and assert that omission clears the prior current task.

- [ ] **Step 2: Run focused tests and confirm red**

```bash
go test ./agent -run 'TestTaskTool_.*EmitsTaskUpdated' -count=1
go test ./internal/appprojector -run TestProject_TaskUpdated -count=1
cd cmd/evener-hub/frontend && npx vitest run src/protocol/reducer.test.ts --maxWorkers=4
```

Expected: compile/type failures for the new current-task fields.

- [ ] **Step 3: Emit and project one authoritative summary**

Define:

```go
type TaskSummaryData struct {
    ID          int    `json:"id"`
    Description string `json:"description"`
}

type TaskUpdatedData struct {
    Total   int              `json:"total"`
    Done    int              `json:"done"`
    Current *TaskSummaryData `json:"current,omitempty"`
}
```

Add a helper in `session_tools_task.go` that derives total, done, and the first in-progress task from the post-mutation slice. Use it at every `EventTaskUpdated` emission site. Map it into `TaskUpdatedParams.Current` in `appprojector`.

Extend `TaskUpdatedParams` with `Current *TaskSummary`. Regenerate. Type `ThreadModel.tasks` as generated `TaskAggregate | null`. Replace tasks in the reducer with total, done, and the notification’s optional current summary.

- [ ] **Step 4: Format and rerun focused tests**

```bash
gofmt -w agent/events/payloads.go agent/events/payloads_test.go agent/session_tools_task.go agent/cov_task_updated_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go appwire/types.go
make generate
cd cmd/evener-hub/frontend && npx biome check --write src/protocol/model.ts src/protocol/reducer.ts src/protocol/reducer.test.ts
go test ./agent -run 'TestTaskTool_.*EmitsTaskUpdated' -count=1
go test ./internal/appprojector -run TestProject_TaskUpdated -count=1
cd cmd/evener-hub/frontend && npx vitest run src/protocol/reducer.test.ts --maxWorkers=4
```

Expected: all focused tests pass.

- [ ] **Step 5: Commit live task updates**

```bash
git add agent/events/payloads.go agent/events/payloads_test.go agent/session_tools_task.go agent/cov_task_updated_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go appwire/types.go appwire/protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts cmd/evener-hub/frontend/src/protocol/model.ts cmd/evener-hub/frontend/src/protocol/reducer.ts cmd/evener-hub/frontend/src/protocol/reducer.test.ts
git commit -m "feat: push the current task to clients"
```

### Task 3: Push structured goal state to every client

**Files:**
- Modify: `agent/events/events.go`
- Modify: `agent/events/payloads.go`
- Test: `agent/events/payloads_test.go`
- Modify: `agent/session_goal.go`
- Test: `agent/session_goal_test.go`
- Modify: `agent/session_tools_goal.go`
- Test: `agent/session_tools_goal_test.go`
- Modify: `server/thread_envelope.go`
- Test: `server/thread_envelope_test.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Test: `internal/appprojector/appwire_projection_test.go`
- Modify: `appwire/methods.go`
- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Regenerate: `appwire/protocol.md`
- Regenerate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`
- Modify: `cmd/evener-tui/hub_notification_coverage_test.go`
- Modify: `cmd/evener-hub/frontend/src/protocol/reducer.ts`
- Test: `cmd/evener-hub/frontend/src/protocol/reducer.test.ts`
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`
- Test: `cmd/evener-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/palette/commands.ts`
- Test: `cmd/evener-hub/frontend/src/shell/palette/commands.test.ts`

**Interfaces:**
- Produces: `events.EventGoalUpdated` with `GoalUpdatedData{Goal *GoalStateData}`; nil means clear.
- Produces: `appwire.NotifyEvenerGoalUpdated` with `GoalUpdatedParams{ThreadID, Ref, Goal *GoalState}`; JSON includes `goal:null` on clear.
- Consumes: `ThreadModel.goal` as the only UI goal state.

- [ ] **Step 1: Write failing goal lifecycle and projection tests**

Observe session events for SetGoal, ClearGoal, continuation iteration, `update_goal`, and error blocking. Assert each committed transition emits one structured update whose objective, status, and iterations equal the goal store after the mutation. Clear must carry a nil goal.

Add projector tests:

```go
Data: events.GoalUpdatedData{Goal: &events.GoalStateData{
    Objective: "ship focus sentence", Status: "active", Iterations: 1,
}}
```

Assert one `evener/goal/updated` notification with matching typed params. Add a clear case.

Add reducer tests that set and clear `model.goal`. Add a threads-store test proving a successful local `setGoal(ref, objective)` patches the tracked model immediately. Update GoalControl and command tests to expect model-driven state and no override call.

- [ ] **Step 2: Run focused tests and confirm red**

```bash
go test ./agent -run 'Test.*Goal' -count=1
go test ./internal/appprojector -run 'TestProject_GoalUpdated' -count=1
go test ./server -run 'Test.*Goal' -count=1
go test ./cmd/evener-tui -run TestEveryWireNotificationIsHandledOrExplicitlyIgnored -count=1
cd cmd/evener-hub/frontend && npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts src/panes/session/chrome/GoalControl.test.tsx src/shell/palette/commands.test.ts --maxWorkers=4
```

Expected: missing event, wire, reducer, and store-update failures.

- [ ] **Step 3: Emit structured state at every mutation boundary**

Define `GoalStateData` and `GoalUpdatedData` with `json:"goal"` rather than `omitempty`, so clear serializes as null. Add helpers in `session_goal.go` that convert a `goal.Snapshot` and emit the current store state.

Emit after:

- `SetGoal` releases `s.mu`;
- `ClearGoal` releases `s.mu`;
- every `RecordContinuation` result;
- `update_goal` successfully calls `SetTerminal`;
- `terminateGoalOnError` successfully blocks the goal.

Do not emit while holding `Session.mu`.

Add `EventGoalUpdated` to `facetsByEvent` with `facetGoal`, preserving refresh-before-project ordering.

- [ ] **Step 4: Add AppWire projection and frontend folding**

Define:

```go
type GoalUpdatedParams struct {
    ThreadID string     `json:"threadId"`
    Ref      string     `json:"ref"`
    Goal     *GoalState `json:"goal"`
}
```

Register `NotifyEvenerGoalUpdated` in the notification catalog and project the event. Add the notification to the TUI’s deliberate-ignore list with a comment that TUI goal status still comes from its own fetch/status surface.

Regenerate types. Add a reducer case that replaces `model.goal` with `n.params.goal ?? null`. In `threadsStore.setGoal`, patch the local tracked model after a successful response:

```ts
const goal = objective === "" ? null : { objective, status: "active", iterations: 0 };
set((state) => replaceThread(state, ref, (model) => ({ ...model, goal })));
```

Adapt this to the store’s existing immutable-map helper rather than mutating the map. Remove GoalControl’s override cache and `applyGoalSetOptimistically`; GoalControl reads `model.goal`. Remove the command-layer override call.

- [ ] **Step 5: Format and rerun focused tests**

```bash
gofmt -w agent/events/events.go agent/events/payloads.go agent/events/payloads_test.go agent/session_goal.go agent/session_goal_test.go agent/session_tools_goal.go agent/session_tools_goal_test.go server/thread_envelope.go server/thread_envelope_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go appwire/methods.go appwire/types.go appwire/protocol.go cmd/evener-tui/hub_notification_coverage_test.go
make generate
cd cmd/evener-hub/frontend && npx biome check --write src/protocol/reducer.ts src/protocol/reducer.test.ts src/stores/threads.ts src/stores/threads.test.ts src/panes/session/chrome/GoalControl.tsx src/panes/session/chrome/GoalControl.test.tsx src/shell/palette/commands.ts src/shell/palette/commands.test.ts
go test ./agent -run 'Test.*Goal' -count=1
go test ./internal/appprojector -run 'TestProject_GoalUpdated' -count=1
go test ./server -run 'Test.*Goal' -count=1
go test ./cmd/evener-tui -run TestEveryWireNotificationIsHandledOrExplicitlyIgnored -count=1
cd cmd/evener-hub/frontend && npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts src/panes/session/chrome/GoalControl.test.tsx src/shell/palette/commands.test.ts --maxWorkers=4
```

Expected: every focused test passes.

- [ ] **Step 6: Commit live goal updates**

```bash
git add agent/events/events.go agent/events/payloads.go agent/events/payloads_test.go agent/session_goal.go agent/session_goal_test.go agent/session_tools_goal.go agent/session_tools_goal_test.go server/thread_envelope.go server/thread_envelope_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go appwire/methods.go appwire/types.go appwire/protocol.go appwire/protocol.md cmd/evener-tui/hub_notification_coverage_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts cmd/evener-hub/frontend/src/protocol/reducer.ts cmd/evener-hub/frontend/src/protocol/reducer.test.ts cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.test.ts cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.tsx cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx cmd/evener-hub/frontend/src/shell/palette/commands.ts cmd/evener-hub/frontend/src/shell/palette/commands.test.ts
git commit -m "feat: push goal state to clients"
```

### Task 4: Render the Focus sentence in Composer

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/composer/currentwork.module.css`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs`

**Interfaces:**
- Consumes: `model.tasks?.current?.description` and `model.goal?.objective`.
- Produces: `CurrentWork({task, goal}: {task?: string; goal?: string})`.
- Produces: a polite atomic status region directly before the compose form.

- [ ] **Step 1: Write failing component and integration tests**

Create table-driven component tests for task-plus-goal, task-only, goal-only, and empty states. Assert visible labels, accessible status naming, and absent divider/goal nodes. Add a long-string test that preserves full values in `title`.

Add a Composer test with `tasks.current` and `goal.objective`. Assert `current-work` precedes `composer-input-card` in document order and disappears when AskDock is pending.

Extend overflowguard so each width asserts the current-work region and compose controls share the pane without horizontal overflow and the current-work region’s bottom is at or above the composer card’s top.

- [ ] **Step 2: Run focused frontend tests and confirm red**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/composer/CurrentWork.test.tsx src/panes/session/composer/Composer.test.tsx --maxWorkers=4
npm run overflowguard
```

Expected: missing component/module failures and missing overflow fixture assertions.

- [ ] **Step 3: Implement `CurrentWork` and place it before the form**

Return null when both trimmed values are empty. Render decorative dot/flag elements, explicit `Working on` and `Goal` labels, ellipsized values with `title`, and a full composed `aria-label`.

In `Composer.tsx`, place this after staged attachments and before `.formAnchor`:

```tsx
{!askPending && (
  <CurrentWork
    task={model.tasks?.current?.description}
    goal={model.goal?.objective}
  />
)}
```

- [ ] **Step 4: Implement responsive styling at 559px**

Desktop uses one flex row with the task first and the goal after a hairline. At `@container (max-width: 559px)`, switch to one grid column and place the goal on a second indented row. Give every flex/grid link `min-width: 0`; use `overflow: hidden`, `white-space: nowrap`, and `text-overflow: ellipsis` on each value. Do not assign fixed widths to compose controls.

Seed `overflowharness-entry.tsx` with a long current task and goal objective so the real Session guard exercises both rows.

- [ ] **Step 5: Format and run focused unit/browser tests**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/panes/session/composer/CurrentWork.tsx src/panes/session/composer/CurrentWork.test.tsx src/panes/session/composer/Composer.tsx src/panes/session/composer/Composer.test.tsx src/dev/overflowharness-entry.tsx
npx vitest run src/panes/session/composer/CurrentWork.test.tsx src/panes/session/composer/Composer.test.tsx --maxWorkers=4
npm run overflowguard
```

Expected: unit tests pass and overflowguard reports no horizontal overflow or control overlap at every Session width.

- [ ] **Step 6: Commit the Focus sentence UI**

```bash
git add cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.tsx cmd/evener-hub/frontend/src/panes/session/composer/currentwork.module.css cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.test.tsx cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx cmd/evener-hub/frontend/scripts/overflowguard/run.mjs
git commit -m "feat: show current work above compose"
```

### Task 5: Run complete gates and inspect the result

**Files:**
- Verify only; modify a file only when evidence identifies a root cause.

**Interfaces:**
- Consumes: every contract from Tasks 1–4.
- Produces: gate evidence and a clean implementation worktree.

- [ ] **Step 1: Prove generated output and formatting are current**

```bash
make generate
git status --short
git diff --check
```

Expected: generation adds no new diff and `git diff --check` exits zero.

- [ ] **Step 2: Run package and frontend gates**

```bash
go test ./agent/... ./server/... ./internal/appprojector/... ./cmd/evener-hub/... ./cmd/evener-tui/... -count=1
make test-web
make test-web-browser
```

Expected: every command exits zero. A missing Chrome is a blocked browser gate, not a pass.

- [ ] **Step 3: Run repository static gates**

```bash
make lint
make vet
```

Expected: both commands exit zero.

- [ ] **Step 4: Review final commits and worktree state**

```bash
git log --oneline --decorate main..HEAD
git diff main...HEAD --stat
git diff main...HEAD --check
git status --short --branch
```

Confirm every design requirement maps to a test, the diff contains no unrelated changes, and the `focus-sentence` worktree is clean.
