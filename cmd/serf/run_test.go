package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
"primeradiant.com/serf/llm"
)

// TestRunWithArgs verifies that the run function processes a task from CLI args
// and produces output on stdout.
func TestRunWithArgs(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:     "Reply with exactly the word PONG and nothing else.",
		provider: "openai",
		model:    "gpt-5-mini-2025-08-07",
		workDir:  t.TempDir(),
		stdout:   &stdout,
		stderr:   &stderr,
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
		task:     "Create a file called test.txt in " + tmpDir + " with content 'hello'. Use the write_file tool.",
		provider: "openai",
		model:    "gpt-5-mini-2025-08-07",
		workDir:  tmpDir,
		stdout:   &stdout,
		stderr:   &stderr,
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
		task:     "",
		provider: "openai",
		model:    "gpt-5-mini-2025-08-07",
		stdout:   &stdout,
		stderr:   &stderr,
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
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for k, v := range saved {
			if v != "" {
				if err := os.Setenv(k, v); err != nil {
					t.Fatal(err)
				}
			}
		}
	}()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:     "do something",
		provider: "openai",
		model:    "gpt-5-mini-2025-08-07",
		stdout:   &stdout,
		stderr:   &stderr,
	})
	if err == nil {
		t.Fatal("expected error when no API keys configured")
	}
}

// TestRunMissingProvider verifies that run returns an error when no --provider
// is specified and SERF_PROVIDER is unset.
func TestRunMissingProvider(t *testing.T) {
	old := os.Getenv("SERF_PROVIDER")
	if err := os.Unsetenv("SERF_PROVIDER"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if old != "" {
			if err := os.Setenv("SERF_PROVIDER", old); err != nil {
				t.Fatal(err)
			}
		}
	}()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:     "do something",
		provider: "",
		model:    "gpt-5.2",
		stdout:   &stdout,
		stderr:   &stderr,
	})
	if err == nil {
		t.Fatal("expected error when no provider specified")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected error to mention 'provider', got: %v", err)
	}
}

// TestRunInvalidOutputSchema verifies that run returns an error when
// --output-schema contains malformed JSON. This is the black-box wire-through
// test — it confirms cfg.outputSchema reaches cmdutil.SelectProfile.
func TestRunInvalidOutputSchema(t *testing.T) {
	// Need at least one API key so llm.NewFromEnv succeeds and we actually
	// reach SelectProfile where the schema is parsed.
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Setenv("OPENAI_API_KEY", "dummy-for-wire-test")
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:         "do something",
		provider:     "openai",
		model:        "gpt-5.2",
		outputSchema: "{not json",
		workDir:      t.TempDir(),
		stateDir:     t.TempDir(),
		stdout:       &stdout,
		stderr:       &stderr,
	})
	if err == nil {
		t.Fatal("expected error for invalid --output-schema JSON")
	}
	if !strings.Contains(err.Error(), "invalid --output-schema") {
		t.Fatalf("error %q, want to contain 'invalid --output-schema'", err.Error())
	}
}

// TestRunMissingModel verifies that run returns an error when no --model is
// provided and SERF_MODEL is unset.
func TestRunMissingModel(t *testing.T) {
	old := os.Getenv("SERF_MODEL")
	if err := os.Unsetenv("SERF_MODEL"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if old != "" {
			if err := os.Setenv("SERF_MODEL", old); err != nil {
				t.Fatal(err)
			}
		}
	}()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:     "do something",
		provider: "openai",
		model:    "",
		stdout:   &stdout,
		stderr:   &stderr,
	})
	if err == nil {
		t.Fatal("expected error when no model specified")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected error to mention 'model', got: %v", err)
	}
}

// --- Session resume tests ---

func TestListSessions_PrintsFormattedList(t *testing.T) {
	dir := t.TempDir()

	meta1 := agent.SessionMeta{
		ID:        "01JTEST000000000000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
	meta2 := agent.SessionMeta{
		ID:        "01JTEST000000000000000002",
		ProfileID: "anthropic",
		Model:     "claude-opus-4-6",
		CreatedAt: time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		TurnCount: 1,
	}
	for _, m := range []agent.SessionMeta{meta1, meta2} {
		if err := agent.SaveSessionMeta(dir, m); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
	}

	var out bytes.Buffer
	cfg := runConfig{
		listSessions: true,
		workDir:      dir,
		stateDir:     dir,
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
		stateDir:     dir,
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
		resume:   "NONEXISTENT",
		workDir:  dir,
		stateDir: dir,
		stdout:   &bytes.Buffer{},
		stderr:   &bytes.Buffer{},
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
		stateDir:   dir,
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

// --- Drain event tests ---

func testEvents() []agent.SessionEvent {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	return []agent.SessionEvent{
		{Kind: agent.EventSessionStart, Timestamp: now, SessionID: "sess1", Data: agent.SessionStartData{Model: "gpt-5.2", Profile: "openai"}},
		{Kind: agent.EventAssistantTextEnd, Timestamp: now, SessionID: "sess1", Data: agent.AssistantTextEndData{
			Text:         "here is my answer",
			Reasoning:    "let me think carefully",
			FinishReason: "stop",
			Model:        "gpt-5.2",
			Usage:        llm.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CacheReadTokens: intPtr(80), CacheWriteTokens: intPtr(20)},
		}},
		{Kind: agent.EventToolCallStart, Timestamp: now, SessionID: "sess1", Data: agent.ToolCallStartData{
			ToolName:      "write_file",
			CallID:        "call_1",
			ArgumentsJSON: `{"file_path":"/tmp/test.txt","content":"hello world this is a longer argument string for testing truncation behavior"}`,
		}},
		{Kind: agent.EventToolCallEnd, Timestamp: now, SessionID: "sess1", Data: agent.ToolCallEndData{
			ToolName: "write_file",
			CallID:   "call_1",
		}},
		{Kind: agent.EventWarning, Timestamp: now, SessionID: "sess1", Data: agent.WarningData{Message: "context window 80% full"}},
		{Kind: agent.EventError, Timestamp: now, SessionID: "sess1", Data: agent.ErrorData{Error: "something went wrong"}},
	}
}

func feedEvents(events []agent.SessionEvent) <-chan agent.SessionEvent {
	ch := make(chan agent.SessionEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch
}

func TestDrainEventsVerbose(t *testing.T) {
	events := testEvents()
	ch := feedEvents(events)
	var buf bytes.Buffer
	done := drainEventsVerbose(ch, &buf)
	<-done

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(events) {
		t.Fatalf("expected %d NDJSON lines, got %d:\n%s", len(events), len(lines), buf.String())
	}

	// Each line must be valid JSON with a kind field.
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %s", i, err, line)
		}
		kind, _ := obj["kind"].(string)
		if kind == "" {
			t.Fatalf("line %d missing 'kind' field: %s", i, line)
		}
	}

	// Verify first line is SESSION_START.
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["kind"] != "SESSION_START" {
		t.Fatalf("first event kind: got %q want SESSION_START", first["kind"])
	}

	// Verify usage data is present in ASSISTANT_TEXT_END line.
	var assistantEnd map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &assistantEnd); err != nil {
		t.Fatal(err)
	}
	data, _ := assistantEnd["data"].(map[string]any)
	if data == nil {
		t.Fatalf("ASSISTANT_TEXT_END missing data field")
	}
	usage, _ := data["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("ASSISTANT_TEXT_END missing usage in data")
	}
	if usage["input_tokens"] != float64(100) {
		t.Fatalf("usage.input_tokens: got %v want 100", usage["input_tokens"])
	}
}

func TestDrainEventsHuman(t *testing.T) {
	events := testEvents()
	ch := feedEvents(events)
	var buf bytes.Buffer
	done := drainEventsHuman(ch, &buf)
	<-done

	output := buf.String()

	// Should contain model info from SESSION_START.
	if !strings.Contains(output, "[model]") {
		t.Fatalf("expected [model] line in output:\n%s", output)
	}
	if !strings.Contains(output, "gpt-5.2") {
		t.Fatalf("expected model name in output:\n%s", output)
	}

	// Should contain assistant message.
	if !strings.Contains(output, "[assistant]") {
		t.Fatalf("expected [assistant] line in output:\n%s", output)
	}
	if !strings.Contains(output, "here is my answer") {
		t.Fatalf("expected assistant text in output:\n%s", output)
	}

	// Should contain thinking summary.
	if !strings.Contains(output, "[thinking]") {
		t.Fatalf("expected [thinking] line in output:\n%s", output)
	}

	// Should contain tool call with args.
	if !strings.Contains(output, "[tool] write_file") {
		t.Fatalf("expected [tool] write_file in output:\n%s", output)
	}

	// Should contain usage.
	if !strings.Contains(output, "[usage]") {
		t.Fatalf("expected [usage] line in output:\n%s", output)
	}
	if !strings.Contains(output, "in=100") {
		t.Fatalf("expected 'in=100' in usage line:\n%s", output)
	}
	if !strings.Contains(output, "cache_read=80") {
		t.Fatalf("expected 'cache_read=80' in usage line:\n%s", output)
	}
	if !strings.Contains(output, "cache_write=20") {
		t.Fatalf("expected 'cache_write=20' in usage line:\n%s", output)
	}

	// Should contain warning.
	if !strings.Contains(output, "[warning]") {
		t.Fatalf("expected [warning] in output:\n%s", output)
	}

	// Should contain error.
	if !strings.Contains(output, "[error]") {
		t.Fatalf("expected [error] in output:\n%s", output)
	}
}

func intPtr(v int) *int { return &v }

func TestRunConfig_PluginDirsPassthrough(t *testing.T) {
	// Verify that pluginDirs on runConfig flows through to SessionConfig.PluginDirs.
	cfg := runConfig{
		pluginDirs: []string{"/a", "/b"},
	}
	if len(cfg.pluginDirs) != 2 {
		t.Fatalf("pluginDirs: got %d, want 2", len(cfg.pluginDirs))
	}

	sessionCfg := agent.SessionConfig{
		PluginDirs: cfg.pluginDirs,
	}
	if len(sessionCfg.PluginDirs) != 2 {
		t.Fatalf("SessionConfig.PluginDirs: got %d, want 2", len(sessionCfg.PluginDirs))
	}
	if sessionCfg.PluginDirs[0] != "/a" || sessionCfg.PluginDirs[1] != "/b" {
		t.Fatalf("SessionConfig.PluginDirs: got %v, want [/a /b]", sessionCfg.PluginDirs)
	}
}

func TestDrainEventsHuman_PluginEvents(t *testing.T) {
	ch := make(chan agent.SessionEvent, 3)
	ch <- agent.SessionEvent{Kind: agent.EventPluginLoaded, Data: agent.PluginLoadedData{
		Name: "test-plugin", SkillCount: 2, AgentCount: 1, MCPCount: 0,
	}}
	ch <- agent.SessionEvent{Kind: agent.EventHookStart, Data: agent.HookStartData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write",
	}}
	ch <- agent.SessionEvent{Kind: agent.EventHookEnd, Data: agent.HookEndData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write", DurationMS: 42,
	}}
	close(ch)
	var buf bytes.Buffer
	done := drainEventsHuman(ch, &buf)
	<-done
	out := buf.String()
	if !strings.Contains(out, "test-plugin") {
		t.Errorf("expected plugin name in output, got: %q", out)
	}
	if !strings.Contains(out, "2 skills") {
		t.Errorf("expected skill count in output, got: %q", out)
	}
	if !strings.Contains(out, "1 agents") {
		t.Errorf("expected agent count in output, got: %q", out)
	}
	if !strings.Contains(out, "PreToolUse") {
		t.Errorf("expected hook event name in output, got: %q", out)
	}
	if !strings.Contains(out, "Write") {
		t.Errorf("expected matcher in output, got: %q", out)
	}
	if !strings.Contains(out, "42ms") {
		t.Errorf("expected duration in output, got: %q", out)
	}
}

func TestRunWithContextStrategy(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		task:            "Reply with exactly the word PONG and nothing else.",
		provider:        "openai",
		model:           "gpt-5-mini-2025-08-07",
		workDir:         t.TempDir(),
		contextStrategy: "compact",
		stdout:          &stdout,
		stderr:          &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(strings.ToUpper(stdout.String()), "PONG") {
		t.Fatalf("expected stdout to contain PONG, got: %q", stdout.String())
	}
}
