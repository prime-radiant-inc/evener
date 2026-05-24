package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
)

func TestHistoryToMessages_UserAndCommunicate(t *testing.T) {
	turns := []agent.Turn{
		{Kind: agent.TurnUserInput, Message: llm.User("what is 2+2?")},
		{Kind: agent.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "communicate",
					Arguments: json.RawMessage(`{"message":"The answer is 4."}`),
				}},
			},
		}},
		{Kind: agent.TurnToolResults, Message: llm.ToolResult("call_1", "ok", false)},
	}

	msgs := historyToMessages(turns)

	// Should have: user message + communicate message.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != msgUser || msgs[0].Text != "what is 2+2?" {
		t.Errorf("msg[0] = %+v, want user 'what is 2+2?'", msgs[0])
	}
	if msgs[1].Kind != msgCommunicate || msgs[1].Text != "The answer is 4." {
		t.Errorf("msg[1] = %+v, want communicate 'The answer is 4.'", msgs[1])
	}
}

func TestHistoryToMessages_ToolCalls(t *testing.T) {
	turns := []agent.Turn{
		{Kind: agent.TurnUserInput, Message: llm.User("list files")},
		{Kind: agent.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"ls -la"}`),
				}},
			},
		}},
		{Kind: agent.TurnToolResults, Message: llm.ToolResult("call_1", "file1.go\nfile2.go", false)},
		{Kind: agent.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_2",
					Name:      "communicate",
					Arguments: json.RawMessage(`{"message":"Found 2 files."}`),
				}},
			},
		}},
		{Kind: agent.TurnToolResults, Message: llm.ToolResult("call_2", "ok", false)},
	}

	msgs := historyToMessages(turns)

	// Should have: user + tool + communicate.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != msgUser {
		t.Errorf("msg[0].Kind = %v, want msgUser", msgs[0].Kind)
	}
	if msgs[1].Kind != msgTool || msgs[1].Tool == nil {
		t.Fatalf("msg[1] should be a tool call, got %+v", msgs[1])
	}
	if msgs[1].Tool.Name != "shell" {
		t.Errorf("tool name = %q, want 'shell'", msgs[1].Tool.Name)
	}
	if msgs[1].Tool.Output != "file1.go\nfile2.go" {
		t.Errorf("tool output = %q, want file listing", msgs[1].Tool.Output)
	}
	if msgs[2].Kind != msgCommunicate || msgs[2].Text != "Found 2 files." {
		t.Errorf("msg[2] = %+v, want communicate 'Found 2 files.'", msgs[2])
	}
}

func TestHistoryToMessages_ThinkingText(t *testing.T) {
	turns := []agent.Turn{
		{Kind: agent.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Let me think about this..."},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "communicate",
					Arguments: json.RawMessage(`{"message":"Done."}`),
				}},
			},
		}},
	}

	msgs := historyToMessages(turns)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != msgAssistant || msgs[0].Text != "Let me think about this..." {
		t.Errorf("msg[0] = %+v, want assistant thinking", msgs[0])
	}
	if msgs[1].Kind != msgCommunicate {
		t.Errorf("msg[1].Kind = %v, want msgCommunicate", msgs[1].Kind)
	}
}

func TestHistoryToMessages_Empty(t *testing.T) {
	msgs := historyToMessages(nil)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages for nil history, got %d", len(msgs))
	}
}

func TestRenderMessage_RendersAssistantMarkdown(t *testing.T) {
	previous := markdownRenderer
	previousWidth := markdownRendererWidth
	markdownRenderer = nil
	t.Cleanup(func() {
		markdownRenderer = previous
		markdownRendererWidth = previousWidth
	})

	got := renderMessage(chatMessage{Kind: msgAssistant, Text: "**bold**\n\n- one"}, 80, false)

	if strings.Contains(got, "**bold**") || strings.Contains(got, "- one") {
		t.Fatalf("assistant markdown rendered raw:\n%q", got)
	}
	if !strings.Contains(got, "bold") || !strings.Contains(got, "one") {
		t.Fatalf("assistant markdown lost content:\n%q", got)
	}
}

func TestRenderMessage_KeepsPlainAssistantTextSearchable(t *testing.T) {
	got := renderMessage(chatMessage{Kind: msgAssistant, Text: "main transcript answer"}, 80, false)

	if !strings.Contains(got, "main transcript answer") {
		t.Fatalf("plain assistant text should remain contiguous:\n%q", got)
	}
}

func TestRenderToolCallUsesRegistry(t *testing.T) {
	tc := toolCallInfo{
		Name:        "read_file",
		Description: `{"file_path":"src/x.go"}`,
		Output:      "line1\nline2\nline3",
		Duration:    50 * time.Millisecond,
		Done:        true,
	}
	got := renderToolCall(tc, 100, false)
	if !strings.Contains(got, "read") {
		t.Errorf("output should include verb 'read': %q", got)
	}
	if !strings.Contains(got, "src/x.go") {
		t.Errorf("output should include target: %q", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("output should include result: %q", got)
	}
}
