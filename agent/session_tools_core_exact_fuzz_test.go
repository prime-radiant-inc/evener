//go:build serffuzz

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// FuzzSessionToolsCoreExactCoverage keeps the cold error, side-channel, and
// registry-cache contracts in the native fuzz-only coverage profile. All model
// calls use ScriptedAdapter and all filesystem behavior is test-owned.
func FuzzSessionToolsCoreExactCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, selector byte) {
		stceFileTools(t)
		stceVisionAndWeb(t, selector)
		stceCompaction(t)
		stceRegistryAndHelpers(t)
		stceExecToolBranches(t)
	})
}

type stceFileEnv struct {
	*agenttest.DenyEnv
	readOutput string
	readErr    error
	writeErr   error
	editErr    error
}

func (e *stceFileEnv) ReadFile(string, *int, *int) (string, error) { return e.readOutput, e.readErr }
func (e *stceFileEnv) WriteFile(path, content string) (string, error) {
	return "wrote:" + path + ":" + content, e.writeErr
}
func (e *stceFileEnv) EditFile(path, oldText, newText string, all bool) (string, error) {
	return fmt.Sprintf("edited:%s:%s:%s:%t", path, oldText, newText, all), e.editErr
}

func stceFileTools(t *testing.T) {
	t.Helper()
	tracked := ""
	warning := "warning: unread\n"
	deps := &toolDeps{readGuard: readGuard{
		trackRead:              func(path string) { tracked = path },
		readBeforeWriteWarning: func(string) string { return warning },
	}}
	reg := tool.NewRegistry()
	if err := registerFileTools(reg, deps); err != nil {
		t.Fatal(err)
	}
	exec := func(env execenv.ExecutionEnvironment, id, name string, args map[string]any) tool.ExecResult {
		return reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: id, Name: name, Arguments: stmMarshal(t, args)})
	}
	root := t.TempDir()
	for _, tc := range []struct{ path, output string }{
		{"pic.png", "[image: pic.png]\n" + base64.StdEncoding.EncodeToString([]byte("png"))},
		{"doc.pdf", "[document: doc.pdf]\n" + base64.StdEncoding.EncodeToString([]byte("pdf"))},
		{"plain.txt", "plain"},
	} {
		res := exec(&stceFileEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: root}, readOutput: tc.output}, "read", "read_file", map[string]any{"file_path": tc.path, "offset": 1.0, "limit": 2.0, "purpose": "inspect"})
		if res.IsError || tracked != tc.path {
			t.Fatalf("read %s = %#v tracked=%q", tc.path, res, tracked)
		}
	}
	readErr := errors.New("read failure")
	if res := exec(&stceFileEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: root}, readErr: readErr}, "read-error", "read_file", map[string]any{"file_path": "bad"}); !res.IsError {
		t.Fatalf("read error = %#v", res)
	}
	if res := exec(&stceFileEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: root}}, "write", "write_file", map[string]any{"file_path": "x", "content": "y"}); res.IsError || !strings.HasPrefix(res.Output, warning) {
		t.Fatalf("write warning = %#v", res)
	}
	if res := exec(&stceFileEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: root}, writeErr: errors.New("write failure")}, "write-error", "write_file", map[string]any{"file_path": "x", "content": "y"}); !res.IsError {
		t.Fatalf("write error = %#v", res)
	}
	if res := exec(&stceFileEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: root}}, "edit", "edit_file", map[string]any{"file_path": "x", "old_string": "a", "new_string": "b", "replace_all": true}); res.IsError || !strings.HasPrefix(res.Output, warning) {
		t.Fatalf("edit warning = %#v", res)
	}
	if res := exec(&stceFileEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: root}, editErr: errors.New("edit failure")}, "edit-error", "edit_file", map[string]any{"file_path": "x"}); !res.IsError {
		t.Fatalf("edit error = %#v", res)
	}

	sentinel := errors.New("register failure")
	for failAt := 1; failAt <= 2; failAt++ {
		calls := 0
		faultDeps := &toolDeps{registerTool: func(reg *tool.Registry, registered tool.RegisteredTool) error {
			calls++
			if calls == failAt {
				return sentinel
			}
			return reg.Register(registered)
		}}
		if err := registerFileTools(tool.NewRegistry(), faultDeps); !errors.Is(err, sentinel) {
			t.Fatalf("registration failure %d = %v", failAt, err)
		}
	}
}

func stceVisionAndWeb(t *testing.T, selector byte) {
	t.Helper()
	s, _, adapter := stmNewSession(t, []byte{selector})
	if got := s.describeImage(context.Background(), tool.ExecResult{}); got != "" {
		t.Fatalf("empty image = %q", got)
	}
	s.cfg.AgentName = "explorer"
	if got := s.describeImage(context.Background(), tool.ExecResult{ImageData: []byte("x")}); got != "" {
		t.Fatalf("explorer image = %q", got)
	}
	s.cfg.AgentName = ""
	if got := s.describeImage(context.Background(), tool.ExecResult{ImageData: []byte("x")}); got != "scripted vision" {
		t.Fatalf("default image = %q", got)
	}
	store := s.getOrCreateTaskStore()
	if _, err := store.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeImplement, Description: "vision", Prompt: "vision", ReasoningEffort: "max"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatal(err)
	}
	if got := s.describeImage(context.Background(), tool.ExecResult{ImageData: []byte("x"), ImageMediaType: "image/png"}); got != "scripted vision" {
		t.Fatalf("task effort image = %q", got)
	}

	_ = adapter

	result, err := s.webSearch(context.Background(), "query")
	if err != nil || result != "scripted vision" {
		t.Fatalf("web search = %#v, %v", result, err)
	}
	adapter.FaultResponder = func(llm.Request) error { return errors.New("search failure") }
	if got := s.describeImage(context.Background(), tool.ExecResult{ImageData: []byte("x")}); got != "" {
		t.Fatalf("vision error = %q", got)
	}
	if _, err := s.webSearch(context.Background(), "query"); err == nil || !strings.Contains(err.Error(), "web search failed") {
		t.Fatalf("web search error = %v", err)
	}
}

func stceCompaction(t *testing.T) {
	t.Helper()
	s, _, _ := stmNewSession(t, []byte("compact"))
	s.askPending = []askQuestion{{Question: "pending"}}
	if err := s.Compact(context.Background()); err == nil {
		t.Fatal("pending ask compact succeeded")
	}
	s.askPending = nil
	mgr := s.contextMgr
	s.contextMgr = nil
	if err := s.Compact(context.Background()); err == nil {
		t.Fatal("nil context manager compact succeeded")
	}
	s.contextMgr = mgr
	if records := s.runPreCompactHook(context.Background(), nil); records != nil {
		t.Fatalf("nil history records = %#v", records)
	}
	history := []schema.Turn{}
	records := appendSteeringMessagesToHistory(&history, []preCompactMessage{{text: ""}, {text: "  "}, {text: "kept", kind: events.SteeringKindPrecompactHook}})
	if len(records) != 1 {
		t.Fatalf("steering records = %#v", records)
	}
	s.flushSteeringTurnRecords(nil)
	s.cfg.testOnly.appendCompactionTurn = func(schema.Turn) error { return errors.New("append failure") }
	s.flushSteeringTurnRecords(records)
	s.cfg.testOnly.appendCompactionTurn = nil

	hookClient := llm.NewClient()
	hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(`{"systemMessage":"compact user","hookSpecificOutput":{"additionalContext":"compact model"}}`)}
	}})
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "compact"})
	s.hookRunner = runner
	s.runPreCompactHook(context.Background(), &history)

	_, emit, flush := s.compactionEmitFunc(context.Background(), &history)
	emit(events.EventWarning, events.WarningData{Message: "unrelated"})
	emit(events.EventContextCompaction, events.ContextCompactionData{})
	emit(events.EventContextCompaction, events.ContextCompactionData{})
	flush()
}

func stceRegistryAndHelpers(t *testing.T) {
	t.Helper()
	if got := newProfileToolRegistry(nil); got == nil {
		t.Fatal("nil profile registry")
	}
	badDefs := []llm.ToolDefinition{{Name: "bad", Parameters: map[string]any{"bad": func() {}}}}
	if _, ok := profileToolRegistryCacheKey(badDefs); ok {
		t.Fatal("unmarshalable registry key cached")
	}
	_ = newProfileToolRegistryForDefs(badDefs)
	reg := buildProfileToolRegistry([]llm.ToolDefinition{{Name: "communicate"}, {Name: "other"}})
	if _, err := reg.Get("other").Exec(context.Background(), nil, nil); err == nil {
		t.Fatal("placeholder executor succeeded")
	}
	p := NewOpenAIProfile("gpt-5.2")
	first := newProfileToolRegistry(p)
	first.Remove("read_file")
	if second := newProfileToolRegistry(p); second.Get("read_file") == nil {
		t.Fatal("registry cache shared mutation")
	}
	stceRegistrationFailures(t)

	if got := skippedToolResult(llm.ToolCallData{}, errors.New("closed")); !strings.Contains(got.Output, "closed") {
		t.Fatalf("skipped result = %#v", got)
	}
	if got := skippedToolResult(llm.ToolCallData{}, context.Canceled); !strings.Contains(got.Output, "closing") {
		t.Fatalf("canceled skipped result = %#v", got)
	}
	var call llm.ToolCallData
	if err := applyUpdatedToolInput(nil, map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdatedToolInput(&call, nil); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdatedToolInput(&call, map[string]any{"x": func() {}}); err == nil {
		t.Fatal("unmarshalable update succeeded")
	}
	if got := optionalIntArg(map[string]any{"n": "bad"}, "n"); got != nil {
		t.Fatalf("optional int = %v", *got)
	}
	if got := optionalIntArg(map[string]any{}, "n"); got != nil {
		t.Fatalf("missing optional int = %v", *got)
	}
	if got := (&Session{env: &agenttest.DenyEnv{WorkDir: "/work"}}).resolveFilePath(" /abs "); got != filepath.Clean("/abs") {
		t.Fatalf("absolute path = %q", got)
	}

	s, _, _ := stmNewSession(t, []byte("helpers"))
	s.appendCanceledToolResults(nil, nil, nil)
	s.appendCanceledToolResults([]llm.ToolCallData{{ID: "c", Name: "x"}}, nil, nil)
	s.delegationAllowance = 0
	_ = s.defaultToolSummaryForAgent(plugin.Agent{AllTools: true})
	if !isResultToolDefinition("communicate", "renamed", "") || !isResultToolDefinition("other", "result", "result") {
		t.Fatal("result tool classification")
	}
	mcpDef := llm.ToolDefinition{Name: "mcp_custom", Description: "mcp", Parameters: map[string]any{"type": "object"}}
	dummyExec := func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return "ok", nil }
	if err := s.reg.Register(tool.RegisteredTool{Tool: llm.Tool{Definition: mcpDef}, Exec: dummyExec}); err != nil {
		t.Fatal(err)
	}
	if err := s.reg.Register(tool.RegisteredTool{Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "raw_custom", Description: "raw", Parameters: map[string]any{"properties": map[string]any{}}}}, Exec: dummyExec}); err != nil {
		t.Fatal(err)
	}
	s.mcpTools = []llm.ToolDefinition{mcpDef}
	s.rebuildToolDefsCache()
	_ = normalizeRegistryToolDefinition(llm.ToolDefinition{Parameters: map[string]any{"properties": map[string]any{}}})
	deps := newToolDeps(s)
	_, _ = deps.skill("missing")

	shared := s.getOrCreateTaskStore()
	if _, err := shared.Append([]taskpkg.TaskInput{{Type: taskpkg.TaskTypeImplement, Description: "remind", Prompt: "remind"}}); err != nil {
		t.Fatal(err)
	}
	if err := shared.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatal(err)
	}
	child, _, _ := stmNewSession(t, []byte("shared"))
	child.cfg.spawn.sharedTaskStore = shared
	child.taskStore = nil
	child.taskStoreOnce = sync.Once{}
	if child.getOrCreateTaskStore() != shared {
		t.Fatal("shared task store not reused")
	}
	s.mu.Lock()
	s.taskToolEverUsed = true
	s.taskToolLastRound = 0
	s.totalRounds = 25
	s.mu.Unlock()
	if got, _ := s.maybeInjectTaskReminder(); got == "" {
		t.Fatalf("task inactivity reminder missing: tasks=%#v storeSame=%v", shared.View(), s.getOrCreateTaskStore() == shared)
	}
}

func stceRegistrationFailures(t *testing.T) {
	t.Helper()
	s, _, _ := stmNewSession(t, []byte("registration"))
	sentinel := errors.New("registration failure")
	for _, tc := range []struct {
		name string
		call func(*tool.Registry, *Session) error
	}{
		{"read_file", registerMinimalWorktreeTools},
		{"read_file", registerCoreTools},
		{"shell", registerCoreTools},
		{"job_status", registerCoreTools},
		{"read_transcript", registerCoreTools},
	} {
		s.cfg.testOnly.registerTool = func(reg *tool.Registry, registered tool.RegisteredTool) error {
			if registered.Tool.Definition.Name == tc.name {
				return sentinel
			}
			return reg.Register(registered)
		}
		if err := tc.call(tool.NewRegistry(), s); !errors.Is(err, sentinel) {
			t.Fatalf("registration %s = %v", tc.name, err)
		}
	}
}

type stceGrantEnv struct {
	*agenttest.DenyEnv
	grants []string
}

func (e *stceGrantEnv) WithSandboxInvocationGrant(path string) execenv.ExecutionEnvironment {
	e.grants = append(e.grants, path)
	return e
}

func stceExecToolBranches(t *testing.T) {
	t.Helper()
	newSession := func() *Session {
		s, _, _ := stmNewSession(t, []byte("exec"))
		s.RegisterTool("probe", "probe", map[string]any{"type": "object"}, func(context.Context, any) (any, error) { return "ok", nil })
		return s
	}
	setClosing := func(s *Session) { s.mu.Lock(); s.closing = true; s.mu.Unlock() }
	for _, phase := range []string{"after_pre_hook", "before_side_effects", "after_start", "after_execute", "after_side_effect_lock"} {
		s := newSession()
		s.cfg.testOnly.execToolCheckpoint = func(got string) {
			if got == phase {
				setClosing(s)
			}
		}
		if res := s.execTool(context.Background(), stmCall(t, phase, "probe", map[string]any{})); !res.IsError {
			t.Fatalf("closing phase %s = %#v", phase, res)
		}
	}

	for _, response := range []string{
		`{"systemMessage":"hook user","hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"denied","additionalContext":"hook model"}}`,
		`{"systemMessage":"hook user","hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"value":"updated"},"additionalContext":"hook model"}}`,
	} {
		s := newSession()
		client := llm.NewClient()
		client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(response)} }})
		runner := hooks.NewRunner(client, "gpt-5.2")
		runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "pre"})
		s.hookRunner = runner
		args := json.RawMessage(`{"value":"original"}`)
		if strings.Contains(response, "updatedInput") {
			args = json.RawMessage(`{`)
		}
		res := s.execTool(context.Background(), llm.ToolCallData{ID: "hook", Name: "probe", Arguments: args})
		if !res.IsError {
			t.Fatalf("pre hook branch = %#v", res)
		}
	}

	s := newSession()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(`{"systemMessage":"post user","hookSpecificOutput":{"additionalContext":"post model"}}`)}
	}})
	runner := hooks.NewRunner(client, "gpt-5.2")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "post"})
	s.hookRunner = runner
	_ = s.execTool(context.Background(), stmCall(t, "post", "probe", map[string]any{}))
	_ = s.execTool(context.Background(), llm.ToolCallData{ID: "item", ItemID: "item-id", Name: "probe", Arguments: json.RawMessage(`{}`)})

	grantEnv := &stceGrantEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: t.TempDir()}}
	s.env = grantEnv
	call := stmCall(t, "grant", "probe", map[string]any{})
	_ = s.rerunToolWithGrant(context.Background(), call)
	_ = s.rerunToolWithGrant(withInvocationGrant(context.Background(), "/granted"), call)
	_ = (toolCallRerunner{session: s, call: call}).run(context.Background())
	if len(grantEnv.grants) != 1 {
		t.Fatalf("grant env calls = %#v", grantEnv.grants)
	}
}
