package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// TestDelegateSendParkedDelegateSteerIsConsumed pins #2796. A delegate whose
// turn has ended while its run is parked in the owned-job drain reports
// Action "steered"; before the fix that admission sat unread until the owned
// job ended, so the caller's success was a silent no-op. The drain now stops
// parking when a steer is owed, letting the run take its steering continuation
// and bind the admission into a model request. The owned shell is deliberately
// NOT released until the assertion, so the message must be acted on while the
// delegate is still parked — the exact case the field reported.
func TestDelegateSendParkedDelegateSteerIsConsumed(t *testing.T) {
	t.Parallel()
	fixture := newOwnedJobDrainFixture(t)
	// The fixture's construction already consumed its first two scripted steps
	// (the owned shell call and the interim result). Answer by request content so
	// the assertion does not depend on the exact turn count: every turn here
	// reports a result; the shell-completion turn reports its own.
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		respond2796, respond2796, respond2796, respond2796, respond2796, respond2796,
	}

	fixture.child.mu.Lock()
	finalizing := fixture.child.finalizing
	running := fixture.child.running
	fixture.child.mu.Unlock()
	if !running || !finalizing {
		t.Fatalf("fixture child state = running %v finalizing %v, want a parked finalization drain", running, finalizing)
	}

	outcome := (delegateRuntime{owner: fixture.parent}).send(context.Background(), fixture.result.DelegateID, "follow-up while parked", 0)
	if outcome.result.Err != nil {
		t.Fatalf("send to parked delegate: %v", outcome.result.Err)
	}
	if outcome.result.Action != "steered" {
		t.Fatalf("send action = %q, want steered (the generation is live)", outcome.result.Action)
	}

	// The owned shell is still held: only the steering itself can drive the run.
	// TRIPWIRE: every step here is scripted and answered in-process; the steer
	// is consumed within a drain re-check (250ms), so 5s only fires on a hang.
	waitForCondition(t, 5*time.Second, "parked delegate consumes the steered message", func() bool {
		return requestsContain(fixture.adapter.Requests(), "follow-up while parked")
	})

	// Release the owned shell so the run settles and the fixture tears down
	// cleanly; the steer was already consumed above.
	fixture.env.releaseJob()
	select {
	case <-fixture.runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job.
		t.Fatalf("delegate did not settle after its owned shell exited (requests=%d)", len(fixture.adapter.Requests()))
	}
}

// respond2796 answers any finalization turn with a reported result; it lets the
// parked-delegate test key assertions on the request contents rather than on the
// exact number of turns.
func respond2796(req llm.Request) llm.Response {
	if requestContainsText(req, "child shell complete") {
		return finalResponse("owned shell handled")
	}
	return finalResponse("turn handled")
}

// TestDelegateSendRunningDelegateSteerStaysSteered pins the other half of
// #2796: steering a delegate with a live consuming turn must keep working and
// must still report "steered" rather than starting a successor generation. The
// run is blocked inside its first model call when the steer lands, so the
// admission is consumed by the turn's next model request.
func TestDelegateSendRunningDelegateSteerStaysSteered(t *testing.T) {
	t.Parallel()
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
		t.Fatalf("start delegate: %v", started.result.Err)
	}
	if started.result.Action != "started" {
		t.Fatalf("start action = %q, want started", started.result.Action)
	}
	<-entered

	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "new steering", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer running delegate = %+v, want steered", steered.result)
	}
	if generation := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID).Generation; generation != 1 {
		t.Fatalf("steer started a successor generation: got %d, want 1", generation)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)

	requests := fixture.adapter.Requests()
	if !requestsContain(requests, "new steering") {
		t.Fatalf("running delegate never consumed the steered message; requests = %d", len(requests))
	}
}
