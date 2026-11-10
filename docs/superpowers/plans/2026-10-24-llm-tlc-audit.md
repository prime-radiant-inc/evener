# LLM Core Formal Audit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Find state-machine bugs with TLC, confirm them in real-library unit tests, and fix only confirmed bugs.

**Architecture:** Small TLA+ models mirror stream publication, retry admission, and completed tool-round settlement. Model traces are hypotheses until independent Go tests reproduce them. Each confirmed finding gets a minimal production fix and a bounded post-fix check.

**Tech Stack:** TLA+, TLC on Java 21, Go, existing scripted provider test fixtures.

**Spec:** `docs/superpowers/specs/2026-10-24-llm-tlc-audit-design.md`

## Global Constraints

- Work only in the isolated audit worktree; leave existing untracked work untouched.
- No runtime dependency and no live provider request.
- Real library behavior is the test oracle; no internal mocks or prose assertions.
- Red test before each fix, green afterward; regression tests run by default.
- Bounded TLC success is not a proof of the entire implementation.
- Keep local checks targeted and send broad regression gates to CI.

## Review Focus

- Non-text content, including reasoning and tool arguments, can reach callers before failure.
- A retryable error can follow an empty/control-only stream and should retain valid recovery.
- Cancellation can occur during a backoff or after a completed tool round.
- Rate limits use a different budget from ordinary retryable errors.
- A completed tool round has observable side effects even if the next model call fails.

## Task 1: Establish reproducible bounded models

**Files:** Create `docs/formal/llm/README.md`, `StreamCommit.tla`, `StreamSteps.tla`, `RetryBudget.tla`, and matching `.cfg` files.

**Interfaces:** Consume source transitions in `llm/stream_generate.go`, `llm/stream_retry.go`, and `llm/retry_util.go`. Produce named invariants and TLC counterexample traces with action-to-source mappings.

- [ ] Inspect each complete source path and relevant tests before translating it.
- [ ] Obtain a version-pinned `tla2tools.jar`, record its checksum, and run TLC's version/help entry point.
- [ ] Model stream publication using text, reasoning, tool, and control events; begin with two attempts and two events per attempt. Check that a retry cannot follow unreset published content.
- [ ] Model retry admission using finite retry and wall-clock budgets, explicit caller cancellation, and a progressing fake clock. Check termination under stated fairness and that no admitted attempt violates its owning budget.
- [ ] Model completed tool rounds and terminal success/failure. Check that published final bookkeeping retains completed results and usage, and execution cannot exceed the round limit.
- [ ] Run every model with one TLC worker and a bounded Java heap. Record exact commands, bounds, verdicts, and shortest counterexamples.
- [ ] Review whether each counterexample input is admitted by the real library. Reject model artifacts rather than inventing production fixes.

## Task 2: Confirm and fix stream-publication findings

**Files:** Create `llm/stream_generate_formal_test.go`; modify `llm/stream_generate.go` and `llm/README.md` only for confirmed behavior.

**Interfaces:** Use `scriptedStreamAdapter`, `NewChanStream`, and `StreamGenerate(context.Context, GenerateOptions) (*StreamResult, error)` to replay model traces.

- [ ] For any publication counterexample, add `TestStreamGenerate_RetryAfterPublishedContent` with subcases for text, reasoning, and tool events. Assert request count, structured event identities, and terminal error identity. Include an empty/control-only transient failure that still retries successfully.
- [ ] Run `go test ./llm -run '^TestStreamGenerate_RetryAfterPublishedContent$' -count=1 -v`; confirm the suspected cases fail for the predicted behavior.
- [ ] State up to five hypotheses in likelihood order; use the smallest source/test check to identify the cause.
- [ ] Make the smallest production change at the confirmed cause. Do not alter unrelated retry ownership.
- [ ] Run the same test and the directly related existing stream-retry tests; require zero failures.
- [ ] Update the model/configuration to represent the fix and rerun TLC. Retain a reproducible pre-fix setting.
- [ ] Stage only the task's files, read the staged diff, and commit with hooks enabled.

## Task 3: Confirm and fix completed-round findings

**Files:** Extend `llm/stream_generate_formal_test.go`; modify `llm/stream_generate.go` and `llm/README.md` only for confirmed behavior.

**Interfaces:** Read results through `StreamResult.Steps()`, `TotalUsage()`, and `Response()`. Drive real tool handlers through `GenerateOptions.Tools`.

- [ ] For any retention counterexample, add `TestStreamGenerate_PreservesCompletedStepsOnFailure`. Drive one successful tool round followed by a permanent open failure, truncated stream, or deterministic cancellation. Assert the tool's execution count, completed step identity and tool result, usage, and the terminal error.
- [ ] Run `go test ./llm -run '^TestStreamGenerate_PreservesCompletedStepsOnFailure$' -count=1 -v`; confirm failure matches the trace rather than test setup.
- [ ] Record and test causal hypotheses before modifying production code.
- [ ] Fix completed-round publication at its owning boundary, reusing existing synchronization.
- [ ] Rerun the reproducer and directly related successful tool-loop tests; require zero failures.
- [ ] Rerun the corrected model and document bounds and limitations.
- [ ] Stage exact files, read the staged diff, and commit with hooks enabled.

## Task 4: Review and deliver the audit

**Files:** Finalize `docs/formal/llm/README.md`, owning product documentation if a contract changes, and checker configurations.

- [ ] Obtain independent review of source/model correspondence, realistic inputs, regression tests, and minimal fixes. Include cancellation, control-only retries, and tool-round bounds in the brief.
- [ ] Resolve every valid finding with red-green evidence; document disproved findings with source evidence.
- [ ] Run all documented bounded checker commands and the narrow changed-test selection, including race detection for shared-result publication where practical.
- [ ] Review the full diff, run formatting and whitespace checks, and report what broad checks were not run locally.
- [ ] Preserve reproducible model/configuration artifacts; remove disposable downloaded tools and TLC state output from scratch.
- [ ] Report each confirmed bug, user impact, reproduction command, fix, and the audit's unmodeled boundaries.

This is an investigation plan: Tasks 2 and 3 are conditional on actual checker
traces and Go reproductions. If either hypothesis is disproved, record that
result rather than making the planned production change. A new class of finding
requires its own concrete reproduction and minimal fix before being called a bug.
