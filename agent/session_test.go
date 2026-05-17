package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
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
	return nil, llm.ErrStreamUnsupported
}

func (a *fakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

type streamingAdapter struct {
	name string

	mu             sync.Mutex
	completeCalls  int
	streamCalls    int
	requests       []llm.Request
	completeResult llm.Response
	streamErr      error
	streamScript   func(*llm.ChanStream)
}

func (a *streamingAdapter) Name() string { return a.name }

func (a *streamingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.completeCalls++
	a.requests = append(a.requests, req)
	resp := a.completeResult
	a.mu.Unlock()
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *streamingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	a.mu.Lock()
	a.streamCalls++
	a.requests = append(a.requests, req)
	err := a.streamErr
	script := a.streamScript
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	_ = streamCtx
	st := llm.NewChanStream(cancel)
	go func() {
		defer st.CloseSend()
		if script != nil {
			script(st)
		}
	}()
	return st, nil
}

func (a *streamingAdapter) Counts() (completeCalls int, streamCalls int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.completeCalls, a.streamCalls
}

func TestSession_StreamOpenFailureHonorsRetryBudget(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 500, "temporary upstream failure", nil, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatal("expected stream error")
	}
	sess.Close()

	completeCalls, streamCalls := f.Counts()
	if got, want := streamCalls, 1; got != want {
		t.Fatalf("stream calls: got %d want %d", got, want)
	}
	if got, want := completeCalls, 0; got != want {
		t.Fatalf("complete calls: got %d want %d", got, want)
	}
}

// kata r6y9: after the LLM call returns a retryable stream error and the retry
// policy is exhausted, the session must NOT be left in PROCESSING. Otherwise
// the daemon's /status keeps reporting PROCESSING forever, the hub disables
// steer/send, and the user has no recovery path.
func TestSession_StreamErrorReturnsSessionToIdle(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			// Send nothing and close: callModel sees the channel close
			// before any finish event and surfaces a retryable StreamError
			// ("stream ended without finish event").
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected stream-ended error")
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after stream error: got %q want %q", got, SessionIdle)
	}
}

// fakeErrAdapter is like fakeAdapter but supports steps that return errors.
type fakeErrAdapter struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) (llm.Response, error)
	i        int
}

func (a *fakeErrAdapter) Name() string { return a.name }

func (a *fakeErrAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if a.i >= len(a.steps) {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp, err := a.steps[a.i](req)
	a.i++
	if err != nil {
		return resp, err
	}
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *fakeErrAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *fakeErrAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

func TestStreamUnavailableIgnoresPlainTextUnsupportedMessages(t *testing.T) {
	if !streamUnavailable(errStreamUnavailable) {
		t.Fatal("expected internal sentinel to mark stream unavailable")
	}
	if !streamUnavailable(llm.ErrStreamUnsupported) {
		t.Fatal("expected LLM sentinel to mark stream unavailable")
	}
	if streamUnavailable(errors.New("provider returned: streaming not supported for this request")) {
		t.Fatal("plain error text should not mark stream unavailable")
	}
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
				return finalResponse("ok")
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
	out, err := sess.ProcessInput(ctx, "hi", nil)
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
	for _, want := range []string{"<environment>", "Working directory:", "Is git repository:", "Platform:", "Today's date:", "Knowledge cutoff:"} {
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
				return finalResponse("ok")
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
	out, err := sess.ProcessInput(ctx, "hi", nil)
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

func TestSession_TrackReadFile_Concurrent(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var wg sync.WaitGroup
	start := make(chan struct{})
	workers := 32
	iterations := 500

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				sess.trackReadFile(fmt.Sprintf("file-%d-%d.txt", i, j))
			}
		}()
	}

	close(start)
	wg.Wait()
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
				return finalResponse("ok")
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
	out, err := sess.ProcessInput(ctx, "hi", nil)
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
	if err := os.WriteFile(promptFile, []byte("You are a custom test agent."), 0644); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("ok")
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
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}

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

func TestSession_CoreTools_ListDir(t *testing.T) {
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

	// list_dir
	res := sess.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
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
					return finalResponse("missing tool result")
				}
				return finalResponse("ok")
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
	out, err := sess.ProcessInput(ctx, "write a file", nil)
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
				return finalResponse("ok")
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
	out, err := sess.ProcessInput(ctx, "run a big command", nil)
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

	// TOOL_CALL_OUTPUT_DELTA / TOOL_CALL_END should reflect the full shell output,
	// not the truncated payload sent back to the model in the next request.
	var full string
	totalDeltaBytes := 0
	for ev := range sess.Events() {
		switch ev.Kind {
		case EventToolCallOutputDelta:
			if d, ok := ev.Data.(ToolCallOutputDeltaData); ok && d.ToolName == "shell" {
				totalDeltaBytes += len(d.Delta)
			}
		case EventToolCallEnd:
			if d, ok := ev.Data.(ToolCallEndData); ok && d.ToolName == "shell" {
				if d.Output != "" {
					full = d.Output
				} else {
					full = d.Error
				}
			}
		}
	}
	if strings.TrimSpace(full) == "" {
		t.Fatalf("expected non-empty full output from TOOL_CALL_END event")
	}
	if totalDeltaBytes <= len(truncated) {
		t.Fatalf("expected shell output deltas to exceed truncated request payload: deltas=%d truncated=%d", totalDeltaBytes, len(truncated))
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	out, err := sess.ProcessInput(ctx, "run", nil)
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
				return finalResponse("ok")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Register a slow read-only tool that blocks until the test releases it.
	// ReadOnly: true ensures the ordered-group algorithm batches both calls
	// for parallel execution (non-read-only tools are serialized).
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name: "slow",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "integer"}},
			},
		}, ReadOnly: true},
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
		_, err := sess.ProcessInput(ctx, "run slow tools", nil)
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

func TestSession_ParallelToolCalls_NonReadOnlyToolsSerialize(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Track execution order: each tool call records its start and end.
	type callTiming struct {
		name  string
		start time.Time
		end   time.Time
	}
	var mu sync.Mutex
	var timings []callTiming

	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// read, write, read — the write must not overlap with reads.
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "reader", Arguments: json.RawMessage(`{}`)}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "2", Name: "writer", Arguments: json.RawMessage(`{}`)}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "3", Name: "reader", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return finalResponse("ok")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	makeTool := func(name string, readOnly bool) RegisteredTool {
		return RegisteredTool{
			Tool: llm.Tool{
				Definition: llm.ToolDefinition{
					Name:       name,
					Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				},
				ReadOnly: readOnly,
			},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				start := time.Now()
				time.Sleep(50 * time.Millisecond)
				end := time.Now()
				mu.Lock()
				timings = append(timings, callTiming{name: name, start: start, end: end})
				mu.Unlock()
				return "ok", nil
			},
		}
	}
	_ = sess.reg.Register(makeTool("reader", true))
	_ = sess.reg.Register(makeTool("writer", false))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "run tools", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(timings) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(timings))
	}

	// Find the writer timing.
	var writerTiming callTiming
	for _, ct := range timings {
		if ct.name == "writer" {
			writerTiming = ct
			break
		}
	}
	// Every reader must not overlap with the writer.
	for _, ct := range timings {
		if ct.name == "reader" {
			if ct.start.Before(writerTiming.end) && writerTiming.start.Before(ct.end) {
				t.Errorf("reader overlapped with writer: reader=%v-%v writer=%v-%v",
					ct.start, ct.end, writerTiming.start, writerTiming.end)
			}
		}
	}
}

func TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Make the repo dirty before session start so the snapshot reflects it.
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\nmore\n"), 0o644) // modified tracked file
	_ = os.WriteFile(filepath.Join(dir, "UNTRACKED.txt"), []byte("u\n"), 0o644)    // untracked file

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.HasSuffix(strings.TrimSpace(sys), override) {
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
			func(req llm.Request) llm.Response { return finalResponse("first") },
			func(req llm.Request) llm.Response { return finalResponse("second") },
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
	out, err := sess.ProcessInput(ctx, "do first", nil)
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	_, err = sess.ProcessInput(ctx, "loop", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Spec: loop detection warning is recorded as a SteeringTurn in history.
	sess.mu.Lock()
	turns := append([]Turn{}, sess.history...)
	sess.mu.Unlock()
	foundSteering := false
	for _, tr := range turns {
		if tr.Kind == TurnSteering && tr.Message.Role == llm.RoleUser && strings.Contains(tr.Message.Text(), "stuck") {
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
			if s, _ := ev.DataMap()["text"].(string); strings.Contains(s, "stuck") {
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
			if m.Role == llm.RoleUser && strings.Contains(m.Text(), "stuck") {
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
				return wrapCommunicateResponse(llm.Response{
					Model:  "gpt-5.2",
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
				})
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

	_, err = sess.ProcessInput(ctx, "test", nil)
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

	// Type-assert to the typed payload struct.
	endData, ok := found.Data.(AssistantTextEndData)
	if !ok {
		t.Fatalf("expected AssistantTextEndData, got %T", found.Data)
	}

	// Verify text.
	if endData.Text != "here is my answer" {
		t.Fatalf("text: got %q want %q", endData.Text, "here is my answer")
	}

	// Verify reasoning.
	if endData.Reasoning != "let me think about this" {
		t.Fatalf("reasoning: got %q want %q", endData.Reasoning, "let me think about this")
	}

	// Verify finish_reason.
	if endData.FinishReason != "stop" {
		t.Fatalf("finish_reason: got %q want %q", endData.FinishReason, "stop")
	}

	// Verify model.
	if endData.Model != "gpt-5.2" {
		t.Fatalf("model: got %q want %q", endData.Model, "gpt-5.2")
	}

	// Verify usage is present and has expected values.
	usage, ok2 := endData.Usage.(llm.Usage)
	if !ok2 {
		t.Fatalf("usage: expected llm.Usage, got %T", endData.Usage)
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
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "done"}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				})
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
	_, err = sess.ProcessInput(ctx, "hello", nil)
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
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Here is the answer."}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
				})
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
	result, err := sess.ProcessInput(ctx, "search for something", nil)
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
			return finalResponse("done")
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
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
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

func TestSession_ProjectDocsCachedAtInit(t *testing.T) {
	// Project docs are loaded once at session init and cached.
	// Mid-session writes to AGENTS.md should NOT be reflected in subsequent rounds.
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
			// Round 2: project docs are cached at init, so the newly-written AGENTS.md
			// should NOT appear in the system prompt (it didn't exist at init time).
			sys := req.Messages[0].Text()
			if strings.Contains(sys, "New agent instructions") {
				t.Error("project docs should be cached at init, not reloaded per round")
			}
			return finalResponse("done")
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()
	ctx := context.Background()
	if _, err := env.ExecCommand(ctx, "git init", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), env, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx2, "write agents.md then verify", nil); err != nil {
		t.Fatal(err)
	}
}

// blockingAdapter is a test adapter whose Complete blocks until context is cancelled.
type blockingAdapter struct {
	name    string
	blocked chan struct{} // closed when LLM call starts blocking
}

func (a *blockingAdapter) Name() string { return a.name }
func (a *blockingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	close(a.blocked)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func (a *blockingAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
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
		_, err := sess.ProcessInput(context.Background(), "hello", nil)
		done <- err
	}()

	<-blocked    // Wait until the LLM call is in-flight.
	sess.Close() // Should cancel the LLM call.

	select {
	case <-done:
		// ProcessInput returned -- Close() successfully cancelled it.
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after Close() -- in-flight LLM call not cancelled")
	}
}

// cleanupTrackingEnv wraps an ExecutionEnvironment and records the order
// of operations during shutdown. Cleanup() pauses briefly so that any
// SESSION_END event already in the buffered channel has time to be consumed,
// which lets us prove the ordering via a shared log.
type cleanupTrackingEnv struct {
	ExecutionEnvironment
	mu  sync.Mutex
	log []string
}

func (e *cleanupTrackingEnv) Cleanup() {
	e.mu.Lock()
	e.log = append(e.log, "cleanup_start")
	e.mu.Unlock()

	// Pause to give the consumer goroutine time to drain any events that
	// were already in the buffered channel. If SESSION_END was sent before
	// Cleanup, the consumer will record "session_end_received" during this
	// sleep, causing it to appear before "cleanup_end" in the log.
	time.Sleep(100 * time.Millisecond)

	e.ExecutionEnvironment.Cleanup()

	e.mu.Lock()
	e.log = append(e.log, "cleanup_end")
	e.mu.Unlock()
}

func (e *cleanupTrackingEnv) Log() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.log...)
}

func (e *cleanupTrackingEnv) Append(op string) {
	e.mu.Lock()
	e.log = append(e.log, op)
	e.mu.Unlock()
}

func TestSession_GracefulShutdown_CorrectOrdering(t *testing.T) {
	// Use a blocking adapter so we can call Close() while the LLM call is
	// in-flight. This ensures Close() is the one that emits SESSION_END
	// (not ProcessInput), exercising the abort/shutdown path.
	blocked := make(chan struct{})
	c := llm.NewClient()
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	dir := t.TempDir()

	inner := NewLocalExecutionEnvironment(dir)
	trackEnv := &cleanupTrackingEnv{ExecutionEnvironment: inner}

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), trackEnv, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// The event consumer records when SESSION_END is received and when the
	// channel closes. Because Close() is synchronous and we share the log
	// with Cleanup(), we can verify the ordering of operations in Close().
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range sess.Events() {
			if ev.Kind == EventSessionEnd {
				trackEnv.Append("session_end_received")
			}
		}
		trackEnv.Append("channel_closed")
	}()

	// Start ProcessInput in the background; it will block on the LLM call.
	processDone := make(chan struct{})
	go func() {
		defer close(processDone)
		_, _ = sess.ProcessInput(context.Background(), "hello", nil)
	}()

	// Wait until the LLM call is in-flight, then abort via Close().
	<-blocked
	sess.Close()
	<-processDone
	<-doneCh

	log := trackEnv.Log()

	// Find positions of key operations.
	indexOf := func(op string) int {
		for i, v := range log {
			if v == op {
				return i
			}
		}
		return -1
	}

	cleanupStart := indexOf("cleanup_start")
	cleanupEnd := indexOf("cleanup_end")
	sessionEnd := indexOf("session_end_received")
	channelClosed := indexOf("channel_closed")

	if cleanupStart == -1 || cleanupEnd == -1 {
		t.Fatalf("env.Cleanup() was never called; log: %v", log)
	}
	if sessionEnd == -1 {
		t.Fatalf("SESSION_END event was never emitted; log: %v", log)
	}
	if channelClosed == -1 {
		t.Fatalf("events channel was never closed; log: %v", log)
	}

	// Spec Appendix B ordering: Cleanup (kill processes) must complete
	// before SESSION_END is emitted.
	if cleanupEnd >= sessionEnd {
		t.Fatalf("env.Cleanup() must complete before SESSION_END is emitted (spec Appendix B);\n"+
			"cleanup_end at %d, session_end at %d; log: %v", cleanupEnd, sessionEnd, log)
	}

	// SESSION_END must be emitted before the events channel is closed.
	if sessionEnd >= channelClosed {
		t.Fatalf("SESSION_END must be emitted before events channel closes;\n"+
			"session_end at %d, channel_closed at %d; log: %v", sessionEnd, channelClosed, log)
	}

	// State should be CLOSED after Close() returns.
	if sess.State() != SessionClosed {
		t.Fatalf("state after Close(): got %s, want CLOSED", sess.State())
	}
}

func TestSession_CustomRegisteredTool_AppearsInSystemPrompt(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			// Custom tools appear in the API tools parameter, not the system prompt.
			found := false
			for _, tool := range req.Tools {
				if tool.Name == "my_custom_tool" {
					found = true
					break
				}
			}
			if !found {
				t.Error("custom tool not in request tools")
			}
			return finalResponse("done")
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Register a custom tool after session creation.
	if err := sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "my_custom_tool", Description: "Does custom things"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Refresh caches to pick up tool registered after session creation.
	sess.rebuildToolDefsCache()
	sess.refreshSystemPromptCache()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "test", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSession_ToolCallEnd_UsesOutputKeyOnSuccess(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "glob",
		Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
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
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

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
	if _, err := sess.ProcessInput(ctx, "test", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	var found *SessionEvent
	for i, ev := range events {
		if ev.Kind == EventToolCallEnd {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no TOOL_CALL_END event found")
	}

	// Success: Output is populated, Error is empty (not present).
	d, ok := found.Data.(ToolCallEndData)
	if !ok {
		t.Fatalf("expected ToolCallEndData, got %T", found.Data)
	}
	// For success, Error should be empty.
	if d.Error != "" {
		t.Fatalf("TOOL_CALL_END for success should not have error, got %q", d.Error)
	}
	// DataMap should not have legacy keys "full_output" or "is_error".
	dm := found.DataMap()
	if _, ok := dm["full_output"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'full_output' key")
	}
	if _, ok := dm["is_error"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'is_error' key")
	}
}

func TestSession_ToolCallEnd_UsesErrorKeyOnFailure(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"/nonexistent/path/xyz.txt"}`),
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
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

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
	if _, err := sess.ProcessInput(ctx, "read missing file", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	var found *SessionEvent
	for i, ev := range events {
		if ev.Kind == EventToolCallEnd {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no TOOL_CALL_END event found")
	}

	// Error: Error field should be populated, Output empty.
	d, ok := found.Data.(ToolCallEndData)
	if !ok {
		t.Fatalf("expected ToolCallEndData, got %T", found.Data)
	}
	if d.Error == "" {
		t.Fatalf("expected non-empty error in TOOL_CALL_END data for error case")
	}
	if d.Output != "" {
		t.Fatalf("TOOL_CALL_END for error should not have output, got %q", d.Output)
	}
	// DataMap should not have legacy keys "full_output" or "is_error".
	dm := found.DataMap()
	if _, ok := dm["full_output"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'full_output' key")
	}
	if _, ok := dm["is_error"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'is_error' key")
	}
}

func TestSession_AssistantTextStart_IncludesModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Model:   "test-model-42",
					Message: llm.Assistant("hello"),
				})
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("test-model-42"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

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
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	var found *SessionEvent
	for i, ev := range events {
		if ev.Kind == EventAssistantTextStart {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no ASSISTANT_TEXT_START event found")
	}
	model, ok := found.DataMap()["model"].(string)
	if !ok || model != "test-model-42" {
		t.Fatalf("expected model 'test-model-42' in ASSISTANT_TEXT_START, got: %v", found.Data)
	}
}

func TestSession_StreamsCommunicateToolArgumentsAsAssistantDeltas(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCall("c1", "Hello")
	f := &streamingAdapter{
		name:           "openai",
		completeResult: toolCallResponse(comm),
		streamScript: func(st *llm.ChanStream) {
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			st.Send(llm.StreamEvent{
				Type: llm.StreamEventToolCallStart,
				ToolCall: &llm.ToolCallData{
					ID:   "c1",
					Name: "communicate",
					Type: "function",
				},
			})
			st.Send(llm.StreamEvent{
				Type: llm.StreamEventToolCallDelta,
				ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Arguments: json.RawMessage(`{"message":"Hel`),
				},
			})
			st.Send(llm.StreamEvent{
				Type: llm.StreamEventToolCallDelta,
				ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Arguments: json.RawMessage(`lo","await_reply":false,"output":{"message":"","data":{},"artifacts":[]}}`),
				},
			})
			st.Send(llm.StreamEvent{
				Type:     llm.StreamEventToolCallEnd,
				ToolCall: &comm,
			})
			finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

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
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	if out != "Hello" {
		t.Fatalf("ProcessInput output = %q, want Hello", out)
	}
	completeCalls, streamCalls := f.Counts()
	if completeCalls != 0 || streamCalls != 1 {
		t.Fatalf("model calls: complete=%d stream=%d, want complete=0 stream=1", completeCalls, streamCalls)
	}

	var deltas []string
	for _, ev := range events {
		if ev.Kind != EventAssistantTextDelta {
			continue
		}
		data, ok := ev.Data.(AssistantTextDeltaData)
		if !ok {
			t.Fatalf("ASSISTANT_TEXT_DELTA data type = %T", ev.Data)
		}
		deltas = append(deltas, data.Delta)
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("assistant deltas = %q, want chunks composing Hello; events=%v", deltas, events)
	}
	if len(deltas) < 2 {
		t.Fatalf("assistant deltas = %q, want multiple streamed chunks", deltas)
	}
}

func TestPartialJSONStringFieldDecodesUnicodeEscapes(t *testing.T) {
	got, ok := partialJSONStringField(`{"message":"Hello \u263A"}`, "message")
	if !ok {
		t.Fatal("message field not found")
	}
	if got != "Hello \u263A" {
		t.Fatalf("message=%q, want decoded unicode escape", got)
	}

	got, ok = partialJSONStringField(`{"message":"Hello \ud83d\ude00"}`, "message")
	if !ok {
		t.Fatal("message field with surrogate pair not found")
	}
	if got != "Hello 😀" {
		t.Fatalf("message=%q, want decoded surrogate pair", got)
	}

	got, ok = partialJSONStringField(`{"message":"Hello \u26`, "message")
	if !ok {
		t.Fatal("partial message field not found")
	}
	if got != "Hello " {
		t.Fatalf("partial message=%q, want prefix before incomplete unicode escape", got)
	}
}

func TestSession_PauseTurn_DoesNotCountAsToolRound(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Return 3 pause_turns, then a final stop. With MaxToolRoundsPerInput=2,
	// this should succeed because pause_turns are not counted.
	callNum := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("searching..."),
					Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
				}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("still searching..."),
					Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
				}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("more searching..."),
					Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
				}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return wrapCommunicateResponse(llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("here is the answer"),
					Finish:  llm.FinishReason{Reason: "stop"},
				})
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2, // Only 2 real rounds allowed.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := sess.ProcessInput(ctx, "search for something", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if !strings.Contains(result, "here is the answer") {
		t.Fatalf("expected final answer in result, got: %q", result)
	}
	// All 4 LLM calls should have been made (3 pause_turns + 1 stop).
	reqs := f.Requests()
	if len(reqs) != 4 {
		t.Fatalf("expected 4 LLM calls (3 pause_turns + 1 stop), got %d", len(reqs))
	}
}

func TestSession_LoopDetection_WarningWording(t *testing.T) {
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LoopDetectionWindow: 3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

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
	if _, err := sess.ProcessInput(ctx, "loop", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	// Verify the LOOP_DETECTION event message matches the spec wording.
	var loopMsg string
	for _, ev := range events {
		if ev.Kind == EventLoopDetection {
			loopMsg, _ = ev.DataMap()["message"].(string)
			break
		}
	}
	if loopMsg == "" {
		t.Fatal("no LOOP_DETECTION event found")
	}
	if !strings.Contains(loopMsg, "stuck in a loop") {
		t.Fatalf("loop message should contain 'stuck in a loop', got: %q", loopMsg)
	}
	if !strings.Contains(loopMsg, "reasoning effort has been increased") {
		t.Fatalf("first loop detection should mention reasoning escalation, got: %q", loopMsg)
	}
}

func TestLooksLikeQuestion(t *testing.T) {
	tests := []struct {
		text     string
		expected bool
	}{
		{"What file should I edit?", true},
		{"Done.", false},
		{"Please provide the API key:", true},
		{"Which approach do you prefer?\n", true},
		{"I need more information.", false},
		{"", false},
		{"Result: success", false},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			got := looksLikeQuestion(tc.text)
			if got != tc.expected {
				t.Errorf("looksLikeQuestion(%q) = %v, want %v", tc.text, got, tc.expected)
			}
		})
	}
}

func TestSession_SetModel_TakesEffectOnNextCall(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	var capturedModel string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				capturedModel = req.Model
				return finalResponse("ok")
			},
			func(req llm.Request) llm.Response {
				capturedModel = req.Model
				return finalResponse("ok")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	if capturedModel != "test-model" {
		t.Errorf("expected 'test-model', got %q", capturedModel)
	}

	sess.SetModel("new-model")
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}
	if capturedModel != "new-model" {
		t.Errorf("expected 'new-model', got %q", capturedModel)
	}
}

func TestSession_SetTimeout(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetTimeout(30000)
	if sess.cfg.DefaultCommandTimeoutMS != 30000 {
		t.Errorf("expected 30000, got %d", sess.cfg.DefaultCommandTimeoutMS)
	}
}

func TestSession_RegisterTool(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.RegisterTool("my_tool", "a custom tool", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
		return "hello from custom", nil
	})

	tool := sess.reg.Get("my_tool")
	if tool == nil {
		t.Fatal("expected tool to be registered")
	}
	if tool.Definition.Description != "a custom tool" {
		t.Errorf("expected description 'a custom tool', got %q", tool.Definition.Description)
	}
}

func TestSession_RecordInputTokens_SkipsWebSearchResponse(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Response 1: contains ContentWebSearch — inflated tokens should NOT be recorded.
	// Response 2: no web search — tokens should be recorded normally.
	c.Register(&fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "test query"}},
							{Kind: llm.ContentText, Text: "Found results via web search."},
						},
					},
					Usage: llm.Usage{InputTokens: 200_000}, // inflated ~2x
				})
			},
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "done"}},
					},
					Usage: llm.Usage{InputTokens: 100_000}, // real count
				})
			},
		},
	})

	sess, err := NewSession(c, NewAnthropicProfile("claude-opus-4-6"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()

	// First call: web search response with inflated tokens.
	if _, err := sess.ProcessInput(ctx, "search something", nil); err != nil {
		t.Fatal(err)
	}

	// The inflated 200K should NOT have been recorded as lastInputTokens.
	lit := sess.contextMgr.LastInputTokens()
	if lit == 200_000 {
		t.Fatalf("lastInputTokens = %d; inflated web search tokens should not be recorded", lit)
	}

	// Second call: normal response.
	if _, err := sess.ProcessInput(ctx, "follow up", nil); err != nil {
		t.Fatal(err)
	}

	// Now the real 100K should be recorded.
	lit2 := sess.contextMgr.LastInputTokens()
	if lit2 != 100_000 {
		t.Fatalf("lastInputTokens after normal response = %d, want 100000", lit2)
	}
}

func TestSession_SetModel_UpdatesContextManager(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Assistant("ok"),
					Usage:   llm.Usage{InputTokens: 100_000},
				})
			},
			func(req llm.Request) llm.Response {
				return finalResponse("ok")
			},
		},
	})

	sess, err := NewSession(c, NewAnthropicProfile("claude-opus-4-6"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// After first ProcessInput, contextMgr has InputTokens from 200K profile.
	ctx := context.Background()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}

	// Pressure with 200K window: 100K/200K = 0.50
	p1 := sess.ContextPressure()
	if p1 < 0.40 || p1 > 0.60 {
		t.Fatalf("pressure before SetModel = %.2f, expected ~0.50", p1)
	}

	// Switch to 1M context model. Context manager should see the new window.
	sess.SetModel("claude-opus-4-6[1m]")

	// Pressure with 1M window: 100K/1M = 0.10
	p2 := sess.ContextPressure()
	if p2 > 0.20 {
		t.Fatalf("pressure after SetModel to 1M = %.2f, expected < 0.20 (stale profile?)", p2)
	}
}

func TestSession_ContentFilterRecovery_CompactsAndRetries(t *testing.T) {
	dir := t.TempDir()

	// Build a content filter error (HTTP 400 with invalid_prompt code).
	contentFilterErr := llm.ErrorFromHTTPStatus(
		"openai", 400, "content filter triggered",
		map[string]any{"error": map[string]any{"code": "invalid_prompt"}},
		nil,
	)

	callCount := 0
	f := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			// First call succeeds with a tool call to build up history.
			func(req llm.Request) (llm.Response, error) {
				callCount++
				return llm.Response{Message: llm.Message{
					Role: "assistant",
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Let me read the file."},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "call_1", Name: "exec_command",
							Arguments: json.RawMessage(`{"command":"echo hello","workdir":"/tmp"}`),
						}},
					},
				}}, nil
			},
			// Second call: content filter error.
			func(req llm.Request) (llm.Response, error) {
				callCount++
				return llm.Response{}, contentFilterErr
			},
			// Third call (after compaction): succeeds.
			func(req llm.Request) (llm.Response, error) {
				callCount++
				return finalResponse("recovered after compaction"), nil
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 20,
		LLMRetryPolicy:        &llm.RetryPolicy{MaxRetries: 0}, // no transport retries
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Collect events in background.
	var compactionCount int
	evDone := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == EventContextCompaction {
				compactionCount++
			}
		}
		close(evDone)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := sess.ProcessInput(ctx, "trigger content filter", nil)
	if err != nil {
		t.Fatalf("ProcessInput should have recovered, got error: %v", err)
	}
	sess.Close()
	<-evDone

	if !strings.Contains(out, "recovered after compaction") {
		t.Errorf("expected recovery text, got: %q", out)
	}
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (initial + filter + recovery), got %d", callCount)
	}
	if compactionCount == 0 {
		t.Error("expected at least one compaction event from content filter recovery")
	}
}

func TestSession_ContentFilterRecovery_FailsOnSecondFilterHit(t *testing.T) {
	dir := t.TempDir()

	contentFilterErr := llm.ErrorFromHTTPStatus(
		"openai", 400, "content filter triggered",
		map[string]any{"error": map[string]any{"code": "invalid_prompt"}},
		nil,
	)

	f := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			// First call succeeds with a tool call.
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Message{
					Role: "assistant",
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "working..."},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "call_1", Name: "exec_command",
							Arguments: json.RawMessage(`{"command":"echo hello","workdir":"/tmp"}`),
						}},
					},
				}}, nil
			},
			// Second call: content filter error.
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, contentFilterErr
			},
			// Third call (after compaction): content filter AGAIN.
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, contentFilterErr
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 20,
		LLMRetryPolicy:        &llm.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "trigger content filter twice", nil)
	if err == nil {
		t.Fatal("expected error on second content filter hit, got nil")
	}

	var cfe *llm.ContentFilterError
	if !errors.As(err, &cfe) {
		t.Errorf("expected ContentFilterError, got: %T: %v", err, err)
	}
	// All 3 steps should have been called: success, filter, recovery-filter.
	reqs := f.Requests()
	if len(reqs) != 3 {
		t.Errorf("expected 3 LLM calls (success + filter + recovery-filter), got %d", len(reqs))
	}
	sess.Close()
}

func TestSession_SystemPromptAsUser_CombinesIntoOneMessage(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		SystemPromptAsUser: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	task := "solve the mteb-retrieve task"
	if _, err := sess.ProcessInput(ctx, task, nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	msgs := reqs[0].Messages

	// Should be exactly one message (combined), not two separate ones.
	userMsgs := 0
	for _, m := range msgs {
		if m.Role == llm.RoleSystem {
			t.Fatal("SystemPromptAsUser should produce no system-role message")
		}
		if m.Role == llm.RoleUser {
			userMsgs++
		}
	}
	if userMsgs != 1 {
		t.Fatalf("expected 1 user message (combined), got %d", userMsgs)
	}

	combined := msgs[0].Text()

	// System prompt should be present (environment block is always included).
	if !strings.Contains(combined, "<environment>") {
		t.Fatal("combined message missing system prompt content")
	}

	// Task input should be present.
	if !strings.Contains(combined, task) {
		t.Fatal("combined message missing task input")
	}

	// System prompt should come FIRST, task after.
	sysIdx := strings.Index(combined, "<environment>")
	taskIdx := strings.Index(combined, task)
	if taskIdx < sysIdx {
		t.Fatalf("system prompt should precede task in combined message (sysIdx=%d taskIdx=%d)", sysIdx, taskIdx)
	}
}

func TestSession_Meta_PopulatesOriginalPrompt(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "write a haiku about goroutines", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	meta := sess.Meta()
	if meta.OriginalPrompt != "write a haiku about goroutines" {
		t.Fatalf("OriginalPrompt: got %q, want %q",
			meta.OriginalPrompt, "write a haiku about goroutines")
	}
}

func TestSession_Meta_OriginalPrompt_EmptyForFreshSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	meta := sess.Meta()
	if meta.OriginalPrompt != "" {
		t.Fatalf("OriginalPrompt: got %q, want empty", meta.OriginalPrompt)
	}
}

func TestSession_ProcessInput_WithImage_BuildsMultiPartUserMessage(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	imgs := []ImageAttachment{
		{MediaType: "image/png", Data: imgBytes, Name: "test.png"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "caption", imgs); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Check the transcript: the user-input turn should carry both a text part
	// and an image part.
	var userTurn *Turn
	for i := range sess.history {
		if sess.history[i].Kind == TurnUserInput {
			userTurn = &sess.history[i]
			break
		}
	}
	if userTurn == nil {
		t.Fatal("no TurnUserInput in history")
	}

	parts := userTurn.Message.Content
	if len(parts) != 2 {
		t.Fatalf("user message parts: got %d, want 2 (text + image)", len(parts))
	}

	var sawText, sawImage bool
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "caption" {
				t.Errorf("text part: got %q, want %q", p.Text, "caption")
			}
			sawText = true
		case llm.ContentImage:
			if p.Image == nil {
				t.Fatal("image part has nil Image")
			}
			if p.Image.MediaType != "image/png" {
				t.Errorf("image media_type: got %q, want image/png", p.Image.MediaType)
			}
			if string(p.Image.Data) != string(imgBytes) {
				t.Errorf("image bytes mismatch")
			}
			sawImage = true
		}
	}
	if !sawText || !sawImage {
		t.Fatalf("expected both text and image parts; sawText=%v sawImage=%v", sawText, sawImage)
	}
}

func TestSession_ProcessInput_ImageOnly_OmitsEmptyTextPart(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgs := []ImageAttachment{
		{MediaType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}, Name: "p.jpg"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "", imgs); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	var userTurn *Turn
	for i := range sess.history {
		if sess.history[i].Kind == TurnUserInput {
			userTurn = &sess.history[i]
			break
		}
	}
	if userTurn == nil {
		t.Fatal("no TurnUserInput in history")
	}
	parts := userTurn.Message.Content
	if len(parts) != 1 {
		t.Fatalf("parts: got %d, want 1 (image only)", len(parts))
	}
	if parts[0].Kind != llm.ContentImage {
		t.Fatalf("part kind: got %q, want %q", parts[0].Kind, llm.ContentImage)
	}
}
