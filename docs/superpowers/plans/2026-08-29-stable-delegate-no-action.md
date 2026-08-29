# Stable Delegate No-Action Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an attention-triggered stable delegate acknowledge durable attention without `communicate`, settle as private `completed_no_action`, and emit no duplicate parent report.

**Architecture:** Add one lease-bound, process-local completion-evidence object to the existing delegate runtime binding. The model loop records an explicit no-action outcome only for non-empty bare `EntryDelegateAttention`; one monotonic completion requirement preserves result enforcement when owner/hook work enters. The existing finalization claim authorizes a new controller-owned packetless finish; all terminal-communicate, job-cut, crash, and delivery behavior outside this branch remains unchanged.

**Tech Stack:** Go 1.26, existing `agent` package session/delegate controller, scripted LLM provider tests, existing `delegatestore` journal/fold.

**Spec:** `docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md`

## Global Constraints

- Follow `docs/developing-evener/testing.md`: scripted provider at the LLM boundary, real Evener below it, deterministic barriers instead of sleeps.
- Strict TDD: write each behavioral test first and observe the expected failure before production edits.
- `ask_user` is unconditionally excluded from subagents; do not add pending-ask or owed-generation behavior.
- Do not change transcript or `delegatestore` schemas; `completed_no_action` already exists.
- Do not change terminal `communicate` packet durability (#569), same-round
  result selection (#570), or terminal-cut behavior (#571); each is separate
  adjacent work.
- Preserve explicit `communicate`, user/owner missing-terminal, stop, cancellation, exhaustion, recovery, delivery ordering, lifecycle updates, and watch behavior outside the new no-action branch.
- Keep process evidence authenticated by exact `delegateLease`; never mutate it by delegate ID alone.
- Stage named paths only. Never use `git add .` or `git add -A`.

---

### Task 1: Lease-Bound Completion Evidence

**Files:**
- Modify: `agent/delegate_tree_controller.go:110-141`
- Modify: `agent/delegate_tree_start.go:698-862`
- Create: `agent/delegate_tree_completion_test.go`

**Interfaces:**
- Produces:
  - `delegateCompletionRequirement`
  - `delegateCompletionOutcome`
  - `delegateGenerationEvidence`
  - exact-lease controller methods used by later tasks
- Consumes: existing `delegateLease`, `delegateRuntimeBinding`, `CommitStart`, `releaseGenerationLocked`.

- [ ] **Step 1: Write failing evidence-lifecycle tests**

Create `agent/delegate_tree_completion_test.go` with tests that use `newDelegateControllerTestHarness` and existing start helpers:

```go
func TestDelegateGenerationEvidenceInitialRequirement(t *testing.T) {
    // Start one TriggerAttention generation and one TriggerOwnerInput generation.
    // Assert attention starts attention-only; owner input starts report-required.
}

func TestDelegateGenerationEvidenceRejectsStaleLease(t *testing.T) {
    // Finish generation 1, start generation 2, then attempt every mutation with lease 1.
    // Assert stale-lease errors and unchanged generation-2 evidence.
}

func TestDelegateGenerationEvidenceEscalationIsMonotonic(t *testing.T) {
    // Escalate attention-only twice; assert report-required both times.
    // Attempt no-action outcome after escalation; assert refusal.
}

func TestDelegateGenerationEvidenceClearsOnRelease(t *testing.T) {
    // Release generation 1 and assert no evidence is reachable through its stale lease.
}
```

The production mutations these tests catch are: wrong initial requirement, delegate-ID-only mutation, requirement downgrade, or evidence surviving generation release.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./agent -run '^TestDelegateGenerationEvidence' -count=1
```

Expected: compile failure because the evidence types/methods do not exist.

- [ ] **Step 3: Add the process-local evidence types**

In `delegate_tree_controller.go`, add:

```go
type delegateCompletionRequirement uint8

const (
    delegateCompletionAttentionOnly delegateCompletionRequirement = iota
    delegateCompletionReportRequired
)

type delegateCompletionOutcome uint8

const (
    delegateCompletionOutcomeNone delegateCompletionOutcome = iota
    delegateCompletionOutcomeAttentionNoAction
)

type delegateGenerationEvidence struct {
    requirement  delegateCompletionRequirement
    outcome      delegateCompletionOutcome
    terminalSeen bool
    fallback     *delegateFinish
}
```

Add `evidence *delegateGenerationEvidence` to `delegateRuntimeBinding`.

- [ ] **Step 4: Initialize evidence exactly once per committed start**

In `CommitStart`, derive requirement from `record.trigger`:

```go
requirement := delegateCompletionReportRequired
if record.trigger == delegatestore.TriggerAttention {
    requirement = delegateCompletionAttentionOnly
}

live.binding = &delegateRuntimeBinding{
    lease:    lease,
    runtime:  record.runtime,
    cancel:   record.cancel,
    ready:    record.trigger == delegatestore.TriggerAttention,
    evidence: &delegateGenerationEvidence{requirement: requirement},
}
```

`releaseGenerationLocked` already drops the whole binding; do not add a second cleanup authority.

- [ ] **Step 5: Add exact-lease evidence methods**

Add controller methods that lock `c.mu`, call `exactLeaseLocked`, verify `live.binding.lease == lease`, and then operate on evidence:

```go
type delegateCompletionSnapshot struct {
    requirement  delegateCompletionRequirement
    outcome      delegateCompletionOutcome
    terminalSeen bool
    fallback     *delegateFinish
}

func (c *delegateTreeController) escalateCompletionRequirement(lease delegateLease) error
func (c *delegateTreeController) recordAttentionNoAction(lease delegateLease) (bool, error)
func (c *delegateTreeController) recordTerminalSeen(lease delegateLease) error
func (c *delegateTreeController) completionSnapshot(lease delegateLease) (delegateCompletionSnapshot, error)
```

Rules:

- escalation only moves attention-only -> report-required;
- no-action records only while attention-only and terminal-unseen;
- terminal-seen is monotonic;
- snapshot clones `fallback` when present.

Do not add fallback mutation yet; Task 4 owns its finalization claim.

- [ ] **Step 6: Run focused tests GREEN**

```bash
go test ./agent -run '^TestDelegateGenerationEvidence' -count=1
```

Expected: PASS.

- [ ] **Step 7: Run nearby controller tests**

```bash
go test ./agent -run '^(TestDelegateControllerRuntimeAttachmentIsOneToOne|TestDelegateControllerReserveAttentionRequiresResidentRuntimeAndPendingID|TestDelegateGenerationEvidence)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 1**

```bash
git add agent/delegate_tree_controller.go agent/delegate_tree_start.go agent/delegate_tree_completion_test.go
git commit -m "refactor(delegate): track generation completion evidence"
```

---

### Task 2: Completion Policy and Explicit No-Action Outcome

**Files:**
- Modify: `agent/delegate_tree_steer.go:228-332`
- Modify: `agent/session_lifecycle.go:498-524,1204-1236,1335-1493`
- Modify: `agent/session_tools_communicate.go:73-112`
- Modify: `agent/session_tool_registry.go:276-297`
- Modify: `agent/subagents.go:1517-1671,2017-2042`
- Modify: `agent/fuzz_lf_roundcontent_test.go:79-116`
- Modify: `agent/delegate_tree_steer_test.go`
- Modify: `agent/delegate_resource_supervision_test.go`

**Interfaces:**
- Consumes Task 1 evidence methods.
- Produces:
  - explicit no-action route/outcome;
  - monotonic escalation for bound stable work;
  - terminal-seen marker;
  - one shared clean-exit recovery decision used by Task 4.

- [ ] **Step 1: Write RED tests for steering escalation**

Extend `delegate_tree_steer_test.go` near `TestDelegateControllerBeginModelRequestBindsPendingEntriesOnce` and `TestDelegateControllerSteerAfterRequestBindWaitsForNextRequest`:

```go
func TestDelegateControllerBoundSteeringRequiresReport(t *testing.T) {
    // Seed an attention generation, persist steering, bind it through CompleteModelRequest,
    // and assert completion requirement becomes report-required.
}

func TestDelegateControllerLateSteeringEscalatesOnNextRequest(t *testing.T) {
    // Bind request 1, accept steering afterward, bind request 2, and assert escalation
    // occurs only when request 2 projects the steering.
}
```

Run:

```bash
go test ./agent -run '^TestDelegateController.*Steering.*Require|^TestDelegateControllerLateSteering' -count=1
```

Expected: FAIL because `CompleteModelRequest` does not escalate evidence.

- [ ] **Step 2: Escalate when steering is actually bound**

In `CompleteModelRequest`, after `bound` is known and before returning history, set the exact binding evidence to report-required when `len(bound) != 0`. Do it under the controller lock already protecting the lease; do not call a lock-taking wrapper recursively.

Run the Step 1 tests. Expected: PASS.

- [ ] **Step 3: Write RED route tests for bare attention**

Update `FuzzLfRouteNoToolCalls` seeds/oracle and add a table unit test around `routeNoToolCalls`:

```go
func TestRouteNoToolCallsDelegateAttention(t *testing.T) {
    tests := []struct {
        name       string
        noContent  bool
        allowNoAct bool
        want       noCallsRoute
    }{
        {"bare eligible attention", false, true, finishDelegateAttentionNoAction},
        {"bare report-required attention", false, false, runNoToolCalls},
        {"empty eligible attention", true, true, runNoToolCalls},
    }
    // Existing EntryNotification cases remain unchanged.
}
```

Run:

```bash
go test ./agent -run '^(TestRouteNoToolCallsDelegateAttention|FuzzLfRouteNoToolCalls)$' -count=1
```

Expected: FAIL because the route enum/signature lacks delegate no-action.

- [ ] **Step 4: Add the explicit route and outcome recording**

Add `finishDelegateAttentionNoAction` to `noCallsRoute` and extend `routeNoToolCalls` with an `allowDelegateNoAction bool` argument. In `processOneInput`, for a non-empty `EntryDelegateAttention`, extract the exact lease from `ctx`, ask the controller to `recordAttentionNoAction`, and pass the returned eligibility into the pure router.

On `finishDelegateAttentionNoAction`, end the input idle and return without `handleNoToolCalls`. Empty attention still uses the retry budget. Root `EntryNotification` routing remains byte-for-byte equivalent.

- [ ] **Step 5: Mark terminal communicate with the exact lease**

Change the communicate dependency callback to accept context:

```go
setCommunicateResult func(context.Context, string, string, string)
```

Pass the tool handler's `ctx` from `session_tools_communicate.go`. After setting `s.comm` under `s.mu`, extract `delegateRunLeaseContextKey` and call `recordTerminalSeen` outside `s.mu`. Non-stable/root calls have no lease and remain no-ops.

Add a focused test:

```go
func TestDelegateTerminalCommunicateMarksGenerationEvidence(t *testing.T)
```

It must prove a later attempt to record no-action is refused while the existing reported path remains unchanged.

- [ ] **Step 6: Write RED supervision tests for every clean exit**

In `delegate_resource_supervision_test.go`, use the scripted provider and existing cold-stable harness to add:

```go
func TestDelegateResourceSupervision_AttentionBareTextRecordsExplicitNoAction(t *testing.T)
func TestDelegateResourceSupervision_CompletionGateRecoversEveryCleanExit(t *testing.T)
func TestDelegateResourceSupervision_ExplicitAttentionCommunicateRemainsReported(t *testing.T)
func TestDelegateResourceSupervision_UserRunWithoutCommunicateRemainsMissingTerminal(t *testing.T)
```

`CompletionGateRecoversEveryCleanExit` must have deterministic subtests for:

- no-tool response;
- tool-bearing observer handoff;
- notification yield;
- goal-controlled cap;
- blocked hook continuation;
- unblocked hook model context;
- post-drain owner steering.

Each subtest names the production break: returning cleanly without the one bounded recovery nudge.

Run:

```bash
go test ./agent -run '^TestDelegateResourceSupervision_(AttentionBareText|CompletionGate|ExplicitAttention|UserRun)' -count=1
```

Expected: FAIL on duplicate/absent nudge and missing outcome behavior.

- [ ] **Step 7: Implement one evidence-based recovery decision**

Add a controller query that returns:

```go
type delegateCompletionDecision uint8
const (
    delegateCompletionUseExistingTerminal delegateCompletionDecision = iota
    delegateCompletionFinishNoAction
    delegateCompletionNeedsNudge
)
```

Decision rules:

- terminalSeen -> existing terminal;
- attention-only + explicit no-action -> finish no-action;
- otherwise -> needs nudge.

Use this same decision at the existing pre-hook nudge point and once after hook/drain. Preserve `nudgeAvailable` as the single budget. If a post-hook/drain decision needs the nudge, run the continuation, then rerun required drain/finalization checks before settling.

When `runSubagentStopHook` emits model context or user messages, escalate exact-lease completion requirement before returning. Blocked hooks already continue through `ProcessInput`; unblocked model context must force the bounded continuation through the shared decision.

- [ ] **Step 8: Run Task 2 tests GREEN**

```bash
go test ./agent -run '^(TestRouteNoToolCallsDelegateAttention|FuzzLfRouteNoToolCalls|TestDelegateController.*Steering.*Require|TestDelegateControllerLateSteering|TestDelegateTerminalCommunicateMarksGenerationEvidence|TestDelegateResourceSupervision_(AttentionBareText|CompletionGate|ExplicitAttention|UserRun))$' -count=1
```

Expected: PASS.

- [ ] **Step 9: Run nearby lifecycle tests**

```bash
go test ./agent -run '^(TestDelegateResourceSupervision_AutoNudgeOccursOnceForEligibleBuiltin|TestDelegateResourceSupervision_PendingSteerPrecedesAutoNudge|TestDelegateResourceSupervision_SubagentStopBlockingStartsOneContinuation|TestDelegateResourceSupervision_SubagentStopNonblockingStartsNoContinuation|FuzzSessionLifecycleTailCoverage)$' -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit Task 2**

```bash
git add agent/delegate_tree_steer.go agent/session_lifecycle.go agent/session_tools_communicate.go agent/session_tool_registry.go agent/subagents.go agent/fuzz_lf_roundcontent_test.go agent/delegate_tree_steer_test.go agent/delegate_resource_supervision_test.go
git commit -m "fix(delegate): recognize attention no-action completion"
```

---

### Task 3: Claim-Bound Packetless Finalization

**Files:**
- Modify: `agent/delegate_tree_finish.go:69-177,343-525`
- Modify: `agent/subagents.go:1632-1747`
- Modify: `agent/delegate_tree_finish_test.go:547-579`
- Modify: `agent/delegate_resource_supervision_test.go`
- Modify: `agent/delegate_tree_finish_covtest2_test.go` only if existing claim helpers need coverage updates.

**Interfaces:**
- Consumes Task 1 evidence and Task 2 decision.
- Produces `FinishNoAction(claim)` as the only runtime path to `DispositionCompletedNoAction`.

The runtime enters this path through `BeginRunFinalization`, which binds the
sampled run error to the exact claim. After attention resolution it calls
`prepareNoAction(claim, fallback)` to retain the ordinary finish only for live
recovery, then calls `FinishNoAction(claim)`. General `FinishGeneration` must
reject a caller-forged `DispositionCompletedNoAction`.

- [ ] **Step 1: Replace the permissive controller test with RED authority tests**

Replace `TestDelegateControllerAttentionCompletedNoActionStaysPrivate`, which directly forges the disposition, with:

```go
func TestDelegateControllerFinishNoActionRequiresExactEligibleClaim(t *testing.T)
func TestDelegateControllerFinishNoActionRejectsMissingStaleMismatchedAndUnreadyClaims(t *testing.T)
func TestDelegateControllerFinishNoActionRejectsReportRequiredTerminalAndPreparedState(t *testing.T)
func TestDelegateControllerFinishNoActionStopUsesRetainedFallback(t *testing.T)
func TestDelegateControllerFinishNoActionAppendFailureRetainsRecoveryState(t *testing.T)
func TestDelegateControllerFinishGenerationCannotForgeNoAction(t *testing.T)
```

The stop test must assert task, worktree, scratch, usage, timing, and warnings in the retained fallback, not only status.

Also add the end-to-end incident regression before finalization production code:

```go
func TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport(t *testing.T)
```

Use `newColdStableDelegateFixture`, `restoreSupervisionRoot`, `waitForStableSupervisionRun`, and stable shell helpers. Drive one initially reported generation, one stable-owned shell completion attention, and one bare attention response. Assert one attention provider request, consumed attention, completed/private `completed_no_action`, nil packet, empty delivery ID, no pending delivery, and no second parent result notification.

Run:

```bash
go test ./agent -run '^TestDelegateControllerFinish(NoAction|GenerationCannotForge)|^TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport$' -count=1
```

Expected: compile failure because `FinishNoAction` does not exist and, after the test compiles, an incident failure showing the forced communicate/second report against pre-fix behavior.

- [ ] **Step 2: Add fallback retention under the exact claim**

Add a method used after attention resolutions and before local state publication:

```go
func (c *delegateTreeController) prepareNoAction(
    claim *delegateSettlementClaim,
    fallback delegateFinish,
) (bool, error)
```

It validates the live ordinary claim, ready fence, exact lease, `TriggerAttention`, eligible evidence, no prepared terminal, no attention IDs, and nil run error. If eligible, clone/store `fallback` in the same binding evidence and leave the claim live. If not eligible, return false without mutation.

- [ ] **Step 3: Add `FinishNoAction(claim)`**

Under `c.mu`:

- revalidate the exact claim and evidence;
- running path: internally construct `OutcomeCompleted` + `DispositionCompletedNoAction`, append only `RunFinished`, release claim/generation, and create no current-generation delivery;
- stopping path: route the retained fallback through the existing stopped branch;
- append failure: retain claim/evidence/fallback/capacity and latch existing finalization recovery;
- stale/missing/unready claim: reject.

Change general `FinishGeneration` so a caller-supplied `DispositionCompletedNoAction` is rejected unless it came through `FinishNoAction`.

- [ ] **Step 4: Wire subagent finalization**

After `AttentionResolutionsForFinalization`, construct the ordinary evidence-bearing `delegateFinish`. Query the completion decision:

- existing terminal/abnormal -> current `CompleteSettlement`/`FinishGeneration` path;
- eligible no-action -> `prepareNoAction`, skip `CompleteSettlement`, retain the claim until common final state publication, then call `FinishNoAction`;
- needs nudge -> return to Task 2's bounded continuation before acquiring final state.

Do not publish completed local state before the controller has a finish path selected.

- [ ] **Step 5: Run Task 3 tests GREEN**

```bash
go test ./agent -run '^TestDelegateControllerFinish(NoAction|GenerationCannotForge)|^TestDelegateControllerOrdinaryFinalizationAdoptsExactCoveringStop$|^TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport$' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run stable stop/recovery tests**

```bash
go test ./agent -run '^(TestDelegateResourceStop_ExternalCancellationPreservesRunEvidence|TestDelegateResourceStop_RequestFsyncPrecedesExternalCancellation|TestDelegateResourceSupervision_.*Finalization|TestDelegateController.*Settlement)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit Task 3**

```bash
git add agent/delegate_tree_finish.go agent/subagents.go agent/delegate_tree_finish_test.go agent/delegate_resource_supervision_test.go agent/delegate_tree_finish_covtest2_test.go
git commit -m "fix(delegate): finish attention no-action without delivery"
```

If `delegate_tree_finish_covtest2_test.go` is unchanged, omit it from `git add`.

---

### Task 4: Documentation and Aggregate Verification

**Files:**
- Modify: `docs/subagent-management/11-delegate-resource-model.md:937-989`
- Add: `docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md`
- Add: `docs/superpowers/plans/2026-08-29-stable-delegate-no-action.md`

**Interfaces:**
- Consumes Tasks 1–3 complete runtime behavior and the incident regression added RED in Task 3.
- Produces durable documentation and aggregate proof.

- [ ] **Step 1: Update the evergreen lifecycle documentation**

In `docs/subagent-management/11-delegate-resource-model.md`:

- state that attention-only completion requires explicit model-loop evidence and monotonic completion requirement;
- document the claim-bound `running -> idle` packetless transition;
- state that explicit terminal communicate remains on its existing path;
- state that caller-forged no-action through general `FinishGeneration` is invalid.

Do not copy the incident transcript or adjacent #569/#570/#571 designs into the evergreen doc.

- [ ] **Step 2: Re-run the already-red/green incident proof**

```bash
go test ./agent -run '^TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport$' -count=1
```

Expected: PASS. No new production special case is permitted here.

- [ ] **Step 3: Run focused aggregate tests**

```bash
go test ./agent -run 'DelegateGenerationEvidence|DelegateControllerFinishNoAction|AttentionBareText|CompletionGate|BareShellAttentionCompletesNoAction|FuzzLfRouteNoToolCalls' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit Task 4 documentation**

```bash
git add docs/subagent-management/11-delegate-resource-model.md docs/superpowers/specs/2026-08-29-stable-delegate-attention-no-action-design.md docs/superpowers/plans/2026-08-29-stable-delegate-no-action.md
git commit -m "docs: specify packetless delegate attention completion"
```

---

### Task 5: Simplification, Review, and Delivery Gates

**Files:**
- Review every path changed by Tasks 1–4.
- Modify only changed paths when a verified finding requires it.

**Interfaces:**
- Consumes complete implementation.
- Produces reviewed, gated branch ready for PR.

- [ ] **Step 1: Run simplify-code over the branch diff**

Compare against `origin/main`. Dispatch four read-only angles: reuse, simplification, efficiency, and altitude. Apply only behavior-preserving findings; do not remove tests, weaken assertions, or broaden into #569/#570/#571.

- [ ] **Step 2: Run independent correctness review**

Require reviewers to verify:

- one exact evidence authority;
- no stale-lease mutation;
- no-action only for explicit attention outcome;
- every clean exit preserves one bounded recovery nudge;
- stop and append-failure behavior;
- zero current-generation delivery;
- explicit terminal path unchanged.

Address legitimate findings with new RED tests before production fixes.

- [ ] **Step 3: Run formatting and focused tests**

```bash
gofmt -w agent/delegate_tree_controller.go agent/delegate_tree_start.go agent/delegate_tree_steer.go agent/session_lifecycle.go agent/session_tools_communicate.go agent/session_tool_registry.go agent/subagents.go agent/delegate_tree_finish.go agent/delegate_tree_completion_test.go agent/delegate_tree_steer_test.go agent/delegate_resource_supervision_test.go agent/delegate_tree_finish_test.go agent/fuzz_lf_roundcontent_test.go
go test ./agent -run 'DelegateGenerationEvidence|DelegateControllerFinishNoAction|AttentionBareText|CompletionGate|BareShellAttentionCompletesNoAction|FuzzLfRouteNoToolCalls' -count=1
go test ./agent -count=1
```

Expected: all PASS, no warnings.

- [ ] **Step 4: Run required repository gates**

```bash
make lint
make vet
make test
```

Expected: each exits 0. A timeout, launch failure, or environmental block is not a pass.

- [ ] **Step 5: Inspect the final diff and repository state**

```bash
git diff --check origin/main...HEAD
git status --short
git log --oneline --decorate origin/main..HEAD
```

Verify only named implementation, tests, spec, plan, and evergreen doc are changed.

- [ ] **Step 6: Push and open the PR**

Push `stable-delegate-no-action` to `origin`, then open a PR against `main`. The PR body must:

- summarize the incident/root cause;
- state RED/GREEN evidence and all gates;
- link #569, #570, and #571 as intentionally separate adjacent work;
- state that `ask_user` is excluded from subagents and therefore no ask recovery was added;
- list commits and changed paths;
- include `Fixes` only for a dedicated duplicate-notification issue if one exists; otherwise describe the incident without auto-closing unrelated issues.

- [ ] **Step 7: Report delivery evidence**

Report exact commands/results, staged paths, commit hashes, pushed branch, issue URLs, PR URL, and final clean/dirty status.
