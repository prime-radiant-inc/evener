package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
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

// TestServerAppWireSetProcessingPublishesActiveTurnID proves generic processing
// paths atomically publish an active identity even before a SessionStart event
// reaches the projector. Durable client-mutation turns use SetProcessingTurn
// to publish their already-authoritative stable identity instead.
func TestServerAppWireSetProcessingPublishesActiveTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	// Mirrors nextTurnCtx (cmd/evener/serve.go): RecordAppEvent has not yet
	// populated appActiveTurnID from the next turn's SessionStart event.
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
	if data.Thread.Evener.ActiveTurnID == "" {
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
// pins that thread.evener.activeTurnId and the snapshot's turns answer different
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
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
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
	if out.Thread.Evener.ActiveTurnID != startResp.Turn.ID {
		t.Fatalf("active turn id=%q, want %q", out.Thread.Evener.ActiveTurnID, startResp.Turn.ID)
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
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
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	_ = start.Response.Result.(appwire.TurnStartResponse)

	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
		Ref: "local:th_1",
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
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	_ = start.Response.Result.(appwire.TurnStartResponse)

	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "image", MediaType: "image/png", Name: "shot.png", Data: []byte("png")}},
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

// TestServerAppWireTurnInterruptCancelsTheRunningTurn covers the legacy
// SetCancelFunc path (not RetrySafeTurnFunctions): an interrupt names no turn,
// so what it must do is cancel whatever is running -- exactly once.
func TestServerAppWireTurnInterruptCancelsTheRunningTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	cancelled := 0
	srv.SetCancelFunc(func() { cancelled++ })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	_ = start.Response.Result.(appwire.TurnStartResponse)

	stopped := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		ClientMutationID:   "test-mutation",
		ExpectedInstanceID: "th_1",
		Ref:                "local:th_1",
	}))
	if stopped.Kind() != appwire.MessageResponse {
		t.Fatalf("interrupt=%+v", stopped)
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
		if got := appStatus(state, false, false); got != want {
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
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

func TestServerAppWireGoalUpdatedFanoutToEverySubscribedClient(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	ctx := context.Background()
	newClient := func(name string) *appwire.Client {
		t.Helper()
		transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
		if err != nil {
			t.Fatalf("%s dial: %v", name, err)
		}
		t.Cleanup(func() { _ = transport.Close() })
		client := appwire.NewClient(transport)
		client.Start(ctx)
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("%s initialize: %v", name, err)
		}
		return client
	}
	awaitGoalUpdated := func(name string, client *appwire.Client, want *appwire.GoalState) {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for {
			select {
			case notification := <-client.Notifications():
				if notification.Method != appwire.NotifyEvenerGoalUpdated {
					continue
				}
				var params appwire.GoalUpdatedParams
				if err := json.Unmarshal(notification.Params, &params); err != nil {
					t.Fatalf("%s decode goal update: %v", name, err)
				}
				if params.ThreadID != "th_1" || params.Ref != "local:th_1" {
					t.Fatalf("%s goal update target = %+v, want th_1/local:th_1", name, params)
				}
				if want == nil {
					if params.Goal != nil {
						t.Fatalf("%s clear goal = %+v, want nil", name, params.Goal)
					}
					return
				}
				if params.Goal == nil || *params.Goal != *want {
					t.Fatalf("%s goal = %+v, want %+v", name, params.Goal, want)
				}
				return
			case <-deadline.C:
				t.Fatalf("%s timed out waiting for evener/goal/updated", name)
			}
		}
	}

	first := newClient("first")
	second := newClient("second")
	for _, subscriber := range []struct {
		name   string
		client *appwire.Client
	}{
		{name: "first", client: first},
		{name: "second", client: second},
	} {
		if _, err := subscriber.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}); err != nil {
			t.Fatalf("%s thread/read: %v", subscriber.name, err)
		}
	}
	if got := srv.AppSubscriberCount("th_1"); got != 2 {
		t.Fatalf("subscriber count=%d, want 2", got)
	}

	wantSet := &appwire.GoalState{Objective: "ship every client", Status: "active", Iterations: 3}
	srv.RecordAppEvent(events.SessionEvent{
		Kind:      events.EventGoalUpdated,
		SessionID: "th_1",
		Data:      events.GoalUpdatedData{Goal: &events.GoalStateData{Objective: wantSet.Objective, Status: wantSet.Status, Iterations: wantSet.Iterations}},
	})
	awaitGoalUpdated("first", first, wantSet)
	awaitGoalUpdated("second", second, wantSet)
	for _, reader := range []struct {
		name   string
		client *appwire.Client
	}{{"first", first}, {"second", second}} {
		read, err := reader.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1"})
		if err != nil {
			t.Fatalf("%s thread/read after goal update: %v", reader.name, err)
		}
		if read.Thread.Evener.Goal == nil || *read.Thread.Evener.Goal != *wantSet {
			t.Fatalf("%s read goal = %+v, want %+v", reader.name, read.Thread.Evener.Goal, wantSet)
		}
	}

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "th_1", Data: events.GoalUpdatedData{Goal: nil}})
	awaitGoalUpdated("first", first, nil)
	awaitGoalUpdated("second", second, nil)
}

func newTask2SubscribedClient(t *testing.T, srv *Server, name string, refs ...string) *appwire.Client {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	t.Cleanup(httpServer.Close)
	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("%s dial: %v", name, err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("%s initialize: %v", name, err)
	}
	for _, ref := range refs {
		if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true, ReplaceSubscription: false}); err != nil {
			t.Fatalf("%s subscribe %s: %v", name, ref, err)
		}
	}
	return client
}

func awaitTask2Notification(t *testing.T, client *appwire.Client, method string) appwire.Notification {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method == method {
				return notification
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", method)
		}
	}
}

func TestServerAppWireTaskAndGoalPatchesAreInSnapshotBeforeNotificationDelivery(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	source := publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "stale source task"}},
		meta:  schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "stale source goal", Status: "active"}},
	})
	client := newTask2SubscribedClient(t, srv, "atomic", "local:root")

	feedBridge(srv, events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "root", Data: events.TaskUpdatedData{
		Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "carrier task"},
		TaskStoreOwnerSessionID: "root",
	}})
	awaitTask2Notification(t, client, appwire.NotifyEvenerTaskUpdated)
	read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:root"})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Tasks == nil || read.Thread.Evener.Tasks.Current == nil || read.Thread.Evener.Tasks.Current.Description != "carrier task" {
		t.Fatalf("snapshot after delivered task notification = %+v", read.Thread.Evener.Tasks)
	}

	goal := &events.GoalStateData{Objective: "carrier goal", Status: "active", Iterations: 2}
	feedBridge(srv, events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "root", Data: events.GoalUpdatedData{Goal: goal}})
	awaitTask2Notification(t, client, appwire.NotifyEvenerGoalUpdated)
	read, err = client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:root"})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Goal == nil || read.Thread.Evener.Goal.Objective != goal.Objective || read.Thread.Evener.Goal.Status != goal.Status || read.Thread.Evener.Goal.Iterations != goal.Iterations {
		t.Fatalf("snapshot after delivered goal notification = %+v", read.Thread.Evener.Goal)
	}
	if source.tasks.Current.Description != "stale source task" || source.meta.Goal.Objective != "stale source goal" {
		t.Fatal("fixture source changed; test no longer proves carrier-first projection")
	}
}

func TestServerAppWireCheckpointCannotOverwriteDescendantSharedTaskCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "old sampled task"}},
	})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    10,
		TaskPublicationRevision: 1,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "old sampled task"},
		}},
	}})
	src.queue = appwire.QueueState{Depth: 4, Revision: 2}

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	src.parkOnMeta = func() {
		once.Do(func() {
			close(parked)
			<-release
		})
	}
	checkpointDone := make(chan struct{})
	go func() {
		defer close(checkpointDone)
		BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root"}, nil)
	}()
	<-parked

	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "child", Data: events.TaskUpdatedData{
		Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "new shared-owner task"},
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    10,
		TaskPublicationRevision: 2,
	}})
	notifications := srv.AppNotificationsAfter(0, "root")
	if len(notifications) == 0 || notifications[len(notifications)-1].Notification.Method != appwire.NotifyEvenerTaskUpdated {
		t.Fatalf("root notifications = %+v, want task update", notifications)
	}
	assertCurrentTask := func(stage string) {
		t.Helper()
		thread := readThreadOverWire(t, srv, "local:root")
		if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "new shared-owner task" {
			t.Fatalf("%s tasks = %+v, want descendant carrier", stage, thread.Evener.Tasks)
		}
	}
	assertCurrentTask("notification cut")
	close(release)
	<-checkpointDone
	assertCurrentTask("after checkpoint")
	if queue := readThreadOverWire(t, srv, "local:root").Evener.Queue; queue.Depth != 4 || queue.Revision != 2 {
		t.Fatalf("unaffected checkpoint queue = %+v, want updated sample", queue)
	}
}

func TestServerAppWireCheckpointCannotOverwriteConcurrentGoalCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{Goal: &schema.GoalSnapshot{
		Objective: "old sampled goal", Status: "active", Iterations: 1,
	}}})

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	src.parkAfterMeta = func() {
		once.Do(func() {
			close(parked)
			<-release
		})
	}
	checkpointDone := make(chan struct{})
	go func() {
		defer close(checkpointDone)
		BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root"}, nil)
	}()
	<-parked

	BridgeEvent(srv, events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "root", Data: events.GoalUpdatedData{Goal: &events.GoalStateData{
		Objective: "new carrier goal", Status: "active", Iterations: 2,
	}}}, nil)
	notifications := srv.AppNotificationsAfter(0, "root")
	if len(notifications) == 0 || notifications[len(notifications)-1].Notification.Method != appwire.NotifyEvenerGoalUpdated {
		t.Fatalf("root notifications = %+v, want goal update", notifications)
	}
	assertGoal := func(stage string) {
		t.Helper()
		thread := readThreadOverWire(t, srv, "local:root")
		if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "new carrier goal" || thread.Evener.Goal.Iterations != 2 {
			t.Fatalf("%s goal = %+v, want direct carrier", stage, thread.Evener.Goal)
		}
	}
	assertGoal("notification cut")
	close(release)
	<-checkpointDone
	assertGoal("after checkpoint")
}

func TestServerAppWireCheckpointStillRepairsTaskAndGoalWithoutConcurrentCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "initial sampled task"}},
		meta:  schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "initial sampled goal", Status: "active", Iterations: 1}},
	})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "root", Data: events.TaskUpdatedData{
		Total: 2, Current: &events.TaskSummaryData{ID: 2, Description: "stale carrier task"},
	}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "root", Data: events.GoalUpdatedData{Goal: &events.GoalStateData{
		Objective: "stale carrier goal", Status: "active", Iterations: 2,
	}}})

	src.tasks = &appwire.TaskAggregate{Total: 3, Done: 1, Current: &appwire.TaskSummary{ID: 3, Description: "checkpoint repaired task"}}
	src.meta.Goal = &schema.GoalSnapshot{Objective: "checkpoint repaired goal", Status: "active", Iterations: 4}
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root"}, nil)

	thread := readThreadOverWire(t, srv, "local:root")
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "checkpoint repaired task" {
		t.Fatalf("repaired tasks = %+v", thread.Evener.Tasks)
	}
	if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "checkpoint repaired goal" || thread.Evener.Goal.Iterations != 4 {
		t.Fatalf("repaired goal = %+v", thread.Evener.Goal)
	}
}

func TestServerAppWireTaskAndGoalUpdatesHaveOneOrderForEveryClient(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{})
	first := newTask2SubscribedClient(t, srv, "ordered-first", "local:root")
	second := newTask2SubscribedClient(t, srv, "ordered-second", "local:root")

	insideCommit := make(chan struct{})
	release := make(chan struct{})
	var parked sync.Once
	setInsideAppProjectionCommitHook(t, func() {
		parked.Do(func() {
			close(insideCommit)
			<-release
		})
	})

	taskDone := make(chan struct{})
	go func() {
		defer close(taskDone)
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "root", Data: events.TaskUpdatedData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "first carrier"},
			TaskStoreOwnerSessionID: "root",
		}})
	}()
	<-insideCommit

	goalReached := make(chan struct{})
	var reached sync.Once
	srv.mu.Lock()
	srv.beforeAppProjectionCommit = func() { reached.Do(func() { close(goalReached) }) }
	srv.mu.Unlock()
	goalDone := make(chan struct{})
	go func() {
		defer close(goalDone)
		srv.RecordAppEvent(events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "root", Data: events.GoalUpdatedData{
			Goal: &events.GoalStateData{Objective: "second carrier", Status: "active", Iterations: 1},
		}})
	}()
	<-goalReached
	close(release)
	<-taskDone
	<-goalDone

	for _, receiver := range []struct {
		name   string
		client *appwire.Client
	}{{"first", first}, {"second", second}} {
		methods := []string{
			awaitTask2Notification(t, receiver.client, appwire.NotifyEvenerTaskUpdated).Method,
			awaitTask2Notification(t, receiver.client, appwire.NotifyEvenerGoalUpdated).Method,
		}
		if methods[0] != appwire.NotifyEvenerTaskUpdated || methods[1] != appwire.NotifyEvenerGoalUpdated {
			t.Fatalf("%s methods = %v, want task then goal", receiver.name, methods)
		}
		read, err := receiver.client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:root"})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.Evener.Tasks == nil || read.Thread.Evener.Tasks.Current == nil || read.Thread.Evener.Tasks.Current.Description != "first carrier" ||
			read.Thread.Evener.Goal == nil || read.Thread.Evener.Goal.Objective != "second carrier" {
			t.Fatalf("%s final state = tasks:%+v goal:%+v", receiver.name, read.Thread.Evener.Tasks, read.Thread.Evener.Goal)
		}
	}
}

func TestServerAppWireDescendantSessionStartSeedsCurrentTaskAndGoal(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "child",
		CurrentWork: &events.CurrentWorkSeedData{
			Tasks: &events.TaskStateData{Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "descendant seed"}},
			Goal:  &events.GoalStateData{Objective: "descendant objective", Status: "active", Iterations: 3},
		},
	}})
	thread := readThreadOverWire(t, srv, "local:child")
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "descendant seed" {
		t.Fatalf("descendant start tasks = %+v", thread.Evener.Tasks)
	}
	if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "descendant objective" || thread.Evener.Goal.Iterations != 3 {
		t.Fatalf("descendant start goal = %+v", thread.Evener.Goal)
	}
}

func TestServerAppWireDescendantSessionStartExplicitlyClearsGoal(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	seed := events.SessionStartData{TaskStoreOwnerSessionID: "child", CurrentWork: &events.CurrentWorkSeedData{
		Goal: &events.GoalStateData{Objective: "cached descendant goal", Status: "active", Iterations: 2},
	}}
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: seed})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{}})
	if got := readThreadOverWire(t, srv, "local:child").Evener.Goal; got == nil || got.Objective != "cached descendant goal" {
		t.Fatalf("legacy descendant start cleared cached goal: %+v", got)
	}
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "child", CurrentWork: &events.CurrentWorkSeedData{Goal: nil},
	}})
	if got := readThreadOverWire(t, srv, "local:child").Evener.Goal; got != nil {
		t.Fatalf("present descendant start did not clear goal: %+v", got)
	}
}

func TestServerAppWireDescendantCarriersReplaceSeedAndClearGoal(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{
		tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "root task"}},
		meta:  schema.SessionMeta{Goal: &schema.GoalSnapshot{Objective: "root goal", Status: "active"}},
	})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "child", CurrentWork: &events.CurrentWorkSeedData{
			Tasks: &events.TaskStateData{Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "child old task"}},
			Goal:  &events.GoalStateData{Objective: "child old goal", Status: "active"},
		},
	}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "child", Data: events.TaskUpdatedData{
		Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "child replacement"},
		TaskStoreOwnerSessionID: "child",
	}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventGoalUpdated, SessionID: "child", Data: events.GoalUpdatedData{Goal: nil}})
	child := readThreadOverWire(t, srv, "local:child")
	if child.Evener.Tasks == nil || child.Evener.Tasks.Current == nil || child.Evener.Tasks.Current.Description != "child replacement" || child.Evener.Goal != nil {
		t.Fatalf("child carriers = tasks:%+v goal:%+v", child.Evener.Tasks, child.Evener.Goal)
	}
	root := readThreadOverWire(t, srv, "local:root")
	if root.Evener.Tasks == nil || root.Evener.Tasks.Current == nil || root.Evener.Tasks.Current.Description != "root task" || root.Evener.Goal == nil || root.Evener.Goal.Objective != "root goal" {
		t.Fatalf("child carriers changed root = tasks:%+v goal:%+v", root.Evener.Tasks, root.Evener.Goal)
	}
}

func TestServerAppWireSharedTaskOwnerFansOutInOneCommit(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "root old"}}})
	for _, start := range []struct {
		id, owner, task string
		epoch           uint64
	}{
		{"matching-child", "root", "matching old", 20},
		{"unrelated-child", "unrelated-child", "unrelated old", 21},
	} {
		srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: start.id, Data: events.SessionStartData{
			TaskStoreOwnerSessionID: start.owner,
			TaskPublicationEpoch:    start.epoch,
			TaskPublicationRevision: 1,
			CurrentWork:             &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: start.task}}},
		}})
	}
	cursor := srv.appNotifier.CurrentSequence()
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "matching-child", Data: events.TaskUpdatedData{
		Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "shared replacement"},
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    20,
		TaskPublicationRevision: 2,
	}})
	for _, id := range []string{"root", "matching-child"} {
		thread := readThreadOverWire(t, srv, "local:"+id)
		if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "shared replacement" {
			t.Fatalf("%s tasks = %+v, want shared replacement", id, thread.Evener.Tasks)
		}
		notifications := srv.AppNotificationsAfter(cursor, id)
		if len(notifications) != 1 || notifications[0].Notification.Method != appwire.NotifyEvenerTaskUpdated {
			t.Fatalf("%s notifications = %+v, want one task update", id, notifications)
		}
		var params appwire.TaskUpdatedParams
		if err := json.Unmarshal(notifications[0].Notification.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.ThreadID != id || params.Ref != "local:"+id {
			t.Fatalf("%s notification target = %+v", id, params)
		}
	}
	unrelated := readThreadOverWire(t, srv, "local:unrelated-child")
	if unrelated.Evener.Tasks == nil || unrelated.Evener.Tasks.Current == nil || unrelated.Evener.Tasks.Current.Description != "unrelated old" {
		t.Fatalf("unrelated tasks changed: %+v", unrelated.Evener.Tasks)
	}
	if got := srv.AppNotificationsAfter(cursor, "unrelated-child"); len(got) != 0 {
		t.Fatalf("unrelated child received shared update: %+v", got)
	}
}

func TestServerAppWireTaskPublicationRevisionRejectsDelayedRootCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "base task"}}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "root", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    30,
		TaskPublicationRevision: 1,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "base task"},
		}},
	}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    30,
		TaskPublicationRevision: 1,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "base task"},
		}},
	}})

	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "child", Data: events.TaskUpdatedData{
		Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "newer child carrier"},
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    30,
		TaskPublicationRevision: 3,
	}})
	afterChild := srv.appNotifier.CurrentSequence()

	// This models the actual transport inversion: the root committed revision 2
	// first but its asynchronous event drain delivers it only after the child has
	// synchronously applied revision 3.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "root", Data: events.TaskUpdatedData{
		Total: 2, Current: &events.TaskSummaryData{ID: 1, Description: "delayed older root carrier"},
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    30,
		TaskPublicationRevision: 2,
	}})
	for _, id := range []string{"root", "child"} {
		thread := readThreadOverWire(t, srv, "local:"+id)
		if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "newer child carrier" {
			t.Fatalf("%s tasks after delayed root = %+v, want newer child carrier", id, thread.Evener.Tasks)
		}
		if notifications := srv.AppNotificationsAfter(afterChild, id); len(notifications) != 0 {
			t.Fatalf("%s received delayed older notification: %+v", id, notifications)
		}
	}

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "root", Data: events.TaskUpdatedData{
		Total: 3, Done: 2, Current: &events.TaskSummaryData{ID: 3, Description: "newest root carrier"},
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    30,
		TaskPublicationRevision: 4,
	}})
	for _, id := range []string{"root", "child"} {
		thread := readThreadOverWire(t, srv, "local:"+id)
		if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "newest root carrier" {
			t.Fatalf("%s tasks after newer root = %+v", id, thread.Evener.Tasks)
		}
		notifications := srv.AppNotificationsAfter(afterChild, id)
		if len(notifications) != 1 || notifications[0].Notification.Method != appwire.NotifyEvenerTaskUpdated {
			t.Fatalf("%s newer-root notifications = %+v, want one task update", id, notifications)
		}
	}
}

func TestServerAppWireOldTaskProducerUpdatesOnlySource(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{tasks: &appwire.TaskAggregate{Total: 1, Current: &appwire.TaskSummary{ID: 1, Description: "root old"}}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    40,
		TaskPublicationRevision: 5,
		CurrentWork:             &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "child old"}}},
	}})
	cursor := srv.appNotifier.CurrentSequence()
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: "child", Data: events.TaskUpdatedData{
		Total: 1, Current: &events.TaskSummaryData{ID: 2, Description: "legacy source only"},
		TaskStoreOwnerSessionID: "root",
		// Revision zero identifies a producer predating the ordering fence. Even
		// with owner metadata, compatibility routing must remain source-only.
	}})
	child := readThreadOverWire(t, srv, "local:child")
	if child.Evener.Tasks == nil || child.Evener.Tasks.Current == nil || child.Evener.Tasks.Current.Description != "legacy source only" {
		t.Fatalf("legacy source tasks = %+v", child.Evener.Tasks)
	}
	root := readThreadOverWire(t, srv, "local:root")
	if root.Evener.Tasks == nil || root.Evener.Tasks.Current == nil || root.Evener.Tasks.Current.Description != "root old" {
		t.Fatalf("legacy producer changed root: %+v", root.Evener.Tasks)
	}
	if got := srv.AppNotificationsAfter(cursor, "root"); len(got) != 0 {
		t.Fatalf("legacy producer notified root: %+v", got)
	}
}

func TestServerAppWireTaskPublicationRevisionResetsWithIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "root", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    50,
		TaskPublicationRevision: 10,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "old identity high revision"},
		}},
	}})

	// A restored/replaced identity can reuse the same durable session ID while
	// owning a newly constructed TaskStore whose in-memory revision restarts.
	srv.SetAppIdentity("local", "root")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "root", Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "root",
		TaskPublicationEpoch:    51,
		TaskPublicationRevision: 1,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "replacement low revision"},
		}},
	}})
	thread := readThreadOverWire(t, srv, "local:root")
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "replacement low revision" {
		t.Fatalf("replacement tasks = %+v, want reset revision fence", thread.Evener.Tasks)
	}
}

func TestServerAppWireTaskPublicationEpochAllowsSameIDColdRestore(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	publishEnvelope(srv, &stubThreadEnvelopeSource{})
	ownerID := "stable-child"
	nextPublication := func(store *taskpkg.TaskStore) (epoch, revision uint64) {
		t.Helper()
		if err := store.MutateAndPublish(func(gotEpoch, gotRevision uint64) error {
			epoch, revision = gotEpoch, gotRevision
			return nil
		}); err != nil {
			t.Fatalf("reserve task publication: %v", err)
		}
		return epoch, revision
	}
	oldStore := taskpkg.NewTaskStore(t.TempDir(), ownerID)
	oldEpoch, oldStartRevision := nextPublication(oldStore)
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: ownerID, Data: events.SessionStartData{
		TaskStoreOwnerSessionID: ownerID,
		TaskPublicationEpoch:    oldEpoch,
		TaskPublicationRevision: oldStartRevision,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "old incarnation start"},
		}},
	}})
	var oldHighRevision uint64
	for range 5 {
		_, oldHighRevision = nextPublication(oldStore)
	}
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: ownerID, Data: events.TaskUpdatedData{
		Total: 6, Done: 5, Current: &events.TaskSummaryData{ID: 6, Description: "old incarnation high revision"},
		TaskStoreOwnerSessionID: ownerID,
		TaskPublicationEpoch:    oldEpoch,
		TaskPublicationRevision: oldHighRevision,
	}})

	// Cold restore constructs a new non-shared TaskStore but reuses both root and
	// child/owner IDs. Its first revision must establish the newer incarnation.
	newStore := taskpkg.NewTaskStore(t.TempDir(), ownerID)
	newEpoch, newStartRevision := nextPublication(newStore)
	if newEpoch <= oldEpoch || newStartRevision != 1 {
		t.Fatalf("cold restore publication = %d:%d after %d, want newer epoch revision 1", newEpoch, newStartRevision, oldEpoch)
	}
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: ownerID, Data: events.SessionStartData{
		TaskStoreOwnerSessionID: ownerID,
		TaskPublicationEpoch:    newEpoch,
		TaskPublicationRevision: newStartRevision,
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{
			Total: 1, Current: &events.TaskSummaryData{ID: 1, Description: "restored incarnation start"},
		}},
	}})
	thread := readThreadOverWire(t, srv, "local:"+ownerID)
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "restored incarnation start" {
		t.Fatalf("cold-restored start tasks = %+v", thread.Evener.Tasks)
	}

	_, newUpdateRevision := nextPublication(newStore)
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: ownerID, Data: events.TaskUpdatedData{
		Total: 2, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "restored incarnation update"},
		TaskStoreOwnerSessionID: ownerID,
		TaskPublicationEpoch:    newEpoch,
		TaskPublicationRevision: newUpdateRevision,
	}})
	cursor := srv.appNotifier.CurrentSequence()

	// A delayed carrier from the retired store has a numerically higher revision,
	// but its older epoch must no longer be accepted.
	_, delayedOldRevision := nextPublication(oldStore)
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventTaskUpdated, SessionID: ownerID, Data: events.TaskUpdatedData{
		Total: 7, Done: 6, Current: &events.TaskSummaryData{ID: 7, Description: "delayed retired incarnation"},
		TaskStoreOwnerSessionID: ownerID,
		TaskPublicationEpoch:    oldEpoch,
		TaskPublicationRevision: delayedOldRevision,
	}})
	thread = readThreadOverWire(t, srv, "local:"+ownerID)
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "restored incarnation update" {
		t.Fatalf("tasks after retired carrier = %+v", thread.Evener.Tasks)
	}
	if notifications := srv.AppNotificationsAfter(cursor, ownerID); len(notifications) != 0 {
		t.Fatalf("retired incarnation produced notifications: %+v", notifications)
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
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.contextPressure = 0.42 })
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.contextMetrics = ContextMetrics{Used: 42000, Window: 100000, Remaining: 58000}
	})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.detailedStatus = DetailedStatus{
			Tools: []ToolInfo{{Name: "shell", Source: "core"}},
			MCP:   []MCPServerInfo{{Name: "linear", Tools: []string{"search"}}},
			Skills: []SkillInfo{
				{Name: "superpowers:systematic-debugging", Description: "debug"},
			},
			Plugins:    []PluginStatusInfo{{Name: "superpowers", Version: "4.3.0", SkillCount: 12, AgentCount: 2, HookCount: 4}},
			HookEvents: []HookEventStatus{{Event: "PreToolUse", Count: 3}},
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
	if data.Thread.ID != "th_1" || data.Thread.SessionID != "sess_1" || data.Thread.Evener.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", data.Thread)
	}
	if data.Thread.ModelProvider != "gpt-5" || data.Thread.Evener.Profile != "openai" {
		t.Fatalf("thread model/profile=%+v", data.Thread)
	}
	if data.Thread.Evener.ContextPressure != 0.42 {
		t.Fatalf("context pressure=%v", data.Thread.Evener.ContextPressure)
	}
	if data.Thread.Evener.ContextUsed != 42000 || data.Thread.Evener.ContextWindow != 100000 || data.Thread.Evener.ContextRemaining != 58000 {
		t.Fatalf("context metrics=%+v", data.Thread.Evener)
	}
	diag := data.Thread.Evener.Diagnostics
	if diag == nil || len(diag.Tools) != 1 || len(diag.MCP) != 1 || len(diag.Skills) != 1 || len(diag.Plugins) != 1 || len(diag.Jobs) != 1 || len(diag.Agents) != 1 {
		t.Fatalf("diagnostics=%+v", diag)
	}
	if diag.Jobs[0].JobID != "job-1" || diag.Jobs[0].JobType != "delegate" || diag.Jobs[0].Status != "failed" ||
		diag.Jobs[0].Reason != "exit_nonzero" || diag.Jobs[0].ExitCode == nil || *diag.Jobs[0].ExitCode != exitCode ||
		diag.Jobs[0].OutputBytes != 128 || diag.Jobs[0].TranscriptRef != "local:child-1" {
		t.Fatalf("job diagnostics=%+v", diag.Jobs)
	}
	if len(diag.HookEvents) != 1 || diag.HookEvents[0].Event != "PreToolUse" || diag.HookEvents[0].Count != 3 {
		t.Fatalf("hook events=%+v", diag.HookEvents)
	}
}

func TestServerAppWireThreadReadCarriesTurnCountWithoutTurns(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "idle", Turns: 37})

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
	if data.Thread.Evener.TurnCount != 37 {
		t.Fatalf("turnCount=%d, want 37", data.Thread.Evener.TurnCount)
	}
	if len(data.Thread.Turns) != 0 {
		t.Fatalf("turns=%d, want none on a bounded status read", len(data.Thread.Turns))
	}
}

// TestServerAppWireThreadReadIncludesWorkMetrics (WS2 A7) verifies appThread
// populates EvenerThread.Usage/WorkMillis/ActiveTurnStartedAt from the
// workMetricsFn pull callback, alongside the existing pressure/detailed-status
// callbacks exercised by TestServerAppWireThreadReadReturnsStatus.
func TestServerAppWireThreadReadIncludesWorkMetrics(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.workMillis = 4200
		e.usage = &appwire.EvenerUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalTokens: 30}
		e.turnStartedAt = 1234567890
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
	evener := data.Thread.Evener
	if evener.WorkMillis != 4200 {
		t.Fatalf("workMillis=%d, want 4200", evener.WorkMillis)
	}
	if evener.ActiveTurnStartedAt != 1234567890 {
		t.Fatalf("activeTurnStartedAt=%d, want 1234567890", evener.ActiveTurnStartedAt)
	}
	wantUsage := appwire.EvenerUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalTokens: 30}
	if evener.Usage == nil || *evener.Usage != wantUsage {
		t.Fatalf("usage=%+v, want %+v", evener.Usage, wantUsage)
	}
}

func TestServerAppWireThreadReadTaskAggregatePresence(t *testing.T) {
	readTasks := func(aggregate *appwire.TaskAggregate) *appwire.TaskAggregate {
		t.Helper()
		srv := NewServer(ServerConfig{})
		srv.SetAppIdentity("local", "th_1")
		if aggregate != nil {
			setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.tasks = aggregate })
		}

		conn := srv.AppServer().NewConnection("test")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
		data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
		if !ok {
			t.Fatalf("result=%T", resp.Response.Result)
		}
		return data.Thread.Evener.Tasks
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
// stamps EvenerThread.Cost from the pulled cumulative usage at the session
// model's price — the session-level dollar total kept current across
// snapshots exactly as WorkMillis/Usage are — and omits it when the model is
// uncataloged (absent-vs-zero honesty).
func TestServerAppWireThreadReadIncludesCostTotal(t *testing.T) {
	readEvener := func(model string) appwire.EvenerThread {
		t.Helper()
		srv := NewServer(ServerConfig{})
		srv.SetAppIdentity("local", "th_1")
		srv.SetStatus(StatusInfo{SessionID: "th_1", Model: model})
		setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
			e.workMillis = 4200
			e.usage = &appwire.EvenerUsage{InputTokens: 100_000, OutputTokens: 20_000, TotalTokens: 120_000}
			e.turnStartedAt = 0
		})
		conn := srv.AppServer().NewConnection("test")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
		data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
		if !ok {
			t.Fatalf("result=%T", resp.Response.Result)
		}
		return data.Thread.Evener
	}

	priced := readEvener("claude-opus-4-5")
	if want := appwire.EstimateCost("claude-opus-4-5", priced.Usage); priced.Cost != want || want == "" {
		t.Fatalf("cost=%q, want non-empty %q", priced.Cost, want)
	}
	if !strings.HasPrefix(priced.Cost, "~$") {
		t.Fatalf("cost=%q, want ~$ prefix", priced.Cost)
	}

	if uncataloged := readEvener("totally-unknown-model-xyz"); uncataloged.Cost != "" {
		t.Fatalf("uncataloged cost=%q, want \"\" (absent, not ~$0.00)", uncataloged.Cost)
	}
}

// TestServerAppWireThreadReadOmitsWorkMetricsWhenUnwired (WS2 A7) verifies
// that a daemon which never wired SetWorkMetricsFunc (e.g. mid-upgrade, or a
// non-evener thread source) projects zero/nil metrics rather than panicking on
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
	evener := data.Thread.Evener
	if evener.Usage != nil || evener.WorkMillis != 0 || evener.ActiveTurnStartedAt != 0 {
		t.Fatalf("evener=%+v, want zero work metrics when unwired", evener)
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
		SystemPrompt: "You are Evener.",
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
	if got := prelude.Items[0]; got.Type != "systemMessage" || got.Description != "System prompt" || got.Text != "You are Evener." {
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
	if got := srv.AppSubscriberCount("th_1"); got != 1 {
		t.Fatalf("subscriber count=%d, want 1", got)
	}
}

func TestServerAppWireRootAndDescendantSubscribeOnOneConnection(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	srv.RecordDescendantAppEvent("root", events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "child",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5.5"},
	})

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
	for _, ref := range []string{"local:root", "local:child"} {
		read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{
			Ref:                 ref,
			Subscribe:           true,
			ReplaceSubscription: false,
		})
		if err != nil {
			t.Fatalf("thread/read %s: %v", ref, err)
		}
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			t.Fatalf("parse test ref %s: %v", ref, err)
		}
		if read.Thread.ID != parsed.ThreadID {
			t.Fatalf("thread/read %s returned thread %q", ref, read.Thread.ID)
		}
	}
	if got := srv.AppSubscriberCount("root"); got != 1 {
		t.Fatalf("root subscriber count = %d, want 1", got)
	}
	if got := srv.AppSubscriberCount("child"); got != 1 {
		t.Fatalf("child subscriber count = %d, want 1", got)
	}

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "root", Data: events.AssistantTextDeltaData{Delta: "root"}})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "child", Data: events.AssistantTextDeltaData{Delta: "child"}})
	if got := srv.AppNotificationsAfter(0, "root"); len(got) == 0 {
		t.Fatal("root event produced no root notification")
	}
	if got := srv.AppNotificationsAfter(0, "child"); len(got) == 0 {
		t.Fatal("descendant event produced no child notification")
	}
	wantRefs := map[string]bool{"local:root": false, "local:child": false}
	for wantRefs["local:root"] == false || wantRefs["local:child"] == false {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyAgentMessageDelta {
				continue
			}
			var params appwire.AgentMessageDeltaParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			if _, ok := wantRefs[params.Ref]; ok {
				wantRefs[params.Ref] = true
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for root and child deltas; received %v", wantRefs)
		}
	}
}

// thread/unsubscribe drops one connection's subscription to a thread — the
// same registry entry a subscribed thread/read created — and is idempotent.
// The hub-facing counterpart (relay teardown) rides on this count reaching 0.
func TestServerAppWireThreadUnsubscribeDropsSubscription(t *testing.T) {
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
	if got := srv.AppSubscriberCount("th_1"); got != 1 {
		t.Fatalf("subscriber count after subscribe = %d, want 1", got)
	}

	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("thread unsubscribe: %v", err)
	}
	if got := srv.AppSubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscriber count after unsubscribe = %d, want 0", got)
	}

	// Unsubscribed, the connection no longer receives this thread's events.
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "post-unsubscribe"}})
	select {
	case notification := <-client.Notifications():
		t.Fatalf("notification delivered after unsubscribe: %+v", notification)
	case <-time.After(200 * time.Millisecond):
	}

	// Idempotent: unsubscribing again succeeds quietly.
	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("second thread unsubscribe: %v", err)
	}
	if got := srv.AppSubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscriber count after second unsubscribe = %d, want 0", got)
	}
}

// Across a replace/clear identity swap, a subscriber registered under the
// STABLE REF must be removable by an unsubscribe naming that ref after the
// swap advanced the session: the resolution path maps the ref to the current
// session and back to the stable ref — the same key the subscribe used.
func TestServerAppWireThreadUnsubscribeResolvesStableRefAcrossSwap(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_old")
	prepared, err := PrepareAppIdentityForRef("local", "th_new", "local:th_stable", "")
	if err != nil {
		t.Fatalf("prepare replacement identity: %v", err)
	}
	srv.ReplaceAppIdentity(prepared, nil)

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
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_stable", Subscribe: true}); err != nil {
		t.Fatalf("subscribed read via stable ref: %v", err)
	}
	if got := srv.AppSubscriberCount("th_new"); got != 1 {
		t.Fatalf("subscriber count after subscribe via stable ref = %d, want 1", got)
	}

	// The unsubscribe names the SAME stable ref, after the swap.
	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:th_stable"}); err != nil {
		t.Fatalf("unsubscribe via stable ref: %v", err)
	}
	if got := srv.AppSubscriberCount("th_new"); got != 0 {
		t.Fatalf("subscriber count after unsubscribe via stable ref = %d, want 0", got)
	}
}

// An unsubscribe for a ref the daemon no longer resolves (the pre-swap ref)
// quietly succeeds and still clears the raw key the subscribe could have
// used — teardown finding nothing is a success, and a lingering key must
// not outlive the client's interest.
func TestServerAppWireThreadUnsubscribeUnresolvedRefCleansRawKeys(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_live")

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
	// Subscribe to the live thread by its bare id (one raw key form).
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{ThreadID: "th_live", Subscribe: true}); err != nil {
		t.Fatalf("subscribed read by bare id: %v", err)
	}
	if got := srv.AppSubscriberCount("th_live"); got != 1 {
		t.Fatalf("subscriber count after subscribe = %d, want 1", got)
	}

	// A ref this daemon never served: quiet success, nothing removed.
	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:th_never_served"}); err != nil {
		t.Fatalf("unsubscribe for unresolvable ref: %v", err)
	}
	if got := srv.AppSubscriberCount("th_live"); got != 1 {
		t.Fatalf("unrelated unsubscribe changed the live count = %d, want 1", got)
	}

	// The same connection unsubscribes its own bare-id key.
	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{ThreadID: "th_live"}); err != nil {
		t.Fatalf("unsubscribe by thread id: %v", err)
	}
	if got := srv.AppSubscriberCount("th_live"); got != 0 {
		t.Fatalf("subscriber count after bare-id unsubscribe = %d, want 0", got)
	}
}

func TestServerRejectsLateDescendantEventAfterIdentityReplacement(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old-root")
	srv.RecordDescendantAppEvent("old-root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "old-child"})
	srv.SetAppIdentity("local", "new-root")
	cursor := srv.appNotifier.CurrentSequence()
	closed := srv.AppNotificationsAfter(0, "old-child")
	if len(closed) == 0 || closed[len(closed)-1].Notification.Method != appwire.NotifyThreadClosed {
		t.Fatalf("old child stream was not closed on identity replacement: %+v", closed)
	}

	srv.RecordDescendantAppEvent("old-root", events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "old-child",
		Data:      events.AssistantTextDeltaData{Delta: "late"},
	})

	if got := srv.AppNotificationsAfter(cursor, "old-child"); len(got) != 0 {
		t.Fatalf("late old-child notifications after identity replacement = %d, want 0", len(got))
	}
	list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "new-root" {
		t.Fatalf("threads after replacement = %+v, want only new-root", list.Data)
	}
}

func TestServerRejectsDescendantMutationInsteadOfMutatingRoot(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	srv.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionStart, SessionID: "child"})
	called := false
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		called = true
		return appwire.TurnStartResponse{}, nil
	}})

	_, err := srv.handleAppTurnStart(context.Background(), appwire.TurnStartParams{
		Ref:                "local:child",
		ClientMutationID:   "child-mutation",
		ExpectedInstanceID: "root",
		Input:              []appwire.InputItem{{Type: "text", Text: "hello"}},
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("child turn/start error = %T %v, want session unavailable", err, err)
	}
	if called {
		t.Fatal("child-targeted turn/start invoked the root mutation callback")
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
	if got := srv.AppSubscriberCount("th_1"); got != 0 {
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", Ref: "local:th_1",
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", Ref: "local:th_1",
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1",
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", Ref: "local:th_1",
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
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", Ref: "local:th_1",
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
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 0 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", ExpectedQueueRevision: 0,
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
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 1 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", ExpectedQueueRevision: 0,
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
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 2 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", ExpectedQueueRevision: 0,
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
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.queue.Depth = 0 })
	installProjectedMutationCallbacksForTest(srv)

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "th_1", ExpectedQueueRevision: 0,
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

// TestAppTurnsFromTranscriptFileProjectsToolResultImages covers the daemon's
// only cold read of its own history (kata 2fxm). The seed it installs is the
// sole turn authority for the rest of the session, so an image it drops here
// stays missing from every later read of that thread — including the reload a
// reader does mid-session.
func TestAppTurnsFromTranscriptFileProjectsToolResultImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 's', 'e', 'e', 'd'}
	if err := w.Append(schema.NewTurn(schema.TurnToolResults, llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_shot", Name: "screenshot", Content: "captured",
				ImageData: png, ImageMediaType: "image/png",
			},
		}},
	})); err != nil {
		t.Fatalf("append tool result: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	turns := requireTranscriptFileTurns(t, path)
	if len(turns) != 1 || len(turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", turns)
	}
	images := turns[0].Items[0].OutputImages
	if len(images) != 1 {
		t.Fatalf("OutputImages=%+v, want the tool result's own image described", images)
	}
	sum := sha256.Sum256(png)
	want := appwire.OutputImage{
		Source: "tool-result", Name: "screenshot", MediaType: "image/png",
		Size: int64(len(png)), SHA: hex.EncodeToString(sum[:]),
	}
	if images[0] != want {
		t.Fatalf("OutputImages[0]=%+v, want %+v", images[0], want)
	}
}

// TestServerAppWireThreadReadRejectsMalformedOrForeignRef guards ledger
// #110/#111: a present-but-unparseable ref, or a ref naming a foreign
// source, must never fall through to the ROOT thread's own content.
// appThreadIDForRead's empty-ref fallback to appProjectionThreadID is only
// correct when the caller supplied NEITHER a ThreadID nor a Ref; a ref that
// IS supplied but doesn't resolve must error instead of silently answering
// for a different session.
func TestServerAppWireThreadReadRejectsMalformedOrForeignRef(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_root")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionStart, SessionID: "th_root"})

	for _, ref := range []string{"not-a-valid-ref", "remote:th_1"} {
		resp, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref})
		if err == nil && resp.Thread.ID != "" {
			t.Fatalf("ref=%q returned thread %+v instead of erroring", ref, resp.Thread)
		}
	}
}

// TestServerAppWireDescendantThreadReadIncludesSeededTranscriptHistory
// guards ledger #110/#111's second half: a descendant SessionStart{Restored:
// true} with a real backing transcript must seed the descendant's turn
// snapshot from that transcript on first observation, the same way
// PrepareAppIdentity seeds the ROOT thread's snapshot before any live event
// arrives. Without seeding, thread/read for the descendant only ever shows
// events recorded after the restore point, silently losing the persisted
// history.
func TestServerAppWireDescendantThreadReadIncludesSeededTranscriptHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "child"})
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

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	srv.SetDescendantTranscriptPathFunc(func(threadID string) string {
		if threadID == "child" {
			return path
		}
		return ""
	})
	srv.RecordDescendantAppEvent("root", events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "child",
		Data:      events.SessionStartData{Restored: true, TranscriptEntries: 2},
	})

	resp, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{ThreadID: "child", IncludeTurns: true})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 2 {
		t.Fatalf("turns=%v, want the 2 seeded turns from the descendant's transcript", turnIDs(resp.Thread.Turns))
	}
	if got := resp.Thread.Turns[0].Items[0].Text; got != "first" {
		t.Fatalf("first turn text=%q, want the seeded head", got)
	}
	if got := resp.Thread.Turns[1].Items[0].Text; got != "second" {
		t.Fatalf("second turn text=%q, want the seeded tail", got)
	}
}
