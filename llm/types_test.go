package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateReasoningEffort(t *testing.T) {
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
	got := NormalizeFinishReason("anthropic", "pause_turn")
	if got.Reason != FinishReasonPauseTurn {
		t.Fatalf("Reason = %q, want %q", got.Reason, FinishReasonPauseTurn)
	}
	if got.Raw != "pause_turn" {
		t.Fatalf("Raw = %q, want %q", got.Raw, "pause_turn")
	}
}

func TestAdapterTimeout_Defaults(t *testing.T) {
	at := DefaultAdapterTimeout()
	if at.Connect != 10*time.Second {
		t.Fatalf("Connect = %v, want 10s", at.Connect)
	}
	if at.Request != 120*time.Second {
		t.Fatalf("Request = %v, want 120s", at.Request)
	}
	if at.StreamRead != 30*time.Second {
		t.Fatalf("StreamRead = %v, want 30s", at.StreamRead)
	}
}

func TestToolCallData_Parse_PopulatesParsedArguments(t *testing.T) {
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

func TestOrderedEffortLevelsSortsByRankNotByMapOrder(t *testing.T) {
	// Go randomizes map iteration, so the only way the result can be stable is
	// if it is genuinely sorted by ReasoningEffortRank. That stability is the
	// point: ReasoningEffortLevels, the task_list enum, the spawn-form chip and
	// ClampReasoningEffort all read this one definition so they cannot drift.
	levels := map[string]string{
		"max": "max", "low": "low", "xhigh": "xhigh",
		"minimal": "minimal", "high": "high", "medium": "medium",
	}
	want := "minimal,low,medium,high,xhigh,max"
	for range 20 {
		if got := strings.Join(OrderedEffortLevels(levels), ","); got != want {
			t.Fatalf("OrderedEffortLevels = %q, want %q", got, want)
		}
	}

	if got := OrderedEffortLevels(nil); len(got) != 0 {
		t.Fatalf("OrderedEffortLevels(nil) = %v, want empty", got)
	}

	// An unranked level ranks 0, so it sorts ahead of every known level rather
	// than being dropped — the caller still sees it and can decide.
	got := strings.Join(OrderedEffortLevels(map[string]string{"high": "high", "bogus": "bogus"}), ",")
	if got != "bogus,high" {
		t.Fatalf("OrderedEffortLevels with an unranked level = %q, want %q", got, "bogus,high")
	}
}

func TestIsOpenAICompatReasoningFieldMatchesTheWireFieldNames(t *testing.T) {
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
	req := Request{
		Model:     "test-model",
		Messages:  []Message{User("hello")},
		WebSearch: true,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate with WebSearch=true: %v", err)
	}
}
