# Task Update Classification and Reopen Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make task starts depend on an actual transition into `in_progress`, keep notes-only reassertions from steering or rendering as starts, and emit task refresh events for reopen updates.

**Architecture:** Preserve the model-facing `task_list` schema and the task store's atomic validation. Capture the store snapshot before `Update`, classify transitions from that pre-state, and carry a small additive `started` marker in the tool's existing task-state side channel so the frontend card can use the same transition result. Emit `EventTaskUpdated` for every successful update batch, including a batch that only reopens a task.

**Tech Stack:** Go task store and session tool registry, JSON `StateResult` side channel, React/TypeScript task-card renderer, Vitest behavior tests.

## Global Constraints

- Do not change `src/shell/rail/**` or any `Steering*` renderer/behavior.
- Do not make `updates[].status` optional in the model-facing tool schema; report that alternative only in the handoff.
- Read and follow `docs/testing.md`; default tests remain deterministic and use real Serf code below any external boundary.
- Write each regression test first, run it red for the intended reason, then implement the smallest change and run it green.
- Preserve the existing task store single-`in_progress` validation and batch atomicity.
- Do not hand-edit generated files; this kata has no generated-file change.
- Keep `fgqa` open and add a substantive ready-for-controller-review comment only after all verification is complete.

---

### Task 1: Prove Go transition classification and reopen emission

**Files:**
- Create: `agent/session_tools_task_test.go`
- Modify: `agent/session_tools_task.go:135-236`

**Interfaces:**
- Consumes: `registerTaskTools`, `tool.Registry.ExecuteCall`, `toolDeps`, `task.TaskStore`, and `events.TaskUpdatedData`.
- Produces: a pre-state-based update classifier and task-state snapshots whose updated tasks carry `started: true` only for a real transition into `in_progress` and `started: false` for an explicit `in_progress` reassertion.

- [ ] **Step 1: Write the failing Go behavior test**

  Build a task store with task 1 already `in_progress` and task 2 `open`. Execute these successful `task_list` calls through `registerTaskTools`, recording steering strings, steering kinds, emitted event data, and the returned `ExecResult.ToolState`:

  1. Update task 1 to `in_progress` with a note. Assert no new steering is emitted, assert the returned task-state entry for task 1 has `started == false`, and assert the task-updated event still reports the authoritative aggregate.
  2. Complete task 1 and explicitly update task 2 to `in_progress` in one batch. Assert exactly one current-task steering for task 2 and `started == true` for task 2.
  3. Reopen a completed task to `open` without completing or starting anything. Assert one task-updated event is emitted and the result remains the short update acknowledgement rather than taking a start path.

  Decode the side-channel JSON into a test-only struct embedding `task.Task` with `Started *bool`; assert behavior, not the entire rendered JSON string.

- [ ] **Step 2: Run the focused test and verify the expected red failure**

  Run:

  ```bash
  go test ./agent -run 'TestTaskTool_UpdateClassifiesTransitions|TestTaskTool_UpdateReopenEmitsTaskUpdated' -count=1 -v
  ```

  Expected: the notes-only case records a fresh steering call and the returned state lacks the required false transition marker; the reopen case has no event because the current early return precedes emission.

- [ ] **Step 3: Implement the minimal Go fix**

  In `registerTaskTools`'s `update` branch:

  ```go
  before := store.View()
  if err := store.Update(updates); err != nil { ... }

  previous := make(map[int]taskpkg.TaskStatus, len(before))
  for _, task := range before {
      previous[task.ID] = task.Status
  }
  started := make(map[int]bool)
  for _, update := range updates {
      if update.Status == taskpkg.TaskInProgress && previous[update.ID] != taskpkg.TaskInProgress {
          started[update.ID] = true
      }
  }
  ```

  Use `started` to select the manual steering ID instead of treating every `in_progress` argument as a start. Return update state through an internal side-channel snapshot type that embeds each `task.Task` and includes `Started *bool` only for IDs present in the explicit update batch; its pointer must be non-nil and false for an in-progress reassertion. Keep append/view state as the existing task slice. Emit `EventTaskUpdated` on the early no-completion/no-start path before returning, preserving its concise output.

- [ ] **Step 4: Run the Go behavior test and package tests**

  Run the focused command again, then:

  ```bash
  go test ./agent -run 'TestTask(Store|ListTool|TaskTool_)' -count=1
  ```

  Expected: all focused tests pass with no provider calls, no fresh steering for reassertions, one steering for the real transition, and an event for reopening.

- [ ] **Step 5: Commit the Go fix**

  ```bash
  git add agent/session_tools_task.go agent/session_tools_task_test.go
  git commit -m "fix(tasks): classify starts from pre-update status"
  ```

### Task 2: Make the frontend task card honor the Go transition marker

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskData.ts:15-70`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx:85-135`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx:85-230`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/taskData.test.ts:1-100`

**Interfaces:**
- Consumes: the additive `started` boolean on updated task-state entries emitted by the Go tool; old array state without the marker remains parseable for transcript readability.
- Produces: `TaskRow.started?: boolean`, parser coverage for the marker, and a task-card rule that suppresses `started` when the authoritative marker is false while retaining the existing auto-start and argument-only fallback behavior.

- [ ] **Step 1: Write the failing frontend tests**

  Add a parser assertion that preserves `started: false` and `started: true` from task-state entries. Add task-card behavior cases with the same update arguments and post-state:

  ```tsx
  { action: "update", updates: [{ id: 1, status: "in_progress", notes: "found the root cause" }] }
  ```

  with raw task 1 `status: "in_progress", started: false`, asserting no `task-card-row`; and with `started: true`, asserting one row whose `data-touch` is `started`. Keep the existing real-transition, explicit mixed batch, auto-start, and absent-raw cases.

- [ ] **Step 2: Run the focused frontend tests and verify red**

  Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/panes/session/chrome/taskData.test.ts src/panes/session/transcript/tools/taskCard.test.tsx
  ```

  Expected: the parser drops the new marker and the notes-only card still renders `started`.

- [ ] **Step 3: Implement the minimal frontend fix**

  Parse an optional boolean `started` without changing the task-list endpoint shape. In `mutationRows`, resolve the post-state row for an explicit `in_progress` update; when that row has an authoritative `started` marker, render the started row only when it is `true`. Leave the existing argument-only behavior for absent raw state and unmarked historical state, and leave `autoStartedTask` responsible for a distinct auto-advanced task after completion.

- [ ] **Step 4: Run the focused frontend tests and typecheck**

  Run the focused command again, then:

  ```bash
  npm run typecheck
  ```

  Expected: the new false marker produces no STARTED row, true produces exactly one, existing task-card behavior remains green, and TypeScript reports no errors.

- [ ] **Step 5: Commit the frontend fix**

  ```bash
  git add cmd/serf-hub/frontend/src/panes/session/chrome/taskData.ts cmd/serf-hub/frontend/src/panes/session/chrome/taskData.test.ts cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/taskCard.test.tsx
  git commit -m "fix(webui): hide task starts on status reassertions"
  ```

### Task 3: Verify `fgqa` and prepare the controller handoff

**Files:**
- No additional production files.

**Interfaces:**
- Consumes: the Go classifier, task event emission, side-channel marker, and frontend card behavior from Tasks 1-2.
- Produces: fresh evidence, a clean focused diff, and a substantive open-kata review comment.

- [ ] **Step 1: Run the relevant Go and frontend suites**

  Run the focused Go tests, the relevant `agent` package tests, the task-card/task-data frontend tests, the full frontend suite, typecheck, lint, production build, appwire drift/round-trip checks, `make build-runtime`, and `git diff --check` as listed in the handoff verification checklist.

- [ ] **Step 2: Review the diff against the base commit**

  Confirm `git diff --name-only 09f2161a6..HEAD` contains no `src/shell/rail/**`, no `Steering*` file, and no unrelated work. Review each changed line for pre-state classification, event ordering, and frontend marker semantics.

- [ ] **Step 3: Add the ready-for-controller-review kata comment**

  Keep `fgqa` open and add a comment containing the root cause, exact commits, exact verification commands/results, and the rejected model-schema alternative: making `status` optional would change the model-facing contract and was not implemented without Jesse’s approval.
