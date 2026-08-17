# Stop Always Works Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Supersedes** `2026-08-16-busy-means-named.md` and `2026-08-16-one-running-turn.md`.
Both were written for a problem that the two landed plans had already fixed. See
"What was already true" below — it is the reason this plan is short.

**Goal:** Steer, Send and Stop work on a running session at every moment, and
any control that cannot be honoured says so instead of failing silently.

**Architecture:** No new abstraction. Four independent defects, each with its own
failing test, plus a cleanup list. The turn-naming work is done; what is left is
the moments *between* named turns, one missing escape hatch, one projection
corruption, and the fact that a rejection is invisible.

**Tech Stack:** Go 1.25 multi-module workspace, `appwire` JSON-RPC,
React/TypeScript frontend (Vitest).

**Spec:** this document.

**Prior art:** `2026-08-16-one-active-turn-identity.md` (goal continuations) and
`2026-08-16-one-turn-boundary.md` (notification wakes), both landed and correct.
Katas: `c2ty` (the window, and Jesse's ruling), `2f41` (rejections are
invisible), `7vmd` (the notification turn), `eptj` (the data-loss precedent for
two minters).

---

## What was already true, and how that was missed

Two adversarial reviews established that on a served root session **every
production turn kind already carries a durable `turn_m<n>` name**:

| Turn kind | Named at | Status |
| --- | --- | --- |
| `turn/start` | `AcceptClientMutationStart`, `agent/session_client_mutation.go:246` | always was |
| queued drain | `popQueueHead`, `agent/session_queue.go:581` | always was |
| goal continuation | `agent/session_lifecycle.go:1100` | landed this week |
| notification wake | `agent/session_lifecycle.go:1041-1043` | landed this week |

`EntryWatchDelivery` has no production producer; `FollowUp` has no non-test
callers. And the queued path is already proven end to end against a live hub and
daemon: `TestE2E_TurnControlReachesTheSession`
(`cmd/serf-hub/e2e_turn_control_test.go:118-177`) queues while busy, waits for
the queued message's own turn through `awaitActiveTurn` — which requires status
`active` **and** a fresh non-empty id — and asserts `turn/interrupt` against that
id returns `Applied`.

**Process note, recorded because it cost two discarded specs.** Both of those
specs opened with a task whose job was to make the invariant assertable before
changing anything. Neither was executed before its remaining tasks were written.
Had the measurement come first, the tasks would not have been written at all.
**Task 1 below is that measurement, and nothing after it may be implemented
until it produces a genuinely failing test.**

---

## Global Constraints

- **Strict TDD.** Every task: write the test, run it, watch it fail *for the
  stated reason*, implement the minimum, watch it pass. A step that cannot be
  made to fail first is not a test — say so and redesign it.
- **No test may be tautological.** `invariant.Hold` is a **no-op** outside
  `-tags serffuzz` (`invariant/invariant.go:28,32`, `const Enabled = false`), so
  an invariant is never a test. A length assertion against a literal is not a
  completeness check.
- **One minter of live turn ids.** `reserveClientMutationTurnID`. Kata `eptj` is
  what two minters in one namespace did: a collision made `turn/completed`
  overwrite a persisted turn's content — real data loss.
- **Do not re-derive line numbers from memory.** Every citation in this document
  was verified at `24111733f`; re-check before editing, and fix the spec if it
  has drifted.
- **Gates:** `make lint` runs **nine** targets (`Makefile:566`), not seven.

---

## Task 1: Measure the invariant, and find the case that actually fails

Nothing else in this plan may start until Step 4 produces a red test.

**Files:** create `server/control_invariant_test.go`

- [ ] **Step 1: Write the harness.** A helper that returns
      `(statusType, activeTurnID)` from `thread/read`, and one that asserts the
      control invariant: *if the wire says a turn is running, a `turn/interrupt`
      aimed at the id the wire just published is accepted.* Prefer reusing
      `awaitActiveTurn`/`awaitThread` (`cmd/serf-hub/e2e_turn_control_test.go:437-451`)
      over re-implementing them.
- [ ] **Step 2: Run it against a goal-continuation turn and a notification
      turn.** Both must PASS. If either fails, stop — a landed plan regressed and
      that is a different bug.
- [ ] **Step 3: Commit the harness green.**
- [ ] **Step 4: Drive the between-turn gap and watch it FAIL.** A queued message
      drains, its turn ends, and the next turn has not begun.
      `completeClientMutationTurnWithState` clears the durable name
      (`agent/session_client_mutation_queue.go:1135-1137`) from the drain loop at
      `agent/session_lifecycle.go:656`, while `processing` is still true and
      `s.appActiveTurnID` still names the finished turn until the next event
      reaches `server/appwire_runtime.go:212`. Expected failure: `thread/read`
      returns `active` plus a stale id, and `turn/interrupt` against it is
      rejected `Conflict("turn is not active")`
      (`agent/session_client_mutation.go:422-424`).
- [ ] **Step 5:** If Step 4 passes, STOP and report. This plan has no premise.

## Task 2: Stop can always stop, even with no addressable turn

`InterruptClientMutation` requires a non-empty `expectedTurnId` exactly equal to
`ActiveTurnID` (`agent/session_client_mutation.go:414-425`). There is no
"stop whatever is running" form. That single precondition is why Stop fails in
the gap Task 1 just proved, and it would fail the same way in any future window.

**Decision (Jesse's, recorded rather than assumed):** add a session-scoped
interrupt. Preferred shape: `expectedTurnId` stays required for the
compare-and-commit path a client uses when it *has* a turn id, and an explicit
opt-in field (or a distinguished sentinel) means "interrupt whatever is
running". The alternative — making `expectedTurnId` optional — is rejected:
`turn/interrupt` is a durable retry-safe mutation, and an interrupt with no
target cannot be made idempotent across a retry.

**Files:** `appwire/types.go`, `appwire/protocol.go`,
`agent/session_client_mutation.go`, `server/appwire_runtime.go`

- [ ] **Step 1:** Write the failing test: a session-scoped interrupt issued
      during the Task 1 gap stops the session. Run it; expect a schema error.
- [ ] **Step 2:** Add the field to the wire type and the protocol table.
- [ ] **Step 3:** Run again; expect `Conflict("turn is not active")` — the
      precondition, now reached with a well-formed request.
- [ ] **Step 4:** Implement: when the session-scoped form is set, the
      precondition becomes "the session is processing" rather than an id match.
      The fence still records the id it actually cancelled, so
      `finalizeClientMutationInterrupt` (`:566`) is unchanged.
- [ ] **Step 5:** Prove idempotence explicitly — issue the same
      `clientMutationId` twice and assert one effect, since this is the property
      the rejected alternative could not hold.
- [ ] **Step 6:** Mutation-check, run `./agent/ ./server/`, commit.

## Task 3: A steer with no active turn is queued, not rejected

Steer needs somewhere to land. During the gap there is no turn to steer, but
there is an obvious correct semantic: inject it before the next turn — which is
exactly what the steering queue already does.

**Open call for Jesse.** The alternative is to leave steer rejected in the gap
and rely on Task 5 to make the rejection legible. Recommendation: queue it. A
steer typed while the agent is working means "take this into account", and the
user cannot see turn boundaries.

- [ ] **Step 1:** Failing test: `turn/steer` during the Task 1 gap is accepted
      and reaches the next turn's model request.
- [ ] **Step 2:** Implement via the existing steering queue; do not add a second
      path.
- [ ] **Step 3:** Assert the steer is not *lost* if no next turn ever runs —
      either it is delivered or it is visibly still pending. A silently dropped
      steer is worse than a rejected one.
- [ ] **Step 4:** Mutation-check, commit.

## Task 4: `EventError` stops re-opening a real turn under a bucket id

Both reviews called this the best defect either spec found, and it is a
projection corruption rather than hygiene.

`EventError` clears `p.activeTurnID` directly (`internal/appprojector/appwire_projection.go:658-664`)
instead of going through `closeActiveTurn`. Any later content event in the same
input re-opens a real turn through `ensureTurn` (`:1788-1798`) under a
`turn_<n>`, and `server/appwire_runtime.go:212` republishes it as the thread's
active turn id while `processing` is still true.

`ensureTurn` has **eight call sites across seven event kinds**: `:272`, `:283`
(via `ensureAssistantItem`), `:308` (via `ensureReasoningItem`), `:333`, `:352`,
`:413`, `:432`, `:658`. The two indirect ones are exactly what a reasoning or
text delta after an error hits, so a test that only drives `EventAssistantTextStart`
will miss them.

`EventError` also leaves `reasoningItem`, `toolArgsByKey` and `toolStartByKey`
set, and `startTurn` does not clear `reasoningItem` either — so
`ensureReasoningItem` (`:1817-1824`) reuses a stale item id in the next turn with
no `item/started`.

- [ ] **Step 1:** Failing test: after `EventError` mid-turn, a following
      `EventAssistantTextDelta` does not publish an active thread carrying a
      `turn_<n>`.
- [ ] **Step 2:** Second failing test: after `EventError`, the next turn's
      reasoning delta emits its own `item/started` rather than reusing the failed
      turn's item id.
- [ ] **Step 3:** Introduce one `resetTurnScopedState()` with an explicit field
      list, and use it from `closeActiveTurn`, `startTurn` and the `EventError`
      path. `closeActiveTurn` clears seven fields, `startTurn` six, `EventError`
      five — the divergence is undocumented and must end deliberate or gone.
- [ ] **Step 4:** Mutation-check each, run `./internal/appprojector/ ./server/`,
      commit.

## Task 5: A rejected control says why (kata `2f41`)

**Corrected premise.** Rejections are not invisible on the wire — the daemon
stamps `MutationOutcome = notAccepted`
(`agent/session_client_mutation_queue.go:177-195`), it reaches the client
(`appwire/errors.go:44-50`), the dispatcher calls `transferToRecovery`
(`cmd/serf-hub/frontend/src/stores/mutationDispatcher.ts:137-141`), and
`QueueStrip` renders a row. The defects are narrower and worse:

- [ ] **Step 1: The reason is never persisted.** `MutationRecoveryRecord`
      (`stores/mutationOutbox.ts:57-59`) carries only `recoveryKind`, and
      `mutationOutboxIndexedDB.ts:264` builds it with
      `{ ...record, recoveryKind }` — the wire error's code and message are
      dropped at the storage boundary. Failing test first: a rejected mutation's
      reason survives a round-trip through the outbox. This is a schema change,
      not a rendering change.
- [ ] **Step 2: The row does not say it failed.** It is *not* indistinguishable
      from a pending row (pending carries `CLASS.rowPending`, opacity 0.6, and no
      buttons) — it is indistinguishable from a real daemon **queue** row, and
      the header counts it as queued. Failing test, then render the reason.
- [ ] **Step 3: A Stop must not become a message.** A rejected `turn/interrupt`
      lands in a text-recovery store; `activateRecovery` (`Composer.tsx:663-688`)
      loads it into the composer draft and `resendRecoveryMutation`
      (`stores/threads.ts:547-563`) re-mints it through `composerMutationIntent`,
      so it can be resurrected as a `turn/start` carrying whatever the user
      typed. Worse, `Composer.tsx:374-390` auto-activates the first rejected
      entry into an empty composer. Failing test, then keep interrupts out of the
      text-recovery path entirely.
- [ ] **Step 4: The second rejection path.** `mutationDispatcher.ts:126-134`
      handles a `WireError` with no `clientMutationId` — which is what a Stop
      with an empty `expectedTurnId` takes, since
      `server/appwire_runtime.go:828-830` returns `InvalidParams` naming no
      mutation. Cover it, or Step 2's reason is still blank on that path.
- [ ] **Step 5:** Any test here must drive `MutationDispatcher`. A test against
      `threadsStore.steer` cannot observe a server rejection and would be
      tautological — the promise resolves at the local IndexedDB commit.
- [ ] **Step 6:** `handleInterruptClick`'s catch (`Composer.tsx:875-885`) is
      **not** dead — `requireClient()`, the not-ready throw
      (`stores/threads.ts:821`) and `requireMutationRuntime` all reach it. Keep
      it; do not "wire or delete" as an earlier draft said.
- [ ] **Step 7:** Close kata `2f41` citing test names. Commit.

## Task 6: The client stops bouncing its own second message

The last user-visible window, and the only one that is client-side. Send-vs-Queue
is decided when the message is composed, from `statusType` alone
(`protocol/sendQueueAvailability.ts:63-72`, deliberately — see its header). A
message composed before the UI learns the session went busy is built as
`turn/start` and rejected `Conflict("turn is already active")`.

- [ ] **Step 1:** Failing test: two submits in quick succession, the second
      composed before any status frame arrives, both reach the session.
- [ ] **Step 2:** Route from the client's own optimistic state as well as the
      published status — the pending-turn machinery already tracks "I just
      started a turn". Do **not** fold `activeTurnId` into
      `deriveSendQueueAvailability`; its header explains why that reintroduces a
      different race.
- [ ] **Step 3:** Assert the composer still queues correctly on a cold hydrate,
      where there is no optimistic state. Commit.

## Task 7: Descendant threads

**Open call for Jesse**, unchanged from the previous spec and still undecided:
subagent threads are unserved, so they can take no client mutation and need no
durable name — but they have a rendered projection a client reads, and today the
unconditional `threadStatus(Active)` (`internal/appprojector/appwire_projection.go:241`)
is the only reason a subagent thread reads busy at all.

Recommendation: descendants get turn separation and status, never a name. Note
that the `servedByDaemon()` gate on the boundary emit is at
`agent/session_lifecycle.go:1652`, and `TestUnservedSessionAnnouncesNoBoundary`
(`agent/session_turn_boundary_test.go:569`) pins today's behaviour and must be
inverted deliberately.

- [ ] Decide first. Then: failing test, implement, mutation-check, commit.

## Task 8: Coverage and hygiene

Each bullet is its own red-green-commit.

- [ ] `EventTurnStarted` is in neither fuzz corpus:
      `internal/appprojector/project_fuzz_test.go`'s `projectorCases` and
      `agent/events/eventdata_program_fuzz_test.go`, whose doc claims
      completeness while omitting `TurnStartedData` and `ModelRetryData`. Derive
      the case set from something that changes when the sealed set changes; a
      literal length assertion cannot catch the next omission.
- [ ] `agent/session_client_mutation.go:128-141` says three sites write
      `ActiveTurnID`. There are eight, and the omitted
      `finalizeClientMutationInterrupt` (`:566`) is the unconditional clearer the
      identity guard exists to survive.
- [ ] Duplicate helpers in `internal/appprojector`: `startedTurnID`
      (`goal_turn_identity_test.go:12`) and `turnStartedID`
      (`turn_boundary_test.go:10`) are the same function, and `completedTurnID`
      is re-implemented inline at `goal_turn_identity_test.go:90-104`. Separately
      in `agent/`, `TestUnservedSessionNamesNoTurn`
      (`session_turn_boundary_test.go:550`) and
      `TestMintRunningTurnIDSkipsUnservedSessions`
      (`session_active_turn_test.go:195`) assert the same thing.
- [ ] `server/thread_envelope.go:174-190`: the justification for
      `EventTurnStarted: facetWork` sits after that row and before
      `EventSteeringInjected`, reading as documentation of the wrong entry; and
      `thread_envelope_test.go:71-85` asserts `WorkMillis` while the comment
      argues `ActiveTurnStartedAt`.
- [ ] Dedupe `cmd/serf-hub/e2e_turn_control_test.go` (700 lines): the
      `thread/start` + cleanup block appears three times, steer-and-prove twice,
      interrupt-and-prove three times. Makes visible that only one test covers
      Send-while-busy.
- [ ] `agent/entrykind_audit_test.go` is weak — a kind added after
      `entryKindCount` leaves both assertions green. Strengthen it, or record why
      not. Do **not** replace it with a `switch` + `invariant.Hold` default: that
      is a production no-op and its test cannot fail.

## Task 9: Retire the superseded plans

- [ ] `2026-08-16-one-active-turn-identity.md` still instructs the
      `SteeringInjectedData.StableTurnID` emit that `b5ce354a5` reverted and that
      `internal/appprojector/appwire_projection.go:693-699` says "must never be
      adopted" — at `:144`, `:501`, `:526-529`, with stale claims at `:12-15` and
      `:149`. Its header tells an agent to execute it task-by-task, so it is a
      live trap. Mark the superseded approach historical and point here.
- [ ] `2026-08-16-one-running-turn.md`: mark superseded by this document, keeping
      its diagnosis and rejected-options record.
      (`2026-08-16-busy-means-named.md` is already marked.)

---

## Definition of done

- [ ] Task 1's harness passes for every turn kind **and** in the between-turn
      gap, on the push path and the read path.
- [ ] `make lint` (nine targets), `make build`, root suite, all seven module
      suites, `make test-web`, all live-stack e2e tests.
- [ ] Live browser check: Stop works at every moment of a multi-turn drain,
      including between turns; Steer during a gap lands in the next turn; two
      fast messages both arrive. Zero rejections in the mutation journal — and
      any rejection that does occur is legible on screen.
- [ ] Katas `c2ty`, `7vmd` and `2f41` closed, each citing a test name.

---

## Does this reach the endgame?

Stated plainly, because this plan follows two that promised more than they could
deliver.

**Yes for Stop.** Task 2 removes the only reason a Stop can fail on a running
session: the exact-match precondition. After it, Stop does not depend on the
client holding a current turn id, so it survives every window — the ones found
here and any not yet found.

**Yes for Send.** It already works, and Task 6 closes the fast-second-message
case.

**Yes for Steer, conditional on the Task 3 decision.** If a steer with no active
turn is queued rather than rejected, Steer works at every moment. If that call
goes the other way, Steer stays rejected in the gap and Task 5 only makes the
rejection legible.

**What this does not promise.** It does not promise there are no windows left.
This is a durable store, three goroutines and a browser; new windows will be
found. What it promises is different and more useful: after Task 5, a control
that cannot be honoured **says so**. The reason the original bug survived weeks
was not that it existed, it was that it was silent — five rejections in the
mutation journal and nothing on screen.

**The honest residue.** Descendant threads (Task 7) are undecided. The projection
corruption in Task 4 predates all this work and may have other symptoms not yet
found. And `main` still carries the e2e disk leak without its fix — kata `tx4q`,
which is unrelated to all of the above and should be cherry-picked regardless.
