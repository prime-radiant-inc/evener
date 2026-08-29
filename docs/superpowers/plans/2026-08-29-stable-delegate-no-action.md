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
package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestDelegateGenerationEvidenceInitialRequirement(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_attention", "")
	attentionLease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_attention")
	attention, err := c.completionSnapshot(attentionLease)
	if err != nil {
		t.Fatalf("attention completionSnapshot: %v", err)
	}
	if attention.requirement != delegateCompletionAttentionOnly || attention.outcome != delegateCompletionOutcomeNone || attention.terminalSeen {
		t.Fatalf("attention evidence = %#v, want attention-only, empty, terminal-unseen", attention)
	}

	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	ownerLease, _ := startDelegateDeliveryGeneration(t, c, "dlg_owner", false)
	owner, err := c.completionSnapshot(ownerLease)
	if err != nil {
		t.Fatalf("owner completionSnapshot: %v", err)
	}
	if owner.requirement != delegateCompletionReportRequired || owner.outcome != delegateCompletionOutcomeNone || owner.terminalSeen {
		t.Fatalf("owner evidence = %#v, want report-required, empty, terminal-unseen", owner)
	}
}

func TestDelegateGenerationEvidenceRejectsStaleLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	finishDelegateDeliveryGeneration(t, c, first, "first")
	second, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	before, err := c.completionSnapshot(second)
	if err != nil {
		t.Fatalf("generation 2 completionSnapshot: %v", err)
	}

	if err := c.escalateCompletionRequirement(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale escalation error = %v, want stale lease", err)
	}
	if _, err := c.recordAttentionNoAction(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale no-action error = %v, want stale lease", err)
	}
	if err := c.recordTerminalSeen(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale terminal error = %v, want stale lease", err)
	}
	if _, err := c.completionSnapshot(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale snapshot error = %v, want stale lease", err)
	}

	after, err := c.completionSnapshot(second)
	if err != nil {
		t.Fatalf("generation 2 completionSnapshot after stale mutations: %v", err)
	}
	if after != before {
		t.Fatalf("generation 2 evidence changed after stale mutations: before=%#v after=%#v", before, after)
	}
}

func TestDelegateGenerationEvidenceEscalationIsMonotonic(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_target")

	if err := c.escalateCompletionRequirement(lease); err != nil {
		t.Fatalf("first escalation: %v", err)
	}
	if err := c.escalateCompletionRequirement(lease); err != nil {
		t.Fatalf("second escalation: %v", err)
	}
	if recorded, err := c.recordAttentionNoAction(lease); err != nil || recorded {
		t.Fatalf("no-action after escalation = recorded %t err %v, want refusal", recorded, err)
	}
	snapshot, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot: %v", err)
	}
	if snapshot.requirement != delegateCompletionReportRequired || snapshot.outcome != delegateCompletionOutcomeNone || snapshot.terminalSeen {
		t.Fatalf("escalated evidence = %#v, want report-required with no outcome", snapshot)
	}
}

func TestDelegateGenerationEvidenceClearsOnRelease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	if err := c.recordTerminalSeen(lease); err != nil {
		t.Fatalf("record terminal: %v", err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCompleted}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.completionSnapshot(lease); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("released snapshot error = %v, want stale lease", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if live := c.live[lease.delegateID]; live != nil && live.binding != nil {
		t.Fatalf("released generation retained binding: %#v", live.binding)
	}
}

func startDelegateAttentionEvidenceGeneration(t *testing.T, c *delegateTreeController, delegateID string) delegateLease {
	t.Helper()
	runtime := &Session{id: "child-" + delegateID, stateDir: c.stateDir, delegateController: c}
	c.mu.Lock()
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionChanged,
		DelegateID: delegateID,
		AttentionChanged: &delegatestore.DelegateAttentionChanged{
			NeedsAttention: true,
		},
	}); err != nil {
		c.mu.Unlock()
		t.Fatalf("append attention projection: %v", err)
	}
	c.live[delegateID] = &delegateLiveState{runtime: runtime}
	c.noteDelegateAttentionLocked(delegateID, "attention-"+delegateID)
	c.mu.Unlock()

	reservation, err := c.ReserveAttention(runtime, "attention-"+delegateID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := c.prepareAttentionStart(reservation, runtime, nil); err != nil {
		t.Fatalf("prepareAttentionStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart attention: %v", err)
	}
	return started.lease
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

```text
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

```text
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

Do not add fallback mutation yet; Task 3 owns fallback retention under the
finalization claim.

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
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_target")
	attachDelegateSteerRuntime(t, c, lease.delegateID, afero.NewMemMapFs())
	c.mu.Lock()
	evidence := c.live[lease.delegateID].binding.evidence
	c.mu.Unlock()
	if evidence == nil {
		t.Fatal("CommitStart retained no generation evidence")
	}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, "inspect this"); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	if _, err := completeDelegateModelRequest(c, lease); err != nil {
		t.Fatalf("complete model request: %v", err)
	}
	snapshot, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot: %v", err)
	}
	if snapshot.requirement != delegateCompletionReportRequired {
		t.Fatalf("bound steering requirement = %v, want report-required", snapshot.requirement)
	}
	c.mu.Lock()
	retained := c.live[lease.delegateID].binding.evidence
	c.mu.Unlock()
	if retained != evidence {
		t.Fatalf("bound steering replaced generation evidence: before=%p after=%p", evidence, retained)
	}
}

func TestDelegateControllerLateSteeringEscalatesOnNextRequest(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_target")
	attachDelegateSteerRuntime(t, c, lease.delegateID, afero.NewMemMapFs())

	first, err := c.BeginModelRequest(lease)
	if err != nil {
		t.Fatalf("BeginModelRequest first: %v", err)
	}
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, "late steering"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if _, err := c.CompleteModelRequest(first, first.runtime.delegateModelHistorySnapshot(), replayScope{}); err != nil {
		t.Fatalf("CompleteModelRequest first: %v", err)
	}
	before, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot before next request: %v", err)
	}
	if before.requirement != delegateCompletionAttentionOnly {
		t.Fatalf("late steering escalated first request to %v, want attention-only", before.requirement)
	}

	if _, err := completeDelegateModelRequest(c, lease); err != nil {
		t.Fatalf("complete next model request: %v", err)
	}
	after, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot after next request: %v", err)
	}
	if after.requirement != delegateCompletionReportRequired {
		t.Fatalf("next request requirement = %v, want report-required", after.requirement)
	}
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

- [ ] **Step 3: Write RED process-boundary coverage for bare attention**

Add `TestDelegateResourceSupervision_AttentionBareTextRecordsExplicitNoAction`.
Drive a real stable attention generation with a non-empty bare model response and
assert its exact completion snapshot records attention no-action without terminal
evidence. This is the process/supervision assertion for delegate eligibility.

Table-drive every generic `routeNoToolCalls(kind, noContent,
afterTerminalCommunicate)` case in `TestRouteNoToolCalls`. Keep this router
notification-only: notification acknowledgement and post-terminal silence finish
idle (#329); all other cases use `runNoToolCalls`.

Update `FuzzLfRouteNoToolCalls` to the same two-route oracle. Retain its existing
four-field fuzz input and seeds, but ignore the legacy eligibility boolean so the
checked-in corpus wire shape stays compatible.

Run:

```bash
go test ./agent -run '^(TestRouteNoToolCalls|TestDelegateResourceSupervision_AttentionBareTextRecordsExplicitNoAction)$' -count=1
go test -tags evenerfuzz ./agent -run '^FuzzLfRouteNoToolCalls$' -count=1
```

Expected: FAIL because the process boundary does not yet record exact-lease
delegate no-action; the generic router and fuzz oracle remain notification-only.

- [ ] **Step 4: Record no-action at the process boundary**

In `processOneInput`, before generic no-tool routing, handle a non-empty
`EntryDelegateAttention`: extract the exact lease from `ctx`, call
`recordAttentionNoAction`, return its error, and when accepted take the existing
idle boundary directly. If it is ineligible, continue through the generic route
and retry budget.

Keep `routeNoToolCalls` three-argument and notification-only; do not add a
delegate-specific enum. Empty attention still uses the retry budget. Root
`EntryNotification` behavior and #329 ordering remain unchanged. Run the Step 3
commands. Expected: PASS.

- [ ] **Step 5: Mark terminal communicate with the exact lease**

Retain both communicate dependency callbacks. Direct and legacy `toolDeps`
constructions continue to provide the legacy callback:

```text
// Inside the existing toolDeps struct:
setCommunicateResult        func(message, reply, output string)
setCommunicateResultContext func(context.Context, string, string, string)
```

Add and use `setCommunicateResultContext` for live `Session` registrations in
`session_tool_registry.go`; keep `setCommunicateResult` for direct/legacy
dependency constructions. In `session_tools_communicate.go`, select the
context-bearing callback when it is non-nil and otherwise fall back to the
legacy callback. Pass the tool handler's `ctx` to the context-bearing callback.
After setting `s.comm` under `s.mu`, the live callback extracts
`delegateRunLeaseContextKey` and calls `recordTerminalSeen` outside `s.mu`.
Non-stable/root calls have no lease and remain no-ops.

Add a focused test:

```go
func TestDelegateTerminalCommunicateMarksGenerationEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_target")
	c.mu.Lock()
	runtime := c.live[lease.delegateID].binding.runtime
	c.mu.Unlock()
	runtime.profile = NewOpenAIProfile("gpt-5.2")

	reg := toolpkg.NewRegistry()
	registerCommunicateTool(reg, newToolDeps(runtime))
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	if _, err := reg.Get("communicate").Exec(ctx, nil, map[string]any{
		"message":  "reported result",
		"end_turn": true,
	}); err != nil {
		t.Fatalf("communicate: %v", err)
	}
	if !runtime.Communicated() || !strings.Contains(runtime.CommunicateOutput(), "reported result") {
		t.Fatalf("reported path changed: called=%t output=%q", runtime.Communicated(), runtime.CommunicateOutput())
	}
	if recorded, err := c.recordAttentionNoAction(lease); err != nil || recorded {
		t.Fatalf("record no-action after terminal = recorded:%t err:%v, want refusal", recorded, err)
	}
	snapshot, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot: %v", err)
	}
	if !snapshot.terminalSeen || snapshot.outcome != delegateCompletionOutcomeNone {
		t.Fatalf("terminal evidence = %#v, want terminal-seen with no no-action outcome", snapshot)
	}
}
```

It must prove a later attempt to record no-action is refused while the existing reported path remains unchanged.

- [ ] **Step 6: Write RED supervision tests for every clean exit**

In `delegate_resource_supervision_test.go`, use the scripted provider and existing cold-stable harness to add:

```go
func TestDelegateResourceSupervision_AttentionBareTextRecordsExplicitNoAction(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	sub.sess.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	armStableSupervisionAttention(t, sub, "attention:no-action", "inspect the completed work")
	waitForStableSupervisionRun(t, root, fixture.childID)
	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionAttentionOnly || snapshot.outcome != delegateCompletionOutcomeAttentionNoAction || snapshot.terminalSeen {
		t.Fatalf("bare attention evidence = %#v, want explicit attention no-action", snapshot)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm report plus one bare attention response", got)
	}
}

func TestDelegateResourceSupervision_CompletionGateRecoversEveryCleanExit(t *testing.T) {
	t.Run("no-tool response cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		bare := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response")} }
		fixture.adapter.steps = []func(llm.Request) llm.Response{bare, bare, bare, bare,
			func(llm.Request) llm.Response { return finalResponse("recovered") }}
		root := restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("tool-bearing observer handoff cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		var root *Session
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				jm := sub.sess.jobManager
				receiver := root.ID()
				installWatchBelowValidation(t, jm, watchArgs{
					Target: runtimeMessageAliasCaller,
					Events: []string{"error"},
					Send:   &watchSendArgs{To: receiver, Message: "observer handoff"},
				})
				key := watchKey{VisibleSessionID: jm.sessionID, Target: runtimeMessageAliasCaller, SendTo: receiver}
				cfg := jm.watches[key]
				state := jobstore.WatchSendState{
					Key: jobstore.WatchSendKey{
						VisibleSessionID:        jm.sessionID,
						WatchTarget:             runtimeMessageAliasCaller,
						ResolvedWatchedIdentity: runtimeMessageAliasCaller,
						ResolvedSendTo:          receiver,
						WatchGeneration:         cfg.generation,
					},
					DeliveryID:               "delivery_observer_handoff",
					UpdateSeq:                1,
					Frame:                    "observer handoff frame",
					StableReceiver:           true,
					ReceiverSessionID:        receiver,
					SourceDelegateID:         fixture.delegateID,
					SourceDelegateGeneration: 1,
				}
				jm.recordWatchSendPending(state, watchSendDelivery{cfg: cfg, key: key, generation: cfg.generation, send: cfg.send})
				return communicateResponse(false, "handoff")
			},
			func(llm.Request) llm.Response { return finalResponse("recovered") },
		}
		root = restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("notification yield cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		var root *Session
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				sub.sess.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: "watch-test", Status: jobNotificationEventWatch})
				return communicateResponse(false, "work before notification")
			},
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("notification acknowledged")}
			},
			func(llm.Request) llm.Response { return finalResponse("recovered") },
		}
		root = restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("goal-controlled cap cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.Config.MaxToolRoundsPerInput = goal.GoalTurnMaxRounds
		})
		enteredFinalBare := make(chan struct{})
		releaseFinalBare := make(chan struct{})
		bare := func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("bare before continuation")}
		}
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			bare, bare, bare,
			func(llm.Request) llm.Response {
				close(enteredFinalBare)
				<-releaseFinalBare
				return bare(llm.Request{})
			},
		}
		for range goal.GoalTurnMaxRounds {
			fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response {
				return communicateResponse(false, "goal partial")
			})
		}
		fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response { return finalResponse("recovered") })
		root := restoreSupervisionRoot(t, fixture, nil)
		snapshots := make(chan delegateCompletionSnapshot, 1)
		root.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
		started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "continue goal", 0)
		if started.result.Err != nil {
			t.Fatalf("start goal-cap run: %v", started.result.Err)
		}
		<-enteredFinalBare
		steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "continue through goal-controlled cap", 0)
		if steered.result.Err != nil || steered.result.Action != "steered" {
			t.Fatalf("steer goal-cap run = %#v", steered.result)
		}
		close(releaseFinalBare)
		waitForStableSupervisionRun(t, root, fixture.childID)
		if snapshot := <-snapshots; !snapshot.terminalSeen {
			t.Fatalf("goal-cap recovery evidence = %#v, want terminal after bounded nudge", snapshot)
		}
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("blocked hook continuation cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		bare := func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("hook continuation without report")}
		}
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("warm result") },
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			bare, bare, bare, bare,
			func(llm.Request) llm.Response { return finalResponse("recovered after hook") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmStableSupervisionDelegate(t, root, fixture)
		sub.sess.hookRunner = stableSupervisionStopHook(`{"decision":"block","reason":"address hook feedback"}`)
		armStableSupervisionAttention(t, sub, "attention:blocked-hook", "run blocked hook")
		waitForStableSupervisionRun(t, root, fixture.childID)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("unblocked hook model context cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("warm result") },
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			func(llm.Request) llm.Response { return finalResponse("recovered after context") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmStableSupervisionDelegate(t, root, fixture)
		sub.sess.hookRunner = stableSupervisionStopHook(`{"hookSpecificOutput":{"additionalContext":"hook model context"}}`)
		armStableSupervisionAttention(t, sub, "attention:hook-context", "run context hook")
		waitForStableSupervisionRun(t, root, fixture.childID)
		assertSingleRecoveryNudge(t, fixture.adapter)
		requests := fixture.adapter.Requests()
		if !requestMessagesContainText(requests[len(requests)-1].Messages, "hook model context") {
			t.Fatalf("recovery request omitted unblocked hook context: %#v", requests[len(requests)-1].Messages)
		}
	})

	t.Run("post-drain owner steering cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		var root *Session
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				sub.sess.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: "post-drain", Status: jobNotificationEventWatch})
				return communicateResponse(false, "handoff")
			},
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("post-drain notification acknowledged")}
			},
			func(llm.Request) llm.Response { return finalResponse("recovered") },
			func(llm.Request) llm.Response { return finalResponse("steering handled") },
		}
		root = restoreSupervisionRoot(t, fixture, nil)
		steered := false
		root.cfg.testOnly.subagentBeforeSettlement = func(sub *subagent) {
			if steered {
				return
			}
			steered = true
			plans, err := root.delegateController.Steer(context.Background(), rootDelegateActor(root.delegateRootSessionID), fixture.delegateID, "post-drain steering")
			if err != nil {
				t.Errorf("post-drain Steer: %v", err)
				return
			}
			if err := sub.sess.executeDelegateMutationPlans(plans); err != nil {
				t.Errorf("execute post-drain steering: %v", err)
			}
		}
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
		requests := fixture.adapter.Requests()
		if !requestMessagesContainText(requests[len(requests)-1].Messages, "post-drain steering") {
			t.Fatalf("post-drain continuation omitted owner steering: %#v", requests[len(requests)-1].Messages)
		}
	})
}

func TestDelegateResourceSupervision_ExplicitAttentionCommunicateRemainsReported(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("attention report") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	sub.sess.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	armStableSupervisionAttention(t, sub, "attention:reported", "report the completed work")
	waitForStableSupervisionRun(t, root, fixture.childID)
	snapshot := <-snapshots
	if !snapshot.terminalSeen || snapshot.outcome != delegateCompletionOutcomeNone {
		t.Fatalf("explicit attention evidence = %#v, want existing reported path", snapshot)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want no recovery after explicit attention communicate", got)
	}
}

func TestDelegateResourceSupervision_UserRunWithoutCommunicateRemainsMissingTerminal(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	for range 8 {
		fixture.adapter.steps = append(fixture.adapter.steps, bare)
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	root.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionReportRequired || snapshot.outcome != delegateCompletionOutcomeNone || snapshot.terminalSeen {
		t.Fatalf("user-run evidence = %#v, want report-required missing terminal", snapshot)
	}
	assertSingleRecoveryNudge(t, fixture.adapter)
}
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
func TestDelegateControllerFinishNoActionRequiresExactEligibleClaim(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
	fallback := stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "bare attention response"})
	prepared, err := c.prepareNoAction(claim, fallback)
	if err != nil {
		t.Fatalf("prepareNoAction: %v", err)
	}
	if !prepared {
		t.Fatal("prepareNoAction rejected exact eligible claim")
	}
	plans, err := c.FinishNoAction(claim)
	if err != nil {
		t.Fatalf("FinishNoAction: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	finished := latestDelegateControllerRunFinished(t, c, "dlg_target")
	if len(plans.deliveries) != 0 || len(aggregate.PendingDeliveries) != 0 || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted ||
		finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" || finished.Packet != nil {
		t.Fatalf("completed-no-action leaked publicly: plans=%#v aggregate=%#v", plans, aggregate)
	}
	c.mu.Lock()
	_, claimLive := c.settlementClaims[claim.token]
	live := c.live["dlg_target"]
	drives := c.drivesInUse
	c.mu.Unlock()
	if claimLive || (live != nil && live.binding != nil) || drives != 0 {
		t.Fatalf("no-action finish retained authority/capacity: claim=%t live=%#v drives=%d", claimLive, live, drives)
	}
}

func TestDelegateControllerFinishNoActionRejectsMissingStaleMismatchedAndUnreadyClaims(t *testing.T) {
	t.Run("missing preparation", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction without preparation error = %v, want busy", err)
		}
	})

	t.Run("stale and mismatched", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || !prepared {
			t.Fatalf("prepareNoAction = %t, %v", prepared, err)
		}
		if _, err := c.FinishNoAction(nil); !errors.Is(err, errDelegateStaleLease) {
			t.Fatalf("FinishNoAction(nil) error = %v, want stale lease", err)
		}
		forged := *claim
		forged.lease.generation++
		if _, err := c.FinishNoAction(&forged); !errors.Is(err, errDelegateStaleLease) {
			t.Fatalf("FinishNoAction(mismatched) error = %v, want stale lease", err)
		}
	})

	t.Run("unready", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || !prepared {
			t.Fatalf("prepareNoAction = %t, %v", prepared, err)
		}
		claim.ready = make(chan struct{})
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(unready) error = %v, want busy", err)
		}
	})
}

func TestDelegateControllerFinishNoActionRejectsReportRequiredTerminalAndPreparedState(t *testing.T) {
	t.Run("non-nil run error", func(t *testing.T) {
		for name, runErr := range map[string]error{
			"cancellation": context.Canceled,
			"failure":      errors.New("run failed"),
		} {
			t.Run(name, func(t *testing.T) {
				c, _ := newDelegateControllerTestHarness(t, 1, 1)
				claim := eligibleDelegateNoActionClaimForRun(t, c, "dlg_target", runErr)
				fallback := stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "error fallback", runErr: runErr})
				if prepared, err := c.prepareNoAction(claim, fallback); err != nil || prepared {
					t.Fatalf("prepareNoAction(non-nil run error) = %t, %v, want false/nil", prepared, err)
				}
			})
		}
	})

	t.Run("report required", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, c, "dlg_target", "")
		claim, continued, err := c.BeginSettlement(delegateLease{delegateID: "dlg_target", generation: 1})
		if err != nil || continued {
			t.Fatalf("BeginSettlement = claim:%#v continued:%t err:%v", claim, continued, err)
		}
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || prepared {
			t.Fatalf("prepareNoAction(report required) = %t, %v, want false/nil", prepared, err)
		}
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(report required) error = %v, want busy", err)
		}
	})

	t.Run("terminal claim", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, c, "dlg_target", "")
		lease := delegateLease{delegateID: "dlg_target", generation: 1}
		claim, continued, err := c.BeginFinalization(lease, delegateSettlementTerminal)
		if err != nil || continued {
			t.Fatalf("BeginFinalization(terminal) = claim:%#v continued:%t err:%v", claim, continued, err)
		}
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || prepared {
			t.Fatalf("prepareNoAction(terminal) = %t, %v, want false/nil", prepared, err)
		}
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(terminal) error = %v, want busy", err)
		}
	})

	t.Run("prepared terminal", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
		c.mu.Lock()
		_, appendErr := c.appendLocked(delegatestore.Event{
			Kind:       delegatestore.EventDelegateTerminalPrepared,
			DelegateID: "dlg_target",
			TerminalPrepared: &delegatestore.TerminalPrepared{
				Generation: 1,
				Packet:     delegateMissingTerminalPacket(),
			},
		})
		c.mu.Unlock()
		if appendErr != nil {
			t.Fatalf("append prepared terminal: %v", appendErr)
		}
		if prepared, err := c.prepareNoAction(claim, delegateFinish{}); err != nil || prepared {
			t.Fatalf("prepareNoAction(prepared terminal) = %t, %v, want false/nil", prepared, err)
		}
		if _, err := c.FinishNoAction(claim); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("FinishNoAction(prepared terminal) error = %v, want busy", err)
		}
	})
}

func TestDelegateControllerFinishNoActionStopUsesRetainedFallback(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
	startedAt := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	endedAt := startedAt.Add(4 * time.Minute)
	activityAt := startedAt.Add(3 * time.Minute)
	fallback := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result:           "retained fallback",
		descriptor:       delegatestore.Descriptor{Task: "inspect task", Description: "inspect description"},
		startedAt:        startedAt,
		endedAt:          endedAt,
		latestActivityAt: activityAt,
		usage:            schema.CumulativeUsage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, TotalTokens: 21},
		warnings:         []string{"retained warning"},
		worktree:         &delegateWorktreeReport{Path: "/tmp/worktree", Branch: "task-3", HeadSHA: "deadbeef", Ahead: 2, Dirty: true},
		scratchPath:      "/tmp/scratch",
	})
	if prepared, err := c.prepareNoAction(claim, fallback); err != nil || !prepared {
		t.Fatalf("prepareNoAction = %t, %v", prepared, err)
	}
	// The stopping finish must use the controller-retained clone, not this caller value.
	fallback.packet.Warnings[0] = "mutated warning"
	fallback.packet.Metadata[0] = 'X'
	appendDelegateControllerStopRequest(t, c, "dlg_target")

	plans, err := c.FinishNoAction(claim)
	if err != nil {
		t.Fatalf("FinishNoAction under stop: %v", err)
	}
	finished := latestDelegateControllerRunFinished(t, c, "dlg_target")
	if finished.Outcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("stopped outcome = %q, want %q", finished.Outcome.Status, delegatestore.OutcomeStopped)
	}
	if finished.Outcome.Reason != "stopped_by_parent" {
		t.Fatalf("stopped reason = %q, want stopped_by_parent", finished.Outcome.Reason)
	}
	if finished.Disposition != delegatestore.DispositionTerminalError {
		t.Fatalf("stopped disposition = %q, want %q", finished.Disposition, delegatestore.DispositionTerminalError)
	}
	if finished.DeliveryID == "" {
		t.Fatal("stopped delivery ID is empty")
	}
	if len(plans.deliveries) != 0 {
		t.Fatalf("stopped delivery plans = %#v, want none for covered owner", plans.deliveries)
	}
	if finished.Packet == nil {
		t.Fatal("stopped packet is nil")
	}
	if finished.Packet.Kind != delegatestore.PacketTerminalError {
		t.Fatalf("stopped packet kind = %q, want %q", finished.Packet.Kind, delegatestore.PacketTerminalError)
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(finished.Packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode retained fallback metadata: %v", err)
	}
	if metadata.Task != "inspect task" {
		t.Fatalf("retained task = %q, want inspect task", metadata.Task)
	}
	if metadata.Worktree == nil {
		t.Fatal("retained worktree is nil")
	}
	if metadata.Worktree.Path != "/tmp/worktree" {
		t.Fatalf("retained worktree path = %q, want /tmp/worktree", metadata.Worktree.Path)
	}
	if metadata.Worktree.Branch != "task-3" {
		t.Fatalf("retained worktree branch = %q, want task-3", metadata.Worktree.Branch)
	}
	if metadata.Worktree.HeadSHA != "deadbeef" {
		t.Fatalf("retained worktree head = %q, want deadbeef", metadata.Worktree.HeadSHA)
	}
	if metadata.Worktree.Ahead != 2 {
		t.Fatalf("retained worktree ahead = %d, want 2", metadata.Worktree.Ahead)
	}
	if !metadata.Worktree.Dirty {
		t.Fatal("retained worktree dirty = false, want true")
	}
	if metadata.ScratchPath != "/tmp/scratch" {
		t.Fatalf("retained scratch path = %q, want /tmp/scratch", metadata.ScratchPath)
	}
	if metadata.CumulativeUsage == nil {
		t.Fatal("retained cumulative usage is nil")
	}
	if metadata.CumulativeUsage.InputTokens != 11 {
		t.Fatalf("retained input tokens = %d, want 11", metadata.CumulativeUsage.InputTokens)
	}
	if metadata.CumulativeUsage.OutputTokens != 7 {
		t.Fatalf("retained output tokens = %d, want 7", metadata.CumulativeUsage.OutputTokens)
	}
	if metadata.CumulativeUsage.CacheReadTokens != 3 {
		t.Fatalf("retained cache-read tokens = %d, want 3", metadata.CumulativeUsage.CacheReadTokens)
	}
	if metadata.CumulativeUsage.TotalTokens != 21 {
		t.Fatalf("retained total tokens = %d, want 21", metadata.CumulativeUsage.TotalTokens)
	}
	if metadata.RunStartedAt != startedAt.Format(time.RFC3339Nano) {
		t.Fatalf("retained start time = %q, want %q", metadata.RunStartedAt, startedAt.Format(time.RFC3339Nano))
	}
	if metadata.RunEndedAt != endedAt.Format(time.RFC3339Nano) {
		t.Fatalf("retained end time = %q, want %q", metadata.RunEndedAt, endedAt.Format(time.RFC3339Nano))
	}
	if metadata.LatestActivityAt != activityAt.Format(time.RFC3339Nano) {
		t.Fatalf("retained latest activity = %q, want %q", metadata.LatestActivityAt, activityAt.Format(time.RFC3339Nano))
	}
	if !reflect.DeepEqual(finished.Packet.Warnings, []string{"retained warning"}) {
		t.Fatalf("retained warnings = %#v, want retained warning", finished.Packet.Warnings)
	}
}

func TestDelegateControllerFinishNoActionAppendFailureRetainsRecoveryState(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	claim := eligibleDelegateNoActionClaim(t, c, "dlg_target")
	if prepared, err := c.prepareNoAction(claim, stableDelegateFinishFromRun(delegateTerminalRunInputs{result: "fallback"})); err != nil || !prepared {
		t.Fatalf("prepareNoAction = %t, %v", prepared, err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := c.FinishNoAction(claim); err == nil {
		t.Fatal("FinishNoAction succeeded after store close")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live["dlg_target"]
	if c.settlementClaims[claim.token] != claim || live == nil || live.binding == nil || live.binding.evidence == nil || live.binding.evidence.fallback == nil ||
		!live.recoveryRequired || !live.finalizationRecoveryRequired || !live.recoveryRunnerPending || c.drivesInUse != 1 || !c.durable["dlg_target"].CurrentRunOpen {
		t.Fatalf("append failure recovery state = claim:%#v live:%#v drives:%d aggregate:%#v", c.settlementClaims[claim.token], live, c.drivesInUse, c.durable["dlg_target"])
	}
}

func TestDelegateControllerFinishGenerationCannotForgeNoAction(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	plans, err := c.FinishGeneration(lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionCompletedNoAction,
	})
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("FinishGeneration forged no-action error = %v, want busy", err)
	}
	if len(plans.deliveries) != 0 || !c.durable["dlg_target"].CurrentRunOpen || c.live["dlg_target"].binding == nil {
		t.Fatalf("forged no-action mutated controller: plans=%#v aggregate=%#v live=%#v", plans, c.durable["dlg_target"], c.live["dlg_target"])
	}
}

func eligibleDelegateNoActionClaim(t *testing.T, c *delegateTreeController, delegateID string) *delegateSettlementClaim {
	return eligibleDelegateNoActionClaimForRun(t, c, delegateID, nil)
}

func eligibleDelegateNoActionClaimForRun(t *testing.T, c *delegateTreeController, delegateID string, runErr error) *delegateSettlementClaim {
	t.Helper()
	seedDelegateControllerIdle(t, c, delegateID, "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, delegateID)
	if recorded, err := c.recordAttentionNoAction(lease); err != nil || !recorded {
		t.Fatalf("recordAttentionNoAction = %t, %v", recorded, err)
	}
	claim, continued, err := c.BeginRunFinalization(lease, delegateSettlementOrdinary, runErr)
	if err != nil || continued {
		t.Fatalf("BeginSettlement = claim:%#v continued:%t err:%v", claim, continued, err)
	}
	<-claim.ready
	c.mu.Lock()
	c.live[delegateID].attentionIDs = nil
	c.mu.Unlock()
	return claim
}
```

The stop test must assert task, worktree, scratch, usage, timing, and warnings in the retained fallback, not only status.

Also add the end-to-end incident regression before finalization production code:

```go
func TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)

	shell := createStableDelegateShell(t, sub.sess.jobManager, "bare attention incident")
	finishStableDelegateShell(t, sub.sess.jobManager, shell.JobID)
	waitForStableSupervisionRun(t, root, fixture.childID)

	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm report plus one bare shell-attention response", got)
	}
	stored := loadStableShellRecord(t, sub.sess.jobManager, shell.JobID)
	attentionID := stableShellAttentionID(shell.JobID, stored.TerminalGen)
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, sub.sess.ID()), sub.sess.ID())
	if err != nil {
		t.Fatalf("read shell attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("shell attention %q resolution = %q, want consumed", attentionID, got)
	}

	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted ||
		finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.Packet != nil || finished.DeliveryID != "" ||
		len(aggregate.PendingDeliveries) != 0 {
		t.Fatalf("private no-action completion = finished:%#v aggregate:%#v", finished, aggregate)
	}
	parentPending, err := readPendingDelegateAttention(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read parent attention: %v", err)
	}
	if len(parentPending) != 0 {
		t.Fatalf("parent pending attention = %#v, want no second result notification", parentPending)
	}
}
```

Use `newColdStableDelegateFixture`, `restoreSupervisionRoot`, `waitForStableSupervisionRun`, and stable shell helpers. Drive one initially reported generation, one stable-owned shell completion attention, and one bare attention response. Assert one attention provider request, consumed attention, completed/private `completed_no_action`, nil packet, empty delivery ID, no pending delivery, and no second parent result notification.

Run:

```bash
go test ./agent -run '^TestDelegateControllerFinish(NoAction|GenerationCannotForge)|^TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport$' -count=1
```

Expected: compile failure because `FinishNoAction` does not exist and, after the test compiles, an incident failure showing the forced communicate/second report against pre-fix behavior.

- [ ] **Step 2: Add fallback retention under the exact claim**

Add a method used after attention resolutions and before local state publication:

```text
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
