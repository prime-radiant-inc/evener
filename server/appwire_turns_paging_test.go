package server

import (
	"context"
	"path/filepath"
	"reflect"
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
	if !reflect.DeepEqual(projected, []int{30}) {
		t.Fatalf("page read used legacy full projection of %d turns; bounded projection reports = %v, want [30]", len(all), projected)
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

	all := srv.appAllTurns("th_1")
	want := appwire.PageTurns(all, "160", 30)
	if len(want.Data) != 30 {
		t.Fatalf("reference page has %d turns, want 30", len(want.Data))
	}

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) {
		projected = append(projected, stats.ProjectedTurns)
	})
	t.Cleanup(restore)
	got := srv.appPageTurns("th_1", "160", 30)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct page did not preserve transcript authority\n got: %v\nwant: %v", turnIDs(got.Data), turnIDs(want.Data))
	}
	if !reflect.DeepEqual(projected, []int{30, 0}) {
		t.Fatalf("direct page projection reports = %v, want requested page plus zero-projection count [30 0]", projected)
	}
}
