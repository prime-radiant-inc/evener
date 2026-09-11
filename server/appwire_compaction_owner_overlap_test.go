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
	t.Run("fold publishes under staging owner", testCompactionFoldPublishesUnderStagingOwner)
}

// preCompactSteeringPlugin installs a plugin whose PreCompact hook returns
// model context, so a fold injects a steering turn alongside its compaction
// metadata and its checkpoint/summary artifacts.
func preCompactSteeringPlugin(t *testing.T) string {
	t.Helper()
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
	return pluginDir
}

// overlapReplayItems keeps the items a compaction across a mutation boundary
// is judged by: the compaction metadata and artifacts, the steering a fold
// injects, and the round timings whose owners frame them.
func overlapReplayItems(turns []appwire.Turn) []replayItemIdentity {
	var items []replayItemIdentity
	for _, item := range replayItemIdentities(turns) {
		if item.EventKind == appwire.ThreadItemEventKindContextCompaction || item.EventKind == appwire.ThreadItemEventKindCompaction || item.EventKind == appwire.ThreadItemEventKindRoundTimings || item.Type == "steering" {
			items = append(items, item)
		}
	}
	return items
}

// overlapFixture stages a compaction that straddles a mutation boundary: a
// session on the scripted overlap provider, a live server fed from its event
// stream, and six seed turns of context behind it. published reports the event
// a case treats as the fold's last publication.
type overlapFixture struct {
	t         *testing.T
	session   *agent.Session
	adapter   *overlapAdapter
	server    *Server
	ctx       context.Context
	ends      chan struct{}
	published chan struct{}
}

func newOverlapFixture(t *testing.T, pluginDirs []string, published func(events.SessionEvent) bool) *overlapFixture {
	t.Helper()
	dir := t.TempDir()
	adapter := &overlapAdapter{mainStarted: make(chan int, 2), summaryStarted: make(chan struct{}, 1), releaseA: make(chan struct{}), releaseB: make(chan struct{}), releaseSummary: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	s, err := agent.NewSession(c, provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), "openai/fixture-summary"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir, PluginDirs: pluginDirs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	s.SetClientMutationStartWakeFunc(func() {})
	f := &overlapFixture{t: t, session: s, adapter: adapter, ctx: ctx, ends: make(chan struct{}, 32), published: make(chan struct{}, 1)}
	var live atomic.Pointer[Server]
	go s.ConsumeEventsLossless(func(e events.SessionEvent) {
		if srv := live.Load(); srv != nil {
			srv.RecordAppEvent(e)
		}
		if e.Kind == events.EventSessionEnd {
			f.ends <- struct{}{}
		}
		if published(e) {
			f.published <- struct{}{}
		}
	}, func() {})
	for i := range 6 {
		f.run(fmt.Sprintf("seed-%d", i))
	}
	f.server = NewServer(ServerConfig{})
	installTranscriptIdentity(t, f.server, s.ID(), s.TranscriptPath())
	live.Store(f.server)
	return f
}

func (f *overlapFixture) wait(signal <-chan struct{}) {
	f.t.Helper()
	select {
	case <-signal:
	case <-f.ctx.Done():
		f.t.Fatal(f.ctx.Err())
	}
}

func (f *overlapFixture) waitMainCall(want int) {
	f.t.Helper()
	select {
	case got := <-f.adapter.mainStarted:
		if got != want {
			f.t.Fatalf("main provider call=%d, want %d", got, want)
		}
	case <-f.ctx.Done():
		f.t.Fatal(f.ctx.Err())
	}
}

// run drives one client mutation from accept to session end.
func (f *overlapFixture) run(id string) {
	f.t.Helper()
	if _, err := f.session.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, ExpectedInstanceID: f.session.ID(), Input: []appwire.InputItem{{Type: "text", Text: id}}}); err != nil {
		f.t.Fatal(err)
	}
	if _, _, err := f.session.ProcessClientMutationStart(f.ctx, nil); err != nil {
		f.t.Fatal(err)
	}
	f.wait(f.ends)
}

func testCompactionAcrossMutationBoundary(t *testing.T, startNext, hookSteering bool) {
	var pluginDirs []string
	if hookSteering {
		pluginDirs = []string{preCompactSteeringPlugin(t)}
	}
	f := newOverlapFixture(t, pluginDirs, func(e events.SessionEvent) bool {
		if data, ok := e.Data.(events.CompactionTurnData); !hookSteering && e.Kind == events.EventCompactionTurn && ok && data.Kind == string(schema.TurnSummary) {
			return true
		}
		data, ok := e.Data.(events.SteeringInjectedData)
		return hookSteering && ok && data.Kind == events.SteeringKindPrecompactHook
	})
	s, a, srv, ctx := f.session, f.adapter, f.server, f.ctx
	ends, compactionPublished := f.ends, f.published
	wait, waitMainCall, run := f.wait, f.waitMainCall, f.run
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
	liveItems := overlapReplayItems(read.Thread.Turns)
	coldItems := overlapReplayItems(cold)
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
	if got := overlapReplayItems(paged); !reflect.DeepEqual(got, liveItems) {
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
	assertReplayItemParity(t, "turn after compaction", overlapReplayItems(after.Thread.Turns), overlapReplayItems(afterCold))
}

// A fold's owner is captured when staging begins and every publication path
// carries that owner: the live compaction metadata and injected steering, and
// the durable TurnContextCompaction, checkpoint/summary and steering records
// alike. This is the ordering where a publish-time owner lookup would differ
// from the staged one — mutation B opens while the fold staged under mutation
// A is still in flight, and that same attempt goes on to publish. B is
// accepted but deliberately not processed: a running turn's own ManageContext
// fold bumps the history revision, which makes foldWithForceCompact re-stage
// under B and hides the boundary the sibling cases were meant to cross.
//
// An event that arrives after its turn completed stays with its owner and does
// not move the current turn's lifecycle, so the whole compaction sequence
// belongs to mutation A on both projections — before mutation B runs and
// after it finishes.
func testCompactionFoldPublishesUnderStagingOwner(t *testing.T) {
	// The fold emits its injected steering last, so this signal means the
	// whole published sequence has reached the live projection.
	f := newOverlapFixture(t, []string{preCompactSteeringPlugin(t)}, func(e events.SessionEvent) bool {
		data, ok := e.Data.(events.SteeringInjectedData)
		return ok && data.Kind == events.SteeringKindPrecompactHook
	})
	s, a, srv, ctx := f.session, f.adapter, f.server, f.ctx
	ends, steeringPublished := f.ends, f.published
	wait, waitMainCall := f.wait, f.waitMainCall
	acceptedA, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "mutation-a", ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: "mutation-a"}}})
	if err != nil {
		t.Fatal(err)
	}
	wantOwner := acceptedA.Turn.ID
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
	acceptedB, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "mutation-b", ExpectedInstanceID: s.ID(), Input: []appwire.InputItem{{Type: "text", Text: "mutation-b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if acceptedB.Turn.ID == wantOwner {
		t.Fatalf("mutation B reused mutation A's turn id %s", wantOwner)
	}
	close(a.releaseSummary)
	if err := <-compactDone; err != nil {
		t.Fatal(err)
	}
	wait(steeringPublished)

	read, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	cold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	liveItems := overlapReplayItems(read.Thread.Turns)
	coldItems := overlapReplayItems(cold)
	// One round timing per completed main call, plus the compaction sequence:
	// two context-compaction metadata items, two checkpoint/summary artifacts,
	// and the PreCompact hook's steering.
	if expected := 5 + int(a.mainCalls.Load()); len(liveItems) != expected {
		t.Fatalf("live overlap published %d items, want %d: %#v", len(liveItems), expected, liveItems)
	}
	for _, item := range liveItems {
		if item.EventKind == appwire.ThreadItemEventKindRoundTimings {
			continue
		}
		if item.TurnID != wantOwner {
			t.Fatalf("compaction item %s owner=%s, want staging owner mutation A=%s", item.EventKind, item.TurnID, wantOwner)
		}
	}
	if !reflect.DeepEqual(liveItems, coldItems) {
		assertReplayItemParity(t, "staged owner replay", liveItems, coldItems)
	}

	// Mutation B now runs to completion. The published compaction sequence is
	// already durable, so both projections must still open on the same items,
	// under mutation A.
	bDone := make(chan error, 1)
	go func() { _, _, e := s.ProcessClientMutationStart(ctx, nil); bDone <- e }()
	waitMainCall(8)
	close(a.releaseB)
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	wait(ends)
	after, err := srv.handleAppThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + s.ID(), IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	afterCold, _, err := appTurnsFromTranscriptFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	afterItems := overlapReplayItems(after.Thread.Turns)
	assertReplayItemParity(t, "turn after staged owner compaction", afterItems, overlapReplayItems(afterCold))
	if len(afterItems) < len(liveItems) || !reflect.DeepEqual(afterItems[:len(liveItems)], liveItems) {
		assertReplayItemParity(t, "staged owner sequence after next mutation", liveItems, afterItems)
	}
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
