package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/test/e2e/fakellm"
)

// boundaryParkSeconds is how long the queued turn is held inside the
// between-turns window. Long enough to sample and act through two RPC round
// trips, well under the 10s cap agent/command.runInlineCommand puts on an
// inline span.
const boundaryParkSeconds = 3

// parkAtTheTurnBoundary writes a plugin whose /park command shells out, and
// returns the directory to hand the daemon as --plugin-dir.
//
// This is the deterministic coordination the measurement needs. The window
// between two turns of one drain is a few instructions wide and no test seam
// reaches into a spawned daemon, so an out-of-process sampler can only ever
// report what the scheduler happened to allow -- which is how the previous
// version of this test sampled the boundary 22 times and never once observed
// turn 2. A plugin slash command is production machinery that lands exactly
// inside the window: processOneInput expands it (agent/session_lifecycle.go,
// the `kind == EntryUserInput` branch at the top) AFTER popQueueHead has
// claimed the queued entry and BEFORE anything announces turn 2, and
// agent/command.Expand runs an inline !`cmd` span synchronously. The state the
// park holds open is the ordinary transient state of every drain; the command
// only makes it long enough to measure.
func parkAtTheTurnBoundary(t *testing.T, sentinel string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("create plugin metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name": "boundary-park"}`), 0o600); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "commands"), 0o755); err != nil {
		t.Fatalf("create plugin commands dir: %v", err)
	}
	// Only a PLUGIN command executes its inline spans; a serf-wide command
	// expands inert (agent/session_slash_command.go). The sentinel rides in the
	// body so the model request proves this expansion is what ran.
	body := fmt.Sprintf("---\ndescription: hold the drain loop at the turn boundary\n---\n%s !`sleep %d`\n",
		sentinel, boundaryParkSeconds)
	if err := os.WriteFile(filepath.Join(dir, "commands", "park.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write park command: %v", err)
	}
	return dir
}

// TestE2E_ControlInvariantAcrossATurnBoundary measures what a client can
// observe, and what its Stop does, in the window between two turns of one
// drain.
//
// The invariant is the one the composer gates on (submitRouting.ts's
// isTurnActive: statusType === "active" && !!activeTurnId): whenever the wire
// says a turn is running it must publish an id, and a Stop pressed while it
// says so must land. Stop carries no turn id any more -- control mutations are
// session-scoped since appwire v3 -- so "the published id is one the
// preconditions accept" is no longer the question. "Is Stop accepted at all"
// is.
//
// The window is real and this test now enters it deliberately:
// completeClientMutationTurnWithState clears the durable turn name when turn 1
// ends, popQueueHead claims turn 2's, and the wire learns nothing until turn
// 2's EventUserInput reaches the projector -- so throughout, thread/read
// answers status=active with TURN 1's id. Queue depth is the discriminator
// that makes "stale" measurable rather than indistinguishable from "turn 1 is
// still running": it comes from the session's live durable snapshot
// (server/thread_envelope.go samples it per event), while the turn id comes
// from the projector, so active + turn 1's id + depth 0 can only mean turn 2
// has been claimed and not yet announced.
//
// WHAT IT FINDS, pinned below: in that window the wire shows a working session
// and the daemon refuses the Stop. See the block at the interrupt.
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
	pluginDir := parkAtTheTurnBoundary(t, queuedText)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "serf",
		CWD:     stack.workDir,
		Input:   []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:   stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{
			Sandbox:    "off",
			PluginDirs: []string{pluginDir},
		},
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

	// Queue the parking command so the drain loop runs a SECOND turn straight
	// after the first ends, and holds itself inside the boundary while it
	// starts. Without a queued message the session simply settles idle.
	if _, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: "/park"}},
	}); err != nil {
		t.Fatalf("turn/queue: %v", err)
	}
	awaitThread(ctx, t, client, ref, "queue depth 1", func(thread appwire.Thread) bool {
		return thread.Serf.Queue.Depth == 1
	})

	// Sample thread/read continuously from before the boundary until after
	// turn 2 has announced itself, so the window is inside the sample stream
	// rather than either side of it.
	type sample struct {
		status string
		turnID string
		depth  int
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
			case samples <- sample{
				status: read.Thread.Status.Type,
				turnID: read.Thread.Serf.ActiveTurnID,
				depth:  read.Thread.Serf.Queue.Depth,
			}:
			default:
			}
		}
	}()

	// End turn 1. The drain loop then completes its mutation, pops the queued
	// message and parks inside the command expansion -- the boundary.
	round1.RespondToolCall("communicate", communicateArgs("first turn done"))

	// Wait for the window itself, not for a clock. Turn 2 has been claimed
	// (depth 0) and has not been announced (the id is still turn 1's).
	inWindow := appwire.Thread{}
	windowDeadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(windowDeadline) {
			t.Fatalf("the boundary never opened: thread/read never reported status=active with turn %s and an emptied queue, so nothing below measures the window it names", firstTurn)
		}
		read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
		if err != nil {
			continue
		}
		if read.Thread.Status.Type == string(appwire.ThreadStatusActive) && read.Thread.Serf.ActiveTurnID == "" {
			t.Fatalf("the wire published status=active with no turn id at the boundary (queueDepth=%d): the composer hides Stop and Steer for a session that is working", read.Thread.Serf.Queue.Depth)
		}
		if read.Thread.Status.Type == string(appwire.ThreadStatusActive) &&
			read.Thread.Serf.ActiveTurnID == firstTurn &&
			read.Thread.Serf.Queue.Depth == 0 {
			inWindow = read.Thread
			break
		}
		if read.Thread.Serf.ActiveTurnID != "" && read.Thread.Serf.ActiveTurnID != firstTurn {
			t.Fatalf("turn 2 announced itself (%s) before the window was sampled; the parking command did not hold the boundary open", read.Thread.Serf.ActiveTurnID)
		}
	}
	t.Logf("inside the boundary the wire published status=%s activeTurnId=%s queueDepth=%d (turn 1 was %s)",
		inWindow.Status.Type, inWindow.Serf.ActiveTurnID, inWindow.Serf.Queue.Depth, firstTurn)

	// A Stop pressed right here is a Stop pressed while the composer is
	// showing the button: the wire says active and publishes an id, which is
	// the whole of isTurnActive. The request is session-scoped -- it names no
	// turn, because appwire v3 removed expectedTurnId from every control
	// mutation -- so the only thing that can refuse it is the daemon's own
	// "is this session processing" precondition.
	_, interruptErr := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})

	// PINNED DEFECT -- read this before changing the assertion.
	//
	// The Stop is REJECTED, and this test asserts the rejection so it cannot
	// change silently in either direction. It is not the behaviour anyone
	// wants: "Stop means stop what you are doing" is the recorded ruling
	// behind appwire v3.
	//
	// Why it happens: InterruptClientMutation samples s.WireState()
	// (agent/session_client_mutation.go, "Stop cancels whatever is running, so
	// its precondition is the fact the wire publishes as the thread's status")
	// -- but that is NOT the fact the wire publishes. The wire's `active` comes
	// from the daemon's own processing flag, which spans the whole drain, while
	// WireState reads the SESSION: turn 1 settled it idle, popQueueHead already
	// emptied the queue so sessionWorkPending() is false, and processOneInput
	// does not set SessionProcessing until after the command expansion. Wire
	// says working; session says idle; Stop is refused.
	//
	// This directly contradicts
	// docs/superpowers/plans/2026-08-16-stop-always-works.md, whose RESULT
	// block withdrew Task 2's premise on the strength of THIS test in its
	// earlier form -- a form that sampled only after the boundary and never
	// reached the window. The gap is observable, and a client's Stop dies in
	// it.
	//
	// When the daemon is fixed, invert this block and move that RESULT block
	// with it.
	if interruptErr == nil {
		t.Fatalf("Stop now lands inside the turn boundary. That is the desired behaviour and this assertion is stale: invert it, and update the RESULT block in docs/superpowers/plans/2026-08-16-stop-always-works.md, which still records Task 2's premise as withdrawn")
	}
	if !strings.Contains(interruptErr.Error(), "session is not processing") {
		t.Fatalf("Stop inside the turn boundary failed for an unexpected reason: %v", interruptErr)
	}
	t.Logf("MEASURED DEFECT: a Stop pressed while the wire showed status=%s activeTurnId=%s was refused: %v",
		inWindow.Status.Type, inWindow.Serf.ActiveTurnID, interruptErr)

	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the queued message's turn: %v", err)
	}
	if !round2.Contains(queuedText) {
		t.Fatalf("the parked command never reached the model; messages:\n%s", strings.Join(round2.Texts(), "\n"))
	}
	secondTurn := awaitActiveTurn(ctx, t, client, ref, firstTurn)
	stopSampling()
	<-sampling
	close(samples)

	var activeWithNoID, staleAcrossTheBoundary, notActive, total int
	seen := map[string]int{}
	for s := range samples {
		total++
		seen[fmt.Sprintf("%s/%s/depth=%d", s.status, s.turnID, s.depth)]++
		if s.status != string(appwire.ThreadStatusActive) {
			notActive++
			continue
		}
		if s.turnID == "" {
			activeWithNoID++
		}
		// Turn 1's id with an emptied queue: turn 2 is claimed and unannounced.
		// Turn 1's id with depth 1 is just turn 1 running, and is not stale.
		if s.turnID == firstTurn && s.depth == 0 {
			staleAcrossTheBoundary++
		}
	}
	for k, n := range seen {
		t.Logf("observed %5d x %s", n, k)
	}
	t.Logf("samples=%d activeWithNoID=%d staleAcrossTheBoundary=%d notActive=%d (turn 1 %s, turn 2 %s)",
		total, activeWithNoID, staleAcrossTheBoundary, notActive, firstTurn, secondTurn)

	if total == 0 {
		t.Fatal("no samples taken; the test proves nothing")
	}
	if activeWithNoID > 0 {
		t.Fatalf("the wire published status=active with no turn id in %d of %d samples: the composer hides Stop and Steer for a session that is working", activeWithNoID, total)
	}
	if notActive > 0 {
		t.Fatalf("the wire dropped out of status=active in %d of %d samples across a boundary the session never left: the composer swaps Stop for Send mid-work", notActive, total)
	}
	// The counter the old version of this test computed and only logged. It
	// has to be non-zero or nothing above measured the window -- the earlier
	// harness took 22 samples across this boundary and every one of them
	// landed before it.
	if staleAcrossTheBoundary == 0 {
		t.Fatalf("no sample fell inside the boundary (active + turn %s + an emptied queue) in %d samples; this test sampled either side of the window it names", firstTurn, total)
	}

	// The contrast that shows the refusal above is specific to the window: the
	// same session-scoped Stop, once turn 2 has announced itself, is applied.
	receipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("a session-scoped Stop failed against a turn that has announced itself (%s): %v", secondTurn, err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("a session-scoped Stop against the announced turn %s returned disposition %q, want %q",
			secondTurn, receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
}
