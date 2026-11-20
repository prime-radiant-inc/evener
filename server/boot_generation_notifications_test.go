package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

// A client retires its held view of a thread when the frames that describe it
// name a different boot generation, so every frame a client keeps beside
// history/updated must carry the SAME generation history/updated does. Two of
// them never did: evener/delegate/updated and overlay/upserted. A client that
// absorbed either from a replaced daemon could not tell it belonged to a boot
// it had moved past.
//
// The root's generation is its counter; a descendant's is that counter
// qualified by the root (DescendantBootGeneration). Both are asserted against
// the generation an actual history/updated frame carried for the same thread,
// so this cannot pass by both sides being empty.
func TestDelegateAndOverlayNotificationsCarryTheThreadsBootGeneration(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		srv := NewServer(ServerConfig{})
		st := newServedTranscriptAt(t, srv, "root", "7", schema.NewTurn(schema.TurnUserInput, llm.User("first")))
		cursor := srv.appNotifier.CurrentSequence()
		st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("second")))
		st.settle(t)
		srv.RecordAppEvent(threadEvent("root", events.DelegateUpdatedData{
			DelegateID: "dlg_1", OwnerSessionID: "root", RootSessionID: "root", ChildSessionID: "child",
			Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle", ProjectionRevision: 1,
		}))
		srv.RecordAppEvent(threadEvent("root", events.RoundStartedData{RoundID: "r_1"}))
		srv.RecordAppEvent(threadEvent("root", events.AssistantTextDeltaData{Delta: "streaming"}))
		srv.RecordAppEvent(threadEvent("root", events.NotesUpdatedData{HumanNote: "note"}))

		if want := historyBootGeneration(t, srv, cursor, "root"); want != "7" {
			t.Fatalf("history/updated boot generation = %q, want the served root's 7", want)
		}
		assertDelegateAndOverlayBootGeneration(t, srv, cursor, "root", "7")

		// The stamp is scoped to the two frames whose identity a client keeps
		// beside history. Every other notification is left exactly as it was.
		for _, n := range srv.AppNotificationsAfter(cursor, "root") {
			if n.Notification.Method != appwire.NotifyEvenerNotesUpdated {
				continue
			}
			if generation, present := wireBootGeneration(t, n); present {
				t.Fatalf("notes/updated carries bootGeneration %q, want the field absent", generation)
			}
		}
	})

	t.Run("descendant", func(t *testing.T) {
		root := newServedTranscriptAt(t, NewServer(ServerConfig{}), "root", "3")
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
		child.OnRecorded(func(rec transcript.Record) { srv.recordTranscriptEntry("child", rec) })
		srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionStartData{}))
		history := srv.appHistoryForID("child")
		if history == nil {
			t.Fatal("the descendant has no history")
		}
		cursor := srv.appNotifier.CurrentSequence()
		if rec, err := child.Record(schema.NewTurn(schema.TurnUserInput, llm.User("child work")), transcript.RecordOptions{}); err != nil || !rec.Recorded {
			t.Fatalf("record = %+v, %v", rec, err)
		}
		root.published.await(t, history, child.RecordedLength())
		srv.RecordDescendantAppEvent("root", threadEvent("child", events.DelegateUpdatedData{
			DelegateID: "dlg_1", OwnerSessionID: "root", RootSessionID: "root", ChildSessionID: "grandchild",
			AncestorSessionIDs: []string{"child"},
			Type:               "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle", ProjectionRevision: 1,
		}))
		srv.RecordDescendantAppEvent("root", threadEvent("child", events.RoundStartedData{RoundID: "r_1"}))
		srv.RecordDescendantAppEvent("root", threadEvent("child", events.AssistantTextDeltaData{Delta: "streaming"}))

		const want = "3@root"
		if got := historyBootGeneration(t, srv, cursor, "child"); got != want {
			t.Fatalf("descendant history/updated boot generation = %q, want %q", got, want)
		}
		assertDelegateAndOverlayBootGeneration(t, srv, cursor, "child", want)
	})
}

// historyBootGeneration is the generation the last history/updated for target
// carried, and fails when the thread published none.
func historyBootGeneration(t *testing.T, srv *Server, cursor uint64, target string) string {
	t.Helper()
	generation, found := "", false
	for _, n := range srv.AppNotificationsAfter(cursor, target) {
		if n.Notification.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		got, present := wireBootGeneration(t, n)
		if !present {
			t.Fatalf("history/updated for %s carries no bootGeneration", target)
		}
		generation, found = got, true
	}
	if !found {
		t.Fatalf("%s published no history/updated", target)
	}
	return generation
}

// assertDelegateAndOverlayBootGeneration requires target to have published both
// a delegate update and an overlay upsert, each carrying want.
func assertDelegateAndOverlayBootGeneration(t *testing.T, srv *Server, cursor uint64, target, want string) {
	t.Helper()
	seen := map[string]int{}
	for _, n := range srv.AppNotificationsAfter(cursor, target) {
		switch n.Notification.Method {
		case appwire.NotifyEvenerDelegateUpdated, appwire.NotifyOverlayUpserted:
			generation, present := wireBootGeneration(t, n)
			if !present || generation != want {
				t.Fatalf("%s bootGeneration = %q (present %v), want the history/updated's %q", n.Notification.Method, generation, present, want)
			}
			seen[n.Notification.Method]++
		}
	}
	for _, method := range []string{appwire.NotifyEvenerDelegateUpdated, appwire.NotifyOverlayUpserted} {
		if seen[method] == 0 {
			t.Fatalf("%s published no %s", target, method)
		}
	}
}

// wireBootGeneration reports a notification's bootGeneration member exactly as
// it arrived, and whether the member is present at all: the field is
// omitempty, so a missing key and an empty generation are different claims.
func wireBootGeneration(t *testing.T, n appserver.SequencedNotification) (string, bool) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(n.Notification.Params, &fields); err != nil {
		t.Fatalf("%s params: %v", n.Notification.Method, err)
	}
	raw, present := fields["bootGeneration"]
	if !present {
		return "", false
	}
	var generation string
	if err := json.Unmarshal(raw, &generation); err != nil {
		t.Fatalf("%s bootGeneration: %v", n.Notification.Method, err)
	}
	return generation, true
}
