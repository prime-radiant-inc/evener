package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/llm"
)

// seedTranscriptServer writes a transcript with `pairs` user/assistant
// exchanges and returns a daemon Server seeded from it.
func seedTranscriptServer(t *testing.T, pairs int) *Server {
	t.Helper()
	srv, _ := seedTranscriptServerPath(t, pairs)
	return srv
}

func seedTranscriptServerPath(t *testing.T, pairs int) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1", CreatedAt: time.Now(), ProfileID: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for range pairs {
		if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("in"))); err != nil {
			t.Fatalf("append user: %v", err)
		}
		if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("out"))); err != nil {
			t.Fatalf("append assistant: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)
	srv.SetSteerFunc(func(string) {})
	srv.SetCancelFunc(func() {})
	return srv, path
}

// installTranscriptIdentity seeds srv from a real transcript the way production
// serve does: project once, then publish.
func installTranscriptIdentity(t testing.TB, srv *Server, threadID, path string) {
	t.Helper()
	prepared, err := PrepareAppIdentity("local", threadID, path)
	if err != nil {
		t.Fatalf("PrepareAppIdentity(%s): %v", path, err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
}

func readTurns(t *testing.T, conn *appserver.Connection, params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	t.Helper()
	msg := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(99), appwire.MethodThreadRead, params))
	out, ok := msg.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("thread/read result=%T (%+v)", msg.Response.Result, msg)
	}
	return out
}

func listTurns(t *testing.T, conn *appserver.Connection, params appwire.ThreadTurnsListParams) appwire.ThreadTurnsListResponse {
	t.Helper()
	msg := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(99), appwire.MethodThreadTurnsList, params))
	out, ok := msg.Response.Result.(appwire.ThreadTurnsListResponse)
	if !ok {
		t.Fatalf("thread/turns/list result=%T (%+v)", msg.Response.Result, msg)
	}
	return out
}

func turnIDs(turns []appwire.Turn) []string {
	out := make([]string, len(turns))
	for i, tn := range turns {
		out[i] = tn.ID
	}
	return out
}

func TestDaemonThreadReadWindowsAndTurnsListPagesToHead(t *testing.T) {
	srv := seedTranscriptServer(t, 4)
	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))

	// Baseline: the full, unbounded transcript.
	all := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true}).Thread.Turns
	n := len(all)
	if n < 3 {
		t.Fatalf("need >=3 turns to exercise paging, got %d", n)
	}

	// Bounded read returns the latest turn and an older-cursor.
	win := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, TurnLimit: 1})
	if len(win.Thread.Turns) != 1 || win.Thread.Turns[0].ID != all[n-1].ID {
		t.Fatalf("windowed read = %v, want latest turn %q", turnIDs(win.Thread.Turns), all[n-1].ID)
	}
	if win.OlderCursor == "" {
		t.Fatal("windowed read must set OlderCursor when turns remain")
	}

	// Walk older pages from the window's cursor; collect IDs newest→oldest.
	var walked []string
	cursor := win.OlderCursor
	for cursor != "" {
		page := listTurns(t, conn, appwire.ThreadTurnsListParams{Ref: "local:th_1", Cursor: cursor, Limit: 1})
		if len(page.Data) != 1 {
			t.Fatalf("page at cursor %q = %d turns, want 1", cursor, len(page.Data))
		}
		walked = append(walked, page.Data[0].ID)
		cursor = page.NextCursor
	}

	// The window (latest turn) plus the walked older pages must reconstruct the
	// whole transcript, in order.
	got := append([]string{win.Thread.Turns[0].ID}, walked...)
	if len(got) != n {
		t.Fatalf("reconstructed %d turns, want %d", len(got), n)
	}
	for i, id := range got {
		want := all[n-1-i].ID // got is newest→oldest
		if id != want {
			t.Fatalf("position %d = %q, want %q (full order: %v)", i, id, want, turnIDs(all))
		}
	}
}

// TestDaemonTranscriptPreparationPropagatesUnsupportedFormat pins where a
// transcript the daemon cannot read is now reported: preparation, before
// anything is published. A read cannot report it, because a read no longer
// opens the file.
func TestDaemonTranscriptPreparationPropagatesUnsupportedFormat(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "version one", body: `{"kind":"header","format_version":1,"session_id":"th_1"}` + "\n"},
		{name: "empty", body: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareAppIdentity("local", "th_1", path); !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("PrepareAppIdentity = %v, want ErrUnsupportedFormat", err)
			}
		})
	}
}

// TestServerAppWireBoundedReadsWindowOneInstalledSlice pins invariant 1: the
// unbounded read, the latest window, and an older page are three views of the
// SAME installed slice. They used to be three independent derivations -- two of
// them re-reading a file that could have moved between them -- which is how a
// window and the page below it could disagree about what the thread contains.
func TestServerAppWireBoundedReadsWindowOneInstalledSlice(t *testing.T) {
	srv := seedTranscriptServer(t, 100)
	conn := srv.AppServer().NewConnection("bounded-work")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))

	all := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true}).Thread.Turns
	if len(all) != 200 {
		t.Fatalf("full transcript has %d turns, want 200", len(all))
	}
	wantLatest, wantCursor := appwire.WindowTurns(all, 40)
	wantPage := appwire.PageTurns(all, wantCursor, 30)

	latest := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, TurnLimit: 40})
	if !reflect.DeepEqual(latest.Thread.Turns, wantLatest) || latest.OlderCursor != wantCursor {
		t.Fatalf("latest window = %v (cursor %q), want %v (cursor %q)", turnIDs(latest.Thread.Turns), latest.OlderCursor, turnIDs(wantLatest), wantCursor)
	}
	page := listTurns(t, conn, appwire.ThreadTurnsListParams{Ref: "local:th_1", Cursor: wantCursor, Limit: 30})
	if !reflect.DeepEqual(page, wantPage) {
		t.Fatalf("older page = %v, want %v", turnIDs(page.Data), turnIDs(wantPage.Data))
	}
}

// TestServerAppWireNotifierEvictionDoesNotTruncateMaterializedSnapshot pins the
// inverted contract this design turns on. The notifier's replay buffer is a
// bounded REPLAY window -- how far a reconnecting subscriber can catch up from
// deltas -- not the authority for what the thread contains. Rebuilding turn
// state from the retained suffix made a long conversation lose its own
// beginning the moment the buffer wrapped: the pane showed a thread that
// started in the middle.
//
// The materialized snapshot accumulates every committed notification, so
// eviction changes replay availability and nothing else.
func TestServerAppWireNotifierEvictionDoesNotTruncateMaterializedSnapshot(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 5})
	srv.SetAppIdentity("local", "th_1")
	for _, text := range []string{"first", "second", "third"} {
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: text}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: text + " reply"}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	}

	// The replay buffer has long since wrapped past the first turn.
	if replay := srv.AppNotificationsAfter(0, "th_1"); len(replay) > 5 {
		t.Fatalf("replay window = %d records, want the bounded 5 that make this test meaningful", len(replay))
	}

	got, gotCursor := srv.appLatestTurns("th_1", 40)
	var foundFirst bool
	for _, turn := range got {
		for _, item := range turn.Items {
			if item.Text == "first" || item.Text == "first reply" {
				foundFirst = true
			}
		}
	}
	if !foundFirst {
		t.Fatalf("notifier eviction truncated the materialized snapshot; turns = %v", turnIDs(got))
	}
	if len(got) != 3 {
		t.Fatalf("turns = %v, want all three turns regardless of replay eviction", turnIDs(got))
	}
	if gotCursor != "" {
		t.Fatalf("cursor = %q, want empty when the whole thread fits the window", gotCursor)
	}
}

// TestServerAppWireInstalledSnapshotNeedsNoTranscriptReads proves the daemon
// answers bounded turn reads from memory. Read-time transcript I/O is what let
// a subscribing hydration observe entries the matching live event had not yet
// projected, which is the duplicate-item race this design removes; it is also
// per-request file work on the hot path.
func TestServerAppWireInstalledSnapshotNeedsNoTranscriptReads(t *testing.T) {
	srv, path := seedTranscriptServerPath(t, 3)
	installed := srv.appAllTurns("th_1")
	if len(installed) != 6 {
		t.Fatalf("installed turns = %v, want the transcript's 6", turnIDs(installed))
	}

	// Take the file away. The daemon claims it needs no transcript I/O to
	// answer a read, so removing the file must change nothing. This catches a
	// regression that reintroduces FULL projection at read time, which the
	// observer below cannot see -- it instruments only the bounded index
	// readers.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}

	var reads int
	restore := apptranscript.InstallReadObserverForTesting(func(apptranscript.ReadStats) { reads++ })
	t.Cleanup(restore)

	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{
		Ref:          "local:th_1",
		Subscribe:    true,
		IncludeTurns: true,
		TurnLimit:    40,
	})
	if !reflect.DeepEqual(read.Thread.Turns, installed) {
		t.Fatalf("thread/read = %v, want the installed %v", turnIDs(read.Thread.Turns), turnIDs(installed))
	}
	page := srv.appPageTurns("th_1", "1", 30)
	if !reflect.DeepEqual(page, appwire.PageTurns(installed, "1", 30)) {
		t.Fatalf("thread/turns/list = %v, want the installed page", turnIDs(page.Data))
	}

	if reads != 0 {
		t.Fatalf("bounded turn reads performed %d transcript read(s); the installed snapshot must answer from memory", reads)
	}
}

func appendTranscriptTurns(t *testing.T, path string, count int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck // test fixture writer
	for i := range count {
		line, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: 100 + i, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("appended"))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func recordNotificationTurns(t *testing.T, srv *Server, count int) {
	t.Helper()
	for range count {
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "notification"}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "reply"}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	}
}

// TestServerAppWireLaterTranscriptWritesCannotReachAnInstalledSnapshot pins the
// seed-once rule. The transcript keeps growing under a live session; if a read
// re-derived from it, the daemon would answer with entries whose matching
// notifications are still in flight. The snapshot advances by notification
// only, so what lands on disk after preparation is invisible until the event
// that wrote it commits.
func TestServerAppWireLaterTranscriptWritesCannotReachAnInstalledSnapshot(t *testing.T) {
	srv, path := seedTranscriptServerPath(t, 1)
	seeded := len(srv.appAllTurns("th_1"))
	if seeded != 2 {
		t.Fatalf("seeded turns = %d, want the transcript's 2", seeded)
	}

	appendTranscriptTurns(t, path, 8)
	if got := len(srv.appAllTurns("th_1")); got != seeded {
		t.Fatalf("turns = %d after 8 transcript appends, want the installed %d", got, seeded)
	}

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Restored: true, TranscriptEntries: seeded},
	})
	recordNotificationTurns(t, srv, 4)
	if got := len(srv.appAllTurns("th_1")); got != seeded+4 {
		t.Fatalf("turns = %d after 4 live turns, want %d", got, seeded+4)
	}
}

// TestServerAppWireOldIdentityCannotPublishAfterReplacement pins that a
// replaced identity is finished: its turns are neither readable under the old
// ref nor inherited by the new thread.
func TestServerAppWireOldIdentityCannotPublishAfterReplacement(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "old", Data: events.UserInputData{Text: "old turn"}})
	oldThreadID := srv.appThread().ID
	if len(srv.appAllTurns(oldThreadID)) == 0 {
		t.Fatal("old identity recorded no turns, so the fence below would prove nothing")
	}

	srv.SetAppIdentity("local", "new")
	if oldTurns := srv.appAllTurns(oldThreadID); len(oldTurns) != 0 {
		t.Fatalf("old identity read returned turns after replacement: %v", turnIDs(oldTurns))
	}
	if newTurns := srv.appAllTurns("new"); len(newTurns) != 0 {
		t.Fatalf("replaced identity inherited old turns: %v", turnIDs(newTurns))
	}

	// Once the new thread has content of its own, a read still addressed to the
	// old ref must return nothing rather than the new thread's conversation.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "new", Data: events.UserInputData{Text: "new turn"}})
	if newTurns := srv.appAllTurns("new"); len(newTurns) == 0 {
		t.Fatal("new identity recorded no turns, so the fence below would prove nothing")
	}
	if oldTurns := srv.appAllTurns(oldThreadID); len(oldTurns) != 0 {
		t.Fatalf("read for the replaced thread %q returned the new thread's turns: %v", oldThreadID, turnIDs(oldTurns))
	}
	if page := srv.appPageTurns(oldThreadID, "", 30); len(page.Data) != 0 {
		t.Fatalf("page for the replaced thread %q returned the new thread's turns: %v", oldThreadID, turnIDs(page.Data))
	}
}

// TestServerAppWireReplacementClosesTheOldStreamOnce pins that the old thread's
// subscribers are told their thread ended -- once, targeted at the OLD ref --
// and that the closure is not reduced into the new thread's turns.
func TestServerAppWireReplacementClosesTheOldStreamOnce(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	srv.SetAppIdentity("local", "new")

	closed := srv.AppNotificationsAfter(0, "old")
	if len(closed) != 1 || closed[0].Notification.Method != appwire.NotifyThreadClosed {
		t.Fatalf("old-thread records = %+v, want exactly one thread/closed", closed)
	}
	var params appwire.ThreadClosedParams
	if err := json.Unmarshal(closed[0].Notification.Params, &params); err != nil {
		t.Fatalf("decode thread/closed: %v", err)
	}
	if params.ThreadID != "old" || params.Ref != "local:old" {
		t.Fatalf("thread/closed target = (%q, %q), want the old identity", params.ThreadID, params.Ref)
	}
	if turns := srv.appAllTurns("new"); len(turns) != 0 {
		t.Fatalf("old-thread closure reduced into the new snapshot: %v", turnIDs(turns))
	}
	if same := srv.AppNotificationsAfter(0, "new"); len(same) != 0 {
		t.Fatalf("new thread received the old thread's closure: %+v", same)
	}
}

// TestServerAppWireReplacementLeavesNoActiveTurn pins that the daemon's two
// active-turn answers agree on "none" the moment an identity is installed.
// They answer different questions -- thread.serf.activeTurnId reports a turn in
// flight OR RESERVED, the reducer's activeTurnID names the turn steering items
// append to -- and only a reserved turn can make them differ. A fresh projector
// has reserved nothing, so both must be empty together or steering could target
// a turn that is not in the snapshot.
func TestServerAppWireReplacementLeavesNoActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	reserved, err := srv.reserveAppTurnIDForStart()
	if err != nil {
		t.Fatalf("reserveAppTurnIDForStart: %v", err)
	}
	if srv.appThread().Serf.ActiveTurnID != reserved {
		t.Fatalf("thread.serf.activeTurnId = %q, want the reserved %q", srv.appThread().Serf.ActiveTurnID, reserved)
	}

	srv.SetAppIdentity("local", "th_2")
	if got := srv.appThread().Serf.ActiveTurnID; got != "" {
		t.Fatalf("thread.serf.activeTurnId = %q after replacement, want none", got)
	}
	srv.mu.RLock()
	snapshot := srv.appTurns
	reservedAfter := srv.appReservedTurnID
	srv.mu.RUnlock()
	if reservedAfter != "" {
		t.Fatalf("reserved turn = %q after replacement, want none", reservedAfter)
	}
	snapshot.mu.Lock()
	steeringTarget := snapshot.activeTurnID
	snapshot.mu.Unlock()
	if steeringTarget != "" {
		t.Fatalf("reducer activeTurnID = %q after replacement, want none", steeringTarget)
	}
}

func TestAppTurnSnapshotIsDeepDefensiveCopy(t *testing.T) {
	started, completed, duration := int64(10), int64(20), int64(30)
	itemStarted, itemCompleted := int64(11), int64(19)
	retained := appwire.Turn{
		ID: "turn_1", ItemsView: "full", Status: appwire.TurnStatusCompleted,
		StartedAt: &started, CompletedAt: &completed, DurationMS: &duration,
		Usage: &appwire.SerfUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		Error: &appwire.TurnError{
			Message: "boom", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "openai"},
			CodexErrorInfo: map[string]any{"nested": map[string]any{"code": "original"}, "items": []any{"first"}},
		},
		Items: []appwire.ThreadItem{{
			Type: "userMessage", ID: "item_1", TurnID: "turn_1", Status: appwire.TurnStatusCompleted,
			StartedAt: &itemStarted, CompletedAt: &itemCompleted,
			Raw:          json.RawMessage(`{"state":{"value":"original"}}`),
			Images:       []appwire.InputItem{{Type: "image", Data: []byte("original"), Metadata: map[string]string{"name": "original"}}},
			OutputImages: []appwire.OutputImage{{Name: "original", SHA: "sha"}},
		}},
	}
	snapshot := &appTurnSnapshot{turns: []appwire.Turn{retained}, turnIndex: map[string]int{"turn_1": 0}}

	first := snapshot.Snapshot()
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	*first[0].StartedAt = 100
	*first[0].CompletedAt = 200
	*first[0].DurationMS = 300
	first[0].Usage.InputTokens = 100
	first[0].Error.Message = "mutated"
	first[0].Error.Cause.Provider = "mutated"
	info := first[0].Error.CodexErrorInfo.(map[string]any)
	info["nested"].(map[string]any)["code"] = "mutated"
	info["items"].([]any)[0] = "mutated"
	item := &first[0].Items[0]
	*item.StartedAt = 110
	*item.CompletedAt = 190
	item.Raw[bytes.Index(item.Raw, []byte("original"))] = 'X'
	item.Images[0].Data[0] = 'x'
	item.Images[0].Metadata["name"] = "mutated"
	item.OutputImages[0].Name = "mutated"

	got, err := json.Marshal(snapshot.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("snapshot aliases returned mutable state\n got: %s\nwant: %s", got, want)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 100 {
			_ = snapshot.Snapshot()
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 100 {
			delta, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_delta", Delta: "x"})
			if err != nil {
				panic(err)
			}
			snapshot.Apply([]appserver.SequencedNotification{{Seq: uint64(i + 2), Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: delta}}})
		}
	}()
	wg.Wait()
}

func TestAppTurnsFromNotificationsPreservesInputOrderWithMixedSequences(t *testing.T) {
	record := func(seq uint64, text string) appserver.SequencedNotification {
		params, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: text})
		if err != nil {
			t.Fatal(err)
		}
		return appserver.SequencedNotification{Seq: seq, Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: params}}
	}
	turns := appTurnsFromNotifications([]appserver.SequencedNotification{record(10, "first"), record(0, " second")})
	if len(turns) != 1 || len(turns[0].Items) != 1 || turns[0].Items[0].Text != "first second" {
		t.Fatalf("mixed-sequence legacy projection = %+v, want both input-order deltas", turns)
	}
}

// TestAppTurnSnapshotReducesInProducerOrderUnderConcurrentEvents pins what
// replaced the reducer's sequence bookkeeping. Sequence allocation and
// reduction now happen inside the SAME projection commit, so a record cannot
// reach the snapshot before an earlier one -- the reducer needs no cursor,
// retained window, or re-sort to get the order right.
func TestAppTurnSnapshotReducesInProducerOrderUnderConcurrentEvents(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"})

	// Hold the first delta between its projection and its commit, then start a
	// second delta behind it.
	firstProjecting := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.mu.Lock()
	srv.beforeAppProjectionCommit = func() {
		once.Do(func() {
			close(firstProjecting)
			<-release
		})
	}
	srv.mu.Unlock()

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "first"}})
	}()
	<-firstProjecting

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: " second"}})
	}()
	close(release)
	<-firstDone
	<-secondDone

	turns := srv.appAllTurns("th_1")
	if len(turns) != 1 || len(turns[0].Items) != 1 {
		t.Fatalf("turns = %+v, want one turn with one streaming item", turns)
	}
	if got := turns[0].Items[0].Text; got != "first second" {
		t.Fatalf("reduced text = %q, want %q", got, "first second")
	}
	if want := appTurnsFromNotifications(srv.AppNotificationsAfter(0, "th_1")); !reflect.DeepEqual(turns, want) {
		t.Fatalf("installed snapshot diverged from its own notification stream\n got: %#v\nwant: %#v", turns, want)
	}
}

// TestTranscriptHeaderReadsOnlyLeadingHeader pins that the identity check reads
// the header line and stops. A session with no api_call entries can carry
// thousands of entries the check has no business decoding.
func TestTranscriptHeaderReadsOnlyLeadingHeader(t *testing.T) {
	writeNoAPICallTranscript := func(entries int) string {
		path := filepath.Join(t.TempDir(), "no-api-call.transcript.jsonl")
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1", CreatedAt: time.Unix(1700000000, 0), ProfileID: "openai", Model: "gpt-5"})
		if err != nil {
			t.Fatal(err)
		}
		// This is an input fixture, not a durability test: batch the writes so
		// building the 2000-entry file does not pay 2000 per-Append fsyncs.
		// Close still flushes, so the file read back is byte-identical.
		writer.SyncInterval = time.Hour
		for range entries {
			if err := writer.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("historical entry"))); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	small := writeNoAPICallTranscript(1)
	large := writeNoAPICallTranscript(2000)
	if got := transcriptHeader(large, appTranscriptMaxLineBytes).SessionID; got != "th_1" {
		t.Fatalf("header session = %q, want th_1", got)
	}

	smallAllocs := testing.AllocsPerRun(3, func() { _ = transcriptHeader(small, appTranscriptMaxLineBytes) })
	largeAllocs := testing.AllocsPerRun(3, func() { _ = transcriptHeader(large, appTranscriptMaxLineBytes) })
	if largeAllocs > smallAllocs+10 {
		t.Fatalf("large no-api_call identity validation inspected historical entries: allocations large=%.0f small=%.0f", largeAllocs, smallAllocs)
	}
}

// TestPreparedAppIdentityRejectsAnotherSessionsTranscript pins that preparation
// refuses to seed one thread from another thread's history -- and that a
// refusal leaves the server exactly as it was, since nothing is published until
// preparation succeeds.
func TestPreparedAppIdentityRejectsAnotherSessionsTranscript(t *testing.T) {
	write := func(sessionID string) string {
		path := filepath.Join(t.TempDir(), sessionID+".transcript.jsonl")
		// Leading blank lines: the header is the first NON-EMPTY line.
		body := "\n \r\n" + fmt.Sprintf(`{"kind":"header","format_version":2,"session_id":%q}`, sessionID) + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	if _, err := PrepareAppIdentity("local", "th_1", write("th_1")); err != nil {
		t.Fatalf("PrepareAppIdentity with a matching header = %v, want success", err)
	}

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "installed"}})
	before := srv.appAllTurns("th_1")

	if _, err := PrepareAppIdentity("local", "th_1", write("th_other")); err == nil {
		t.Fatal("PrepareAppIdentity accepted another session's transcript")
	}
	if after := srv.appAllTurns("th_1"); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed preparation mutated installed state\n got: %v\nwant: %v", turnIDs(after), turnIDs(before))
	}
	if srv.appThread().ID != "th_1" {
		t.Fatalf("failed preparation moved the installed identity to %q", srv.appThread().ID)
	}
}

// TestPreparedAppIdentitySeedsEmptyStateWithoutATranscript pins the two ways a
// thread legitimately has no history to seed from: no path at all, and a path
// whose file does not exist yet.
func TestPreparedAppIdentitySeedsEmptyStateWithoutATranscript(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "no path", path: ""},
		{name: "blank path", path: "   "},
		{name: "missing file", path: filepath.Join(t.TempDir(), "absent.transcript.jsonl")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			prepared, err := PrepareAppIdentity("local", "th_1", tc.path)
			if err != nil {
				t.Fatalf("PrepareAppIdentity: %v", err)
			}
			srv.ReplaceAppIdentity(prepared, nil)
			if turns := srv.appAllTurns("th_1"); len(turns) != 0 {
				t.Fatalf("seeded turns = %v, want none", turnIDs(turns))
			}
			srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "live"}})
			if turns := srv.appAllTurns("th_1"); len(turns) != 1 {
				t.Fatalf("turns after one live event = %v, want one", turnIDs(turns))
			}
		})
	}
}

func TestPreparedAppIdentityRequiresAThreadID(t *testing.T) {
	for _, threadID := range []string{"", "   "} {
		if _, err := PrepareAppIdentity("local", threadID, ""); err == nil {
			t.Fatalf("PrepareAppIdentity(%q) succeeded, want an error", threadID)
		}
	}
}

// TestPreparedAppIdentityKeepsLiveTurnIDsAboveSeededTranscriptIDs pins kata
// eptj through the new seam. Both the seeded projection and the live projector
// mint "turn_N"; a restored SessionStart carries the persisted entry count so
// the first live turn cannot reuse an id the seed already owns.
func TestPreparedAppIdentityKeepsLiveTurnIDsAboveSeededTranscriptIDs(t *testing.T) {
	srv, _ := seedTranscriptServerPath(t, 3)
	seeded := srv.appAllTurns("th_1")
	if len(seeded) != 6 {
		t.Fatalf("seeded turns = %v, want the transcript's 6", turnIDs(seeded))
	}
	seededIDs := map[string]bool{}
	for _, id := range turnIDs(seeded) {
		seededIDs[id] = true
	}

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Restored: true, TranscriptEntries: len(seeded), Profile: "openai", Model: "gpt-5.5"},
	})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "next"}})

	live := srv.appAllTurns("th_1")
	if len(live) != len(seeded)+1 {
		t.Fatalf("turns after one live turn = %v, want one more than the seed", turnIDs(live))
	}
	newest := live[len(live)-1]
	if seededIDs[newest.ID] {
		t.Fatalf("live turn reused seeded transcript id %q; seeded = %v", newest.ID, turnIDs(seeded))
	}
}
