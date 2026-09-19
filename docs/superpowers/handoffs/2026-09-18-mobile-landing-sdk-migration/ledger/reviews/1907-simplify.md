# /simplify review — PR #1907 (S3: the steering-carrier claim id is released on every exit; head a3da29b82)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's.
Reviewed `git diff -M pr-1906-review..pr-1907-review` (this PR's own diff): `agent/session_lifecycle.go:2740-2748`
and `agent/session_ask_test.go:2127-2172`. Line numbers are at this PR's head. Compared against the existing
`sessionLifecycleFault` points (`session_lifecycle.go:28-33` and its fourteen call sites), the panic seam at
`:1740`, and the drain-path test seams in `session_drain_as_steer_turn_boundary_test.go`.

The production change proper is right and minimal: `defer s.setSteeringCarrierClaimDrain("")` replaces the manual
clear after `injectDrainedSteering`, three lines net. The fault point follows the existing convention exactly —
`if fault := sessionLifecycleFault(ctx, "steering_carrier_drain"); fault != nil { panic(fault) }` is character-for
-character the shape of the `"panic"` point at `:1740`, and it is the panic-style variant rather than the
`errors.Join` variant the other thirteen points use, which is the correct choice for what it injects. The test
reuses `newQueuePersistTestSession` (`session_queue_persist_test.go:30`) and the real
`AcceptClientMutationSteer`/`claimSteeringCarrierTurn` path rather than fabricating a claim. Not including the new
point in `FuzzSessionLifecyclePhaseFaultCoverage`'s list (`session_lifecycle_tail_coverage_fuzz_test.go:221-226`)
is right too: that list is scoped to `processOneInput`'s phases and this point is not on them.

## Must fix before merge

### 1. `agent/session_lifecycle.go:2745-2747` — the new production fault point duplicates an existing test seam that reaches deeper into the same window

`steerAppendRefusal.onRefuse` (`session_drain_as_steer_turn_boundary_test.go:169-172`) exists for exactly this
window: "runs on the refusing write before it fails — the moment the steer is popped and its append is about to
fail". It is armed by `refuseSteerAppends`, which #1905's and #1906's tests in this same file already use six
times, and it fires from inside `s.clientMutationTranscriptAppend`, i.e. inside `consumeSteeringMessage` inside
`injectDrainedSteering` — *after* `setSteeringCarrierClaimDrain(identity.ClientMutationID)` has run and after the
steer has been popped.

- Cost: a permanent production branch (a context lookup plus a `panic`) whose only purpose is one test that an
  existing seam can already drive — and drive better. The new point panics *before* `injectDrainedSteering` is
  ever entered, so the test proves only that a `defer` on the line above it runs; it never exercises a panic
  escaping the drain itself, which is the hazard the PR description names ("a panic mid-drain").
- Simpler form: delete the three production lines and arm the existing hook in the test:

  ```go
  refusal := refuseSteerAppends(sess, "steer-1")
  refusal.onRefuse = func() { panic("injected steering carrier drain panic") }
  refusal.refuse.Store(true)
  ```

  then keep the same `recover()` wrapper and the same
  `if sess.steeringSelectionFailureIsCarrierClaim("steer-1")` assertion. The unwind path looks clean by
  inspection: nothing between `acceptSteeringCarrierInput` and the append hook recovers
  (`git grep -n 'recover()' -- agent/session_queue.go agent/session_client_mutation.go` is empty; the only
  recover on the lifecycle path is `processOneInput`'s at `:1733`, which this test does not go through because it
  calls `acceptSteeringCarrierInput` directly), and `appendTurnAfterTranscriptWrite` (`session.go:2005`) holds
  `attentionMu` under a `defer` and does not hold `s.mu` across `write()`, so the panic releases both before the
  test's assertion takes `s.mu`.
- I could not run this (read-only review), so keep the escape hatch: if the substitution does not unwind cleanly,
  keep the fault point and record the measured reason in one line beside it, rather than leaving it unexplained.
- Severity: must-fix-before-merge — it adds a production seam that an existing seam covers. If the measurement
  goes the other way, it downgrades to "no finding" with a one-line comment.

## Follow-up

### 2. `agent/session_lifecycle.go:2740-2744` — this PR exists because the claim is ambient state, and a parameter removes all of it

The leak this PR closes is only reachable because `steeringCarrierClaimClientMutationID` (#1905,
`session.go:579`) is mutable session state set around a call rather than a value passed into it. The path is two
hops: `acceptSteeringCarrierInput` → `injectDrainedSteering` → `consumeSteeringMessage` →
`recordFailedSteeringSelection`.

- Cost: the field, `setSteeringCarrierClaimDrain`, `steeringSelectionFailureIsCarrierClaim`, this PR's `defer`,
  this PR's fault point and this PR's test — roughly 90 lines across S1 and S3 to protect an invariant that does
  not exist if the id is an argument.
- Simpler form: `injectDrainedSteering(claimedCarrierID string)`, threaded to `consumeSteeringMessage` and on to
  `recordFailedSteeringSelection`. Five of its six call sites (`session_lifecycle.go:2417`, `:2538`, `:2582`,
  `:2692`, `session_tool_round.go:456`) pass `""`; the carrier at `:2748` passes `identity.ClientMutationID`.
  There is then nothing to leak on a panic and nothing to defer.
- Severity: follow-up (cross-PR reshape; the same item is #1905's follow-up 5).

### 3. `agent/session_ask_test.go:2139` — the test is filed under `TestAskUser_*` in `session_ask_test.go` but asserts claim hygiene, not ask behaviour

`TestAcceptSteeringCarrierInput_PanicMidDrainStillClearsTheClaim` is the only non-`TestAskUser_` test in the file,
it asserts nothing about `askPending` (the ask connection is only in its doc comment's motivation), and the helper
it uses, `newQueuePersistTestSession`, lives in `session_queue_persist_test.go` alongside the other
carrier-claim/queue-persistence tests.

- Cost: minor — the ask suite becomes the home for a queue-invariant test, so the next person grepping for carrier
  claim coverage looks in the wrong file.
- Simpler form: move it to `session_queue_persist_test.go` (or beside `refuseSteerAppends`'s other users in
  `session_drain_as_steer_turn_boundary_test.go`, which is also where must-fix 1 would take its hook from) and
  keep the one-sentence "why this matters for the ask boundary" pointer in the comment.
- Severity: follow-up.

Also checked, no finding: the `defer` is placed after the set and before the fault point, which is the only
ordering that proves anything; `errors` and `context` were already imported by the test file; the assertion reads
the claim through the production predicate `steeringSelectionFailureIsCarrierClaim` rather than the raw field,
which is the right level; nothing in the diff re-derives or re-scans anything, so there is no efficiency finding;
no dead code.

---

## Verdict: fix before merge (3 findings, 1 must-fix)

The `defer` itself is exactly right and I would merge it on its own. The must-fix is the three production lines
beside it: `steerAppendRefusal.onRefuse` already exists as the seam for "the steer is popped and its append is
about to fail", it is already used six times by this stack's own tests, and it fires from *inside*
`injectDrainedSteering` — so it drives the real mid-drain panic the PR description promises, where the new
`"steering_carrier_drain"` point panics before the drain is entered and therefore only proves the line above it
deferred. Swapping the injection for `refusal.onRefuse = func() { panic(...) }` deletes a permanent production
branch and strengthens the test; nothing on the unwind path recovers and no lock is held across the append hook,
so it should work, and if the measurement says otherwise the point can stay with one line saying why. The two
follow-ups are that this PR only exists because #1905 made the carrier claim ambient session state instead of an
argument to `injectDrainedSteering` (fixing that deletes this PR, its `defer`, its seam, its test and two helpers),
and that the test belongs in the queue-persistence file rather than the ask suite.
