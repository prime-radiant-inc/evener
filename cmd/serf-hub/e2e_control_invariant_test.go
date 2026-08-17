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

// preTurnParkSeconds is how long the session is held inside its pre-turn work.
// Long enough to sample and act through several RPC round trips, well under the
// 10s cap agent/command.runInlineCommand puts on an inline span.
const preTurnParkSeconds = 3

// parkInPreTurnWork writes a plugin whose /park command shells out, and returns
// the directory to hand the daemon as --plugin-dir.
//
// This is the deterministic coordination the measurement needs. Pre-turn work
// is a few instructions wide for ordinary input and no test seam reaches into a
// spawned daemon, so an out-of-process sampler could only ever report what the
// scheduler happened to allow -- which is how the previous version of this test
// sampled a turn boundary 22 times and never once observed the second turn.
//
// A plugin slash command is production machinery (LaunchConfigLayer.PluginDirs
// is a real launch layer, with a settings UI) that lands exactly in the window:
// processOneInput expands it (agent/session_lifecycle.go:977, the
// `kind == EntryUserInput` branch) 23 lines BEFORE
// s.setStateIfOpenLocked(SessionProcessing) at :1000, and agent/command.Expand
// runs an inline !`cmd` span synchronously. The state it holds open is the
// ordinary transient state of every turn; the command only makes it long enough
// to measure.
func parkInPreTurnWork(t *testing.T, sentinel string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("create plugin metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name": "preturn-park"}`), 0o600); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "commands"), 0o755); err != nil {
		t.Fatalf("create plugin commands dir: %v", err)
	}
	// Only a PLUGIN command executes its inline spans; a serf-wide command
	// expands inert (agent/session_slash_command.go). The sentinel rides in the
	// body so the model request proves this expansion is what ran.
	body := fmt.Sprintf("---\ndescription: hold the session in pre-turn work\n---\n%s !`sleep %d`\n",
		sentinel, preTurnParkSeconds)
	if err := os.WriteFile(filepath.Join(dir, "commands", "park.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write park command: %v", err)
	}
	return dir
}

// pinStopRefusedDuringPreTurnWork asserts the MEASURED DEFECT and, on the one
// branch where a pass could be an accident, proves which it was.
//
// PINNED DEFECT -- read this before changing the assertion. The Stop is
// REFUSED, and this is asserted so it cannot change silently in either
// direction. It is not the behaviour anyone wants: "Stop means stop what you
// are doing" is the recorded ruling behind appwire v3.
//
// Why it happens: InterruptClientMutation samples s.WireState()
// (agent/session_client_mutation.go, "Stop cancels whatever is running, so its
// precondition is the fact the wire publishes as the thread's status") -- but
// that is NOT the fact the wire publishes. The wire's `active` comes from
// appStatus() reading srv.processing (server/appwire_runtime.go), which the
// daemon sets for the whole of ProcessInputKind and clears only after the
// entire drain returns (cmd/serf/serve.go). WireState reads the SESSION, and
// during pre-turn work the session is idle with no pending work. Wire says
// working; session says idle; Stop is refused.
//
// When the daemon is fixed, invert this and move the CORRECTION block in
// docs/superpowers/plans/2026-08-16-stop-always-works.md with it.
func pinStopRefusedDuringPreTurnWork(ctx context.Context, t *testing.T, provider *fakellm.Server, interruptErr error, where string) {
	t.Helper()
	if interruptErr != nil {
		if !strings.Contains(interruptErr.Error(), "session is not processing") {
			t.Fatalf("Stop %s failed for an unexpected reason: %v", where, interruptErr)
		}
		return
	}

	// Applied. Either the daemon was fixed, or the park closed while the Stop
	// was in flight and what we actually measured was a Stop against a running
	// turn -- which proves nothing and must not be reported as a fix. Those two
	// are distinguishable by observation rather than by timing margin: pre-turn
	// work ends before the model round begins, so if a model request is already
	// waiting, the window had closed.
	probe, cancelProbe := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancelProbe()
	if call, err := provider.Next(probe.Done()); err == nil {
		call.RespondText("park had already closed")
		t.Fatalf("the park closed before the Stop landed %s: a model request was already waiting, so this run measured a Stop against a RUNNING turn and says nothing about pre-turn work. Rerun; do not read this as the defect being fixed", where)
	}
	t.Fatalf("Stop now lands during pre-turn work %s, with no model round started. That is the desired behaviour and this assertion is stale: invert it, and update the CORRECTION block in docs/superpowers/plans/2026-08-16-stop-always-works.md, which still records the refusal", where)
}

// TestE2E_ControlInvariantDuringPreTurnWorkOnTheFirstTurn is the smallest form
// of the defect: no queue, no turn boundary, nothing to drain. A thread whose
// OPENING prompt is a slash command spends the whole expansion with the wire
// publishing status=active and an id -- which is the entirety of the composer's
// gate (submitRouting.ts's isTurnActive: statusType === "active" &&
// !!activeTurnId) -- while the session reports itself idle and refuses Stop.
//
// This case exists because the defect was first found at a turn boundary and
// mis-scoped there. It is not a boundary defect. Pre-turn work runs on EVERY
// turn, the first included, and a fix that only covers the drain-loop window
// would leave this one standing.
func TestE2E_ControlInvariantDuringPreTurnWorkOnTheFirstTurn(t *testing.T) {
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

	const openingSentinel = "SERF-E2E-FIRST-TURN-PARKED"
	pluginDir := parkInPreTurnWork(t, openingSentinel)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "serf",
		CWD:     stack.workDir,
		// The opening prompt IS the slash command, so the session parks in
		// pre-turn work before it has ever been processing.
		Input: []appwire.InputItem{{Type: "text", Text: "/park"}},
		Model: stack.model,
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

	// Wait for the state a user would act on: the composer is showing Stop.
	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")
	t.Logf("on the first turn, before any model round, the wire published status=active activeTurnId=%s", firstTurn)

	_, interruptErr := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	pinStopRefusedDuringPreTurnWork(ctx, t, provider, interruptErr, "on the first turn")
	t.Logf("MEASURED DEFECT: a Stop pressed on the FIRST turn, with no queue and no turn boundary, while the wire showed status=active activeTurnId=%s, was refused: %v", firstTurn, interruptErr)

	// Let the parked turn finish so the thread shuts down cleanly, and prove in
	// passing that the expansion this test parked in is the one that ran.
	round, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the first turn's model request: %v", err)
	}
	if !round.Contains(openingSentinel) {
		t.Fatalf("the parked command never reached the model; messages:\n%s", strings.Join(round.Texts(), "\n"))
	}
	round.RespondToolCall("communicate", communicateArgs("first turn done"))
}

// TestE2E_ControlInvariantDuringPreTurnWorkAtATurnBoundary is the same defect
// in the window it was first found in, plus the sampling that shows what a
// client can observe across a boundary between two turns of one drain.
//
// The invariant is the one the composer gates on (submitRouting.ts's
// isTurnActive: statusType === "active" && !!activeTurnId): whenever the wire
// says a turn is running it must publish an id, and a Stop pressed while it
// says so must land. Stop carries no turn id any more -- control mutations are
// session-scoped since appwire v3 -- so "the published id is one the
// preconditions accept" is no longer the question. "Is Stop accepted at all"
// is.
//
// The boundary window is real and this test enters it deliberately:
// completeClientMutationTurnWithState clears the durable turn name when turn 1
// ends, popQueueHead claims turn 2's, and the wire learns nothing until turn
// 2's EventUserInput reaches the projector -- so throughout, thread/read
// answers status=active with TURN 1's id. Queue depth is the discriminator that
// makes "stale" measurable rather than indistinguishable from "turn 1 is still
// running": it comes from the session's live durable snapshot, while the turn
// id comes from the projector, so active + turn 1's id + depth 0 can only mean
// turn 2 has been claimed and not yet announced.
func TestE2E_ControlInvariantDuringPreTurnWorkAtATurnBoundary(t *testing.T) {
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
	pluginDir := parkInPreTurnWork(t, queuedText)

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
	// after the first ends, and holds itself in that turn's pre-turn work.
	// Without a queued message the session simply settles idle.
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

	// Sample thread/read continuously from before the boundary until after turn
	// 2 has announced itself, so the window is inside the sample stream rather
	// than either side of it.
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
				// Back off: a dead daemon would otherwise spin a core here for
				// as long as the test's deadline allows.
				time.Sleep(5 * time.Millisecond)
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
	// message and parks in turn 2's pre-turn work -- the boundary.
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
			time.Sleep(5 * time.Millisecond)
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
	windowSeenAt := time.Now()
	t.Logf("inside the boundary the wire published status=%s activeTurnId=%s queueDepth=%d (turn 1 was %s)",
		inWindow.Status.Type, inWindow.Serf.ActiveTurnID, inWindow.Serf.Queue.Depth, firstTurn)

	// A Stop pressed right here is a Stop pressed while the composer is showing
	// the button: the wire says active and publishes an id, which is the whole
	// of isTurnActive. The request is session-scoped -- it names no turn,
	// because appwire v3 removed expectedTurnId from every control mutation --
	// so the only thing that can refuse it is the daemon's own "is this session
	// processing" precondition.
	_, interruptErr := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	pinStopRefusedDuringPreTurnWork(ctx, t, provider, interruptErr, "at the turn boundary")
	t.Logf("MEASURED DEFECT: a Stop pressed %s after the window opened, while the wire showed status=%s activeTurnId=%s, was refused: %v",
		time.Since(windowSeenAt), inWindow.Status.Type, inWindow.Serf.ActiveTurnID, interruptErr)

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
	// These counts are polling throughput, not a property of the system: they
	// move with machine load from run to run. Only their being zero or non-zero
	// is a result, which is what the assertions below use.
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
	// The counter the old version of this test computed and only logged. It has
	// to be non-zero or nothing above measured the window -- the earlier harness
	// took its samples across this boundary and every one of them landed before
	// it.
	if staleAcrossTheBoundary == 0 {
		t.Fatalf("no sample fell inside the boundary (active + turn %s + an emptied queue) in %d samples; this test sampled either side of the window it names", firstTurn, total)
	}

	// The contrast that shows the refusal above is specific to pre-turn work:
	// the same session-scoped Stop, once turn 2 has announced itself and the
	// session is really processing, is applied.
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
