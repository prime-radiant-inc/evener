package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// requireStopLandsDuringPreTurnWork asserts the invariant that replaced the
// pinned defect, and proves the run measured it rather than stumbling past it.
//
// This assertion used to run the other way: it pinned a REFUSAL, because
// InterruptClientMutation's precondition sampled s.WireState() -- the SESSION's
// state -- while the wire publishes active from the DAEMON's reservation, and
// during pre-turn work those disagree. Kata vewa fixed that: the precondition
// now also accepts a turn the durable snapshot has claimed, so a session with
// work in progress can be stopped whether or not it has reached the model yet.
//
// A pass still has to be earned. If the park closed while the Stop was in
// flight, what landed was a Stop against a running turn -- true, but nothing to
// do with pre-turn work -- and reporting that as a pass would leave the window
// untested. The two are separable by observation rather than timing margin:
// pre-turn work ends before the model round begins, so a model request waiting
// here means the window had already closed. A Stop that lands inside the window
// cancels the turn before it ever reaches the model, so nothing arrives.
func requireStopLandsDuringPreTurnWork(ctx context.Context, t *testing.T, provider *fakellm.Server, receipt appwire.TurnInterruptResponse, interruptErr error, where string) {
	t.Helper()
	if interruptErr != nil {
		t.Fatalf("Stop %s was refused: %v -- the user was looking at a Stop button on a session the wire said was working", where, interruptErr)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("Stop %s returned disposition %q, want %q", where, receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	probe, cancelProbe := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancelProbe()
	if call, err := provider.Next(probe.Done()); err == nil {
		call.RespondText("park had already closed")
		t.Fatalf("the park closed before the Stop landed %s: a model request was already waiting, so this run measured a Stop against a RUNNING turn and says nothing about pre-turn work. Rerun; do not read this as the window being covered", where)
	}
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

	receipt, interruptErr := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	requireStopLandsDuringPreTurnWork(ctx, t, provider, receipt, interruptErr, "on the first turn")
	t.Logf("a Stop pressed on the FIRST turn, with no queue and no turn boundary, while the wire showed status=active activeTurnId=%s, was applied", firstTurn)

	// The Stop cancelled the turn inside its pre-turn work, so the model was
	// never called and the session settles without one. That is the whole
	// point: the opening prompt never reached the provider.
	awaitThread(ctx, t, client, ref, "the stopped session to settle", func(thread appwire.Thread) bool {
		return thread.Status.Type != string(appwire.ThreadStatusActive)
	})
}

// TestE2E_StopIsOfferedWheneverTheWireSaysActive is the OTHER half of the same
// window, and the half a user actually sees first (kata 5gdv).
//
// The test above measures whether a Stop is accepted. This one measures whether
// the composer offers a Stop to press: the capability set published beside
// status=active must say interrupt=true, because Composer.tsx renders Stop on
// exactly that bit. A set saying steer=true interrupt=false draws Steer and
// Send with a gap where Stop belongs, which is the shape Jesse reported.
//
// The invariant asserted here is the composer's own gate, not an
// implementation detail: whenever the wire publishes a status the composer
// reads as a running turn, the capability that decides whether Stop is drawn
// must be true. A user who can see that the agent is working must be able to
// see the button that stops it.
func TestE2E_StopIsOfferedWheneverTheWireSaysActive(t *testing.T) {
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

	const openingSentinel = "SERF-E2E-STOP-OFFERED-PARKED"
	pluginDir := parkInPreTurnWork(t, openingSentinel)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "serf",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: "/park"}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", PluginDirs: []string{pluginDir}},
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

	// The state a user acts on: the composer's isTurnActive is satisfied, so it
	// is drawing a busy composer with Steer in it.
	turnID := awaitActiveTurn(ctx, t, client, ref, "")

	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("thread/read inside the pre-turn window: %v", err)
	}
	caps := read.Thread.Serf.Capabilities
	t.Logf("wire published status=%s activeTurnId=%s with steer=%v interrupt=%v",
		read.Thread.Status.Type, read.Thread.Serf.ActiveTurnID, caps.Steer, caps.Interrupt)

	// Guard the premise: if the park has closed, this read is of a running turn
	// and says nothing about the window.
	if read.Thread.Status.Type != string(appwire.ThreadStatusActive) || read.Thread.Serf.ActiveTurnID != turnID {
		t.Fatalf("the window closed before the read: status=%q activeTurnId=%q (turn was %s); rerun",
			read.Thread.Status.Type, read.Thread.Serf.ActiveTurnID, turnID)
	}
	if !caps.Steer {
		t.Fatalf("steer=false while the wire published status=active turn %s: the asymmetry this test is about is not present, so its measurement of interrupt means something else", turnID)
	}
	if !caps.Interrupt {
		t.Fatalf("the wire published status=active turn %s with steer=true and interrupt=FALSE: the composer draws Steer and Send and hides Stop, for a session the user can see is working", turnID)
	}

	// Let the parked turn finish so the thread shuts down cleanly.
	round, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the parked turn's model request: %v", err)
	}
	if !round.Contains(openingSentinel) {
		t.Fatalf("the parked command never reached the model; messages:\n%s", strings.Join(round.Texts(), "\n"))
	}
	round.RespondToolCall("communicate", communicateArgs("parked turn done"))
}

// TestE2E_PushedActiveStatusAlwaysCarriesStop measures the PUSH path across a
// queued drain, which is the shape of Jesse's live report (kata 5gdv): a queued
// message on a session that was already working, and afterwards a composer with
// Steer and Send in it and no Stop, for the whole of the next turn.
//
// Pull and push answer this question from different places, and only pull was
// ever measured. thread/read recomputes the set on demand, so a client that
// re-reads is always told the truth. A subscriber is not: it holds whatever the
// last thread/status/changed pushed, and the reducer replaces the set only on
// that frame (frontend/src/protocol/reducer.ts, `n.params.capabilities ??
// model.capabilities`). Status does not change again until the turn ends, so a
// set pushed at the start of a turn is the set the composer uses for all of it.
//
// The assertion is the invariant, not the ordering: no frame that tells a
// client a turn is running may tell it in the same breath that Stop is
// unavailable.
func TestE2E_PushedActiveStatusAlwaysCarriesStop(t *testing.T) {
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
		openingPrompt = "SERF-E2E-PUSHED-STOP-OPENING"
		queuedText    = "SERF-E2E-PUSHED-STOP-QUEUED"
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

	// thread/read is what puts this client on the relay, so every frame below
	// is one a browser holding this session open would have applied.
	if _, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref}); err != nil {
		t.Fatalf("thread/read to join the relay: %v", err)
	}

	// Collect status frames for the whole run, in the order a subscriber
	// applies them.
	type statusFrame struct {
		status string
		caps   *appwire.ThreadCapabilities
	}
	frames := make(chan statusFrame, 4096)
	collectCtx, stopCollecting := context.WithCancel(ctx)
	collecting := make(chan struct{})
	go func() {
		defer close(collecting)
		for {
			select {
			case <-collectCtx.Done():
				return
			case notification := <-client.Notifications():
				if notification.Method != appwire.NotifyThreadStatusChanged {
					continue
				}
				var params appwire.ThreadStatusChangedParams
				if err := json.Unmarshal(notification.Params, &params); err != nil {
					continue
				}
				select {
				case frames <- statusFrame{status: params.Status.Type, caps: params.Capabilities}:
				default:
				}
			}
		}
	}()

	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the first model request: %v", err)
	}
	firstTurn := awaitActiveTurn(ctx, t, client, ref, "")

	// Queue a message while the session is working, exactly as the composer's
	// Send does mid-turn. The drain loop runs it as a second turn the moment
	// turn 1 ends -- the window this test is about.
	if _, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: queuedText}},
	}); err != nil {
		t.Fatalf("turn/queue: %v", err)
	}
	awaitThread(ctx, t, client, ref, "queue depth 1", func(thread appwire.Thread) bool {
		return thread.Serf.Queue.Depth == 1
	})

	round1.RespondToolCall("communicate", communicateArgs("first turn done"))

	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the queued message's turn: %v", err)
	}
	if !round2.Contains(queuedText) {
		t.Fatalf("the queued message never reached the model; messages:\n%s", strings.Join(round2.Texts(), "\n"))
	}
	secondTurn := awaitActiveTurn(ctx, t, client, ref, firstTurn)
	round2.RespondToolCall("communicate", communicateArgs("queued turn done"))
	// Settled means "no longer working". A turn that ends by asking the user
	// something settles into awaiting rather than idle, and either one closes
	// the window this test collects over.
	awaitThread(ctx, t, client, ref, "the drain to settle", func(thread appwire.Thread) bool {
		return thread.Status.Type != string(appwire.ThreadStatusActive)
	})

	stopCollecting()
	<-collecting
	close(frames)

	var activeFrames, stopless int
	for frame := range frames {
		if frame.status != string(appwire.ThreadStatusActive) {
			continue
		}
		if frame.caps == nil {
			// Absent means "no update", so the client keeps a set that was
			// correct for some earlier status. Not this defect.
			continue
		}
		activeFrames++
		t.Logf("pushed status=active with send=%v steer=%v interrupt=%v",
			frame.caps.Send, frame.caps.Steer, frame.caps.Interrupt)
		if !frame.caps.Interrupt {
			stopless++
		}
	}
	if activeFrames == 0 {
		t.Fatalf("no thread/status/changed frame carrying an active status and a capability set was pushed across turns %s and %s; this test measured nothing", firstTurn, secondTurn)
	}
	if stopless > 0 {
		t.Fatalf("%d of %d pushed active-status frames carried interrupt=false: a subscriber applying one draws a busy composer with no Stop, and keeps it until the turn ends", stopless, activeFrames)
	}
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
		status    string
		turnID    string
		depth     int
		send      bool
		steer     bool
		interrupt bool
	}
	samples := make(chan sample, 8192)
	// Counted by the sampler itself, so the test can wait for the SAMPLER to
	// have measured the window rather than assume it did. The wait loop below
	// breaks the instant it observes the boundary, and the sampler is a separate
	// goroutine: stopping it right there left the whole measurement resting on
	// one lucky poll.
	var sampledInWindow atomic.Int64
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
			inBoundary := read.Thread.Status.Type == string(appwire.ThreadStatusActive) &&
				read.Thread.Serf.ActiveTurnID == firstTurn &&
				read.Thread.Serf.Queue.Depth == 0
			select {
			case samples <- sample{
				status:    read.Thread.Status.Type,
				turnID:    read.Thread.Serf.ActiveTurnID,
				depth:     read.Thread.Serf.Queue.Depth,
				send:      read.Thread.Serf.Capabilities.Send,
				steer:     read.Thread.Serf.Capabilities.Steer,
				interrupt: read.Thread.Serf.Capabilities.Interrupt,
			}:
				if inBoundary {
					sampledInWindow.Add(1)
				}
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

	// Let the sampler measure the window it is here to measure. The park holds
	// the boundary open for preTurnParkSeconds, so waiting for a batch of
	// in-window samples costs a fraction of that and replaces a lucky single
	// poll with a real sample set.
	const wantInWindowSamples = 20
	batchDeadline := time.Now().Add(time.Duration(preTurnParkSeconds) * time.Second / 2)
	for sampledInWindow.Load() < wantInWindowSamples && time.Now().Before(batchDeadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Sampling closes here, before the Stop below, so every sample counted is
	// one taken while the session was working across the boundary. Leaving the
	// sampler running through a Stop would fold the settled session that
	// follows it into the same counts and make notActive fire on the test's own
	// cancellation.
	stopSampling()
	<-sampling
	close(samples)

	var activeWithNoID, staleAcrossTheBoundary, notActive, total int
	seen := map[string]int{}
	for s := range samples {
		total++
		seen[fmt.Sprintf("%s/%s/depth=%d send=%v steer=%v interrupt=%v", s.status, s.turnID, s.depth, s.send, s.steer, s.interrupt)]++
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
	t.Logf("samples=%d activeWithNoID=%d staleAcrossTheBoundary=%d notActive=%d (turn 1 %s)",
		total, activeWithNoID, staleAcrossTheBoundary, notActive, firstTurn)

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

	// Now the Stop, in the window the samples above prove was open: the wire
	// says active and publishes an id, which is the whole of isTurnActive, so
	// the composer is showing the button. The request is session-scoped -- it
	// names no turn, because appwire v3 removed expectedTurnId from every
	// control mutation -- so the only thing that can refuse it is the daemon's
	// own "is this session quiesced" precondition, and turn 2 being claimed is
	// what makes that answer no (kata vewa).
	receipt, interruptErr := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	requireStopLandsDuringPreTurnWork(ctx, t, provider, receipt, interruptErr, "at the turn boundary")
	t.Logf("a Stop pressed %s after the window opened, while the wire showed status=%s activeTurnId=%s, was applied",
		time.Since(windowSeenAt), inWindow.Status.Type, inWindow.Serf.ActiveTurnID)

	// PINNED DEFECT (kata wms7) -- read this before changing the assertion.
	//
	// The Stop above was ACCEPTED, and the turn it was aimed at runs anyway: the
	// queued message the user just stopped reaches the model. That is asserted
	// here, in both directions, so it cannot change silently. It is not what
	// anyone wants, and Jesse has ruled on what should happen instead: the
	// claimed turn is cancelled and its message goes back on the queue, so
	// nothing is lost and nothing auto-starts.
	//
	// Half of that ruling is already built. A turn claimed out of the queue that
	// never incorporates its input IS returned to the queue -- see
	// agent/session_stop_and_queued_work_test.go's
	// TestClaimedQueuedTurnIsReturnedWhenItNeverRan. What is missing is the
	// cancellation ever reaching this turn, and three measurements narrow where
	// it goes wrong:
	//
	//   - the mutation runner is armed when the Stop lands (cancel and done both
	//     non-nil), so this is NOT a Stop firing into an empty slot;
	//   - nextTurnCtx is never invoked, so the drain-on-interrupt handler is not
	//     quietly restarting the turn under a fresh context;
	//   - popQueueHead already refuses to claim under a pending fence, so a Stop
	//     that arrives BEFORE the claim keeps the message queued correctly. Only
	//     a Stop that arrives after the claim and before the turn starts
	//     producing lands in this hole.
	//
	// Which leaves the turn's PRE-TURN WORK not observing its cancelled context
	// and carrying on into the model round. That is the next thing to measure,
	// and it is deliberately not guessed at here.
	//
	// This is reachable at all only because kata vewa made Stop land in this
	// window; before that it was refused. So the fix belongs with that work, not
	// filed away as pre-existing.
	probe, cancelProbe := context.WithTimeout(ctx, 3*time.Second)
	defer cancelProbe()
	round2, err := provider.Next(probe.Done())
	if err != nil {
		t.Fatal("no model round followed the Stop: the turn the user stopped no longer runs, which is the DESIRED behaviour (kata wms7) and makes this assertion stale. Invert it -- assert no model round, and that the message is back on the queue with the session settled -- and close wms7")
	}
	if !round2.Contains(queuedText) {
		t.Fatalf("a model round followed the Stop but does not carry the queued text; this pin no longer describes what happens. messages:\n%s", strings.Join(round2.Texts(), "\n"))
	}
	t.Logf("MEASURED DEFECT (wms7): the Stop was applied and the queued turn ran anyway, carrying the queued text to the model")
	round2.RespondToolCall("communicate", communicateArgs("post-stop turn done"))
	awaitThread(ctx, t, client, ref, "the session to settle", func(thread appwire.Thread) bool {
		return thread.Status.Type != string(appwire.ThreadStatusActive)
	})
}
