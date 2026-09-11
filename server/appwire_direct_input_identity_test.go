package server

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
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
	compactionOwner := ""
	compactionItems := 0
	for _, item := range liveItems {
		if item.EventKind != appwire.ThreadItemEventKindContextCompaction && item.EventKind != appwire.ThreadItemEventKindCompaction {
			continue
		}
		compactionItems++
		if compactionOwner == "" {
			compactionOwner = item.TurnID
		}
		if item.TurnID != compactionOwner {
			t.Fatalf("live compaction sequence split across turns %s and %s", compactionOwner, item.TurnID)
		}
	}
	if compactionItems != 4 {
		t.Fatalf("live compaction items = %d, want two metadata and two artifacts: %#v", compactionItems, liveItems)
	}
	if !reflect.DeepEqual(liveItems, coldItems) {
		assertReplayItemParity(t, "direct input compaction replay", liveItems, coldItems)
	}
}

// directInputReplayItems keeps the items whose turn identity these cases are
// about: the user's own message, the environment block that precedes it, the
// compaction sequence and the round timings that close each turn.
func directInputReplayItems(turns []appwire.Turn) []replayItemIdentity {
	var items []replayItemIdentity
	for _, item := range replayItemIdentities(turns) {
		switch {
		case item.Type == "userMessage", item.Type == "steering",
			item.EventKind == appwire.ThreadItemEventKindEnvironment,
			item.EventKind == appwire.ThreadItemEventKindContextCompaction,
			item.EventKind == appwire.ThreadItemEventKindCompaction,
			item.EventKind == appwire.ThreadItemEventKindRoundTimings:
			items = append(items, item)
		}
	}
	return items
}
