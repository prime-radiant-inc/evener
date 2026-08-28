# Delegate lifecycle: differential harness and finish-path intent reducer

Date: 2026-08-29 (revised after adversarial review: reviewers A `dlg_034FgqrvqKHckKdZrrSgyF`, B `dlg_034Fgrl5XrPnt1VceshQGK`; A 7 significant findings, B 6, one conflict adjudicated in A's favor with parent verification)
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

This spec covers two projects, delivered in one branch in dependency order:

1. **Differential lifecycle harness** (test-only): drive randomized,
   deterministic operation sequences through the real controller and assert
   cross-layer agreement after every operation, including across simulated
   crash/recovery.
2. **Finish-path intent reducer** (production, behavior-preserving):
   relocate every guard of the generation finish path into one locked
   transition function; existing mutators become intent constructors and
   effect executors. Scope is defined precisely below; adjacent finish
   sites are named and deferred, not silently duplicated.

The harness ships first because it is the proof net for the reducer, and it
remains permanently valuable after the reducer lands.

## Project 1: differential lifecycle harness

### Shape

- Test-only. No production code changes. The deterministic runner and the
  model live in **untagged** test files
  (`agent/delegate_lifecycle_differential_test.go`,
  `agent/delegate_lifecycle_model_test.go`) so the Task 1 gate runs real
  tests; the Go native fuzz target lives in a tagged file
  (`//go:build evenerfuzz`) and registers in
  `scripts/fuzz/fuzz-targets.txt` (native format
  `tag:module:package-relpath:name` with optional `coverpkg`), so
  `lint-fuzz-registry` and `make fuzz` cover it.
- The driver applies each operation to a **real** `delegateTreeController`
  using the existing scripted fixtures (`newColdStableDelegateFixture`,
  testkit `newSession` builders, fake adapters), never a mock of the
  controller itself. Append-failure injection uses the existing agent-side
  mechanism: `c.store.Close()` plus, when the sequence needs recovery to
  continue, reopening and swapping `c.store` under `c.mu` (existing
  precedent: `agent/delegate_tree_finish_test.go`, `agent/delegate_tree_stop_test.go`).
  No new fault seam, no production change.
- An independent **model state machine** lives in the test file. It is
  intentionally a third, tiny, auditable copy — that is what differential
  testing is. It never constructs packets.

### Model state (per delegate, single-delegate scope)

`phase`, `runOpen`, `stopPending`, `reportRequired`, `terminalSeen`,
`delivered`, `evidencePresent`, `evidenceRequirement`, and the
**exhaustion-payload source** for terminal outcomes (budget, limit,
resumable, and whether they came from the run error or from the prepared
terminal — the value-level dimension the exhaustion-overwrite bug lived
in).

Explicitly **not** modeled in v1 (named non-coverage, follow-up
candidates): nested-delegate/subtree-stop races and ancestor fences;
model-request claim races beyond the settlement-claim path; multi-delegate
interleavings. The harness generates single-delegate sequences including
steering and settlement-claim interactions.

### Operation vocabulary

A seed dimension plus fourteen operations; the driver maps fuzz bytes onto
them:

- **Seed: binding kind** — production start (evidence attached, the
  `CommitStart` path) or legacy/manual binding with nil evidence (the
  tolerance path in `escalateCompletionRequirementLocked`; existing
  seeding precedent in `delegate_tree_controller_test.go`). This is what
  makes the nil-evidence defect class reachable.
1. start a generation (trigger: user work / shell attention);
2. admit report-requiring work (user input, follow-up, goal continuation,
   steering bind, hook output);
3. admit system-only work (attention, notification);
4. bare attention response (records attention no-action when eligible);
5. terminal communicate (sets terminalSeen);
6. supervision-boundary decision (pending steers → continue; suppression
   arbitration) — distinct from op 7;
7. bounded nudge continuation (needs-nudge decision → nudge → outcome);
8. run-end error injection: the fake adapter returns a selected terminal
   error — nil, generic, budget exhaustion, `context.Canceled`, or
   `errors.Join(budget, context.Canceled)` — optionally racing a cancel
   request. This is what makes the exhaustion-overwrite defect class
   reachable; model outcome rules mirror `classifyRunEnd`'s documented
   pins;
9. stop request (before finalization, and racing finalization);
10. ordinary finish (`FinishGeneration`);
11. no-action finish (`FinishNoAction` via the claim path);
12. delivery execution + acknowledgment (`BeginDelivery` /
    `CompleteDelivery`, both synchronous) — this is what makes I4's
    parent-visible half reachable;
13. injected append failure (`c.store.Close()`), optionally followed by
    reopen/swap and a retry;
14. simulated crash: discard runtime state without finalizing, snapshot
    the journal, durable transcripts, and job logs, then run the real
    restore path and let recovery settle.

### Invariants (asserted after every operation and after recovery)

- **I1 — single authority:** at most one open generation per delegate.
  `CurrentRunOpen` and live-binding presence agree even during the
  append-failure latch (the latch retains the binding); the only
  divergence window is crash-before-restore, bounded by
  `reconcileRuntimeLostFromEvidenceLocked`. The window is listed in the
  test, not discovered by it.
- **I2 — legal transitions:** the durable phase sequence is a path in the
  model's transition table.
- **I3 — stop precedence (event-scoped):** a generation whose
  `RunFinished` is journaled *after* a covering stop request has outcome
  neither `completed` nor `completed_no_action`. (A generation finished
  before the stop legitimately keeps its outcome — the invariant is over
  event order, not final state.)
- **I4 — delivery uniqueness:** at most one parent delivery is executed
  and acknowledged per generation, and none for `completed_no_action`.
- **I5 — report requirement:** if any report-requiring work was admitted
  with evidence present, the final outcome is not `completed_no_action`.
  With nil evidence, escalation is a no-op and the legacy outcome rules
  apply — the model tracks this explicitly.
- **I6 — durable truth:** replaying the journal through
  `delegatestore`'s fold yields the same aggregate the controller holds;
  no parent-visible effect exists without its journal event.
- **I7 — recovery determinism:** recovery after a crash at any operation
  boundary converges to the state predicted from the **journal plus the
  durable transcripts and job logs captured at the crash boundary**
  (attention reconstruction reads transcripts; delivery dedup reads
  parent transcripts — those are legitimate durable inputs). State that
  was only in pre-crash memory — evidence contents, bindings, claims —
  must never influence the recovered state.

### Determinism requirements

No wall-clock, no sleeps, no network, no provider credentials. Scripted
clock (required on the root runtime: attention retry uses
`sclock().AfterFunc`) and adapters; synchronization only through existing
test barriers/condition waits; avoid the `StopSubtreeAndDrive` driver
goroutine in favor of `StopSubtree` + synchronous drain (precedent:
`FuzzDelegateControllerTransitions` drives the same controller with a
fixed clock and no goroutines). A failing seed must reproduce
byte-for-byte from the fuzz input alone.

## Project 2: finish-path intent reducer

### Scope

The **generation finish path**: settlement-claim acquisition and fencing
(`BeginSettlement`, `BeginFinalization`, `BeginRunFinalization`,
`finalizationReadyLocked`), the supervision boundary, completion decision,
settlement completion (`CompleteSettlement`,
`AttentionResolutionsForFinalization`), no-action preparation and finish
(`prepareNoAction`, `noActionFinishLocked`), ordinary finish
(`finishGenerationLocked`), evidence observations feeding these decisions
(`recordAttentionNoAction`, `recordTerminalSeen`,
`escalateCompletionRequirement`), and the finish-path recovery fences
(`RequireFinalizationRecovery`, `ReportFinalizationQuiesced`).

**Adjacent finish sites — explicitly out of Phase-1 scope, named, and
deferred to a follow-up phase:** the start-failure finishers
(`CompleteStartInput`, `FailCommittedStart`, `FailCommittedRestart`,
`finishStoppedStartLocked` — which is a second copy of the stop-finish
decision) and the recovery finishers (`reconcileRecoveryRequiredStopLocked`
— a third copy of the stop-finish decision;
`reconcileRuntimeLostFromEvidenceLocked` — a second copy of
prepared-terminal outcome normalization, already structurally divergent
from the finish-path copy). Rationale: these run on separate entry paths
with their own claim types and batch builders; folding them into the same
pass triples the diff and the risk. The cost of deferral is documented
here: until the follow-up, stop-finish selection exists in three places
and prepared-terminal normalization in two. The reducer's "single
authority" claim below applies to the generation finish path only.

### Shape

- Production change, behavior-preserving. No signature changes to existing
  methods, including `FinishGeneration`, `FinishNoAction`,
  `BeginSettlement`, `BeginFinalization`, `BeginRunFinalization`,
  `CompleteSettlement`, `AttentionResolutionsForFinalization`,
  `SupervisionBoundary`, `RequireFinalizationRecovery`, and
  `ReportFinalizationQuiesced`.
- New typed intents (new file `agent/delegate_tree_intents.go`) describing
  what a caller wants: ordinary finish, no-action finish, settlement
  completion, and the evidence observations (`workAdmitted`,
  `attentionNoAction`, `terminalSeen`).
- One locked transition function, `reduceFinishIntent`, in the
  `classifyRunEnd` idiom but operating on controller state: the caller
  holds `c.mu`; the reducer acquires no locks, performs no journal I/O,
  and calls no Session methods (comparing runtime **pointer identity** for
  the binding-runtime guard is permitted; taking `s.mu` or calling Session
  behavior is not). It returns the **decision**: events to append, the
  per-site latch plan for append failure (shapes differ — see below), the
  abstract effect descriptors (release claim, release generation, deliver,
  cancel-after-unlock), and the stale-lease policy outcome for this entry
  point (suppress / propagate / swallow — three existing variants).
- Commit stays where it is: wrappers append through `appendLocked`.
  Concrete mutation-plan construction happens **after** append using the
  existing pure plan helpers against the post-append durable state (plans
  legitimately depend on it); that step branches only on the reducer's
  decision and never re-validates state. The reducer returns descriptors,
  not closures.
- `reduceFinishAppendResult(intent, appendErr)` applies the per-site latch
  plan the reducer computed: `finishGenerationLocked` latches the full
  triple unconditionally; `CompleteSettlement` latches only when the live
  binding still matches the claim's lease; `RequireFinalizationRecovery`
  sets `recoveryRequired` conditionally; start-path sites are out of scope
  (see above). No uniform latching — the shapes are inputs, not a
  constant.

### Guard inventory (generation finish path; everything moves, nothing duplicates)

| Guard | Current location | After |
|---|---|---|
| exact-lease generation/binding authentication | `exactLeaseLocked`, re-checked per method | reducer entry (single call) |
| finalization phase admission + stop-promotion arbitration + continue decision | `admitLeaseLocked`, `beginFinalization` | reducer entry |
| settlement claim creation/fencing | `BeginSettlement`/`BeginFinalization`/`BeginRunFinalization`, `hasSettlementClaimLocked`, `finalizationReadyLocked` (quiet-claim + ready fence) | reducer |
| settlement claim token/ready validation | `CompleteSettlement`, `AttentionResolutionsForFinalization`, `prepareNoAction`, `noActionFinishLocked` | reducer |
| completion decision (useExistingTerminal / finishNoAction / needsNudge) | `completionDecision` | reducer (query form, pure read) |
| suppression (closing/stopping/stop-pending/recovery) | `supervisionSuppressedLocked` + echoes | reducer (predicate already shared) |
| stop precedence and fallback selection | `finishGenerationLocked`, `noActionFinishLocked` | reducer |
| prepared-terminal interplay / outcome normalization | `finishGenerationLocked` | reducer |
| no-action eligibility chain (trigger, phase, attention, evidence, run-error, fallback) | `prepareNoAction`, `noActionFinishLocked` (existing shared predicates) | reducer |
| append-failure recovery latching (per-site shapes) | inline at append sites | `reduceFinishAppendResult` from the reducer's latch plan |
| stale-lease policy (suppress / propagate / swallow) | `FinishGeneration`, `FinishNoAction`, `ReportFinalizationQuiesced` | reducer returns the policy outcome; wrappers only surface it |
| recovery fences (exact-lease + runtime-identity) | `RequireFinalizationRecovery`, `ReportFinalizationQuiesced` | reducer (runtime pointer identity permitted) |

Shared predicates (`exactLeaseLocked`, `supervisionSuppressedLocked`,
`noActionBaseEligibleLocked`, `noActionEvidenceEligible`,
`finalizationReadyLocked`) remain single implementations the reducer
**calls**; the rule is no second evaluation of these conditions at call
sites, not textual inlining.

### Acceptance criterion

Semantic, not textual: (a) one locked function is the only finish-path
decision site; (b) wrappers contain no conditional on aggregate/live/
controller state and no outcome/disposition assignment — only intent
construction, `appendLocked`, and effect application; (c) verified by a
hand-audited manifest in the review package listing every wrapper body and
every guard's single home. Plus: full `agent` suite, incident regressions,
and the differential harness all green with zero test modifications.

## Non-goals

- No journal schema change and no journaled completion evidence (follow-up
  phase, deliberately deferred).
- No single-writer actor and no new effect abstraction layer.
- No folding of start-failure or recovery finishers in this pass (named
  above as the follow-up).
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
  and its table is reviewed with the same rigor as production code.
- **Reducer relocation ordering:** append-before-publish,
  cancel-after-unlock, and post-append plan construction must be preserved
  exactly; the harness, full suite, and adversarial review all focus here.
- **Partial adoption:** the semantic acceptance criterion and the named
  adjacent-sites section exist so the branch cannot merge guards in both
  old and new locations, and cannot pretend the deferred copies do not
  exist.
