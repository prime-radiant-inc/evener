package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/jobstore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

func TestRouteNoToolCalls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		kind              EntryKind
		priorToolActivity bool
		noContent         bool
		afterTerminal     bool
		want              noCallsRoute
	}{
		{name: "notification acknowledgement before tool activity", kind: EntryNotification, want: finishIdle},
		{name: "notification bare text after prior tool activity", kind: EntryNotification, priorToolActivity: true, want: runNoToolCalls},
		{name: "notification silence", kind: EntryNotification, noContent: true, want: runNoToolCalls},
		{name: "notification silence after terminal", kind: EntryNotification, noContent: true, afterTerminal: true, want: finishIdle},
		{name: "notification silence after terminal and prior tools", kind: EntryNotification, priorToolActivity: true, noContent: true, afterTerminal: true, want: finishIdle},
		{name: "notification bare text after terminal and prior tools", kind: EntryNotification, priorToolActivity: true, afterTerminal: true, want: finishIdle},
		{name: "user input", kind: EntryUserInput, want: runNoToolCalls},
		{name: "continuation", kind: EntryContinuation, want: runNoToolCalls},
		{name: "delegate attention", kind: EntryDelegateAttention, want: runNoToolCalls},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := routeNoToolCalls(tt.kind, tt.priorToolActivity, tt.noContent, tt.afterTerminal); got != tt.want {
				t.Fatalf("routeNoToolCalls() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDelegateTerminalCommunicateMarksGenerationEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_target")
	c.mu.Lock()
	runtime := c.live[lease.delegateID].binding.runtime
	c.mu.Unlock()
	runtime.profile = NewOpenAIProfile("gpt-5.2")

	reg := toolpkg.NewRegistry()
	_ = registerCommunicateTool(reg, newToolDeps(runtime))
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	if _, err := reg.Get("communicate").Exec(ctx, nil, map[string]any{
		"message":  "reported result",
		"end_turn": true,
	}); err != nil {
		t.Fatalf("communicate: %v", err)
	}
	if !runtime.communicated() || !strings.Contains(runtime.communicateOutput(), "reported result") {
		t.Fatalf("reported path changed: called=%t output=%q", runtime.communicated(), runtime.communicateOutput())
	}
	if recorded, err := c.recordAttentionNoAction(lease); err != nil || recorded {
		t.Fatalf("record no-action after terminal = recorded:%t err:%v, want refusal", recorded, err)
	}
	snapshot, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot: %v", err)
	}
	if !snapshot.terminalSeen || snapshot.outcome != delegateCompletionOutcomeNone {
		t.Fatalf("terminal evidence = %#v, want terminal-seen with no no-action outcome", snapshot)
	}
}

func TestDelegateResourceSupervision_AttentionBareTextRecordsExplicitNoAction(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	})
	armStableSupervisionAttention(t, sub, "attention:no-action", "inspect the completed work")
	waitForStableSupervisionRun(t, root, fixture.childID)
	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionAttentionOnly || snapshot.outcome != delegateCompletionOutcomeAttentionNoAction || snapshot.terminalSeen {
		t.Fatalf("bare attention evidence = %#v, want explicit attention no-action", snapshot)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm report plus one bare attention response", got)
	}
}

func TestDelegateResourceSupervision_AttentionFollowUpRequiresReport(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	var root *Session
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			if err := root.subagents.get(fixture.childID).sess.Enqueue(context.Background(), "queued follow-up work"); err != nil {
				t.Fatal(err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return agenttest.FinalResponse("follow-up report") },
	})
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	})
	armStableSupervisionAttention(t, sub, "attention:follow-up", "inspect before follow-up")
	waitForStableSupervisionRun(t, root, fixture.childID)

	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionReportRequired || !snapshot.terminalSeen {
		t.Fatalf("attention follow-up evidence = %#v, want report-required terminal", snapshot)
	}
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if finished.Disposition != delegatestore.DispositionReported || finished.DeliveryID == "" || aggregate.LatestPacket == nil || aggregate.LatestPacket.Kind != delegatestore.PacketReported {
		t.Fatalf("attention follow-up finish = %#v aggregate=%#v, want reported delivery", finished, aggregate)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 7 {
		t.Fatalf("provider requests = %d, want warm, attention, four follow-up attempts, and recovery report", got)
	}
}

// TestDelegateResourceSupervision_FinalTurnFinalizesWithNoRootWake pins the
// half of the finalization contract that would be a defect if it failed: a
// delegate whose final turn completes with nothing outstanding reaches its
// terminal phase on its own. It supplies no wake and installs no root wake
// consumer — it never calls serveSupervisionRootWakes, never runs a root turn,
// and arms no attention that a root retry would have to drive. Nothing but the
// run's own completion can move the row, so a green result here says
// finalization does not wait for an unrelated event.
//
// It also shows why the report's own wake is not the trigger: the report is
// delivered to a root that is never woken, and the row is terminal anyway.
// The run's tail holds finalization open only while the delegate owns
// outstanding work (drainForFinalization, agent/subagents.go); with no jobs
// there is nothing to hold it, so the tail reaches RunFinished directly. A
// tail that waited on a root wake instead would leave sub.finalizing set, and
// waitForStableSupervisionRun would refuse until its tripwire.
func TestDelegateResourceSupervision_FinalTurnFinalizesWithNoRootWake(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("final report") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	// Unserved: the warm run IS the terminal run under test, so there is no
	// attention arm here for the root's retry to own.
	warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Non-vacuity: the turn ran and answered terminally. A row that finalized
	// without ever reaching the provider would satisfy the phase check for the
	// wrong reason.
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("provider requests = %d, want the final turn's single request", got)
	}
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 1 || finished.Disposition != delegatestore.DispositionReported || finished.DeliveryID == "" {
		t.Fatalf("final turn finish = %#v, want generation 1's reported delivery", finished)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseIdle {
		t.Fatalf("finalized row = phase:%s current_run_open:%t, want the terminal idle row with no run open", aggregate.Phase, aggregate.CurrentRunOpen)
	}
	if aggregate.LatestPacket == nil || aggregate.LatestPacket.Kind != delegatestore.PacketReported {
		t.Fatalf("finalized row packet = %#v, want the delivered report", aggregate.LatestPacket)
	}
}

// TestDelegateResourceSupervision_CommittedAttentionStartRefusesASecondTurn
// pins the attention start hand-off. Between the committed start and the run
// goroutine that takes the generation over, sub.running is still false, the
// reservation is consumed, and the attention id is no longer pending -- so a
// wake-edge drive arriving in that gap declines the attention drive (nothing
// left to dispatch) and falls through to driveSubagentNotificationTurn, which
// reads the child as idle and launches a second, UNLEASED EntryNotification
// turn on the very session the generation is about to run.
//
// The two turns then share one session: whichever reaches its drain ladder
// first pops the follow-up the attention turn queued, and when that is the
// unleased turn the run never admits report-requiring work. The generation
// settles attention-only/no-action instead of report-required, which is the
// load-sensitive failure this pins:
//
//	attention follow-up evidence = {requirement:0x0 outcome:0x1 terminalSeen:false}
func TestDelegateResourceSupervision_CommittedAttentionStartRefusesASecondTurn(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	var root *Session
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			if err := root.subagents.get(fixture.childID).sess.Enqueue(context.Background(), "queued follow-up work"); err != nil {
				t.Fatal(err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return agenttest.FinalResponse("follow-up report") },
	})
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	})
	var handoffMu sync.Mutex
	var handoffSeen, secondTurnLaunched bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionStartCommitted = func(committed *subagent) {
			launched := root.driveSubagentNotificationTurn(committed)
			handoffMu.Lock()
			defer handoffMu.Unlock()
			if handoffSeen {
				return
			}
			handoffSeen, secondTurnLaunched = true, launched
		}
	})
	armStableSupervisionAttention(t, sub, "attention:committed-start", "inspect before follow-up")
	waitForStableSupervisionRun(t, root, fixture.childID)

	handoffMu.Lock()
	seen, launched := handoffSeen, secondTurnLaunched
	handoffMu.Unlock()
	if !seen {
		t.Fatal("the committed attention start hand-off was never observed")
	}
	if launched {
		t.Fatal("a committed attention start left the child drivable as a plain notification turn")
	}
	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionReportRequired || !snapshot.terminalSeen {
		t.Fatalf("committed-start attention evidence = %#v, want report-required terminal", snapshot)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 7 {
		t.Fatalf("provider requests = %d, want warm, attention, four follow-up attempts, and recovery report", got)
	}
}

// TestDelegateResourceSupervision_CommittedSendStartRefusesASecondTurn pins the
// send-path start hand-off (issue #940): the same committed-start window #932
// closed for attention starts also exists on the delegate send path. CommitStart
// consumes the reservation, but sub.running stays false through the restored
// side effects and the start-input mutation plans (BeginStartInput, preseedInput,
// CompleteStartInput), so a wake-edge drive arriving in that gap reads an idle
// child and driveChildIfNotStopGated falls through to
// driveSubagentNotificationTurn, which launches a second, UNLEASED
// EntryNotification turn on the session the generation is about to run. The two
// turns then share one drain ladder and the queue pop (a destructive pop with no
// owner check) lets the unleased turn steal the run's queued input. The send path
// must take the drive claim atomically with its drivability check and hold it
// across the whole window, so the child refuses the drive-down.
func TestDelegateResourceSupervision_CommittedSendStartRefusesASecondTurn(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("send result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var handoffMu sync.Mutex
	var handoffSeen, secondTurnLaunched bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateSendStartCommitted = func(committed *subagent) {
			launched := root.driveSubagentNotificationTurn(committed)
			handoffMu.Lock()
			defer handoffMu.Unlock()
			if handoffSeen {
				return
			}
			handoffSeen, secondTurnLaunched = true, launched
		}
	})
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "committed send start", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("committed send start = %+v, want started", outcome.result)
	}

	handoffMu.Lock()
	seen, launched := handoffSeen, secondTurnLaunched
	handoffMu.Unlock()
	if !seen {
		t.Fatal("the committed send start hand-off was never observed")
	}
	if launched {
		t.Fatal("a committed send start left the child drivable as a plain notification turn")
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
}

// TestDelegateResourceSupervision_EarlyCommittedSendStartRefusesASecondTurn
// pins the EARLIEST stretch of the send-start window (#940): commit -> child
// resolution. The late delegateSendStartCommitted seam fires only after
// restoreIdleForSend has produced the child AND the per-subagent `driving`
// claim is held, so it proves nothing about the stretch the issue names:
// CommitStart -> restoreIdleForSend -> admitReconstructed/AttachRuntime. This
// test drives the child from a seam placed immediately after CommitStart, while
// the child object is not yet resolved, and asserts the id-keyed claim refuses
// the drive: the retained idle child never reads drivable and no second,
// unleased EntryNotification turn launches on it.
func TestDelegateResourceSupervision_EarlyCommittedSendStartRefusesASecondTurn(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("send result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var handoffMu sync.Mutex
	var claimSeen, claimHeld, candidateFound, secondTurnLaunched bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateSendStartClaimed = func(childSessionID string) {
			// Resolve the retained idle child by the id the claim is keyed by,
			// exactly as the wake edge would before the child is re-resolved.
			candidate := root.subagents.get(childSessionID)
			handoffMu.Lock()
			defer handoffMu.Unlock()
			if claimSeen {
				return
			}
			claimSeen = true
			claimHeld = root.childCommittedSendStart(childSessionID)
			if candidate == nil {
				return
			}
			candidateFound = true
			// This is the drive the wake edge would launch. With the id-keyed
			// claim held the guard must refuse it; without the claim the idle
			// child passes and this launches a second, unleased turn.
			secondTurnLaunched = root.driveSubagentNotificationTurn(candidate)
		}
	})
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "early committed send start", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("early committed send start = %+v, want started", outcome.result)
	}

	handoffMu.Lock()
	seen, held, found, launched := claimSeen, claimHeld, candidateFound, secondTurnLaunched
	handoffMu.Unlock()
	if !seen {
		t.Fatal("the early committed send start claim was never observed")
	}
	if !found {
		t.Fatal("the retained idle child was not resident at the early committed send start")
	}
	if !held {
		t.Fatal("the committed send start did not hold the id-keyed claim before the child was resolved")
	}
	if launched {
		t.Fatal("an early committed send start left the child drivable as a plain notification turn")
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm plus the one send turn", got)
	}
}

// TestDelegateResourceSupervision_UnleasedTurnWithholdsWatchAttentionOnly pins
// both directions of a stable delegate's unleased admission.
//
// Withholding: a pending watch row is durable attention a leased generation
// presents, and the unleased projection keeps it out of the request the child
// prepares. Carrying: ordinary work the child owns is not watch work and still
// runs, because a guard that suppressed the whole unleased turn is WORSE than
// the bug it would fix -- it strands the completion drives and restored-delegate
// tasks the same code path serves.
//
// The two directions are one test on purpose. Only both together describe the
// invariant: withholding the watch row may not cost the child its ordinary work.
func TestDelegateResourceSupervision_UnleasedTurnWithholdsWatchAttentionOnly(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Preparation is not a pure builder: its context-management phase elicits a
	// compaction note and summarizes history, both provider calls. The child is
	// put under enough pressure that the phase runs, so the assertions below are
	// about a request that really was prepared rather than one short-circuited
	// before the history was built.
	seedSessionHistory(t, sub.sess, 10)
	forcePressureAbove(t, sub.sess, sub.sess.contextMgr.CheckpointThreshold)

	// DIRECTION ONE -- the notice never reaches an unleased request. The case is
	// the issue's own: a watch notice on the child's queue while an unleased turn
	// prepares. Asserted structurally -- the notice is ABSENT from the request
	// history and PRESENT as pending durable attention -- never by message text.
	//
	// Both surfaces matter and pin different bugs. Asking only about the queue is
	// the wrong surface: a deferred notice has been APPENDED to resident history,
	// so the queue reads empty while the request expands exactly the turn the
	// projection exists to withhold. Asking only about the durable row pins the
	// write and misses the presentation.
	// The notice is made durable attention WITHOUT arming its wake, so this proof
	// cannot race the leased generation the arm would start: the row is still
	// pending when the unleased request is prepared, which is precisely the
	// window the finding names.
	queued := jobNotification{
		Kind:   jobNotificationKindWatch,
		JobID:  "job:unleased-admission",
		Status: jobNotificationEventWatch,
		Reason: "watch send failed",
	}
	wantID := deferredWatchAttentionID(queued)
	notice := sub.sess.formatJobNotificationReminder([]deliverableJobNotification{{notification: queued}})
	if _, err := sub.sess.appendDelegateNotificationDurably(wantID, notice); err != nil {
		t.Fatalf("append the deferred watch notice: %v", err)
	}
	fold, foldErr := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if foldErr != nil {
		t.Fatalf("read the child's attention fold: %v", foldErr)
	}
	if !slices.Contains(fold.pendingIDs(), wantID) {
		t.Fatalf("pending attention rows = %v, want %q: the notice is not the pending item this proof needs", fold.pendingIDs(), wantID)
	}
	_, _, history, _, _, _, err := sub.sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("unleased model request on the stable child = %v, want it to carry its ordinary work", err)
	}
	for _, msg := range history {
		if strings.Contains(msg.Text(), "job:unleased-admission") {
			t.Fatal("the unleased request presents the watch notice: the durable row was appended to history and then expanded into the request")
		}
	}
	// Settled history is PRESERVED: a row a leased generation already presented
	// is conversation, not withheld work, and holding it back would rewrite the
	// child's own history.
	if kept, dropped := sub.sess.dropUnleasedWatchAttentionTurns(context.Background(), []schema.Turn{
		{Kind: schema.TurnSteering, AttentionID: "delegate:already-presented"},
	}); len(dropped) != 0 || len(kept) != 1 {
		t.Fatalf("the unleased projection dropped %d settled row(s), want it to preserve conversation the child already holds", len(dropped))
	}
	sub.sess.drainJobNotifications()

	// DIRECTION TWO -- unleased work that is NOT watch work still runs. This is
	// the assertion a "refuse everything without a lease" guard fails, and that
	// guard is worse than the bug: it strands every legitimate unleased path the
	// audit says to preserve (a restored delegate's first task, a completion
	// drive). An ordinary terminal completion is the sharpest case, because it is
	// the one a stable child must still be able to consume.
	seedOwnedDurablePending(t, sub.sess.jobManager, "job_ordinary_unleased", jobstore.JobShell)
	sub.sess.enqueueJobNotification(jobNotification{
		JobID:   "job_ordinary_unleased",
		JobType: "shell",
		Status:  "completed",
	})
	if queuedWatchNotices(t, sub.sess) != 0 {
		t.Fatal("an ordinary completion is being read as a watch notice")
	}
	markProcessing(sub.sess)
	if !sub.sess.acceptNotificationInput(context.Background(), "unleased-ordinary") {
		t.Fatal("an unleased turn carrying an ordinary completion was refused: the guard broke the fallback it must preserve")
	}
}

// TestDelegateResourceSupervision_LateWatchNoticeNeverReachesAnUnleasedRequest
// pins the round-boundary write.
//
// A watch notice can arrive AFTER a stable child's unleased turn has accepted its
// opening batch and BEFORE the next request is prepared. injectLaterJobNotifications
// is the write that sees it there, so the deferral has to be that same shared
// code: a check that only looked at opening acceptance would leave this arrival
// on the queue, and the request that follows would present it outside a lease.
//
// The assertion is the DURABLE ATTENTION ROW plus the emptied queue, not the
// message text: the notice must be owed to a leased generation and absent from
// the unleased turn's reminder.
func TestDelegateResourceSupervision_LateWatchNoticeNeverReachesAnUnleasedRequest(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	late := watchSendDiagnosticNotification("watch:late", "job:late", "watch send failed: delivery_id=late")
	if !sub.sess.stableChildTurnNeedsLease(context.Background()) {
		t.Fatal("the child is not the unleased stable-turn case this test needs")
	}
	// Hold the wake the deferral arms. The wake drives a leased generation
	// synchronously, and that generation presents the row: running it here would
	// measure the leased presentation instead of the unleased request this test
	// is about. Held, the row stays pending across that request, which is
	// exactly the window the projection has to cover.
	sub.sess.mu.Lock()
	previousNotify := sub.sess.notifyFunc
	sub.sess.mu.Unlock()
	var wakes atomic.Int64
	sub.sess.SetNotifyFunc(func() { wakes.Add(1) })
	t.Cleanup(func() { sub.sess.SetNotifyFunc(previousNotify) })

	// The round boundary: this is exactly what the round loop calls before each
	// request, and it is where the late notice would otherwise be written.
	sub.sess.enqueueJobNotification(late)
	sub.sess.injectLaterJobNotifications(context.Background())
	if wakes.Load() == 0 {
		t.Fatal("the deferral armed no wake: no leased generation would ever present the row")
	}

	// The notice is not on the queue (it cannot ride an unleased turn)...
	if got := queuedWatchNotices(t, sub.sess); got != 0 {
		t.Fatal("the late watch notice is still on the ordinary queue after the round-boundary write")
	}
	// ...and it is DURABLE, so a leased generation still presents it.
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	wantID := deferredWatchAttentionID(late)
	found := false
	for _, id := range fold.order {
		if id == wantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("child attention rows = %v, want the late notice under %q: it was neither carried nor deferred", fold.order, wantID)
	}
	// And the unleased request that follows is admitted without the notice: with
	// nothing queued there is no unleased watch turn to prepare, so the request
	// proceeds and its history carries no watch reminder.
	_, _, history, _, _, _, err := sub.sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("unleased request after the round-boundary write = %v, want it admitted", err)
	}
	for _, msg := range history {
		if strings.Contains(msg.Text(), wantID) || strings.Contains(msg.Text(), "job:late") {
			t.Fatal("the unleased request presents the late watch notice: the boundary write left it deliverable")
		}
	}
}

// TestDelegateResourceSupervision_MixedBatchCarriesTheCompletionAndDefersTheWatch
// pins the MIXED batch. A recovered shell completion and a deliverable watch
// notice can share a stable child's queue. The completion has no lease to inherit
// but is perfectly deliverable, so the turn must carry it; the watch notice must
// not ride along, and must not be dropped either.
//
// A gate that refuses the whole batch passes the watch half and fails here: every
// retry sees the same pair, so the completion is never delivered until some
// unrelated generation intervenes. That is the failure mode this test exists for.
func TestDelegateResourceSupervision_MixedBatchCarriesTheCompletionAndDefersTheWatch(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// A deliverable ordinary completion beside a watch notice, both owed.
	seedOwnedDurablePending(t, sub.sess.jobManager, "job_mixed_completion", jobstore.JobShell)
	sub.sess.enqueueJobNotification(jobNotification{
		JobID:   "job_mixed_completion",
		JobType: "shell",
		Status:  "completed",
	})
	sub.sess.enqueueJobNotification(watchSendDiagnosticNotification("watch:mixed", "job:mixed", "watch send failed: delivery_id=mixed"))
	if queuedWatchNotices(t, sub.sess) != 1 || queuedOrdinaryNotices(t, sub.sess) != 1 {
		t.Fatal("the mixed batch did not land as one watch notice plus one ordinary completion")
	}
	foldBefore, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold before the turn: %v", err)
	}

	// The unleased turn is ADMITTED: the completion is real work it owns.
	markProcessing(sub.sess)
	if !sub.sess.acceptNotificationInput(context.Background(), "unleased-mixed") {
		t.Fatal("a mixed batch was refused whole: the ordinary completion can never be delivered on any retry")
	}

	// The watch notice left the ordinary queue: it can no longer ride an unleased
	// turn.
	if got := queuedWatchNotices(t, sub.sess); got != 0 {
		t.Fatal("the watch notice is still on the ordinary queue after the mixed turn")
	}
	// And it is DURABLE, not dropped: the child's own attention fold holds a row
	// under the deferral identity this half owns. The fold is the model-visible
	// surface, so a row there is what a leased generation presents.
	foldAfter, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold after the turn: %v", err)
	}
	if len(foldAfter.order) <= len(foldBefore.order) {
		t.Fatalf("child attention rows = %v, want the deferred watch notice appended (was %v)", foldAfter.order, foldBefore.order)
	}
	if !slices.ContainsFunc(foldAfter.order, func(id string) bool {
		return strings.HasPrefix(id, deferredWatchAttentionPrefix)
	}) {
		t.Fatalf("child attention rows = %v, want one under the deferral identity", foldAfter.order)
	}
}

// TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesDroppedWake
// pins the deferred-wake regression the id-keyed committed-send-start claim
// introduced (#940). While the claim is held a wake-edge drive refuses, so a
// child notification that arrives in the window is DROPPED: its notify runs
// driveChildIfNotStopGated, which returns early on the claim, and nothing else
// re-triggers the drive. On the hand-off path the run about to launch drains the
// child's queue, so the drop is harmless. But when the send FAILS after taking
// the claim -- here its start reservation is aborted before the commit -- no run
// is handed over, and unless the non-handoff rollback re-drives the child's
// undelivered attention the queued notification sits undriven forever.
//
// The notice is an ORDINARY terminal completion, not a watch delivery. A watch
// notice is the one payload a stable child's unleased turn must refuse (LIFE-01,
// #2340), so seeding one here would pin the drop against a drive the gate is
// required to deny. What this test pins is the rollback's re-drive, which every
// other kind of queued notification still needs, so the payload is the kind the
// gate permits: a terminal shell completion with a durable pending record, which
// is what makes it deliverable at all.
//
// This test is load-bearing for that fix: with the rollback's re-drive removed,
// the notification enqueued while the claim was held is never drained and the
// child never runs a second turn.
func TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesDroppedWake(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("drained notification") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var mu sync.Mutex
	var claimSeen, wakeDropped bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateSendStartClaimed = func(childSessionID string) {
			candidate := root.subagents.get(childSessionID)
			if candidate == nil || candidate.sess == nil {
				return
			}
			// A child notification arrives with the claim held: enqueue it and
			// fire the child's notify exactly as a real arrival would. The wake
			// edge reads the claim and refuses, leaving the queue undelivered.
			seedOwnedDurablePending(t, candidate.sess.jobManager, "job_redrive_dropped_wake", jobstore.JobShell)
			candidate.sess.enqueueJobNotification(jobNotification{
				JobID:   "job_redrive_dropped_wake",
				JobType: "shell",
				Status:  "completed",
			})
			candidate.sess.notify()
			mu.Lock()
			claimSeen = true
			wakeDropped = root.childCommittedSendStart(childSessionID) &&
				root.subagents.get(childSessionID).sess.peekNotifications() > 0
			mu.Unlock()
			// Fail the send after the claim: abort the in-flight reservation so
			// the commit below cannot hand a run over.
			abortDelegateStartReservation(root, fixture.delegateID)
		}
	})

	warmRequests := supervisionRequestCount(fixture.adapter)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "must not launch", 1000)
	if outcome.result.Err == nil {
		t.Fatalf("aborted committed send start = %#v, want refusal", outcome.result)
	}

	mu.Lock()
	seen, dropped := claimSeen, wakeDropped
	mu.Unlock()
	if !seen {
		t.Fatal("the committed send start claim was never observed")
	}
	if !dropped {
		t.Fatal("the claim did not leave the child's queued notification undelivered")
	}

	// The rollback's re-drive is now the only thing that can drain the queue.
	testwait.Until(t, "dropped notification redriven after the rollback", func() bool {
		return supervisionRequestCount(fixture.adapter) > warmRequests
	})
	waitForStableSupervisionRun(t, root, fixture.childID)
	if pending := sub.sess.peekNotifications(); pending != 0 {
		t.Fatalf("child notifications pending after rollback re-drive = %d, want 0", pending)
	}
	if got := supervisionRequestCount(fixture.adapter); got != warmRequests+1 {
		t.Fatalf("provider requests = %d, want warm plus one notification-drive turn", got)
	}
}

// TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesDroppedAttention
// pins the stable-delegate-attention half of the deferred-wake regression the
// id-keyed committed-send-start claim introduced (#940). Attention wakes do not
// travel the notification path: an armed attention reaches
// driveDelegateAttention through the child's notify, and the claim makes
// that return early. So attention armed while the claim is held is dropped
// exactly like a queued notification, and driveChildrenWithUndeliveredAttention
// does not see it. When the send FAILS after taking the claim -- here its start
// reservation is aborted before the commit -- no run is handed over, and unless
// the rollback also re-drives pending stable attention the armed attention sits
// undriven forever.
//
// This test is load-bearing for that fix: with the rollback's attention re-drive
// removed, the attention armed while the claim was held is never driven and the
// child never runs a second turn.
func TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesDroppedAttention(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var mu sync.Mutex
	var claimSeen, wakeDropped bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateSendStartClaimed = func(childSessionID string) {
			candidate := root.subagents.get(childSessionID)
			if candidate == nil || candidate.sess == nil {
				return
			}
			// A stable attention is armed with the claim held: arming notifies,
			// the child's notify runs driveDelegateAttention, and the claim
			// refuses it, leaving the attention pending and undriven.
			armStableSupervisionAttention(t, candidate, "attention:redrive-dropped-wake", "inspect before the drive is admitted")
			pending, err := candidate.sess.pendingDelegateAttentionIDs()
			if err != nil {
				t.Errorf("inspect armed attention: %v", err)
			}
			mu.Lock()
			claimSeen = true
			wakeDropped = root.childCommittedSendStart(childSessionID) && len(pending) > 0
			mu.Unlock()
			// Fail the send after the claim: abort the in-flight reservation so
			// the commit below cannot hand a run over.
			abortDelegateStartReservation(root, fixture.delegateID)
		}
	})

	warmRequests := supervisionRequestCount(fixture.adapter)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "must not launch", 1000)
	if outcome.result.Err == nil {
		t.Fatalf("aborted committed send start = %#v, want refusal", outcome.result)
	}

	mu.Lock()
	seen, dropped := claimSeen, wakeDropped
	mu.Unlock()
	if !seen {
		t.Fatal("the committed send start claim was never observed")
	}
	if !dropped {
		t.Fatal("the claim did not leave the armed attention undriven")
	}

	// The rollback's attention re-drive is now the only thing that can drive the
	// armed attention.
	testwait.Until(t, "dropped attention redriven after the rollback", func() bool {
		return supervisionRequestCount(fixture.adapter) > warmRequests
	})
	waitForStableSupervisionRun(t, root, fixture.childID)
	pending, err := sub.sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("inspect pending attention after rollback re-drive: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("child attention pending after rollback re-drive = %v, want none", pending)
	}
	if got := supervisionRequestCount(fixture.adapter); got != warmRequests+1 {
		t.Fatalf("provider requests = %d, want warm plus one attention-drive turn", got)
	}
}

// TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesBoth
// pins the ORDERING half of the deferred-wake fix (#940): when BOTH a child
// notification and armed stable attention land inside the committed-send-start
// window, the rollback's re-drive must not strand one of them. The previous
// inline order drove the notification turn first; driveSubagentNotificationTurn
// sets sub.driving synchronously before it returns, so the immediate
// driveDelegateAttention refused on that flag, and nothing retried it --
// the notification turn's own re-drive check looks only at notifications, not
// armed attention. The rollback now shares the wake edge's order
// (driveChildIfNotStopGated: stable attention FIRST, whose run drains the
// notification queue, then the notification turn), so neither stays pending.
//
// With the pre-fix order this test's final wait never sees the attention drain
// and times out: that is the load-bearing falsification.
func TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesBoth(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("drained notification") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var mu sync.Mutex
	var claimSeen, bothPending bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateSendStartClaimed = func(childSessionID string) {
			candidate := root.subagents.get(childSessionID)
			if candidate == nil || candidate.sess == nil {
				return
			}
			// Both kinds of dropped wake arrive with the claim held: a queued
			// child notification fires the wake edge, the armed attention fires
			// it again, and the claim refuses each.
			candidate.sess.enqueueJobNotification(jobNotification{
				Kind:   jobNotificationKindWatch,
				JobID:  "redrive-both-wake",
				Status: jobNotificationEventWatch,
			})
			candidate.sess.notify()
			armStableSupervisionAttention(t, candidate, "attention:redrive-both", "inspect before the drive is admitted")
			pending, err := candidate.sess.pendingDelegateAttentionIDs()
			if err != nil {
				t.Errorf("inspect armed attention: %v", err)
			}
			mu.Lock()
			claimSeen = true
			bothPending = root.childCommittedSendStart(childSessionID) &&
				candidate.sess.peekNotifications() > 0 && len(pending) > 0
			mu.Unlock()
			// Fail the send after the claim so the non-handoff rollback owns the
			// re-drive.
			abortDelegateStartReservation(root, fixture.delegateID)
		}
	})

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "must not launch", 1000)
	if outcome.result.Err == nil {
		t.Fatalf("aborted committed send start = %#v, want refusal", outcome.result)
	}

	mu.Lock()
	seen, pendingBoth := claimSeen, bothPending
	mu.Unlock()
	if !seen {
		t.Fatal("the committed send start claim was never observed")
	}
	if !pendingBoth {
		t.Fatal("the claim did not leave both a notification and the armed attention pending")
	}

	// With the fix the attention is driven first and its run drains the
	// notification, so both queues empty. With the pre-fix notification-first
	// order the attention stays pending and this bounds out.
	testwait.Until(t, "attention and notification both redriven after the rollback", func() bool {
		pending, err := sub.sess.pendingDelegateAttentionIDs()
		if err != nil {
			return false
		}
		return len(pending) == 0 && sub.sess.peekNotifications() == 0
	})
	waitForStableSupervisionRun(t, root, fixture.childID)
}

// TestDelegateResourceSupervision_SendRefusedWhileChildFinalizing pins the
// delegate send admission guard (#940 review): a send must refuse busy while
// the target child's finalizer is still live, exactly as the sibling drive
// guard in driveDelegateAttention already does. `running` goes false at
// the top of the run's finalize block, before FinishGeneration moves the
// aggregate back to idle and before finalizing is cleared, so a send landing in
// that window would otherwise pass a guard that checks only running/driving,
// set driving, and start a run concurrently with the in-flight finalizer.
//
// The reachable idle+finalizing ordering has no test seam: the
// subagentAfterFinalStatePublish hook fires BEFORE FinishGeneration, so the
// aggregate still reads Running there and a send is refused by ReserveStart
// before it ever reaches this guard. This test therefore sets the finalizing
// flag on the quiescent retained child directly -- the guard under test reads
// exactly this flag under sub.mu, so the state is faithful to the race window
// even though it is installed rather than raced.
func TestDelegateResourceSupervision_SendRefusedWhileChildFinalizing(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("must not have started") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	sub.mu.Lock()
	if sub.running || sub.driving {
		sub.mu.Unlock()
		t.Fatal("retained child is not quiescent before the finalizing pin")
	}
	sub.finalizing = true
	sub.mu.Unlock()

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "must be refused while finalizing", 0)
	sub.mu.Lock()
	sub.finalizing = false
	sub.mu.Unlock()

	if !errors.Is(outcome.result.Err, errDelegateTargetBusy) {
		t.Fatalf("send while child finalizing = %+v, want target busy", outcome.result)
	}
	if outcome.result.Action == "started" || outcome.result.Action == "steered" {
		t.Fatalf("send while child finalizing action = %q, want a busy refusal", outcome.result.Action)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
}

// abortDelegateStartReservation aborts the delegate's outstanding start
// reservation from inside a test seam, forcing the in-flight send down a
// non-handoff exit after it has taken the committed-send-start claim.
func abortDelegateStartReservation(root *Session, delegateID string) {
	c := root.delegateController
	c.mu.Lock()
	var receipt *delegateStartReservation
	for _, record := range c.reservations {
		if record != nil && record.delegateID == delegateID {
			receipt = record.receipt
			break
		}
	}
	c.mu.Unlock()
	if receipt != nil {
		_ = c.AbortStart(receipt)
	}
}

// TestDelegateResourceSupervision_CommittedSendStartContentionRefusesSecondSend
// pins the contention path on the SAME child session id: while one send for the
// delegate holds the committed-send-start window, a second send for that same
// child must refuse busy and must not launch a second run. The second send is
// issued from the first send's claim seam, so it lands with the first
// reservation outstanding and the claim held.
//
// Reachability (#940): this is the reachable contention path. The
// claimChildCommittedSendStart refusal branch in delegateRuntime.send is a
// defensive guard that no public call sequence can enter:
//   - ReserveStart refuses a second reservation for one delegateID
//     (delegate_tree_start.go "for _, existing := range c.reservations"),
//   - the run-started event moves the aggregate to PhaseRunning, so ReserveStart
//     refuses again between CommitStart and the hand-off (fold.go PhaseRunning),
//   - child session ids are minted uniquely per delegate
//     (delegate_tree_start.go ReserveCreate "identifier.MustNewSessionID"), so
//     two different delegates can never share the claim's key.
//
// The busy refusal asserted here is therefore produced by ReserveStart's
// reservation guard, not by the claim branch. The claim branch is still correct
// as defence in depth, but it cannot be driven from the public API.
func TestDelegateResourceSupervision_CommittedSendStartContentionRefusesSecondSend(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("contended send result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var mu sync.Mutex
	var second sendMessageResult
	var secondSeen bool
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateSendStartClaimed = func(string) {
			mu.Lock()
			defer mu.Unlock()
			if secondSeen {
				return
			}
			secondSeen = true
			// A concurrent positive-wait send for the same child, landing with
			// the first reservation outstanding. It must refuse busy rather than
			// start a second run on this session.
			second = (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "contending send", 1000).result
		}
	})

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "winning send", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("winning send = %#v, want started", outcome.result)
	}

	mu.Lock()
	seen := secondSeen
	contended := second
	mu.Unlock()
	if !seen {
		t.Fatal("the committed send start claim was never observed")
	}
	if !errors.Is(contended.Err, errDelegateTargetBusy) {
		t.Fatalf("contending send error = %v, want target busy", contended.Err)
	}
	if contended.Action == "started" || contended.Action == "steered" {
		t.Fatalf("contending send action = %q, want a busy refusal", contended.Action)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	// Only the warm run and the winning send's run were launched.
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm plus exactly one contended winner run", got)
	}
}

func TestDelegateResourceSupervision_AttentionGoalContinuationRequiresReport(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	var root *Session
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			started, err := root.subagents.get(fixture.childID).sess.SetGoal(context.Background(), "finish continuation work")
			if err != nil || started {
				t.Errorf("SetGoal during attention = started:%t err:%v, want deferred", started, err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return agenttest.FinalResponse("continuation report") },
	})
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	})
	armStableSupervisionAttention(t, sub, "attention:goal", "inspect before continuation")
	waitForStableSupervisionRun(t, root, fixture.childID)

	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionReportRequired || !snapshot.terminalSeen {
		t.Fatalf("attention continuation evidence = %#v, want report-required terminal", snapshot)
	}
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if finished.Disposition != delegatestore.DispositionReported || finished.DeliveryID == "" || aggregate.LatestPacket == nil || aggregate.LatestPacket.Kind != delegatestore.PacketReported {
		t.Fatalf("attention continuation finish = %#v aggregate=%#v, want reported delivery", finished, aggregate)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 7 {
		t.Fatalf("provider requests = %d, want warm, attention, four continuation attempts, and recovery report", got)
	}
}

func TestDelegateResourceSupervision_AttentionNotificationRemainsNoAction(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	var root *Session
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			root.subagents.get(fixture.childID).sess.enqueueJobNotification(jobNotification{
				Kind:   jobNotificationKindWatch,
				JobID:  "system-only",
				Status: jobNotificationEventWatch,
			})
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("notification acknowledged")}
		},
	})
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	})
	armStableSupervisionAttention(t, sub, "attention:notification", "inspect before notification")
	waitForStableSupervisionRun(t, root, fixture.childID)

	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionAttentionOnly || snapshot.outcome != delegateCompletionOutcomeAttentionNoAction || snapshot.terminalSeen {
		t.Fatalf("system-only attention evidence = %#v, want attention no-action", snapshot)
	}
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("system-only attention finish = %#v, want private no-action", finished)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests = %d, want warm, attention, and notification only", got)
	}
}

func TestDelegateResourceSupervision_BareShellAttentionCompletesNoActionWithoutSecondReport(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)

	shell := createDelegateShell(t, sub.sess.jobManager, "bare attention incident")
	finishDelegateShell(t, sub.sess.jobManager, shell.JobID)
	waitForStableSupervisionRun(t, root, fixture.childID)

	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm report plus one bare shell-attention response", got)
	}
	stored := loadStableShellRecord(t, sub.sess.jobManager, shell.JobID)
	attentionID := stableShellAttentionID(shell.JobID, stored.TerminalGen)
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, sub.sess.ID()), sub.sess.ID())
	if err != nil {
		t.Fatalf("read shell attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("shell attention %q resolution = %q, want consumed", attentionID, got)
	}

	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted ||
		finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.Packet != nil || finished.DeliveryID != "" ||
		len(aggregate.PendingDeliveries) != 0 {
		t.Fatalf("private no-action completion = finished:%#v aggregate:%#v", finished, aggregate)
	}
	parentPending, err := readPendingDelegateAttention(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read parent attention: %v", err)
	}
	if len(parentPending) != 0 {
		t.Fatalf("parent pending attention = %#v, want no second result notification", parentPending)
	}
}

func TestDelegateResourceSupervision_ExplicitAttentionCommunicateRemainsReported(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("attention report") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	})
	armStableSupervisionAttention(t, sub, "attention:reported", "report the completed work")
	waitForStableSupervisionRun(t, root, fixture.childID)
	snapshot := <-snapshots
	if !snapshot.terminalSeen || snapshot.outcome != delegateCompletionOutcomeNone {
		t.Fatalf("explicit attention evidence = %#v, want existing reported path", snapshot)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want no recovery after explicit attention communicate", got)
	}
}

func TestDelegateResourceSupervision_UserRunWithoutCommunicateRemainsMissingTerminal(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	for range 8 {
		fixture.adapter.appendSteps(bare)
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	root.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedDelegateOutcome(t, outcome)
	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionReportRequired || snapshot.outcome != delegateCompletionOutcomeNone || snapshot.terminalSeen {
		t.Fatalf("user-run evidence = %#v, want report-required missing terminal", snapshot)
	}
	assertSingleRecoveryNudge(t, fixture.adapter)
}

func TestDelegateResourceSupervision_CompletionGateRecoversEveryCleanExit(t *testing.T) {
	t.Run("no-tool response cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixture(t, "")
		bare := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response")} }
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{bare, bare, bare, bare,
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") }})
		root := restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("tool-bearing observer handoff cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixture(t, "")
		var root *Session
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				jm := sub.sess.jobManager
				receiver := root.ID()
				installWatchBelowValidation(t, jm, watchArgs{
					Target: runtimeMessageAliasCaller,
					Events: []string{"error"},
					Send:   &watchSendArgs{To: receiver, Message: "observer handoff"},
				})
				key := watchKey{VisibleSessionID: jm.sessionID, Target: runtimeMessageAliasCaller, SendTo: receiver}
				cfg := jm.watches[key]
				state := jobstore.WatchSendState{
					Key: jobstore.WatchSendKey{
						VisibleSessionID:        jm.sessionID,
						WatchTarget:             runtimeMessageAliasCaller,
						ResolvedWatchedIdentity: runtimeMessageAliasCaller,
						ResolvedSendTo:          receiver,
						WatchGeneration:         cfg.generation,
					},
					DeliveryID:               "delivery_observer_handoff",
					UpdateSeq:                1,
					Frame:                    "observer handoff frame",
					StableReceiver:           true,
					ReceiverSessionID:        receiver,
					SourceDelegateID:         fixture.delegateID,
					SourceDelegateGeneration: 1,
				}
				jm.recordWatchSendPending(state, watchSendDelivery{cfg: cfg, key: key, generation: cfg.generation, send: cfg.send})
				return agenttest.CommunicateResponse(false, "handoff")
			},
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") },
		})
		root = restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("notification yield cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixture(t, "")
		var root *Session
		nudgeUpdate := make(chan bool, 1)
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
					cfg.subagentNudgeSelected = func(updated bool) { nudgeUpdate <- updated }
				})
				sub.sess.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: "watch-test", Status: jobNotificationEventWatch})
				return agenttest.CommunicateResponse(false, "work before notification")
			},
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("notification acknowledged")}
			},
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") },
		})
		root = restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
		select {
		case updated := <-nudgeUpdate:
			if !updated {
				t.Fatal("the reminder ignored the non-terminal update across the notification turn")
			}
		default:
			t.Fatal("the recovery nudge selected no reminder")
		}
	})

	t.Run("goal-controlled cap cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.Config.MaxToolRoundsPerInput = goal.GoalTurnMaxRounds
		})
		enteredFinalBare := make(chan struct{})
		releaseFinalBare := make(chan struct{})
		bare := func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("bare before continuation")}
		}
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			bare, bare, bare,
			func(llm.Request) llm.Response {
				close(enteredFinalBare)
				<-releaseFinalBare
				return bare(llm.Request{})
			},
		})
		for range goal.GoalTurnMaxRounds {
			fixture.adapter.appendSteps(func(llm.Request) llm.Response {
				return agenttest.CommunicateResponse(false, "goal partial")
			})
		}
		fixture.adapter.appendSteps(func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") })
		root := restoreSupervisionRoot(t, fixture, nil)
		snapshots := make(chan delegateCompletionSnapshot, 1)
		root.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
		started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "continue goal", 0)
		if started.result.Err != nil {
			t.Fatalf("start goal-cap run: %v", started.result.Err)
		}
		<-enteredFinalBare
		steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "continue through goal-controlled cap", 0)
		if steered.result.Err != nil || steered.result.Action != "steered" {
			t.Fatalf("steer goal-cap run = %#v", steered.result)
		}
		close(releaseFinalBare)
		waitForStableSupervisionRun(t, root, fixture.childID)
		if snapshot := <-snapshots; !snapshot.terminalSeen {
			t.Fatalf("goal-cap recovery evidence = %#v, want terminal after bounded nudge", snapshot)
		}
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("blocked hook continuation cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixture(t, "")
		bare := func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("hook continuation without report")}
		}
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			bare, bare, bare, bare,
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered after hook") },
		})
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmStableSupervisionDelegate(t, root, fixture)
		sub.sess.hookRunner = stableSupervisionStopHook(`{"decision":"block","reason":"address hook feedback"}`)
		armStableSupervisionAttention(t, sub, "attention:blocked-hook", "run blocked hook")
		waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("unblocked hook model context cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixture(t, "")
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered after context") },
		})
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmStableSupervisionDelegate(t, root, fixture)
		sub.sess.hookRunner = stableSupervisionStopHook(`{"hookSpecificOutput":{"additionalContext":"hook model context"}}`)
		armStableSupervisionAttention(t, sub, "attention:hook-context", "run context hook")
		waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
		assertSingleRecoveryNudge(t, fixture.adapter)
		requests := fixture.adapter.Requests()
		if !requestMessagesContainText(requests[len(requests)-1].Messages, "hook model context") {
			t.Fatalf("recovery request omitted unblocked hook context: %#v", requests[len(requests)-1].Messages)
		}
	})

	t.Run("post-drain owner steering cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdDelegateFixture(t, "")
		var root *Session
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				sub.sess.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: "post-drain", Status: jobNotificationEventWatch})
				return agenttest.CommunicateResponse(false, "handoff")
			},
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("post-drain notification acknowledged")}
			},
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") },
			func(llm.Request) llm.Response { return agenttest.FinalResponse("steering handled") },
		})
		root = restoreSupervisionRoot(t, fixture, nil)
		steered := false
		root.cfg.testOnly.subagentBeforeSettlement = func(sub *subagent) {
			if steered {
				return
			}
			steered = true
			plans, err := root.delegateController.Steer(context.Background(), rootDelegateActor(root.delegateRootSessionID), fixture.delegateID, "post-drain steering")
			if err != nil {
				t.Errorf("post-drain Steer: %v", err)
				return
			}
			if err := sub.sess.executeDelegateMutationPlans(plans); err != nil {
				t.Errorf("execute post-drain steering: %v", err)
			}
		}
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
		requests := fixture.adapter.Requests()
		if !requestMessagesContainText(requests[len(requests)-1].Messages, "post-drain steering") {
			t.Fatalf("post-drain continuation omitted owner steering: %#v", requests[len(requests)-1].Messages)
		}
	})
}

// Unlike the before-announcement control, this holds generation 1 after its
// inline result has reached the waiter. The caller acknowledges that exact
// result before arming attention, while only the controller finalizing claim
// still fences generation 2. A passing run is a control, not a reproduction of
// the intermittent original blocked/context hook failures.
func TestDelegateResourceSupervision_PostAcknowledgementAttentionBeforeRelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, attentionID, hookOutput string
		bareContinuations             int
		wantRequests                  int
	}{
		{"blocked_hook", "attention:postack-blocked-hook", `{"decision":"block","reason":"address hook feedback"}`, 4, 7},
		{"unblocked_hook_context", "attention:postack-hook-context", `{"hookSpecificOutput":{"additionalContext":"hook model context"}}`, 0, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newColdDelegateFixture(t, "")
			fixture.adapter.setSteps([]func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
				func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			})
			for range tc.bareContinuations {
				fixture.adapter.appendSteps(func(llm.Request) llm.Response {
					return llm.Response{Message: llm.Assistant("hook continuation without report")}
				})
			}
			fixture.adapter.appendSteps(func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered after hook") })
			root := restoreSupervisionRoot(t, fixture, nil)
			entered := make(chan delegateLease, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			// Registered after root.Close, so every failure releases the barrier
			// before cleanup tries to join the real runner.
			t.Cleanup(unblock)
			defer unblock()
			root.cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) {
				if lease.generation == 1 {
					entered <- lease
					<-release
				}
			}
			sub := warmStableSupervisionDelegate(t, root, fixture)
			var warmLease delegateLease
			select {
			case warmLease = <-entered:
			// TRIPWIRE: the in-process warm announcement normally reaches this signal in milliseconds, this only bounds a hung finalizer.
			case <-time.After(30 * time.Second):
				t.Fatal("warm announcement never reached the pre-release barrier")
			}
			if warmLease != (delegateLease{delegateID: fixture.delegateID, generation: 1}) {
				t.Fatalf("warm barrier lease = %#v, want exact generation 1", warmLease)
			}
			warmFinish := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
			events, err := root.delegateController.store.Load()
			if err != nil {
				t.Fatalf("load warm acknowledgement: %v", err)
			}
			acknowledged := false
			for _, event := range events {
				if event.DelegateID == fixture.delegateID && event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == warmFinish.DeliveryID {
					acknowledged = true
				}
			}
			if warmFinish.Generation != warmLease.generation || warmFinish.DeliveryID == "" || !acknowledged {
				t.Fatalf("warm exact delivery not durably acknowledged before attention: finish=%#v acknowledged=%t", warmFinish, acknowledged)
			}
			c := root.delegateController
			assertWarmFence := func() {
				t.Helper()
				c.mu.Lock()
				aggregate, live := c.durable[fixture.delegateID], c.live[fixture.delegateID]
				retained := live != nil && live.finalizing != nil && live.finalizing.generation == warmLease.generation && live.finalizing.runtime == sub.sess
				closedRun := aggregate != nil && aggregate.Generation == warmLease.generation && !aggregate.CurrentRunOpen && len(aggregate.PendingDeliveries) == 0
				c.mu.Unlock()
				sub.mu.Lock()
				childIdle := !sub.running && !sub.driving && !sub.finalizing
				warmDoneOpen := sub.done != nil && !supervisionChannelClosed(sub.done)
				sub.mu.Unlock()
				if !retained || !closedRun || !childIdle || !warmDoneOpen {
					t.Fatalf("post-ack fence = retained:%t closedRun:%t childIdle:%t warmDoneOpen:%t", retained, closedRun, childIdle, warmDoneOpen)
				}
			}
			assertWarmFence()
			sub.sess.hookRunner = stableSupervisionStopHook(tc.hookOutput)
			armStableSupervisionAttention(t, sub, tc.attentionID, "postack-attention-sentinel")
			if !root.driveDelegateAttention(sub) {
				t.Fatal("real post-ack drive did not handle pending attention")
			}
			assertWarmFence()
			fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, fixture.childID), fixture.childID)
			if err != nil || !reflect.DeepEqual(fold.pendingIDs(), []string{tc.attentionID}) || fold.resumeGenerations[tc.attentionID] != 0 {
				t.Fatalf("held generation attention identity = pending:%#v generations:%#v err:%v", fold.pendingIDs(), fold.resumeGenerations, err)
			}
			unblock()
			waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
			assertSingleRecoveryNudge(t, fixture.adapter)
			finished := latestDelegateControllerRunFinished(t, c, fixture.delegateID)
			if finished.Generation != 2 || finished.Disposition != delegatestore.DispositionReported || finished.DeliveryID == "" {
				t.Fatalf("post-ack attention finish = %#v, want reported generation 2", finished)
			}
			fold, err = readDelegateAttentionFold(transcriptPath(root.stateDir, fixture.childID), fixture.childID)
			if err != nil || fold.resolutions[tc.attentionID] != delegateAttentionConsumed || fold.resumeGenerations[tc.attentionID] != 2 {
				t.Fatalf("post-ack resolution identity = resolutions:%#v generations:%#v err:%v", fold.resolutions, fold.resumeGenerations, err)
			}
			requests := fixture.adapter.Requests()
			if len(requests) != tc.wantRequests || !requestMessagesContainText(requests[1].Messages, "postack-attention-sentinel") {
				t.Fatalf("post-ack provider requests = %#v, want %d including exact attention payload", requests, tc.wantRequests)
			}
			if tc.name == "unblocked_hook_context" && !requestMessagesContainText(requests[len(requests)-1].Messages, "hook model context") {
				t.Fatalf("recovery request omitted unblocked hook context: %#v", requests[len(requests)-1].Messages)
			}
		})
	}
}

func TestDelegateResourceSupervision_AutoNudgeOccursOnceForEligibleBuiltin(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered after the one nudge") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedDelegateOutcome(t, outcome)
	if outcome.result.Err != nil || outcome.result.Status != jobstore.StatusCompleted || !strings.Contains(outcome.result.Output, "recovered after the one nudge") {
		t.Fatalf("nudged stable delegate = %#v", outcome.result)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 5 {
		t.Fatalf("provider requests = %d, want four empty attempts plus one nudge", got)
	}
}

func TestDelegateResourceSupervision_AutoNudgeSuppressedBySteerCancellationAndExhaustion(t *testing.T) {
	t.Run("steer", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		fixture := newColdDelegateFixture(t, "")
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				close(entered)
				<-release
				return agenttest.FinalResponse("first result")
			},
			func(llm.Request) llm.Response { return agenttest.FinalResponse("continued after steer") },
		})
		root := restoreSupervisionRoot(t, fixture, nil)
		started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
		if started.result.Err != nil {
			t.Fatalf("start stable delegate: %v", started.result.Err)
		}
		<-entered
		steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "new steering", 0)
		if steered.result.Err != nil || steered.result.Action != "steered" {
			t.Fatalf("steer stable delegate = %#v", steered.result)
		}
		close(release)
		waitForStableSupervisionRun(t, root, fixture.childID)
		if got := supervisionRequestCount(fixture.adapter); got != 2 {
			t.Fatalf("steered provider requests = %d, want initial plus steering continuation without nudge", got)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		fixture := newColdDelegateFixture(t, "")
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{func(llm.Request) llm.Response {
			close(entered)
			<-release
			return agenttest.FinalResponse("cancelled result")
		}})
		root := restoreSupervisionRoot(t, fixture, nil)
		started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
		if started.result.Err != nil {
			t.Fatalf("start stable delegate: %v", started.result.Err)
		}
		<-entered
		sub := root.subagents.get(fixture.childID)
		if sub == nil {
			t.Fatal("stable child was not tracked")
		}
		sub.mu.Lock()
		sub.cancelRequested = true
		cancel := sub.cancel
		sub.mu.Unlock()
		if cancel == nil {
			t.Fatal("stable child has no generation cancel")
		}
		cancel()
		close(release)
		waitForStableSupervisionRun(t, root, fixture.childID)
		if got := supervisionRequestCount(fixture.adapter); got != 1 {
			t.Fatalf("cancelled provider requests = %d, want no nudge", got)
		}
	})

	t.Run("exhaustion", func(t *testing.T) {
		fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.Config.MaxToolRoundsPerInput = 1
		})
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return agenttest.CommunicateResponse(false, "continue") },
		})
		root := restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "exhaust", 60_000)
		abortUnpersistedDelegateOutcome(t, outcome)
		if outcome.result.Err != nil || outcome.result.Status != jobstore.StatusExhausted {
			t.Fatalf("exhausted stable delegate = %#v", outcome.result)
		}
		if got := supervisionRequestCount(fixture.adapter); got != 1 {
			t.Fatalf("exhausted provider requests = %d, want no nudge", got)
		}
	})
}

func TestDelegateResourceSupervision_FatalFailureBeatsPendingSteer(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := newFakeErrAdapter("openai", []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			close(entered)
			<-release
			return llm.Response{}, fatalErr
		},
		func(llm.Request) (llm.Response, error) {
			return agenttest.FinalResponse("unexpected continuation after fatal failure"), nil
		},
	})
	fixture := newColdDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	phaseBeforeFinish := make(chan delegatestore.Phase, 1)
	updateSessionTestConfig(child.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = func(got *subagent) {
			got.sess.delegateController.mu.Lock()
			aggregate := got.sess.delegateController.durable[fixture.delegateID]
			phase := aggregate.Phase
			got.sess.delegateController.mu.Unlock()
			phaseBeforeFinish <- phase
		}
	})
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "accepted before fatal failure", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests after fatal failure = %d, want no steering continuation", got)
	}
	if got := <-phaseBeforeFinish; got != delegatestore.PhaseRunning {
		t.Fatalf("fatal generation phase before atomic finish = %s, want running without a prepared-only state", got)
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeFailed)
}

func TestDelegateResourceSupervision_ExhaustionBeatsPendingSteer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.MaxToolRoundsPerInput = 1
	})
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return agenttest.CommunicateResponse(false, "exhaust this activation")
		},
		func(llm.Request) llm.Response {
			return agenttest.FinalResponse("unexpected continuation after exhaustion")
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "accepted before exhaustion", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("provider requests after exhaustion = %d, want no steering continuation", got)
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeExhausted)
}

func TestDelegateResourceSupervision_CancellationBeatsPendingSteer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	continued := make(chan struct{})
	var continuedOnce sync.Once
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return agenttest.FinalResponse("cancelled result")
	}})
	root := restoreSupervisionRoot(t, fixture, nil)
	root.cfg.testOnly.subagentRunIteration = func(_ *subagent, iteration int) {
		if iteration > 1 {
			continuedOnce.Do(func() { close(continued) })
		}
	}
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "accepted before cancellation", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil {
		t.Fatal("stable child was not tracked")
	}
	sub.mu.Lock()
	sub.cancelRequested = true
	cancel := sub.cancel
	done := sub.done
	sub.mu.Unlock()
	if cancel == nil || done == nil {
		t.Fatal("stable child has no cancellable generation")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-continued:
		root.delegateController.mu.Lock()
		root.delegateController.live[fixture.delegateID].pendingSteers = nil
		root.delegateController.mu.Unlock()
		<-done
		t.Fatal("cancelled generation entered a steering continuation")
	}
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("provider requests after cancellation = %d, want no steering continuation", got)
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeCancelled)
}

func assertStableSupervisionOutcome(t *testing.T, root *Session, delegateID string, want delegatestore.OutcomeStatus) {
	t.Helper()
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[delegateID]
	root.delegateController.mu.Unlock()
	if aggregate == nil || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != want {
		t.Fatalf("stable delegate outcome = %#v, want %s", aggregate, want)
	}
}

func TestDelegateResourceSupervision_PendingSteerPrecedesAutoNudge(t *testing.T) {
	enteredFinalEmpty := make(chan struct{})
	releaseFinalEmpty := make(chan struct{})
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response {
			close(enteredFinalEmpty)
			<-releaseFinalEmpty
			return agenttest.EmptyResponse()
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("continued after steering") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-enteredFinalEmpty
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "priority steering", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(releaseFinalEmpty)
	waitForStableSupervisionRun(t, root, fixture.childID)
	requests := fixture.adapter.Requests()
	if len(requests) != 5 {
		t.Fatalf("provider requests = %d, want four empty attempts plus steering continuation", len(requests))
	}
	if requestMessagesContainText(requests[4].Messages, communicateNudge("communicate")) {
		t.Fatalf("pending steer was delayed behind auto-nudge: %#v", requests[4].Messages)
	}
	if !requestMessagesContainText(requests[4].Messages, "priority steering") {
		t.Fatalf("steering continuation omitted accepted steer: %#v", requests[4].Messages)
	}
}

func TestDelegateResourceSupervision_LateOrdinarySteerPreservesOwnedWorkForContinuation(t *testing.T) {
	enteredInitialRequest := make(chan struct{})
	releaseInitialRequest := make(chan struct{})
	ownedWorkStopped := make(chan struct{}, 1)
	stoppedBeforeContinuation := make(chan bool, 1)
	releaseContinuation := make(chan struct{})
	lateSteer := make(chan sendMessageResult, 1)
	var child *subagent
	var ownedShell *runningJob
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare text without communicate")}
	}
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(enteredInitialRequest)
			<-releaseInitialRequest
			return bare(llm.Request{})
		},
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		func(llm.Request) llm.Response {
			select {
			case <-ownedWorkStopped:
				stoppedBeforeContinuation <- true
			default:
				stoppedBeforeContinuation <- false
			}
			<-releaseContinuation
			return agenttest.FinalResponse("continued after late nudge steering")
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-enteredInitialRequest
	child = root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil || child.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	ownedShell = &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_late_nudge_owned_shell",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: child.sess.ID(),
		},
		signal: func() {
			ownedWorkStopped <- struct{}{}
			ownedShell.closeDone()
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	child.sess.jobManager.mu.Lock()
	child.sess.jobManager.running[ownedShell.rec.JobID] = ownedShell
	child.sess.jobManager.mu.Unlock()
	t.Cleanup(func() {
		child.sess.jobManager.mu.Lock()
		delete(child.sess.jobManager.running, ownedShell.rec.JobID)
		child.sess.jobManager.mu.Unlock()
		ownedShell.closeDone()
	})
	var steerOnce sync.Once
	updateSessionTestConfig(child.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = func(*subagent) {
			steerOnce.Do(func() {
				lateSteer <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "late steering at ordinary settlement", 0).result
			})
		}
	})
	close(releaseInitialRequest)
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	if steered := <-lateSteer; steered.Err != nil || steered.Action != "steered" {
		t.Fatalf("late ordinary steer = %#v", steered)
	}
	var stopped bool
	select {
	case stopped = <-stoppedBeforeContinuation:
		child.sess.jobManager.mu.Lock()
		delete(child.sess.jobManager.running, ownedShell.rec.JobID)
		child.sess.jobManager.mu.Unlock()
		ownedShell.closeDone()
		close(releaseContinuation)
		<-done
	case <-done:
		close(releaseContinuation)
		t.Fatal("ordinary missing-terminal path settled without honoring late steering")
	}
	if stopped {
		t.Fatal("ordinary missing-terminal path stopped owned work before honoring late steering")
	}
	requests := fixture.adapter.Requests()
	if len(requests) != 9 || !requestMessagesContainText(requests[8].Messages, "late steering at ordinary settlement") {
		t.Fatalf("late nudge continuation requests = %d, final history %#v", len(requests), requests[len(requests)-1].Messages)
	}
}

func TestDelegateResourceSupervision_LateCancellationBeatsSettlementSteer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	continued := make(chan struct{})
	lateCancel := make(chan sendMessageResult, 1)
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return agenttest.FinalResponse("result before late cancellation")
		},
		func(llm.Request) llm.Response {
			return agenttest.FinalResponse("unexpected continuation after late cancellation")
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	var cancelOnce sync.Once
	updateSessionTestConfig(child.sess, func(cfg *testConfig) {
		cfg.subagentRunIteration = func(_ *subagent, iteration int) {
			if iteration > 1 {
				select {
				case <-continued:
				default:
					close(continued)
				}
			}
		}
		cfg.subagentBeforeSettlement = func(got *subagent) {
			cancelOnce.Do(func() {
				lateCancel <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer admitted at cancellation boundary", 0).result
				got.mu.Lock()
				got.cancelRequested = true
				cancel := got.cancel
				got.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			})
		}
	})
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if steered := <-lateCancel; steered.Err != nil || steered.Action != "steered" {
		t.Fatalf("late cancellation steer = %#v", steered)
	}
	select {
	case <-continued:
		t.Fatal("cancellation admitted at the settlement boundary entered a steering continuation")
	default:
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeCancelled)
}

// closeGraceArmClock signals the instant a job manager arms its closeGrace
// deadline. BlockUntil(n) accepts ANY parked waiter, and this fixture's clock
// is the whole session tree's clock: a child session keeps its own tickers and
// retry AfterFuncs on it (delegate_quiet_watch.go's 30s quiet ticker,
// session_worktree_relock.go's 1s lane ticker, the attention retry chain), so
// waiting for "one more waiter than I sampled" can be satisfied by one of
// those instead of the deadline closeRuntimeState arms. Advancing then moves
// virtual time past a deadline that does not exist yet, and the close parks on
// it forever (#6561). This waits on the arm itself, which no other timer can
// satisfy.
type closeGraceArmClock struct {
	*agenttest.FakeClock
	grace time.Duration
	armed chan struct{}
	once  sync.Once
}

func newCloseGraceArmClock(grace time.Duration) *closeGraceArmClock {
	return &closeGraceArmClock{
		FakeClock: agenttest.NewFakeClock(),
		grace:     grace,
		armed:     make(chan struct{}),
	}
}

func (c *closeGraceArmClock) NewTimer(d time.Duration) clock.Timer {
	// Register the waiter BEFORE signalling: the signal says "the deadline
	// exists", so the test's Advance must find it parked. Signalling first
	// leaves a window where Advance fires against an unarmed clock and the
	// deadline is then armed past the advance, reintroducing the very hang
	// this replaces.
	timer := c.FakeClock.NewTimer(d)
	if d == c.grace {
		c.once.Do(func() { close(c.armed) })
	}
	return timer
}

// errCloseGraceNeverArmed reports a close that neither armed its deadline nor
// settled. It is deliberately not the abandon error: the test's assertion then
// fails on this value rather than the wait parking forever.
var errCloseGraceNeverArmed = errors.New("closeRuntimeState neither armed its closeGrace deadline nor settled")

// awaitCloseSettled arms the deadline and reports the close's result. It
// advances virtual time only once the deadline exists, so the advance can never
// run before there is something to fire. A close that settles without a
// deadline (nothing was still running) reports that result unchanged.
//
// The caller runs this on the fatal-run goroutine, so the stall path returns a
// sentinel error the test's own assertion reports, rather than calling Fatal
// off the test goroutine.
func (c *closeGraceArmClock) awaitCloseSettled(closeResult <-chan error) error {
	select {
	case <-c.armed:
		c.Advance(c.grace)
		return <-closeResult
	case err := <-closeResult:
		return err
	// TRIPWIRE: the deadline is armed synchronously once the running-job wait
	// begins, so 30s only fires on a genuine stall, not on host load.
	case <-time.After(30 * time.Second):
		return errCloseGraceNeverArmed
	}
}

type asynchronousCleanupStreamingExecutor struct {
	marker         string
	signalReturned chan struct{}
	releaseWait    chan struct{}
	waitReturned   chan struct{}
	signalOnce     sync.Once
	releaseOnce    sync.Once
}

func newAsynchronousCleanupStreamingExecutor(marker string) *asynchronousCleanupStreamingExecutor {
	return &asynchronousCleanupStreamingExecutor{
		marker:         marker,
		signalReturned: make(chan struct{}),
		releaseWait:    make(chan struct{}),
		waitReturned:   make(chan struct{}),
	}
}

func (e *asynchronousCleanupStreamingExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, _ io.Writer) (*execenv.StreamHandle, error) {
	return &execenv.StreamHandle{
		Signal: func() {
			e.signalOnce.Do(func() { close(e.signalReturned) })
		},
		Wait: func() (int, error) {
			<-e.releaseWait
			err := os.WriteFile(e.marker, []byte("cleaned\n"), 0o644)
			close(e.waitReturned)
			return 143, err
		},
	}, nil
}

func (e *asynchronousCleanupStreamingExecutor) release() {
	e.releaseOnce.Do(func() { close(e.releaseWait) })
}

func TestDelegateResourceSupervision_FatalNudgeRunStopsOwnedShell(t *testing.T) {
	worktreeRepo := newWorktreeRepo(t)
	lane, _, _, _, _, err := worktreeRepo.s.createDelegateWorktree(context.Background(), "dlg_01TASK7FATALPACKET000001", "dlg_01TASK7FATALPACKET000001")
	if err != nil {
		t.Fatalf("create fatal-evidence worktree: %v", err)
	}
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal nudge turn", nil, nil)
	enteredFatalNudge := make(chan struct{})
	releaseFatalNudge := make(chan struct{})
	adapter := newFakeErrAdapter("openai", []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return agenttest.EmptyResponse(), nil
		},
		func(llm.Request) (llm.Response, error) {
			return agenttest.EmptyResponse(), nil
		},
		func(llm.Request) (llm.Response, error) {
			return agenttest.EmptyResponse(), nil
		},
		func(llm.Request) (llm.Response, error) {
			return agenttest.EmptyResponse(), nil
		},
		func(llm.Request) (llm.Response, error) {
			close(enteredFatalNudge)
			<-releaseFatalNudge
			return llm.Response{}, fatalErr
		},
	})
	fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Isolation = "worktree"
		descriptor.WorkingDir = lane
	})
	// The lane above was cut under a stand-in delegate id; the delegate this
	// fixture revives owns the lane under its own id and root, the shape a real
	// spawn leaves (revive revalidates the marker before handing it back).
	wtGit(t, worktreeRepo.mainRoot, "worktree", "unlock", lane)
	wtGit(t, worktreeRepo.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lane)
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	finalStatePublished := make(chan struct{})
	restore := RestoreSessionConfig{
		StateDir:    fixture.stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(fixture.workspace),
			subagentAfterFinalStatePublish: func(*subagent) {
				close(finalStatePublished)
			},
		},
	}
	root, err := RestoreSessionFromMetaWithConfig(client, fixture.profile, execenv.NewLocalExecutionEnvironment(fixture.workspace), fixture.meta, restore)
	if err != nil {
		t.Fatalf("restore fatal-nudge root: %v", err)
	}
	t.Cleanup(root.Close)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start fatal nudge run", 0)
	if started.result.Err != nil {
		t.Fatalf("start fatal-nudge delegate: %v", started.result.Err)
	}
	<-enteredFatalNudge
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	root.delegateController.mu.Lock()
	descriptor := cloneDelegateStartDescriptor(root.delegateController.durable[fixture.delegateID].Descriptor)
	root.delegateController.mu.Unlock()
	if report := root.descriptorWorktreeReport(descriptor); report == nil || report.Dirty {
		t.Fatalf("initial fatal-evidence worktree report = %#v, want clean", report)
	}
	cleanupMarker := filepath.Join(lane, "fatal-shell-cleanup.txt")
	executor := newAsynchronousCleanupStreamingExecutor(cleanupMarker)
	t.Cleanup(executor.release)
	ownedShell := runShell(context.Background(), sub.sess.jobManager, executor, shellArgs{
		Command:    "asynchronous fatal cleanup",
		Background: true,
		WorkingDir: lane,
	})
	if ownedShell.JobID == "" || !ownedShell.RunningInBackground {
		t.Fatalf("start fatal owned shell = %#v", ownedShell)
	}
	joinStarted := make(chan struct{})
	var joinOnce sync.Once
	sub.sess.jobManager.stopReceiptBeforeWait = func(jobID string) {
		if jobID == ownedShell.JobID {
			joinOnce.Do(func() { close(joinStarted) })
		}
	}
	close(releaseFatalNudge)
	select {
	case <-joinStarted:
		executor.release()
	case <-finalStatePublished:
		executor.release()
		<-executor.waitReturned
		t.Fatal("fatal stable nudge run sampled terminal evidence before joining its owned shell")
	}
	<-finalStatePublished
	<-executor.waitReturned
	if report := root.descriptorWorktreeReport(descriptor); report == nil || !report.Dirty {
		t.Fatalf("post-cleanup worktree report = %#v, want dirty", report)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load fatal terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil {
		t.Fatal("fatal stable generation has no terminal packet")
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode fatal terminal metadata: %v", err)
	}
	if metadata.Worktree == nil || !metadata.Worktree.Dirty {
		t.Fatalf("fatal packet sampled worktree before shell cleanup: %#v", metadata.Worktree)
	}
}

func TestDelegateResourceSupervision_FatalCleanupFailureIsObservable(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal cleanup failure turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := newFakeErrAdapter("openai", []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
		close(entered)
		<-release
		return llm.Response{}, fatalErr
	}})
	fixture := newColdDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start cleanup-failure run", 0)
	if started.result.Err != nil {
		t.Fatalf("start cleanup-failure delegate: %v", started.result.Err)
	}
	<-entered
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	ownedShell := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_owned_cleanup_failure",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: sub.sess.ID(),
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	ownedShell.signal = func() { ownedShell.closeDoneAbandoned() }
	cleanupErr := "owned job " + ownedShell.rec.JobID + " ended without durable completion"
	jm := sub.sess.jobManager
	jm.mu.Lock()
	jm.running[ownedShell.rec.JobID] = ownedShell
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, ownedShell.rec.JobID)
		jm.mu.Unlock()
	})
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	sub.mu.Lock()
	runErr := sub.err
	sub.mu.Unlock()
	if !strings.Contains(runErr.Error(), cleanupErr) {
		t.Fatalf("retained fatal error = %v, want owned cleanup failure", runErr)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load cleanup-failure terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), cleanupErr) {
		t.Fatalf("cleanup-failure terminal packet = %#v, want observable cleanup error", packet)
	}
}

func TestDelegateResourceSupervision_RootCloseBeforeReceiptCaptureIsCleanupFailure(t *testing.T) {
	const closeGrace = time.Second
	// The child session keeps its own waiters on this clock, so the close is
	// paced by its own deadline arming, not by a count of parked waiters.
	clk := newCloseGraceArmClock(closeGrace)
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal close-abandon turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := newFakeErrAdapter("openai", []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
		close(entered)
		<-release
		return llm.Response{}, fatalErr
	}})
	fixture := newColdDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	finalStatePublished := make(chan struct{})
	root := restoreSupervisionRoot(t, fixture, clk.FakeClock)
	root.cfg.testOnly.subagentAfterFinalStatePublish = func(*subagent) {
		close(finalStatePublished)
	}
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start close-abandon run", 0)
	if started.result.Err != nil {
		t.Fatalf("start close-abandon delegate: %v", started.result.Err)
	}
	<-entered
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	cleanupMarker := filepath.Join(fixture.workspace, "close-abandon-cleanup.txt")
	executor := newAsynchronousCleanupStreamingExecutor(cleanupMarker)
	t.Cleanup(executor.release)
	ownedShell := runShell(context.Background(), sub.sess.jobManager, executor, shellArgs{
		Command:    "asynchronous close-abandon cleanup",
		Background: true,
		WorkingDir: fixture.workspace,
	})
	if ownedShell.JobID == "" || !ownedShell.RunningInBackground {
		t.Fatalf("start close-abandon owned shell = %#v", ownedShell)
	}
	jm := sub.sess.jobManager
	jm.closeGrace = closeGrace
	// Observe the child manager's own deadline arm on the same virtual time the
	// fixture's sessions already use.
	jm.clock = clk
	var closeErr error
	var closeOnce sync.Once
	jm.stopReceiptsAfterCapture = func() {
		closeOnce.Do(func() {
			closeResult := make(chan error, 1)
			go func() {
				closeResult <- jm.closeRuntimeState(context.Background())
			}()
			closeErr = clk.awaitCloseSettled(closeResult)
		})
	}
	close(release)
	<-finalStatePublished
	if closeErr == nil || !strings.Contains(closeErr.Error(), "timed out waiting for running jobs") {
		t.Fatalf("close runtime result = %v, want bounded running-job timeout", closeErr)
	}
	select {
	case <-executor.waitReturned:
		t.Fatal("blocked executor Wait returned before the test released it")
	default:
	}
	sub.mu.Lock()
	runErr := sub.err
	sub.mu.Unlock()
	if runErr == nil || !strings.Contains(runErr.Error(), ownedShell.JobID) || !strings.Contains(runErr.Error(), "durable completion") {
		t.Fatalf("retained close-abandon error = %v, want exact job and durable-completion failure", runErr)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load close-abandon terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), ownedShell.JobID) || !strings.Contains(string(packet.Message), "durable completion") {
		t.Fatalf("close-abandon terminal packet = %#v, want exact cleanup failure", packet)
	}
	executor.release()
	<-executor.waitReturned
}

func TestDelegateResourceSupervision_OrdinaryCleanupFailureIsObservable(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("partial ordinary result")}
	}
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return bare(llm.Request{})
		},
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start ordinary cleanup-failure run", 0)
	if started.result.Err != nil {
		t.Fatalf("start ordinary cleanup-failure delegate: %v", started.result.Err)
	}
	<-entered
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	ownedShell := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_ordinary_cleanup_failure",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: sub.sess.ID(),
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	ownedShell.signal = func() { ownedShell.closeDoneAbandoned() }
	cleanupErr := "owned job " + ownedShell.rec.JobID + " ended without durable completion"
	jm := sub.sess.jobManager
	jm.mu.Lock()
	jm.running[ownedShell.rec.JobID] = ownedShell
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, ownedShell.rec.JobID)
		jm.mu.Unlock()
	})
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load ordinary cleanup-failure packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), cleanupErr) {
		t.Fatalf("ordinary cleanup-failure packet = %#v, want observable cleanup failure", packet)
	}
}

func TestDelegateResourceSupervision_OrdinaryMissingTerminalCleanupPrecedesPacketEvidence(t *testing.T) {
	worktreeRepo := newWorktreeRepo(t)
	lane, _, _, _, _, err := worktreeRepo.s.createDelegateWorktree(context.Background(), "dlg_01TASK7ORDINARYPACKET001", "dlg_01TASK7ORDINARYPACKET001")
	if err != nil {
		t.Fatalf("create ordinary-evidence worktree: %v", err)
	}
	enteredInitialRequest := make(chan struct{})
	releaseInitialRequest := make(chan struct{})
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare text without communicate")}
	}
	fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Isolation = "worktree"
		descriptor.WorkingDir = lane
	})
	// The lane above was cut under a stand-in delegate id; the delegate this
	// fixture revives owns the lane under its own id and root, the shape a real
	// spawn leaves (revive revalidates the marker before handing it back).
	wtGit(t, worktreeRepo.mainRoot, "worktree", "unlock", lane)
	wtGit(t, worktreeRepo.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lane)
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(enteredInitialRequest)
			<-releaseInitialRequest
			return bare(llm.Request{})
		},
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
	})
	finalStatePublished := make(chan struct{})
	restore := RestoreSessionConfig{
		StateDir:    fixture.stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(fixture.workspace),
			subagentAfterFinalStatePublish: func(*subagent) {
				close(finalStatePublished)
			},
		},
	}
	root, err := RestoreSessionFromMetaWithConfig(fixture.client, fixture.profile, execenv.NewLocalExecutionEnvironment(fixture.workspace), fixture.meta, restore)
	if err != nil {
		t.Fatalf("restore ordinary-evidence root: %v", err)
	}
	t.Cleanup(root.Close)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start ordinary missing-terminal run", 0)
	if started.result.Err != nil {
		t.Fatalf("start ordinary missing-terminal delegate: %v", started.result.Err)
	}
	<-enteredInitialRequest
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	root.delegateController.mu.Lock()
	descriptor := cloneDelegateStartDescriptor(root.delegateController.durable[fixture.delegateID].Descriptor)
	root.delegateController.mu.Unlock()
	if report := root.descriptorWorktreeReport(descriptor); report == nil || report.Dirty {
		t.Fatalf("initial ordinary-evidence worktree report = %#v, want clean", report)
	}
	cleanupMarker := filepath.Join(lane, "ordinary-shell-cleanup.txt")
	executor := newAsynchronousCleanupStreamingExecutor(cleanupMarker)
	t.Cleanup(executor.release)
	ownedShell := runShell(context.Background(), sub.sess.jobManager, executor, shellArgs{
		Command:    "asynchronous ordinary cleanup",
		Background: true,
		WorkingDir: lane,
	})
	if ownedShell.JobID == "" || !ownedShell.RunningInBackground {
		t.Fatalf("start ordinary owned shell = %#v", ownedShell)
	}
	joinStarted := make(chan struct{})
	var joinOnce sync.Once
	sub.sess.jobManager.stopReceiptBeforeWait = func(jobID string) {
		if jobID == ownedShell.JobID {
			joinOnce.Do(func() { close(joinStarted) })
		}
	}
	close(releaseInitialRequest)
	<-executor.signalReturned
	lateSteer := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer after ordinary settlement claim", 0)
	if !errors.Is(lateSteer.result.Err, errDelegateTargetBusy) {
		executor.release()
		t.Fatalf("steer after ordinary settlement claim error = %v, want target busy", lateSteer.result.Err)
	}
	select {
	case <-joinStarted:
		executor.release()
	case <-finalStatePublished:
		executor.release()
		<-executor.waitReturned
		t.Fatal("ordinary missing-terminal run sampled terminal evidence before joining its owned shell")
	}
	<-finalStatePublished
	<-executor.waitReturned
	if report := root.descriptorWorktreeReport(descriptor); report == nil || !report.Dirty {
		t.Fatalf("post-cleanup ordinary worktree report = %#v, want dirty", report)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load ordinary terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil {
		t.Fatal("ordinary missing-terminal generation has no terminal packet")
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode ordinary terminal metadata: %v", err)
	}
	if metadata.Worktree == nil || !metadata.Worktree.Dirty {
		t.Fatalf("ordinary packet sampled worktree before shell cleanup: %#v", metadata.Worktree)
	}
}

func TestDelegateResourceSupervision_SubagentStopRunsAfterFinishAndBeforeContinuation(t *testing.T) {
	observation := runStableSubagentStopHook(t, true)
	if !observation.continuationSawHook || observation.providerRequests != 2 || observation.hookRuns != 1 {
		t.Fatalf("blocking SubagentStop ordering = %#v", observation)
	}
}

func TestDelegateResourceSupervision_SubagentStopBlockingStartsOneContinuation(t *testing.T) {
	observation := runStableSubagentStopHook(t, true)
	if observation.providerRequests != 2 || observation.hookRuns != 1 || !strings.Contains(observation.output, "continued after hook") {
		t.Fatalf("blocking SubagentStop continuation = %#v", observation)
	}
}

func TestDelegateResourceSupervision_SubagentStopNonblockingStartsNoContinuation(t *testing.T) {
	observation := runStableSubagentStopHook(t, false)
	if observation.providerRequests != 1 || observation.hookRuns != 1 || !strings.Contains(observation.output, "initial result") {
		t.Fatalf("nonblocking SubagentStop = %#v", observation)
	}
}

func TestDelegateResourceSupervision_SubtreeStopSuppressesSubagentStop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "subagent-stop-runs")
	pluginDir := writeStableSubagentStopPlugin(t, marker, `{}`)
	fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return agenttest.FinalResponse("would normally run the hook")
	}})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	if _, _, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if raw, err := os.ReadFile(marker); err == nil {
		t.Fatalf("stopped generation ran SubagentStop: %q", raw)
	} else if !os.IsNotExist(err) {
		t.Fatalf("read hook marker: %v", err)
	}
}

func TestDelegateResourceSupervision_BlockingSubagentStopContinuesOnlyOnceWithPendingSteer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "subagent-stop-runs")
	pluginDir := writeStableSubagentStopPlugin(t, marker, `{"decision":"block","reason":"address hook feedback"}`)
	fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		// This fixture's SubagentStop hook persistently writes the marker.
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	enteredHookContinuation := make(chan struct{})
	releaseHookContinuation := make(chan struct{})
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("initial result") },
		func(llm.Request) llm.Response {
			close(enteredHookContinuation)
			<-releaseHookContinuation
			return agenttest.FinalResponse("hook continuation result")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("steering continuation result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("unexpected second hook continuation") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-enteredHookContinuation
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer during hook continuation", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(releaseHookContinuation)
	waitForStableSupervisionRun(t, root, fixture.childID)
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read hook marker: %v", err)
	}
	if got := len(strings.Fields(string(raw))); got != 1 {
		t.Fatalf("SubagentStop hook runs = %d, want one per generation", got)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests = %d, want initial + one hook continuation + steering continuation", got)
	}
}

func TestDelegateResourceSupervision_FinalRoundFailedSalvageAddsResumeHint(t *testing.T) {
	t.Parallel()
	child := newTestSession(t)
	child.totalRounds = 1
	if err := child.persistSalvagedTurn("partial final draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist final salvage: %v", err)
	}
	sub := &subagent{sess: child}
	packet := sub.buildDelegateFinish("", errors.New("provider failed after partial stream")).packet
	if packet == nil || !containsDelegateSalvageWarning(packet.Warnings) {
		t.Fatalf("failed final-round warnings = %#v", packetWarnings(packet))
	}
}

func TestDelegateResourceSupervision_SuccessExhaustionCancellationStopAndStaleSalvageAddNoHint(t *testing.T) {
	t.Parallel()
	final := newTestSession(t)
	final.totalRounds = 1
	if err := final.persistSalvagedTurn("partial final draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist final salvage: %v", err)
	}
	final.comm.called = true
	stale := newTestSession(t)
	stale.totalRounds = 1
	if err := stale.persistSalvagedTurn("stale draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist stale salvage: %v", err)
	}
	stale.totalRounds = 2

	cases := []struct {
		name string
		sub  *subagent
		err  error
	}{
		{name: "success", sub: &subagent{sess: final}},
		{name: "tool exhaustion", sub: &subagent{sess: final}, err: &budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 1, Resumable: true}},
		{name: "turn exhaustion", sub: &subagent{sess: final}, err: &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 1, Resumable: false}},
		{name: "cancellation", sub: &subagent{sess: final}, err: context.Canceled},
		{name: "stop", sub: &subagent{sess: final, cancelRequested: true}, err: context.Canceled},
		{name: "stale", sub: &subagent{sess: stale}, err: errors.New("later failure")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			packet := tc.sub.buildDelegateFinish("done", tc.err).packet
			if packet != nil && containsDelegateSalvageWarning(packet.Warnings) {
				t.Fatalf("unexpected salvage hint for %s: %#v", tc.name, packet.Warnings)
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietWatchdogUsesTenMinuteThresholdAndThirtySecondChecks(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	cancelWatchdog := root.startDelegateQuietWatchdog(context.Background(), lease)
	if clock.BlockedCount() != 1 {
		t.Errorf("quiet watchdog waiters = %d, want one 30-second ticker", clock.BlockedCount())
	}
	cancelWatchdog()

	clock.Advance(10*time.Minute - time.Second)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("pre-threshold tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 0 {
		t.Fatalf("pre-threshold quiet attention = %#v", got)
	}
	clock.Advance(time.Second)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("threshold tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(lease) {
		t.Fatalf("threshold quiet attention = %#v", got)
	}
	controller.mu.Lock()
	activity := controller.live[lease.delegateID].activityAt
	controller.mu.Unlock()
	if activity.IsZero() {
		t.Fatal("quiet harness lost exact activity")
	}
}

func TestDelegateResourceSupervision_QuietWatchdogSuppressesRepeatWithinWindowAndRearmsOnActivity(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 {
		t.Fatalf("within-window quiet attention = %#v", got)
	}
	if err := controller.ReportActivity(lease, clock.Now()); err != nil {
		t.Fatalf("ReportActivity: %v", err)
	}
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 2 {
		t.Fatalf("rearmed quiet attention = %#v", got)
	}
}

func TestDelegateResourceSupervision_QuietWatchdogRepeatsEachWindowWhileSilent(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	// A permanently silent running delegate wakes once per further quiet
	// window, each under a fresh attention id so the durable fold keeps every
	// repeat instead of replaying the first.
	for i := uint64(1); i <= 3; i++ {
		clock.Advance(delegateQuietWindow)
		if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
			t.Fatalf("window %d tick: %v", i, err)
		}
		// A tick inside the same window must not add a second wake.
		if err := root.runDelegateQuietWatchdogTick(lease, clock.Now().Add(delegateQuietWindow/2)); err != nil {
			t.Fatalf("window %d mid-window tick: %v", i, err)
		}
		got := pendingQuietAttention(t, root)
		if len(got) != int(i) {
			t.Fatalf("after %d quiet window(s) attention = %#v, want %d", i, got, i)
		}
		if want := delegateQuietAttentionIDForStretch(lease, i); got[len(got)-1] != want {
			t.Fatalf("window %d attention id = %q, want %q", i, got[len(got)-1], want)
		}
	}

	// Activity rearms: the next wake is due a full window after the activity,
	// not after the previous wake, and lands under a fresh id again.
	if err := controller.ReportActivity(lease, clock.Now()); err != nil {
		t.Fatalf("ReportActivity: %v", err)
	}
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now().Add(delegateQuietWindow-time.Second)); err != nil {
		t.Fatalf("post-activity pre-window tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 3 {
		t.Fatalf("post-activity pre-window attention = %#v, want 3", got)
	}
	clock.Advance(delegateQuietWindow)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("post-activity window tick: %v", err)
	}
	got := pendingQuietAttention(t, root)
	if len(got) != 4 {
		t.Fatalf("post-activity window attention = %#v, want 4", got)
	}
	if want := delegateQuietAttentionIDForStretch(lease, 5); got[len(got)-1] != want {
		t.Fatalf("post-activity attention id = %q, want %q", got[len(got)-1], want)
	}
}

// quietFrameSilence extracts the duration token a quiet frame carries — the
// same token the shared client parses out of the sentence
// ("quiet for <token>; last activity: <RFC 3339>").
func quietFrameSilence(t *testing.T, frame string) string {
	t.Helper()
	const prefix, suffix = "quiet for ", "; last activity: "
	start := strings.Index(frame, prefix)
	end := strings.Index(frame, suffix)
	if start < 0 || end < 0 || end <= start+len(prefix) {
		t.Fatalf("quiet frame %q carries no silence token", frame)
	}
	return frame[start+len(prefix) : end]
}

// A repeat of one silent stretch must name the silence measured at its due
// boundary — the first wake names the window, each later wake the ongoing
// total — instead of re-stating the window byte-identically, which read in
// the field as stale re-feeds of one stored frame.
func TestDelegateResourceSupervision_QuietWatchdogRepeatFramesNameTheMeasuredSilence(t *testing.T) {
	root, _, lease, clock := newStableQuietSupervisionHarness(t)
	for i := 1; i <= 3; i++ {
		// Window 3's tick lands two minutes past its due boundary: the frame
		// must still name the silence at the boundary ("30m"), not at the
		// admitted tick ("32m"), which is what keeps a deferred or re-admitted
		// claim re-minting identical content under one stretch identity.
		if i == 3 {
			clock.Advance(delegateQuietWindow + 2*time.Minute)
		} else {
			clock.Advance(delegateQuietWindow)
		}
		if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
			t.Fatalf("window %d tick: %v", i, err)
		}
		fold := rootAttentionFold(t, root)
		if len(fold.order) != i {
			t.Fatalf("after %d quiet window(s) attention = %#v, want %d", i, fold.order, i)
		}
		attentionID := fold.order[len(fold.order)-1]
		if want := delegateQuietAttentionIDForStretch(lease, uint64(i)); attentionID != want {
			t.Fatalf("window %d attention id = %q, want %q", i, attentionID, want)
		}
		frame := fold.content[attentionID].Text()
		want := fmt.Sprintf("%dm", i*int(delegateQuietWindow/time.Minute))
		if got := quietFrameSilence(t, frame); got != want {
			t.Fatalf("window %d frame silence = %q, want the measured silence %q (frame %q)", i, got, want, frame)
		}
	}
}

// A quiet claim dropped because the receiver is mid-turn re-admits under the
// same stretch identity once the receiver is idle, and the re-admitted frame
// carries the content the dropped claim minted: the silence is measured at
// the wake's due boundary, which no re-admission moves, so receiver-busy time
// cannot shift it. Measuring at the admitted tick instead would re-mint a
// different content under the same attention id on every re-admission, which
// the durable fold rejects as conflicting content — wedging the watchdog on
// that id forever.
func TestDelegateResourceSupervision_QuietWatchdogDeferredClaimReadmitsIdenticalContent(t *testing.T) {
	root, _, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow + 2*time.Minute)

	// The receiver is mid-turn: the tick mints the stretch's first claim and
	// drops it uncommitted, so nothing appends yet.
	root.mu.Lock()
	idle := root.state
	root.state = SessionProcessing
	root.mu.Unlock()
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 0 {
		t.Fatalf("deferred quiet attention = %#v, want none appended", got)
	}

	// A later tick, with the receiver idle again, re-admits the same stretch
	// identity. The frame must name the due-boundary silence — the window —
	// not the re-admission tick's later measurement.
	root.mu.Lock()
	root.state = idle
	root.mu.Unlock()
	clock.Advance(3 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	fold := rootAttentionFold(t, root)
	if len(fold.order) != 1 {
		t.Fatalf("re-admitted quiet attention = %#v, want exactly one", fold.order)
	}
	attentionID := fold.order[0]
	if want := delegateQuietAttentionIDForStretch(lease, 1); attentionID != want {
		t.Fatalf("re-admitted attention id = %q, want %q", attentionID, want)
	}
	want := fmt.Sprintf("%dm", int(delegateQuietWindow/time.Minute))
	if got := quietFrameSilence(t, fold.content[attentionID].Text()); got != want {
		t.Fatalf("re-admitted frame silence = %q, want the due-boundary silence %q", got, want)
	}
}

// quietSteerHarness is the quiet supervision harness with a steer-capable
// target runtime on the shared fake clock, so a persisted steer's timestamp is
// the exact virtual instant the test chooses.
func quietSteerHarness(t *testing.T) (*Session, *delegateTreeController, delegateLease, *agenttest.FakeClock) {
	t.Helper()
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	childWriter, err := transcript.NewWriter(transcriptPath(root.stateDir, "child-"+lease.delegateID), transcript.Header{SessionID: "child-" + lease.delegateID})
	if err != nil {
		t.Fatalf("steer runtime transcript: %v", err)
	}
	t.Cleanup(func() { _ = childWriter.Close() })
	child := &Session{clock: clock, delegateController: controller}
	child.attachTranscript(childWriter, nil)
	controller.mu.Lock()
	controller.live[lease.delegateID].runtime = child
	controller.live[lease.delegateID].binding.runtime = child
	controller.mu.Unlock()
	return root, controller, lease, clock
}

// persistQuietSteer persists one steer through the controller's real begin,
// append, and complete path at the current fake-clock instant.
func persistQuietSteer(t *testing.T, controller *delegateTreeController, lease delegateLease, message string) {
	t.Helper()
	claim, err := controller.BeginSteerPersistence(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("BeginSteerPersistence: %v", err)
	}
	entry, err := claim.runtime.appendDelegateSteeringDurably(message, claim.entryID)
	if err != nil {
		t.Fatalf("append steering: %v", err)
	}
	if _, err := controller.CompleteSteerPersistence(claim, entry); err != nil {
		t.Fatalf("CompleteSteerPersistence: %v", err)
	}
}

func TestDelegateResourceSupervision_QuietWatchdogRearmsOnSteerActivity(t *testing.T) {
	root, controller, lease, clock := quietSteerHarness(t)

	// First wake at the window boundary.
	clock.Advance(delegateQuietWindow)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("first quiet tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 {
		t.Fatalf("first wake attention = %#v, want 1", got)
	}

	// A steer persisted just before the next boundary is fresh parent-visible
	// activity and must reset the cadence (the steer path advances activityAt
	// directly rather than through ReportActivityPhase).
	clock.Advance(delegateQuietWindow - time.Minute)
	persistQuietSteer(t, controller, lease, "late steer")
	controller.mu.Lock()
	activityAt := controller.live[lease.delegateID].activityAt
	notified := controller.live[lease.delegateID].quietNotified
	controller.mu.Unlock()
	if !activityAt.Equal(clock.Now()) {
		t.Fatalf("steer activityAt = %v, want %v", activityAt, clock.Now())
	}
	if notified {
		t.Fatal("steer left the quiet-notified repeat state set")
	}

	// The boundary that would have fired without the rearm must stay silent:
	// the next wake needs a full window from the steer, not from the last wake.
	clock.Advance(time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("post-steer boundary tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 {
		t.Fatalf("post-steer attention = %#v, want still 1", got)
	}
	clock.Advance(delegateQuietWindow - time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("post-steer window tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 2 {
		t.Fatalf("post-steer window attention = %#v, want 2", got)
	}
}

func TestDelegateResourceSupervision_QuietWatchdogRetiresInFlightClaimOnSteer(t *testing.T) {
	root, controller, lease, clock := quietSteerHarness(t)
	clock.Advance(delegateQuietWindow)
	// The watchdog admits and durably appends its claim, but has not completed
	// it yet — the append-then-complete window a racing steer can land in.
	claim, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil {
		t.Fatalf("BeginQuietAttention: %v", err)
	}
	if claim == nil {
		t.Fatal("quiet claim not admitted at the window boundary")
	}
	if _, err := root.appendQuietAttentionAtTurnBoundary(claim.attentionID, claim.content); err != nil {
		t.Fatalf("append quiet attention: %v", err)
	}

	// A steer lands while that claim is in flight.
	clock.Advance(time.Minute)
	persistQuietSteer(t, controller, lease, "in-flight steer")
	controller.mu.Lock()
	sequence := controller.live[lease.delegateID].quietSequence
	controller.mu.Unlock()
	if sequence != 2 {
		t.Fatalf("in-flight steer left quietSequence = %d, want 2 (the consumed id must be retired)", sequence)
	}
	if err := controller.CompleteQuietAttention(claim, true); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("CompleteQuietAttention on steered claim = %v, want stale lease", err)
	}

	// The next wake must append under a fresh id. Reusing the consumed id with
	// the newer activity timestamp folds as permanent conflicting content.
	clock.Advance(delegateQuietWindow)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("post-steer wake: %v", err)
	}
	got := pendingQuietAttention(t, root)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("post-steer wake ids = %#v, want two distinct ids", got)
	}
}

func TestDelegateResourceSupervision_QuietWatchdogArmsStaleDurableAttention(t *testing.T) {
	root, controller, lease, clock := quietSteerHarness(t)
	wake := make(chan struct{}, 4)
	root.SetNotifyFunc(func() { wake <- struct{}{} })

	clock.Advance(delegateQuietWindow)
	claim, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil {
		t.Fatalf("BeginQuietAttention: %v", err)
	}
	if claim == nil {
		t.Fatal("quiet claim not admitted at the window boundary")
	}
	// The watchdog's durable append succeeds; a steer then retires the claim
	// before the tick completes it, so completion is stale. The durable
	// attention must still be armed or nothing ever delivers it.
	deferred, appendErr := root.appendQuietAttentionAtTurnBoundary(claim.attentionID, claim.content)
	if deferred || appendErr != nil {
		t.Fatalf("append quiet attention: deferred=%t err=%v", deferred, appendErr)
	}
	clock.Advance(time.Minute)
	persistQuietSteer(t, controller, lease, "racing steer")
	if err := root.completeQuietWatchdogClaim(claim, appendErr); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("completeQuietWatchdogClaim = %v, want stale lease", err)
	}
	root.attentionMu.Lock()
	_, armed := root.rootAttentionWakeIDs[claim.attentionID]
	root.attentionMu.Unlock()
	if !armed {
		t.Fatalf("stale durable attention %q was not armed", claim.attentionID)
	}
	select {
	case <-wake:
	case <-time.After(quietHubTripwire): // TRIPWIRE: arming is synchronous notify, not a poll.
		t.Fatal("no wake fired for the armed stale attention")
	}
}

func TestDelegateResourceSupervision_QuietWatchdogRearmsOnEqualTimestampSteer(t *testing.T) {
	root, controller, lease, clock := quietSteerHarness(t)
	clock.Advance(delegateQuietWindow)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("first quiet tick: %v", err)
	}
	// Seed the equal-instant case: activity already sits at the instant the
	// steer will carry, so its timestamp is not strictly after activityAt.
	controller.mu.Lock()
	controller.live[lease.delegateID].activityAt = clock.Now()
	controller.mu.Unlock()
	persistQuietSteer(t, controller, lease, "equal-instant steer")
	controller.mu.Lock()
	notified := controller.live[lease.delegateID].quietNotified
	controller.mu.Unlock()
	if notified {
		t.Fatal("equal-timestamp steer left the quiet-notified state set")
	}

	// The next window must deliver a distinct repeat rather than a replay.
	clock.Advance(delegateQuietWindow)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("post-steer window tick: %v", err)
	}
	got := pendingQuietAttention(t, root)
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("equal-timestamp repeat ids = %#v, want two distinct ids", got)
	}
}

func TestDelegateLiveState_RearmQuietCadencePredicate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		live         delegateLiveState
		wantSequence uint64
		wantNotified bool
	}{
		{name: "no quiet state", live: delegateLiveState{quietSequence: 3}, wantSequence: 3},
		{name: "committed wake", live: delegateLiveState{quietSequence: 3, quietNotified: true, quietNotifiedAt: time.Unix(5, 0)}, wantSequence: 4},
		{name: "in-flight current claim", live: delegateLiveState{quietSequence: 3, quietClaim: &delegateQuietAttentionClaim{sequence: 3}}, wantSequence: 4},
		{name: "in-flight stale claim", live: delegateLiveState{quietSequence: 3, quietClaim: &delegateQuietAttentionClaim{sequence: 2}}, wantSequence: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := tc.live
			live.rearmQuietCadenceLocked()
			if live.quietSequence != tc.wantSequence {
				t.Fatalf("quietSequence = %d, want %d", live.quietSequence, tc.wantSequence)
			}
			if live.quietNotified != tc.wantNotified {
				t.Fatalf("quietNotified = %t, want %t", live.quietNotified, tc.wantNotified)
			}
			if !live.quietNotifiedAt.IsZero() {
				t.Fatalf("quietNotifiedAt = %v, want zero", live.quietNotifiedAt)
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietAttentionAppendFailureRetriesSameIdentity(t *testing.T) {
	root, _, lease, clock := newStableQuietSupervisionHarness(t)
	root.mu.Lock()
	writer := root.transcript
	root.mu.Unlock()
	if err := writer.Close(); err != nil {
		t.Fatalf("close receiver writer: %v", err)
	}
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err == nil {
		t.Fatal("quiet tick succeeded without a writable receiver transcript")
	}
	path := transcriptPath(root.stateDir, root.id)
	reopened, _, err := transcript.OpenWriterForSession(path, root.id)
	if err != nil {
		t.Fatalf("reopen receiver writer: %v", err)
	}
	root.mu.Lock()
	root.transcript = reopened
	root.transcriptReady = true
	root.mu.Unlock()
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("retry quiet tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(lease) {
		t.Fatalf("retried quiet attention identities = %#v", got)
	}

	controller, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerRunning(t, controller, "dlg_owner", "")
	seedDelegateControllerRunning(t, controller, "dlg_nested", "dlg_owner")
	nestedClock := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC))
	ownerSessionID := controller.durable["dlg_owner"].Descriptor.ChildSessionID
	ownerWriter, err := transcript.NewWriter(transcriptPath(controller.stateDir, ownerSessionID), transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("create nested quiet owner transcript: %v", err)
	}
	t.Cleanup(func() { _ = ownerWriter.Close() })
	owner := &Session{
		id:                    ownerSessionID,
		stateDir:              controller.stateDir,
		clock:                 nestedClock,
		delegateController:    controller,
		delegateRootSessionID: "root-session",
		owningDelegateID:      "dlg_owner",
		state:                 SessionIdle,
	}
	owner.attachTranscript(ownerWriter, nil)
	retryWake := make(chan struct{}, 1)
	owner.SetNotifyFunc(func() { retryWake <- struct{}{} })
	controller.mu.Lock()
	controller.rootRuntime = &Session{id: "root-session", delegateController: controller}
	controller.live["dlg_owner"].runtime = owner
	controller.live["dlg_owner"].binding.runtime = owner
	controller.live["dlg_nested"].runtime = &Session{delegateController: controller}
	controller.live["dlg_nested"].binding.runtime = controller.live["dlg_nested"].runtime
	controller.live["dlg_nested"].activityAt = nestedClock.Now()
	controller.mu.Unlock()
	readCalls := 0
	injected := errors.New("injected nested quiet arm fold failure")
	owner.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		readCalls++
		if readCalls == 3 {
			return delegateAttentionFold{}, injected
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	nestedClock.Advance(delegateQuietWindow)
	nestedLease := delegateLease{delegateID: "dlg_nested", generation: 1}
	if err := owner.runDelegateQuietWatchdogTick(nestedLease, nestedClock.Now()); !errors.Is(err, injected) {
		t.Fatalf("nested quiet arm error = %v, want %v", err, injected)
	}
	nestedAttentionID := delegateQuietAttentionID(nestedLease)
	owner.attentionMu.Lock()
	_, retrying := owner.delegateAttentionArmIDs[nestedAttentionID]
	owner.attentionMu.Unlock()
	controller.mu.Lock()
	quietNotified := controller.live["dlg_nested"].quietNotified
	controller.mu.Unlock()
	if !retrying || !quietNotified {
		t.Fatalf("failed nested quiet arm retention = retrying:%t quietNotified:%t", retrying, quietNotified)
	}
	owner.cfg.testOnly.delegateAttentionReadFold = nil
	nestedClock.Advance(jobNotificationRetryInitialDelay)
	<-retryWake
	// The wake is emitted inside the retry callback before it clears the
	// resolved arm ID. Wait for that callback to finish before inspecting its
	// final bookkeeping state.
	nestedClock.Drain()
	controller.mu.Lock()
	ownerAttention := controller.durable["dlg_owner"].NeedsAttention
	controller.mu.Unlock()
	owner.attentionMu.Lock()
	_, retrying = owner.delegateAttentionArmIDs[nestedAttentionID]
	owner.attentionMu.Unlock()
	if !ownerAttention || retrying {
		t.Fatalf("nested quiet arm retry = attention:%t retrying:%t, want true/false", ownerAttention, retrying)
	}
}

func TestDelegateResourceSupervision_QuietAttentionWaitsForOwnerTurnBoundary(t *testing.T) {
	root, _, lease, clock := newStableQuietSupervisionHarness(t)
	root.mu.Lock()
	root.state = SessionProcessing
	root.mu.Unlock()
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("processing-owner quiet tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 0 {
		t.Fatalf("quiet attention split an active owner turn: %#v", got)
	}

	root.mu.Lock()
	root.state = SessionIdle
	root.mu.Unlock()
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("idle-owner quiet retry: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(lease) {
		t.Fatalf("boundary-retried quiet attention identities = %#v", got)
	}
}

func TestDelegateControllerFinalizationDrainsAdmittedQuietAttention(t *testing.T) {
	tests := []struct {
		name string
		mode delegateSettlementMode
	}{
		{name: "ordinary", mode: delegateSettlementOrdinary},
		{name: "terminal", mode: delegateSettlementTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			clock.Advance(delegateQuietWindow)
			quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if err != nil || quiet == nil {
				t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
			}
			finalization, continueRun, err := controller.BeginFinalization(lease, test.mode)
			if err != nil || continueRun || finalization == nil {
				t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
			}
			select {
			case <-finalization.ready:
				t.Fatal("finalization became ready before admitted quiet attention completed")
			default:
			}
			if err := controller.CompleteQuietAttention(quiet, false); err != nil {
				t.Fatalf("CompleteQuietAttention: %v", err)
			}
			select {
			case <-finalization.ready:
			default:
				t.Fatal("finalization remained blocked after quiet attention aborted")
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietAttentionFsyncPrecedesFinalization(t *testing.T) {
	tests := []struct {
		name string
		mode delegateSettlementMode
	}{
		{name: "ordinary", mode: delegateSettlementOrdinary},
		{name: "terminal", mode: delegateSettlementTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			clock.Advance(delegateQuietWindow)
			quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if err != nil || quiet == nil {
				t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
			}
			finalization, continueRun, err := controller.BeginFinalization(lease, test.mode)
			if err != nil || continueRun || finalization == nil {
				t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
			}

			var prematureErr error
			if test.mode == delegateSettlementOrdinary {
				_, prematureErr = controller.CompleteSettlement(finalization, nil)
			} else {
				_, prematureErr = controller.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "provider_failed"})
			}
			if !errors.Is(prematureErr, errDelegateTargetBusy) {
				t.Fatalf("finalization before quiet fsync error = %v, want target busy", prematureErr)
			}

			deferred, err := root.appendQuietAttentionAtTurnBoundary(quiet.attentionID, quiet.content)
			if err != nil || deferred {
				t.Fatalf("appendQuietAttentionAtTurnBoundary = deferred:%t err:%v", deferred, err)
			}
			if err := controller.CompleteQuietAttention(quiet, true); err != nil {
				t.Fatalf("CompleteQuietAttention after fsync: %v", err)
			}
			select {
			case <-finalization.ready:
			default:
				t.Fatal("finalization remained blocked after quiet attention fsync")
			}
			if test.mode == delegateSettlementOrdinary {
				if _, err := controller.CompleteSettlement(finalization, nil); err != nil {
					t.Fatalf("CompleteSettlement after quiet fsync: %v", err)
				}
			}
			if _, err := controller.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "provider_failed"}); err != nil {
				t.Fatalf("FinishGeneration after quiet fsync: %v", err)
			}
			if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != quiet.attentionID {
				t.Fatalf("durable quiet attention after finalization = %#v, want %q", got, quiet.attentionID)
			}
		})
	}
}

func TestDelegateResourceSupervision_FinalizationRejectsLaterQuietAttention(t *testing.T) {
	tests := []struct {
		name string
		mode delegateSettlementMode
	}{
		{name: "ordinary", mode: delegateSettlementOrdinary},
		{name: "terminal", mode: delegateSettlementTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			clock.Advance(delegateQuietWindow)
			finalization, continueRun, err := controller.BeginFinalization(lease, test.mode)
			if err != nil || continueRun || finalization == nil {
				t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
			}
			quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if quiet != nil || !errors.Is(err, errDelegateTargetBusy) {
				t.Fatalf("BeginQuietAttention after finalization = claim:%#v err:%v, want target busy", quiet, err)
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietAttentionClaimDrainsBeforeStopCompletion(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow)
	quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || quiet == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
	}
	finalization, continueRun, err := controller.BeginFinalization(lease, delegateSettlementTerminal)
	if err != nil || continueRun || finalization == nil {
		t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
	}
	select {
	case <-finalization.ready:
		t.Fatal("finalization became ready before admitted quiet attention completed")
	default:
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile with quiet claim: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before its pre-admitted quiet claim drained")
	default:
	}
	deferred, err := root.appendQuietAttentionAtTurnBoundary(quiet.attentionID, quiet.content)
	if err != nil || deferred {
		t.Fatalf("appendQuietAttentionAtTurnBoundary = deferred:%t err:%v", deferred, err)
	}
	if err := controller.CompleteQuietAttention(quiet, true); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("CompleteQuietAttention after stop = %v, want stale lease after durable drain", err)
	}
	select {
	case <-finalization.ready:
	default:
		t.Fatal("finalization remained blocked after stopped quiet attention drained")
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile while finalization owns generation: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before finalization recorded stopped outcome")
	default:
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after quiet drain: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after finalization: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after quiet drain and stopped finalization")
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != quiet.attentionID {
		t.Fatalf("durable quiet attention after stop = %#v, want %q", got, quiet.attentionID)
	}
}

func TestDelegateControllerOrdinaryFinalizationAdoptsExactCoveringStop(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow)
	quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || quiet == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}

	finalization, continueRun, err := controller.BeginFinalization(lease, delegateSettlementOrdinary)
	if err != nil || continueRun || finalization == nil {
		t.Fatalf("BeginFinalization after covered stop = claim:%#v continue:%t err:%v", finalization, continueRun, err)
	}
	if finalization.mode != delegateSettlementTerminal {
		t.Fatalf("effective finalization mode = %d, want terminal stop", finalization.mode)
	}
	select {
	case <-finalization.ready:
		t.Fatal("stopped finalization became ready before admitted quiet attention completed")
	default:
	}
	if err := controller.CompleteQuietAttention(quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	select {
	case <-finalization.ready:
	default:
		t.Fatal("stopped finalization remained blocked after quiet attention aborted")
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after quiet drain: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after stopped finalization: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after its exact generation finalized")
	}
	controller.mu.Lock()
	aggregate := controller.durable[lease.delegateID]
	live := controller.live[lease.delegateID]
	stop := controller.stop
	controller.mu.Unlock()
	if aggregate == nil || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("durable outcome = %#v, want stopped", aggregate)
	}
	if live == nil || live.binding != nil || stop != nil {
		t.Fatalf("released state = live:%#v stop:%#v, want no binding or active stop", live, stop)
	}
}

func TestDelegateControllerOrdinaryFinalizationRetainsModeWhenItPrecedesStop(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow)
	quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || quiet == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
	}
	finalization, continueRun, err := controller.BeginFinalization(lease, delegateSettlementOrdinary)
	if err != nil || continueRun || finalization == nil {
		t.Fatalf("BeginFinalization before stop = claim:%#v continue:%t err:%v", finalization, continueRun, err)
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if finalization.mode != delegateSettlementOrdinary {
		t.Fatalf("effective finalization mode = %d, want ordinary winner", finalization.mode)
	}
	if err := controller.CompleteQuietAttention(quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	if _, err := controller.CompleteSettlement(finalization, nil); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CompleteSettlement after stop = %v, want target busy", err)
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after quiet drain: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after stopped finalization: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after ordinary finalization winner drained")
	}
}

func TestDelegateResourceSupervision_StopBeforeOrdinaryFinalizationDrainsQuietAttention(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return agenttest.FinalResponse("ordinary result before covered stop")
	}})
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
	type boundaryResult struct {
		quiet *delegateQuietAttentionClaim
		stop  delegateStopResult
		err   error
	}
	boundary := make(chan boundaryResult, 1)
	var boundaryOnce sync.Once
	updateSessionTestConfig(child.sess, func(cfg *testConfig) {
		cfg.subagentBeforeSettlement = func(*subagent) {
			boundaryOnce.Do(func() {
				root.delegateController.mu.Lock()
				live := root.delegateController.live[lease.delegateID]
				activityAt := live.activityAt
				root.delegateController.mu.Unlock()
				quiet, err := root.delegateController.BeginQuietAttention(root, lease, activityAt.Add(delegateQuietWindow))
				if err != nil || quiet == nil {
					boundary <- boundaryResult{err: fmt.Errorf("BeginQuietAttention = %#v, %w", quiet, err)}
					return
				}
				stop, _, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.delegateRootSessionID), lease.delegateID)
				boundary <- boundaryResult{quiet: quiet, stop: stop, err: err}
			})
		}
	})
	close(release)
	observed := <-boundary
	if observed.err != nil {
		t.Fatalf("stop-before-finalization boundary: %v", observed.err)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	root.delegateController.mu.Lock()
	stop := root.delegateController.stop
	root.delegateController.mu.Unlock()
	if stop == nil || stop.requestSeq != observed.stop.requestSeq {
		t.Fatalf("active stop = %#v, want request %d", stop, observed.stop.requestSeq)
	}

	select {
	case <-done:
		root.delegateController.mu.Lock()
		_, active := stop.active[lease]
		live := root.delegateController.live[lease.delegateID]
		bindingRetained := live != nil && live.binding != nil && live.binding.lease == lease
		root.delegateController.mu.Unlock()
		if err := root.delegateController.CompleteQuietAttention(observed.quiet, false); err != nil {
			t.Fatalf("cleanup CompleteQuietAttention: %v", err)
		}
		if _, err := root.delegateController.FinishGeneration(lease, delegateFinish{}); err != nil {
			t.Fatalf("cleanup FinishGeneration: %v", err)
		}
		if _, err := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController)); err != nil {
			t.Fatalf("cleanup Reconcile: %v", err)
		}
		t.Fatalf("ordinary runner exited before quiet completion; stop active = %t, binding retained = %t", active, bindingRetained)
	case <-stop.progress:
	}
	select {
	case <-done:
		t.Fatal("ordinary runner exited after claiming the stop but before quiet completion")
	default:
	}
	if err := root.delegateController.CompleteQuietAttention(observed.quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	<-done
	if _, err := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController)); err != nil {
		t.Fatalf("Reconcile after stopped finalization: %v", err)
	}
	select {
	case <-observed.stop.done:
	default:
		t.Fatal("stop remained pending after runner drained quiet attention")
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeStopped)
}

func TestDelegateResourceSupervision_RestartStartsNoWatchdogOrProvider(t *testing.T) {
	clock := agenttest.NewFakeClock()
	controller, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_target", "")
	if err := controller.store.Close(); err != nil {
		t.Fatalf("close controller store: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen delegate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		now:           clock.Now,
	}); err != nil {
		t.Fatalf("reopen controller: %v", err)
	}
	clock.Advance(24 * time.Hour)
	if clock.BlockedCount() != 0 {
		t.Fatalf("restart armed %d watchdog/provider waiters", clock.BlockedCount())
	}
}

type stableSubagentStopObservation struct {
	providerRequests    int
	hookRuns            int
	continuationSawHook bool
	output              string
}

func runStableSubagentStopHook(t *testing.T, blocking bool) stableSubagentStopObservation {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "subagent-stop-runs")
	decision := `{}`
	if blocking {
		decision = `{"decision":"block","reason":"address hook feedback"}`
	}
	pluginDir := writeStableSubagentStopPlugin(t, marker, decision)
	fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		// The hook appends to a persistent marker, so this is intentionally a
		// mutating fixture rather than a read-only role.
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	continuationSawHook := false
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("initial result") },
	})
	if blocking {
		fixture.adapter.appendSteps(func(llm.Request) llm.Response {
			_, err := os.Stat(marker)
			continuationSawHook = err == nil
			return agenttest.FinalResponse("continued after hook")
		})
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedDelegateOutcome(t, outcome)
	if outcome.result.Err != nil {
		t.Fatalf("stable hook run: %v", outcome.result.Err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read hook marker: %v", err)
	}
	return stableSubagentStopObservation{
		providerRequests:    supervisionRequestCount(fixture.adapter),
		hookRuns:            len(strings.Fields(string(raw))),
		continuationSawHook: continuationSawHook,
		output:              outcome.result.Output,
	}
}

func writeStableSubagentStopPlugin(t *testing.T, marker, decision string) string {
	t.Helper()
	pluginDir := makePluginDir(t, "task7-supervision")
	hooksDir := filepath.Join(pluginDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := "printf 'run\\n' >> " + shellquote.Literal(marker) + "; printf '%s' " + shellquote.Literal(decision)
	payload := map[string]any{"hooks": map[string]any{"SubagentStop": []any{map[string]any{
		"matcher": "*",
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	}}}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

func restoreSupervisionRoot(t *testing.T, fixture coldDelegateFixture, clock *agenttest.FakeClock) *Session {
	t.Helper()
	restore := RestoreSessionConfig{
		StateDir:    fixture.stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(fixture.workspace),
			// The supervision suite's subject is the warm attention-drive
			// machinery itself — a deliberately retained runtime under an
			// explicit in-process mode — not the idle retention policy the
			// default exercises.
			disableDelegateIdleRelease: true,
		},
	}
	if clock != nil {
		restore.clock = clock
	}
	root, err := RestoreSessionFromMetaWithConfig(
		fixture.client,
		fixture.profile,
		execenv.NewLocalExecutionEnvironment(fixture.workspace),
		fixture.meta,
		restore,
	)
	if err != nil {
		t.Fatalf("restore supervision root: %v", err)
	}
	t.Cleanup(root.Close)
	return root
}

// serveSupervisionRootWakes stands in for the server's input loop on the
// root (serve.go's SetNotifyFunc): a wake is coalesced into a one-slot
// channel, and each one runs the root's stable delegate attention drive, the
// first thing the root's notification turn does (acceptNotificationInput).
// Attention whose child-side drive was refused falls back to that drive
// through two root wakes: the one releaseFinalizationLocked sends when a
// finished generation is released with attention still owed, and the root's
// retry (retryDelegateAttentionLater). A root nothing wakes leaves it undriven
// (TestSupervisionRootDrivesAttentionTheChildDropped). Tests that call
// drivePendingDelegateAttention or run root turns themselves don't call
// this. The loop stops before the root closes, since cleanups run
// last-registered first.
func serveSupervisionRootWakes(t *testing.T, root *Session) {
	t.Helper()
	wakes := make(chan struct{}, 1)
	stop := make(chan struct{})
	stopped := make(chan struct{})
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			case <-wakes:
				root.drivePendingDelegateAttention()
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-stopped
	})
}

// waitForStableSupervisionRun blocks until the stable child owes no more
// supervision work: no run, drive turn, or finalization is live, and no
// delegate attention is still waiting for a run to be dispatched.
//
// It cannot simply join the child's completion channel. That channel is
// REPLACED (resetSubagentForRunLocked) at the start of every run, so the
// channel current when this helper is entered can still be the PREVIOUS run's:
// arming attention only notifies, and the drive that notify triggers is
// refused while the prior run is still running or finalizing, and is deferred
// to a retry while the controller still holds the prior generation. Joining
// that stale channel returns while the awaited run is only starting, so a
// caller that then reads the delegate event log sees the PRIOR generation's
// run-finished record.
func waitForStableSupervisionRun(t *testing.T, root *Session, childID string, adapters ...*fakeAdapter) {
	t.Helper()
	sub := root.subagents.get(childID)
	if sub == nil {
		t.Fatalf("stable child %q was not tracked", childID)
	}
	quiesced := false
	defer func() {
		if !quiesced {
			var adapter *fakeAdapter
			if len(adapters) != 0 {
				adapter = adapters[0]
			}
			t.Logf("failed supervision wait snapshot: %#v", stableSupervisionFailureSnapshot(root, sub, adapter))
		}
	}()
	if refusal := stableSupervisionEntryRefusal(sub, childID); refusal != "" {
		t.Fatalf("%s", refusal)
	}
	desc := fmt.Sprintf("stable child %q supervision to quiesce", childID)
	// why is the last check that refused; a timeout reports it with the state
	// behind it, since the hang this guards against has only shown on CI.
	// Only the wait's own timeout reports: a failure from earlier in the test
	// is not this wait's.
	var why string
	failedBefore := t.Failed()
	defer func() {
		if !failedBefore && t.Failed() {
			t.Logf("last refusal: %s; state: %s", why, stableSupervisionState(root, sub))
		}
	}()
	testwait.Until(t, desc, func() bool {
		why = stableSupervisionRefusal(sub)
		return why == ""
	})
	quiesced = true
}

// stableSupervisionEntryRefusal is waitForStableSupervisionRun's entry
// precondition: it returns the fatal message when sub positively has no
// completion channel to wait on, or "" when the channel is present or when
// sub.mu is busy.
//
// The read is non-blocking. waitForStableSupervisionRun makes it before it
// enters its 30s bounded poll, so a blocking sub.mu.Lock() here would wait on a
// stall that holds sub.mu, keeping the wait from ever reaching its poll — and
// so from ever firing the bound and the #4513 timeout report that runs only
// after it — the same class #4548 removed from the poll predicate. A busy
// sub.mu observes neither presence nor absence, so it returns "" and falls
// through to the poll, whose predicate re-TryLocks sub.mu and refuses
// ("subagent lock busy") for as long as it stays held; a busy source therefore
// keeps the wait waiting and can never be read as a present channel or as a
// missing one. The fatal is returned only after positively observing
// sub.done == nil under its own lock. sub.done is nil only until the child's
// first run starts — a cold-restored record is built without a runner channel,
// and a run start assigns a fresh one (resetSubagentForRunLocked) — and it is
// never nilled once assigned, so the fatal is reachable only for a child that
// has not run in this process.
func stableSupervisionEntryRefusal(sub *subagent, childID string) string {
	if !sub.mu.TryLock() {
		return ""
	}
	channel := sub.done
	sub.mu.Unlock()
	if channel != nil {
		return ""
	}
	return fmt.Sprintf("stable child %q has no completion channel", childID)
}

// stableSupervisionRefusal is the first stage of stable child supervision
// still holding sub, as waitForStableSupervisionRun reads them, or "" once
// all are quiet.
//
// Every source is read with TryLock: a source production holds is, by
// definition, not quiesced, so a busy lock REFUSES (keeps the wait waiting)
// rather than blocking. testwait.Until calls this inline, so a blocking read
// would keep the wait's bound from ever being reached; a busy read always
// returns before the deadline check is skipped again. The predicate returns ""
// only after positively observing every stage under its own lock, so a busy
// lock can only add a refusal — it can never turn an unquiesced run into a
// quiesced one.
func stableSupervisionRefusal(sub *subagent) string {
	// sub.sess is assigned once, at subagent creation, and never reassigned
	// (delegate_restore_idle.go's cold restore and subagents.go's start), so an
	// unlocked read cannot race a writer; stableSupervisionState reads it the
	// same way.
	sess := sub.sess
	if !sub.mu.TryLock() {
		return "subagent lock busy"
	}
	closed := sub.closed
	sub.mu.Unlock()
	// Dispatching an armed attention hands the work along a chain of
	// states, and no single one of them covers the whole chain: an arm
	// awaiting retry, attention pending with no reservation yet, a
	// reservation that has consumed the pending id but not yet committed,
	// an open generation whose run goroutine has not started, and finally
	// the child's own run flags. Each stage is entered before its
	// predecessor is left, so reading them in dispatch order means work
	// that races past one read is caught by a later one.
	if sess != nil && !closed {
		if refusal := stableSupervisionAttentionRefusal(sess); refusal != "" {
			return refusal
		}
	}
	if !sub.mu.TryLock() {
		return "subagent lock busy"
	}
	done := sub.done
	live := sub.running || sub.driving || sub.finalizing
	sub.mu.Unlock()
	if live || done == nil {
		return fmt.Sprintf("child live=%t completion channel=%t", live, done != nil)
	}
	select {
	case <-done:
	default:
		return "completion channel open"
	}
	if sess != nil {
		// The session namer is the last stage: a run launches it and it
		// can outlive the run, taking the session lock to name and save
		// the session.
		if !sess.mu.TryLock() {
			return "session lock busy"
		}
		naming := sess.naming.pending
		sess.mu.Unlock()
		if naming != 0 {
			return fmt.Sprintf("%d session namer runs pending", naming)
		}
	}
	return ""
}

// stableSupervisionAttentionRefusal reads sess's attention stages — an arm
// awaiting retry, attention pending with no reservation yet, a reservation that
// has consumed the pending id but not yet committed, and the controller's open
// run — without waiting on any lock, refusing when a source is busy. It mirrors
// what the predicate read through the Session helpers
// (hasPendingDelegateAttentionArmRetry, pendingDelegateAttentionIDs ->
// delegateAttentionFoldLocked) and through delegateTreeController.reservedAttentionID,
// which take these locks blocking and so could hold the predicate past its bound.
func stableSupervisionAttentionRefusal(sess *Session) string {
	// The arm-retry ids and the pending-attention fold are both read under
	// attentionMu (which also guards the fold cursor); reading them together
	// under one TryLock is a snapshot at least as strong as the two separate
	// blocking reads it replaces, so it can never read quiesced where they
	// would have refused.
	if !sess.attentionMu.TryLock() {
		return "attention lock busy"
	}
	if len(sess.delegateAttentionArmIDs) != 0 {
		sess.attentionMu.Unlock()
		return "attention arm retry pending"
	}
	pending, busy, err := stableSupervisionPendingAttentionLocked(sess)
	sess.attentionMu.Unlock()
	if busy {
		return "session lock busy"
	}
	if err != nil || len(pending) != 0 {
		return fmt.Sprintf("pending attention %v (err %v)", pending, err)
	}
	controller, delegateID := sess.delegateController, sess.owningDelegateID
	if controller == nil || delegateID == "" {
		return ""
	}
	if !controller.mu.TryLock() {
		return "controller lock busy"
	}
	reserved := stableSupervisionReservedAttentionIDLocked(controller, sess)
	aggregate := controller.durable[delegateID]
	runOpen := aggregate != nil && aggregate.CurrentRunOpen
	// A finished generation whose finalize tail hasn't reported
	// quiescence still holds the delegate: it takes no start yet.
	live := controller.live[delegateID]
	finalizing := live != nil && live.finalizing != nil
	controller.mu.Unlock()
	if reserved != "" {
		return "attention reserved"
	}
	if runOpen || finalizing {
		return fmt.Sprintf("controller runOpen=%t finalizing=%t", runOpen, finalizing)
	}
	return ""
}

// stableSupervisionPendingAttentionLocked lists sess's durable pending attention
// ids, mirroring pendingDelegateAttentionIDs -> delegateAttentionFoldLocked. The
// caller holds sess.attentionMu, which guards the fold cursor
// (Session.attentionFoldCursor) that readDelegateAttentionFold reads. It keeps
// delegateAttentionFoldLocked's early return: no ready transcript, session id,
// or state dir reads as no pending attention. A busy sess.mu — whose fields the
// fold needs to locate the transcript — is reported busy so the caller refuses
// rather than reading a path it could not observe.
func stableSupervisionPendingAttentionLocked(sess *Session) ([]string, bool, error) {
	if !sess.mu.TryLock() {
		return nil, true, nil
	}
	ready := sess.transcriptReady
	sessionID := sess.id
	stateDir := sess.stateDir
	sess.mu.Unlock()
	if !ready || sessionID == "" || stateDir == "" {
		return nil, false, nil
	}
	fold, err := sess.readDelegateAttentionFold(transcriptPath(stateDir, sessionID), sessionID)
	if err != nil {
		return nil, false, err
	}
	return fold.pendingIDs(), false, nil
}

// stableSupervisionReservedAttentionIDLocked mirrors
// delegateTreeController.reservedAttentionID while c.mu is held: that accessor
// takes c.mu blocking, so the predicate cannot call it. It routes through
// runtimeOwnerLocked and keeps the same three guards — the record's delegate id
// matches, its trigger is TriggerAttention, and its runtime is this session — so
// a reservation of another kind for the same delegate does not read as reserved.
func stableSupervisionReservedAttentionIDLocked(c *delegateTreeController, runtime *Session) string {
	delegateID, _, err := c.runtimeOwnerLocked(runtime)
	if err != nil || delegateID == "" {
		return ""
	}
	for _, record := range c.reservations {
		if record != nil && record.delegateID == delegateID && record.trigger == delegatestore.TriggerAttention && record.runtime == runtime {
			return record.attentionID
		}
	}
	return ""
}

// stableSupervisionState renders the controller, root and child state a
// supervision quiescence wait reads, for its timeout report. The report runs
// after the wait's bound has already fired, so it must print whatever it can
// read without waiting: every source is taken with TryLock, a source that is
// busy renders its "<source>_lock_busy" marker in place of the fields behind it,
// and each source is taken once, released before the next. This is the same
// best-effort discipline stableSupervisionFailureSnapshot uses. A report that
// waits on the lock it reports on turns a did-not-quiesce failure into a hang,
// which is the opposite of what it exists for.
//
// The trailing childGates{...} line cannot call the live gate accessors —
// childStopGated, childFatalRunGated, childDrainAbandoned, childDrainGracePending
// and childCommittedSendStart — because the first three take c.mu through
// directDelegateForChildSession, and the fatal-run gate takes the subagent
// registry's mu through getSub and then sub.mu. Each gate is instead rendered
// from the TryLocked source that already printed its fields, and each predicate
// names the accessor it mirrors, so a change there shows up here as a drift.
func stableSupervisionState(root *Session, sub *subagent) string {
	// sub.sess is assigned once, at subagent creation (delegate_restore_idle.go's cold
	// restore and subagents.go's start), and never reassigned, so an unlocked read
	// cannot race a writer; stableSupervisionFailureSnapshot reads it the same way.
	sess := sub.sess
	if sess == nil || sess.delegateController == nil {
		return "child has no session or controller"
	}
	c := sess.delegateController
	owner := sess.owningDelegateID
	id := sess.id
	stop, drainAbandoned, drainGrace := "controller_lock_busy", "controller_lock_busy", "controller_lock_busy"
	var b strings.Builder
	if c.mu.TryLock() {
		if agg := c.durable[owner]; agg != nil {
			fmt.Fprintf(&b, "aggregate{gen=%d phase=%v open=%v needs=%v} ", agg.Generation, agg.Phase, agg.CurrentRunOpen, agg.NeedsAttention)
		}
		if live := c.live[owner]; live != nil {
			fmt.Fprintf(&b, "live{binding=%v finalizing=%v recovery=%v} ", live.binding != nil, live.finalizing != nil, live.recoveryRequired)
		}
		if st := c.attention[owner]; st != nil {
			fmt.Fprintf(&b, "attention{wake=%v parked=%v restoreFailures=%d turn=%d} ", st.wakeIDs, st.parked, st.restoreFailures, st.driveTurn)
		}
		fmt.Fprintf(&b, "reservations=%d eligible=%v delivery=%v ", len(c.reservations), c.delegateAttentionWakeEligibleLocked(owner), c.hasDeliveryWorkForOwnerLocked(owner))
		row, ok := stableSupervisionDirectRowLocked(c, root, id)
		c.mu.Unlock()
		// childStopGated: ok && delegateRowStopGated(row).
		stop = strconv.FormatBool(ok && delegateRowStopGated(row))
		if root.drainAbandonedMu.TryLock() {
			abandoned, abandonedOK := root.drainAbandonedChildren[id]
			grace, graceOK := root.drainGraceChildren[id]
			root.drainAbandonedMu.Unlock()
			// childDrainAbandoned: exists && abandoned.generation == row.generation.
			drainAbandoned = strconv.FormatBool(ok && abandonedOK && abandoned.generation == row.generation)
			// childDrainGracePending: row.currentRunOpen && drainGraceChildForRow(id, row) matched.
			drainGrace = strconv.FormatBool(ok && row.currentRunOpen && graceOK && grace.delegateID == row.id && grace.generation == row.generation)
		} else {
			drainAbandoned, drainGrace = "drain_lock_busy", "drain_lock_busy"
		}
	} else {
		b.WriteString("controller_lock_busy ")
	}
	if root.attentionMu.TryLock() {
		fmt.Fprintf(&b, "rootRetry{active=%v delay=%v} ", root.stableAttentionRetry.active, root.stableAttentionRetry.delay)
		root.attentionMu.Unlock()
	} else {
		b.WriteString("attention_lock_busy ")
	}
	fatalRun := "subagent_lock_busy"
	if sub.mu.TryLock() {
		fmt.Fprintf(&b, "sub{running=%v driving=%v finalizing=%v dispose=%v closed=%v fatal=%v status=%v} ", sub.running, sub.driving, sub.finalizing, sub.disposeGated, sub.closed, sub.fatalRunGated, sub.status)
		// childFatalRunGated: getSub(id).fatalRunGated. The report reads the passed
		// sub, so it never takes the subagent registry's mu.
		fatalRun = strconv.FormatBool(sub.fatalRunGated)
		sub.mu.Unlock()
	} else {
		b.WriteString("subagent_lock_busy ")
	}
	if root.mu.TryLock() {
		fmt.Fprintf(&b, "root{state=%v} ", root.state)
		root.mu.Unlock()
	} else {
		b.WriteString("root_lock_busy ")
	}
	if sess.mu.TryLock() {
		fmt.Fprintf(&b, "child{state=%v} ", sess.state)
		sess.mu.Unlock()
	} else {
		b.WriteString("session_lock_busy ")
	}
	committedSend := "send_start_lock_busy"
	if root.childCommittedSendMu.TryLock() {
		// childCommittedSendStart: childCommittedSendChildren[id] present.
		_, held := root.childCommittedSendChildren[id]
		root.childCommittedSendMu.Unlock()
		committedSend = strconv.FormatBool(held)
	}
	fmt.Fprintf(&b, "childGates{stop=%s fatalRun=%s drainAbandoned=%s drainGrace=%s committedSend=%s}", stop, fatalRun, drainAbandoned, drainGrace, committedSend)
	return b.String()
}

// stableSupervisionDirectRowLocked captures the direct stable delegate row for
// childSessionID while c.mu is held. It mirrors directDelegateForChildSession
// — snapshotsForChildSession's filter over c.durable
// (delegate_tree_controller.go) plus delegateRowVisibleTo's pick — which the
// report cannot call because that accessor would re-take c.mu.
func stableSupervisionDirectRowLocked(c *delegateTreeController, root *Session, childSessionID string) (delegateSnapshot, bool) {
	ids := make([]string, 0)
	for id, agg := range c.durable {
		if agg != nil && agg.Descriptor.ChildSessionID == childSessionID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, row := range c.captureDelegateSnapshotsLocked(ids) {
		if delegateRowVisibleTo(root, row.parentID, row.descriptor.OwnerSessionID, row.descriptor.VisibleSessionID) {
			return row, true
		}
	}
	return delegateSnapshot{}, false
}

// TestWaitForStableSupervisionRunOutlastsDeferredAttentionDrive pins the
// contract of waitForStableSupervisionRun: it must not return while an armed
// delegate attention still owes a run. Arming attention only notifies, and
// driveDelegateAttention refuses the resulting drive whenever the child
// is not drivable yet (mid-run, mid-finalization, or dispose gated) or the
// controller is still busy with the previous generation. The dispose gate
// stands in for those refusals here because it is the one a test can hold open
// deterministically. Without the wait the helper joins the WARM run's already
// closed channel and the caller reads that generation's reported delivery
// instead of the attention generation's private no-action finish.
func TestWaitForStableSupervisionRunOutlastsDeferredAttentionDrive(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	if !sub.trySetDisposeGate() {
		t.Fatal("could not gate the quiescent stable child")
	}
	armStableSupervisionAttention(t, sub, "attention:deferred", "inspect before the drive is admitted")
	// Release the refused drive only after the helper has had time to observe
	// the idle child, which is the state the stale-channel join returned on.
	go func() {
		time.Sleep(50 * time.Millisecond)
		sub.clearDisposeGate()
		sub.sess.notify()
	}()

	waitForStableSupervisionRun(t, root, fixture.childID)

	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 2 || finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("deferred attention finish = %#v, want the attention generation's private no-action", finished)
	}
}

// TestSupervisionRootDrivesAttentionTheChildDropped pins the harness root's
// wake consumer (#3592; the mechanism is on serveSupervisionRootWakes). The
// child drops an attention drive refused on a send's drive guard, and a send
// that gives the guard back does not re-drive the child (releaseDriveGuard).
// The root's retry is the wake this test exercises.
func TestSupervisionRootDrivesAttentionTheChildDropped(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	if !root.takeSendDriveGuard(sub) {
		t.Fatal("could not take the quiescent stable child's drive guard")
	}
	armStableSupervisionAttention(t, sub, "attention:dropped", "inspect after the dropped drive")
	root.delegateController.retryDelegateAttentionLater()
	releaseDriveGuard(sub)

	waitForStableSupervisionRun(t, root, fixture.childID)

	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 2 || finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("dropped attention finish = %#v, want the attention generation's private no-action", finished)
	}
}

// pauseStableTailBeforeRelease starts the stable delegate's next generation
// and parks that generation's finalize tail just before it announces and
// releases the generation, so attention armed meanwhile is refused as busy.
// It returns the tail's completion channel, closed after the tail re-arms
// the remaining attention, and the func that lets the tail go on.
func pauseStableTailBeforeRelease(t *testing.T, root *Session, sub *subagent, fixture coldDelegateFixture) (<-chan struct{}, func()) {
	t.Helper()
	paused, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	resumeTail := func() { resumeOnce.Do(func() { close(resume) }) }
	t.Cleanup(resumeTail)
	var tailDone chan struct{}
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentBeforeGenerationAnnounced = func(a *subagent) {
			if tailDone != nil {
				return
			}
			a.mu.Lock()
			tailDone = a.done
			a.mu.Unlock()
			close(paused)
			<-resume
		}
	})
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "second generation", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("second generation send = %#v", outcome.result)
	}
	select {
	case <-paused:
	case <-time.After(30 * time.Second): // TRIPWIRE: the scripted run finishes in milliseconds.
		t.Fatal("the second generation's finalize tail never reached its announcement")
	}
	return tailDone, resumeTail
}

// TestDelegateResourceSupervision_AttentionRefusedOnAHeldGuardIsRedriven
// pins #3723. Attention armed while the previous generation is still
// finalizing takes the child's drive guard and is refused busy; the finalize
// tail's re-arm drive then lands while that guard is still held and is
// refused on it. The guard holder must drive the child again once it lets
// go. Nothing wakes this root, so only that re-drive can deliver.
func TestDelegateResourceSupervision_AttentionRefusedOnAHeldGuardIsRedriven(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("second result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	tailDone, resumeTail := pauseStableTailBeforeRelease(t, root, sub, fixture)

	var starts, held atomic.Int32
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionStartCommitted = func(*subagent) { starts.Add(1) }
		cfg.delegateAttentionBeforeGuardRelease = func(*subagent) {
			if held.Add(1) != 1 {
				return
			}
			// Hold the busy drive's guard while the tail releases the
			// generation and its re-arm drive is refused on that guard.
			resumeTail()
			<-tailDone
		}
	})
	armStableSupervisionAttention(t, sub, "attention:held-guard", "inspect after the held guard")

	// The re-drive runs on the holder's exit, so it has started the
	// attention generation by the time the arm's drive returns.
	if got := starts.Load(); got != 1 {
		t.Fatalf("attention generations started after the guard holder let go = %d, want 1", got)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 3 || finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("re-driven attention finish = %#v, want generation 3's private no-action", finished)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("attention generations started = %d, want exactly 1", got)
	}
}

// TestDelegateResourceSupervision_AttentionRedriveThatLosesStops pins that
// the #3723 re-drive cannot loop: a re-drive that is itself refused busy
// re-drives again only if another drive was refused on its own guard. Here
// nothing is, so it stops after one try, and the finalize tail's re-arm
// delivers the attention once, with no second generation.
func TestDelegateResourceSupervision_AttentionRedriveThatLosesStops(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("second result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	_, resumeTail := pauseStableTailBeforeRelease(t, root, sub, fixture)

	var starts, releases atomic.Int32
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionStartCommitted = func(*subagent) { starts.Add(1) }
		cfg.delegateAttentionBeforeGuardRelease = func(held *subagent) {
			n := releases.Add(1)
			if n > 2 {
				// A looping re-drive would recurse until the stack overflows.
				t.Fatalf("attention drive %d let the guard go: the re-drive is looping", n)
			}
			if n != 1 {
				return
			}
			// A second drive refused on the busy drive's guard owes the
			// child one re-drive. The tail is still parked, so that
			// re-drive is refused busy too.
			root.driveDelegateAttention(held)
		}
	})
	armStableSupervisionAttention(t, sub, "attention:losing-redrive", "inspect after the losing re-drive")

	if got := releases.Load(); got != 2 {
		t.Fatalf("attention drives that let the guard go without a run = %d, want the busy drive and its one re-drive", got)
	}
	if got := starts.Load(); got != 0 {
		t.Fatalf("attention generations started before the release = %d, want 0", got)
	}
	resumeTail()
	waitForStableSupervisionRun(t, root, fixture.childID)
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 3 || finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("re-armed attention finish = %#v, want generation 3's private no-action", finished)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("attention generations started = %d, want exactly 1", got)
	}
}

// TestDelegateResourceSupervision_AttentionRefusedOnANotificationTurnIsRedriven
// pins #3723 for the other drive that holds the guard on an idle child: a
// notification turn. Attention armed while that turn runs is refused on its
// guard, and the turn drives the child again when it ends. Nothing wakes this
// root, so only that re-drive can deliver.
func TestDelegateResourceSupervision_AttentionRefusedOnANotificationTurnIsRedriven(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	inTurn, endTurn := make(chan struct{}), make(chan struct{})
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			close(inTurn)
			<-endTurn
			return llm.Response{Message: llm.Assistant("noted the finished job")}
		},
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var starts atomic.Int32
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionStartCommitted = func(*subagent) { starts.Add(1) }
	})
	enqueueCompletedJobNotification(t, sub.sess, "job:finished")
	if !root.driveSubagentNotificationTurn(sub) {
		t.Fatal("the child's notification turn did not launch")
	}
	select {
	case <-inTurn:
	case <-time.After(30 * time.Second): // TRIPWIRE: the turn reaches the scripted provider in milliseconds.
		t.Fatal("the child's notification turn never reached the provider")
	}
	armStableSupervisionAttention(t, sub, "attention:notification-turn", "inspect after the notification turn")
	if got := starts.Load(); got != 0 {
		t.Fatalf("attention generations started during the notification turn = %d, want 0", got)
	}
	close(endTurn)

	// The re-drive starts the generation as the turn ends; without it nothing
	// does.
	testwait.Until(t, "the attention refused on the notification turn's guard to start", func() bool {
		return starts.Load() == 1
	})
	waitForStableSupervisionRun(t, root, fixture.childID)
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 2 || finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("re-driven attention finish = %#v, want generation 2's private no-action", finished)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("attention generations started = %d, want exactly 1", got)
	}
}

// TestDelegateAttentionDriveClearsResolvedProjection pins that a
// delegate whose attention already resolved in its transcript does not stay
// flagged. A resolution can outrun the committed attention run that would emit
// the clearing event -- acceptDelegateAttention makes the marker durable, and a
// later-declined CommitStart leaves the last journal event at
// needs_attention:true with nothing pending -- and the transcript-driven retry
// then finds no pending attention and never starts the run that would clear it.
func TestDelegateAttentionDriveClearsResolvedProjection(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	controller := root.delegateController
	const attentionID = "delegate:resolved-before-the-run"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "already handled"); err != nil || !appended {
		t.Fatalf("append stable attention = appended:%t err:%v", appended, err)
	}
	if _, err := controller.openDelegateAttention(fixture.delegateID, attentionID); err != nil {
		t.Fatalf("open stable attention: %v", err)
	}
	controller.mu.Lock()
	armedFlag := controller.durable[fixture.delegateID].NeedsAttention
	wakeIDs := len(attentionWakeIDsOf(controller, fixture.delegateID))
	controller.mu.Unlock()
	if !armedFlag || wakeIDs != 1 {
		t.Fatalf("armed attention projection = needs:%t wakeIDs:%d, want true/1", armedFlag, wakeIDs)
	}

	// Unresolved transcript attention is never cleared: the fold still lists a
	// pending ID, so the projection and its wake must survive untouched.
	if err := controller.clearResolvedDelegateAttention(fixture.delegateID); err != nil {
		t.Fatalf("clear with pending attention: %v", err)
	}
	controller.mu.Lock()
	keptFlag := controller.durable[fixture.delegateID].NeedsAttention
	keptWakeIDs := len(attentionWakeIDsOf(controller, fixture.delegateID))
	controller.mu.Unlock()
	if !keptFlag || keptWakeIDs != 1 {
		t.Fatalf("pending attention projection = needs:%t wakeIDs:%d, want true/1", keptFlag, keptWakeIDs)
	}

	// Model the accepted resolution whose committed run never landed: the
	// marker is durable in the child transcript, so the drive finds nothing
	// pending while the aggregate still carries the true event.
	if err := sub.sess.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("resolve stable attention: %v", err)
	}

	// An attention opened between the transcript fold and the locked clearing
	// update must survive: the update revalidates the evidence version, so a
	// stale empty fold never drops the concurrently opened wake.
	const racedID = "delegate:opened-during-the-fold"
	compute := readExistingDelegateAttentionFoldCompute
	readExistingDelegateAttentionFoldCompute = func(path, sessionID string) (delegateAttentionFold, int64, error) {
		fold, consumed, err := compute(path, sessionID)
		if _, openErr := controller.openDelegateAttention(fixture.delegateID, racedID); openErr != nil {
			t.Errorf("open raced attention: %v", openErr)
		}
		return fold, consumed, err
	}
	t.Cleanup(func() { readExistingDelegateAttentionFoldCompute = compute })
	if err := controller.clearResolvedDelegateAttention(fixture.delegateID); err != nil {
		t.Fatalf("clear with a racing open: %v", err)
	}
	readExistingDelegateAttentionFoldCompute = compute
	controller.mu.Lock()
	racedFlag := controller.durable[fixture.delegateID].NeedsAttention
	_, racedKept := attentionWakeIDsOf(controller, fixture.delegateID)[racedID]
	controller.mu.Unlock()
	if !racedFlag || !racedKept {
		t.Fatalf("racing open projection = needs:%t keptRacedWake:%t, want true/true", racedFlag, racedKept)
	}

	root.driveDelegateAttention(sub)

	controller.mu.Lock()
	cleared := !controller.durable[fixture.delegateID].NeedsAttention
	remaining := len(attentionWakeIDsOf(controller, fixture.delegateID))
	controller.mu.Unlock()
	if !cleared || remaining != 0 {
		t.Fatalf("resolved attention projection = needs:%t wakeIDs:%d, want false/0", !cleared, remaining)
	}
	events, err := controller.store.Load()
	if err != nil {
		t.Fatalf("load delegate journal: %v", err)
	}
	tail := events[len(events)-1]
	if tail.Kind != delegatestore.EventDelegateAttentionChanged || tail.DelegateID != fixture.delegateID || tail.AttentionChanged == nil || tail.AttentionChanged.NeedsAttention {
		t.Fatalf("cleared projection tail = %#v", tail)
	}
}

// TestClearResolvedDelegateAttentionDefersWhenTranscriptMissing pins that a
// missing child transcript is not read as an empty one: the projection and its
// wake stay put for retry instead of the attention being silently discarded.
func TestClearResolvedDelegateAttentionDefersWhenTranscriptMissing(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	controller := root.delegateController
	const attentionID = "delegate:transcript-missing"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "still pending"); err != nil || !appended {
		t.Fatalf("append stable attention = appended:%t err:%v", appended, err)
	}
	if _, err := controller.openDelegateAttention(fixture.delegateID, attentionID); err != nil {
		t.Fatalf("open stable attention: %v", err)
	}
	if err := os.Remove(sub.sess.TranscriptPath()); err != nil {
		t.Fatalf("remove child transcript: %v", err)
	}
	if err := controller.clearResolvedDelegateAttention(fixture.delegateID); err == nil {
		t.Fatal("clear with a missing transcript returned no error")
	}
	controller.mu.Lock()
	keptFlag := controller.durable[fixture.delegateID].NeedsAttention
	keptWakeIDs := len(attentionWakeIDsOf(controller, fixture.delegateID))
	controller.mu.Unlock()
	if !keptFlag || keptWakeIDs != 1 {
		t.Fatalf("missing-transcript projection = needs:%t wakeIDs:%d, want true/1", keptFlag, keptWakeIDs)
	}
}

// resolvedDelegateAttention warms the stable supervision delegate, then
// appends and opens one attention item on it and resolves that item with
// disposition, leaving its wake noted. It returns the root's controller and
// the delegate's ID.
func resolvedDelegateAttention(t *testing.T, attentionID string, disposition delegateAttentionResolution) (*delegateTreeController, string) {
	t.Helper()
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	controller := root.delegateController
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "already handled"); err != nil || !appended {
		t.Fatalf("append stable attention = appended:%t err:%v", appended, err)
	}
	if _, err := controller.openDelegateAttention(fixture.delegateID, attentionID); err != nil {
		t.Fatalf("open stable attention: %v", err)
	}
	if err := sub.sess.resolveAttentionDurably([]string{attentionID}, disposition); err != nil {
		t.Fatalf("resolve stable attention: %v", err)
	}
	return controller, fixture.delegateID
}

// TestClearResolvedDelegateAttentionDropsWakesAfterTheFlagWasLowered pins that
// the clear reconciles a resolved wake even when the journal fold already
// lowered NeedsAttention (a closing delegate does): a wake left behind would
// block retirement of the whole tree with nothing pending in the transcript.
func TestClearResolvedDelegateAttentionDropsWakesAfterTheFlagWasLowered(t *testing.T) {
	controller, delegateID := resolvedDelegateAttention(t, "delegate:resolved-after-the-flag-lowered", delegateAttentionConsumed)
	controller.mu.Lock()
	_, err := controller.appendLocked(delegatestore.Event{
		Kind:             delegatestore.EventDelegateAttentionChanged,
		DelegateID:       delegateID,
		AttentionChanged: &delegatestore.DelegateAttentionChanged{NeedsAttention: false},
	})
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("lower the flag: %v", err)
	}

	if err := controller.clearResolvedDelegateAttention(delegateID); err != nil {
		t.Fatalf("clear after the flag was lowered: %v", err)
	}
	controller.mu.Lock()
	remaining := len(attentionWakeIDsOf(controller, delegateID))
	controller.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("wakes after the clear = %d, want 0", remaining)
	}
}

// TestClearResolvedDelegateAttentionLeavesWakesToAPendingStop pins that the
// clear does not forget a wake while a stop is pending: the stop's cleanup owns
// it and renews the durability of its resolution marker before forgetting it.
func TestClearResolvedDelegateAttentionLeavesWakesToAPendingStop(t *testing.T) {
	const attentionID = "delegate:resolved-during-a-stop"
	controller, delegateID := resolvedDelegateAttention(t, attentionID, delegateAttentionDiscarded)
	controller.mu.Lock()
	controller.durable[delegateID].PendingStopSeq = 1
	controller.mu.Unlock()

	if err := controller.clearResolvedDelegateAttention(delegateID); err != nil {
		t.Fatalf("clear during a pending stop: %v", err)
	}
	controller.mu.Lock()
	_, kept := attentionWakeIDsOf(controller, delegateID)[attentionID]
	controller.mu.Unlock()
	if !kept {
		t.Fatal("the clear forgot a wake the pending stop's cleanup owns")
	}
}

// warmStableSupervisionDelegate starts the warm run and acknowledges its result.
// It does NOT wait for that run to quiesce: the run's finalizer outlives the
// acknowledged result, because `running` goes false at the top of the finalize
// block and `finalizing` clears only at the end, and a send landing in that
// window is refused as target busy by design. A caller that needs a drivable
// child must wait for quiescence first; the callers that arm attention
// deliberately do not, so that the drive they exercise is the deferred one.
// A deferred drive can end on the root's retry, so the root serves its wakes
// as a served root does (serveSupervisionRootWakes).
func warmStableSupervisionDelegate(t *testing.T, root *Session, fixture coldDelegateFixture) *subagent {
	t.Helper()
	serveSupervisionRootWakes(t, root)
	return warmDelegateUnservedRoot(t, root, fixture)
}

// warmDelegateUnservedRoot is warmStableSupervisionDelegate without the
// root's wake consumer, for a test that must show the child delivers its own
// attention: nothing on the root drives it.
func warmDelegateUnservedRoot(t *testing.T, root *Session, fixture coldDelegateFixture) *subagent {
	t.Helper()
	sendAndAcknowledge(t, root, fixture.delegateID, "warm retained runtime")
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("warm stable delegate retained no child session %q", fixture.childID)
	}
	return sub
}

// sendAndAcknowledge sends message to the delegate as its parent and
// acknowledges the result, as the parent's delegate_send does.
func sendAndAcknowledge(t *testing.T, root *Session, delegateID, message string) {
	t.Helper()
	outcome := (delegateRuntime{owner: root}).send(context.Background(), delegateID, message, 60_000)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("send %q = %#v", message, outcome)
	}
	plans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge %q: %v", message, err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute %q acknowledgement: %v", message, err)
	}
}

func armStableSupervisionAttention(t *testing.T, sub *subagent, attentionID, content string) {
	t.Helper()
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, content); err != nil || !appended {
		t.Fatalf("append stable attention = appended:%t err:%v", appended, err)
	}
	if err := sub.sess.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm stable attention: %v", err)
	}
}

func captureStableCompletionSnapshot(ch chan<- delegateCompletionSnapshot) func(*subagent) {
	return func(sub *subagent) {
		controller := sub.sess.delegateController
		controller.mu.Lock()
		live := controller.live[sub.sess.owningDelegateID]
		var lease delegateLease
		if live != nil && live.binding != nil {
			lease = live.binding.lease
		}
		controller.mu.Unlock()
		if lease == (delegateLease{}) {
			return
		}
		snapshot, err := controller.completionSnapshot(lease)
		if err != nil {
			return
		}
		select {
		case ch <- snapshot:
		default:
		}
	}
}

func stableSupervisionStopHook(output string) *hooks.Runner {
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "printf '%s' " + shellquote.Literal(output),
		Timeout: 5,
	})
	return runner
}

func assertSingleRecoveryNudge(t *testing.T, adapter *fakeAdapter) {
	t.Helper()
	requests := adapter.Requests()
	if len(requests) == 0 {
		t.Fatal("provider received no requests")
	}
	last := requests[len(requests)-1]
	// The reminder text depends on whether the delegate already sent a
	// non-terminal update; either way exactly one reminder must land.
	got := countMessageText(last.Messages, wrapHookContext(communicateNudge("communicate"))) +
		countMessageText(last.Messages, wrapHookContext(communicateUpdateNudge("communicate")))
	if got != 1 {
		t.Fatalf("recovery nudge count in final request = %d, want exactly one: %#v", got, last.Messages)
	}
}

func supervisionRequestCount(adapter *fakeAdapter) int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return len(adapter.requests)
}

func containsDelegateSalvageWarning(warnings []string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, "partial draft salvaged") && strings.Contains(warning, "delegate_send") {
			return true
		}
	}
	return false
}

func packetWarnings(packet *delegatestore.TerminalPacket) []string {
	if packet == nil {
		return nil
	}
	return packet.Warnings
}

func newStableQuietSupervisionHarness(t *testing.T) (*Session, *delegateTreeController, delegateLease, *agenttest.FakeClock) {
	t.Helper()
	clock := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	root := newDelegateAttentionTestSession(t)
	root.clock = clock
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	controller.rootRuntime = root
	root.delegateController = controller
	seedDelegateControllerRunning(t, controller, "dlg_target", "")
	child := &Session{clock: clock, delegateController: controller}
	controller.live["dlg_target"].runtime = child
	controller.live["dlg_target"].binding.runtime = child
	controller.live["dlg_target"].activityAt = clock.Now()
	return root, controller, delegateLease{delegateID: "dlg_target", generation: 1}, clock
}

func pendingQuietAttention(t *testing.T, root *Session) []string {
	t.Helper()
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.id), root.id)
	if err != nil {
		t.Fatalf("read quiet attention: %v", err)
	}
	return append([]string(nil), fold.order...)
}

// The child's initial-prompt session namer runs detached and can outlive the
// run, taking the session lock to name and save the session. Quiescence
// waits for it, so a test reading the child's meta, name or lock-guarded
// state right after the wait doesn't race it (#4009).
func TestStableSupervisionQuiescenceWaitsForTheChildNamer(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	released, releaseNamer := context.WithCancel(context.Background())
	fixture.client.Register(&agenttest.ScriptedAdapter{Provider: testSessionNamerProvider, Responder: func(request llm.Request) llm.Response {
		<-released.Done()
		return llm.Response{Provider: testSessionNamerProvider, Model: request.Model, Message: llm.Assistant(`{"name":"Slow Name"}`)}
	}})
	root := restoreSupervisionRoot(t, fixture, nil)
	// Registered after the root's Close cleanup so it runs first: a failure
	// before the namer is released must not leave it blocked for Close to
	// wait out.
	t.Cleanup(releaseNamer)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	// With every earlier stage quiet, the blocked namer alone must still hold
	// the child; only then is it released.
	const namerPending = "1 session namer runs pending"
	testwait.Until(t, "the blocked namer to be the only refusal left", func() bool {
		return stableSupervisionRefusal(sub) == namerPending
	})
	releaseNamer()
	waitForStableSupervisionRun(t, root, fixture.childID)
	sub.sess.mu.Lock()
	pending, name := sub.sess.naming.pending, sub.sess.naming.value
	sub.sess.mu.Unlock()
	if pending != 0 || name != "Slow Name" {
		t.Fatalf("quiescence returned with %d child namer runs pending and name %q, want none pending and the slow namer's name", pending, name)
	}
}

// TestDelegateResourceSupervision_WatchFilteredFoldDoesNotDuplicateHistory pins
// the HIGH the branch review found in the projection drop itself (LIFE-01,
// #2340).
//
// Withholding a pending watch row from the fold's input shortens the slice the
// summarizer sees. That shortened length is NOT the publication merge boundary:
// publishFoldedHistory carries concurrent turns by appending
// s.history[snapLen:] onto the folded result, so shrinking snapLen to the
// filtered length makes every turn between the two look like a turn that
// arrived during the fold, and the fold re-appends history it already folded.
// The gate would then duplicate ordinary conversation in exchange for
// withholding one notice.
//
// The shape that shows it: a deferred row in the MIDDLE of history, with turns
// after it, so the removal is not simply a truncation of the tail.
//
// Honest note on the red proof: with preManageLen shortened to the filtered
// length this test DOES fail, but it fails earlier, at the request's own
// budget admission -- the re-appended turns inflate the estimate before the
// duplicate-turn assertion is reached. The assertion below is the precise
// statement of the defect; the red proof is the prepare error above it.
func TestDelegateResourceSupervision_WatchFilteredFoldDoesNotDuplicateHistory(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	// Small on purpose: the duplication this pins must be caught by the
	// duplicate-turn assertion below, not by the request later exceeding the
	// token budget, which would be a different failure wearing this test's name.
	seedSessionHistory(t, sub.sess, 4)

	deferred := jobNotification{
		Kind:   jobNotificationKindWatch,
		JobID:  "job:fold-duplicate",
		Status: jobNotificationEventWatch,
		Reason: "watch send failed",
	}
	attentionID := deferredWatchAttentionID(deferred)
	notice := sub.sess.formatJobNotificationReminder([]deliverableJobNotification{{notification: deferred}})
	if _, err := sub.sess.appendDelegateNotificationDurably(attentionID, notice); err != nil {
		t.Fatalf("append the deferred watch notice: %v", err)
	}
	// Ordinary turns AFTER the deferred row, so the filter's removal is interior.
	sub.sess.mu.Lock()
	for i := range 2 {
		turn := schema.NewTurn(schema.TurnSteering, llm.User(fmt.Sprintf("after the deferred watch %d", i)))
		sub.sess.history = append(sub.sess.history, turn)
	}
	before := len(sub.sess.history)
	sub.sess.mu.Unlock()

	forcePressureAbove(t, sub.sess, sub.sess.contextMgr.CheckpointThreshold)
	sub.sess.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "note", nil }
	if _, _, _, _, _, _, err := sub.sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings)); err != nil {
		t.Fatalf("unleased model request on the stable child = %v, want it to carry its ordinary work", err)
	}
	sub.sess.mu.Lock()
	defer sub.sess.mu.Unlock()
	// A fold summarizes, so the history may SHRINK. It must never GROW by
	// re-appending turns that were in the snapshot it folded.
	if len(sub.sess.history) > before {
		t.Fatalf("history after the filtered fold = %d turns, want <= %d: the fold re-appended turns it already folded", len(sub.sess.history), before)
	}
	// The sharpest statement of the same fact: the deferred row is what was
	// withheld, and the turns after it are still each present exactly once.
	after := make(map[string]int, len(sub.sess.history))
	for _, turn := range sub.sess.history {
		if turn.StableTurnID != "" {
			after[turn.StableTurnID]++
		}
	}
	for id, count := range after {
		if count > 1 {
			t.Fatalf("durable fold published turn %q %d times, want it exactly once", id, count)
		}
	}
}

// TestDelegateResourceSupervision_UnleasedWatchFilterReadsTheFoldUnderTheAttentionLock
// pins the lock the unleased watch filter's fold read must hold.
//
// The fold reader resumes from a cursor the session keeps resident
// (delegateAttentionFoldCursor), and every writer that moves that cursor — a
// watch delivery, a resolution, a parent wake read — holds attentionMu. A read
// taken outside that lock is an unsynchronized pair with all of them.
//
// The rule is stated deterministically through the fold-read seam: a read that
// finds attentionMu takeable is a read running without it. The seam wraps every
// read this path makes, so no ordering trick is needed to prove the hold.
func TestDelegateResourceSupervision_UnleasedWatchFilterReadsTheFoldUnderTheAttentionLock(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Phase one: the read must run under attentionMu.
	var reads, unlocked atomic.Int64
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			reads.Add(1)
			if sub.sess.attentionMu.TryLock() {
				sub.sess.attentionMu.Unlock()
				unlocked.Add(1)
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	sub.sess.pendingDeferredWatchAttention()
	if reads.Load() == 0 {
		t.Fatal("the pending-watch read never reached the fold: this proof needs the read it guards")
	}
	if unlocked.Load() != 0 {
		t.Fatalf("%d fold read(s) ran with attentionMu takeable, want every read under the lock", unlocked.Load())
	}
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) { cfg.delegateAttentionReadFold = nil })
}

// TestDelegateResourceSupervision_UnleasedProjectionWithholdsBothWatchRowProducers
// pins that the unleased projection recognizes a watch row by EITHER identity
// its producers write: the send rail's watch: row and the deferral's
// watchdefer: row. A filter that knows only one of them is blind to a live
// notice arriving through the other.
//
// Identity, not position or content, is the test: the rows are constructed with
// the two identities and an ordinary turn, and the settled delegate row beside
// them is the control a coarse "drop every attention turn" filter fails.
func TestDelegateResourceSupervision_UnleasedProjectionWithholdsBothWatchRowProducers(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.sess.stableChildTurnNeedsLease(context.Background()) {
		t.Fatal("the child is not the unleased stable-turn case this test needs")
	}

	const (
		sendRowID    = "watch:send-rail-row:1"
		deferRowID   = "watchdefer:deferred-row:1"
		settledRowID = "delegate:settled-row"
		ordinaryText = "ordinary resident turn"
	)
	for _, id := range []string{sendRowID, deferRowID, settledRowID} {
		if appended, err := sub.sess.appendDelegateNotificationDurably(id, "row "+id); err != nil || !appended {
			t.Fatalf("append attention row %q = appended:%t err:%v", id, appended, err)
		}
	}
	if err := sub.sess.resolveAttentionDurably([]string{settledRowID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("resolve the settled control row: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	pending := fold.pendingIDs()
	if !slices.Contains(pending, sendRowID) || !slices.Contains(pending, deferRowID) {
		t.Fatalf("pending attention rows = %v, want both watch producers' rows pending", pending)
	}
	if slices.Contains(pending, settledRowID) {
		t.Fatalf("pending attention rows = %v, want the control row settled", pending)
	}

	kept, droppedAt := sub.sess.dropUnleasedWatchAttentionTurns(context.Background(), []schema.Turn{
		{Kind: schema.TurnSteering, AttentionID: sendRowID, Message: llm.User("row " + sendRowID)},
		{Kind: schema.TurnSteering, AttentionID: deferRowID, Message: llm.User("row " + deferRowID)},
		{Kind: schema.TurnSteering, AttentionID: settledRowID, Message: llm.User("row " + settledRowID)},
		{Kind: schema.TurnUserInput, Message: llm.User(ordinaryText)},
	})
	if !slices.Equal(droppedAt, []int{0, 1}) {
		t.Fatalf("dropped positions = %v, want both watch rows and nothing else", droppedAt)
	}
	if len(kept) != 2 || kept[0].AttentionID != settledRowID || kept[1].Message.Text() != ordinaryText {
		t.Fatalf("kept turns = %#v, want the settled row and the ordinary turn", kept)
	}
}

// TestDelegateResourceSupervision_SkillRebuildWithholdsAPendingWatchRow pins the
// skill-rebuild path: a pending watch row that lands after a request's snapshot
// must not ride into the rebuilt request through the appended tail or through
// the planning copy of the resident history.
//
// The rebuild is provoked the way the delivery path provokes it: a pending skill
// obligation whose recorded identity cannot be resolved appends a typed
// notification turn, and the same dispatch rebuilds so the notification reaches
// the model. The control row appended beside the watch row proves the rebuild
// really carried the appended tail; if the rebuild never ran, the control is
// absent too and this test fails on its own vacuity check rather than passing.
func TestDelegateResourceSupervision_SkillRebuildWithholdsAPendingWatchRow(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// An obligation with no recorded identity cannot be re-prepared, so the
	// delivery path records a typed notification and sets appendedNotifications.
	sub.sess.mu.Lock()
	sub.sess.skillLifecycle.Obligations = append(sub.sess.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
		InvocationID: "inv-rebuild-withhold",
		Route:        "model_tool",
	})
	sub.sess.skillLifecycle.Revision++
	sub.sess.mu.Unlock()

	// Both rows land after this request's snapshot and are appended as-is by the
	// rebuild. The delegate row is the control: it is pending durable attention
	// too, and only a watch row may be withheld.
	const (
		controlMarker = "control row carried by the rebuild"
		watchMarker   = "watch row the rebuild must not carry"
	)
	var landOnce sync.Once
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.afterModelHistorySnapshot = func() {
			landOnce.Do(func() {
				if _, err := sub.sess.appendDelegateNotificationDurably("delegate:rebuild-control", controlMarker); err != nil {
					t.Errorf("append the rebuild control row: %v", err)
				}
				if _, err := sub.sess.appendDelegateNotificationDurably("watch:rebuild-row:1", watchMarker); err != nil {
					t.Errorf("append the watch row: %v", err)
				}
			})
		}
	})

	_, _, history, _, _, _, err := sub.sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("unleased model request on the stable child = %v", err)
	}
	carried := make(map[string]bool, len(history))
	for _, msg := range history {
		text := msg.Text()
		carried[controlMarker] = carried[controlMarker] || strings.Contains(text, controlMarker)
		carried[watchMarker] = carried[watchMarker] || strings.Contains(text, watchMarker)
	}
	if !carried[controlMarker] {
		t.Fatal("the rebuilt request does not carry the appended control row: the rebuild never ran, so this proof is vacuous")
	}
	if carried[watchMarker] {
		t.Fatal("the rebuilt request carries the pending watch row: the appended tail bypassed the unleased projection")
	}
	// And the row is still owed: withholding is not resolution.
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	if !slices.Contains(fold.pendingIDs(), "watch:rebuild-row:1") {
		t.Fatalf("pending attention rows = %v, want the withheld watch row still pending", fold.pendingIDs())
	}
}

// TestDelegateResourceSupervision_AfterActionSeesTheUnleasedProjection pins that
// a tool round's AfterAction snapshot obeys the same withholding rule as the
// request that follows it: a still-pending watch row must not reach the context
// strategy either.
//
// The settled row is the control: it is attention the child already presented,
// so it is conversation and stays in the snapshot.
func TestDelegateResourceSupervision_AfterActionSeesTheUnleasedProjection(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.sess.stableChildTurnNeedsLease(context.Background()) {
		t.Fatal("the child is not the unleased stable-turn case this test needs")
	}

	const (
		watchID       = "watch:after-action-row:1"
		settledID     = "delegate:after-action-settled"
		watchMarker   = "watch row handed to the strategy"
		settledMarker = "settled row handed to the strategy"
	)
	for id, marker := range map[string]string{watchID: watchMarker, settledID: settledMarker} {
		if appended, err := sub.sess.appendDelegateNotificationDurably(id, marker); err != nil || !appended {
			t.Fatalf("append attention row %q = appended:%t err:%v", id, appended, err)
		}
	}
	if err := sub.sess.resolveAttentionDurably([]string{settledID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("resolve the settled control row: %v", err)
	}

	probe := &afterActionCapture{spyStrategy: &spyStrategy{}}
	probe.onAfterAction = func(history []schema.Turn) {
		var sawWatch, sawSettled bool
		for _, turn := range history {
			switch {
			case strings.Contains(turn.Message.Text(), watchMarker):
				sawWatch = true
			case strings.Contains(turn.Message.Text(), settledMarker):
				sawSettled = true
			}
		}
		if sawWatch {
			t.Error("the strategy's AfterAction history holds a pending watch row: the snapshot bypassed the unleased projection")
		}
		if !sawSettled {
			t.Error("the strategy's AfterAction history lost the settled control row: the filter dropped conversation, not withheld work")
		}
	}
	sub.sess.strategy = probe
	if err := sub.sess.notifyStrategyAfterAction(context.Background()); err != nil {
		t.Fatalf("notifyStrategyAfterAction: %v", err)
	}
	if probe.AfterActionCount() != 1 {
		t.Fatalf("strategy AfterAction calls = %d, want 1: the snapshot assertion never ran", probe.AfterActionCount())
	}
}

// TestDelegateResourceSupervision_FilteredFoldKeepsTheInFlightBoundary pins the
// in-flight boundary across a round > 0 fold whose input dropped a pending watch
// row.
//
// The boundary indexes the projection this round expands, and that projection is
// the filtered one: the withheld row is not in the fold's input, so the fold's
// own shrink cannot account for it. Both halves of the correction are exercised,
// one per ordering:
//
//   - The watch row sits before the boundary, so the boundary must step down for
//     the withheld row and then take the fold's shrink.
//   - The watch row sits at or after the boundary, so only the length the shrink
//     is measured from can move it.
//
// Ground truth is the marked in-flight turn's actual post-fold position, not a
// re-derived index: the marked turn is the first in-flight turn and the fold
// preserves it, so a boundary that miscounts leaves it pointing at a different
// turn.
func TestDelegateResourceSupervision_FilteredFoldKeepsTheInFlightBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		// watchBeforeBoundary places the withheld row inside the in-flight
		// region; otherwise the boundary opens before the row arrives.
		watchBeforeBoundary bool
	}{
		{name: "watch row before the boundary", watchBeforeBoundary: true},
		{name: "watch row after the boundary", watchBeforeBoundary: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newColdDelegateFixture(t, "")
			fixture.adapter.setSteps([]func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
			})
			root := restoreSupervisionRoot(t, fixture, nil)
			sub := warmStableSupervisionDelegate(t, root, fixture)
			waitForStableSupervisionRun(t, root, fixture.childID)
			if !sub.sess.stableChildTurnNeedsLease(context.Background()) {
				t.Fatal("the child is not the unleased stable-turn case this test needs")
			}
			// No elicited note on this fold: it would inject an extra turn whose
			// own correction is a different test's subject.
			sub.sess.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }

			seedNumberedSessionHistory(t, sub.sess, 12) // more than PreserveRecentTurns: forces a real fold
			const watchID = "watch:boundary-row:1"
			const markedText = "in-flight turn keep me"
			if !tc.watchBeforeBoundary {
				sub.sess.mu.Lock()
				sub.sess.turnHistoryBaseline = len(sub.sess.history)
				sub.sess.mu.Unlock()
			}
			if _, err := sub.sess.appendDelegateNotificationDurably(watchID, "withheld watch row"); err != nil {
				t.Fatalf("append the pending watch row: %v", err)
			}
			if tc.watchBeforeBoundary {
				sub.sess.mu.Lock()
				sub.sess.turnHistoryBaseline = len(sub.sess.history)
				sub.sess.mu.Unlock()
			}
			// The marked turn is the first in-flight turn; two more keep it far
			// enough from the tail that the fold preserves it.
			sub.sess.mu.Lock()
			sub.sess.history = append(sub.sess.history,
				schema.NewTurn(schema.TurnUserInput, llm.User(markedText)),
				schema.NewTurn(schema.TurnUserInput, llm.User("turn after the marked one 0")),
				schema.NewTurn(schema.TurnUserInput, llm.User("turn after the marked one 1")),
			)
			preHistory := append([]schema.Turn{}, sub.sess.history...)
			sub.sess.mu.Unlock()
			if indexOfTurnText(preHistory, markedText) < 0 {
				t.Fatalf("test setup invalid: %q is not in the %d-turn history", markedText, len(preHistory))
			}

			// The compaction manager's exact measurement is keyed to the turn count
			// it was recorded at, and the projection this fold receives is one turn
			// shorter than the resident history (the withheld row). Recording it at
			// the resident length would make the manager fall back to a character
			// estimate and never reach the fold, so it is recorded at the length the
			// fold will actually see.
			cw := sub.sess.contextMgr.EstimateUsage(nil, 0).Window
			if cw <= 0 {
				t.Fatalf("context window size is %d; cannot force pressure", cw)
			}
			sub.sess.contextMgr.RecordInputTokens(int(0.93*float64(cw))+1, len(preHistory)-1)

			if _, _, _, _, _, _, err := sub.sess.prepareModelRequestWithError(context.Background(), 1, new(events.RoundTimings)); err != nil {
				t.Fatalf("round 1 model request on the stable child = %v", err)
			}

			postHistory := currentHistory(t, sub.sess)
			if len(postHistory) >= len(preHistory) {
				t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
			}
			want := indexOfTurnText(postHistory, markedText)
			if want < 0 {
				t.Fatalf("the in-flight turn did not survive the fold: test setup invalid (post-fold history of %d turns)", len(postHistory))
			}
			sub.sess.mu.Lock()
			got := sub.sess.turnHistoryBaseline
			sub.sess.mu.Unlock()
			if got != want {
				t.Fatalf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d: the boundary does not account for the withheld watch row",
					got, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), want)
			}
		})
	}
}

// TestDelegateResourceSupervision_FailedWatchDeferralStaysOwedForTheLeasedRun
// pins the retry half of the deferral split (LIFE-01, #2340). A watch notice
// whose durable append FAILS is not durable, so the unleased turn must not
// carry it: the writer would put it into this reminder and settle it before
// admission could see the failure, and the notice would be gone. It goes back
// on the queue instead, owed and untouched, while the ordinary completion in
// the same batch rides the turn.
func TestDelegateResourceSupervision_FailedWatchDeferralStaysOwedForTheLeasedRun(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Only the durable ATTENTION append fails: every fold read the append
	// verifies against is refused, which is the append refusing. The ordinary
	// reminder write that carries the completion does not read this fold, so
	// the turn still runs and the mixed batch keeps its ordinary half.
	injected := errors.New("injected attention append failure")
	setFoldFault := func(fault func(string, string) (delegateAttentionFold, error)) {
		sub.sess.mu.Lock()
		sub.sess.cfg.testOnly.delegateAttentionReadFold = fault
		sub.sess.mu.Unlock()
	}
	setFoldFault(func(string, string) (delegateAttentionFold, error) { return delegateAttentionFold{}, injected })
	t.Cleanup(func() { setFoldFault(nil) })

	seedOwnedDurablePending(t, sub.sess.jobManager, "job_failed_deferral", jobstore.JobShell)
	sub.sess.enqueueJobNotification(jobNotification{
		JobID:   "job_failed_deferral",
		JobType: "shell",
		Status:  "completed",
	})
	sub.sess.enqueueJobNotification(watchSendDiagnosticNotification("watch:failed-deferral", "job:failed-deferral", "watch send failed: delivery_id=failed"))
	if queuedWatchNotices(t, sub.sess) != 1 || queuedOrdinaryNotices(t, sub.sess) != 1 {
		t.Fatal("the mixed batch did not land as one watch notice plus one ordinary completion")
	}

	markProcessing(sub.sess)
	if !sub.sess.acceptNotificationInput(context.Background(), "unleased-failed-deferral") {
		t.Fatal("the mixed batch was refused whole: the ordinary completion can never be delivered on any retry")
	}

	// The notice could not be made durable, so it stays OWED on the queue: a
	// writer that settled it here would have lost it.
	if queuedWatchNotices(t, sub.sess) != 1 {
		t.Fatal("a watch notice whose durable append failed left the queue: an unleased turn settled a notice nothing made durable")
	}
	// The ordinary completion beside it was carried by the turn: it waits for
	// the request that will present its reminder.
	carried := sub.sess.jobNotificationsAwaitingRequest()
	var carriedCompletion bool
	for _, d := range carried {
		if d.notification.JobID == "job_failed_deferral" {
			carriedCompletion = true
		}
	}
	if !carriedCompletion {
		t.Fatalf("notifications awaiting the request = %#v, want the ordinary completion", carried)
	}

	// While the append is still refused, a second split keeps the notice owed
	// rather than dropping it.
	applyUnleasedWatchSplit(t, sub.sess)
	if queuedWatchNotices(t, sub.sess) != 1 {
		t.Fatal("a failed deferral consumed the notice: nothing would ever present it")
	}

	// With the append working again the split makes the notice durable attention
	// and the queue lets go of it.
	setFoldFault(nil)
	if kept := applyUnleasedWatchSplit(t, sub.sess); len(kept) != 0 {
		t.Fatalf("notices carried by the recovered split = %v, want the watch notice deferred and nothing else", carriedNotificationJobIDs(kept))
	}
	if got := queuedWatchNotices(t, sub.sess); got != 0 {
		t.Fatalf("watch notices still queued = %d, want 0 after the append succeeded", got)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	if !slices.ContainsFunc(fold.order, func(id string) bool {
		return strings.HasPrefix(id, deferredWatchAttentionPrefix)
	}) {
		t.Fatalf("child attention rows = %v, want the recovered watch notice under %q", fold.order, deferredWatchAttentionPrefix)
	}
}

// TestDelegateResourceSupervision_ForcedFoldWithholdsAPendingWatchRow pins the
// pending-watch projection filter on the FORCED fold path, which every
// compact_context, model-recovery and /compact fold shares.
//
// A forced fold is a summarizer call on an unleased turn, so it obeys the same
// rule as a request: a pending watch row must not ride into the summary,
// because the summary is what later requests expand. Without the filter the row
// is folded into the summary and the leased generation that was owed it never
// presents it.
//
// The observable is the fold's own layer event: the checkpoint layer reports
// the history it was handed. The row sits mid-history with ordinary turns on
// both sides, so its omission shortens the input the fold sees by exactly one
// turn.
//
// Two invariants ride beside it. Publication must still merge from the
// UNFILTERED snapshot length: the turns after the withheld row are already in
// the snapshot the fold took, so a boundary lowered to the filtered length
// would re-append them as concurrent arrivals and duplicate ordinary history.
// And the row itself must survive the fold: it is pending durable attention, so
// the leased generation it waits for is still owed it.
func TestDelegateResourceSupervision_ForcedFoldWithholdsAPendingWatchRow(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.sess.stableChildTurnNeedsLease(context.Background()) {
		t.Fatal("the child is not the unleased stable-turn case this test needs")
	}

	// The row is a real deferral: the split below writes it under the identity
	// the filter reads back, so this test cannot pass on a row the production
	// deferral path would never have produced.
	deferred := watchSendDiagnosticNotification("watch:forced-fold-withhold", "job:forced-fold-withhold", "watch send failed: delivery_id=forced-fold")
	// Hold the wake the deferral arms. Its leased generation presents the row,
	// so running it here would settle the very row this fold must withhold.
	sub.sess.mu.Lock()
	previousNotify := sub.sess.notifyFunc
	sub.sess.mu.Unlock()
	sub.sess.SetNotifyFunc(func() {})
	t.Cleanup(func() { sub.sess.SetNotifyFunc(previousNotify) })
	sub.sess.enqueueJobNotification(deferred)
	attentionID := deferredWatchAttentionID(deferred)
	if kept := applyUnleasedWatchSplit(t, sub.sess); len(kept) != 0 {
		t.Fatalf("notices carried by the split = %v, want the watch notice deferred and nothing else", carriedNotificationJobIDs(kept))
	}
	if pending := sub.sess.pendingDeferredWatchAttention(); !pending[attentionID] {
		t.Fatalf("pending watch rows = %v, want %q", pending, attentionID)
	}

	// Ordinary turns on both sides make the withhold an interior removal, not a
	// truncation of the tail.
	const (
		lead = 10
		tail = 10
	)
	sub.sess.mu.Lock()
	var seeded []schema.Turn
	for i := range lead {
		seeded = append(seeded, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("lead %d", i))))
	}
	for i := range tail {
		seeded = append(seeded, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("tail %d", i))))
	}
	sub.sess.history = append(append([]schema.Turn{}, seeded[:lead]...), sub.sess.history...)
	sub.sess.history = append(sub.sess.history, seeded[lead:]...)
	before := len(sub.sess.history)
	rowAt := -1
	for i, turn := range sub.sess.history {
		if turn.AttentionID == attentionID {
			rowAt = i
		}
	}
	sub.sess.mu.Unlock()
	if rowAt <= 0 || rowAt >= before-1 {
		t.Fatalf("the pending watch row sits at %d of %d turns, want it interior to ordinary history", rowAt, before)
	}

	// The layer events are the fold's own report of what it was handed.
	var (
		layersMu sync.Mutex
		layers   []events.ContextCompactionData
	)
	// checkpointSeen closes the first time the reader consumes a checkpoint
	// layer event. The fold emits it synchronously, before foldWithForceCompact
	// returns, but only the reader has seen it: stopping the reader first would
	// drop the very event this proof reads.
	stopReading := make(chan struct{})
	readerDone := make(chan struct{})
	checkpointSeen := make(chan struct{})
	var stopOnce sync.Once
	t.Cleanup(func() { stopOnce.Do(func() { close(stopReading) }) })
	go func() {
		defer close(readerDone)
		var sawCheckpoint sync.Once
		for {
			select {
			case ev := <-sub.sess.Events():
				if data, ok := ev.Data.(events.ContextCompactionData); ok {
					layersMu.Lock()
					layers = append(layers, data)
					layersMu.Unlock()
					if data.Layer == "checkpoint" {
						sawCheckpoint.Do(func() { close(checkpointSeen) })
					}
				}
			case <-stopReading:
				return
			}
		}
	}()

	if ok, refusal := sub.sess.foldWithForceCompact(context.Background(), "", nil, triggerAuto, false); !ok {
		t.Fatalf("forced fold = %v %v, want it published", ok, refusal)
	}
	testwait.Until(t, "the forced fold's checkpoint layer event to reach its reader", func() bool {
		select {
		case <-checkpointSeen:
			return true
		default:
			return false
		}
	})
	stopOnce.Do(func() { close(stopReading) })
	<-readerDone
	layersMu.Lock()
	published := append([]events.ContextCompactionData(nil), layers...)
	layersMu.Unlock()
	foldedIn := -1
	for _, layer := range published {
		if layer.Layer == "checkpoint" {
			foldedIn = layer.TurnsBefore
			break
		}
	}
	// The withheld row is the only turn the fold may not see, so the fold's
	// input is the snapshot minus exactly that row. Equality with the unfiltered
	// snapshot is the red state: the row rode into the summary.
	if foldedIn < 0 {
		t.Fatal("the fold published no checkpoint layer event, so this proof never saw its input")
	}
	if foldedIn >= before {
		t.Fatalf("the fold was handed %d turns of a %d-turn snapshot, want the pending watch row withheld", foldedIn, before)
	}
	if want := before - 1; foldedIn != want {
		t.Fatalf("the fold was handed %d turns, want %d: only the pending watch row may be withheld", foldedIn, want)
	}

	sub.sess.mu.Lock()
	defer sub.sess.mu.Unlock()
	// Publication merged from the UNFILTERED snapshot length: no turn of the
	// snapshot came back a second time.
	seen := make(map[string]int, len(sub.sess.history))
	for _, turn := range sub.sess.history {
		if turn.StableTurnID != "" {
			seen[turn.StableTurnID]++
		}
	}
	for id, count := range seen {
		if count > 1 {
			t.Fatalf("the forced fold published turn %q %d times, want it exactly once: the merge boundary must stay the unfiltered snapshot length", id, count)
		}
	}
	if len(sub.sess.history) > before {
		t.Fatalf("history after the fold = %d turns, want <= %d: the fold re-appended turns it already folded", len(sub.sess.history), before)
	}
	// The row is still owed: pending durable attention survives the fold, so the
	// leased generation it waits for still presents it.
	rowKept := slices.ContainsFunc(sub.sess.history, func(turn schema.Turn) bool {
		return turn.AttentionID == attentionID
	})
	if !rowKept {
		t.Fatalf("the pending watch row is gone from history after the fold, want it preserved for its leased generation")
	}
}

// TestDelegateResourceSupervision_RestartCancellationNoticeKeysOnTheBatch pins
// the identity of the coalesced restart-cancellation notice.
//
// The notice is coalesced once per receiver, and its row is consumed by the
// generation that presents it. An identity that names only the receiver then
// deduplicates a LATER restart's cancellations against that consumed row: no
// new row, no arm, and the delegate never learns that the watches it registered
// since then are gone. Keying on the cancelled generations tells the two
// batches apart while a retry of the same batch still deduplicates.
func TestDelegateResourceSupervision_RestartCancellationNoticeKeysOnTheBatch(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	jm := sub.sess.jobManager
	if jm == nil || jm.watchAttention == nil {
		t.Fatal("the stable child's job manager has no watch-attention route wired")
	}
	sub.sess.mu.Lock()
	previousNotify := sub.sess.notifyFunc
	sub.sess.mu.Unlock()
	var wakes atomic.Int64
	sub.sess.SetNotifyFunc(func() { wakes.Add(1) })
	t.Cleanup(func() { sub.sess.SetNotifyFunc(previousNotify) })

	first := []*jobstore.WatchRecord{{
		WatchID:           "watch_first_batch",
		Generation:        "1",
		Target:            "job_gone",
		ReceiverSessionID: sub.sess.id,
	}}
	jm.watchesLostAtRestore = first
	if err := jm.notifyRestartCancelledCallbackWatches(); err != nil {
		t.Fatalf("notify the first restart cancellation: %v", err)
	}
	firstID := restartCancelledCallbackWatchAttentionID(sub.sess.id, first)
	if wakes.Load() == 0 {
		t.Fatal("the first cancellation armed no wake")
	}

	// The generation that presents the row consumes it, which is the state a
	// retry and a later batch both have to answer for.
	if err := sub.sess.resolveAttentionDurably([]string{firstID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("consume the first cancellation row: %v", err)
	}
	wakesAfterConsume := wakes.Load()

	// A retry of the SAME batch deduplicates: the row is already there and
	// consumed, so nothing new is appended and no wake is published.
	jm.watchesLostAtRestore = first
	if err := jm.notifyRestartCancelledCallbackWatches(); err != nil {
		t.Fatalf("retry the first restart cancellation: %v", err)
	}
	if got := wakes.Load(); got != wakesAfterConsume {
		t.Fatalf("wakes after retrying the same batch = %d, want %d: a duplicate notice was published", got, wakesAfterConsume)
	}

	// A LATER restart cancelling a new generation is a new notice: its own row
	// and its own wake.
	second := []*jobstore.WatchRecord{{
		WatchID:           "watch_second_batch",
		Generation:        "2",
		Target:            "job_gone",
		ReceiverSessionID: sub.sess.id,
	}}
	jm.watchesLostAtRestore = second
	if err := jm.notifyRestartCancelledCallbackWatches(); err != nil {
		t.Fatalf("notify the second restart cancellation: %v", err)
	}
	secondID := restartCancelledCallbackWatchAttentionID(sub.sess.id, second)
	if secondID == firstID {
		t.Fatalf("both batches derived attention identity %q: a later batch would deduplicate against the consumed row", secondID)
	}
	if got := wakes.Load(); got <= wakesAfterConsume {
		t.Fatalf("wakes after the second batch = %d, want more than %d: the new cancellation armed nothing", got, wakesAfterConsume)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	if _, exists := fold.content[secondID]; !exists {
		t.Fatalf("child attention rows = %v, want the second batch's cancellation under %q", fold.order, secondID)
	}
	if !slices.Contains(fold.pendingIDs(), secondID) {
		t.Fatalf("pending attention rows = %v, want the second batch's cancellation pending under %q", fold.pendingIDs(), secondID)
	}
}

// queuedJobNotifications snapshots the session's own notification queue without
// draining it, so a test can assert what a turn would actually be offered
// rather than what it did with a batch it already took.
func queuedJobNotifications(t *testing.T, sess *Session) []jobNotification {
	t.Helper()
	sess.pendingJobNotifsMu.Lock()
	defer sess.pendingJobNotifsMu.Unlock()
	return append([]jobNotification(nil), sess.pendingJobNotifs...)
}

// queuedWatchNotices counts the watch-typed notices still queued on the
// session's own rail.
func queuedWatchNotices(t *testing.T, sess *Session) int {
	t.Helper()
	var queued int
	for _, n := range queuedJobNotifications(t, sess) {
		if n.isWatch() {
			queued++
		}
	}
	return queued
}

// queuedOrdinaryNotices counts the non-watch notices still queued on the
// session's own rail.
func queuedOrdinaryNotices(t *testing.T, sess *Session) int {
	t.Helper()
	return len(queuedJobNotifications(t, sess)) - queuedWatchNotices(t, sess)
}

// applyUnleasedWatchSplit runs THE RULE's split over the session's current
// queue exactly as a notification turn does at its opening write, and returns
// the notices the turn would carry. It mirrors acceptNotificationInput's
// filter → awaiting-request → split sequence, so a test drives the same code
// the turn drives.
func applyUnleasedWatchSplit(t *testing.T, sess *Session) []deliverableJobNotification {
	t.Helper()
	jobNotifs, retryJobNotifs, injectedJobNotifs := sess.filterDeliverableJobNotifications(sess.drainJobNotifications())
	jobNotifs, injectedJobNotifs = sess.withoutAwaitingRequest(jobNotifs), sess.withoutAwaitingRequest(injectedJobNotifs)
	sess.requeueJobNotifications(retryJobNotifs)
	sess.requeueJobNotifications(sess.markJobNotificationsDelivered(injectedJobNotifs))
	return sess.deferUnleasedWatchNotices(context.Background(), jobNotifs)
}

// carriedNotificationJobIDs reports which notices in kept the turn would write
// into its reminder.
func carriedNotificationJobIDs(kept []deliverableJobNotification) []string {
	out := make([]string, 0, len(kept))
	for _, d := range kept {
		out = append(out, d.notification.JobID)
	}
	return out
}

// TestDelegateResourceSupervision_RestartCancelledCallbackWatchBecomesLeasedAttention
// pins the route a restart-CANCELLED callback watch takes to a stable delegate
// receiver. The notice names a watch, and only a leased generation presents a
// watch notice, so an unleased turn must not carry it: the receiver's steering
// queue would present it beside a claim that no watch is behind it and leave no
// durable row for the generation that may present it.
//
// The row under the receiver's identity is the durable half, and the
// controller's committed wake for that identity is the leased half: an arm that
// published a wake is what a generation may drive from.
func TestDelegateResourceSupervision_RestartCancelledCallbackWatchBecomesLeasedAttention(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	jm := sub.sess.jobManager
	if jm == nil || jm.watchAttention == nil {
		t.Fatal("the stable child's job manager has no watch-attention route wired")
	}
	// Hold the wake instead of letting it drive: the arm publishes the wake and
	// then wakes the receiver, and a driving receiver presents the row and
	// resolves it before the assertions below can read the armed state.
	sub.sess.mu.Lock()
	previousNotify := sub.sess.notifyFunc
	sub.sess.mu.Unlock()
	var wakes atomic.Int64
	sub.sess.SetNotifyFunc(func() { wakes.Add(1) })
	t.Cleanup(func() { sub.sess.SetNotifyFunc(previousNotify) })
	// The shape restore leaves: a no-send callback watch whose receiver is the
	// child itself. A send watch is a different rail and is skipped here.
	jm.watchesLostAtRestore = []*jobstore.WatchRecord{{
		WatchID:           "watch_cancelled",
		Generation:        "1",
		Target:            "job_gone",
		ReceiverSessionID: sub.sess.id,
	}}
	if err := jm.notifyRestartCancelledCallbackWatches(); err != nil {
		t.Fatalf("notify restart-cancelled callback watches: %v", err)
	}
	if wakes.Load() == 0 {
		t.Fatal("the cancellation armed no wake: a leased generation would never be driven for the row")
	}

	attentionID := restartCancelledCallbackWatchAttentionID(sub.sess.id, jm.watchesLostAtRestore)
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	_, ok := fold.content[attentionID]
	if !ok {
		t.Fatalf("child attention rows = %v, want the cancellation under %q", fold.order, attentionID)
	}
	if !isWatchNoticeAttentionID(attentionID) {
		t.Fatalf("attention identity %q does not read as a watch notice: an unleased projection would present it", attentionID)
	}
	// The leased half: the controller holds a published wake for the row, which
	// is what a generation drives from.
	controller := sub.sess.delegateController
	controller.mu.Lock()
	state := controller.attention[fixture.delegateID]
	_, committed := false, false
	if state != nil {
		_, committed = state.committedWakeIDs[attentionID]
	}
	controller.mu.Unlock()
	if !committed {
		t.Fatalf("controller committed no wake for %q: the row would wait for a drive that never comes", attentionID)
	}
	// And it did not ride the steering queue, which is the presentation a leased
	// generation alone may make.
	if got := sub.sess.peekNotifications(); got != 0 {
		t.Fatalf("queued notifications after the cancellation = %d, want 0: the notice is durable attention, not steering", got)
	}
	if sub.sess.hasPendingSteering() {
		t.Fatal("the cancellation landed in the child's steering queue, where an unleased turn would present it")
	}
}

// TestDelegateResourceSupervision_IdleStableDelegateTimerRoutesThroughLeasedAttention
// pins the real rail the split defers: an idle stable delegate's timer fires
// while nothing has leased the child, so the notice it makes must reach the
// model through leased attention and never through an unleased request.
//
// The fire is a real timer tick on the child's own job manager, advanced by the
// fixture clock, so what is deferred here is the notice the production cadence
// makes rather than a hand-built one. The child is idle: the warm run has
// quiesced, so nothing holds a lease, and the only presentation left to this
// row is the one its arm drives.
//
// The wake is held for the body of the test, which is what makes the fire
// observable: an unheld wake drives the child's leased generation at once, and
// that generation presents and settles the notice before it can be read on the
// queue. Holding the wake is production behaviour — the same primitive the
// cadence uses to keep one event's notices in one turn — so the queue the
// assertions read is the queue a real notification turn would open on.
//
// Three things make the proof mean something:
//
//   - The row is DURABLE and its wake is COMMITTED on the controller, which is
//     the leased half. A notice left on the queue has neither.
//   - The unleased request the child builds carries no watch turn, so the row
//     never rides outside a lease.
//   - An ordinary completion queued after the fire is still carried by the next
//     unleased turn. A split that stranded the queue behind the watch rail would
//     pass the first two and fail this one.
func TestDelegateResourceSupervision_IdleStableDelegateTimerRoutesThroughLeasedAttention(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("leased timer handled") },
	})
	clockAt := time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)
	clk := agenttest.NewFakeClockAt(clockAt)
	root := restoreSupervisionRoot(t, fixture, clk)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.sess.stableChildTurnNeedsLease(context.Background()) {
		t.Fatal("the child is not the unleased stable-turn case this test needs")
	}

	// The timer is installed through the production create path on the child's
	// own manager, so its fire is the cadence's own work.
	jm := sub.sess.jobManager
	const timerNote = "timer-note-sentinel"
	if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60, Note: timerNote}); err != nil {
		t.Fatalf("create the child's timer watch: %v", err)
	}

	// Count the child's wake instead of driving it, before the tick lands. A
	// driving receiver presents the row and resolves it before the assertions
	// below can read the armed state, and the arm wakes the receiver itself --
	// through notify, not through the notification queue -- so the wake is what
	// has to be held. The release at the end runs the drive on purpose.
	sub.sess.mu.Lock()
	previousNotify := sub.sess.notifyFunc
	sub.sess.mu.Unlock()
	var wakes atomic.Int64
	sub.sess.SetNotifyFunc(func() { wakes.Add(1) })
	var restoreOnce sync.Once
	releaseWake := func() { restoreOnce.Do(func() { sub.sess.SetNotifyFunc(previousNotify) }) }
	t.Cleanup(releaseWake)
	// Registering a callback wakes once for work already owed, so the fire's own
	// wake is the difference from here.
	wakesBeforeFire := wakes.Load()

	// Fire the timer the way its own ticker does, on the fixture clock.
	clk.Advance(60 * time.Second)
	// Wait for the wake callback itself before inspecting the queue: the
	// producer releases the queue lock before invoking notify, so observing
	// the queued notice first can race ahead of the wake increment.
	testwait.Until(t, "the timer fire to arm the child's wake", func() bool {
		return wakes.Load() > wakesBeforeFire
	})
	if got := queuedWatchNotices(t, sub.sess); got != 1 {
		t.Fatalf("queued watch notices = %d, want the one the timer fired", got)
	}

	// The timer notice is on the queue and the child is unleased, so the split is
	// what has to move it: this is the same code the notification turn runs at
	// its opening write.
	if kept := applyUnleasedWatchSplit(t, sub.sess); len(kept) != 0 {
		t.Fatalf("notices carried by the split = %v, want the timer notice deferred and nothing else", carriedNotificationJobIDs(kept))
	}
	if got := queuedWatchNotices(t, sub.sess); got != 0 {
		t.Fatalf("watch notices still queued = %d, want the deferred row to own it", got)
	}

	// The durable half: a watch row the unleased projection withholds.
	pending := sub.sess.pendingDeferredWatchAttention()
	var rowID string
	for id := range pending {
		if strings.HasPrefix(id, deferredWatchAttentionPrefix) {
			rowID = id
		}
	}
	if rowID == "" {
		t.Fatalf("pending watch rows = %v, want the timer notice under %q", pending, deferredWatchAttentionPrefix)
	}
	// The leased half: the controller holds a committed wake for that row, which
	// is what a generation drives from. Without it the row waits for a drive that
	// never comes.
	controller := sub.sess.delegateController
	controller.mu.Lock()
	state := controller.attention[fixture.delegateID]
	committed := false
	if state != nil {
		_, committed = state.committedWakeIDs[rowID]
	}
	controller.mu.Unlock()
	if !committed {
		t.Fatalf("controller committed no wake for %q: the row would wait for a drive that never comes", rowID)
	}

	// The unleased request still builds, and it does not present the row: the
	// projection withholds exactly the pending watch attention.
	_, _, unleasedHistory, _, _, _, err := sub.sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("unleased model request on the stable child = %v", err)
	}
	if len(unleasedHistory) == 0 {
		t.Fatal("the unleased request built an empty history: this proof needs the request the row was withheld from")
	}
	for _, msg := range unleasedHistory {
		if strings.Contains(msg.Text(), timerNote) {
			t.Fatal("the unleased request presents the deferred timer notice: the row rode outside a lease")
		}
	}

	// The ordinary rail still works beside it: a completion queued now is carried
	// by the next unleased turn rather than stranded behind the watch row.
	seedOwnedDurablePending(t, jm, "job_after_timer", jobstore.JobShell)
	sub.sess.enqueueJobNotification(jobNotification{JobID: "job_after_timer", JobType: "shell", Status: "completed"})
	markProcessing(sub.sess)
	if !sub.sess.acceptNotificationInput(context.Background(), "after-timer") {
		t.Fatal("an ordinary completion was refused after the timer notice: the drain is stalled behind the watch rail")
	}
	carried := sub.sess.jobNotificationsAwaitingRequest()
	var carriedCompletion bool
	for _, d := range carried {
		if d.notification.JobID == "job_after_timer" {
			carriedCompletion = true
		}
	}
	if !carriedCompletion {
		t.Fatalf("notifications awaiting the request = %#v, want the completion carried past the timer notice", carried)
	}

	// Release the hold: the wake the fire armed now reaches the child, and the
	// generation it drives is the one that must present and settle the row. A row
	// left pending here would be attention no turn ever collects.
	requestsBeforeRelease := supervisionRequestCount(fixture.adapter)
	releaseWake()
	testwait.Until(t, "the leased generation to present and settle the deferred timer row", func() bool {
		if supervisionRequestCount(fixture.adapter) <= requestsBeforeRelease {
			return false
		}
		if !requestsContain(fixture.adapter.Requests()[requestsBeforeRelease:], timerNote) {
			return false
		}
		return !sub.sess.pendingDeferredWatchAttention()[rowID]
	})
}

// TestProgressTicksCarryDistinctDeliveryOrdinals pins the identity half of the
// progress cadence (LIFE-01, #2340). A progress_interval_ms watch is not a
// timer, but every tick is still its own delivery: without an ordinal, two
// ticks of one watch derive one durable attention identity -- same watch, same
// job, same reason -- and the second is dropped as a replay of the first.
func TestProgressTicksCarryDistinctDeliveryOrdinals(t *testing.T) {
	t.Parallel()
	h := newCoalesceHarness(t)

	if !h.tick(t, 1) {
		t.Fatal("the first progress tick retired the watch")
	}
	h.produce(t, "second delivery\n")
	if !h.tick(t, 2) {
		t.Fatal("the tick after new output retired the watch")
	}
	if len(h.notified) != 2 {
		t.Fatalf("delivered ticks = %d, want 2", len(h.notified))
	}
	first, second := h.notified[0], h.notified[1]
	if first.DeliveryOrdinal != 1 || second.DeliveryOrdinal != 2 {
		t.Fatalf("tick ordinals = %d, %d, want 1, 2", first.DeliveryOrdinal, second.DeliveryOrdinal)
	}
	if first.OriginWatchID == "" || first.OriginWatchID != second.OriginWatchID {
		t.Fatalf("tick origin watch ids = %q, %q, want one non-empty watch id", first.OriginWatchID, second.OriginWatchID)
	}
	if deferredWatchAttentionID(first) == deferredWatchAttentionID(second) {
		t.Fatalf("both ticks derive attention identity %q: the second would be dropped as a replay", deferredWatchAttentionID(first))
	}
}

// TestLongRunningRemindersCarryDistinctDeliveryIDs pins the identity rule on
// the long-running shell reminder (LIFE-01, #2340). Every fire says the same
// thing about the same jobs, so each fire carries its own delivery ID and two
// fires derive two durable attention identities.
func TestLongRunningRemindersCarryDistinctDeliveryIDs(t *testing.T) {
	t.Parallel()
	r := newReminderRig(t)
	r.startShell(t)

	r.advance(longRunningJobReminderInterval)
	first := r.reminders()
	if len(first) != 1 {
		t.Fatalf("first reminder count = %d, want 1", len(first))
	}
	r.advance(longRunningJobReminderInterval)
	second := r.reminders()
	if len(second) != 1 {
		t.Fatalf("second reminder count = %d, want 1", len(second))
	}
	if deferredWatchAttentionID(first[0]) == deferredWatchAttentionID(second[0]) {
		t.Fatalf("both reminders derive attention identity %q: the second would be dropped as a replay", deferredWatchAttentionID(first[0]))
	}
}

// TestLongRunningReminderAfterRestoreAppendsBesideThePreRestoreRow pins that
// identity across a restore. A fresh job manager on the same transcript and
// delegate restarts every in-process counter it owns, while the pre-restore
// reminder's durable attention row survives in the transcript. An identity
// derived from such a counter would hand the new reminder the old row's
// identity, and the append would reject it as conflicting content instead of
// presenting the new reminder.
//
// The retry half is pinned with it: the same notice appended twice keeps one
// row, so a retry of a reminder that already landed deduplicates.
func TestLongRunningReminderAfterRestoreAppendsBesideThePreRestoreRow(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Two managers over ONE job store, each with its own running shell job and
	// each firing the real reminder cadence: that is the restore shape, where
	// the second manager's in-process counters start from zero while the first
	// manager's durable attention row is still in the transcript. The managers
	// live under their own session id so they cannot disturb the child's own
	// job manager; only the attention target is the child.
	reminderManager := identifier.MustNewSessionID()
	const command = "go test ./..."
	fireReminder := func(t *testing.T) jobNotification {
		t.Helper()
		var fired []jobNotification
		jm, err := newJobManagerNoSync(sub.sess.stateDir, reminderManager, func(n jobNotification) {
			fired = append(fired, n)
		})
		if err != nil {
			t.Fatalf("open reminder job manager: %v", err)
		}
		clock := agenttest.NewFakeClock()
		jm.reminderClock = clock
		startAt := clock.Now().Add(-2 * longRunningJobReminderInterval)
		freezeClockAt(jm, startAt)
		if _, err := jm.createShell(createShellOpts{Command: command}); err != nil {
			t.Fatalf("create the long-running shell job: %v", err)
		}
		freezeClockAt(jm, clock.Now())
		jm.armLongRunningReminder()
		clock.Advance(longRunningJobReminderInterval)
		clock.Drain()
		// Crash rather than close: the runtime goes away, the store keeps what
		// it appended, which is what a restart leaves behind.
		crashJobManager(t, jm)
		var reminders []jobNotification
		for _, n := range fired {
			if n.isLongRunningJobsReminder() {
				reminders = append(reminders, n)
			}
		}
		if len(reminders) != 1 {
			t.Fatalf("reminders fired = %d, want 1", len(reminders))
		}
		return reminders[0]
	}

	before := fireReminder(t)
	if _, err := sub.sess.persistDeferredWatchNotice(before); err != nil {
		t.Fatalf("persist the pre-restore reminder: %v", err)
	}
	beforeID := deferredWatchAttentionID(before)

	after := fireReminder(t)
	afterID := deferredWatchAttentionID(after)
	if afterID == beforeID {
		t.Fatalf("the post-restore reminder reused attention identity %q: the append would reject it as conflicting content", beforeID)
	}
	if _, err := sub.sess.persistDeferredWatchNotice(after); err != nil {
		t.Fatalf("append the post-restore reminder beside the pre-restore row: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold: %v", err)
	}
	for _, wantID := range []string{beforeID, afterID} {
		if _, ok := fold.content[wantID]; !ok {
			t.Fatalf("child attention rows = %v, want %q", fold.order, wantID)
		}
	}

	// Retrying the SAME notice re-derives the same identity and content, so the
	// append is a no-op: the fold keeps exactly one row for it. A retry that
	// re-derived a fresh identity would add a second row for the same notice.
	if _, err := sub.sess.persistDeferredWatchNotice(after); err != nil {
		t.Fatalf("retry the same reminder: %v", err)
	}
	retried, err := readDelegateAttentionFold(transcriptPath(sub.sess.stateDir, sub.sess.id), sub.sess.id)
	if err != nil {
		t.Fatalf("read the child's attention fold after the retry: %v", err)
	}
	rows := 0
	for _, id := range retried.order {
		if id == afterID {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("rows for the retried reminder = %d, want 1: the retry must deduplicate against its own row", rows)
	}
}

// TestDelegateResourceSupervision_LiveWatchedCommandNoticeReachesTheFinalizingDrain pins the notice
// a finalizing stable delegate must still receive: an output_match match on a
// background command the delegate itself owns, fired while that command is
// still running.
//
// The delegate has ended its own turn and is parked in its owned-job drain
// waiting for that command. The parent's loop-boundary sweep reads the child's
// queue and tries to drive it; the child is finalizing, so the drive is
// refused. Refusing the DRIVE must not consume the NOTICE: the drain the child
// is already parked in runs under the run's lease, and that turn is the reader
// that presents the notice. A sweep that moved the notice off the queue on its
// way past would hand it to the one reader that cannot present it -- no new
// generation can start while this one is finalizing -- and the run would wait
// for a readiness line the model never sees.
//
// The fire and the sweep are placed inside the child's own park, so no drain
// pass can run between them and the ordering under test is exact.
//
// The assertion is the provider request the drain makes: it carries the watch's
// own note while the watched command is still alive. That is the notice arriving
// at the leased generation, not merely still being queued somewhere.
func TestDelegateResourceSupervision_LiveWatchedCommandNoticeReachesTheFinalizingDrain(t *testing.T) {
	t.Parallel()

	const note = "watch delivery under test"
	var fired sync.Once
	var fixture *ownedJobDrainFixture
	parked := func(s *Session, ready <-chan struct{}) {
		if s == nil || s.owningDelegateID == "" {
			return
		}
		// The drain parks as soon as the child is finalizing, which is before
		// the fixture exists and before the watch below is installed. Firing
		// then would spend the only fire on a watch that does not exist yet.
		<-ready
		fired.Do(func() {
			// The command the delegate owns is still alive and prints the
			// readiness line now, which is the fire.
			line := []byte("server ready\n")
			s.jobManager.feedJobOutput(fixture.shellJobID, line, int64(len(line)))
			// The parent's sweep, with the notice queued and the child
			// finalizing.
			fixture.parent.driveChildrenWithUndeliveredAttention()
		})
	}
	fixture = newOwnedJobDrainFixtureWithParkedHook(t, parked)

	// A watch on the delegate's OWN live background command, waiting for the
	// readiness line the command prints from inside the park below.
	if _, err := fixture.child.sess.jobManager.configureWatch(watchArgs{
		Operation:   "create",
		Target:      fixture.shellJobID,
		OutputMatch: "server ready",
		Note:        note,
	}); err != nil {
		t.Fatalf("configure the output_match watch: %v", err)
	}
	// The fixture and the watch are both in place, so the parked hook may fire.
	fixture.releaseParked()

	// The drain's next turn reaches the model with the notice: no other
	// notification in this fixture carries the note, and the leased drain is the
	// only turn that can present it.
	testwait.UntilSeen(t, "the drain's provider request carrying the watch notice", func() (bool, string) {
		requests := fixture.adapter.Requests()
		if requestsContain(requests, note) {
			return true, ""
		}
		return false, fmt.Sprintf("%d provider request(s), %d watch notice(s) queued", len(requests), queuedWatchNotices(t, fixture.child.sess))
	})

	// And it arrived while the watched command was still running: that is the
	// live-command window the finding is about.
	fixture.child.sess.jobManager.mu.Lock()
	_, alive := fixture.child.sess.jobManager.running[fixture.shellJobID]
	fixture.child.sess.jobManager.mu.Unlock()
	if !alive {
		t.Fatal("the watched command had already exited: this proof needs the notice delivered while it was still running")
	}
}
