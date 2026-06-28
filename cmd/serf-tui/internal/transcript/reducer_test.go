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

func TestTranscriptReducerAppliesSerfJobNotificationsToDelegateTool(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "item_delegate",
		CallID:        "call_delegate",
		TurnID:        "turn_1",
		ToolName:      "delegate",
		ArgumentsJSON: `{"task":"inspect billing"}`,
		Output:        `{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`,
		Status:        appwire.TurnStatusCompleted,
	}, 1, true)

	reducer.ApplySerfJob(appwire.SerfJobInfo{
		JobID:            "job_A",
		JobType:          "delegate",
		Status:           "completed",
		OutputBytes:      42,
		TranscriptRef:    "local:child",
		DelegateID:       "dlg_A",
		Task:             "inspect billing",
		OriginToolCallID: "call_delegate",
		OriginItemID:     "item_delegate",
	})

	tools := transcriptTools(reducer.messages)
	if len(tools) != 1 || tools[0].Subagent == nil {
		t.Fatalf("tools=%+v, want one delegate tool with Subagent metadata", tools)
	}
	got := tools[0].Subagent
	if got.JobID != "job_A" || got.DelegateID != "dlg_A" || got.Status != "completed" || got.Task != "inspect billing" || got.TranscriptRef != "local:child" || got.OriginToolCallID != "call_delegate" || got.OutputBytes != 42 {
		t.Fatalf("Subagent = %+v", got)
	}
	if !tools[0].Done {
		t.Fatalf("delegate tool should be marked done after terminal job")
	}
}

func TestTranscriptReducerResumedDelegateJobWithSameOriginCreatesNewRun(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
	first := appwire.SerfJobInfo{
		JobID:            "job_first",
		JobType:          "delegate",
		Status:           "running",
		TranscriptRef:    "local:child-first",
		DelegateID:       "dlg_same",
		Task:             "inspect billing",
		OriginToolCallID: "call_delegate",
		OriginItemID:     "item_delegate",
	}
	reducer.ApplySerfJob(first)
	first.Status = "completed"
	reducer.ApplySerfJob(first)
	reducer.ApplySerfJob(appwire.SerfJobInfo{
		JobID:            "job_second",
		JobType:          "delegate",
		Status:           "running",
		TranscriptRef:    "local:child-second",
		DelegateID:       "dlg_same",
		Task:             "inspect billing",
		OriginToolCallID: "call_delegate",
		OriginItemID:     "item_delegate",
	})

	tools := transcriptTools(reducer.messages)
	if len(tools) != 2 {
		t.Fatalf("tools=%+v messages=%+v, want two delegate run rows", tools, reducer.messages)
	}
	if tools[0].Subagent == nil || tools[0].Subagent.JobID != "job_first" || tools[0].Subagent.Status != "completed" || !tools[0].Done {
		t.Fatalf("first run overwritten or not completed: %+v", tools[0])
	}
	if tools[1].Subagent == nil || tools[1].Subagent.JobID != "job_second" || tools[1].Subagent.Status != "running" || tools[1].Done {
		t.Fatalf("second run missing or not running: %+v", tools[1])
	}
}

func TestTranscriptReducerIgnoresNonDelegateSerfJobNotification(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)

	reducer.ApplySerfJob(appwire.SerfJobInfo{
		JobID:       "job_shell",
		JobType:     "shell",
		Status:      "completed",
		OutputBytes: 128,
	})

	if len(reducer.messages) != 0 {
		t.Fatalf("non-delegate job should not create transcript messages: %+v", reducer.messages)
	}
}

func TestTranscriptReducerTracksBackgroundShellJob(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
	// A foreground shell is still ignored.
	reducer.ApplySerfJob(appwire.SerfJobInfo{JobID: "job_fg", JobType: "shell", Status: "running", Command: "ls"})
	if len(reducer.messages) != 0 {
		t.Fatalf("a foreground shell must not be tracked: %+v", reducer.messages)
	}
	// A long-lived (background) shell IS tracked, named by its command.
	reducer.ApplySerfJob(appwire.SerfJobInfo{JobID: "job_bg", JobType: "shell", Background: true, Command: "go test ./... -count=1", Status: "running"})
	tools := transcriptTools(reducer.messages)
	if len(tools) != 1 || tools[0].Subagent == nil || !tools[0].Subagent.Background || tools[0].Subagent.JobID != "job_bg" {
		t.Fatalf("background shell run not captured: %+v", reducer.messages)
	}
	if tools[0].Description == "" || tools[0].Done {
		t.Fatalf("background shell should be named by its command and still running: %+v", tools[0])
	}
	// Finishing it reconciles the SAME entry (no duplicate) and marks it done.
	reducer.ApplySerfJob(appwire.SerfJobInfo{JobID: "job_bg", JobType: "shell", Background: true, Command: "go test ./... -count=1", Status: "completed", OutputBytes: 42})
	tools = transcriptTools(reducer.messages)
	if len(tools) != 1 {
		t.Fatalf("finishing a background shell must reconcile, not duplicate: %+v", reducer.messages)
	}
	if !tools[0].Done || tools[0].Subagent.Status != "completed" {
		t.Fatalf("background shell should be marked done on finish: %+v", tools[0])
	}
}

func TestTranscriptReducerDoesNotAttachNonDelegateJobToOriginTool(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
	reducer.ApplyThreadItem(appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "item_shell",
		CallID:        "call_shell",
		TurnID:        "turn_1",
		ToolName:      "exec_command",
		ArgumentsJSON: `{"command":"echo hi"}`,
		Output:        "hi\n",
		Status:        appwire.TurnStatusCompleted,
	}, 1, true)

	reducer.ApplySerfJob(appwire.SerfJobInfo{
		JobID:            "job_shell",
		JobType:          "shell",
		Status:           "completed",
		OutputBytes:      3,
		OriginToolCallID: "call_shell",
		OriginItemID:     "item_shell",
	})

	tools := transcriptTools(reducer.messages)
	if len(tools) != 1 {
		t.Fatalf("tools=%+v, want existing shell tool only", tools)
	}
	if tools[0].Name != "exec_command" {
		t.Fatalf("tool name = %q, want exec_command", tools[0].Name)
	}
	if tools[0].Subagent != nil {
		t.Fatalf("non-delegate job mutated shell tool with Subagent metadata: %+v", tools[0].Subagent)
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

func TestApplyChildActivityTracksRunningRunHonestly(t *testing.T) {
	reducer := NewTranscriptReducer(nil, nil, nil)
	reducer.ApplySerfJob(appwire.SerfJobInfo{JobID: "job_a", JobType: "delegate", Status: "running", TranscriptRef: "local:c1", Task: "port webhook"})

	if !reducer.ApplyChildActivity("local:c1", "shell: go test ./...") {
		t.Fatalf("activity for a watched running child should apply")
	}
	tools := transcriptTools(reducer.messages)
	if tools[0].Subagent.Activity != "shell: go test ./..." || tools[0].Subagent.Steps != 1 {
		t.Fatalf("first activity not recorded: %+v", tools[0].Subagent)
	}
	// New activity advances the step count.
	reducer.ApplyChildActivity("local:c1", "read_file: auth/session.go")
	if tools[0].Subagent.Steps != 2 {
		t.Fatalf("new activity should advance steps to 2: %+v", tools[0].Subagent)
	}
	// An identical repeat does NOT advance (honest: a stalled child's count freezes).
	reducer.ApplyChildActivity("local:c1", "read_file: auth/session.go")
	if tools[0].Subagent.Steps != 2 {
		t.Fatalf("identical activity must not advance steps: %+v", tools[0].Subagent)
	}
	// A frame for an unwatched ref does not apply.
	if reducer.ApplyChildActivity("local:other", "grep") {
		t.Fatalf("activity for an unknown ref must not apply")
	}
	// Once terminal, activity no longer applies.
	reducer.ApplySerfJob(appwire.SerfJobInfo{JobID: "job_a", JobType: "delegate", Status: "completed", TranscriptRef: "local:c1"})
	if reducer.ApplyChildActivity("local:c1", "late frame") {
		t.Fatalf("activity must not apply to a finished run")
	}
}

func TestTranscriptReducerGetters(t *testing.T) {
	msgs := []ChatMessage{{Kind: MsgUser, Text: "hello"}}
	activeTools := map[string]int{"tool1": 7}
	activeMessages := map[string]int{"msg1": 3}
	r := NewTranscriptReducer(msgs, activeTools, activeMessages)

	if got := r.Messages(); len(got) != 1 || got[0].Text != "hello" {
		t.Errorf("Messages() = %+v", got)
	}
	if got := r.ActiveTools(); len(got) != 1 || got["tool1"] != 7 {
		t.Errorf("ActiveTools() = %+v", got)
	}
	if got := r.ActiveMessages(); len(got) != 1 || got["msg1"] != 3 {
		t.Errorf("ActiveMessages() = %+v", got)
	}

	// The getters expose the live fields, not defensive copies: a mutation
	// through the returned map must be visible on a subsequent call.
	r.ActiveTools()["tool2"] = 9
	if got := r.ActiveTools(); len(got) != 2 || got["tool2"] != 9 {
		t.Errorf("ActiveTools() did not return the live field: %+v", got)
	}
	r.ActiveMessages()["msg2"] = 4
	if got := r.ActiveMessages(); len(got) != 2 || got["msg2"] != 4 {
		t.Errorf("ActiveMessages() did not return the live field: %+v", got)
	}
}

func TestRemoveUserMessageEcho(t *testing.T) {
	tests := []struct {
		name    string
		initial []ChatMessage
		text    string
		wantLen int
	}{
		{
			name:    "removes matching echo",
			initial: []ChatMessage{{Kind: MsgUser, Text: "hello"}},
			text:    "hello",
			wantLen: 0,
		},
		{
			name:    "ignores non-matching echo",
			initial: []ChatMessage{{Kind: MsgUser, Text: "hello"}},
			text:    "world",
			wantLen: 1,
		},
		{
			name:    "ignores empty text",
			initial: []ChatMessage{{Kind: MsgUser, Text: "hello"}},
			text:    "",
			wantLen: 1,
		},
		{
			name:    "skips pending messages",
			initial: []ChatMessage{{Kind: MsgUser, Text: "hello", Pending: true}},
			text:    "hello",
			wantLen: 1,
		},
		{
			name:    "skips failed messages",
			initial: []ChatMessage{{Kind: MsgUser, Text: "hello", Failed: true, PendingID: 1}},
			text:    "hello",
			wantLen: 1,
		},
		{
			name:    "skips messages with itemID",
			initial: []ChatMessage{{Kind: MsgUser, Text: "hello", ItemID: "item1"}},
			text:    "hello",
			wantLen: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewTranscriptReducer(tc.initial, nil, nil)
			r.RemoveUserMessageEcho(tc.text)
			if len(r.messages) != tc.wantLen {
				t.Errorf("messages len=%d, want %d", len(r.messages), tc.wantLen)
			}
		})
	}
}

func TestAppendPendingMessages(t *testing.T) {
	// Reset the global counter so IDs are deterministic.
	pendingMessageIDCounter = 0

	r := NewTranscriptReducer(nil, nil, nil)

	id1 := r.AppendPendingSteering("steer me")
	if len(r.messages) != 1 || r.messages[0].Kind != MsgSteering || r.messages[0].Text != "steer me" || !r.messages[0].Pending || r.messages[0].PendingID != 1 {
		t.Fatalf("AppendPendingSteering = %+v", r.messages[0])
	}

	id2 := r.AppendPendingUser("user input")
	if len(r.messages) != 2 || r.messages[1].Kind != MsgUser || r.messages[1].Text != "user input" || !r.messages[1].Pending || r.messages[1].PendingID != 2 {
		t.Fatalf("AppendPendingUser = %+v", r.messages[1])
	}

	id3 := r.AppendPendingDrain(3)
	if len(r.messages) != 3 || r.messages[2].Kind != MsgSteering || r.messages[2].Text != "draining 3 → steering" || !r.messages[2].Pending || r.messages[2].PendingID != 3 {
		t.Fatalf("AppendPendingDrain = %+v", r.messages[2])
	}

	if id1 != 1 || id2 != 2 || id3 != 3 {
		t.Fatalf("Pending IDs = %d, %d, %d, want 1, 2, 3", id1, id2, id3)
	}
}

func TestMarkPendingFailed(t *testing.T) {
	pendingMessageIDCounter = 0
	r := NewTranscriptReducer(nil, nil, nil)
	r.AppendPendingSteering("steer me")

	r.MarkPendingFailed(1, "timeout")
	if r.messages[0].Pending {
		t.Errorf("Pending should be false after MarkPendingFailed")
	}
	if !r.messages[0].Failed {
		t.Errorf("Failed should be true after MarkPendingFailed")
	}
	if r.messages[0].Reason != "timeout" {
		t.Errorf("Reason = %q, want timeout", r.messages[0].Reason)
	}

	// Marking unknown ID is a no-op.
	r.MarkPendingFailed(99, "nope")
	if r.messages[0].Reason != "timeout" {
		t.Errorf("Reason mutated by unknown ID: %q", r.messages[0].Reason)
	}
}

func TestRemovePending(t *testing.T) {
	pendingMessageIDCounter = 0
	r := NewTranscriptReducer(nil, nil, nil)
	r.AppendPendingSteering("first")
	r.AppendPendingSteering("second")

	r.RemovePending(1)
	if len(r.messages) != 1 || r.messages[0].Text != "second" {
		t.Fatalf("after removing first: %+v", r.messages)
	}

	// Removing unknown ID is a no-op.
	r.RemovePending(99)
	if len(r.messages) != 1 {
		t.Fatalf("after removing unknown: %+v", r.messages)
	}
}
