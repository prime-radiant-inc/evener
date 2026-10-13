package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// The controller's intent channel (docs/specs/2026-10-11-delegate-latest-intent-
// design.md, "A separate intent channel"): ReportIntent journals the open
// generation's newest stated tool intent, keeps it on the live state, and
// publishes it with a five-second floor — while advancing no supervision
// clock at all. These tests pin the channel's controller contract; the
// delegatestore fold they ride is pinned in internal/delegatestore.

// newIntentChannelHarness seeds one running delegate on the controller
// harness, the same seed the activity publish tests use, and returns the
// journal path so a test can prove what did and did not append.
func newIntentChannelHarness(t *testing.T, turnLimit int) (*delegateTreeController, delegateLease, string) {
	t.Helper()
	c, path := newDelegateControllerTestHarness(t, turnLimit, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	return c, delegateLease{delegateID: "dlg_target", generation: 1}, path
}

// intentChannelFrames collects every published snapshot row.
type intentChannelFrames struct {
	rows []delegateSnapshot
}

func (log *intentChannelFrames) install(c *delegateTreeController) {
	c.emitUpdate = func(plan delegateUpdatePlan) {
		log.rows = append(log.rows, plan.rows...)
	}
}

// intentJournalLines counts the durable events the journal file holds — the
// direct proof an intent report did or did not append.
func intentJournalLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	return bytes.Count(raw, []byte{'\n'})
}

// liveIntentState copies a delegate's live state under the controller lock.
func liveIntentState(t *testing.T, c *delegateTreeController, delegateID string) delegateLiveState {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live[delegateID]
	if live == nil {
		t.Fatalf("delegate %s has no live state", delegateID)
	}
	return *live
}

// durableIntentState copies a delegate's folded aggregate under the lock.
func durableIntentState(t *testing.T, c *delegateTreeController, delegateID string) delegatestore.Aggregate {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		t.Fatalf("delegate %s has no folded state", delegateID)
	}
	return *aggregate
}

// TestReportIntentUnchangedFoldedIntentIsNoOp pins the channel's short-circuit:
// a report whose Excerpt-bounded value equals the folded intent appends nothing
// and publishes nothing, mirroring ReportActivityPhase's own no-op.
func TestReportIntentUnchangedFoldedIntentIsNoOp(t *testing.T) {
	c, lease, path := newIntentChannelHarness(t, 1)
	seedDelegateControllerRunning(t, c, "dlg_second", "")
	second := delegateLease{delegateID: "dlg_second", generation: 1}
	var frames intentChannelFrames
	frames.install(c)
	at := time.Unix(200, 0).UTC()

	if err := c.ReportIntent(lease, at, "Reading the board's row tests."); err != nil {
		t.Fatalf("first ReportIntent: %v", err)
	}
	if len(frames.rows) != 1 || frames.rows[0].latestIntent != "Reading the board's row tests." {
		t.Fatalf("first intent published %d rows, want one row carrying the stated line", len(frames.rows))
	}
	lines := intentJournalLines(t, path)
	revision := durableIntentState(t, c, lease.delegateID).ProjectionRevision

	if err := c.ReportIntent(lease, at.Add(time.Second), "Reading the board's row tests."); err != nil {
		t.Fatalf("unchanged ReportIntent: %v", err)
	}
	if got := intentJournalLines(t, path); got != lines {
		t.Fatalf("unchanged report appended %d journal lines, want 0", got-lines)
	}
	if len(frames.rows) != 1 {
		t.Fatalf("unchanged report published %d rows, want the one from before", len(frames.rows))
	}
	if got := durableIntentState(t, c, lease.delegateID).ProjectionRevision; got != revision {
		t.Fatalf("unchanged report advanced the projection revision from %d to %d", revision, got)
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != "Reading the board's row tests." {
		t.Fatalf("live intent = %q after an unchanged report", got)
	}

	// A report that states no intent against an empty folded line is the same
	// no-op: nothing appends, nothing publishes.
	if err := c.ReportIntent(second, at, ""); err != nil {
		t.Fatalf("empty ReportIntent against an empty line: %v", err)
	}
	if got := intentJournalLines(t, path); got != lines {
		t.Fatalf("empty-vs-empty report appended %d journal lines, want 0", got-lines)
	}
	if len(frames.rows) != 1 {
		t.Fatalf("empty-vs-empty report published %d rows, want the one from before", len(frames.rows))
	}
}

// TestReportIntentFailedAppendLeavesStateUntouched pins the stop-at-failure
// order: when the journal append fails, nothing else happens — the live state
// keeps the standing line, no frame publishes, the fold never sees the report.
func TestReportIntentFailedAppendLeavesStateUntouched(t *testing.T) {
	c, lease, path := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	at := time.Unix(200, 0).UTC()
	if err := c.ReportIntent(lease, at, "Reading the board's row tests."); err != nil {
		t.Fatalf("first ReportIntent: %v", err)
	}
	lines := intentJournalLines(t, path)

	if err := c.store.Close(); err != nil {
		t.Fatalf("close the delegate journal: %v", err)
	}
	if err := c.ReportIntent(lease, at.Add(time.Minute), "Fixing the parser."); err == nil {
		t.Fatal("ReportIntent succeeded against a closed journal")
	}
	if got := intentJournalLines(t, path); got != lines {
		t.Fatalf("failed report appended %d journal lines, want 0", got-lines)
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != "Reading the board's row tests." {
		t.Fatalf("failed report moved the live line to %q", got)
	}
	if got := durableIntentState(t, c, lease.delegateID).LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("failed report moved the folded line to %q", got)
	}
	if len(frames.rows) != 1 {
		t.Fatalf("failed report published %d rows, want the one from before", len(frames.rows))
	}
}

// TestReportIntentAdvancesNoSupervisionClock pins the spec's central
// divergence from ReportActivityPhase: intent is display-only state. Firing
// nothing but intent reports leaves the quiet baseline, the drain-grace clock
// and the quiet rearm byte-for-byte where they were — including while a quiet
// wake is pending, which any accepted activity report would rearm.
func TestReportIntentAdvancesNoSupervisionClock(t *testing.T) {
	c, lease, _ := newIntentChannelHarness(t, 1)
	base := time.Unix(150, 0).UTC()
	c.mu.Lock()
	live := c.live[lease.delegateID]
	live.activityAt = base
	live.productiveActivityAt = base
	live.quietSequence = 5
	live.quietNotified = true
	live.quietNotifiedAt = base
	c.mu.Unlock()

	report := func(at time.Time, intent string) {
		t.Helper()
		if err := c.ReportIntent(lease, at, intent); err != nil {
			t.Fatalf("ReportIntent(%q): %v", intent, err)
		}
	}
	report(base.Add(30*time.Second), "Reading the board's row tests.")
	report(base.Add(31*time.Second), "Fixing the parser.")
	report(base.Add(32*time.Second), "Running the suite.")

	after := liveIntentState(t, c, lease.delegateID)
	if !after.activityAt.Equal(base) {
		t.Fatalf("intent reports moved the quiet baseline: activityAt = %v, want %v", after.activityAt, base)
	}
	if !after.productiveActivityAt.Equal(base) {
		t.Fatalf("intent reports moved the drain clock: productiveActivityAt = %v, want %v", after.productiveActivityAt, base)
	}
	if after.quietNotified != true || after.quietSequence != 5 || !after.quietNotifiedAt.Equal(base) {
		t.Fatalf("intent reports rearmed the quiet cadence: notified=%t sequence=%d notifiedAt=%v, want the pending wake untouched", after.quietNotified, after.quietSequence, after.quietNotifiedAt)
	}
	if after.quietClaim != nil {
		t.Fatal("intent reports left a quiet claim behind")
	}

	// Control: an activity report in the same state does move every one of
	// those clocks, so the assertions above are not vacuous.
	if err := c.ReportActivity(lease, base.Add(40*time.Second)); err != nil {
		t.Fatalf("control ReportActivity: %v", err)
	}
	control := liveIntentState(t, c, lease.delegateID)
	if !control.activityAt.Equal(base.Add(40 * time.Second)) {
		t.Fatalf("control activity did not move the quiet baseline: activityAt = %v", control.activityAt)
	}
	if control.quietNotified || control.quietSequence != 6 {
		t.Fatalf("control activity did not rearm the quiet cadence: notified=%t sequence=%d", control.quietNotified, control.quietSequence)
	}
}

// TestReportIntentPublishesAfterQuiescentGapAndNeverDrops pins the amended
// publication floor: an intent change publishes immediately only once at least
// five seconds have passed since the last publication of any kind. Inside the
// window the change is journaled and held pending, and rides the next
// publication of any kind — the next activity report carries the newest line,
// never a dropped one.
func TestReportIntentPublishesAfterQuiescentGapAndNeverDrops(t *testing.T) {
	c, lease, path := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	t0 := time.Unix(300, 0).UTC()
	reportIntent := func(at time.Time, intent string) {
		t.Helper()
		if err := c.ReportIntent(lease, at, intent); err != nil {
			t.Fatalf("ReportIntent(%q): %v", intent, err)
		}
	}

	// The first change lands the moment the call starts: no publication stands.
	reportIntent(t0, "Reading the config file.")
	if len(frames.rows) != 1 || frames.rows[0].latestIntent != "Reading the config file." {
		t.Fatalf("first intent change published %d rows, want one carrying the line", len(frames.rows))
	}
	lines := intentJournalLines(t, path)

	// Inside the five-second window both further changes journal and hold.
	reportIntent(t0.Add(time.Second), "Fixing the parser.")
	reportIntent(t0.Add(2*time.Second), "Running the suite.")
	if len(frames.rows) != 1 {
		t.Fatalf("in-window changes published %d rows, want the one from before", len(frames.rows))
	}
	if got := intentJournalLines(t, path); got != lines+2 {
		t.Fatalf("in-window changes appended %d journal lines, want 2 (never dropped)", got-lines)
	}
	if got := durableIntentState(t, c, lease.delegateID).LatestIntent; got != "Running the suite." {
		t.Fatalf("newest in-window change did not reach the fold: %q", got)
	}

	// The pending line rides the next publication of any kind — an activity
	// report — carrying the newest value, not a dropped one.
	if err := c.ReportActivity(lease, t0.Add(3*time.Second)); err != nil {
		t.Fatalf("ReportActivity: %v", err)
	}
	if len(frames.rows) != 2 {
		t.Fatalf("activity publication after pending intent published %d rows, want 2", len(frames.rows))
	}
	if got := frames.rows[1].latestIntent; got != "Running the suite." {
		t.Fatalf("the riding publication carried %q, want the newest pending line", got)
	}
	if got := frames.rows[1].latestActivityAt; !got.Equal(t0.Add(3 * time.Second)) {
		t.Fatalf("the riding publication was not the activity report: latestActivityAt = %v", got)
	}

	// After a quiescent gap of exactly the floor, the next change publishes
	// immediately again.
	reportIntent(t0.Add(3*time.Second).Add(delegateIntentPublishFloor), "Updating the docs.")
	if len(frames.rows) != 3 {
		t.Fatalf("post-gap intent change published %d rows, want 3", len(frames.rows))
	}
	if got := frames.rows[2].latestIntent; got != "Updating the docs." {
		t.Fatalf("post-gap publication carried %q, want the new line", got)
	}
}

// TestReportIntentLandsAtCallStartBesideTheBatchReport pins the real
// call-start sequence: the lifecycle fires the batch-start activity report
// before the tool batch, and the stated intent arrives at the same instant.
// The floor must read the intent channel's own clock — the spec's outcome is
// that the intent lands the moment the call starts, the multi-minute-call case
// immediacy exists for — so the activity publication beside it cannot arm the
// window the intent would then wait out; during a single long call nothing
// else publishes, so a floor shared with the activity channel would shelve
// the line until the next event.
func TestReportIntentLandsAtCallStartBesideTheBatchReport(t *testing.T) {
	c, lease, _ := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	t0 := time.Unix(300, 0).UTC()

	// The batch-start activity report publishes: the run was quiet before it.
	if err := c.ReportActivity(lease, t0); err != nil {
		t.Fatalf("ReportActivity: %v", err)
	}
	// The stated intent at the same instant publishes as its own frame.
	if err := c.ReportIntent(lease, t0, "Reading the config file."); err != nil {
		t.Fatalf("ReportIntent: %v", err)
	}
	if len(frames.rows) != 2 {
		t.Fatalf("call start published %d rows, want the activity frame and the intent frame", len(frames.rows))
	}
	if got := frames.rows[len(frames.rows)-1].latestIntent; got != "Reading the config file." {
		t.Fatalf("the call-start frame carried %q, want the stated line", got)
	}

	// The burst bound still holds on the intent channel's own clock: a change
	// inside the floor journals and rides the next publication of any kind.
	if err := c.ReportIntent(lease, t0.Add(time.Second), "Fixing the parser."); err != nil {
		t.Fatalf("in-window ReportIntent: %v", err)
	}
	if len(frames.rows) != 2 {
		t.Fatalf("in-window change published %d rows, want the two from before", len(frames.rows))
	}
	if err := c.ReportActivity(lease, t0.Add(2*time.Second)); err != nil {
		t.Fatalf("riding ReportActivity: %v", err)
	}
	if len(frames.rows) != 3 {
		t.Fatalf("the ride published %d rows, want 3", len(frames.rows))
	}
	if got := frames.rows[len(frames.rows)-1].latestIntent; got != "Fixing the parser." {
		t.Fatalf("the riding frame carried %q, want the pending line", got)
	}
}

// TestNonResumableExhaustionTerminalFrameMatchesTheRead pins the closure
// path's terminal frame against the read after release: the closure plan
// captures its terminal rows before releaseGenerationLocked clears the live
// line, so an ungated live overlay would resurrect the settled run's intent
// in the terminal frame while the read after release shows it cleared.
func TestNonResumableExhaustionTerminalFrameMatchesTheRead(t *testing.T) {
	c, lease, _ := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	if err := c.ReportIntent(lease, time.Unix(300, 0).UTC(), "Reading the config file."); err != nil {
		t.Fatalf("ReportIntent: %v", err)
	}
	resumable := false
	finish := delegateFinish{
		outcome:             delegatestore.OutcomeExhausted,
		reason:              "turn_budget_exhausted",
		exhaustionBudget:    delegatestore.ExhaustionBudgetTurns,
		exhaustionLimit:     3,
		exhaustionResumable: &resumable,
	}
	plans, err := c.FinishGeneration(lease, finish)
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	var terminal *delegateSnapshot
	for i := range plans.updates {
		for j := range plans.updates[i].rows {
			if plans.updates[i].rows[j].id == lease.delegateID {
				terminal = &plans.updates[i].rows[j]
			}
		}
	}
	if terminal == nil {
		t.Fatal("the finish plans carry no terminal row for the delegate")
	}
	if terminal.latestIntent != "" {
		t.Fatalf("the terminal frame carried the settled run's intent %q, want the cleared line", terminal.latestIntent)
	}
	c.mu.Lock()
	read := c.captureDelegateSnapshotLocked(lease.delegateID)
	c.mu.Unlock()
	if read.latestIntent != terminal.latestIntent {
		t.Fatalf("the read showed %q while the terminal frame showed %q", read.latestIntent, terminal.latestIntent)
	}
}

// TestReportIntentRejectedWhileSettling pins the settling-window fence: through
// the documented settling window a report — statement or clear — is rejected
// exactly the way an activity report is, and the row keeps the pre-report
// intent for the settlement to retire.
func TestReportIntentRejectedWhileSettling(t *testing.T) {
	c, lease, path := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	at := time.Unix(200, 0).UTC()
	if err := c.ReportIntent(lease, at, "Reading the board's row tests."); err != nil {
		t.Fatalf("ReportIntent before settling: %v", err)
	}

	if _, _, err := c.prepareSettlementForTest(lease, nil); err != nil {
		t.Fatalf("prepareSettlementForTest: %v", err)
	}
	if got := durableIntentState(t, c, lease.delegateID).Phase; got != delegatestore.PhaseSettling {
		t.Fatalf("delegate phase = %s, want settling", got)
	}
	// Baseline after the settling preparation: its own terminal-prepared
	// append is not one of the reports under test.
	lines := intentJournalLines(t, path)

	if err := c.ReportIntent(lease, at.Add(time.Second), "Words that must not land."); err == nil {
		t.Fatal("ReportIntent was accepted while settling")
	} else if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("settling ReportIntent error = %v, want the same busy rejection activity reports take", err)
	}
	if err := c.ReportIntent(lease, at.Add(time.Second), ""); err == nil {
		t.Fatal("the settling clear was accepted")
	}
	if err := c.ReportActivity(lease, at.Add(time.Second)); err == nil {
		t.Fatal("control: activity reports are accepted while settling")
	}

	if got := intentJournalLines(t, path); got != lines {
		t.Fatalf("rejected reports appended %d journal lines, want 0", got-lines)
	}
	if len(frames.rows) != 1 {
		t.Fatalf("rejected reports published %d rows, want the one from before", len(frames.rows))
	}
	if got := durableIntentState(t, c, lease.delegateID).LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("rejected reports moved the folded line to %q", got)
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != "Reading the board's row tests." {
		t.Fatalf("rejected reports moved the live line to %q", got)
	}
}

// TestRestoredWindowFrameCarriesStandingFoldedIntent pins the frame builder's
// join: the frame applies the read's rule — the controller's live value when
// non-empty, else the folded value — so a frame published during the restored
// window (a fresh controller over the journal, live state re-bound with no
// in-process intent) carries the standing journaled line. A live-only frame
// builder fails this test.
func TestRestoredWindowFrameCarriesStandingFoldedIntent(t *testing.T) {
	c, lease, journalPath := newIntentChannelHarness(t, 1)
	at := time.Unix(200, 0).UTC()
	if err := c.ReportIntent(lease, at, "Reading the board's row tests."); err != nil {
		t.Fatalf("ReportIntent before the restart: %v", err)
	}

	// A real restart: a fresh controller folds the same journal, and the
	// restored window re-binds the live state with no in-process intent — the
	// zero value a restored delegateLiveState carries.
	restart := reopenIntentController(t, journalPath)
	restart.mu.Lock()
	restart.live[lease.delegateID] = &delegateLiveState{
		binding: &delegateRuntimeBinding{
			lease:    lease,
			cancel:   func() {},
			ready:    true,
			evidence: &delegateGenerationEvidence{requirement: delegateCompletionReportRequired},
		},
	}
	restart.mu.Unlock()
	if got := durableIntentState(t, restart, lease.delegateID).LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("the restart folded the intent as %q, want the journaled line", got)
	}

	var frames intentChannelFrames
	frames.install(restart)
	// An unrelated publication of any kind — here the first activity report —
	// must carry the standing folded intent.
	if err := restart.ReportActivity(lease, at); err != nil {
		t.Fatalf("ReportActivity in the restored window: %v", err)
	}
	if len(frames.rows) != 1 {
		t.Fatalf("the restored window published %d rows, want 1", len(frames.rows))
	}
	if got := frames.rows[0].latestIntent; got != "Reading the board's row tests." {
		t.Fatalf("the restored-window frame carried %q, want the standing folded line", got)
	}
	// The whole frame chain agrees: the pushed payload field reads the same
	// join as the snapshot row.
	data := delegateUpdatedDataFromStatus(delegateFrameStatusInfo(at, "root-session", frames.rows[0]))
	if data.LatestIntent != "Reading the board's row tests." {
		t.Fatalf("DelegateUpdatedData.LatestIntent = %q, want the standing folded line", data.LatestIntent)
	}
}

// TestRuntimeLostReconcileRetiresTheStatedIntent pins the restore lane's
// retirement: a lost runtime's reconcile settles the interrupted open
// generation with the failed outcome, and the run's settlement clears the
// stated line with it — the fold and the row the reconcile publishes both show
// a retired run with no line left standing.
func TestRuntimeLostReconcileRetiresTheStatedIntent(t *testing.T) {
	c, lease, journalPath := newIntentChannelHarness(t, 1)
	at := time.Unix(200, 0).UTC()
	if err := c.ReportIntent(lease, at, "Reading the board's row tests."); err != nil {
		t.Fatalf("ReportIntent before the restart: %v", err)
	}

	// The restart finds the generation open with its runtime gone: the same
	// journal, a fresh controller, and no live binding to reclaim the run.
	restart := reopenIntentController(t, journalPath)
	restart.mu.Lock()
	restart.reconcileOrder = []delegateLease{lease}
	_, reconcileErr := restart.reconcileRuntimeLostFromEvidenceLocked()
	restart.mu.Unlock()
	if reconcileErr != nil {
		t.Fatalf("runtime-lost reconcile: %v", reconcileErr)
	}

	aggregate := durableIntentState(t, restart, lease.delegateID)
	if aggregate.CurrentRunOpen || aggregate.Phase == delegatestore.PhaseRunning {
		t.Fatalf("the runtime-lost reconcile left the run open: %+v", aggregate)
	}
	if aggregate.LatestIntent != "" {
		t.Fatalf("the runtime-lost retirement left the stated line %q in the fold", aggregate.LatestIntent)
	}
}

// TestReportIntentRejectsStaleLease pins the channel's lease admission: only
// the open generation's running lease may state a line, so a report from any
// other generation is stale-lease-rejected exactly like an activity report,
// leaving the journal, the fold and the live line untouched.
func TestReportIntentRejectsStaleLease(t *testing.T) {
	c, lease, path := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	lines := intentJournalLines(t, path)
	stale := lease
	stale.generation++
	at := time.Unix(200, 0).UTC()
	intentErr := c.ReportIntent(stale, at, "From a stale lease.")
	activityErr := c.ReportActivity(stale, at)
	if intentErr == nil || activityErr == nil || intentErr.Error() != activityErr.Error() {
		t.Fatalf("stale-lease ReportIntent error = %v, want the activity channel's %v", intentErr, activityErr)
	}
	if got := intentJournalLines(t, path); got != lines {
		t.Fatalf("stale-lease report appended %d journal lines, want 0", got-lines)
	}
	if len(frames.rows) != 0 {
		t.Fatalf("stale-lease report published %d rows, want none", len(frames.rows))
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != "" {
		t.Fatalf("live intent = %q after a stale-lease report", got)
	}
	if got := durableIntentState(t, c, lease.delegateID).LatestIntent; got != "" {
		t.Fatalf("folded intent = %q after a stale-lease report", got)
	}
}

// reopenIntentController rebuilds the controller over the journal the harness
// wrote — a real restart: the same durable events, a fresh process's state.
func reopenIntentController(t *testing.T, journalPath string) *delegateTreeController {
	t.Helper()
	store, err := delegatestore.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen the delegate journal: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := filepath.Dir(journalPath)
	restart, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      root,
		worktreeRoot:  filepath.Join(root, "worktrees"),
		turnLimit:     1,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("reopen the controller over the journal: %v", err)
	}
	return restart
}

// TestRunBoundariesRetireTheIntentLine pins the two generation boundaries: run
// settlement clears the fold, the live line and the settled frame together,
// and a new generation starts with an empty line even if a stale one somehow
// survived the release — the per-generation discipline.
func TestRunBoundariesRetireTheIntentLine(t *testing.T) {
	c, lease, _ := newIntentChannelHarness(t, 2)
	at := time.Unix(200, 0).UTC()
	if err := c.ReportIntent(lease, at, "Reading the board's row tests."); err != nil {
		t.Fatalf("ReportIntent: %v", err)
	}

	plans, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "boundary test"})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	var settled *delegateSnapshot
	for i := range plans.updates {
		for _, row := range plans.updates[i].rows {
			if row.id == lease.delegateID {
				settled = &row
			}
		}
	}
	if settled == nil {
		t.Fatal("the finish plans carry no row for the delegate")
	}
	if settled.latestIntent != "" {
		t.Fatalf("the settled frame carries %q, want the retired line", settled.latestIntent)
	}
	if got := durableIntentState(t, c, lease.delegateID).LatestIntent; got != "" {
		t.Fatalf("run settlement left the folded line %q", got)
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != "" {
		t.Fatalf("run settlement left the live line %q", got)
	}

	// A resumed run never inherits the previous run's words. The release
	// already cleared the live line; poison it back to the state a missed
	// clear would leave, and prove the run-start re-baseline clears it.
	c.mu.Lock()
	c.live[lease.delegateID].intent = "Words from the run that ended."
	c.mu.Unlock()
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != "" {
		t.Fatalf("the resumed run inherited the line %q", got)
	}
	if len(started.plan.rows) != 1 || started.plan.rows[0].latestIntent != "" {
		t.Fatalf("the resumed run's first frame carries %q, want an empty line", started.plan.rows[0].latestIntent)
	}
}

// TestReportIntentBoundsTheLine pins the one bound rule at the channel's
// entry: the journal never holds an unbounded line, because Excerpt is the
// contract every projection of the field applies — the same bound the read and
// the projector apply, so push and read produce identical strings.
func TestReportIntentBoundsTheLine(t *testing.T) {
	c, lease, _ := newIntentChannelHarness(t, 1)
	var frames intentChannelFrames
	frames.install(c)
	long := "Reading  the\tvery   long\nboard's row tests"
	for len(long) < 2*appwire.MaxIntentRunes {
		long += " and then some more words"
	}
	want := appwire.Excerpt(long, appwire.MaxIntentRunes)

	if err := c.ReportIntent(lease, time.Unix(200, 0).UTC(), long); err != nil {
		t.Fatalf("ReportIntent: %v", err)
	}
	if got := durableIntentState(t, c, lease.delegateID).LatestIntent; got != want {
		t.Fatalf("the journal holds a different line than Excerpt's rule:\n got %q\nwant %q", got, want)
	}
	if got := liveIntentState(t, c, lease.delegateID).intent; got != want {
		t.Fatalf("the live line = %q, want the bounded one", got)
	}
	if len(frames.rows) != 1 || frames.rows[0].latestIntent != want {
		t.Fatalf("the published frame carries a different line than the bound rule: %q", frames.rows[0].latestIntent)
	}
	if runes := len([]rune(want)); runes > appwire.MaxIntentRunes {
		t.Fatalf("the bounded line is %d runes, want at most %d", runes, appwire.MaxIntentRunes)
	}
}
