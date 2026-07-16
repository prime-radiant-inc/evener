# Delegate Budget Truthfulness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make lifetime-turn and per-input tool-round exhaustion durable, explicitly non-successful delegate outcomes, while warning a session once when it reaches five remaining lifetime turns.

**Architecture:** Keep budget accounting in `Session`, convert both budget exits into one typed internal exhaustion error, and translate that error at the delegate boundary into a durable `exhausted` job terminal state. Persist the budget name, limit, delegate resumability, accepted-input turn count, and warning latch in the existing session/job stores; project those same facts through job tools, notifications, AppWire, Hub, TUI, and transcript events without changing the 500-turn delegate default or any continuation/retry policy.

**Tech Stack:** Go, Serf's append-only job store, Serf transcript/event types, AppWire, JavaScript Hub renderer tests, deterministic scripted LLM adapters, standard Go testing.

## Global Constraints

This spec does not:

- change the 500-turn delegate default;
- change goal iteration limits or goal continuation behavior;
- change provider retry limits;
- add Superpowers behavior or configuration;
- add an automatic escalation/reslicing policy;
- treat exhaustion as success for compatibility.

- Treat every requirement outside `docs/superpowers/specs/2026-07-15-delegate-budget-truthfulness-design.md` as a defect. Stop and ask Jesse instead of expanding the implementation scope.
- Preserve partial assistant text, tool evidence, output bytes, and transcript references when either budget is exhausted.
- Use the existing scripted-provider boundary for deterministic tests. Default tests must not use provider credentials, the network, quota, or ambient model behavior.
- Exercise structured state and observable behavior. Do not add large-string, rendered-command, HTML, JSON, or shell-script snapshots.
- Keep lifetime exhaustion non-resumable with reason `turn_budget_exhausted`; keep tool-round exhaustion resumable after the exhausted job is durably terminal.
- Treat a loop exit as tool-round exhaustion only when configured `MaxToolRoundsPerInput` is the binding cap; a continuation clamped by `goalRoundCap` retains its existing nil-error goal-continuation behavior.
- Persist the exhausted terminal record before arming or delivering its parent notification. Reuse the existing terminal generation and notification-deduplication machinery on recovery.
- A failure to persist the exhausted terminal record must settle the job as `failed`; it must never be surfaced as `completed` or `exhausted`.
- Do not add fallback handling for legacy state shapes. The only restore derivation is exact detection of the new warning steering text when its transcript append won a crash race against session-meta persistence.
- Before changing any test, re-read `docs/testing.md` and keep the tests below aligned with its deterministic Serf-plumbing boundary.

---

## File Structure

| Path | Responsibility |
|---|---|
| `agent/session_budget.go` | New internal budget names, typed exhaustion error, warning text, threshold decision, and transcript-warning derivation. |
| `agent/session.go` | Hold the accepted-input turn count and once-per-session warning latch. |
| `agent/session_lifecycle.go` | Queue the warning, return typed budget errors, and keep those expected terminal outcomes out of generic goal-error termination. |
| `agent/schema/snapshot.go` | Persist accepted-input turns and the warning latch in session metadata. |
| `agent/session_state.go` | Project the two new session fields into metadata. |
| `agent/session_init.go` | Restore accepted-input turns and the warning latch, deriving the latch from exact transcript steering when necessary. |
| `agent/session_budget_test.go` | Deterministic warning, accounting, restore, root/child wording, active-goal preservation, and goal-round-cap regression contracts. |
| `agent/session_dod_definition_test.go` | Update existing MaxTurns and MaxToolRounds behavior tests to require typed exhaustion and preserved partial output. |
| `agent/internal/jobstore/record.go` | Add terminal status `exhausted` and durable exhaustion metadata. |
| `agent/internal/jobstore/event.go` | Carry exhaustion metadata on `job_finished`. |
| `agent/internal/jobstore/fold.go` | Fold exhaustion metadata under existing first-terminal-wins semantics. |
| `agent/internal/jobstore/record_test.go` | Prove `exhausted` is terminal. |
| `agent/internal/jobstore/fold_test.go` | Prove exhaustion metadata folds durably and cannot be overwritten by a later terminal event. |
| `agent/subagents.go` | Add `SubagentExhausted`, classify typed budget errors, and keep the existing `subagentResult.Success` false. |
| `agent/subagent_manager.go` | Treat exhausted subagent runs as terminal retained results. |
| `agent/jobs.go` | Carry exhaustion metadata through job state and latch failed fallback after an exhausted terminal append failure. |
| `agent/job_delegate.go` | Resolve delegate exhaustion, force lifetime non-resumability, preserve tool-round resumability, and reject lifetime resume. |
| `agent/job_delegate_budget_test.go` | End-to-end delegate lifetime/tool-round exhaustion, evidence retention, resumability, and persistence-failure contracts. |
| `agent/job_notify.go` | Format exhausted parent notifications with budget, limit, and resumability. |
| `agent/job_notify_test.go` | Verify exhausted notification content and pending-notification recovery/deduplication. |
| `agent/internal/tool/definitions.go` | Admit `exhausted` in the `job_list` status filter schema. |
| `agent/session_tools_jobs.go` | Project exhaustion metadata through `job_status`, `job_list`, `delegate`, and the actual `delegateSendResult` tool-state wire shape. |
| `agent/session_tools_jobs_test.go` | Prove tool surfaces and `marshalDelegateSendResult` agree on exhausted state and metadata. |
| `agent/events/payloads.go` | Add exhaustion metadata to `JobFinishedData`. |
| `agent/events/payloads_test.go` | New tests locking the structured job-finished exhaustion payload. |
| `agent/status.go` | Carry exhaustion through `agent.JobStatusInfo` diagnostic snapshots. |
| `agent/status_test.go` | Prove durable exhausted records project into agent diagnostics. |
| `appwire/types.go` | Add exhaustion fields to `SerfJobInfo`. |
| `appwire/types_test.go` | Lock the AppWire exhaustion field names and optional encoding. |
| `internal/appprojector/appwire_projection.go` | Project `events.JobFinishedData` exhaustion fields into live AppWire job-finished notifications. |
| `internal/appprojector/appwire_projection_test.go` | Prove the live job-finished event projection preserves exhaustion facts. |
| `server/server.go` | Carry exhaustion through `server.JobStatusInfo`. |
| `cmd/serf/serve.go` | Convert `agent.JobStatusInfo` exhaustion fields into `server.JobStatusInfo`. |
| `cmd/serf/serve_test.go` | Prove the agent-to-server diagnostics conversion preserves exhaustion facts. |
| `server/appwire_runtime.go` | Convert `server.JobStatusInfo` exhaustion fields into diagnostic `SerfJobInfo`. |
| `server/appwire_runtime_test.go` | Prove the server-to-AppWire diagnostic projection preserves exhaustion facts. |
| `cmd/serf-hub/app_threadread.go` | Treat `exhausted` as terminal in historical Hub jobs. |
| `cmd/serf-hub/app_threadread_test.go` | Prove an exhausted historical delegate is terminal and not shown as running. |
| `cmd/serf-hub/assets/renderer-format.js` | Render exhausted notifications as a non-success warning/error tone. |
| `cmd/serf-hub/jstest/test-renderer-notifications.js` | Prove exhausted notifications are not assigned success or neutral tone. |
| `cmd/serf-hub/assets/renderer.js` | Classify live exhausted subagent rails as terminal non-success rows. |
| `cmd/serf-hub/jstest/test-subagents.js` | Prove live exhausted rails stop spinning and never use success styling. |
| `cmd/serf-tui/hub_notifications.go` | Treat exhausted delegate runs as stopped. |
| `cmd/serf-tui/hub_notifications_test.go` | Prove exhausted runs do not remain live in TUI state. |
| `cmd/serf-tui/internal/transcript/reducer.go` | Treat exhausted transcript subagents as terminal. |
| `cmd/serf-tui/internal/transcript/reducer_test.go` | Prove exhausted transcript jobs are terminal. |
| `cmd/serf-tui/internal/msgrender/tool_bodies.go` | Render exhausted delegate rails as non-success. |
| `cmd/serf-tui/internal/msgrender/tool_bodies_test.go` | Prove exhausted rails cannot use completed styling. |

## Task 1: Make Session Budget Accounting Explicit and Restorable

**Files:**

- Create: `agent/session_budget.go`
- Create: `agent/session_budget_test.go`
- Modify: `agent/session.go`
- Modify: `agent/schema/snapshot.go`
- Modify: `agent/session_state.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_dod_definition_test.go`
- Test: `agent/session_budget_test.go`
- Test: `agent/session_dod_definition_test.go`

- [ ] **Step 1: Re-read the test policy and verify the worktree boundary**

Run:

```bash
sed -n '1,240p' docs/testing.md
git status --short
git branch --show-current
```

Expected: testing guidance requires scripted providers for default Serf plumbing tests; the implementation branch is a WIP branch; unrelated user changes remain untouched.

- [ ] **Step 2: Add failing warning and goal-routing tests**

Create `agent/session_budget_test.go` with same-package behavioral tests using `agent/internal/agenttest.ScriptedAdapter` and the existing `communicateResponse` helper:

```go
func TestSession_TurnBudgetWarningAtFiveRemainingOnceAndDoesNotConsumeTurn(t *testing.T)
func TestSession_TurnBudgetWarningAfterThresholdOnNextAcceptedTurn(t *testing.T)
func TestSession_TurnBudgetWarningRestoresWithoutDuplication(t *testing.T)
func TestSession_TurnBudgetWarningRootAndChildWording(t *testing.T)
func TestSession_UnlimitedNeverWarns(t *testing.T)
func TestSession_BudgetExhaustionLeavesActiveGoalUnchanged(t *testing.T)
func TestSession_GoalRoundCapExitIsNotToolBudgetExhaustion(t *testing.T)
```

Use these exact warning constants as the assertions' public text contract:

```go
const childTurnBudgetWarning = "You have 5 turns remaining in this session. Report your current status and evidence to your parent soon, and ask for direction if the task cannot be completed safely within the remaining budget."

const rootTurnBudgetWarning = "You have 5 turns remaining in this session. Report your current status and evidence soon, and ask for direction if the task cannot be completed safely within the remaining budget."
```

The warning tests must establish these observable contracts:

```go
func budgetHistory(sess *Session) []schema.Turn {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return append([]schema.Turn(nil), sess.history...)
}

func countBudgetSteering(history []schema.Turn, text string) int {
	count := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == text {
			count++
		}
	}
	return count
}

if got := countBudgetSteering(budgetHistory(sess), childTurnBudgetWarning); got != 1 {
	t.Fatalf("child warning count = %d, want 1", got)
}
if got := sess.turns; got != acceptedBefore+1 {
	t.Fatalf("accepted turns = %d, want %d; warning consumed a turn", got, acceptedBefore+1)
}
```

The exact-threshold test must set `MaxTurns: 7` with two already accepted inputs, accept the third input, and assert the first model request for that input contains the warning as a user-role steering message. The warning does not consume a turn. The after-threshold test must restore/configure the same limit with four accepted inputs and no latch, then assert the next accepted input emits the warning once.

For the restore test, close the first session after the warning is in its transcript, restore it from `SessionMeta`, accept another input, then assert the restored history still contains exactly one exact warning. Set the metadata warning flag false before restore so the test proves the transcript derivation closes the transcript/meta crash window.

For `TestSession_BudgetExhaustionLeavesActiveGoalUnchanged`, use subtests for lifetime turns and tool rounds. Seed the goal deterministically with `sess.getOrCreateGoalStore().Set("ship it", sess.sclock().Now())`, capture its snapshot, drive the real `processInputKindWithProvenance` path to the corresponding typed budget error, and assert status, iteration count, no-progress streak, and stop reason are unchanged. Also retain the existing `TestGoalErrorBlockIsPersisted` contract to prove an ordinary provider/system error still blocks an active goal.

For `TestSession_GoalRoundCapExitIsNotToolBudgetExhaustion`, use table cases `MaxToolRoundsPerInput: -1` and `MaxToolRoundsPerInput: 200`. Drive `processOneInput` with `EntryContinuation` and a deterministic scripted adapter that returns another tool call for all `goal.GoalTurnMaxRounds` rounds. Assert the adapter receives exactly `goal.GoalTurnMaxRounds` requests, `err == nil`, partial text remains returned, and the goal snapshot remains active. Retain `TestRoundCapSelection` in `agent/session_goal_gate_test.go` to pin the existing cap selection itself.

- [ ] **Step 3: Run the warning/goal tests and confirm the intended red state**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_(TurnBudgetWarning|UnlimitedNeverWarns|BudgetExhaustionLeavesActiveGoalUnchanged|GoalRoundCapExitIsNotToolBudgetExhaustion)' -count=1 -v
```

Expected: FAIL because the warning constants, persisted accepted-turn count, warning latch, typed budget errors, threshold injection, budget-specific goal-error routing, and binding-cap distinction do not exist.

- [ ] **Step 4: Add one typed representation for both exhaustion sources**

Create `agent/session_budget.go` with these domain types and exact stable values:

```go
package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/serf/agent/schema"
)

type exhaustedBudget string

const (
	exhaustedBudgetTurns     exhaustedBudget = "max_turns"
	exhaustedBudgetToolRounds exhaustedBudget = "max_tool_rounds_per_input"
)

const childTurnBudgetWarning = "You have 5 turns remaining in this session. Report your current status and evidence to your parent soon, and ask for direction if the task cannot be completed safely within the remaining budget."

const rootTurnBudgetWarning = "You have 5 turns remaining in this session. Report your current status and evidence soon, and ask for direction if the task cannot be completed safely within the remaining budget."

type budgetExhaustionError struct {
	Budget    exhaustedBudget
	Limit     int
	Resumable bool
}

func (e *budgetExhaustionError) Error() string {
	return fmt.Sprintf("%s exhausted at limit %d", e.Budget, e.Limit)
}

func (e *budgetExhaustionError) reason() string {
	if e.Budget == exhaustedBudgetTurns {
		return "turn_budget_exhausted"
	}
	return "tool_round_budget_exhausted"
}

func budgetExhaustionFromError(err error) (*budgetExhaustionError, bool) {
	var exhausted *budgetExhaustionError
	if !errors.As(err, &exhausted) {
		return nil, false
	}
	return exhausted, true
}

func turnBudgetWarningInHistory(history []schema.Turn) bool {
	for _, turn := range history {
		if turn.Kind != schema.TurnSteering {
			continue
		}
		text := turn.Message.Text()
		if text == childTurnBudgetWarning || text == rootTurnBudgetWarning {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Persist and restore the accepted-input count and warning latch**

Keep the existing accepted-input counter in `Session` and add the warning latch beside it in `agent/session.go`:

```go
turns                    int
turnBudgetWarningEmitted bool
```

Add the fields to `schema.SessionMeta` in `agent/schema/snapshot.go`:

```go
AcceptedInputTurns       int  `json:"accepted_input_turns,omitempty"`
TurnBudgetWarningEmitted bool `json:"turn_budget_warning_emitted,omitempty"`
```

Keep the existing `TurnCount` field mapped to model responses. In `Session.Meta` in `agent/session_state.go`, map the new fields independently:

```go
AcceptedInputTurns:       s.turns,
TurnBudgetWarningEmitted: s.turnBudgetWarningEmitted,
```

Correct the existing `TurnCount` comment in `agent/schema/snapshot.go` to `number of model responses processed` so it no longer claims to be the accepted-input counter.

In `agent/session_init.go`, restore them when constructing `Session`:

```go
turns:                    meta.AcceptedInputTurns,
turnBudgetWarningEmitted: meta.TurnBudgetWarningEmitted || turnBudgetWarningInHistory(resumeHistory),
```

Do not infer accepted input turns from model-response count or legacy transcript shape.

- [ ] **Step 6: Inject the warning through the normal steering path**

Add a `Session` helper in `agent/session_budget.go`:

```go
func (s *Session) queueTurnBudgetWarning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.MaxTurns <= 0 || s.turnBudgetWarningEmitted {
		return
	}
	remaining := s.cfg.MaxTurns - s.turns
	if remaining > 5 {
		return
	}
	text := rootTurnBudgetWarning
	if s.cfg.spawn.parentSessionID != "" || s.restoredMetaIsSubagent {
		text = childTurnBudgetWarning
	}
	s.steeringQueue = append(s.steeringQueue, steeringMessage{Text: text})
	s.turnBudgetWarningEmitted = true
}
```

Call this helper in `acceptUserInput` after the lifetime-limit rejection check and before incrementing `s.turns`, appending the user turn, running hooks, and draining steering. The existing drain must create `schema.TurnSteering` and `events.EventSteeringInjected`; do not append a special transcript record or consume a turn. The `remaining > 5` comparison intentionally warns on the next accepted turn when a restored/configured session is already past the threshold.

- [ ] **Step 7: Return typed errors from both budget exits**

Change `acceptUserInput` from a boolean result to an error result. On lifetime exhaustion, preserve the existing `TURN_LIMIT` event and idle transition, then return:

```go
return &budgetExhaustionError{
	Budget:    exhaustedBudgetTurns,
	Limit:     s.cfg.MaxTurns,
	Resumable: false,
}
```

Update `processOneInput` to propagate that non-nil error instead of returning a successful nil error.

Keep the existing `roundCap := goalRoundCap(s.cfg.MaxToolRoundsPerInput, kind)` selection. At the loop exit, preserve the last assistant text, `progressed` value, existing `TURN_LIMIT` event, and idle transition. Return typed tool-round exhaustion only when the configured tool-round budget was the cap that ended the loop:

```go
if roundCap != s.cfg.MaxToolRoundsPerInput {
	return lastText, progressed, nil
}
return lastText, progressed, &budgetExhaustionError{
	Budget:    exhaustedBudgetToolRounds,
	Limit:     s.cfg.MaxToolRoundsPerInput,
	Resumable: true,
}
```

For `EntryContinuation` with configured `-1` or any value greater than `goal.GoalTurnMaxRounds`, `goalRoundCap` returns `goal.GoalTurnMaxRounds`, so the comparison is unequal and the established autonomous goal-cap exit remains a nil-error result. For user inputs and continuation configurations at or below `goal.GoalTurnMaxRounds`, the configured tool cap is binding and returns the typed exhaustion error. Do not change `goalRoundCap`, `goal.GoalTurnMaxRounds`, goal iteration accounting, or goal continuation policy.

In the error arm of `processInputKindWithProvenance`, classify this expected terminal outcome before the existing generic goal-error route:

```go
if _, exhausted := budgetExhaustionFromError(err); !exhausted {
	s.terminateGoalOnError(processCtx, err)
}
s.finishProcessingAtBoundary(processCtx, SessionIdle)
return strings.Join(outputs, "\n"), err
```

Do not change `terminateGoalOnError`, the goal gate, continuation accounting, or goal limits. The typed budget error still returns to the caller; it simply does not convert an unrelated active goal to blocked.

- [ ] **Step 8: Update existing limit tests to require typed exhaustion and partial output**

In `agent/session_dod_definition_test.go`, change the existing MaxTurns and MaxToolRounds tests to use `errors.As` and exact metadata checks:

```go
var exhausted *budgetExhaustionError
if !errors.As(err, &exhausted) {
	t.Fatalf("ProcessInput error = %v, want budgetExhaustionError", err)
}
if exhausted.Budget != exhaustedBudgetTurns || exhausted.Limit != maxTurns || exhausted.Resumable {
	t.Fatalf("lifetime exhaustion = %+v, want max_turns/%d/non-resumable", exhausted, maxTurns)
}
```

For the tool-round case, assert the returned text equals the last assistant text emitted before the limit and the error has budget `max_tool_rounds_per_input`, the configured limit, and `Resumable == true`.

- [ ] **Step 9: Run the focused Session tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'Test(Session_(TurnBudgetWarning|UnlimitedNeverWarns|MaxTurns|MaxToolRounds|BudgetExhaustionLeavesActiveGoalUnchanged|GoalRoundCapExitIsNotToolBudgetExhaustion)|GoalErrorBlockIsPersisted|RoundCapSelection)' -count=1 -v
```

Expected: PASS. The warning appears exactly once, survives restore without duplication, does not increment accepted turns, and uses child-only parent wording. Binding lifetime/tool-round budgets return typed errors without changing active-goal state, while `-1` and over-goal-cap continuation configurations stop at `GoalTurnMaxRounds` with the established nil error. The ordinary goal-error regression still blocks and persists exactly as before.

- [ ] **Step 10: Commit the Session budget foundation**

Run:

```bash
git status --short
git add agent/session_budget.go agent/session_budget_test.go agent/session.go agent/schema/snapshot.go agent/session_state.go agent/session_init.go agent/session_lifecycle.go agent/session_dod_definition_test.go
git commit -m "feat(agent): make session budget exhaustion explicit" -m "Persist accepted input turns and the once-only five-turn warning latch. Emit the warning through normal steering and return typed errors for lifetime and tool-round exhaustion while retaining partial output."
```

Expected: one focused commit; unrelated worktree files remain unstaged.

## Task 2: Add Durable Exhausted Job State

**Files:**

- Modify: `agent/internal/jobstore/record.go`
- Modify: `agent/internal/jobstore/event.go`
- Modify: `agent/internal/jobstore/fold.go`
- Modify: `agent/internal/jobstore/record_test.go`
- Modify: `agent/internal/jobstore/fold_test.go`

- [ ] **Step 1: Add failing job-store terminal-state tests**

Add these tests:

```go
func TestStatusIsTerminal_Exhausted(t *testing.T) {
	if !StatusExhausted.IsTerminal() {
		t.Fatal("exhausted status is not terminal")
	}
}

func TestFold_ExhaustedPreservesBudgetMetadataAndFirstTerminalWins(t *testing.T)
```

The fold test must append `job_started`, then one exhausted `job_finished` with budget `max_turns`, limit `500`, resumable false, and a terminal generation. Fold the events and assert all fields. Append a later completed `job_finished`, fold again, and assert the first exhausted terminal state and metadata remain unchanged.

- [ ] **Step 2: Run the store tests and confirm the intended red state**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/jobstore -run 'Test(StatusIsTerminal_Exhausted|Fold_Exhausted)' -count=1 -v
```

Expected: FAIL because `StatusExhausted` and the exhaustion fields do not exist.

- [ ] **Step 3: Add the exhausted status and consistent metadata fields**

In `agent/internal/jobstore/record.go`, add:

```go
StatusExhausted Status = "exhausted"
```

Include it in `Status.IsTerminal`:

```go
case StatusCompleted, StatusFailed, StatusCancelled, StatusStopped, StatusExhausted:
	return true
```

Add these fields to `JobRecord` beside the other terminal fields:

```go
ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
```

Use the existing `JobRecord.Resumable *bool` for the exhausted terminal's resumability fact; do not add a second resumability field.

In `agent/internal/jobstore/event.go`, add the same names to the `job_finished` payload:

```go
ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
```

- [ ] **Step 4: Fold exhaustion metadata with the first terminal event**

In the `EventJobFinished` arm in `agent/internal/jobstore/fold.go`, copy `ExhaustionBudget`, `ExhaustionLimit`, and the event's `Resumable` pointer only when accepting the first terminal event. Keep later terminal events from overwriting the status, reason, generation, metadata, or resumability.

- [ ] **Step 5: Run all job-store tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/jobstore -count=1
```

Expected: PASS, including the new exhausted first-terminal-wins contract and all prior completed/failed/cancelled/stopped cases.

- [ ] **Step 6: Commit the durable status**

Run:

```bash
git status --short
git add agent/internal/jobstore/record.go agent/internal/jobstore/event.go agent/internal/jobstore/fold.go agent/internal/jobstore/record_test.go agent/internal/jobstore/fold_test.go
git commit -m "feat(jobstore): persist exhausted terminal jobs" -m "Add an exhausted terminal status plus budget and limit metadata to job_finished folding. Preserve existing first-terminal-wins behavior so recovery cannot rewrite an exhausted job as completed."
```

Expected: one job-store commit with no unrelated paths staged.

## Task 3: Translate Delegate Budget Errors into Truthful Terminal Outcomes

**Files:**

- Modify: `agent/subagents.go`
- Modify: `agent/subagent_manager.go`
- Modify: `agent/jobs.go`
- Modify: `agent/job_delegate.go`
- Create: `agent/job_delegate_budget_test.go`
- Modify: `agent/job_delegate_finalize_test.go`
- Modify: `agent/subagents_test.go`

- [ ] **Step 1: Add failing delegate behavior tests with scripted child sessions**

Create `agent/job_delegate_budget_test.go` with these tests:

```go
func TestDelegate_LifetimeBudgetExhaustionIsDurableAndNotResumable(t *testing.T)
func TestDelegate_ToolRoundBudgetExhaustionIsDurableAndResumable(t *testing.T)
func TestDelegate_BudgetExhaustionPreservesPartialOutputAndTranscript(t *testing.T)
func TestDelegate_ExhaustedFinishPersistFailureStaysFailedAcrossRetries(t *testing.T)
func TestDelegate_BudgetTruthfulnessLeavesExistingTerminalStatusesUnchanged(t *testing.T)
```

Build the parent and child with the existing scripted-provider helpers and real delegate/job plumbing. Do not call a live provider. The lifetime test must prove this record shape after the child attempts an input beyond its positive `MaxTurns`:

```go
if rec.Status != jobstore.StatusExhausted || rec.Reason != "turn_budget_exhausted" {
	t.Fatalf("terminal state = %s/%q, want exhausted/turn_budget_exhausted", rec.Status, rec.Reason)
}
if rec.ExhaustionBudget != string(exhaustedBudgetTurns) || rec.ExhaustionLimit != child.cfg.MaxTurns {
	t.Fatalf("exhaustion metadata = %q/%d", rec.ExhaustionBudget, rec.ExhaustionLimit)
}
if rec.Resumable == nil || *rec.Resumable {
	t.Fatalf("lifetime resumable = %v, want false", rec.Resumable)
}
```

Then call `delegate_send` with `on_idle:"start"` and assert it rejects the delegate with reason `turn_budget_exhausted` without starting a child request.

The tool-round test must configure a scripted response containing assistant text plus a tool call at `MaxToolRoundsPerInput == 1`. Assert the current job becomes exhausted with `Resumable == true`, then use `delegate_send` with `on_idle:"start"` and prove a new job is accepted for the same delegate.

The evidence test must inspect job output and child transcript records to prove the last assistant text, tool result/evidence, and transcript reference remain readable after the exhausted finish.

For the persistence-failure test, wrap the existing `jobManager.appendEvent` seam and reject three consecutive `EventJobFinished` appends while recording the attempted statuses:

```go
origAppend := parent.jobManager.appendEvent
failuresRemaining := 3
var attempted []jobstore.Status
parent.jobManager.appendEvent = func(event jobstore.Event) error {
	if event.Kind == jobstore.EventJobFinished {
		attempted = append(attempted, event.Status)
		if failuresRemaining > 0 {
			failuresRemaining--
			return errors.New("injected job_finished append failure")
		}
	}
	return origAppend(event)
}
```

Assert the attempted status sequence is exactly `[]jobstore.Status{StatusExhausted, StatusFailed, StatusFailed, StatusFailed}`. The eventual durable record and sole terminal notification must be failed with reason `exhausted_persist_failed`; neither exhausted nor completed may be delivered. This proves the failed fallback survives the existing indefinite finalization retry loop.

The existing-status regression test must table-drive `resolveDelegateTerminalStatus` with nil exhaustion and pin completed, failed, cancelled, and explicit parent-stopped outcomes to their current status/reason values.

- [ ] **Step 2: Run the delegate tests and confirm the intended red state**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestDelegate_(LifetimeBudget|ToolRoundBudget|BudgetExhaustion|ExhaustedFinish|BudgetTruthfulness)' -count=1 -v
```

Expected: FAIL because subagents currently translate nil/error exits only into completed/failed/cancelled and job finalization has no exhausted status or metadata.

- [ ] **Step 3: Add `SubagentExhausted` and classify typed errors before generic failures**

In `agent/subagents.go`, add:

```go
SubagentExhausted SubagentStatus = "exhausted"
```

Immediately after the initial `processInputKindWithProvenance` call, detect whether the result is budget exhaustion. A budget-exhausted run must not enter the communicate nudge or a blocking SubagentStop continuation, because either path could spend another input and replace the exhausted outcome:

```go
_, budgetExhausted := budgetExhaustionFromError(err)
```

Retain the ordinary SubagentStop hook path for non-budget outcomes; do not add plugin-specific or Superpowers-specific behavior. In the subagent run completion switch, recognize `budgetExhaustionError` before the generic non-nil error arm:

```go
if exhausted, ok := budgetExhaustionFromError(err); ok {
	a.status = SubagentExhausted
	a.err = exhausted
} else if err != nil {
	a.status = SubagentFailed
	a.err = err
} else {
	a.status = SubagentCompleted
}
```

Leave the existing `subagentResult.Success` calculation exact:

```go
Success: a.status == SubagentCompleted,
```

Extend status tests in `agent/subagents_test.go` to assert exhausted is terminal and `subagentResult.Success == false`.

In `agent/subagent_manager.go`, include `SubagentExhausted` in `terminalStatus` so retention/eviction accounts for it exactly like other completed run outcomes:

```go
case SubagentCompleted, SubagentFailed, SubagentCancelled, SubagentExhausted:
	return true
```

- [ ] **Step 4: Carry exhaustion through running and terminal job state**

In `agent/jobs.go`, add the typed exhaustion to `runningJob`:

```go
exhaustion                     *budgetExhaustionError
delegateExhaustionPersistFailed bool
```

Add consistent primitive fields to `terminalJob`:

```go
exhaustionBudget string
exhaustionLimit  int
resumable        *bool
```

When `writeFinishJob` builds `jobstore.Event{Kind: EventJobFinished}`, populate `ExhaustionBudget`, `ExhaustionLimit`, and `Resumable` from the running job. Copy the same values into the in-memory record and `terminalJob`. Keep the current order: append `job_finished`, publish/forward the durable event, then arm the notification.

- [ ] **Step 5: Resolve status and resumability from the typed error**

In `agent/job_delegate.go`, add the exact lifetime stop reason:

```go
const notResumableTurnBudgetExhausted = "turn_budget_exhausted"
```

Change `delegateTerminalStatus` and `resolveDelegateTerminalStatus` to accept the exhaustion pointer. Preserve the existing explicit parent-stop precedence, then add this arm before switching on child status:

```go
if stopStatus != "" {
	return stopStatus, stopReason
}
if exhausted != nil {
	return jobstore.StatusExhausted, exhausted.reason()
}
```

Add exhausted mappings to `subagentStatusFromJobStatus` and every exhaustive delegate-status switch:

```go
case jobstore.StatusExhausted:
	return SubagentExhausted
```

In `finalizeDelegateOnce`, copy `sub.err` into `subErr` while holding `sub.mu`, alongside the existing status/prose snapshot. Under `jobManager.mu`, assign its typed exhaustion to `run.exhaustion` only while `run.delegateExhaustionPersistFailed` is false; once the latch is set, clear `run.exhaustion` and return failed/`exhausted_persist_failed` from every later prepare attempt. Preserve `sub.result` in the same `delegateOutputBytes` append path used by successful/failed delegates.

```go
exhaustion, _ := budgetExhaustionFromError(subErr)
jm.mu.Lock()
persistFailed := run.delegateExhaustionPersistFailed
if persistFailed {
	run.exhaustion = nil
} else {
	run.exhaustion = exhaustion
}
jm.mu.Unlock()
if persistFailed {
	return jobstore.StatusFailed, "exhausted_persist_failed", nil, nil
}
```

For lifetime exhaustion, persist the delegate record with:

```go
delegateResumability{
	Resumable: false,
	Reason:    notResumableTurnBudgetExhausted,
}
```

For tool-round exhaustion, run the existing resumability assessment and persist its successful `Resumable: true` result. Do not close the child session or its delegate handle merely because the current job exhausted its tool-round budget.

Extend `notResumableSendError` with an exact lifetime exhaustion error that names `turn_budget_exhausted`, so the preflight path refuses a lifetime-exhausted delegate before attempting restore.

- [ ] **Step 6: Make terminal-persistence failure settle as failed**

Add an internal wrapper in `agent/jobs.go` and use it only for an `EventJobFinished` append failure:

```go
type terminalRecordPersistError struct {
	err error
}

func (e *terminalRecordPersistError) Error() string {
	return "persist terminal job record: " + e.err.Error()
}

func (e *terminalRecordPersistError) Unwrap() error {
	return e.err
}
```

Wrap only the `jm.appendEvent(finished)` error in `writeFinishJob`; forwarding, emission, and notification errors keep their existing types:

```go
if err := jm.appendEvent(finished); err != nil {
	return nil, &terminalRecordPersistError{err: err}
}
```

Add this helper in `agent/job_delegate.go`:

```go
func latchDelegateExhaustionPersistFailure(jm *jobManager, jobID string, err error) {
	var persistErr *terminalRecordPersistError
	if !errors.As(err, &persistErr) {
		return
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.exhaustion == nil {
		return
	}
	run.delegateExhaustionPersistFailed = true
	run.exhaustion = nil
}
```

Call it in the outer `finalizeDelegateWithNotification` retry loop immediately after a failed `finalizeDelegateOnce` and before the stop-retry check/sleep. If the running job attempted an exhausted finish, it sets this latch under `jobManager.mu` before retrying:

```go
latchDelegateExhaustionPersistFailure(jm, jobID, err)
```

On every subsequent pass through `prepare`, read the latch and return:

```go
if persistFailed {
	return jobstore.StatusFailed, "exhausted_persist_failed", nil, nil
}
```

Never clear the latch while the job remains in `jobManager.running`. Do not set it for failures after the exhausted event is durable, such as forwarding or pending-notification append failure. Those paths must reuse the already-persisted exhausted terminal generation and existing retry/deduplication behavior.

- [ ] **Step 7: Run focused delegate and finalization tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'Test(Delegate_(LifetimeBudget|ToolRoundBudget|BudgetExhaustion|ExhaustedFinish|BudgetTruthfulness)|FinalizeDelegate|Subagent.*Exhausted)' -count=1 -v
```

Expected: PASS. Lifetime exhaustion is durable/non-resumable, tool-round exhaustion is durable/resumable, both retain evidence, and the first exhausted terminal append failure permanently latches every retry to failed.

- [ ] **Step 8: Commit the delegate translation**

Run:

```bash
git status --short
git add agent/subagents.go agent/subagent_manager.go agent/jobs.go agent/job_delegate.go agent/job_delegate_budget_test.go agent/job_delegate_finalize_test.go agent/subagents_test.go
git commit -m "feat(agent): surface exhausted delegate outcomes" -m "Translate typed session budget errors into durable exhausted jobs. Preserve partial delegate evidence, force lifetime exhaustion non-resumable, keep tool-round exhaustion resumable, and fail closed when terminal persistence fails."
```

Expected: one delegate-lifecycle commit with no unrelated paths staged.

## Task 4: Expose Exhaustion Through Tools, Notifications, and Transcript Events

**Files:**

- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/session_tools_jobs_test.go`
- Modify: `agent/job_notify.go`
- Modify: `agent/job_notify_test.go`
- Modify: `agent/jobs.go`
- Modify: `agent/events/payloads.go`
- Create: `agent/events/payloads_test.go`

- [ ] **Step 1: Add failing cross-surface agreement tests**

Add these tests:

```go
func TestJobTools_ExhaustedStateAgreesAcrossStatusListAndDelegate(t *testing.T)
func TestMarshalDelegateSendResult_ExhaustionMetadataInToolState(t *testing.T)
func TestJobNotification_ExhaustedNamesBudgetLimitAndResumability(t *testing.T)
func TestJobNotification_ExhaustedPendingReplayIsDeduplicated(t *testing.T)
func TestJobFinishedData_ExhaustionMetadata(t *testing.T)
```

Seed or run one durable exhausted delegate job and assert `job_status`, `job_list`, and the delegate result all agree on these exact values:

```go
Status:           "exhausted"
ExhaustionBudget: "max_turns"
ExhaustionLimit:  500
Resumable:        false
```

For `TestMarshalDelegateSendResult_ExhaustionMetadataInToolState`, pass a terminal `sendMessageResult` through the real `marshalDelegateSendResult`, type-assert the returned value to `tool.StateResult`, type-assert `State` to `delegateSendResult`, and JSON-marshal that state. Assert both the typed state and JSON contain `status:"exhausted"`, `exhaustion_budget:"max_tool_rounds_per_input"`, `exhaustion_limit:1`, and `resumable:true`. This is the `delegate_send` state that becomes `TOOL_CALL_END.tool_state`; do not test only the internal `sendMessageResult`.

The notification test must render the actual block and assert these quoted attributes are present:

```go
block := formatJobNotificationBlock(notification, notificationExcerpt{})
for _, want := range []string{
	`status="exhausted"`,
	`budget="max_turns"`,
	`limit="500"`,
	`resumable="false"`,
} {
	if !strings.Contains(block, want) {
		t.Fatalf("notification %q missing %s", block, want)
	}
}
```

The recovery test must persist an exhausted `job_finished` and `job_notification_pending`, reopen/reconcile the job manager, and assert exactly one exhausted parent notification with the original terminal generation. Also assert no child/model request is rerun during recovery.

- [ ] **Step 2: Run the surface tests and confirm the intended red state**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent ./agent/events -run 'Test(JobTools_Exhausted|MarshalDelegateSendResult_Exhaustion|JobNotification_Exhausted|JobFinishedData_Exhaustion)' -count=1 -v
```

Expected: FAIL because the projections, notification attributes, schema enum, and event payload do not admit exhaustion.

- [ ] **Step 3: Extend tool schemas and typed results**

In `agent/internal/tool/definitions.go`, add `exhausted` to the existing `job_list` status enum and description. Do not change defaults or meanings of other filters.

In `agent/session_tools_jobs.go`, add these fields to `jobStatusResult`, `jobListEntry`, and `delegateToolResult`:

```go
ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
Resumable        *bool  `json:"resumable,omitempty"`
```

`jobListEntry` already has `Resumable *bool`; retain that field and add only the two exhaustion fields there. Add all three fields to `jobStatusResult` and `delegateToolResult`. Accept `jobstore.StatusExhausted` in the status filter. Update every projection constructor to copy `JobRecord.ExhaustionBudget`, `JobRecord.ExhaustionLimit`, and `JobRecord.Resumable` without recomputing them.

Add the same exhaustion fields plus `Resumable *bool` to `delegateResult` and `sendMessageResult` in `agent/job_delegate.go`. Populate `delegateResult` in `delegateTerminalResult` from the durable record, then copy all three fields in `sendMessageResultFromDelegateResult`; this is the foreground/resumed `delegate_send` terminal path.

Add the fields to the actual `delegate_send` wire shape in `agent/session_tools_jobs.go`:

```go
type delegateSendResult struct {
	ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
	Resumable        *bool  `json:"resumable,omitempty"`
}
```

These are additions to the existing struct, not a replacement definition. In `marshalDelegateSendResult`, copy all three fields from `sendMessageResult` into `out`:

```go
ExhaustionBudget: res.ExhaustionBudget,
ExhaustionLimit:  res.ExhaustionLimit,
Resumable:        res.Resumable,
```

Return the existing `tool.StateResult{Output: formatDelegateSend(out), State: out}` so the fields ride the structured `TOOL_CALL_END.tool_state`. Keep the wire types limited to their existing status/reason contract plus the three exhaustion fields; do not add another outcome boolean.

- [ ] **Step 4: Extend parent notifications without changing delivery order**

Add these fields to `jobNotification` in `agent/jobs.go`:

```go
ExhaustionBudget string
ExhaustionLimit  int
Resumable        *bool
```

Copy them when creating, persisting, recovering, and enqueueing terminal notifications. `formatJobNotificationBlock` builds `attrs` as `[]string`; append exhausted-only quoted key/value strings to that slice:

```go
if n.Status == string(jobstore.StatusExhausted) {
	attrs = append(attrs,
		fmt.Sprintf("budget=%q", n.ExhaustionBudget),
		fmt.Sprintf("limit=%q", strconv.Itoa(n.ExhaustionLimit)),
		fmt.Sprintf("resumable=%q", strconv.FormatBool(n.Resumable != nil && *n.Resumable)),
	)
}
```

Keep the status attribute `exhausted` and the existing transcript/job identifiers. Do not flatten this into prose-only notification text.

- [ ] **Step 5: Extend structured job-finished events**

Add to `events.JobFinishedData` in `agent/events/payloads.go`:

```go
ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
Resumable        *bool  `json:"resumable,omitempty"`
```

Update `emitJobFinished` in `agent/jobs.go` to copy those values from the durable terminal state. This existing event is the transcript-lifecycle surface; do not create a parallel exhaustion event kind.

- [ ] **Step 6: Run focused tools, notification, and event tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent ./agent/events -run 'Test(JobTools_Exhausted|MarshalDelegateSendResult_Exhaustion|JobNotification_Exhausted|JobFinishedData_Exhaustion)' -count=1 -v
```

Expected: PASS, including one-shot pending notification replay and identical status/budget/limit/resumability across tools, `delegate_send` tool state, notification, and transcript event.

- [ ] **Step 7: Run the complete agent package regression suite**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/... -count=1
```

Expected: PASS. Existing completed, failed, stopped, cancelled, unlimited-turn, goal-continuation, and provider-retry contracts remain unchanged.

- [ ] **Step 8: Commit the parent-visible contract**

Run:

```bash
git status --short
git add agent/internal/tool/definitions.go agent/session_tools_jobs.go agent/session_tools_jobs_test.go agent/job_delegate.go agent/job_notify.go agent/job_notify_test.go agent/jobs.go agent/events/payloads.go agent/events/payloads_test.go
git commit -m "feat(agent): report delegate exhaustion consistently" -m "Expose exhausted status, budget, limit, and resumability through job tools, delegate results, durable notifications, and job-finished transcript events. Reuse terminal generations so recovery delivers once without rerunning delegates."
```

Expected: one surface-contract commit with no unrelated paths staged.

## Task 5: Preserve Exhausted State Through AppWire, Hub, and TUI

**Files:**

- Modify: `agent/status.go`
- Modify: `agent/status_test.go`
- Modify: `appwire/types.go`
- Modify: `appwire/types_test.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Modify: `internal/appprojector/appwire_projection_test.go`
- Modify: `server/server.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf/serve_test.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/appwire_runtime_test.go`
- Modify: `cmd/serf-hub/app_threadread.go`
- Modify: `cmd/serf-hub/app_threadread_test.go`
- Modify: `cmd/serf-hub/assets/renderer-format.js`
- Modify: `cmd/serf-hub/jstest/test-renderer-notifications.js`
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/jstest/test-subagents.js`
- Modify: `cmd/serf-tui/hub_notifications.go`
- Create: `cmd/serf-tui/hub_notifications_test.go`
- Modify: `cmd/serf-tui/internal/transcript/reducer.go`
- Modify: `cmd/serf-tui/internal/transcript/reducer_test.go`
- Modify: `cmd/serf-tui/internal/msgrender/tool_bodies.go`
- Modify: `cmd/serf-tui/internal/msgrender/tool_bodies_test.go`

- [ ] **Step 1: Add failing tests for both real AppWire data paths**

Add or extend these tests:

```go
func TestSession_DetailedStatus_JobsIncludesExhaustion(t *testing.T)
func TestAgentToServerDetailedStatus_Exhaustion(t *testing.T)
func TestAppDiagnosticsFromDetailedStatus_Exhaustion(t *testing.T)
func TestProjectJobFinished_ExhaustionMetadata(t *testing.T)
func TestSerfJobInfo_ExhaustionFields(t *testing.T)
```

The live event test in `internal/appprojector/appwire_projection_test.go` must set `resumable := true` and feed `events.JobFinishedData`, not a job-store record:

```go
Data: events.JobFinishedData{
	JobID:            "job_exhausted",
	JobType:          "delegate",
	Status:           "exhausted",
	Reason:           "tool_round_budget_exhausted",
	ExhaustionBudget: "max_tool_rounds_per_input",
	ExhaustionLimit:  1,
	Resumable:        &resumable,
},
```

Assert the projected `appwire.SerfJobInfo` has the same status, budget, limit, and resumability.

The runtime diagnostic tests must cover the actual chain independently:

```text
jobstore.JobRecord
  -> agent.JobStatusInfo
  -> agentToServerDetailedStatus (cmd/serf/serve.go)
  -> server.JobStatusInfo
  -> appDiagnosticsFromDetailedStatus (server/appwire_runtime.go)
  -> appwire.SerfJobInfo
```

In `agent/status_test.go`, seed an exhausted `JobRecord` and assert `Session.DetailedStatus().Jobs`. In `cmd/serf/serve_test.go`, feed an `agent.JobStatusInfo` into `agentToServerDetailedStatus`. In `server/appwire_runtime_test.go`, feed a `server.JobStatusInfo` into `appDiagnosticsFromDetailedStatus`. Do not name or add a nonexistent direct `JobRecord`-to-AppWire projector.

- [ ] **Step 2: Add failing Hub and TUI terminal/rendering tests**

Add these exact behavioral tests:

```go
func TestHistoricalJob_ExhaustedIsTerminal(t *testing.T)
func TestRunStillRunning_Exhausted(t *testing.T)
func TestSubagentTerminalStatus_Exhausted(t *testing.T)
func TestSubagentRailClass_Exhausted(t *testing.T)
```

The assertions must establish:

```go
if runStillRunning("exhausted") {
	t.Fatal("exhausted run reported as still running")
}
if !subagentTerminalStatus("exhausted") {
	t.Fatal("exhausted transcript subagent reported as non-terminal")
}
if got := subagentRailClass("exhausted"); got != "failed" {
	t.Fatalf("exhausted rail class = %q, want failed", got)
}
```

In `cmd/serf-hub/jstest/test-renderer-notifications.js`, add an exhausted notification case and assert its tone is neither success nor neutral.

In `cmd/serf-hub/jstest/test-subagents.js`, add this live rail scenario using a real `JOB_STARTED` followed by `JOB_FINISHED`:

```js
await scenario("exhausted child is terminal non-success without spinner", [
  ["SESSION_START", { session_id: "01TEST" }],
  jobStarted("job_exhausted", "dlg_exhausted", "bounded work", "local:child-exhausted", "d1"),
  ["JOB_FINISHED", {
    jobId: "job_exhausted", jobType: "delegate", status: "exhausted",
    reason: "tool_round_budget_exhausted", delegateId: "dlg_exhausted",
    task: "bounded work", transcriptRef: "local:child-exhausted",
    originToolCallId: "d1", originItemId: "item_d1", outputBytes: 42,
  }],
], ({ conv }) => {
  const row = conv.querySelector('.sub-r[data-job-id="job_exhausted"]');
  if (!row) return { ok: false, detail: "missing exhausted row" };
  const glyph = row.querySelector(".g");
  if (row.dataset.status !== "exhausted") return { ok: false, detail: "raw status was relabeled" };
  if (row.dataset.statusKind === "running") return { ok: false, detail: "exhausted row is still running" };
  if (!glyph || glyph.classList.contains("run") || !glyph.classList.contains("err")) return { ok: false, detail: "exhausted glyph is not terminal non-success" };
  return { ok: true };
});
```

- [ ] **Step 3: Run projection/rendering tests and confirm the intended red state**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent ./appwire ./internal/appprojector ./server ./cmd/serf ./cmd/serf-hub ./cmd/serf-tui/... -run 'Exhaust' -count=1 -v
bash cmd/serf-hub/jstest/run-all.sh
```

Expected: FAIL because both AppWire paths drop exhaustion metadata and the newly added full-suite Hub/TUI cases do not recognize `exhausted` as a terminal non-success state.

- [ ] **Step 4: Add exhaustion metadata to AppWire and server projections**

Add these fields to `appwire.SerfJobInfo` in `appwire/types.go`:

```go
ExhaustionBudget string `json:"exhaustionBudget,omitempty"`
ExhaustionLimit  int    `json:"exhaustionLimit,omitempty"`
Resumable        *bool  `json:"resumable,omitempty"`
```

Add the same Go field names to `agent.JobStatusInfo` in `agent/status.go` and `server.JobStatusInfo` in `server/server.go`, retaining those structs' snake-case diagnostic JSON convention:

```go
ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
Resumable        *bool  `json:"resumable,omitempty"`
```

Update these actual converters, copying fields directly at each boundary:

- `projectJobStatusInfos` in `agent/status.go`: `jobstore.JobRecord -> agent.JobStatusInfo`.
- `agentToServerDetailedStatus` in `cmd/serf/serve.go`: `agent.JobStatusInfo -> server.JobStatusInfo`.
- `appDiagnosticsFromDetailedStatus` in `server/appwire_runtime.go`: `server.JobStatusInfo -> appwire.SerfJobInfo`.
- the `events.EventJobFinished` arm in `internal/appprojector/appwire_projection.go`: `events.JobFinishedData -> appwire.SerfJobInfo`.

The live event path receives the fields from Task 4's `events.JobFinishedData`; the runtime diagnostic path receives them from `agent.JobStatusInfo`. Keep these as two explicit sources rather than inventing a shared direct-record projector.

- [ ] **Step 5: Mark exhaustion terminal in Hub history and non-success in Hub notifications**

In `cmd/serf-hub/app_threadread.go`, add `exhausted` to `isTerminalHistoricalJobStatus` without mapping it to another status. Keep the raw status available to the renderer.

In `cmd/serf-hub/assets/renderer-format.js`, give `attrs.status === "exhausted"` the existing warning/error tone used by unsuccessful terminal notifications. Do not assign the completed/success tone and do not relabel the status.

In `cmd/serf-hub/assets/renderer.js`, update the live rail classifier:

```js
if (s === "failed" || s === "errored" || s === "error" || s === "exhausted") return "failed";
```

This reuses the existing terminal non-success glyph/class, removes the running spinner through the existing `kind !== "running"` path, and preserves `row.dataset.status === "exhausted"`. Do not map exhausted to done/success.

- [ ] **Step 6: Mark exhaustion terminal in TUI state and rendering**

In `cmd/serf-tui/hub_notifications.go`, make `runStillRunning("exhausted")` return false.

In `cmd/serf-tui/internal/transcript/reducer.go`, include `exhausted` in `subagentTerminalStatus`.

In `cmd/serf-tui/internal/msgrender/tool_bodies.go`, add exhausted to the existing failed/non-success rail class while leaving completed/cancelled/stopped styling unchanged:

```go
case "failed", "error", "exhausted":
	return "failed"
case "completed", "done", "succeeded", "cancelled", "stopped":
	return "done"
```

Keep the literal `exhausted` status/reason text in the body so lifetime and tool-round exhaustion remain distinguishable from failure.

- [ ] **Step 7: Run all AppWire, Hub, and TUI tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./appwire ./internal/appprojector ./server ./cmd/serf-hub ./cmd/serf-tui/... -count=1
GOCACHE=/tmp/serf-gocache go test ./agent ./cmd/serf -count=1
bash cmd/serf-hub/jstest/run-all.sh
```

Expected: PASS. Both AppWire paths preserve the same fields; exhausted jobs remain exhausted, terminal, and visibly non-successful in historical and live Hub rendering and TUI. The full Hub JavaScript suite passes, not only the two new files.

- [ ] **Step 8: Commit UI projection support**

Run:

```bash
git status --short
git add agent/status.go agent/status_test.go appwire/types.go appwire/types_test.go internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go server/server.go cmd/serf/serve.go cmd/serf/serve_test.go server/appwire_runtime.go server/appwire_runtime_test.go cmd/serf-hub/app_threadread.go cmd/serf-hub/app_threadread_test.go cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/jstest/test-renderer-notifications.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-subagents.js cmd/serf-tui/hub_notifications.go cmd/serf-tui/hub_notifications_test.go cmd/serf-tui/internal/transcript/reducer.go cmd/serf-tui/internal/transcript/reducer_test.go cmd/serf-tui/internal/msgrender/tool_bodies.go cmd/serf-tui/internal/msgrender/tool_bodies_test.go
git commit -m "feat(ui): render exhausted jobs as terminal" -m "Carry exhaustion through live job-finished events and the complete runtime diagnostics chain. Teach Hub history, live rails, notifications, and TUI reducers/renderers that exhausted is a distinct non-success terminal state."
```

Expected: one UI-projection commit with no unrelated paths staged.

## Task 6: Verify the Scope Lock and Full Regression Contract

**Files:**

- Verify only: all files changed in Tasks 1-5

- [ ] **Step 1: Verify the unchanged configuration and policy boundaries**

Run:

```bash
git diff HEAD~5 -- agent/subagents.go agent/session_config.go agent/session_goal.go agent/session_lifecycle.go
rg -n 'MaxTurns:[[:space:]]*500|MaxTurns[[:space:]]*=[[:space:]]*500' agent
rg -n 'Goal.*Limit|MaxGoal|retry' agent/session_goal.go agent/session_lifecycle.go agent/session_config.go
```

Expected: the delegate default remains 500; no goal iteration/continuation constant or behavior changed; no provider retry constant or behavior changed. If the repository uses different exact goal/retry filenames, locate the existing definitions with `rg` and inspect them read-only; do not edit them.

- [ ] **Step 2: Run the focused truthfulness contract as one gate**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/... ./appwire ./internal/appprojector ./server ./cmd/serf ./cmd/serf-hub ./cmd/serf-tui/... -run 'TurnBudgetWarning|MaxTurns|MaxToolRounds|Exhausted|BudgetExhaustion' -count=1 -v
bash cmd/serf-hub/jstest/run-all.sh
```

Expected: PASS with evidence for warning timing/restore/accounting, the unchanged `TestSubagent_MaxTurns_DefaultsTo500_NotInheritedFromParent` contract, lifetime non-resumability, tool-round resumability, partial evidence, durable status, cross-surface agreement, and crash replay.

- [ ] **Step 3: Run the repository's complete deterministic gate**

Run:

```bash
make test
```

Expected: PASS without provider credentials or network access. A skipped, timed-out, or credential-triggered live test is not a green result; investigate it under `docs/testing.md` before proceeding.

- [ ] **Step 4: Inspect the final diff for scope and type consistency**

Run:

```bash
git status --short
git diff --check
git diff --stat HEAD~5
git diff HEAD~5 -- . ':(exclude)docs/superpowers/specs/2026-07-15-delegate-budget-truthfulness-design.md'
rg -n 'StatusExhausted|SubagentExhausted|ExhaustionBudget|ExhaustionLimit|turn_budget_exhausted|tool_round_budget_exhausted' agent appwire internal server cmd
rg -n 'Superpowers|automatic escalation|reslic|compatib' agent appwire internal server cmd
```

Expected: no whitespace errors; no spec edit; the same status and field names flow through every layer; the final search finds no new Superpowers behavior, automatic escalation/reslicing, or compatibility branch.

- [ ] **Step 5: Route any verification correction back through its owning task**

If verification reveals a defect, return to the task that owns that file, add a failing focused assertion, make the smallest correction, rerun that task's focused command, and use that task's explicit `git add` list and commit format. Then rerun Steps 1-4 in this task. Do not create an untested catch-all correction commit.

Expected: no correction when Tasks 1-5 are complete. Any correction is test-backed and committed with the owning layer's explicit paths.

- [ ] **Step 6: Record the handoff evidence**

Run these evidence commands:

```bash
git log --oneline --max-count=6
git status --short
git branch --show-current
git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}'
```

Report separately: the implementation commit IDs from the log; the exact focused and full test commands with their exit status; the current branch and whether it is locally merged; and whether it has been pushed to its upstream. End with this exact scope audit: `500 default unchanged; goal/retry behavior unchanged; no Superpowers, escalation/reslicing, or compatibility behavior added`.

If the branch has no upstream, report `push state: no upstream configured`; do not treat that command's non-zero exit as a test failure. Do not report blocked, skipped, timed-out, or pending checks as passing.
