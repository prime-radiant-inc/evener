# Stop Parks the Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Stop leaves queued messages on the durable queue and stops anything from auto-running them, so the session goes idle with the user's words visible in the queue strip.

**Architecture:** One durable flag, `QueueHeld`, on the client-mutation snapshot. It is set where the interrupt fence is written and cleared by any user-initiated run. `popQueueHead` refuses while it is set — and because `popQueueHead` is the single claim gate for BOTH restart rails (the drain loop and `ProcessPendingUserInput` behind the wake), one guard closes both. There is deliberately NO process-local mirror of the flag; `sessionWorkPending` reads it through a narrow O(1) store accessor.

**Tech Stack:** Go (agent, server), the client-mutation durable store (`agent/session_client_mutation*.go`).

**Spec:** kata `wms7` — its "STATE OF PLAY" comment carries the two-rail analysis, the ruling, and the record of both rejected designs. Read it before this plan.

## Global Constraints

- **No process-local mirror of `QueueHeld`.** The first attempt at this kata mirrored it into `Session.inputQueueHeld` and the review found the mirror unupdated by three of four writers, unrestored across daemon restart, and corrupted by a rejected promote. One source of truth, read directly.
- **`sessionWorkPending` must not deep-copy.** `clientMutationStore.snapshot()` clones the whole journal under an RLock; `WireState` samples dozens of times a second. Use the narrow accessor from Task 1 only.
- **Steering is OUT OF SCOPE.** A Stop with pending steering still restarts. That is kata `1k3m`, blocked on where a parked steer surfaces and on `q62f`'s provenance loss. Do not touch `wakeForPendingSteering`.
- **`QueueDepth()` keeps counting held entries.** The queue strip must still show the messages. Only "is this session working" changes.
- Every clear of `QueueHeld` goes BELOW its mutation's rejection checks. `executeAtomic` commits a rejected record, so a clear above them unparks the queue on a refused request.

---

### Task 1: A cheap durable read for the held flag

**Files:**
- Modify: `agent/session_client_mutation.go` (the `clientMutationSnapshot` struct, and `clientMutationStore` methods near `snapshot()` at ~:1167)
- Test: `agent/session_stop_and_queued_work_test.go`

**Interfaces:**
- Produces: `clientMutationSnapshot.QueueHeld bool` (json `queue_held,omitempty`); `func (s *clientMutationStore) queueHeld() bool`.

- [ ] **Step 1: Write the failing test**

```go
func TestQueueHeldIsReadableWithoutCloningTheSnapshot(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	if sess.clientMutations.queueHeld() {
		t.Fatal("a fresh store reports the queue held")
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.QueueHeld = true
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if !sess.clientMutations.queueHeld() {
		t.Fatal("queueHeld did not see the durable flag")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./agent/ -run TestQueueHeldIsReadableWithoutCloningTheSnapshot -count=1`
Expected: build failure — `QueueHeld` and `queueHeld` undefined.

- [ ] **Step 3: Add the field and the accessor**

In `clientMutationSnapshot`, after `InputQueue`:

```go
	// QueueHeld parks the input queue after a Stop: the messages stay where they
	// are and nothing claims them until the user asks for one to run ("Stop
	// should cancel execution and wait", kata wms7).
	//
	// Durable because the queue it parks is durable: a daemon that restarts must
	// not treat parked messages as work to resume. There is deliberately no
	// process-local mirror -- the first attempt at this kata had one and the
	// review found it drifting on three of four writers, across restarts, and on
	// a rejected promote.
	QueueHeld bool `json:"queue_held,omitempty"`
```

Beside `snapshot()`:

```go
// queueHeld reads the parked-queue flag without cloning the snapshot.
// sessionWorkPending calls this on every WireState sample, and snapshot()
// deep-copies the whole journal.
func (s *clientMutationStore) queueHeld() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state.QueueHeld
}
```

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./agent/ -run TestQueueHeldIsReadableWithoutCloningTheSnapshot -count=1`
Expected: PASS.

- [ ] **Step 5: Update serf-doctor's mirror of this schema**

`agent/doctor/mutations.go` decodes the snapshot with unknown-field rejection, so this will otherwise fail `TestClientMutationSnapshotStaysReadableByTheDoctor`. Add to `clientMutationStoreFile` after `InputQueue`:

```go
	QueueHeld              bool                                  `json:"queue_held"`
```

and to `MutationReport` after `InputQueue`, because a parked queue is the answer to the question that tool gets opened for — a session with a non-empty queue running nothing looks wedged:

```go
	// QueueHeld reports a queue parked by a Stop: the entries below are real and
	// nothing will claim them until the user asks (kata wms7).
	QueueHeld bool `json:"queue_held"`
```

set it in `Mutations` beside `report.QueueRevision`:

```go
	report.QueueHeld = store.QueueHeld
```

and RENDER it in `RenderMutations`, beside the `input queue:` line — the previous attempt declared it and forgot to print it, so only `--json` callers could see it:

```go
	if r.QueueHeld {
		fmt.Fprintf(&b, "queue: held (parked by a Stop; waiting on the user)\n")
	}
```

Then prove the render, because declaring without printing is the rejected attempt's exact mistake and nothing else in the suite fails on it: in `agent/doctor/mutations_test.go`, add `"queue_held": true,` at the top level of the `storeWithBothOutcomes` fixture (after `"accepted_turns": 1,`) and add this line to the want list in `TestMutations_RenderCarriesTheDecisiveFields`:

```go
		"queue: held (parked by a Stop; waiting on the user)",
```

- [ ] **Step 6: Run the doctor drift guard and commit**

Run: `go test ./agent/ ./agent/doctor/ -run 'TestQueueHeld|Doctor|TestClientMutationSnapshot|TestMutations_Render' -count=1`
Expected: PASS.

```bash
git add agent/session_client_mutation.go agent/session_stop_and_queued_work_test.go agent/doctor/mutations.go agent/doctor/mutations_test.go
git commit -m "feat(agent): a durable parked-queue flag, read without cloning"
```

---

### Task 2: A Stop sets the hold; popQueueHead refuses while it is set

**Files:**
- Modify: `agent/session_client_mutation.go` (`InterruptClientMutation`: the `reservePrepared` callback where `InterruptFence` is written, and the wake calls at the applied path's tail)
- Modify: `agent/session_queue.go` (`popQueueHead`, beside its existing `InterruptFence` guard)
- Test: `agent/session_stop_and_queued_work_test.go` (adds the new test; deletes `TestStopWakesTheSessionForWorkItLeftQueued`, which pins the superseded contract)

**Interfaces:**
- Consumes: `clientMutationSnapshot.QueueHeld` from Task 1.

- [ ] **Step 1: Write the failing test**

```go
func TestStopParksTheQueueAgainstBothRestartRails(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	runningStartTurn(t, sess, "running-turn", "do the thing")
	queueOneMutation(t, sess, "queued-behind", "and then this")

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// popQueueHead is the single claim gate for BOTH rails that restart a
	// session: the drain loop in processOneInput, and ProcessPendingUserInput
	// behind the queued-input wake. Refusing here closes both.
	if claimed := sess.popQueueHead(); claimed.ClientMutationID != "" {
		t.Fatalf("the queue head was claimed after a Stop (%q): the message the user stopped will run", claimed.ClientMutationID)
	}
	// The SECOND return is "did a turn run"; the first is that turn's output
	// text, which an adapter may legitimately leave empty.
	if _, ranTurn, err := sess.ProcessPendingUserInput(context.Background(), nil); err != nil || ranTurn {
		t.Fatalf("ProcessPendingUserInput ran work after a Stop: ranTurn=%v err=%v", ranTurn, err)
	}
	// And the message is still the user's.
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the Stop = %d, want 1: parking must not cost the message", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./agent/ -run TestStopParksTheQueueAgainstBothRestartRails -count=1 -v`
Expected: FAIL — "the queue head was claimed after a Stop".

- [ ] **Step 3: Set the hold where the fence is written**

In `InterruptClientMutation`'s `reservePrepared` callback, immediately after `snapshot.InterruptFence = &clientMutationInterruptFence{...}`:

```go
		// Park the queue from the moment the Stop is ACCEPTED, not when the fence
		// finalizes. The claim this Stop cancels is returned to the queue by the
		// runner on its way out and the drain loop is right behind it, so holding
		// only at finalize leaves exactly the window the restart used (wms7).
		snapshot.QueueHeld = true
```

- [ ] **Step 4: Refuse the claim while held**

In `popQueueHead`, immediately after the existing `if snapshot.InterruptFence != nil { return nil }` guard:

```go
		// The same refusal as the fence above, held past the fence's finalize. A
		// Stop parks the queue until the user asks for something to run, and this
		// is the single gate both restart rails claim through -- the drain loop
		// and ProcessPendingUserInput behind the wake (kata wms7).
		if snapshot.QueueHeld {
			return nil
		}
```

- [ ] **Step 5: Run it and watch it pass**

Run: `go test ./agent/ -run TestStopParksTheQueueAgainstBothRestartRails -count=1 -v`
Expected: PASS.

- [ ] **Step 6: Remove the interrupt's own queue wake, which the hold makes dead**

The tail of `InterruptClientMutation`'s applied path currently reads:

```go
	// The fence is cleared now, so popQueueHead will claim again -- but nothing
	// has asked the session to run. A Stop cancelled the runner; anything still
	// queued behind it would sit there while the session reports itself as
	// working, which is the strand the fence guard would otherwise create.
	s.wakeForPendingQueuedInput()
	s.wakeForPendingSteering()
```

That wake IS the interim behaviour this plan reverses: it was added so a Stop delivers what it left queued. With `QueueHeld` set by this same mutation and cleared only by the user, the kick can never claim anything, and the comment's "popQueueHead will claim again" becomes false. Replace both lines with:

```go
	// No queued-input wake here, deliberately: the hold above parks the queue
	// until the user asks for something to run, so a kick would find nothing
	// claimable. The steering wake stays; a Stop with pending steering still
	// restarts, and that is kata 1k3m, not this one.
	s.wakeForPendingSteering()
```

(Task 3 then makes the session stop REPORTING a parked queue as pending work, which is what retires the strand the old wake existed to close — a session claiming to be busy over a queue nobody drains.)

(`wakePendingUserInput` feeds `notifyFunc`, the server's drain kick. It is not how state reaches the hub -- that is the event stream -- so removing the kick loses no publication. Task 5's flipped e2e proves the live daemon still settles.)

- [ ] **Step 7: Delete the test that pins the superseded contract**

Delete `TestStopWakesTheSessionForWorkItLeftQueued` from `agent/session_stop_and_queued_work_test.go`, including its doc comment. It asserts a Stop over queued work fires the drain kick — the delivery contract this task abolishes — and it fails once Step 6 lands. Its concern (a Stop must not strand queued work invisibly) is carried forward by `TestStopParksTheQueueAgainstBothRestartRails` (nothing runs, the message survives) and Task 3's `TestAParkedQueueDoesNotMakeTheSessionLookBusy` (the session stops claiming to be busy over it). This is contract replacement, not coverage reduction; say so in the commit message.

- [ ] **Step 8: Sweep the sibling comments that still document delivery as the ruling**

Three surviving comments pin "a Stop delivers what it left queued" as current. Their tests and code pass every gate in this plan (they assert durability, not delivery), so nothing forces the update — do it here, where the contract actually changes.

In `agent/session_stop_and_queued_work_test.go`, replace `TestStopCancelsTheTurnAndKeepsUserWorkDurable`'s doc comment (the block quoting the interim ruling, "…stays durable, is delivered, and starts the next turn… KNOWN and INTENDED…") with:

```go
// TestStopCancelsTheTurnAndKeepsUserWorkDurable pins the two halves of a Stop
// that must not regress: it actually cancels the running turn, and work the
// user already gave the session survives it durably.
//
// The steering case is the one no other test covers. A steer not yet injected
// stays durable and is still DELIVERED -- a Stop with pending steering
// restarts, and that stays true until kata 1k3m gives a parked steer somewhere
// to surface. A restart the user can stop again beats text they cannot get
// back: the rejected designs moved the user's words out of durable server
// storage and then failed to keep them safe. Queued messages, the other
// user-authored rail, park instead (kata wms7,
// TestStopParksTheQueueAgainstBothRestartRails).
```

Replace `TestStopKeepsAQueuedMessageDurable`'s doc comment ("…the promise is identical: Stop cancels, the message stays, and the session delivers it rather than dropping it.") with:

```go
// TestStopKeepsAQueuedMessageDurable covers the queued-message rail of the
// same promise: Stop cancels, and the message stays durably on the queue --
// parked, visible in the queue strip, payload intact -- until the user asks
// for it to run (kata wms7).
```

In `agent/session_lifecycle.go` (the drain-loop comment above `if !closed && !stopSettledThisTurn`), replace the sentence fragment "and the message waits for one of the ordinary wake paths." with "and the message waits, parked, until a user-initiated run releases the hold." — the wake paths now refuse a parked queue, so the old phrasing promises a delivery that will never come.

- [ ] **Step 9: Run the agent suite and commit**

Run: `go test ./agent/ -count=1`
Expected: PASS, with `TestStopWakesTheSessionForWorkItLeftQueued` gone and nothing else newly failing.

Known interim state, deliberate: from this commit until Task 5 flips it, the live boundary e2e (`TestE2E_ControlInvariantDuringPreTurnWorkAtATurnBoundary`) is RED at its delivery probe — it pins the contract this commit abolishes, and its own comment says the test and kata must update together when the wake rail closes. `make test` runs `-short` and skips it, so the default gate stays green. Do not "fix" it mid-plan; Task 5 is the fix.

```bash
git add agent/session_client_mutation.go agent/session_queue.go agent/session_stop_and_queued_work_test.go agent/session_lifecycle.go
git commit -m "feat(agent): a Stop parks the queue against both restart rails

Removes the interrupt-tail queued-input wake and its test, and rewrites
the three sibling comments that documented delivery as the ruling: all
of them pinned the interim contract (a Stop delivers what it left
queued) that this change replaces with the parked contract (kata wms7)."
```

---

### Task 3: The session stops calling a parked queue "work"

**Files:**
- Modify: `agent/session_state.go` (`sessionWorkPending`, ~:65)
- Modify: `agent/session_queue.go` (add `pendingQueueDepth` beside `QueueDepth`, ~:492)
- Test: `agent/session_stop_and_queued_work_test.go`

**Interfaces:**
- Consumes: `clientMutationStore.queueHeld()` from Task 1.
- Produces: `func (s *Session) pendingQueueDepth() int`.

- [ ] **Step 1: Write the failing test**

```go
func TestAParkedQueueDoesNotMakeTheSessionLookBusy(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-behind", "and then this")
	// A queue with no Stop IS pending work: something will drain it.
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with a live queue = %q, want %q", got, SessionProcessing)
	}

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Parked, so nothing will drain it, so claiming to be working is a lie --
	// and it is the lie that leaves a Stop button on screen doing nothing.
	if got := sess.WireState(); got == string(SessionProcessing) {
		t.Fatalf("WireState over a parked queue = %q: the composer keeps showing a busy session with a Stop that cannot help", got)
	}
	// The strip must still show the message.
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth = %d, want 1: the queue strip has nothing to show", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./agent/ -run TestAParkedQueueDoesNotMakeTheSessionLookBusy -count=1 -v`
Expected: FAIL — WireState is still "active" after the Stop.

- [ ] **Step 3: Add the narrower depth and use it**

In `agent/session_queue.go`, after `QueueDepth`:

```go
// pendingQueueDepth is QueueDepth for the purpose of "is this session
// working": it reports zero for a queue parked by a Stop, because parked
// messages are not work in progress -- nothing will run them until the user
// asks.
//
// QueueDepth itself still counts them. The messages ARE there and the queue
// strip must keep showing them; what changes is only whether their presence
// makes the session claim to be busy (kata wms7).
func (s *Session) pendingQueueDepth() int {
	if s.clientMutations != nil && s.clientMutations.queueHeld() {
		return 0
	}
	return s.QueueDepth()
}
```

In `sessionWorkPending`, replace `s.QueueDepth() > 0` with `s.pendingQueueDepth() > 0`.

Also in `agent/session_state.go`, fix `WireState`'s doc comment, which this change makes false ("…an idle session with undelivered job notifications or queued input reads as 'active' because parent-owned work can resume it without user input."). Replace that sentence with:

```go
// except for one override: an idle session with undelivered job notifications
// or claimable queued input reads as "active" because work the session owns
// can resume it without user input. A queue parked by a Stop is not claimable
// and reads idle -- nothing will move it until the user acts (kata wms7).
```

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./agent/ -run TestAParkedQueueDoesNotMakeTheSessionLookBusy -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_queue.go agent/session_state.go agent/session_stop_and_queued_work_test.go
git commit -m "feat(agent): a parked queue is not pending work"
```

---

### Task 4: Any user-initiated run releases the hold

**Files:**
- Modify: `agent/session_client_mutation.go` (`AcceptClientMutationStart`'s `executeAtomic` callback)
- Modify: `agent/session_client_mutation_queue.go` (`clientMutationQueue`, `clientMutationDrain`, `clientMutationPromote` callbacks)
- Test: `agent/session_stop_and_queued_work_test.go`

**Interfaces:**
- Consumes: `clientMutationSnapshot.QueueHeld` from Task 1.

- [ ] **Step 1: Write the failing tests**

```go
func TestSendingAgainReleasesTheParkedQueue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(t *testing.T, sess *Session)
	}{
		{"turn/start", func(t *testing.T, sess *Session) {
			if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
				ClientMutationID: "next-turn",
				Input:            []appwire.InputItem{{Type: "text", Text: "carry on"}},
			}); err != nil {
				t.Fatalf("turn/start: %v", err)
			}
		}},
		// A user who queues another message has re-engaged. Without this the
		// hold is sticky: a message queued after the Stop inherits it and is
		// parked forever, with the session reporting idle and nothing saying why.
		{"turn/queue", func(t *testing.T, sess *Session) {
			queueOneMutation(t, sess, "one-more", "and this too")
		}},
		// Draining and promoting are the queue-strip's own "run this now"
		// gestures. Each gets its own table entry because each clear is an
		// independent edit an implementer can forget: without these two cases,
		// reverting either clear leaves the whole suite green while a promoted
		// message strands its siblings parked forever.
		{"turn/drainAsSteer", func(t *testing.T, sess *Session) {
			revision := sess.clientMutations.snapshot().QueueRevision
			if _, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
				ClientMutationID:      "drain-after-stop",
				ExpectedQueueRevision: revision,
			}); err != nil {
				t.Fatalf("turn/drainAsSteer: %v", err)
			}
		}},
		{"turn/promoteQueuedAsSteer", func(t *testing.T, sess *Session) {
			if _, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
				ClientMutationID: "promote-after-stop",
				Index:            0,
			}); err != nil {
				t.Fatalf("turn/promoteQueuedAsSteer: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()

			queueOneMutation(t, sess, "queued-behind", "and then this")
			if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
				ClientMutationID: "stop-over-queued",
			}, func() {}); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if !sess.clientMutations.queueHeld() {
				t.Fatal("the Stop did not park the queue; this test is not in the state it means to be")
			}

			tc.release(t, sess)

			if sess.clientMutations.queueHeld() {
				t.Fatalf("%s left the queue parked: the user asked for work to run and it will not", tc.name)
			}
		})
	}
}

// The clear must sit BELOW every rejection check. executeAtomic commits a
// rejected record, so a clear above them unparks the queue on a request the
// daemon refused -- and the next wake then runs the message the user stopped.
func TestARejectedPromoteLeavesTheQueueParked(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-behind", "and then this")
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// A promote naming an entry id the queue no longer has: the ordinary
	// "the queue changed under you" CAS refusal the token exists for.
	if _, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
		ClientMutationID: "promote-stale",
		Index:            0,
		ExpectedEntryID:  "queue_does_not_exist",
	}); err == nil {
		t.Fatal("the stale promote was accepted; this test needs a refusal to be meaningful")
	}
	if !sess.clientMutations.queueHeld() {
		t.Fatal("a REJECTED promote unparked the queue, so the next wake runs the message the user stopped")
	}
}
```

- [ ] **Step 2: Run them and watch the release table fail**

Run: `go test ./agent/ -run 'TestSendingAgainReleasesTheParkedQueue|TestARejectedPromoteLeavesTheQueueParked' -count=1 -v`
Expected: all four `TestSendingAgainReleasesTheParkedQueue` subtests FAIL — nothing clears the flag yet.

`TestARejectedPromoteLeavesTheQueueParked` PASSES here, and that is correct, not vacuous: with no clear implemented, a rejected promote trivially leaves the flag set. It is a PLACEMENT regression guard, not a TDD driver — the only production change that fails it is a clear written above its rejection checks, which is exactly the mistake it exists to catch. Task 5's mutation testing proves it can fail (move the promote clear above the rejections and run it).

- [ ] **Step 3: Clear it on each user-initiated run, below the rejections**

`AcceptClientMutationStart`, immediately before `reserveClientMutationTurnID(snapshot, record)` (which is already below all four reject checks):

```go
		// The user is speaking again, so the wait a Stop started is over. Their
		// new turn runs first and the drain loop takes the parked messages after
		// it, which is the ordinary queue behaviour they were promised (wms7).
		snapshot.QueueHeld = false
```

`clientMutationQueue`, in its SECOND callback (the commit half, which runs only for an accepted mutation), immediately before `snapshot.InputQueue = append(...)`:

```go
		// Queueing another message is re-engaging: release the wait.
		snapshot.QueueHeld = false
```

`clientMutationDrain` and `clientMutationPromote` each have a SECOND rejection in their second (effect) callback — "reserved queue entries are no longer available" / "reserved queue entry is no longer available" — and `executeAtomic` durably commits the prepare callback's writes before the effect runs. A prepare-phase clear would therefore be committed below the prepare rejections but ABOVE the effect ones. Those effect rejections look unreachable today (reserved entries are protected from removal at every remover), but the constraint is "below every rejection", not "below every rejection we can currently reach" — so both clears go in the effect callback.

`clientMutationDrain`, in its SECOND callback, immediately after the `if !foundAll { ... }` rejection:

```go
		// Draining IS the user asking for the queue to run. Below the effect
		// rejection so a refused drain cannot unpark it.
		snapshot.QueueHeld = false
```

`clientMutationPromote`, in its SECOND callback, immediately after the `if index < 0 { ... }` rejection:

```go
		// Promoting is the user picking one to run now. Below the effect
		// rejection so a refused promote cannot unpark it.
		snapshot.QueueHeld = false
```

- [ ] **Step 4: Run them and watch them pass**

Run: `go test ./agent/ -run 'TestSendingAgainReleasesTheParkedQueue|TestARejectedPromoteLeavesTheQueueParked' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_client_mutation.go agent/session_client_mutation_queue.go agent/session_stop_and_queued_work_test.go
git commit -m "feat(agent): a user-initiated run releases the parked queue"
```

---

### Task 5: Prove it end to end, and prove the tests are not vacuous

**Files:**
- Modify: `cmd/evener-hub/e2e_control_invariant_test.go` (the tail of `TestE2E_ControlInvariantDuringPreTurnWorkAtATurnBoundary`, currently asserting delivery in both directions)

- [ ] **Step 1: Flip the boundary e2e to the parked contract**

The span to replace runs from the `// MEASURED, and it is the state of play rather than the ruling (kata wms7).` comment through the end of the test: the whole two-rails comment block (which documents the wake rail as OPEN — false once Task 2 landed), the 30-second delivery probe, the `round2` assertions and `RespondToolCall`, and the settle `awaitThread`. Replace all of it with:

```go
	// MEASURED, CLOSED, and now the ruling (kata wms7). Both restart rails end
	// at popQueueHead, and it refuses while a Stop holds the queue:
	//
	//   1. The DRAIN LOOP: a turn ended by a Stop leaves the queue head alone --
	//      91810a3be for a turn already producing, the claimed-and-unrun
	//      completion's fence report for one still in pre-turn work.
	//   2. The WAKE: ProcessPendingUserInput claims through popQueueHead, which
	//      refuses while QueueHeld is set.
	//
	// So nothing reaches the model, the message stays on the queue -- visible in
	// the queue strip, waiting for the user -- and the session settles idle.
	//
	// Probed over the whole remaining park plus margin, because the delivery
	// this replaces re-ran the parked command in full -- a shorter probe would
	// pass while it was still in flight.
	probe, cancelProbe := context.WithTimeout(ctx, time.Duration(preTurnParkSeconds+2)*time.Second)
	defer cancelProbe()
	if call, err := provider.Next(probe.Done()); err == nil {
		carries := call.Contains(queuedText)
		call.RespondText("this turn should never have run")
		t.Fatalf("a model round followed the Stop (carries the queued text: %v): a restart rail is still open", carries)
	}
	awaitThread(ctx, t, client, ref, "the stopped session to settle with its message parked", func(thread appwire.Thread) bool {
		return thread.Status.Type != string(appwire.ThreadStatusActive) && thread.Serf.Queue.Depth == 1
	})
	t.Logf("the Stop cancelled its turn and parked the queued message (wms7)")
```

- [ ] **Step 2: Run the four live control e2es**

Run: `go test ./cmd/evener-hub/ -run 'TestE2E_ControlInvariant|TestE2E_StopIsOffered|TestE2E_PushedActive' -count=1 -v`
Expected: all four PASS. If the boundary one still sees a model round, a third rail exists — measure it before changing anything, the way the two known rails were found.

- [ ] **Step 3: Mutation-test every new assertion**

For each of the SEVEN production edits — the `QueueHeld = true` set, the `popQueueHead` guard, the `pendingQueueDepth` swap in `sessionWorkPending`, and EACH of the four clears (turn/start, turn/queue, drain, promote) — revert it, run the suite below, and confirm a test fails. The clears get individual reverts because each is an edit an implementer can forget independently, and the first draft of this plan could not catch a missing drain or promote clear at all. Then one placement mutation: move the promote clear ABOVE its rejection checks and confirm `TestARejectedPromoteLeavesTheQueueParked` fails — that is the only mutation that test exists to catch, and this is where it proves it can. A test that stays green with its subject removed is guarding nothing — two tests in this exact area were found to be that way earlier in this kata.

Run per mutation: `go test ./agent/ -run 'TestStop|TestQueue|TestAParked|TestSendingAgain|TestARejected' -count=1`
Expected: at least one FAIL per mutation. Record which test catches which edit in the commit message.

- [ ] **Step 4: Full gates**

Run: `make test` then `make lint`
Expected: green, except `TestForkedDescendantIsReaped` (kata `q0gj`, pre-existing, load-sensitive, untouched by this work).

- [ ] **Step 5: Commit and update the kata**

```bash
git add cmd/evener-hub/e2e_control_invariant_test.go
git commit -m "test(turn-control): the boundary Stop parks its message end to end"
```

Then comment on `wms7` with: which rails are closed, the mutation-test results from Step 3, that steering remains open as `1k3m`, and these two DELIBERATE deferrals so they are ruled on rather than forgotten:

1. `armAwaitingAtSettle`/`settleTerminalState` (`agent/session_state.go` ~:298-344) still read raw `QueueDepth`, so a turn that settles cleanly over a parked queue rests **idle** rather than **awaiting**. RULED by Jesse (2026-08-17, during plan review): idle, as planned. Recorded so the tier question does not resurface as a bug report.
2. MaxTurns: parked entries keep their `BudgetReservations` slots, and at the cap both `turn/start` and `turn/queue` reject on budget ABOVE the clears (correctly, per the ordering rule). A user at the cap who Stops can still release via promote, drain, or cancelling entries — but cannot type a fresh message. Corner case with escape hatches; recorded, not solved.

---

## Self-Review

**Spec coverage.** `wms7`'s ruling for queued messages: cancel, do not auto-start, do not lose. Task 2 stops both rails, Task 3 makes the resulting idle state honest, Task 4 gives the user the way out, Task 5 proves it live. Steering is explicitly deferred to `1k3m` in Global Constraints.

**The interim contract this replaces, retired explicitly (found on plan review):** `InterruptClientMutation`'s applied path carried `wakeForPendingQueuedInput()` under a comment promising "popQueueHead will claim again" — the deliberate interim behaviour (a Stop delivers what it left queued) that this plan reverses. Task 2 Steps 6–8 remove the wake, rewrite the comment, and delete `TestStopWakesTheSessionForWorkItLeftQueued`, the test that pinned it. Left alone, they would have been dead code under a false comment plus a failing test at Task 5's full gates.

**Review findings from the rejected attempt, each mapped:** mirror drift → no mirror (Global Constraints, Task 1's accessor). Clear above rejections → Task 4 Step 3 ordering plus `TestARejectedPromoteLeavesTheQueueParked`. Sticky hold → `turn/queue` clear in Task 4. Doctor declared-but-unrendered → Task 1 Step 5 renders it. Vacuous tests → Task 5 Step 3 mutation-tests every edit.

**Adversarial review round (two independent reviewers, 2026-08-17), each finding mapped:** false red-phase claim on the rejected-promote test → Task 4 Step 2 now labels it a placement regression guard, proven able to fail in Task 5 Step 3. Drain/promote clears unguarded by any test → two new cases in the release table plus per-clear mutation testing. Sibling comments pinning the dead delivery contract (two test doc comments, the drain-loop comment) → Task 2 Step 8 sweep. `WireState` doc comment made false by Task 3 → fixed in Task 3 Step 3. Drain/promote clears above their effect-phase rejections → moved into the effect callbacks. Doctor render unguarded → fixture + want-line in Task 1 Step 5. Live boundary e2e knowingly red between Task 2 and Task 5 → acknowledged in Task 2 Step 9 (Jesse waived commit-ordering concerns; end state is the bar). `ProcessPendingUserInput`'s first return is output text, not ran-a-turn → Task 2's test asserts the bool.

**Deferred with reasons, recorded on the kata in Task 5 Step 5:** the `armAwaitingAtSettle`/`settleTerminalState` raw `QueueDepth` reads (idle-vs-awaiting tier over a parked queue; may be intended, Jesse rules), and the MaxTurns corner (budget rejections above the clears lock out `turn/start`/`turn/queue` at the cap; promote/drain/cancel remain open).

**Not covered here, deliberately:** REST `/status` computes `Steer` differently from `appCapabilities` (found in the same review). Unrelated to parking; belongs in its own change.

**Type consistency:** `queueHeld()` (store method, Task 1) and `pendingQueueDepth()` (Session method, Task 3) are the only new names, used consistently in Tasks 2–4.
