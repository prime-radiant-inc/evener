package server

import (
	"context"
	"fmt"
	"strings"
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

// driveIdleCompaction runs six turns and then folds with the session idle, so
// no turn is running when the fold stages and its records land on a synthetic
// owner of their own.
func driveIdleCompaction(t *testing.T) (*Server, *agent.Session, context.Context) {
	t.Helper()
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
	return srv, s, ctx
}

// The synthetic owner only ever carries item completions, so the live store
// opens it like any unknown turn — InProgress — and nothing it receives closes
// it, while the cold projection stamps every grouped turn Completed. An idle
// fold's group could therefore read InProgress live and Completed after a
// reload, from the same records.
func TestIdleCompactionKeepsLiveAndColdTurnStatus(t *testing.T) {
	srv, s, ctx := driveIdleCompaction(t)

	read, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	status := func(turns []appwire.Turn) map[string]string {
		byID := make(map[string]string, len(turns))
		for _, turn := range turns {
			byID[turn.ID] = turn.Status
		}
		return byID
	}
	liveStatus, coldStatus := status(read.Thread.Turns), status(cold)
	gap := ""
	for id := range coldStatus {
		if strings.HasPrefix(id, "turn_compaction_") {
			gap = id
		}
	}
	if gap == "" {
		t.Fatalf("cold projection has no idle-fold group: %v", coldStatus)
	}
	if coldStatus[gap] != appwire.TurnStatusCompleted {
		t.Fatalf("cold idle-fold group %s status = %q, want %q", gap, coldStatus[gap], appwire.TurnStatusCompleted)
	}
	if liveStatus[gap] != coldStatus[gap] {
		t.Fatalf("idle-fold group %s is %q live and %q cold; the fold is over on both sides", gap, liveStatus[gap], coldStatus[gap])
	}
	// Nothing else may drift either: every turn both projections know about
	// reports the same status.
	for id, want := range coldStatus {
		got, ok := liveStatus[id]
		if !ok {
			continue
		}
		if got != want {
			t.Fatalf("turn %s status = %q live, %q cold", id, got, want)
		}
	}
}

// A fold with no turn running has no turn to own its records, but it still
// produces a contiguous run of them — the context-compaction metadata and the
// checkpoint/summary artifacts. Both projections have to put that run in ONE
// group, and they used to do it differently: the live projector coalesces
// ownerless announcements on a synthetic gap id it mints once per gap, while
// the transcript projection makes every unowned record a standalone group of
// its own entry index. The result is a different turn id and a different entry
// ordinal for every item once the session is reloaded.
func TestIdleCompactionKeepsLiveAndColdGrouping(t *testing.T) {
	srv, s, ctx := driveIdleCompaction(t)

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
	// The prefix is what proves the idle path ran: a fold staged under a
	// running turn would carry that turn's id (agent's compactionGapIDPrefix;
	// unexported, so named here).
	if !strings.HasPrefix(gapOwner, "turn_compaction_") {
		t.Fatalf("idle fold owner %s is not a compaction gap id; the fold did not stage idle", gapOwner)
	}
	assertReplayItemParity(t, "idle compaction replay", liveItems, coldItems)
}
