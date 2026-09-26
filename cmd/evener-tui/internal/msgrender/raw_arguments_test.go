package msgrender

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/llm"
)

// TestHistoryToMessages_RejectedToolCallShowsRawArguments mirrors the hub
// projection test TestRenderMarkdown_RejectedCallShowsRawArguments (#2204):
// when a non-communicate tool call's Arguments were not valid JSON, the
// durable transcript replaces them with the replay-safe {} placeholder and
// preserves the model's original bytes in RawArguments. The TUI must show
// the raw bytes the model actually sent (via SentArguments), not the {}
// placeholder — matching the hub's writeToolCardLine, which uses
// SentArguments for the input summary.
func TestHistoryToMessages_RejectedToolCallShowsRawArguments(t *testing.T) {
	t.Parallel()
	const rawArgs = `{command: "ls", }` // malformed JSON
	tc := &llm.ToolCallData{
		ID:           "call-rejected",
		Name:         "shell",
		Arguments:    json.RawMessage(`{}`),
		RawArguments: rawArgs,
	}
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: tc},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call-rejected", "arguments not valid JSON", true)},
	}
	msgs := historyToMessages(turns)

	var tool *transcript.ToolCallInfo
	for i := range msgs {
		if msgs[i].Kind == transcript.MsgTool && msgs[i].Tool != nil {
			tool = msgs[i].Tool
			break
		}
	}
	if tool == nil {
		t.Fatalf("expected a MsgTool for the rejected shell call, got: %+v", msgs)
	}
	if !strings.Contains(tool.RawArgs, rawArgs) {
		t.Errorf("expected the tool entry to carry the model's raw arguments %q, got RawArgs %q", rawArgs, tool.RawArgs)
	}
	if tool.RawArgs == "{}" {
		t.Errorf("the tool entry must not carry the {} placeholder as RawArgs for a rejected call, got %q", tool.RawArgs)
	}
}

// TestHistoryToMessages_RejectedCommunicateShowsRawArguments mirrors the hub
// projection test TestRenderMarkdown_ResultToolRejectedCallShowsRawArguments:
// a rejected communicate (result is "error") with malformed raw arguments must
// render the model's raw bytes as the communicate text, not the replay-safe {}
// placeholder or nothing. Before the raw-arguments migration the TUI parsed
// tc.Arguments (the {} placeholder), found no message, and silently dropped
// the call.
func TestHistoryToMessages_RejectedCommunicateShowsRawArguments(t *testing.T) {
	t.Parallel()
	const rawArgs = `{message: "done", }` // malformed JSON
	tc := &llm.ToolCallData{
		ID:           "call-rej-comm",
		Name:         "communicate",
		Arguments:    json.RawMessage(`{}`),
		RawArguments: rawArgs,
	}
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: tc},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call-rej-comm", "arguments not valid JSON", true)},
	}
	msgs := historyToMessages(turns)

	var comm *transcript.ChatMessage
	for i := range msgs {
		if msgs[i].Kind == transcript.MsgCommunicate {
			comm = &msgs[i]
			break
		}
	}
	if comm == nil {
		t.Fatalf("expected a MsgCommunicate for the rejected communicate call, got: %+v", msgs)
	}
	if !strings.Contains(comm.Text, rawArgs) {
		t.Errorf("expected the rejected communicate to show the model's raw arguments %q, got Text %q", rawArgs, comm.Text)
	}
	if comm.Text == "{}" {
		t.Errorf("the rejected communicate must not show the {} placeholder, got %q", comm.Text)
	}
}

// TestHistoryToMessages_HealedCommunicateShowsRepairedMessage mirrors the hub
// projection test TestRenderMarkdown_HealedCommunicateSuppressesRawArgs: a
// healed communicate (result is "ok") with malformed raw arguments must NOT
// show the raw malformed bytes. Instead it repairs them to recover the
// message that live delivery produced, matching the hub's
// writeResultToolMessage healed branch.
func TestHistoryToMessages_HealedCommunicateShowsRepairedMessage(t *testing.T) {
	t.Parallel()
	const rawArgs = `{message: "hello"}` // malformed JSON — bare key
	tc := &llm.ToolCallData{
		ID:           "call-healed-comm",
		Name:         "communicate",
		Arguments:    json.RawMessage(`{}`),
		RawArguments: rawArgs,
	}
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: tc},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResult("call-healed-comm", "ok", false)},
	}
	msgs := historyToMessages(turns)

	var comm *transcript.ChatMessage
	for i := range msgs {
		if msgs[i].Kind == transcript.MsgCommunicate {
			comm = &msgs[i]
			break
		}
	}
	if comm == nil {
		t.Fatalf("expected a MsgCommunicate for the healed communicate call, got: %+v", msgs)
	}
	if strings.Contains(comm.Text, rawArgs) {
		t.Errorf("healed communicate must not show raw malformed bytes, got Text %q", comm.Text)
	}
	if !strings.Contains(comm.Text, "hello") {
		t.Errorf("healed communicate must render the repaired message 'hello', got Text %q", comm.Text)
	}
}

// TestHistoryToMessages_PendingCommunicateShowsRawArguments verifies a pending
// communicate (no result turn yet) with malformed raw arguments renders the
// raw bytes, matching the hub's pending-call raw-arguments fallback. Before
// the migration the TUI dropped such a call entirely (no parseable message).
func TestHistoryToMessages_PendingCommunicateShowsRawArguments(t *testing.T) {
	t.Parallel()
	const rawArgs = `{message: "pending", }` // malformed JSON
	tc := &llm.ToolCallData{
		ID:           "call-pending-comm",
		Name:         "communicate",
		Arguments:    json.RawMessage(`{}`),
		RawArguments: rawArgs,
	}
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: tc},
			},
		}},
		// No TurnToolResults: the call is pending.
	}
	msgs := historyToMessages(turns)

	var comm *transcript.ChatMessage
	for i := range msgs {
		if msgs[i].Kind == transcript.MsgCommunicate {
			comm = &msgs[i]
			break
		}
	}
	if comm == nil {
		t.Fatalf("expected a MsgCommunicate for the pending communicate call, got: %+v", msgs)
	}
	if !strings.Contains(comm.Text, rawArgs) {
		t.Errorf("expected the pending communicate to show the model's raw arguments %q, got Text %q", rawArgs, comm.Text)
	}
}
