package atif

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestConvertToATIF_SimpleConversation(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-001",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0-abc1234",
		ProfileID:    "openai",
		WorkingDir:   "/app",
		CreatedAt:    ts,
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnUserInput,
				Message:   llm.User("Hello, world!"),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind:      schema.TurnAssistant,
				Message:   llm.Assistant("Hi there! How can I help?"),
				Timestamp: ts.Add(2 * time.Second),
				Usage: llm.Usage{
					InputTokens:  100,
					OutputTokens: 50,
				},
			},
		},
	}

	traj := Convert(header, entries)

	// --- Root trajectory fields ---
	if traj.SchemaVersion != "ATIF-v1.7" {
		t.Errorf("SchemaVersion = %q, want %q", traj.SchemaVersion, "ATIF-v1.7")
	}
	if traj.SessionID != "sess-001" {
		t.Errorf("SessionID = %q, want %q", traj.SessionID, "sess-001")
	}

	// --- Agent fields ---
	if traj.Agent.Name != "serf" {
		t.Errorf("Agent.Name = %q, want %q", traj.Agent.Name, "serf")
	}
	if traj.Agent.Version != "v0.1.0-abc1234" {
		t.Errorf("Agent.Version = %q, want %q", traj.Agent.Version, "v0.1.0-abc1234")
	}
	if traj.Agent.ModelName != "gpt-5.3-codex" {
		t.Errorf("Agent.ModelName = %q, want %q", traj.Agent.ModelName, "gpt-5.3-codex")
	}
	if traj.Agent.Extra == nil {
		t.Fatal("Agent.Extra is nil")
	}
	if traj.Agent.Extra["profile_id"] != "openai" {
		t.Errorf("Agent.Extra[profile_id] = %v, want %q", traj.Agent.Extra["profile_id"], "openai")
	}

	// --- Steps ---
	if len(traj.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(traj.Steps))
	}

	// Step 1: user
	s1 := traj.Steps[0]
	if s1.StepID != 1 {
		t.Errorf("Step[0].StepID = %d, want 1", s1.StepID)
	}
	if s1.Source != "user" {
		t.Errorf("Step[0].Source = %q, want %q", s1.Source, "user")
	}
	if s1.Message != "Hello, world!" {
		t.Errorf("Step[0].Message = %q, want %q", s1.Message, "Hello, world!")
	}
	if s1.Timestamp != "2026-03-01T10:00:00Z" {
		t.Errorf("Step[0].Timestamp = %q, want %q", s1.Timestamp, "2026-03-01T10:00:00Z")
	}
	if s1.Extra == nil {
		t.Error("Step[0].Extra is nil")
	}

	// Step 2: agent
	s2 := traj.Steps[1]
	if s2.StepID != 2 {
		t.Errorf("Step[1].StepID = %d, want 2", s2.StepID)
	}
	if s2.Source != "agent" {
		t.Errorf("Step[1].Source = %q, want %q", s2.Source, "agent")
	}
	if s2.Message != "Hi there! How can I help?" {
		t.Errorf("Step[1].Message = %q, want %q", s2.Message, "Hi there! How can I help?")
	}
	if s2.Timestamp != "2026-03-01T10:00:02Z" {
		t.Errorf("Step[1].Timestamp = %q, want %q", s2.Timestamp, "2026-03-01T10:00:02Z")
	}
	if s2.Metrics == nil {
		t.Fatal("Step[1].Metrics is nil")
	}
	if s2.Metrics.PromptTokens != 100 {
		t.Errorf("Step[1].Metrics.PromptTokens = %d, want 100", s2.Metrics.PromptTokens)
	}
	if s2.Metrics.CompletionTokens != 50 {
		t.Errorf("Step[1].Metrics.CompletionTokens = %d, want 50", s2.Metrics.CompletionTokens)
	}

	// --- FinalMetrics ---
	if traj.FinalMetrics == nil {
		t.Fatal("FinalMetrics is nil")
	}
	if traj.FinalMetrics.TotalPromptTokens != 100 {
		t.Errorf("FinalMetrics.TotalPromptTokens = %d, want 100", traj.FinalMetrics.TotalPromptTokens)
	}
	if traj.FinalMetrics.TotalCompletionTokens != 50 {
		t.Errorf("FinalMetrics.TotalCompletionTokens = %d, want 50", traj.FinalMetrics.TotalCompletionTokens)
	}
	if traj.FinalMetrics.TotalSteps != 2 {
		t.Errorf("FinalMetrics.TotalSteps = %d, want 2", traj.FinalMetrics.TotalSteps)
	}

	// --- Root Extra ---
	if traj.Extra == nil {
		t.Fatal("Extra is nil")
	}
	if traj.Extra["working_dir"] != "/app" {
		t.Errorf("Extra[working_dir] = %v, want %q", traj.Extra["working_dir"], "/app")
	}

	// --- JSON round-trip: Extra maps should not be null ---
	data, err := json.Marshal(traj)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	// Step extras should be objects, not null
	steps := raw["steps"].([]any)
	for i, step := range steps {
		stepMap := step.(map[string]any)
		if stepMap["extra"] == nil {
			t.Errorf("Step[%d].extra is null in JSON", i)
		}
	}
}

func TestConvertToATIF_ToolUse(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-tool",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.2.0",
		ProfileID:    "openai",
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnUserInput,
				Message:   llm.User("List files in /app"),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "I'll list the files for you."},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-1",
								Name:      "shell",
								Arguments: json.RawMessage(`{"command":"ls /app"}`),
							},
						},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-2",
								Name:      "read_file",
								Arguments: json.RawMessage(`{"path":"/app/main.go","offset":0}`),
							},
						},
					},
				},
				Timestamp: ts.Add(1 * time.Second),
				Usage:     llm.Usage{InputTokens: 200, OutputTokens: 80},
			},
		},
		{
			Kind: "entry",
			Seq:  2,
			Turn: schema.Turn{
				Kind: schema.TurnToolResults,
				Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID: "call-1",
								Content:    "main.go\ngo.mod\n",
								DurationMS: 150,
							},
						},
						{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID: "call-2",
								Content:    "package main\n",
								DurationMS: 45,
							},
						},
					},
				},
				Timestamp: ts.Add(2 * time.Second),
			},
		},
	}

	traj := Convert(header, entries)

	// ASSISTANT + TOOL_RESULTS should merge into one step → 2 total steps (user + agent).
	if len(traj.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(traj.Steps))
	}

	agentStep := traj.Steps[1]

	// Source and text
	if agentStep.Source != "agent" {
		t.Errorf("agentStep.Source = %q, want %q", agentStep.Source, "agent")
	}
	if agentStep.Message != "I'll list the files for you." {
		t.Errorf("agentStep.Message = %q, want %q", agentStep.Message, "I'll list the files for you.")
	}

	// Tool calls
	if len(agentStep.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2", len(agentStep.ToolCalls))
	}

	tc1 := agentStep.ToolCalls[0]
	if tc1.ToolCallID != "call-1" {
		t.Errorf("ToolCalls[0].ToolCallID = %q, want %q", tc1.ToolCallID, "call-1")
	}
	if tc1.FunctionName != "shell" {
		t.Errorf("ToolCalls[0].FunctionName = %q, want %q", tc1.FunctionName, "shell")
	}
	if tc1.Arguments["command"] != "ls /app" {
		t.Errorf("ToolCalls[0].Arguments[command] = %v, want %q", tc1.Arguments["command"], "ls /app")
	}

	tc2 := agentStep.ToolCalls[1]
	if tc2.ToolCallID != "call-2" {
		t.Errorf("ToolCalls[1].ToolCallID = %q, want %q", tc2.ToolCallID, "call-2")
	}
	if tc2.FunctionName != "read_file" {
		t.Errorf("ToolCalls[1].FunctionName = %q, want %q", tc2.FunctionName, "read_file")
	}
	if tc2.Arguments["path"] != "/app/main.go" {
		t.Errorf("ToolCalls[1].Arguments[path] = %v, want %q", tc2.Arguments["path"], "/app/main.go")
	}
	// Verify numeric argument parsed correctly (JSON numbers become float64 in map[string]any)
	if tc2.Arguments["offset"] != float64(0) {
		t.Errorf("ToolCalls[1].Arguments[offset] = %v (%T), want float64(0)", tc2.Arguments["offset"], tc2.Arguments["offset"])
	}

	// Observation (merged from TOOL_RESULTS)
	if agentStep.Observation == nil {
		t.Fatal("agentStep.Observation is nil")
	}
	if len(agentStep.Observation.Results) != 2 {
		t.Fatalf("len(Observation.Results) = %d, want 2", len(agentStep.Observation.Results))
	}

	r1 := agentStep.Observation.Results[0]
	if r1.SourceCallID != "call-1" {
		t.Errorf("Results[0].SourceCallID = %q, want %q", r1.SourceCallID, "call-1")
	}
	if r1.Content != "main.go\ngo.mod\n" {
		t.Errorf("Results[0].Content = %q, want %q", r1.Content, "main.go\ngo.mod\n")
	}

	r2 := agentStep.Observation.Results[1]
	if r2.SourceCallID != "call-2" {
		t.Errorf("Results[1].SourceCallID = %q, want %q", r2.SourceCallID, "call-2")
	}
	if r2.Content != "package main\n" {
		t.Errorf("Results[1].Content = %q, want %q", r2.Content, "package main\n")
	}

	// Tool durations in step.Extra
	durMap, ok := agentStep.Extra["tool_durations_ms"].(map[string]int64)
	if !ok {
		t.Fatalf("Extra[tool_durations_ms] type = %T, want map[string]int64", agentStep.Extra["tool_durations_ms"])
	}
	if durMap["call-1"] != 150 {
		t.Errorf("tool_durations_ms[call-1] = %d, want 150", durMap["call-1"])
	}
	if durMap["call-2"] != 45 {
		t.Errorf("tool_durations_ms[call-2] = %d, want 45", durMap["call-2"])
	}

	// No tool errors in this case
	if _, hasErrors := agentStep.Extra["tool_errors"]; hasErrors {
		t.Errorf("Extra[tool_errors] should not be present for successful tool calls")
	}

	// Metrics still populated
	if agentStep.Metrics == nil {
		t.Fatal("agentStep.Metrics is nil")
	}
	if agentStep.Metrics.PromptTokens != 200 {
		t.Errorf("Metrics.PromptTokens = %d, want 200", agentStep.Metrics.PromptTokens)
	}
	if agentStep.Metrics.CompletionTokens != 80 {
		t.Errorf("Metrics.CompletionTokens = %d, want 80", agentStep.Metrics.CompletionTokens)
	}

	// FinalMetrics totals
	if traj.FinalMetrics.TotalSteps != 2 {
		t.Errorf("FinalMetrics.TotalSteps = %d, want 2", traj.FinalMetrics.TotalSteps)
	}
	if traj.FinalMetrics.TotalPromptTokens != 200 {
		t.Errorf("FinalMetrics.TotalPromptTokens = %d, want 200", traj.FinalMetrics.TotalPromptTokens)
	}
}

func TestConvertToATIF_ToolError(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-err",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.2.0",
		ProfileID:    "openai",
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnUserInput,
				Message:   llm.User("Delete the file"),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Deleting the file."},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-ok",
								Name:      "shell",
								Arguments: json.RawMessage(`{"command":"ls"}`),
							},
						},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call-err",
								Name:      "shell",
								Arguments: json.RawMessage(`{"command":"rm /protected"}`),
							},
						},
					},
				},
				Timestamp: ts.Add(1 * time.Second),
			},
		},
		{
			Kind: "entry",
			Seq:  2,
			Turn: schema.Turn{
				Kind: schema.TurnToolResults,
				Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID: "call-ok",
								Content:    "file1.txt\nfile2.txt\n",
								DurationMS: 50,
							},
						},
						{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID: "call-err",
								Content:    "permission denied",
								IsError:    true,
								DurationMS: 12,
							},
						},
					},
				},
				Timestamp: ts.Add(2 * time.Second),
			},
		},
	}

	traj := Convert(header, entries)

	if len(traj.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(traj.Steps))
	}

	agentStep := traj.Steps[1]

	// Observation present with both results
	if agentStep.Observation == nil {
		t.Fatal("agentStep.Observation is nil")
	}
	if len(agentStep.Observation.Results) != 2 {
		t.Fatalf("len(Observation.Results) = %d, want 2", len(agentStep.Observation.Results))
	}

	// Verify error result content
	errResult := agentStep.Observation.Results[1]
	if errResult.SourceCallID != "call-err" {
		t.Errorf("error result SourceCallID = %q, want %q", errResult.SourceCallID, "call-err")
	}
	if errResult.Content != "permission denied" {
		t.Errorf("error result Content = %q, want %q", errResult.Content, "permission denied")
	}

	// tool_errors should flag only the errored call
	errFlags, ok := agentStep.Extra["tool_errors"].(map[string]bool)
	if !ok {
		t.Fatalf("Extra[tool_errors] type = %T, want map[string]bool", agentStep.Extra["tool_errors"])
	}
	if !errFlags["call-err"] {
		t.Errorf("tool_errors[call-err] = false, want true")
	}
	if errFlags["call-ok"] {
		t.Errorf("tool_errors[call-ok] = true, want absent or false")
	}
	if len(errFlags) != 1 {
		t.Errorf("len(tool_errors) = %d, want 1 (only errored calls)", len(errFlags))
	}

	// tool_durations_ms should have both
	durMap, ok := agentStep.Extra["tool_durations_ms"].(map[string]int64)
	if !ok {
		t.Fatalf("Extra[tool_durations_ms] type = %T, want map[string]int64", agentStep.Extra["tool_durations_ms"])
	}
	if durMap["call-ok"] != 50 {
		t.Errorf("tool_durations_ms[call-ok] = %d, want 50", durMap["call-ok"])
	}
	if durMap["call-err"] != 12 {
		t.Errorf("tool_durations_ms[call-err] = %d, want 12", durMap["call-err"])
	}
}

func TestConvertToATIF_ThinkingContent(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-think",
		Model:        "claude-sonnet-4-20250514",
		BuildVersion: "v0.3.0",
		ProfileID:    "anthropic",
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnUserInput,
				Message:   llm.User("Explain recursion"),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentThinking,
							Thinking: &llm.ThinkingData{
								Text:      "Let me think about how to explain recursion clearly...",
								Signature: "sig-abc",
							},
						},
						{Kind: llm.ContentText, Text: "Recursion is when a function calls itself."},
					},
				},
				Timestamp: ts.Add(2 * time.Second),
				Usage:     llm.Usage{InputTokens: 50, OutputTokens: 30},
			},
		},
	}

	traj := Convert(header, entries)

	if len(traj.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(traj.Steps))
	}

	agentStep := traj.Steps[1]
	if agentStep.Source != "agent" {
		t.Errorf("Source = %q, want %q", agentStep.Source, "agent")
	}
	if agentStep.Message != "Recursion is when a function calls itself." {
		t.Errorf("Message = %q, want %q", agentStep.Message, "Recursion is when a function calls itself.")
	}
	if agentStep.ReasoningContent != "Let me think about how to explain recursion clearly..." {
		t.Errorf("ReasoningContent = %q, want thinking text", agentStep.ReasoningContent)
	}
	if agentStep.Extra["thinking_signature"] != "sig-abc" {
		t.Errorf("Extra[thinking_signature] = %v, want %q", agentStep.Extra["thinking_signature"], "sig-abc")
	}

	// Redacted thinking: separate entry using ContentRedThinking kind
	entries[1].Turn.Message.Content = []llm.ContentPart{
		{Kind: llm.ContentRedThinking},
		{Kind: llm.ContentText, Text: "Here is the answer."},
	}

	traj2 := Convert(header, entries)
	agentStep2 := traj2.Steps[1]
	if agentStep2.Extra["has_redacted_thinking"] != true {
		t.Errorf("Extra[has_redacted_thinking] = %v, want true (ContentRedThinking)", agentStep2.Extra["has_redacted_thinking"])
	}
	if agentStep2.ReasoningContent != "" {
		t.Errorf("ReasoningContent = %q, want empty for redacted thinking", agentStep2.ReasoningContent)
	}

	// Redacted thinking via ThinkingData.Redacted flag
	entries[1].Turn.Message.Content = []llm.ContentPart{
		{
			Kind: llm.ContentThinking,
			Thinking: &llm.ThinkingData{
				Redacted: true,
			},
		},
		{Kind: llm.ContentText, Text: "Answer with redacted flag."},
	}

	traj3 := Convert(header, entries)
	agentStep3 := traj3.Steps[1]
	if agentStep3.Extra["has_redacted_thinking"] != true {
		t.Errorf("Extra[has_redacted_thinking] = %v, want true (Redacted flag)", agentStep3.Extra["has_redacted_thinking"])
	}
}

func TestConvertToATIF_CheckpointAndSummary(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-compact",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.3.0",
		ProfileID:    "openai",
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnCheckpoint,
				Message:   llm.User("Checkpoint: 5 tool calls executed, 3 files modified."),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind:      schema.TurnSummary,
				Message:   llm.User("Summary: The agent refactored the auth module."),
				Timestamp: ts.Add(1 * time.Second),
			},
		},
	}

	traj := Convert(header, entries)

	if len(traj.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(traj.Steps))
	}

	// Checkpoint step
	cp := traj.Steps[0]
	if cp.StepID != 1 {
		t.Errorf("checkpoint StepID = %d, want 1", cp.StepID)
	}
	if cp.Source != "system" {
		t.Errorf("checkpoint Source = %q, want %q", cp.Source, "system")
	}
	if cp.Extra["serf_kind"] != "checkpoint" {
		t.Errorf("checkpoint Extra[serf_kind] = %v, want %q", cp.Extra["serf_kind"], "checkpoint")
	}
	if cp.Message != "Checkpoint: 5 tool calls executed, 3 files modified." {
		t.Errorf("checkpoint Message = %q, want checkpoint text", cp.Message)
	}

	// Summary step
	sm := traj.Steps[1]
	if sm.StepID != 2 {
		t.Errorf("summary StepID = %d, want 2", sm.StepID)
	}
	if sm.Source != "system" {
		t.Errorf("summary Source = %q, want %q", sm.Source, "system")
	}
	if sm.Extra["serf_kind"] != "summary" {
		t.Errorf("summary Extra[serf_kind] = %v, want %q", sm.Extra["serf_kind"], "summary")
	}
	if sm.Message != "Summary: The agent refactored the auth module." {
		t.Errorf("summary Message = %q, want summary text", sm.Message)
	}
}

func TestConvertToATIF_EmptyTranscript(t *testing.T) {
	header := transcript.Header{
		SessionID:    "sess-empty",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0",
		ProfileID:    "openai",
	}

	traj := Convert(header, nil)

	if traj.SchemaVersion != "ATIF-v1.7" {
		t.Errorf("SchemaVersion = %q, want %q", traj.SchemaVersion, "ATIF-v1.7")
	}
	if traj.SessionID != "sess-empty" {
		t.Errorf("SessionID = %q, want %q", traj.SessionID, "sess-empty")
	}
	if len(traj.Steps) != 0 {
		t.Errorf("len(Steps) = %d, want 0", len(traj.Steps))
	}
	if traj.FinalMetrics == nil {
		t.Fatal("FinalMetrics is nil")
	}
	if traj.FinalMetrics.TotalSteps != 0 {
		t.Errorf("FinalMetrics.TotalSteps = %d, want 0", traj.FinalMetrics.TotalSteps)
	}
	if traj.FinalMetrics.TotalPromptTokens != 0 {
		t.Errorf("FinalMetrics.TotalPromptTokens = %d, want 0", traj.FinalMetrics.TotalPromptTokens)
	}

	// JSON round-trip: steps should be null or empty array (not an error)
	data, err := json.Marshal(traj)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	// Agent and extra should be present
	if raw["agent"] == nil {
		t.Error("agent is nil in JSON")
	}
	if raw["extra"] == nil {
		t.Error("extra is nil in JSON")
	}
}

func TestConvertToATIF_MissingBuildVersion(t *testing.T) {
	header := transcript.Header{
		SessionID: "sess-noversion",
		Model:     "gpt-5.3-codex",
		ProfileID: "openai",
		// BuildVersion deliberately empty
	}

	traj := Convert(header, nil)

	if traj.Agent.Version != "unknown" {
		t.Errorf("Agent.Version = %q, want %q", traj.Agent.Version, "unknown")
	}
}

func TestConvertToATIF_SteeringTurn(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-steer",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.3.0",
		ProfileID:    "openai",
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnSteering,
				Message:   llm.User("You are now in verification mode."),
				Timestamp: ts,
			},
		},
	}

	traj := Convert(header, entries)

	if len(traj.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(traj.Steps))
	}

	step := traj.Steps[0]
	if step.Source != "system" {
		t.Errorf("Source = %q, want %q", step.Source, "system")
	}
	if step.Message != "You are now in verification mode." {
		t.Errorf("Message = %q, want steering text", step.Message)
	}
	if step.Timestamp != "2026-03-01T10:00:00Z" {
		t.Errorf("Timestamp = %q, want %q", step.Timestamp, "2026-03-01T10:00:00Z")
	}
	// Steering turns should NOT have serf_kind (unlike checkpoint/summary)
	if _, has := step.Extra["serf_kind"]; has {
		t.Errorf("Extra[serf_kind] = %v, steering turns should not have serf_kind", step.Extra["serf_kind"])
	}
}

func TestConvertToATIF_OrphanedToolResults(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-orphan",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.3.0",
		ProfileID:    "openai",
	}

	// TOOL_RESULTS without a preceding ASSISTANT turn
	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind: schema.TurnToolResults,
				Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID: "orphan-call-1",
								Content:    "orphaned result",
								DurationMS: 100,
							},
						},
					},
				},
				Timestamp: ts,
			},
		},
	}

	traj := Convert(header, entries)

	if len(traj.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(traj.Steps))
	}

	step := traj.Steps[0]
	// ATIF forbids an observation on a non-agent step and requires every
	// observation source_call_id to reference a tool_call in the same step.
	// An orphaned tool result has no originating tool_call, so it is emitted as
	// an agent step with the source_call_id nulled; the original ids are kept
	// in extra for traceability.
	if step.Source != "agent" {
		t.Errorf("Source = %q, want %q", step.Source, "agent")
	}
	if step.Extra["serf_kind"] != "orphaned_tool_results" {
		t.Errorf("Extra[serf_kind] = %v, want %q", step.Extra["serf_kind"], "orphaned_tool_results")
	}
	if step.Observation == nil {
		t.Fatal("Observation is nil")
	}
	if len(step.Observation.Results) != 1 {
		t.Fatalf("len(Observation.Results) = %d, want 1", len(step.Observation.Results))
	}
	if step.Observation.Results[0].SourceCallID != "" {
		t.Errorf("Results[0].SourceCallID = %q, want empty (nulled)", step.Observation.Results[0].SourceCallID)
	}
	origIDs, ok := step.Extra["orphaned_source_call_ids"].([]string)
	if !ok {
		t.Fatalf("Extra[orphaned_source_call_ids] type = %T, want []string", step.Extra["orphaned_source_call_ids"])
	}
	if len(origIDs) != 1 || origIDs[0] != "orphan-call-1" {
		t.Errorf("Extra[orphaned_source_call_ids] = %v, want [orphan-call-1]", origIDs)
	}
	if step.Observation.Results[0].Content != "orphaned result" {
		t.Errorf("Results[0].Content = %q, want %q", step.Observation.Results[0].Content, "orphaned result")
	}

	// Duration should be captured
	durMap, ok := step.Extra["tool_durations_ms"].(map[string]int64)
	if !ok {
		t.Fatalf("Extra[tool_durations_ms] type = %T, want map[string]int64", step.Extra["tool_durations_ms"])
	}
	if durMap["orphan-call-1"] != 100 {
		t.Errorf("tool_durations_ms[orphan-call-1] = %d, want 100", durMap["orphan-call-1"])
	}
}

func TestConvertToATIF_WebSearch(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := transcript.Header{
		SessionID:    "sess-web",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.3.0",
		ProfileID:    "openai",
	}

	entries := []transcript.Entry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnUserInput,
				Message:   llm.User("Search for Go testing best practices"),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{
							Kind:      llm.ContentWebSearch,
							WebSearch: &llm.WebSearchData{Query: "Go testing best practices 2026"},
						},
						{Kind: llm.ContentText, Text: "Here are the results from my search."},
					},
				},
				Timestamp: ts.Add(3 * time.Second),
				Usage:     llm.Usage{InputTokens: 80, OutputTokens: 40},
			},
		},
	}

	traj := Convert(header, entries)

	if len(traj.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(traj.Steps))
	}

	agentStep := traj.Steps[1]
	if agentStep.Message != "Here are the results from my search." {
		t.Errorf("Message = %q, want search result text", agentStep.Message)
	}

	wsRaw, ok := agentStep.Extra["web_searches"]
	if !ok {
		t.Fatal("Extra[web_searches] not present")
	}
	wsList, ok := wsRaw.([]any)
	if !ok {
		t.Fatalf("Extra[web_searches] type = %T, want []any", wsRaw)
	}
	if len(wsList) != 1 {
		t.Fatalf("len(web_searches) = %d, want 1", len(wsList))
	}
	wsEntry, ok := wsList[0].(map[string]any)
	if !ok {
		t.Fatalf("web_searches[0] type = %T, want map[string]any", wsList[0])
	}
	if wsEntry["query"] != "Go testing best practices 2026" {
		t.Errorf("web_searches[0].query = %v, want %q", wsEntry["query"], "Go testing best practices 2026")
	}
}

func TestConvertToATIF_MultiRound(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	cacheRead := 20

	header := transcript.Header{
		SessionID:    "sess-multi",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.3.0",
		ProfileID:    "openai",
	}

	entries := []transcript.Entry{
		// Round 1: USER
		{
			Kind: "entry",
			Seq:  0,
			Turn: schema.Turn{
				Kind:      schema.TurnUserInput,
				Message:   llm.User("List files"),
				Timestamp: ts,
			},
		},
		// Round 1: ASSISTANT with tool call
		{
			Kind: "entry",
			Seq:  1,
			Turn: schema.Turn{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Listing files."},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "mc-1",
								Name:      "shell",
								Arguments: json.RawMessage(`{"command":"ls"}`),
							},
						},
					},
				},
				Timestamp: ts.Add(1 * time.Second),
				Usage: llm.Usage{
					InputTokens:     100,
					OutputTokens:    40,
					CacheReadTokens: &cacheRead,
				},
			},
		},
		// Round 1: TOOL_RESULTS (merged with preceding ASSISTANT)
		{
			Kind: "entry",
			Seq:  2,
			Turn: schema.Turn{
				Kind: schema.TurnToolResults,
				Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentToolResult,
							ToolResult: &llm.ToolResultData{
								ToolCallID: "mc-1",
								Content:    "main.go\ngo.mod\n",
								DurationMS: 50,
							},
						},
					},
				},
				Timestamp: ts.Add(2 * time.Second),
			},
		},
		// Round 2: ASSISTANT (follow-up, no tool call)
		{
			Kind: "entry",
			Seq:  3,
			Turn: schema.Turn{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Found 2 files: main.go and go.mod."},
					},
				},
				Timestamp: ts.Add(3 * time.Second),
				Usage:     llm.Usage{InputTokens: 150, OutputTokens: 25},
			},
		},
	}

	traj := Convert(header, entries)

	// USER + ASSISTANT/TOOL_RESULTS (merged) + ASSISTANT = 3 steps
	if len(traj.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(traj.Steps))
	}

	// Verify sequential step IDs
	for i, step := range traj.Steps {
		expected := i + 1
		if step.StepID != expected {
			t.Errorf("Steps[%d].StepID = %d, want %d", i, step.StepID, expected)
		}
	}

	// Step 1: user
	if traj.Steps[0].Source != "user" {
		t.Errorf("Steps[0].Source = %q, want %q", traj.Steps[0].Source, "user")
	}

	// Step 2: agent with tool call + observation
	s2 := traj.Steps[1]
	if s2.Source != "agent" {
		t.Errorf("Steps[1].Source = %q, want %q", s2.Source, "agent")
	}
	if len(s2.ToolCalls) != 1 {
		t.Fatalf("Steps[1] len(ToolCalls) = %d, want 1", len(s2.ToolCalls))
	}
	if s2.Observation == nil {
		t.Fatal("Steps[1].Observation is nil")
	}
	if s2.Metrics.CachedTokens != 20 {
		t.Errorf("Steps[1].Metrics.CachedTokens = %d, want 20", s2.Metrics.CachedTokens)
	}

	// Step 3: agent follow-up (no tools)
	s3 := traj.Steps[2]
	if s3.Source != "agent" {
		t.Errorf("Steps[2].Source = %q, want %q", s3.Source, "agent")
	}
	if s3.Message != "Found 2 files: main.go and go.mod." {
		t.Errorf("Steps[2].Message = %q, want follow-up text", s3.Message)
	}
	if len(s3.ToolCalls) != 0 {
		t.Errorf("Steps[2] len(ToolCalls) = %d, want 0", len(s3.ToolCalls))
	}
	if s3.Observation != nil {
		t.Errorf("Steps[2].Observation should be nil")
	}

	// FinalMetrics: totals from both assistant turns
	fm := traj.FinalMetrics
	if fm == nil {
		t.Fatal("FinalMetrics is nil")
	}
	if fm.TotalSteps != 3 {
		t.Errorf("FinalMetrics.TotalSteps = %d, want 3", fm.TotalSteps)
	}
	// 100 + 150 = 250 prompt tokens
	if fm.TotalPromptTokens != 250 {
		t.Errorf("FinalMetrics.TotalPromptTokens = %d, want 250", fm.TotalPromptTokens)
	}
	// 40 + 25 = 65 completion tokens
	if fm.TotalCompletionTokens != 65 {
		t.Errorf("FinalMetrics.TotalCompletionTokens = %d, want 65", fm.TotalCompletionTokens)
	}
	// Only step 2 has cached tokens: 20
	if fm.TotalCachedTokens != 20 {
		t.Errorf("FinalMetrics.TotalCachedTokens = %d, want 20", fm.TotalCachedTokens)
	}
}

func TestConvertToATIF_RawUsage(t *testing.T) {
	header := transcript.Header{SessionID: "sess-raw", Model: "gpt-5.3-codex"}
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 0, Turn: schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hello"}},
			},
			Timestamp: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
			Usage: llm.Usage{
				InputTokens:  50,
				OutputTokens: 10,
				Raw: map[string]any{
					"prompt_tokens_details": map[string]any{"cached_tokens": 25},
					"some_provider_field":   "custom_value",
				},
			},
		}},
	}

	traj := Convert(header, entries)
	if len(traj.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(traj.Steps))
	}
	m := traj.Steps[0].Metrics
	if m == nil {
		t.Fatal("Metrics is nil")
	}
	if m.Extra == nil {
		t.Fatal("Metrics.Extra is nil")
	}
	rawUsage, ok := m.Extra["raw_usage"].(map[string]any)
	if !ok {
		t.Fatalf("raw_usage type = %T, want map[string]any", m.Extra["raw_usage"])
	}
	if rawUsage["some_provider_field"] != "custom_value" {
		t.Errorf("raw_usage[some_provider_field] = %v", rawUsage["some_provider_field"])
	}
}

func TestConvertToATIF_WebSearchRaw(t *testing.T) {
	header := transcript.Header{SessionID: "sess-ws-raw", Model: "gpt-5.3-codex"}
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 0, Turn: schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "searching"},
					{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{
						Query: "golang testing",
						Raw:   json.RawMessage(`{"provider_data":"test123"}`),
					}},
				},
			},
			Timestamp: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		}},
	}

	traj := Convert(header, entries)
	if len(traj.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(traj.Steps))
	}
	searches, ok := traj.Steps[0].Extra["web_searches"].([]any)
	if !ok {
		t.Fatalf("web_searches type = %T", traj.Steps[0].Extra["web_searches"])
	}
	if len(searches) != 1 {
		t.Fatalf("len(web_searches) = %d, want 1", len(searches))
	}
	ws := searches[0].(map[string]any)
	if ws["query"] != "golang testing" {
		t.Errorf("query = %v", ws["query"])
	}
	rawMsg, ok := ws["raw"].(json.RawMessage)
	if !ok {
		t.Fatalf("raw type = %T, want json.RawMessage", ws["raw"])
	}
	if string(rawMsg) != `{"provider_data":"test123"}` {
		t.Errorf("raw = %s", rawMsg)
	}
}

func TestConvertToATIF_ResponsesProviderHandlesRedacted(t *testing.T) {
	header := transcript.Header{SessionID: "sess-responses-redacted", Model: "gpt-5.3-codex"}
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 0, Turn: schema.Turn{
			Kind:                            schema.TurnAssistant,
			Message:                         llm.Assistant("done"),
			ResponseID:                      "resp_raw_phase11",
			ResponseIDHash:                  "cont-handle-v1:response_id:phase11",
			ResponseEndpoint:                "openai_responses",
			ResponseStorageScopeFingerprint: "scope-phase11",
			ResponseRequestFingerprint:      "request-phase11",
			ResponseContextMarker:           "ctx-phase11",
			Timestamp:                       time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		}},
	}

	traj := Convert(header, entries)
	if len(traj.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(traj.Steps))
	}
	extra := traj.Steps[0].Extra
	if _, ok := extra["response_id"]; ok {
		t.Fatalf("default ATIF export leaked raw response_id: %#v", extra["response_id"])
	}
	wantExtra := map[string]string{
		"response_id_hash":                   "cont-handle-v1:response_id:phase11",
		"response_endpoint":                  "openai_responses",
		"response_storage_scope_fingerprint": "scope-phase11",
		"response_request_fingerprint":       "request-phase11",
		"response_context_marker":            "ctx-phase11",
	}
	for key, want := range wantExtra {
		if got := extra[key]; got != want {
			t.Fatalf("extra[%s] = %#v, want %q", key, got, want)
		}
	}
}

func TestConvertToATIF_TurnSystem(t *testing.T) {
	header := transcript.Header{SessionID: "sess-sys", Model: "gpt-5.3-codex"}
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 0, Turn: schema.Turn{
			Kind:      schema.TurnSystem,
			Message:   llm.User("System message content"),
			Timestamp: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		}},
	}

	traj := Convert(header, entries)
	if len(traj.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(traj.Steps))
	}
	step := traj.Steps[0]
	if step.Source != "system" {
		t.Errorf("Source = %q, want system", step.Source)
	}
	if step.Message != "System message content" {
		t.Errorf("Message = %q", step.Message)
	}
}
