package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// kata wnfz: SetReasoningEffort mutates s.cfg (which is part of the persisted
// meta.json payload) but did not flush meta. If the daemon crashed before the
// next happy-path turn boundary, on-disk meta.json would still reflect the
// previous effort. The fix calls maybeAutoSave inside the setter (with the
// session mutex released first, since maybeAutoSave re-acquires it via Meta).
func TestSession_SetReasoningEffort_FlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	sessID := sess.ID()
	sess.SetReasoningEffort("high")

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.Config.ReasoningEffort != "high" {
		t.Fatalf("meta.Config.ReasoningEffort: got %q want %q", meta.Config.ReasoningEffort, "high")
	}
}

// kata wnfz: SetModel mutates s.profile, which Meta() surfaces as the top-level
// Model field. Without the fix, a daemon crash between SetModel and the next
// happy-path autosave leaves meta.json reflecting the previous model.
func TestSession_SetModel_FlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	sessID := sess.ID()
	sess.SetModel("gpt-5.3")

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.Model != "gpt-5.3" {
		t.Fatalf("meta.Model: got %q want %q", meta.Model, "gpt-5.3")
	}
}

// kata wnfz: SetTimeout mutates s.cfg.DefaultCommandTimeoutMS. Same crash
// window: without flushing, on-disk meta.json keeps the previous timeout
// until the next happy-path autosave.
func TestSession_SetTimeout_FlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	sessID := sess.ID()
	sess.SetTimeout(45_000)

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.Config.DefaultCommandTimeoutMS != 45_000 {
		t.Fatalf("meta.Config.DefaultCommandTimeoutMS: got %d want %d", meta.Config.DefaultCommandTimeoutMS, 45_000)
	}
}

func TestSession_SetModelAndTimeout_NoOpAfterClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                dir,
		DefaultCommandTimeoutMS: 10_000,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	sessID := sess.ID()
	sess.Close()

	before, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta before setters: %v", err)
	}

	sess.SetModel("gpt-5.3")
	sess.SetTimeout(45_000)

	after, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta after setters: %v", err)
	}
	if after.Model != before.Model {
		t.Fatalf("closed SetModel changed meta model: got %q want %q", after.Model, before.Model)
	}
	if after.Config.DefaultCommandTimeoutMS != before.Config.DefaultCommandTimeoutMS {
		t.Fatalf("closed SetTimeout changed meta timeout: got %d want %d", after.Config.DefaultCommandTimeoutMS, before.Config.DefaultCommandTimeoutMS)
	}
	if got := sess.Meta().Model; got != before.Model {
		t.Fatalf("closed SetModel changed in-memory model: got %q want %q", got, before.Model)
	}
	if got := sess.Meta().Config.DefaultCommandTimeoutMS; got != before.Config.DefaultCommandTimeoutMS {
		t.Fatalf("closed SetTimeout changed in-memory timeout: got %d want %d", got, before.Config.DefaultCommandTimeoutMS)
	}
}

func TestSession_NaturalCompletion_LoadsOnlyProfileDocs(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	for _, want := range []string{"<environment>", "Working directory:", "Platform:", "Today's date:", "Knowledge cutoff:"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
}

func TestSession_NaturalCompletion_LoadsOnlyProfileDocs_Anthropic(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	t.Parallel()
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	t.Parallel()
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

	sess, err := NewSession(c, newGeminiProfile("gemini-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

func TestSearchToolsDefaultPathAndFilterWhenOmitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("notes.txt", "the quick brown fox\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "google"})
	sess, err := NewSession(c, newGeminiProfile("gemini-test"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// glob with no path must default to the working dir. Regression: an omitted
	// arg was rendered with fmt.Sprint as the literal string "<nil>", so glob
	// searched a bogus "<nil>" directory and matched nothing.
	globRes := sess.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID: "g1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.txt"}`),
	})
	if globRes.IsError || !strings.Contains(globRes.Output, "notes.txt") {
		t.Fatalf("glob without path must find notes.txt in the working dir; got %q (err=%v)", globRes.Output, globRes.IsError)
	}

	// grep over a directory with no glob_filter must search all files. Regression:
	// an omitted glob_filter became -g "<nil>", filtering every file out, so
	// directory-wide grep silently returned empty.
	grepRes := sess.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
		ID: "r1", Name: "grep", Arguments: json.RawMessage(`{"pattern":"quick","path":"."}`),
	})
	if grepRes.IsError || !strings.Contains(grepRes.Output, "quick") {
		t.Fatalf("grep over a directory without glob_filter must find the match; got %q (err=%v)", grepRes.Output, grepRes.IsError)
	}
}

func TestSession_CoreTools_ListDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.txt", "hello\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := env.WriteFile("sub/b.txt", "world\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "google"})
	sess, err := NewSession(c, newGeminiProfile("gemini-test"), env, SessionConfig{})
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
	// Plain-text ls-style output: files as "name\tsize", directories suffixed "/",
	// nested names depth-prefixed (depth=2).
	if strings.Contains(res.Output, "{") {
		t.Fatalf("list_dir output must be plain text, not JSON:\n%s", res.Output)
	}
	for _, want := range []string{"a.txt\t", "sub/\n", "sub/b.txt\t"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("list_dir missing %q:\n%s", want, res.Output)
		}
	}
}

func TestSession_ToolLoop_ExecutesToolsAndContinues(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestSession_PreToolUseUpdatedInputRewritesToolCall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "Write",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"updatedInput":{"content":"rewritten by hook"}}}'`,
		Timeout: 5,
	})
	sess.hookRunner = runner

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "c1",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"file_path":"hook.txt","content":"original"}`),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("write_file error: %s", res.FullOutput)
	}
	got, err := os.ReadFile(filepath.Join(dir, "hook.txt"))
	if err != nil {
		t.Fatalf("read hook.txt: %v", err)
	}
	if string(got) != "rewritten by hook" {
		t.Fatalf("hook.txt = %q, want rewritten content", string(got))
	}
}

func TestSession_PreCompactHookOnlyRunsWhenCompactionEmits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{contextStrategyOverride: compactionEventStrategy{}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	runner := hooks.NewRunner(nil, "")
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		sess.emit(kind, data)
	})
	runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo precompact`,
		Timeout: 5,
	})
	sess.hookRunner = runner

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	if got := countHookStarts(*eventsPtr, plugin.HookPreCompact); got != 0 {
		t.Fatalf("PreCompact hook starts = %d, want 0 without compaction", got)
	}
}

func TestSession_PreCompactHookRunsAtCompactionBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{contextStrategyOverride: compactionEventStrategy{emitCompaction: true}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	runner := hooks.NewRunner(nil, "")
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		sess.emit(kind, data)
	})
	runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo precompact`,
		Timeout: 5,
	})
	sess.hookRunner = runner

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	if got := countHookStarts(*eventsPtr, plugin.HookPreCompact); got != 1 {
		t.Fatalf("PreCompact hook starts = %d, want 1", got)
	}
}

func TestSession_CompactEmitsCompactionTurnEvent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return finalResponse("forced summary")
		},
	}})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.contextMgr.PreserveRecentTurns = 1
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I will inspect the project and report back with enough detail to become a working note.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	sess.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	var got []events.CompactionTurnData
	for _, ev := range *eventsPtr {
		if ev.Kind == events.EventCompactionTurn {
			data, ok := ev.Data.(events.CompactionTurnData)
			if !ok {
				t.Fatalf("compaction turn data=%T", ev.Data)
			}
			got = append(got, data)
		}
	}
	if len(got) == 0 {
		t.Fatalf("missing %s event in %+v", events.EventCompactionTurn, *eventsPtr)
	}
	if got[0].Kind != string(schema.TurnCheckpoint) || !strings.Contains(got[0].Text, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("first compaction event=%+v", got[0])
	}
}

func TestSession_CompactionReminderUsesTranscriptTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.handleCompactionTurn(schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nsummary")))

	sess.mu.Lock()
	queue := append([]steeringMessage(nil), sess.steeringQueue...)
	sess.mu.Unlock()

	if len(queue) == 0 {
		t.Fatal("expected compaction transcript steering reminder")
	}
	got := queue[len(queue)-1].Text
	wantCall := `read_session_transcript({"transcript_ref": "local:` + sess.ID() + `", "format": "markdown"})`
	wantOutlineCall := `read_session_transcript({"transcript_ref": "local:` + sess.ID() + `", "format": "outline"})`
	wantRangeCall := `read_session_transcript({"transcript_ref": "local:` + sess.ID() + `", "range": "A-B"})`
	for _, want := range []string{
		"<SYSTEM-REMINDER>",
		"If you need the exact transcript of this session before compaction",
		"use the transcript tool instead of reading raw transcript files directly",
		wantCall,
		"For long sessions, first get a turn map",
		wantOutlineCall,
		wantRangeCall,
		"</SYSTEM-REMINDER>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compaction transcript reminder missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, ".transcript.jsonl") || strings.Contains(got, dir) {
		t.Fatalf("compaction transcript reminder should not expose raw transcript path; got:\n%s", got)
	}
}

func TestSession_NotificationHookRunsOnWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	marker := filepath.Join(dir, "notification-hook")
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "touch " + shellQuote(marker),
		Timeout: 5,
	})
	sess.hookRunner = runner

	sess.emit(events.EventWarning, events.WarningData{Message: "heads up"})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Notification hook did not run: %v", err)
	}
}

func TestSession_SubagentStopHookRunsWhenSubagentFinishes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "subagent-stop-hook")
	plugin := makePluginDir(t, "subagent-stop-plugin")
	hooksDir := filepath.Join(plugin, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{
		"hooks": {
			"SubagentStop": [{
				"matcher": "*",
				"hooks": [{"type": "command", "command": "touch ` + shellQuote(marker) + `"}]
			}]
		}
	}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("subagent done") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
		PluginDirs:       []string{plugin},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	result, err := sess.spawnAgent(context.Background(), "inspect", "", "", 1, "explorer", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	var spawned struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
		t.Fatalf("spawn result: %v", err)
	}
	if spawned.AgentID == "" {
		t.Fatalf("spawn result missing agent_id: %s", result)
	}
	waitForRuntimeSubagent(t, sess, spawned.AgentID)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("SubagentStop hook did not run: %v", err)
	}
}

func TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsShellJSONValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"head -c 60000 </dev/zero | tr '\\\\0' 'x'"}`),
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"shell": {MaxChars: 800, Strategy: schema.TruncHeadTail},
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
	toolResult := ""
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool {
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					if s, ok := p.ToolResult.Content.(string); ok {
						toolResult = s
					}
				}
			}
		}
	}
	if toolResult == "" {
		t.Fatalf("expected tool result content")
	}
	// Shell returns plain text now (output + footer), not JSON. The per-tool char
	// limit still bounds it far below the 60k raw output; allow the registry
	// truncation marker's overhead on top of the configured 800.
	if got := len([]rune(toolResult)); got > 1200 {
		t.Fatalf("tool result not bounded by configured limit: got %d (want ~800 + marker)", got)
	}
	if !strings.Contains(toolResult, "Tool output was truncated") && !strings.Contains(toolResult, "elided") {
		t.Fatalf("a 60k output under an 800-char limit must show truncation/elision: %s", toolResult)
	}
}

func TestSession_ToolOutputTruncation_LineLimitPreservesStreamingShellJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'l0\\nl1\\nl2\\nl3\\nl4\\nl5\\nl6\\nl7\\nl8\\nl9\\n'"}`),
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"shell": {MaxChars: 100_000, MaxLines: 4, Strategy: schema.TruncHeadTail},
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
	// Shell returns plain text now; the MaxLines:4 limit truncates it to a head+tail
	// of the output lines. The head (l0) and tail (l9) survive the line truncation.
	if !strings.Contains(truncated, "l0") || !strings.Contains(truncated, "l9") {
		t.Fatalf("line-truncated result must keep head and tail lines: %s", truncated)
	}
	if got := strings.Count(truncated, "\n"); got > 8 {
		t.Fatalf("result not line-bounded: %d newlines, want the MaxLines:4 head+tail+marker", got)
	}
}

func TestSession_ParallelToolCalls_RunConcurrentlyWhenSupported(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Register a slow read-only tool that blocks until the test releases it.
	// ReadOnly: true ensures the ordered-group algorithm batches both calls
	// for parallel execution (non-read-only tools are serialized).
	_ = sess.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name: "slow",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "integer"}},
			},
		}, ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
	t.Parallel()
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

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	makeTool := func(name string, readOnly bool) tool.RegisteredTool {
		return tool.RegisteredTool{
			Tool: llm.Tool{
				Definition: llm.ToolDefinition{
					Name:       name,
					Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				},
				ReadOnly: readOnly,
			},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
		"<git>",
		"Branch:",
		"Modified files: 1",
		"Untracked files: 1",
		"Recent commits:",
		"init",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
	// Ensure the branch has a value (not just an empty placeholder).
	if i := strings.Index(sys, "Branch: "); i >= 0 {
		val := strings.TrimSpace(strings.Split(strings.TrimPrefix(sys[i:], "Branch: "), "\n")[0])
		if val == "" {
			t.Fatalf("expected non-empty branch:\n%s", sys)
		}
	}
}

func TestSession_UserInstructionOverride_AppendedLastToSystemPrompt(t *testing.T) {
	t.Parallel()
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

func TestSession_CustomRegisteredTool_AppearsInSystemPrompt(t *testing.T) {
	t.Parallel()
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
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Register a custom tool after session creation.
	if err := sess.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "my_custom_tool", Description: "Does custom things"}},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "test", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	var found *events.SessionEvent
	for i, ev := range evs {
		if ev.Kind == events.EventToolCallEnd {
			found = &evs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no TOOL_CALL_END event found")
	}

	// Success: Output is populated, Error is empty (not present).
	d, ok := found.Data.(events.ToolCallEndData)
	if !ok {
		t.Fatalf("expected ToolCallEndData, got %T", found.Data)
	}
	// For success, Error should be empty.
	if d.Error != "" {
		t.Fatalf("TOOL_CALL_END for success should not have error, got %q", d.Error)
	}
	// The wire JSON should not carry legacy keys "full_output" or "is_error".
	dm := marshalToMap(t, found.Data)
	if _, ok := dm["full_output"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'full_output' key")
	}
	if _, ok := dm["is_error"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'is_error' key")
	}
}

func TestSession_ToolCallEnd_UsesErrorKeyOnFailure(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "read missing file", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	var found *events.SessionEvent
	for i, ev := range evs {
		if ev.Kind == events.EventToolCallEnd {
			found = &evs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no TOOL_CALL_END event found")
	}

	// Error: Error field should be populated, Output empty.
	d, ok := found.Data.(events.ToolCallEndData)
	if !ok {
		t.Fatalf("expected ToolCallEndData, got %T", found.Data)
	}
	if d.Error == "" {
		t.Fatalf("expected non-empty error in TOOL_CALL_END data for error case")
	}
	if d.Output != "" {
		t.Fatalf("TOOL_CALL_END for error should not have output, got %q", d.Output)
	}
	// The wire JSON should not carry legacy keys "full_output" or "is_error".
	dm := marshalToMap(t, found.Data)
	if _, ok := dm["full_output"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'full_output' key")
	}
	if _, ok := dm["is_error"]; ok {
		t.Fatal("TOOL_CALL_END should not have 'is_error' key")
	}
}

func TestSession_SetModel_TakesEffectOnNextCall(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestSession_SetModel_UpdatesContextManager(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestSession_SystemPromptAsUser_CombinesIntoOneMessage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

func TestSession_SystemPromptAsUserPreservesImageParts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		SystemPromptAsUser: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "caption this", []ImageAttachment{{
		MediaType: "image/png",
		Data:      imgBytes,
		Name:      "test.png",
	}}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: got %d want 1", len(reqs))
	}
	msg := reqs[0].Messages[0]
	if msg.Role != llm.RoleUser {
		t.Fatalf("first message role=%q, want user", msg.Role)
	}
	var sawSystem, sawTask, sawImage bool
	for _, part := range msg.Content {
		switch part.Kind {
		case llm.ContentText:
			if strings.Contains(part.Text, "<environment>") {
				sawSystem = true
			}
			if strings.Contains(part.Text, "caption this") {
				sawTask = true
			}
		case llm.ContentImage:
			if part.Image != nil && part.Image.MediaType == "image/png" && bytes.Equal(part.Image.Data, imgBytes) {
				sawImage = true
			}
		}
	}
	if !sawSystem || !sawTask || !sawImage {
		t.Fatalf("combined user message missing parts: system=%v task=%v image=%v content=%+v", sawSystem, sawTask, sawImage, msg.Content)
	}
}

func TestSession_ProjectDocsCachedAtInit(t *testing.T) {
	t.Parallel()
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
	env := execenv.NewLocalExecutionEnvironment(dir)
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

// TestSessionConfigDefaultSubagentDepth pins the default subagent depth at 2: a
// root session derives its delegation allowance from MaxSubagentDepth, so the
// default of 2 lets a delegate itself delegate one level (grant allowance 1).
func TestSessionConfigDefaultSubagentDepth(t *testing.T) {
	t.Parallel()
	var c SessionConfig
	c.applyDefaults()
	if c.MaxSubagentDepth != 2 {
		t.Fatalf("default MaxSubagentDepth = %d, want 2", c.MaxSubagentDepth)
	}

	// An explicit value is preserved, not overwritten by the default.
	explicit := SessionConfig{MaxSubagentDepth: 5}
	explicit.applyDefaults()
	if explicit.MaxSubagentDepth != 5 {
		t.Fatalf("explicit MaxSubagentDepth = %d, want 5 preserved", explicit.MaxSubagentDepth)
	}
}
