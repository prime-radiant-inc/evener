//go:build !short

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// integrationTestModel is the OpenAI model used by live integration tests.
// Codex/ChatGPT-account routes reject pinned snapshot IDs like
// "gpt-5-mini-2025-08-07" with HTTP 400, so we use the alias that's known to
// work via OAuth. Update here to retarget all live integration tests at once.
const integrationTestModel = "gpt-5.4-mini"

// intentionallyShortTimeout is the independent variable in
// TestIntegration_Timeout, not a hang guard: it must be far shorter than any
// real completion so ProcessInput is forced to return via context
// cancellation rather than success, proving the session honors ctx.
const intentionallyShortTimeout = 1 * time.Second

func skipWithoutAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run live agent integration tests")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
}

func integrationSession(t *testing.T) *Session {
	t.Helper()
	client := liveRegistryClient(t)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile(integrationTestModel), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 20,
		MaxTurns:              5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// drainEvents starts a goroutine that drains the session's event channel to
// prevent blocking. Returns a function that stops draining and returns all
// collected events.
func drainEvents(sess *Session) func() []events.SessionEvent {
	var evs []events.SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
		close(done)
	}()
	return func() []events.SessionEvent {
		<-done
		mu.Lock()
		defer mu.Unlock()
		return evs
	}
}

func TestIntegration_SimpleFileCreation(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	collectEvents := drainEvents(sess)

	// TRIPWIRE: bounds a live OpenAI ProcessInput call (EVENER_LIVE_TESTS=1);
	// only fires on a genuine hang or provider outage, never ordinary model
	// latency.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := sess.ProcessInput(ctx, "Create a file called hello.txt containing exactly 'Hello, World!' and nothing else. Do not include any extra newlines or whitespace.", nil)
	sess.Close()
	evs := collectEvents()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(sess.env.WorkingDirectory(), "hello.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if strings.TrimSpace(string(data)) != "Hello, World!" {
		t.Fatalf("unexpected content: %q", string(data))
	}

	// Verify we got tool call events (the model used tools).
	var sawToolCall bool
	for _, ev := range evs {
		if ev.Kind == events.EventToolCallStart {
			sawToolCall = true
			break
		}
	}
	if !sawToolCall {
		t.Log("warning: no TOOL_CALL_START events observed (model may have responded without tools)")
	}
	t.Logf("file content: %q", string(data))
}

func TestIntegration_FileReadAndEdit(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	_ = drainEvents(sess)

	// Pre-create a file in the session's working directory.
	workDir := sess.env.WorkingDirectory()
	origContent := "The quick brown fox jumps over the lazy dog.\n"
	if err := os.WriteFile(filepath.Join(workDir, "story.txt"), []byte(origContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// TRIPWIRE: bounds a live OpenAI ProcessInput call (EVENER_LIVE_TESTS=1)
	// that also does a file read+edit; only fires on a genuine hang or
	// provider outage.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, err := sess.ProcessInput(ctx, "Read the file story.txt, then edit it to replace 'fox' with 'cat'. Do not change anything else in the file.", nil)
	sess.Close()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workDir, "story.txt"))
	if err != nil {
		t.Fatalf("reading edited file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "cat") {
		t.Fatalf("edit not applied: %q", content)
	}
	if strings.Contains(content, "fox") {
		t.Fatalf("original text still present: %q", content)
	}
	t.Logf("edited content: %q", content)
}

func TestIntegration_ShellCommand(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	collectEvents := drainEvents(sess)

	// TRIPWIRE: bounds a live OpenAI ProcessInput call (EVENER_LIVE_TESTS=1);
	// only fires on a genuine hang or provider outage, never ordinary model
	// latency.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	output, err := sess.ProcessInput(ctx, "Run the shell command: echo 'hello from shell'. Report the output.", nil)
	sess.Close()
	evs := collectEvents()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Verify the model called a shell tool by checking events.
	var shellCalled bool
	for _, ev := range evs {
		if d, ok := ev.Data.(events.ToolCallStartData); ok {
			// OpenAI profile maps "shell" to "exec_command"
			if d.ToolName == "shell" || d.ToolName == "exec_command" {
				shellCalled = true
				break
			}
		}
	}
	if !shellCalled {
		t.Fatalf("model did not call shell tool; output: %q", output)
	}

	// Verify the tool output contains the expected string.
	var sawHello bool
	for _, ev := range evs {
		if d, ok := ev.Data.(events.ToolCallEndData); ok {
			if strings.Contains(d.Output, "hello from shell") {
				sawHello = true
				break
			}
		}
	}
	if !sawHello {
		t.Fatal("shell tool output did not contain 'hello from shell'")
	}
	t.Logf("output: %q", output)
}

func TestIntegration_LargeFileReadDoesNotCrash(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	_ = drainEvents(sess)

	// Create a large file (>50K chars) in the working directory.
	workDir := sess.env.WorkingDirectory()
	line := strings.Repeat("A", 100) + "\n"
	bigContent := strings.Repeat(line, 600) // ~60K chars
	if err := os.WriteFile(filepath.Join(workDir, "big.txt"), []byte(bigContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// TRIPWIRE: bounds a live OpenAI ProcessInput call (EVENER_LIVE_TESTS=1)
	// reading a large file; only fires on a genuine hang or provider outage.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The key assertion is that ProcessInput completes without crashing or
	// hanging, even though the file is very large.
	_, err := sess.ProcessInput(ctx, "Read the file big.txt and tell me how many lines it has.", nil)
	sess.Close()

	if err != nil {
		t.Fatalf("ProcessInput on large file: %v", err)
	}
	t.Log("large file read completed without crash")
}

func TestIntegration_Steering(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	collectEvents := drainEvents(sess)

	// TRIPWIRE: bounds a live OpenAI ProcessInput call (EVENER_LIVE_TESTS=1)
	// plus steering injection; only fires on a genuine hang or provider
	// outage.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Queue steering before ProcessInput. The session drains steering at
	// the start of processOneInput, before the first LLM call.
	sess.Steer("IMPORTANT: Before doing anything else, create a file called steered.txt with the content 'steering works'.")

	_, err := sess.ProcessInput(ctx, "Create a file called main.txt with the content 'main task'.", nil)
	sess.Close()
	evs := collectEvents()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Verify steering was injected.
	var sawSteering bool
	for _, ev := range evs {
		if ev.Kind == events.EventSteeringInjected {
			sawSteering = true
			break
		}
	}
	if !sawSteering {
		t.Fatal("no STEERING_INJECTED event observed")
	}

	// Check that the steered file was created.
	workDir := sess.env.WorkingDirectory()
	data, err := os.ReadFile(filepath.Join(workDir, "steered.txt"))
	if err != nil {
		t.Fatalf("steered file not created: %v", err)
	}
	if !strings.Contains(string(data), "steering") {
		t.Fatalf("steered file has unexpected content: %q", string(data))
	}
	t.Logf("steered.txt content: %q", string(data))
}

func TestIntegration_Delegate(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)

	// Delegate flow needs more tool rounds: the parent calls delegate and may
	// inspect the resulting job while the delegated session uses several rounds.
	client := liveRegistryClient(t)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile(integrationTestModel), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 40,
		MaxTurns:              5,
	})
	if err != nil {
		t.Fatal(err)
	}
	collectEvents := drainEvents(sess)

	// TRIPWIRE: bounds a live OpenAI ProcessInput call (EVENER_LIVE_TESTS=1)
	// that also drives a delegated subagent through several rounds; only
	// fires on a genuine hang or provider outage.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx,
		"Use the delegate tool with the task: "+
			"'Create a file called delegate_output.txt containing exactly the text: created by delegate'. "+
			"Wait for the delegate job to finish and inspect it if needed. Do not use the task_list tool.", nil)
	sess.Close()
	evs := collectEvents()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Verify delegate was called by checking events.
	var sawDelegate bool
	for _, ev := range evs {
		if d, ok := ev.Data.(events.ToolCallStartData); ok {
			if d.ToolName == "delegate" {
				sawDelegate = true
				break
			}
		}
	}
	if !sawDelegate {
		t.Fatal("model did not call delegate tool")
	}

	// The delegate shares the parent working directory, so the file should
	// be there. Search recursively as a fallback in case the model used a
	// subdirectory.
	workDir := sess.env.WorkingDirectory()
	target := filepath.Join(workDir, "delegate_output.txt")
	data, err := os.ReadFile(target)
	if err != nil {
		// Walk the temp dir tree to find the file.
		var found string
		_ = filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil //nolint:nilerr // best-effort search: skip unreadable entries and keep walking
			}
			if info.Name() == "delegate_output.txt" {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found == "" {
			t.Fatalf("subagent file not created anywhere under %s", workDir)
		}
		data, err = os.ReadFile(found)
		if err != nil {
			t.Fatalf("reading found subagent file: %v", err)
		}
		t.Logf("file found at %s (not at root)", found)
	}
	if !strings.Contains(strings.ToLower(string(data)), "created by delegate") {
		t.Fatalf("unexpected delegate file content: %q", string(data))
	}
	t.Logf("delegate_output.txt content: %q", string(data))
}

func TestIntegration_Timeout(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	_ = drainEvents(sess)

	// Use a very short timeout. The LLM call won't complete in 1 second.
	ctx, cancel := context.WithTimeout(context.Background(), intentionallyShortTimeout)
	defer cancel()

	start := time.Now()
	_, err := sess.ProcessInput(ctx, "Write a detailed 5000-word essay about the history of computing.", nil)

	elapsed := time.Since(start)
	// Session is closed by ProcessInput on context cancellation.

	if err == nil {
		t.Fatal("expected error from short timeout, got nil")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected context error, got: %v", err)
	}
	// Verify we returned reasonably quickly (within 10 seconds of the 1s timeout).
	if elapsed > 15*time.Second {
		t.Fatalf("ProcessInput took too long after timeout: %v", elapsed)
	}
	t.Logf("timeout error after %v: %v", elapsed, err)
}

// liveRegistryClient builds the client these live tests dispatch through: the
// real registry, so the developer's environment and user layer name the
// instances exactly as a real run would.
func liveRegistryClient(t *testing.T) *llm.Client {
	t.Helper()
	r, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}
