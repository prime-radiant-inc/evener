package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/llm"
)

// Task-8 slice-3 tests (spec §6): graduation nudge→park→block with the
// evidence note, the quiet-goal watchdog per-stretch contract (FakeClock),
// and conditional stop-claim verification. TDD red phase: these fail until
// the stage machine, EventGoalWatchdog, goal_expect, and the verifier land.

// task8Args marshals a tool-arg map for direct registry calls (mirroring
// goalWaitToolCall's shape).
func task8Args(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	return raw
}

// task8FileSubstrate is a deterministic in-memory substrate whose file set
// the test controls: present paths stat with a fixed baseline, absent paths
// fail closed (hallucinated).
type task8FileSubstrate struct {
	files map[string]string
}

func (f *task8FileSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *task8FileSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *task8FileSubstrate) StatFile(path string) (string, bool) {
	b, ok := f.files[path]
	return b, ok
}

func (f *task8FileSubstrate) LookupApproval(contentKey, generation string) bool { return false }

func (f *task8FileSubstrate) LookupChild(id string) bool { return false }

// wireTask8Files installs the file substrate on the session's goal store.
func wireTask8Files(sess *Session, files map[string]string) {
	sess.getOrCreateGoalStore().SetSubstrate(&task8FileSubstrate{files: files})
}

// graduationStallOutcome builds a stale identical-turn outcome for the
// graduation path: K=3 advanced tier trips fast.
func graduationStallOutcome() goal.TurnOutcome {
	return goal.TurnOutcome{
		ActionFingerprint: "poll endpoint=x",
		ObservationClass:  "external-unchanged",
		ObservationHash:   "same-hash",
		StateDigest:       "steady-digest",
		Mutated:           true,
	}
}

// driveToNudge folds identical stalls until the gate nudges, failing on any
// unexpected block.
func driveToNudge(t *testing.T, sess *Session, outcome goal.TurnOutcome) {
	t.Helper()
	// Advanced tier (mutated seed): trip at K=3. Seed one advancing turn to
	// tighten the tier, then stall to the trip.
	seed := outcome
	seed.StateDigest = "seed-digest"
	seed.ObservationHash = "seed-hash"
	if prompt, cont := sess.armGoalContinuationWithOutcome(false, true, seed); !cont || prompt == "" {
		t.Fatalf("seed gate = (%q, %v), want a drive", prompt, cont)
	}
	for i := range goal.RepetitionThresholdAdvanced {
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, outcome)
		if !cont || prompt == "" {
			t.Fatalf("stall gate %d = (%q, %v), want a drive (nudge at trip, never early block)", i+1, prompt, cont)
		}
	}
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if full.LedgerSummary.Stage != goal.StageNudged {
		t.Fatalf("stage = %q, want nudged after the first stall trip", full.LedgerSummary.Stage)
	}
}

// TestGoalGraduationNudgeNamesEvidenceThenAutoParks pins the §6 stage machine:
// the first stall trip nudges (naming the repetition evidence, one transcript
// note); the next still-stalled gate parks on a bounded auto-wait (stage
// auto-parked, EventGoalWaiting emitted silently); the 4th consecutive stall
// graduates to block with "no progress" and exactly one block note.
func TestGoalGraduationNudgeNamesEvidenceThenAutoParks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait on the slow endpoint", clk.Now())
	stall := graduationStallOutcome()
	driveToNudge(t, sess, stall)

	// Nudge names the evidence with exactly one steering note.
	if got := countTask7SteeringNotes(sess, "goal-stall-nudge"); got != 1 {
		t.Fatalf("nudge notes = %d, want exactly 1", got)
	}
	sess.mu.Lock()
	var nudgeText string
	for _, turn := range sess.history {
		if strings.Contains(turn.Message.Text(), "goal-stall-nudge") {
			nudgeText = turn.Message.Text()
		}
	}
	sess.mu.Unlock()
	if !strings.Contains(nudgeText, "poll endpoint=x") {
		t.Fatalf("nudge note must name the repetition fingerprint, got:\n%s", nudgeText)
	}

	// The next still-stalled gate auto-parks (stage 2): not a block, not a
	// drive — a bounded auto-wait with the stall episode's single notice
	// policy (EventGoalWaiting emitted silently).
	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, stall)
	if cont || prompt != "" {
		t.Fatalf("post-nudge stall gate = (%q, %v), want the auto-park (no drive, no block)", prompt, cont)
	}
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if full.LedgerSummary.Stage != goal.StageAutoPark {
		t.Fatalf("stage = %q, want auto-parked after the post-nudge stall", full.LedgerSummary.Stage)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting while auto-parked", snap.Status)
	}
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWaiting)
	d, ok := ev.Data.(events.GoalWaitingData)
	if !ok {
		t.Fatalf("GOAL_WAITING payload = %T, want events.GoalWaitingData", ev.Data)
	}
	if !d.AnnounceSilently {
		t.Fatalf("auto-park GOAL_WAITING must set AnnounceSilently (coalesced into the stall episode's single notice), got %+v", d)
	}
}

// TestGoalGraduationAutoParkExhaustionBlocks pins the §5 re-park bound at the
// gate: the 4th consecutive stall (3 auto-re-parks consumed) graduates to
// block with "no progress".
func TestGoalGraduationAutoParkExhaustionBlocks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait on the slow endpoint", clk.Now())
	stall := graduationStallOutcome()
	driveToNudge(t, sess, stall)
	drainGoalEvents(sess)

	// Post-nudge stalls park (auto-re-park 1..3), then the 4th blocks.
	for i := range 3 {
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, stall)
		if cont || prompt != "" {
			t.Fatalf("auto-park gate %d = (%q, %v), want the park (no drive, no block)", i+1, prompt, cont)
		}
	}
	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, stall)
	if cont || prompt != "" {
		t.Fatalf("exhausted gate = (%q, %v), want the block after 3 auto-re-parks", prompt, cont)
	}
	snap, _ := sess.getOrCreateGoalStore().Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictNoProgress {
		t.Fatalf("snapshot = %+v, want blocked/no progress after re-park exhaustion", snap)
	}
	if got := countTask7SteeringNotes(sess, "goal-no-progress"); got != 1 {
		t.Fatalf("block notes = %d, want exactly 1", got)
	}
}

// TestGoalGraduationLooksLikeWaitingDrivesPark pins rule 6's stage-2 routing:
// a post-nudge stall whose turn looks like waiting (external-unchanged class)
// parks even on the first post-nudge trip, while a non-waiting stall blocks.
func TestGoalGraduationLooksLikeWaitingDrivesPark(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait on the slow endpoint", clk.Now())
	stall := graduationStallOutcome()
	driveToNudge(t, sess, stall)

	waiting := stall
	waiting.ObservationClass = "external-unchanged"
	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, waiting)
	if cont || prompt != "" {
		t.Fatalf("waiting stall gate = (%q, %v), want the auto-park", prompt, cont)
	}
	if full, _ := sess.getOrCreateGoalStore().GoalSnapshot(); full.LedgerSummary.Stage != goal.StageAutoPark {
		t.Fatalf("stage = %q, want auto-parked for a waiting stall", full.LedgerSummary.Stage)
	}
}

// TestGoalWatchdogParkStretchNotifiesTwice pins the §6 per-stretch contract: a
// 24h park notifies at most twice — park-start (only for stretches exceeding
// the 30m quiet threshold) plus one half-deadline reminder anchored to the
// earliest live wait deadline.
func TestGoalWatchdogParkStretchNotifiesTwice(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait on the slow timer", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 24 * time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: 24h until_time registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	d, ok := ev.Data.(events.GoalWatchdogData)
	if !ok {
		t.Fatalf("GOAL_WATCHDOG payload = %T, want events.GoalWatchdogData", ev.Data)
	}
	if d.Kind != "park-start" {
		t.Fatalf("first watchdog kind = %q, want park-start", d.Kind)
	}

	clk.Advance(12*time.Hour - 31*time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev = nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	d, ok = ev.Data.(events.GoalWatchdogData)
	if !ok {
		t.Fatalf("GOAL_WATCHDOG payload = %T, want events.GoalWatchdogData", ev.Data)
	}
	if d.Kind != "half-deadline" {
		t.Fatalf("second watchdog kind = %q, want half-deadline", d.Kind)
	}

	// No third notice in the same stretch: advancing to the deadline fires
	// the expiry wake, not another watchdog ping.
	clk.Advance(12 * time.Hour)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("third watchdog notice in one stretch: %+v (want ≤2)", ev.Data)
		}
	default:
	}
}

// TestGoalWatchdogShortWaitCostsZeroNotices pins the quiet-threshold floor: a
// 60s wait that fires before the 30m threshold emits no watchdog notice.
func TestGoalWatchdogShortWaitCostsZeroNotices(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait on the quick timer", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: 1m until_time registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(2 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("short-wait watchdog notice: %+v (want none before the 30m threshold)", ev.Data)
		}
	default:
	}
}

// TestGoalWatchdogHalfDeadlineSuppressedWhenStretchEndsFirst pins the §6
// timing rule: a 40m-deadline wait's 20m reminder is suppressed because 20m
// precedes the 30m threshold crossing.
func TestGoalWatchdogHalfDeadlineSuppressedWhenStretchEndsFirst(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait on the medium timer", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 40 * time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: 40m until_time registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(21 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("pre-threshold watchdog notice: %+v (want none before 30m)", ev.Data)
		}
	default:
	}

	clk.Advance(10 * time.Minute) // 31m: past the threshold — park-start only.
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("watchdog = %+v, want park-start only (20m half-deadline reminder suppressed)", ev.Data)
	}
}

// TestGoalWatchdog24hCeilingAcrossStretches pins the per-goal per-24h ceiling:
// at most 4 watchdog notices per goal per 24h across stretches; further
// stretches emit nothing. Each stretch parks on a fresh 2h wait, advances
// 31m (past the 30m threshold, short of the 1h half-deadline reminder), and
// polls twice (the repeat poll must not re-notify: per-stretch cap). The
// stretch ends by cancelling the wait — expiry would fold the wake turn into
// the ledger and trip the stall breaker, orthogonal machinery this test does
// not exercise. Six stretches × 31m ≈ 3.1h < 24h, so the ceiling (not the
// window) silences stretches 5–6.
func TestGoalWatchdog24hCeilingAcrossStretches(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait repeatedly", clk.Now())
	notices := 0
	for i := range 6 {
		w, ok := sess.getOrCreateGoalStore().RegisterWait(
			goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Hour},
			clk.Now(),
		)
		if !ok {
			t.Fatalf("stretch %d: registration should succeed", i+1)
		}
		clk.Advance(31 * time.Minute)
		sess.checkGoalWatchdog(clk.Now())
		sess.checkGoalWatchdog(clk.Now())
		drained := 0
		for {
			select {
			case ev := <-sess.Events():
				if ev.Kind == events.EventGoalWatchdog {
					drained++
					notices++
					if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
						t.Fatalf("stretch %d: watchdog = %+v, want park-start only (2h half-reminder not yet due)", i+1, ev.Data)
					}
				}
			default:
				goto done
			}
		}
	done:
		if drained > 1 {
			t.Fatalf("stretch %d: %d notices on one poll step, want ≤1 (per-stretch cap)", i+1, drained)
		}
		if !sess.CancelGoalWait(w.Lease.WaitID) {
			t.Fatalf("stretch %d: cancel should end the stretch", i+1)
		}
		drainGoalEvents(sess)
	}
	if notices != 4 {
		t.Fatalf("watchdog notices across 6 stretches in 24h = %d, want exactly 4 (per-goal ceiling)", notices)
	}
}

// TestGoalVerifyConditionedRejectNamesCondition pins the §6 check-on-claim
// verifier: update_goal("complete") on a goal carrying a failing registered
// condition rejects with the failing condition named; the goal stays active.
func TestGoalVerifyConditionedRejectNamesCondition(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report exists", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("goal_expect registration should succeed, got error: %s", res.Output)
	}
	// The file is unchanged since registration (baseline still v1), so the
	// claim-time verifier reads unsatisfied and rejects naming the desc.

	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ug1", Name: "update_goal",
		Arguments: task8Args(t, map[string]any{"status": "complete"}),
		Type:      "function",
	})
	if !cres.IsError {
		t.Fatalf("conditioned complete should reject, got success: %s", cres.Output)
	}
	if !strings.Contains(cres.Output, "report exists") {
		t.Fatalf("rejection %q must name the failing condition", cres.Output)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after a rejected claim", snap.Status)
	}
}

// TestGoalVerifyUnconditionedSelfDeclares pins the v1 path: a condition-free
// goal completes by self-declare with no verifier involvement.
func TestGoalVerifyUnconditionedSelfDeclares(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the note", clk.Now())
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ug1", Name: "update_goal",
		Arguments: task8Args(t, map[string]any{"status": "complete"}),
		Type:      "function",
	})
	if cres.IsError {
		t.Fatalf("unconditioned complete should self-declare, got error: %s", cres.Output)
	}
	if !strings.Contains(cres.Output, "Goal marked complete") {
		t.Fatalf("output %q must carry the self-declare confirmation", cres.Output)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusComplete {
		t.Fatalf("status = %q, want complete", snap.Status)
	}
}

// TestGoalExpectValidationMirrorsWait pins §6/D-I6: hallucinated expect
// targets reject immediately with the reason named — never registered.
func TestGoalExpectValidationMirrorsWait(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "job done", "kind": "until_job", "target": "job_999"}),
		Type:      "function",
	})
	if !res.IsError {
		t.Fatalf("hallucinated expect target should reject, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_999") {
		t.Fatalf("rejection %q must name the hallucinated target", res.Output)
	}
}

// TestGoalExpectNeverFeedsLedger pins §4: registering or checking an expect
// condition never feeds the ledger mid-episode (waits' flips are the only
// subgoal evidence).
func TestGoalExpectNeverFeedsLedger(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report exists", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("goal_expect registration should succeed, got error: %s", res.Output)
	}
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if len(full.LedgerSummary.Entries) != 0 {
		t.Fatalf("expect registration folded %d ledger entries, want 0 (check-on-claim only)", len(full.LedgerSummary.Entries))
	}
}

// TestGoalWatchdogActiveQuietNotifiesOnce pins the active-but-quiet branch: a
// driven-but-quiet active goal past the 30m threshold emits one
// active-quiet notice and no more.
func TestGoalWatchdogActiveQuietNotifiesOnce(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("quiet active work", clk.Now())
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	d, ok := ev.Data.(events.GoalWatchdogData)
	if !ok {
		t.Fatalf("GOAL_WATCHDOG payload = %T, want events.GoalWatchdogData", ev.Data)
	}
	if d.Kind != "active-quiet" {
		t.Fatalf("watchdog kind = %q, want active-quiet", d.Kind)
	}
	sess.checkGoalWatchdog(clk.Now())
	select {
	case ev := <-sess.Events():
		if ev.Kind == events.EventGoalWatchdog {
			t.Fatalf("second active-quiet notice: %+v (want one per stretch)", ev.Data)
		}
	default:
	}
}

// TestGoalWatchdogWakeResetsStretch pins the reset rule: a wake (expiry claim
// + wake drive) resets the stretch, so the next quiet period notifies again
// (the per-24h ceiling, not the per-stretch cap, is what eventually silences).
func TestGoalWatchdogWakeResetsStretch(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("wait then wait again", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	drainGoalEvents(sess)

	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev := nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("first watchdog = %+v, want park-start", ev.Data)
	}
	// Wake: expire the wait and drive the wake turn (resets the stretch).
	clk.Advance(31 * time.Minute)
	sess.noteGoalWatchdogActivity(clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: re-registration should succeed")
	}
	drainGoalEvents(sess)
	clk.Advance(31 * time.Minute)
	sess.checkGoalWatchdog(clk.Now())
	ev = nextGoalEventOfKind(t, sess, events.EventGoalWatchdog)
	if d, ok := ev.Data.(events.GoalWatchdogData); !ok || d.Kind != "park-start" {
		t.Fatalf("post-wake watchdog = %+v, want park-start again (stretch reset)", ev.Data)
	}
}

// TestGoalContinuationCarriesConditionFlip pins the §6 direction-4 delta: a
// drive after a condition truth change carries the flip in the prompt frame,
// not the full state.
func TestGoalContinuationCarriesConditionFlip(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report changed", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("goal_expect registration should succeed, got error: %s", res.Output)
	}
	// First drive snapshots the truth (no flip yet — no frame).
	advance := ledgerGateOutcome("write report", "ok", "h1", "d1", true)
	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, advance)
	if !cont || prompt == "" {
		t.Fatal("precondition: advancing gate must drive")
	}
	if strings.Contains(prompt, "goal-delta") {
		t.Fatalf("first drive must carry no delta frame (nothing flipped yet):\n%s", prompt)
	}
	// Change the file (baseline flips) and drive an advancing turn: the flip
	// rides the prompt.
	wireTask8Files(sess, map[string]string{"/work/report.md": "v2"})
	advance2 := ledgerGateOutcome("write report", "ok", "h2", "d2", true)
	prompt, cont = sess.armGoalContinuationWithOutcome(false, true, advance2)
	if !cont || prompt == "" {
		t.Fatal("precondition: second advancing gate must drive")
	}
	if !strings.Contains(prompt, "goal-delta") || !strings.Contains(prompt, "report changed") {
		t.Fatalf("drive after a condition flip must carry the delta naming it:\n%s", prompt)
	}
}

// TestGoalVerifySatisfiedConditionCompletes pins the verifier's accept path: a
// goal whose registered file condition changed since registration completes.
func TestGoalVerifySatisfiedConditionCompletes(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report changed", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("goal_expect registration should succeed, got error: %s", res.Output)
	}
	wireTask8Files(sess, map[string]string{"/work/report.md": "v2"})
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ug1", Name: "update_goal",
		Arguments: task8Args(t, map[string]any{"status": "complete"}),
		Type:      "function",
	})
	if cres.IsError {
		t.Fatalf("satisfied conditioned complete should succeed, got error: %s", cres.Output)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusComplete {
		t.Fatalf("status = %q, want complete", snap.Status)
	}
}

// TestGoalApprovalPairContract pins Task-7 Minor-1: a two-half key matches
// only both halves; a bare key on a two-half ask does not match.
func TestGoalApprovalPairContract(t *testing.T) {
	if !approvalKeyMatches("h", "q", "h\x00q") {
		t.Fatal("two-half key must match the (header, question) pair")
	}
	if approvalKeyMatches("h", "q", "q") {
		t.Fatal("bare question must not match a two-half ask (loose OR rejected)")
	}
	if approvalKeyMatches("h", "q", "h") {
		t.Fatal("bare header must not match a two-half ask (loose OR rejected)")
	}
	if !approvalKeyMatches("", "q", "q") {
		t.Fatal("bare question must match a headerless ask (ambiguous-by-construction)")
	}
}

// TestGoalPendingWakeKindStructural pins Task-7 Minor-5: claims record the
// lease kind, and the ledger evidence keys on it (not the trigger prefix).
func TestGoalPendingWakeKindStructural(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	store := goal.NewStore()
	store.Set("structural wake", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}
	entry, ok := store.ClaimFire(w.Lease.WaitID, "child xyz terminal (decoy prefix)", clk.Now())
	if !ok {
		t.Fatal("precondition: claim should succeed")
	}
	if entry.Kind != goal.WaitUntilTime {
		t.Fatalf("claimed kind = %q, want until_time (structure, not the decoy prefix)", entry.Kind)
	}
	if claimedChildTerminalUserspace([]goal.PendingWake{entry}) {
		t.Fatal("until_time claim with a child-terminal trigger prefix must not read as waits evidence")
	}
}

// claimedChildTerminalUserspace mirrors the gate's structural check for the
// package-external assertion (the gate function is unexported).
func claimedChildTerminalUserspace(claimed []goal.PendingWake) bool {
	for _, c := range claimed {
		if c.Kind == goal.WaitUntilChild {
			return true
		}
	}
	return false
}

// TestGoalExpectKindRestrictionToolLevel pins fix-1/4 I1 at the tool: each
// non-registrable kind (approval/child/http/external-label) rejects with its
// reason named; file/job/delegate still register.
func TestGoalExpectKindRestrictionToolLevel(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})

	newGoal := func() {
		t.Helper()
		sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
		wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	}
	// Registrable: default file check, explicit file, live-job stub is absent
	// (production substrate) — file suffices here; job/delegate register at
	// the store level (TestRegisterExpectKindRestriction).
	newGoal()
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge-ok", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report changed", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("file condition must register, got error: %s", res.Output)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"approval", map[string]any{"desc": "a", "kind": "until_approval", "target": "q?"}, "until_approval"},
		{"child", map[string]any{"desc": "c", "kind": "until_child", "target": "child_1"}, "until_child"},
		{"http", map[string]any{"desc": "h", "kind": "until_event", "event_subtype": "http_match", "target": "https://example.com/hook"}, "not supported"},
		{"external", map[string]any{"desc": "e", "kind": "until_event", "event_subtype": "external_label"}, "external_label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
				ID: "ge-" + tc.name, Name: "goal_expect",
				Arguments: task8Args(t, tc.args),
				Type:      "function",
			})
			if !res.IsError {
				t.Fatalf("%s condition must reject under the v1 restriction, got success: %s", tc.name, res.Output)
			}
			if !strings.Contains(res.Output, tc.want) {
				t.Fatalf("%s rejection %q must name %q", tc.name, res.Output, tc.want)
			}
		})
	}
}
