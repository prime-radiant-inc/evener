package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/atif"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestExportATIF_WritesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "sessions", "test-sess.transcript.jsonl")

	ts := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	header := transcript.Header{
		SessionID:    "test-sess",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0",
		ProfileID:    "openai",
		CreatedAt:    ts,
	}

	tw, err := transcript.NewWriter(transcriptPath, header)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	err = tw.Append(schema.Turn{
		Kind:      schema.TurnUserInput,
		Message:   llm.User("Hello!"),
		Timestamp: ts,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	outputPath := filepath.Join(dir, "output", "trajectory.json")
	if err := exportATIF(transcriptPath, outputPath); err != nil {
		t.Fatalf("exportATIF: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var traj atif.Trajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if traj.SessionID != "test-sess" {
		t.Errorf("SessionID = %q, want %q", traj.SessionID, "test-sess")
	}
	if traj.SchemaVersion != "ATIF-v1.7" {
		t.Errorf("SchemaVersion = %q, want %q", traj.SchemaVersion, "ATIF-v1.7")
	}
	if len(traj.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1", len(traj.Steps))
	}
	if traj.Steps[0].Source != "user" {
		t.Errorf("Steps[0].Source = %q, want %q", traj.Steps[0].Source, "user")
	}
	if traj.Steps[0].Message != "Hello!" {
		t.Errorf("Steps[0].Message = %q, want %q", traj.Steps[0].Message, "Hello!")
	}
}
