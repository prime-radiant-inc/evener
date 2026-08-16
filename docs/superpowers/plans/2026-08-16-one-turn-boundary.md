# Notification Turns Get Their Own Turn Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A turn opened by a notification wake becomes a real, named turn —
addressable by Steer/Send/Stop, and separate from the turn before it.

**Architecture:** A notification turn is the only kind that opens with no
event of its own, so it gets one: `EventTurnStarted`, emitted from
`acceptNotificationInput` after it commits to running, carrying the id the
daemon reserved. The AppWire projector closes whatever turn is open and opens
the named one — the same shape `EventGoalContinuation` already uses. An audit
test forces every present and future `EntryKind` to declare how its turn
opens, so this cannot silently recur.

**Tech Stack:** Go 1.25 multi-module workspace (`agent` module, root module's
`internal/appprojector` / `server` / `cmd/serf-hub`), `appwire` JSON-RPC.

**Spec:** this document.

**Prior art:** `docs/superpowers/plans/2026-08-16-one-active-turn-identity.md`
landed the same fix for goal-continuation turns and is where the identity
model below is established. Kata `7vmd` is the notification turn. Kata `2f41`
is why nobody noticed. Kata `c2ty` records the ruling this plan does not touch.

## Global Constraints

- **No backward compatibility.** Jesse's call. Delete the superseded path.
- **One minter of live turn ids.** `reserveClientMutationTurnID`
  (`agent/session_client_mutation_queue.go:642`) and nothing else.
  `agent/session_client_mutation_turn_namespace_test.go:52-57` enforces it,
  and kata `eptj` is what happened when two minters shared a namespace:
  a collision made `turn/completed` overwrite a persisted turn's content.
- **Nothing names a turn out of band.** The projector consumes events on its
  own goroutine (`agent/session_events.go:95`, `cmd/serf/serve.go:192`), so a
  side-channel announcement races the stream. The name must ride an event.
- **Every production line this plan adds must be killed by a test.** Each
  task names the mutation its test must catch; the implementer applies that
  mutation, watches the test fail, and reverts it.
- Gates: `make lint`, `make build`, `go test ./...`, the seven module suites
  (`go test primeradiant.com/serf/<mod>/...` for agent, auth, envvars, fuzz,
  identifier, invariant, llm), `make test-web`.
- Never `git stash`; never `git checkout <file>` to undo.

---

## The identity model (established, not proposed)

A turn's identity is minted once, by the Session, at the moment the turn
begins, and it reaches the AppWire projection on the event that opens the
turn. Two facts share the `clientMutationSnapshot.ActiveTurnID` field under
one rule:

> ActiveTurnID names the running turn, and it persists exactly as long as the
> thing that would resume that turn persists.

A client-started turn's pending execution is reclaimed and re-run on restart,
so its id survives (`agent/session_client_mutation.go:235`,
`agent/session_queue.go:581`). An agent-started turn has nothing to resume it,
so `loadClientMutationSnapshotFS` drops an id no pending execution owns
(`agent/session_client_mutation_persist.go:74-84`). Mid-turn mutations compare
`expectedTurnId` against this field
(`session_client_mutation_queue.go:123,325,392,497`,
`session_client_mutation.go:411`).

The daemon publishes `serf.activeTurnId` from the projector
(`server/appwire_runtime.go:212,1198`), so the projector's turn id **is** the
one clients send back. When the projector names a turn itself — `startTurn`'s
`p.nextTurn++` / `turn_<n>` fallback (`internal/appprojector/appwire_projection.go:1665`)
— that id is in a different namespace from the durable `turn_m<n>`, and every
mid-turn mutation aimed at it is rejected.

---

## Diagnosis

`processOneInput` (`agent/session_lifecycle.go:1046-1057`) dispatches five
entry kinds (`:388-408`). How each one's turn acquires a name:

| Entry kind | Opens the projector turn via | Named? | Addressable by a client? |
| --- | --- | --- | --- |
| `EntryUserInput` | `EventUserInput` (`appwire_projection.go:229`) | yes — `UserInputData.StableTurnID` | yes |
| `EntryContinuation` | `EventGoalContinuation` (`:289`) | yes — `GoalContinuationData.StableTurnID` | yes |
| `EntryWatchDelivery` | falls through to `acceptUserInput`, so `EventUserInput` | inherits that path | runs on a child session |
| `EntryNotification` | **nothing** | **no** | **yes — and broken** |
| `EntryDelegateAttention` | **nothing** (`acceptDelegateAttentionInput`, `:1510`, emits no event) | **no** | runs on a child session |

`acceptNotificationInput` (`:1527`) emits `EventSteeringInjected` for its
reminder (`:1575`), and the projector's case for that event
(`appwire_projection.go:733-773`) never calls `ensureTurn` — it only produces
a `serf/steering/injected` notification. So nothing in a notification turn
opens a turn. Worse, the reminder is emitted only when `len(jobNotifs) > 0`,
while the turn proceeds on job notifications **or** pending steering **or**
root delegate attention (`:1547`) — so on two of three paths the turn produces
no distinguishing event at all.

That yields two live defects, on two different paths:

**1. Idle wake → an unaddressable turn.** `SubmitNotification`
(`server/server.go:757`) wakes an idle session. The previous turn was already
closed, so the notification turn's first content event reaches `ensureTurn`
with no reservation and opens `turn_<n>`. The daemon publishes it; every
`turn/steer`, `turn/queue`, `turn/drainAsSteer`, `turn/promoteQueuedAsSteer`
and `turn/interrupt` aimed at it is rejected with
`Conflict("turn is not active")`. The composer surfaces nothing (kata `2f41`),
so it reads as a dead button.

**2. Interleave → a turn that swallows the next one.** The drain loop's
notification rung (`agent/session_lifecycle.go:878-883`) `continue`s into an
`EntryNotification` turn without leaving `processInputKindWithProvenance`.
`finishProcessingAtBoundary` (`agent/session_state.go:208-225`) emits
`EventTurnEnded`, which the projector only *stashes* for timing
(`appwire_projection.go:1037-1045`) — it does not close the turn. So the
previous turn stays open, the notification turn's items are appended to it,
and it is finally closed by `EventSessionEnd` (`:1059-1081`) at the end of the
whole drain, carrying merged content and a duration that belongs to a
different turn.

**3. Nothing prevents recurrence.** Five kinds exist; two were never
considered when the naming mechanism was designed, and Go enforces no
exhaustiveness over `EntryKind`. A sixth kind would inherit whichever
behaviour its author's chosen accept function happened to have.

### Evidence

Live session `0348HuXSlWRtoLEoQ4EOE8` on a real hub: durable `ActiveTurnID`
`turn_m6`, wire `turn_6`, and 3× `turn/steer` + 1× `turn/queue` +
1× `turn/interrupt` recorded `rejected — "turn is not active"` in
`<state-dir>/mutations/<SID>.json`. The goal-continuation analogue reproduced
deterministically and is fixed; `scripts/e2e-webui-turn-controls.sh` is the
harness.

---

## Design decisions, and what is deliberately not done

**A notification turn gets an opening event.** It is the only kind that both
takes client mutations and has no event of its own. `EventTurnStarted` closes
the open turn and opens a named one, which fixes defects 1 and 2 together.

**`EventUserInput` and `EventGoalContinuation` are not touched.** They already
name their turns. They are also the only two producers of
`thread/statusChanged: active` (`appwire_projection.go:252,310`; the only
other `threadStatus` call sites are `:193` and `:1083`), so moving their
boundary would silently stop publishing that. And opening a turn earlier in
`acceptUserInput` would relocate `SessionStart` hook announcements
(`session_lifecycle.go:1364-1372`) out of the prelude bucket
`preTurnAnnouncementTurnID` groups them into (`appwire_projection.go:1687-1720`,
katas `9ekv`/`bz2z`).

**The four turn-close blocks are not unified.** `:199`, `:264` and `:1065`
each reset eight fields and build a `TurnStatusCompleted` turn, but `:701`
resets five, has no `activeTurnID != ""` guard, and carries an
`appwire.TurnError` with `TurnStatusFailed`. One helper cannot express that
without an error parameter and a silent change to which fields the failure
path clears.

**Delegate-attention and watch-delivery turns are not named.** Both run on a
child session (`agent/subagents.go:1424-1428`, on `a.sess`), which is unserved
by construction — `agent/session_lossless_events_test.go:238-261` pins that a
prepared subagent gets no authoritative consumer, and descendant mutations
"remain owned by the agent tree" (`server/appwire_runtime.go:243-245`). No
client addresses those turns, so there is nothing to fix. Task 4 records this
as an explicit exemption rather than an omission.

**`setProcessingLocked`'s `ReserveTurnID` branch stays** (`server/server.go:713`).
Kata `c2ty`'s 2026-07-26 ruling keeps it deliberately, weighing a brief window
of wrong-but-recoverable controls against the `eptj` data-loss class.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `agent/events/events.go` (modify) | `EventTurnStarted` kind, beside the existing `EventTurnEnded`. |
| `agent/events/payloads.go` (modify) | `TurnStartedData{TurnID}`. |
| `agent/events/eventdata.go` (modify) | The `eventKind()` binding **and** the compile-time assertion block at `:90-129`. |
| `agent/events/payloads_test.go` (modify) | In-package binding test (`events_test.go` is `package events_test` and cannot see `eventKind()`). |
| `agent/session_lifecycle.go` (modify) | `acceptNotificationInput` reordered to decide → persist → open → announce; `entryKindCount` sentinel. |
| `agent/session_turn_boundary_test.go` (create) | Wiring: the boundary fires once for a proceeding wake, never for a refused one, and carries a `turn_m<n>`. |
| `agent/entrykind_audit_test.go` (create) | Every `EntryKind` must declare how its turn opens. |
| `internal/appprojector/appwire_projection.go` (modify) | One `EventTurnStarted` case. |
| `internal/appprojector/turn_boundary_test.go` (create) | Opens named, opens unnamed, closes the previous turn, and the interleave case. |
| `server/thread_envelope.go` (modify) | Facet row for the new kind. |
| `cmd/serf-hub/e2e_turn_control_test.go` (modify) | Live-stack regression for a notification turn. |

---

### Task 1: The boundary event

**Files:**
- Modify: `agent/events/events.go` (beside `EventTurnEnded`, `:109-111`)
- Modify: `agent/events/payloads.go` (beside `TurnEndedData`, `:686-689`)
- Modify: `agent/events/eventdata.go` (binding at `:79`; assertion block at `:90-129`)
- Test: `agent/events/payloads_test.go` (in-package)

**Interfaces:**
- Produces: `events.EventTurnStarted` and `events.TurnStartedData{TurnID string}`.
  `TurnID` empty means the session has no client to name a turn to; the
  projection then mints as it does today.

`Kind` is deliberately **not** a field. `agent/events` cannot import `agent`
(import cycle), `EntryKind` has no `String()` method anywhere in the tree, and
no consumer in this plan needs it. Do not add one speculatively.

- [ ] **Step 1: Write the failing test** in `agent/events/payloads_test.go`:

```go
func TestTurnStartedDataBindsItsKind(t *testing.T) {
	if got := TurnStartedData{}.eventKind(); got != EventTurnStarted {
		t.Fatalf("TurnStartedData.eventKind() = %q, want %q", got, EventTurnStarted)
	}
}
```

- [ ] **Step 2: Run it, watch it fail.** `cd agent && go test ./events/ -run TestTurnStartedDataBindsItsKind -v`. Expected: undefined identifiers.
- [ ] **Step 3: Implement.**

```go
	// EventTurnStarted marks a turn beginning, carrying the identity the daemon
	// reserved for it. It exists for turns that open with no content event of
	// their own -- a notification wake's turn is announced by nothing else, so
	// without this its items join the previous turn and its id is one no
	// mutation precondition accepts. Turns whose first event already names them
	// (EventUserInput, EventGoalContinuation) do not emit it.
	EventTurnStarted EventKind = "TURN_STARTED"
```

```go
// TurnStartedData is the payload for an EventTurnStarted event.
type TurnStartedData struct {
	// TurnID is the identity the daemon's mutation preconditions accept for
	// this turn. Empty when no daemon serves this session, in which case the
	// AppWire projection mints its own id as it does for any unnamed turn.
	TurnID string `json:"turn_id,omitempty"`
}
```

Add `func (TurnStartedData) eventKind() EventKind { return EventTurnStarted }`
and the matching entry in the compile-time assertion block at `:90-129`.

- [ ] **Step 4: Run it, watch it pass.** Then `cd agent && go test ./events/...`.
- [ ] **Step 5: Commit.**

---

### Task 2: The notification turn announces itself

**Files:**
- Modify: `agent/session_lifecycle.go` (`acceptNotificationInput`, `:1527-1594`)
- Create: `agent/session_turn_boundary_test.go`

**Interfaces:**
- Consumes: `events.EventTurnStarted`, `Session.mintRunningTurnID`
  (`agent/session_active_turn.go`), `Session.releaseRunningTurnID`.
- Produces: no new exported surface.

**The ordering constraint, and why the function must be reordered.**
`acceptNotificationInput` currently interleaves three things: it decides
whether to proceed (`:1547`), it durably persists the reminder and can still
refuse afterwards (`:1568-1574`), and it emits the reminder as content
(`:1575`). The boundary must land after the last refusal and before the first
content event. So the function becomes linear:

1. decide (existing early return at `:1547`),
2. persist the reminder if there is one — the existing
   `appendSteeringTurnDurably` and its refusal,
3. **mint and announce the boundary**,
4. emit the reminder content,
5. the rest, unchanged.

Emitting the boundary at step 4 or later reinstates the bug. Emitting it at
step 1 names a turn that a persistence failure then refuses to run.

- [ ] **Step 1: Write the failing tests** in `agent/session_turn_boundary_test.go`.
      Collect events with `ConsumeEventsLossless` — it is the only writer of
      `authoritativeConsumer` (`agent/session.go:109-123`), so registering a
      real drain is also what makes the session "served" for
      `mintRunningTurnID`.

1. `TestNotificationTurnAnnouncesOneNamedBoundary` — seed a pending steering
   message so the wake proceeds with **no** job notifications (the path that
   emits no reminder at all), run
   `ProcessInputKind(ctx, "", nil, EntryNotification)`, and assert exactly one
   `EventTurnStarted` whose `TurnID` has prefix `turn_m`.
2. `TestNotificationBoundaryPrecedesItsReminder` — seed a job notification,
   run the turn, and assert the `EventTurnStarted` index is lower than the
   `EventSteeringInjected` index in the collected slice. This is the ordering
   the whole task exists for.
3. `TestRefusedNotificationWakeAnnouncesNoBoundary` — nothing pending: assert
   zero `EventTurnStarted` and `s.clientMutations.snapshot().ActiveTurnID == ""`.
   A turn that never runs must not burn a sequence number or hold the slot.

- [ ] **Step 2: Run them, watch them fail.**
- [ ] **Step 3: Implement the reorder** described above.
- [ ] **Step 4: Run them, watch them pass. Then run these mutations:**
      - Replace the mint with `""` → test 1 must fail on the `turn_m` prefix.
      - Move the announce below the reminder emit → test 2 must fail.
      - Move the announce above the `:1547` early return → test 3 must fail.
      Any mutation that does not fail its test means the test is not pinning
      the line; fix the test before continuing.
- [ ] **Step 5:** `go test primeradiant.com/serf/agent/...`, then commit.

---

### Task 3: The projector opens the named turn

**Files:**
- Modify: `internal/appprojector/appwire_projection.go`
- Create: `internal/appprojector/turn_boundary_test.go`

**Interfaces:**
- Consumes: `events.EventTurnStarted` from Task 1.

- [ ] **Step 1: Write the failing tests** in `turn_boundary_test.go`:

1. `TestTurnBoundaryOpensTheNamedTurn` — `EventTurnStarted{TurnID:"turn_m9"}`
   emits `turn/started` for `turn_m9`; `ActiveTurnID() == "turn_m9"`.
2. `TestTurnBoundaryWithoutAnIDMints` — empty `TurnID` still opens a turn
   (`turn_1`), so an unserved session keeps working.
3. `TestTurnBoundaryCompletesThePreviousTurn` — with a turn open, a boundary
   emits `turn/completed` for the old id **before** `turn/started` for the new.
4. `TestNotificationTurnDoesNotJoinThePreviousTurn` — the interleave case:
   `EventUserInput{StableTurnID:"turn_m1"}` → assistant text →
   `EventTurnStarted{TurnID:"turn_m2"}` → `EventSteeringInjected` → assistant
   text. Assert the second assistant item's `TurnID` is `turn_m2` and that
   `turn_m1` was completed. **Assert on the assistant item, not the steering
   notification** — `NotifySerfSteeringInjected`'s params are
   `{threadId, ref, text, images, source?, kind?, clientMutationId?}`
   (`appwire_projection.go:753-773`) and carry no turn id at all.

- [ ] **Step 2: Run them, watch them fail.**
- [ ] **Step 3: Implement** one case, modelled on `EventGoalContinuation`
      (`:255-312`) minus its item and its `threadStatus`:

      close the open turn exactly as that case does (the eight-field reset and
      the `turn/completed` map literal), then adopt `data.TurnID` into
      `p.reservedTurnID` when non-empty, then `startTurn()`, then emit
      `turn/started`. Do not emit `p.threadStatus(...)`: the session is
      already active when a notification turn opens, and the two existing
      producers keep that job.

- [ ] **Step 4: Run them, watch them pass. Mutations:** delete the
      `reservedTurnID` assignment → test 1 fails; delete the close block →
      tests 3 and 4 fail.
- [ ] **Step 5:** `go test ./internal/appprojector/ ./server/ ./cmd/serf/`.
      No existing test should need changing; if one does, that is a signal the
      implementation moved behaviour it should not have — stop and say so.
- [ ] **Step 6: Facet row.** Add `events.EventTurnStarted` to
      `server/thread_envelope.go`'s map. A notification turn starting drains
      the job-notification queue and can resolve a pending ask, so it needs at
      least what `EventGoalContinuation` declares at `:208`
      (`facetQueue | facetAsk`); it carries no goal transition, so `facetGoal`
      is not obviously right — state the choice and the reason in a comment.
- [ ] **Step 7: Commit.**

---

### Task 4: No sixth kind repeats this

**Files:**
- Modify: `agent/session_lifecycle.go` (`EntryKind` block, `:388-408`)
- Create: `agent/entrykind_audit_test.go`

**Interfaces:**
- Produces: unexported `entryKindCount` sentinel, declared last in the iota
  block.

- [ ] **Step 1: Write the failing test.**

```go
// turnOpening records how a turn of a given EntryKind acquires the identity
// clients address it by. Every EntryKind must appear here: a kind whose turn
// opens unnamed publishes an id in the projector's own turn_<n> namespace,
// which no mutation precondition accepts, so Steer, Send and Stop all fail on
// it silently. That is kata 7vmd, and it reached production because two of
// the five kinds were never considered.
type turnOpening string

const (
	// opensOnItsContentEvent: the turn's first event already carries a
	// StableTurnID (EventUserInput, EventGoalContinuation).
	opensOnItsContentEvent turnOpening = "content-event"
	// opensOnTurnStarted: the turn has no content event of its own and emits
	// EventTurnStarted.
	opensOnTurnStarted turnOpening = "turn-started"
	// unservedSoUnaddressable: the turn only ever runs on a child session,
	// which has no authoritative consumer and takes no client mutations, so no
	// client can name it. See agent/session_lossless_events_test.go.
	unservedSoUnaddressable turnOpening = "unserved"
)

var entryKindTurnOpening = map[EntryKind]turnOpening{
	EntryUserInput:         opensOnItsContentEvent,
	EntryContinuation:      opensOnItsContentEvent,
	EntryWatchDelivery:     unservedSoUnaddressable,
	EntryNotification:      opensOnTurnStarted,
	EntryDelegateAttention: unservedSoUnaddressable,
}

func TestEveryEntryKindDeclaresHowItsTurnOpens(t *testing.T) {
	for kind := EntryUserInput; kind < entryKindCount; kind++ {
		if _, ok := entryKindTurnOpening[kind]; !ok {
			t.Fatalf("EntryKind %d declares no turn opening: add it to entryKindTurnOpening, "+
				"or its turns will open under an id no client can address (kata 7vmd)", kind)
		}
	}
	if len(entryKindTurnOpening) != int(entryKindCount) {
		t.Fatalf("entryKindTurnOpening has %d entries for %d kinds", len(entryKindTurnOpening), entryKindCount)
	}
}
```

- [ ] **Step 2: Run it, watch it fail** — `entryKindCount` undefined.
- [ ] **Step 3: Add the sentinel** as the last member of the iota block:

```go
	// entryKindCount is the number of EntryKind values. Keep it last: the
	// turn-opening audit iterates up to it, so a kind added above this line
	// must declare how its turn opens before the suite goes green.
	entryKindCount
)
```

- [ ] **Step 4: Run it, watch it pass. Mutation:** delete one map entry →
      the test must name that kind.
- [ ] **Step 5: Commit.**

---

### Task 5: Live-stack regression

**Files:**
- Modify: `cmd/serf-hub/e2e_turn_control_test.go`

**Interfaces:**
- Consumes: `fakellm.Server`, `startHubStack`, `awaitActiveTurn`,
  `awaitThread`, `clientRequest`, `newMutationID`, `communicateArgs` — all
  already in that file.

- [ ] **Step 1: Write the failing test**,
      `TestE2E_TurnControlReachesANotificationTurn`, mirroring the
      goal-continuation test in the same file.

      Provoke the wake by scripting fakellm to call `shell` with
      `{"command": "...", "mode": "background"}`
      (`agent/session_tools_shell.go:318-324`; driven with
      `fakellm.Call.RespondToolCall`), then end the turn with
      `communicate(end_turn=true)` so the session goes idle. The job's
      completion fires `notifyCallback` → `srv.SubmitNotification()`
      (`cmd/serf/serve.go:684`, `server/server.go:757`), which is the
      **idle-wake** path — defect 1.

      Then: `awaitActiveTurn` excluding the earlier turn, `turn/steer` with
      that id (assert applied), release the round, assert the steer text
      reaches the next model request, `turn/interrupt` (assert applied),
      `awaitThread` until the active turn changes.

      Record in the test comment which of the two defect paths it covers.
      The interleave path (defect 2) is covered by Task 3's test 4 at the
      projector level; if it can also be provoked here deterministically, add
      it, but do not make the e2e depend on drain-loop timing.

- [ ] **Step 2: Prove it red.** Add a scratch worktree at the commit before
      Task 1 (`git worktree add <path> <sha>` — never `git stash`), copy the
      test file in, run it there. Expected: `turn/steer against ... "turn_<n>":
      turn is not active`. Remove the worktree afterwards.
- [ ] **Step 3: Green here.** Run the whole `TestE2E_TurnControl` family.
- [ ] **Step 4: Commit.**

---

### Task 6: Gates, live verification, and closing the kata

- [ ] **Step 1: Gates.** `make lint`, `make build`, `go test ./...`, the seven
      module suites, `make test-web`.
- [ ] **Step 2: Live browser pass.** `./scripts/e2e-webui-turn-controls.sh`,
      spawn a session, provoke a notification turn, drive Steer and Stop from
      the UI, and confirm the run's mutation journal
      (`$run/home/.local/state/serf/projects/*/mutations/<SID>.json`) records
      both `terminal`, not `rejected`. Stop the stack with `--stop`.
- [ ] **Step 3: Close kata `7vmd`** with typed evidence: the e2e test name and
      the journal result.
- [ ] **Step 4: Commit.**

## Self-Review

**Spec coverage.** Defect 1 → Tasks 2, 3 and 5. Defect 2 → Task 3's test 4.
Defect 3 → Task 4. The two kinds this plan does not name are covered by Task
4's explicit `unservedSoUnaddressable` classification, with the test that pins
it named in the design section.

**Placeholders.** Task 3 Step 6 leaves the exact facet set to the implementer
because the right answer depends on what a notification turn actually changes;
it names the floor (`facetQueue | facetAsk`), the candidate it is unsure about
(`facetGoal`), and requires the reasoning be written down. Every other step
carries the code, the command and the expected output.

**Type consistency.** `EventTurnStarted`, `TurnStartedData`, `TurnID`,
`entryKindCount`, `entryKindTurnOpening`, `turnOpening`,
`opensOnItsContentEvent`, `opensOnTurnStarted`, `unservedSoUnaddressable`,
`mintRunningTurnID`, `releaseRunningTurnID` are spelled identically throughout.

**Risk.** The blast radius is one new projector case, one reordered function,
and one new event kind that no persisted format or external reader consumes
(agent event kinds appear in no session log, golden file, or `serf-doctor`
reader). The turn-opening paths for user input and goal continuations are
untouched, which is what keeps this out of kata `eptj`'s territory.
