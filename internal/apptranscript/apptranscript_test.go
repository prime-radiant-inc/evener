package apptranscript

import (
	"encoding/json"
	"os"
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
	if turns[1].Error.Message != "provider failed" {
		t.Fatalf("error turn Message=%q, want %q", turns[1].Error.Message, "provider failed")
	}
	if turns[1].Error.Source != "provider" {
		t.Fatalf("error turn Source=%q, want %q", turns[1].Error.Source, "provider")
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

func TestDefaultImageProjector(t *testing.T) {
	img := llm.ImageData{MediaType: "image/png", Data: []byte("secret")}
	item := DefaultImageProjector(img)
	if item.Type != "input_image" || item.MediaType != "image/png" || item.Name != "" {
		t.Errorf("DefaultImageProjector = %+v", item)
	}
}

func TestCompactionDescription(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"SUMMARY", "Context summary"},
		{"summary", "Context summary"},
		{"  summary  ", "Context summary"},
		{"CHECKPOINT", "Context checkpoint"},
		{"checkpoint", "Context checkpoint"},
		{"unknown", "Context checkpoint"},
		{"", "Context checkpoint"},
	}
	for _, tc := range tests {
		got := CompactionDescription(tc.kind)
		if got != tc.want {
			t.Errorf("CompactionDescription(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestImagePlaceholder(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{0, ""},
		{1, "[image]"},
		{2, "[2 images]"},
		{5, "[5 images]"},
	}
	for _, tc := range tests {
		got := ImagePlaceholder(tc.count)
		if got != tc.want {
			t.Errorf("ImagePlaceholder(%d) = %q, want %q", tc.count, got, tc.want)
		}
	}
}

func TestCommunicateMessageFromArguments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"message", `{"message":"hello"}`, "hello"},
		{"output nested", `{"output":{"message":"nested"}}`, "nested"},
		{"message wins", `{"message":"direct","output":{"message":"nested"}}`, "direct"},
		{"empty object", `{}`, ""},
		{"invalid json", `not json`, ""},
		{"empty string", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CommunicateMessageFromArguments(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("CommunicateMessageFromArguments(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestToolIntentFromArguments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"intent", `{"intent":"do it"}`, "do it"},
		{"purpose", `{"purpose":"inspect"}`, "inspect"},
		{"description", `{"description":"read file"}`, "read file"},
		{"intent priority", `{"intent":"a","purpose":"b"}`, "a"},
		{"non-string ignored", `{"intent":123}`, ""},
		{"empty object", `{}`, ""},
		{"invalid json", `not json`, ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ToolIntentFromArguments(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("ToolIntentFromArguments(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestStringifyToolContent(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hello", "hello"},
		{"map", map[string]any{"ok": true}, `{"ok":true}`},
		{"int", 42, "42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StringifyToolContent(tc.v)
			if got != tc.want {
				t.Errorf("StringifyToolContent(%v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

func TestWebSearchProjection(t *testing.T) {
	tests := []struct {
		name        string
		ws          *llm.WebSearchData
		wantQuery   string
		wantResults string
	}{
		{
			name:        "nil",
			ws:          nil,
			wantQuery:   "",
			wantResults: "",
		},
		{
			name:        "query only",
			ws:          &llm.WebSearchData{Query: "go generics"},
			wantQuery:   "go generics",
			wantResults: "",
		},
		{
			name:        "anthropic content",
			ws:          &llm.WebSearchData{Raw: json.RawMessage(`{"content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"}]}`)},
			wantQuery:   "",
			wantResults: "Go — https://go.dev",
		},
		{
			name:        "gemini grounding",
			ws:          &llm.WebSearchData{Raw: json.RawMessage(`{"webSearchQueries":["golang"],"groundingChunks":[{"web":{"uri":"https://go.dev","title":"Go"}}]}`)},
			wantQuery:   "golang",
			wantResults: "Go — https://go.dev",
		},
		{
			name:        "openai action",
			ws:          &llm.WebSearchData{Raw: json.RawMessage(`{"action":{"query":"rust"}}`)},
			wantQuery:   "rust",
			wantResults: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, r := WebSearchProjection(tc.ws)
			if q != tc.wantQuery {
				t.Errorf("query = %q, want %q", q, tc.wantQuery)
			}
			if r != tc.wantResults {
				t.Errorf("results = %q, want %q", r, tc.wantResults)
			}
		})
	}
}

func TestWebSearchResultLine(t *testing.T) {
	tests := []struct {
		title string
		url   string
		want  string
	}{
		{"Go", "https://go.dev", "Go — https://go.dev"},
		{"Go", "", "Go"},
		{"", "https://go.dev", "https://go.dev"},
		{"", "", ""},
		{"  Go  ", "  https://go.dev  ", "Go — https://go.dev"},
	}
	for _, tc := range tests {
		got := webSearchResultLine(tc.title, tc.url)
		if got != tc.want {
			t.Errorf("webSearchResultLine(%q, %q) = %q, want %q", tc.title, tc.url, got, tc.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		values []string
		want   string
	}{
		{[]string{"a", "b"}, "a"},
		{[]string{"", "b", "c"}, "b"},
		{[]string{"", "", ""}, ""},
		{[]string{}, ""},
		{[]string{"  ", "x"}, "x"},
	}
	for _, tc := range tests {
		got := firstNonEmpty(tc.values...)
		if got != tc.want {
			t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.values, got, tc.want)
		}
	}
}

func TestFormatTools(t *testing.T) {
	// Empty tools returns empty.
	if got := FormatTools(llm.APILogRequest{}); got != "" {
		t.Errorf("FormatTools empty = %q, want empty", got)
	}
	// Tools present returns the markdown-fenced MarshalIndent of the tools,
	// including the closing fence.
	req := llm.APILogRequest{Tools: []llm.ToolDefinition{{Name: "read_file"}}}
	want := "```json\n[\n  {\n    \"name\": \"read_file\"\n  }\n]\n```"
	if got := FormatTools(req); got != want {
		t.Errorf("FormatTools = %q, want %q", got, want)
	}
}

func TestScanPrelude(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	// File with header then api_call.
	content := `{"kind":"header","system_prompt":"You are Serf."}
{"kind":"api_call","system_prompt":"Call prompt.","request":{"tools":[{"name":"read_file"}]}}
{"kind":"entry","turn":{}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	header, call := ScanPrelude(path, 1<<20)
	if strings.TrimSpace(header.SystemPrompt) != "You are Serf." {
		t.Errorf("header.SystemPrompt = %q", header.SystemPrompt)
	}
	if call == nil {
		t.Fatal("expected api_call")
	}
	// The api_call line must be fully parsed, not discarded.
	if call.SystemPrompt != "Call prompt." {
		t.Errorf("call.SystemPrompt = %q, want %q", call.SystemPrompt, "Call prompt.")
	}
	if len(call.Request.Tools) != 1 || call.Request.Tools[0].Name != "read_file" {
		t.Errorf("call.Request.Tools = %+v, want one read_file tool", call.Request.Tools)
	}

	// Missing file returns zero header and nil call.
	header, call = ScanPrelude(filepath.Join(dir, "missing"), 1<<20)
	if header.SystemPrompt != "" || call != nil {
		t.Errorf("missing file: header=%+v call=%+v", header, call)
	}
}

func TestPreludeTurn(t *testing.T) {
	// Empty header and no call returns nil.
	if got := PreludeTurn(transcript.Header{}, nil); got != nil {
		t.Errorf("empty prelude = %+v, want nil", got)
	}
	// Header with system prompt only.
	turn := PreludeTurn(transcript.Header{SystemPrompt: "You are Serf."}, nil)
	if turn == nil || len(turn.Items) != 1 || turn.Items[0].Text != "You are Serf." {
		t.Errorf("system prompt only = %+v", turn)
	}
	// Header with fallback system prompt from first call.
	turn = PreludeTurn(transcript.Header{}, &transcript.APICall{SystemPrompt: "Fallback."})
	if turn == nil || len(turn.Items) != 1 || turn.Items[0].Text != "Fallback." {
		t.Errorf("fallback system prompt = %+v", turn)
	}
}

func TestTurnsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"kind":"header","system_prompt":"You are Serf."}
{"kind":"entry","turn":{}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without projector, only prelude turns are emitted (since no projector returns items).
	turns := TurnsFromFile(path, 1<<20, nil)
	if len(turns) != 1 || turns[0].ID != "turn_system" {
		t.Errorf("turns = %+v", turns)
	}

	// With a projector that returns items.
	project := func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "userMessage", Text: "hello"}}
	}
	turns = TurnsFromFile(path, 1<<20, project)
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].ID != "turn_system" {
		t.Errorf("first turn = %+v", turns[0])
	}
	if turns[1].ID != "turn_1" || len(turns[1].Items) != 1 {
		t.Errorf("second turn = %+v", turns[1])
	}

	// Missing file returns nil.
	turns = TurnsFromFile(filepath.Join(dir, "missing"), 1<<20, nil)
	if turns != nil {
		t.Errorf("missing file turns = %+v", turns)
	}
}

// TestProjectTurnDedupsCommunicateEcho pins that an assistant turn emitting text
// AND a communicate tool_call carrying the SAME message renders the message once
// (mirroring the live projector's matchesLastAssistantMessage dedup), so a
// transcript reload doesn't show a duplicate. A communicate with DIFFERENT text
// is still rendered. (Surfaced by FuzzHubReplayLiveVsReload.)
func TestProjectTurnDedupsCommunicateEcho(t *testing.T) {
	mkTurn := func(text, communicate string) schema.Turn {
		return schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: text},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID:        "c1",
						Name:      "communicate",
						Arguments: json.RawMessage(`{"message":` + mustJSON(communicate) + `}`),
					}},
				},
			},
		}
	}

	agentMsgs := func(items []appwire.ThreadItem) []string {
		var out []string
		for _, it := range items {
			if it.Type == "agentMessage" {
				out = append(out, it.Text)
			}
		}
		return out
	}

	echo := agentMsgs(ProjectTurn("t1", 0, mkTurn("Done.", "Done."), nil, nil))
	if len(echo) != 1 || echo[0] != "Done." {
		t.Fatalf("echoed communicate: got agentMessages %q, want exactly [\"Done.\"]", echo)
	}

	distinct := agentMsgs(ProjectTurn("t2", 0, mkTurn("Working...", "All set."), nil, nil))
	if len(distinct) != 2 || distinct[0] != "Working..." || distinct[1] != "All set." {
		t.Fatalf("distinct communicate: got agentMessages %q, want [\"Working...\" \"All set.\"]", distinct)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
