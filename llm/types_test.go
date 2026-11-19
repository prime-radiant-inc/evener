package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateReasoningEffort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		effort  string
		wantErr bool
	}{
		{name: "empty", effort: "", wantErr: false},
		{name: "minimal", effort: "minimal", wantErr: false},
		{name: "low", effort: "low", wantErr: false},
		{name: "medium", effort: "medium", wantErr: false},
		{name: "high", effort: "high", wantErr: false},
		{name: "xhigh", effort: "xhigh", wantErr: false},
		{name: "max", effort: "max", wantErr: false},
		{name: "uppercase", effort: "HIGH", wantErr: false},
		{name: "whitespace", effort: "  medium  ", wantErr: false},
		{name: "unknown", effort: "ultra", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReasoningEffort(tc.effort)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateReasoningEffort(%q) = nil, want error", tc.effort)
				}
				if !strings.Contains(err.Error(), tc.effort) {
					t.Errorf("error %q does not mention invalid value %q", err.Error(), tc.effort)
				}
				for _, lvl := range ReasoningEffortVocabulary() {
					if !strings.Contains(err.Error(), lvl) {
						t.Errorf("error %q does not mention vocabulary level %q", err.Error(), lvl)
					}
				}
			} else if err != nil {
				t.Errorf("ValidateReasoningEffort(%q) = %v, want nil", tc.effort, err)
			}
		})
	}
}

func TestReasoningEffortVocabulary(t *testing.T) {
	t.Parallel()
	want := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	got := ReasoningEffortVocabulary()
	if len(got) != len(want) {
		t.Fatalf("ReasoningEffortVocabulary() = %v, want %v", got, want)
	}
	for i, lvl := range want {
		if got[i] != lvl {
			t.Fatalf("ReasoningEffortVocabulary()[%d] = %q, want %q", i, got[i], lvl)
		}
	}
}

func TestNormalizeFinishReason(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	cases := []struct {
		provider string
		raw      string
		want     string
	}{
		// OpenAI - already canonical
		{"openai", "stop", "stop"},
		{"openai", "length", "length"},
		{"openai", "tool_calls", "tool_calls"},
		{"openai", "content_filter", "content_filter"},

		// Anthropic
		{"anthropic", "end_turn", "stop"},
		{"anthropic", "stop_sequence", "stop"},
		{"anthropic", "max_tokens", "length"},
		{"anthropic", "tool_use", "tool_calls"},
		{"anthropic", "refusal", "content_filter"},

		// Google/Gemini
		{"google", "STOP", "stop"},
		{"google", "MAX_TOKENS", "length"},
		{"google", "SAFETY", "content_filter"},
		{"google", "RECITATION", "content_filter"},

		// Unrecognized -> other
		{"openai", "weird_value", "other"},
		{"anthropic", "unknown", "other"},
		{"google", "BLOCKLIST", "other"},

		// Empty -> stop (default)
		{"openai", "", "stop"},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.raw, func(t *testing.T) {
			got := NormalizeFinishReason(tc.provider, tc.raw)
			if got.Reason != tc.want {
				t.Fatalf("NormalizeFinishReason(%q, %q).Reason = %q, want %q", tc.provider, tc.raw, got.Reason, tc.want)
			}
			if tc.raw != "" && got.Raw != tc.raw {
				t.Fatalf("NormalizeFinishReason(%q, %q).Raw = %q, want %q", tc.provider, tc.raw, got.Raw, tc.raw)
			}
		})
	}
}

func TestContentPart_WebSearch_JSONRoundTrip(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	raw := json.RawMessage(`{"type":"web_search_call","id":"ws_1","action":{"type":"search","query":"go error handling"}}`)
	part := ContentPart{
		Kind: ContentWebSearch,
		WebSearch: &WebSearchData{
			Query: "go error handling",
			Raw:   raw,
		},
	}
	b, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ContentPart
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != ContentWebSearch {
		t.Fatalf("kind: got %q want %q", got.Kind, ContentWebSearch)
	}
	if got.WebSearch == nil {
		t.Fatalf("web_search is nil")
	}
	if got.WebSearch.Query != "go error handling" {
		t.Fatalf("query: got %q", got.WebSearch.Query)
	}
	if string(got.WebSearch.Raw) != string(raw) {
		t.Fatalf("raw: got %s", got.WebSearch.Raw)
	}
}

func TestNormalizeFinishReason_PauseTurn(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	got := NormalizeFinishReason("anthropic", "pause_turn")
	if got.Reason != FinishReasonPauseTurn {
		t.Fatalf("Reason = %q, want %q", got.Reason, FinishReasonPauseTurn)
	}
	if got.Raw != "pause_turn" {
		t.Fatalf("Raw = %q, want %q", got.Raw, "pause_turn")
	}
}

func TestAdapterTimeout_Defaults(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	at := DefaultAdapterTimeout()
	if at.Connect != 10*time.Second {
		t.Fatalf("Connect = %v, want 10s", at.Connect)
	}
	if at.Request != 0 {
		t.Fatalf("Request = %v, want disabled", at.Request)
	}
	if at.StreamRead != 10*time.Minute {
		t.Fatalf("StreamRead = %v, want 10m", at.StreamRead)
	}
}

func TestToolCallData_Parse_PopulatesParsedArguments(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	tc := ToolCallData{
		ID:        "call_1",
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"city":"Seattle","units":"celsius"}`),
	}
	if err := tc.Parse(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tc.ParsedArguments == nil {
		t.Fatal("ParsedArguments is nil after Parse()")
	}
	if tc.ParsedArguments["city"] != "Seattle" {
		t.Fatalf("city = %v", tc.ParsedArguments["city"])
	}
	if tc.ParsedArguments["units"] != "celsius" {
		t.Fatalf("units = %v", tc.ParsedArguments["units"])
	}
}

func TestToolCallData_Parse_EmptyArguments(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	tc := ToolCallData{
		ID:        "call_2",
		Name:      "get_time",
		Arguments: json.RawMessage(`{}`),
	}
	if err := tc.Parse(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tc.ParsedArguments == nil {
		t.Fatal("ParsedArguments is nil after Parse() with empty object")
	}
	if len(tc.ParsedArguments) != 0 {
		t.Fatalf("expected empty map, got %v", tc.ParsedArguments)
	}
}

func TestToolCallData_Parse_NilArguments(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	tc := ToolCallData{
		ID:   "call_3",
		Name: "no_args",
	}
	if err := tc.Parse(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tc.ParsedArguments == nil {
		t.Fatal("ParsedArguments is nil after Parse() with nil arguments")
	}
	if len(tc.ParsedArguments) != 0 {
		t.Fatalf("expected empty map, got %v", tc.ParsedArguments)
	}
}

func TestToolCallData_Parse_InvalidJSON(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	tc := ToolCallData{
		ID:        "call_4",
		Name:      "broken",
		Arguments: json.RawMessage(`{not valid json`),
	}
	if err := tc.Parse(); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestToolResultData_DurationMS_JSONRoundTrip(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	tr := ToolResultData{
		ToolCallID: "call_1",
		Name:       "shell",
		Content:    "hello",
		IsError:    false,
		DurationMS: 1234,
	}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify the JSON contains duration_ms
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := raw["duration_ms"]; !ok {
		t.Fatalf("JSON missing duration_ms field: %s", b)
	}

	// Round-trip
	var got ToolResultData
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DurationMS != 1234 {
		t.Fatalf("DurationMS = %d, want 1234", got.DurationMS)
	}
}

func TestToolResultData_DurationMS_OmittedWhenZero(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	tr := ToolResultData{
		ToolCallID: "call_1",
		Content:    "hello",
	}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["duration_ms"]; ok {
		t.Fatalf("JSON should omit duration_ms when zero: %s", b)
	}
}

func TestIsOpenAICompatReasoningFieldMatchesTheWireFieldNames(t *testing.T) {
	t.Parallel()
	// Anthropic-style providers use this to tell a wire field name apart from a
	// cryptographic signature, and replaying the wrong one is an API rejection.
	for _, sig := range OpenAICompatReasoningFields() {
		if !IsOpenAICompatReasoningField(sig) {
			t.Errorf("IsOpenAICompatReasoningField(%q) = false, want true for a published field name", sig)
		}
	}
	for _, sig := range []string{"", "Reasoning", "reasoning_", "signature", "erp_1a2b3c"} {
		if IsOpenAICompatReasoningField(sig) {
			t.Errorf("IsOpenAICompatReasoningField(%q) = true, want false", sig)
		}
	}
}

func TestRequest_Validate_WithWebSearch(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	req := Request{
		Model:     "test-model",
		Messages:  []Message{User("hello")},
		WebSearch: true,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate with WebSearch=true: %v", err)
	}
}

// TestMessageConstructors covers the Developer and ToolResult constructors.
func TestMessageConstructors(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	dev := Developer("guidance")
	if dev.Role != RoleDeveloper || dev.Text() != "guidance" {
		t.Fatalf("Developer() = %+v", dev)
	}

	tr := ToolResult("call-1", "output", true)
	if tr.Role != RoleTool || tr.ToolCallID != "call-1" {
		t.Fatalf("ToolResult() role/id = %+v", tr)
	}
	if len(tr.Content) != 1 || tr.Content[0].ToolResult == nil {
		t.Fatalf("ToolResult() content = %+v", tr.Content)
	}
	if !tr.Content[0].ToolResult.IsError || tr.Content[0].ToolResult.Content != "output" {
		t.Fatalf("ToolResult() data = %+v", tr.Content[0].ToolResult)
	}
}

// TestReasoningTextJoinsBlocksWithBlankLine covers the blank-line separator arm
// between multiple thinking blocks.
func TestReasoningTextJoinsBlocksWithBlankLine(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	r := Response{Message: Message{Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: "first"}},
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: "second"}},
	}}}
	if got := r.ReasoningText(); got != "first\n\nsecond" {
		t.Fatalf("ReasoningText() = %q, want joined blocks", got)
	}
}

func TestNormalizeToolArguments(t *testing.T) {
	for _, tc := range []struct{ in, compact, spelled, wire string }{
		{`{"a":1,"b":"<x&y>"}`, `{"a":1,"b":"\u003cx\u0026y\u003e"}`, `{"a":1,"b":"<x&y>"}`, `{"a":1,"b":"<x&y>"}`},
		{`{ "a" : 1 , "b" : 2 }`, `{"a":1,"b":2}`, `{ "a" : 1 , "b" : 2 }`, `{ "a" : 1 , "b" : 2 }`},
		{`{"a":1}`, `{"a":1}`, "", `{"a":1}`}, {"  {\"a\":1}\n", `{"a":1}`, "  {\"a\":1}\n", "  {\"a\":1}\n"},
		{`{"n": 1.50}`, `{"n":1.50}`, `{"n": 1.50}`, `{"n": 1.50}`}, {`{not json}`, `{not json}`, "", `{not json}`},
		{`{ "a" : `, `{ "a" : `, "", `{ "a" : `}, {"", "", "", ""}, {"   ", "   ", "", "   "},
		{"\u00a0{\"a\":1}", "\u00a0{\"a\":1}", "", "\u00a0{\"a\":1}"},
	} {
		c, s := NormalizeToolArguments([]byte(tc.in))
		call := ToolCallData{Arguments: c, ArgumentsSpelling: s}
		if string(c) != tc.compact || string(s) != tc.spelled || call.WireArguments() != tc.wire {
			t.Errorf("NormalizeToolArguments(%q) = (%q, %q), WireArguments = %q, want (%q, %q) / %q", tc.in, c, s, call.WireArguments(), tc.compact, tc.spelled, tc.wire)
		}
	}
	d := ToolCallData{Arguments: json.RawMessage(`{"a":1}`), ArgumentsSpelling: `{ "a" : 2 }`}
	var tolerated ToolCallData
	bad := json.Unmarshal([]byte(`{"arguments":{"a":1},"arguments_spelling":123}`), &tolerated)
	if n, _ := NormalizeToolArguments(nil); n != nil || d.WireArguments() != `{"a":1}` || (*ToolCallData)(nil).WireArguments() != "" || bad != nil || tolerated.WireArguments() != `{"a":1}` {
		t.Errorf("nil empty = %v, mismatched = %q, nil wire = %q, non-string spelling = %v / %q", n == nil, d.WireArguments(), (*ToolCallData)(nil).WireArguments(), bad, tolerated.WireArguments())
	}
}

// TestStripOpaqueThinkingForCrossProvider: the kept part loses its blob and a
// non-compat signature, a shape reasoningItem refuses drops, and the stored part
// is never mutated. The wire-level cases live in the agent package.
func TestStripOpaqueThinkingForCrossProvider(t *testing.T) {
	t.Parallel()
	native := []ResponsesReasoningPart{{Type: "reasoning_text", Text: "t"}}
	stored := &ThinkingData{EncryptedContent: "enc_A", Signature: "sig_anth", ResponsesContentParts: native}
	got, dropped := StripOpaqueThinkingForCrossProvider(ContentPart{Kind: ContentThinking, Thinking: stored})
	if dropped || got.Thinking.EncryptedContent != "" || got.Thinking.Signature != "" {
		t.Fatalf("got dropped=%v thinking=%+v, want the portable half without blob or signature", dropped, got.Thinking)
	}
	if stored.EncryptedContent != "enc_A" || stored.Signature != "sig_anth" {
		t.Fatalf("the strip mutated the stored part: %+v", stored)
	}
	if _, dropped := StripOpaqueThinkingForCrossProvider(ContentPart{Kind: ContentThinking,
		Thinking: &ThinkingData{Signature: "reasoning_content", ResponsesSummaryParts: native}}); dropped {
		t.Fatal("a compat reasoning_details field must survive as wire continuation state")
	}
}
