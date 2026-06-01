package apptranscript

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestPreludeTurnRendersFullToolsOnly(t *testing.T) {
	strict := true
	turn := PreludeTurn(agent.TranscriptHeader{SystemPrompt: "You are Serf."}, &agent.TranscriptAPICall{
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
	turn := PreludeTurn(agent.TranscriptHeader{SystemPrompt: "You are Serf."}, &agent.TranscriptAPICall{
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
	w, err := agent.NewTranscriptWriter(path, agent.TranscriptHeader{
		SessionID:    "th_1",
		SystemPrompt: "You are Serf.",
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	if err := w.AppendAPICall(agent.TranscriptAPICall{
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
	start := ProjectTurn("turn_1", 1, agent.Turn{
		Kind: agent.TurnAssistant,
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
	done := ProjectTurn("turn_2", 2, agent.Turn{
		Kind: agent.TurnToolResults,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_read",
				Content:    "ok",
			},
		}}},
	}, toolNames, nil)

	if len(done) != 1 || done[0].ToolName != "read_file" || done[0].Output != "ok" {
		t.Fatalf("tool result=%+v", done)
	}
}
