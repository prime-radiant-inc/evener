package apptranscript

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestPreludeTurnRendersFullToolsOnly(t *testing.T) {
	strict := true
	turn := PreludeTurn(transcript.Header{SystemPrompt: "You are Serf."}, &transcript.APICall{
		Request: llm.APILogRequest{
			ToolCount: 1,
			ToolNames: []string{"read_file"},
			Tools: []llm.ToolDefinition{{
				Name:        "read_file",
				Description: "Read a file from disk.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []any{"path"},
				},
				Strict: &strict,
			}},
		},
	})

	if turn == nil || len(turn.Items) != 2 {
		t.Fatalf("prelude=%+v", turn)
	}
	tools := turn.Items[1]
	if tools.Type != "systemMessage" || tools.Description != "Tools (1)" {
		t.Fatalf("tools item=%+v", tools)
	}
	for _, want := range []string{"```json", `"name": "read_file"`, `"description": "Read a file from disk."`, `"parameters"`, `"strict": true`} {
		if !strings.Contains(tools.Text, want) {
			t.Fatalf("tools text missing %q:\n%s", want, tools.Text)
		}
	}
	if strings.Contains(tools.Text, "- read_file") {
		t.Fatalf("tools text should not render legacy name list:\n%s", tools.Text)
	}
}

func TestPreludeTurnDoesNotRenderToolNamesWithoutDefinitions(t *testing.T) {
	turn := PreludeTurn(transcript.Header{SystemPrompt: "You are Serf."}, &transcript.APICall{
		Request: llm.APILogRequest{
			ToolCount: 2,
			ToolNames: []string{"read_file", "apply_patch"},
		},
	})

	if turn == nil || len(turn.Items) != 1 {
		t.Fatalf("prelude=%+v, want only system prompt", turn)
	}
	if turn.Items[0].Description != "System prompt" {
		t.Fatalf("item=%+v", turn.Items[0])
	}
}

func TestTurnsFromFileProjectsPreludeAndAPICallError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID:    "th_1",
		SystemPrompt: "You are Serf.",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.AppendAPICall(transcript.APICall{
		Request: llm.APILogRequest{
			Tools: []llm.ToolDefinition{{Name: "read_file", Description: "Read a file."}},
		},
		Error:  "provider failed",
		Source: "provider",
		Title:  "Provider error",
	}); err != nil {
		t.Fatalf("AppendAPICall: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	turns := TurnsFromFile(path, 128<<20, nil)
	if len(turns) != 2 {
		t.Fatalf("turns=%+v", turns)
	}
	if turns[0].ID != "turn_system" || len(turns[0].Items) != 2 {
		t.Fatalf("prelude=%+v", turns[0])
	}
	if turns[1].Status != appwire.TurnStatusFailed || turns[1].Error == nil || turns[1].Error.Title != "Provider error" {
		t.Fatalf("error turn=%+v", turns[1])
	}
}

func TestSharedTranscriptHelpers(t *testing.T) {
	if got := CompactionDescription("SUMMARY"); got != "Context summary" {
		t.Fatalf("summary description=%q", got)
	}
	if got := ImagePlaceholder(2); got != "[2 images]" {
		t.Fatalf("image placeholder=%q", got)
	}
	if got := CommunicateMessageFromArguments([]byte(`{"output":{"message":"nested"}}`)); got != "nested" {
		t.Fatalf("communicate message=%q", got)
	}
	if got := ToolIntentFromArguments([]byte(`{"purpose":"inspect file"}`)); got != "inspect file" {
		t.Fatalf("tool intent=%q", got)
	}
	if got := StringifyToolContent(map[string]any{"ok": true}); !strings.Contains(got, `"ok":true`) {
		t.Fatalf("tool content=%q", got)
	}
}

func TestProjectTurnMapsToolCallsAndResults(t *testing.T) {
	toolNames := map[string]string{}
	start := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_read",
				Name:      "read_file",
				Arguments: []byte(`{"path":"README.md","purpose":"inspect docs"}`),
			},
		}}},
	}, toolNames, nil)

	if len(start) != 1 || start[0].Type != "commandExecution" || start[0].Description != "inspect docs" {
		t.Fatalf("tool start=%+v", start)
	}
	done := ProjectTurn("turn_2", 2, schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_read",
				Content:    "ok",
				ToolState:  json.RawMessage(`{"job_id":"job_1"}`),
			},
		}}},
	}, toolNames, nil)

	if len(done) != 1 || done[0].ToolName != "read_file" || done[0].Output != "ok" {
		t.Fatalf("tool result=%+v", done)
	}
	if string(done[0].Raw) != `{"job_id":"job_1"}` {
		t.Fatalf("tool result Raw = %s, want tool_state", done[0].Raw)
	}
}

func TestProjectTurnPreservesDelegateToolStateForColdReconciliation(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_delegate",
				Name:       "delegate",
				Content:    `{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`,
				ToolState:  json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`),
			},
		}}},
	}

	items := ProjectTurn("turn_1", 1, turn, map[string]string{"call_delegate": "delegate"}, nil)
	if len(items) != 1 || items[0].Type != "commandExecution" || items[0].CallID != "call_delegate" {
		t.Fatalf("items=%+v", items)
	}
	if !strings.Contains(string(items[0].Raw), `"delegate_id":"dlg_A"`) || !strings.Contains(string(items[0].Raw), `"transcript_ref":"local:child"`) {
		t.Fatalf("delegate raw = %s", items[0].Raw)
	}
}

func TestProjectTurnMapsThinkingContent(t *testing.T) {
	toolNames := map[string]string{}
	items := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "Let me plan this out."}},
			{Kind: llm.ContentText, Text: "The answer is 42."},
		}},
	}, toolNames, nil)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me plan this out." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
	if items[1].Type != "agentMessage" || items[1].Text != "The answer is 42." {
		t.Fatalf("agent message item=%+v", items[1])
	}
}

func TestProjectTurnMapsRedactedThinkingContent(t *testing.T) {
	toolNames := map[string]string{}
	items := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Redacted: true}},
			{Kind: llm.ContentText, Text: "The answer is 42."},
		}},
	}, toolNames, nil)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "[redacted thinking]" {
		t.Fatalf("redacted reasoning item=%+v", items[0])
	}
	if items[1].Type != "agentMessage" || items[1].Text != "The answer is 42." {
		t.Fatalf("agent message item=%+v", items[1])
	}
}

func TestProjectTurnProjectsWebSearchCall(t *testing.T) {
	items := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{
				Query: "go context cancellation",
				Raw:   json.RawMessage(`{"type":"web_search_call","status":"completed"}`),
			}},
		}},
	}, map[string]string{}, nil)
	if len(items) != 1 {
		t.Fatalf("expected 1 web_search item, got %+v", items)
	}
	got := items[0]
	if got.Type != "commandExecution" || got.ToolName != "web_search" {
		t.Fatalf("web_search item=%+v", got)
	}
	if !strings.Contains(got.ArgumentsJSON, "go context cancellation") {
		t.Fatalf("args missing query: %s", got.ArgumentsJSON)
	}
	if got.Status != appwire.TurnStatusCompleted {
		t.Fatalf("status=%q", got.Status)
	}
}

func TestProjectTurnProjectsWebSearchResultsFromRaw(t *testing.T) {
	anthropic := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(
				`{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://go.dev/blog/err","title":"Error Handling"},{"type":"web_search_result","url":"https://go.dev/ctx","title":"Context"}]}`)}},
		}},
	}, map[string]string{}, nil)
	if len(anthropic) != 1 || anthropic[0].Output == "" {
		t.Fatalf("anthropic web_search results=%+v", anthropic)
	}
	for _, want := range []string{"Error Handling", "https://go.dev/blog/err", "Context"} {
		if !strings.Contains(anthropic[0].Output, want) {
			t.Fatalf("output missing %q: %q", want, anthropic[0].Output)
		}
	}

	gemini := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(
				`{"webSearchQueries":["golang generics"],"groundingChunks":[{"web":{"uri":"https://go.dev/generics","title":"Generics"}}]}`)}},
		}},
	}, map[string]string{}, nil)
	if len(gemini) != 1 {
		t.Fatalf("gemini web_search=%+v", gemini)
	}
	if !strings.Contains(gemini[0].ArgumentsJSON, "golang generics") {
		t.Fatalf("gemini query missing: %s", gemini[0].ArgumentsJSON)
	}
	if !strings.Contains(gemini[0].Output, "Generics") || !strings.Contains(gemini[0].Output, "https://go.dev/generics") {
		t.Fatalf("gemini output=%q", gemini[0].Output)
	}
}

func TestProjectTurnProjectsAudioAndDocumentAttachments(t *testing.T) {
	items := ProjectTurn("turn_1", 1, schema.Turn{
		Kind: schema.TurnUserInput,
		Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "summarize these"},
			{Kind: llm.ContentDocument, Document: &llm.DocumentData{FileName: "report.pdf", MediaType: "application/pdf"}},
			{Kind: llm.ContentAudio, Audio: &llm.AudioData{MediaType: "audio/wav"}},
		}},
	}, map[string]string{}, nil)
	if len(items) != 1 || items[0].Type != "userMessage" {
		t.Fatalf("expected 1 userMessage, got %+v", items)
	}
	atts := items[0].Images
	if len(atts) != 2 {
		t.Fatalf("expected 2 attachments, got %+v", atts)
	}
	if atts[0].Type != "input_document" || atts[0].Name != "report.pdf" || atts[0].MediaType != "application/pdf" {
		t.Fatalf("document attachment=%+v", atts[0])
	}
	if atts[1].Type != "input_audio" || atts[1].MediaType != "audio/wav" {
		t.Fatalf("audio attachment=%+v", atts[1])
	}
}
