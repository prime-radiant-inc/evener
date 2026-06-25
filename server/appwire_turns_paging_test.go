package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
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
