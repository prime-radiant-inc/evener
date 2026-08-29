# Delegate lifecycle: differential harness and finish-path intent reducer

Date: 2026-08-29
Status: design for implementation
Depends on: PR #580 (merged), PR #582 (`classifyRunEnd`, `supervisionSuppressedLocked`)

## Problem

Every significant defect found in the stable-delegate lifecycle work is the
same defect in different clothes: **two copies of a decision disagreed.**

- #580's root cause: the durable store knew `completed_no_action`; the active
  runtime path had no way to select it.
- The nil-evidence regression: one escalation path validated evidence
  strictly; a legacy path did not, and consolidating them changed behavior.
- The exhaustion-overwrite divergence: the decision to replace the run error
  existed in two forms (switch branch vs. payload presence) and they split
  under a joined cancel+budget error.

Today the finish path's guards — exact-lease authentication, phase checks,
stop precedence, suppression, no-action eligibility, prepared-terminal
interplay, append-failure latching — are spread across
`SupervisionBoundary`, `admitLeaseLocked`, `BeginSettlement`,
`BeginFinalization`, `BeginRunFinalization`, `prepareNoAction`,
`noActionFinishLocked`, `finishGenerationLocked`, `CompleteSettlement`, and
the `record*`/`escalate*` methods. Each mutator re-validates its own slice.
There is no single place to read to learn what may happen at finalization,
and no test that drives the runtime and durable layers through adversarial
sequences and asserts they still agree.

This spec covers two projects, delivered in one branch in dependency order:

1. **Differential lifecycle harness** (test-only): drive randomized,
   deterministic operation sequences through the real controller and assert
   cross-layer agreement after every operation, including across simulated
   crash/recovery.
2. **Finish-path intent reducer** (production, behavior-preserving):
   relocate every finish-path guard into one locked transition function;
   existing mutators become intent constructors and effect executors.

The harness ships first because it is the proof net for the reducer, and it
remains permanently valuable after the reducer lands.

## Project 1: differential lifecycle harness

### Shape

- Test-only. New files under `agent/` (e.g.
  `delegate_lifecycle_differential_test.go` plus a model/helper file). No
  production code changes.
- A Go native fuzz target (`testing.F`) derives a deterministic operation
  sequence from the fuzz input. Seeds for the historically interesting
  sequences are checked in as corpus; the target registers in
  `scripts/fuzz/fuzz-targets.txt` (four-column native format) so
  `lint-fuzz-registry` and `make fuzz` cover it.
- The driver applies each operation to a **real** `delegateTreeController`
  using the existing scripted fixtures (`newColdStableDelegateFixture`,
  `newSession`/`withAdapter`/`withConfig` testkit, fake adapters), never a
  mock of the controller itself.
- An independent **model state machine** lives in the test file: a small
  transition table over (phase, open-run, stop-pending, report-required,
  terminal-seen, delivered) that is the harness's own opinion of the rules.
  The model is intentionally a third, tiny, auditable copy — that is what
  differential testing is. It tracks phases and outcomes only, never packet
  contents, to keep its maintenance cost bounded.

### Operation vocabulary

The driver maps input bytes onto these operations, applied in sequence:

1. start a generation (trigger: user work / shell attention);
2. admit report-requiring work (user input, follow-up, goal continuation,
   steering bind, hook output);
3. admit system-only work (attention, notification);
4. bare attention response (records attention no-action when eligible);
5. terminal communicate (sets terminalSeen);
6. needs-nudge decision followed by the bounded nudge;
7. stop request (before finalization, and racing finalization);
8. ordinary finish (`FinishGeneration`);
9. no-action finish (`FinishNoAction` via the claim path);
10. injected append failure (reuse the existing recovery-test injection
    seam; do not add a new one), optionally followed by a retry;
11. simulated crash: discard runtime state without finalizing, then run the
    real restore/replay path and let recovery settle.

### Invariants (asserted after every operation and after recovery)

- **I1 — single authority:** at most one open generation per delegate;
  `CurrentRunOpen` in the durable aggregate and the presence of a live
  binding differ only inside the explicitly enumerated recovery windows
  (append-failure latch; crash-before-restore). The windows are listed in
  the test, not discovered by it.
- **I2 — legal transitions:** the durable phase sequence is a path in the
  model's transition table.
- **I3 — stop precedence:** once a stop is durably recorded, the terminal
  outcome is never `completed` or `completed_no_action`.
- **I4 — delivery uniqueness:** at most one parent delivery per generation,
  and none for `completed_no_action`.
- **I5 — report requirement:** if any report-requiring work was admitted,
  the final outcome is not `completed_no_action`.
- **I6 — durable truth:** replaying the journal through
  `delegatestore`'s fold yields the same aggregate the controller holds;
  no parent-visible effect exists without its journal event.
- **I7 — recovery determinism:** recovery after a crash at any operation
  boundary converges to the state the model predicts from the journal
  alone; recovered state never depends on what was only in pre-crash
  memory.

### Determinism requirements

No wall-clock, no sleeps, no network, no provider credentials. Scripted
clock and adapters; synchronization only through the existing test
barriers/condition waits. A failing seed must reproduce byte-for-byte from
the fuzz input alone.

## Project 2: finish-path intent reducer

### Shape

- Production change, behavior-preserving. No signature changes to existing
  exported/plan-interface methods (`FinishGeneration`, `FinishNoAction`,
  `BeginSettlement`, `BeginFinalization`, `BeginRunFinalization`,
  `CompleteSettlement`, `SupervisionBoundary`, `AttentionResolutionsForFinalization`).
- New typed intents describing what a caller wants:
  `finishIntent{ordinary{lease, finish}}`,
  `finishIntent{noAction{claim}}`, settlement completion, and the
  escalation/evidence observations (`workAdmitted`, `attentionNoAction`,
  `terminalSeen`) insofar as they feed finish-path decisions.
- One locked transition function (working name `reduceFinishIntent`), in
  the `classifyRunEnd` idiom but operating on controller state: the caller
  holds `c.mu`; the reducer performs **no** lock acquisition, no Session
  access, and no journal I/O. It returns the decision: events to append,
  mutation plans to execute, cancellation to run after unlock, and the
  recovery latches to set on append failure.
- Commit stays where it is: wrappers append through the existing
  `appendLocked` path, then apply the reducer's returned effects. The
  append-failure reconciliation (setting `recoveryRequired` /
  `finalizationRecoveryRequired`) is a second, explicit reducer step
  (`reduceFinishAppendResult`) so post-commit decisions also live in one
  place.

### Guard inventory (everything moves, nothing duplicates)

| Guard | Current location | After |
|---|---|---|
| exact-lease generation/binding authentication | `exactLeaseLocked`, re-checked per method | reducer entry (single call) |
| finalization phase admission | `admitLeaseLocked` variants | reducer entry |
| settlement claim token/ready/lease/phase validation | `BeginRunFinalization`, `prepareNoAction`, `noActionFinishLocked` | reducer |
| suppression (closing/stopping/stop-pending/recovery) | `supervisionSuppressedLocked` + echoes | reducer (predicate already shared) |
| stop precedence and fallback selection | `finishGenerationLocked`, `noActionFinishLocked` | reducer |
| prepared-terminal interplay / outcome normalization | `finishGenerationLocked` | reducer |
| no-action eligibility chain (trigger, phase, attention, evidence, run-error, fallback) | `prepareNoAction`, `noActionFinishLocked` | reducer (existing shared predicates) |
| append-failure recovery latching | inline at append sites | `reduceFinishAppendResult` |
| stale-lease suppression policy (ordinary vs. authorized no-action) | `FinishGeneration`/`FinishNoAction` wrappers | reducer returns the policy outcome; wrappers only surface it |

### Acceptance criterion

Every finish-path guard above is textually inside the reducer; wrappers
contain intent construction, the `appendLocked` call, and effect
application only. Measured: grep-based count of decision sites before and
after; full `agent` suite, incident regressions, and the differential
harness all green with zero test modifications.

## Non-goals

- No journal schema change and no journaled completion evidence (that is
  the follow-up phase, deliberately deferred).
- No single-writer actor and no new effect abstraction layer.
- No behavior change for #569 (terminal durability), #570 (same-round
  atomicity), or #571 (terminal-cut enqueue).
- No root-session routing changes; no `ask_user` changes.
- The harness does not mock the controller, and the model does not grow
  into a fourth implementation of packet construction.

## Risks

- **Harness flakiness** is the primary risk; the determinism requirements
  above are the mitigation, and any timing-dependent failure blocks
  delivery until removed.
- **Model drift:** the model is a third copy by design; it is kept minimal
  (phases/outcomes) and its table is reviewed with the same rigor as
  production code.
- **Reducer relocation ordering:** append-before-publish and
  cancel-after-unlock ordering must be preserved exactly; the harness,
  full suite, and adversarial review all focus here.
- **Partial adoption:** if the reducer stalls, the branch must not merge a
  state where guards exist in both old and new locations; the acceptance
  criterion's "textually inside" rule is the guard against it.
