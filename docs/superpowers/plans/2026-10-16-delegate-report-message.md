# Delegate Report Message Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a delegate's accepted human report without replacing it with its structured summary.

**Architecture:** Keep generic session reply semantics unchanged. At both delegate completion entry points, capture the accepted report and raw structured result together under the session lock. Existing packet persistence and delivery carry the corrected message.

**Tech Stack:** Go, existing scripted-provider and delegate-journal test harnesses.

**Spec:** `docs/superpowers/specs/2026-10-16-delegate-report-message.md`

## Global Constraints

- No new public wire field, storage migration, dependency, or compatibility layer.
- Root session return values and `CommunicateOutput` retain their current structured-output semantics.
- Failed, cancelled, exhausted, and unreported runs retain their current classification and diagnostic result text.
- Existing storage limits, delivery receipts, and preview bounds remain unchanged.
- Tests compare supplied fixture content and machine fields, never product copy.
- Default tests require no provider credentials or network access.

## Review Focus

1. A custom schema with a `message` property must not redefine the human report.
2. Explicit null must remain present and validate against a null schema.
3. A losing terminal call must not contribute either message or structured output.
4. A drain turn without a report must preserve the latest accepted pair, while a new generation must reset it.
5. A run error after an accepted report must retain the current diagnostic result, rather than being reported as success.

## Task 1: Preserve the accepted report at delegate completion

**Files:**
- Modify: `agent/subagents.go`, both `stableDelegateFinish` entry points.
- Create: `agent/delegate_report_message_test.go`.
- Extend if needed: `agent/session_jobtree_drain_report_test.go`.
- Preserve unchanged: `agent/session_tools_communicate.go` and existing root-result expectations in `agent/session_communicate_atomic_test.go`.

**Interfaces:**
- Consumes: `Session.acceptCommunicateTerminal`, existing `communicateResult` state, and `delegateTerminalRunInputs`.
- Produces: `func (inputs *delegateTerminalRunInputs) captureCommunicateReport(sess *Session)`, a shared completion-boundary capture. Under `sess.mu`, populate `communicated`, `structuredResult`, and `structuredResultPresent` from `sess.comm`; for a reported run with no error, select `sess.comm.text` as `inputs.result`. A nil session leaves the run evidence untouched.
- Preserve: `stableDelegateFinishFromRun(inputs delegateTerminalRunInputs) delegateFinish` and public APIs.

- [ ] Add `TestDelegateReportMessagePreservesAcceptedCommunicate`: execute the real registered communicate handler using existing `newCompetingCommunicateSession` and `execCommunicateCall` helpers. Supply a multiline human report and a different summary with data/artifacts. Exercise the free function and subagent method completion entry points. Decode `packet.Message` and assert equality with the supplied report; compare decoded `StructuredResult` with the supplied output independently of canonicalization helpers.
- [ ] Add table cases for message-only, output-message fallback, custom-schema message-bearing objects, explicit null, and schema-invalid values. Assert source markers, presence, and validation fields independently of message text.
- [ ] Add competing-call and drain-restoration cases. Extend `TestDrainTurnKeepsTheLatestAcceptedReport` with distinct text, reply, and structured values. Use the real acceptance and existing drain wrapper, and verify neither message nor structured result comes from the losing call. Exercise a real reused runtime's next generation, including a generation with no report and a generation ending in a run error; do not simulate this by manually clearing `comm`. Verify the prior generation's human report is not inherited. Add error/cancel/exhaustion cases with a different supplied generic result and assert that result and existing outcome remain unchanged.
- [ ] Run `(cd agent && go test . -run 'TestDelegateReportMessage' -count=1)` and record the expected failure: packet message differs from the accepted report because it contains canonical structured output. Fix test construction if it fails for any other reason.
- [ ] Implement the shared capture helper and call it from both completion entry points instead of their separate communicated/structured reads. Keep packet construction, schema validation, and root communicate reply selection unchanged.
- [ ] Run `(cd agent && go test . -run 'TestDelegateReportMessage|TestCommunicate_|TestDrainTurn|TestStableDelegateFinish' -count=1)`. All selected tests must pass with no unexpected diagnostics.
- [ ] Commit the source, tests, spec, and plan by explicit paths after inspecting the staged diff. Use the repository hooks.

## Task 2: Prove durable delivery and document the contract

**Files:**
- Extend: `agent/delegate_report_message_test.go` using harnesses from `agent/delegate_delivery_test.go`, `agent/delegate_delivery_held_test.go`, and `agent/delegate_resource_tools_test.go`.
- Modify: `docs/subagent-runtime-contracts.md` and S12 in `docs/product/subsystems.md`.

**Interfaces:**
- Consumes: Task 1's corrected packet through existing controller settlement and delivery APIs.
- Produces: durable recovery regression coverage and evergreen message ownership documentation, without changing delivery logic.

- [ ] Add `TestDelegateReportMessageSurvivesDeliveryRecovery`: generate a packet through the real communicate/completion path, settle it through the controller, reopen its journal, and deliver it to the owner. Decode the delivered notification and compare the report and structured payload to the original supplied inputs. Exercise the synchronous result path in a separate fresh harness so delivery acknowledgement from one case cannot mask the other.
- [ ] Assert generation and delivery identity, then retry acknowledged delivery and verify it does not create a second owner report. Use existing journal/receiver harnesses, never a fake report-producing implementation.
- [ ] Add a prepared-before-settlement crash/reconcile case using `TestDelegateResourceRuntime_CanonicalPacketReusedAcrossFinishReplayAndDelivery` in `agent/delegate_resource_runtime_test.go`. Compare replayed message and structured output to the original supplied fixture values, not merely to another builder-generated packet.
- [ ] Run `(cd agent && go test . -run 'TestDelegateReportMessage|TestDelegateDelivery|TestDelegateResourceRuntime_CanonicalPacketReusedAcrossFinishReplayAndDelivery|TestStableDelegate|TestCommunicate_|TestDrainTurn' -count=1)` and the same selection with `-race`. All selected tests must pass. If a delivery assertion fails, diagnose it before extending production scope.
- [ ] Document that packet message comes from accepted human text, structured output remains separate, and recovery reuses the immutable packet. Keep notification timing and historical reconstruction outside this contract.
- [ ] Commit tests and docs by explicit paths after inspecting the staged diff.

## Task 3: Review and integrate

- [ ] Run the requested `simplify-code:simplify-code` workflow. Apply only behavior-preserving improvements and rerun affected checks.
- [ ] Run `roborev review --branch --wait`; inspect the actual review, fix actionable findings with red/green regressions, and rerun until satisfied. Stop and report if the same work is rejected twice without resolution.
- [ ] Run the `shepherd-pr` workflow: push once per round, open the PR, wait for current-head CI and combined review, and inspect per-commit reviews. CI owns full repository gates.
- [ ] Merge the current base before approval using repository safety checks. Merge only when repository authorization permits; report any blocker rather than claiming completion.
- [ ] Report the PR, spec and plan paths, exact local checks, CI status, and remaining notification-delay investigation.
