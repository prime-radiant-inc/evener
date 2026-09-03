package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
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
	writeTranscriptPairs(t, path, pairs)
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)
	srv.SetSteerFunc(func(string) {})
	srv.SetCancelFunc(func() {})
	return srv, path
}

// writeTranscriptPairs writes (or overwrites) a valid th_1 transcript holding
// `pairs` user/assistant exchanges.
func writeTranscriptPairs(t testing.TB, path string, pairs int) {
	t.Helper()
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
}

func TestTranscriptItemKeysMatchLiveAndIndexedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key-parity.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_key_parity", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("representative"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	items := []appwire.ThreadItem{{ID: "first"}, {ID: "second"}}
	live := positionAppItems(append([]appwire.ThreadItem(nil), items...), "turn_1", 0)
	history, identity, err := apptranscript.NewTurnCache().LatestItemWindowFromFile(
		path,
		appTranscriptMaxLineBytes,
		apptranscript.ItemWindowOptions{ThreadRef: "local:th_key_parity", Limit: len(items)},
		func(schema.Turn, string, int, map[string]string) []appwire.ThreadItem {
			return append([]appwire.ThreadItem(nil), items...)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ProjectionVersion != appitempaging.TranscriptItemProjectionVersion {
		t.Fatalf("historical projection version = %d, want %d", identity.ProjectionVersion, appitempaging.TranscriptItemProjectionVersion)
	}
	if len(history.Candidates) != len(live) {
		t.Fatalf("historical candidates = %d, want %d", len(history.Candidates), len(live))
	}
	for i := range live {
		if got, want := history.Candidates[i].Item.TranscriptKey, live[i].TranscriptKey; got != want {
			t.Errorf("item %d historical key = %q, live key = %q", i, got, want)
		}
		if got, want := live[i].TranscriptKey, appitempaging.TranscriptItemKey("turn_1", appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}); got != want {
			t.Errorf("item %d live key = %q, shared key = %q", i, got, want)
		}
	}
}

func TestPrepareAppIdentityHydratesPersistedCommunicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "communicate.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_hydrate", CreatedAt: time.Now(), ProfileID: "openai", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCallData{ID: "persisted-call", Name: "communicate", Arguments: json.RawMessage(`{"message":"hydrated"}`)}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("run"))); err != nil {
		t.Fatal(err)
	}
	if err := tw.Append(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareAppIdentity("local", "th_hydrate", path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.turns.turns) == 0 {
		t.Fatal("hydration produced no turns")
	}
	found := false
	for _, turn := range prepared.turns.turns {
		for _, item := range turn.Items {
			if item.Type == "agentMessage" && item.Text == "hydrated" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("persisted communicate missing after runtime hydration: %+v", prepared.turns.turns)
	}
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

type cloneCountingJSONValue struct {
	calls *int
}

func (v cloneCountingJSONValue) MarshalJSON() ([]byte, error) {
	(*v.calls)++
	return []byte(`{"value":"excluded"}`), nil
}

func installTurnSnapshotForTest(srv *Server, turns []appwire.Turn) {
	srv.mu.Lock()
	srv.appTurns = &appTurnSnapshot{threadID: "th_1", turns: turns}
	srv.mu.Unlock()
}

func countedCloneTurn(id string, calls *int) appwire.Turn {
	return appwire.Turn{
		ID: id,
		Error: &appwire.TurnError{
			CodexErrorInfo: cloneCountingJSONValue{calls: calls},
		},
	}
}

func TestServerAppWireLatestWindowClonesOnlyReturnedTurns(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var excludedCloneCalls int
	installTurnSnapshotForTest(srv, []appwire.Turn{
		countedCloneTurn("turn_1", &excludedCloneCalls),
		countedCloneTurn("turn_2", &excludedCloneCalls),
		{ID: "turn_3", Items: []appwire.ThreadItem{{Raw: json.RawMessage(`{"value":"latest"}`)}}},
	})

	got, cursor := srv.appLatestTurns("th_1", 1)
	if ids := turnIDs(got); !reflect.DeepEqual(ids, []string{"turn_3"}) || cursor != "2" {
		t.Fatalf("latest window = %v (cursor %q), want [turn_3] (cursor 2)", ids, cursor)
	}
	if excludedCloneCalls != 0 {
		t.Fatalf("latest window deep-cloned %d excluded turn value(s), want 0", excludedCloneCalls)
	}

	got[0].Items[0].Raw[0] = 'X'
	again, _ := srv.appLatestTurns("th_1", 1)
	if string(again[0].Items[0].Raw) != `{"value":"latest"}` {
		t.Fatalf("latest window aliases installed state: %s", again[0].Items[0].Raw)
	}
}

func TestServerAppWireOlderPageClonesOnlyReturnedTurns(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var excludedCloneCalls int
	installTurnSnapshotForTest(srv, []appwire.Turn{
		countedCloneTurn("turn_1", &excludedCloneCalls),
		countedCloneTurn("turn_2", &excludedCloneCalls),
		{ID: "turn_3", Items: []appwire.ThreadItem{{Raw: json.RawMessage(`{"value":"page"}`)}}},
		countedCloneTurn("turn_4", &excludedCloneCalls),
	})

	got := srv.appPageTurns("th_1", "3", 1)
	if ids := turnIDs(got.Data); !reflect.DeepEqual(ids, []string{"turn_3"}) || got.NextCursor != "2" {
		t.Fatalf("older page = %v (cursor %q), want [turn_3] (cursor 2)", ids, got.NextCursor)
	}
	if excludedCloneCalls != 0 {
		t.Fatalf("older page deep-cloned %d excluded turn value(s), want 0", excludedCloneCalls)
	}

	got.Data[0].Items[0].Raw[0] = 'X'
	again := srv.appPageTurns("th_1", "3", 1)
	if string(again.Data[0].Items[0].Raw) != `{"value":"page"}` {
		t.Fatalf("older page aliases installed state: %s", again.Data[0].Items[0].Raw)
	}
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

func TestAppWireItemPagingNormalizesBoundaryCompleteness(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	turns := []appwire.Turn{
		{
			ID:     "turn_old",
			Status: appwire.TurnStatusCompleted,
			Items:  positionAppItems([]appwire.ThreadItem{{ID: "old_item", Text: "old"}}, "turn_old", 0),
		},
		{
			ID:     "turn_latest",
			Status: appwire.TurnStatusCompleted,
			Items:  positionAppItems([]appwire.ThreadItem{{ID: "latest_first", Text: "first"}, {ID: "latest_last", Text: "last"}}, "turn_latest", 1),
		},
	}
	installTurnSnapshotForTest(srv, turns)

	full, _, err := srv.appLatestItemTurns("th_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 || len(full[0].Items) != 2 {
		t.Fatalf("fully contained item read = %+v, want one turn with two items", full)
	} else if full[0].HasEarlierItems || full[0].HasLaterItems {
		t.Fatalf("fully contained turn flags = (%v,%v), want (false,false)", full[0].HasEarlierItems, full[0].HasLaterItems)
	}

	partial, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		Ref:          "local:th_1",
		IncludeTurns: true,
		PageUnit:     appwire.TranscriptPageUnitItem,
		ItemLimit:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Thread.Turns) != 1 || len(partial.Thread.Turns[0].Items) != 1 || partial.Thread.Turns[0].Items[0].ID != "latest_last" {
		t.Fatalf("partial latest item read = %+v, want latest_last", partial.Thread.Turns)
	}
	if !partial.Thread.Turns[0].HasEarlierItems || partial.Thread.Turns[0].HasLaterItems {
		t.Fatalf("partial latest flags = (%v,%v), want (true,false)", partial.Thread.Turns[0].HasEarlierItems, partial.Thread.Turns[0].HasLaterItems)
	}
	if partial.OlderCursor == "" {
		t.Fatal("partial latest item read returned no older cursor")
	}

	older, err := srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "local:th_1",
		Cursor:    partial.OlderCursor,
		PageUnit:  appwire.TranscriptPageUnitItem,
		ItemLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Data) != 1 || len(older.Data[0].Items) != 1 || older.Data[0].Items[0].ID != "latest_first" {
		t.Fatalf("older item page = %+v, want latest_first", older.Data)
	}
	if older.Data[0].HasEarlierItems || !older.Data[0].HasLaterItems {
		t.Fatalf("older boundary flags = (%v,%v), want (false,true)", older.Data[0].HasEarlierItems, older.Data[0].HasLaterItems)
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
//
// Two independent arms, because neither covers the other:
//
//   - The read observer instruments only the BOUNDED turn-index readers
//     (apptranscript's observeIndexRead), and only on their success path. A
//     regression that re-projected the whole file at read time reports nothing,
//     and a bounded read that errors reports nothing either.
//   - So the file is first replaced with a DIFFERENT but still-valid transcript
//     for the same session. Any read that reopens it now succeeds and answers
//     with content that is not this thread, which the equality assertions catch
//     whichever reader it used.
//
// The observer check runs first so that a bounded-reader regression is named as
// one rather than being reported as a content mismatch.
func TestServerAppWireInstalledSnapshotNeedsNoTranscriptReads(t *testing.T) {
	srv, path := seedTranscriptServerPath(t, 3)
	installed := srv.appAllTurns("th_1")
	if len(installed) != 6 {
		t.Fatalf("installed turns = %v, want the transcript's 6", turnIDs(installed))
	}
	writeTranscriptPairs(t, path, 1)

	var reads int
	restore := apptranscript.InstallReadObserverForTesting(func(apptranscript.ReadStats) { reads++ })
	t.Cleanup(restore)

	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{
		Ref:          "local:th_1",
		Subscribe:    true,
		IncludeTurns: true,
		TurnLimit:    40,
	})
	page := srv.appPageTurns("th_1", "1", 30)

	if reads != 0 {
		t.Fatalf("bounded turn reads performed %d transcript read(s); the installed snapshot must answer from memory", reads)
	}
	if !reflect.DeepEqual(read.Thread.Turns, installed) {
		t.Fatalf("thread/read = %v, want the installed %v", turnIDs(read.Thread.Turns), turnIDs(installed))
	}
	if !reflect.DeepEqual(page, appwire.PageTurns(installed, "1", 30)) {
		t.Fatalf("thread/turns/list = %v, want the installed page", turnIDs(page.Data))
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

	closed := srv.AppNotificationsAfter(0, "local:old")
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
	if same := srv.AppNotificationsAfter(0, "local:new"); len(same) != 0 {
		t.Fatalf("new thread received the old thread's closure: %+v", same)
	}
}

// TestServerAppWireReplacementLeavesNoActiveTurn pins that the daemon's two
// active-turn answers agree on "none" the moment an identity is installed.
//
// They answer different questions and can legitimately hold different values at
// once: thread.evener.activeTurnId reports a turn in flight OR RESERVED and gates
// capabilities, while the reducer's activeTurnID names the turn steering ITEMS
// append to. The setup below drives them apart on purpose -- an in-flight turn
// for the reducer, a later reserved turn for the daemon -- so that the
// post-replacement assertions have something real to clear. Both must land on
// empty together, or steering could target a turn absent from the snapshot.
func TestServerAppWireReplacementLeavesNoActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	// An in-flight turn: the reducer now has a steering target.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "in flight"}})
	inFlight := readReducerActiveTurnID(srv)
	if inFlight == "" {
		t.Fatal("reducer has no active turn before replacement, so clearing it would prove nothing")
	}
	// A reservation on top: the daemon's answer moves, the reducer's does not.
	reserved, err := srv.reserveAppTurnIDForStart()
	if err != nil {
		t.Fatalf("reserveAppTurnIDForStart: %v", err)
	}
	if srv.appThread().Evener.ActiveTurnID != reserved {
		t.Fatalf("thread.evener.activeTurnId = %q, want the reserved %q", srv.appThread().Evener.ActiveTurnID, reserved)
	}
	if reserved == inFlight {
		t.Fatalf("reserved turn %q equals the in-flight turn; the two answers are not being driven apart", reserved)
	}
	if got := readReducerActiveTurnID(srv); got != inFlight {
		t.Fatalf("reserving a turn moved the reducer's steering target to %q; a reserved turn is not in the snapshot", got)
	}

	srv.SetAppIdentity("local", "th_2")
	if got := srv.appThread().Evener.ActiveTurnID; got != "" {
		t.Fatalf("thread.evener.activeTurnId = %q after replacement, want none", got)
	}
	srv.mu.RLock()
	reservedAfter := srv.appReservedTurnID
	srv.mu.RUnlock()
	if reservedAfter != "" {
		t.Fatalf("reserved turn = %q after replacement, want none", reservedAfter)
	}
	if got := readReducerActiveTurnID(srv); got != "" {
		t.Fatalf("reducer activeTurnID = %q after replacement, want none", got)
	}
}

func readReducerActiveTurnID(srv *Server) string {
	srv.mu.RLock()
	snapshot := srv.appTurns
	srv.mu.RUnlock()
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	return snapshot.activeTurnID
}

func TestAppTurnSnapshotIsDeepDefensiveCopy(t *testing.T) {
	started, completed, duration := int64(10), int64(20), int64(30)
	itemStarted, itemCompleted := int64(11), int64(19)
	retained := appwire.Turn{
		ID: "turn_1", ItemsView: "full", Status: appwire.TurnStatusCompleted,
		StartedAt: &started, CompletedAt: &completed, DurationMS: &duration,
		Usage: &appwire.EvenerUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
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
//
// The order here is established by a real happens-before, not by racing two
// goroutines and hoping: the first event is held INSIDE its commit callback,
// where it owns the projection lock, while the second event is started and
// blocks trying to enter. beforeAppProjectionCommit alone cannot do this -- it
// runs before CommitProjection, so a goroutine parked there holds nothing and
// either goroutine may win the lock. The failure-count stamp runs inside the
// callback instead, which is where the lock actually is.
func TestAppTurnSnapshotReducesInProducerOrderUnderConcurrentEvents(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "prompt"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"})

	// Park the first event inside its commit, holding the projection lock.
	insideCommit := make(chan struct{})
	release := make(chan struct{})
	var stamped sync.Once
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.failuresMeasured = true })
	setInsideAppProjectionCommitHook(t, func() {
		stamped.Do(func() {
			close(insideCommit)
			<-release
		})
	})

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "first"}})
	}()
	<-insideCommit

	// The second event is genuinely in flight while the first owns the lock, so
	// this is a concurrent commit -- it just cannot be the one that wins.
	secondReached := make(chan struct{})
	var reached sync.Once
	srv.mu.Lock()
	srv.beforeAppProjectionCommit = func() { reached.Do(func() { close(secondReached) }) }
	srv.mu.Unlock()
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "second"}})
	}()
	<-secondReached
	close(release)
	<-firstDone
	<-secondDone

	turns := srv.appAllTurns("th_1")
	if len(turns) != 1 {
		t.Fatalf("turns = %v, want one turn", turnIDs(turns))
	}
	// The completed assistant message committed first, so it precedes the item
	// the later delta opened. A reducer that re-ordered would swap them.
	var texts []string
	for _, item := range turns[0].Items {
		if item.Type == "agentMessage" {
			texts = append(texts, item.Text)
		}
	}
	if !reflect.DeepEqual(texts, []string{"first", "second"}) {
		t.Fatalf("reduced assistant items = %v, want [first second] in commit order", texts)
	}
	// The invariant behind the ordering: the installed snapshot is exactly what
	// reducing its own committed sequence produces. This holds whichever event
	// wins, so it also guards the ordering assertion above against a fixture
	// that stops establishing the order it claims.
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
	large := writeNoAPICallTranscript(2000)
	if got := transcriptHeader(large, appTranscriptMaxLineBytes).SessionID; got != "th_1" {
		t.Fatalf("header session = %q, want th_1", got)
	}

	file, err := os.Open(large)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck // read-only fixture
	counted := &countingHeaderReader{Reader: file}
	if got := transcriptHeaderFromReader(counted, appTranscriptMaxLineBytes); got.SessionID != "th_1" {
		t.Fatalf("counted header session = %q, want th_1", got.SessionID)
	}
	if counted.bytes > transcriptHeaderReadBufferBytes {
		t.Fatalf("header validation read %d bytes from a %d-byte-bound reader", counted.bytes, transcriptHeaderReadBufferBytes)
	}
}

type countingHeaderReader struct {
	io.Reader
	bytes int64
}

func (r *countingHeaderReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	return n, err
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

func TestAppTurnSnapshotSeedPanicsBeforeMutatingOnUnsupportedType(t *testing.T) {
	projection := &appItemProjection{}
	snapshot := &appTurnSnapshot{itemProjection: projection}
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("Seed did not panic for unsupported input")
		}
		if snapshot.itemProjection != projection {
			t.Fatal("Seed mutated item projection before rejecting unsupported input")
		}
	}()

	snapshot.Seed(struct{ unsupported bool }{unsupported: true})
}

// TestPreparedAppIdentityRequiresAThreadID also covers SetAppIdentity, which
// installs through the same validation. An identity with no thread cannot be
// published half-way: it would blank status.SessionID while leaving the caller
// believing a thread was installed.
func TestPreparedAppIdentityRequiresAThreadID(t *testing.T) {
	for _, threadID := range []string{"", "   "} {
		if _, err := PrepareAppIdentity("local", threadID, ""); err == nil {
			t.Fatalf("PrepareAppIdentity(%q) succeeded, want an error", threadID)
		}

		srv := NewServer(ServerConfig{})
		srv.SetAppIdentity("local", "th_1")
		srv.UpdateSessionInfo("01SESS001", "gpt-5", "openai")
		srv.SetAppIdentity("local", threadID)
		if got := srv.GetStatus().SessionID; got != "01SESS001" {
			t.Fatalf("SetAppIdentity(%q) left status.SessionID = %q, want the untouched 01SESS001", threadID, got)
		}
		if got := srv.appThread().ID; got != "th_1" {
			t.Fatalf("SetAppIdentity(%q) replaced the installed thread with %q", threadID, got)
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

// TestSeededTranscriptDoesNotAbsorbAReservedClientMutationTurn pins kata rk09,
// the SECOND minter in the same collision class as eptj.
//
// A client-authored input reserves its turn id from the daemon's durable
// mutation counter (agent's reserveClientMutationTurnID), and that counter is
// not the projector's — SeedPersistedTurns never fenced it. One reply is one
// mutation but several transcript entries, so the reservation always names a
// LOW number, and after a restart reseeds this snapshot every low number is
// already an unrelated early turn. ensureTurn then appends the reply's
// userMessage to THAT turn, and since the reserved id becomes the projector's
// active turn, the whole agent response follows it.
func TestSeededTranscriptDoesNotAbsorbAReservedClientMutationTurn(t *testing.T) {
	srv, _ := seedTranscriptServerPath(t, 5)
	seeded := srv.appAllTurns("th_1")
	if len(seeded) != 10 {
		t.Fatalf("seeded turns = %v, want the transcript's 10", turnIDs(seeded))
	}
	seededItems := map[string]int{}
	for _, turn := range seeded {
		seededItems[turn.ID] = len(turn.Items)
	}

	// The daemon restarted: the projector is fenced above the persisted entry
	// count, exactly as PrepareAppIdentity and a restored SessionStart leave it.
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Restored: true, TranscriptEntries: len(seeded), Profile: "openai", Model: "gpt-5.5"},
	})

	// The reply answering a pending ask carries the id reserved from the
	// mutation counter, which is far behind the entry count.
	reserved := appwire.ClientMutationTurnID(3)
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data: events.UserInputData{
			Text:             "the answer",
			StableTurnID:     reserved,
			ClientMutationID: "reply-1",
			Turn:             len(seeded) + 1,
		},
	})

	live := srv.appAllTurns("th_1")
	if len(live) != len(seeded)+1 {
		t.Fatalf("turns after the reply = %v, want one more than the seed %v — the reserved id %q named a turn that already existed, so the reply merged into it",
			turnIDs(live), turnIDs(seeded), reserved)
	}
	newest := live[len(live)-1]
	if newest.ID != reserved {
		t.Fatalf("newest turn = %q, want the reserved %q", newest.ID, reserved)
	}
	for _, turn := range live {
		want, wasSeeded := seededItems[turn.ID]
		if !wasSeeded || len(turn.Items) == want {
			continue
		}
		t.Fatalf("seeded turn %q now holds %d items, want its original %d — the reply landed in it",
			turn.ID, len(turn.Items), want)
	}
}

// TestSeedingAReservedTurnIDFromTheTranscriptKeepsTurnIDsUnique (kata rk09)
// carries the same invariant across a restart.
//
// A reserved id is PERSISTED: apptranscript keeps a persisted entry's
// StableTurnID in preference to its entry-index number, so the id a reply was
// reserved under is the id it keeps forever. Sharing the entry-index namespace
// therefore does not just merge the live turn — it seeds two turns under one
// id, which is the turn-id-uniqueness invariant the browser reducer logs.
func TestSeedingAReservedTurnIDFromTheTranscriptKeepsTurnIDsUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1", CreatedAt: time.Now(), ProfileID: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Five exchanges: the last user input is transcript entry 9, but it is only
	// the third client mutation, so its reservation is numbered 3.
	reserved := appwire.ClientMutationTurnID(3)
	for i := range 5 {
		user := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("in-%d", i)))
		if i == 4 {
			user.ClientMutationID = "reply-1"
			user.StableTurnID = reserved
		}
		if err := tw.Append(user); err != nil {
			t.Fatalf("append user: %v", err)
		}
		if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("out-%d", i)))); err != nil {
			t.Fatalf("append assistant: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)
	seeded := srv.appAllTurns("th_1")

	occurrences := map[string]int{}
	for _, turn := range seeded {
		occurrences[turn.ID]++
	}
	for id, n := range occurrences {
		if n > 1 {
			t.Fatalf("seeded turn id %q appears %d times in %v — the reserved id %q is also an entry-index id",
				id, n, turnIDs(seeded), reserved)
		}
	}
	if occurrences[reserved] != 1 {
		t.Fatalf("seeded turns %v do not carry the persisted reserved id %q", turnIDs(seeded), reserved)
	}
}

func TestAppTurnSnapshotItemPaging(t *testing.T) {
	items := make([]appwire.ThreadItem, 45)
	for i := range items {
		position := appwire.ThreadItemPosition{Entry: 7, Item: uint32(i)}
		items[i] = appwire.ThreadItem{Type: "agentMessage", ID: fmt.Sprintf("item_%d", i), TurnID: "turn_7", Text: fmt.Sprintf("item %d", i), TranscriptKey: fmt.Sprintf("key_%d", i), Position: &position, Status: appwire.TurnStatusCompleted}
	}
	snapshot := &appTurnSnapshot{}
	snapshot.Seed(appTurnSeed{Turns: []appwire.Turn{{ID: "turn_7", ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted, Items: items}}, ThreadRef: "local:th_1", TranscriptIncarnation: "inc-1", NextEntry: 8})

	latest, identity, err := snapshot.LatestItemCandidates(40)
	if err != nil {
		t.Fatalf("LatestItemCandidates: %v", err)
	}
	if len(latest.Candidates) != 40 || latest.Candidates[0].Item.ID != "item_5" || latest.Candidates[39].Item.ID != "item_44" {
		t.Fatalf("latest candidates = %d (%q..%q), want items 5..44", len(latest.Candidates), latest.Candidates[0].Item.ID, latest.Candidates[len(latest.Candidates)-1].Item.ID)
	}
	if latest.OlderCursor == "" {
		t.Fatal("latest item window has no older cursor")
	}
	boundary, err := appitempaging.DecodeCursor(latest.OlderCursor, identity)
	if err != nil || boundary != (appwire.ThreadItemPosition{Entry: 7, Item: 5}) {
		t.Fatalf("older cursor boundary = %+v, %v; want (7,5)", boundary, err)
	}
	previous, _, err := snapshot.PreviousItemCandidates(latest.OlderCursor, 40)
	if err != nil {
		t.Fatalf("PreviousItemCandidates: %v", err)
	}
	if len(previous.Candidates) != 5 || previous.Candidates[0].Item.ID != "item_0" || previous.Candidates[4].Item.ID != "item_4" {
		t.Fatalf("previous candidates = %d, want items 0..4", len(previous.Candidates))
	}
	seen := map[string]bool{}
	for _, candidate := range append(latest.Candidates, previous.Candidates...) {
		if seen[candidate.Item.ID] {
			t.Fatalf("duplicate item id across pages: %q", candidate.Item.ID)
		}
		seen[candidate.Item.ID] = true
	}
}

func TestAppTurnSnapshotItemProjectionCacheReusesAndInvalidates(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 1, Item: 0}
	secondPosition := appwire.ThreadItemPosition{Entry: 1, Item: 1}
	snapshot := &appTurnSnapshot{}
	snapshot.Seed(appTurnSeed{
		Turns: []appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusInProgress, Items: []appwire.ThreadItem{
			{ID: "item_0", TurnID: "turn_1", Type: "agentMessage", Text: "earlier", Position: &position},
			{ID: "item_1", TurnID: "turn_1", Type: "agentMessage", Text: "initial", Position: &secondPosition},
		}}},
		ThreadRef: "local:th_1", TranscriptIncarnation: "inc-1", NextEntry: 2,
	})

	first, _, err := snapshot.LatestItemCandidates(1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.mu.Lock()
	initialCache := snapshot.itemProjection
	snapshot.mu.Unlock()
	if initialCache == nil {
		t.Fatal("first item page did not build an item projection cache")
	}
	wantLatestPosition := secondPosition
	first.Candidates[0].Item.Text = "mutated page"
	*first.Candidates[0].Item.Position = appwire.ThreadItemPosition{Entry: 99, Item: 99}
	second, _, err := snapshot.LatestItemCandidates(1)
	if err != nil {
		t.Fatal(err)
	}
	previous, _, err := snapshot.PreviousItemCandidates(first.OlderCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.mu.Lock()
	if snapshot.itemProjection != initialCache {
		t.Fatal("unchanged item page rebuilt its projection cache")
	}
	snapshot.mu.Unlock()
	if len(previous.Candidates) != 1 || previous.Candidates[0].Item.ID != "item_0" {
		t.Fatalf("previous item page = %+v, want item_0", previous.Candidates)
	}
	if got := second.Candidates[0].Item.Text; got != "initial" {
		t.Fatalf("cached item page aliases returned page mutation: %q", got)
	}
	if got := second.Candidates[0].Item.Position; got == nil || *got != wantLatestPosition || *got != second.Candidates[0].Position {
		t.Fatalf("cached item position = %+v, candidate position = %+v; want %+v", got, second.Candidates[0].Position, wantLatestPosition)
	}
	if _, err := appitempaging.RegroupTurnFragments(second.Candidates); err != nil {
		t.Fatalf("regrouping cached item page: %v", err)
	}

	seedPosition := appwire.ThreadItemPosition{Entry: 2, Item: 0}
	snapshot.Seed(appTurnSeed{
		Turns: []appwire.Turn{{ID: "turn_seed", Status: appwire.TurnStatusInProgress, Items: []appwire.ThreadItem{{
			ID: "seed_item", TurnID: "turn_seed", Type: "agentMessage", Text: "seeded", Position: &seedPosition,
		}}}},
		ThreadRef: "local:th_1", TranscriptIncarnation: "inc-1", NextEntry: 3,
	})
	seeded, _, err := snapshot.LatestItemCandidates(1)
	if err != nil || len(seeded.Candidates) != 1 || seeded.Candidates[0].Item.ID != "seed_item" {
		t.Fatalf("latest after seed = %+v, err=%v; want seed_item", seeded.Candidates, err)
	}
	snapshot.mu.Lock()
	seedCache := snapshot.itemProjection
	snapshot.mu.Unlock()
	if seedCache == initialCache {
		t.Fatal("Seed did not invalidate projection cache")
	}

	started, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: "turn_1", Item: appwire.ThreadItem{
		ID: "item_2", TurnID: "turn_1", Type: "agentMessage", Status: appwire.TurnStatusInProgress,
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 1, Notification: appwire.Notification{Method: appwire.NotifyItemStarted, Params: started}}})
	appended, _, err := snapshot.LatestItemCandidates(1)
	if err != nil || len(appended.Candidates) != 1 || appended.Candidates[0].Item.ID != "item_2" {
		t.Fatalf("latest after append = %+v, err=%v; want item_2", appended.Candidates, err)
	}
	snapshot.mu.Lock()
	appendCache := snapshot.itemProjection
	snapshot.mu.Unlock()
	if appendCache == seedCache {
		t.Fatal("item append did not invalidate projection cache")
	}

	delta, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_2", Delta: "delta"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 2, Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: delta}}})
	updated, _, err := snapshot.LatestItemCandidates(1)
	if err != nil || len(updated.Candidates) != 1 || updated.Candidates[0].Item.Text != "delta" {
		t.Fatalf("latest after delta = %+v, err=%v; want delta", updated.Candidates, err)
	}
	snapshot.mu.Lock()
	deltaCache := snapshot.itemProjection
	snapshot.mu.Unlock()
	if deltaCache == appendCache {
		t.Fatal("item delta did not invalidate projection cache")
	}
}

func TestAppTurnSnapshotCursorGeneration(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 1, Item: 0}
	secondPosition := appwire.ThreadItemPosition{Entry: 1, Item: 1}
	snapshot := &appTurnSnapshot{}
	snapshot.Seed(appTurnSeed{Turns: []appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusInProgress, Items: []appwire.ThreadItem{{ID: "item_1", TurnID: "turn_1", Type: "agentMessage", Position: &position, TranscriptKey: "key-1"}, {ID: "item_2", TurnID: "turn_1", Type: "agentMessage", Position: &secondPosition, TranscriptKey: "key-2"}}}}, ThreadRef: "local:th_1", TranscriptIncarnation: "inc-1", NextEntry: 2})
	window, _, err := snapshot.LatestItemCandidates(1)
	if err != nil || window.OlderCursor == "" {
		t.Fatalf("latest cursor = %q, err=%v; want cursor for two items", window.OlderCursor, err)
	}
	started, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: "turn_1", Item: appwire.ThreadItem{ID: "item_3", Type: "agentMessage", TurnID: "turn_1", Status: appwire.TurnStatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 1, Notification: appwire.Notification{Method: appwire.NotifyItemStarted, Params: started}}})
	if _, _, err := snapshot.PreviousItemCandidates("not-a-cursor", 1); !isStaleItemCursorError(err) {
		t.Fatalf("malformed cursor error = %v, want typed stale error", err)
	}
	window, _, err = snapshot.LatestItemCandidates(1)
	if err != nil || window.Candidates[0].Item.ID != "item_3" {
		t.Fatalf("latest after append = %+v, err=%v; want item_3", window.Candidates, err)
	}
	item3Position := *window.Candidates[0].Item.Position
	delta, err := json.Marshal(appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_3", Delta: "updated"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 11, Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: delta}}})
	completed, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: "turn_1", Item: appwire.ThreadItem{ID: "item_3", Status: appwire.TurnStatusCompleted, Text: "final"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 12, Notification: appwire.Notification{Method: appwire.NotifyItemCompleted, Params: completed}}})
	updated := snapshot.Snapshot()[0].Items[2]
	if updated.Position == nil || *updated.Position != item3Position || updated.Status != appwire.TurnStatusCompleted {
		t.Fatalf("lifecycle update changed item identity: %+v; want position %+v and completed status", updated, item3Position)
	}
	old := window
	if _, _, err := snapshot.PreviousItemCandidates(old.OlderCursor, 1); err != nil {
		t.Fatalf("tail append staled existing cursor: %v", err)
	}
	reset, err := json.Marshal(appwire.AgentMessageResetParams{TurnID: "turn_1", ItemID: "item_1"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 2, Notification: appwire.Notification{Method: appwire.NotifyAgentMessageReset, Params: reset}}})
	if _, _, err := snapshot.PreviousItemCandidates(old.OlderCursor, 1); !isStaleItemCursorError(err) {
		t.Fatalf("cursor after reset error = %v, want typed stale error", err)
	}
	prelude, err := json.Marshal(appwire.TurnStartedParams{ThreadID: "th_1", Turn: appwire.Turn{ID: appwire.SystemPreludeTurnID, Status: appwire.TurnStatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 3, Notification: appwire.Notification{Method: appwire.NotifyTurnStarted, Params: prelude}}})
	if _, _, err := snapshot.LatestItemCandidates(1); err != nil {
		t.Fatalf("latest after late prelude insertion: %v", err)
	}
	if _, _, err := snapshot.PreviousItemCandidates(old.OlderCursor, 1); !isStaleItemCursorError(err) {
		t.Fatalf("cursor after late prelude insertion = %v, want typed stale error", err)
	}
}

func isStaleItemCursorError(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		return false
	}
	data, ok := wire.Data.(appwire.ErrorData)
	return ok && data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
}

func TestAppTurnSnapshotLatePreludeRebasesExistingPositions(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 0, Item: 0}
	snapshot := &appTurnSnapshot{}
	snapshot.Seed(appTurnSeed{
		Turns: []appwire.Turn{{ID: "turn_1", Status: appwire.TurnStatusCompleted, Items: []appwire.ThreadItem{{
			ID: "item_1", TurnID: "turn_1", Type: "agentMessage", Text: "history", Position: &position, TranscriptKey: appitempaging.TranscriptItemKey("turn_1", position),
		}}}},
		ThreadRef: "local:th_1", TranscriptIncarnation: "inc-1", NextEntry: 1,
	})
	preludeStarted, err := json.Marshal(appwire.TurnStartedParams{ThreadID: "th_1", Turn: appwire.Turn{ID: appwire.SystemPreludeTurnID, Status: appwire.TurnStatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 1, Notification: appwire.Notification{Method: appwire.NotifyTurnStarted, Params: preludeStarted}}})
	itemStarted, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: appwire.SystemPreludeTurnID, Item: appwire.ThreadItem{
		ID: "prelude_item", Type: "agentMessage", TurnID: appwire.SystemPreludeTurnID, Status: appwire.TurnStatusCompleted, Text: "prelude",
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 2, Notification: appwire.Notification{Method: appwire.NotifyItemStarted, Params: itemStarted}}})
	window, _, err := snapshot.LatestItemCandidates(40)
	if err != nil {
		t.Fatalf("LatestItemCandidates: %v", err)
	}
	if len(window.Candidates) != 2 {
		t.Fatalf("candidate count=%d, want 2", len(window.Candidates))
	}
	if got := window.Candidates[0].Position; got != (appwire.ThreadItemPosition{Entry: 0, Item: 0}) {
		t.Fatalf("prelude position=%+v, want (0,0)", got)
	}
	if got := window.Candidates[1].Position; got != (appwire.ThreadItemPosition{Entry: 1, Item: 0}) {
		t.Fatalf("rebased history position=%+v, want (1,0)", got)
	}
	if window.Candidates[0].Item.TranscriptKey == window.Candidates[1].Item.TranscriptKey {
		t.Fatalf("prelude and history keys collided: %q", window.Candidates[0].Item.TranscriptKey)
	}
	liveStarted, err := json.Marshal(appwire.TurnStartedParams{ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_live", Status: appwire.TurnStatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 3, Notification: appwire.Notification{Method: appwire.NotifyTurnStarted, Params: liveStarted}}})
	liveItem, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: "turn_live", Item: appwire.ThreadItem{
		ID: "live_item", Type: "agentMessage", TurnID: "turn_live", Status: appwire.TurnStatusCompleted, Text: "live",
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 4, Notification: appwire.Notification{Method: appwire.NotifyItemStarted, Params: liveItem}}})
	window, _, err = snapshot.LatestItemCandidates(40)
	if err != nil {
		t.Fatalf("LatestItemCandidates after live append: %v", err)
	}
	if got := window.Candidates[2].Position; got != (appwire.ThreadItemPosition{Entry: 2, Item: 0}) {
		t.Fatalf("live position=%+v, want (2,0) after reserved prelude coordinate", got)
	}
}

func TestAppTurnSnapshotZeroItemTurnConsumesEntryOrdinal(t *testing.T) {
	snapshot := &appTurnSnapshot{}
	snapshot.Seed(appTurnSeed{ThreadRef: "local:th_1", TranscriptIncarnation: "inc-1", NextEntry: 5})
	emptyStarted, err := json.Marshal(appwire.TurnStartedParams{ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_empty", Status: appwire.TurnStatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 1, Notification: appwire.Notification{Method: appwire.NotifyTurnStarted, Params: emptyStarted}}})
	liveStarted, err := json.Marshal(appwire.TurnStartedParams{ThreadID: "th_1", Turn: appwire.Turn{ID: "turn_visible", Status: appwire.TurnStatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	visibleItem, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: "turn_visible", Item: appwire.ThreadItem{
		ID: "visible_item", Type: "agentMessage", TurnID: "turn_visible", Status: appwire.TurnStatusCompleted, Text: "visible",
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{
		{Seq: 2, Notification: appwire.Notification{Method: appwire.NotifyTurnStarted, Params: liveStarted}},
		{Seq: 3, Notification: appwire.Notification{Method: appwire.NotifyItemStarted, Params: visibleItem}},
	})
	window, _, err := snapshot.LatestItemCandidates(40)
	if err != nil {
		t.Fatalf("LatestItemCandidates: %v", err)
	}
	if len(window.Candidates) != 1 || window.Candidates[0].Position != (appwire.ThreadItemPosition{Entry: 6, Item: 0}) {
		t.Fatalf("visible candidate=%+v, want entry 6 after zero-item entry", window.Candidates)
	}
}

func TestAppTurnSnapshotUpsertMatchesStableKeyAcrossDisplayIDs(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 7, Item: 2}
	key := appitempaging.TranscriptItemKey("turn_resume", position)
	snapshot := &appTurnSnapshot{}
	snapshot.Seed(appTurnSeed{
		Turns: []appwire.Turn{{ID: "turn_resume", Status: appwire.TurnStatusCompleted, Items: []appwire.ThreadItem{{
			ID: "historical-id", TurnID: "turn_resume", Type: "agentMessage", Text: "old", Status: appwire.TurnStatusInProgress,
			TranscriptKey: key, Position: &position,
		}}}},
		ThreadRef: "local:resume", TranscriptIncarnation: "inc-resume", NextEntry: 8,
	})
	completed, err := json.Marshal(appwire.ItemLifecycleParams{TurnID: "turn_resume", Item: appwire.ThreadItem{
		ID: "live-id", TurnID: "turn_resume", Type: "agentMessage", Text: "new", Status: appwire.TurnStatusCompleted,
		TranscriptKey: key, Position: &position,
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Apply([]appserver.SequencedNotification{{Seq: 1, Notification: appwire.Notification{Method: appwire.NotifyItemCompleted, Params: completed}}})
	turns := snapshot.Snapshot()
	if len(turns) != 1 || len(turns[0].Items) != 1 {
		t.Fatalf("resumed item upsert produced turns=%+v, want one persisted item", turns)
	}
	got := turns[0].Items[0]
	if got.ID != "live-id" || got.Text != "new" || got.TranscriptKey != key || got.Position == nil || *got.Position != position {
		t.Fatalf("resumed item = %+v, want updated display fields with stable identity", got)
	}
}
