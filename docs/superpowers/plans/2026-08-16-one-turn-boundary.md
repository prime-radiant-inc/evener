# Notification Turns Get Their Own Turn Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A turn opened by a notification wake becomes a real, named, *visibly
active* turn — separate from the turn before it, and addressable by Steer,
Send and Stop.

**Architecture:** A notification turn is the only entry kind that both takes
client mutations and opens with no event of its own. It gets one:
`EventTurnStarted`, carrying the id the daemon reserved. The AppWire projector
closes whatever turn is open, opens the named one, and publishes the thread as
active — the three things a turn boundary owes its subscribers. An audit test
forces every present and future `EntryKind` to declare how its turn opens.

**Tech Stack:** Go 1.25 multi-module workspace (`agent` module, root module's
`internal/appprojector` / `server` / `cmd/serf-hub`), `appwire` JSON-RPC.

**Spec:** this document.

**Prior art:** `docs/superpowers/plans/2026-08-16-one-active-turn-identity.md`
landed the same fix for goal-continuation turns and establishes the identity
model. Kata `7vmd` is this turn kind. Kata `2f41` is why nobody noticed. Kata
`c2ty` records a ruling this plan deliberately does not touch. Kata `eptj` is
the data-loss precedent for two minters sharing a turn-id namespace.

## Global Constraints

- **No backward compatibility.** Jesse's call. Delete the superseded path.
- **Publish and reserve together, or do neither.** Kata `7vmd`'s own words. A
  turn that holds `snapshot.ActiveTurnID` without publishing a matching,
  *active* thread status is worse than the bug: the composer offers Send,
  which routes to `turn/start`, which `AcceptClientMutationStart` then rejects
  with `Conflict("turn is already active")` (`agent/session_client_mutation.go:216`).
- **One minter of live turn ids.** `reserveClientMutationTurnID`
  (`agent/session_client_mutation_queue.go:642`). Kata `eptj` is what two
  minters sharing a namespace did: a collision made `turn/completed` overwrite
  a persisted turn's content. (Note: no test enforces single-minter-ness;
  `agent/session_client_mutation_turn_namespace_test.go` pins only that
  reserved ids stay outside the transcript entry-index namespace.)
- **Nothing names a turn out of band.** The projector consumes events on its
  own goroutine (`agent/session_events.go:95`, `cmd/serf/serve.go:190-195`),
  so a side-channel announcement races the stream.
- **Every minted id must be released on every path.** The mint writes durable
  state that gates all later turns; a leak wedges the session for the life of
  the process.
- **Every production line this plan adds must be killed by a test.** Each task
  names its mutations; apply each, watch the test fail, revert.
- Gates: `make lint`, `make build`, `go test ./...`, the seven module suites
  (`go test primeradiant.com/serf/<mod>/...` for agent, auth, envvars, fuzz,
  identifier, invariant, llm), `make test-web`.
- Never `git stash`; never `git checkout <file>` to undo.

---

## The identity model (established, not proposed)

A turn's identity is minted once, by the Session, at the moment the turn
begins, and reaches the AppWire projection on an event. Two facts share
`clientMutationSnapshot.ActiveTurnID` under one rule:

> ActiveTurnID names the running turn, and it persists exactly as long as the
> thing that would resume that turn persists.

A client turn's pending execution is reclaimed and re-run on restart, so its
id survives (`agent/session_client_mutation.go:235`, `agent/session_queue.go:581`).
An agent turn has nothing to resume it, so `loadClientMutationSnapshotFS`
drops an id no pending execution owns
(`agent/session_client_mutation_persist.go:74-84`). Mid-turn mutations compare
`expectedTurnId` against this field (`session_client_mutation_queue.go:123,325,392,497`,
`session_client_mutation.go:421`).

---

## Diagnosis

`processOneInput` (`agent/session_lifecycle.go:1046-1057`) dispatches five
entry kinds (`:388-408`):

| Entry kind | Opens the projector turn via | Named? | Client-addressable? |
| --- | --- | --- | --- |
| `EntryUserInput` | `EventUserInput` (`appwire_projection.go:229`) | yes — `UserInputData.StableTurnID` | yes |
| `EntryContinuation` | `EventGoalContinuation` (`:289`) | yes — `GoalContinuationData.StableTurnID` | yes |
| `EntryNotification` | **nothing of its own** | **no** | **yes — and broken** |
| `EntryDelegateAttention` | nothing (`acceptDelegateAttentionInput`, `:1510`) | no | child session only |
| `EntryWatchDelivery` | — | — | **no production producer at all** |

`acceptNotificationInput` (`:1527`) emits `EventSteeringInjected` for its
reminder (`:1575`), and that projector case (`appwire_projection.go:733-773`)
never calls `ensureTurn` — it only produces `serf/steering/injected`. The
pending-steering path emits `EventSteeringInjected` too, via
`injectDrainedSteering` → `consumeSteeringMessage` (`agent/session_queue.go:843,852`).
Neither opens a turn.

Three live defects:

**1. Idle wake → an id no client can name.** `SubmitNotification`
(`server/server.go:755`) wakes an idle session. `cmd/serf/serve.go:1014` calls
`srv.SetProcessing(true)`, and `setProcessingLocked` reserves `turn_<n>` and
publishes it as `appActiveTurnID` **before the session goroutine runs**
(`server/server.go:712-716`); `startTurn` then consumes that reservation
(`appwire_projection.go:1661-1664`). So the wire carries a `turn_<n>` while the
preconditions hold `turn_m<n>`, and every `turn/steer`, `turn/queue`,
`turn/drainAsSteer`, `turn/promoteQueuedAsSteer` and `turn/interrupt` is
rejected `Conflict("turn is not active")` — invisibly (kata `2f41`).

**2. Idle wake → the thread never goes active for a live subscriber.** The only
producers of `thread/statusChanged` are `:193` (session start), `:252`
(`EventUserInput`), `:310` (`EventGoalContinuation`) and `:1083` (session end).
On an idle wake the previous turn ended at `:1083` with `idle`, and a
notification turn publishes nothing. Capabilities ride that same frame by
design (`server/appwire_runtime.go:396-406`: "a client applying both fields of
one notification can never hold a status and a capability set that
disagree"), so the client keeps `steer:false, interrupt:false`. The composer
gates on it — `isTurnActive` requires `statusType === "active"`
(`cmd/serf-hub/frontend/src/panes/session/composer/submitRouting.ts:47-49`),
and `showSteer`/`showStop` require `busy`
(`.../composer/Composer.tsx:516,531-532`). **Stop and Steer are not even
rendered.** Naming the turn without fixing this delivers nothing.

**3. Interleave → a turn that swallows the next one.** The drain loop's
notification rung (`agent/session_lifecycle.go:878-883`) `continue`s into an
`EntryNotification` turn without leaving `processInputKindWithProvenance`.
`finishProcessingAtBoundary` (`agent/session_state.go:208-225`) emits
`EventTurnEnded`, which the projector only *stashes* for timing
(`appwire_projection.go:1037-1045`). The previous turn stays open, the
notification turn's items append to it, and `EventSessionEnd` closes it at the
end of the whole drain with merged content and a foreign duration.

**4. Nothing prevents recurrence.** Go enforces no exhaustiveness over
`EntryKind`, and two of five kinds were never considered when the naming
mechanism was designed.

### Evidence

Live session `0348HuXSlWRtoLEoQ4EOE8`: durable `ActiveTurnID` `turn_m6`, wire
`turn_6`, and 3× `turn/steer` + 1× `turn/queue` + 1× `turn/interrupt` recorded
`rejected — "turn is not active"` in `<state-dir>/mutations/<SID>.json`.
`scripts/e2e-webui-turn-controls.sh` is the harness.

---

## Design decisions

**The boundary publishes three things**: `turn/completed` for the turn it
closes, `turn/started` for the one it opens, and `thread/statusChanged: active`.
Defect 2 is why the third is not optional.

**The id is minted in `processOneInput`, not in `acceptNotificationInput`.**
Minting and announcing are separable. The mint sits beside the existing
continuation mint so it reuses the existing `defer releaseRunningTurnID`, and
the notification branch's refusal path releases explicitly — the same shape
that branch already has. Minting inside the accept function would leak the id
on every proceeding turn, which wedges `turn/start` for the life of the
process and makes the *next* notification turn refuse to mint.

**The boundary is emitted only when the turn is named.** An unnamed boundary
carries no information, and child sessions run `EntryNotification` too
(`agent/subagents.go:1251`, `childSess.ProcessInputKind(driveCtx, "", nil, EntryNotification)`).
Gating on a non-empty id keeps every descendant projection
(`server/appwire_runtime.go:246-291`) bit-identical to today.

**`EventUserInput` and `EventGoalContinuation` are untouched.** They already
name their turns and are two of the four `thread/statusChanged` producers.
Opening a turn earlier in `acceptUserInput` would also relocate `SessionStart`
hook announcements (`session_lifecycle.go:1364-1372`) out of the prelude bucket
`preTurnAnnouncementTurnID` groups them into (`appwire_projection.go:1687-1720`,
katas `9ekv`/`bz2z`).

**Three close blocks are factored; the fourth is not.** `:197-224`, `:262-281`
and `:1059-1081` reset the same eight fields and differ only in the turn
status. `:701-724` resets five, has no `activeTurnID != ""` guard, carries an
`appwire.TurnError` with `TurnStatusFailed`, and calls `ensureTurn` first —
one helper cannot express it without an error parameter and a silent change to
what the failure path clears.

**Delegate-attention turns are not named.** They run only on a child session
(`agent/subagents.go:1424-1428`, on `a.sess`), unserved by construction
(`agent/session_lossless_events_test.go:238-261`), and descendant mutations
"remain owned by the agent tree" (`server/appwire_runtime.go:243-245`).

**`setProcessingLocked`'s `ReserveTurnID` branch stays** (`server/server.go:713`)
per kata `c2ty`'s 2026-07-26 ruling. The residual window is therefore
"wake → boundary event", during which the wire still carries that `turn_<n>`.
Task 5 must wait past it rather than pretend it is closed.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `agent/events/events.go` (modify) | `EventTurnStarted`, beside `EventTurnEnded` (`:109-111`). |
| `agent/events/payloads.go` (modify) | `TurnStartedData{TurnID}`. |
| `agent/events/eventdata.go` (modify) | `eventKind()` binding (`:79`) and the `var _ EventData` list (`:91-133`). |
| `agent/events/payloads_test.go` (modify) | In-package binding test. |
| `agent/session_lifecycle.go` (modify) | Mint beside the continuation mint; pass the id into `acceptNotificationInput`; release on its refusal; emit the boundary; `entryKindCount`; update the now-false comment at `:1035-1040`. |
| `agent/session_turn_boundary_test.go` (create) | Wiring + release coverage. |
| `agent/entrykind_audit_test.go` (create) | Every `EntryKind` declares how its turn opens. |
| `agent/session_client_mutation.go` (modify) | The `ActiveTurnID` doc comment at `:128-135` names its writers; add this one. |
| `internal/appprojector/appwire_projection.go` (modify) | `closeActiveTurn(status)` factored from three sites; one `EventTurnStarted` case; update the "four completion sites" comments at `:719-722` and `:1266-1271`; drop the stale `EventSteeringInjected` note at `:736-742`. |
| `internal/appprojector/turn_boundary_test.go` (create) | Boundary coverage. |
| `server/thread_envelope.go` (modify) | Facet row. |
| `server/thread_envelope_test.go` (modify) | The freshness case for that row. |
| `cmd/serf-hub/e2e_turn_control_test.go` (modify) | Live-stack regression. |

---

### Task 1: The boundary event

**Files:** `agent/events/{events,payloads,eventdata}.go`; test in
`agent/events/payloads_test.go` (in-package — `events_test.go` is
`package events_test` and cannot reach `eventKind()`).

**Interfaces:** produces `events.EventTurnStarted` and
`events.TurnStartedData{TurnID string}`. No `Kind` field: `agent/events`
cannot import `agent` (cycle), `EntryKind` has no `String()`, and no consumer
here needs it.

- [ ] **Step 1: failing test**

```go
func TestTurnStartedDataBindsItsKind(t *testing.T) {
	if got := TurnStartedData{}.eventKind(); got != EventTurnStarted {
		t.Fatalf("TurnStartedData.eventKind() = %q, want %q", got, EventTurnStarted)
	}
}
```

- [ ] **Step 2: watch it fail** — `go test primeradiant.com/serf/agent/events/ -run TestTurnStartedDataBindsItsKind -v`.
- [ ] **Step 3: implement**

```go
	// EventTurnStarted marks a turn beginning, carrying the identity the daemon
	// reserved for it. It exists for turns that open with no content event of
	// their own: a notification wake announces its turn through nothing else, so
	// without this its items join the previous turn and its id is one no
	// mutation precondition accepts (kata 7vmd). Turns whose first event already
	// names them -- EventUserInput, EventGoalContinuation -- do not emit it.
	EventTurnStarted EventKind = "TURN_STARTED"
```

```go
// TurnStartedData is the payload for an EventTurnStarted event.
type TurnStartedData struct {
	// TurnID is the identity the daemon's mutation preconditions accept for this
	// turn. Never empty: the emitter suppresses the event entirely rather than
	// announce a turn it cannot name.
	TurnID string `json:"turn_id"`
}
```

Add the `eventKind()` binding and the `var _ EventData = TurnStartedData{}`
line. (That list is documentation, not a gate — a payload with the method
satisfies the interface either way; the real binding is `events.New`
(`eventdata.go:25-31`), which the test above pins.)

- [ ] **Step 4: watch it pass**, then `go test primeradiant.com/serf/agent/events/...`.
- [ ] **Step 5: commit.**

---

### Task 2: Mint once, release always, announce when named

**Files:** `agent/session_lifecycle.go`; create `agent/session_turn_boundary_test.go`.

**Interfaces:** `acceptNotificationInput` gains a trailing
`stableTurnID string` parameter. Update its ~12 test call sites
(`grep -rn "acceptNotificationInput" --include="*_test.go" agent/`).

**Shape** — the mint moves nowhere new; it joins the existing one:

```go
	var runningTurnID string
	if kind == EntryContinuation || kind == EntryNotification {
		runningTurnID = s.mintRunningTurnID()
	}

	if kind == EntryContinuation {
		s.acceptContinuationInput(ctx, input, runningTurnID)
	} else if kind == EntryNotification {
		if !s.acceptNotificationInput(ctx, runningTurnID) {
			s.releaseRunningTurnID(runningTurnID)
			return "", false, nil
		}
		rootAttentionAccepted = true
	} else if ...
```

The existing `defer s.releaseRunningTurnID(runningTurnID)` below the chain
then covers every proceeding path, exactly as it already does for
continuations.

Inside `acceptNotificationInput`, the announce goes after the last refusal
(`appendSteeringTurnDurably`, `:1568-1574`) and before the first content event
(the reminder emit at `:1575`), guarded on a non-empty id:

```go
	if stableTurnID != "" {
		s.emit(events.EventTurnStarted, events.TurnStartedData{TurnID: stableTurnID})
	}
```

Everything after that refusal proceeds or warns
(`markJobNotificationsDelivered`, `settleDeliveredWatchNotification`,
`injectDrainedSteering`), and `repairOrphanedToolResults` emits only
`EventWarning`, which does not open a turn — so no content can precede the
boundary.

- [ ] **Step 1: failing tests** in `agent/session_turn_boundary_test.go`. Use
      `ConsumeEventsLossless` to collect — it is the only writer of
      `authoritativeConsumer` (`agent/session.go:109-123`), so a real drain is
      also what makes the session "served" for `mintRunningTurnID`.

1. `TestNotificationTurnAnnouncesOneNamedBoundary` — seed a pending steering
   message so the wake proceeds with no job notifications; assert exactly one
   `EventTurnStarted` whose `TurnID` has prefix `turn_m`.
2. `TestNotificationBoundaryPrecedesItsReminder` — seed a job notification;
   assert the `EventTurnStarted` index is lower than the `EventSteeringInjected`
   index.
3. `TestNotificationTurnReleasesItsTurnID` — **the leak guard**: after a
   *proceeding* wake returns, `s.clientMutations.snapshot().ActiveTurnID == ""`.
4. `TestRefusedNotificationWakeAnnouncesNoBoundary` — nothing pending: zero
   `EventTurnStarted`, and `ActiveTurnID == ""`. Call
   `s.ensureClientMutationStore()` first: `s.clientMutations` is created lazily
   (`session_client_mutation_queue.go:88-100`) and nothing on the refused path
   creates it, so `snapshot()` would nil-deref.
5. `TestUnservedSessionAnnouncesNoBoundary` — no drain registered: zero
   `EventTurnStarted`, so descendant projections are untouched.

- [ ] **Step 2: watch them fail.**
- [ ] **Step 3: implement.**
- [ ] **Step 4: watch them pass, then run each mutation:**
      - mint → `""`: test 1 fails on the prefix, test 5 still passes.
      - announce moved below the reminder emit: test 2 fails.
      - drop the `defer`'s coverage (assign `runningTurnID` after the chain):
        test 3 fails.
      - announce moved above the `:1547` early return: test 4 fails.
      - drop the `stableTurnID != ""` guard: test 5 fails.
      A mutation that does not fail its test means the test is not pinning the
      line. Fix the test before continuing.
- [ ] **Step 5:** `go test primeradiant.com/serf/agent/...`; update the stale
      comment at `:1035-1040` and the `ActiveTurnID` doc at
      `session_client_mutation.go:128-135`; commit.

---

### Task 3: The projector closes, opens, and publishes active

**Files:** `internal/appprojector/appwire_projection.go`; create
`internal/appprojector/turn_boundary_test.go`.

- [ ] **Step 1: failing tests**

1. `TestTurnBoundaryOpensTheNamedTurn` — `EventTurnStarted{TurnID:"turn_m9"}`
   emits `turn/started` for `turn_m9`; `ActiveTurnID() == "turn_m9"`.
2. `TestTurnBoundaryPublishesActive` — the same projection contains a
   `thread/statusChanged` with `active`. Without it the composer never renders
   Stop or Steer, whatever the turn is called.
3. `TestTurnBoundaryCompletesThePreviousTurn` — with a turn open, `turn/completed`
   for the old id precedes `turn/started` for the new.
4. `TestNotificationTurnDoesNotJoinThePreviousTurn` — the interleave case:
   `EventUserInput{StableTurnID:"turn_m1"}` → assistant text →
   `EventTurnStarted{TurnID:"turn_m2"}` → `EventSteeringInjected` → assistant
   text. Assert the second assistant item's `TurnID` is `turn_m2`, and that
   `turn_m1` completed. **Assert on the assistant item, not the steering
   notification** — `NotifySerfSteeringInjected`'s params
   (`appwire_projection.go:753-773`) carry no turn id.

- [ ] **Step 2: watch them fail.**
- [ ] **Step 3: implement.**
      - Factor `closeActiveTurn(status appwire.TurnStatus) []AppNotification`
        from `:197-224`, `:262-281` and `:1059-1081`. Behaviour must be
        identical; the only variable is the status. Do not touch `:701-724`.
      - Add the `EventTurnStarted` case: `closeActiveTurn(Completed)`, adopt
        `data.TurnID` into `p.reservedTurnID`, `startTurn()`, then emit
        `turn/started` and `p.threadStatus(appwire.ThreadStatusActive)`.
      - `startTurn` already handles `anyTurnStarted` and
        `midSessionAnnouncementTurnID` (`:1668-1672`); `clearSkillCandidate` is
        inert here because the candidate's `turnID` no longer matches.
- [ ] **Step 4: watch them pass. Mutations:** drop the `reservedTurnID`
      assignment → test 1 fails; drop `threadStatus` → test 2 fails; drop
      `closeActiveTurn` → tests 3 and 4 fail.
- [ ] **Step 5:** `go test ./internal/appprojector/ ./server/ ./cmd/serf/`.
      The `closeActiveTurn` factoring must change no existing test; if one
      moves, the factoring changed behaviour — stop and say so.
- [ ] **Step 6: facet row.** `server/thread_envelope.go:149-162` calls turn
      boundaries one of "THE THREE CHECKPOINTS" and gives them `facetAll`
      (`EventTurnEnded: facetAll`, justified by "several values move DURING a
      turn with no event of their own"). Give `EventTurnStarted` the same, and
      add its case to `server/thread_envelope_test.go:53-63`, which asserts
      freshness one producer at a time.
- [ ] **Step 7: commit.**

---

### Task 4: No sixth kind repeats this

**Files:** `agent/session_lifecycle.go` (`EntryKind` block, `:388-408`);
create `agent/entrykind_audit_test.go`.

- [ ] **Step 1: failing test.** Classify all five kinds. `EntryWatchDelivery`
      has **no production producer** — whole-tree grep finds it only in reads
      (`session_tools.go:152`, `session_tools_communicate.go:144`,
      `subagents.go:1687,1734`, `session_lifecycle.go:460,1108`) and tests —
      so classify it honestly as that, not as "unserved".

      The test's doc comment must state what the audit does and does not
      catch: it catches a kind added above the sentinel; it cannot verify a
      classification is *true*, so a wrong label passes. Say so, rather than
      letting a future reader trust it further than it goes.

```go
var entryKindTurnOpening = map[EntryKind]turnOpening{
	EntryUserInput:         opensOnItsContentEvent,
	EntryContinuation:      opensOnItsContentEvent,
	EntryNotification:      opensOnTurnStarted,
	EntryWatchDelivery:     noProductionProducer,
	EntryDelegateAttention: unservedSoUnaddressable,
}

func TestEveryEntryKindDeclaresHowItsTurnOpens(t *testing.T) {
	for kind := EntryUserInput; kind < entryKindCount; kind++ {
		if _, ok := entryKindTurnOpening[kind]; !ok {
			t.Fatalf("EntryKind %d declares no turn opening: add it to entryKindTurnOpening, "+
				"or its turns open under an id no client can address (kata 7vmd)", kind)
		}
	}
	if len(entryKindTurnOpening) != int(entryKindCount) {
		t.Fatalf("entryKindTurnOpening has %d entries for %d kinds", len(entryKindTurnOpening), entryKindCount)
	}
}
```

- [ ] **Step 2: watch it fail** (`entryKindCount` undefined).
- [ ] **Step 3: add the sentinel** last in the iota block, with a comment
      requiring it stay last.
- [ ] **Step 4: watch it pass. Mutation:** delete one map entry → the test must
      name that kind.
- [ ] **Step 5: commit.**

---

### Task 5: Live-stack regression

**Files:** `cmd/serf-hub/e2e_turn_control_test.go`.

- [ ] **Step 1: failing test**, `TestE2E_TurnControlReachesANotificationTurn`.

      **Force the idle path deterministically.** A short background job
      finishing during the same `ProcessInputKind` is taken by the drain
      rung (`session_lifecycle.go:878-883`) as an interleave, not an idle wake,
      and the plan must not leave which defect it covers to timing. Script
      fakellm to start a background `shell` job that blocks on a file
      (`agent/session_tools_shell.go:318-324` for the `mode: "background"`
      argument shape), end the turn with `communicate(end_turn=true)`, wait for
      `awaitThread(... ActiveTurnID == "")`, and only then create the file the
      job is waiting on. Its completion then reaches
      `enqueueJobNotificationAndNotify` (`agent/session.go:621-632`) →
      `SubmitNotification` (`server/server.go:755`) with the session genuinely
      idle.

      **Wait past the `c2ty` window.** `setProcessingLocked` publishes a
      `turn_<n>` before the session goroutine runs, and `awaitActiveTurn`
      (`:294-298`) would latch it. Block on `provider.Next` first — as the
      goal-continuation test does (`:246-252`) — and additionally require the
      id to carry the `turn_m` prefix before steering.

      Then: `turn/steer` with that id (assert applied), release the round,
      assert the steer text reaches the next model request, `turn/interrupt`
      (assert applied), `awaitThread` until the active turn changes.

- [ ] **Step 2: prove it red.** `git worktree add <path> <sha-before-task-1>`
      (never `git stash`), copy the test file in, run it there. Expected:
      `turn/steer against ... : turn is not active`. Remove the worktree.
- [ ] **Step 3: green here.** Run the whole `TestE2E_TurnControl` family.
- [ ] **Step 4: commit.**

---

### Task 6: Gates, live verification, kata

- [ ] **Step 1: gates** (the full list in Global Constraints).
- [ ] **Step 2: live browser pass.** `./scripts/e2e-webui-turn-controls.sh`,
      spawn a session, provoke a notification turn, and confirm in the UI that
      **Stop and Steer actually render** — defect 2 means a fix that names the
      turn but leaves the thread idle would pass every Go test and still fail
      here. Drive both, then confirm the run's mutation journal
      (`$run/home/.local/state/serf/projects/*/mutations/<SID>.json`) records
      them `terminal`, not `rejected`. Stop the stack with `--stop`.
- [ ] **Step 3: close kata `7vmd`** with typed evidence: the e2e test name and
      the journal result.
- [ ] **Step 4: commit.**

## Self-Review

**Spec coverage.** Defect 1 → Tasks 2, 3, 5. Defect 2 → Task 3 test 2 and Task
6 Step 2. Defect 3 → Task 3 test 4. Defect 4 → Task 4.

**Placeholders.** None. Task 3 Step 6's facet choice is now decided
(`facetAll`, matching the file's own turn-boundary precedent) rather than left
open.

**Type consistency.** `EventTurnStarted`, `TurnStartedData`, `TurnID`,
`closeActiveTurn`, `entryKindCount`, `entryKindTurnOpening`, `turnOpening`,
`opensOnItsContentEvent`, `opensOnTurnStarted`, `noProductionProducer`,
`unservedSoUnaddressable`, `mintRunningTurnID`, `releaseRunningTurnID` are
spelled identically throughout.

**Risk.** One new event kind that no persisted format, golden file, decoder
registry or external reader consumes; one new projector case; one factoring of
three provably identical blocks; one reordered emit inside a function whose
refusal points are enumerated above. The turn-opening paths for user input and
goal continuations are untouched, which is what keeps this out of kata `eptj`'s
territory.
