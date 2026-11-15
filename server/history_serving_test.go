package server

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestHistoryServingLiveShapesCarryViewFacts(t *testing.T) {
	srv := NewServer(ServerConfig{})
	entries := []schema.Turn{}
	for _, id := range []string{"A", "B", "C"} {
		entries = append(entries, schema.Turn{Format: schema.TurnFormatIdentity, TurnID: id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(id)})
	}
	st := newServedTranscript(t, srv, "instance-A", entries...)
	st.settle(t)
	client := dialServerAppWire(t, srv)
	var read appwire.ThreadHistoryReadResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: "local:instance-A", IncludeTurns: true, ItemLimit: 1}, &read); err != nil {
		t.Fatal(err)
	}
	if read.ServedFrom != "live-daemon" || read.InstanceID != "instance-A" || read.LatestExecutionTurnID != "C" {
		t.Fatalf("read facts=%+v", read)
	}
	if _, err := time.Parse(time.RFC3339Nano, read.AsOf); err != nil {
		t.Fatal(err)
	}
	if read.OlderCursor == "" {
		t.Fatal("fixture did not create an older page")
	}
	var page appwire.ThreadHistoryTurnsListResponse
	if err := client.Request(t.Context(), appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{Ref: "local:instance-A", Cursor: read.OlderCursor, ItemLimit: 1}, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "B" {
		t.Fatalf("page=%+v", page.Data)
	}
	if page.ServedFrom != "live-daemon" || page.InstanceID != "instance-A" || page.LatestExecutionTurnID != "C" {
		t.Fatalf("page facts=%+v", page)
	}
}

func TestHistoryServingReplacementKeepsCapturedInstance(t *testing.T) {
	for _, page := range []bool{false, true} {
		name := "read"
		if page {
			name = "page"
		}
		t.Run(name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			old := newServedTranscript(t, srv, "instance-A",
				inExecution("A", true, schema.NewTurn(schema.TurnUserInput, llm.User("old-only"))),
				inExecution("A", false, schema.NewTurn(schema.TurnAssistant, llm.Assistant("old-recent"))))
			old.settle(t)
			latest, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{Ref: "local:instance-A", IncludeTurns: true, ItemLimit: 1})
			if err != nil || latest.OlderCursor == "" {
				t.Fatalf("initial cursor setup: %v %+v", err, latest)
			}
			client := dialServerAppWire(t, srv)
			history := srv.appHistoryForID("instance-A")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			hook := func(h *threadHistory) {
				if h == history {
					close(entered)
					<-release
				}
			}
			historyReadAfterBeginHook.Store(&hook)
			t.Cleanup(func() { historyReadAfterBeginHook.Store(nil) })
			result := make(chan error, 1)
			var read appwire.ThreadHistoryReadResponse
			var older appwire.ThreadHistoryTurnsListResponse
			go func() {
				if page {
					result <- client.Request(t.Context(), appwire.MethodThreadHistoryTurnsList, appwire.ThreadTurnsListParams{Ref: "local:instance-A", Cursor: latest.OlderCursor, ItemLimit: 1}, &older)
				} else {
					result <- client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: "local:instance-A", IncludeTurns: true}, &read)
				}
			}()
			select {
			case <-entered:
			case err := <-result:
				t.Fatalf("read returned before admission: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("read never reached admission barrier")
			}
			path := filepath.Join(t.TempDir(), "new.transcript.jsonl")
			writer, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: "instance-B", CreatedAt: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.Close() })
			if err := writer.Append(inExecution("B", true, schema.NewTurn(schema.TurnUserInput, llm.User("new-only")))); err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareAppIdentity("local", "instance-B", path)
			if err != nil {
				t.Fatal(err)
			}
			prepared.ref = "local:instance-A"
			replaced := make(chan struct{})
			go func() {
				srv.ReplaceAppIdentity(prepared.WithRecordedLength(writer.RecordedLength()).WithBootGeneration("2"), nil)
				close(replaced)
			}()
			// Closing old history happens after the new identity has committed,
			// and waits for this admitted read. No roster sampling or sleeps.
			select {
			case <-history.stop:
			case <-time.After(10 * time.Second):
				t.Fatal("replacement never retired old history")
			}
			meta, err := srv.handleAppThreadRead(appwire.WithHistoryServingFacts(t.Context()), appwire.ThreadReadParams{Ref: "local:instance-A"})
			if err != nil {
				t.Fatal(err)
			}
			if meta.Thread.Evener.InstanceID != "instance-B" {
				t.Fatalf("replacement did not commit: %+v", meta.Thread.Evener)
			}
			unblock()
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			<-replaced
			instance, frontier, turns := read.InstanceID, read.LatestExecutionTurnID, read.Thread.Turns
			if page {
				instance, frontier, turns = older.InstanceID, older.LatestExecutionTurnID, older.Data
			}
			if instance != "instance-A" || frontier != "A" || len(turns) != 1 || turns[0].Items[0].Text != "old-only" {
				t.Fatalf("mixed replacement result: instance=%q frontier=%q turns=%+v", instance, frontier, turns)
			}
			var refreshed appwire.ThreadHistoryReadResponse
			if err := client.Request(t.Context(), appwire.MethodThreadHistoryRead, appwire.ThreadReadParams{Ref: "local:instance-A", IncludeTurns: true}, &refreshed); err != nil {
				t.Fatal(err)
			}
			if refreshed.InstanceID != "instance-B" || refreshed.LatestExecutionTurnID != "B" || len(refreshed.Thread.Turns) != 1 || refreshed.Thread.Turns[0].Items[0].Text != "new-only" {
				t.Fatalf("refreshed replacement view=%+v", refreshed)
			}
		})
	}
}

func TestHistoryServingRotationUsesOneIndexView(t *testing.T) {
	srv := NewServer(ServerConfig{})
	st := newServedTranscript(t, srv, "instance-A", inExecution("A", true, schema.NewTurn(schema.TurnUserInput, llm.User("only-A"))))
	st.settle(t)
	before := st.read(t)
	rotated := ""
	threadReadAfterCutHook = func() {
		idx, err := transcriptindex.Open(st.path, transcriptindex.DirFor(st.path))
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()
		if err := idx.Rebuild(st.writer.RecordedLength()); err != nil {
			t.Fatal(err)
		}
		rotated, err = idx.Incarnation()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { threadReadAfterCutHook = nil })
	response, err := srv.handleAppThreadRead(appwire.WithHistoryServingFacts(t.Context()), appwire.ThreadReadParams{Ref: "local:instance-A", IncludeTurns: true, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	read := appwire.HistoryReadResponse(response)
	if rotated == "" || before.Snapshot.Incarnation == rotated {
		t.Fatal("index rotation scenario did not execute")
	}
	if read.Snapshot.Incarnation != rotated || read.LatestExecutionTurnID != "A" || read.InstanceID != "instance-A" || read.ServedFrom != "live-daemon" || len(read.Thread.Turns) != 1 || read.Thread.Turns[0].Items[0].Text != "only-A" {
		t.Fatalf("mixed rotated read=%+v", read)
	}
}
