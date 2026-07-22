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
// exchanges and returns a daemon Server reading from it.
func seedTranscriptServer(t *testing.T, pairs int) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1", CreatedAt: time.Now(), ProfileID: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < pairs; i++ {
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
	srv.SetAppIdentity("local", "th_1")
	srv.SetTranscriptPathFunc(func() string { return path })
	srv.SetSteerFunc(func(string) {})
	srv.SetCancelFunc(func() {})
	return srv
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

func requireAppAllTurns(t testing.TB, srv *Server, threadID string) []appwire.Turn {
	t.Helper()
	turns, err := srv.appAllTurns(threadID)
	if err != nil {
		t.Fatalf("appAllTurns: %v", err)
	}
	return turns
}

func requireAppLatestTurns(t testing.TB, srv *Server, threadID string, limit int) ([]appwire.Turn, string) {
	t.Helper()
	turns, cursor, err := srv.appLatestTurns(threadID, limit)
	if err != nil {
		t.Fatalf("appLatestTurns: %v", err)
	}
	return turns, cursor
}

func requireAppPageTurns(t testing.TB, srv *Server, threadID, cursor string, limit int) appwire.ThreadTurnsListResponse {
	t.Helper()
	page, err := srv.appPageTurns(threadID, cursor, limit)
	if err != nil {
		t.Fatalf("appPageTurns: %v", err)
	}
	return page
}

func TestDaemonThreadReadWindowsAndTurnsListPagesToHead(t *testing.T) {
	srv := seedTranscriptServer(t, 4)
	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))

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

func TestDaemonTranscriptReadersPropagateUnsupportedFormat(t *testing.T) {
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
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_1")
			srv.SetTranscriptPathFunc(func() string { return path })

			for _, params := range []appwire.ThreadReadParams{
				{IncludeTurns: true},
				{IncludeTurns: true, TurnLimit: 1},
			} {
				resp, err := srv.handleAppThreadRead(context.Background(), params)
				if !errors.Is(err, transcript.ErrUnsupportedFormat) || resp.Thread.Turns != nil {
					t.Fatalf("thread/read = (%+v, %v), want empty ErrUnsupportedFormat", resp, err)
				}
			}
			page, err := srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{Limit: 1})
			if !errors.Is(err, transcript.ErrUnsupportedFormat) || page.Data != nil {
				t.Fatalf("thread/turns/list = (%+v, %v), want empty ErrUnsupportedFormat", page, err)
			}
		})
	}
}

func TestServerAppWireBoundedReadsDoNotProjectFullTranscript(t *testing.T) {
	srv := seedTranscriptServer(t, 100)
	conn := srv.AppServer().NewConnection("bounded-work")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))

	all := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true}).Thread.Turns
	if len(all) != 200 {
		t.Fatalf("full transcript has %d turns, want 200", len(all))
	}
	wantLatest, wantCursor := appwire.WindowTurns(all, 40)
	wantPage := appwire.PageTurns(all, wantCursor, 30)

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) {
		projected = append(projected, stats.ProjectedTurns)
	})
	t.Cleanup(restore)

	latest := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, TurnLimit: 40})
	if !reflect.DeepEqual(latest.Thread.Turns, wantLatest) || latest.OlderCursor != wantCursor {
		t.Fatalf("latest bounded response differs from full reference")
	}
	if !reflect.DeepEqual(projected, []int{40}) {
		t.Fatalf("latest read used legacy full projection of %d turns; bounded projection reports = %v, want [40]", len(all), projected)
	}

	projected = nil
	page := listTurns(t, conn, appwire.ThreadTurnsListParams{Ref: "local:th_1", Cursor: wantCursor, Limit: 30})
	if !reflect.DeepEqual(page, wantPage) {
		t.Fatalf("bounded page differs from full reference")
	}
	if !reflect.DeepEqual(projected, []int{30, 0}) {
		t.Fatalf("page read used legacy full projection of %d turns; bounded projection reports = %v, want page plus zero-projection count [30 0]", len(all), projected)
	}
}

func TestServerAppWireNotificationSnapshotAdvancesFromLastSequence(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "complete"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	// Simulate attaching the snapshot after retained notifier history already
	// exists, so the first read must initialize lazily from ReplayAfter(0).
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("notification-snapshot")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))

	processed := 0
	previousHook := appTurnsEnsureTurnHook
	appTurnsEnsureTurnHook = func(string) bool {
		processed++
		return false
	}
	t.Cleanup(func() { appTurnsEnsureTurnHook = previousHook })

	first := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, TurnLimit: 40}).Thread.Turns
	if processed == 0 {
		t.Fatal("initial notification snapshot processed no records")
	}

	processed = 0
	second := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, TurnLimit: 40}).Thread.Turns
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("unchanged snapshot differs\n got: %#v\nwant: %#v", second, first)
	}
	if processed != 0 {
		t.Fatalf("unchanged read replayed retained notification history: processed=%d, want 0", processed)
	}

	processed = 0
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: " tail"}})
	want := appTurnsFromNotifications(srv.AppNotificationsAfter(0, "th_1"))
	// Exclude construction of the full replay reference from the incremental
	// work count while retaining the direct RecordAppEvent application.
	processed = 1
	third := readTurns(t, conn, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, TurnLimit: 40}).Thread.Turns
	if !reflect.DeepEqual(third, want) {
		t.Fatalf("incremental snapshot differs from full replay\n got: %#v\nwant: %#v", third, want)
	}
	if processed != 1 {
		t.Fatalf("incremental read processed %d turn records, want only the appended delta", processed)
	}
}

func TestServerAppWireDirectPageUsesExactTranscriptAuthority(t *testing.T) {
	srv := seedTranscriptServer(t, 100)
	// Notifications start at turn_1, but retained notification history is shorter
	// than the transcript. A direct page must still choose the richer transcript.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "notification-only"}})

	all := requireAppAllTurns(t, srv, "th_1")
	want := appwire.PageTurns(all, "160", 30)
	if len(want.Data) != 30 {
		t.Fatalf("reference page has %d turns, want 30", len(want.Data))
	}

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) {
		projected = append(projected, stats.ProjectedTurns)
	})
	t.Cleanup(restore)
	got := requireAppPageTurns(t, srv, "th_1", "160", 30)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct page did not preserve transcript authority\n got: %v\nwant: %v", turnIDs(got.Data), turnIDs(want.Data))
	}
	if !reflect.DeepEqual(projected, []int{30, 0}) {
		t.Fatalf("direct page projection reports = %v, want requested page plus zero-projection count [30 0]", projected)
	}
}

func TestServerAppWireNotificationSnapshotMatchesRetainedWindowAfterEviction(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 5})
	srv.SetAppIdentity("local", "th_1")
	for _, text := range []string{"first", "second", "third"} {
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: text}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: text + " reply"}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	}

	wantAll := appTurnsFromNotifications(srv.AppNotificationsAfter(0, "th_1"))
	want, wantCursor := appwire.WindowTurns(wantAll, 40)
	got, gotCursor := requireAppLatestTurns(t, srv, "th_1", 40)
	if !reflect.DeepEqual(got, want) || gotCursor != wantCursor {
		t.Fatalf("bounded notification snapshot retained evicted state\n got: %v cursor=%q\nwant: %v cursor=%q", turnIDs(got), gotCursor, turnIDs(want), wantCursor)
	}
	for _, turn := range got {
		for _, item := range turn.Items {
			if item.Text == "first" || item.Text == "first reply" {
				t.Fatalf("evicted item survived retained snapshot: %+v", item)
			}
		}
	}
}

func appendTranscriptTurns(t *testing.T, path string, count int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for i := 0; i < count; i++ {
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
	for i := 0; i < count; i++ {
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "notification"}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "reply"}})
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	}
}

func TestServerAppWirePageRecomputesTranscriptAuthorityAfterInputsChange(t *testing.T) {
	t.Run("transcript append reverses notification authority", func(t *testing.T) {
		srv := seedTranscriptServer(t, 1)
		recordNotificationTurns(t, srv, 4)
		_, _ = requireAppLatestTurns(t, srv, "th_1", 1)
		appendTranscriptTurns(t, srv.transcriptPath(), 8)

		want := appwire.PageTurns(requireAppAllTurns(t, srv, "th_1"), "8", 2)
		var projected []int
		restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) { projected = append(projected, stats.ProjectedTurns) })
		t.Cleanup(restore)
		got := requireAppPageTurns(t, srv, "th_1", "8", 2)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("page retained stale notification authority\n got: %v\nwant: %v", turnIDs(got.Data), turnIDs(want.Data))
		}
		if !reflect.DeepEqual(projected, []int{2, 0}) {
			t.Fatalf("projection reports = %v, want page plus zero-projection count [2 0]", projected)
		}
	})

	t.Run("notification growth reverses transcript authority", func(t *testing.T) {
		srv := seedTranscriptServer(t, 2)
		_, _ = requireAppLatestTurns(t, srv, "th_1", 1)
		recordNotificationTurns(t, srv, 4)

		want := appwire.PageTurns(requireAppAllTurns(t, srv, "th_1"), "4", 2)
		var projected []int
		restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) { projected = append(projected, stats.ProjectedTurns) })
		t.Cleanup(restore)
		got := requireAppPageTurns(t, srv, "th_1", "4", 2)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("page retained stale transcript authority\n got: %v\nwant: %v", turnIDs(got.Data), turnIDs(want.Data))
		}
		if !reflect.DeepEqual(projected, []int{2, 0}) {
			t.Fatalf("projection reports = %v, want page plus zero-projection count [2 0]", projected)
		}
	})
}

func TestServerAppWireOldIdentityReplayCannotPopulateNewSnapshot(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "old", Data: events.UserInputData{Text: "old turn"}})
	oldThreadID := srv.appThread().ID

	srv.SetAppIdentity("local", "new")
	oldTurns, _ := srv.appNotificationTurns(oldThreadID)
	if len(oldTurns) != 0 {
		t.Fatalf("old identity read returned turns after switch: %v", turnIDs(oldTurns))
	}
	newTurns, _ := srv.appNotificationTurns("new")
	if len(newTurns) != 0 {
		t.Fatalf("old identity replay populated new snapshot: %v", turnIDs(newTurns))
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
	snapshot := &appTurnSnapshot{limit: 100, cursor: 1, turns: []appwire.Turn{retained}, turnIndex: map[string]int{"turn_1": 0}}

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
		for i := 0; i < 100; i++ {
			_ = snapshot.Snapshot()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
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

func TestAppTurnSnapshotDoesNotDropEarlierConcurrentRecord(t *testing.T) {
	notifier := appserver.NewNotifier(10)
	snapshot := &appTurnSnapshot{threadID: "th_1", limit: 10}
	params := func(delta string) appwire.AgentMessageDeltaParams {
		return appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: delta}
	}

	recordedFirst := make(chan appserver.SequencedNotification, 1)
	releaseFirst := make(chan struct{})
	done := make(chan struct{})
	go func() {
		first := notifier.Record("th_1", appwire.NotifyAgentMessageDelta, params("first"))
		recordedFirst <- first
		<-releaseFirst
		snapshot.Apply([]appserver.SequencedNotification{first})
		close(done)
	}()
	first := <-recordedFirst
	second := notifier.Record("th_1", appwire.NotifyAgentMessageDelta, params(" second"))
	if second.Seq != first.Seq+1 {
		t.Fatalf("sequences = %d, %d, want consecutive", first.Seq, second.Seq)
	}
	snapshot.Apply([]appserver.SequencedNotification{second})
	close(releaseFirst)
	<-done

	turns := snapshot.Snapshot()
	if len(turns) != 1 || len(turns[0].Items) != 1 || turns[0].Items[0].Text != "first second" {
		t.Fatalf("out-of-order concurrent apply dropped earlier record: %+v", turns)
	}
}

func TestServerAppWireCrossThreadEvictionRebuildsCurrentSnapshot(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 2})
	srv.SetAppIdentity("local", "current")
	currentParams := appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "current"}
	current := srv.appNotifier.Record("current", appwire.NotifyAgentMessageDelta, currentParams)
	srv.appTurns.Apply([]appserver.SequencedNotification{current})
	if got := srv.appTurns.Snapshot(); len(got) != 1 {
		t.Fatalf("initial current snapshot = %+v, want one turn", got)
	}

	// Model old RecordAppEvent work captured before the identity switch: it can
	// still record globally afterward, but must not apply into the current snapshot.
	srv.appNotifier.Record("old", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{TurnID: "old_1", ItemID: "old_item_1", Delta: "old"})
	srv.appNotifier.Record("old", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{TurnID: "old_2", ItemID: "old_item_2", Delta: "old"})
	if retained := srv.AppNotificationsAfter(0, "current"); len(retained) != 0 {
		t.Fatalf("current notifier records were not globally evicted: %+v", retained)
	}

	got, _ := srv.appNotificationTurns("current")
	if len(got) != 0 {
		t.Fatalf("current snapshot retained globally evicted state: %+v", got)
	}
}

func TestServerAppWireStaleTranscriptPathCannotCrossIdentity(t *testing.T) {
	oldPath := seedTranscriptServer(t, 1).transcriptPath()
	newPath := seedTranscriptServer(t, 2).transcriptPath()
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")

	entered := make(chan struct{})
	release := make(chan struct{})
	srv.SetTranscriptPathFunc(func() string {
		close(entered)
		<-release
		return newPath
	})
	type latestResult struct {
		turns []appwire.Turn
		err   error
	}
	done := make(chan latestResult, 1)
	go func() {
		turns, _, err := srv.appLatestTurns("old", 40)
		done <- latestResult{turns: turns, err: err}
	}()
	<-entered
	srv.SetAppIdentity("local", "new")
	srv.SetTranscriptPathFunc(func() string { return oldPath })
	close(release)
	if result := <-done; result.err != nil || len(result.turns) != 0 {
		t.Fatalf("stale old-identity latest returned (%v, %v)", turnIDs(result.turns), result.err)
	}

	entered = make(chan struct{})
	release = make(chan struct{})
	srv.SetTranscriptPathFunc(func() string {
		close(entered)
		<-release
		return oldPath
	})
	type pageResult struct {
		page appwire.ThreadTurnsListResponse
		err  error
	}
	pageDone := make(chan pageResult, 1)
	go func() {
		page, err := srv.appPageTurns("new", "2", 1)
		pageDone <- pageResult{page: page, err: err}
	}()
	<-entered
	srv.SetAppIdentity("local", "newer")
	srv.SetTranscriptPathFunc(func() string { return newPath })
	close(release)
	if result := <-pageDone; result.err != nil || len(result.page.Data) != 0 || result.page.NextCursor != "" {
		t.Fatalf("stale page crossed identity generation: %+v err=%v", result.page, result.err)
	}
}

func TestAppTurnSnapshotRejectsStaleWindowAfterCurrentApply(t *testing.T) {
	notifier := appserver.NewNotifier(2)
	snapshot := &appTurnSnapshot{threadID: "current", limit: 2}
	current := notifier.Record("current", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "one"})
	snapshot.Apply([]appserver.SequencedNotification{current})
	notifier.Record("other", appwire.NotifyAgentMessageDelta, nil)
	stale := notifier.RetainedWindow("current")

	notifier.Record("other", appwire.NotifyAgentMessageDelta, nil) // globally evicts current seq 1
	latest := notifier.Record("current", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{TurnID: "turn_4", ItemID: "item_4", Delta: "four"})
	snapshot.Apply([]appserver.SequencedNotification{latest})

	got := snapshot.ReconcileAndSnapshot(stale.LowerSeq, stale.Records)
	if !notifier.RetainedWindowCurrent(stale.UpperSeq) {
		fresh := notifier.RetainedWindow("current")
		got = snapshot.ReconcileAndSnapshot(fresh.LowerSeq, fresh.Records)
	}
	want := appTurnsFromNotifications(notifier.RetainedWindow("current").Records)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale retained window produced state that never existed\n got: %v\nwant: %v", turnIDs(got), turnIDs(want))
	}
}

func TestAppTurnSnapshotRejectsDelayedGloballyEvictedDirectApply(t *testing.T) {
	notifier := appserver.NewNotifier(2)
	snapshot := &appTurnSnapshot{threadID: "current", limit: 2}
	delayed := notifier.Record("current", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_1", Delta: "evicted"})
	notifier.Record("other", appwire.NotifyAgentMessageDelta, nil)
	notifier.Record("other", appwire.NotifyAgentMessageDelta, nil) // globally evicts delayed seq 1

	window := notifier.RetainedWindow("current")
	_ = snapshot.ReconcileAndSnapshot(window.LowerSeq, window.Records)
	snapshot.Apply([]appserver.SequencedNotification{delayed})
	if got := snapshot.Snapshot(); len(got) != 0 {
		t.Fatalf("delayed globally evicted direct apply contaminated snapshot: %+v", got)
	}
}

func TestServerAppWireOldIdentityRejectsCallbackBackingSwitchBeforeIdentity(t *testing.T) {
	writeTranscript := func(sessionID string, turns int) string {
		path := filepath.Join(t.TempDir(), sessionID+".transcript.jsonl")
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID, CreatedAt: time.Unix(1700000000, 0), ProfileID: "openai", Model: "gpt-5"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < turns; i++ {
			if err := writer.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant(sessionID))); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldPath := writeTranscript("old", 1)
	newPath := writeTranscript("new", 3)
	backingPath := oldPath
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	srv.SetTranscriptPathFunc(func() string { return backingPath })

	// Match production clear ordering: setSession changes callback backing before
	// SetAppIdentity advances the server identity generation.
	backingPath = newPath
	turns, _ := requireAppLatestTurns(t, srv, "old", 40)
	if len(turns) != 0 {
		t.Fatalf("old identity returned new callback backing before identity switch: %v", turnIDs(turns))
	}
}

func TestValidatedTranscriptPathReadsOnlyLeadingHeader(t *testing.T) {
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
		for i := 0; i < entries; i++ {
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
	if got := validatedTranscriptPath("th_1", large); got != large {
		t.Fatalf("validated path=%q, want %q", got, large)
	}

	smallAllocs := testing.AllocsPerRun(3, func() { _ = validatedTranscriptPath("th_1", small) })
	largeAllocs := testing.AllocsPerRun(3, func() { _ = validatedTranscriptPath("th_1", large) })
	if largeAllocs > smallAllocs+10 {
		t.Fatalf("large no-api_call identity validation inspected historical entries: allocations large=%.0f small=%.0f", largeAllocs, smallAllocs)
	}
}

func TestValidatedTranscriptPathUsesFirstNonEmptyHeaderIdentity(t *testing.T) {
	write := func(sessionID string) string {
		path := filepath.Join(t.TempDir(), sessionID+".transcript.jsonl")
		body := "\n \r\n" + fmt.Sprintf(`{"kind":"header","format_version":2,"session_id":%q}`, sessionID) + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	matching := write("th_1")
	if got := validatedTranscriptPath("th_1", matching); got != matching {
		t.Fatalf("matching leading-blank transcript path = %q, want %q", got, matching)
	}
	mismatched := write("th_other")
	if got := validatedTranscriptPath("th_1", mismatched); got != "" {
		t.Fatalf("mismatched leading-blank transcript path = %q, want rejection", got)
	}
}
