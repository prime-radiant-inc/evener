package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Slice-2 gate integration tests (spec §§4, 7, 8): the ledger fold replaces
// the interim v1 judge in the gate; child terminal reports forward into
// until_child waits; migration seeds feed the real ledger.
//
// Deterministic: explicit TurnOutcomes (no live-digest dependence), FakeClock
// where time matters, scripted provider for full-turn tests. StateDigest ""
// in these tests means "verbatim empty" (the hermetic gate seam); production
// pre-fills it via buildGoalTurnOutcome/goalStateDigest.

// ledgerGateOutcome builds an explicit gate-fold input.
func ledgerGateOutcome(fp, class, hash, digest string, mutated bool) goal.TurnOutcome {
	return goal.TurnOutcome{
		ActionFingerprint: fp,
		ObservationClass:  class,
		ObservationHash:   hash,
		StateDigest:       digest,
		Mutated:           mutated,
	}
}

// foldGateContinuations runs n gate folds with the same outcome, failing on
// any unexpected non-drive.
func foldGateContinuations(t *testing.T, sess *Session, n int, outcome goal.TurnOutcome) {
	t.Helper()
	for i := range n {
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, outcome)
		if !cont || prompt == "" {
			t.Fatalf("gate %d = (%q, %v), want a drive: no stall trip expected yet", i+1, prompt, cont)
		}
	}
}

func countTask7SteeringNotes(sess *Session, substr string) int {
	n := 0
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), substr) {
			n++
		}
	}
	return n
}

// TestGoalLedgerGateReadHeavyOpeningNudgesThenBlocks pins the §4 two-tier rule
// at the gate: 6 identical reads with no digest delta reach K=6 (fresh tier)
// and NUDGE — never block outright — naming the repetition evidence; the next
// identical turn blocks with "no progress".
func TestGoalLedgerGateReadHeavyOpeningNudgesThenBlocks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("survey the tree", clk.Now())
	read := ledgerGateOutcome("grep pattern=TODO path=/work", "ok", "same-output", "digest-1", false)
	foldGateContinuations(t, sess, goal.RepetitionThresholdFresh-1, read)

	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, read)
	if !cont || prompt == "" {
		t.Fatalf("6th identical gate = (%q, %v), want a nudge drive, not a block", prompt, cont)
	}
	if got := countTask7SteeringNotes(sess, "grep"); got != 1 {
		t.Fatalf("nudge steering notes = %d, want exactly 1 naming the repetition evidence", got)
	}
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if full.LedgerSummary.Stage != goal.StageNudged {
		t.Fatalf("stage = %q, want nudged after the first stall trip", full.LedgerSummary.Stage)
	}

	prompt, cont = sess.armGoalContinuationWithOutcome(false, true, read)
	if cont || prompt != "" {
		t.Fatalf("7th identical gate = (%q, %v), want a block after the nudge", prompt, cont)
	}
	snap, _ := sess.getOrCreateGoalStore().Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictNoProgress {
		t.Fatalf("snapshot = %+v, want blocked/no progress", snap)
	}
	if got := countTask7SteeringNotes(sess, "no progress"); got != 1 {
		t.Fatalf("block steering notes = %d, want exactly 1", got)
	}
	// The block path reports exactly once through its own once-gate (the
	// second take must be silent); the first take is already consumed by the
	// gate's reportGoalEnded, so assert the no-reemit half here.
	if _, reported := sess.getOrCreateGoalStore().TakeTerminalReport(); reported {
		t.Fatal("terminal report must not re-emit after the gate already reported the block")
	}
}

// TestGoalLedgerGateAlternatingBackstopNudgesThenBlocks pins the B=12 total
// backstop at the gate: a period-2 alternation never trips repetition, trips
// the backstop once 12 trailing non-advancing entries stand (the bounded
// 12-entry window holds the opening novel pair until turn 13, so the trip
// lands on the 14th turn), nudges there, and blocks on the next
// still-stalled turn.
func TestGoalLedgerGateAlternatingBackstopNudgesThenBlocks(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("poll two endpoints", clk.Now())
	pollA := ledgerGateOutcome("poll endpoint=a", "ok", "hash-a", "steady-digest", false)
	pollB := ledgerGateOutcome("poll endpoint=b", "ok", "hash-b", "steady-digest", false)
	for i := range goal.BackstopThreshold + 2 {
		outcome := pollA
		if i%2 == 1 {
			outcome = pollB
		}
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, outcome)
		if i < goal.BackstopThreshold {
			if !cont || prompt == "" {
				t.Fatalf("alternating gate %d = (%q, %v), want a drive (backstop needs 12 non-advancing)", i+1, prompt, cont)
			}
			continue
		}
		// 14th real turn (the 12-entry window finally holds 12 trailing
		// non-advancing): the backstop trips with a nudge, not a block.
		if !cont || prompt == "" {
			t.Fatalf("backstop-trip gate = (%q, %v), want a nudge drive", prompt, cont)
		}
	}
	if got := countTask7SteeringNotes(sess, "non-advancing"); got != 1 {
		t.Fatalf("backstop nudge notes = %d, want exactly 1", got)
	}
	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, pollB)
	if cont || prompt != "" {
		t.Fatalf("post-nudge alternating gate = (%q, %v), want a block", prompt, cont)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictNoProgress {
		t.Fatalf("snapshot = %+v, want blocked/no progress", snap)
	}
}

// TestGoalLedgerGateGenuineRetryStaysLive pins the live side: the same action
// three times WITH a state-digest delta each turn never accrues repetition and
// never trips either signal.
func TestGoalLedgerGateGenuineRetryStaysLive(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("land the fix", clk.Now())
	for i := range 3 {
		retry := ledgerGateOutcome("run_test package=foo", "ok", "same-summary", "digest-"+string(rune('1'+i)), true)
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, retry)
		if !cont || prompt == "" {
			t.Fatalf("retry %d = (%q, %v), want a drive: digest deltas stay live", i+1, prompt, cont)
		}
	}
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if full.LedgerSummary.Repetition != 1 {
		t.Fatalf("repetition = %d, want 1 (every retry advanced)", full.LedgerSummary.Repetition)
	}
	if goal.LedgerStalled(full.LedgerSummary) {
		t.Fatalf("genuine-retry ledger must not read stalled: %+v", full.LedgerSummary)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active", snap.Status)
	}
}

// TestGoalLedgerGateTimestampNoiseStillStalls pins canonicalization at the
// gate: timestamp-only observation noise canonicalizes equal, so identical
// polls still reach K and graduate nudge → block.
func TestGoalLedgerGateTimestampNoiseStillStalls(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	sess.getOrCreateGoalStore().Set("watch the log", clk.Now())
	for i := range goal.RepetitionThresholdFresh - 1 {
		noisy := ledgerGateOutcome("tail log", "ok", "lines=3 at 2026-09-08T05:00:0"+string(rune('0'+i)), "steady", false)
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, noisy)
		if !cont || prompt == "" {
			t.Fatalf("noisy gate %d = (%q, %v), want a drive", i+1, prompt, cont)
		}
	}
	sixth := ledgerGateOutcome("tail log", "ok", "lines=3 at 2026-09-08T05:00:09", "steady", false)
	if prompt, cont := sess.armGoalContinuationWithOutcome(false, true, sixth); !cont || prompt == "" {
		t.Fatalf("6th noisy gate = (%q, %v), want a nudge drive: timestamp noise must not launder repetition", prompt, cont)
	}
	if got := countTask7SteeringNotes(sess, "tail"); got != 1 {
		t.Fatalf("nudge notes = %d, want exactly 1 naming the action", got)
	}
}

// TestGoalLedgerGateMigratedSeedFeedsRealLedger pins the §7 migration table
// feeding the real ledger: a streak-5 never-advanced migration seeds 5
// "migrated" entries, and the first real non-advancing turn resets the run
// (the disclosed at-most-K−1 residual) instead of blocking — the 6th
// consecutive identical real turn nudges.
func TestGoalLedgerGateMigratedSeedFeedsRealLedger(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	created := clk.Now()
	v1seed := goal.MigrateV1ToPersisted("migrate me", "active", "", 7, 5, false, created, created, created)
	fresh := goal.NewStore()
	fresh.RestoreSnapshot(v1seed)
	_ = fresh

	clk2 := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk2)
	defer sess.Close()
	wireKickAndNotify(sess)
	sess.getOrCreateGoalStore().RestoreSnapshot(v1seed)

	real := ledgerGateOutcome("grep pattern=x", "ok", "out", "d", false)
	foldGateContinuations(t, sess, goal.RepetitionThresholdFresh-1, real)
	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, real)
	if !cont || prompt == "" {
		t.Fatalf("6th real gate = (%q, %v), want the residual nudge, not a block", prompt, cont)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after the nudge", snap.Status)
	}
	prompt, cont = sess.armGoalContinuationWithOutcome(false, true, real)
	if cont || prompt != "" {
		t.Fatalf("7th real gate = (%q, %v), want a block", prompt, cont)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want blocked", snap.Status)
	}
}

// TestGoalLedgerGateBackstopNeedsRefillAfterMigration pins the carried Task-6
// note: a migrated summary with <12 entries cannot backstop-stall until the
// window refills — 5 seeded + alternating real turns trip the backstop only
// once 12 trailing non-advancing entries stand.
func TestGoalLedgerGateBackstopNeedsRefillAfterMigration(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	created := clk.Now()
	v1seed := goal.MigrateV1ToPersisted("old goal", "active", "", 2, 0, false, created, created, created)
	if len(v1seed.LedgerSummary.Entries) != 0 {
		t.Fatalf("precondition: streak-0 migration seeds no entries, got %d", len(v1seed.LedgerSummary.Entries))
	}
	_ = created

	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)
	// Seed 5 synthetic non-advancing entries directly (a streak-5 shape) and
	// refill with alternating real turns: no backstop until 12 trailing
	// non-advancing entries accumulate.
	seeded := goal.MigrateV1ToPersisted("old goal", "active", "", 7, 5, false, clk.Now(), clk.Now(), clk.Now())
	sess.getOrCreateGoalStore().RestoreSnapshot(seeded)

	pollA := ledgerGateOutcome("poll endpoint=a", "ok", "hash-a", "steady-digest", false)
	pollB := ledgerGateOutcome("poll endpoint=b", "ok", "hash-b", "steady-digest", false)
	// 5 seeded + opening novel pair: the 11th real turn holds 11 trailing
	// non-advancing (5+2 novel +... ) — assert no trip before the 12th.
	for i := range 6 {
		outcome := pollA
		if i%2 == 1 {
			outcome = pollB
		}
		prompt, cont := sess.armGoalContinuationWithOutcome(false, true, outcome)
		if !cont || prompt == "" {
			t.Fatalf("refill gate %d = (%q, %v), want a drive: window not full yet", i+1, prompt, cont)
		}
	}
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if goal.BackstopStalled(full.LedgerSummary) {
		t.Fatalf("backstop must not stall with %d trailing non-advancing (<12)", countTrailingNonAdvancing(full.LedgerSummary))
	}
}

func countTrailingNonAdvancing(s goal.LedgerSummary) int {
	n := 0
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Advancement {
			break
		}
		n++
	}
	return n
}

// TestGoalWakeTurnFoldsLedgerAndAccruesBudget pins the interim-bypass removal:
// a wait-attributable wake turn now folds into the ledger and accrues the
// continuation budget like any other turn (spec §5: wake turns count).
func TestGoalWakeTurnFoldsLedgerAndAccruesBudget(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("wake folds now", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now()); !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, cont := sess.armGoalContinuationWithOutcome(false, true, goal.TurnOutcome{}); !cont {
		t.Fatal("precondition: expired gate must drive the wake turn")
	}
	full, _ := store.GoalSnapshot()
	if len(full.LedgerSummary.Entries) != 1 {
		t.Fatalf("ledger entries after wake drive = %d, want 1 (wake turns fold since slice 2)", len(full.LedgerSummary.Entries))
	}
	if full.Budgets.UsedContinuations != 1 {
		t.Fatalf("used continuations after wake drive = %d, want 1 (wake turns count)", full.Budgets.UsedContinuations)
	}
}

// TestGoalChildForwardTerminalClaimsAndDrives pins the §8 forward: a parent
// parked on until_child whose target child is terminal claims the wait at its
// gate attach-scan and drives the wake carrying the terminal trigger.
func TestGoalChildForwardTerminalClaimsAndDrives(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	trackSyntheticChild(t, sess, "fwd_child_1", SubagentCompleted, false, false, clk.Now(), false)
	store := sess.getOrCreateGoalStore()
	store.Set("wait on the child", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilChild, Target: "fwd_child_1", Timeout: time.Hour}, clk.Now())
	if !ok {
		t.Fatalf("precondition: until_child on a known descendant must register (boundary lifted): %q", store.LastRejectReason())
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting after until_child registration", snap.Status)
	}

	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, goal.TurnOutcome{})
	if !cont || prompt == "" {
		t.Fatalf("terminal-child gate = (%q, %v), want the forwarded wake drive", prompt, cont)
	}
	if !strings.Contains(prompt, w.Lease.WaitID) {
		t.Fatalf("wake prompt missing wait_id %q\nprompt:\n%s", w.Lease.WaitID, prompt)
	}
	if !strings.Contains(prompt, "fwd_child_1") {
		t.Fatalf("wake prompt must carry the terminal child identity\nprompt:\n%s", prompt)
	}
}

// TestGoalChildForwardIntermediateChatterDoesNotClaim pins terminal-only
// matching: a still-running child never fires its parent's until_child — the
// goal stays parked with no backlog.
func TestGoalChildForwardIntermediateChatterDoesNotClaim(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	trackSyntheticChild(t, sess, "fwd_child_live", SubagentRunning, false, false, time.Time{}, false)
	store := sess.getOrCreateGoalStore()
	store.Set("wait on the live child", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilChild, Target: "fwd_child_live", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: registration must succeed: %q", store.LastRejectReason())
	}

	prompt, cont := sess.armGoalContinuationWithOutcome(false, true, goal.TurnOutcome{})
	if cont || prompt != "" {
		t.Fatalf("live-child gate = (%q, %v), want a park: intermediate chatter never claims", prompt, cont)
	}
	if full, _ := store.GoalSnapshot(); len(full.PendingWake) != 0 {
		t.Fatalf("pendingWake = %+v, want empty (no claim on a live child)", full.PendingWake)
	}
}

// TestGoalChildForwardStopGatedEmitsLossNotice pins gate-before-claim: a
// forward aimed at a stop-gated child claims nothing and leaves the honest
// loss notice on the child's next turn tail instead.
func TestGoalChildForwardStopGatedEmitsLossNotice(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	// Gate-before-claim at the store seam: ClaimChildWaits checks the waiter's
	// stop gate before consuming. The gate check lives in childStopGated
	// (stable-delegate rows); here the waiter is pinned through the exported
	// ClaimChildWaits with a stop-gated wrapper: a waiter whose session is
	// closed cannot be driven, so the forward must not claim into it.
	//
	// Deterministic seam: the production forward calls
	// forwardChildTerminalToWaits on run-end; the gate-before-claim half is
	// the childStopGated/childFatalRunGated branch inside it. Drive that
	// branch with a waiter whose drive path is closed: closing the waiter
	// session marks it undrivable while the lease stays live.
	trackSyntheticChild(t, sess, "fwd_child_gated", SubagentRunning, false, false, time.Time{}, false)
	var gatedChild *subagent
	for _, sub := range sess.subagents.directSubagents() {
		if sub.id == "fwd_child_gated" {
			gatedChild = sub
		}
	}
	if gatedChild == nil {
		t.Fatal("precondition: gated child should be tracked")
	}
	gatedChild.sess.getOrCreateGoalStore().Set("wait on sibling", clk.Now())
	// Track the terminal sibling BEFORE registering: the substrate resolves
	// known descendants against the live tracked set.
	trackSyntheticChild(t, sess, "fwd_child_done", SubagentCompleted, false, false, clk.Now(), false)
	var doneChild *subagent
	for _, sub := range sess.subagents.directSubagents() {
		if sub.id == "fwd_child_done" {
			doneChild = sub
		}
	}
	if doneChild == nil {
		t.Fatal("precondition: terminal child should be tracked")
	}
	// The PARENT waits on the terminal child (the until_child-on-my-child
	// shape the substrate resolves); the gate-before-claim half is pinned by
	// withholding the forward to a fatal-gated sibling waiter below. First
	// register the parent's own lease.
	sess.getOrCreateGoalStore().Set("wait on the done child", clk.Now())
	if _, ok := sess.getOrCreateGoalStore().RegisterWait(goal.WaitKind{Kind: goal.WaitUntilChild, Target: "fwd_child_done", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("precondition: parent registration should succeed: %q", sess.getOrCreateGoalStore().LastRejectReason())
	}
	// Mark the sibling waiter fatal-gated (a terminal run error freezes
	// automatic drives): the forward must withhold the sibling claim and
	// leave the loss notice, while still claiming the parent's own lease.
	gatedChild.mu.Lock()
	gatedChild.fatalRunGated = true
	gatedChild.mu.Unlock()

	sess.forwardChildTerminalToWaits(doneChild)

	parentFull, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if len(parentFull.PendingWake) != 1 {
		t.Fatalf("parent pendingWake = %+v, want the parent's own claim to land", parentFull.PendingWake)
	}
	waiterFull, _ := gatedChild.sess.getOrCreateGoalStore().GoalSnapshot()
	if len(waiterFull.PendingWake) != 0 || len(waiterFull.Waits) != 0 {
		t.Fatalf("gated waiter = %+v, want untouched (no lease, no claim)", waiterFull)
	}
	if got := countTask7SteeringNotes(gatedChild.sess, "[goal-wait-lost]"); got != 1 {
		t.Fatalf("loss-notice notes on the gated child = %d, want exactly 1", got)
	}
}

// goalWaitTerminalStub is a test substrate accepting a fixed child set.
type goalWaitTerminalStub struct {
	children map[string]bool
}

func (f *goalWaitTerminalStub) LookupJob(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *goalWaitTerminalStub) LookupDelegate(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (f *goalWaitTerminalStub) StatFile(path string) (string, bool) { return "", false }

func (f *goalWaitTerminalStub) LookupApproval(contentKey, generation string) bool { return false }

func (f *goalWaitTerminalStub) LookupChild(id string) bool { return f.children[id] }

func (f *goalWaitTerminalStub) CheckURL(rawURL string, timeout time.Duration) bool { return false }

// TestGoalChildWaitRegistrationRequiresKnownDescendant pins the lifted
// boundary: unknown child targets still reject fail-closed, while a tracked
// descendant registers (the slice-1 scoped-out rejection is gone).
func TestGoalChildWaitRegistrationRequiresKnownDescendant(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("wait on descendants", clk.Now())
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilChild, Target: "no_such_child", Timeout: time.Hour}, clk.Now()); ok {
		t.Fatal("unknown child target must reject fail-closed")
	}
	if reason := store.LastRejectReason(); !strings.Contains(reason, "no_such_child") {
		t.Fatalf("rejection %q must name the unknown child", reason)
	}

	trackSyntheticChild(t, sess, "fwd_child_known", SubagentRunning, false, false, time.Time{}, false)
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilChild, Target: "fwd_child_known", Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatalf("known descendant must register: %q", store.LastRejectReason())
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting", snap.Status)
	}
}

// TestGoalLedgerFullTurnStallGraduates pins the fold end to end through a
// scripted turn: communicate-only continuation turns with no state movement
// nudge at K and block after, with the evidence named.
func TestGoalLedgerFullTurnStallGraduates(t *testing.T) {
	t.Parallel()
	steps := make([]func(req llm.Request) llm.Response, 0, goal.RepetitionThresholdFresh+2)
	for range goal.RepetitionThresholdFresh + 1 {
		steps = append(steps, func(req llm.Request) llm.Response {
			return toolCallResponse(communicateCall("c1", "still working"))
		})
	}
	steps = append(steps, func(req llm.Request) llm.Response {
		t.Fatalf("reached a further LLM call: the stalled goal must stop driving continuations")
		return llm.Response{}
	})
	sess := newSession(t, withSteps(steps...))
	defer sess.Close()
	wireKickAndNotify(sess)
	sess.getOrCreateGoalStore().Set("doomed polling loop", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}

	snap, _ := sess.getOrCreateGoalStore().Snapshot()
	if snap.Status != goal.StatusBlocked || snap.StopReason != goal.VerdictNoProgress {
		t.Fatalf("snapshot = %+v, want blocked/no progress after nudge+block graduation", snap)
	}
	if got := countTask7SteeringNotes(sess, "no progress"); got < 1 {
		t.Fatalf("block notes = %d, want at least 1 naming the stall", got)
	}
}
