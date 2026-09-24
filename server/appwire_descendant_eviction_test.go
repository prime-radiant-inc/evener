package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

const (
	evictionRootPrompt  = "ROOT-EVICTION-PARITY"
	evictionChildTask   = "CHILD-EVICTION-PARITY"
	evictionSendPrompt  = "ROOT-EVICTION-SEND"
	evictionSendMessage = "RESUME-EVICTED-CHILD"
)

var evictionDelegateID = regexp.MustCompile(`dlg_[A-Za-z0-9_-]+`)

// evictionDelegateAdapter scripts one root that delegates once and a child
// that makes one real tool call before reporting. Everything below the LLM
// boundary -- the delegate runtime, the child's transcript, and the server's
// descendant projection -- is real.
type evictionDelegateAdapter struct {
	childFile string

	mu         sync.Mutex
	rootCalls  int
	childCalls int
	sent       bool
}

func (*evictionDelegateAdapter) Name() string { return "openai" }

func (*evictionDelegateAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *evictionDelegateAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	text := evictionRequestText(req)
	if len(req.Tools) == 0 {
		// Session naming is a toolless side call; it is not a script step.
		return llm.Response{Provider: "openai", Model: req.Model, Message: llm.Assistant("eviction parity")}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.Contains(text, evictionChildTask) && !strings.Contains(text, evictionRootPrompt) {
		a.childCalls++
		if a.childCalls == 1 {
			args, _ := json.Marshal(map[string]any{"file_path": a.childFile, "content": "child wrote this"})
			return evictionToolCall(req, llm.ToolCallData{ID: "child_write", Name: "write_file", Arguments: args, Type: "function"}), nil
		}
		return evictionCommunicate(req, "child finished"), nil
	}
	if strings.Contains(text, evictionSendPrompt) && !a.sent {
		a.sent = true
		args, _ := json.Marshal(map[string]any{"to": evictionDelegateID.FindString(text), "message": evictionSendMessage, "max_wait_ms": 30_000})
		return evictionToolCall(req, llm.ToolCallData{ID: "root_send", Name: "delegate_send", Arguments: args, Type: "function"}), nil
	}
	a.rootCalls++
	if a.rootCalls == 1 {
		args, _ := json.Marshal(map[string]any{"prompt": evictionChildTask})
		return evictionToolCall(req, llm.ToolCallData{ID: "root_delegate", Name: "delegate", Arguments: args, Type: "function"}), nil
	}
	return evictionCommunicate(req, "root finished"), nil
}

func evictionRequestText(req llm.Request) string {
	var b strings.Builder
	for _, message := range req.Messages {
		for _, part := range message.Content {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func evictionToolCall(req llm.Request, call llm.ToolCallData) llm.Response {
	return llm.Response{
		Provider: "openai",
		Model:    req.Model,
		Message:  llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}},
		Finish:   llm.FinishReason{Reason: llm.FinishReasonToolCalls},
	}
}

func evictionCommunicate(req llm.Request, message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": true,
		"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}},
	})
	return evictionToolCall(req, llm.ToolCallData{ID: "communicate_" + message, Name: "communicate", Arguments: args, Type: "function"})
}

// runEvictionDelegate drives a real root session through one delegate run,
// projecting the child onto srv exactly as cmd/evener/serve.go wires it, and
// returns the root and the child's thread ID once the whole job tree has
// quiesced.
func runEvictionDelegate(t *testing.T, srv *Server) (*agent.Session, string) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&evictionDelegateAdapter{childFile: filepath.Join(dir, "child.txt")})
	root, err := agent.NewSession(c, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(root.Close)
	srv.SetAppIdentity("local", root.ID())
	root.SetDescendantEventFunc(func(event events.SessionEvent) {
		srv.RecordDescendantAppEvent(root.ID(), event)
	})
	srv.SetDescendantTranscriptPathFunc(func(threadID string) string {
		return filepath.Join(dir, "sessions", threadID+".transcript.jsonl")
	})

	driveEvictionRoot(t, root, evictionRootPrompt)

	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if len(srv.appDescendants) != 1 {
		t.Fatalf("descendants = %d, want exactly the one delegate", len(srv.appDescendants))
	}
	for id := range srv.appDescendants {
		return root, id
	}
	return root, ""
}

// driveEvictionRoot runs one root input and waits for the whole job tree to
// quiesce.
func driveEvictionRoot(t *testing.T, root *agent.Session, input string) {
	t.Helper()
	// TRIPWIRE: scripted in-process adapter; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, input, nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if _, err := root.DrainJobTree(ctx); err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
}

func descendantProjectionForTest(t *testing.T, srv *Server, threadID string) *appDescendantProjection {
	t.Helper()
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	projection := srv.appDescendants[threadID]
	if projection == nil {
		t.Fatalf("no descendant projection for %s", threadID)
	}
	return projection
}

func descendantTurnsResident(t *testing.T, srv *Server, threadID string) bool {
	t.Helper()
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	projection := srv.appDescendants[threadID]
	if projection == nil {
		t.Fatalf("no descendant projection for %s", threadID)
	}
	return projection.turns != nil
}

// evictQuiescentDescendantsForTest runs the production eviction pass with a
// resident budget the test chooses, under the same gate production uses.
func evictQuiescentDescendantsForTest(srv *Server, keep int) {
	srv.appServer.CommitProjection(func() []appserver.SequencedNotification {
		srv.mu.Lock()
		srv.evictQuiescentDescendantsLocked(keep)
		srv.mu.Unlock()
		return nil
	})
}

func readDescendantTurns(srv *Server, threadID string, limit int) (appwire.ThreadReadResponse, error) {
	return srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          appwire.Ref{SourceID: "local", ThreadID: threadID}.String(),
		IncludeTurns: true,
		ItemLimit:    limit,
	})
}

// transcriptProjectionTurns is what a thread/read of threadID would answer from
// a snapshot freshly projected from its transcript file.
func transcriptProjectionTurns(t *testing.T, path, threadID string, limit int) []appwire.Turn {
	t.Helper()
	persisted, err := appTurnProjectionFromTranscriptFile(path)
	if err != nil {
		t.Fatalf("project %s: %v", path, err)
	}
	snapshot := &appTurnSnapshot{threadID: threadID}
	snapshot.Seed(appTurnSeed{Turns: persisted.turns, NextEntry: persisted.nextEntry})
	window, _, err := snapshot.LatestItemCandidates(limit)
	if err != nil {
		t.Fatalf("LatestItemCandidates: %v", err)
	}
	turns, err := appitempaging.RegroupTurnFragments(appitempaging.NormalizeProjectedItemCompleteness(window.Candidates))
	if err != nil {
		t.Fatalf("RegroupTurnFragments: %v", err)
	}
	return turns
}

type conversationItem struct {
	itemType string
	text     string
	output   string
}

// conversationItems is the persisted conversation a transcript carries: what
// the user said, what tools returned, and what the agent answered. Live-only
// diagnostics (prompt_loaded, round_timings) are never persisted.
func conversationItems(turns []appwire.Turn) []conversationItem {
	var out []conversationItem
	for _, turn := range turns {
		for _, item := range turn.Items {
			switch item.Type {
			case "userMessage", "agentMessage", "commandExecution":
				out = append(out, conversationItem{itemType: item.Type, text: item.Text, output: item.Output})
			}
		}
	}
	return out
}

// A rebuilt descendant snapshot cannot reproduce the live one: live-only system
// items are never persisted, so the same (entry, item) position names a
// different item after a rebuild. Rehydration therefore answers from the
// transcript projection under a fresh cursor incarnation, so a cursor minted
// against the live snapshot fails as stale instead of paging the wrong items.
func TestEvictedDescendantReadServesTranscriptProjectionUnderFreshIncarnation(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 1000})
	_, childID := runEvictionDelegate(t, srv)
	projection := descendantProjectionForTest(t, srv, childID)
	srv.mu.RLock()
	status, active := projection.thread.Status.Type, projection.activeTurnID
	srv.mu.RUnlock()
	if status == appwire.ThreadStatusActive || active != "" {
		t.Fatalf("finished delegate status=%q active=%q, want settled with no active turn", status, active)
	}

	live, err := readDescendantTurns(srv, childID, appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatalf("live read: %v", err)
	}
	liveWindow, err := readDescendantTurns(srv, childID, 1)
	if err != nil || liveWindow.OlderCursor == "" {
		t.Fatalf("live one-item window: cursor=%q err=%v, want an older cursor", liveWindow.OlderCursor, err)
	}

	evictQuiescentDescendantsForTest(srv, 0)
	if descendantTurnsResident(t, srv, childID) {
		t.Fatal("quiescent descendant still holds its turn snapshot after eviction")
	}

	rehydrated, err := readDescendantTurns(srv, childID, appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatalf("rehydrated read: %v", err)
	}
	if !descendantTurnsResident(t, srv, childID) {
		t.Fatal("read did not reinstall the descendant's turn snapshot")
	}
	if want := transcriptProjectionTurns(t, srv.appDescendantTranscriptPathFunc(childID), childID, appwire.TranscriptItemPageLimit); !reflect.DeepEqual(rehydrated.Thread.Turns, want) {
		t.Fatalf("rehydrated turns differ from the transcript projection\ngot:  %+v\nwant: %+v", rehydrated.Thread.Turns, want)
	}
	if got, want := conversationItems(rehydrated.Thread.Turns), conversationItems(live.Thread.Turns); len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("rehydrated conversation = %+v, want the live conversation %+v", got, want)
	}

	_, err = srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       appwire.Ref{SourceID: "local", ThreadID: childID}.String(),
		Cursor:    liveWindow.OlderCursor,
		ItemLimit: 1,
	})
	if !isStaleItemCursorError(err) {
		t.Fatalf("pre-eviction cursor after rehydration = %v, want typed stale error", err)
	}
}

// A delegate resumed after eviction has already persisted the message that
// resumes it by the time the event announcing it arrives, so the rebuild the
// event triggers must not also carry it: the reader would see it twice.
func TestEvictedDelegateResumedBySendShowsTheMessageOnce(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 1000})
	root, childID := runEvictionDelegate(t, srv)
	evictQuiescentDescendantsForTest(srv, 0)
	if descendantTurnsResident(t, srv, childID) {
		t.Fatal("quiescent delegate still resident after eviction")
	}

	driveEvictionRoot(t, root, evictionSendPrompt)

	read, err := readDescendantTurns(srv, childID, appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatalf("read resumed delegate: %v", err)
	}
	count := 0
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" && strings.Contains(item.Text, evictionSendMessage) {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("resumed delegate shows the resuming message %d times, want once; turns=%+v", count, read.Thread.Turns)
	}
}

// quiescentDescendantFixture drives synthetic descendants of one root whose
// transcripts live under dir/sessions, the layout cmd/evener/serve.go wires.
type quiescentDescendantFixture struct {
	t   *testing.T
	srv *Server
	dir string
}

func newQuiescentDescendantFixture(t *testing.T) *quiescentDescendantFixture {
	t.Helper()
	srv := NewServer(ServerConfig{AppReplaySize: 10000})
	srv.SetAppIdentity("local", "root")
	f := &quiescentDescendantFixture{t: t, srv: srv, dir: t.TempDir()}
	srv.SetDescendantTranscriptPathFunc(f.transcriptPath)
	return f
}

func (f *quiescentDescendantFixture) transcriptPath(threadID string) string {
	return filepath.Join(f.dir, "sessions", threadID+".transcript.jsonl")
}

// persist appends one user/assistant exchange per text to threadID's transcript.
func (f *quiescentDescendantFixture) persist(threadID string, texts ...string) {
	f.t.Helper()
	path := f.transcriptPath(threadID)
	var writer *transcript.Writer
	var err error
	if _, statErr := os.Stat(path); statErr == nil {
		writer, _, err = transcript.OpenWriterForSession(path, threadID)
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			f.t.Fatal(err)
		}
		writer, err = transcript.NewWriter(path, transcript.Header{SessionID: threadID})
	}
	if err != nil {
		f.t.Fatalf("open transcript %s: %v", path, err)
	}
	for _, text := range texts {
		for _, turn := range []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User(text)),
			schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer to "+text)),
		} {
			if err := writer.Append(turn); err != nil {
				f.t.Fatalf("append %s: %v", path, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		f.t.Fatalf("close %s: %v", path, err)
	}
}

func (f *quiescentDescendantFixture) startTurn(threadID, text string) {
	f.srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventUserInput, SessionID: threadID, Data: events.UserInputData{Text: text}})
}

func (f *quiescentDescendantFixture) finish(threadID string) {
	f.srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionEnd, SessionID: threadID, Data: events.SessionEndData{Reason: "input_complete", State: appwire.ThreadStatusIdle}})
}

// finishedDescendant persists history for threadID, then runs and finishes one
// live turn, leaving the descendant quiescent.
func (f *quiescentDescendantFixture) finishedDescendant(threadID string) {
	f.t.Helper()
	f.persist(threadID, "persisted "+threadID)
	f.startTurn(threadID, "live "+threadID)
	f.finish(threadID)
}

func (f *quiescentDescendantFixture) requireResident(threadID string, want bool) {
	f.t.Helper()
	if got := descendantTurnsResident(f.t, f.srv, threadID); got != want {
		f.t.Fatalf("%s resident = %v, want %v", threadID, got, want)
	}
}

func turnsContainText(turns []appwire.Turn, text string) bool {
	return countItemsWithText(turns, text) > 0
}

func countItemsWithText(turns []appwire.Turn, text string) int {
	count := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Text == text {
				count++
			}
		}
	}
	return count
}

func TestQuiescentDescendantTurnsKeepOnlyTheMostRecentlyUsed(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	// The running descendant is the oldest; running work is never evicted.
	f.persist("running", "persisted running")
	f.startTurn("running", "still working")
	ids := make([]string, appResidentQuiescentDescendants+2)
	for i := range ids {
		ids[i] = fmt.Sprintf("child-%02d", i)
		f.finishedDescendant(ids[i])
	}

	f.requireResident("running", true)
	f.requireResident(ids[0], false)
	f.requireResident(ids[1], false)
	for _, id := range ids[2:] {
		f.requireResident(id, true)
	}

	// Reading the oldest brings it back and pushes out the least recently used.
	read, err := readDescendantTurns(f.srv, ids[0], appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatalf("read %s: %v", ids[0], err)
	}
	if !turnsContainText(read.Thread.Turns, "persisted "+ids[0]) {
		t.Fatalf("rehydrated %s turns = %+v, want its persisted history", ids[0], read.Thread.Turns)
	}
	f.requireResident(ids[0], true)
	f.requireResident(ids[1], false)
	f.requireResident(ids[2], false)
	f.requireResident(ids[3], true)
}

func TestQuiescentDescendantWithoutTranscriptResolverIsNeverEvicted(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 1000})
	srv.SetAppIdentity("local", "root")
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventUserInput, SessionID: "child", Data: events.UserInputData{Text: "only in memory"}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "child", Data: events.SessionEndData{Reason: "input_complete", State: appwire.ThreadStatusIdle}})

	evictQuiescentDescendantsForTest(srv, 0)

	if !descendantTurnsResident(t, srv, "child") {
		t.Fatal("a descendant with no transcript to rebuild from lost its only copy of its turns")
	}
}

// A resumed delegate persists the input that resumes it before the event
// announcing it arrives, so the rebuild that event triggers covers only the
// transcript as it stood at eviction: the input then arrives once, live.
func TestEvictedDescendantResumeRebuildsTheTranscriptAsEvicted(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.persist("child", "persisted child")
	f.startTurn("child", "live child")
	// One live turn persists as several entries, so the transcript's turn ids
	// run past the ids the live projector has handed out.
	f.persist("child", "later one", "later two", "later three")
	f.finish("child")
	evictedAt, err := appTurnProjectionFromTranscriptFile(f.transcriptPath("child"))
	if err != nil {
		t.Fatal(err)
	}
	evictQuiescentDescendantsForTest(f.srv, 0)
	f.requireResident("child", false)

	f.persist("child", "resumed")
	f.startTurn("child", "resumed")
	f.requireResident("child", true)

	turns := f.srv.appAllTurns("child")
	if !turnsContainText(turns, "persisted child") || !turnsContainText(turns, "later three") {
		t.Fatalf("resumed turns = %+v, want the history persisted before eviction", turns)
	}
	if count := countItemsWithText(turns, "resumed"); count != 1 {
		t.Fatalf("resumed input appears %d times, want once: %+v", count, turns)
	}
	resumed := findItemByText(t, turns, "resumed")
	var ordinal int
	if _, err := fmt.Sscanf(resumed.TurnID, "turn_%d", &ordinal); err != nil || ordinal <= evictedAt.persistedEntries {
		t.Fatalf("resumed turn id %q, want one fenced above the transcript's persisted turn_%d", resumed.TurnID, evictedAt.persistedEntries)
	}
}

// Eviction discards the only in-memory copy, so a descendant whose transcript
// cannot be found when it settles stays resident.
func TestQuiescentDescendantWithoutTranscriptFileIsNeverEvicted(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.persist("child", "persisted child")
	f.startTurn("child", "live child")
	if err := os.Remove(f.transcriptPath("child")); err != nil {
		t.Fatal(err)
	}
	f.finish("child")

	evictQuiescentDescendantsForTest(f.srv, 0)

	f.requireResident("child", true)
}

// A resuming delegate persists its input before the event announcing it
// arrives. An eviction pass that runs in that window, from any other commit,
// must not fold the input into the prefix the rebuild projects: the input
// would then arrive a second time, live.
func TestDescendantEvictedBetweenPersistAndEmitShowsTheResumeOnce(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.finishedDescendant("child")
	f.persist("child", "resumed")
	evictQuiescentDescendantsForTest(f.srv, 0)
	f.requireResident("child", false)

	f.startTurn("child", "resumed")

	if count := countItemsWithText(f.srv.appAllTurns("child"), "resumed"); count != 1 {
		t.Fatalf("resumed input appears %d times, want once: %+v", count, f.srv.appAllTurns("child"))
	}
}

// An event for an evicted descendant whose transcript cannot be read installs
// an empty snapshot, since the event must land somewhere. That snapshot is
// evicted as soon as the descendant settles, whatever the resident budget, so
// the next read rebuilds the history the failed rebuild left out.
func TestDescendantWhoseRebuildFailedRecoversItsHistoryAfterSettling(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.finishedDescendant("child")
	evictQuiescentDescendantsForTest(f.srv, 0)
	path := f.transcriptPath("child")
	if err := os.Rename(path, path+".aside"); err != nil {
		t.Fatal(err)
	}
	f.startTurn("child", "resumed")
	if err := os.Rename(path+".aside", path); err != nil {
		t.Fatal(err)
	}
	f.finish("child")

	read, err := readDescendantTurns(f.srv, "child", appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatalf("read child: %v", err)
	}
	if !turnsContainText(read.Thread.Turns, "persisted child") {
		t.Fatalf("child turns = %+v, want the history the failed rebuild left out", read.Thread.Turns)
	}
}

// Items the live snapshot streamed but the transcript never persisted leave
// the rebuilt snapshot short of entries; a resumed turn must still order after
// them, since a subscriber that never re-read holds them.
func TestEvictedDescendantResumeOrdersAfterStreamedItems(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.finishedDescendant("child")
	projection := descendantProjectionForTest(t, f.srv, "child")
	f.srv.mu.RLock()
	projection.turns.mu.Lock()
	liveNextEntry := projection.turns.nextLiveEntry
	projection.turns.mu.Unlock()
	f.srv.mu.RUnlock()

	evictQuiescentDescendantsForTest(f.srv, 0)
	f.startTurn("child", "resumed")

	resumed := findItemByText(t, f.srv.appAllTurns("child"), "resumed")
	if resumed.Position == nil || resumed.Position.Entry < liveNextEntry {
		t.Fatalf("resumed item position = %+v, want an entry at or after %d", resumed.Position, liveNextEntry)
	}
}

func TestEvictedDescendantReadFailsWhenItsTranscriptIsGone(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.finishedDescendant("child")
	evictQuiescentDescendantsForTest(f.srv, 0)
	if err := os.Remove(f.transcriptPath("child")); err != nil {
		t.Fatal(err)
	}

	if _, err := readDescendantTurns(f.srv, "child", appwire.TranscriptItemPageLimit); err == nil {
		t.Fatal("read of an evicted descendant with no transcript answered as if its history were empty")
	}
	f.requireResident("child", false)
}

func TestPinnedDescendantTurnsSurviveEviction(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	f.finishedDescendant("child")
	release, err := f.srv.pinDescendantTurns("child")
	if err != nil {
		t.Fatal(err)
	}

	evictQuiescentDescendantsForTest(f.srv, 0)
	f.requireResident("child", true)

	release()
	evictQuiescentDescendantsForTest(f.srv, 0)
	f.requireResident("child", false)
}

// Reads, resumes, and eviction passes race freely; no read may observe a
// descendant without its history.
func TestDescendantEvictionRacesReadsAndResumes(t *testing.T) {
	f := newQuiescentDescendantFixture(t)
	ids := []string{"child-a", "child-b", "child-c"}
	for _, id := range ids {
		f.finishedDescendant(id)
	}
	const rounds = 50
	var wg sync.WaitGroup
	failures := make(chan string, len(ids)*rounds)
	for _, id := range ids {
		wg.Go(func() {
			for range rounds {
				read, err := readDescendantTurns(f.srv, id, appwire.TranscriptItemPageLimit)
				// Resumes push the persisted exchange out of the latest window,
				// so the proof of history is that the window is not empty.
				if err != nil || len(read.Thread.Turns) == 0 {
					failures <- fmt.Sprintf("%s read err=%v turns=%+v", id, err, read.Thread.Turns)
					return
				}
			}
		})
		wg.Go(func() {
			for i := range rounds {
				f.startTurn(id, fmt.Sprintf("resume %s %d", id, i))
				f.finish(id)
			}
		})
	}
	wg.Go(func() {
		for range rounds {
			evictQuiescentDescendantsForTest(f.srv, 0)
		}
	})
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}
