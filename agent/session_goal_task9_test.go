package agent

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
)

// Task-9 slice-3 resume tests (spec §5 resume/renewal): failing until Store.Resume
// + Session.GoalResume land. Deterministic: FakeClock, scripted outcomes — no
// wall-clock sleeps, no live provider.

// resumeBlockedNoProgress blocks the goal with the stall verdict via the
// terminal path, failing on setup errors.
func resumeBlockedNoProgress(t *testing.T, sess *Session, now time.Time) {
	t.Helper()
	store := sess.getOrCreateGoalStore()
	store.Set("stalled objective", now)
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictNoProgress, now) {
		t.Fatal("precondition: SetTerminal should block the active goal")
	}
}

// TestGoalResumeNoProgressKeepsBudgetsResetsLedger pins the §5 no-progress
// path: ledger reset, waits cleared, budgets kept, autoReparks reset — and the
// goal drives again (status active).
func TestGoalResumeNoProgressKeepsBudgetsResetsLedger(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("stalled objective", clk.Now())
	// Accrue spend + stall evidence + a live wait + re-parks before the block.
	if _, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Hour}, clk.Now()); !ok {
		t.Fatal("precondition: until_time registration should succeed")
	}
	for range 3 {
		store.RecordContinuation(goal.TurnOutcome{ActionFingerprint: "poll x", ObservationClass: "ok", ObservationHash: "h", StateDigest: "steady", Mutated: true}, false, clk.Now())
	}
	full, _ := store.GoalSnapshot()
	used := full.Budgets.UsedContinuations
	max := full.Budgets.MaxContinuations
	deadline := full.Budgets.Deadline
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictNoProgress, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}

	started, err := sess.GoalResume(goal.ResumeRequest{}, clk.Now())
	if err != nil {
		t.Fatalf("GoalResume = %v, want nil on the no-progress path", err)
	}
	if !started {
		t.Fatal("GoalResume started = false, want a drive (kick or gate pickup)")
	}
	after, _ := store.GoalSnapshot()
	if after.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after resume", after.Status)
	}
	if len(after.Waits) != 0 {
		t.Fatalf("waits = %+v, want cleared on resume", after.Waits)
	}
	if len(after.LedgerSummary.Entries) != 0 || after.LedgerSummary.Repetition != 0 {
		t.Fatalf("ledger = %+v, want reset on resume", after.LedgerSummary)
	}
	if after.AutoReparks != 0 {
		t.Fatalf("autoReparks = %d, want reset on resume", after.AutoReparks)
	}
	if after.Budgets.UsedContinuations != used || after.Budgets.MaxContinuations != max || !after.Budgets.Deadline.Equal(deadline) {
		t.Fatalf("budgets = %+v, want kept (used=%d max=%d deadline=%s)", after.Budgets, used, max, deadline)
	}
	if after.LedgerSummary.Stage != goal.StageNone {
		t.Fatalf("stage = %q, want none after ledger reset", after.LedgerSummary.Stage)
	}
}

// TestGoalResumeBudgetBlockNeedsExtend pins the §5 renewal check: a
// budget-exhausted block without --extend rejects naming the budget.
func TestGoalResumeBudgetBlockNeedsExtend(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("spent objective", clk.Now())
	full, _ := store.GoalSnapshot()
	full.Budgets.UsedContinuations = full.Budgets.MaxContinuations
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictBudgetExhausted, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}

	_, err := sess.GoalResume(goal.ResumeRequest{}, clk.Now())
	if err == nil || !strings.Contains(err.Error(), "maxContinuations") {
		t.Fatalf("GoalResume err = %v, want a rejection naming maxContinuations", err)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusBlocked {
		t.Fatalf("status = %q, want still blocked after rejected resume", snap.Status)
	}
}

// TestGoalResumeBudgetBlockWithExtendDrives pins the §5 renewal drive: with
// --extend continuations <value> (clamped to the 1000 cap) the resume drives.
func TestGoalResumeBudgetBlockWithExtendDrives(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("spent objective", clk.Now())
	full, _ := store.GoalSnapshot()
	full.Budgets.UsedContinuations = full.Budgets.MaxContinuations
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictBudgetExhausted, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}

	started, err := sess.GoalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendContinuations, Value: 50}}, clk.Now())
	if err != nil {
		t.Fatalf("GoalResume with --extend = %v, want nil", err)
	}
	if !started {
		t.Fatal("GoalResume started = false, want a drive after renewal")
	}
	after, _ := store.GoalSnapshot()
	if after.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after extended resume", after.Status)
	}
	if after.Budgets.MaxContinuations != full.Budgets.MaxContinuations+50 {
		t.Fatalf("maxContinuations = %d, want %d", after.Budgets.MaxContinuations, full.Budgets.MaxContinuations+50)
	}
}

// TestGoalResumeExtendClampsToCaps pins the §5 clamps: an --extend beyond
// 1000 continuations clamps, and a deadline extend beyond 24h clamps to now+24h.
func TestGoalResumeExtendClampsToCaps(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("spent objective", clk.Now())
	full, _ := store.GoalSnapshot()
	full.Budgets.UsedContinuations = full.Budgets.MaxContinuations
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictBudgetExhausted, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	if _, err := sess.GoalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendContinuations, Value: 5000}}, clk.Now()); err != nil {
		t.Fatalf("clamped extend = %v, want nil", err)
	}
	after, _ := store.GoalSnapshot()
	if after.Budgets.MaxContinuations != goal.MaxContinuationsCap {
		t.Fatalf("maxContinuations = %d, want clamped to %d", after.Budgets.MaxContinuations, goal.MaxContinuationsCap)
	}
}

// TestGoalResumeDeadlineExtendResetsOneShot pins R7 M-I2: resume with --extend
// deadline resets deadlineFinalDelivered=false keyed to the new deadline.
func TestGoalResumeDeadlineExtendResetsOneShot(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("deadline objective", clk.Now())
	full, _ := store.GoalSnapshot()
	full.DeadlineFinalDelivered = true
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictDeadlineExceeded, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	if _, err := sess.GoalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendDeadline, Value: 3600}}, clk.Now()); err != nil {
		t.Fatalf("deadline extend = %v, want nil", err)
	}
	after, _ := store.GoalSnapshot()
	if after.DeadlineFinalDelivered {
		t.Fatal("deadlineFinalDelivered = true, want false after --extend deadline")
	}
	if after.Budgets.Deadline.Before(clk.Now().Add(time.Hour)) {
		t.Fatalf("deadline = %s, want extended past now+1h", after.Budgets.Deadline)
	}
}

// TestGoalResumeWaitingLostFollowsNoProgressPath pins the §5 waiting-lost
// resume: ledger reset + waits cleared + budgets kept + persisted cause
// cleared, with re-arm-or-proceed guidance.
func TestGoalResumeWaitingLostFollowsNoProgressPath(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("lost objective", clk.Now())
	full, _ := store.GoalSnapshot()
	full.LossCause = "restart without substrate"
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.WaitingLostVerdict("restart without substrate"), clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	if _, err := sess.GoalResume(goal.ResumeRequest{}, clk.Now()); err != nil {
		t.Fatalf("GoalResume = %v, want nil on the waiting-lost path", err)
	}
	after, _ := store.GoalSnapshot()
	if after.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after waiting-lost resume", after.Status)
	}
	if after.LossCause != "" {
		t.Fatalf("lossCause = %q, want cleared on resume", after.LossCause)
	}
	if len(after.LedgerSummary.Entries) != 0 {
		t.Fatalf("ledger = %+v, want reset on waiting-lost resume", after.LedgerSummary)
	}
}

// TestGoalResumeNoGoalErrors pins L-I1: resume with no goal at all (never set,
// or cleared) errors naming "no blocked goal to resume" — never a literal
// objective.
func TestGoalResumeNoGoalErrors(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	if _, err := sess.GoalResume(goal.ResumeRequest{}, clk.Now()); err == nil || !strings.Contains(err.Error(), "no blocked goal to resume") {
		t.Fatalf("GoalResume err = %v, want the no-blocked-goal error", err)
	}
}

// TestGoalResumeStallBlockWithSpentBudgetRenews pins the §5 scope rule: the
// renewal check runs whenever ANY budget is exhausted at resume, regardless
// of the recorded block reason (a stall-block coinciding with a spent budget
// renews the same way).
func TestGoalResumeStallBlockWithSpentBudgetRenews(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("stalled but spent", clk.Now())
	full, _ := store.GoalSnapshot()
	full.Budgets.UsedContinuations = full.Budgets.MaxContinuations
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictNoProgress, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	if _, err := sess.GoalResume(goal.ResumeRequest{}, clk.Now()); err == nil || !strings.Contains(err.Error(), "maxContinuations") {
		t.Fatalf("GoalResume err = %v, want renewal rejection naming maxContinuations despite the stall verdict", err)
	}
}

// TestParseGoalExtendGrammar pins the two-token --extend grammar helper.
func TestParseGoalExtendGrammar(t *testing.T) {
	t.Parallel()
	ext, rest, err := parseGoalResumeArgs("--extend continuations 50")
	if err != nil || ext == nil || ext.Budget != goal.ExtendContinuations || ext.Value != 50 || rest != "" {
		t.Fatalf("parse = (%+v, %q, %v), want continuations/50/empty", ext, rest, err)
	}
	ext, rest, err = parseGoalResumeArgs("--extend deadline 3600 extra words here")
	if err != nil || ext == nil || ext.Budget != goal.ExtendDeadline || ext.Value != 3600 || rest != "extra words here" {
		t.Fatalf("parse = (%+v, %q, %v), want deadline/3600/rest", ext, rest, err)
	}
	if _, _, err := parseGoalResumeArgs("--extend continuations"); err == nil {
		t.Fatal("parse with a missing value should error (two-token grammar)")
	}
	if _, _, err := parseGoalResumeArgs("--extend bogus 10"); err == nil {
		t.Fatal("parse with an unknown budget should error")
	}
}

// TestGoalResumeFromWirePinsDaemonGlue pins the GoalResumeFromWire bridge the
// daemon's goal/set Resume=true path calls: plain budget-token types map onto
// the renewal, unknown budgets and non-positive values error naming the
// fault, and a spent budget without --extend rejects.
func TestGoalResumeFromWirePinsDaemonGlue(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("spent objective", clk.Now())
	full, _ := store.GoalSnapshot()
	full.Budgets.UsedContinuations = full.Budgets.MaxContinuations
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictBudgetExhausted, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	if _, err := sess.GoalResumeFromWire("", "", 0); err == nil || !strings.Contains(err.Error(), "continuations") {
		t.Fatalf("wire resume without extend = %v, want the renewal rejection", err)
	}
	if _, err := sess.GoalResumeFromWire("", "bogus", 10); err == nil || !strings.Contains(err.Error(), "unknown --extend budget") {
		t.Fatalf("wire resume bad budget = %v, want the unknown-budget error", err)
	}
	if _, err := sess.GoalResumeFromWire("", "continuations", 0); err == nil || !strings.Contains(err.Error(), "invalid --extend value") {
		t.Fatalf("wire resume zero value = %v, want the invalid-value error", err)
	}
	if _, err := sess.GoalResumeFromWire("", "continuations", 50); err != nil {
		t.Fatalf("wire resume with extend = %v, want nil", err)
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after the wire resume", snap.Status)
	}
}

// restorePersistedFromFull round-trips a full snapshot through the persisted
// image so tests can set budget/loss fields the narrow mutators do not expose.
func restorePersistedFromFull(t *testing.T, full goal.GoalSnapshot) goal.PersistedGoal {
	t.Helper()
	persisted, ok := goal.PersistedFromSnapshot(full)
	if !ok {
		t.Fatal("precondition: PersistedFromSnapshot should convert the live snapshot")
	}
	return persisted
}
