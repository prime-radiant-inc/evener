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
		fmt.Fprintf(&b, "queue:          held (parked by a Stop; waiting on the user)\n")
	}
```

- [ ] **Step 6: Run the doctor drift guard and commit**

Run: `go test ./agent/ ./agent/doctor/ -run 'TestQueueHeld|Doctor|TestClientMutationSnapshot' -count=1`
Expected: PASS.

```bash
git add agent/session_client_mutation.go agent/session_stop_and_queued_work_test.go agent/doctor/mutations.go
git commit -m "feat(agent): a durable parked-queue flag, read without cloning"
```

---

### Task 2: A Stop sets the hold; popQueueHead refuses while it is set

**Files:**
- Modify: `agent/session_client_mutation.go` (`InterruptClientMutation`'s `reservePrepared` callback, where `InterruptFence` is written)
- Modify: `agent/session_queue.go` (`popQueueHead`, beside its existing `InterruptFence` guard)
- Test: `agent/session_stop_and_queued_work_test.go`

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
	if ran, _, err := sess.ProcessPendingUserInput(context.Background(), nil); err != nil || ran != "" {
		t.Fatalf("ProcessPendingUserInput ran work after a Stop: ran=%q err=%v", ran, err)
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

- [ ] **Step 6: Commit**

```bash
git add agent/session_client_mutation.go agent/session_queue.go agent/session_stop_and_queued_work_test.go
git commit -m "feat(agent): a Stop parks the queue against both restart rails"
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

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./agent/ -run 'TestSendingAgainReleasesTheParkedQueue|TestARejectedPromoteLeavesTheQueueParked' -count=1 -v`
Expected: both FAIL — nothing clears the flag yet.

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

`clientMutationDrain`, AFTER the `queue is empty` check and every other rejection, immediately before the accepted path's first mutation of the snapshot:

```go
		// Draining IS the user asking for it to run.
		snapshot.QueueHeld = false
```

`clientMutationPromote`, AFTER the index-range, `ExpectedEntryID` and reserved-entry rejections:

```go
		// Promoting is the user picking one to run now.
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
- Modify: `cmd/serf-hub/e2e_control_invariant_test.go` (the tail of `TestE2E_ControlInvariantDuringPreTurnWorkAtATurnBoundary`, currently asserting delivery in both directions)

- [ ] **Step 1: Flip the boundary e2e to the parked contract**

Replace the delivery tail with:

```go
	// Both restart rails are closed now: the drain loop (91810a3be plus the
	// claimed-branch fence report) and the wake, via popQueueHead refusing while
	// the queue is parked. So nothing reaches the model, the message is still on
	// the queue for the user to send, and the session settles.
	//
	// Probed over the whole remaining park plus margin, because the restart this
	// replaces re-ran the parked command in full -- a shorter probe would pass
	// while it was still in flight.
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

Run: `go test ./cmd/serf-hub/ -run 'TestE2E_ControlInvariant|TestE2E_StopIsOffered|TestE2E_PushedActive' -count=1 -v`
Expected: all four PASS. If the boundary one still sees a model round, a third rail exists — measure it before changing anything, the way the two known rails were found.

- [ ] **Step 3: Mutation-test every new assertion**

For each of the four production edits (the `popQueueHead` guard, the `QueueHeld = true` set, the `pendingQueueDepth` swap in `sessionWorkPending`, and one of the clears), revert it, run the agent suite, and confirm a test fails. A test that stays green with its subject removed is guarding nothing — two tests in this exact area were found to be that way earlier in this kata.

Run per mutation: `go test ./agent/ -run 'TestStop|TestQueue|TestAParked|TestSendingAgain|TestARejected' -count=1`
Expected: at least one FAIL per reverted edit. Record which test catches which edit in the commit message.

- [ ] **Step 4: Full gates**

Run: `make test` then `make lint`
Expected: green, except `TestForkedDescendantIsReaped` (kata `q0gj`, pre-existing, load-sensitive, untouched by this work).

- [ ] **Step 5: Commit and update the kata**

```bash
git add cmd/serf-hub/e2e_control_invariant_test.go
git commit -m "test(turn-control): the boundary Stop parks its message end to end"
```

Then comment on `wms7` with: which rails are closed, the mutation-test results from Step 3, and that steering remains open as `1k3m`.

---

## Self-Review

**Spec coverage.** `wms7`'s ruling for queued messages: cancel, do not auto-start, do not lose. Task 2 stops both rails, Task 3 makes the resulting idle state honest, Task 4 gives the user the way out, Task 5 proves it live. Steering is explicitly deferred to `1k3m` in Global Constraints.

**Review findings from the rejected attempt, each mapped:** mirror drift → no mirror (Global Constraints, Task 1's accessor). Clear above rejections → Task 4 Step 3 ordering plus `TestARejectedPromoteLeavesTheQueueParked`. Sticky hold → `turn/queue` clear in Task 4. Doctor declared-but-unrendered → Task 1 Step 5 renders it. Vacuous tests → Task 5 Step 3 mutation-tests every edit.

**Not covered here, deliberately:** REST `/status` computes `Steer` differently from `appCapabilities` (found in the same review). Unrelated to parking; belongs in its own change.

**Type consistency:** `queueHeld()` (store method, Task 1) and `pendingQueueDepth()` (Session method, Task 3) are the only new names, used consistently in Tasks 2–4.
