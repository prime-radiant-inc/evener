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
	maxCont := full.Budgets.MaxContinuations
	deadline := full.Budgets.Deadline
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictNoProgress, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}

	started, err := sess.goalResume(goal.ResumeRequest{}, clk.Now())
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
	if after.Budgets.UsedContinuations != used || after.Budgets.MaxContinuations != maxCont || !after.Budgets.Deadline.Equal(deadline) {
		t.Fatalf("budgets = %+v, want kept (used=%d max=%d deadline=%s)", after.Budgets, used, maxCont, deadline)
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

	_, err := sess.goalResume(goal.ResumeRequest{}, clk.Now())
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

	started, err := sess.goalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendContinuations, Value: 50}}, clk.Now())
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

// TestGoalResumeExtendClampsToCaps pins the §5 clamps: continuations clamp to
// 1000 total, deadline clamps to now+24h, parked-total clamps to 24h total.
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
	if _, err := sess.goalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendContinuations, Value: 5000}}, clk.Now()); err != nil {
		t.Fatalf("clamped extend = %v, want nil", err)
	}
	after, _ := store.GoalSnapshot()
	if after.Budgets.MaxContinuations != goal.MaxContinuationsCap {
		t.Fatalf("maxContinuations = %d, want clamped to %d", after.Budgets.MaxContinuations, goal.MaxContinuationsCap)
	}
}

// TestGoalResumeExtendDeadlineClampsTo24h pins the §5 deadline clamp: an
// --extend deadline beyond the 24h cap lands exactly at now+24h (and resets
// the final-delivered one-shot so the second expiry gets its final turn).
func TestGoalResumeExtendDeadlineClampsTo24h(t *testing.T) {
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
	// 7 days of seconds: must clamp to the 24h cap, not apply verbatim.
	if _, err := sess.goalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendDeadline, Value: 7 * 24 * 3600}}, clk.Now()); err != nil {
		t.Fatalf("clamped deadline extend = %v, want nil", err)
	}
	after, _ := store.GoalSnapshot()
	if want := clk.Now().Add(goal.GoalDeadlineCap); !after.Budgets.Deadline.Equal(want) {
		t.Fatalf("deadline = %s, want clamped to now+24h (%s)", after.Budgets.Deadline, want)
	}
	if after.DeadlineFinalDelivered {
		t.Fatal("deadlineFinalDelivered = true, want false after a clamped --extend deadline")
	}
}

// TestGoalResumeExtendParkedTotalClampsTo24h pins the §5 parked-total clamp:
// an --extend parked-total past the 24h total cap lands exactly at the cap.
func TestGoalResumeExtendParkedTotalClampsTo24h(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	wireKickAndNotify(sess)

	store := sess.getOrCreateGoalStore()
	store.Set("parked objective", clk.Now())
	full, _ := store.GoalSnapshot()
	// A sub-cap parked-total budget (1h), fully consumed: the extend must
	// both renew (usage 1h < clamped 24h max) and clamp (1h+48h → 24h cap).
	// (At the 24h default cap an exhausted parked-total cannot renew — no
	// extend fits under the cap — so the clamp is only observable sub-cap.)
	full.Budgets.MaxParkedTotal = time.Hour
	full.Budgets.ParkedTotal = time.Hour
	store.RestoreSnapshot(restorePersistedFromFull(t, full))
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictBudgetExhausted, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	if _, err := sess.goalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendParkedTotal, Value: int64((48 * time.Hour).Seconds())}}, clk.Now()); err != nil {
		t.Fatalf("clamped parked-total extend = %v, want nil", err)
	}
	after, _ := store.GoalSnapshot()
	if after.Budgets.MaxParkedTotal != goal.MaxParkedTotalCap {
		t.Fatalf("maxParkedTotal = %s, want clamped to %s", after.Budgets.MaxParkedTotal, goal.MaxParkedTotalCap)
	}
	if after.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after the renewing extend", after.Status)
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
	if _, err := sess.goalResume(goal.ResumeRequest{Extend: &goal.ExtendRequest{Budget: goal.ExtendDeadline, Value: 3600}}, clk.Now()); err != nil {
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
	if _, err := sess.goalResume(goal.ResumeRequest{}, clk.Now()); err != nil {
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
	// Spec §5 requires re-arm-or-proceed guidance on the waiting-lost resume
	// path (the lost wait will not refire).
	if got := countTask7SteeringNotes(sess, "re-arm it with goal_wait or proceed without the wait"); got != 1 {
		t.Fatalf("waiting-lost guidance notes = %d, want exactly 1", got)
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

	if _, err := sess.goalResume(goal.ResumeRequest{}, clk.Now()); err == nil || !strings.Contains(err.Error(), "no blocked goal to resume") {
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
	if _, err := sess.goalResume(goal.ResumeRequest{}, clk.Now()); err == nil || !strings.Contains(err.Error(), "maxContinuations") {
		t.Fatalf("GoalResume err = %v, want renewal rejection naming maxContinuations despite the stall verdict", err)
	}
}

// TestParseGoalExtendGrammar pins the single budget-name grammar
// (goal.ParseExtendBudget, spec §5 two-token "--extend <budget> <value>"):
// canonical tokens plus the parked-total spellings map; unknown budgets
// error naming the fault. The two-token arity (missing value, non-numeric
// value, trailing objective text) is pinned at its production site — the
// TUI splitter — by TestHubGoalResumeExtendParsesTwoTokenGrammar; the
// daemon-side value shape is pinned by TestGoalResumeFromWirePinsDaemonGlue.
func TestParseGoalExtendGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		token string
		want  goal.ExtendBudget
	}{
		{"continuations", goal.ExtendContinuations},
		{"deadline", goal.ExtendDeadline},
		{"parked-total", goal.ExtendParkedTotal},
		{"parked_total", goal.ExtendParkedTotal},
		{"parkedtotal", goal.ExtendParkedTotal},
		{"Deadline", goal.ExtendDeadline},
	}
	for _, tc := range cases {
		got, err := goal.ParseExtendBudget(tc.token)
		if err != nil || got != tc.want {
			t.Fatalf("ParseExtendBudget(%q) = (%q, %v), want (%q, nil)", tc.token, got, err, tc.want)
		}
	}
	if _, err := goal.ParseExtendBudget("bogus"); err == nil {
		t.Fatal("ParseExtendBudget(bogus) should error naming the fault")
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

// TestGoalResumeFromWireAppliesReplacementAtomically pins the stale-kick
// guard: a wire resume carrying replacement objective text recovers AND
// retargets under one hold — the kick (if any) renders the NEW objective,
// never a stale old-objective continuation first.
func TestGoalResumeFromWireSuppressesKickWithReplacementText(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()
	var prompts []string
	sess.SetKickFunc(func(p string) { prompts = append(prompts, p) })
	sess.SetNotifyFunc(func() {})

	store := sess.getOrCreateGoalStore()
	store.Set("old objective", clk.Now())
	if !store.SetTerminal(goal.StatusBlocked, goal.VerdictBudgetExhausted, clk.Now()) {
		t.Fatal("precondition: SetTerminal should block")
	}
	// Idle session (no turn running): the resume kicks exactly once.
	started, err := sess.GoalResumeFromWire("new objective", "continuations", 50)
	if err != nil {
		t.Fatalf("wire resume with replacement = %v, want nil", err)
	}
	if !started {
		t.Fatal("idle wire resume with replacement must kick (rendering the NEW objective)")
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active", snap.Status)
	}
	if full, _ := store.GoalSnapshot(); full.Objective != "new objective" {
		t.Fatalf("objective = %q, want the replacement applied atomically", full.Objective)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "new objective") {
		t.Fatalf("kick prompts = %d, want exactly 1 rendering the NEW objective", len(prompts))
	}
	if strings.Contains(prompts[0], "old objective") {
		t.Fatalf("kick must never render the stale objective:\n%.200q...", prompts[0])
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
