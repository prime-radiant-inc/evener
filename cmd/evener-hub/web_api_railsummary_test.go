package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
)

func TestHubThreadRailSummaryReturnsTurnTuples(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rail-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: start, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	w.SyncInterval = time.Hour

	// Turn 1: user input
	if err := w.Append(schema.Turn{
		Kind:      schema.TurnUserInput,
		Message:   llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hello"}}},
		Timestamp: start,
	}); err != nil {
		t.Fatal(err)
	}
	// Turn 2: assistant with usage
	if err := w.Append(schema.Turn{
		Kind:      schema.TurnAssistant,
		Message:   llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi there"}}},
		Usage:     llm.Usage{InputTokens: 100, OutputTokens: 200, TotalTokens: 300},
		Timestamp: start.Add(5 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	// Turn 3: tool results with content (result bytes)
	if err := w.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "shell", Content: "output line"},
		}}},
		Timestamp: start.Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	// Turn 4: assistant with error
	if err := w.Append(schema.Turn{
		Kind:      schema.TurnFailure,
		Message:   llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "oops"}}},
		Usage:     llm.Usage{InputTokens: 50, OutputTokens: 0, TotalTokens: 50},
		Error:     &schema.TurnFailureInfo{Message: "provider error"},
		Timestamp: start.Add(15 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	// Turn 5: steering
	if err := w.Append(schema.Turn{
		Kind:           schema.TurnSteering,
		SteeringSource: "user",
		Message:        llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "steer"}}},
		Timestamp:      start.Add(20 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: idx}
	resp, err := hubThreadRailSummary(t.Context(), cfg, nil, appwire.RailSummaryParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubThreadRailSummary: %v", err)
	}

	if len(resp.Turns) != 5 {
		t.Fatalf("turns: got %d, want 5", len(resp.Turns))
	}

	// Turn 0: user input
	if !resp.Turns[0].UserInput {
		t.Error("turn 0: expected UserInput=true")
	}
	if resp.Turns[0].StartedAt != start.UnixMilli() {
		t.Errorf("turn 0: StartedAt=%d, want %d", resp.Turns[0].StartedAt, start.UnixMilli())
	}

	// Turn 1: assistant with tokens
	if resp.Turns[1].InTokens != 100 || resp.Turns[1].OutTokens != 200 {
		t.Errorf("turn 1: In=%d Out=%d, want 100/200", resp.Turns[1].InTokens, resp.Turns[1].OutTokens)
	}

	// Turn 2: tool results with result bytes
	if resp.Turns[2].ResultBytes == 0 {
		t.Error("turn 2: expected ResultBytes > 0")
	}

	// Turn 3: failure
	if !resp.Turns[3].Error {
		t.Error("turn 3: expected Error=true")
	}

	// Turn 4: steering
	if !resp.Turns[4].Steering {
		t.Error("turn 4: expected Steering=true")
	}

	// Totals
	if resp.TotalTokens != 350 {
		t.Errorf("TotalTokens: got %d, want 350", resp.TotalTokens)
	}
	if resp.ResultBytes == 0 {
		t.Error("ResultBytes: expected > 0")
	}
	if resp.StartedAt != start.UnixMilli() {
		t.Errorf("StartedAt: got %d, want %d", resp.StartedAt, start.UnixMilli())
	}
	if resp.EndedAt != start.Add(20*time.Second).UnixMilli() {
		t.Errorf("EndedAt: got %d, want %d", resp.EndedAt, start.Add(20*time.Second).UnixMilli())
	}
}

func TestHubThreadRailSummaryNotFound(t *testing.T) {
	root := t.TempDir()
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: idx}
	_, err := hubThreadRailSummary(t.Context(), cfg, nil, appwire.RailSummaryParams{Ref: "local:nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestHubThreadRailSummaryEmptySession(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-empty-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9ZK"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: start, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: idx}
	resp, err := hubThreadRailSummary(t.Context(), cfg, nil, appwire.RailSummaryParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubThreadRailSummary: %v", err)
	}
	if len(resp.Turns) != 0 {
		t.Errorf("turns: got %d, want 0", len(resp.Turns))
	}
	if resp.TotalTokens != 0 {
		t.Errorf("TotalTokens: got %d, want 0", resp.TotalTokens)
	}
}
