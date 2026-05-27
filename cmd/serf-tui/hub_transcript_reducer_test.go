package main

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/internal/appwire"
)

func TestHubTranscriptReducerReconcilesUserEchoWithReplay(t *testing.T) {
	reducer := newHubTranscriptReducer([]chatMessage{{Kind: msgUser, Text: "please inspect this"}}, nil, nil)

	reducer.applyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "user_1",
		TurnID: "turn_7",
		Text:   "please inspect this",
	}, 7, false)

	if len(reducer.messages) != 1 {
		t.Fatalf("expected replayed user message to reconcile with echo, got %+v", reducer.messages)
	}
	if reducer.messages[0].Kind != msgUser || reducer.messages[0].Text != "please inspect this" || reducer.messages[0].TurnIndex != 7 || reducer.messages[0].ItemID != "user_1" {
		t.Fatalf("reconciled user message = %+v", reducer.messages[0])
	}
}

func TestHubTranscriptReducerKeepsImageOnlyUserMessage(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyThreadItem(appwire.ThreadItem{
		Type: "userMessage",
		ID:   "user_img",
		Images: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte("png"),
		}},
	}, 3, true)

	if len(reducer.messages) != 1 {
		t.Fatalf("messages len=%d, want 1", len(reducer.messages))
	}
	if reducer.messages[0].Kind != msgUser || reducer.messages[0].Text != "[image]" || reducer.messages[0].TurnIndex != 3 {
		t.Fatalf("image-only user message = %+v", reducer.messages[0])
	}
}

func TestHubTranscriptReducerRendersSystemMessage(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyThreadItem(appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          "item_system_prompt",
		TurnID:      "turn_system",
		Description: "System prompt",
		Text:        "You are Serf.",
	}, 0, true)

	if len(reducer.messages) != 1 {
		t.Fatalf("messages len=%d, want 1", len(reducer.messages))
	}
	msg := reducer.messages[0]
	if msg.Kind != msgSystem || msg.Text != "System prompt\nYou are Serf." || msg.ItemID != "item_system_prompt" {
		t.Fatalf("system message = %+v", msg)
	}
}

func TestHubTranscriptReducerSuppressesReplayedCompletedTool(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)
	started := appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "tool_1",
		CallID:        "call_1",
		TurnID:        "turn_1",
		ToolName:      "shell",
		ArgumentsJSON: `{"command":"printf 'alpha\n'"}`,
		Status:        appwire.TurnStatusInProgress,
	}
	completed := started
	completed.Output = "alpha\n"
	completed.Status = "completed"

	reducer.applyThreadItem(started, 1, false)
	reducer.applyThreadItem(completed, 1, true)
	reducer.applyThreadItem(completed, 1, false)

	var tools []chatMessage
	for _, msg := range reducer.messages {
		if msg.Kind == msgTool {
			tools = append(tools, msg)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected one coherent tool group after live completion and replay, got %+v", reducer.messages)
	}
	if tools[0].ItemID != "tool_1" || tools[0].ToolCallID != "call_1" || tools[0].Tool == nil || tools[0].Tool.Output != "alpha\n" || !tools[0].Tool.Done {
		t.Fatalf("tool group = %+v", tools[0])
	}
}

func TestHubTranscriptReducerPairsTranscriptToolStartAndResultAcrossTurns(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)
	started := appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "tool_start",
		CallID:        "call_read",
		TurnID:        "turn_1",
		ToolName:      "read_file",
		ArgumentsJSON: `{"file_path":"/tmp/example.txt"}`,
		Status:        appwire.TurnStatusInProgress,
	}
	completed := appwire.ThreadItem{
		Type:     "commandExecution",
		ID:       "tool_result",
		CallID:   "call_read",
		TurnID:   "turn_2",
		ToolName: "read_file",
		Output:   "line 1\nline 2\n",
		Status:   appwire.TurnStatusCompleted,
	}

	reducer.applyThreadItem(started, 1, false)
	reducer.applyThreadItem(completed, 2, true)

	tools := transcriptTools(reducer.messages)
	if len(tools) != 1 {
		t.Fatalf("tools=%+v messages=%+v, want one paired tool", tools, reducer.messages)
	}
	if tools[0].Name != "read_file" || tools[0].Output != "line 1\nline 2\n" || !tools[0].Done {
		t.Fatalf("tool=%+v", tools[0])
	}
	if msg := reducer.messages[0]; msg.ItemID != "tool_result" || msg.ToolCallID != "call_read" || msg.TurnID != "turn_2" {
		t.Fatalf("message=%+v", msg)
	}
}

func TestHubTranscriptReducerScopesItemIDReconciliationByTurn(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "item_reused",
		TurnID: "turn_1",
		Text:   "first",
	}, 1, true)
	reducer.applyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "item_reused",
		TurnID: "turn_2",
		Text:   "second",
	}, 2, true)

	if len(reducer.messages) != 2 {
		t.Fatalf("messages=%+v, want separate entries for reused item IDs across turns", reducer.messages)
	}
	if reducer.messages[0].Text != "first" || reducer.messages[0].TurnIndex != 1 || reducer.messages[1].Text != "second" || reducer.messages[1].TurnIndex != 2 {
		t.Fatalf("messages=%+v, want turn-scoped reconciliation", reducer.messages)
	}
}

func TestHubTranscriptReducerScopesNonNumericTurnIDs(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "assistant_reused",
		TurnID: "turn_codex",
		Text:   "first",
	}, 0, true)
	reducer.applyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "assistant_reused",
		TurnID: "turn_stream",
		Text:   "second",
	}, 0, true)

	if len(reducer.messages) != 2 {
		t.Fatalf("messages=%+v, want separate assistant entries for non-numeric turn IDs", reducer.messages)
	}
	if reducer.messages[0].Text != "first" || reducer.messages[0].TurnID != "turn_codex" || reducer.messages[1].Text != "second" || reducer.messages[1].TurnID != "turn_stream" {
		t.Fatalf("messages=%+v, want raw turn-scoped assistant reconciliation", reducer.messages)
	}
}

func TestHubTranscriptReducerScopesAssistantDeltasByTurnID(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyAgentMessageDelta("turn_codex", "assistant_reused", "first")
	reducer.applyAgentMessageDelta("turn_stream", "assistant_reused", "second")

	if len(reducer.messages) != 2 {
		t.Fatalf("messages=%+v, want separate delta-created assistant entries", reducer.messages)
	}
	if reducer.messages[0].Text != "first" || reducer.messages[0].TurnID != "turn_codex" || reducer.messages[1].Text != "second" || reducer.messages[1].TurnID != "turn_stream" {
		t.Fatalf("messages=%+v, want turn-scoped assistant deltas", reducer.messages)
	}
}

func TestHubTranscriptReducerLiveAndReplayReconstructSameTranscript(t *testing.T) {
	user := appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "user_1",
		TurnID: "turn_1",
		Text:   "please inspect this",
	}
	toolStart := appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "tool_1",
		CallID:        "call_1",
		TurnID:        "turn_1",
		ToolName:      "shell",
		ArgumentsJSON: `{"command":"printf 'alpha\n'"}`,
		Status:        appwire.TurnStatusInProgress,
	}
	toolDone := toolStart
	toolDone.Output = "alpha\n"
	toolDone.Status = "completed"
	assistantDone := appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "agent_1",
		TurnID: "turn_1",
		Text:   "Thinking **bold**\nDone.",
		Status: "completed",
	}

	live := newHubTranscriptReducer(nil, nil, nil)
	live.applyUserMessageEcho(user.Text)
	live.applyThreadItem(user, 1, false)
	live.applyAgentMessageDelta("turn_1", "agent_1", "Thinking **bo")
	live.applyAgentMessageDelta("turn_1", "agent_1", "ld**")
	live.applyThreadItem(toolStart, 1, false)
	live.applyToolOutputDelta("tool_1", "alpha\n")
	live.applyThreadItem(toolDone, 1, true)
	live.applyThreadItem(assistantDone, 1, true)

	replay := newHubTranscriptReducer(nil, nil, nil)
	for _, item := range []appwire.ThreadItem{user, {Type: "agentMessage", ID: "agent_1", TurnID: "turn_1", Text: "Thinking **bold**\nDone.", Status: "completed"}, toolDone} {
		replay.applyThreadItem(item, 1, item.Status == "completed")
	}

	if got, want := transcriptSnapshot(live.messages), transcriptSnapshot(replay.messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("live transcript != replay transcript\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestHubTranscriptReducerToolOutputDeltaBeforeStartStaysInOneToolGroup(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyToolOutputDelta("tool_late", "first\n")
	reducer.applyThreadItem(appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "tool_late",
		CallID:        "call_late",
		TurnID:        "turn_1",
		ToolName:      "shell",
		ArgumentsJSON: `{"command":"printf 'first\n'"}`,
		Status:        "completed",
	}, 1, true)

	if len(reducer.messages) != 1 {
		t.Fatalf("expected one tool group, got %+v", reducer.messages)
	}
	msg := reducer.messages[0]
	if msg.Kind != msgTool || msg.ItemID != "tool_late" || msg.ToolCallID != "call_late" || msg.Tool == nil {
		t.Fatalf("tool message = %+v", msg)
	}
	if msg.Tool.Name != "shell" || msg.Tool.Output != "first\n" || !msg.Tool.Done {
		t.Fatalf("tool info = %+v", msg.Tool)
	}
}

func TestHubTranscriptReducerPreservesSerfAndCodexToolShapes(t *testing.T) {
	reducer := newHubTranscriptReducer(nil, nil, nil)

	reducer.applyThreadItem(appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "serf_tool",
		CallID:        "serf_call",
		TurnID:        "turn_1",
		ToolName:      "read_file",
		ArgumentsJSON: `{"file_path":"/tmp/missing.go"}`,
		Error:         "open /tmp/missing.go: no such file or directory",
		Status:        "completed",
	}, 1, true)
	reducer.applyThreadItem(appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "codex_tool",
		CallID:        "codex_tool",
		TurnID:        "turn_1",
		ToolName:      "mcp.read",
		ArgumentsJSON: `{"path":"/tmp/data.json"}`,
		Output:        `{"ok":true}`,
		Status:        "completed",
	}, 1, true)

	tools := transcriptTools(reducer.messages)
	if len(tools) != 2 {
		t.Fatalf("tools=%+v messages=%+v", tools, reducer.messages)
	}
	if tools[0].Name != "read_file" || tools[0].Error == "" || !tools[0].Done {
		t.Fatalf("serf tool not preserved: %+v", tools[0])
	}
	if tools[1].Name != "mcp.read" || tools[1].Output != `{"ok":true}` || !tools[1].Done {
		t.Fatalf("codex tool not preserved: %+v", tools[1])
	}
}

type transcriptMessageSnapshot struct {
	Kind       messageKind
	Text       string
	TurnIndex  int
	ItemID     string
	ToolCallID string
	Tool       *toolInfoSnapshot
}

type toolInfoSnapshot struct {
	Name        string
	Description string
	Output      string
	Error       string
	Done        bool
}

func transcriptSnapshot(messages []chatMessage) []transcriptMessageSnapshot {
	out := make([]transcriptMessageSnapshot, 0, len(messages))
	for _, msg := range messages {
		snap := transcriptMessageSnapshot{
			Kind:       msg.Kind,
			Text:       msg.Text,
			TurnIndex:  msg.TurnIndex,
			ItemID:     msg.ItemID,
			ToolCallID: msg.ToolCallID,
		}
		if msg.Tool != nil {
			snap.Tool = &toolInfoSnapshot{
				Name:        msg.Tool.Name,
				Description: msg.Tool.Description,
				Output:      msg.Tool.Output,
				Error:       msg.Tool.Error,
				Done:        msg.Tool.Done,
			}
		}
		out = append(out, snap)
	}
	return out
}

func transcriptTools(messages []chatMessage) []toolCallInfo {
	var tools []toolCallInfo
	for _, msg := range messages {
		if msg.Kind == msgTool && msg.Tool != nil {
			tools = append(tools, *msg.Tool)
		}
	}
	return tools
}
