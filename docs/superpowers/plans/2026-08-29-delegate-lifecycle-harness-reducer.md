# Implementation plan: differential harness + finish-path reducer

Date: 2026-08-29 (revised after adversarial spec review)
Spec: `docs/superpowers/specs/2026-08-29-delegate-lifecycle-harness-reducer-design.md`
Branch: `lifecycle-reducer` (stacked on `lifecycle-dedup` / PR #582)
Process per task: one implementer subagent → two competing adversarial
reviewers → adjudicated fix wave → scoped re-review → four-angle simplify
pass → gates. SDD working files (briefs/reports/ledger) stay uncommitted.

## Task 1: harness skeleton and core invariants

Implement the deterministic runner, the model state machine, the binding-kind
seed dimension, operations 1–12 (all except append-failure and crash), and
invariants I1–I5 (I1 asserted outside the crash window, which Task 2
introduces).

- `agent/delegate_lifecycle_differential_test.go` (**untagged**):
  deterministic runner translating a byte sequence into the operation
  sequence, plus ordinary `testing.T` drivers over the checked-in seeds.
- `agent/delegate_lifecycle_model_test.go` (**untagged**): the model
  transition table over the spec's model state (phase, runOpen,
  stopPending, reportRequired, terminalSeen, delivered, evidencePresent,
  evidenceRequirement, exhaustion-payload source) and the per-op
  agreement checker.
- Reuse `newColdStableDelegateFixture`, testkit `newSession` builders,
  existing fake adapters, and the nil-evidence manual-binding seeding
  precedent in `delegate_tree_controller_test.go`. No production changes.
- Run-end error injection drives the fake adapter with the selected
  terminal error (nil / generic / budget / canceled /
  `errors.Join(budget, context.Canceled)`), optionally racing cancel.
- Determinism: scripted clock on the root runtime, existing barriers, no
  sleeps, no `StopSubtreeAndDrive` goroutine (use `StopSubtree` +
  synchronous drain).
- Corpus seeds: bare-attention-then-queued-work; stop-before-finish;
  attention-no-action finish; terminal-communicate finish;
  nudge-then-report; nil-evidence steering; joined cancel+budget
  exhaustion; delivery execute+ack.

Gate: `go test ./agent -run 'Differential' -count=1` — must match and run
real tests (untagged files; a vacuous match is a delivery failure).
Review gate: adversarial pair — angle: prove by hand, with a mutation per
historical bug (#580 no-action selection, nil-evidence tolerance,
exhaustion overwrite), that the harness catches all three.

## Task 2: append failure, crash/recovery, fuzz registration

Add operations 13–14 and invariants I6–I7; register the fuzz target.

- Append failure: `c.store.Close()`; when the sequence continues,
  reopen and swap `c.store` under `c.mu` (existing agent-side precedent;
  no production seam changes).
- Crash simulation: discard runtime state without finalizing; snapshot
  journal + durable transcripts + job logs at the boundary; run the real
  restore path; assert I7 (recovery may consume those durable inputs,
  never memory-only state).
- `agent/delegate_lifecycle_differential_fuzz_test.go`
  (`//go:build evenerfuzz`): the `testing.F` target feeding bytes into the
  Task 1 runner.
- Register in `scripts/fuzz/fuzz-targets.txt` (native format, add
  `coverpkg` if coverage spans `agent` root and
  `agent/internal/delegatestore`); `make lint-fuzz-registry` passes; seed
  corpus replays green under the tag.

Gate: `go test -tags evenerfuzz ./agent -run 'FuzzDelegateLifecycleDifferential' -count=1`
plus two consecutive full-corpus runs with identical results.
Review gate: adversarial pair — angle: race/timing-dependence hunt,
I7 input-model correctness, recovery-window enumeration completeness.

## Task 3: finish-path intent reducer

Extract the reducer per the spec's scope and guard inventory. Zero
behavior diff.

- New `agent/delegate_tree_intents.go`: intent types,
  `reduceFinishIntent` (locked; no lock acquisition, no journal I/O, no
  Session method calls — runtime pointer-identity comparison permitted),
  `reduceFinishAppendResult` (applies the reducer's per-site latch plan).
- Wrappers (`FinishGeneration`, `FinishNoAction`, `CompleteSettlement`,
  `AttentionResolutionsForFinalization`, `BeginSettlement`,
  `BeginFinalization`, `BeginRunFinalization`, `SupervisionBoundary`,
  `RequireFinalizationRecovery`, `ReportFinalizationQuiesced`,
  `prepareNoAction`, `noActionFinishLocked`, `finishGenerationLocked`,
  `completionDecision`, the `record*`/`escalate*` methods) become: intent
  construction → reduce → `appendLocked` → post-append plan construction
  from the reducer's descriptors (branching only on the decision) → effect
  application. Signatures unchanged.
- Per-site latch shapes preserved exactly (unconditional triple /
  lease-conditional triple / conditional single-flag — no uniform
  latching).
- Adjacent finish sites (start-failure and recovery finishers) are NOT
  touched; the review package names them as deferred per spec.
- Review package includes the hand-audited manifest: every wrapper body,
  every guard's single home, proof no wrapper branches on
  aggregate/live/controller state.

Gate: full `go test ./agent -count=1` + harness corpus + incident
regressions, zero test modifications.
Review gate: adversarial pair (guard-by-guard equivalence against
pre-refactor source; append-before-publish, cancel-after-unlock,
post-append plan ordering; latch-shape fidelity) + four-angle simplify
pass.

## Task 4: documentation and gates

- Update `docs/subagent-management/11-delegate-resource-model.md`: the
  reducer as the generation-finish-path authority (with the deferred
  adjacent sites named), the harness as the cross-layer agreement gate.
- Full gates: focused suites, `go test ./agent -count=1`, `make lint`,
  `make vet`, `make test`.
- Final whole-branch review; push; open PR stacked on `lifecycle-dedup`
  (retarget to `main` when #582 merges).

## Explicit exclusions

- No schema/journal change, no journaled evidence, no actor model.
- No #569/#570/#571 behavior, no `ask_user` changes.
- No production changes in Tasks 1–2 (injection uses the existing
  `c.store.Close()`/reopen-swap mechanism).
- No committing SDD working files.
- No touching start-failure or recovery finishers in Task 3.
