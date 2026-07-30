package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/diagnostic"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func requireTranscriptFileTurns(t testing.TB, path string) []appwire.Turn {
	t.Helper()
	turns, _, err := appTurnsFromTranscriptFile(path)
	if err != nil {
		t.Fatalf("appTurnsFromTranscriptFile: %v", err)
	}
	return turns
}

func TestServerAppWireTurnStartQueuesInput(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	select {
	case msg := <-srv.InputCh():
		if msg.Text != "hello" {
			t.Fatalf("text=%q", msg.Text)
		}
	default:
		t.Fatal("input was not queued")
	}
}

// TestServerAppWireProcessingWithoutReservedTurnIDReadsActiveWithNoTurnID
// (kata c2ty) proves the wire-level mechanism behind the reported bug:
// SetProcessing(true) and reserveAppTurnIDForStart's ActiveTurnID stamp are
// two SEPARATE writes to two separate fields (server/appwire_runtime.go's
// reserveAppTurnIDForStart sets appActiveTurnID; server/server.go's
// SetProcessing sets processing, which alone drives Status.Type via
// appStatus - see appThread's own status/activeTurnID assembly, both read
// from ONE RLock snapshot so a single thread/read call is internally
// self-consistent with WHATEVER the two fields currently hold, but nothing
// enforces that the two fields are written together).
//
// cmd/serf/serve.go's input-processing loop has exactly one caller that
// writes the first without the second: handleAppTurnStart (a fresh
// turn/start RPC) calls reserveAppTurnIDForStart before ever queuing the
// input, so ActiveTurnID is already stamped before SetProcessing(true) can
// run. But nextTurnCtx - the queued-input auto-continuation path that fires
// when a turn finishes with input already queued behind it - calls
// SetProcessing(true) directly with no turnID reservation at all; the next
// turn's ID is only learned later, asynchronously, once its SessionStart
// event reaches RecordAppEvent (appwire_runtime.go) via the buffered event
// channel + bridge goroutine. A thread/read landing in that window - which
// is exactly what navigating straight to a session URL does - reads
// Status.Type "active" with Serf.ActiveTurnID empty: the composer's own
// isTurnActive gate (panes/session/composer/submitRouting.ts) requires
// both, so it renders the idle Send-only controls Composer.test.tsx's own
// "the timing caption is absent while status reads active but no turn has
// actually started yet" test already documents as the INTENTIONAL
// behavior for a turn that hasn't truly started - except here a real turn
// HAS started, just not yet reflected. This is not the vybn/SessionChrome
// shape (no null-ref, no ResizeObserver, no first-pass-null render): it is
// a two-signal write-ordering gap between two independently-mutated server
// fields, reachable without RecordAppEvent ever running (proven below by
// calling SetProcessing(true) alone, mirroring nextTurnCtx exactly).
func TestServerAppWireProcessingWithoutReservedTurnIDReadsActiveWithNoTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	// Mirrors nextTurnCtx (cmd/serf/serve.go): the queued-continuation path
	// flips processing straight away, with no prior reserveAppTurnIDForStart
	// call - RecordAppEvent (which would populate appActiveTurnID from the
	// next turn's SessionStart event) has not run yet.
	srv.SetProcessing(true)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if data.Thread.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("status=%q, want active", data.Thread.Status.Type)
	}
	// The fix (kata c2ty): going processing now reserves an id in the same
	// lock hold, so the two fields can no longer disagree. Asserting a
	// non-empty id rather than a specific one - the value is the projector's
	// to choose, and pinning it here would just restate ReserveTurnID.
	if data.Thread.Serf.ActiveTurnID == "" {
		t.Fatal("activeTurnId is empty while status reads active: the composer's isTurnActive gate needs both, and offers idle controls for a working session when they disagree")
	}
}

// A reservation already made by turn/start must be kept, not replaced. If
// SetProcessing minted unconditionally it would burn a second id for one turn
// and hand the client an id the turn never uses.
func TestServerAppWireProcessingKeepsAnAlreadyReservedTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	reserved, err := srv.reserveAppTurnIDForStart()
	if err != nil {
		t.Fatalf("reserveAppTurnIDForStart: %v", err)
	}
	srv.SetProcessing(true)

	srv.mu.RLock()
	got := srv.appActiveTurnID
	srv.mu.RUnlock()
	if got != reserved {
		t.Fatalf("appActiveTurnID = %q after SetProcessing, want the reserved %q", got, reserved)
	}
}

// TestServerAppWireThreadReadExposesReservedActiveTurnIDAlongsideSeededTurns
// pins that thread.serf.activeTurnId and the snapshot's turns answer different
// questions. turn/start RESERVES a turn id before any turn/started exists, so
// the id it reports is deliberately absent from turns -- which is why nothing
// downstream may treat it as "the turn to append items to".
func TestServerAppWireThreadReadExposesReservedActiveTurnIDAlongsideSeededTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	tw, err := transcript.NewWriter(path, transcript.Header{
		SessionID: "th_1",
		CreatedAt: time.Now(),
		ProfileID: "openai",
		Model:     "gpt-5.5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("old input"))); err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("old output"))); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, "th_1", path)
	// Production restores before it bridges: SessionStart carries the persisted
	// entry count so live ids start above the seeded ones.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "th_1", Data: events.SessionStartData{Restored: true, TranscriptEntries: 2}})
	srv.SetSteerFunc(func(string) {})
	srv.SetCancelFunc(func() {})
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "new input"}},
	}))
	startResp, ok := start.Response.Result.(appwire.TurnStartResponse)
	if !ok {
		t.Fatalf("start response=%T", start.Response.Result)
	}

	read := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodThreadRead, appwire.ThreadReadParams{
		Ref:          "local:th_1",
		IncludeTurns: true,
	}))
	if read.Kind() != appwire.MessageResponse {
		t.Fatalf("read=%+v", read)
	}
	out, ok := read.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("read response=%T", read.Response.Result)
	}
	if len(out.Thread.Turns) < 2 {
		t.Fatalf("thread turns=%d, want the seeded transcript turns", len(out.Thread.Turns))
	}
	for _, turn := range out.Thread.Turns {
		if turn.Status == appwire.TurnStatusInProgress {
			t.Fatalf("seeded transcript turn unexpectedly in progress: %+v", turn)
		}
		if turn.ID == startResp.Turn.ID {
			t.Fatalf("reserved turn %q appears in turns; nothing started it yet", turn.ID)
		}
	}
	if out.Thread.Serf.ActiveTurnID != startResp.Turn.ID {
		t.Fatalf("active turn id=%q, want %q", out.Thread.Serf.ActiveTurnID, startResp.Turn.ID)
	}
}

func TestServerAppWireGoalSetInvokesGoalFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var got []string
	srv.SetGoalFunc(func(objective string) (bool, error) {
		got = append(got, objective)
		return true, nil
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodGoalSet, appwire.GoalSetParams{
		Ref:       "local:th_1",
		Objective: "improve coverage",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	out, ok := resp.Response.Result.(appwire.GoalSetResponse)
	if !ok {
		t.Fatalf("response result=%T", resp.Response.Result)
	}
	if !out.Started {
		t.Fatalf("started=%v, want true", out.Started)
	}
	if len(got) != 1 || got[0] != "improve coverage" {
		t.Fatalf("goalFunc objectives=%v", got)
	}
}

func TestServerAppWireGoalSetEmptyObjectiveRoutesThroughGoalFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var got []string
	srv.SetGoalFunc(func(objective string) (bool, error) {
		got = append(got, objective)
		return false, nil
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodGoalSet, appwire.GoalSetParams{
		Ref:       "local:th_1",
		Objective: "",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	out := resp.Response.Result.(appwire.GoalSetResponse)
	if out.Started {
		t.Fatalf("started=%v, want false for clear", out.Started)
	}
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("goalFunc objectives=%v, want one empty-objective clear", got)
	}
}

func TestServerAppWireGoalSetWithoutGoalFuncIsUnavailable(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodGoalSet, appwire.GoalSetParams{
		Ref:       "local:th_1",
		Objective: "x",
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v, want error when goalFunc unwired", resp.Kind())
	}
}

func TestServerAppWireTurnStartAcceptsCodexInput(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		ThreadID: "th_1",
		Input: []appwire.InputItem{
			{Type: "text", Text: "hello"},
			{Type: "image", MediaType: "image/png", Data: []byte("png"), Name: "shot.png"},
		},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	select {
	case msg := <-srv.InputCh():
		if msg.Text != "hello" {
			t.Fatalf("text=%q", msg.Text)
		}
		if len(msg.Images) != 1 || msg.Images[0].MediaType != "image/png" || string(msg.Images[0].Data) != "png" || msg.Images[0].Name != "shot.png" {
			t.Fatalf("images=%+v", msg.Images)
		}
	default:
		t.Fatal("input was not queued")
	}
}

func TestServerAppWireTurnStartIDMatchesProjectedNotifications(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "earlier"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      events.AssistantTextEndData{Text: "done"},
	})
	installProjectedMutationCallbacksForTest(srv)
	history := srv.AppNotificationsAfter(0, "th_1")
	cursor := history[len(history)-1].Seq

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	startResp, ok := resp.Response.Result.(appwire.TurnStartResponse)
	if !ok {
		t.Fatalf("response result=%T", resp.Response.Result)
	}

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello"},
	})

	notifications := srv.AppNotificationsAfter(cursor, "th_1")
	var startedID string
	var itemTurnID string
	for _, item := range notifications {
		switch item.Notification.Method {
		case appwire.NotifyTurnStarted:
			var params struct {
				Turn appwire.Turn `json:"turn"`
			}
			if err := json.Unmarshal(item.Notification.Params, &params); err != nil {
				t.Fatalf("turn started params: %v", err)
			}
			startedID = params.Turn.ID
		case appwire.NotifyItemCompleted:
			var params struct {
				Item appwire.ThreadItem `json:"item"`
			}
			if err := json.Unmarshal(item.Notification.Params, &params); err != nil {
				t.Fatalf("item completed params: %v", err)
			}
			if params.Item.Type == "userMessage" && params.Item.Text == "hello" {
				itemTurnID = params.Item.TurnID
			}
		}
	}
	if startedID != startResp.Turn.ID {
		t.Fatalf("turn/start id=%q, turn/started id=%q", startResp.Turn.ID, startedID)
	}
	if itemTurnID != startResp.Turn.ID {
		t.Fatalf("turn/start id=%q, user item turn id=%q", startResp.Turn.ID, itemTurnID)
	}
}

func TestServerAppWireThreadReadIncludesProjectedTurns(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      events.AssistantTextEndData{Text: "hi there"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "th_1",
		Data:      events.SessionEndData{Reason: "input_complete", State: "idle"},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	turn := data.Thread.Turns[0]
	if turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" {
		t.Fatalf("turn=%+v", turn)
	}
	if len(turn.Items) != 2 {
		t.Fatalf("items=%+v", turn.Items)
	}
	if turn.Items[0].Type != "userMessage" || turn.Items[0].Text != "hello" {
		t.Fatalf("user item=%+v", turn.Items[0])
	}
	if turn.Items[1].Type != "agentMessage" || turn.Items[1].Text != "hi there" {
		t.Fatalf("agent item=%+v", turn.Items[1])
	}
}

func TestServerAppWireThreadReadIncludesInProgressDeltas(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "run"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "partial "},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "answer"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallStart,
		SessionID: "th_1",
		Data:      events.ToolCallStartData{ToolName: "shell", CallID: "call_1", ArgumentsJSON: `{"cmd":"go test"}`},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "ok "},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "done"},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	var agentItem, toolItem *appwire.ThreadItem
	for i := range data.Thread.Turns[0].Items {
		item := &data.Thread.Turns[0].Items[i]
		switch item.Type {
		case "agentMessage":
			agentItem = item
		case "commandExecution":
			toolItem = item
		}
	}
	if agentItem == nil || agentItem.Text != "partial answer" || agentItem.Status != appwire.TurnStatusInProgress {
		t.Fatalf("agent item=%+v", agentItem)
	}
	if toolItem == nil || toolItem.Output != "ok done" || toolItem.Status != appwire.TurnStatusInProgress {
		t.Fatalf("tool item=%+v", toolItem)
	}
}

func TestServerAppWireThreadReadMergesCompletionItemsWithDeltas(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "run"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      events.AssistantTextStartData{},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "partial "},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "answer"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      events.AssistantTextEndData{},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallStart,
		SessionID: "th_1",
		Data:      events.ToolCallStartData{ToolName: "shell", CallID: "call_1", ArgumentsJSON: `{"cmd":"go test"}`},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "ok "},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "done"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: "th_1",
		Data:      events.ToolCallEndData{ToolName: "shell", CallID: "call_1"},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	var agentItem, toolItem *appwire.ThreadItem
	for i := range data.Thread.Turns[0].Items {
		item := &data.Thread.Turns[0].Items[i]
		switch item.Type {
		case "agentMessage":
			agentItem = item
		case "commandExecution":
			toolItem = item
		}
	}
	if agentItem == nil || agentItem.Text != "partial answer" || agentItem.Status != appwire.TurnStatusCompleted {
		t.Fatalf("agent item=%+v", agentItem)
	}
	if toolItem == nil || toolItem.Output != "ok done" || toolItem.Status != appwire.TurnStatusCompleted {
		t.Fatalf("tool item=%+v", toolItem)
	}
}

func TestServerAppWireThreadReadUsesCommunicateAsAssistantMessage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	for _, ev := range []events.SessionEvent{
		{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}},
		{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
			ToolName:      "communicate",
			CallID:        "call_1",
			ArgumentsJSON: `{"message":"done","end_turn":true}`,
		}},
		{Kind: events.EventCommunicate, SessionID: "th_1", Data: events.CommunicateData{Message: "done"}},
		{Kind: events.EventToolCallOutputDelta, SessionID: "th_1", Data: events.ToolCallOutputDeltaData{
			ToolName: "communicate",
			CallID:   "call_1",
			Delta:    `{"accepted":true}`,
		}},
		{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
			ToolName: "communicate",
			CallID:   "call_1",
			Output:   `{"accepted":true}`,
		}},
		{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}},
	} {
		srv.RecordAppEvent(ev)
	}

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	var agentMessages, communicateTools int
	for _, item := range data.Thread.Turns[0].Items {
		if item.Type == "agentMessage" && item.Text == "done" {
			agentMessages++
		}
		if item.Type == "commandExecution" && item.ToolName == "communicate" {
			communicateTools++
		}
	}
	if agentMessages != 1 || communicateTools != 0 {
		t.Fatalf("items=%+v, want one agent message and no communicate tool", data.Thread.Turns[0].Items)
	}
}

func TestServerAppWireInitializeAdvertisesTurnList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	conn := srv.AppServer().NewConnection("test")
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.InitializeResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	// thread/turns/list is implemented (lazy transcript loading).
	if !data.Features.ThreadTurnsList {
		t.Fatalf("ThreadTurnsList not advertised despite handlers: %+v", data.Features)
	}
}

func TestServerAppWireTurnSteerRejectsMismatchedTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var steered []string
	srv.SetSteerFunc(func(text string) {
		steered = append(steered, text)
	})
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	bad := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation",
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID + "-stale",
		Input:          []appwire.InputItem{{Type: "text", Text: "wrong turn"}},
	}))
	if bad.Kind() != appwire.MessageError {
		t.Fatalf("bad steer response=%v", bad.Kind())
	}
	if len(steered) != 0 {
		t.Fatalf("stale steer invoked handler: %v", steered)
	}

	good := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation",
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID,
		Input:          []appwire.InputItem{{Type: "text", Text: "right turn"}},
	}))
	if good.Kind() != appwire.MessageResponse {
		t.Fatalf("good steer response=%v error=%+v", good.Kind(), good.Error)
	}
	if len(steered) != 1 || steered[0] != "right turn" {
		t.Fatalf("steered=%v", steered)
	}
}

func TestServerAppWireTurnSteerPreservesImages(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var gotText string
	var gotImages []ImageAttachment
	srv.SetSteerWithImagesFunc(func(text string, images []ImageAttachment) {
		gotText = text
		gotImages = append([]ImageAttachment(nil), images...)
	})
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation",
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID,
		Input: []appwire.InputItem{
			{Type: "text", Text: "look at this"},
			{Type: "image", MediaType: "image/png", Name: "shot.png", Data: []byte("png")},
		},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("steer response=%v error=%+v", resp.Kind(), resp.Error)
	}
	if gotText != "look at this" {
		t.Fatalf("text=%q, want look at this", gotText)
	}
	if len(gotImages) != 1 || gotImages[0].MediaType != "image/png" || gotImages[0].Name != "shot.png" || !bytes.Equal(gotImages[0].Data, []byte("png")) {
		t.Fatalf("images=%+v", gotImages)
	}
}

func TestServerAppWireTurnSteerRejectsImagesWithoutImageHook(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var steered []string
	srv.SetSteerFunc(func(text string) {
		steered = append(steered, text)
	})
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation",
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID,
		Input:          []appwire.InputItem{{Type: "image", MediaType: "image/png", Name: "shot.png", Data: []byte("png")}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("steer response=%v, want error", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
	if len(steered) != 0 {
		t.Fatalf("text steer invoked despite image input: %v", steered)
	}
}

func TestServerAppWireTurnSteerRequiresTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetSteerFunc(func(string) {})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "missing turn"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("steer without turn id response=%v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

func TestServerAppWireTurnInterruptRequiresActiveTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	cancelled := 0
	srv.SetCancelFunc(func() { cancelled++ })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	missing := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{ClientMutationID: "test-mutation",
		Ref: "local:th_1",
	}))
	if missing.Kind() != appwire.MessageError || missing.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("interrupt without turn id=%+v", missing)
	}
	stale := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{ClientMutationID: "test-mutation",
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID + "-stale",
	}))
	if stale.Kind() != appwire.MessageError || stale.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("stale interrupt=%+v", stale)
	}
	good := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(5), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{ClientMutationID: "test-mutation",
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID,
	}))
	if good.Kind() != appwire.MessageResponse {
		t.Fatalf("good interrupt=%+v", good)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled=%d, want 1", cancelled)
	}
}

func TestServerAppWireThreadModelSetQualifiesProvider(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var got string
	srv.SetModelFunc(func(model string) error {
		got = model
		return nil
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{
		Ref:           "local:th_1",
		ModelProvider: "openai",
		Model:         "gpt-5",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if got != "openai/gpt-5" {
		t.Fatalf("model=%q, want openai/gpt-5", got)
	}
}

func TestAppStatusPreservesAttentionStates(t *testing.T) {
	tests := map[string]string{
		appwire.ThreadStatusAwaiting:    appwire.ThreadStatusAwaiting,
		appwire.ThreadStatusWarning:     appwire.ThreadStatusWarning,
		appwire.ThreadStatusSystemError: appwire.ThreadStatusSystemError,
	}
	for state, want := range tests {
		if got := appStatus(state, false); got != want {
			t.Fatalf("appStatus(%q)=%q, want %q", state, got, want)
		}
	}
}

func TestServerAppWireTurnStartRejectsClosedSession(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetState("closed")
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v", resp.Kind())
	}
	if resp.Error == nil || resp.Error.Error.Message != "session is closed" {
		t.Fatalf("error=%+v", resp.Error)
	}
	select {
	case msg := <-srv.InputCh():
		t.Fatalf("input should not be queued: %+v", msg)
	default:
	}
}

func TestServerAppWireErrorEventNotifiesSubscribers(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(context.Background())

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello"},
	})
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data:      events.ErrorData{Error: "provider unavailable"},
	})

	// A genuine provider failure is surfaced exactly once — as a failed turn
	// carrying the diagnostic. It must NOT also emit a redundant NotifyWarning
	// (that made the same error render twice in clients showing both channels).
	var sawFailedTurn bool
	deadline := time.After(time.Second)
	for !sawFailedTurn {
		select {
		case got := <-client.Notifications():
			switch got.Method {
			case appwire.NotifyWarning:
				t.Fatalf("non-cancelled provider error emitted a redundant NotifyWarning: %s", got.Params)
			case appwire.NotifyTurnCompleted:
				var params struct {
					Turn appwire.Turn `json:"turn"`
				}
				if err := json.Unmarshal(got.Params, &params); err != nil {
					t.Fatalf("turn params: %v", err)
				}
				if params.Turn.Status == appwire.TurnStatusFailed && params.Turn.Error != nil && params.Turn.Error.Message == "provider unavailable" && params.Turn.Error.Source == string(diagnostic.SourceProvider) {
					sawFailedTurn = true
				}
			}
		case <-deadline:
			t.Fatalf("missing failed-turn notification")
		}
	}

	// Drain for a short window after seeing the failed turn to catch any
	// NotifyWarning that arrives after the completed notification. Without this
	// drain a late warning is left unread in the channel and the test passes
	// even when the invariant is broken.
	drainDeadline := time.After(100 * time.Millisecond)
	for {
		select {
		case got := <-client.Notifications():
			if got.Method == appwire.NotifyWarning {
				t.Fatalf("non-cancelled provider error emitted a redundant NotifyWarning after the failed turn: %s", got.Params)
			}
		case <-drainDeadline:
			return
		}
	}
}

func TestServerAppWireThreadReadReturnsStatus(t *testing.T) {
	exitCode := 7
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetStatus(StatusInfo{
		SessionID:  "sess_1",
		State:      "idle",
		Model:      "gpt-5",
		Profile:    "openai",
		WorkingDir: "/tmp/project",
	})
	srv.SetContextPressureFunc(func() float64 { return 0.42 })
	srv.SetContextMetricsFunc(func() ContextMetrics {
		return ContextMetrics{Used: 42000, Window: 100000, Remaining: 58000}
	})
	srv.SetDetailedStatusFunc(func() DetailedStatus {
		return DetailedStatus{
			Tools: []ToolInfo{{Name: "shell", Source: "core"}},
			MCP:   []MCPServerInfo{{Name: "linear", Tools: []string{"search"}}},
			Skills: []SkillInfo{
				{Name: "superpowers:systematic-debugging", Description: "debug"},
			},
			Plugins: []PluginStatusInfo{{Name: "superpowers", Version: "4.3.0", SkillCount: 12, AgentCount: 2, HookCount: 4}},
			Hooks:   map[string]int{"PreToolUse": 3},
			Jobs: []JobStatusInfo{{
				JobID:         "job-1",
				JobType:       "delegate",
				Status:        "failed",
				Reason:        "exit_nonzero",
				ExitCode:      &exitCode,
				OutputBytes:   128,
				TranscriptRef: "local:child-1",
			}},
			Agents: []string{"explorer"},
		}
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if data.Thread.ID != "th_1" || data.Thread.SessionID != "sess_1" || data.Thread.Serf.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", data.Thread)
	}
	if data.Thread.ModelProvider != "gpt-5" || data.Thread.Serf.Profile != "openai" {
		t.Fatalf("thread model/profile=%+v", data.Thread)
	}
	if data.Thread.Serf.ContextPressure != 0.42 {
		t.Fatalf("context pressure=%v", data.Thread.Serf.ContextPressure)
	}
	if data.Thread.Serf.ContextUsed != 42000 || data.Thread.Serf.ContextWindow != 100000 || data.Thread.Serf.ContextRemaining != 58000 {
		t.Fatalf("context metrics=%+v", data.Thread.Serf)
	}
	diag := data.Thread.Serf.Diagnostics
	if diag == nil || len(diag.Tools) != 1 || len(diag.MCP) != 1 || len(diag.Skills) != 1 || len(diag.Plugins) != 1 || len(diag.Jobs) != 1 || len(diag.Agents) != 1 {
		t.Fatalf("diagnostics=%+v", diag)
	}
	if diag.Jobs[0].JobID != "job-1" || diag.Jobs[0].JobType != "delegate" || diag.Jobs[0].Status != "failed" ||
		diag.Jobs[0].Reason != "exit_nonzero" || diag.Jobs[0].ExitCode == nil || *diag.Jobs[0].ExitCode != exitCode ||
		diag.Jobs[0].OutputBytes != 128 || diag.Jobs[0].TranscriptRef != "local:child-1" {
		t.Fatalf("job diagnostics=%+v", diag.Jobs)
	}
	if diag.Hooks["PreToolUse"] != 3 {
		t.Fatalf("hooks=%+v", diag.Hooks)
	}
}

// TestServerAppWireThreadReadIncludesWorkMetrics (WS2 A7) verifies appThread
// populates SerfThread.Usage/WorkMillis/ActiveTurnStartedAt from the
// workMetricsFn pull callback, alongside the existing pressure/detailed-status
// callbacks exercised by TestServerAppWireThreadReadReturnsStatus.
func TestServerAppWireThreadReadIncludesWorkMetrics(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetWorkMetricsFunc(func() (int64, *appwire.SerfUsage, int64) {
		return 4200, &appwire.SerfUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalTokens: 30}, 1234567890
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	serf := data.Thread.Serf
	if serf.WorkMillis != 4200 {
		t.Fatalf("workMillis=%d, want 4200", serf.WorkMillis)
	}
	if serf.ActiveTurnStartedAt != 1234567890 {
		t.Fatalf("activeTurnStartedAt=%d, want 1234567890", serf.ActiveTurnStartedAt)
	}
	wantUsage := appwire.SerfUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalTokens: 30}
	if serf.Usage == nil || *serf.Usage != wantUsage {
		t.Fatalf("usage=%+v, want %+v", serf.Usage, wantUsage)
	}
}

func TestServerAppWireThreadReadTaskAggregatePresence(t *testing.T) {
	readTasks := func(aggregate *appwire.TaskAggregate) *appwire.TaskAggregate {
		t.Helper()
		srv := NewServer(ServerConfig{})
		srv.SetAppIdentity("local", "th_1")
		if aggregate != nil {
			srv.SetTaskAggregateFunc(func() *appwire.TaskAggregate { return aggregate })
		}

		conn := srv.AppServer().NewConnection("test")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
		data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
		if !ok {
			t.Fatalf("result=%T", resp.Response.Result)
		}
		return data.Thread.Serf.Tasks
	}

	if got := readTasks(nil); got != nil {
		t.Fatalf("unwired task aggregate=%+v, want nil", got)
	}
	want := &appwire.TaskAggregate{Total: 4, Done: 2}
	if got := readTasks(want); got == nil || *got != *want {
		t.Fatalf("task aggregate=%+v, want %+v", got, want)
	}
	zero := &appwire.TaskAggregate{}
	if got := readTasks(zero); got == nil || *got != *zero {
		t.Fatalf("zero task aggregate=%+v, want a present zero", got)
	}
}

// TestServerAppWireThreadReadIncludesCostTotal verifies the live producer
// stamps SerfThread.Cost from the pulled cumulative usage at the session
// model's price — the session-level dollar total kept current across
// snapshots exactly as WorkMillis/Usage are — and omits it when the model is
// uncataloged (absent-vs-zero honesty).
func TestServerAppWireThreadReadIncludesCostTotal(t *testing.T) {
	readSerf := func(model string) appwire.SerfThread {
		t.Helper()
		srv := NewServer(ServerConfig{})
		srv.SetAppIdentity("local", "th_1")
		srv.SetStatus(StatusInfo{SessionID: "th_1", Model: model})
		srv.SetWorkMetricsFunc(func() (int64, *appwire.SerfUsage, int64) {
			return 4200, &appwire.SerfUsage{InputTokens: 100_000, OutputTokens: 20_000, TotalTokens: 120_000}, 0
		})
		conn := srv.AppServer().NewConnection("test")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
		data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
		if !ok {
			t.Fatalf("result=%T", resp.Response.Result)
		}
		return data.Thread.Serf
	}

	priced := readSerf("claude-opus-4-5")
	if want := appwire.EstimateCost("claude-opus-4-5", priced.Usage); priced.Cost != want || want == "" {
		t.Fatalf("cost=%q, want non-empty %q", priced.Cost, want)
	}
	if !strings.HasPrefix(priced.Cost, "~$") {
		t.Fatalf("cost=%q, want ~$ prefix", priced.Cost)
	}

	if uncataloged := readSerf("totally-unknown-model-xyz"); uncataloged.Cost != "" {
		t.Fatalf("uncataloged cost=%q, want \"\" (absent, not ~$0.00)", uncataloged.Cost)
	}
}

// TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired (WS2 A7) verifies
// that a daemon which never wired SetWorkMetricsFunc (e.g. mid-upgrade, or a
// non-serf thread source) projects zero/nil metrics rather than panicking on
// a nil callback.
func TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	serf := data.Thread.Serf
	if serf.Usage != nil || serf.WorkMillis != 0 || serf.ActiveTurnStartedAt != 0 {
		t.Fatalf("serf=%+v, want zero work metrics when unwired", serf)
	}
}

func TestAppTurnsFromTranscriptFilePreservesToolCallArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_read",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"file_path":"/tmp/example.txt"}`),
			},
		}},
	})); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_read", "read_file", "line 1\nline 2\n", false))); err != nil {
		t.Fatalf("append tool result: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	turns := requireTranscriptFileTurns(t, path)
	if len(turns) != 2 || len(turns[0].Items) != 1 || len(turns[1].Items) != 1 {
		t.Fatalf("turns=%+v", turns)
	}
	start := turns[0].Items[0]
	done := turns[1].Items[0]
	if start.CallID != "call_read" || start.ArgumentsJSON == "" || !strings.Contains(start.ArgumentsJSON, "/tmp/example.txt") {
		t.Fatalf("start item=%+v", start)
	}
	if done.CallID != "call_read" || done.Output != "line 1\nline 2\n" {
		t.Fatalf("done item=%+v", done)
	}
}

func TestAppTurnsFromTranscriptFileIncludesPrelude(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    "th_1",
		SystemPrompt: "You are Serf.",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("hello"))); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	turns := requireTranscriptFileTurns(t, path)
	if len(turns) != 2 {
		t.Fatalf("turns=%+v", turns)
	}
	prelude := turns[0]
	if prelude.ID != "turn_system" || len(prelude.Items) != 1 {
		t.Fatalf("prelude=%+v", prelude)
	}
	if got := prelude.Items[0]; got.Type != "systemMessage" || got.Description != "System prompt" || got.Text != "You are Serf." {
		t.Fatalf("system item=%+v", got)
	}
	if got := turns[1].Items[0]; got.Type != "userMessage" || got.Text != "hello" {
		t.Fatalf("first user item=%+v", got)
	}
}

func TestAppTurnsFromTranscriptFileIncludesCompactionTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nfirst compacted state"))); err != nil {
		t.Fatalf("append checkpoint: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nsecond compacted state"))); err != nil {
		t.Fatalf("append summary: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	turns := requireTranscriptFileTurns(t, path)
	if len(turns) != 2 {
		t.Fatalf("turns=%+v", turns)
	}
	if got := turns[0].Items[0]; got.Type != "systemMessage" || got.Description != "Context checkpoint" || !strings.Contains(got.Text, "first compacted state") {
		t.Fatalf("checkpoint item=%+v", got)
	}
	if got := turns[1].Items[0]; got.Type != "systemMessage" || got.Description != "Context summary" || !strings.Contains(got.Text, "second compacted state") {
		t.Fatalf("summary item=%+v", got)
	}
}

// TestServerAppWireThreadReadKeepsSeededHistoryAheadOfLiveTurns pins that the
// seed and the live stream compose into one ordered thread: seeded transcript
// turns stay at the head, live turns append after them, and a replay buffer far
// too small to hold the whole session changes neither.
func TestServerAppWireThreadReadKeepsSeededHistoryAheadOfLiveTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("first"))); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("second"))); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	srv := NewServer(ServerConfig{AppReplaySize: 2})
	installTranscriptIdentity(t, srv, "th_1", path)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "th_1", Data: events.SessionStartData{Restored: true, TranscriptEntries: 2}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "tail"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "only tail"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{State: appwire.ThreadStatusIdle}})

	resp, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{IncludeTurns: true})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 3 {
		t.Fatalf("turns=%v, want the 2 seeded turns plus the live one", turnIDs(resp.Thread.Turns))
	}
	if got := resp.Thread.Turns[0].Items[0].Text; got != "first" {
		t.Fatalf("first turn text=%q, want the seeded head", got)
	}
	if got := resp.Thread.Turns[1].Items[0].Text; got != "second" {
		t.Fatalf("second turn text=%q, want the seeded tail", got)
	}
	live := resp.Thread.Turns[2]
	if len(live.Items) == 0 || live.Items[0].Text != "tail" {
		t.Fatalf("live turn items=%+v, want the live user input", live.Items)
	}
}

func TestServerAppWireThreadShutdownInvokesCallback(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	done := make(chan struct{}, 1)
	srv.SetShutdownFunc(func() { done <- struct{}{} })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked")
	}
}

func TestServerAppWireThreadReadSubscribesForNotifications(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	defer transport.Close() //nolint:errcheck // test cleanup
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}); err != nil {
		t.Fatalf("thread read: %v", err)
	}
	if got := srv.AppServer().SubscriberCount("th_1"); got != 1 {
		t.Fatalf("subscriber count=%d, want 1", got)
	}
}

func TestServerAppWireThreadReadDoesNotSubscribeByDefault(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	if got := srv.AppServer().SubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscriber count=%d, want 0", got)
	}
}

// TestServerAppWireQueueCapabilityFlipsWithProcessing verifies that the
// appwire Capabilities.queue bit is gated on the session being mid-turn
// (kata 111a).
func TestServerAppWireQueueCapabilityFlipsWithProcessing(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetQueueFunc(func(string) error { return nil })

	// Idle: capabilities.queue must be false even with QueueFunc set.
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	caps := srv.appCapabilities("idle", false)
	if caps.Queue {
		t.Fatalf("Queue should be false when idle")
	}

	// Processing: capabilities.queue flips to true.
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	caps = srv.appCapabilities("active", true)
	if !caps.Queue {
		t.Fatalf("Queue should be true mid-turn")
	}

	// Reserved active turn: turn/start has returned an active turn ID, but
	// the input loop has not necessarily flipped processing yet.
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"
	caps = srv.appCapabilities("idle", false)
	if !caps.Queue {
		t.Fatalf("Queue should be true while an active turn is reserved")
	}
	if caps.Send {
		t.Fatalf("Send should be false while an active turn is reserved")
	}

	// No QueueFunc registered: Queue stays false.
	srv2 := NewServer(ServerConfig{})
	srv2.SetAppIdentity("local", "th_2")
	srv2.SetProcessing(true)
	if caps2 := srv2.appCapabilities("active", true); caps2.Queue {
		t.Fatalf("Queue should be false without QueueFunc registered")
	}
}

// TestServerAppWireTurnQueueAcceptsMidTurnMessage verifies the
// turn/queue handler dispatches text to the registered QueueFunc when a
// turn is in flight.
func TestServerAppWireTurnQueueAcceptsMidTurnMessage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	var got []string
	srv.SetQueueFunc(func(text string) error {
		got = append(got, text)
		return nil
	})
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if len(got) != 1 || got[0] != "queued" {
		t.Fatalf("got=%v", got)
	}
}

func TestServerAppWireTurnQueueAcceptsReservedActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"
	var got []string
	srv.SetQueueFunc(func(text string) error {
		got = append(got, text)
		return nil
	})
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if len(got) != 1 || got[0] != "queued" {
		t.Fatalf("got=%v", got)
	}
}

func TestServerAppWireTurnStartRejectsReservedActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "second"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error response, got %v", resp.Kind())
	}
	if srv.appReservedTurnID != "turn_reserved" || srv.appActiveTurnID != "turn_reserved" {
		t.Fatalf("reservation mutated after rejected start: active=%q reserved=%q", srv.appActiveTurnID, srv.appReservedTurnID)
	}
}

func TestReserveAppTurnIDForStartIsAtomic(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	ids := make(chan string, 2)
	for range 2 {
		wg.Go(func() {
			<-start
			id, err := srv.reserveAppTurnIDForStart()
			errs <- err
			ids <- id
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	close(ids)

	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
		} else {
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want one success and one conflict", successes, conflicts)
	}
}

func TestServerAppWireTurnQueueRejectsStaleProjectedActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	srv.appActiveTurnID = "turn_stale"
	srv.SetQueueFunc(func(string) error { return nil })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error response, got %v", resp.Kind())
	}
}

// TestServerAppWireTurnQueueRejectsWhenIdle verifies the handler returns
// Conflict when the session is not processing.
func TestServerAppWireTurnQueueRejectsWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	srv.SetQueueFunc(func(string) error { return nil })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error response, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

// TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages verifies the
// handler returns Conflict when no messages are queued.
func TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	srv.SetDrainAsSteerFunc(func() error { return nil })
	srv.SetQueueDepthFunc(func() int { return 0 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", ExpectedQueueRevision: 0,
		Ref: "local:th_1",
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

func TestServerAppWireTurnDrainAsSteerRejectsReservedTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"
	called := 0
	srv.SetDrainAsSteerFunc(func() error { called++; return nil })
	srv.SetQueueDepthFunc(func() int { return 1 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", ExpectedQueueRevision: 0,
		Ref: "local:th_1",
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
	if called != 0 {
		t.Fatalf("drain called=%d, want 0", called)
	}
}

// TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued verifies the
// handler invokes the registered drain callback.
func TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	called := 0
	srv.SetDrainAsSteerFunc(func() error { called++; return nil })
	srv.SetQueueDepthFunc(func() int { return 2 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", ExpectedQueueRevision: 0,
		Ref: "local:th_1",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if called != 1 {
		t.Fatalf("drain called=%d, want 1", called)
	}
}

func TestServerAppWireTurnDrainAsSteerDispatchesInputAtomically(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "active"})
	srv.SetDrainAsSteerFunc(func() error {
		t.Fatal("classic drain callback should not be used for input-bearing drain")
		return nil
	})
	var gotText string
	var gotImages []ImageAttachment
	srv.SetDrainAsSteerWithInputFunc(func(text string, images []ImageAttachment) error {
		gotText = text
		gotImages = append([]ImageAttachment(nil), images...)
		return nil
	})
	srv.SetQueueDepthFunc(func() int { return 0 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedTurnID: "test-turn", ExpectedQueueRevision: 0,
		Ref: "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "composer payload"}, {
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte("png"),
			Name:      "shot.png",
		}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if gotText != "composer payload" {
		t.Fatalf("text=%q, want composer payload", gotText)
	}
	if len(gotImages) != 1 || gotImages[0].Name != "shot.png" || !bytes.Equal(gotImages[0].Data, []byte("png")) {
		t.Fatalf("images=%+v", gotImages)
	}
}
