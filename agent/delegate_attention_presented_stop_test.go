package agent

import (
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/llm"
)

// A user's stop that lands after an attention generation's first model
// request settled — one that presented the trigger — must still consume that
// trigger. The presentation clears the trigger latch, so the settle tail used
// to find nothing to settle and leave the already-presented row pending with
// its commit-time wake armed; releasing finalization then drove a duplicate
// successor to present the same report again. #5681.
func TestDelegateAttentionStopAfterPresentedRequestConsumesTheTrigger(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	parked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var requestsMu sync.Mutex
	var parkedRequest []llm.Message
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		// The warm send's own answer.
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm retained runtime") },
		// The attention run's first request: a settled round presenting the
		// armed row that answers with a mid-turn tool call, so the trigger
		// latch clears before the turn can consume the row.
		func(llm.Request) llm.Response {
			return agenttest.CommunicateResponse(false, "checking the lane")
		},
		// The attention run's second request: parked until the test stops the
		// run, then settled through the cancellation.
		func(req llm.Request) llm.Response {
			requestsMu.Lock()
			parkedRequest = append([]llm.Message{}, req.Messages...)
			requestsMu.Unlock()
			close(parked)
			<-release
			return agenttest.FinalResponse("done")
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	testwait.Until(t, "the warm generation to release its finalization fence", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return !sub.running && !sub.driving && !sub.finalizing
	})

	const rowID = "delegate:dlg_afterpresented/watch/1"
	const rowText = "after-presented: which cursor holds the merge base?"
	armStableSupervisionAttention(t, sub, rowID, rowText)
	testwait.Until(t, "the attention run's second request to park", func() bool {
		root.drivePendingDelegateAttention()
		select {
		case <-parked:
			return true
		default:
			return false
		}
	})

	// The scenario's shape, read while the run is parked: the presented row
	// cleared the latch, and the parked request carried the report.
	sub.sess.attentionMu.Lock()
	latched := sub.sess.attentionAcceptedID
	sub.sess.attentionMu.Unlock()
	if latched != "" {
		t.Fatalf("the parked run still latches its trigger %q, so this is the unpresented shape, not the presented-stop case", latched)
	}
	requestsMu.Lock()
	carried := requestMessagesContainText(parkedRequest, rowText)
	requestsMu.Unlock()
	if !carried {
		t.Fatal("the parked request did not carry the armed row")
	}
	generations := attentionRunGenerations(t, root.delegateController, fixture.delegateID)
	stoppedGeneration := generations[rowID]
	if stoppedGeneration == 0 {
		t.Fatal("the journal does not name the generation started for the armed row")
	}

	if _, err := root.StopDelegateRun(fixture.delegateID); err != nil {
		t.Fatalf("StopDelegateRun: %v", err)
	}
	sub.mu.Lock()
	runDone := sub.done
	sub.mu.Unlock()
	releaseOnce.Do(func() { close(release) })
	select {
	case <-runDone:
	// TRIPWIRE: the cancelled run settles within seconds of the release; the
	// bound only fires on a genuine hang.
	case <-time.After(60 * time.Second):
		t.Fatal("the user-stopped run never completed its settle tail")
	}

	// The report was presented, so the stop owes its consumption: leaving it
	// pending re-arms the successor that presents it a second time.
	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
	if err != nil {
		t.Fatalf("read the delegate's attention fold after the stop: %v", err)
	}
	if got := fold.resolutions[rowID]; got != delegateAttentionConsumed {
		t.Fatalf("the stopped generation's presented trigger resolution = %q, want consumed (pending=%v)", got, fold.pendingIDs())
	}
	if pending, err := sub.sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending attention after the stopped presented trigger = %v (%v), want none", pending, err)
	}
	// A drive pass now finds nothing pending, so releasing finalization into
	// the daemon's wake loop cannot launch a duplicate successor.
	root.drivePendingDelegateAttention()
	if got := attentionRunGenerations(t, root.delegateController, fixture.delegateID)[rowID]; got != stoppedGeneration {
		t.Fatalf("the already-presented trigger ran again on generation %d after the stop, want only the stopped generation %d", got, stoppedGeneration)
	}
}
