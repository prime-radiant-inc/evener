package server

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
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
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

// wireTranscriptHistory makes sess srv's served session the way serve does:
// the session's recorded-entry hooks first, then its identity with the length
// its writer has recorded, so every entry is either covered by that length or
// reaches the history through its hook; and its executions published through
// SetProcessingTurn.
func wireTranscriptHistory(t *testing.T, sess *agent.Session, srv *Server) {
	t.Helper()
	prepared, err := PrepareAppIdentity("local", sess.ID(), sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	srv.WireTranscriptHistory(sess)
	sess.SetExecutionStartedFunc(srv.SetProcessingTurn)
	srv.ReplaceAppIdentity(prepared.WithRecordedLength(sess.TranscriptRecordedLength()), nil)
}

// historyPublications wakes waiters on a thread history's publication,
// through threadHistoryPublishedHook. Tests using it must not run in
// parallel.
type historyPublications struct {
	mu     sync.Mutex
	cond   *sync.Cond
	failed bool
}

var (
	historyWatchersMu sync.Mutex
	historyWatchers   = map[*historyPublications]bool{}
)

func watchHistoryPublications(t *testing.T) *historyPublications {
	t.Helper()
	w := &historyPublications{}
	w.cond = sync.NewCond(&w.mu)
	historyWatchersMu.Lock()
	historyWatchers[w] = true
	wake := func(string, int64) {
		historyWatchersMu.Lock()
		defer historyWatchersMu.Unlock()
		for watcher := range historyWatchers {
			watcher.mu.Lock()
			watcher.cond.Broadcast()
			watcher.mu.Unlock()
		}
	}
	threadHistoryPublishedHook.Store(&wake)
	historyWatchersMu.Unlock()
	t.Cleanup(func() {
		historyWatchersMu.Lock()
		defer historyWatchersMu.Unlock()
		delete(historyWatchers, w)
		if len(historyWatchers) == 0 {
			threadHistoryPublishedHook.Store(nil)
		}
	})
	return w
}

// await returns once h has published through length.
func (w *historyPublications) await(t *testing.T, h *threadHistory, length int64) {
	t.Helper()
	// A tripwire, not the mechanism: the published hook wakes the wait.
	timer := time.AfterFunc(20*time.Second, func() {
		w.mu.Lock()
		w.failed = true
		w.mu.Unlock()
		w.cond.Broadcast()
	})
	defer timer.Stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		h.mu.Lock()
		published := h.published
		h.mu.Unlock()
		if published >= length {
			return
		}
		if w.failed {
			t.Fatalf("history published through %d, never reached %d", published, length)
		}
		w.cond.Wait()
	}
}

// historyClient reduces what a client receives: a read, then history/updated
// merged by key with the higher version winning.
type historyClient struct {
	items map[string]appwire.ThreadItem
	turns map[string]appwire.Turn
}

func newHistoryClient(read appwire.ThreadReadResponse) *historyClient {
	c := &historyClient{items: map[string]appwire.ThreadItem{}, turns: map[string]appwire.Turn{}}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			c.mergeItem(item)
		}
		turn.Items = nil
		c.turns[turn.ID] = turn
	}
	return c
}

// turnsInOrder is the held history as turns of items, both in position
// order, with the fields that describe a read's page rather than the turn
// (fragment view, earlier/later flags, cost) cleared.
func (c *historyClient) turnsInOrder() []appwire.Turn {
	items := make([]appwire.ThreadItem, 0, len(c.items))
	for _, item := range c.items {
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b appwire.ThreadItem) int { return comparePositions(a.Position, b.Position) })
	var turns []appwire.Turn
	index := map[string]int{}
	for _, item := range items {
		i, ok := index[item.TurnID]
		if !ok {
			turn := c.turns[item.TurnID]
			turn.ID = item.TurnID
			turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems, turn.Cost = "", false, false, ""
			turn.Items = nil
			i = len(turns)
			index[item.TurnID] = i
			turns = append(turns, turn)
		}
		turns[i].Items = append(turns[i].Items, item)
	}
	return turns
}

func comparePositions(a, b *appwire.ThreadItemPosition) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		default:
			return 1
		}
	}
	if c := cmp.Compare(a.Entry, b.Entry); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Item, b.Item); c != 0 {
		return c
	}
	return cmp.Compare(a.Sub, b.Sub)
}

func (c *historyClient) mergeItem(item appwire.ThreadItem) {
	if held, ok := c.items[item.TranscriptKey]; !ok || item.Version > held.Version {
		c.items[item.TranscriptKey] = item
	}
}

func (c *historyClient) apply(t *testing.T, update appwire.HistoryUpdatedParams) {
	t.Helper()
	for _, item := range update.Items {
		if item.TranscriptKey == "" {
			t.Fatalf("history/updated item %s has no key", item.ID)
		}
		c.mergeItem(item)
	}
	for _, turn := range update.Turns {
		if held, ok := c.turns[turn.ID]; !ok || turn.Version > held.Version {
			c.turns[turn.ID] = turn
		}
	}
}

func notificationParams[T any](t *testing.T, n appserver.SequencedNotification) T {
	t.Helper()
	var params T
	if err := json.Unmarshal(n.Notification.Params, &params); err != nil {
		t.Fatalf("%s params: %v", n.Notification.Method, err)
	}
	return params
}

// retiredHistoryMethods are the notifications v6 no longer sends: history
// comes from recorded entries only.
var retiredHistoryMethods = []string{
	appwire.NotifyTurnStarted, appwire.NotifyTurnCompleted,
	appwire.NotifyItemStarted, appwire.NotifyItemCompleted,
	appwire.NotifyAgentMessageDelta, appwire.NotifyEvenerSteeringInjected,
}

func TestHistorySwitchPublishesRecordedHistoryAndTheOverlay(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := &parityProvider{childRelease: make(chan struct{})}
	client := llm.NewClient()
	client.Register(script)
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
		StateDir:    stateDir,
		VisionModel: "off",
		LLMSleep:    noSleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.SetClientMutationStartWakeFunc(func() {})
	published := watchHistoryPublications(t)
	ps := bridgeParitySession(t, sess, stateDir)
	t.Cleanup(func() { ps.close(t) })
	srv := ps.srv
	history := srv.appHistoryForID(sess.ID())
	if history == nil {
		t.Fatal("the served session has no history")
	}
	initial, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: sess.ID(), IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil {
		t.Fatal(err)
	}

	// A turn with text, a tool call, a client steer mid-turn, and communicate.
	script.script(
		step(parityResponse(
			llm.ContentPart{Kind: llm.ContentText, Text: "Reading the notes."},
			parityCall("read-1", "read_file", map[string]any{"file_path": "notes.txt"}),
		)),
		func(llm.Request) (llm.Response, error) {
			if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
				ClientMutationID: "switch-steer", ExpectedInstanceID: sess.ID(),
				Input: []appwire.InputItem{{Type: "text", Text: "also count the words"}},
			}); err != nil {
				return llm.Response{}, err
			}
			return parityCommunicate("comm-1", "Working on it.", false), nil
		},
		step(parityCommunicate("comm-2", "The notes say hello, one word.", true)),
	)
	if _, err := sess.ProcessInput(context.Background(), "read my notes", nil); err != nil {
		t.Fatal(err)
	}
	ps.await(t, events.EventSessionEnd, nil)
	published.await(t, history, sess.TranscriptRecordedLength())

	notifications := srv.appNotifier.ReplayAfter(0, "")
	reduced := newHistoryClient(initial)
	var statusTurnID string
	statusSeq, firstHistorySeq := uint64(0), uint64(0)
	overlayRounds := map[string]string{} // round id -> last overlay method for it
	var overlayEnds []string
	for _, n := range notifications {
		method := n.Notification.Method
		if slices.Contains(retiredHistoryMethods, method) {
			t.Fatalf("committed retired notification %s: %s", method, n.Notification.Params)
		}
		switch method {
		case appwire.NotifyThreadStatusChanged:
			params := notificationParams[appwire.ThreadStatusChangedParams](t, n)
			if params.Status.Type == appwire.ThreadStatusActive {
				if params.ActiveTurnID == "" {
					t.Fatalf("active status without an activeTurnId: %s", n.Notification.Params)
				}
				if statusTurnID == "" {
					statusTurnID, statusSeq = params.ActiveTurnID, n.Seq
				}
			} else if params.ActiveTurnID != "" {
				t.Fatalf("%s status carries activeTurnId %q", params.Status.Type, params.ActiveTurnID)
			}
		case appwire.NotifyHistoryUpdated:
			update := notificationParams[appwire.HistoryUpdatedParams](t, n)
			if update.ThreadID != sess.ID() || update.Ref != "local:"+sess.ID() {
				t.Fatalf("history/updated for %s %s, want the served session", update.ThreadID, update.Ref)
			}
			for _, item := range update.Items {
				if statusTurnID == "" || item.TurnID == statusTurnID {
					if firstHistorySeq == 0 {
						firstHistorySeq = n.Seq
					}
				}
			}
			reduced.apply(t, update)
		case appwire.NotifyOverlayUpserted:
			params := notificationParams[appwire.OverlayUpsertedParams](t, n)
			if params.Item.RoundID != "" {
				overlayRounds[params.Item.RoundID] = method
			}
		case appwire.NotifyOverlayEnd:
			params := notificationParams[appwire.OverlayEndParams](t, n)
			overlayRounds[params.RoundID] = method
			overlayEnds = append(overlayEnds, params.RoundID)
		}
	}

	if statusTurnID == "" || firstHistorySeq == 0 || statusSeq > firstHistorySeq {
		t.Fatalf("active status for %q at seq %d, first history/updated of the turn at seq %d: the status must come first", statusTurnID, statusSeq, firstHistorySeq)
	}
	if len(overlayEnds) == 0 {
		t.Fatal("no overlay/end was committed for the turn's rounds")
	}
	for roundID, last := range overlayRounds {
		if last != appwire.NotifyOverlayEnd {
			t.Fatalf("round %s's overlay notifications end with %s, want overlay/end", roundID, last)
		}
	}

	final, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: sess.ID(), IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if final.OlderCursor != "" {
		t.Fatal("the read did not return the whole history")
	}
	readItems := map[string]appwire.ThreadItem{}
	turnIDs := map[string]bool{}
	for _, turn := range final.Thread.Turns {
		turnIDs[turn.ID] = true
		for _, item := range turn.Items {
			readItems[item.TranscriptKey] = item
		}
	}
	if !reflect.DeepEqual(reduced.items, readItems) {
		t.Fatalf("reduced history/updated items differ from a fresh read:\nreduced %s\nread    %s", describeItems(reduced.items), describeItems(readItems))
	}
	if !turnIDs[statusTurnID] {
		t.Fatalf("the published running turn %q is not a turn of the read %v", statusTurnID, turnIDs)
	}
	var steering *appwire.ThreadItem
	for _, item := range readItems {
		if item.ClientMutationID == "switch-steer" {
			steering = &item
		}
	}
	if steering == nil || steering.Type != "steering" || steering.ID == "" || steering.ID == "switch-steer" || steering.TranscriptKey == "" {
		t.Fatalf("the client steer's history item = %+v, want a steering item with the mutation id and a server-derived id", steering)
	}
	if final.Snapshot == nil || final.Snapshot.Length != sess.TranscriptRecordedLength() {
		t.Fatalf("read snapshot %+v, want the recorded length %d", final.Snapshot, sess.TranscriptRecordedLength())
	}
}

func describeItems(items map[string]appwire.ThreadItem) string {
	keys := make([]string, 0, len(items))
	for key, item := range items {
		keys = append(keys, key+"="+item.Type+"@"+strings.TrimSpace(item.Text))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n        ")
}

// servedTranscript is a server serving one thread from a real transcript
// writer whose recorded-entry hook is wired as serve wires a session's.
type servedTranscript struct {
	srv       *Server
	writer    *transcript.Writer
	path      string
	threadID  string
	published *historyPublications
}

// newServedTranscript serves threadID from a fresh transcript holding turns.
func newServedTranscript(t *testing.T, srv *Server, threadID string, turns ...schema.Turn) *servedTranscript {
	t.Helper()
	path := filepath.Join(t.TempDir(), threadID+".transcript.jsonl")
	writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: threadID, CreatedAt: time.Now(), ProfileID: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	t.Cleanup(func() { _ = writer.Close() })
	t.Cleanup(srv.Close)
	st := &servedTranscript{srv: srv, writer: writer, path: path, threadID: threadID, published: watchHistoryPublications(t)}
	for _, turn := range turns {
		st.record(t, turn)
	}
	prepared, err := PrepareAppIdentity("local", threadID, path)
	if err != nil {
		t.Fatal(err)
	}
	writer.OnRecorded(func(rec transcript.Record) { srv.recordTranscriptEntry(threadID, rec) })
	srv.ReplaceAppIdentity(prepared.WithRecordedLength(writer.RecordedLength()), nil)
	return st
}

func (st *servedTranscript) record(t *testing.T, turn schema.Turn) transcript.Record {
	t.Helper()
	rec, err := st.writer.Record(turn, transcript.RecordOptions{})
	if err != nil || !rec.Recorded {
		t.Fatalf("record %s = %+v, %v", turn.Kind, rec, err)
	}
	return rec
}

// settle returns once the thread's history has published every recorded
// entry.
func (st *servedTranscript) settle(t *testing.T) {
	t.Helper()
	history := st.srv.appHistoryForID(st.threadID)
	if history == nil {
		t.Fatal("the served thread has no history")
	}
	st.published.await(t, history, st.writer.RecordedLength())
}

// read is a thread/read of the latest window with turns.
func (st *servedTranscript) read(t *testing.T) appwire.ThreadReadResponse {
	t.Helper()
	response, err := st.srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: st.threadID, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// readTexts is every item text of a read, in order.
func readTexts(response appwire.ThreadReadResponse) []string {
	var texts []string
	for _, turn := range response.Thread.Turns {
		for _, item := range turn.Items {
			texts = append(texts, item.Text)
		}
	}
	return texts
}

// threadEvent is the session event data carries, emitted by threadID.
func threadEvent(threadID string, data events.EventData) events.SessionEvent {
	ev := events.New(data)
	ev.SessionID = threadID
	return ev
}

// inExecution stamps turn as a new-format entry of execution turnID, the way
// the session's writer does; opens marks the execution's first entry.
func inExecution(turnID string, opens bool, turn schema.Turn) schema.Turn {
	turn.Format = schema.TurnFormatIdentity
	turn.TurnID = turnID
	if opens {
		turn.TurnKind = schema.TurnSpanExecution
	}
	return turn
}

// startExecution is what serve's wiring does when threadID's session starts
// execution turnID: SetProcessingTurn from the session loop, then the
// execution's EXECUTION_STARTED through the bridge.
func startExecution(srv *Server, threadID, turnID string) {
	srv.SetProcessingTurn(turnID)
	srv.RecordAppEvent(threadEvent(threadID, events.ExecutionStartedData{TurnID: turnID}))
}

// A descendant's recorded entries reach its own history through the owner's
// descendant hook, published to the descendant; a record from a tree whose
// root is no longer served creates nothing.
func TestDescendantRecordedEntriesPublishToTheDescendant(t *testing.T) {
	root := newServedTranscript(t, NewServer(ServerConfig{}), "root")
	srv := root.srv
	childPath := filepath.Join(t.TempDir(), "child.transcript.jsonl")
	srv.SetDescendantTranscriptPathFunc(func(threadID string) string {
		if threadID == "child" {
			return childPath
		}
		return ""
	})
	child, err := transcript.NewWriterNoSync(childPath, transcript.Header{SessionID: "child"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Close() })
	child.OnRecorded(func(rec transcript.Record) { srv.recordDescendantTranscriptEntry("root", "child", rec) })
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionStartData{}))
	history := srv.appHistoryForID("child")
	if history == nil {
		t.Fatal("the descendant has no history")
	}

	rec, err := child.Record(schema.NewTurn(schema.TurnUserInput, llm.User("child work")), transcript.RecordOptions{})
	if err != nil || !rec.Recorded {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	root.published.await(t, history, child.RecordedLength())
	published := false
	for _, n := range srv.AppNotificationsAfter(0, "child") {
		if n.Notification.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		update := notificationParams[appwire.HistoryUpdatedParams](t, n)
		if update.ThreadID != "child" || update.Ref != "local:child" {
			t.Fatalf("descendant history/updated for %s %s", update.ThreadID, update.Ref)
		}
		for _, item := range update.Items {
			published = published || item.Text == "child work"
		}
	}
	if !published {
		t.Fatal("the descendant's recorded entry was not published to it")
	}

	srv.recordDescendantTranscriptEntry("old-root", "stranger", rec)
	if srv.appHistoryForID("stranger") != nil {
		t.Fatal("a record from a tree no longer served created a history")
	}
}

// Replacing the served identity under the same ref (thread/clear) drops the
// old root's and every descendant's history, and resyncs subscribers to the
// new history at an epoch past the old one, so the ref's epochs only
// increase.
func TestReplaceUnderTheSameRefResyncsPastTheOldEpoch(t *testing.T) {
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	oldPath := writeDelegateTranscript(t, "old", "old history")
	prepared, err := PrepareAppIdentityForRef("local", "old", "local:workspace", oldPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	old := srv.appHistoryForID("old")
	childPath := writeDelegateTranscript(t, "child", "child history")
	srv.SetDescendantTranscriptPathFunc(func(string) string { return childPath })
	srv.RecordDescendantAppEvent("old", threadEvent("child", events.SessionStartData{}))
	child := srv.appHistoryForID("child")
	if old == nil || child == nil {
		t.Fatal("the old root or its descendant has no history")
	}

	cursor := srv.appNotifier.CurrentSequence()
	newPath := writeDelegateTranscript(t, "new", "new history")
	prepared, err = PrepareAppIdentityForRef("local", "new", "local:workspace", newPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared, nil)

	if !old.stopping() || !child.stopping() || srv.appHistoryForID("old") != nil || srv.appHistoryForID("child") != nil {
		t.Fatal("the replaced root's and descendant's histories were not dropped")
	}
	fresh := srv.appHistoryForID("new")
	if fresh == nil || fresh.Epoch() <= old.Epoch() {
		t.Fatalf("new history %v at epoch %d, want one past the old epoch %d", fresh, fresh.Epoch(), old.Epoch())
	}
	var resync *appwire.ThreadResyncParams
	for _, n := range srv.AppNotificationsAfter(cursor, "local:workspace") {
		if n.Notification.Method == appwire.NotifyEvenerThreadResync {
			params := notificationParams[appwire.ThreadResyncParams](t, n)
			resync = &params
		}
	}
	if resync == nil || resync.ThreadID != "new" || resync.Epoch != fresh.Epoch() {
		t.Fatalf("resync = %+v, want the new thread at its history's epoch %d", resync, fresh.Epoch())
	}
	if got := readTexts(srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:workspace", IncludeTurns: true})); len(got) != 1 || got[0] != "new history" {
		t.Fatalf("read after the replacement = %q, want the new history alone", got)
	}
}
