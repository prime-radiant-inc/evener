package server

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A fold with no turn running has no turn to own its records, but it still
// produces a contiguous run of them — the context-compaction metadata and the
// checkpoint/summary artifacts. Both projections have to put that run in ONE
// group, and they used to do it differently: the live projector coalesces
// ownerless announcements on a synthetic gap id it mints once per gap, while
// the transcript projection makes every unowned record a standalone group of
// its own entry index. The result is a different turn id and a different entry
// ordinal for every item once the session is reloaded.
func TestIdleCompactionKeepsLiveAndColdGrouping(t *testing.T) {
	dir := t.TempDir()
	a := &overlapAdapter{mainStarted: make(chan int, 8), summaryStarted: make(chan struct{}, 1), releaseA: make(chan struct{}), releaseB: make(chan struct{}), releaseSummary: make(chan struct{})}
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
	compacted := make(chan struct{}, 4)
	go s.ConsumeEventsLossless(func(e events.SessionEvent) {
		if srv := live.Load(); srv != nil {
			srv.RecordAppEvent(e)
		}
		if e.Kind == events.EventSessionEnd {
			ends <- struct{}{}
		}
		if data, ok := e.Data.(events.CompactionTurnData); ok && e.Kind == events.EventCompactionTurn && data.Kind == string(schema.TurnSummary) {
			compacted <- struct{}{}
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
	// The session is idle: no turn is running when this fold stages.
	if err := s.Compact(ctx); err != nil {
		t.Fatalf("idle compact: %v", err)
	}
	wait(compacted)

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
	lastUserTurn := ""
	gapOwner := ""
	gapItems := 0
	for _, item := range liveItems {
		if item.Type == "userMessage" {
			lastUserTurn = item.TurnID
			continue
		}
		if item.EventKind != appwire.ThreadItemEventKindContextCompaction && item.EventKind != appwire.ThreadItemEventKindCompaction {
			continue
		}
		gapItems++
		if gapOwner == "" {
			gapOwner = item.TurnID
		}
		if item.TurnID != gapOwner {
			t.Fatalf("idle fold split across turns %s and %s: %#v", gapOwner, item.TurnID, liveItems)
		}
	}
	if gapItems != 4 {
		t.Fatalf("live idle compaction items = %d, want two metadata and two artifacts: %#v", gapItems, liveItems)
	}
	if gapOwner == lastUserTurn {
		t.Fatalf("idle fold was attributed to the preceding turn %s; no turn was running", lastUserTurn)
	}
	assertReplayItemParity(t, "idle compaction replay", liveItems, coldItems)
}
