package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
)

// Slice-1 projection + interim judge tests (Task 5, spec §§7 wire, 9 slice-1
// scope + interim rule). TDD red phase: these fail until GoalStateData carries
// the wait list, EventGoalWaiting/EventGoalResumed exist and are emitted, the
// autonomy treatment covers timer-parked goals, and the interim judge keeps the
// v1 3/6 breaker armed for non-parked loops while wait-attributable turns
// bypass RecordContinuation.

// TestGoalSeedDataV1NilBudgetsDoesNotPanic is the fix-1/4 regression test: a
// v1 (budgetless) snapshot — Budgets nilable by design
// (schema/snapshot.go) — must seed without panicking, yielding
// Used=Iterations and Max=0 (mirroring goalStateFromMeta), never a silent
// zero-max presented as a real cap.
func TestGoalSeedDataV1NilBudgetsDoesNotPanic(t *testing.T) {
	t.Parallel()
	v1 := &schema.GoalSnapshot{
		Objective:  "v1 goal",
		Status:     "active",
		Iterations: 4,
		Budgets:    nil,
	}
	var data *events.GoalStateData
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("goalSeedData panicked on nil Budgets: %v", r)
			}
		}()
		data = goalSeedData(v1)
	}()
	if data.UsedContinuations != 4 || data.MaxContinuations != 0 {
		t.Fatalf("progress = %d/%d, want 4/0 (used=iterations, max unset)", data.UsedContinuations, data.MaxContinuations)
	}
	if data.Objective != "v1 goal" || data.Status != "active" || data.Iterations != 4 {
		t.Fatalf("seed = %+v, want the v1 objective/status/iterations", data)
	}
}

// drainGoalEvents discards buffered session events so the next read observes
// only events emitted after this point.
func drainGoalEvents(sess *Session) {
	for {
		select {
		case _, ok := <-sess.Events():
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// nextGoalEventOfKind blocks (bounded) for the next event of the named kind,
// skipping unrelated events.
func nextGoalEventOfKind(t *testing.T, sess *Session, kind events.EventKind) events.SessionEvent {
	t.Helper()
	// TRIPWIRE: mutations emit synchronously in-process; the bound only fires
	// if the emission path wedges (cf. nextGoalUpdated's 1s tripwire).
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("session event stream closed before %s", kind)
			}
			if ev.Kind != kind {
				continue
			}
			return ev
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", kind)
			return events.SessionEvent{}
		}
	}
}

// TestGoalStateDataCarriesWaitList pins the §7 wire gap (M2): the GoalUpdated
// payload must carry the live wait list (labels + deadlines only — never full
// predicate payloads) so the wire projection can summarize waiting state.
func TestGoalStateDataCarriesWaitList(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("wire the waits", clk.Now())
	drainGoalEvents(sess)
	deadline := clk.Now().Add(time.Hour)
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "timer-one"}, clk.Now())
	if !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}
	// The store-level RegisterWait used here emits nothing; build the payload
	// from the full-shape read exactly as the emission path does.
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal should still be set")
	}
	data := goalStateDataFromFull(full)
	if len(data.WaitingOn) != 1 {
		t.Fatalf("WaitingOn = %+v, want exactly the one live wait", data.WaitingOn)
	}
	got := data.WaitingOn[0]
	if got.WaitID != w.Lease.WaitID || got.Label != "timer-one" {
		t.Fatalf("WaitingOn[0] = %+v, want wait_id %q label %q", got, w.Lease.WaitID, "timer-one")
	}
	if got.DeadlineUnixMilli != deadline.UnixMilli() {
		t.Fatalf("WaitingOn[0].DeadlineUnixMilli = %d, want %d", got.DeadlineUnixMilli, deadline.UnixMilli())
	}
	if data.NearestDeadlineUnixMilli != deadline.UnixMilli() || data.NearestLabel != "timer-one" {
		t.Fatalf("nearest = (%q, %d), want (timer-one, %d)", data.NearestLabel, data.NearestDeadlineUnixMilli, deadline.UnixMilli())
	}
	if data.UsedContinuations != 0 || data.MaxContinuations != goal.DefaultMaxContinuations {
		t.Fatalf("progress = %d/%d, want 0/%d", data.UsedContinuations, data.MaxContinuations, goal.DefaultMaxContinuations)
	}
}

// TestGoalWaitNearestDeadlineTieBreaksOnWaitID pins the §6 chip rule: nearest =
// earliest deadline, tie → smallest wait_id.
func TestGoalWaitNearestDeadlineTieBreaksOnWaitID(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("tie deadlines", clk.Now())
	now := clk.Now()
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "first"}, now); !ok {
		t.Fatal("precondition: first registration should succeed")
	}
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "second", Target: "other"}, now); !ok {
		t.Fatal("precondition: second registration should succeed")
	}
	// Both deadlines are now+1h (same FakeClock instant). The payload must
	// resolve the nearest to the smaller wait_id.
	full, ok := store.GoalSnapshot()
	if !ok {
		t.Fatal("precondition: goal should still be set")
	}
	data := goalStateDataFromFull(full)
	if len(data.WaitingOn) != 2 {
		t.Fatalf("WaitingOn = %+v, want both waits", data.WaitingOn)
	}
	if data.NearestLabel != "first" {
		t.Fatalf("NearestLabel = %q, want first (wait_1 wins the tie)", data.NearestLabel)
	}
	// Earliest-deadline ordering beats registration order: an earlier
	// deadline registered second still wins.
	store2 := goal.NewStore()
	store2.Set("earliest wins", now)
	if _, ok := store2.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour, Label: "later"}, now); !ok {
		t.Fatal("precondition: later registration should succeed")
	}
	// A different target avoids the same-target replace rule.
	if _, ok := store2.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "earlier", Target: "other"}, now); !ok {
		t.Fatal("precondition: earlier registration should succeed")
	}
	full2, _ := store2.GoalSnapshot()
	data2 := goalStateDataFromFull(full2)
	if data2.NearestLabel != "earlier" {
		t.Fatalf("NearestLabel = %q, want earlier (earliest deadline wins)", data2.NearestLabel)
	}
}

// TestGoalWaitingEventEmittedOnPark pins §7: parking on a wait emits
// EventGoalWaiting (the announcement channel for the park), carrying the wait
// count and nearest label.
func TestGoalWaitingEventEmittedOnPark(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("announce the park", clk.Now())
	drainGoalEvents(sess)
	// Park through the session path (not the bare store): it owns the
	// GOAL_UPDATED + GOAL_WAITING emission pair.
	if _, ok := sess.registerGoalWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour, Label: "timer-one"}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWaiting)
	d, ok := ev.Data.(events.GoalWaitingData)
	if !ok {
		t.Fatalf("GOAL_WAITING payload = %T, want events.GoalWaitingData", ev.Data)
	}
	if d.Count != 1 || d.NearestLabel != "timer-one" {
		t.Fatalf("GoalWaitingData = %+v, want count 1 nearest timer-one", d)
	}
}

// TestGoalResumedEventEmittedOnWake pins §7: the wake turn's delivery emits
// EventGoalResumed carrying the fired wait_ids.
func TestGoalResumedEventEmittedOnWake(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("announce the wake", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute, Label: "timer-one"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)
	clk.Advance(2 * time.Minute)
	prompt, cont := sess.armGoalContinuation(false, true)
	if !cont || prompt == "" {
		t.Fatal("precondition: expired gate must drive the wake turn")
	}
	ev := nextGoalEventOfKind(t, sess, events.EventGoalResumed)
	d, ok := ev.Data.(events.GoalResumedData)
	if !ok {
		t.Fatalf("GOAL_RESUMED payload = %T, want events.GoalResumedData", ev.Data)
	}
	if len(d.WaitIDs) != 1 || d.WaitIDs[0] != w.Lease.WaitID {
		t.Fatalf("GoalResumedData = %+v, want the fired wait_id", d)
	}
}

// TestGoalResumedNotEmittedOnSupersededNoop is the fix-1/4 finding-2 pin: a
// retarget-after-claim no-op evaluation drives (stale trigger as dropped
// context) but must NOT announce "Goal resumed: <wait_id>" — the payload
// carries only WaitIDs, so the announcement would be identical to a real
// wake while the kick prompt frames the trigger as superseded.
//
// The seam is the timer callback's retarget-wins branch
// (kickClaimedGoalWake's full.Objective != objective path, extracted from
// fireGoalWaitTimer — the cited :936): the gate's own superseded branch
// returns a prompt without kicking and never announced. The test drives the
// extracted helper with the claim-time objective of the old goal while the
// store already owns the new one.
func TestGoalResumedNotEmittedOnSupersededNoop(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	var prompts []string
	sess.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	claimed, objective, _ := sess.claimGoalWaitExpiredWaits(clk.Now())
	if len(claimed) == 0 {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	// Retarget between claim and kick: Set carries the batch marked
	// Superseded onto the new objective — exactly the interleaving the
	// timer callback's re-read observes.
	store.Set("new objective", clk.Now())
	drainGoalEvents(sess)
	sess.kickClaimedGoalWake(claimed, objective)
	if len(prompts) != 1 {
		t.Fatalf("kicks = %d, want exactly the superseded no-op kick", len(prompts))
	}
	for _, ev := range drainGoalEventsToList(sess) {
		if ev.Kind == events.EventGoalResumed {
			t.Fatalf("superseded no-op emitted GOAL_RESUMED %+v: a dropped trigger must not announce a resume", ev.Data)
		}
	}
}

// drainGoalEventsToList drains buffered session events and returns them.
func drainGoalEventsToList(sess *Session) []events.SessionEvent {
	var out []events.SessionEvent
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestTimerParkedGoalIsAutonomyInFlight pins §7: a goal parked on a timer wait
// (nothing for the user to answer) reads as autonomy-in-flight — never "needs
// you" with nothing to answer.
//
// The pin is on autonomyInFlight (the settle/restore driver): a parked goal
// owns a live wait whose coalesced timer will kick the wake turn without user
// input. Like the live-child precedent
// (TestWireState_LiveChildDoesNotMakeIdleParentActive), autonomy does not
// itself upgrade WireState: the needs-you tier keys on awaiting/warning, so a
// parked goal resting idle never lands there (never "needs you" with nothing
// to answer) while the wake path still re-arms through the timer.
func TestTimerParkedGoalIsAutonomyInFlight(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	store := sess.getOrCreateGoalStore()
	store.Set("parked but moving", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	// The store-level registration above does not arm the coalesced timer
	// (that is the session path's job); arm it as the gate would on park.
	sess.armGoalWaitTimer()
	if !sess.autonomyInFlight() {
		t.Fatal("a timer-parked goal must read as autonomy-in-flight")
	}
	// Disarming (cancel the last live lease, as the cancel path does) clears
	// the treatment: with no live waits nothing will move the session.
	full, _ := store.GoalSnapshot()
	for _, w := range full.Waits {
		if !sess.CancelGoalWait(w.Lease.WaitID) {
			t.Fatalf("precondition: cancel of %s should succeed", w.Lease.WaitID)
		}
	}
	if sess.autonomyInFlight() {
		t.Fatal("a goal with no live waits must not read as autonomy-in-flight")
	}
}

// Slice-2 retirement note: the interim v1 judge (3/6 mutation breaker +
// wait-attributable bypass) is REPLACED by the ledger fold in slice 2 — the
// Task-7 brief forbids leaving both judges armed. The retired pins were
// TestInterimJudgeStaysArmedForNonParkedLoops (v1 breaker armed) and
// TestInterimWakeTurnsBypassRecordContinuation (wake/tail/superseded bypass
// the fold). Their replacements: TestGoalLedgerFullTurnStallGraduates
// (ledger nudge→block end to end), TestGoalWakeTurnFoldsLedgerAndAccruesBudget
// (wake drive folds + accrues), and the superseded zero-fold pins inside
// TestGateRetargetBetweenClaimAndKickDrivesSupersededNoop /
// TestGateSupersededThreeGateSequenceNoHang (session_goal_wait_test.go).
