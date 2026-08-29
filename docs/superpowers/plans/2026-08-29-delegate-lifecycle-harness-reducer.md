# Implementation plan: differential harness + finish-path reducer

Date: 2026-08-29
Spec: `docs/superpowers/specs/2026-08-29-delegate-lifecycle-harness-reducer-design.md`
Branch: `lifecycle-reducer` (stacked on `lifecycle-dedup` / PR #582)
Process per task: one implementer subagent → two competing adversarial
reviewers → adjudicated fix wave → scoped re-review → four-angle simplify
pass → gates. SDD working files (briefs/reports/ledger) stay uncommitted.

## Task 1: harness skeleton and core invariants

Implement the differential driver, the model state machine, operations
1–5 and 8–9, and invariants I1–I5.

- `agent/delegate_lifecycle_differential_test.go`: fuzz target +
  deterministic runner translating fuzz bytes into the operation sequence.
- `agent/delegate_lifecycle_model_test.go`: the model transition table
  (phases, open-run, stop-pending, report-required, terminal-seen,
  delivered) and the per-op agreement checker.
- Reuse `newColdStableDelegateFixture`, testkit `newSession` builders,
  and existing fake adapters. No production changes.
- The model's transition table cites the spec's invariants for each rule.
- Corpus seeds: bare-attention-then-queued-work; stop-before-finish;
  attention-no-action finish; terminal-communicate finish; nudge-then-report.

Tests: the new tests pass; `go test ./agent -run 'Differential' -count=1`.
Review gate: adversarial pair (angle: can the harness actually catch the
three historical bugs — prove each by hand with a mutation).

## Task 2: fault injection, crash/recovery, fuzz registration

Add operations 7, 10, 11 and invariants I6–I7; register the target.

- Reuse the existing append-failure injection seam from the recovery
  tests; if none is reusable without modification, report back before
  adding one (that would be a production seam change).
- Crash simulation: drop the runtime binding without finalizing, run the
  real restore path, assert I7 convergence from the journal alone.
- Register in `scripts/fuzz/fuzz-targets.txt`; `make lint-fuzz-registry`
  and the target's seed corpus pass under `make fuzz-seeds`-equivalent
  invocation for this target.
- Determinism proof: run the full seed corpus twice, identical results;
  no sleeps introduced (grep check in review).

Tests: `go test -tags evenerfuzz ./agent -run 'FuzzDelegateLifecycleDifferential' -count=1`.
Review gate: adversarial pair (angle: race/timing dependence hunt,
invariant completeness vs. the bug catalog, model-table correctness).

## Task 3: finish-path intent reducer

Extract the reducer per the spec's guard inventory. Zero behavior diff.

- New `agent/delegate_tree_intents.go`: intent types, `reduceFinishIntent`
  (locked, no I/O/locks/Session), `reduceFinishAppendResult`.
- Wrappers (`FinishGeneration`, `FinishNoAction`, `CompleteSettlement`,
  `BeginRunFinalization` path, `prepareNoAction`, `noActionFinishLocked`,
  `finishGenerationLocked`) become: construct intent → reduce →
  `appendLocked` → apply effects. Signatures unchanged.
- Every spec-table guard textually inside the reducer; the review package
  includes a grep manifest proving no guard remains at call sites.
- The differential harness and the full existing suite run unchanged.

Tests: full `go test ./agent -count=1` + harness corpus.
Review gate: adversarial pair (angle: guard-by-guard equivalence proof
against pre-refactor source; ordering: append-before-publish,
cancel-after-unlock, recovery-latch-on-append-failure) + four-angle
simplify pass.

## Task 4: documentation and gates

- Update `docs/subagent-management/11-delegate-resource-model.md` to name
  the reducer as the finish-path authority and the harness as the
  cross-layer agreement gate.
- Full gates: focused suites, `go test ./agent -count=1`, `make lint`,
  `make vet`, `make test`.
- Final review of the whole branch diff; push; open PR stacked on
  `lifecycle-dedup` (retarget to `main` when #582 merges).

## Explicit exclusions

- No schema/journal change, no journaled evidence, no actor model.
- No #569/#570/#571 behavior, no `ask_user` changes.
- No production seam changes in Tasks 1–2 without parent approval.
- No committing SDD working files.
