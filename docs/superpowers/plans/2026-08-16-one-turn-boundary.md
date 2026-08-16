# One Turn Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every turn begin at one explicit, named boundary, so a turn's
identity cannot depend on which kind of turn it happens to be — and a turn
kind added later cannot silently reintroduce an unaddressable turn.

**Architecture:** A new `EventTurnStarted` is emitted once from
`Session.processOneInput`, the single place a turn begins, for every entry
kind, carrying the id the daemon reserved for it. The AppWire projector gains
one case that closes the previous turn and opens the named one;
`EventUserInput` and `EventGoalContinuation` stop being turn boundaries and
become content. `ensureTurn` remains the fallback for event streams that
arrive with no boundary (transcript replay, sessions no daemon serves).

**Tech Stack:** Go 1.25 multi-module workspace (`agent`, `agent/events`,
root module's `internal/appprojector` / `server`), `appwire` JSON-RPC.

**Spec:** this document.

**Predecessor:** `2026-08-16-one-active-turn-identity.md` landed the
goal-continuation half of this and is the source of the Diagnosis below.
Kata `7vmd` is the notification half this plan closes.

## Global Constraints

- **No backward compatibility.** Jesse's call. Delete the superseded path.
- **One mint site.** Turn ids come from `reserveClientMutationTurnID`
  (`agent/session_client_mutation_queue.go:642`) and nowhere else.
  `agent/session_client_mutation_turn_namespace_test.go:52-57` enforces it,
  and kata `eptj` is the data loss that happens when two minters share a
  namespace.
- **Every production line this plan adds must be killed by a test.** Both
  reviews of the predecessor found its projector edit and its wiring had no
  unit coverage. Each task states the mutation its test must catch, and the
  implementer must run that mutation and watch the test fail.
- Gates: `make lint`, `make build`, `go test ./...`, the seven module suites,
  `make test-web`.
- Never `git stash`; never `git checkout <file>` to undo.

---

## Diagnosis

A turn's AppWire identity is set by whichever event happens to open it, and
the set of events that open a turn is not the set of ways a turn starts.

| Entry kind | Opens the projector turn via | Named? |
| --- | --- | --- |
| `EntryUserInput` | `EventUserInput` (`appwire_projection.go:229`) | yes — `UserInputData.StableTurnID` |
| `EntryContinuation` | `EventGoalContinuation` (`:289`) | yes, since the predecessor plan |
| `EntryNotification` | **nothing** — its reminder is `EventSteeringInjected`, which never calls `ensureTurn`, and the reminder is emitted only when `len(jobNotifs) > 0` (`session_lifecycle.go:1562`) while the turn proceeds on job notifications **or** pending steering **or** root delegate attention (`:1541`) | **no** |
| `EntryDelegateAttention` | **nothing** — `acceptDelegateAttentionInput` (`:1509`) emits no event at all | **no** |

Two consequences, both live today:

1. **Unaddressable turns.** An unnamed turn opens under `startTurn`'s own
   counter as `turn_<n>` (`:1662`). The daemon publishes that
   (`server/appwire_runtime.go:212,1198`) while its mutation preconditions
   compare against `clientMutationSnapshot.ActiveTurnID` in the `turn_m<n>`
   family, so `turn/steer`, `turn/queue`, `turn/drainAsSteer`,
   `turn/promoteQueuedAsSteer` and `turn/interrupt` are all rejected with
   `Conflict("turn is not active")` — silently (kata `2f41`).
2. **Turns that never close.** The drain loop's notification rung
   (`session_lifecycle.go:878-883`) `continue`s into an `EntryNotification`
   turn with no `EventSessionEnd` between, and nothing in that turn closes
   the previous one. Its items are attributed to the previous turn, and the
   previous turn never gets `turn/completed`, its timing, or its usage stamp.

Patching a third event carrier repeats the mistake for the fourth turn kind.
The boundary is what is missing, not the carrier.

### Supporting evidence that the boundary belongs in the event stream

The projector consumes events on its own goroutine
(`agent/session_events.go:95`, `cmd/serf/serve.go:192`), so anything that
names a turn out of band races the stream. An earlier draft of the
predecessor used `Server.SetProcessingTurn` from `processOneInput`; two
independent reviews killed it, because `ReserveStableTurnID` blanks
`p.activeTurnID` (`appwire_projection.go:1749`) and would drop the previous
turn's `turn/completed`. The boundary must be an event.

### What this plan does not change

- `setProcessingLocked`'s `ReserveTurnID` branch (`server/server.go:713`)
  stays. Kata `c2ty`'s 2026-07-26 ruling keeps it deliberately. The boundary
  shrinks the window it covers to one event-queue hop, which is the argument
  for revisiting that ruling — Jesse's call, not this plan's.
- Transcript replay. `apptranscript.TurnsFromFile` mints its own `turn_<n>`
  per entry and `SeedPersistedTurns` (`:135`) raises the live counter above
  it. Untouched.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `agent/events/events.go` (modify) | `EventTurnStarted` kind, symmetric with the existing `EventTurnEnded`. |
| `agent/events/payloads.go` (modify) | `TurnStartedData{TurnID, Kind}`. |
| `agent/events/eventdata.go` (modify) | The `eventKind()` binding. |
| `agent/session_active_turn.go` (modify) | `beginRunningTurn(kind, claimed)` resolves this turn's id for every kind and announces the boundary. Replaces the continuation-only `mintRunningTurnID` call site. |
| `agent/session_lifecycle.go` (modify) | Emit the boundary once per turn, after the accept decision; drop the per-kind `StableTurnID` threading. |
| `internal/appprojector/appwire_projection.go` (modify) | One `EventTurnStarted` case that closes and opens; `closeActiveTurn` factored out of the four duplicated close blocks; `EventUserInput` / `EventGoalContinuation` reduced to content. |
| `internal/appprojector/turn_boundary_test.go` (create) | Per-kind boundary coverage, including the interleaved notification case. |
| `agent/session_turn_boundary_test.go` (create) | Wiring coverage: every kind emits exactly one boundary, refused wakes emit none. |
| `server/thread_envelope.go` (modify) | Facet mapping for the new kind. |
| `cmd/serf-hub/e2e_turn_control_test.go` (modify) | Live-stack regression for a notification-wake turn. |

---

### Task 1: The boundary event

**Files:**
- Modify: `agent/events/events.go` (beside `EventTurnEnded` at :109-111)
- Modify: `agent/events/payloads.go` (beside `TurnEndedData` at :686-689)
- Modify: `agent/events/eventdata.go` (beside :79)
- Test: `agent/events/events_test.go`

**Interfaces:**
- Produces: `events.EventTurnStarted` and
  `events.TurnStartedData{TurnID string, Kind string}`. `Kind` is the
  `agent.EntryKind` string, so the projection can label a turn without
  guessing from its contents. `TurnID` is empty for a session no daemon
  serves — the projector then mints as it does today.

- [ ] **Step 1: Write the failing test**

```go
// TestTurnStartedDataBindsItsKind pins the payload/kind binding the event
// registry relies on; a payload whose eventKind() disagrees is routed as the
// wrong event.
func TestTurnStartedDataBindsItsKind(t *testing.T) {
	if got := TurnStartedData{}.eventKind(); got != EventTurnStarted {
		t.Fatalf("TurnStartedData.eventKind() = %q, want %q", got, EventTurnStarted)
	}
	if EventTurnStarted == EventTurnEnded {
		t.Fatal("EventTurnStarted collides with EventTurnEnded")
	}
}
```

- [ ] **Step 2: Run it, watch it fail** — `cd agent && go test ./events/ -run TestTurnStartedDataBindsItsKind -v`. Expected: undefined.
- [ ] **Step 3: Add the kind, the payload and the binding.**

```go
	// EventTurnStarted marks a single turn beginning, carrying the identity the
	// daemon reserved for it and the kind of input that opened it. It is the
	// ONE boundary: every turn emits it, whatever started it, before any of the
	// turn's own content. The AppWire projection closes the previous turn and
	// opens this one from it, so a turn's identity never depends on which kind
	// of event happens to carry content first. Symmetric with EventTurnEnded.
	EventTurnStarted EventKind = "TURN_STARTED"
```

```go
// TurnStartedData is the payload for an EventTurnStarted event.
type TurnStartedData struct {
	// TurnID is the identity the daemon's mutation preconditions accept for
	// this turn. Empty means the session has no client to name a turn to, and
	// the projection mints its own.
	TurnID string `json:"turn_id,omitempty"`
	// Kind is the agent.EntryKind that opened the turn ("user_input",
	// "continuation", "notification", "delegate_attention").
	Kind string `json:"kind,omitempty"`
}
```

- [ ] **Step 4: Run the events suite** — `cd agent && go test ./events/...`. Check whether any exhaustive registry (`events_fuzz_test.go`, `eventdata_program_fuzz_test.go`) needs the new kind registered; if a test enumerates kinds, add it there rather than exempting it.
- [ ] **Step 5: Commit.**

---

### Task 2: One place resolves a turn's identity and announces it

**Files:**
- Modify: `agent/session_active_turn.go`
- Modify: `agent/session_lifecycle.go` (the mint/accept block, currently ~:1030-1055; `acceptContinuationInput` at :1489; `acceptNotificationInput` at :1527)
- Create: `agent/session_turn_boundary_test.go`

**Interfaces:**
- Produces: `func (s *Session) beginRunningTurn(kind EntryKind, claimed string) (turnID string, minted bool)`.
  `claimed` is the id this turn's own input already owns — `queuedIdentity.StableTurnID`
  for user input claimed off a client mutation, empty otherwise. When
  `claimed` is non-empty it is used as-is (that turn is already named and
  already durable). Otherwise the existing `mintRunningTurnID` rules apply:
  refuse under an interrupt fence, refuse when another mutation owns the
  slot, skip when no daemon drains this session.
- Produces: `func (s *Session) announceTurnStarted(turnID string, kind EntryKind)`.

**Why `claimed` rather than reading `snapshot.ActiveTurnID`:** an earlier
draft adopted whatever the slot held, and a review found that a `turn/start`
accepted between the serve loop dequeuing an agent wake and the mint would
have its identity taken by the agent turn — a later Stop would then cancel
the agent turn while marking the user's never-run message "interrupted".
The caller knows which id is genuinely this turn's; the store does not.

- [ ] **Step 1: Write the failing tests**

Four cases, in `agent/session_turn_boundary_test.go`. Each collects events
through `ConsumeEventsLossless` (the only writer of `authoritativeConsumer`,
so a real drain is also what makes the session "served"):

1. `TestUserInputTurnAnnouncesOneBoundary` — `ProcessInput` emits exactly one
   `EventTurnStarted`, with `Kind == "user_input"`.
2. `TestGoalContinuationTurnAnnouncesOneBoundary` — same for
   `EntryContinuation`, `TurnID` matching `turn_m<n>`.
3. `TestNotificationTurnAnnouncesOneBoundary` — same for `EntryNotification`.
   Seed a pending steering message so the wake proceeds with **no** job
   notifications: that is the path the predecessor plan got wrong, and it must
   be the one under test.
4. `TestRefusedNotificationWakeAnnouncesNoBoundary` — an `EntryNotification`
   turn with nothing pending emits no `EventTurnStarted` and leaves
   `snapshot.ActiveTurnID` empty. A turn that never runs must not burn a
   sequence number, publish an id, or hold the slot.

- [ ] **Step 2: Run them, watch them fail.**
- [ ] **Step 3: Implement.** In `processOneInput`, after the accept chain
      decides (so a refused wake announces nothing) and before any content
      event of the turn:

```go
	runningTurnID, mintedRunningTurn := s.beginRunningTurn(kind, claimedTurnID)
	if mintedRunningTurn {
		defer s.releaseRunningTurnID(runningTurnID)
	}
	s.announceTurnStarted(runningTurnID, kind)
```

      Note the ordering problem the implementer must solve: `acceptUserInput`
      both resolves `queuedIdentity` and emits `EventUserInput`. The boundary
      must precede `EventUserInput`, so `acceptUserInput` has to hand the
      claimed identity back before it emits, or resolve it in a step the
      caller runs first. Do NOT emit the boundary after the content event —
      that reinstates the ordering bug this plan exists to remove.

      Delete the `stableTurnID` parameters added to `acceptContinuationInput`
      and `acceptNotificationInput` by the predecessor, and the
      `StableTurnID` field on `GoalContinuationData`: the boundary carries it
      now, and two carriers is the defect.

- [ ] **Step 4: Run them, watch them pass. Then run each mutation:** replace
      the `beginRunningTurn` call with `("", false)` and confirm tests 1-3
      fail; move the announce above the accept chain and confirm test 4 fails.
- [ ] **Step 5: Full agent suite**, then commit.

---

### Task 3: One projector case opens and closes turns

**Files:**
- Modify: `internal/appprojector/appwire_projection.go`
- Create: `internal/appprojector/turn_boundary_test.go`

**Interfaces:**
- Consumes: `events.EventTurnStarted` from Task 1.
- Produces: `func (p *AppEventProjector) closeActiveTurn(status appwire.TurnStatus) []AppNotification` —
  the single close, factored from the four copies at `:199`, `:264`, `:703`
  and `:1065`, each of which resets the same seven fields and builds the same
  `turn/completed`.

- [ ] **Step 1: Write the failing tests** in `turn_boundary_test.go`:

1. `TestTurnBoundaryOpensTheNamedTurn` — `EventTurnStarted{TurnID: "turn_m9"}`
   emits `turn/started` for `turn_m9` and `ActiveTurnID() == "turn_m9"`.
2. `TestTurnBoundaryWithoutAnIDMints` — empty `TurnID` still opens a turn
   (`turn_1`), so an unserved session keeps working.
3. `TestTurnBoundaryCompletesThePreviousTurn` — a boundary after an open turn
   emits `turn/completed` for the previous id *and* `turn/started` for the new
   one, in that order.
4. `TestNotificationTurnGetsItsOwnTurn` — the interleaved case, and the reason
   this task exists: boundary(`turn_m1`) → assistant text → boundary(`turn_m2`,
   kind `notification`) → `EventSteeringInjected` → assistant text. Assert the
   steering item and the second assistant text carry `turn_m2`, and that
   `turn_m1` was completed. Today the notification turn's content lands in
   `turn_m1` and `turn_m1` never completes.
5. `TestContentEventsDoNotOpenASecondTurn` — boundary then `EventUserInput`
   emits exactly one `turn/started` total.

- [ ] **Step 2: Run them, watch them fail.**
- [ ] **Step 3: Implement.**
      - Factor `closeActiveTurn`; use it at all four sites.
      - Add the `EventTurnStarted` case: `closeActiveTurn(completed)`, then
        `p.reservedTurnID = data.TurnID` when non-empty, then `startTurn()`
        and the `turn/started` notification.
      - Reduce `EventUserInput` (`:195-232`) and `EventGoalContinuation`
        (`:258-292`) to `ensureTurn` + their item. Delete their
        `StableTurnID` adoption — the boundary owns naming now.
      - Keep `ensureTurn`'s mint: it is the fallback for a stream with no
        boundary (replay, unserved sessions), and Task 2's tests prove the
        boundary is what serves the live path.
- [ ] **Step 4: Run them, watch them pass. Mutations to verify:** delete the
      `reservedTurnID` assignment in the new case (test 1 fails); delete the
      `closeActiveTurn` call in the new case (tests 3, 4 fail).
- [ ] **Step 5: Run `go test ./internal/appprojector/ ./server/ ./cmd/serf/`.**
      Turn-boundary tests written against the old two-opener model will need
      updating; each such update must be justified in the commit, not waved
      through.
- [ ] **Step 6: Facet mapping.** Add `events.EventTurnStarted` to
      `server/thread_envelope.go`'s map beside `EventGoalContinuation`
      (`:208`). A turn starting changes queue and ask state; mirror what the
      kinds it replaces already declared, and say why in a comment.
- [ ] **Step 7: Commit.**

---

### Task 4: Live-stack regression for a notification turn

**Files:**
- Modify: `cmd/serf-hub/e2e_turn_control_test.go`

- [ ] **Step 1: Write the failing test.**
      `TestE2E_TurnControlReachesANotificationTurn`, mirroring
      `TestE2E_TurnControlReachesAnAgentStartedTurn`. Provoke a notification
      wake by scripting fakellm to start a background shell job that exits
      while the session is idle; the job's completion notification is what
      wakes it. If a background job proves awkward to drive deterministically,
      a `delegate` that finishes is the other producer — pick whichever the
      fixture can script without a real model, and say which in the comment.
      Then: `awaitActiveTurn`, `turn/steer` with that id, assert applied,
      release the round, assert the steer text reaches the next model request,
      `turn/interrupt`, assert applied.
- [ ] **Step 2: Verify it is red before Tasks 1-3** by running it in a scratch
      worktree at this branch's merge-base (`git worktree add`, never
      `git stash`). Expected: `turn/steer against ... "turn_<n>": turn is not
      active`.
- [ ] **Step 3: Green after.** Run the whole `TestE2E_TurnControl` family.
- [ ] **Step 4: Commit.**

---

### Task 5: Close the kata and run the gates

- [ ] **Step 1:** `make lint`, `make build`, `go test ./...`, the seven module
      suites, `make test-web`.
- [ ] **Step 2: Live browser pass.** `./scripts/e2e-webui-turn-controls.sh`,
      spawn a session, provoke a notification turn, drive Steer and Stop from
      the UI, and confirm the run's mutation journal
      (`$run/home/.local/state/serf/projects/*/mutations/<SID>.json`) records
      them `terminal`, not `rejected`.
- [ ] **Step 3: Close kata `7vmd`** with typed evidence — the e2e test name
      and the journal result. Comment on `c2ty` that the boundary has shrunk
      its window to one event-queue hop, and leave the ruling to Jesse.
- [ ] **Step 4: Commit.**

## Self-Review

**Spec coverage.** The Diagnosis names four entry kinds and two consequences.
Task 2 announces a boundary for all four kinds (tests 1-3 cover three;
`EntryDelegateAttention` shares the same call site and is covered by the same
production line). Task 3's test 4 is the un-closed-turn consequence. Task 4
pins the notification kind end to end.

**Placeholders.** Task 4 Step 1 leaves the notification producer to the
implementer because which of the two is deterministic under fakellm is a
question the fixture answers, not the plan; it names both candidates and
requires the choice be recorded. Every other step carries the code, the
command and the expected output.

**Type consistency.** `EventTurnStarted`, `TurnStartedData`, `TurnID`,
`Kind`, `beginRunningTurn`, `announceTurnStarted`, `releaseRunningTurnID`,
`closeActiveTurn` are spelled identically throughout.

**Known risk, stated plainly.** This changes how every turn opens and closes
in the projector — the same area as kata `eptj`, whose two-minters bug
silently overwrote persisted turn content. The mitigations are: one mint site
(global constraint), `ensureTurn` retained as the no-boundary fallback so no
stream loses the ability to open a turn, `SeedPersistedTurns` untouched so
replayed ids still fence the live counter, and a mutation check on every new
production line. An implementer who cannot make a mutation fail its test
should stop and say so rather than proceed.
