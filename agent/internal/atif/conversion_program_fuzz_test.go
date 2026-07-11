//go:build serffuzz

package atif

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzATIFConversionProgram drives the complete typed transcript-to-ATIF
// conversion boundary. The program keeps the durable transcript structure
// valid while fuzzing user-controlled text, response handles, and usage values.
// It covers both provider-handle export policies, response continuation
// metadata, rich assistant content, merged and orphaned tool results, and
// aggregate metrics without touching a provider, filesystem, or clock.
func FuzzATIFConversionProgram(f *testing.F) {
	f.Add("request body", "resp_local_1", uint8(1))
	f.Add("unicode text", "response_2", uint8(31))
	f.Add("", "", uint8(255))

	f.Fuzz(func(t *testing.T, text, responseID string, value uint8) {
		text = atifProgramString(text, "body", 512)
		responseID = atifProgramString(responseID, "response", 128)
		usage := int(value%63) + 1
		cacheRead := usage + 1
		reasoning := usage + 2
		estimated := usage + 3
		cacheWrite := usage + 4
		cacheWrite1h := usage + 5

		for _, tc := range []struct {
			input string
			want  ProviderHandleMode
			valid bool
		}{
			{input: "", want: ProviderHandleModeRedacted, valid: true},
			{input: " redacted ", want: ProviderHandleModeRedacted, valid: true},
			{input: "raw-local", want: ProviderHandleModeRawLocal, valid: true},
			{input: "invalid-mode", valid: false},
		} {
			got, err := NormalizeProviderHandleMode(tc.input)
			if tc.valid {
				if err != nil || got != tc.want {
					t.Fatalf("NormalizeProviderHandleMode(%q) = %q, %v; want %q, nil", tc.input, got, err, tc.want)
				}
			} else if err == nil || got != "" {
				t.Fatalf("NormalizeProviderHandleMode(%q) = %q, %v; want invalid", tc.input, got, err)
			}
		}

		arguments, err := json.Marshal(map[string]string{"input": text})
		if err != nil {
			t.Fatalf("marshal tool arguments: %v", err)
		}
		timestamp := time.Date(2026, time.June, 1, 10, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
		header := transcript.Header{
			SessionID:        "session",
			Model:            "model",
			ProfileID:        "profile",
			BuildVersion:     "v1",
			WorkingDir:       "/workspace",
			ParentSessionID:  "parent-session",
			ParentToolCallID: "parent-call",
			Depth:            2,
			Task:             "child task",
			SystemPrompt:     "system prompt",
		}

		entries := []transcript.Entry{
			atifProgramEntry(0, schema.Turn{
				Kind:      schema.TurnUserInput,
				Timestamp: timestamp,
				Message:   llm.User(text),
			}),
			atifProgramEntry(1, schema.Turn{
				Kind:                            schema.TurnAssistant,
				Timestamp:                       timestamp.Add(time.Second),
				ResponseID:                      responseID,
				ResponseIDHash:                  "present-hash",
				ResponseProvider:                "provider",
				ResponseModel:                   "response-model",
				ResponseRequestModel:            "request-model",
				ResponseEndpoint:                "responses",
				ResponseStorageScopeFingerprint: "response-scope",
				ResponseRequestFingerprint:      "response-fingerprint",
				ResponseContextMarker:           "response-context",
				Usage: llm.Usage{
					InputTokens:              usage,
					OutputTokens:             usage + 1,
					CacheReadTokens:          &cacheRead,
					ReasoningTokens:          &reasoning,
					ReasoningTokensEstimated: &estimated,
					CacheWriteTokens:         &cacheWrite,
					CacheWrite1hTokens:       &cacheWrite1h,
					Raw:                      map[string]any{"source": "program", "value": usage},
				},
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "prefix", Phase: "commentary"},
					{Kind: llm.ContentText, Text: text, Phase: "final_answer"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "tool-valid", Name: "shell", Arguments: arguments}},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "tool-invalid", Name: "shell", Arguments: json.RawMessage("{")}},
					{Kind: llm.ContentToolCall},
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reason one"}},
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reason two", Signature: "signature"}},
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Redacted: true}},
					{Kind: llm.ContentThinking},
					{Kind: llm.ContentRedThinking},
					{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: text, Raw: json.RawMessage(arguments)}},
					{Kind: llm.ContentWebSearch},
				}},
			}),
			atifProgramEntry(2, schema.Turn{
				Kind:      schema.TurnToolResults,
				Timestamp: timestamp.Add(2 * time.Second),
				Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "ignored"},
					{Kind: llm.ContentToolResult},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "tool-valid", Content: text, IsError: true, DurationMS: int64(usage)}},
				}},
			}),
			atifProgramEntry(3, schema.Turn{
				Kind:           schema.TurnAssistant,
				Timestamp:      timestamp.Add(3 * time.Second),
				ResponseID:     "other-response",
				ResponseIDHash: "other-hash",
				Message:        llm.Assistant("second assistant"),
			}),
			atifProgramEntry(4, schema.Turn{
				Kind:      schema.TurnAssistant,
				Timestamp: timestamp.Add(4 * time.Second),
				Message:   llm.Assistant("third assistant"),
			}),
			atifProgramEntry(5, schema.Turn{Kind: schema.TurnSystem, Message: llm.System("untimed system")}),
			atifProgramEntry(6, schema.Turn{Kind: schema.TurnSteering, Timestamp: timestamp.Add(5 * time.Second), Message: llm.User("steer")}),
			atifProgramEntry(7, schema.Turn{Kind: schema.TurnSystem, Timestamp: timestamp.Add(6 * time.Second), Message: llm.System("system")}),
			atifProgramEntry(8, schema.Turn{Kind: schema.TurnCheckpoint, Timestamp: timestamp.Add(7 * time.Second), Message: llm.Assistant("checkpoint")}),
			atifProgramEntry(9, schema.Turn{Kind: schema.TurnSummary, Timestamp: timestamp.Add(8 * time.Second), Message: llm.Assistant("summary")}),
			atifProgramEntry(10, schema.Turn{
				Kind:      schema.TurnToolResults,
				Timestamp: timestamp.Add(9 * time.Second),
				Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "ignored"},
					{Kind: llm.ContentToolResult},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "orphan", Content: text, IsError: true, DurationMS: int64(usage + 1)}},
				}},
			}),
			atifProgramEntry(11, schema.Turn{
				Kind:      schema.TurnToolResults,
				Timestamp: timestamp.Add(10 * time.Second),
				Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "ignored"},
					{Kind: llm.ContentToolResult},
				}},
			}),
		}
		calls := []transcript.APICall{
			{},
			{
				PreviousResponseIDHash: "present-hash",
				ConversationIDHash:     "conversation-hash",
				Request: llm.APILogRequest{
					HistoryMode:             llm.HistoryModeResponsesDelta,
					EndpointFamily:          "responses",
					RequestFingerprint:      "request-fingerprint",
					ContextMarker:           "request-context",
					StorageScopeFingerprint: "request-scope",
					StoragePolicyLabel:      "storage-policy",
				},
			},
			{PreviousResponseIDHash: "missing-hash", HistoryMode: llm.HistoryModeFullHistoryFallback},
			{HistoryMode: llm.HistoryModeResponsesDelta},
		}

		if got := Convert(transcript.Header{}, nil); got.Agent.Version != "unknown" || got.SchemaVersion != "ATIF-v1.7" {
			t.Fatalf("empty Convert() = %+v; want unknown ATIF trajectory", got.Agent)
		}
		if got := ConvertWithOptions(header, entries, Options{ProviderHandles: ProviderHandleMode("invalid-mode")}); got.SchemaVersion != "ATIF-v1.7" {
			t.Fatalf("ConvertWithOptions invalid mode did not produce ATIF")
		}

		redacted := ConvertTranscriptWithOptions(header, entries, calls, Options{ProviderHandles: ProviderHandleModeRedacted})
		rawLocal := ConvertTranscriptWithOptions(header, entries, calls, Options{ProviderHandles: ProviderHandleModeRawLocal})
		atifProgramAssertTrajectory(t, redacted, rawLocal, responseID, usage, cacheRead)
	})
}

func atifProgramEntry(seq int, turn schema.Turn) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: turn}
}

func atifProgramString(value, fallback string, limit int) string {
	if value == "" {
		return fallback
	}
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func atifProgramAssertTrajectory(t *testing.T, redacted, rawLocal Trajectory, responseID string, usage, cacheRead int) {
	t.Helper()
	for name, trajectory := range map[string]Trajectory{"redacted": redacted, "raw-local": rawLocal} {
		if trajectory.SchemaVersion != "ATIF-v1.7" || trajectory.SessionID != "session" {
			t.Fatalf("%s root = %+v; want ATIF session", name, trajectory)
		}
		if trajectory.FinalMetrics == nil || trajectory.FinalMetrics.TotalSteps != len(trajectory.Steps) {
			t.Fatalf("%s final metrics = %+v, steps=%d", name, trajectory.FinalMetrics, len(trajectory.Steps))
		}
		if trajectory.FinalMetrics.TotalPromptTokens != usage || trajectory.FinalMetrics.TotalCompletionTokens != usage+1 || trajectory.FinalMetrics.TotalCachedTokens != cacheRead {
			t.Fatalf("%s metrics = %+v", name, trajectory.FinalMetrics)
		}
		for i, step := range trajectory.Steps {
			if step.StepID != i+1 || step.Extra == nil {
				t.Fatalf("%s step %d = %+v; want ordered non-nil extra", name, i, step)
			}
		}
		if _, err := json.Marshal(trajectory); err != nil {
			t.Fatalf("%s ATIF trajectory does not marshal: %v", name, err)
		}
	}

	for _, key := range []string{"working_dir", "parent_session_id", "parent_tool_call_id", "depth", "task", "system_prompt"} {
		if _, ok := rawLocal.Extra[key]; !ok {
			t.Fatalf("root extra missing %q: %#v", key, rawLocal.Extra)
		}
	}

	first := rawLocal.Steps[1]
	if first.Message == "" || first.ReasoningContent != "reason one\nreason two" || len(first.ToolCalls) != 2 || first.Observation == nil || len(first.Observation.Results) != 1 {
		t.Fatalf("rich assistant step = %+v", first)
	}
	if first.Extra["response_id"] != responseID || first.Extra["previous_response_id"] != responseID || first.Extra["conversation_id_unavailable"] != true {
		t.Fatalf("raw-local response handles = %#v", first.Extra)
	}
	if first.Extra["request_history_mode"] != string(llm.HistoryModeResponsesDelta) || first.Extra["tool_errors"].(map[string]bool)["tool-valid"] != true || first.Extra["tool_durations_ms"].(map[string]int64)["tool-valid"] != int64(usage) {
		t.Fatalf("rich assistant metadata = %#v", first.Extra)
	}
	if _, exposed := redacted.Steps[1].Extra["response_id"]; exposed {
		t.Fatalf("redacted mode exposed raw response handle: %#v", redacted.Steps[1].Extra)
	}

	second := rawLocal.Steps[2]
	if second.Extra["previous_response_id_unavailable"] != true || second.Extra["request_history_mode"] != string(llm.HistoryModeFullHistoryFallback) {
		t.Fatalf("unavailable response metadata = %#v", second.Extra)
	}
	if rawLocal.Steps[3].Extra["request_history_mode"] != string(llm.HistoryModeResponsesDelta) {
		t.Fatalf("history-only metadata = %#v", rawLocal.Steps[3].Extra)
	}

	orphan := rawLocal.Steps[len(rawLocal.Steps)-2]
	if orphan.Observation == nil || orphan.Observation.Results[0].SourceCallID != "" || orphan.Extra["orphaned_source_call_ids"].([]string)[0] != "orphan" || orphan.Extra["tool_errors"].(map[string]bool)["orphan"] != true {
		t.Fatalf("orphaned tool result = %+v", orphan)
	}
	if rawLocal.Steps[len(rawLocal.Steps)-1].Observation != nil {
		t.Fatalf("empty orphan tool results unexpectedly created an observation: %+v", rawLocal.Steps[len(rawLocal.Steps)-1])
	}
}
