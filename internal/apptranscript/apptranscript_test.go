package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestPreludeTurnUsesSemanticHeaderOnly(t *testing.T) {
	turn := PreludeTurn(transcript.Header{SystemPrompt: "You are Serf."})
	if turn == nil || len(turn.Items) != 1 {
		t.Fatalf("prelude=%+v, want only system prompt", turn)
	}
	if turn.Items[0].Description != "System prompt" {
		t.Fatalf("item=%+v", turn.Items[0])
	}
}

// TestTurnsFromFileStampsStartedAtFromEntryTimestamp verifies that a
// reconstructed turn carries StartedAt from the transcript entry's recorded
// timestamp, with DurationMS left nil — a message record has a point in time,
// not a span, so only StartedAt can be honestly reconstructed from it.
func TestTurnsFromFileStampsStartedAtFromEntryTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "th_1"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	ts := time.Unix(1_700_000_000, 0).UTC()
	if err := w.Append(schema.Turn{
		Kind:      schema.TurnUserInput,
		Message:   llm.User("hello"),
		Timestamp: ts,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	project := func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		var entry transcript.Entry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil
		}
		return ProjectTurn(turnID, turnIndex, entry.Turn, map[string]string{}, nil, nil)
	}
	turns, err := TurnsFromFile(path, 128<<20, project)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns=%+v, want 1", turns)
	}
	turn := turns[0]
	if turn.StartedAt == nil {
		t.Fatalf("turn.StartedAt is nil, want %d", ts.UnixMilli())
	}
	if *turn.StartedAt != ts.UnixMilli() {
		t.Fatalf("turn.StartedAt=%d, want %d", *turn.StartedAt, ts.UnixMilli())
	}
	if turn.DurationMS != nil {
		t.Fatalf("turn.DurationMS=%v, want nil (message records cannot span a duration)", *turn.DurationMS)
	}
}

// TestProjectTurnSteeringCarriesUserSource (issue #24): a steering turn
// recorded with SteeringSource="user" (human-sent steering) projects a
// steering item whose Source reaches the web UI, so reload/hydration renders
// it as a user message like the live path does. System steering keeps an
// empty source.
func TestProjectTurnSteeringCarriesUserSource(t *testing.T) {
	userTurn := schema.Turn{
		Kind:           schema.TurnSteering,
		Message:        llm.User("focus on the tests"),
		Timestamp:      time.Unix(1_700_000_000, 0).UTC(),
		SteeringSource: "user",
	}
	items := ProjectTurn("turn_1", 1, userTurn, map[string]string{}, nil, nil)
	if len(items) != 1 || items[0].Type != "steering" {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	if items[0].Source != "user" {
		t.Fatalf("item.Source=%q, want %q", items[0].Source, "user")
	}

	sysTurn := schema.Turn{
		Kind:      schema.TurnSteering,
		Message:   llm.User("<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>"),
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}
	items = ProjectTurn("turn_2", 2, sysTurn, map[string]string{}, nil, nil)
	if len(items) != 1 || items[0].Source != "" {
		t.Fatalf("system steering item.Source=%q, want empty", items[0].Source)
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
	}, toolNames, nil, nil)

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
	}, toolNames, nil, nil)

	if len(done) != 1 || done[0].ToolName != "read_file" || done[0].Output != "ok" {
		t.Fatalf("tool result=%+v", done)
	}
	if string(done[0].Raw) != `{"job_id":"job_1"}` {
		t.Fatalf("tool result Raw = %s, want tool_state", done[0].Raw)
	}
}

// TestProjectTurnStampsFailedStatusOnErroredToolResult (Go follow-up: the
// projector/transcript hardcoded Status:"completed" on every settled item
// regardless of IsError, so clients had to infer error state by checking
// Error's presence instead of trusting Status). Reload must stamp
// TurnStatusFailed when the persisted tool result carries IsError, matching
// the live path.
func TestProjectTurnStampsFailedStatusOnErroredToolResult(t *testing.T) {
	toolNames := map[string]string{}
	done := ProjectTurn("turn_2", 2, schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_fail",
				Name:       "shell",
				Content:    "boom",
				IsError:    true,
			},
		}}},
	}, toolNames, nil, nil)

	if len(done) != 1 {
		t.Fatalf("items=%+v, want 1", done)
	}
	if done[0].Status != appwire.TurnStatusFailed {
		t.Fatalf("errored tool result Status=%q, want %q", done[0].Status, appwire.TurnStatusFailed)
	}
	if done[0].Error != "boom" {
		t.Fatalf("errored tool result Error=%q, want %q", done[0].Error, "boom")
	}
}

// TestProjectTurnKeepsCompletedStatusOnSuccessfulToolResult pins the
// non-error branch alongside the failed-status test above so both sides of
// the status decision are exercised.
func TestProjectTurnKeepsCompletedStatusOnSuccessfulToolResult(t *testing.T) {
	toolNames := map[string]string{}
	done := ProjectTurn("turn_2", 2, schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_ok",
				Name:       "shell",
				Content:    "ok",
			},
		}}},
	}, toolNames, nil, nil)

	if len(done) != 1 {
		t.Fatalf("items=%+v, want 1", done)
	}
	if done[0].Status != appwire.TurnStatusCompleted {
		t.Fatalf("successful tool result Status=%q, want %q", done[0].Status, appwire.TurnStatusCompleted)
	}
}

// TestProjectTurnMapsToolResultExitCode (wire-honesty spec Part A): reload
// promotes a shell tool's exit code from the persisted ToolState the same way
// the live projector does.
func TestProjectTurnMapsToolResultExitCode(t *testing.T) {
	toolNames := map[string]string{}
	done := ProjectTurn("turn_2", 2, schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_shell",
				Name:       "shell",
				Content:    "ok",
				ToolState:  json.RawMessage(`{"type":"shell","status":"completed","exit_code":1}`),
			},
		}}},
	}, toolNames, nil, nil)

	if len(done) != 1 || done[0].ExitCode == nil || *done[0].ExitCode != 1 {
		t.Fatalf("tool result ExitCode=%v, want *1", done[0].ExitCode)
	}
}

// TestProjectTurnMapsToolResultZeroExitCode (wire-honesty spec Part A, review
// Minor) pins the boundary that makes the pointer field honest on reload too:
// a successful shell run's persisted ToolState literally contains
// "exit_code":0, which must produce a non-nil *int64 pointing at 0 —
// distinguishable from the "no exit_code in ToolState at all" case
// (TestProjectTurnOmitsExitCodeForNonShellToolState below), which leaves it
// nil. Go's json only touches the pointer when the key is present, so this
// already holds by construction; pinned here so it can never silently
// regress.
func TestProjectTurnMapsToolResultZeroExitCode(t *testing.T) {
	toolNames := map[string]string{}
	done := ProjectTurn("turn_2", 2, schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_shell_zero",
				Name:       "shell",
				Content:    "ok",
				ToolState:  json.RawMessage(`{"type":"shell","status":"completed","exit_code":0}`),
			},
		}}},
	}, toolNames, nil, nil)

	if len(done) != 1 || done[0].ExitCode == nil {
		t.Fatalf("tool result ExitCode=nil, want a non-nil pointer to 0 (present-and-zero, not absent)")
	}
	if *done[0].ExitCode != 0 {
		t.Fatalf("tool result ExitCode=%v, want *0", *done[0].ExitCode)
	}
}

// TestProjectTurnOmitsExitCodeForNonShellToolState (wire-honesty spec Part A):
// a non-shell tool's ToolState carries no exit_code, so reload must leave
// ExitCode nil rather than fabricating zero.
func TestProjectTurnOmitsExitCodeForNonShellToolState(t *testing.T) {
	toolNames := map[string]string{}
	done := ProjectTurn("turn_2", 2, schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_read",
				Name:       "read_file",
				Content:    "ok",
				ToolState:  json.RawMessage(`{"job_id":"job_1"}`),
			},
		}}},
	}, toolNames, nil, nil)

	if len(done) != 1 || done[0].ExitCode != nil {
		t.Fatalf("tool result ExitCode=%v, want nil for a non-shell ToolState", done[0].ExitCode)
	}
}

func TestProjectTurnProjectsToolResultOutputImages(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	turn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_img",
				Name:           "screenshot",
				Content:        "captured image",
				ImageData:      png,
				ImageMediaType: "image/png",
			},
		}}},
	}

	items := ProjectTurn("turn_1", 1, turn, map[string]string{}, nil, func(result *llm.ToolResultData) []appwire.OutputImage {
		if result == nil || len(result.ImageData) == 0 {
			return nil
		}
		return []appwire.OutputImage{{
			Source:    "tool-result",
			Name:      result.Name,
			MediaType: result.ImageMediaType,
			Size:      int64(len(result.ImageData)),
			SHA:       "sha-img",
			URL:       "/s/01IMG/images/sha-img",
		}}
	})
	if len(items) != 1 || items[0].Type != "commandExecution" {
		t.Fatalf("items=%+v", items)
	}
	if len(items[0].OutputImages) != 1 || items[0].OutputImages[0].SHA != "sha-img" || items[0].OutputImages[0].URL != "/s/01IMG/images/sha-img" {
		t.Fatalf("OutputImages=%+v", items[0].OutputImages)
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

	items := ProjectTurn("turn_1", 1, turn, map[string]string{"call_delegate": "delegate"}, nil, nil)
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
	}, toolNames, nil, nil)
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
	}, toolNames, nil, nil)
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
	}, map[string]string{}, nil, nil)
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
	}, map[string]string{}, nil, nil)
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
	}, map[string]string{}, nil, nil)
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
	}, map[string]string{}, nil, nil)
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

func TestProjectTurnTagsCompactionSystemMessages(t *testing.T) {
	items := ProjectTurn("turn_1", 1, schema.Turn{
		Kind:    schema.TurnSummary,
		Message: llm.Assistant("kept useful context"),
	}, map[string]string{}, nil, nil)

	if len(items) != 1 {
		t.Fatalf("summary items=%+v, want 1", items)
	}
	item := items[0]
	if item.Type != "systemMessage" || item.Description != "Context summary" || item.EventKind != appwire.ThreadItemEventKindCompaction {
		t.Fatalf("summary item=%+v", item)
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

func TestScanPrelude(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	content := `{"kind":"header","format_version":2,"system_prompt":"You are Serf."}
{"kind":"entry","turn":{}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	header, err := ScanPrelude(path, 1<<20)
	if err != nil {
		t.Fatalf("ScanPrelude: %v", err)
	}
	if strings.TrimSpace(header.SystemPrompt) != "You are Serf." {
		t.Errorf("header.SystemPrompt = %q", header.SystemPrompt)
	}
	if _, err := ScanPrelude(filepath.Join(dir, "missing"), 1<<20); err == nil {
		t.Fatal("missing file error = nil")
	}
}

func TestPreludeTurn(t *testing.T) {
	if got := PreludeTurn(transcript.Header{}); got != nil {
		t.Errorf("empty prelude = %+v, want nil", got)
	}
	turn := PreludeTurn(transcript.Header{SystemPrompt: "You are Serf."})
	if turn == nil || len(turn.Items) != 1 || turn.Items[0].Text != "You are Serf." {
		t.Errorf("system prompt only = %+v", turn)
	}
}

func TestTurnsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"kind":"header","format_version":2,"system_prompt":"You are Serf."}
{"kind":"entry","turn":{}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without projector, only prelude turns are emitted (since no projector returns items).
	turns, err := TurnsFromFile(path, 1<<20, nil)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != "turn_system" {
		t.Errorf("turns = %+v", turns)
	}

	// With a projector that returns items.
	project := func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "userMessage", Text: "hello"}}
	}
	turns, err = TurnsFromFile(path, 1<<20, project)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].ID != "turn_system" {
		t.Errorf("first turn = %+v", turns[0])
	}
	if turns[1].ID != "turn_1" || len(turns[1].Items) != 1 {
		t.Errorf("second turn = %+v", turns[1])
	}

	if _, err := TurnsFromFile(filepath.Join(dir, "missing"), 1<<20, nil); err == nil {
		t.Fatal("missing file error = nil")
	}
}

// TestTurnsFromFile_StampsUsageFromEntry verifies an ended session's
// per-round usage (persisted on schema.Turn.Usage) is surfaced on the
// projected appwire.Turn, so the ended-session view can show the same
// per-turn token totals the live projector stamps as the turn happens.
func TestTurnsFromFile_StampsUsageFromEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"kind":"header","format_version":2,"system_prompt":"You are Serf."}
{"kind":"entry","turn":{"usage":{"input_tokens":100,"output_tokens":50}}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	project := func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "agentMessage", Text: "hi"}}
	}
	turns, err := TurnsFromFile(path, 1<<20, project)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d: %+v", len(turns), turns)
	}
	turn := turns[1]
	if turn.Usage == nil {
		t.Fatalf("turn.Usage=nil, want stamped usage")
	}
	if turn.Usage.InputTokens != 100 || turn.Usage.OutputTokens != 50 {
		t.Fatalf("turn.Usage=%+v, want InputTokens=100 OutputTokens=50", turn.Usage)
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

	echo := agentMsgs(ProjectTurn("t1", 0, mkTurn("Done.", "Done."), nil, nil, nil))
	if len(echo) != 1 || echo[0] != "Done." {
		t.Fatalf("echoed communicate: got agentMessages %q, want exactly [\"Done.\"]", echo)
	}

	distinct := agentMsgs(ProjectTurn("t2", 0, mkTurn("Working...", "All set."), nil, nil, nil))
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

// TestProjectTurnRendersModelSwitchMarker verifies (a): a persisted
// TurnModelSwitch turn projects to a systemMessage item reading
// "Switched model: <old> → <new>", extending the TurnCheckpoint/TurnSummary
// systemMessage projection pattern above.
func TestProjectTurnRendersModelSwitchMarker(t *testing.T) {
	out := ProjectTurn("turn_9", 9, schema.Turn{
		Kind:    schema.TurnModelSwitch,
		Message: llm.System("Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6"),
	}, nil, nil, nil)

	if len(out) != 1 {
		t.Fatalf("items=%+v, want exactly one", out)
	}
	item := out[0]
	if item.Type != "systemMessage" {
		t.Fatalf("Type = %q, want systemMessage", item.Type)
	}
	if item.TurnID != "turn_9" || item.TranscriptEntryIndex != 9 {
		t.Fatalf("TurnID/TranscriptEntryIndex = %q/%d, want turn_9/9", item.TurnID, item.TranscriptEntryIndex)
	}
	if item.Text != "Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6" {
		t.Fatalf("Text = %q", item.Text)
	}
	if item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("Status = %q, want completed", item.Status)
	}
}

// TestProjectTurnModelSwitchMarkerEmptyTextOmitted mirrors the
// TurnCheckpoint/TurnSummary empty-text no-op behavior: a marker turn with
// blank text projects to nothing rather than an empty systemMessage.
func TestProjectTurnModelSwitchMarkerEmptyTextOmitted(t *testing.T) {
	out := ProjectTurn("turn_9", 9, schema.Turn{
		Kind:    schema.TurnModelSwitch,
		Message: llm.System("   "),
	}, nil, nil, nil)
	if out != nil {
		t.Fatalf("items=%+v, want nil for blank marker text", out)
	}
}

// TestProjectTurnStampsToolItemTimestamps (issue #37): a replayed tool row's
// hover meta (timestamp · runtime) must come from REAL recorded times. The
// transcript stamps every entry with when it was recorded, so an assistant
// entry's tool-call item carries StartedAt (when the call was issued) and a
// tool-results entry's item carries CompletedAt (when the result landed).
// Zero timestamps stay unset — no epoch-0 fakes.
func TestProjectTurnStampsToolItemTimestamps(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	end := start.Add(3 * time.Second)
	toolNames := map[string]string{}

	items := ProjectTurn("turn_1", 1, schema.Turn{
		Kind:      schema.TurnAssistant,
		Timestamp: start,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_read",
				Name:      "read_file",
				Arguments: []byte(`{"path":"README.md"}`),
			},
		}}},
	}, toolNames, nil, nil)
	if len(items) != 1 || items[0].Type != "commandExecution" {
		t.Fatalf("tool start items=%+v", items)
	}
	if items[0].StartedAt == nil || *items[0].StartedAt != start.UnixMilli() {
		t.Fatalf("tool call item StartedAt=%v, want %d (entry timestamp)", items[0].StartedAt, start.UnixMilli())
	}

	items = ProjectTurn("turn_2", 2, schema.Turn{
		Kind:      schema.TurnToolResults,
		Timestamp: end,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_read",
				Content:    "ok",
			},
		}}},
	}, toolNames, nil, nil)
	if len(items) != 1 || items[0].Type != "commandExecution" {
		t.Fatalf("tool result items=%+v", items)
	}
	if items[0].CompletedAt == nil || *items[0].CompletedAt != end.UnixMilli() {
		t.Fatalf("tool result item CompletedAt=%v, want %d (entry timestamp)", items[0].CompletedAt, end.UnixMilli())
	}

	// A zero entry timestamp mints no stamps.
	items = ProjectTurn("turn_3", 3, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_notime",
				Name:      "read_file",
				Arguments: []byte(`{"path":"x"}`),
			},
		}}},
	}, toolNames, nil, nil)
	if len(items) != 1 || items[0].StartedAt != nil {
		t.Fatalf("zero-timestamp tool call item StartedAt=%v, want nil", items[0].StartedAt)
	}
}
