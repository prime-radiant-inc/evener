package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/test/e2e/fakellm"
)

// TestE2E_ControlInvariantAcrossATurnBoundary is Task 1 of
// docs/superpowers/plans/2026-08-16-stop-always-works.md: the measurement that
// decides whether that plan has a premise at all.
//
// The invariant under test is the one the composer actually gates on
// (submitRouting.ts's isTurnActive: statusType === "active" && !!activeTurnId):
// whenever the wire says a turn is running it must publish an id, and that id
// must be one the daemon's mutation preconditions still accept. If either half
// fails, Stop and Steer either vanish mid-work or are rejected in silence.
//
// The moment under suspicion is the boundary between two turns of one drain.
// completeClientMutationTurnWithState clears the durable ActiveTurnID from the
// drain loop (agent/session_lifecycle.go, right after processOneInput returns)
// while `processing` is still true, and s.appActiveTurnID keeps naming the
// finished turn until the next event reaches the bridge. This test samples
// thread/read as hard as it can across that boundary and reports what a client
// could actually observe.
func TestE2E_ControlInvariantAcrossATurnBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const (
		openingPrompt = "SERF-E2E-BOUNDARY-OPENING"
		queuedText    = "SERF-E2E-BOUNDARY-QUEUED"
	)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "serf",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Serf.Ref
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the first model request: %v", err)
	}
	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")

	// Queue a message so the drain loop runs a SECOND turn straight after the
	// first ends. That back-to-back transition is the boundary under test;
	// without a queued message the session simply settles idle.
	if _, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		ExpectedTurnID:   firstTurn,
		Input:            []appwire.InputItem{{Type: "text", Text: queuedText}},
	}); err != nil {
		t.Fatalf("turn/queue: %v", err)
	}
	awaitThread(ctx, t, client, ref, "queue depth 1", func(thread appwire.Thread) bool {
		return thread.Serf.Queue.Depth == 1
	})

	// Sample thread/read continuously from just before the boundary until the
	// second turn's model request lands, so the window cannot be stepped over.
	type sample struct {
		status string
		turnID string
	}
	samples := make(chan sample, 8192)
	sampleCtx, stopSampling := context.WithCancel(ctx)
	sampling := make(chan struct{})
	go func() {
		defer close(sampling)
		for sampleCtx.Err() == nil {
			read, err := clientRequest[appwire.ThreadReadResponse](sampleCtx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				continue
			}
			select {
			case samples <- sample{status: string(read.Thread.Status.Type), turnID: read.Thread.Serf.ActiveTurnID}:
			default:
			}
		}
	}()

	// End turn 1. The drain loop then completes its mutation, pops the queued
	// message and opens turn 2 -- the boundary.
	round1.RespondToolCall("communicate", communicateArgs("first turn done"))

	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the queued message's turn: %v", err)
	}
	if !round2.Contains(queuedText) {
		t.Fatalf("the queued message never ran; messages:\n%s", strings.Join(round2.Texts(), "\n"))
	}
	stopSampling()
	<-sampling
	close(samples)

	var activeWithNoID, activeWithStale, total int
	seen := map[string]int{}
	for s := range samples {
		total++
		seen[s.status+"/"+s.turnID]++
		if s.status != string(appwire.ThreadStatusActive) {
			continue
		}
		if s.turnID == "" {
			activeWithNoID++
		}
		if s.turnID == firstTurn {
			activeWithStale++
		}
	}
	for k, n := range seen {
		t.Logf("observed %4d x %s", n, k)
	}
	t.Logf("samples=%d activeWithNoID=%d activeWithStaleFirstTurn=%d", total, activeWithNoID, activeWithStale)

	if total == 0 {
		t.Fatal("no samples taken; the test proves nothing")
	}
	if activeWithNoID > 0 {
		t.Fatalf("the wire published status=active with no turn id in %d of %d samples: the composer hides Stop and Steer for a session that is working", activeWithNoID, total)
	}

	// The half that actually decides the plan. Turn 2 is demonstrably running --
	// its model request is in hand -- so read the thread exactly as a client
	// would and aim a Stop at whatever id it publishes, WITHOUT waiting for that
	// id to catch up. If the wire is handing out an id the preconditions no
	// longer accept, this is where a user's Stop dies.
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("thread/read at the boundary: %v", err)
	}
	published := read.Thread.Serf.ActiveTurnID
	t.Logf("at the boundary the wire published status=%s activeTurnId=%s (first turn was %s)",
		read.Thread.Status.Type, published, firstTurn)
	if read.Thread.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("thread reads %q while turn 2 is running", read.Thread.Status.Type)
	}

	receipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		ExpectedTurnID:   published,
	})
	if err != nil {
		t.Fatalf("STOP IS BROKEN AT THE TURN BOUNDARY: thread/read published activeTurnId=%q with status active, and turn/interrupt against that very id failed: %v", published, err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("STOP IS BROKEN AT THE TURN BOUNDARY: interrupt against the published id %q returned disposition %q, want %q",
			published, receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
}
