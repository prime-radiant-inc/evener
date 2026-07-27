# Steering Backend Maintenance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve kata yyzz, 9tzm, hb55, x8vk, ppf1, and mjf5 with minimal, deterministic changes to steering coverage, transcript persistence, locking conventions, and stale documentation.

**Architecture:** Keep the existing steering production path unchanged except for removing the uncalled generic durable helper. Make the producer coverage test parse non-test Go files and count identifier references, extend the existing on-disk apptranscript reload harness, and update only comments/spec claims proven stale by current code.

**Tech Stack:** Go, `go/ast`, Go `testing`, JSONL transcripts, `gofmt`, kata CLI.

## Global Constraints

- Default tests remain deterministic and must not require provider credentials or network access.
- Tests must exercise real Serf behavior below scripted/external boundaries and must not assert brittle large rendered strings.
- Do not merge controller changes or close kata issues; add substantive ready comments naming commits and tests.
- Make the smallest maintainable changes, commit frequently with detailed messages, and leave the worktree clean.

---

### Task 1: yyzz producer coverage uses the AST

**Files:**
- Modify: `agent/steering_kind_coverage_test.go`

**Interfaces:**
- Consumes: parsed non-test `agent/*.go` files and `events.AllSteeringKinds`.
- Produces: a counted AST identifier-reference helper used by `TestEverySteeringKindHasAProducer`.

- [ ] **Step 1: Write the failing AST behavior test.** Add a focused test fixture containing a matching `events.SteeringKindTasksDone` selector plus a comment, string literal, bare/local identifier, and unrelated-package selector with the same spelling; assert the helper counts exactly one producer reference.
- [ ] **Step 2: Run the focused test and verify the expected red result.** Run `go test ./agent -run 'TestSteeringKindProducerReferencesRequireEventsSelector' -count=1`; it must fail because the existing helper counts non-`events` identifiers.
- [ ] **Step 3: Implement the smallest AST helper.** Parse each non-test source file with the already imported `go/parser`, walk `*ast.SelectorExpr` nodes whose package expression is the `events` identifier, count matching selector names, and replace the raw `strings.Contains` body scan with the aggregate count.
- [ ] **Step 4: Run the focused AST test and producer coverage test.** Run `go test ./agent -run 'Test(SteeringKindProducerReferencesRequireEventsSelector|EverySteeringKindHasAProducer|NoProducerPassesEmptySourceAndEmptyKindToTrySteerEnqueue)$' -count=1` and require PASS.
- [ ] **Step 5: Commit.** Stage only `agent/steering_kind_coverage_test.go` and commit with a message explaining that comments and string literals no longer count as producers.

### Task 2: Remove the dead durable helper and repair its comments

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/session_queue.go`
- Modify: `agent/session_transcript_ready_test.go`
- Modify: `agent/session_go_tail_coverage_fuzz_test.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_jobtree_drain.go`
- Modify: `agent/session_resume_turn_seed_test.go`

**Interfaces:**
- Consumes: `Session.appendSteeringTurnDurably(text, kind)` and the shared `writeTranscriptDurable` gate.
- Produces: one production durable steering append path whose explanation states that disk durability precedes the in-memory history append.

- [ ] **Step 1: Update test callers before deleting the helper.** Change the construction-window and transcript-warning tests to exercise `appendSteeringTurnDurably` with the same steering turn behavior and explicit empty kind where the test does not care about classification.
- [ ] **Step 2: Run the affected tests before deletion.** Run `go test ./agent -run 'Test(DurableTurnRecordedBeforeTranscriptWriterExistsReachesTheFile|.*Transcript.*Warning.*|.*GoTail.*)$' -count=1` with the exact available test names narrowed if needed; confirm the redirected tests pass.
- [ ] **Step 3: Delete `Session.appendTurnDurably`.** Remove only its definition from `agent/session.go`; retain `writeTranscriptDurable` and `appendSteeringTurnDurably`.
- [ ] **Step 4: Correct the survivor and surrounding comments.** Explain write-before-history ordering in `appendSteeringTurnDurably`, and replace every stale `appendTurnDurably` reference in the lifecycle, job-tree, and resume-seed comments with `appendSteeringTurnDurably` without changing behavior.
- [ ] **Step 5: Verify no production caller or stale source reference remains.** Run `rg -n 'appendTurnDurably' --glob '*.go' .` and inspect that any remaining matches are absent; run the focused agent tests and commit the cleanup with detailed durable-ordering intent.

### Task 3: Lock the six test-only Session accesses

**Files:**
- Modify: `agent/steering_kind_coverage_test.go`
- Modify: `agent/notification_test.go`
- Modify: `agent/session_tool_round_test.go`
- Modify: `agent/session_config_test.go`

**Interfaces:**
- Consumes: existing `Session.mu` conventions used by neighboring tests.
- Produces: mutex-protected reads/writes for the six existing test accesses, with no new concurrency or helper behavior.

- [ ] **Step 1: Run the focused tests and race checks against the baseline.** Run the affected test names with `go test ./agent -run 'Test(MaybeInjectTaskReminderReturnsItsKind|AcceptNotificationInput_PersistsNotificationKind|ApplyNoToolCallsDecision_PersistsNoToolCallsKind|InjectPostToolSteering_PersistsTaskReminderKind|DeliverSessionStartHookResultForUserTurn_PersistsHookContextKind)$' -count=1`, then repeat with `-race -count=3`.
- [ ] **Step 2: Add the existing lock/unlock pattern around each direct access.** Protect the one `totalRounds` write and five `history` reads exactly as surrounding tests do; copy a last turn under the lock before asserting outside it.
- [ ] **Step 3: Run the focused tests and race checks again, then commit.** Require both commands to pass and commit only the four test files with a message describing convention alignment rather than introducing fake concurrency.

### Task 4: Prove steering-kind JSON persistence through reload

**Files:**
- Modify: `internal/apptranscript/apptranscript_test.go`

**Interfaces:**
- Consumes: the existing `transcript.NewWriter` → `TurnsFromFile` → `ProjectTurn` harness.
- Produces: a deterministic assertion that a real `schema.Turn.SteeringKind` survives JSONL write/read and reaches the projected `ThreadItem`.

- [ ] **Step 1: Extend the existing persistence/reload test fixture before any production change.** Add a real steering turn with a distinguishable `events.SteeringKind` value to the current writer/projector harness and assert both the decoded projector input and projected item carry that value.
- [ ] **Step 2: Run the focused test and verify it passes on the current implementation.** Run `go test ./internal/apptranscript -run 'TestTurnsFromFileProjectorReceivesDecodedTurn' -count=1`; mutate the field expectation locally only if needed to confirm the assertion is sensitive, then restore the test fixture.
- [ ] **Step 3: Keep the extension narrow and commit.** Do not add a new transcript harness or production code; commit the existing-harness coverage with the JSON round-trip contract in the message.

### Task 5: Remove stale site-count claims from the steering spec

**Files:**
- Modify: `docs/superpowers/specs/2026-07-26-system-steering-voice-design.md`

**Interfaces:**
- Consumes: the current exact, machine-checked 17-kind statements in the testing section.
- Produces: prose that makes no exact assertion about the drifting raw injection-site count while preserving the 17-kind facts verbatim.

- [ ] **Step 1: Remove only the two stale `18` site-count assertions.** Reword the problem and insertion-point paragraphs so they describe the sites without asserting an untracked total.
- [ ] **Step 2: Verify the spec’s exact-kind facts and stale-count absence.** Run `rg -n '\b18\b|\b17\b|precise count|injection site|insertion point' docs/superpowers/specs/2026-07-26-system-steering-voice-design.md` and inspect that the 17-kind claims and non-assertion remain while the two 18-site claims are gone; commit the prose-only change.

### Task 6: Final verification and kata handoff

**Files:**
- Inspect: full branch diff and worktree status.

- [ ] **Step 1: Run all focused tests and relevant package tests for the changed packages.** Include `go test ./agent`, `go test ./internal/apptranscript`, and deterministic focused/race commands from Tasks 1–4.
- [ ] **Step 2: Run repository verification.** Run `go test ./...` if feasible and deterministic, `make build-runtime`, and `git diff --check`.
- [ ] **Step 3: Self-review the full branch diff.** Confirm no controller changes, no issue closure, no unrelated edits, no stale names, and no test that depends on credentials/network/ambient model behavior.
- [ ] **Step 4: Add substantive comments to yyzz, 9tzm, hb55, x8vk, ppf1, and mjf5.** Name the relevant commit hashes and test commands; report any concern rather than closing the kata.
- [ ] **Step 5: Commit any remaining plan or code changes, verify `git status --short` is empty, and report only status, commit hashes, test evidence, new kata ids, and concerns.**
