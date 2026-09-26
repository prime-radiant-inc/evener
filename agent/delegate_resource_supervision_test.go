package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/jobstore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/shellquote"
	"primeradiant.com/evener/llm"
)

func TestRouteNoToolCalls(t *testing.T) {
	tests := []struct {
		name          string
		kind          EntryKind
		noContent     bool
		afterTerminal bool
		want          noCallsRoute
	}{
		{name: "notification acknowledgement", kind: EntryNotification, want: finishIdle},
		{name: "notification silence", kind: EntryNotification, noContent: true, want: runNoToolCalls},
		{name: "notification silence after terminal", kind: EntryNotification, noContent: true, afterTerminal: true, want: finishIdle},
		{name: "user input", kind: EntryUserInput, want: runNoToolCalls},
		{name: "continuation", kind: EntryContinuation, want: runNoToolCalls},
		{name: "delegate attention", kind: EntryDelegateAttention, want: runNoToolCalls},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := routeNoToolCalls(tt.kind, tt.noContent, tt.afterTerminal); got != tt.want {
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
	registerCommunicateTool(reg, newToolDeps(runtime))
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	if _, err := reg.Get("communicate").Exec(ctx, nil, map[string]any{
		"message":  "reported result",
		"end_turn": true,
	}); err != nil {
		t.Fatalf("communicate: %v", err)
	}
	if !runtime.Communicated() || !strings.Contains(runtime.CommunicateOutput(), "reported result") {
		t.Fatalf("reported path changed: called=%t output=%q", runtime.Communicated(), runtime.CommunicateOutput())
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			if err := root.subagents.get(fixture.childID).sess.FollowUp("queued follow-up work"); err != nil {
				t.Fatal(err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return finalResponse("follow-up report") },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			if err := root.subagents.get(fixture.childID).sess.FollowUp("queued follow-up work"); err != nil {
				t.Fatal(err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return finalResponse("follow-up report") },
	}
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
// turns then share one drain ladder and popFollowUp (a destructive pop with no
// owner check) lets the unleased turn steal the run's follow-up. The send path
// must take the drive claim atomically with its drivability check and hold it
// across the whole window, so the child refuses the drive-down.
func TestDelegateResourceSupervision_CommittedSendStartRefusesASecondTurn(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("send result") },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("send result") },
	}
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
// This test is load-bearing for that fix: with the rollback's re-drive removed,
// the notification enqueued while the claim was held is never drained and the
// child never runs a second turn.
func TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesDroppedWake(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("drained notification") },
	}
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
			candidate.sess.enqueueJobNotification(jobNotification{
				Kind:   jobNotificationKindWatch,
				JobID:  "redrive-dropped-wake",
				Status: jobNotificationEventWatch,
			})
			candidate.sess.notify()
			mu.Lock()
			claimSeen = true
			wakeDropped = root.childCommittedSendStart(childSessionID) &&
				root.subagents.get(childSessionID).sess.peekNotifications() > 0
			mu.Unlock()
			// Fail the send after the claim: abort the in-flight reservation so
			// the commit below cannot hand a run over.
			abortStableDelegateStartReservation(root, fixture.delegateID)
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
	// TRIPWIRE: the rollback re-drive and the drive turn it launches are served
	// by the scripted in-process adapter, so the notification drains in
	// milliseconds; this bound only fires on the regression this test pins.
	waitForCondition(t, 10*time.Second, "dropped notification redriven after the rollback", func() bool {
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
// driveStableDelegateAttention through the child's notify, and the claim makes
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	}
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
			// the child's notify runs driveStableDelegateAttention, and the claim
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
			abortStableDelegateStartReservation(root, fixture.delegateID)
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
	// armed attention. TRIPWIRE: the rollback re-drive and the drive turn it
	// launches are served by the scripted in-process adapter, so the attention
	// drains in milliseconds; this bound only fires on the regression this test
	// pins.
	waitForCondition(t, 10*time.Second, "dropped attention redriven after the rollback", func() bool {
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
// driveStableDelegateAttention refused on that flag, and nothing retried it --
// the notification turn's own re-drive check looks only at notifications, not
// armed attention. The rollback now shares the wake edge's order
// (driveChildIfNotStopGated: stable attention FIRST, whose run drains the
// notification queue, then the notification turn), so neither stays pending.
//
// With the pre-fix order this test's final wait never sees the attention drain
// and times out: that is the load-bearing falsification.
func TestDelegateResourceSupervision_CommittedSendStartRollbackRedrivesBoth(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		func(llm.Request) llm.Response { return finalResponse("drained notification") },
	}
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
			abortStableDelegateStartReservation(root, fixture.delegateID)
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
	// TRIPWIRE: the rollback re-drive and the drive turn it launches are served
	// by the scripted in-process adapter, so both queues empty in milliseconds;
	// this bound only fires on the regression this test pins.
	waitForCondition(t, 10*time.Second, "attention and notification both redriven after the rollback", func() bool {
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
// guard in driveStableDelegateAttention already does. `running` goes false at
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("must not have started") },
	}
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

// abortStableDelegateStartReservation aborts the delegate's outstanding start
// reservation from inside a test seam, forcing the in-flight send down a
// non-handoff exit after it has taken the committed-send-start claim.
func abortStableDelegateStartReservation(root *Session, delegateID string) {
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("contended send result") },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			started, err := root.subagents.get(fixture.childID).sess.SetGoal(context.Background(), "finish continuation work")
			if err != nil || started {
				t.Errorf("SetGoal during attention = started:%t err:%v, want deferred", started, err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return finalResponse("continuation report") },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
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
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)

	shell := createStableDelegateShell(t, sub.sess.jobManager, "bare attention incident")
	finishStableDelegateShell(t, sub.sess.jobManager, shell.JobID)
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return finalResponse("attention report") },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	for range 8 {
		fixture.adapter.steps = append(fixture.adapter.steps, bare)
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	snapshots := make(chan delegateCompletionSnapshot, 1)
	root.cfg.testOnly.subagentBeforeSettlement = captureStableCompletionSnapshot(snapshots)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	snapshot := <-snapshots
	if snapshot.requirement != delegateCompletionReportRequired || snapshot.outcome != delegateCompletionOutcomeNone || snapshot.terminalSeen {
		t.Fatalf("user-run evidence = %#v, want report-required missing terminal", snapshot)
	}
	assertSingleRecoveryNudge(t, fixture.adapter)
}

func TestDelegateResourceSupervision_CompletionGateRecoversEveryCleanExit(t *testing.T) {
	t.Run("no-tool response cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		bare := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response")} }
		fixture.adapter.steps = []func(llm.Request) llm.Response{bare, bare, bare, bare,
			func(llm.Request) llm.Response { return finalResponse("recovered") }}
		root := restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("tool-bearing observer handoff cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		var root *Session
		fixture.adapter.steps = []func(llm.Request) llm.Response{
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
				return communicateResponse(false, "handoff")
			},
			func(llm.Request) llm.Response { return finalResponse("recovered") },
		}
		root = restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("notification yield cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		var root *Session
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				sub.sess.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: "watch-test", Status: jobNotificationEventWatch})
				return communicateResponse(false, "work before notification")
			},
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("notification acknowledged")}
			},
			func(llm.Request) llm.Response { return finalResponse("recovered") },
		}
		root = restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("goal-controlled cap cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.Config.MaxToolRoundsPerInput = goal.GoalTurnMaxRounds
		})
		enteredFinalBare := make(chan struct{})
		releaseFinalBare := make(chan struct{})
		bare := func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("bare before continuation")}
		}
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			bare, bare, bare,
			func(llm.Request) llm.Response {
				close(enteredFinalBare)
				<-releaseFinalBare
				return bare(llm.Request{})
			},
		}
		for range goal.GoalTurnMaxRounds {
			fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response {
				return communicateResponse(false, "goal partial")
			})
		}
		fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response { return finalResponse("recovered") })
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
		fixture := newColdStableDelegateFixture(t, "")
		bare := func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("hook continuation without report")}
		}
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("warm result") },
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			bare, bare, bare, bare,
			func(llm.Request) llm.Response { return finalResponse("recovered after hook") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmStableSupervisionDelegate(t, root, fixture)
		sub.sess.hookRunner = stableSupervisionStopHook(`{"decision":"block","reason":"address hook feedback"}`)
		armStableSupervisionAttention(t, sub, "attention:blocked-hook", "run blocked hook")
		waitForStableSupervisionRun(t, root, fixture.childID)
		assertSingleRecoveryNudge(t, fixture.adapter)
	})

	t.Run("unblocked hook model context cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("warm result") },
			func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("attention no action")} },
			func(llm.Request) llm.Response { return finalResponse("recovered after context") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmStableSupervisionDelegate(t, root, fixture)
		sub.sess.hookRunner = stableSupervisionStopHook(`{"hookSpecificOutput":{"additionalContext":"hook model context"}}`)
		armStableSupervisionAttention(t, sub, "attention:hook-context", "run context hook")
		waitForStableSupervisionRun(t, root, fixture.childID)
		assertSingleRecoveryNudge(t, fixture.adapter)
		requests := fixture.adapter.Requests()
		if !requestMessagesContainText(requests[len(requests)-1].Messages, "hook model context") {
			t.Fatalf("recovery request omitted unblocked hook context: %#v", requests[len(requests)-1].Messages)
		}
	})

	t.Run("post-drain owner steering cannot return cleanly without one bounded recovery nudge", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		var root *Session
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				sub := root.subagents.get(fixture.childID)
				sub.sess.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, JobID: "post-drain", Status: jobNotificationEventWatch})
				return communicateResponse(false, "handoff")
			},
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("post-drain notification acknowledged")}
			},
			func(llm.Request) llm.Response { return finalResponse("recovered") },
			func(llm.Request) llm.Response { return finalResponse("steering handled") },
		}
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
		abortUnpersistedStableDelegateOutcome(t, outcome)
		assertSingleRecoveryNudge(t, fixture.adapter)
		requests := fixture.adapter.Requests()
		if !requestMessagesContainText(requests[len(requests)-1].Messages, "post-drain steering") {
			t.Fatalf("post-drain continuation omitted owner steering: %#v", requests[len(requests)-1].Messages)
		}
	})
}

func TestDelegateResourceSupervision_AutoNudgeOccursOnceForEligibleBuiltin(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return finalResponse("recovered after the one nudge") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
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
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				close(entered)
				<-release
				return finalResponse("first result")
			},
			func(llm.Request) llm.Response { return finalResponse("continued after steer") },
		}
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
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
			close(entered)
			<-release
			return finalResponse("cancelled result")
		}}
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
		fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.Config.MaxToolRoundsPerInput = 1
		})
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateResponse(false, "continue") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "exhaust", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
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
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				close(entered)
				<-release
				return llm.Response{}, fatalErr
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("unexpected continuation after fatal failure"), nil
			},
		},
	}
	fixture := newColdStableDelegateFixture(t, "")
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
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.MaxToolRoundsPerInput = 1
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return communicateResponse(false, "exhaust this activation")
		},
		func(llm.Request) llm.Response {
			return finalResponse("unexpected continuation after exhaustion")
		},
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return finalResponse("cancelled result")
	}}
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response {
			close(enteredFinalEmpty)
			<-releaseFinalEmpty
			return agenttest.EmptyResponse()
		},
		func(llm.Request) llm.Response { return finalResponse("continued after steering") },
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
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
			return finalResponse("continued after late nudge steering")
		},
	}
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return finalResponse("result before late cancellation")
		},
		func(llm.Request) llm.Response {
			return finalResponse("unexpected continuation after late cancellation")
		},
	}
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
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
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
		},
	}
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Isolation = "worktree"
		descriptor.WorkingDir = lane
	})
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
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || report.Dirty {
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
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || !report.Dirty {
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
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
			close(entered)
			<-release
			return llm.Response{}, fatalErr
		}},
	}
	fixture := newColdStableDelegateFixture(t, "")
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
	clock := agenttest.NewFakeClock()
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal close-abandon turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
			close(entered)
			<-release
			return llm.Response{}, fatalErr
		}},
	}
	fixture := newColdStableDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	finalStatePublished := make(chan struct{})
	root := restoreSupervisionRoot(t, fixture, clock)
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
	jm.closeGrace = time.Second
	var closeErr error
	var closeOnce sync.Once
	jm.stopReceiptsAfterCapture = func() {
		closeOnce.Do(func() {
			blockedBeforeClose := clock.BlockedCount()
			closeResult := make(chan error, 1)
			go func() {
				closeResult <- jm.closeRuntimeState()
			}()
			clock.BlockUntil(blockedBeforeClose + 1)
			clock.Advance(time.Second)
			closeErr = <-closeResult
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
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
	}
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
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Isolation = "worktree"
		descriptor.WorkingDir = lane
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
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
	}
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
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || report.Dirty {
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
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || !report.Dirty {
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
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return finalResponse("would normally run the hook")
	}}
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
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		// This fixture's SubagentStop hook persistently writes the marker.
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	enteredHookContinuation := make(chan struct{})
	releaseHookContinuation := make(chan struct{})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("initial result") },
		func(llm.Request) llm.Response {
			close(enteredHookContinuation)
			<-releaseHookContinuation
			return finalResponse("hook continuation result")
		},
		func(llm.Request) llm.Response { return finalResponse("steering continuation result") },
		func(llm.Request) llm.Response { return finalResponse("unexpected second hook continuation") },
	}
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
	child := newTestSession(t)
	child.totalRounds = 1
	if err := child.persistSalvagedTurn("partial final draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist final salvage: %v", err)
	}
	sub := &subagent{sess: child}
	packet := sub.stableDelegateFinish("", errors.New("provider failed after partial stream")).packet
	if packet == nil || !containsDelegateSalvageWarning(packet.Warnings) {
		t.Fatalf("failed final-round warnings = %#v", packetWarnings(packet))
	}
}

func TestDelegateResourceSupervision_SuccessExhaustionCancellationStopAndStaleSalvageAddNoHint(t *testing.T) {
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
			packet := tc.sub.stableDelegateFinish("done", tc.err).packet
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
	child.attachTranscript(childWriter)
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
	owner.attachTranscript(ownerWriter)
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
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return finalResponse("ordinary result before covered stop")
	}}
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
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		// The hook appends to a persistent marker, so this is intentionally a
		// mutating fixture rather than a read-only role.
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	continuationSawHook := false
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("initial result") },
	}
	if blocking {
		fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response {
			_, err := os.Stat(marker)
			continuationSawHook = err == nil
			return finalResponse("continued after hook")
		})
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
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

func restoreSupervisionRoot(t *testing.T, fixture coldStableDelegateFixture, clock *agenttest.FakeClock) *Session {
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
func waitForStableSupervisionRun(t *testing.T, root *Session, childID string) {
	t.Helper()
	sub := root.subagents.get(childID)
	if sub == nil {
		t.Fatalf("stable child %q was not tracked", childID)
	}
	sub.mu.Lock()
	hasChannel := sub.done != nil
	sub.mu.Unlock()
	if !hasChannel {
		t.Fatalf("stable child %q has no completion channel", childID)
	}
	desc := fmt.Sprintf("stable child %q supervision to quiesce", childID)
	// TRIPWIRE: every supervision run in these tests is served by a scripted
	// in-process adapter, so quiescence is reached in milliseconds; this bound
	// only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, desc, func() bool {
		sub.mu.Lock()
		sess := sub.sess
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
			if sess.hasPendingDelegateAttentionArmRetry() {
				return false
			}
			if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
				return false
			}
			if controller, delegateID := sess.delegateController, sess.owningDelegateID; controller != nil && delegateID != "" {
				if controller.reservedAttentionID(sess) != "" {
					return false
				}
				controller.mu.Lock()
				aggregate := controller.durable[delegateID]
				runOpen := aggregate != nil && aggregate.CurrentRunOpen
				controller.mu.Unlock()
				if runOpen {
					return false
				}
			}
		}
		sub.mu.Lock()
		done := sub.done
		live := sub.running || sub.driving || sub.finalizing
		sub.mu.Unlock()
		if live || done == nil {
			return false
		}
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

// TestWaitForStableSupervisionRunOutlastsDeferredAttentionDrive pins the
// contract of waitForStableSupervisionRun: it must not return while an armed
// delegate attention still owes a run. Arming attention only notifies, and
// driveStableDelegateAttention refuses the resulting drive whenever the child
// is not drivable yet (mid-run, mid-finalization, or dispose gated) or the
// controller is still busy with the previous generation. The dispose gate
// stands in for those refusals here because it is the one a test can hold open
// deterministically. Without the wait the helper joins the WARM run's already
// closed channel and the caller reads that generation's reported delivery
// instead of the attention generation's private no-action finish.
func TestWaitForStableSupervisionRunOutlastsDeferredAttentionDrive(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
	}
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

// warmStableSupervisionDelegate starts the warm run and acknowledges its result.
// It does NOT wait for that run to quiesce: the run's finalizer outlives the
// acknowledged result, because `running` goes false at the top of the finalize
// block and `finalizing` clears only at the end, and a send landing in that
// window is refused as target busy by design. A caller that needs a drivable
// child must wait for quiescence first; the callers that arm attention
// deliberately do not, so that the drive they exercise is the deferred one.
func warmStableSupervisionDelegate(t *testing.T, root *Session, fixture coldStableDelegateFixture) *subagent {
	t.Helper()
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "warm retained runtime", 60_000)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("warm stable delegate = %#v", outcome)
	}
	plans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge warm stable result: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute warm delivery acknowledgement: %v", err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("warm stable delegate retained no child session %q", fixture.childID)
	}
	return sub
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
	nudge := communicateNudge("communicate")
	last := requests[len(requests)-1]
	if got := countMessageText(last.Messages, nudge); got != 1 {
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
