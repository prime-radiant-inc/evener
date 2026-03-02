package agent

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestConvertToATIF_SimpleConversation(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	header := TranscriptHeader{
		SessionID:    "sess-001",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0-abc1234",
		ProfileID:    "openai",
		WorkingDir:   "/app",
		CreatedAt:    ts,
	}

	entries := []TranscriptEntry{
		{
			Kind: "entry",
			Seq:  0,
			Turn: Turn{
				Kind:      TurnUserInput,
				Message:   llm.User("Hello, world!"),
				Timestamp: ts,
			},
		},
		{
			Kind: "entry",
			Seq:  1,
			Turn: Turn{
				Kind:      TurnAssistant,
				Message:   llm.Assistant("Hi there! How can I help?"),
				Timestamp: ts.Add(2 * time.Second),
				Usage: llm.Usage{
					InputTokens:  100,
					OutputTokens: 50,
				},
			},
		},
	}

	traj := ConvertToATIF(header, entries)

	// --- Root trajectory fields ---
	if traj.SchemaVersion != "ATIF-v1.6" {
		t.Errorf("SchemaVersion = %q, want %q", traj.SchemaVersion, "ATIF-v1.6")
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
