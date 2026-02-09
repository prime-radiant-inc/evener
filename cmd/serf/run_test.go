package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prime-radiant/serf/internal/agent"
	"github.com/prime-radiant/serf/internal/llm"
)

// TestRunWithArgs verifies that the run function processes a task from CLI args
// and produces output on stdout.
func TestRunWithArgs(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:    "Reply with exactly the word PONG and nothing else.",
		model:   "gpt-5-mini-2025-08-07",
		workDir: t.TempDir(),
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(strings.ToUpper(stdout.String()), "PONG") {
		t.Fatalf("expected stdout to contain PONG, got: %q", stdout.String())
	}
}

// TestRunEmitsToolEvents verifies that tool call events are written to stderr
// when the model uses tools.
func TestRunEmitsToolEvents(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:    "Create a file called test.txt in " + tmpDir + " with content 'hello'. Use the write_file tool.",
		model:   "gpt-5-mini-2025-08-07",
		workDir: tmpDir,
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}

	// stderr should contain tool call info.
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "write_file") {
		t.Fatalf("expected stderr to mention write_file tool call, got: %q", stderrStr)
	}

	// File should exist.
	content, err := os.ReadFile(tmpDir + "/test.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "hello") {
		t.Fatalf("expected file to contain 'hello', got: %q", string(content))
	}
}

// TestRunMissingTask verifies that run returns an error when no task is provided.
func TestRunMissingTask(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:   "",
		model:  "gpt-5-mini-2025-08-07",
		stdout: &stdout,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected error for empty task")
	}
	if !strings.Contains(err.Error(), "task") {
		t.Fatalf("expected error to mention 'task', got: %v", err)
	}
}

// TestRunMissingAPIKey verifies that run returns an error when no API keys
// are available.
func TestRunMissingAPIKey(t *testing.T) {
	// Temporarily clear all API key env vars.
	keys := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"}
	saved := map[string]string{}
	for _, k := range keys {
		saved[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	defer func() {
		for k, v := range saved {
			if v != "" {
				os.Setenv(k, v)
			}
		}
	}()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:   "do something",
		model:  "gpt-5-mini-2025-08-07",
		stdout: &stdout,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected error when no API keys configured")
	}
}

// --- Session resume tests ---

func TestListSessions_PrintsFormattedList(t *testing.T) {
	dir := t.TempDir()

	snap1 := agent.SessionSnapshot{
		ID:        "01JTEST000000000000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		History: []agent.Turn{
			{Kind: agent.TurnUserInput, Message: llm.User("hello")},
			{Kind: agent.TurnAssistant, Message: llm.Assistant("hi")},
		},
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
	snap2 := agent.SessionSnapshot{
		ID:        "01JTEST000000000000000002",
		ProfileID: "anthropic",
		Model:     "claude-opus-4-6",
		History: []agent.Turn{
			{Kind: agent.TurnUserInput, Message: llm.User("world")},
		},
		CreatedAt: time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		TurnCount: 1,
	}
	for _, s := range []agent.SessionSnapshot{snap1, snap2} {
		if err := agent.SaveSession(dir, s); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}

	var out bytes.Buffer
	cfg := runConfig{
		listSessions: true,
		workDir:      dir,
		stdout:       &out,
		stderr:       &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	output := out.String()
	// Most recent first (snap2).
	if !strings.Contains(output, "01JTEST000000000000000002") {
		t.Fatalf("expected snap2 ID in output, got:\n%s", output)
	}
	if !strings.Contains(output, "01JTEST000000000000000001") {
		t.Fatalf("expected snap1 ID in output, got:\n%s", output)
	}
}

func TestListSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	cfg := runConfig{
		listSessions: true,
		workDir:      dir,
		stdout:       &out,
		stderr:       &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "No saved sessions") {
		t.Fatalf("expected 'No saved sessions' message, got: %q", out.String())
	}
}

func TestResume_NonexistentID(t *testing.T) {
	dir := t.TempDir()
	cfg := runConfig{
		resume:  "NONEXISTENT",
		workDir: dir,
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT") {
		t.Fatalf("expected error to mention session ID, got: %v", err)
	}
}

func TestResumeLast_NoSessions(t *testing.T) {
	dir := t.TempDir()
	cfg := runConfig{
		resumeLast: true,
		workDir:    dir,
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error when no sessions exist")
	}
	if !strings.Contains(err.Error(), "no saved sessions") {
		t.Fatalf("expected error about no sessions, got: %v", err)
	}
}
