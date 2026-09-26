package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
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
	srv.SetSteerFunc(func(string) error { ; return nil })
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

// A thread served from a transcript reads its persisted history: a persisted
// communicate call is the agent message it said.
func TestServedTranscriptReadsPersistedCommunicate(t *testing.T) {
	call := llm.ToolCallData{ID: "persisted-call", Name: "communicate", Arguments: json.RawMessage(`{"message":"hydrated"}`)}
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_hydrate",
		schema.NewTurn(schema.TurnUserInput, llm.User("run")),
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}},
	)
	read := st.read(t)
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "agentMessage" && item.Text == "hydrated" {
				return
			}
		}
	}
	t.Fatalf("persisted communicate missing from the read: %+v", read.Thread.Turns)
}

// installTranscriptIdentity seeds srv from a real transcript the way production
// serve does: project once, then publish.
func installTranscriptIdentity(t testing.TB, srv *Server, threadID, path string) {
	t.Helper()
	prepared, err := PrepareAppIdentity("local", threadID, path)
	if err != nil {
		t.Fatalf("PrepareAppIdentity(%s): %v", path, err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
}

func turnIDs(turns []appwire.Turn) []string {
	out := make([]string, len(turns))
	for i, tn := range turns {
		out[i] = tn.ID
	}
	return out
}

func TestAppWireItemPagingNormalizesBoundaryCompleteness(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1",
		schema.NewTurn(schema.TurnUserInput, llm.User("old")),
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("last")),
	)
	srv := st.srv

	full, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Thread.Turns) != 1 || len(full.Thread.Turns[0].Items) != 2 {
		t.Fatalf("fully contained item read = %+v, want one turn with two items", full.Thread.Turns)
	} else if full.Thread.Turns[0].HasEarlierItems || full.Thread.Turns[0].HasLaterItems {
		t.Fatalf("fully contained turn flags = (%v,%v), want (false,false)", full.Thread.Turns[0].HasEarlierItems, full.Thread.Turns[0].HasLaterItems)
	}

	partial, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{
		Ref:          "local:th_1",
		IncludeTurns: true,
		ItemLimit:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Thread.Turns) != 1 || len(partial.Thread.Turns[0].Items) != 1 || partial.Thread.Turns[0].Items[0].Text != "last" {
		t.Fatalf("partial latest item read = %+v, want the last item", partial.Thread.Turns)
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
		ItemLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Data) != 1 || len(older.Data[0].Items) != 1 || older.Data[0].Items[0].Text != "first" {
		t.Fatalf("older item page = %+v, want the first item", older.Data)
	}
	if older.Data[0].HasEarlierItems || !older.Data[0].HasLaterItems {
		t.Fatalf("older boundary flags = (%v,%v), want (false,true)", older.Data[0].HasEarlierItems, older.Data[0].HasLaterItems)
	}
	if older.Snapshot == nil || *older.Snapshot != *partial.Snapshot {
		t.Fatalf("older page snapshot %+v, want the latest read's %+v", older.Snapshot, partial.Snapshot)
	}
}

// TestDaemonTranscriptPreparationPropagatesUnsupportedFormat pins where a
// transcript the daemon cannot read is reported: preparation, before anything
// is published, from the transcript's header.
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

// TestServerAppWireNotifierEvictionDoesNotTruncateTheThread pins that the
// notifier's replay buffer is a bounded REPLAY window -- how far a
// reconnecting subscriber can catch up from notifications -- not the authority
// for what the thread contains: a read projects the transcript, however far
// the buffer has wrapped.
func TestServerAppWireNotifierEvictionDoesNotTruncateTheThread(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{AppReplaySize: 2}), "th_1")
	for _, text := range []string{"first", "second", "third"} {
		st.record(t, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
		st.record(t, schema.NewTurn(schema.TurnAssistant, llm.Assistant(text+" reply")))
		st.settle(t)
	}

	// The replay buffer has long since wrapped past the first exchange.
	if replay := st.srv.AppNotificationsAfter(0, "th_1"); len(replay) > 2 {
		t.Fatalf("replay window = %d records, want the bounded 2 that make this test meaningful", len(replay))
	}
	if got, want := readTexts(st.read(t)), []string{"first", "first reply", "second", "second reply", "third", "third reply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read after replay eviction = %q, want every exchange %q", got, want)
	}
}

// TestServerAppWireOldIdentityCannotPublishAfterReplacement pins that a
// replaced identity is finished: its history is neither readable under the old
// thread nor inherited by the new one, and what its writer records afterwards
// is published nowhere.
func TestServerAppWireOldIdentityCannotPublishAfterReplacement(t *testing.T) {
	srv := NewServer(ServerConfig{})
	old := newServedTranscript(t, srv, "old", schema.NewTurn(schema.TurnUserInput, llm.User("old turn")))
	if len(readTexts(old.read(t))) == 0 {
		t.Fatal("old identity read no history, so the fence below would prove nothing")
	}

	fresh := newServedTranscript(t, srv, "new")
	if got := srv.appThreadReadSnapshot(appwire.ThreadReadParams{ThreadID: "old", IncludeTurns: true}); got.Thread.ID != "" || len(got.Thread.Turns) != 0 {
		t.Fatalf("old identity read after replacement = %+v, want nothing", got)
	}
	if texts := readTexts(fresh.read(t)); len(texts) != 0 {
		t.Fatalf("replaced identity inherited old history: %q", texts)
	}
	before := srv.appNotifier.CurrentSequence()
	old.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("late old turn")))
	fresh.record(t, schema.NewTurn(schema.TurnUserInput, llm.User("new turn")))
	fresh.settle(t)
	for _, n := range srv.appNotifier.ReplayAfter(before, "") {
		if n.Notification.Method == appwire.NotifyHistoryUpdated && strings.Contains(string(n.Notification.Params), "late old turn") {
			t.Fatalf("the replaced identity published history: %s", n.Notification.Params)
		}
	}
	if got, want := readTexts(fresh.read(t)), []string{"new turn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new thread read = %q, want %q", got, want)
	}
}

// TestServerAppWireReplacementClosesTheOldStreamOnce pins that the old thread's
// subscribers are told their thread ended -- once, targeted at the OLD ref --
// and that the new thread's subscribers are not.
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
	if same := srv.AppNotificationsAfter(0, "local:new"); len(same) != 0 {
		t.Fatalf("new thread received the old thread's closure: %+v", same)
	}
}

// TestServerAppWireReplacementLeavesNoActiveTurn pins that an installed
// identity starts with no active turn: neither the running execution the old
// session published nor a reserved one survives the replacement.
func TestServerAppWireReplacementLeavesNoActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	reserved, err := srv.reserveAppTurnIDForStart()
	if err != nil {
		t.Fatalf("reserveAppTurnIDForStart: %v", err)
	}
	if srv.appThread().Evener.ActiveTurnID != reserved {
		t.Fatalf("thread.evener.activeTurnId = %q, want the reserved %q", srv.appThread().Evener.ActiveTurnID, reserved)
	}
	srv.SetAppIdentity("local", "th_2")
	if got := srv.appThread().Evener.ActiveTurnID; got != "" {
		t.Fatalf("thread.evener.activeTurnId = %q after replacement, want none", got)
	}

	srv.SetProcessingTurn("t_running")
	if got := srv.appThread().Evener.ActiveTurnID; got != "t_running" {
		t.Fatalf("thread.evener.activeTurnId = %q, want the running t_running", got)
	}
	srv.SetAppIdentity("local", "th_3")
	if got := srv.appThread().Evener.ActiveTurnID; got != "" {
		t.Fatalf("thread.evener.activeTurnId = %q after replacement, want none", got)
	}
	srv.mu.RLock()
	reservedAfter := srv.appReservedTurnID
	srv.mu.RUnlock()
	if reservedAfter != "" {
		t.Fatalf("reserved turn = %q after replacement, want none", reservedAfter)
	}
}

// TestOverlayCommitsInProducerOrderUnderConcurrentEvents pins that an event's
// overlay changes are applied and sequenced inside the SAME projection commit,
// so a later event's change cannot be recorded before an earlier one's.
//
// The order here is established by a real happens-before, not by racing two
// goroutines and hoping: the first event is held INSIDE its commit callback,
// where it owns the projection lock, while the second event is started and
// blocks trying to enter. beforeAppProjectionCommit alone cannot do this -- it
// runs before CommitProjection, so a goroutine parked there holds nothing and
// either goroutine may win the lock.
func TestOverlayCommitsInProducerOrderUnderConcurrentEvents(t *testing.T) {
	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("prompt")))
	srv := st.srv
	srv.RecordAppEvent(threadEvent("th_1", events.RoundStartedData{RoundID: "r_1"}))
	start := srv.appNotifier.CurrentSequence()

	// Park the first event inside its commit, holding the projection lock.
	insideCommit := make(chan struct{})
	release := make(chan struct{})
	var parked sync.Once
	setInsideAppProjectionCommitHook(t, func() {
		parked.Do(func() {
			close(insideCommit)
			<-release
		})
	})

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		srv.RecordAppEvent(threadEvent("th_1", events.AssistantTextDeltaData{Delta: "first"}))
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
		srv.RecordAppEvent(threadEvent("th_1", events.AssistantTextDeltaData{Delta: "second"}))
	}()
	<-secondReached
	close(release)
	<-firstDone
	<-secondDone

	var methods []string
	for _, n := range srv.appNotifier.ReplayAfter(start, "") {
		methods = append(methods, n.Notification.Method)
	}
	if want := []string{appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("committed %v, want the first delta's stream upsert before the second's delta %v", methods, want)
	}
	read := st.read(t)
	if len(read.Overlay) != 1 || read.Overlay[0].Item.Text != "firstsecond" {
		t.Fatalf("read overlay = %+v, want one stream holding both deltas in commit order", read.Overlay)
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
	if got, err := transcriptHeader(large, appTranscriptMaxLineBytes); err != nil || got.SessionID != "th_1" {
		t.Fatalf("header = %+v, %v; want session th_1", got, err)
	}

	file, err := os.Open(large)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck // read-only fixture
	counted := &countingHeaderReader{Reader: file}
	if got, err := transcriptHeaderFromReader(counted, appTranscriptMaxLineBytes); err != nil || got.SessionID != "th_1" {
		t.Fatalf("counted header = %+v, %v; want session th_1", got, err)
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

	st := newServedTranscript(t, NewServer(ServerConfig{}), "th_1", schema.NewTurn(schema.TurnUserInput, llm.User("installed")))
	srv := st.srv
	before := st.read(t)

	if _, err := PrepareAppIdentity("local", "th_1", write("th_other")); err == nil {
		t.Fatal("PrepareAppIdentity accepted another session's transcript")
	}
	if after := st.read(t); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed preparation mutated installed state\n got: %+v\nwant: %+v", after, before)
	}
	if srv.appThread().ID != "th_1" {
		t.Fatalf("failed preparation moved the installed identity to %q", srv.appThread().ID)
	}
}

// TestPreparedAppIdentityServesNoHistoryWithoutATranscript pins the two ways a
// thread legitimately has no history: no path at all, and a path whose file
// does not exist.
func TestPreparedAppIdentityServesNoHistoryWithoutATranscript(t *testing.T) {
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
			t.Cleanup(srv.Close)
			prepared, err := PrepareAppIdentity("local", "th_1", tc.path)
			if err != nil {
				t.Fatalf("PrepareAppIdentity: %v", err)
			}
			srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
			if srv.appHistoryForID("th_1") != nil {
				t.Fatal("a thread with no transcript has a history")
			}
			read, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: "th_1", IncludeTurns: true})
			if err != nil {
				t.Fatal(err)
			}
			if read.Thread.ID != "th_1" || len(read.Thread.Turns) != 0 || read.Snapshot != nil {
				t.Fatalf("read = %+v, want the thread with no history", read)
			}
		})
	}
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
	t.Cleanup(srv.Close)
	installTranscriptIdentity(t, srv, "th_1", path)
	seeded := srv.appThreadReadSnapshot(appwire.ThreadReadParams{ThreadID: "th_1", IncludeTurns: true}).Thread.Turns

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
