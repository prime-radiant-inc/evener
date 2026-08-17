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

## STATUS at 2026-08-17 (read this first)

Branch is 69 commits ahead of `6af43a95a`. Nothing merged, nothing pushed.

### The plan was superseded by a simpler rule

Jesse, 2026-08-16: **control mutations do not name turns.** Steer, queue, stop,
drain and promote no longer take an `expectedTurnId` at any layer. By the time a
user's intent reaches the daemon the session may be on a later turn, and that is
fine -- the intent should apply as soon as possible rather than bounce.

The reasoning, because it decides future questions too: `expectedTurnId` was
never the load-bearing precondition. The mutations that need precision already
name a real object (`expectedEntryId`, `expectedQueueRevision`), and
`cancelQueued` -- which needs it most -- never took a turn id at all, nor did
`turn/start`. What the turn id asserted was "the session is still in the state I
saw", which is not what any of these buttons means. It could only ever turn a
success into a refusal, and refusals in exactly the windows that matter are the
bug this whole branch was chasing.

`b2a2f1c8c` removes it: five wire types, the validator's required list, eleven
wire-layer checks, five durable preconditions, `Preconditions.ExpectedTurnID`,
`EffectiveTurnID`, the `interruptRunningTurn` opt-in built earlier the same
night, and the client-side guards in the TUI and web composer. Net -393 lines.
Tasks 2 and 3 below are subsumed by it.

**Live-verified** against a real daemon and browser (2026-08-17): `turn/start`,
`turn/steer` and `turn/interrupt` each carry only `ref`, `clientMutationId` and
(where it applies) `input`. Stop ended a running turn, the transcript showed
"System steered: Interrupted", and the steer reached the model. The live pass
also caught a leftover no compiler could see -- the browser still sending
`interruptRunningTurn` after the Go field was deleted (`306a309ef`).

| Task | State |
| --- | --- |
| 1 measure the invariant | **Re-measured, and the gate did NOT hold** (kata `gwxa`; see the CORRECTION under the RESULT block). The original harness sampled only before the boundary and interrupted only after it. The rewritten one parks inside the window: the wire shows a working session and the daemon refuses the Stop. |
| 2 Stop escape hatch | **Uncommitted work in the worktree** from a subagent. Review before committing. |
| 3 steer always lands | **Done** -- `ddc68ae68`, `d131a80fc`, `e9ce2994b`. |
| 4 EventError turn-scoped state | **Done** -- `7ff6d0128`. |
| 5 kata 2f41 | **Done** -- `af56bffe7`, `402d6c8e7`. |
| 6 client double-send | **REVERTED** -- `8218cefd6`. It disabled Send in the window it targeted; kata `8c65` records what a correct fix needs. |
| 7 subagents | Split to `2026-08-16-controllable-subagents.md`. Kata `tb2k` records two false premises in it. |
| 8, 9 coverage and docs | Mostly done (`77fded8`, `f98ef83`, `b9b9f06`, `85bdecc`, `7807c6f`). |

**Before landing:** a live browser pass. Nothing after the first half has been
verified in a browser, and the original bug existed while the suite was green.
Then e2e coverage for the newer behaviours, then the gates (`make lint` runs
NINE targets), then merge -- kata `tx4q` (P0) means main refills the disk on
every full-suite run until this lands.

**The failure mode this branch kept hitting:** three fixes were wrong in ways
their own tests missed, each because the test asserted something easier than the
claim, or used a fixture that cannot occur in production. Check that a green
test's setup is reachable before believing it.

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
- [x] **Step 5:** If Step 4 passes, STOP and report. This plan has no premise.

### RESULT (2026-08-16): Step 4 came up GREEN. The gate fired.

`TestE2E_ControlInvariantAcrossATurnBoundary`
(`cmd/serf-hub/e2e_control_invariant_test.go`) samples `thread/read` as hard as
it can across a queued-message turn boundary, then aims a real `turn/interrupt`
at whatever id the wire publishes while turn 2 is demonstrably running. Five
consecutive runs:

- `activeWithNoID = 0`. The wire never reports a turn running without an id, so
  the composer never hides Stop and Steer on a working session.
- The published id tracks the boundary correctly (`turn_m1` → `turn_m2`), and
  `turn/interrupt` against the published id returns `Applied`.

**So the between-turn gap is real in the code and NOT observable by a client.**
Both reviews were right that `completeClientMutationTurnWithState` clears the
durable name while `processing` is still true. What neither of us checked is
that `s.appActiveTurnID` is only updated by events
(`server/appwire_runtime.go:212`), so during that window the wire keeps
publishing the *previous* id rather than an empty one — and by the time it
publishes a new id, that id is valid.

What this does NOT prove: that the window is unreachable. It proves an
aggressive sampler could not reach it in five runs. A Stop aimed at `turn_m1`
after turn 1 completed and before turn 2 opened would still be rejected; nothing
here demonstrates a user can land in that microsecond.

### CORRECTION (kata gwxa): the RESULT above rests on a measurement that never
### entered the window. The gap IS observable, and a client's Stop dies in it.

The harness never reached the boundary. Its sampler stopped as soon as turn 2's
model request arrived, which is before the projection catches up, so every
sample it took was pre-boundary: a run of the old test logs
`observed 22 x active/turn_m1` and nothing else. The `turn_m1` → `turn_m2`
transition this block cites as evidence was never in the sample set, and the
`turn/interrupt` it describes as aimed "at whatever id the wire publishes"
carries no id at all — `c435bc579` removed `expectedTurnId` from every control
mutation.

The rewritten test parks the drain loop inside the window with a queued plugin
slash command (`agent/session_lifecycle.go` expands it after `popQueueHead` has
claimed turn 2 and before anything announces it) and uses queue depth as the
discriminator, since depth comes from the session's live durable snapshot while
the turn id comes from the projector. Measured, deterministically, every run:

- The window is reached: 2532 of 2538 samples are `active` + turn 1's id +
  `queueDepth=0`, i.e. turn 2 claimed and unannounced.
- `activeWithNoID = 0` still holds, so the composer does keep showing Stop.
- **A `turn/interrupt` issued there is REFUSED:
  `appwire turn/interrupt: session is not processing`.**

The cause is not the stale id. `InterruptClientMutation` samples
`s.WireState()`, which reads the SESSION — turn 1 settled it idle,
`popQueueHead` already emptied the queue so `sessionWorkPending()` is false, and
`processOneInput` does not set `SessionProcessing` until after the slash-command
expansion. The wire's `active` comes from the daemon's own processing flag,
which spans the whole drain. Wire says working, session says idle, Stop is
refused — the precondition's own comment claims it is "the fact the wire
publishes as the thread's status", and it is not.

A one-line change makes it land (`!sessionProcessing && snapshot.ActiveTurnID
== ""`, verified by mutation), which the rewritten test detects and reports.

### Consequences for the rest of this plan

| Task | Status after the measurement |
| --- | --- |
| 2 (interrupt escape hatch) | ~~**Premise withdrawn.**~~ **Withdrawal retracted (kata gwxa).** The gap is observable and Stop dies in it — measured, not inferred. The withdrawal below was written from a harness that never entered the window. Its justification was this gap. **Jesse's call**, now on live evidence. |
| 3 (steer always lands) | **Stands.** Decided on its own merits: a steer typed between turns should land, and today it is rejected. |
| 4 (`EventError` bucket ids) | **Stands.** Independent, verified by both reviews. |
| 5 (kata `2f41`) | **Stands, and is now the most valuable task here** — if the remaining windows are this hard to reach, the thing that matters is that any control which does fail says so. |
| 6 (client double-send) | **Stands.** Independent and client-side. |
| 8, 9 (coverage, docs) | **Stand.** |

**The headline: Steer, Send and Stop appear to work today.** The bug Jesse
reported is fixed by the two landed plans. What remains is a projection
corruption, invisible failures, a client-side double-send window, and cleanup.

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

**Decided by Jesse, 2026-08-16: the steer always lands.** Accept it into the
steering queue and inject it into the next turn. If the session settles idle
instead of running another turn, the steer provokes a turn of its own rather
than waiting to be re-sent. Steer therefore works at every moment, the same
guarantee Task 2 gives Stop.

The rejected alternatives, recorded so they are not re-proposed: surfacing an
undelivered steer for the user to re-send (correct but makes the user do the
work), and leaving it rejected-but-legible (a button that sometimes does
nothing, which is what the `c2ty` ruling rejects).

**The machinery already exists — do not build a second delivery path.** A wake
proceeds on pending steering alone with no job notifications at all: that is the
shape `acceptNotificationInput` handles and
`TestNotificationTurnAnnouncesOneNamedBoundary`
(`agent/session_turn_boundary_test.go`) covers. So "steer with no active turn"
is: enqueue steering, then arm the same wake a completing job arms
(`s.notify()`). The stand-down and re-arm rules landed in `f40904960` then apply
unchanged, including the hot-loop guard.

- [ ] **Step 1:** Failing test — `turn/steer` during the Task 1 gap is accepted,
      and its text appears in the next turn's model request.
- [ ] **Step 2:** Implement via the existing steering queue.
- [ ] **Step 3:** Second failing test — a `turn/steer` accepted when the session
      then goes idle **starts its own turn** and is delivered. This is the half
      that makes the guarantee unconditional, and it is the half most likely to
      be skipped.
- [ ] **Step 4:** Third failing test — the steer is never delivered twice. The
      next-turn path and the idle-wake path must not both fire for one steer;
      the steering queue drain is the single consumer.
- [ ] **Step 5:** Confirm this cannot livelock: a steer that arms a wake, whose
      wake stands down, must not re-arm forever. `runningTurnNameHasOwner`
      (`agent/session_active_turn.go`) is the existing guard; assert it covers
      this producer too.
- [ ] **Step 6:** Mutation-check each, run `./agent/ ./server/`, commit.

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

## Task 7: Subagent threads — moved out

Split into its own plan at Jesse's request: it is larger than everything else
here combined and sits nearest the `eptj` data-loss precedent, because it puts a
new class of session into the client-mutation store.

See `docs/superpowers/plans/2026-08-16-controllable-subagents.md`. Jesse's
decision (delegates become watchable AND controllable) and the four blockers are
recorded there, along with the design question that must be answered first: what
the parent sees when you stop a delegate running inside its tool call.

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

**Yes for Steer.** Jesse's 2026-08-16 call makes the steer always land: into the
next turn if there is one, and otherwise into a turn of its own. So Steer, like
Stop, stops depending on whether a turn happens to be addressable at the instant
the button is pressed.

**What this does not promise.** It does not promise there are no windows left.
This is a durable store, three goroutines and a browser; new windows will be
found. What it promises is different and more useful: after Task 5, a control
that cannot be honoured **says so**. The reason the original bug survived weeks
was not that it existed, it was that it was silent — five rejections in the
mutation journal and nothing on screen.

**Subagents get the same guarantee**, by Jesse's Task 7 decision — but that task
is the largest here, sits nearest the `eptj` data-loss precedent, and carries an
unanswered design question (what the parent sees when you stop its delegate).
Treat its "yes" as conditional on that answer, not on the tasks above.

**The honest residue.** The projection corruption in Task 4 predates all this
work and may have symptoms not yet found. And `main` still carries the e2e disk
leak without its fix — kata `tx4q`, unrelated to all of the above and worth
cherry-picking regardless.
