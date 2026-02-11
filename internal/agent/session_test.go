package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
)

type fakeAdapter struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) llm.Response
	i        int
}

func (a *fakeAdapter) Name() string { return a.name }

func (a *fakeAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if a.i >= len(a.steps) {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp := a.steps[a.i](req)
	a.i++
	// Fill required response fields best-effort.
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *fakeAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, errors.New("stream not implemented in fakeAdapter")
}

func (a *fakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

func TestSession_NaturalCompletion_LoadsOnlyProfileDocs(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("AGENTS\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("CLAUDE\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("GEMINI\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".codex", "instructions.md"), []byte("CODEX\n"), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	if len(reqs[0].Messages) == 0 || reqs[0].Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected leading system message, got %+v", reqs[0].Messages)
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "BEGIN AGENTS.md") || !strings.Contains(sys, "BEGIN .codex/instructions.md") ||
		strings.Contains(sys, "BEGIN CLAUDE.md") || strings.Contains(sys, "BEGIN GEMINI.md") {
		t.Fatalf("system prompt doc selection failed:\n%s", sys)
	}
	// Spec: system prompt includes environment context.
	for _, want := range []string{"<environment>", "Working directory:", "Is git repository:", "Platform:", "Today's date:", "Knowledge cutoff:", "Tools:"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
}

func TestSession_NaturalCompletion_LoadsOnlyProfileDocs_Anthropic(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("AGENTS\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("CLAUDE\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("GEMINI\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".codex", "instructions.md"), []byte("CODEX\n"), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	if len(reqs[0].Messages) == 0 || reqs[0].Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected leading system message, got %+v", reqs[0].Messages)
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "BEGIN CLAUDE.md") || !strings.Contains(sys, "BEGIN AGENTS.md") {
		t.Fatalf("Anthropic profile should load CLAUDE.md and AGENTS.md:\n%s", sys)
	}
	if strings.Contains(sys, "BEGIN GEMINI.md") || strings.Contains(sys, "BEGIN .codex/instructions.md") {
		t.Fatalf("Anthropic profile should NOT load GEMINI.md or .codex/instructions.md:\n%s", sys)
	}
}

func TestSession_NaturalCompletion_LoadsOnlyProfileDocs_Gemini(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("AGENTS\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("CLAUDE\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("GEMINI\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".codex", "instructions.md"), []byte("CODEX\n"), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "google",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewGeminiProfile("gemini-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	if len(reqs[0].Messages) == 0 || reqs[0].Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected leading system message, got %+v", reqs[0].Messages)
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "BEGIN GEMINI.md") || !strings.Contains(sys, "BEGIN AGENTS.md") {
		t.Fatalf("Gemini profile should load GEMINI.md and AGENTS.md:\n%s", sys)
	}
	if strings.Contains(sys, "BEGIN CLAUDE.md") || strings.Contains(sys, "BEGIN .codex/instructions.md") {
		t.Fatalf("Gemini profile should NOT load CLAUDE.md or .codex/instructions.md:\n%s", sys)
	}
}

func TestSession_SystemPromptFile_OverridesBasePrompt(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "custom-prompt.md")
	os.WriteFile(promptFile, []byte("You are a custom test agent."), 0644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "hello")

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests captured")
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "You are a custom test agent.") {
		t.Errorf("custom system prompt not found in LLM request")
	}
	// The default embedded prompt should be fully replaced.
	if strings.Contains(sys, "OpenAI profile") {
		t.Errorf("default prompt should have been overridden but OpenAI profile text still present")
	}
}

func TestSession_CoreTools_ReadManyFiles_And_ListDir(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.txt", "hello\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := env.WriteFile("sub/b.txt", "world\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "google"})
	sess, err := NewSession(c, NewGeminiProfile("gemini-test"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// read_many_files
	res := sess.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "c1",
		Name:      "read_many_files",
		Arguments: json.RawMessage(`{"file_paths":["a.txt","sub/b.txt"]}`),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("read_many_files error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "BEGIN a.txt") || !strings.Contains(res.Output, "1 | hello") {
		t.Fatalf("read_many_files output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "BEGIN sub/b.txt") || !strings.Contains(res.Output, "1 | world") {
		t.Fatalf("read_many_files output:\n%s", res.Output)
	}

	// list_dir
	res = sess.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID:        "c2",
		Name:      "list_dir",
		Arguments: json.RawMessage(`{"path":"","depth":2}`),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("list_dir error: %s", res.Output)
	}
	for _, want := range []string{`"name": "a.txt"`, `"name": "sub"`, `"name": "sub/b.txt"`} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("list_dir missing %q:\n%s", want, res.Output)
		}
	}
}

func TestSession_ToolLoop_ExecutesToolsAndContinues(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"file_path":"hello.txt","content":"Hello"}`),
		Type:      "function",
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind:     llm.ContentToolCall,
							ToolCall: &call,
						}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				// Expect a tool result message to have been appended.
				foundTool := false
				for _, m := range req.Messages {
					if m.Role == llm.RoleTool {
						foundTool = true
					}
				}
				if !foundTool {
					return llm.Response{Message: llm.Assistant("missing tool result")}
				}
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "write a file")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatalf("read hello.txt: %v", err)
	}
	if strings.TrimSpace(string(b)) != "Hello" {
		t.Fatalf("hello.txt: %q", string(b))
	}
	sess.Close()
}

func TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"head -c 60000 </dev/zero | tr '\\\\0' 'x'","timeout_ms":5000}`),
		Type:      "function",
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{{
							Kind:     llm.ContentToolCall,
							ToolCall: &call,
						}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ToolOutputLimits: map[string]ToolOutputLimit{
			"shell": {MaxChars: 800, Strategy: TruncHeadTail},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "run a big command")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	// The second request should include a truncated tool result sent back to the model.
	truncated := ""
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool {
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					if s, ok := p.ToolResult.Content.(string); ok {
						truncated = s
					}
				}
			}
		}
	}
	if !strings.Contains(truncated, "Tool output was truncated") {
		t.Fatalf("expected truncation marker in tool result, got:\n%s", truncated)
	}
	if len(truncated) > 2000 {
		t.Fatalf("expected truncated tool result to be small, got %d chars", len(truncated))
	}

	// But TOOL_CALL_END should carry the full untruncated output.
	var full string
	for ev := range sess.Events() {
		if ev.Kind == EventToolCallEnd {
			full = anyToString(ev.Data["full_output"])
		}
	}
	if strings.TrimSpace(full) == "" {
		t.Fatalf("expected non-empty full output from TOOL_CALL_END event")
	}
	if len(full) <= len(truncated) {
		t.Fatalf("expected full output larger than truncated output: full=%d truncated=%d", len(full), len(truncated))
	}
}

func TestSession_ToolOutputTruncation_CanOverrideLineLimitViaSessionConfig(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'l0\\nl1\\nl2\\nl3\\nl4\\nl5\\nl6\\nl7\\nl8\\nl9\\n'","timeout_ms":5000}`),
		Type:      "function",
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ToolOutputLimits: map[string]ToolOutputLimit{
			"shell": {MaxChars: 100_000, MaxLines: 4, Strategy: TruncHeadTail},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "run")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}

	truncated := ""
	for _, m := range reqs[1].Messages {
		if m.Role != llm.RoleTool {
			continue
		}
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
				if s, ok := p.ToolResult.Content.(string); ok {
					truncated = s
				}
			}
		}
	}
	if truncated == "" {
		t.Fatalf("expected tool result content")
	}
	for _, want := range []string{"lines omitted", "l0", "exit_code="} {
		if !strings.Contains(truncated, want) {
			t.Fatalf("expected %q in truncated tool output:\n%s", want, truncated)
		}
	}
}

func TestSession_ParallelToolCalls_RunConcurrentlyWhenSupported(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 2)
	release := make(chan struct{})

	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// Two calls to the same slow tool.
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{"n":1}`)}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "2", Name: "slow", Arguments: json.RawMessage(`{"n":2}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Register a slow tool that blocks until the test releases it.
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name: "slow",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "integer"}},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			started <- struct{}{}
			<-release
			return "ok", nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "run slow tools")
		done <- err
	}()

	// If tools are run concurrently, we should see both start before release.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for tool call %d to start", i+1)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("ProcessInput error: %v", err)
	}
	sess.Close()
}

func TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Make the repo dirty before session start so the snapshot reflects it.
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\nmore\n"), 0o644) // modified tracked file
	_ = os.WriteFile(filepath.Join(dir, "UNTRACKED.txt"), []byte("u\n"), 0o644)   // untracked file

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	sys := reqs[0].Messages[0].Text()
	for _, want := range []string{
		"Is git repository: true",
		"Git branch:",
		"<git>",
		"Modified files: 1",
		"Untracked files: 1",
		"Recent commits:",
		"init",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
	// Ensure Git branch has a value (not just an empty placeholder).
	if i := strings.Index(sys, "Git branch: "); i >= 0 {
		val := strings.TrimSpace(strings.Split(strings.TrimPrefix(sys[i:], "Git branch: "), "\n")[0])
		if val == "" {
			t.Fatalf("expected non-empty Git branch:\n%s", sys)
		}
	}
}

func TestSession_UserInstructionOverride_AppendedLastToSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("AGENTS\n"), 0o644)

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	override := "OVERRIDE: highest priority"
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		UserInstructionOverride: override,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.HasSuffix(sys, override+"\n") {
		t.Fatalf("expected system prompt to end with override, got:\n%s", sys)
	}
	if end := strings.LastIndex(sys, "----- END AGENTS.md -----"); end >= 0 {
		if strings.LastIndex(sys, override) < end {
			t.Fatalf("expected override to be appended after project docs, got:\n%s", sys)
		}
	}
}

func TestSession_FollowUp_ProcessesAfterCompletion(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("first")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("second")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.FollowUp("do second")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do first")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "first\nsecond" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()
}

func TestSession_LoopDetection_EmitsEventAndInjectsSteering(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func() llm.Response {
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`)}},
				},
			},
		}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	cfg := SessionConfig{LoopDetectionWindow: 3}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
		_, err = sess.ProcessInput(ctx, "loop")
		if err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}

		// Spec: loop detection warning is recorded as a SteeringTurn in history.
		sess.mu.Lock()
		turns := append([]Turn{}, sess.history...)
		sess.mu.Unlock()
		foundSteering := false
		for _, tr := range turns {
			if tr.Kind == TurnSteering && tr.Message.Role == llm.RoleUser && strings.Contains(tr.Message.Text(), "Loop detected:") {
				foundSteering = true
			}
		}
		if !foundSteering {
			t.Fatalf("expected loop detection steering turn in history; got %+v", turns)
		}
		sess.Close()

	// Verify loop detection event was emitted.
	loopEv := false
	steerEv := false
	for ev := range sess.Events() {
		if ev.Kind == EventLoopDetection {
			loopEv = true
		}
		if ev.Kind == EventSteeringInjected {
			if s, _ := ev.Data["text"].(string); strings.Contains(s, "Loop detected:") {
				steerEv = true
			}
		}
	}
	if !loopEv {
		t.Fatalf("expected LOOP_DETECTION event")
	}
	if !steerEv {
		t.Fatalf("expected STEERING_INJECTED event for loop detection")
	}

	// Verify the steering message made it into a subsequent request.
	reqs := f.Requests()
	found := false
	for _, req := range reqs {
		for _, m := range req.Messages {
			if m.Role == llm.RoleUser && strings.Contains(m.Text(), "Loop detected:") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected loop detection steering message in request history")
	}
}

func anyToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func TestAssistantTextEnd_EnrichedData(t *testing.T) {
	dir := t.TempDir()

	reasoningTokens := 42
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Model: "gpt-5.2",
					Finish: llm.FinishReason{Reason: "stop"},
					Usage: llm.Usage{
						InputTokens:     100,
						OutputTokens:    50,
						TotalTokens:     150,
						ReasoningTokens: &reasoningTokens,
					},
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "let me think about this"}},
							{Kind: llm.ContentText, Text: "here is my answer"},
						},
					},
				}
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Collect events.
	var events []SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			events = append(events, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "test")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	// Find the ASSISTANT_TEXT_END event.
	var found *SessionEvent
	for i, ev := range events {
		if ev.Kind == EventAssistantTextEnd {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no ASSISTANT_TEXT_END event found; events: %v", events)
	}

	// Verify text.
	if txt, _ := found.Data["text"].(string); txt != "here is my answer" {
		t.Fatalf("text: got %q want %q", txt, "here is my answer")
	}

	// Verify reasoning.
	reasoning, _ := found.Data["reasoning"].(string)
	if reasoning != "let me think about this" {
		t.Fatalf("reasoning: got %q want %q", reasoning, "let me think about this")
	}

	// Verify finish_reason.
	if fr, _ := found.Data["finish_reason"].(string); fr != "stop" {
		t.Fatalf("finish_reason: got %q want %q", fr, "stop")
	}

	// Verify model.
	if m, _ := found.Data["model"].(string); m != "gpt-5.2" {
		t.Fatalf("model: got %q want %q", m, "gpt-5.2")
	}

	// Verify usage is present and has expected values.
	usage, ok := found.Data["usage"].(llm.Usage)
	if !ok {
		t.Fatalf("usage: expected llm.Usage, got %T", found.Data["usage"])
	}
	if usage.InputTokens != 100 {
		t.Fatalf("usage.input_tokens: got %d want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Fatalf("usage.output_tokens: got %d want 50", usage.OutputTokens)
	}
	if usage.ReasoningTokens == nil || *usage.ReasoningTokens != 42 {
		t.Fatalf("usage.reasoning_tokens: got %v want 42", usage.ReasoningTokens)
	}
}

func TestSession_WebSearch_FlagSetOnRequest(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "done"}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hello")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatalf("no requests captured")
	}
	if !reqs[0].WebSearch {
		t.Fatalf("WebSearch flag not set on request")
	}
}

func TestSession_PauseTurn_ContinuesLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// First call: return pause_turn (model needs more time for search)
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Searching..."}},
					},
					Finish: llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
					Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				}
			},
			func(req llm.Request) llm.Response {
				// Second call: return final answer
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Here is the answer."}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := sess.ProcessInput(ctx, "search for something")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(reqs))
	}
	if !strings.Contains(result, "Here is the answer.") {
		t.Fatalf("result: %q", result)
	}
}

func TestSessionState_Transitions(t *testing.T) {
	// Verify state type and constants exist
	if SessionIdle != SessionState("IDLE") {
		t.Fatal("SessionIdle wrong")
	}
	if SessionProcessing != SessionState("PROCESSING") {
		t.Fatal("SessionProcessing wrong")
	}
	if SessionAwaitingInput != SessionState("AWAITING_INPUT") {
		t.Fatal("SessionAwaitingInput wrong")
	}
	if SessionClosed != SessionState("CLOSED") {
		t.Fatal("SessionClosed wrong")
	}
}

func TestSession_SessionEnd_EmittedExactlyOnce(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "hello")
	sess.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, ev := range *eventsPtr {
		if ev.Kind == EventSessionEnd {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 SESSION_END event, got %d", count)
	}
}

func TestSession_SystemPromptRebuiltPerRound(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			// Round 1: write AGENTS.md via tool call.
			return llm.Response{
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID: "c1", Name: "write_file", Type: "function",
						Arguments: json.RawMessage(`{"file_path":"AGENTS.md","content":"# New agent instructions"}`),
					}}},
				},
			}
		},
		func(req llm.Request) llm.Response {
			// Round 2: the system prompt should now include the freshly-written AGENTS.md.
			sys := req.Messages[0].Text()
			if !strings.Contains(sys, "New agent instructions") {
				t.Error("system prompt not rebuilt after tool execution -- AGENTS.md changes not reflected")
			}
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	env.ExecCommand(ctx, "git init", 5000, dir, nil)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), env, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx2, "write agents.md then verify")
}

// blockingAdapter is a test adapter whose Complete blocks until context is cancelled.
type blockingAdapter struct {
	name      string
	blocked   chan struct{} // closed when LLM call starts blocking
}

func (a *blockingAdapter) Name() string { return a.name }
func (a *blockingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	close(a.blocked)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func (a *blockingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, errors.New("stream not implemented")
}

func TestSession_Close_CancelsInFlightLLMCall(t *testing.T) {
	blocked := make(chan struct{})
	c := llm.NewClient()
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), "hello")
		done <- err
	}()

	<-blocked // Wait until the LLM call is in-flight.
	sess.Close() // Should cancel the LLM call.

	select {
	case <-done:
		// ProcessInput returned -- Close() successfully cancelled it.
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after Close() -- in-flight LLM call not cancelled")
	}
}
