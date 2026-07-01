package atif

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestCov_NormalizeProviderHandleMode(t *testing.T) {
	if m, err := NormalizeProviderHandleMode(""); err != nil || m != ProviderHandleModeRedacted {
		t.Errorf("empty → %q,%v; want redacted", m, err)
	}
	if m, err := NormalizeProviderHandleMode(string(ProviderHandleModeRawLocal)); err != nil || m != ProviderHandleModeRawLocal {
		t.Errorf("raw-local → %q,%v", m, err)
	}
	if _, err := NormalizeProviderHandleMode("bogus"); err == nil {
		t.Error("bogus mode should error")
	}
}

func TestCov_FirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Errorf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("all-empty firstNonEmpty = %q, want empty", got)
	}
}

func TestCov_BuildRootExtra(t *testing.T) {
	if extra := buildRootExtra(transcript.Header{}); len(extra) != 0 {
		t.Errorf("empty header should yield empty extra, got %v", extra)
	}
	h := transcript.Header{
		WorkingDir:       "/w",
		ParentSessionID:  "psid",
		ParentToolCallID: "ptc",
		Depth:            2,
		Task:             "do it",
		SystemPrompt:     "be good",
	}
	extra := buildRootExtra(h)
	for _, k := range []string{"working_dir", "parent_session_id", "parent_tool_call_id", "depth", "task", "system_prompt"} {
		if _, ok := extra[k]; !ok {
			t.Errorf("extra missing key %q: %v", k, extra)
		}
	}
}

func TestCov_ConvertToolResults(t *testing.T) {
	// No tool results → all-nil.
	empty := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant})
	if obs, errMap, durMap := convertToolResults(empty); obs != nil || errMap != nil || durMap != nil {
		t.Errorf("no results should return nils, got %v %v %v", obs, errMap, durMap)
	}

	turn := schema.NewTurn(schema.TurnToolResults, llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "c1", Name: "shell", Content: "PASS", DurationMS: 42}},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "c2", Name: "shell", Content: "boom", IsError: true}},
			{Kind: llm.ContentText, Text: "ignored"},
			{Kind: llm.ContentToolResult, ToolResult: nil}, // skipped
		},
	})
	obs, errMap, durMap := convertToolResults(turn)
	if obs == nil || len(obs.Results) != 2 {
		t.Fatalf("expected 2 observation results, got %+v", obs)
	}
	if !errMap["c2"] {
		t.Error("c2 should be marked as an error")
	}
	if durMap["c1"] != 42 {
		t.Errorf("c1 duration = %d, want 42", durMap["c1"])
	}
}
