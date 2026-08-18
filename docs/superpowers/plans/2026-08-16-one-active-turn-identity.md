# One Active Turn Identity Implementation Plan — LANDED IN PART

> **LANDED IN PART, and its other half was REVERTED. Do NOT execute this plan.**
>
> Task 1's **goal-continuation** half shipped as `c751369d7` and is live:
> `GoalContinuationData` carries a `StableTurnID` minted through
> `reserveClientMutationTurnID`, and the projector adopts it before the turn
> opens. Task 2 was dropped during execution (its own section says so). Task 3
> shipped as `ce229b9de`, as `TestE2E_TurnControlReachesAnAgentStartedTurn`.
>
> Task 1's **notification** half was reverted the same night by `b5ce354a5` as
> unsound. `SteeringInjectedData.StableTurnID` names the *steering mutation's
> own* durable record — `clientMutationSteer` reserves a fresh one per steer —
> not the turn the steer lands in, so adopting it lets a steer drained across a
> turn boundary name the turn after itself, and every control aimed at that turn
> is then rejected. `TestSteeringInjectedNeverNamesATurn`
> (`internal/appprojector/goal_turn_identity_test.go`) exists to pin that the
> projector must never adopt it. Steps 6 and 7 below still prescribe exactly
> that adoption; both are marked REVERTED inline rather than deleted.
>
> The notification turn was refiled as kata `7vmd` and fixed separately by
> `2026-08-16-one-turn-boundary.md`, which gave it `EventTurnStarted` as a
> carrier of its own precisely because this field was already spoken for. That
> work shipped and `7vmd` is closed.
>
> Kept for its diagnosis of the two id namespaces and its v1 review record. The
> task list is not safe to follow.
>
> **The Diagnosis is history, not a description of the code.** Its account of why
> the two namespaces diverged still explains the shape of what shipped, but its
> line numbers have drifted and, more importantly, the mechanism it describes is
> gone: `c435bc579` deleted `expectedTurnId` from every mutation, so the
> "precondition compares against the durable authority" story it tells has no
> counterpart in the code. The five citations it gives for that comparison
> (`session_client_mutation_queue.go:123,325,392,497` and
> `session_client_mutation.go:411`) now land on unrelated lines. Read it for the
> reasoning, never for a pointer.
>
> **On the commit ids in this document.** They were written on
> `wip/webui-steer-send-stop`, whose commits were rewritten on the way to `main`.
> Every sha quoted in this banner is the one that is reachable from `main` today;
> `git merge-base --is-ancestor <sha> main` succeeds for each.

**Goal:** Give every turn — however it started — exactly one identity, so the
`activeTurnId` the daemon publishes is always the one its mutation
preconditions accept, and Steer / Send / Stop work on every turn.

**Architecture:** A turn is named **in band, on the event that opens it**.
`EventUserInput` already carries `StableTurnID` and the projector adopts it
immediately before `startTurn()`; this change gives the other turn-opening
events the same field and populates it, and makes the durable authority
record the running turn's id for turns no client mutation reserved one for.
Nothing announces a turn id out of band, and the daemon stops minting turn
ids for live turns. (That last clause was Task 2, which was **dropped**:
`setProcessingLocked` still mints for the pre-event window, deliberately, per
kata `c2ty`.)

**Tech Stack:** Go 1.25 multi-module workspace (`agent`, root module's
`server` / `internal/appprojector` / `cmd/serf`), `appwire` JSON-RPC.

**Spec:** this document — the Diagnosis section below is the spec.

**Revision:** v2. v1 named turns through an out-of-band callback from
`processOneInput` to `Server.SetProcessingTurn`. Two independent adversarial
reviews killed that mechanism; the Review Findings section at the end records
each finding and what v2 does about it. Read it before changing this plan.

## Global Constraints

- **No backward compatibility.** Jesse's call. Delete the superseded path;
  do not keep it behind a flag or a fallback.
- **A turn is named by the event that opens it.** No side channel. The
  session goroutine and the projector run on different goroutines
  (`agent/session_events.go:95`), so anything that names a turn out of band
  races the event stream.
- **One mint site.** Turn ids come from `reserveClientMutationTurnID`
  (`agent/session_client_mutation_queue.go:642-645`) and nowhere else.
  `agent/session_client_mutation_turn_namespace_test.go:52-57` exists to
  enforce exactly this.
- **A running turn is not durable state.** `ActiveTurnID` describes a turn in
  flight; a process that restarts has no turn in flight. It is reconciled at
  load, never inherited.
- Gates, in order, from the repo root: `make lint`, `make build`,
  `go test ./...`, the module suites (`cd agent && go test ./...`, and the
  same for `auth`, `envvars`, `fuzz`, `identifier`, `invariant`, `llm`), and
  `make test-web`.
- Never `git stash`; never `git checkout <file>` to undo.

---

## Diagnosis (the spec)

The daemon keeps two "active turn id"s that answer different questions:

- **`turn_m<n>` — durable.** Minted at *accept* time, before the turn runs,
  and returned synchronously in the `turn/start` response
  (`agent/session_client_mutation.go:216,235`). Persisted to
  `<state-dir>/mutations/<SID>.json`. `clientMutationSnapshot.ActiveTurnID`
  is documented as "the sole durable authority used by retry-safe mutation
  preconditions" (`agent/session_client_mutation.go:128-131`), and it is what
  every mid-turn mutation is compared against:
  `session_client_mutation_queue.go:123,325,392,497` and
  `session_client_mutation.go:411`.
- **`turn_<n>` — the projector's.** Minted in memory when a turn opens with
  no reservation standing (`internal/appprojector/appwire_projection.go:1652`),
  re-derived from the persisted transcript entry count on resume
  (`SeedPersistedTurns`).

The daemon's own thread publishes `s.appActiveTurnID` from `appThread()`
(`server/appwire_runtime.go:1198,1245`). That field is re-synced from the
projector on **every** projected event (`server/appwire_runtime.go:212`), and
otherwise only by `setProcessingLocked` (`server/server.go:713-716`) and
`SetProcessingTurn` (`:691-699`).

So the wire follows the projector, and the projector's id is right exactly
when the turn-opening event named it. Which events name it:

| Entry kind | Opening event | Carries `StableTurnID`? |
| --- | --- | --- |
| `EntryUserInput` (client `turn/start`, queued drain) | `EventUserInput`, `agent/session_lifecycle.go:1423-1429` | **yes**, populated from `queuedIdentity.StableTurnID` |
| `EntryContinuation` (goal) | `EventGoalContinuation`, `:1482` | **no such field** on `GoalContinuationData` (`agent/events/payloads.go:668`) |
| `EntryNotification` (job / watch / delegate wakes) | `EventSteeringInjected` (emitted today at `agent/session_lifecycle.go:1656`) | field exists (`agent/events/payloads.go:356`) but is **not populated**, and the projector's case ignores it — and **must keep ignoring it**; this row is the trap, see the banner. The live case is `internal/appprojector/appwire_projection.go:691`, decode at `:693`, and the "must never be adopted" reasoning at `:694-701`. |

The projector adopts a named id at `appwire_projection.go:225-227` —
`if data.StableTurnID != "" { p.reservedTurnID = data.StableTurnID }`,
immediately before `startTurn()`. Unnamed turns fall through to
`p.nextTurn++` and become `turn_<n>`.

Meanwhile the durable side is written only by `AcceptClientMutationStart`
(`:235`) and `popQueueHead` (`agent/session_queue.go:581`). Neither runs for
a goal continuation or a notification wake, so for those turns the wire says
`turn_<n>` and the precondition holds a stale `turn_m<n>` or `""`.

Result: `turn/steer`, `turn/queue`, `turn/drainAsSteer`,
`turn/promoteQueuedAsSteer` and `turn/interrupt` are all rejected with
`Conflict("turn is not active")`. The composer surfaces no error; the draft
just stays in the box (filed as kata `2f41`).

**The queued-drain path is NOT broken.** v1 claimed it was. `popQueueHead`
sets `ActiveTurnID` durably and hands its `StableTurnID` to the
`EventUserInput` emit, so both sides already agree.

**Evidence.** Jesse's live session `0348HuXSlWRtoLEoQ4EOE8`: its mutation
journal holds 3× `turn/steer`, 1× `turn/queue`, 1× `turn/interrupt`, every one
with `expected_turn_id: turn_5`, every one `rejected — "turn is not active"`,
while the daemon's own active turn was `turn_m6`. Reproduced deterministically
on a disposable stack: `goal/set` on an idle session publishes
`activeTurnId: turn_2`, and Steer and Stop from the real browser UI both land
in the journal as `rejected | turn is not active`.

**Decision (Jesse's, 2026-08-16).** `clientMutationSnapshot.ActiveTurnID`
means *the turn that is running*, not *the turn a client mutation reserved*.

### Why not the alternatives

- *Route agent-started turns through `AcceptClientMutationStart`.* Drags
  journal records, budget reservations and `MaxTurns` accounting onto turns
  no client requested — and restore would try to re-run them.
- *Accept either id family at the precondition.* Keeps two authorities alive
  forever, and on the interrupt path
  (`session_client_mutation.go:537`, `record.StableTurnID = fence.ExpectedTurnID`)
  writes a projector-minted id into the durable journal as if it were a
  reservation.
- *Publish an empty id when the turn has no durable identity.* Honest, but
  the UI loses Steer and Stop during goal and notification turns — no way to
  stop a runaway delegate-notification turn from the web UI.

### Out of scope, filed

- **kata `2f41`** — a `Conflict`-rejected steer/send/stop is completely
  silent in the web composer. That silence is why this bug ran unnoticed.
- **`EntryDelegateAttention`** — `acceptDelegateAttentionInput`
  (`session_lifecycle.go:1489-1492`) emits no opening event at all, so its
  turn is named by whatever event happens to arrive first. Task 1 Step 3
  checks whether that kind can reach the serve input loop; if it can, file a
  kata rather than widening this change.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `agent/events/payloads.go` (modify) | `GoalContinuationData` gains `StableTurnID`, matching `UserInputData` and `SteeringInjectedData`. |
| `internal/appprojector/appwire_projection.go` (modify) | The `EventGoalContinuation` case adopts a named id, exactly as `EventUserInput` already does. (**`EventSteeringInjected` was in this row and is REVERTED** — it must never adopt; see Step 7.) |
| `agent/session_active_turn.go` (create) | Mint / release the running turn's durable id under the guards this plan's review earned. One responsibility, so the client-mutation files stay about client mutations. |
| `agent/session_active_turn_test.go` (create) | Unit coverage: mint, refuse-under-fence, refuse-when-owned, release, no-store-no-mint. |
| `agent/session_client_mutation_persist.go` (modify) | Reconcile `ActiveTurnID` at load. |
| `agent/session_lifecycle.go` (modify) | ~~Mint after the accept block; pass the id into the two opening events.~~ **Both halves wrong** — see Step 6's REVERTED marker. The mint is *before* the accept chain (at the top of `processOneInput`'s `EntryNotification` block), and only one opening event carries the id: `EventGoalContinuation`. |
| `server/server.go` (modify) | `SetProcessing` stops minting turn ids. (**Not done** — Task 2 was dropped; `setProcessingLocked` still mints.) |
| `cmd/serf/serve.go` (modify) | Nothing announces turn ids; `holdServeStateForAwaitingWake` stays. (Task 2 left this file untouched.) |
| `cmd/serf-hub/e2e_turn_control_test.go` (modify) | Live-stack regression on a goal-continuation turn. |

---

### Task 1: Name the turns that open unnamed

**Status: shipped for goal continuations (`c751369d7`); the notification half
was reverted by `b5ce354a5`.** Steps 1–5, 8 and 9 are done. Step 6's
notification emit and Step 7's `EventSteeringInjected` adoption are the reverted
part — see the REVERTED markers on each.

**Files:**
- Modify: `agent/events/payloads.go:668` (`GoalContinuationData`)
- Modify: `internal/appprojector/appwire_projection.go` (`EventGoalContinuation` case at :258, `EventSteeringInjected` case at :727)
- Create: `agent/session_active_turn.go`
- Create: `agent/session_active_turn_test.go`
- Modify: `agent/session_lifecycle.go:1029-1039` (mint point), `:1482` (continuation emit), `:1553` (notification emit)
- Modify: `agent/session_client_mutation_persist.go:36` (load reconciliation)

**Interfaces:**
- Produces: `func (s *Session) mintRunningTurnID() string` — package-private.
  Returns `""` when this turn must run unnamed; the caller passes whatever it
  returns straight into the opening event's `StableTurnID`, and `""` there
  means "unnamed", which is the existing behaviour.
- Produces: `func (s *Session) releaseRunningTurnID(turnID string)` — package-private.

- [x] **Step 1: Write the failing unit test**

Create `agent/session_active_turn_test.go`:

```go
package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestMintRunningTurnIDNamesAnAgentStartedTurn pins the contract every
// mid-turn control depends on: a turn no client mutation reserved an id for
// still gets one from the single mint site, and it is the durable value the
// preconditions compare against.
func TestMintRunningTurnIDNamesAnAgentStartedTurn(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.markAuthoritativeConsumerForTest()

	turnID := s.mintRunningTurnID()
	if !strings.HasPrefix(turnID, "turn_m") {
		t.Fatalf("minted %q, want the turn_m<n> family", turnID)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != turnID {
		t.Fatalf("durable ActiveTurnID = %q, want the minted %q", got, turnID)
	}

	s.releaseRunningTurnID(turnID)
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("durable ActiveTurnID after release = %q, want empty", got)
	}
}

// TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed pins the race the v1
// review found: a client turn/start accepted between the serve loop dequeuing
// an agent wake and this mint would otherwise have its identity adopted by
// the agent turn — and a later Stop aimed at it would cancel the agent turn
// while marking the user's never-run message "interrupted".
func TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.markAuthoritativeConsumerForTest()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed an already-named turn: %v", err)
	}
	owned := s.clientMutations.snapshot().ActiveTurnID

	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q, want %q (refuse; the slot is owned)", got, "")
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != owned {
		t.Fatalf("ActiveTurnID = %q, want the owner's %q left untouched", got, owned)
	}
}

// TestMintRunningTurnIDRefusesUnderAnInterruptFence matches every other
// durable entry point (session_client_mutation.go:198,407;
// session_client_mutation_queue.go:119,322,389,494), which all refuse while
// an interrupt is pending.
func TestMintRunningTurnIDRefusesUnderAnInterruptFence(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.markAuthoritativeConsumerForTest()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: "cm-1", ExpectedTurnID: "turn_m1",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed an interrupt fence: %v", err)
	}
	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q under a pending interrupt, want %q", got, "")
	}
}

// TestMintRunningTurnIDSkipsUnservedSessions keeps in-process subagents off
// the durable store entirely. They share the parent's StateDir
// (subagents.go:581) and drive a turn per delegate wake; a turn no client can
// address needs no name and must not cost two fsyncs.
func TestMintRunningTurnIDSkipsUnservedSessions(t *testing.T) {
	s := newTestSessionForEnvctx(t) // no authoritative consumer registered
	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q for an unserved session, want %q", got, "")
	}
}
```

- [x] **Step 2: Run it and watch it fail**

Run: `cd agent && go test ./ -run TestMintRunningTurnID -v`
Expected: FAIL — `s.mintRunningTurnID undefined`.

- [x] **Step 3: Add the mint / release seam**

Create `agent/session_active_turn.go`:

```go
package agent

import (
	"fmt"

	"primeradiant.com/evener/agent/events"
)

// A turn's identity has one owner: whatever opened it. A client's turn/start
// and a queued message claimed off the input queue arrive already named — the
// reservation is what makes them retry-safe across a crash. Everything else —
// a goal continuation, a job or delegate notification wake — is named here,
// so the id the daemon publishes is always an id the mutation preconditions
// accept.
//
// The id is minted, never adopted. An ActiveTurnID this call did not write
// belongs to a mutation that is about to run; taking it would let a Stop
// aimed at that mutation cancel this turn instead and mark a message the user
// sent, and the session never ran, "interrupted".

// mintRunningTurnID names the turn that is about to run and records it as the
// durable authority, or returns "" when this turn must run unnamed. The
// caller passes the result into the turn's opening event; "" there means
// "unnamed", which is what unnamed turns already do today.
func (s *Session) mintRunningTurnID() string {
	// A session nobody serves has no client to name a turn to. In-process
	// subagents share the parent's StateDir (subagents.go:581), so this is
	// the difference between a durable write per delegate wake and none.
	s.eventsMu.Lock()
	served := s.authoritativeConsumer
	s.eventsMu.Unlock()
	if !served {
		return ""
	}
	if err := s.ensureClientMutationStore(); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("open client mutation store: %v", err),
		})
		return ""
	}
	var turnID string
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.InterruptFence != nil {
			return nil
		}
		if snapshot.ActiveTurnID != "" {
			return nil
		}
		record := clientMutationRecord{}
		reserveClientMutationTurnID(snapshot, &record)
		snapshot.ActiveTurnID = record.StableTurnID
		turnID = record.StableTurnID
		return nil
	})
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("name running turn failed: %v", err),
		})
		return ""
	}
	return turnID
}

// releaseRunningTurnID clears an id minted by mintRunningTurnID. It is a
// no-op for any other id, so a turn that ended after a client mutation took
// the slot cannot clear that mutation's identity out from under its own
// settle path.
func (s *Session) releaseRunningTurnID(turnID string) {
	if turnID == "" || s.clientMutations == nil {
		return
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.ActiveTurnID == turnID {
			snapshot.ActiveTurnID = ""
		}
		return nil
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("release running turn failed: %v", err),
		})
	}
}
```

Add the test-only helper to the same file's test companion — put it in
`agent/session_active_turn_test.go`, not in production code:

```go
func (s *Session) markAuthoritativeConsumerForTest() {
	s.eventsMu.Lock()
	s.authoritativeConsumer = true
	s.eventsMu.Unlock()
}
```

- [x] **Step 4: Run the unit tests and watch them pass**

Run: `cd agent && go test ./ -run TestMintRunningTurnID -v`
Expected: PASS, all four.

- [x] **Step 5: Reconcile the durable authority at load**

A running turn cannot survive the process that ran it. Without this, an
ungraceful exit mid-agent-turn leaves `active_turn_id` set with no pending
execution to settle it, and every later `turn/start` is rejected by
`AcceptClientMutationStart`'s guard (`session_client_mutation.go:206`) with
`Conflict("turn is already active")` — forever, silently.

Add a failing test to `agent/session_active_turn_test.go`:

```go
// TestLoadClearsARunningTurnNoPendingExecutionOwns is the crash guard: an
// ungraceful exit mid-turn must not brick every future turn/start.
func TestLoadClearsARunningTurnNoPendingExecutionOwns(t *testing.T) {
	dir := t.TempDir()
	seeded := newEmptyClientMutationSnapshot("sess-1")
	seeded.NextTurnSequence = 7
	seeded.ActiveTurnID = appwire.ClientMutationTurnID(7)
	fs := afero.NewMemMapFs()
	if _, err := saveClientMutationSnapshotFS(fs, dir, "sess-1", seeded, clientMutationWriteEffect, nil); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	loaded, err := loadClientMutationSnapshotFS(fs, dir, "sess-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID survived a restart as %q; a running turn is not durable state", loaded.ActiveTurnID)
	}
	if loaded.NextTurnSequence != 7 {
		t.Fatalf("NextTurnSequence = %d, want 7 — the counter must stay monotonic", loaded.NextTurnSequence)
	}
}
```

Add `"github.com/spf13/afero"` to the test imports. Run it
(`cd agent && go test ./ -run TestLoadClearsARunningTurn -v`), watch it fail,
then in `agent/session_client_mutation_persist.go`, after the snapshot
decodes and before it is returned, add:

```go
	// A running turn does not survive the process that ran it. An id no
	// pending execution owns is the residue of an ungraceful exit; inheriting
	// it makes AcceptClientMutationStart's "turn is already active" guard
	// reject every future turn/start with nothing left to settle it. The
	// sequence counter is kept: ids must stay monotonic across restarts.
	if snapshot.ActiveTurnID != "" {
		owned := false
		for _, pending := range snapshot.PendingExecutions {
			if pending.TurnID == snapshot.ActiveTurnID {
				owned = true
				break
			}
		}
		if !owned {
			snapshot.ActiveTurnID = ""
		}
	}
```

Re-run the test; expect PASS.

- [~] **Step 6: Carry the id on the two opening events** — **the goal-continuation
  half shipped; the notification half is REVERTED (`b5ce354a5`).**

> **REVERTED.** The `acceptNotificationInput` half of this step is not live and
> must not be re-implemented as written. Two problems, both found by adversarial
> review after it shipped:
>
> 1. `acceptNotificationInput` proceeds on job notifications **or** pending
>    steering **or** root delegate attention, but the only emit carrying the name
>    was guarded by `len(jobNotifs) > 0`. An attention-driven wake therefore minted
>    a durable `ActiveTurnID` that no event ever published — leaving the original
>    bug in place **and** newly rejecting `turn/start` with "turn is already
>    active", where before it was accepted and ran next.
> 2. `SteeringInjectedData.StableTurnID` names the steering mutation's own record,
>    not the turn (see Step 7's marker).
>
> The mint shape below is also stale twice over. `c24c283ce` moved the
> notification mint, and `2bf03d10d` then moved it again: the name is now taken at
> the top of `processOneInput`, before any wake state is consumed, because
> `beginRootDelegateAttentionTurn` consumes the process-local wake and a
> stand-down decided after it strands the attention the wake existed to deliver.
> The release is a single `defer` registered *before* the mint. Read
> `agent/session_lifecycle.go` for the shipped shape; do not copy the block below.

In `agent/events/payloads.go`, add to `GoalContinuationData`:

```go
	// StableTurnID names the turn this continuation opens, so the projection
	// adopts the daemon's own id rather than minting one the mutation
	// preconditions will not accept. Matches UserInputData.
	StableTurnID string `json:"stable_turn_id,omitempty"`
```

In `agent/session_lifecycle.go`, mint once, after the accept block that can
refuse the wake — insert at `:1039`, immediately after the
`if kind == EntryContinuation { ... } else if ...` chain, NOT at `:1004`.
`processOneInput` returns early at `:1006-1012` (cancelled ctx) and
`:1031-1033` (`acceptNotificationInput` refuses an empty queue), and a turn
that never runs must not burn a sequence number or publish an id.

Because the id has to reach `acceptContinuationInput` and
`acceptNotificationInput`, which emit the opening events, mint *before* the
chain and release on the refusing paths instead:

```go
	// Name the turn before the event that opens it, so the projection adopts
	// this id in stream order rather than minting turn_<n> of its own. Only
	// the kinds whose opening event carries the id are named here; user input
	// arrives already named by its own reservation.
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
	} else if kind == EntryDelegateAttention {
		s.acceptDelegateAttentionInput()
	} else if err := s.acceptUserInput(ctx, input, images, inputProvenance, kind == EntryUserInput); err != nil {
		return "", false, err
	}
	if runningTurnID != "" {
		defer s.releaseRunningTurnID(runningTurnID)
	}
```

Thread the parameter through both accepts:

- `acceptContinuationInput(_ context.Context, input string, stableTurnID string)`
  — pass it on the emit at `:1482`:
  `s.emit(events.EventGoalContinuation, events.GoalContinuationData{Text: marker, StableTurnID: stableTurnID})`
- ~~`acceptNotificationInput(ctx context.Context, stableTurnID string) (proceed bool)`
  — pass it on the emit at `:1553`:
  `s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: reminder, Kind: events.SteeringKindNotification, StableTurnID: stableTurnID})`~~
  **REVERTED.** The shipped emit carries no `StableTurnID`; the notification turn
  is named by `EventTurnStarted` instead. `acceptNotificationInput` does take a
  trailing `turnID string` today, but it spends it on the boundary event, not on
  the steering payload.

Check the other `acceptNotificationInput` call sites and update them; run
`grep -rn "acceptNotificationInput\|acceptContinuationInput" --include="*.go" .`
first.

While threading, check whether `EntryDelegateAttention` can reach the serve
input loop at all (`grep -rn "EntryDelegateAttention" --include="*.go" .`).
If it can, file a kata — it emits no opening event and cannot be named this
way; do not widen this change to cover it.

- [~] **Step 7: Make the projector adopt the named id** — **the
  `EventGoalContinuation` half shipped; the `EventSteeringInjected` half is
  REVERTED (`b5ce354a5`).**

> **REVERTED.** The projector must **never** adopt
> `SteeringInjectedData.StableTurnID`. That field names the steering mutation's
> own durable record — `clientMutationSteer` reserves a fresh one per steer — so
> adopting it names the turn after a steer that merely landed in it, and every
> mid-turn control aimed at that turn is then rejected. A `p.activeTurnID == ""`
> guard does not rescue it: it makes the adoption inert for the interleaved case,
> which is the common one.
>
> `TestSteeringInjectedNeverNamesATurn`
> (`internal/appprojector/goal_turn_identity_test.go`) fails if this comes back,
> and `internal/appprojector/appwire_projection.go`'s `EventSteeringInjected`
> case carries the same reasoning as a comment.

In `internal/appprojector/appwire_projection.go`, the `EventUserInput` case
already does this at `:225-227`. Add the same two lines to the other two,
immediately before each opens its turn:

- `EventGoalContinuation` (case at `:258`), after the completion block and
  before `startTurn()`:
  ```go
  		if data := eventData[events.GoalContinuationData](event.Data); data.StableTurnID != "" {
  			p.reservedTurnID = data.StableTurnID
  		}
  ```
  (fold into the existing `data :=` if the case already decodes the payload.)
- ~~`EventSteeringInjected` (case at `:727`), after `data` is decoded at `:729`:~~
  **REVERTED — do not add this.** See the marker above.
  ```go
  		// NOT LIVE. Adding this back breaks TestSteeringInjectedNeverNamesATurn.
  		if data.StableTurnID != "" {
  			p.reservedTurnID = data.StableTurnID
  		}
  ```

- [x] **Step 8: Run the agent and projector suites**

Run: `cd agent && go test ./...` then `go test ./internal/appprojector/`
Expected: PASS. `ActiveTurnID` is now set during goal and notification turns,
so `AcceptClientMutationStart`'s `Conflict("turn is already active")` guard
fires where it previously did not. That is the intended meaning of the field
— the composer already routes Send to `turn/queue` while busy — but if a test
asserted the old behaviour, update it and say so in the commit.

- [x] **Step 9: Commit**

```bash
git add agent/session_active_turn.go agent/session_active_turn_test.go agent/session_lifecycle.go agent/events/payloads.go agent/session_client_mutation_persist.go internal/appprojector/appwire_projection.go
git commit -m "fix(agent): name goal and notification turns on the event that opens them"
```

---

### Task 2: NOT DONE — the daemon keeps minting for the pre-event window

**Status: dropped during execution. Do not implement it without asking Jesse.**

The task below would have deleted `setProcessingLocked`'s
`ReserveTurnID()` branch (`server/server.go:713-716`) so that nothing but a
turn's own opening event could publish an id. Reading kata `c2ty` first
showed that branch is a deliberate fix, not an oversight:

- `c2ty`'s 2026-07-26 comment records the constraint that makes a new mint
  site dangerous — kata `eptj` was real data loss from two independent
  minters sharing one `turn_N` namespace, so "a fix here must be shown not to
  mint an id the reload path or the projector can also mint". Task 1 honours
  that by going through `reserveClientMutationTurnID`.
- The same comment weighs c2ty's own symptom — "a brief window of
  wrong-but-recoverable controls" — against that risk and chooses to hold.
  `TestServerAppWireSetProcessingPublishesActiveTurnID`
  (`server/appwire_server_test.go:66`) pins the resulting behaviour, with a
  message explaining that a `status=active` + empty `activeTurnId` pair makes
  the composer offer idle controls for a working session.

So deleting the branch would reverse a ruling Jesse made, trading a silent
rejection for a missing control. Task 1 fixes the steady state — the case
Jesse actually hit, where a notification turn published `turn_6` for its
whole life. What remains is a sub-second window between `SetProcessing(true)`
and the turn's first event, in which the wire can still carry a projector id.
Closing it needs the single-lock-hold transition c2ty describes, which the
in-band naming does not compose with for free. Recorded as a comment on
`c2ty`; Jesse's call.

<details>
<summary>The dropped task, kept for the record</summary>

#### Task 2 (not done): The daemon stops minting turn ids for live turns

**Files:**
- Modify: `server/server.go:701-718` (`setProcessingLocked`)
- Test: `server/appwire_server_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 — this is independent and could land first.
- Produces: no signature changes. `SetProcessing(bool)`, `SetProcessingTurn`,
  the `serveServer` interface (`cmd/serf/serve.go:65`) and both test doubles
  (`cmd/serf/serve_state_test.go:75,154`) all keep their shapes.

**Do not delete `AppEventProjector.ReserveTurnID`.** v1 said it had one
caller; it has two. `server/appwire_runtime.go:1416`
(`reserveAppTurnIDForStart`) still needs it, and deleting it takes out
`server/appwire_mutation_test_helpers_test.go:28,36` — a helper shared across
the retry-safe mutation test family.

- [ ] **Step 1: Write the failing test**

Add to `server/appwire_server_test.go`, matching that file's existing
construction helpers:

```go
// TestSetProcessingDoesNotMintATurnID pins that a generic processing flip
// publishes no turn id. A minted turn_<n> here is one the daemon's own
// mutation preconditions reject, which is how steer/send/stop died on goal
// and notification turns; the turn's opening event names it instead.
func TestSetProcessingDoesNotMintATurnID(t *testing.T) {
	srv := newTestServer(t)
	srv.SetProcessing(true)
	if got := srv.AppActiveTurnIDForTest(); got != "" {
		t.Fatalf("SetProcessing(true) published turn id %q, want none", got)
	}
	srv.SetProcessingTurn("turn_m4")
	if got := srv.AppActiveTurnIDForTest(); got != "turn_m4" {
		t.Fatalf("published turn id = %q, want the announced turn_m4", got)
	}
}
```

`appActiveTurnID` is unexported (`server/server.go:271`). Use whatever
accessor `server/appwire_server_test.go` already uses to read it; if there is
none, add `AppActiveTurnIDForTest()` in a `_test.go` file in package `server`,
not in production code.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./server/ -run TestSetProcessingDoesNotMintATurnID -v`
Expected: FAIL — a `turn_<n>` is published.

- [ ] **Step 3: Drop the mint from the processing flip**

In `server/server.go`, `setProcessingLocked`, delete the `if
strings.TrimSpace(s.appActiveTurnID) == "" { ... ReserveTurnID() }` branch at
`:713-716`, keeping the `s.appReservedTurnID = ""` line. Update
`SetProcessing`'s doc comment: a processing flip publishes no turn id; the
turn's opening event names it, and `SetProcessingTurn` publishes a durable
identity the serve loop already holds.

Drop the now-unused `strings` import only if nothing else in the file uses it.

- [ ] **Step 4: Run the tests**

Run: `go test ./server/ ./cmd/serf/ ./internal/appprojector/`
Expected: PASS. A test asserting that a generic processing flip mints a
`turn_<n>` is asserting the bug; delete it and note that in the commit.
`cmd/serf/serve_state_test.go:139`'s `sessionControlIdentityServer.SetState`
parks on `<-s.releaseProcessing`; nothing in this task moves that park point.

- [ ] **Step 5: Commit**

```bash
git add server/server.go server/appwire_server_test.go
git commit -m "fix(server): a processing flip no longer mints a turn id"
```

</details>

---

### Task 3: Live-stack regression — a goal turn takes steer and stop

**Status: shipped as `ce229b9de`.** It landed under the name
`TestE2E_TurnControlReachesAnAgentStartedTurn`, not the
`TestE2E_TurnControlReachesAGoalContinuationTurn` this task asked for.

**Files:**
- Modify: `cmd/serf-hub/e2e_turn_control_test.go`

**Interfaces:**
- Consumes: `fakellm.Server`, `startHubStack`, `awaitActiveTurn`,
  `awaitThread`, `clientRequest`, `newMutationID`, `communicateArgs` — all
  already in that file.

- [x] **Step 1: Write the failing test**

Append `TestE2E_TurnControlReachesAGoalContinuationTurn` to
`cmd/serf-hub/e2e_turn_control_test.go`. It mirrors the existing
`TestE2E_TurnControlReachesTheSession` exactly — same gating, same
`startHubStack`, same `t.Cleanup` thread shutdown — and differs only in the
middle:

1. `thread/start` with `SERF-E2E-GOAL-OPENING`; capture `ref`.
2. `provider.Next` for the opening round, `awaitActiveTurn(ctx, t, client, ref, "")`
   to capture `firstTurn`, then answer with
   `communicate` / `communicateArgs("opening turn done")` and
   `awaitThread(... func(thread) bool { return thread.Serf.ActiveTurnID == "" })`
   so the goal's idle kick is what starts the next turn.
3. `clientRequest[appwire.GoalSetResponse](ctx, client, appwire.MethodGoalSet,
   appwire.GoalSetParams{Ref: ref, Objective: "count to ten, one number per message"})`.
4. `provider.Next` for the goal continuation's round; `goalTurn :=
   awaitActiveTurn(ctx, t, client, ref, firstTurn)`.
5. `turn/steer` with `ExpectedTurnID: goalTurn` — assert no error and
   `Receipt.Disposition == appwire.MutationDispositionApplied`. Include
   `goalTurn` in the failure message; it is the diagnostic.
6. Answer the held round with `read_file` on `stack.readableFile`, take
   `provider.Next`, and assert the next request `Contains` the steer text —
   the model boundary, not the receipt.
7. `turn/interrupt` with `ExpectedTurnID: goalTurn`; assert applied, then
   `awaitThread` until `thread.Serf.ActiveTurnID != goalTurn`.

- [x] **Step 2: Run it against the unfixed daemon**

Check out the commit before Task 1 into a scratch worktree (never
`git stash`), or simply run this step before implementing Tasks 1–2.

Run: `go test ./cmd/serf-hub/ -run TestE2E_TurnControlReachesAGoalContinuationTurn -v`
Expected: FAIL — `turn/steer against the goal continuation turn "turn_2":
... turn is not active`.

- [x] **Step 3: Run it against the fixed daemon**

Run: `go test ./cmd/serf-hub/ -run TestE2E_TurnControl -v`
Expected: PASS, both tests in the family.

- [x] **Step 4: Commit**

```bash
git add cmd/serf-hub/e2e_turn_control_test.go
git commit -m "test(e2e): pin steer and stop on an agent-started turn"
```

---

### Task 4: Full gates

**Status: unverifiable from the tree.** A gate run leaves no artifact behind, so
nothing here can be checked off from evidence the way Tasks 1 and 3 can. The
boxes below are left as written and carry no claim either way. The plan is not
to be executed, so do not run this list as if it were live.

**Files:** none — this task only runs gates and fixes what they catch.

- [ ] **Step 1: Lint** — `make lint`, clean.
- [ ] **Step 2: Build** — `make build`, clean.
- [ ] **Step 3: Root suite** — `go test ./...`, PASS.
- [ ] **Step 4: Module suites** — `cd agent && go test ./...`, then `auth`,
      `envvars`, `fuzz`, `identifier`, `invariant`, `llm`. PASS.
- [ ] **Step 5: Frontend gate** — `make test-web`, PASS. Nothing here touches
      the frontend; a failure means something was mis-scoped.
- [ ] **Step 6: Verify against a real browser.** Run
      `./scripts/e2e-webui-turn-controls.sh --hold 25 --rounds 60`, spawn the
      session it prints, set a goal on it, then use Steer and Stop from the
      UI. Confirm in the run directory's mutation journal
      (`$run/home/.local/state/serf/projects/*/mutations/<SID>.json`) that both
      mutations read `terminal`, not `rejected`. Stop the stack with `--stop`.
- [ ] **Step 7: Commit any gate fixes.**

---

## Review Findings (v1) and what v2 does

Two independent adversarial reviews of v1. Every finding below was verified
against the code before v2 was written.

| # | Finding | v2 |
| --- | --- | --- |
| Both | v1's out-of-band announce raced the event stream, and `ReserveStableTurnID` blanks `p.activeTurnID` (`appwire_projection.go:1736`), so the previous turn would lose its `turn/completed` | **Mechanism replaced.** Naming rides the turn-opening event, in stream order. No side channel. |
| Both | A crash mid-agent-turn left a record-less `ActiveTurnID` with no clear path, bricking every future `turn/start` | Task 1 Step 5 reconciles at load. A running turn is not durable state. |
| A | An agent turn could adopt a concurrent `turn/start`'s reserved id; a later Stop would cancel the agent turn and mark the user's never-run message "interrupted" | `mintRunningTurnID` **mints, never adopts**, and returns `""` when the slot is owned. Pinned by `TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed`. |
| A | v1 ignored the interrupt fence, unlike every other durable entry point | Refuses under a fence. Pinned by `TestMintRunningTurnIDRefusesUnderAnInterruptFence`. |
| Both | `ReserveTurnID` has two production callers, not one; deleting it breaks `reserveAppTurnIDForStart` and a shared test helper | Task 2 keeps it and says why. |
| Both | Subagents share the parent's `StateDir` (`subagents.go:581`); v1 would write two snapshots per delegate wake, and its "no durable store" rationale was false | Gated on `authoritativeConsumer`. Pinned by `TestMintRunningTurnIDSkipsUnservedSessions`. |
| Both | v1 announced before the early returns at `:1006-1012` and `:1031-1033`, naming turns that never run | Mint moved after the accept chain, with a release on the refusing path. |
| Both | v1's Diagnosis cited `appwire_runtime.go:551,652` — the *descendant* paths, not the root thread | Corrected to `:1198,1245`, and `:212` is now named as the reason the wire follows the projector. |
| Both | v1's Task 2 test did not compile: no `server.New`, no `server.Config`, no exported `ActiveTurnID()`, wrong package | Task 2's test lives in package `server` and says to reuse that file's existing helpers. |
| A | v1 said the interface was `ServerLike`; it is `serveServer` (`serve.go:65`), and two test doubles override `SetProcessing` | Task 2 changes no signatures at all. |
| A | v1 claimed the post-interrupt queue drain was broken; `popQueueHead` (`session_queue.go:581`) already names it | Diagnosis corrected. That path is out of scope because it works. |
| A | v1 inlined the mint, defeating the single-mint-site kata (`session_client_mutation_turn_namespace_test.go:52-57`) | Uses `reserveClientMutationTurnID`. |
| A | v1 swallowed every store error silently, unlike `popQueueHead` | Both functions emit `EventWarning`. |
| A | v1 half-edited `nextTurnCtx`, leaving `SetState` without its `SetProcessing` | Task 2 leaves `serve.go` alone entirely. |
| B | v1 deleted `holdServeStateForAwaitingWake` on a false premise | It stays. |
| B | Descendant threads still mint `turn_<n>` and are untouched | True, and out of scope: a subagent session has no appwire server of its own, so no client names its turns. Recorded here so the next reader does not mistake it for an oversight. |

## Self-Review

**Spec coverage.** The Diagnosis table names two broken turn kinds —
`EntryContinuation` and `EntryNotification` — and Task 1 names both. Task 2
removes the second minter so the divergence cannot return by another route.
Task 3 pins the continuation kind end-to-end; the notification kind shares
the mint site and the unit tests, and `EntryDelegateAttention` is explicitly
scoped out with a check to confirm it cannot reach the serve loop.

**Placeholders.** Task 3 Step 1 describes its test as a numbered diff against
the sibling test in the same file rather than repeating 90 lines verbatim;
every other step carries real code, exact paths, exact commands and exact
expected output.

**Type consistency.** `mintRunningTurnID`, `releaseRunningTurnID`,
`runningTurnID`, `StableTurnID`, `markAuthoritativeConsumerForTest` are
spelled identically everywhere they appear. `acceptContinuationInput` and
`acceptNotificationInput` both gain exactly one trailing `stableTurnID string`
parameter.
