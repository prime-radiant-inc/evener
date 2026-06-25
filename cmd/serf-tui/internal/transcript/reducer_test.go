package transcript

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestReducerGroupedUseSkillActivationDoesNotCreateSystemDuplicate(t *testing.T) {
	thread := appwire.Thread{Turns: []appwire.Turn{{
		ID:     "turn_1",
		Status: appwire.TurnStatusCompleted,
		Items: []appwire.ThreadItem{{
			Type:          "commandExecution",
			ID:            "tool_1",
			TurnID:        "turn_1",
			ToolName:      "use_skill",
			CallID:        "call_skill",
			ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
			Output:        "Skill loaded",
			Status:        appwire.TurnStatusCompleted,
			Raw:           json.RawMessage(`{"skill_activation":{"name":"superpowers:using-superpowers","text":"Activated skill: superpowers:using-superpowers"}}`),
		}},
	}}}

	messages := MessagesFromThread(thread)
	if len(messages) != 1 {
		t.Fatalf("messages len=%d, want 1: %+v", len(messages), messages)
	}
	if messages[0].Kind != MsgTool || messages[0].Tool == nil || messages[0].Tool.Name != "use_skill" {
		t.Fatalf("message should be one use_skill tool: %+v", messages[0])
	}
	if messages[0].Tool.Raw == "" {
		t.Fatalf("grouped raw metadata should be carried for TUI renderers")
	}
}

func TestHubTranscriptReducerReconcilesUserEchoWithReplay(t *testing.T) {
	reducer := NewTranscriptReducer([]ChatMessage{{Kind: MsgUser, Text: "please inspect this"}}, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "user_1",
		TurnID: "turn_7",
		Text:   "please inspect this",
	}, 7, false)

	if len(reducer.messages) != 1 {
		t.Fatalf("expected replayed user message to reconcile with echo, got %+v", reducer.messages)
	}
	if reducer.messages[0].Kind != MsgUser || reducer.messages[0].Text != "please inspect this" || reducer.messages[0].TurnIndex != 7 || reducer.messages[0].ItemID != "user_1" {
		t.Fatalf("reconciled user message = %+v", reducer.messages[0])
	}
}

func TestHubTranscriptReducerKeepsImageOnlyUserMessage(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
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
	if reducer.messages[0].Kind != MsgUser || reducer.messages[0].Text != "[image]" || reducer.messages[0].TurnIndex != 3 {
		t.Fatalf("image-only user message = %+v", reducer.messages[0])
	}
}

func TestHubTranscriptReducerRendersSystemMessage(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
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
	if msg.Kind != MsgSystem || msg.Text != "System prompt\nYou are Serf." || msg.ItemID != "item_system_prompt" {
		t.Fatalf("system message = %+v", msg)
	}
}

func TestHubTranscriptReducerRendersHookSystemMessageAsOneLine(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          "item_hook_1",
		TurnID:      "turn_1",
		Description: "Hook",
		Text:        "SessionStart hook superpowers using-superpowers command exit 0",
	}, 1, true)

	if len(reducer.messages) != 1 {
		t.Fatalf("messages len=%d, want 1", len(reducer.messages))
	}
	msg := reducer.messages[0]
	if msg.Kind != MsgSystem || msg.Text != "SessionStart hook superpowers using-superpowers command exit 0" || strings.Contains(msg.Text, "\n") {
		t.Fatalf("hook system message = %+v", msg)
	}
}

func TestHubTranscriptReducerSuppressesReplayedCompletedTool(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
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

	reducer.ApplyThreadItem(started, 1, false)
	reducer.ApplyThreadItem(completed, 1, true)
	reducer.ApplyThreadItem(completed, 1, false)

	var tools []ChatMessage
	for _, msg := range reducer.messages {
		if msg.Kind == MsgTool {
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
	reducer := NewTranscriptReducer(nil, nil, nil)
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

	reducer.ApplyThreadItem(started, 1, false)
	reducer.ApplyThreadItem(completed, 2, true)

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
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "item_reused",
		TurnID: "turn_1",
		Text:   "first",
	}, 1, true)
	reducer.ApplyThreadItem(appwire.ThreadItem{
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
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "assistant_reused",
		TurnID: "turn_codex",
		Text:   "first",
	}, 0, true)
	reducer.ApplyThreadItem(appwire.ThreadItem{
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
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyAgentMessageDelta("turn_codex", "assistant_reused", "first")
	reducer.ApplyAgentMessageDelta("turn_stream", "assistant_reused", "second")

	if len(reducer.messages) != 2 {
		t.Fatalf("messages=%+v, want separate delta-created assistant entries", reducer.messages)
	}
	if reducer.messages[0].Text != "first" || reducer.messages[0].TurnID != "turn_codex" || reducer.messages[1].Text != "second" || reducer.messages[1].TurnID != "turn_stream" {
		t.Fatalf("messages=%+v, want turn-scoped assistant deltas", reducer.messages)
	}
}

func TestHubTranscriptReducerResetDiscardsInProgressAssistant(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
	reducer.ApplyAgentMessageDelta("turn_1", "assistant_1", "partial reply")
	if len(reducer.messages) != 1 {
		t.Fatalf("expected the in-progress assistant message, got %+v", reducer.messages)
	}

	reducer.ResetAgentMessage("turn_1", "assistant_1")
	if len(reducer.messages) != 0 {
		t.Fatalf("reset should discard the in-progress assistant message, got %+v", reducer.messages)
	}

	// The retry streams onto a fresh item; the discarded partial does not return.
	reducer.ApplyAgentMessageDelta("turn_1", "assistant_2", "final reply")
	if len(reducer.messages) != 1 || reducer.messages[0].Text != "final reply" || reducer.messages[0].ItemID != "assistant_2" {
		t.Fatalf("post-reset messages=%+v", reducer.messages)
	}

	// Resetting an unknown item is a no-op.
	reducer.ResetAgentMessage("turn_1", "nope")
	if len(reducer.messages) != 1 {
		t.Fatalf("reset of unknown item mutated messages: %+v", reducer.messages)
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

	live := NewTranscriptReducer(nil, nil, nil)
	live.ApplyUserMessageEcho(user.Text)
	live.ApplyThreadItem(user, 1, false)
	live.ApplyAgentMessageDelta("turn_1", "agent_1", "Thinking **bo")
	live.ApplyAgentMessageDelta("turn_1", "agent_1", "ld**")
	live.ApplyThreadItem(toolStart, 1, false)
	live.ApplyToolOutputDelta("tool_1", "alpha\n")
	live.ApplyThreadItem(toolDone, 1, true)
	live.ApplyThreadItem(assistantDone, 1, true)

	replay := NewTranscriptReducer(nil, nil, nil)
	for _, item := range []appwire.ThreadItem{user, {Type: "agentMessage", ID: "agent_1", TurnID: "turn_1", Text: "Thinking **bold**\nDone.", Status: "completed"}, toolDone} {
		replay.ApplyThreadItem(item, 1, item.Status == "completed")
	}

	if got, want := transcriptSnapshot(live.messages), transcriptSnapshot(replay.messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("live transcript != replay transcript\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestHubTranscriptReducerToolOutputDeltaBeforeStartStaysInOneToolGroup(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyToolOutputDelta("tool_late", "first\n")
	reducer.ApplyThreadItem(appwire.ThreadItem{
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
	if msg.Kind != MsgTool || msg.ItemID != "tool_late" || msg.ToolCallID != "call_late" || msg.Tool == nil {
		t.Fatalf("tool message = %+v", msg)
	}
	if msg.Tool.Name != "shell" || msg.Tool.Output != "first\n" || !msg.Tool.Done {
		t.Fatalf("tool info = %+v", msg.Tool)
	}
}

func TestHubTranscriptReducerPreservesSerfAndCodexToolShapes(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "serf_tool",
		CallID:        "serf_call",
		TurnID:        "turn_1",
		ToolName:      "read_file",
		ArgumentsJSON: `{"file_path":"/tmp/missing.go"}`,
		Error:         "open /tmp/missing.go: no such file or directory",
		Status:        "completed",
	}, 1, true)
	reducer.ApplyThreadItem(appwire.ThreadItem{
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

func TestHubTranscriptReducerStreamsReasoningThenCollapses(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	start := appwire.ThreadItem{Type: "reasoning", ID: "reasoning_1", TurnID: "turn_1", Status: appwire.TurnStatusInProgress}
	r.ApplyThreadItem(start, 1, false)
	r.ApplyReasoningSummaryDelta("turn_1", "reasoning_1", "weighing ")
	r.ApplyReasoningSummaryDelta("turn_1", "reasoning_1", "the options")

	if len(r.messages) != 1 {
		t.Fatalf("want one reasoning message, got %+v", r.messages)
	}
	if got := r.messages[0]; got.Kind != MsgReasoning || got.Text != "weighing the options" {
		t.Fatalf("reasoning message = %+v", got)
	}
	if r.messages[0].Done {
		t.Fatalf("reasoning must stay live while it is the current turn: %+v", r.messages[0])
	}

	// The assistant starting its answer collapses the thought, preserving its text.
	r.ApplyAgentMessageDelta("turn_1", "agent_1", "Here is the answer")
	if !r.messages[0].Done {
		t.Fatalf("reasoning must collapse once the assistant answers: %+v", r.messages)
	}
	if r.messages[0].Text != "weighing the options" {
		t.Fatalf("collapsing must preserve the reasoning text: %+v", r.messages[0])
	}
	if got := r.messages[1]; got.Kind != MsgAssistant || got.Text != "Here is the answer" {
		t.Fatalf("assistant message = %+v", got)
	}
}

func TestHubTranscriptReducerReasoningSupersededAndTurnFinalized(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// A delta with no preceding item/started still opens the thought.
	r.ApplyReasoningSummaryDelta("turn_1", "reasoning_1", "first thought")
	r.ApplyReasoningSummaryDelta("turn_1", "reasoning_2", "second thought")

	if len(r.messages) != 2 {
		t.Fatalf("want two reasoning messages, got %+v", r.messages)
	}
	if !r.messages[0].Done {
		t.Fatalf("a new reasoning item must collapse the prior one: %+v", r.messages)
	}
	if r.messages[1].Done {
		t.Fatalf("the newest reasoning must stay live: %+v", r.messages)
	}

	// Turn completion collapses whatever thought is still live.
	r.FinalizeReasoning()
	if !r.messages[1].Done {
		t.Fatalf("turn completion must collapse the live reasoning: %+v", r.messages)
	}
}

type transcriptMessageSnapshot struct {
	Kind       MessageKind
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

func transcriptSnapshot(messages []ChatMessage) []transcriptMessageSnapshot {
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

func transcriptTools(messages []ChatMessage) []ToolCallInfo {
	var tools []ToolCallInfo
	for _, msg := range messages {
		if msg.Kind == MsgTool && msg.Tool != nil {
			tools = append(tools, *msg.Tool)
		}
	}
	return tools
}
