# Busy Means Named Implementation Plan — SUPERSEDED

> **SUPERSEDED by `2026-08-16-one-running-turn.md`. Do NOT execute this plan.**
>
> Two independent reviews found that most of its tasks existed only to keep four
> representations of "the running turn" in step, and that two of its central
> premises were false: `SetProcessingTurn` publishes nothing (only the projector
> produces `thread/status/changed`), and `POST /input` — the path Task 1
> changes — has no production client. Task 1 as written also applies its
> reservation to every non-mutation kind, which would permanently disable every
> notification wake and un-name every goal continuation, reverting two shipped
> fixes.
>
> Kept for its rejected-options record and its diagnosis of the c2ty window, both
> carried forward into the successor. Everything below the header is retained
> as written; its task list is not safe to follow.

**Goal:** It is always possible to steer or stop a session that is running.
The daemon reports a thread busy exactly when it holds a turn name every
mid-turn control will accept — never one without the other, in either
direction.

**Architecture:** Two id namespaces exist and they mean different things. A
*real turn* is named by the durable mint (`turn_m<n>`). The `turn_<n>`
namespace names *non-turns* — the system prelude and the announcement gaps
between turns — so a burst of hook completions folds into one collapsed
disclosure. The bug this plan closes is a category error: the bucket namespace
is being used to name real turns wherever nobody handed the projector a real
name. Two of the three sites that mint a bucket id for a real turn are deleted;
the third is legitimate and stays.

**Tech Stack:** Go 1.25 multi-module workspace (`agent` module, root module's
`internal/appprojector` / `server` / `cmd/serf` / `cmd/serf-hub`), `appwire`
JSON-RPC, React/TypeScript frontend (read-only for this plan).

**Spec:** this document.

**Prior art:** `2026-08-16-one-active-turn-identity.md` (goal continuations,
landed) and `2026-08-16-one-turn-boundary.md` (notification wakes, landed) fix
the same bug for two turn kinds and establish the identity model. This plan is
the third and last piece: closing the window *before* a turn's first event,
which both of those deliberately left open. Kata `c2ty` is that window and
carries the ruling this plan reverses. Kata `2f41` is why none of it was
visible. Kata `eptj` is the data-loss precedent for two minters sharing one
namespace. Kata `7vmd` is the notification turn.

---

## Jesse's ruling (2026-08-16)

> "It should always be possible to steer or stop a session that's running."

This settles kata `c2ty`, whose 2026-07-26 ruling chose to *fill* the window
with a wrong-but-recoverable name rather than empty it. Both options on that
kata are now rejected: a button that does not work and a button that is not
there are the same failure. The window must not exist.

Explicitly rejected along the way, and why, so nobody re-proposes them:

- **Capability gating** (hide Stop/Steer while the name is missing). Rejected:
  during the window the composer would offer neither the turn controls nor
  Send, so the user sees nothing actionable.
- **Splitting `ActiveTurnID` into "running" and "claimed."** Rejected after
  reading the code: the accept-time write is load-bearing — it is what makes a
  turn steerable the instant the client is told the turn exists. Removing it
  moves the window rather than closing it.
- **Removing the eager busy flip so the name and the flag are set together in
  the session** (option "a"). Rejected: `sendQueueAvailability.ts` routes
  Send-vs-Queue on `statusType` **alone**, deliberately, so a second message
  queues instead of bouncing. Widening the idle-looking window makes a fast
  second message route to `turn/start` and get rejected with
  `Conflict("turn is already active")` — silently, per kata `2f41`.

## Global Constraints

- **No backward compatibility.** Jesse's standing call. Delete the superseded
  path rather than keeping both.
- **One minter of live turn ids.** `reserveClientMutationTurnID`
  (`agent/session_client_mutation_queue.go:642`). Kata `eptj` is what two
  minters sharing a namespace did: a collision made `turn/completed` overwrite
  a persisted turn's content. That was real data loss. No task here may add a
  second mint site; the eager reservation in Task 1 calls the existing one
  earlier, from a different goroutine, which is not the same thing.
- **The bucket namespace never names a real turn.** `turn_<n>` is for the
  prelude and for announcement gaps (`preTurnAnnouncementTurnID`, katas `bz2z`
  and `9ekv`). It stays. What goes is every use of it to name something that
  *is* a turn.
- **Mint and release are adjacent.** The `defer` that releases a reserved name
  goes on the line after the mint, in the same function. The leak that shipped
  on this branch happened because the two were in different functions with a
  refusal path between them.
- **Every production line this plan adds must be killed by a named test.**
  Verify by reverting the line and watching a specific test fail. A branch
  review found three lines on this branch that no test killed; do not add more.
- **Live-stack proof, not unit proof, for the user-visible claim.** The claim
  is "Steer and Stop work." The e2e tests in
  `cmd/serf-hub/e2e_turn_control_test.go` are what demonstrate it.

---

## Already landed on this branch

Recorded so an executor does not redo them. Verify with
`git log --oneline 6af43a95a..HEAD` before starting.

| Commit | What |
| --- | --- |
| `457369f11` | The live-stack tests no longer leak ~118MB of Go module cache per run into a directory that cannot be deleted. Pins `GOCACHE`/`GOPATH`/`GOMODCACHE` before `TestMain` redirects `HOME`, matching `cmd/serf/testmain_test.go`. Guarded by `TestGoSubprocessesCacheOutsideTheTestRoot`. |
| `ff189e7fe` | Three production lines that no test killed: the goal continuation's release, `releaseRunningTurnID`'s identity guard, and `acceptNotificationInput`'s persist-before-announce ordering. |
| `1faaa6266` | `openTurn` — one copy of the boundary sequence instead of three. The drift between two of those copies *was* the bug this work exists to fix. |
| `8360363c1` | A wake stands down rather than run a turn it cannot name. See below. |
| `c0692a4e4` | Fixes two review findings against the above: the stand-down was a check-then-mint race, and it ran after the wake state had already been consumed. See below. |

### Why the stand-down matters to the rest of this plan

`inputCh` holds one slot (`server/server.go:388`), and a job-completion wake
that finds it full parks a goroutine blocked on the send
(`server/server.go:768`). Finish a turn while a wake is parked, then send a
message, and the parked send races the `turn/start` that just claimed the
name. When the wake wins, the serve loop runs it while the name belongs to the
user's turn — so the wake runs unnamed, and a notification turn that can last
minutes has no Stop for its whole life.

That is ordinary usage: a background job finished while you were working, and
then you typed something.

The wake now stands down. The user's turn is next and already named, and the
drain loop's tail gate (`peekNotifications`, `agent/session_lifecycle.go:816`)
runs the notification turn inline once that turn ends, when the name is free.
`TestWakeResumesOnceTheUserTurnReleasesTheName` proves the deferral recovers
rather than dropping the notification.

**Consequence for Task 1:** a leaked name is now more dangerous than it was
before this commit. It already wedged `turn/start` and every later agent turn;
it now *also* silently disables every future wake. One missed release kills the
session rather than degrading it. Task 4 exists because of this.

### Two findings against the first attempt, and what they settled

The first version checked `runningTurnNameTaken()` and minted later. Both halves
were wrong, and the fixes are now load-bearing rules rather than local patches.

1. **Check-then-mint is a race.** The predicate and the mint had the whole
   notification drain and a durable append between them, and
   `AcceptClientMutationStart` runs on an RPC goroutine. A `turn/start` landing
   in that gap left the wake proceeding unnamed — the exact failure the guard
   was added to prevent. `mintRunningTurnID` is already a single
   take-or-refuse against the store, so **calling it is the check**. The
   separate predicate was deleted rather than left available to be reused.
   *Rule: never ask whether the name is free. Take it, and read what you got.*
2. **The decision must precede any consumption.**
   `beginRootDelegateAttentionTurn` runs ahead of `acceptNotificationInput` and
   consumes the process-local wake — it clears `rootAttentionWake` and cancels
   any scheduled retry (`agent/session_attention.go:518-521`) — and only an
   *accepted* wake re-arms them. A stand-down after that call stranded the
   delegate attention the wake existed to deliver.
   *Rule: decide while standing down is still free.*
   Pinned by `TestStandDownConsumesNoWakeState`, which fails if the two calls
   are reordered.

**Accepted cost.** Every notification wake now takes a durable name up front,
including the coalesced no-op wakes that are the commonest outcome, and hands
it straight back. An earlier draft of this plan claimed no-op wakes would never
reserve anything; that is no longer true and the claim is withdrawn. A cheap
non-consuming pre-check was considered and rejected: the wake's own accounting
(`drivePendingStableDelegateAttention`, `enqueueOwnCallerWatchSendTokens`) can
*enqueue* work before the count is taken, so a pre-check would sometimes see
zero and drop a real wake. One extra fsync on a path that already fsyncs when
it proceeds is the cheaper mistake.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `cmd/serf/serve.go` | Reserves the name for user input before flipping busy (Task 1); heals a stale name between messages (Task 4). |
| `server/server.go` | `setProcessingLocked` stops minting (Task 2). |
| `server/appwire_runtime.go` | Dead reservation machinery deleted (Task 3). |
| `server/server_handlers.go` | Same (Task 3). |
| `agent/session_active_turn.go` | Mint/release/ownership predicates. |
| `agent/session_lifecycle.go` | Turn opening per `EntryKind`; the dispatch invariant (Task 7). |
| `internal/appprojector/appwire_projection.go` | `openTurn`'s active-gate becomes uniform (Task 5). |

---

## Recommended order

**Task 8 first.** It is the instrument the rest of this work is verified with:
until a rejected control says so, every other task is checked by reading
mutation journals instead of by using the app. Then Tasks 1→5 in order (each
depends on its predecessor), with 6, 7 and 9–11 anywhere.

## Task 1: Reserve the user's turn name before the busy flip

**Why only user input.** This is the one kind where a human is mid-compose, so
it is the only kind where the idle-looking window causes the Send-bounce. It is
also the kind that does not decline on the normal path, so there is the least
to leak. Wakes and goal continuations keep taking their name inside the session
at the top of `processOneInput` — nobody is composing against them, so the
window costs nothing, and a wake that cannot be named stands down instead of
running unstoppable. That also removes the spurious busy→idle flash a coalesced
wake causes today, since a refused wake never flips busy at all.

The asymmetry is deliberate and is exactly the kind that caused the original
bug, so Task 7 makes every `EntryKind` declare which path it uses.

**Files:**
- Modify: `cmd/serf/serve.go:1012-1017` (the `!msg.ClientMutationStart` branch)
- Modify: `agent/session_active_turn.go` (export the mint/release for serve.go)
- Test: `cmd/serf/serve_turn_name_test.go` (create)

**Interfaces:**
- Produces: `(*agent.Session).MintRunningTurnID() string` and
  `(*agent.Session).ReleaseRunningTurnID(string)` — exported wrappers over the
  existing unexported pair. No new mint site.
- Consumes: `(*server.Server).SetProcessingTurn(string)` — already exists and
  already sets `processing` and stamps the durable name under one hold of
  `s.mu` (`server/server.go:691`). This is the atomic operation kata `c2ty`'s
  2026-07-26 comment asked for; it simply had no caller on this path.

- [ ] **Step 1: Write the failing test**

The behavioural claim: after the serve loop accepts a non-mutation user input
and before the session has emitted anything, `thread/read` reports the thread
active **and** carries a name the mutation preconditions accept.

```go
func TestUserInputIsNamedBeforeItRuns(t *testing.T) {
	// Stand up a session + server the way serve_test.go does, submit an
	// EntryUserInput message, and block the session before its first event.
	// Assert the wire shows status active AND an ActiveTurnID with the
	// "turn_m" prefix -- never active with a turn_<n>.
}
```

- [ ] **Step 2: Run it and watch it fail**

Expected: the thread reports active with a `turn_<n>`, because
`setProcessingLocked` invented one.

- [ ] **Step 3: Reserve before flipping**

```go
if !holdServeStateForAwaitingWake(msg.Kind, sess.HasPendingAsk()) {
	// Name the turn before announcing it busy. Status and the name ride
	// the same frame for the composer, and a busy thread with no name is
	// a thread whose Stop button cannot work.
	turnID := sess.MintRunningTurnID()
	defer sess.ReleaseRunningTurnID(turnID) // adjacent by constraint
	if turnID != "" {
		srv.SetProcessingTurn(turnID)
	} else {
		srv.SetProcessing(true)
	}
	srv.SetState(string(agent.SessionProcessing))
}
```

The `turnID == ""` fallback is not dead: an unserved session or a store
failure both yield it, and neither should stop the turn from running.

- [ ] **Step 4: Teach `processOneInput` to adopt rather than re-mint**

`mintRunningTurnID` returns `""` when `ActiveTurnID` is already set, so the
turn's opening event would otherwise carry no name. `EntryUserInput` must use
the name the serve loop reserved. Take it from the queued client-mutation
identity where one exists, and from the durable snapshot otherwise.

- [ ] **Step 5: Run the test, plus `go test ./agent/ ./server/ ./cmd/serf/`**

- [ ] **Step 6: Mutation-check** — drop the `SetProcessingTurn` call and watch
  the new test fail; restore.

- [ ] **Step 7: Commit**

## Task 2: `setProcessingLocked` stops naming turns

**Files:**
- Modify: `server/server.go:712-718`
- Test: `server/appwire_server_test.go`

`server/server.go:715` is the single production line that mints a bucket id for
a real turn. With Task 1 supplying the name, it has nothing left to do.

- [ ] **Step 1:** Write a test asserting the wire never reports a thread active
  with an id outside the `turn_m` family. Watch it fail.
- [ ] **Step 2:** Delete the `s.appActiveTurnID = s.appProjector.ReserveTurnID()`
  branch.
- [ ] **Step 3:** Run `go test ./server/ ./internal/appprojector/`. Expect
  `TestServerAppWireProcessingWithoutReservedTurnIDReadsActiveWithNoTurnID`
  (kata `c2ty`'s own proof test) to need updating — it pins the behaviour being
  deleted. Update it to assert the new contract and say so in the message.
- [ ] **Step 4: Commit**

## Task 3: Delete the dead reservation machinery

`turn/start` migrated to the durable client-mutation path in `954e5ff93`;
`handleAppTurnStart` (`server/appwire_runtime.go:735`) goes entirely through
`retrySafeTurns.Start`. The old mechanism was left standing.

**Verified dead:** `reserveAppTurnIDForStart` and `releaseAppTurnID` have no
production callers (tests only), so `appReservedTurnID` is never written
non-empty in production.

**Files:**
- Modify: `server/appwire_runtime.go` (delete both funcs, the field's reads at
  `:119`, `:1039`, `:1371`, and `:1412-1418`)
- Modify: `server/server.go` (the field at `:280`, writes at `:697`, `:717`)
- Modify: `server/server_handlers.go:241`
- Modify: the four test files that call the deleted helpers

Both surviving readers (`server_handlers.go:241`,
`appwire_runtime.go:1039`) refuse a model change while a turn is active or
reserved; with the field always empty, both reduce to `if processing`.
`appCapabilities`'s `active := processing || appReservedTurnID != ""`
(`:1371`) reduces the same way.

- [ ] **Step 1:** Delete, adjusting each reader to drop the reserved-id half.
- [ ] **Step 2:** `go build ./... && go test ./server/`. The compiler finds
  every site; that is the completeness net, per
  `docs/conventions/go-workspace.md`.
- [ ] **Step 3: Commit**

## Task 4: Heal a stale name between messages

**Why.** A reserved name can outlive its turn in ways no `defer` prevents:
the process dies between the write and the release (`kill -9`, OOM, power
loss — deferred functions do not run on `os.Exit` or SIGKILL), or the release
write itself fails on a full disk. `forgetRunningTurnNoOneOwns` already clears
such a name, but only on load, so a live session stays wedged until restart —
and since `8360363c1` a wedged session also silently drops every wake.

Run the same rule at the one moment it is provably safe.

**The safety constraint, which MUST be verified before writing the code:** the
predicate clears a name that no pending execution owns. An agent-minted name
(a wake or a goal turn) has no pending execution by design, so running this
while such a turn is in flight would clear a live turn's name and cause the
exact bug this plan closes. It is safe **only** where no turn is running.

- [ ] **Step 1: Verify the placement constraint.** At the top of the serve
  loop, before `processNextServeInput` blocks on the channel, prove no turn
  outlives the previous `processMessage` returning. Specifically check the
  drain-on-interrupt path (`nextTurnCtx`, `serve.go:995`) and the async
  mutation runner (`setMutationRunner` / `runnerDone`). **If this does not
  hold, stop and report rather than shipping the heal on faith.**
- [ ] **Step 2:** Write a failing test: seed a durable `ActiveTurnID` that no
  pending execution owns, run one serve-loop iteration, assert it is cleared
  and that a subsequent `turn/start` is accepted.
- [ ] **Step 3:** Write a second failing test for the case that must NOT be
  cleared: an `ActiveTurnID` a pending `turn/start` owns survives.
- [ ] **Step 4:** Implement, reusing `forgetRunningTurnNoOneOwns`'s predicate
  rather than restating it.
- [ ] **Step 5:** Mutation-check both directions, then commit.

## Task 5: One active-gate, not two

With Tasks 1 and 2 landed, every real turn on a served session carries a name,
so the gate can be uniform.

**Files:**
- Modify: `internal/appprojector/appwire_projection.go` (`openTurn`, and the
  three cases that currently publish `active` themselves)

Today `EventTurnStarted` withholds `thread/statusChanged: active` for a turn it
could not name, while `EventUserInput` and `EventGoalContinuation` publish it
unconditionally. That inconsistency is a real defect (found independently by a
branch reviewer) and it disappears by moving the status frame into `openTurn`
behind the single `stableID != ""` check.

**This task must not land before Task 1.** `session_lifecycle.go:663` zeroes
the mutation identity after the first turn in the drain loop, so drained
follow-up turns emit `EventUserInput` with an empty `StableTurnID` today.
Making the gate uniform first would hide Stop on those turns.

- [ ] **Step 1:** Write a failing test: a goal turn with no name does not
  publish active.
- [ ] **Step 2:** Move the status frame into `openTurn` behind the gate; delete
  the three call-site copies.
- [ ] **Step 3:** Mutation-check, run `./internal/appprojector/ ./server/`,
  commit.

## Task 6: Descendant threads get their turn boundary

Both branch reviewers found this independently. `agent/session_lifecycle.go`
gates the `EventTurnStarted` emit on `servedByDaemon()`, but `sendEvent`
forwards every event to `descendantEvent` regardless, and each descendant has a
real AppWire projection a client reads as a `subagent` thread. Child sessions
do run notification turns (`agent/subagents.go:1251`). So a delegate's wake
appends its items to the *previous* turn in the rendered descendant thread —
kata `7vmd`'s bug, left standing on the one thread family the fix skipped.

The gate conflates "cannot take a client mutation" (true, and the right reason
not to *mint*) with "needs no turn boundary" (false — separation is a rendering
property descendants do get).

- [ ] **Step 1:** Write a failing test asserting an unserved session forwards a
  boundary to its descendants. This inverts
  `TestUnservedSessionAnnouncesNoBoundary`, which pins today's behaviour;
  replace it and say so in the commit message.
- [ ] **Step 2:** Drop the `servedByDaemon()` emit gate, keeping it on the
  mint. `mintRunningTurnID` already returns `""` for unserved sessions, so the
  boundary carries no name, costs no durable write, and publishes no status.
- [ ] **Step 3:** Confirm `sendEvent` drops the channel send when nobody
  drains, so a served-session cost is not introduced. Commit.

## Task 7: Put the `EntryKind` tripwire on the code that runs

Both reviewers want `agent/entrykind_audit_test.go` replaced, for different
reasons: the sentinel-based audit is walked around by the exact mistake it
guards (a kind added *after* `entryKindCount` leaves both assertions green),
and `processOneInput`'s dispatch chain is already the real table — one that
silently treats an unknown kind as user input.

- [ ] **Step 1:** Convert the `if/else if` chain
  (`agent/session_lifecycle.go:1050-1066`) to a `switch kind` with
  `default: invariant.Hold(false, "unclassified entry kind")`.
- [ ] **Step 2:** Record, next to that switch, which naming path each kind uses
  — eager reservation (Task 1) or mint-at-commit — so Task 1's deliberate
  asymmetry is stated where it is implemented.
- [ ] **Step 3:** Delete `agent/entrykind_audit_test.go` and the
  `entryKindCount` production const.
- [ ] **Step 4:** Test that an unhandled kind trips the invariant. Commit.

## Task 8: A rejected control tells the user (kata `2f41`)

**Do this first.** This is why the bug survived weeks instead of a day: every
Steer, Send and Stop the user pressed was rejected with
`Conflict("turn is not active")` and the composer showed nothing at all. The
daemon's mutation journal recorded five rejections on the session that started
this investigation; the UI recorded none of them.

Every other task in this plan is currently verified by reading a durable
journal after the fact. With this landed they are verified by using the app,
which is also how a user would report the next instance of this class.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
  (the `busyAction` dispatchers at `:814`, `:854`, `:876`)
- Modify: the mutation dispatcher's settle path, so a rejected durable
  intent surfaces rather than settling silently
- Test: `Composer.test.tsx`, plus a live-stack assertion

**Design constraint.** A rejection is not always the user's problem: a Stop that
loses a race with the turn's own completion is benign. Distinguish "the turn you
aimed at is gone" (say nothing, or say it quietly) from "the daemon refused and
nothing happened" (say it). Getting this wrong in the noisy direction trains
people to ignore it, which is the same failure as silence.

- [ ] **Step 1:** Write a failing test: a `turn/steer` that resolves to a
      `ConflictError` leaves user-visible evidence.
- [ ] **Step 2:** Decide and write down the taxonomy above before implementing.
- [ ] **Step 3:** Implement, covering steer, queue, drain, interrupt and start.
- [ ] **Step 4:** Extend one live-stack e2e test to assert the surfaced state,
      so this cannot regress to silence.
- [ ] **Step 5:** Close kata `2f41` citing the test name. Commit.

## Task 9: Fix the plan that reintroduces a reverted bug

`docs/superpowers/plans/2026-08-16-one-active-turn-identity.md:501` still
instructs an implementer to emit
`events.SteeringInjectedData{... StableTurnID: stableTurnID}`, and `:143`
describes the same field as part of the design. That change was reverted in
`b5ce354a5` as actively wrong: `internal/appprojector/appwire_projection.go:693-695`
now carries a comment saying that field names the steering mutation's own
record, "not the turn the steer lands in", and "must never be adopted".

The plan's header tells an agent to execute it task-by-task, so re-running it
reintroduces the bug this branch exists to fix.

- [ ] Correct both sites, keeping the superseded reasoning in a `<details>`
      block rather than deleting it (the sibling plan's `ac0560303` is the
      pattern).
- [ ] Add a pointer from that plan to this one as its successor.
- [ ] Re-check the same document for the two other stale claims a review found:
      `:12-15` and `:150` still say `SetProcessing` stops minting turn ids,
      which was dropped from that plan's scope (`:551`) and is Task 2 here.

## Task 10: Turn-identity coverage the fuzzers and helpers miss

- [ ] `EventTurnStarted` is in neither fuzz corpus. Add it to
      `internal/appprojector/project_fuzz_test.go`'s `projectorCases`
      (`:18-94`) so `FuzzProject` can reach the new case and its empty-id
      path, and to `agent/events/eventdata_program_fuzz_test.go` (`:14-17`),
      whose doc claims it "runs every member of the sealed event payload set"
      and now omits both `TurnStartedData` and `ModelRetryData`. Add a length
      assertion so the next omission fails instead of passing quietly.
- [ ] Register the turn-boundary tests in `projectCoverageSweep`'s registry
      (`:142-198`). The existing `turn_started_at` row is about
      `Turn.StartedAt` and reads like coverage it is not.
- [ ] `agent/session_client_mutation.go:128-141` says three sites write
      `ActiveTurnID`. There are seven, and the omitted one
      (`finalizeClientMutationInterrupt`, `:566`) is the *unconditional
      clearer* that `releaseRunningTurnID`'s identity guard exists to survive.
      A reader auditing "who can clear this" from that comment misses exactly
      the one that matters.
- [ ] Collapse the duplicate helpers: `startedTurnID`
      (`goal_turn_identity_test.go:12-32`) and `turnStartedID`
      (`turn_boundary_test.go:10-30`) are the same function under two names,
      and `completedTurnID` is re-implemented inline at
      `goal_turn_identity_test.go:90-104`. Likewise
      `TestUnservedSessionNamesNoTurn` and
      `TestMintRunningTurnIDSkipsUnservedSessions` assert the same thing about
      the same call; keep one.

## Task 11: Turn-scoped state and the boundary's own bookkeeping

- [ ] **One reset for per-turn projector state.** Three places clear
      overlapping subsets of the same fields: `closeActiveTurn` clears seven,
      `startTurn` clears four *different* ones, and `EventError`
      (`appwire_projection.go:688-694`) clears a third four. `closeActiveTurn`'s
      comment notices the `EventError` divergence and declines to touch it
      without asking whether the smaller set is right. It probably is not:
      after a failed turn `reasoningItem`, `toolArgsByKey` and `toolStartByKey`
      survive into the next turn and `startTurn` does not clear them either, so
      a reasoning delta arriving before the next round's `ASSISTANT_TEXT_START`
      reuses the failed turn's item id with no `item/started`. Give it one
      `resetTurnScopedState()` with an explicit list, making the divergence
      either deliberate and documented or gone.
- [ ] **Move the misplaced justification.** `server/thread_envelope.go:176-188`
      puts the 13-line argument for `events.EventTurnStarted: facetWork` *after*
      that row and immediately before `events.EventSteeringInjected:`, so it
      reads as documentation of the wrong entry. The test pinning it
      (`thread_envelope_test.go:71-83`) asserts `WorkMillis` while the comment's
      whole argument is about `ActiveTurnStartedAt`; assert what the comment
      argues.
- [ ] **Dedupe the e2e scaffolding.** `cmd/serf-hub/e2e_turn_control_test.go`
      (695 lines) repeats the `thread/start` + cleanup block three times, and
      the steer-and-prove and interrupt-and-prove blocks twice each. Two
      helpers cut ~140 lines and make the three tests read as the three turn
      kinds they are — which also makes visible that only one of them covers
      Send-while-busy.

---

## Definition of done

- [ ] The wire never reports a thread active without a `turn_m` name, on the
      push path and the read path, verified by test rather than by argument.
- [ ] `make lint` (all 7 gates), `make build`, the root suite, all seven module
      suites, `make test-web`.
- [ ] All three live-stack e2e tests in `cmd/serf-hub/e2e_turn_control_test.go`
      pass, and a fourth covers a fast second message routing to `turn/queue`
      rather than bouncing.
- [ ] Live browser check: Steer and Stop render and apply on a notification
      turn, a goal turn and a user turn, with zero rejections in the session's
      mutation journal.
- [ ] Kata `c2ty` closed with the ruling above and the commit that implements
      it. Kata `7vmd` closed. Kata `2f41` closed by Task 8.
- [ ] Every control verified from the browser rather than from a journal —
      which Task 8 is what makes possible.
- [ ] `docs/web-ui/decisions.md` updated if any decision it records changed.

## Filed elsewhere

Found by the same reviews but not about turn identity, so they are katas rather
than tasks here:

| Kata | What |
| --- | --- |
| `tx4q` | **Urgent.** `main` carries the leaking e2e tests without their cache fix, so every `go test ./...` on main refills the disk. Fixed at `457369f11` on this branch; cherry-pick or merge. |
| `3htx` | The browser-guard consolidation deleted the only CDP unit test and the Chrome startup diagnostic, and claimed the intent was preserved. |
| `afpk` | `fakellm`'s standalone driver counts model rounds globally, so both documented modes break with a second session. |
| `cvsk` | `e2e-webui-turn-controls.sh --help` hides `--stop`, its only documented teardown. |
| `cg10` | `fakellm`'s package-doc example does not compile. |
| `8bh1` | `Spawn.tsx` reloads the model catalog on every keystroke in the working-directory field. |
| `z5fm` | `EntryWatchDelivery` has no production producer, leaving six dead branches. |

## Known limits, stated rather than hidden

- **The leak is recoverable, not impossible.** Two of its four causes (a return
  between mint and release; another owner's name) are prevented structurally.
  The other two (process death, a failed release write) cannot be prevented at
  all — you cannot write durable state and guarantee you will get to unwrite
  it. Task 4 bounds the exposure to one serve-loop iteration instead of one
  restart. If Task 4's placement constraint does not hold, the exposure is one
  restart and that must be said out loud rather than papered over.
- **The Send-bounce window shrinks but is not proven gone.** Task 1 closes it
  for non-mutation user input. The `turn/start` path already flips busy only
  when the serve loop dequeues the message, so a second message composed in
  that window is still built as `turn/start` and rejected. Today that window is
  milliseconds because the serve loop is parked on the channel — reasoned from
  the code path, not measured. Measuring it, and deciding whether the client
  should route on its own optimistic state, is follow-up work and belongs in
  its own kata rather than in this plan.
