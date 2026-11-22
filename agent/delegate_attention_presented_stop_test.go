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

	t.Run("with earlier held report returns root to idle", func(t *testing.T) {
		t.Parallel()
		fixture := newColdDelegateFixture(t, "")
		firstParked := make(chan struct{})
		releaseFirst := make(chan struct{})
		var releaseFirstOnce sync.Once
		t.Cleanup(func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) })

		secondParked := make(chan struct{})
		releaseSecond := make(chan struct{})
		var releaseSecondOnce sync.Once
		t.Cleanup(func() { releaseSecondOnce.Do(func() { close(releaseSecond) }) })

		var requestsMu sync.Mutex
		var secondParkedRequest []llm.Message
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			// The warm send's own answer.
			func(llm.Request) llm.Response { return agenttest.FinalResponse("warm retained runtime") },
			// First attention run (heldID): parked before its first request
			// settles so the user stop holds heldID unpresented.
			func(llm.Request) llm.Response {
				close(firstParked)
				<-releaseFirst
				return agenttest.FinalResponse("cancelled before presentation")
			},
			// Second attention run (presentedID), request 1: presents
			// presentedID and issues a mid-turn tool call so the latch moves
			// to attentionPresentedTrigger.
			func(llm.Request) llm.Response {
				return agenttest.CommunicateResponse(false, "checking second report")
			},
			// Second attention run (presentedID), request 2: parked until
			// stopped in the later round.
			func(req llm.Request) llm.Response {
				requestsMu.Lock()
				secondParkedRequest = append([]llm.Message{}, req.Messages...)
				requestsMu.Unlock()
				close(secondParked)
				<-releaseSecond
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

		const heldID = "delegate:dlg_afterpresented/watch/held-1"
		const heldText = "earlier report stopped before first request settled"
		const presentedID = "delegate:dlg_afterpresented/watch/presented-2"
		const presentedText = "successor report stopped after presentation"

		// Arm the first report and wait for its attention run to park before
		// its first request settles, then arm the second report while the first
		// run is still in flight.
		armStableSupervisionAttention(t, sub, heldID, heldText)
		testwait.Until(t, "the first attention run to park before its first request settles", func() bool {
			root.drivePendingDelegateAttention()
			select {
			case <-firstParked:
				return true
			default:
				return false
			}
		})
		// Confirm the first run is unpresented (still latched) before stopping it.
		sub.sess.attentionMu.Lock()
		firstLatched := sub.sess.attentionAcceptedID
		sub.sess.attentionMu.Unlock()
		if firstLatched != heldID {
			t.Fatalf("first run latched trigger = %q, want %q", firstLatched, heldID)
		}

		// Arm the second child report while the first run is still active.
		armStableSupervisionAttention(t, sub, presentedID, presentedText)

		// Stop the first run before its first request settles: heldID stays
		// pending and held, while presentedID remains armed for its successor.
		if _, err := root.StopDelegateRun(fixture.delegateID); err != nil {
			t.Fatalf("StopDelegateRun (first): %v", err)
		}
		sub.mu.Lock()
		firstDone := sub.done
		sub.mu.Unlock()
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		select {
		case <-firstDone:
		// TRIPWIRE: the cancelled run settles within seconds of the release; the
		// bound only fires on a genuine hang.
		case <-time.After(60 * time.Second):
			t.Fatal("the first user-stopped run never completed its settle tail")
		}
		testwait.Until(t, "the first stopped generation to release finalization", func() bool {
			sub.mu.Lock()
			defer sub.mu.Unlock()
			return !sub.running && !sub.driving && !sub.finalizing
		})

		// Drive the already-armed second report; wait for its second request
		// to park after the first request presented presentedID.
		testwait.Until(t, "the successor attention run's second request to park", func() bool {
			root.drivePendingDelegateAttention()
			select {
			case <-secondParked:
				return true
			default:
				return false
			}
		})
		requestsMu.Lock()
		carriedSecond := requestMessagesContainText(secondParkedRequest, presentedText)
		carriedHeld := requestMessagesContainText(secondParkedRequest, heldText)
		requestsMu.Unlock()
		if !carriedSecond {
			t.Fatal("the successor's parked request did not carry the second armed row")
		}
		if carriedHeld {
			t.Fatal("the successor's parked request carried the held earlier row")
		}
		generations := attentionRunGenerations(t, root.delegateController, fixture.delegateID)
		secondGen := generations[presentedID]
		if secondGen == 0 {
			t.Fatal("the journal does not name the generation started for the second row")
		}

		// Stop the successor after presentation, while its later round runs.
		if _, err := root.StopDelegateRun(fixture.delegateID); err != nil {
			t.Fatalf("StopDelegateRun (second): %v", err)
		}
		sub.mu.Lock()
		secondDone := sub.done
		sub.mu.Unlock()
		releaseSecondOnce.Do(func() { close(releaseSecond) })
		select {
		case <-secondDone:
		// TRIPWIRE: the cancelled run settles within seconds of the release; the
		// bound only fires on a genuine hang.
		case <-time.After(60 * time.Second):
			t.Fatal("the second user-stopped run never completed its settle tail")
		}
		testwait.Until(t, "the second stopped generation to release finalization", func() bool {
			sub.mu.Lock()
			defer sub.mu.Unlock()
			return !sub.running && !sub.driving && !sub.finalizing
		})

		// Drive once more to mimic the root's post-stop wake loop, then let
		// the root consume its own stopped-child notifications so only the
		// child's delegate attention state can keep the root active.
		root.drivePendingDelegateAttention()
		fixture.adapter.appendSteps(func(llm.Request) llm.Response {
			return agenttest.FinalResponse("root acknowledged stopped child")
		})
		if _, err := root.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
			t.Fatalf("root ProcessInputKind(EntryNotification): %v", err)
		}

		// The presented successor row is consumed, while the earlier row stays
		// pending in the transcript and held from automatic drives.
		fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
		if err != nil {
			t.Fatalf("read delegate attention fold: %v", err)
		}
		if got := fold.resolutions[presentedID]; got != delegateAttentionConsumed {
			t.Fatalf("presented row resolution = %q, want consumed (pending=%v)", got, fold.pendingIDs())
		}
		if got := fold.resolutions[heldID]; got != "" {
			t.Fatalf("earlier held row resolution = %q, want still pending", got)
		}
		sub.sess.attentionMu.Lock()
		stillHeld := sub.sess.delegateAttentionHeldFromAutomaticDriveLocked(heldID)
		sub.sess.attentionMu.Unlock()
		if !stillHeld {
			t.Fatal("earlier held row lost its automatic-drive hold after successor stop")
		}

		// The stale commit-time wake for presentedID must be gone, so the
		// controller no longer retries and the root returns to idle.
		if wakes := attentionWakeIDsOf(root.delegateController, fixture.delegateID); len(wakes) != 0 {
			t.Fatalf("stale attention wakes remained after presented stop with earlier held row: %v", wakes)
		}
		if root.delegateController.hasPendingDelegateAttention() {
			t.Fatal("controller still reports pending delegate attention after presented stop with earlier held row")
		}
		if got := root.WireState(); got != string(SessionIdle) {
			t.Fatalf("root WireState = %q, want %q", got, SessionIdle)
		}
		if got := attentionRunGenerations(t, root.delegateController, fixture.delegateID)[presentedID]; got != secondGen {
			t.Fatalf("presented row ran again on generation %d, want only %d", got, secondGen)
		}

		// Verify consumePresentedDelegateTrigger removes only its own trigger
		// wake and preserves any unrelated wake armed on the same delegate.
		const unrelatedID = "delegate:dlg_afterpresented/watch/unrelated-3"
		const extraPresentedID = "delegate:dlg_afterpresented/watch/presented-4"
		if _, err := sub.sess.appendDelegateNotificationDurably(extraPresentedID, "extra presented row"); err != nil {
			t.Fatalf("append extra presented row: %v", err)
		}
		root.delegateController.noteDelegateAttention(fixture.delegateID, extraPresentedID)
		root.delegateController.noteDelegateAttention(fixture.delegateID, unrelatedID)
		sub.sess.attentionMu.Lock()
		sub.sess.attentionPresentedTrigger = extraPresentedID
		sub.sess.attentionMu.Unlock()
		if consumed, err := sub.sess.consumePresentedDelegateTrigger(); err != nil || consumed != extraPresentedID {
			t.Fatalf("consumePresentedDelegateTrigger = %q, %v; want %q, nil", consumed, err, extraPresentedID)
		}
		remainingWakes := attentionWakeIDsOf(root.delegateController, fixture.delegateID)
		if _, stale := remainingWakes[extraPresentedID]; stale {
			t.Fatalf("consumed trigger wake %q was not removed: %v", extraPresentedID, remainingWakes)
		}
		if _, kept := remainingWakes[unrelatedID]; !kept {
			t.Fatalf("unrelated wake %q was dropped when consuming %q: %v", unrelatedID, extraPresentedID, remainingWakes)
		}
	})
}

// An automatic attention generation that presents its trigger in a settled
// request and then hits its tool-round limit must consume that trigger before
// finalization releases: leaving it pending with its commit-time wake armed
// immediately starts a successor generation with a fresh tool-round budget.
func TestDelegateAttentionExhaustedAfterPresentedRequestConsumesTheTrigger(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		// The warm send's own answer.
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm retained runtime") },
		// The attention run's first request presents the row and issues a
		// mid-turn tool call, exhausting MaxToolRoundsPerInput=1.
		func(llm.Request) llm.Response {
			return agenttest.CommunicateResponse(false, "still working")
		},
		// If a duplicate successor starts on the same trigger, it would consume
		// this extra step and advance the journal generation.
		func(llm.Request) llm.Response { return agenttest.FinalResponse("unexpected successor") },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmDelegateUnservedRoot(t, root, fixture)
	testwait.Until(t, "the warm generation to release its finalization fence", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return !sub.running && !sub.driving && !sub.finalizing
	})
	sub.sess.mu.Lock()
	sub.sess.cfg.MaxToolRoundsPerInput = 1
	sub.sess.mu.Unlock()

	const rowID = "delegate:dlg_exhaustedpresented/watch/1"
	const rowText = "exhausted-presented: check the release gate"
	armStableSupervisionAttention(t, sub, rowID, rowText)
	testwait.Until(t, "the exhausted attention run to start and settle", func() bool {
		root.drivePendingDelegateAttention()
		sub.mu.Lock()
		settled := !sub.running && !sub.driving && !sub.finalizing
		sub.mu.Unlock()
		if !settled {
			return false
		}
		return attentionRunGenerations(t, root.delegateController, fixture.delegateID)[rowID] != 0
	})
	exhaustedGen := attentionRunGenerations(t, root.delegateController, fixture.delegateID)[rowID]

	// A duplicate successor would have fired step 3 (making 3 requests and
	// flipping the delegate status from exhausted to completed).
	if got := len(fixture.adapter.Requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2 (warm + single exhausted attention run): a duplicate successor ran after exhaustion", got)
	}
	if agg := settledDelegateAggregate(t, root, fixture.delegateID); agg.LatestOutcome == nil || string(agg.LatestOutcome.Status) != string(SubagentExhausted) {
		t.Fatalf("delegate latest outcome = %+v, want status %q", agg.LatestOutcome, SubagentExhausted)
	}

	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
	if err != nil {
		t.Fatalf("read the delegate's attention fold after exhaustion: %v", err)
	}
	if got := fold.resolutions[rowID]; got != delegateAttentionConsumed {
		t.Fatalf("the exhausted generation's presented trigger resolution = %q, want consumed (pending=%v)", got, fold.pendingIDs())
	}
	if pending, err := sub.sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending attention after exhausted presented trigger = %v (%v), want none", pending, err)
	}
	root.drivePendingDelegateAttention()
	if got := attentionRunGenerations(t, root.delegateController, fixture.delegateID)[rowID]; got != exhaustedGen {
		t.Fatalf("the already-presented trigger ran again on generation %d after exhaustion, want only %d", got, exhaustedGen)
	}
}

// An automatic attention generation whose first settled request presented the
// trigger and whose later round fails on a provider error must still consume
// that trigger and forget its commit-time wake so the root does not stay active
// and keep retrying a fatal-gated delegate. A provider failure before the first
// request settles keeps the unpresented trigger pending.
func TestDelegateAttentionProviderFailureAfterPresentedRequestConsumesTheTrigger(t *testing.T) {
	t.Parallel()
	t.Run("mid-round provider failure consumes presented trigger and clears wake", func(t *testing.T) {
		t.Parallel()
		fixture := newColdDelegateFixture(t, "")
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			// Request 0: warm send.
			func(llm.Request) llm.Response { return agenttest.FinalResponse("warm retained runtime") },
			// Request 1: attention run's first settled request presents the
			// trigger and returns a mid-turn tool call, moving the latch to
			// attentionPresentedTrigger.
			func(llm.Request) llm.Response {
				return agenttest.CommunicateResponse(false, "processing report")
			},
			// Request 2 fails inside failingRequestAdapter below.
			// Request 3 (root turn consuming the child's failure report).
			func(llm.Request) llm.Response { return agenttest.FinalResponse("root noted child failure") },
		})
		fixture.client.Register(&failingRequestAdapter{
			fakeAdapter: fixture.adapter,
			failAt:      2,
			err:         llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil),
		})
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmDelegateUnservedRoot(t, root, fixture)
		testwait.Until(t, "the warm generation to release its finalization fence", func() bool {
			sub.mu.Lock()
			defer sub.mu.Unlock()
			return !sub.running && !sub.driving && !sub.finalizing
		})

		const rowID = "delegate:dlg_providerfail/watch/1"
		const rowText = "provider-fail-after-present: check the build"
		armStableSupervisionAttention(t, sub, rowID, rowText)
		testwait.Until(t, "the failed attention run to settle with fatal gate", func() bool {
			root.drivePendingDelegateAttention()
			sub.mu.Lock()
			settled := sub.fatalRunGated && !sub.running && !sub.driving && !sub.finalizing
			sub.mu.Unlock()
			return settled
		})

		// Let the root consume its own update and failed-child notifications so
		// only a stale delegate attention wake can keep the root active.
		for root.hasPendingDelegateDeliveries() || root.pendingRootDelegateAttention() || root.peekNotifications() > 0 {
			fixture.adapter.appendSteps(func(llm.Request) llm.Response {
				return agenttest.FinalResponse("root noted child event")
			})
			if _, err := root.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
				t.Fatalf("root ProcessInputKind(EntryNotification): %v", err)
			}
		}

		fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
		if err != nil {
			t.Fatalf("read delegate attention fold: %v", err)
		}
		if got := fold.resolutions[rowID]; got != delegateAttentionConsumed {
			t.Fatalf("presented trigger resolution after mid-round provider failure = %q, want consumed (pending=%v)", got, fold.pendingIDs())
		}
		if wakes := attentionWakeIDsOf(root.delegateController, fixture.delegateID); len(wakes) != 0 {
			t.Fatalf("stale attention wakes remained after mid-round provider failure: %v", wakes)
		}
		if root.delegateController.hasPendingDelegateAttention() {
			t.Fatal("controller still reports pending delegate attention after mid-round provider failure")
		}
		if got := root.WireState(); got != string(SessionIdle) {
			t.Fatalf("root WireState = %q, want %q", got, SessionIdle)
		}
	})

	t.Run("first-request provider failure keeps unpresented trigger pending", func(t *testing.T) {
		t.Parallel()
		fixture := newColdDelegateFixture(t, "")
		fixture.adapter.setSteps([]func(llm.Request) llm.Response{
			// Request 0: warm send.
			func(llm.Request) llm.Response { return agenttest.FinalResponse("warm retained runtime") },
		})
		// Request 1 is the attention run's first request, which fails before
		// presenting the trigger.
		fixture.client.Register(&failingRequestAdapter{
			fakeAdapter: fixture.adapter,
			failAt:      1,
			err:         llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil),
		})
		root := restoreSupervisionRoot(t, fixture, nil)
		sub := warmDelegateUnservedRoot(t, root, fixture)
		testwait.Until(t, "the warm generation to release its finalization fence", func() bool {
			sub.mu.Lock()
			defer sub.mu.Unlock()
			return !sub.running && !sub.driving && !sub.finalizing
		})

		const rowID = "delegate:dlg_providerfail/watch/unpresented-1"
		armStableSupervisionAttention(t, sub, rowID, "unpresented first-request failure")
		testwait.Until(t, "the unpresented failed attention run to settle with fatal gate", func() bool {
			root.drivePendingDelegateAttention()
			sub.mu.Lock()
			settled := sub.fatalRunGated && !sub.running && !sub.driving && !sub.finalizing
			sub.mu.Unlock()
			return settled
		})

		fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
		if err != nil {
			t.Fatalf("read delegate attention fold: %v", err)
		}
		if got := fold.resolutions[rowID]; got != "" {
			t.Fatalf("unpresented trigger resolution after first-request failure = %q, want still pending", got)
		}
	})
}
