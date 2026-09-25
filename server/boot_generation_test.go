package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// Every history message the daemon sends carries the served identity's boot
// generation: history/updated (root and descendant), the resync push, both
// read responses, and a history read's error data.
func TestHistoryMessagesCarryTheBootGeneration(t *testing.T) {
	srv := NewServer(ServerConfig{})
	st := newServedTranscriptAt(t, srv, "root", "7",
		schema.NewTurn(schema.TurnUserInput, llm.User("one")),
		schema.NewTurn(schema.TurnUserInput, llm.User("two")))
	cursor := srv.appNotifier.CurrentSequence()
	st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("three")))
	st.settle(t)
	updates := 0
	for _, n := range srv.AppNotificationsAfter(cursor, "root") {
		if n.Notification.Method == appwire.NotifyHistoryUpdated {
			updates++
			if got := notificationParams[appwire.HistoryUpdatedParams](t, n).BootGeneration; got != "7" {
				t.Fatalf("history/updated boot generation = %q, want 7", got)
			}
		}
	}
	if updates == 0 {
		t.Fatal("no history/updated")
	}

	read, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: "root", IncludeTurns: true, ItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if read.BootGeneration != "7" || read.OlderCursor == "" {
		t.Fatalf("thread/read boot generation %q, older cursor %q", read.BootGeneration, read.OlderCursor)
	}
	page, err := srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{ThreadID: "root", Cursor: read.OlderCursor, ItemLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.BootGeneration != "7" {
		t.Fatalf("thread/turns/list boot generation = %q, want 7", page.BootGeneration)
	}

	// A failed thread whose index cannot be read either.
	history := srv.appHistoryForID("root")
	history.mu.Lock()
	history.failed = &transcriptindex.EntryError{Ordinal: 1, Err: errors.New("unprojectable")}
	epoch := history.epoch
	history.mu.Unlock()
	if err := srv.appHistories.cache.Close(); err != nil {
		t.Fatal(err)
	}
	for label, err := range map[string]error{
		"thread/read": func() error {
			_, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: "root", IncludeTurns: true})
			return err
		}(),
		"thread/turns/list": func() error {
			_, err := srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{ThreadID: "root", Cursor: read.OlderCursor})
			return err
		}(),
	} {
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) {
			t.Fatalf("%s error = %v, want a wire error", label, err)
		}
		data, ok := wireErr.Data.(appwire.HistoryReadErrorData)
		if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptHistoryFailed || data.BootGeneration != "7" || data.Epoch != epoch {
			t.Fatalf("%s error data = %#v, want transcriptHistoryFailed at generation 7, epoch %d", label, wireErr.Data, epoch)
		}
	}
}

func TestDescendantHistoryAndResyncCarryTheBootGeneration(t *testing.T) {
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	rootPath := writeDelegateTranscript(t, "root", "root history")
	prepared, err := PrepareAppIdentityForRef("local", "root", "local:workspace", rootPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("3"), nil)
	childPath := writeDelegateTranscript(t, "child", "child history")
	srv.SetDescendantTranscriptPathFunc(func(string) string { return childPath })
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionStartData{}))
	read, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{Ref: "local:child", IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	// A descendant carries its root's generation qualified by the root.
	if read.BootGeneration != "3@root" {
		t.Fatalf("descendant read boot generation = %q, want 3@root", read.BootGeneration)
	}

	cursor := srv.appNotifier.CurrentSequence()
	nextPath := writeDelegateTranscript(t, "next", "next history")
	prepared, err = PrepareAppIdentityForRef("local", "next", "local:workspace", nextPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	resyncs := 0
	for _, n := range srv.AppNotificationsAfter(cursor, "local:workspace") {
		if n.Notification.Method == appwire.NotifyEvenerThreadResync {
			resyncs++
			if got := notificationParams[appwire.ThreadResyncParams](t, n).BootGeneration; got != "1" {
				t.Fatalf("resync boot generation = %q, want the new identity's 1", got)
			}
		}
	}
	if resyncs == 0 {
		t.Fatal("no resync")
	}
}

// serveRootWithoutHistory makes threadID the served root with no transcript
// of its own, at boot generation 1: descendants' histories take their
// generation from it.
func serveRootWithoutHistory(t *testing.T, srv *Server, threadID string) {
	t.Helper()
	prepared, err := PrepareAppIdentity("local", threadID, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
}

// A thread history created without a boot generation fails loudly under
// test (and under the fuzz build's invariants) instead of publishing
// "bootGeneration":"".
func TestAThreadHistoryWithNoBootGenerationFailsLoudly(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("a thread history with no boot generation was created")
		}
	}()
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	prepared, err := PrepareAppIdentity("local", "root", writeDelegateTranscript(t, "root", "r"))
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
}
