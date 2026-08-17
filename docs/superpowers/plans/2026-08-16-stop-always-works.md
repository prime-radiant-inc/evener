# Stop Always Works Implementation Plan — MOSTLY LANDED

> **Do NOT execute this plan task-by-task.** Tasks 1, 3, 4, 5 and 9 are done,
> Task 2 was subsumed, Task 6 was reverted, and Task 7 moved to its own document.
> What is genuinely open is **two bullets of Task 8 and the Definition of done** —
> nothing else, and in particular nothing under Task 2 or Task 6.
>
> **`- [ ]` does not mean "available to work on" in this file.** Steps under a
> heading marked SUBSUMED or REJECTED are written as `- [~]`: they will never be
> done, because doing them would undo shipped work. Six `- [~]` steps under Task 2
> would rebuild the `interruptRunningTurn` opt-in that `c435bc579` deleted and
> `09ca9beae` had to chase out of the browser; three under Task 6 would re-land
> the routing `ed6db108a` reverted. Take work only from `- [ ]` boxes, and only
> after reading the heading above them.

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

## STATUS (read this first)

**Merged to local `main`, not pushed.** The work was rewritten on the way in, so
every sha this document quotes is the one reachable from `main`, not the
`wip/webui-steer-send-stop` sha it was written with. Check any of them with
`git merge-base --is-ancestor <sha> main`.

**One deliberate exception**, so a sweep does not read as a defect: `24111733f`
is quoted *because* it resolves to nothing. It was the citation baseline, and the
point of the paragraph in Global Constraints is that the baseline is unreachable.
It is the only such sha, and a sweep will report it **twice** — once there and
once in this sentence. Every other sha in this document passes.

No commit count is recorded here on purpose. It went stale twice — once at 64
against 65, once at 69 against 71, each time inside the very commit that updated
it. "Merged locally, not pushed" is the part that carries information and does
not decay.

**Tasks 2, 3 and 6 must not be executed.** Each carries its own REJECTED or
SUBSUMED marker below. A worker running this plan task-by-task would rebuild the
turn-scoped machinery `c435bc579` spent a commit deleting, and re-land a client
fix `ed6db108a` reverted.

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

`c435bc579` removes it: five wire types, the validator's required list, eleven
wire-layer checks, five durable preconditions, `Preconditions.ExpectedTurnID`,
`EffectiveTurnID`, the `interruptRunningTurn` opt-in built earlier the same
night, and the client-side guards in the TUI and web composer. Net -393 lines.
Tasks 2 and 3 below are subsumed by it, and are marked so at their headings.

**Live-verified** against a real daemon and browser (2026-08-17): `turn/start`,
`turn/steer` and `turn/interrupt` each carry only `ref`, `clientMutationId` and
(where it applies) `input`. Stop ended a running turn, the transcript showed
"System steered: Interrupted", and the steer reached the model. The live pass
also caught a leftover no compiler could see -- the browser still sending
`interruptRunningTurn` after the Go field was deleted (`09ca9beae`).

| Task | State |
| --- | --- |
| 1 measure the invariant | **Done, and the gate fired** (RESULT block below). A later review then showed the harness asserts less than it claims -- it interrupts only AFTER the boundary -- so "the gap is unreachable" is NOT established. |
| 2 Stop escape hatch | **SUBSUMED and not to be executed** -- `c435bc579` deleted the precondition the task existed to work around, and the `interruptRunningTurn` opt-in the task prescribes. `TurnInterruptParams` now carries only `ref`, `threadId` and `clientMutationId` (`appwire/types.go:1052-1056`) -- no turn id of any kind. |
| 3 steer always lands | **Done** -- `3393e7905`, `5fdb23fd6`, `daa3eab1b`. Not to be re-executed. |
| 4 EventError turn-scoped state | **Done** -- `0e467e098`. |
| 5 kata 2f41 | **Done** -- `7d4f51141`, `19c4deba4`. Kata `2f41` is closed. |
| 6 client double-send | **REVERTED and not to be executed** -- `ed6db108a`. It disabled Send in the window it targeted; kata `8c65` (open) records what a correct fix needs. |
| 7 subagents | Split to `2026-08-16-controllable-subagents.md`. Kata `tb2k` recorded five statements needing correction there; they have been corrected in that document. |
| 8, 9 coverage and docs | Mostly done (`905e653d2`, `08d511ded`, `647043055`, `e06ecbc98`, `68f5f75b3`). Task 9 is done -- see its section. |

**Before landing** (written while this was still a branch; it has since merged
to local `main`): a live browser pass. Nothing after the first half had been
verified in a browser, and the original bug existed while the suite was green.
Then e2e coverage for the newer behaviours, then the gates (`make lint` runs
NINE targets), then merge. Kata `tx4q`, the disk leak that made this urgent, is
closed.

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
- **Do not re-derive line numbers from memory.** Re-check every citation against
  the working tree before editing, and fix the spec if it has drifted.

  This constraint used to name `24111733f` as the baseline the citations were
  verified at. That commit was rewritten on the way to `main` and its tree is
  reachable from nothing, so the baseline cannot be checked out and the
  instruction could not be obeyed. `d1f06ceab` carries the identical patch, but
  its **tree is not the same**, so it is not a substitute baseline — naming it
  here would assert a verification that never happened against that tree. There
  is no baseline any more. The working tree is the only authority, which is why
  the instruction above is now unconditional.
- **Gates:** `make lint` runs **nine** targets (`Makefile:566`), not seven.

---

## Task 1: Measure the invariant, and find the case that actually fails

Nothing else in this plan may start until Step 4 produces a red test.

**Files:** ~~create `server/control_invariant_test.go`~~ — it shipped as
`cmd/serf-hub/e2e_control_invariant_test.go`. Different module: the harness needs
a live hub and daemon, which `server/` cannot start.

- [x] **Step 1: Write the harness.** A helper that returns
      `(statusType, activeTurnID)` from `thread/read`, and one that asserts the
      control invariant: *if the wire says a turn is running, a `turn/interrupt`
      aimed at the id the wire just published is accepted.* Prefer reusing
      `awaitActiveTurn`/`awaitThread` (`cmd/serf-hub/e2e_turn_control_test.go:437-451`)
      over re-implementing them.
- [x] **Step 2: Run it against a goal-continuation turn and a notification
      turn.** Both must PASS. If either fails, stop — a landed plan regressed and
      that is a different bug.
- [x] **Step 3: Commit the harness green.**
- [x] **Step 4: Drive the between-turn gap and watch it FAIL.** A queued message
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
it can across a queued-message turn boundary, then issues a real `turn/interrupt`
while turn 2 is demonstrably running. Five consecutive runs.

**What the test actually asserts** — two things, and it is worth being exact,
because an earlier version of this block credited it with more:

- **`activeWithNoID == 0`, asserted** (`:153-155`). The wire never reports a turn
  running without an id, so the composer never hides Stop and Steer on a working
  session.
- **The interrupt is `Applied`, asserted** (`:180-183`), issued after the boundary
  while `thread/read` reports turn 2 active.

**What it does not assert, despite the wording that used to be here.**

- **The interrupt names no turn.** The earlier text said "`turn/interrupt`
  against the published id returns `Applied`". No id is sent: the request at
  `:173-176` carries only `Ref` and `ClientMutationID`, because `c435bc579`
  deleted `ExpectedTurnID` from the type. The local `published` (`:166`) survives
  only inside the log line at `:167` and the failure strings at `:178` and `:181`.
  So the test shows an interrupt lands at the boundary; it cannot show anything
  about targeting, because nothing is targeted.
- **"The published id tracks the boundary correctly" was never checked.**
  `activeWithStale` counts samples that still carry the first turn's id, and it is
  incremented at `:142` and logged at `:148` — and never compared to anything. A
  run where the wire published the stale id in every sample passes. (Its sibling
  `activeWithNoID` *is* asserted; only the stale counter is loose.)

**What was NOT measured — and the earlier wording claimed it was.** The harness
never interrupts *inside* the gap. Every interrupt it issues lands after the
boundary, so it cannot distinguish "the gap is unreachable" from "this sampler
did not reach it". The first version of this block concluded "the between-turn
gap is real in the code and NOT observable by a client"; that universal claim is
not supported by the measurement it summarises, and is withdrawn.

Both reviews were right that `completeClientMutationTurnWithState` clears the
durable name while `processing` is still true.

**A previous version of this block explained the green result by claiming
`s.appActiveTurnID` "is only updated by events
(`server/appwire_runtime.go:212`)". That is false, and it is retracted rather
than repaired.** The field has seven writers:
`server/appwire_runtime.go:118`, `:212`, `:1402`, `:1412`, and
`server/server.go:701`, `:713`, `:720`. Two of them are `setProcessingLocked`
itself — `:720` mints a `turn_<n>` on the processing-true edge, `:713` clears the
field on the false edge — which is precisely the window the paragraph was
reasoning about. `2026-08-16-one-active-turn-identity.md` says the same thing
from the other side: that mint is deliberate, and kata `c2ty` is the ruling that
keeps it.

**So the sampler's result has no explanation recorded here.** It was green five
times; nobody has established the mechanism, and the mechanism that was written
down was wrong. Do not carry it forward. It never established that no client can
land in the gap either way.

The question stopped mattering for a different reason: `c435bc579` deleted
`expectedTurnId` entirely, so a Stop no longer names a turn and no longer has a
stale id to be rejected for. The gap is closed by removal, not by measurement.

### Consequences for the rest of this plan

| Task | Status after the measurement |
| --- | --- |
| 2 (interrupt escape hatch) | **Premise withdrawn**, then the task itself was **subsumed** by `c435bc579`. Not to be executed. |
| 3 (steer always lands) | **Stands** — and has since shipped. |
| 4 (`EventError` bucket ids) | **Stands.** Independent, verified by both reviews. Shipped as `0e467e098`. |
| 5 (kata `2f41`) | **Stands, and is now the most valuable task here** — if the remaining windows are this hard to reach, the thing that matters is that any control which does fail says so. Shipped; `2f41` is closed. |
| 6 (client double-send) | ~~**Stands.** Independent and client-side.~~ **Implemented and reverted** (`ed6db108a`). Kata `8c65`. |
| 8, 9 (coverage, docs) | **Stand.** Task 9 is done. |

**The headline: Steer and Stop work today; Send has one open window.** The bug
Jesse reported is fixed by the two landed plans. What remains is the
fast-second-message case (kata `8c65`) and the cleanup list in Task 8.

## Task 2: Stop can always stop, even with no addressable turn — SUBSUMED

> **SUBSUMED by `c435bc579`. Do NOT execute this task.**
>
> The task's whole justification was working *around* the exact-match
> precondition. `c435bc579` deleted the precondition instead, along with the
> `interruptRunningTurn` opt-in this task prescribes building. `turn/interrupt`
> now carries only `ref` and `clientMutationId`, and its precondition is "the
> session is processing", sampled from `WireState()`. Executing the steps below
> would rebuild the turn-scoped machinery that commit removed.
>
> The reasoning is kept because it records why *making `expectedTurnId`
> optional* was rejected — a durable retry-safe mutation with no target cannot be
> made idempotent across a retry. The answer that shipped was neither of the two
> options weighed here: no client names a turn at all.

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

- [~] **Step 1:** Write the failing test: a session-scoped interrupt issued
      during the Task 1 gap stops the session. Run it; expect a schema error.
- [~] **Step 2:** Add the field to the wire type and the protocol table.
- [~] **Step 3:** Run again; expect `Conflict("turn is not active")` — the
      precondition, now reached with a well-formed request.
- [~] **Step 4:** Implement: when the session-scoped form is set, the
      precondition becomes "the session is processing" rather than an id match.
      The fence still records the id it actually cancelled, so
      `finalizeClientMutationInterrupt` (`:566`) is unchanged.
- [~] **Step 5:** Prove idempotence explicitly — issue the same
      `clientMutationId` twice and assert one effect, since this is the property
      the rejected alternative could not hold.
- [~] **Step 6:** Mutation-check, run `./agent/ ./server/`, commit.

## Task 3: A steer with no active turn is queued, not rejected — DONE

> **DONE (`3393e7905`, `5fdb23fd6`, `daa3eab1b`) and subsumed in part by
> `c435bc579`. Do NOT execute this task.**
>
> The steer always lands: it is accepted into the steering queue and injected
> into the next turn, and it provokes a turn of its own if the session settles
> idle instead. The rejection this task existed to remove is gone twice over —
> once because the steer is queued rather than refused, and once because
> `c435bc579` deleted the `expectedTurnId` the refusal was computed from.
>
> Kept for its rejected-options record and its "do not build a second delivery
> path" ruling, both still binding.

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
(`s.notify()`). The stand-down and re-arm rules landed in `f9e5abcd3` then apply
unchanged, including the hot-loop guard.

- [x] **Step 1:** Failing test — `turn/steer` during the Task 1 gap is accepted,
      and its text appears in the next turn's model request.
- [x] **Step 2:** Implement via the existing steering queue.
- [x] **Step 3:** Second failing test — a `turn/steer` accepted when the session
      then goes idle **starts its own turn** and is delivered. This is the half
      that makes the guarantee unconditional, and it is the half most likely to
      be skipped.
- [x] **Step 4:** Third failing test — the steer is never delivered twice. The
      next-turn path and the idle-wake path must not both fire for one steer;
      the steering queue drain is the single consumer.
- [x] **Step 5:** Confirm this cannot livelock: a steer that arms a wake, whose
      wake stands down, must not re-arm forever. `runningTurnNameHasOwner`
      (`agent/session_active_turn.go`) is the existing guard; assert it covers
      this producer too.
- [x] **Step 6:** Mutation-check each, run `./agent/ ./server/`, commit.

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

- [x] **Step 1:** Failing test: after `EventError` mid-turn, a following
      `EventAssistantTextDelta` does not publish an active thread carrying a
      `turn_<n>`.
- [x] **Step 2:** Second failing test: after `EventError`, the next turn's
      reasoning delta emits its own `item/started` rather than reusing the failed
      turn's item id.
- [x] **Step 3:** Introduce one `resetTurnScopedState()` with an explicit field
      list, and use it from `closeActiveTurn`, `startTurn` and the `EventError`
      path. `closeActiveTurn` clears seven fields, `startTurn` six, `EventError`
      five — the divergence is undocumented and must end deliberate or gone.
- [x] **Step 4:** Mutation-check each, run `./internal/appprojector/ ./server/`,
      commit.

## Task 5: A rejected control says why (kata `2f41`)

**Corrected premise.** Rejections are not invisible on the wire — the daemon
stamps `MutationOutcome = notAccepted`
(`agent/session_client_mutation_queue.go:177-195`), it reaches the client
(`appwire/errors.go:44-50`), the dispatcher calls `transferToRecovery`
(`cmd/serf-hub/frontend/src/stores/mutationDispatcher.ts:137-141`), and
`QueueStrip` renders a row. The defects are narrower and worse:

- [x] **Step 1: The reason is never persisted.** `MutationRecoveryRecord`
      (`stores/mutationOutbox.ts:57-59`) carries only `recoveryKind`, and
      `mutationOutboxIndexedDB.ts:264` builds it with
      `{ ...record, recoveryKind }` — the wire error's code and message are
      dropped at the storage boundary. Failing test first: a rejected mutation's
      reason survives a round-trip through the outbox. This is a schema change,
      not a rendering change.
- [x] **Step 2: The row does not say it failed.** It is *not* indistinguishable
      from a pending row (pending carries `CLASS.rowPending`, opacity 0.6, and no
      buttons) — it is indistinguishable from a real daemon **queue** row, and
      the header counts it as queued. Failing test, then render the reason.
- [x] **Step 3: A Stop must not become a message.** A rejected `turn/interrupt`
      lands in a text-recovery store; `activateRecovery` (`Composer.tsx:663-688`)
      loads it into the composer draft and `resendRecoveryMutation`
      (`stores/threads.ts:547-563`) re-mints it through `composerMutationIntent`,
      so it can be resurrected as a `turn/start` carrying whatever the user
      typed. Worse, `Composer.tsx:374-390` auto-activates the first rejected
      entry into an empty composer. Failing test, then keep interrupts out of the
      text-recovery path entirely.
- [x] **Step 4: The second rejection path.** `mutationDispatcher.ts:126-134`
      handles a `WireError` with no `clientMutationId` — which is what a Stop
      with an empty `expectedTurnId` takes, since
      `server/appwire_runtime.go:828-830` returns `InvalidParams` naming no
      mutation. Cover it, or Step 2's reason is still blank on that path.
- [x] **Step 5:** Any test here must drive `MutationDispatcher`. A test against
      `threadsStore.steer` cannot observe a server rejection and would be
      tautological — the promise resolves at the local IndexedDB commit.
- [x] **Step 6:** `handleInterruptClick`'s catch (`Composer.tsx:894` today; the
      `:875-885` this step was written against has drifted) is
      **not** dead — `requireClient()`, the not-ready throw
      (`stores/threads.ts:821`) and `requireMutationRuntime` all reach it. Keep
      it; do not "wire or delete" as an earlier draft said.
- [x] **Step 7:** Close kata `2f41` citing test names. Commit.

## Task 6: The client stops bouncing its own second message — REJECTED

> **REJECTED. It was implemented and reverted by `ed6db108a`. Do NOT execute
> this task.**
>
> Routing from the client's own optimistic state disabled Send in the very
> window it targeted. Kata `8c65` (open) records what a correct fix needs, and
> names the two things this version got wrong: real idle capabilities block the
> queue path, and `turn/queue` rejected an empty `expectedTurnId`.
>
> The diagnosis below is still the right description of the window. The Step 2
> instruction is the part that failed; do not re-derive it from here.

The last user-visible window, and the only one that is client-side. Send-vs-Queue
is decided when the message is composed, from `statusType` alone
(`protocol/sendQueueAvailability.ts:63-72`, deliberately — see its header). A
message composed before the UI learns the session went busy is built as
`turn/start` and rejected `Conflict("turn is already active")`.

- [~] **Step 1:** Failing test: two submits in quick succession, the second
      composed before any status frame arrives, both reach the session.
- [~] **Step 2:** Route from the client's own optimistic state as well as the
      published status — the pending-turn machinery already tracks "I just
      started a turn". Do **not** fold `activeTurnId` into
      `deriveSendQueueAvailability`; its header explains why that reintroduces a
      different race.
- [~] **Step 3:** Assert the composer still queues correctly on a cold hydrate,
      where there is no optimistic state. Commit.

## Task 7: Subagent threads — moved out

Split into its own plan at Jesse's request: it is larger than everything else
here combined and sits nearest the `eptj` data-loss precedent, because it puts a
new class of session into the client-mutation store.

See `docs/superpowers/plans/2026-08-16-controllable-subagents.md`. Jesse's
decision (delegates become watchable AND controllable) and the four blockers are
recorded there, along with the design question that must be answered first: what
the parent sees when you stop a delegate. **A delegate does not run inside the
parent's tool call** — an earlier wording here and in that plan said so, and it
is wrong. Delegate runs are rooted in `context.WithCancel(context.Background())`
specifically to outlive the parent tool-call context, and creation returns
`RunningInBackground: true` before the child finishes. The corrected framing is
in that document.

## Task 8: Coverage and hygiene

Each bullet is its own red-green-commit.

- [x] `EventTurnStarted` is in neither fuzz corpus:
      `internal/appprojector/project_fuzz_test.go`'s `projectorCases` and
      `agent/events/eventdata_program_fuzz_test.go`, whose doc claims
      completeness while omitting `TurnStartedData` and `ModelRetryData`. Derive
      the case set from something that changes when the sealed set changes; a
      literal length assertion cannot catch the next omission.
- [x] `agent/session_client_mutation.go:128-141` says three sites write
      `ActiveTurnID`. There are eight, and the omitted
      `finalizeClientMutationInterrupt` (`:566`) is the unconditional clearer the
      identity guard exists to survive.
- [x] Duplicate helpers in `internal/appprojector`: `startedTurnID`
      (`goal_turn_identity_test.go:12`) and `turnStartedID`
      (`turn_boundary_test.go:10`) are the same function, and `completedTurnID`
      is re-implemented inline at `goal_turn_identity_test.go:90-104`. Separately
      in `agent/`, `TestUnservedSessionNamesNoTurn`
      (`session_turn_boundary_test.go:550`) and
      `TestMintRunningTurnIDSkipsUnservedSessions`
      (`session_active_turn_test.go:195`) assert the same thing.
- [x] `server/thread_envelope.go:174-190`: the justification for
      `EventTurnStarted: facetWork` sits after that row and before
      `EventSteeringInjected`, reading as documentation of the wrong entry; and
      `thread_envelope_test.go:71-85` asserts `WorkMillis` while the comment
      argues `ActiveTurnStartedAt`. **Both fixed by `08d511ded`** — the
      description above is of the pre-fix state. Today the comment precedes its
      row (`server/thread_envelope.go:175`) and the test asserts
      `ActiveTurnStartedAt` directly.
- [ ] **Still open.** Dedupe `cmd/serf-hub/e2e_turn_control_test.go` (700 lines):
      the `thread/start` + cleanup block appears three times, steer-and-prove
      twice, interrupt-and-prove three times. Makes visible that only one test
      covers Send-while-busy. (The file is still 700 lines.)
- [ ] **Partly done.** `agent/entrykind_audit_test.go` is weak — a kind added
      after `entryKindCount` leaves both assertions green. Strengthen it, or
      record why not. Do **not** replace it with a `switch` + `invariant.Hold`
      default: that is a production no-op and its test cannot fail. (The "record
      why not" half landed: the test's doc comment now states plainly what the
      audit catches and that it cannot verify a classification is true. The
      after-the-sentinel gap is still open.)

## Task 9: Retire the superseded plans — DONE

- [x] `2026-08-16-one-active-turn-identity.md` still instructed the
      `SteeringInjectedData.StableTurnID` emit that `b5ce354a5` reverted and that
      the projector's `EventSteeringInjected` case
      (`internal/appprojector/appwire_projection.go:691`, reasoning at `:694-701`)
      says "must never be adopted".
      Its header told an agent to execute it task-by-task, so it was a live trap.
      Now banners as LANDED IN PART, with the reverted instructions marked inline
      at Steps 6 and 7 rather than deleted. Kata `dn16`.
- [x] `2026-08-16-one-running-turn.md`: marked superseded by this document,
      keeping its diagnosis and rejected-options record.
      (`2026-08-16-busy-means-named.md` was already marked.)
- [x] `2026-08-16-one-turn-boundary.md` had the same problem and was not on the
      original list: shipped in full, still reading as unstarted, and its Design
      decisions section stale for a second time after `2bf03d10d` moved the mint
      again. Now banners as SHIPPED with the three divergences corrected inline.
      Kata `tqhg`.
- [x] This document: the volatile commit count is gone, Tasks 2, 3 and 6 carry
      markers, the Task 1 RESULT separates what was measured from what was not,
      and the endgame no longer claims Send is finished. Kata `tqhg`.
- [x] `2026-08-16-controllable-subagents.md` was corrected rather than retired —
      it is the one plan here that has not started. Kata `tb2k`.

---

## Definition of done

- [ ] Task 1's harness passes for every turn kind **and** in the between-turn
      gap, on the push path and the read path. **Not met.** The harness only ever
      interrupts after the boundary — see the RESULT block's correction.
- [ ] `make lint` (nine targets), `make build`, root suite, all seven module
      suites, `make test-web`, all live-stack e2e tests. **Not recorded.** Gate
      runs leave no artifact, so nobody can check this off from the tree; treat
      it as unrun.
- [ ] Live browser check: Stop works at every moment of a multi-turn drain,
      including between turns; Steer during a gap lands in the next turn; two
      fast messages both arrive. Zero rejections in the mutation journal — and
      any rejection that does occur is legible on screen. **Partly met** — the
      2026-08-17 pass covered Stop and Steer. The two-fast-messages half is the
      case Task 6 failed to fix; kata `8c65`.
- [x] Katas `c2ty`, `7vmd` and `2f41` closed, each citing a test name. All three
      are closed.

---

## Does this reach the endgame?

Stated plainly, because this plan follows two that promised more than they could
deliver.

**Yes for Stop** — but not by Task 2. `c435bc579` removed the exact-match
precondition rather than adding a way around it, so Stop does not depend on the
client holding a current turn id and survives every window, found or not. Live
browser pass 2026-08-17: Stop ended a running turn and the transcript showed
"System steered: Interrupted".

**Yes for Steer.** Jesse's 2026-08-16 call makes the steer always land: into the
next turn if there is one, and otherwise into a turn of its own. So Steer, like
Stop, stops depending on whether a turn happens to be addressable at the instant
the button is pressed. Shipped as `3393e7905`, `5fdb23fd6`, `daa3eab1b`.

**NOT YET for Send.** The earlier version of this section said "Yes for Send. It
already works, and Task 6 closes the fast-second-message case." Task 6 closes
nothing — it was implemented and reverted by `ed6db108a` because it disabled Send
in the window it targeted. Send works for every case *except* a second message
composed before the UI learns the session went busy. That case is open, and kata
`8c65` records what a fix has to handle. Do not treat Send as finished.

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
work and may have symptoms not yet found. ~~And `main` still carries the e2e disk
leak without its fix — kata `tx4q`.~~ `tx4q` is closed. The open katas from this
branch are `8c65` (the fast-second-message window), `b19h` (the unnameable-turn
suppression covers only the push path) and `ajg5` (the notification stand-down
under a failing mutation store).
