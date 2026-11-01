# Delegate Terminal Reports Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a delegate's terminal report an admission decision backed by one outstanding-work inventory, a non-terminal wait rail, a versioned task summary on every report path, and an explicit parent disposition, without adding a human attention surface or a retirement blocker.

**Architecture:** A single `OutstandingDelegateWork` snapshot reuses the existing retirement, job-tree drain, delegate-delivery and attention predicates. `subagent.run` admits a terminal report only when that snapshot is empty at the existing serialized finalization boundary, with a second revalidation under the claim. Delegates gain `done`, `blocked` and `waiting_on_work` dispositions; the packet carries a versioned task and inventory summary rendered on every report-bearing path. A root with a human partner must end a delegate-report notification turn with an explicit disposition; bare text gets one existing steering round and then settles, with no attention entry and no retirement block.

**Tech Stack:** Go, the Evener agent runtime, `appwire` JSON-RPC types, transcript/JSONL stores, table-driven Go tests with scripted providers.

**Spec:** `docs/superpowers/specs/2026-10-21-delegate-terminal-reports-design.md`

**Spec input note:** The spec is currently untracked. Treat it as the reviewed design input, commit it with the implementation branch if its owner agrees, and do not edit it from this plan.

**Scope note:** This plan implements the 2026-10-21 design only. The 2026-10-22 unresolved-delegate-obligation design is out of scope: no durable obligation record, no `delegate_report` retirement blocker, no automatic re-drive, no `done_check`, and no human attention entry.

## Global Constraints

- Reuse the existing evidence predicates; do not add a parallel inventory. The snapshot must include running jobs, registered watches, owed watch frames, unsettled watch sends and settlement retries, pending job notifications and terminal-flush entries, queued inputs and steering, open child delegate runs, pending child deliveries, pending child attention and retries, and an active goal.
- A read failure is unknown, never empty. Unknown refuses terminal admission.
- A published `done` packet requires an empty inventory at publication; `blocked` may publish with a live inventory and carries it.
- `TerminalPacket.Kind` is the terminal marker. Put the versioned summary in `TerminalPacket.Metadata`.
- Delegate and root dispositions share the existing `end_reason` rail with audience-gated values. Do not add a second disposition property.
- A delegate `waiting_on_work` is non-terminal and creates no packet. Accept it only for a waker with a guaranteed completion or cancellation signal.
- A bare-text parent acknowledgement of a delegate report gets exactly one existing steering round, then settles. Do not add a second steering loop.
- No human attention entry, no retirement blocker, no task-list gate, no mandatory completion check, no automatic delegate resume, and no prose parsing.
- Failure, cancellation and exhaustion packets keep their existing delivery path. They carry the summary and a generation-failed notice, never a no-work claim.
- A closed caller result schema must not block `waiting_on_work` or `blocked`; those are tool arguments outside `output`.
- No new dependency. Use the repository's pinned tools.
- Tests assert typed values, data fields, sockets, files and process effects. Do not assert human-readable phrasing.
- Default tests are deterministic: no live provider, network, Docker or SSH. Scripted providers only.
- Targeted tests run locally; CI is the full-suite authority. Run heavy local gates serially.

## Review Focus

- **Same-batch work after a terminal candidate:** a `communicate` followed by a job or watch creation in the same response must be refused at finalization, not published from a pre-batch snapshot.
- **Pending child delivery or owed watch frame:** the inventory must see it, and admission must stay refused until it settles.
- **Unreadable evidence:** a failed job/attention/transcript read must make the inventory unknown and refuse admission; it must not look empty.
- **Closed result schema:** a delegate with a strict `result_schema` must still be able to report `waiting_on_work` or `blocked`.
- **Bare-text report acknowledgement:** exactly one steering round, then a settled turn with no attention event and no retirement block.

---

### Task 1: Outstanding-work inventory

**Files:**
- Create: `agent/delegate_outstanding_work.go`
- Create: `agent/delegate_outstanding_work_test.go`
- Modify: `agent/session_retirement_evidence.go` (only to reuse or expose predicates, no behavior change)

**Interfaces:**
- Produces:

```go
type OutstandingDelegateWork struct {
    Jobs            []string
    Watches         []string
    Notifications   []string
    Inputs          []string
    ChildRuns       []string
    ChildDeliveries []string
    Attention       []string
    Goals           []string
    Unknown         []string
}

func (w OutstandingDelegateWork) Empty() bool
func (w OutstandingDelegateWork) IDs() []string
func (s *Session) outstandingDelegateWork() (OutstandingDelegateWork, error)
```

- `Empty` returns false when any category is non-empty; `Unknown` non-empty also makes `Empty` false.
- `IDs` returns a stable, deduplicated list of `"<category>:<id>"` strings for steering and tests.
- Add unexported itemized accessors beside the existing evidence predicates where an ID list does not exist: `outstandingJobIDs`, `activeWatchIDs`, `pendingNotificationIDs`, `pendingInputIDs`, `pendingChildRunIDs`, `pendingChildDeliveryIDs`, `pendingAttentionIDs`, `activeGoalIDs`. Do not change the existing retirement behavior.

- [ ] **Step 1: Write the failing inventory tests**

Cover an empty session, one entry in each category, duplicate IDs across categories, and a read failure from each of the job journal, attention fold and delegate transcript.

```go
func TestOutstandingDelegateWorkEmpty(t *testing.T) { /* Empty() == true */ }
func TestOutstandingDelegateWorkItemizesEachCategory(t *testing.T) { /* category ID present */ }
func TestOutstandingDelegateWorkReadFailureIsUnknown(t *testing.T) { /* Unknown non-empty, Empty() == false */ }
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./agent -run '^TestOutstandingDelegateWork' -count=1`

Expected: fail with undefined `OutstandingDelegateWork` / `outstandingDelegateWork`.

- [ ] **Step 3: Implement the snapshot and accessors**

The snapshot reads the existing `jobManager.retirementEvidence`, the job journal's `NotifyPending` records, the attention registry, `hasPendingDelegateDeliveries`, `pendingRootDelegateAttention`, and the goal store. Treat each read error as an `Unknown` entry and return the snapshot together with the error; callers decide admission.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test ./agent -run '^TestOutstandingDelegateWork' -count=1`

Expected: PASS.

- [ ] **Step 5: Run the existing retirement tests**

Run: `go test ./agent -run 'Retirement' -count=1`

Expected: PASS; the inventory is additive and existing retirement behavior is unchanged.

- [ ] **Step 6: Commit**

```bash
git add agent/delegate_outstanding_work.go agent/delegate_outstanding_work_test.go agent/session_retirement_evidence.go
git commit -m "feat(agent): add outstanding delegate work inventory"
```

---

### Task 2: Delegate disposition rail

**Files:**
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_communicate.go`
- Modify: `agent/schema/turn.go` (only if the disposition enum needs a new value)
- Test: `agent/session_communicate_end_reason_test.go`
- Test: `agent/delegate_disposition_test.go` (create)

**Interfaces:**
- Produces:

```go
const (
    CommunicateEndReasonBlocked = "blocked"
)

var DelegateCommunicateEndReasons = []string{
    CommunicateEndReasonDone,
    CommunicateEndReasonBlocked,
    CommunicateEndReasonWaitingOnWork,
}

func WithDelegateCommunicateEndReason(def llm.ToolDefinition) llm.ToolDefinition
```

- Delegate `communicate` gains `end_reason` with `DelegateCommunicateEndReasons`, plus `blocker` (string, for `blocked`) and `waiting_on` (string: job, watch, or child delegate ID, for `waiting_on_work`). These are tool arguments outside the caller's `output` schema.
- A delegate `end_turn=true` with no `end_reason` keeps the current behavior and defaults to `done`.
- A root keeps `CommunicateEndReasons`; a delegate using `needs_response` is rejected.
- `blocked` requires a non-empty `blocker`; `waiting_on_work` requires a non-empty `waiting_on`.

- [ ] **Step 1: Write the failing schema and handler tests**

```go
func TestDelegateCommunicateOffersOnlyDelegateDispositions(t *testing.T) { /* enum exactly done, blocked, waiting_on_work */ }
func TestDelegateCommunicateRejectsNeedsResponse(t *testing.T) { /* typed invalid-params error */ }
func TestDelegateCommunicateDefaultsTerminalReasonToDone(t *testing.T) { /* empty args accepted as done */ }
func TestDelegateCommunicateRequiresBlockerAndWaitTarget(t *testing.T) { /* missing fields rejected */ }
func TestDelegateDispositionWorksWithClosedResultSchema(t *testing.T) { /* strict schema still accepts waiting_on_work and blocked */ }
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./agent -run '^TestDelegateCommunicate' -count=1`

Expected: fail because the delegate definition has no `end_reason`, `blocker` or `waiting_on`.

- [ ] **Step 3: Implement the delegate schema and argument validation**

Reuse `WithCommunicateEndReason`'s property construction, parameterized by the audience's reason list. Parse `blocker` and `waiting_on` in `session_tools_communicate.go`; reject them on a root and reject `needs_response` on a delegate. Pass the values through `events.CommunicateData` so the finalization boundary sees them.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test ./agent -run '^TestDelegateCommunicate' -count=1`

Expected: PASS.

- [ ] **Step 5: Run the existing communicate tests**

Run: `go test ./agent -run 'Communicate' -count=1`

Expected: PASS; root behavior is unchanged.

- [ ] **Step 6: Commit**

```bash
git add agent/internal/tool/definitions.go agent/session_tools_communicate.go agent/schema/turn.go agent/session_communicate_end_reason_test.go agent/delegate_disposition_test.go
git commit -m "feat(agent): add delegate report dispositions"
```

---

### Task 3: Terminal admission and waits

**Files:**
- Modify: `agent/subagents.go` (finalization boundary in `subagent.run`)
- Modify: `agent/delegate_tree_finish.go`
- Modify: `agent/delegate_tree_intents.go`
- Modify: `agent/session_lifecycle.go` (only the steering entry point)
- Test: `agent/delegate_terminal_admission_test.go` (create)
- Test: `agent/delegate_wait_test.go` (create)

**Interfaces:**
- Produces:

```go
type TerminalCandidate struct {
    Disposition string
    Packet      delegatestore.TerminalPacket
    Blocker     string
    WaitingOn   string
}

func (a *subagent) admitTerminalReport(ctx context.Context, c TerminalCandidate) (bool, error)
func (s *Session) validateWaitTarget(ctx context.Context, target string) error
func (s *Session) steerTerminalRefusal(reason string) error
```

- `admitTerminalReport` runs after the whole tool batch and before the existing drain that consumes evidence. It captures `outstandingDelegateWork`; unknown refuses. For `done`, a non-empty inventory refuses. For `blocked`, a non-empty inventory is admitted and copied into the packet. A refusal steers once and records a refused terminal attempt, not a report.
- The admission is re-run under the finalization claim immediately before packet publication. A same-batch job or watch creation between the two reads refuses publication.
- `waiting_on_work` never calls `admitTerminalReport`. It validates the target; a live waker keeps the run open and ends the turn without a packet. A settled or absent target refuses the wait and steers once.
- Failure, cancellation and exhaustion packets bypass `admitTerminalReport` and keep their existing delivery path.
- The one steer uses the existing no-tool-call steering core; no new loop.
- Until Task 5 lands, the admission may store the `blocked` inventory under an unexported packet-metadata field. Task 5 promotes that field to the versioned `ReportSummary` without changing the admission decision or the packet's terminal identity.

- [ ] **Step 1: Write the failing admission tests**

```go
func TestTerminalAdmissionRefusesDoneWithRunningJob(t *testing.T) { /* run stays open, one steer names the job */ }
func TestTerminalAdmissionRefusesSameBatchWatchCreation(t *testing.T) { /* post-batch snapshot sees the watch */ }
func TestTerminalAdmissionRefusesPendingChildDelivery(t *testing.T) { /* inventory non-empty */ }
func TestTerminalAdmissionRefusesUnknownEvidence(t *testing.T) { /* read failure refuses */ }
func TestTerminalAdmissionPublishesEmptyDone(t *testing.T) { /* packet published */ }
func TestTerminalAdmissionPublishesBlockedWithLiveWork(t *testing.T) { /* packet carries the inventory */ }
func TestTerminalAdmissionRestartSafety(t *testing.T) { /* prepared packet keeps its generation, a refused candidate is not durable, a wait revalidates its waker on restore */ }
```

- [ ] **Step 2: Write the failing wait tests**

```go
func TestDelegateWaitOnRunningJobKeepsRunOpenAndResumes(t *testing.T) { /* no packet; job notification resumes; later done publishes */ }
func TestDelegateWaitOnSettledJobIsRefusedAndSteered(t *testing.T) { /* no packet; one steer */ }
func TestDelegateWaitOnRemovedWatchGetsCancellationNotice(t *testing.T) { /* notice or refusal, no silent open run */ }
```

- [ ] **Step 3: Run the tests and verify they fail**

Run: `go test ./agent -run '^(TestTerminalAdmission|TestDelegateWait)' -count=1`

Expected: fail because admission and wait handling do not exist.

- [ ] **Step 4: Implement admission at the finalization boundary**

Place the first admission after the tool batch and before `drainForFinalization`; place the revalidation under the existing finalization claim before the packet is published. Use the snapshot from Task 1. A refusal releases the claim, keeps the run open and steers once. Copy the live inventory into a `blocked` packet's metadata.

- [ ] **Step 5: Implement wait validation and wake semantics**

Resolve `waiting_on` against a running job, a live watch, a child attention that guarantees a settlement or report notice, or queued input. A valid wait ends the turn without a packet and leaves the existing run open; the existing wake and delivery path resumes it. A watch removal must notify the waiter or the wait is refused.

- [ ] **Step 6: Run the tests and verify they pass**

Run: `go test ./agent -run '^(TestTerminalAdmission|TestDelegateWait)' -count=1`

Expected: PASS.

- [ ] **Step 7: Run the existing delegate finish and delivery tests**

Run: `go test ./agent -run '^(TestDelegate|TestSubagent)' -count=1`

Expected: PASS; failure/cancellation/exhaustion and late-notification behavior are unchanged.

- [ ] **Step 8: Commit**

```bash
git add agent/subagents.go agent/delegate_tree_finish.go agent/delegate_tree_intents.go agent/session_lifecycle.go agent/delegate_terminal_admission_test.go agent/delegate_wait_test.go
git commit -m "feat(agent): admit terminal reports from an empty work inventory"
```

---

### Task 4: Observer readiness and inline waiters

**Files:**
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/session_tools_communicate.go`
- Modify: `agent/delegate_runtime.go`
- Modify: `agent/delegate_delivery.go`
- Modify: `agent/session_state.go` (only if the update payload needs a typed field)
- Test: `agent/delegate_readiness_test.go` (create)

**Interfaces:**
- Produces:

```go
const DelegateReadinessReady = "ready"

type delegateUpdateDelivery struct {
    // existing fields...
    Readiness string
}
```

- `communicate(end_turn=false, readiness="ready")` on a delegate is a non-terminal observer readiness update. It is a tool argument outside `output`.
- `postDelegateUpdate` forwards and persists the readiness value; the parent-facing update renderer includes it once.
- An inline `delegate_send(max_wait_ms>0)` waiter resolves on the readiness update, not only on a terminal packet. The update does not acknowledge or supersede the terminal delivery.

- [ ] **Step 1: Write the failing readiness tests**

```go
func TestDelegateReadinessUpdateReachesParent(t *testing.T) { /* update contains the typed readiness */ }
func TestDelegateReadinessResolvesInlineWaiter(t *testing.T) { /* delegate_send returns on readiness, not terminal */ }
func TestDelegateReadinessDoesNotSupersedeTerminalDelivery(t *testing.T) { /* later report still delivers */ }
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./agent -run '^TestDelegateReadiness' -count=1`

Expected: fail because the update has no readiness field and the inline waiter does not resolve on it.

- [ ] **Step 3: Implement the readiness argument and delivery**

Add the argument only for delegates. Forward it through `postDelegateUpdate`, persist it with the update, and render it in the existing update path. Wake the inline waiter on the update.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test ./agent -run '^TestDelegateReadiness' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/definitions.go agent/session_tools_communicate.go agent/delegate_runtime.go agent/delegate_delivery.go agent/session_state.go agent/delegate_readiness_test.go
git commit -m "feat(agent): deliver non-terminal delegate readiness updates"
```

---

### Task 5: Packet summary on every report path

**Files:**
- Create: `agent/delegate_report_summary.go`
- Create: `agent/delegate_report_summary_test.go`
- Modify: `agent/subagents.go` (`delegateTerminalPacketMetadata`)
- Modify: `agent/internal/delegatestore/record.go` (only if a typed metadata field is needed)
- Modify: `agent/delegate_delivery.go`
- Modify: `agent/delegate_runtime.go`

**Interfaces:**
- Produces:

```go
type ReportSummary struct {
    Version     int              `json:"version"`
    ReportKind  string           `json:"report_kind"`
    Tasks       TaskSummary      `json:"tasks"`
    Inventory   InventorySummary `json:"inventory"`
    TaskOwnerID string           `json:"task_owner_id,omitempty"`
}

type TaskSummary struct {
    Present    bool              `json:"present"`
    Total      int               `json:"total"`
    Done       int               `json:"done"`
    Cancelled  int               `json:"cancelled"`
    Remaining  int               `json:"remaining"`
    Current    *TaskSummaryItem  `json:"current,omitempty"`
    Incomplete []TaskSummaryItem `json:"incomplete,omitempty"`
    Truncated  bool              `json:"truncated,omitempty"`
}

type TaskSummaryItem struct {
    ID          int    `json:"id"`
    Status      string `json:"status"`
    Description string `json:"description"`
}

type InventorySummary struct {
    Categories []InventoryCategory `json:"categories,omitempty"`
}

type InventoryCategory struct {
    Category string   `json:"category"`
    IDs      []string `json:"ids"`
}

func buildReportSummary(s *Session, reportKind string) ReportSummary
func renderReportNotice(summary ReportSummary) string
```

- Build from `task.NewTaskStore(stateDir, owner).ViewWithError()` and `task.Summarize`. A missing store is `Present:false`; a load error is `Present:false` and must not look like an empty completed list.
- Shared task stores are summarized once by `taskStoreOwnerSessionID`.
- `Incomplete` is bounded (first ten items, in-progress first) with a per-description byte bound and `Truncated:true` when cut. `Current` duplicates no item that is already first in `Incomplete`.
- Attach the summary to `delegateTerminalPacketMetadata` under `Report *ReportSummary`.
- Render it once in `delegateNotificationContent`, the inline `delegate_send` result, and each `earlier_results` packet. Do not repeat raw metadata and rendered lines in the same body.
- The notice is generation-scoped: this generation is final; no job, watch, attention or delivery is outstanding at publication; resume the delegate if the work is unfinished; a later supervision attention may start a new generation; `blocked` names its blocker.

- [ ] **Step 1: Write the failing summary tests**

```go
func TestReportSummaryCountsAndCurrent(t *testing.T) { /* exact Total/Done/Cancelled/Remaining/Current */ }
func TestReportSummaryBoundsIncompleteList(t *testing.T) { /* ten items, in-progress first, Truncated */ }
func TestReportSummaryAbsentStoreIsNotComplete(t *testing.T) { /* Present false, Remaining unknown semantics */ }
func TestReportSummarySharedOwnerOnce(t *testing.T) { /* one TaskOwnerID */ }
func TestDelegateNotificationRendersSummaryOnce(t *testing.T) { /* one rendered summary, no raw duplicate */ }
func TestInlineAndEarlierResultsCarrySummary(t *testing.T) { /* both paths */ }
func TestLaterSupervisionAttentionCanStartNewGeneration(t *testing.T) { /* the first notice is generation-scoped; the next generation reports normally */ }
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./agent -run '^TestReportSummary|^TestDelegateNotificationRendersSummary|^TestInlineAndEarlier' -count=1`

Expected: fail because the summary type and renderers do not exist.

- [ ] **Step 3: Implement the summary builder and renderer**

Use `ListSummary` and `ViewWithError`; do not use the lazy loader that drops errors. Keep the fields bounded and stable for tests.

- [ ] **Step 4: Attach the summary to the packet and every path**

Set `Report` in `delegateTerminalPacketMetadata` at packet construction. Render it in `delegateNotificationContent`, the inline result projection and `earlier_results`. A failure/cancellation/exhaustion packet carries the same summary and a generation-failed notice.

- [ ] **Step 5: Run the tests and verify they pass**

Run: `go test ./agent -run '^TestReportSummary|^TestDelegateNotificationRendersSummary|^TestInlineAndEarlier' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/delegate_report_summary.go agent/delegate_report_summary_test.go agent/subagents.go agent/internal/delegatestore/record.go agent/delegate_delivery.go agent/delegate_runtime.go
git commit -m "feat(agent): summarize delegate reports on every delivery path"
```

---

### Task 6: Parent disposition, retirement reason, docs, incident replay

**Files:**
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_tool_round.go`
- Modify: `agent/schema/snapshot.go` (add `RetirementReason`)
- Modify: `cmd/evener/serve.go` (record the reason at retirement commit)
- Modify: `docs/job-control.md`
- Modify: `docs/daemon-idle-retirement.md`
- Modify: `docs/product/subsystems.md`
- Test: `agent/session_delegate_report_disposition_test.go` (create)
- Test: `agent/retirement_reason_test.go` (create)
- Test: `agent/delegate_terminal_incident_replay_test.go` (create)

**Interfaces:**
- Produces:

```go
// in schema.SessionMeta
RetirementReason string `json:"retirement_reason,omitempty"`
```

- A notification turn that carries a delegate report is marked in the turn context (`carriedDelegateReport`). When `routeNoToolCalls` sees that flag, it routes the bare-text response through the existing `handleNoToolCalls`/`decideNoToolCalls` steering with a one-round cap. When the cap is spent, the turn settles. The report stays recorded as unacknowledged; no attention event, no retirement block, no further loop.
- `done` accepts; `waiting_on_work` with a live waker accepts; `waiting_on_work` with no waker is refused and steered once; `needs_response` keeps its existing meaning.
- At a successful retirement commit, write `RetirementReason` to session meta with the reason (`idle`, `manual`, `archive`). Do not add a second timestamp; `UpdatedAt` is the record time.
- Documentation states the delegate report disposition, the terminal notice, the task summary, and the explicit parent disposition. Update the subsystem map row for delegate reports.
- The incident replay test: a delegate reports `done` with one in-progress task and no waker; the summary shows the task; a bare-text parent reply gets exactly one steering round; an explicit `done` settles and may retire; a resumed delegate opens a new generation whose report supersedes the earlier one. No attention entry appears.

- [ ] **Step 1: Write the failing parent-disposition tests**

```go
func TestDelegateReportBareTextGetsOneSteeringRound(t *testing.T) { /* one round, then settle */ }
func TestDelegateReportExplicitDoneSettles(t *testing.T) { /* done accepted */ }
func TestDelegateReportWaitingWithoutWakerIsRefused(t *testing.T) { /* one steer, no settle as waiting */ }
func TestDelegateReportNeedsResponseKeepsExistingQuestion(t *testing.T) { /* unchanged */ }
func TestDelegateReportCreatesNoAttentionEntry(t *testing.T) { /* no Needs You/attention event */ }
func TestNestedParentReceivesSummaryWithoutRootEndReason(t *testing.T) { /* nested parent continues; its own report carries the summary upward */ }
func TestHeadlessRootReceivesSummary(t *testing.T) { /* summary visible without the root end_reason rail */ }
```

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./agent -run '^TestDelegateReport' -count=1`

Expected: fail because a bare-text notification acknowledgement currently settles idle without a disposition.

- [ ] **Step 3: Implement the turn flag and one-round steering**

Mark the turn when the notification content is a delegate report. Branch `routeNoToolCalls` on the flag: bare text routes through the existing steering core with a one-round cap. Do not add a new loop or a retirement blocker.

- [ ] **Step 4: Write the failing retirement-reason test**

```go
func TestSuccessfulRetirementRecordsReason(t *testing.T) { /* meta.RetirementReason == "idle", UpdatedAt set */ }
```

Run: `go test ./agent -run '^TestSuccessfulRetirementRecordsReason' -count=1`

Expected: fail because the meta has no reason field.

- [ ] **Step 5: Implement the retirement reason and update the docs**

Add the meta field, write it at retirement commit, and update the three docs. Use the product guide's existing vocabulary for delegate reports and idle retirement.

- [ ] **Step 6: Write and run the incident replay**

```go
func TestDelegateTerminalIncidentReplay(t *testing.T) {
    // One in-progress task, no live waker, terminal done report.
    // Summary shows the task and generation-final notice.
    // Bare text: one steering round, no attention event, no retirement block.
    // Explicit done: settles.
}
```

Run: `go test ./agent -run '^TestDelegateTerminalIncidentReplay' -count=1`

Expected: PASS, with no attention event and no retirement refusal.

- [ ] **Step 7: Run the focused suites and docs check**

Run:

```bash
go test ./agent -run '^(TestTerminalAdmission|TestDelegateWait|TestDelegateReadiness|TestReportSummary|TestDelegateReport|TestDelegateTerminalIncidentReplay|TestSuccessfulRetirementRecordsReason)' -count=1
go test ./agent/task -count=1
make docs-check
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add agent/session_lifecycle.go agent/session_tool_round.go agent/schema/snapshot.go cmd/evener/serve.go docs/job-control.md docs/daemon-idle-retirement.md docs/product/subsystems.md agent/session_delegate_report_disposition_test.go agent/retirement_reason_test.go agent/delegate_terminal_incident_replay_test.go
git commit -m "feat(agent): require an explicit disposition for delegate reports"
```

---

## Final Verification

- [ ] The focused suites listed in the tasks pass. Do not block a push on a full local `go test ./agent`; CI is the full-suite authority.
- [ ] `go test ./agent/task ./cmd/evener -run 'Retirement|Communicate|Delegate' -count=1` passes.
- [ ] `make docs-check` passes.
- [ ] `git diff --check` passes.
- [ ] No new Needs You/attention event appears in the incident replay.
- [ ] The existing late-notification behavior is unchanged: a later job notification does not produce a second report from the same terminal generation.
- [ ] A successful retirement records `idle` in `RetirementReason`; a manual stop and an archive record their own reasons.
