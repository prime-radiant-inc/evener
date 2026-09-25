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
	"strings"
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

func TestPersistedSteeringKeepsOpenTurnOwnershipInFullAndIndexedItemProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steering-ownership.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_steering"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	user := schema.NewTurn(schema.TurnUserInput, llm.User("question"))
	user.StableTurnID = "turn_m1"
	steering := schema.NewTurn(schema.TurnSteering, llm.User("clarification"))
	steering.StableTurnID = "turn_m77"
	steering.SteeringSource = "user"
	for _, turn := range []schema.Turn{
		user,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
		steering,
	} {
		if err := tw.Append(turn); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	full, _, err := appTurnsFromTranscriptFile(path)
	if err != nil {
		t.Fatalf("appTurnsFromTranscriptFile: %v", err)
	}
	if len(full) != 1 || full[0].ID != "turn_m1" || len(full[0].Items) != 3 {
		t.Fatalf("full item projection = %+v, want one three-item turn owned by turn_m1", full)
	}

	window, _, err := apptranscript.NewTurnCache().LatestItemWindowFromFile(path, appTranscriptMaxLineBytes, apptranscript.ItemWindowOptions{
		ThreadRef: "local:th_steering",
		Limit:     40,
	}, legacyWindowItemProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	if len(window.Candidates) != 3 {
		t.Fatalf("indexed candidates = %+v, want the full projection's three items", window.Candidates)
	}
	for i, candidate := range window.Candidates {
		wantPosition := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
		if candidate.TurnID != "turn_m1" || candidate.Item.TurnID != "turn_m1" || candidate.Position != wantPosition {
			t.Fatalf("indexed candidate %d = %+v, want turn_m1 at %+v", i, candidate, wantPosition)
		}
		if got, want := candidate.Item.TranscriptKey, full[0].Items[i].TranscriptKey; got != want {
			t.Fatalf("indexed candidate %d key = %q, want full projection key %q", i, got, want)
		}
	}
}

// legacyWindowItemProjector projects one entry for apptranscript's bounded
// item-window reader, which assigns each item its grouped position and key.
func legacyWindowItemProjector(turn schema.Turn, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	if entryIndex <= 0 {
		return nil
	}
	return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages)
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
	srv.ReplaceAppIdentity(prepared, nil)
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
			srv.ReplaceAppIdentity(prepared, nil)
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

func TestAppTurnSnapshotItemRemovalPreservesSparseIdentityAndBoundaries(t *testing.T) {
	newSnapshot := func() *appTurnSnapshot {
		items := make([]appwire.ThreadItem, 3)
		for i := range items {
			position := appwire.ThreadItemPosition{Entry: 4, Item: uint32(i)}
			items[i] = appwire.ThreadItem{
				Type: "agentMessage", ID: fmt.Sprintf("item_%d", i), TurnID: "turn_4",
				TranscriptKey: fmt.Sprintf("key_%d", i), Position: &position,
			}
		}
		snapshot := &appTurnSnapshot{}
		snapshot.Seed(appTurnSeed{Turns: []appwire.Turn{{ID: "turn_4", Items: items}}, ThreadRef: "local:th_4", TranscriptIncarnation: "inc-4", NextEntry: 5})
		return snapshot
	}
	reset := func(t *testing.T, snapshot *appTurnSnapshot, itemID string) {
		t.Helper()
		params, err := json.Marshal(appwire.AgentMessageResetParams{TurnID: "turn_4", ItemID: itemID})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Apply([]appserver.SequencedNotification{{Seq: 1, Notification: appwire.Notification{Method: appwire.NotifyAgentMessageReset, Params: params}}})
	}

	t.Run("first removal", func(t *testing.T) {
		snapshot := newSnapshot()
		before, _, err := snapshot.LatestItemCandidates(2)
		if err != nil || before.OlderCursor == "" {
			t.Fatalf("initial item page = %+v, err=%v; want cursor", before, err)
		}
		oldCursor := before.OlderCursor
		reset(t, snapshot, "item_0")
		window, _, err := snapshot.LatestItemCandidates(40)
		if err != nil || len(window.Candidates) != 2 {
			t.Fatalf("after first removal = %+v, err=%v; want two survivors", window.Candidates, err)
		}
		survivor := window.Candidates[0]
		if survivor.Item.ID != "item_1" || survivor.Item.Position == nil || *survivor.Item.Position != (appwire.ThreadItemPosition{Entry: 4, Item: 1}) || survivor.Item.TranscriptKey != "key_1" {
			t.Fatalf("first survivor identity = %+v, want original position/key", survivor.Item)
		}
		if survivor.HasEarlierItems || !survivor.HasLaterItems {
			t.Fatalf("first survivor boundaries = (%v,%v), want (false,true)", survivor.HasEarlierItems, survivor.HasLaterItems)
		}
		if _, _, err := snapshot.PreviousItemCandidates(oldCursor, 2); !isStaleItemCursorError(err) {
			t.Fatalf("cursor from before first removal = %v, want stale", err)
		}
	})

	t.Run("middle removal", func(t *testing.T) {
		snapshot := newSnapshot()
		reset(t, snapshot, "item_1")
		window, _, err := snapshot.LatestItemCandidates(40)
		if err != nil || len(window.Candidates) != 2 {
			t.Fatalf("after middle removal = %+v, err=%v; want two survivors", window.Candidates, err)
		}
		first, last := window.Candidates[0], window.Candidates[1]
		if first.Item.ID != "item_0" || first.HasEarlierItems || !first.HasLaterItems {
			t.Fatalf("first boundary after middle removal = (%v,%v), want (false,true)", first.HasEarlierItems, first.HasLaterItems)
		}
		if last.Item.ID != "item_2" || !last.HasEarlierItems || last.HasLaterItems {
			t.Fatalf("last boundary after middle removal = (%v,%v), want (true,false)", last.HasEarlierItems, last.HasLaterItems)
		}
	})
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
