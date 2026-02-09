package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
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
