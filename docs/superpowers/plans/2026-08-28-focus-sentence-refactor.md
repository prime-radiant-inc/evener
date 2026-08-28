# Focus Sentence Authority Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the current task description and goal objective for live root sessions, live descendants, and persisted sessions while making task/goal notifications the ordered authority committed into the same server snapshot every client reads, and make the Focus sentence’s action links safe and predictable.

**Architecture:** Seed root and descendant thread state from a self-contained optional current-work payload on `SessionStartData`, then fold typed `TaskUpdatedParams` and `GoalUpdatedParams` carriers into that state inside the existing `appserver.Server.CommitProjection` critical section before recording and routing each notification. Every task start/update carrier names its task-store owner; the server fans one mutation out to every cached root/descendant projection with that owner, preserving one commit order without widening descendant callback signatures. Task-list summary semantics live in `agent/task`. The frontend continues to use `ThreadModel` as its sole display source; response-derived goal state is only a fallback until an accepted notification or authoritative hydration wins, and one canonical composer replacement operation owns text, attachments, recovery, slash-menu, draft, and deferred-focus cleanup.

**Tech Stack:** Go 1.27, AppWire JSON-RPC and its Go-to-TypeScript generator, React 19, TypeScript 6, Zustand, CSS modules, Vitest, Testing Library, and the existing Chrome overflow guard.

**Spec:** `docs/superpowers/specs/2026-08-27-focus-sentence-design.md`

**Testing policy read first:** `docs/developing-evener/testing.md`. All tests below are deterministic and offline. They use typed state, notifications, channels, and awaited promises as oracles; they do not assert prompt prose, sleep, poll a race, widen a timeout, or mock an Evener internal merely to make the path pass.

## Scope and invariants

- Keep `appwire.TaskAggregate.Current`, `appwire.GoalState.Objective`, `events.EventTaskUpdated`, `events.EventGoalUpdated`, `appwire.NotifyEvenerTaskUpdated`, and `appwire.NotifyEvenerGoalUpdated` additive and wire-compatible.
- A task summary is always total count, done count, and the first task in list order whose status is `task.TaskInProgress`.
- Root and descendant `thread/read` responses must reflect every task/goal carrier committed before their subscription cut; they must not re-pull a task or goal store when handling that carrier.
- The typed AppWire params are the state patch. Internal task-store owner routing metadata rides `appprojector.AppNotification`, not the public AppWire payload. Do not decode the event a second time or invent a parallel revision counter.
- Goal seed semantics are tri-state: absent `SessionStartData.CurrentWork` means an old/unknown producer and does not patch current work; present `CurrentWork` with `Goal == nil` is an authoritative no-goal seed; present `CurrentWork` with non-nil `Goal` seeds that value. Live `GoalUpdatedParams.Goal == nil` always explicitly clears a previously seeded or updated goal.
- Root goal seeding comes from `ThreadEnvelopeSource.SessionMeta().Goal`; do not add the objective to the positional `Session.GoalStatus` return list. Restore `GoalStatus() (status string, iterations int, ok bool)` for its remaining agent-internal/status callers.
- Every new producer stamps a `CurrentWork` seed and `TaskStoreOwnerSessionID` on `SessionStartData`. Because subagent templates populate after the child’s start event, each successful post-start population emits a `TaskUpdatedData` correction immediately afterward.
- Every `TaskUpdatedData` names `TaskStoreOwnerSessionID`. The server fans that typed carrier to the root and every cached descendant with the same owner, so a shared task store updates all of its views even though only the mutating session emitted an event.
- `ThreadModel` remains the only render source for `CurrentWork` and `GoalControl`.
- The Focus sentence task action means **show/focus Tasks** and is idempotent. The unified `SessionMenu` Tasks item retains its existing **toggle open/closed** meaning.
- Replacing a composer draft with `/goal …` is one operation: it deals with settled and pending attachments, exits recovery ownership, closes slash completion, resets stale submission state, persists the new ordinary draft, and requests focus that is consumed only after the textarea exists.
- Keep `CurrentWork`’s live region mounted while the component is mounted; announce through stable text content, not a changing `aria-label` on an empty element.
- Run `npx biome check --write` only on touched frontend source/test files before the frontend gate.

## Explicit non-goals

- No generic state-management, event-sourcing, or reducer framework in Go.
- No revision field added to all AppWire events, notifications, or thread snapshots.
- No UI redesign, new panel, new Focus sentence layout, or changed 559px responsive treatment.
- No unrelated test cleanup, timeout changes, assertion weakening, fuzz-suite reorganization, or runtime-pair test work.
- Commit `48ed7d5e5` (`test: isolate concurrent web process records`) is not part of this production refactor. Its removal from PR #550 is a delivery/history step only.

## Chosen carrier design and initialization-order proof

The smaller self-contained carrier design is viable and is the required implementation:

1. Root plugin-agent templates populate in `session_init.go` before `emitSessionStartEnvelope`, so the root start seed already carries the first populated in-progress task.
2. A fresh child’s `SessionStart` is emitted during construction, while child template expansion happens afterward in `subagents.go`. The successful population therefore emits a corrective `TaskUpdatedData` immediately afterward. Both events traverse the same child event path, so the correction cannot overtake the seed.
3. Restored stable delegates have the same late-population case in `delegate_runtime.go`; that path emits the same correction after a successful fallback population.
4. Shared children know their task-store owner from `spawn.sharedTaskStoreOwnerSessionID` before start. Their start and every later task carrier stamp that same owner; the root daemon can fan out from cached owner identity without looking up a child session or changing descendant callback signatures.
5. Goal seed clear is not guessed from a missing pointer. The outer `CurrentWork` pointer distinguishes legacy/unknown from a new authoritative seed, and the inner required `goal` member distinguishes explicit null from a goal value.
6. Existing persisted-turn seeding remains separate. The first descendant `SessionStart` supplies current work; `SetDescendantTranscriptPathFunc` supplies turns. Neither depends on disk timing for a live shared task store.

---

### Task 1: Centralize task summaries and make start/update carriers self-contained

**Files:**
- Modify: `agent/task/task_store.go`
- Test: `agent/task/task_store_test.go`
- Modify: `agent/events/payloads.go`
- Test: `agent/events/payloads_test.go`
- Test: `agent/events/eventdata_program_fuzz_test.go`
- Modify: `agent/session_config.go`
- Modify: `agent/session_events.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session_tools.go`
- Modify: `agent/session_tools_task.go`
- Modify: `agent/subagents.go`
- Modify: `agent/delegate_runtime.go`
- Test: `agent/cov_task_updated_test.go`
- Test: `agent/session_lossless_events_test.go`
- Test: `agent/delegate_resource_runtime_test.go`
- Create test: `agent/session_current_work_seed_test.go`

**Interfaces:**
- Add the transport-neutral task summary:

```go
type ListSummary struct {
    Total   int
    Done    int
    Current *Task
}

func Summarize(tasks []Task) ListSummary
```

`Summarize` returns an owned copy for `Current`, counts only `TaskDone`, and chooses the first `TaskInProgress` in slice order.

- Add self-contained event seed/carrier fields:

```go
type CurrentWorkSeedData struct {
    Tasks *TaskStateData `json:"tasks,omitempty"`
    Goal  *GoalStateData `json:"goal"`
}

type TaskStateData struct {
    Total   int              `json:"total"`
    Done    int              `json:"done"`
    Current *TaskSummaryData `json:"current,omitempty"`
}

type SessionStartData struct {
    // existing fields unchanged
    CurrentWork            *CurrentWorkSeedData `json:"current_work,omitempty"`
    TaskStoreOwnerSessionID string               `json:"task_store_owner_session_id,omitempty"`
}

type TaskUpdatedData struct {
    TaskStateData
    TaskStoreOwnerSessionID string           `json:"task_store_owner_session_id,omitempty"`
}
```

- Tri-state seed semantics are structural, not inferred from a zero value:
  - `SessionStartData.CurrentWork == nil`: old/unknown producer; do not change cached task/goal state.
  - `CurrentWork != nil && CurrentWork.Goal == nil`: authoritative seed saying no goal exists.
  - `CurrentWork.Goal != nil`: authoritative goal seed.
  - `CurrentWork.Tasks == nil`: task state unavailable; a non-nil zero-valued task update is authoritative empty.
- Add `(*Session).taskStoreOwnerSessionID() string`: return `cfg.spawn.sharedTaskStoreOwnerSessionID` when non-empty, otherwise `s.id`.
- Add `taskStateData(summary task.ListSummary) events.TaskStateData`; change `taskUpdatedData` to wrap that state plus `taskStoreOwnerSessionID`. Start seeds and update events therefore cannot drift in total/done/current conversion.
- `emitSessionStartEnvelope` stamps `CurrentWork` and owner before emitting. It derives goal from structured `Session.Meta().Goal`, never from the positional `GoalStatus` API.
- Root plugin-agent template population already occurs before `emitSessionStartEnvelope` (`session_init.go`), so its start seed contains the populated current task.
- Fresh subagent population (`subagents.go`) and restored stable-delegate fallback population (`delegate_runtime.go`) occur after their `SessionStart`; each successful post-start population emits one `EventTaskUpdated` with the post-population summary and owner. A no-op population may emit the same authoritative summary, but a failed population emits no success carrier.

- [ ] **Step 1: Write failing summary tests**

Add to `agent/task/task_store_test.go`:

- `TestSummarizeCountsDoneAndSelectsFirstInProgress`: pass done, first-in-progress, later-in-progress, cancelled, and open rows; assert `Total == 5`, `Done == 1`, and `Current.ID`/`Description` name the first in-progress row.
- `TestSummarizeReturnsOwnedCurrentTask`: mutate the input task and its slices after summarizing; assert the returned current task is unchanged.
- Extend the existing `Progress` and `CurrentInProgress` tests to compare their results with `Summarize(store.View())`, proving one semantic definition without deleting either public read API.

- [ ] **Step 2: Run the task tests and confirm red**

```bash
go test ./agent/task -run 'Test(Summarize|TaskStore_Progress|TaskStore_CurrentInProgress)' -count=1
```

Expected red: `ListSummary` and `Summarize` do not exist.

- [ ] **Step 3: Implement the neutral summary only**

Implement `Summarize` beside `Progress`/`CurrentInProgress`. Do not import `agent/events` or `appwire` into `agent/task`; the task package owns list semantics, not transport. Reuse the summary scan in existing readers where that does not widen lock scope or return aliases.

- [ ] **Step 4: Write failing JSON tri-state and owner tests**

Add to `agent/events/payloads_test.go`:

- `TestSessionStartCurrentWorkSeedTriStateJSON`: marshal/unmarshal three rows and assert the JSON distinction among absent `current_work`, `"current_work":{"goal":null}`, and a non-null goal object. Assert `goal` is present inside every non-nil `current_work` object because it intentionally has no `omitempty`.
- `TestSessionStartCurrentWorkSeedCarriesAuthoritativeEmptyTasks`: assert `tasks:{total:0,done:0}` remains non-nil after round-trip.
- `TestTaskUpdatedDataCarriesTaskStoreOwnerSessionID`: assert the owner survives typed and generic event-data round trips.

Register the new shapes in the existing event-data program/fuzz seeds so the test is reachable; do not add a prose snapshot oracle.

- [ ] **Step 5: Write failing initialization-order tests**

Create `agent/session_current_work_seed_test.go` with:

- `TestRootSessionStartSeedsPostTemplateCurrentTask`: construct a root plugin-agent session whose templates auto-start task 1; inspect the already-queued `SessionStartData` and assert `CurrentWork.Tasks.Current.Description` is task 1 and owner is the root ID. This pins the observed `session_init.go` ordering: root population precedes start.
- `TestFreshChildStartThenTemplatePopulationEmitsTaskCorrection`: attach the descendant callback before constructing the child, assert `SessionStart` arrives first, populate the child’s post-start templates through the real spawn path, then assert the next relevant event is `TaskUpdatedData` containing the first populated task and the child owner.
- `TestSharedChildStartAndTaskUpdateNameRootOwner`: spawn with `ShareTasksWithChildren=true`; assert both child start seed and a child mutation carrier name the root owner ID and summarize the shared store.
- `TestRestoredDelegatePostStartPopulationEmitsTaskCorrection`: drive the existing restored-delegate fallback path with an empty store plus descriptor templates; assert start precedes the correction and the correction carries the populated first task.
- `TestSessionStartGoalSeedUsesStructuredMetaAndExplicitClear`: restored metadata with a goal seeds objective/status/iterations; a fresh session produces non-nil `CurrentWork` with nil `Goal`, proving clear rather than unknown.

Use the event channel/callback itself as the barrier. No sleep, retry loop, or provider call.

- [ ] **Step 6: Run event/session tests and confirm red**

```bash
go test ./agent/events -run 'Test(SessionStartCurrentWork|TaskUpdatedDataCarries)' -count=1
go test ./agent -run 'Test(RootSessionStartSeeds|FreshChildStart|SharedChildStart|RestoredDelegatePostStart|SessionStartGoalSeed)' -count=1
```

Expected red: current start payloads contain no current-work seed/owner, and post-start template population emits no task correction.

- [ ] **Step 7: Stamp seeds and owners at the production boundaries**

Add a session helper that reads one task snapshot, calls `task.Summarize`, and converts `Session.Meta().Goal` into `GoalStateData`. If `TasksWithError` fails, leave `CurrentWork.Tasks` nil while still returning non-nil `CurrentWork` so goal absence remains authoritative.

Have `emitSessionStartEnvelope` fill only missing seed/owner fields so explicit event fixtures can still exercise unknown/legacy shapes. Stamp every task-tool carrier with `taskStoreOwnerSessionID`. After successful post-start `PopulateFromTemplates`, emit from the child session itself; do not synthesize the event in the parent or widen `SetDescendantEventFunc`.

Use `task.Summarize` for task-tool progress text and event conversion. Keep one direct event per existing logical mutation point; do not add TaskStore subscriptions or callbacks.

- [ ] **Step 8: Format, rerun, and commit**

```bash
gofmt -w agent/task/task_store.go agent/task/task_store_test.go agent/events/payloads.go agent/events/payloads_test.go agent/events/eventdata_program_fuzz_test.go agent/session_config.go agent/session_events.go agent/session_init.go agent/session_tools.go agent/session_tools_task.go agent/subagents.go agent/delegate_runtime.go agent/cov_task_updated_test.go agent/session_lossless_events_test.go agent/delegate_resource_runtime_test.go agent/session_current_work_seed_test.go
go test ./agent/task -run 'Test(Summarize|TaskStore_Progress|TaskStore_CurrentInProgress)' -count=1
go test ./agent/events -run 'Test(SessionStartCurrentWork|TaskUpdatedDataCarries)' -count=1
go test ./agent -run 'Test(TaskTool_.*EmitsTaskUpdated|RootSessionStartSeeds|FreshChildStart|SharedChildStart|RestoredDelegatePostStart|SessionStartGoalSeed)' -count=1
git add agent/task/task_store.go agent/task/task_store_test.go agent/events/payloads.go agent/events/payloads_test.go agent/events/eventdata_program_fuzz_test.go agent/session_config.go agent/session_events.go agent/session_init.go agent/session_tools.go agent/session_tools_task.go agent/subagents.go agent/delegate_runtime.go agent/cov_task_updated_test.go agent/session_lossless_events_test.go agent/delegate_resource_runtime_test.go agent/session_current_work_seed_test.go
git commit -m "feat: carry self-contained current work state"
```

Expected green: the start event is sufficient to seed current work, later task carriers name their shared owner, and child initialization order is explicit in tests.

### Task 2: Seed cached threads and atomically fan out typed AppWire patches

**Files:**
- Modify: `agent/session_envelope_sampling.go`
- Modify: `agent/session_goal.go`
- Test: `agent/session_goal_test.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Test: `internal/appprojector/appwire_projection_test.go`
- Modify: `server/thread_envelope.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/bridge.go`
- Test: `server/thread_envelope_test_helpers_test.go`
- Test: `server/thread_envelope_test.go`
- Test: `server/appwire_server_test.go`
- Modify: `cmd/evener/serve.go`
- Test: `cmd/evener/serve_residual_fuzz_test.go`
- Test: `cmd/evener-hub/internal/hubcore/prober_wire_test.go`

**Interfaces:**
- Restore `(*agent.Session).GoalStatus() (status string, iterations int, ok bool)` and the same method in `agent.EnvelopeSampling`; objective seeding no longer uses this positional API.
- Keep the existing public AppWire task carrier unchanged. Carry owner identity only as internal projector metadata:

```go
type AppNotification struct {
    ThreadID                string
    Method                  string
    Params                  any
    TaskStoreOwnerSessionID string
}
```

- `AppEventProjector` maps `SessionStartData.CurrentWork` into the `ThreadStartedParams.Thread.Evener.Tasks/Goal` full snapshot. For `SessionStart` and `TaskUpdated`, it copies `TaskStoreOwnerSessionID` into the internal `AppNotification` field while leaving `ThreadStartedParams`/`TaskUpdatedParams` wire shapes unchanged.
- `threadEnvelope` and `appDescendantProjection` cache `TaskStoreOwnerSessionID` internally. Do not add owner identity to `EvenerThread`; it is routing state, not frontend display state.
- Root `RefreshThreadEnvelope` still provides the pre-bridge seed: task aggregate from `TaskAggregate()`, goal from the same structured `SessionMeta` value used for Name/Preview, and owner defaulted to the root thread ID. The subsequent `SessionStartData.CurrentWork` seed is replay evidence and reaffirms/replaces those fields inside the first projection commit.
- Start seed application is tri-state:
  - absent `CurrentWork`: preserve the existing root seed and leave a new descendant’s task/goal unknown;
  - present seed with nil goal: clear goal;
  - present seed with goal: replace goal;
  - nil `Tasks`: preserve/leave unknown task state; non-nil Tasks replaces it, including authoritative zero.
- `EventTaskUpdated` and `EventGoalUpdated` do not trigger source re-pulls. Keep `facetTasks` and `facetGoal` for initial/checkpoint sampling and legacy goal lifecycle events; `facetGoal` reads `SessionMeta().Goal` instead of a widened source method.
- Add narrowly scoped typed patch helpers:
  - `TaskUpdatedParams` replaces the whole `TaskAggregate`;
  - `GoalUpdatedParams.Goal` replaces the whole goal, including nil clear.
  These helpers are not a generic state framework.
- Within one `CommitProjection` closure, a task carrier with a non-empty owner targets its source plus every cached root/descendant projection whose owner matches. Each target gets its own correctly stamped `ThreadID`/`Ref` notification and cached-state patch. An empty owner (old producer) targets only the source.
- Goal updates remain session-specific and target only the source thread.

- [ ] **Step 1: Write failing projector tests for seed/owner propagation**

Extend `internal/appprojector/appwire_projection_test.go`:

- `TestProject_SessionStartCarriesCurrentWorkSeed`: project a present seed with task summary and goal; assert the `ThreadStartedParams.Thread.Evener` snapshot contains both.
- `TestProject_SessionStartExplicitNoGoalSeed`: project present `CurrentWork` with nil goal and assert the full started thread has nil goal.
- `TestProject_SessionStartWithoutCurrentWorkRemainsCompatible`: project a legacy start and assert no task/goal is invented.
- Extend `TestProject_TaskUpdated` to assert `TaskStoreOwnerSessionID` is preserved on internal `AppNotification` metadata and is absent from the public `TaskUpdatedParams` JSON shape.

- [ ] **Step 2: Write failing root seed and no-repull tests**

Update `server/thread_envelope_test_helpers_test.go` so the stub source exposes goal state only through `SessionMeta().Goal`. Add/update in `server/thread_envelope_test.go`:

- `TestThreadEnvelopeSeedUsesTaskAggregateAndStructuredMetaGoal`: source a current task and `schema.GoalSnapshot{Objective:"wire goal objective", Status:"active", Iterations:2}`; assert one root read contains task description and all goal fields.
- `TestSessionStartSeedTriStatePreservesUnknownAndAppliesExplicitClear`: pre-seed a goal, bridge a legacy start with nil `CurrentWork` and assert it remains; replace identity/seed again, bridge a new start with present `CurrentWork`/nil goal and assert it clears.
- `TestTaskAndGoalCarrierEventsDoNotRepullEnvelopeStores`: count source calls, seed once, send task and goal events while source remains stale, and assert task/meta goal sampling counts do not increase.
- `TestTaskAndGoalCarriersReplaceSeededRootState`: seed old task/goal, bridge a typed task replacement then typed goal clear, and assert `thread/read` returns the new task and nil goal.

Restore three-value assertions in `agent/session_goal_test.go`; test objective persistence separately through `Session.Meta().Goal`.

- [ ] **Step 3: Run projector/root tests and confirm red**

```bash
go test ./internal/appprojector -run 'TestProject_(SessionStart|TaskUpdated)' -count=1
go test ./agent -run 'TestGoalStatus' -count=1
go test ./server -run 'Test(ThreadEnvelopeSeedUses|SessionStartSeedTriState|TaskAndGoalCarrier)' -count=1
```

Expected red: internal projector owner metadata is absent, `GoalStatus` is still widened, and carrier events currently refresh stale task/goal source values before projection.

- [ ] **Step 4: Implement structured seeding and remove carrier re-pulls**

Refactor seed/checkpoint sampling so a refresh that needs metadata and goal obtains `SessionMeta` once, derives Name/Preview and goal from `meta.Goal`, obtains the task aggregate only when `facetTasks` is named, and commits after rechecking identity. Keep the session/turn checkpoint backstop and ordinary facets for queue, diagnostics, context, work, failures, ask, escalations, reasoning, and metadata name/preview.

Remove only `EventTaskUpdated` and `EventGoalUpdated` from `facetsByEvent`, and remove `GoalStatus` from `ThreadEnvelopeSource`; keep `facetTasks`/`facetGoal` on session/turn checkpoints and legacy goal lifecycle events. Delete `s.refreshFacets(facetGoal)` from `handleAppGoalSet`: the successful callback’s `EventGoalUpdated` is the direct live authority, while later checkpoints remain a compatibility backstop.

Project the optional start seed into `ThreadStartedParams`. In server code, inspect the typed start event only to cache `TaskStoreOwnerSessionID` and to distinguish absent seed from explicit nil goal; use the projected AppWire thread fields for the actual task/goal values.

- [ ] **Step 5: Write failing atomic fanout and descendant tests**

Add to `server/appwire_server_test.go`:

- `TestServerAppWireTaskAndGoalPatchesAreInSnapshotBeforeNotificationDelivery`: subscribe, commit a task update and goal update, read after each received notification, and assert snapshot equals the typed carrier while the source remains deliberately stale.
- `TestServerAppWireTaskAndGoalUpdatesHaveOneOrderForEveryClient`: subscribe two clients to the root, use the existing inside-commit channel seam to establish task-before-goal happens-before ordering, and assert both receive the same method/sequence order and final state.
- `TestServerAppWireDescendantSessionStartSeedsCurrentTaskAndGoal`: make the first descendant event a `SessionStart` with current-work seed; assert descendant `thread/read` contains both fields before any later task/goal event.
- `TestServerAppWireDescendantSessionStartExplicitlyClearsGoal`: seed/reuse a cached descendant goal, apply a present start seed with nil goal in the controlled identity fixture, and assert clear; a legacy absent seed must not clear.
- `TestServerAppWireDescendantCarriersReplaceSeedAndClearGoal`: send descendant task replacement and goal clear events; assert its cached thread changes and root does not.
- `TestServerAppWireSharedTaskOwnerFansOutInOneCommit`: cache root, child, and unrelated child owner IDs; send one child task event naming the root owner; assert root and matching child each receive correctly targeted task notifications and reads with the same current description, while unrelated child receives neither patch nor notification.
- `TestServerAppWireOldTaskProducerUpdatesOnlySource`: owner empty; assert backward-compatible source-only behavior.

Extend `TestServerAppWireGoalUpdatedFanoutToEverySubscribedClient`: after both clients receive the session-specific goal notification, assert both reads resolve to the same objective/status/iterations.

Use channels and existing commit seams to impose ordering; never rely on goroutine scheduling.

- [ ] **Step 6: Run the server tests and confirm red**

```bash
go test ./server -run 'TestServerAppWire(TaskAndGoal|DescendantSessionStart|DescendantCarriers|SharedTaskOwner|OldTaskProducer|GoalUpdatedFanout)' -count=1
```

Expected red: descendant cached threads currently seed only turns/identity, and a shared-store update targets only the mutating session.

- [ ] **Step 7: Apply and fan out typed patches inside `CommitProjection`**

Project first, then inspect typed `AppNotification` params before `stampAppNotificationTarget` converts them to JSON. Apply start/task/goal replacements under `s.mu` inside the same `CommitProjection` closure that allocates and routes notification sequences. Clone `Current` and `Goal` values when installing them so cached state owns its pointers.

For task updates, compute the target list under `s.mu` from cached owner IDs, sort descendant IDs for deterministic fanout, and include the source exactly once. For each target, make a fresh `TaskUpdatedParams`, stamp its own `ThreadID`/`Ref`, patch that target, then call `Notifier.Record`. Do not broadcast one source-targeted params object to all threads.

For descendants, retain existing persisted transcript seeding. A first `SessionStart` supplies current-work and owner seed; fold it before the notification is recorded. Goal updates never fan out by task owner.

- [ ] **Step 8: Restore three-value `GoalStatus` and run focused tests**

Update compile-affected callers without unrelated cleanup:

- `agent/session_misc_fuzz_test.go`
- `agent/session_tools_stateful_program_fuzz_test.go`
- `agent/session_compaction_lifecycle_fuzz_test.go`
- `agent/lifecycle_seqfuzz_test.go`
- `agent/session_tools_misc_contract_fuzz_test.go`
- `agent/session_state_goal_exact_fuzz_test.go`
- `cmd/evener/serve_residual_fuzz_test.go`
- `cmd/evener-hub/app_threadread_tasks_test.go`
- `cmd/evener-hub/internal/hubcore/prober_wire_test.go`

Run:

```bash
gofmt -w agent/session_envelope_sampling.go agent/session_goal.go agent/session_goal_test.go agent/session_misc_fuzz_test.go agent/session_tools_stateful_program_fuzz_test.go agent/session_compaction_lifecycle_fuzz_test.go agent/lifecycle_seqfuzz_test.go agent/session_tools_misc_contract_fuzz_test.go agent/session_state_goal_exact_fuzz_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go server/thread_envelope.go server/appwire_runtime.go server/bridge.go server/thread_envelope_test_helpers_test.go server/thread_envelope_test.go server/appwire_server_test.go cmd/evener/serve.go cmd/evener/serve_residual_fuzz_test.go cmd/evener-hub/app_threadread_tasks_test.go cmd/evener-hub/internal/hubcore/prober_wire_test.go
go test ./agent -run 'TestGoalStatus' -count=1
go test ./internal/appprojector -run 'TestProject_(SessionStart|TaskUpdated)' -count=1
go test ./server -run 'Test(ThreadEnvelopeSeedUses|SessionStartSeedTriState|TaskAndGoalCarrier|ServerAppWire(TaskAndGoal|DescendantSessionStart|DescendantCarriers|SharedTaskOwner|OldTaskProducer|GoalUpdatedFanout))' -count=1
go test ./cmd/evener -run 'TestServe' -count=1
go test ./cmd/evener-hub/internal/hubcore -run 'Test.*Wire' -count=1
```

Expected: every command exits zero; direct task/goal carrier handling performs no source callback, while checkpoint tests still prove old-producer recovery.

- [ ] **Step 9: Commit the atomic projection refactor**

```bash
git add agent/session_envelope_sampling.go agent/session_goal.go agent/session_goal_test.go agent/session_misc_fuzz_test.go agent/session_tools_stateful_program_fuzz_test.go agent/session_compaction_lifecycle_fuzz_test.go agent/lifecycle_seqfuzz_test.go agent/session_tools_misc_contract_fuzz_test.go agent/session_state_goal_exact_fuzz_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go server/thread_envelope.go server/appwire_runtime.go server/bridge.go server/thread_envelope_test_helpers_test.go server/thread_envelope_test.go server/appwire_server_test.go cmd/evener/serve.go cmd/evener/serve_residual_fuzz_test.go cmd/evener-hub/app_threadread_tasks_test.go cmd/evener-hub/internal/hubcore/prober_wire_test.go
git commit -m "fix: commit focus carriers into thread state"
```

### Task 3: Use the centralized task summary for persisted/live producers

**Files:**
- Modify: `cmd/evener/serve.go`
- Test: `cmd/evener/serve_residual_fuzz_test.go`
- Modify: `cmd/evener-hub/app_threadread.go`
- Test: `cmd/evener-hub/app_threadread_tasks_test.go`

**Interfaces:**
- `liveThreadEnvelopeSource.TaskAggregate` and `persistedTaskAggregate` both call `task.Summarize` and only convert its neutral result to `appwire.TaskAggregate`.
- `persistedGoalState(*schema.GoalSnapshot)` remains the one structural persisted-goal conversion and is also reused by root/descendant seed helpers where package boundaries permit.
- Missing/malformed task files retain the existing nil/unknown semantics; an existing empty task file remains authoritative `0/0`.

- [ ] **Step 1: Tighten persisted/live parity tests**

Extend `cmd/evener-hub/app_threadread_tasks_test.go`:

- `TestPastThreadReadProjectsFirstInProgressTask` must include a later in-progress row and assert the first wins.
- `TestPastThreadReadProjectsPersistedGoal` must assert objective, status, and iterations.
- `TestTaskAggregateMalformedPersistedStoreMatchesLiveAndColdUnknown` continues to assert nil, not a false zero.

Add a focused `cmd/evener` test or the existing residual fuzz assertion proving the live adapter converts a `task.ListSummary` with the same total/done/current values as the cold producer.

- [ ] **Step 2: Prove the old duplicate scans are still present**

```bash
rg -n 'for _, task := range (tasks|items)' cmd/evener/serve.go cmd/evener-hub/app_threadread.go
go test ./cmd/evener-hub -run 'Test(PastThreadReadProjects|TaskAggregateMalformed)' -count=1
```

Expected before implementation: the tests pass but `rg` finds duplicate total/done/current scans. This is a refactor task whose red proof is the static duplication check, while behavioral tests pin compatibility.

- [ ] **Step 3: Replace only the duplicate calculations and commit**

```bash
gofmt -w cmd/evener/serve.go cmd/evener/serve_residual_fuzz_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_tasks_test.go
! rg -n 'aggregate\.Current == nil && task\.Status|done\+\+' cmd/evener/serve.go cmd/evener-hub/app_threadread.go
go test ./cmd/evener-hub -run 'Test(PastThreadReadProjects|TaskAggregateMalformed)' -count=1
go test ./cmd/evener -run 'Test.*Envelope.*Task' -count=1
git add cmd/evener/serve.go cmd/evener/serve_residual_fuzz_test.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_tasks_test.go
git commit -m "refactor: share task summary semantics"
```

Expected: the static check and both test commands exit zero; absent/empty/malformed compatibility is unchanged.

### Task 4: Make frontend goal authority monotonic and reset GoalControl popovers

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts`
- Test: `cmd/evener-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx`

**Interfaces:**
- The locally synthesized `{objective, status:"active", iterations:0}` after `goal/set` is a response fallback only.
- Add one helper, `invalidateGoalResponseFallback(ref: string): void`, around `goalUpdateGenerations`.
- Invalidate only when a goal notification is accepted for that ref (applied to a tracked/watched model or buffered for its matching in-flight hydration), and whenever a full authoritative hydration is accepted/stored for the ref.
- A contradictory/unmatched notification does not cancel a valid response fallback.
- `GoalControl` closes its popover when `sessionRef` changes or the model transitions to `goal == null`; a later goal starts closed. Status/iteration updates to the same present goal may remain visible in an already-open popover.

- [ ] **Step 1: Write failing response/hydration ordering tests**

Add to `cmd/evener-hub/frontend/src/stores/threads.test.ts`:

- `setGoal does not overwrite a newer accepted goal notification in either tracked map` (retain and tighten the existing test).
- `setGoal does not overwrite a newer authoritative hydration`: hold the `goal/set` response promise, accept a `thread/read` hydration carrying a different goal, resolve the response, and assert the hydrated goal remains in both maps.
- `setGoal fallback survives an unaccepted contradictory notification`: emit a goal notification whose `threadId` conflicts with the tracked ref, resolve `goal/set`, and assert the successful response fallback is installed.
- `buffered accepted goal notification invalidates a pending response fallback`: begin hydration, buffer the matching notification, complete hydration/replay, resolve `goal/set`, and assert the replayed notification wins.

Use the fake client’s promises and existing hydration completion helpers as barriers.

- [ ] **Step 2: Write the failing popover reset test**

Add `GoalControl closes an open popover when the goal clears and a later goal starts closed` to `GoalControl.test.tsx`: open the popover, rerender with `goal:null`, rerender with a new goal, and assert the popover content is absent until the trigger is clicked again. Add a session-ref rerender assertion to the same test or a separate `GoalControl resets popover state when reused for another session` test.

- [ ] **Step 3: Run focused frontend tests and confirm red**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/stores/threads.test.ts src/panes/session/chrome/GoalControl.test.tsx --maxWorkers=4
```

Expected red: accepted hydration does not currently advance `goalUpdateGenerations`, and `GoalControl` retains `popoverOpen` while returning null.

- [ ] **Step 4: Implement accepted-authority invalidation and popover reset**

Move notification invalidation out of the unconditional top of `handleNotification`. Invoke it only after target acceptance is known, including the pending-hydration buffer path. Invoke it immediately before publishing an accepted full hydration into `threads` or `watchedThreads`; a late `goal/set` response then observes a changed generation and returns without patching.

In `GoalControl`, use effects keyed to `sessionRef` and goal absence to reset `popoverOpen`; do not add a second goal cache or optimistic component state.

- [ ] **Step 5: Format, rerun, and commit**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/stores/threads.ts src/stores/threads.test.ts src/panes/session/chrome/GoalControl.tsx src/panes/session/chrome/GoalControl.test.tsx
npx vitest run src/stores/threads.test.ts src/panes/session/chrome/GoalControl.test.tsx --maxWorkers=4
cd ../../../..
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/stores/threads.test.ts cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.tsx cmd/evener-hub/frontend/src/panes/session/chrome/GoalControl.test.tsx
git commit -m "fix: preserve authoritative goal updates"
```

### Task 5: Make Focus action links safe and keep the live announcement stable

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/attachments/useAttachments.ts`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/attachments/useAttachments.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/composerFocus.ts`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/composerFocus.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`

**Interfaces:**
- Add `UseAttachmentsResult.reset(): void`. It clears settled and pending items, resets marker numbering, and invalidates every in-flight encode continuation so it cannot strip text, re-add an item, or toast after replacement.
- Add/retain a pending focus request until a real textarea node exists. `Composer` consumes it only after successfully calling `.focus()` on a mounted textarea.
- Replace `replaceDraftWithGoal` with one canonical `replaceComposerWithGoalDraft(objective: string)` operation that:
  1. invalidates queued recovery persistence and synchronously clears `activeRecoveryIdRef`/state without deleting the durable recovery record;
  2. resets all attachments, including pending encodes;
  3. closes slash completion and resets its highlighted index;
  4. clears stale drain/submission snapshots;
  5. writes `/goal ${objective}` through `textEditor.write` after recovery ownership is cleared, so `writeDraft(ref, command)` runs;
  6. closes the confirmation dialog; and
  7. calls `requestComposerFocus(ref)`, leaving focus pending until the textarea is mounted.
- `editGoal` asks for confirmation when any text, attachment (settled or pending), or active recovery draft would be replaced.
- Split Composer task actions:
  - `showTasks`: mobile drawer `.open()`; desktop `workspaceStore.openPane("sessionTasks", {ref}, {slot:"secondary"})`.
  - `toggleTasks`: existing mobile behavior; desktop `workspaceStore.togglePane(...)` and pass this callback to `SessionChrome`/`SessionMenu`.
  - `CurrentWork.onOpenTasks` receives `showTasks`.
- `CurrentWork` always renders one stable, visually hidden `role="status" aria-live="polite" aria-atomic="true"` node while mounted. Its text content is the composed task/goal announcement. The visible row remains absent when both values are empty.

- [ ] **Step 1: Write failing attachment reset tests**

Add to `useAttachments.test.ts`:

- `reset clears settled items and restarts markers at one`.
- `reset invalidates a pending encode success`.
- `reset invalidates a pending encode failure without stripping replacement text or reporting rejection`.

Control the encode promise with the existing encoder mock; resolve/reject it explicitly after `reset`.

- [ ] **Step 2: Write failing Composer replacement tests**

Add to `Composer.test.tsx`:

- `editing the goal confirms before replacing a draft that contains only attachments`.
- `confirmed goal replacement clears settled attachments and persists an ordinary goal draft`.
- `confirmed goal replacement invalidates pending attachments without later changing the goal command`.
- `confirmed goal replacement exits recovery without deleting its durable recovery row`.
- `goal replacement closes slash completion and resets selection`.
- `goal replacement focus waits until an ended follow-up textarea mounts`.
- `clicking the current task twice keeps one Tasks pane open and focuses it`.
- `SessionMenu Tasks still toggles the Tasks pane closed` (place the adapter assertion in `SessionChrome.test.tsx` if that is where the real workspace callback is mounted).

Assert draft storage through `readDraft(ref)`, recovery through the pending-turn store, and workspace state through `workspaceStore`; do not inspect implementation-local React state.

- [ ] **Step 3: Write the failing stable live-region test**

Update `CurrentWork.test.tsx`:

- `keeps one text-content live region while task and goal change`: capture the status node, rerender task+goal, task-only, empty, and goal-only states, assert node identity is unchanged, and assert `textContent` becomes the complete announcement or empty string.
- Keep the existing keyboard/action/title assertions for both visible links.
- Replace assertions against `aria-label` on an empty node with assertions against actual live-region text content.

- [ ] **Step 4: Run focused tests and confirm red**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/composer/attachments/useAttachments.test.ts src/panes/session/composer/composerFocus.test.ts src/panes/session/composer/CurrentWork.test.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/chrome/SessionChrome.test.tsx --maxWorkers=4
```

Expected red: there is no attachment reset, attachment-only drafts bypass confirmation, the task link toggles the panel closed, focus is consumed before a missing textarea mounts, and the live region is replaced/empty-labelled rather than stable text content.

- [ ] **Step 5: Implement the canonical replacement and split link semantics**

Use an attachment generation token captured by each `ingestFiles` encode continuation; `reset` increments it before clearing state. Clear recovery ownership synchronously before calling `textEditor.write`, because that method decides draft persistence from `activeRecoveryIdRef.current`, not React state.

Do not discard the durable recovery record when the user replaces the local composer: it must remain available from QueueStrip. Increment the local recovery write version and clear ownership so an already-queued completion cannot clear the newly written `/goal` draft.

Use the existing `workspaceStore.openPane` idempotency for the inline task link and leave `SessionChrome`’s menu callback on `togglePane`. Do not change `SessionMenu` labels or checked-state rendering.

Render the stable status node before the conditional visible row in `CurrentWork`; return the fragment even when the visible row is absent.

- [ ] **Step 6: Format, rerun, browser-check, and commit**

```bash
cd cmd/evener-hub/frontend
npx biome check --write src/panes/session/composer/attachments/useAttachments.ts src/panes/session/composer/attachments/useAttachments.test.ts src/panes/session/composer/composerFocus.ts src/panes/session/composer/composerFocus.test.ts src/panes/session/composer/Composer.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/composer/CurrentWork.tsx src/panes/session/composer/CurrentWork.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
npx vitest run src/panes/session/composer/attachments/useAttachments.test.ts src/panes/session/composer/composerFocus.test.ts src/panes/session/composer/CurrentWork.test.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/chrome/SessionChrome.test.tsx --maxWorkers=4
npm run overflowguard
cd ../../../..
git add cmd/evener-hub/frontend/src/panes/session/composer/attachments/useAttachments.ts cmd/evener-hub/frontend/src/panes/session/composer/attachments/useAttachments.test.ts cmd/evener-hub/frontend/src/panes/session/composer/composerFocus.ts cmd/evener-hub/frontend/src/panes/session/composer/composerFocus.test.ts cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.tsx cmd/evener-hub/frontend/src/panes/session/composer/CurrentWork.test.tsx cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx
git commit -m "fix: make focus actions preserve composer state"
```

Expected: unit tests and overflowguard exit zero; no visual layout change is expected.

### Task 6: Correct stale design docs and comments

**Files:**
- Modify: `docs/superpowers/specs/2026-08-27-focus-sentence-design.md`
- Modify: `server/thread_envelope.go`
- Modify: `server/bridge.go`
- Modify: `agent/session_envelope_sampling.go`
- Modify: `cmd/evener/serve.go`
- Modify: `cmd/evener-hub/frontend/src/protocol/model.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Review only unless inaccurate: `cmd/evener-tui/hub_notification_coverage_test.go`
- Modify this plan only if implementation discoveries changed an exact name: `docs/superpowers/plans/2026-08-28-focus-sentence-refactor.md`

**Required corrections:**
- Replace the spec’s “live snapshots read from one goal-store snapshot” and “facet refresh runs before projection” language with the pre-bridge structured root seed, self-contained `SessionStartData.CurrentWork`, and typed carrier patching inside `CommitProjection`.
- State root/descendant initialization order, task-store owner fanout, persisted hub behavior, the start-seed unknown/null/value tri-state, live explicit goal clear, response-fallback invalidation, and action-link semantics.
- Remove `protocol/model.ts`’s claim that no goal push exists.
- Remove `Composer.test.tsx` comments claiming the goal chip comes from a module-level override cache.
- Update envelope/bridge comments so they describe direct carrier patching plus retained seed/checkpoint sampling, rather than claiming task/goal carriers re-pull state.
- Keep the TUI ignore comments if still true: the TUI deliberately renders tasks/goals from its own fetch/status surfaces even though the wire notifications now exist.

- [ ] **Step 1: Find every known stale statement**

```bash
rg -n 'No live push exists|no goal-changed|override cache|refresh-before-project|facet refresh runs before projection|Live snapshots read the objective|GoalStatus\(\).*objective|EventTaskUpdated.*facetTasks|EventGoalUpdated.*facetGoal' agent server cmd docs/superpowers/specs/2026-08-27-focus-sentence-design.md
```

Expected before edits: matches in the files listed above. Do not rewrite unrelated historical plans or reports merely because they describe the implementation state at their date.

- [ ] **Step 2: Update comments/spec and run static checks**

```bash
gofmt -w agent/session_envelope_sampling.go server/thread_envelope.go server/bridge.go cmd/evener/serve.go
cd cmd/evener-hub/frontend
npx biome check --write src/protocol/model.ts src/panes/session/composer/Composer.test.tsx
cd ../../../..
! rg -n 'No live push exists|no goal-changed notification|override the command applies|EventTaskUpdated.*facetTasks|EventGoalUpdated.*facetGoal' agent server cmd/evener-hub/frontend/src docs/superpowers/specs/2026-08-27-focus-sentence-design.md
git diff --check
```

- [ ] **Step 3: Commit documentation alignment**

```bash
git add docs/superpowers/specs/2026-08-27-focus-sentence-design.md docs/superpowers/plans/2026-08-28-focus-sentence-refactor.md agent/session_envelope_sampling.go server/thread_envelope.go server/bridge.go cmd/evener/serve.go cmd/evener-hub/frontend/src/protocol/model.ts cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx
git commit -m "docs: align focus sentence authority"
```

### Task 7: Deliver unrelated commit `48ed7d5e5` separately without rewriting history

**Files:**
- Delivery/history only. Do not edit `runtime_pair_build_test.go` as part of Tasks 1–6.

- [ ] **Step 1: Create an isolated side branch from current `origin/main`**

Use a managed worktree named `test-isolate-concurrent-web-process-records` based on freshly fetched `origin/main`. In that isolated checkout, cherry-pick `48ed7d5e5`. Resolve only `runtime_pair_build_test.go` if current main changed the same test fixture; abort rather than touching any other path.

Run `go test . -run '^TestMakeWebCommandsContainNodeProcessState$' -count=50`, `go test . -run '^(TestMakeTestWebInterruptAtWaitHandoff|TestMakeTestWebInterruptAtWaitHandoffRejectsLostSignalMutation)$' -count=5`, and `git diff --check origin/main...HEAD`. Push the side branch and open a focused PR to `main` that names only the concurrent process-record isolation fix.

- [ ] **Step 2: Revert only that commit from PR #550’s branch**

```bash
git revert --no-edit 48ed7d5e5
```

This is intentionally non-destructive: do not rebase, reset, or force-push PR #550. If the revert conflicts outside `runtime_pair_build_test.go`, abort and verify ancestry. Record the side PR URL in PR #550 and state that its focused test isolation should merge independently.

- [ ] **Step 3: Verify the delivery split**

```bash
git log --oneline origin/main..HEAD
git diff --name-only origin/main...HEAD | grep '^runtime_pair_build_test.go$' && exit 1 || true
git status --short --branch
```

Expected: PR #550 has no net runtime-pair diff; the side PR contains only that test file. Push PR #550 normally after all final gates below pass.

### Task 8: Self-review exact coverage, generated types, and complete gates

**Files:**
- Verify all files above.
- Regenerate if inputs require it: `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/types.gen.ts`.
- Modify only when a failing check identifies a root cause.

- [ ] **Step 1: Review specification coverage line by line**

Build a checklist in the PR description (not a new production abstraction) and point each requirement to its test:

- live root task description + goal objective → root seed/carrier tests;
- live descendant task description + goal objective → descendant seed/carrier tests;
- persisted task/goal → `app_threadread_tasks_test.go`;
- ordered multi-client updates → server two-client sequence test;
- shared task store → root/child fanout test;
- action links → Composer show-vs-SessionMenu-toggle tests;
- attachment/recovery/slash/reset/deferred focus → canonical replacement tests;
- goal fallback notification/hydration authority → threads-store tests;
- GoalControl reset → popover rerender test;
- stable text-content live region → node-identity/text-content test;
- stale docs/comments → negative `rg` checks.

If any row lacks a named test, add that test before proceeding; do not mark the row covered by code inspection alone.

- [ ] **Step 2: Review unresolved markers and type consistency**

```bash
marker_pattern='T''ODO|T''BD|FIX''ME|implement'' later|future'' wave'
if rg -n "$marker_pattern" docs/superpowers/plans/2026-08-28-focus-sentence-refactor.md docs/superpowers/specs/2026-08-27-focus-sentence-design.md; then exit 1; fi
rg -n 'type (CurrentWorkSeedData|TaskStateData|TaskUpdatedData|GoalUpdatedData|TaskUpdatedParams|GoalUpdatedParams|TaskAggregate|GoalState)' agent/events appwire
rg -n 'interface (TaskUpdatedParams|GoalUpdatedParams|TaskAggregate|GoalState)' cmd/evener-hub/frontend/src/protocol/types.gen.ts
```

Expected: no unresolved marker remains. Verify field-for-field consistency:

- start seed: absent `current_work` is unknown; present `current_work` has optional tasks and a required nullable goal;
- task: public AppWire remains `total`, `done`, optional `current{id,description}`; task-store owner identity exists only on internal agent events/projector metadata;
- goal: required `goal` key whose value is `{objective,status,iterations}` or null;
- Go event → Go projector → Go AppWire params → generated TypeScript → frontend reducer uses the same optional/null semantics.

Do not manually edit `types.gen.ts` or `docs/appwire-protocol.md`.

- [ ] **Step 3: Regenerate and prove generated output is current**

```bash
make generate
git diff --check
git status --short
```

The refactor intentionally adds no new public AppWire field. Generation must add no new diff.

- [ ] **Step 4: Run focused Go and frontend suites**

```bash
go test ./agent/task ./agent/events ./agent ./internal/appprojector ./appwire ./server ./cmd/evener ./cmd/evener-hub ./cmd/evener-hub/internal/hubcore ./cmd/evener-tui -count=1
cd cmd/evener-hub/frontend
npx vitest run src/protocol/reducer.test.ts src/stores/threads.test.ts src/panes/session/chrome/GoalControl.test.tsx src/panes/session/chrome/SessionChrome.test.tsx src/panes/session/composer/attachments/useAttachments.test.ts src/panes/session/composer/composerFocus.test.ts src/panes/session/composer/CurrentWork.test.tsx src/panes/session/composer/Composer.test.tsx --maxWorkers=4
cd ../../../..
```

Expected: both commands exit zero. Read and fix every warning/error; “no tests” is not a pass.

- [ ] **Step 5: Run repository gates in the documented order**

```bash
make lint
make build
ROOT_FULL=1 make test
make test-dev-tooling
make test-web-browser
make vet
make test-race
```

Expected: every command exits zero. `make test-web-browser` requires Chrome/Chromium; unavailable browser capability is an incomplete gate, not a pass. `make test-race` is required because this refactor adds task-owner fanout while mutating root/descendant cached projection state inside the shared commit path.

- [ ] **Step 6: Inspect final PR tree and push safely**

```bash
git log --oneline --decorate origin/main..HEAD
git diff --stat origin/main...HEAD
git diff --check origin/main...HEAD
git status --short --branch
git diff --name-only origin/main...HEAD | grep '^runtime_pair_build_test.go$' && exit 1 || true
git push origin focus-sentence
```

Confirm the worktree is clean, every commit is Focus sentence scoped, the unrelated runtime-pair commit is absent, the preserved side branch still exists locally, and PR #550’s head contains the plan, implementation, tests, and corrected spec/comments.
