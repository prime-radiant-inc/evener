package server

import (
	"context"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A turn started through Session.ProcessInput carries no client-mutation
// reservation, so nothing outside the session names it. Both projections must
// still land on the same turn id: the live projector reads the id off the
// USER_INPUT event and the cold projection reads it off the persisted entry,
// and an item whose transcript key changes across a reload is re-rendered as a
// duplicate by the web client (PR #822 F3).
//
// The environment entry is what makes this observable from the first turn: it
// owns a durable id of its own and must not consume the following input's.
func TestDirectInputEnvironmentKeepsLiveAndColdTurnIdentity(t *testing.T) {
	adapter := &environmentReplayAdapter{mutationProjectionAdapter{blockAt: 100}}
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", sess.StateDir()}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "--initial-branch=first")
	git("-c", "user.name=Evener Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Environment fixture")
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
	environmentCount := 0
	for input := range 2 {
		if input == 1 {
			git("symbolic-ref", "HEAD", "refs/heads/second")
		}
		if _, err := sess.ProcessInput(context.Background(), fmt.Sprintf("input-%d", input), nil); err != nil {
			t.Fatalf("direct input %d: %v", input, err)
		}
	drain:
		for {
			select {
			case event := <-sess.Events():
				if event.Kind == events.EventEnvironment {
					environmentCount++
				}
				srv.RecordAppEvent(event)
			default:
				break drain
			}
		}
	}
	if environmentCount != 2 {
		t.Fatalf("environment events = %d, want initial context and branch change", environmentCount)
	}
	cold, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	live := directInputReplayItems(srv.appTurns.Snapshot())
	if len(live) != 6 {
		t.Fatalf("live items = %d, want two environment/user/timing sets: %#v", len(live), live)
	}
	assertReplayItemParity(t, "direct input environment replay", live, directInputReplayItems(cold))
}

// A compaction fold that runs during a direct-input turn publishes its
// metadata, artifacts and injected steering while that turn is the one on
// screen. The live projector attributes them to the running turn; the cold
// projection must group them the same way rather than closing the turn and
// opening a standalone group per marker.
func TestDirectInputCompactionKeepsLiveAndColdTurnGrouping(t *testing.T) {
	dir := t.TempDir()
	a := &overlapAdapter{mainStarted: make(chan int, 2), summaryStarted: make(chan struct{}, 1), releaseA: make(chan struct{}), releaseB: make(chan struct{}), releaseSummary: make(chan struct{})}
	c := llm.NewClient()
	c.Register(a)
	s, err := agent.NewSession(c, provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), "openai/fixture-summary"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var live atomic.Pointer[Server]
	ends := make(chan struct{}, 64)
	go s.ConsumeEventsLossless(func(e events.SessionEvent) {
		if srv := live.Load(); srv != nil {
			srv.RecordAppEvent(e)
		}
		if e.Kind == events.EventSessionEnd {
			ends <- struct{}{}
		}
	}, func() {})
	wait := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, s.ID(), s.TranscriptPath())
	live.Store(srv)
	for i := range 6 {
		if _, err := s.ProcessInput(ctx, fmt.Sprintf("seed-%d", i), nil); err != nil {
			t.Fatal(err)
		}
		wait(ends)
	}
	turnDone := make(chan error, 1)
	go func() { _, e := s.ProcessInput(ctx, "direct-turn", nil); turnDone <- e }()
	select {
	case got := <-a.mainStarted:
		if got != 7 {
			t.Fatalf("main provider call=%d, want 7", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	a.enableA.Store(true)
	compactDone := make(chan error, 1)
	go func() { compactDone <- s.Compact(ctx) }()
	wait(a.summaryStarted)
	close(a.releaseSummary)
	if err := <-compactDone; err != nil {
		t.Fatal(err)
	}
	close(a.releaseA)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	wait(ends)
	close(a.releaseB)

	read, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	liveItems := directInputReplayItems(read.Thread.Turns)
	coldItems := directInputReplayItems(cold)
	// The turn the fold ran under is the last user turn; the compaction
	// sequence belongs to it. Pinning the owner to that turn is what stops
	// both projections agreeing on the same WRONG grouping.
	wantOwner := ""
	for _, item := range liveItems {
		if item.Type == "userMessage" {
			wantOwner = item.TurnID
		}
	}
	if wantOwner == "" {
		t.Fatalf("replay has no user message to own the compaction: %#v", liveItems)
	}
	compactionItems := 0
	for _, item := range liveItems {
		if item.EventKind != appwire.ThreadItemEventKindContextCompaction && item.EventKind != appwire.ThreadItemEventKindCompaction {
			continue
		}
		compactionItems++
		if item.TurnID != wantOwner {
			t.Fatalf("live compaction item %s owner=%s, want the direct turn %s", item.EventKind, item.TurnID, wantOwner)
		}
	}
	if compactionItems != 4 {
		t.Fatalf("live compaction items = %d, want two metadata and two artifacts: %#v", compactionItems, liveItems)
	}
	assertReplayItemParity(t, "direct input compaction replay", liveItems, coldItems)
}

// A goal continuation is named by mintRunningTurnID, which refuses outright
// for a session no daemon serves (turnNameUnserved). The continuation then
// runs with no id of its own and reproduces the same divergence a direct user
// turn did: the live projector and the transcript projection each mint a
// turn_%d from a different counter.
func TestUnservedGoalContinuationKeepsLiveAndColdTurnIdentity(t *testing.T) {
	adapter := &environmentReplayAdapter{mutationProjectionAdapter{blockAt: 100}}
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
	drain := func() {
		t.Helper()
		for {
			select {
			case event := <-sess.Events():
				srv.RecordAppEvent(event)
			default:
				return
			}
		}
	}
	if _, err := sess.ProcessInput(context.Background(), "opening input", nil); err != nil {
		t.Fatalf("direct input: %v", err)
	}
	drain()
	// The daemon dispatches a goal continuation exactly this way
	// (cmd/evener/serve.go's ProcessInputKind branch, fed by
	// server.SubmitContinuation).
	for continuation := range 2 {
		if _, err := sess.ProcessInputKind(context.Background(), fmt.Sprintf("continue toward the goal %d", continuation), nil, agent.EntryContinuation); err != nil {
			t.Fatalf("continuation %d: %v", continuation, err)
		}
		drain()
	}
	cold, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	live := directInputReplayItems(srv.appTurns.Snapshot())
	goals := 0
	for _, item := range live {
		if item.Description == goalContinuationItemDescription {
			goals++
		}
	}
	if goals != 2 {
		t.Fatalf("live goal continuation items = %d, want 2: %#v", goals, live)
	}
	assertReplayItemParity(t, "unserved goal continuation replay", live, directInputReplayItems(cold))
}

// A notification wake is named by the same mintRunningTurnID, and for an
// unserved session the daemon-only stand-down above it does not apply: the
// wake must still deliver, so it used to open its turn with an empty id and
// diverge exactly as the continuation did.
func TestUnservedNotificationWakeKeepsLiveAndColdTurnIdentity(t *testing.T) {
	adapter := &environmentReplayAdapter{mutationProjectionAdapter{blockAt: 100}}
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
	drain := func() {
		t.Helper()
		for {
			select {
			case event := <-sess.Events():
				srv.RecordAppEvent(event)
			default:
				return
			}
		}
	}
	if _, err := sess.ProcessInput(context.Background(), "opening input", nil); err != nil {
		t.Fatalf("direct input: %v", err)
	}
	drain()
	// Pending steering is what makes the wake deliverable without a job
	// fixture; the daemon dispatches it the same way server.SubmitNotification
	// does.
	sess.SteerKind("look at this", events.SteeringKindAgentMessage)
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, agent.EntryNotification); err != nil {
		t.Fatalf("notification wake: %v", err)
	}
	drain()
	cold, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	assertReplayItemParity(t, "unserved notification wake replay", directInputReplayItems(srv.appTurns.Snapshot()), directInputReplayItems(cold))
}

// The interrupt marker a cancelled turn appends is persisted ownerless: the
// drain loop clears the self-minted name before the interrupt branch runs
// (session_lifecycle.go clears at the top of the iteration, appends the marker
// further down). Both projections still land it in the turn that was
// interrupted, and for the same reason rather than by coincidence: the marker
// is a STEERING entry, which continues an open group in the cold projection,
// and it is emitted BEFORE the interrupted turn's SESSION_END, so the live
// projector's turn is still open too. This pins that pair — an owner captured
// before the clear would make the grouping explicit instead of inherited, but
// nothing diverges today, so nothing here changes it.
func TestInterruptedDirectTurnKeepsLiveAndColdMarkerGrouping(t *testing.T) {
	adapter := &environmentReplayAdapter{mutationProjectionAdapter{blockAt: 2, blocked: make(chan struct{})}}
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
	drain := func() {
		t.Helper()
		for {
			select {
			case event := <-sess.Events():
				srv.RecordAppEvent(event)
			default:
				return
			}
		}
	}
	if _, err := sess.ProcessInput(context.Background(), "completed input", nil); err != nil {
		t.Fatalf("completed input: %v", err)
	}
	drain()
	turnCtx, cancel := context.WithCancel(context.Background())
	interrupted := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(turnCtx, "interrupted input", nil)
		interrupted <- err
	}()
	<-adapter.blocked
	cancel()
	if err := <-interrupted; err == nil {
		t.Fatal("cancelled turn returned no error")
	}
	drain()
	cold, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	live := directInputReplayItems(srv.appTurns.Snapshot())
	// The marker belongs to the turn that was interrupted, which is the last
	// user message — not a standalone group of its own, and not the completed
	// turn before it.
	interruptedTurn := ""
	markers := 0
	for _, item := range live {
		if item.Type == "userMessage" {
			interruptedTurn = item.TurnID
			continue
		}
		if item.Type != "steering" {
			continue
		}
		markers++
		if item.TurnID != interruptedTurn {
			t.Fatalf("interrupt marker owner=%s, want the interrupted turn %s: %#v", item.TurnID, interruptedTurn, live)
		}
	}
	if markers != 1 {
		t.Fatalf("live steering items = %d, want the single interrupt marker: %#v", markers, live)
	}
	assertReplayItemParity(t, "interrupted direct turn replay", live, directInputReplayItems(cold))
}

// goalContinuationItemDescription is the label the projection gives a goal
// continuation's system item.
const goalContinuationItemDescription = "Goal"

// directInputReplayItems keeps the items whose turn identity these cases are
// about: the user's own message, the environment block that precedes it, the
// compaction sequence and the round timings that close each turn.
func directInputReplayItems(turns []appwire.Turn) []replayItemIdentity {
	var items []replayItemIdentity
	for _, item := range replayItemIdentities(turns) {
		switch {
		case item.Type == "userMessage", item.Type == "steering",
			item.Description == goalContinuationItemDescription,
			item.EventKind == appwire.ThreadItemEventKindEnvironment,
			item.EventKind == appwire.ThreadItemEventKindContextCompaction,
			item.EventKind == appwire.ThreadItemEventKindCompaction,
			item.EventKind == appwire.ThreadItemEventKindRoundTimings:
			items = append(items, item)
		}
	}
	return items
}
