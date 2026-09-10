package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestCompactionOwnerDuringOverlappingMutations(t *testing.T) {
	t.Run("next mutation active", func(t *testing.T) { testCompactionAcrossMutationBoundary(t, true, false) })
	t.Run("mutation finished", func(t *testing.T) { testCompactionAcrossMutationBoundary(t, false, false) })
	t.Run("late compaction steering", func(t *testing.T) { testCompactionAcrossMutationBoundary(t, false, true) })
}

func testCompactionAcrossMutationBoundary(t *testing.T, startNext, hookSteering bool) {
	dir := t.TempDir()
	a := &overlapAdapter{mainStarted: make(chan int, 2), summaryStarted: make(chan struct{}, 1), releaseA: make(chan struct{}), releaseB: make(chan struct{}), releaseSummary: make(chan struct{})}
	c := llm.NewClient()
	c.Register(a)
	cfg := agent.SessionConfig{StateDir: dir}
	if hookSteering {
		pluginDir := t.TempDir()
		for _, child := range []string{".claude-plugin", "hooks"} {
			if err := os.MkdirAll(filepath.Join(pluginDir, child), 0755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), []byte(`{"name":"compaction-fixture"}`), 0600); err != nil {
			t.Fatal(err)
		}
		hooks := `{"hooks":{"PreCompact":[{"matcher":"*","hooks":[{"type":"command","command":"printf '%s\n' '{\"hookSpecificOutput\":{\"additionalContext\":\"compaction-steering-sentinel\"}}'"}]}]}}`
		if err := os.WriteFile(filepath.Join(pluginDir, "hooks", "hooks.json"), []byte(hooks), 0600); err != nil {
			t.Fatal(err)
		}
		cfg.PluginDirs = []string{pluginDir}
	}
	s, err := agent.NewSession(c, provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), "openai/fixture-summary"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	s.SetClientMutationStartWakeFunc(func() {})
	var live atomic.Pointer[Server]
	ends := make(chan struct{}, 32)
	compactionPublished := make(chan struct{}, 1)
	go s.ConsumeEventsLossless(func(e events.SessionEvent) {
		if srv := live.Load(); srv != nil {
			srv.RecordAppEvent(e)
		}
		if e.Kind == events.EventSessionEnd {
			ends <- struct{}{}
		}
		if data, ok := e.Data.(events.CompactionTurnData); !hookSteering && e.Kind == events.EventCompactionTurn && ok && data.Kind == string(schema.TurnSummary) {
			compactionPublished <- struct{}{}
		}
		if data, ok := e.Data.(events.SteeringInjectedData); hookSteering && ok && data.Kind == events.SteeringKindPrecompactHook {
			compactionPublished <- struct{}{}
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
	waitMainCall := func(want int) {
		t.Helper()
		select {
		case got := <-a.mainStarted:
			if got != want {
				t.Fatalf("main provider call=%d, want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	run := func(id string) {
		t.Helper()
		if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: id}}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ProcessClientMutationStart(ctx, nil); err != nil {
			t.Fatal(err)
		}
		wait(ends)
	}
	for i := range 6 {
		run(fmt.Sprintf("seed-%d", i))
	}
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, s.ID(), s.TranscriptPath())
	live.Store(srv)
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "mutation-a", ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: "mutation-a"}}}); err != nil {
		t.Fatal(err)
	}
	aDone := make(chan error, 1)
	go func() { _, _, e := s.ProcessClientMutationStart(ctx, nil); aDone <- e }()
	waitMainCall(7)
	a.enableA.Store(true)
	compactDone := make(chan error, 1)
	go func() { compactDone <- s.Compact(ctx) }()
	wait(a.summaryStarted)
	close(a.releaseA)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	wait(ends)
	wantOwner := ""
	bDone := make(chan error, 1)
	if startNext {
		acceptedB, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "mutation-b", ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: "mutation-b"}}})
		if err != nil {
			t.Fatal(err)
		}
		wantOwner = acceptedB.Turn.ID
		go func() { _, _, e := s.ProcessClientMutationStart(ctx, nil); bDone <- e }()
		waitMainCall(8)
	}
	close(a.releaseSummary)
	if err := <-compactDone; err != nil {
		t.Fatal(err)
	}
	wait(compactionPublished)
	if startNext {
		close(a.releaseB)
		if err := <-bDone; err != nil {
			t.Fatal(err)
		}
		wait(ends)
	}
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	allItems := func(turns []appwire.Turn) []replayItemIdentity {
		var items []replayItemIdentity
		for _, item := range replayItemIdentities(turns) {
			if item.EventKind == appwire.ThreadItemEventKindContextCompaction || item.EventKind == appwire.ThreadItemEventKindCompaction || item.EventKind == appwire.ThreadItemEventKindRoundTimings || item.Type == "steering" {
				items = append(items, item)
			}
		}
		return items
	}
	liveItems := allItems(read.Thread.Turns)
	coldItems := allItems(cold)
	expected := 4 + int(a.mainCalls.Load())
	if hookSteering {
		expected++
	}
	if len(liveItems) != expected {
		t.Fatalf("live overlap omitted compaction sequence: %#v", liveItems)
	}
	for _, item := range liveItems {
		if wantOwner != "" && item.EventKind != appwire.ThreadItemEventKindRoundTimings && item.TurnID != wantOwner {
			t.Fatalf("live compaction owner=%s, want mutation B=%s", item.TurnID, wantOwner)
		}
	}
	if !reflect.DeepEqual(liveItems, coldItems) {
		writer, entries, err := transcript.OpenWriterForSession(s.TranscriptPath(), s.ID())
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		for i, entry := range entries {
			t.Logf("record %d kind=%s stable=%s owner=%s", i, entry.Turn.Kind, entry.Turn.StableTurnID, entry.Turn.OwningTurnID)
		}
		assertReplayItemParity(t, "full overlap replay", liveItems, coldItems)
	}
	initial, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true, ItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	paged, cursor := initial.Thread.Turns, initial.OlderCursor
	for cursor != "" {
		page, err := srv.handleAppThreadTurnsList(ctx, appwire.ThreadTurnsListParams{Ref: "local:" + s.ID(), Cursor: cursor, ItemLimit: 1})
		if err != nil {
			t.Fatal(err)
		}
		paged, cursor = append(page.Data, paged...), page.NextCursor
	}
	if got := allItems(paged); !reflect.DeepEqual(got, liveItems) {
		assertReplayItemParity(t, "bounded overlap replay", liveItems, got)
	}
	// The next ordinary turn must allocate the same transcript coordinates
	// after recovery copies and announcements that arrived after a turn ended.
	if !startNext {
		close(a.releaseB)
	}
	run("after-compaction")
	after, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	afterCold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	assertReplayItemParity(t, "turn after compaction", allItems(after.Thread.Turns), allItems(afterCold))
}

type overlapAdapter struct {
	mainStarted                        chan int
	summaryStarted                     chan struct{}
	releaseA, releaseB, releaseSummary chan struct{}
	mainCalls                          atomic.Int32
	enableA                            atomic.Bool
}

func (*overlapAdapter) Name() string { return "openai" }
func (a *overlapAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Model == "fixture-summary" || !requestHasTool(req, "communicate") {
		if a.enableA.Load() && req.Model == "fixture-summary" {
			select {
			case a.summaryStarted <- struct{}{}:
			default:
			}
			select {
			case <-a.releaseSummary:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return llm.Response{Provider: a.Name(), Model: req.Model, Message: llm.Assistant("[CONTEXT SUMMARY]\nOverlap.\n[END SUMMARY]")}, nil
	}
	n := int(a.mainCalls.Add(1))
	if n >= 7 {
		a.mainStarted <- n
	}
	if n == 7 {
		select {
		case <-a.releaseA:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	if n == 8 {
		select {
		case <-a.releaseB:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	response := steeringOwnerCommunicateResponse(true, n)
	response.Provider = a.Name()
	response.Model = req.Model
	return response, nil
}
func (*overlapAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// Report identity coordinates without dumping generated checkpoint prose.
func assertReplayItemParity(t *testing.T, mode string, want, got []replayItemIdentity) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s item count=%d, want %d", mode, len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("%s item %d differs: kind=%s owner=%s key=%s, want kind=%s owner=%s key=%s (payloadEqual=%v)", mode, i, got[i].EventKind, got[i].TurnID, got[i].Key, want[i].EventKind, want[i].TurnID, want[i].Key, got[i].Text == want[i].Text && reflect.DeepEqual(got[i].Raw, want[i].Raw))
		}
	}
}
