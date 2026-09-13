package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A goal continuation whose mint is refused because a client mutation already
// holds the durable slot (turnNameHeld) opens under a name it minted for
// itself. The records it publishes while it runs -- its round timings, its
// steering, any compaction it triggers -- belong to the turn that is actually
// executing, not to the reserved-but-unstarted mutation that happens to own
// the slot. Both projections have to say so, or the continuation's own entry
// names one turn while everything it produced names another.
func TestHeldContinuationOwnsItsOwnRecords(t *testing.T) {
	dir := t.TempDir()
	adapter := &environmentReplayAdapter{mutationProjectionAdapter{blockAt: 100}}
	c := llm.NewClient()
	c.Register(adapter)
	s, err := agent.NewSession(c, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	s.SetClientMutationStartWakeFunc(func() {})
	var live atomic.Pointer[Server]
	ends := make(chan struct{}, 32)
	// ConsumeEventsLossless is what makes the session daemon-served, so
	// mintRunningTurnID reaches the durable slot instead of refusing outright.
	go s.ConsumeEventsLossless(func(e events.SessionEvent) {
		if srv := live.Load(); srv != nil {
			srv.RecordAppEvent(e)
		}
		if e.Kind == events.EventSessionEnd {
			ends <- struct{}{}
		}
	}, func() {})
	wait := func() {
		t.Helper()
		select {
		case <-ends:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, s.ID(), s.TranscriptPath())
	live.Store(srv)
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "warmup", ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: "warmup"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := s.ProcessClientMutationStart(ctx, nil); err != nil || !ran {
		t.Fatalf("warmup: ran=%v err=%v", ran, err)
	}
	wait()
	// Accepted, never processed: the durable slot is held by a turn that is
	// not the one about to run.
	held, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "held", ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: "held"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInputKind(ctx, "continue toward the goal", nil, agent.EntryContinuation); err != nil {
		t.Fatalf("held continuation: %v", err)
	}
	wait()

	read, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	liveItems := directInputReplayItems(read.Thread.Turns)
	continuationTurn := ""
	for _, item := range liveItems {
		if item.Description == goalContinuationItemDescription {
			continuationTurn = item.TurnID
		}
	}
	if continuationTurn == "" {
		t.Fatalf("replay has no goal continuation item: %#v", liveItems)
	}
	if continuationTurn == held.Turn.ID {
		t.Fatalf("continuation adopted the held mutation's reserved id %s", held.Turn.ID)
	}
	// The prefix is what proves the held path ran: a continuation whose mint
	// succeeded would carry a client-mutation id instead (agent's
	// directTurnIDPrefix; unexported, so named here).
	if !strings.HasPrefix(continuationTurn, "turn_direct_") {
		t.Fatalf("continuation turn %s is not self-minted; mintRunningTurnID was not refused, so this is not the turnNameHeld path", continuationTurn)
	}
	// Everything the continuation published after it opened belongs to it.
	seenContinuation := false
	published := 0
	for _, item := range liveItems {
		if item.Description == goalContinuationItemDescription {
			seenContinuation = true
			continue
		}
		if !seenContinuation {
			continue
		}
		published++
		if item.TurnID != continuationTurn {
			t.Fatalf("record published during the continuation (%s) owner=%s, want the executing turn %s", item.EventKind, item.TurnID, continuationTurn)
		}
	}
	if published == 0 {
		t.Fatalf("continuation published no records to attribute: %#v", liveItems)
	}
	assertReplayItemParity(t, "held continuation replay", liveItems, directInputReplayItems(cold))
}
