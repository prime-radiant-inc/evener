package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/prime-radiant/serf/internal/agent"
	"github.com/prime-radiant/serf/internal/llm"
	_ "github.com/prime-radiant/serf/internal/llm/providers/openai"
)

// TestNewSessionFromEnv verifies that we can create a working session
// from environment variables. This is the core wiring test.
func TestNewSessionFromEnv(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	profile := agent.NewOpenAIProfile("gpt-5-mini-2025-08-07")
	env := agent.NewLocalExecutionEnvironment(t.TempDir())

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Session should emit SESSION_START on creation.
	select {
	case ev := <-sess.Events():
		if ev.Kind != agent.EventSessionStart {
			t.Fatalf("expected SESSION_START, got %s", ev.Kind)
		}
	default:
		t.Fatal("expected SESSION_START event")
	}
}

// TestProcessInputSimpleTask sends a simple prompt to the model and verifies
// that the session returns a non-empty text response.
func TestProcessInputSimpleTask(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	profile := agent.NewOpenAIProfile("gpt-5-mini-2025-08-07")
	env := agent.NewLocalExecutionEnvironment(t.TempDir())

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{
		MaxToolRoundsPerInput: 5,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Drain SESSION_START.
	<-sess.Events()

	ctx := context.Background()
	result, err := sess.ProcessInput(ctx, "Reply with exactly: HELLO SERF")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(strings.ToUpper(result), "HELLO SERF") {
		t.Fatalf("expected response to contain 'HELLO SERF', got: %q", result)
	}
}

// TestProcessInputWithToolUse sends a task that requires the model to use a tool
// (write a file), then verifies the file was created.
func TestProcessInputWithToolUse(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	client, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}

	tmpDir := t.TempDir()
	profile := agent.NewOpenAIProfile("gpt-5-mini-2025-08-07")
	env := agent.NewLocalExecutionEnvironment(tmpDir)

	sess, err := agent.NewSession(client, profile, env, agent.SessionConfig{
		MaxToolRoundsPerInput: 10,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Drain SESSION_START.
	<-sess.Events()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "Create a file called hello.txt in the working directory "+tmpDir+" containing exactly the text 'Hello from serf'. Use the write_file tool. Do not explain, just create the file.")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	content, err := os.ReadFile(tmpDir + "/hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "Hello from serf") {
		t.Fatalf("expected file to contain 'Hello from serf', got: %q", string(content))
	}
}
