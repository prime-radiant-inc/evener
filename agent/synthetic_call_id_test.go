package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// idlessReadCall scripts an assistant tool call that carries NO provider id (as
// Gemini's calls do not), with fixed arguments so every such call hashes the
// same.
func idlessReadCall() llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			Name:      "read_file",
			Arguments: json.RawMessage(`{"file_path":"/tmp/idless"}`),
		}},
	}}}
}

func fixedStep(r llm.Response) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response { return r }
}

// Two provider-id-less calls with identical arguments, in separate turns, must
// be told apart by their synthesized ids all the way into the transcript. The
// id is what the live overlay's per-call map, the recorded item and the phone's
// transcript list key identify a call by, so one shared id would collapse both
// calls into one row (and one list key).
func TestSynthesizedCallID_IsUniqueAcrossTurnsAndSurvivesRecording(t *testing.T) {
	t.Parallel()
	s := newSession(t,
		withSteps(
			fixedStep(idlessReadCall()), fixedStep(communicateResponse(true, "done")),
			fixedStep(idlessReadCall()), fixedStep(communicateResponse(true, "done")),
		),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: t.TempDir()}),
		withoutGitSnapshot(),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := s.ProcessInput(ctx, "go", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if _, err := s.ProcessInput(ctx, "again", nil); err != nil {
		t.Fatalf("ProcessInput second turn: %v", err)
	}
	s.Close()

	path := s.TranscriptPath()
	if path == "" {
		t.Fatal("session has no transcript path; StateDir was not set")
	}
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, 64<<20, func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(turnID, turnIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	// The result item is the recorded call the synthesized id lands on.
	var recorded []string
	for i := range turns {
		for j := range turns[i].Items {
			item := turns[i].Items[j]
			if item.Type == "commandExecution" && item.ToolName == "read_file" && item.CallID != "" {
				recorded = append(recorded, item.CallID)
			}
		}
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded read_file call ids = %v, want the two id-less calls", recorded)
	}
	if recorded[0] == recorded[1] {
		t.Fatalf("two id-less calls with identical arguments recorded the same id %q", recorded[0])
	}
}
