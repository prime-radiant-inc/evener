package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func assertDeferredStubDef(t *testing.T, td llm.ToolDefinition, wantDesc string) {
	t.Helper()
	if td.Description != wantDesc {
		t.Errorf("stub %q Description = %q, want %q", td.Name, td.Description, wantDesc)
	}
	if td.Strict == nil || *td.Strict {
		t.Errorf("stub %q Strict = %v, want pointer to false", td.Name, td.Strict)
	}
	if typ, _ := td.Parameters["type"].(string); typ != "object" {
		t.Errorf("stub %q parameters.type = %#v, want \"object\"", td.Name, td.Parameters["type"])
	}
	if ap, ok := td.Parameters["additionalProperties"].(bool); !ok || !ap {
		t.Errorf("stub %q parameters.additionalProperties = %#v, want true", td.Name, td.Parameters["additionalProperties"])
	}
	props, ok := td.Parameters["properties"].(map[string]any)
	if !ok || len(props) != 0 {
		t.Errorf("stub %q parameters.properties = %#v, want empty map (no intent)", td.Name, td.Parameters["properties"])
	}
	if req, hasReq := td.Parameters["required"]; hasReq && req != nil {
		t.Errorf("stub %q parameters.required = %#v, want absent", td.Name, req)
	}
}

func assertFullTier1Def(t *testing.T, td llm.ToolDefinition) {
	t.Helper()
	if strings.TrimSpace(td.Description) == "" {
		t.Errorf("Tier-1 tool %q has empty Description", td.Name)
	}
	props, ok := td.Parameters["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		t.Errorf("Tier-1 tool %q has empty properties: %#v", td.Name, td.Parameters)
	}
}

func TestDeferredToolsTurn1ToolsAndPromptAvailability(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)
	c := llm.NewClient()
	var captured llm.Request
	c.Register(newFakeAdapter("openai", func(req llm.Request) llm.Response {
		captured = req
		return agenttest.FinalResponse("ok")
	}))

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(tripwireCtx(t), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	wireNames := toolNameSetFromDefinitions(captured.Tools)
	if wireNames["call_tool"] || sess.reg.Get("call_tool") != nil {
		t.Fatalf("call_tool must not be registered or advertised: wire=%v", toolNamesFromDefinitions(captured.Tools))
	}
	byName := make(map[string]llm.ToolDefinition, len(captured.Tools))
	for _, td := range captured.Tools {
		byName[td.Name] = td
	}
	// Core Tier-1 includes task_list, delegate, delegate_send, and tool_search upfront with full schemas; web_fetch is a deferred stub.
	for _, tier1 := range []string{"communicate", "exec_command", "read_file", "apply_patch", "write_file", "grep_files", "find_files", "use_skill", "tool_search", "delegate", "delegate_send", "task_list"} {
		td, ok := byName[tier1]
		if !ok {
			t.Errorf("Turn 1 req.Tools missing Tier-1 tool %q", tier1)
			continue
		}
		assertFullTier1Def(t, td)
	}
	if td, ok := byName["web_fetch"]; !ok {
		t.Errorf("Turn 1 req.Tools missing deferred stub for web_fetch")
	} else {
		assertDeferredStubDef(t, td, "")
	}
	if rt := sess.reg.Get("tool_search"); rt == nil || !rt.ReadOnly {
		t.Fatalf("tool_search must be registered as ReadOnly=true, got %+v", rt)
	}

	stubCount := 0
	for tier2 := range tier2DeferredGroups {
		if sess.reg.Get(tier2) == nil {
			continue
		}
		td, ok := byName[tier2]
		if !ok {
			t.Errorf("Turn 1 req.Tools missing deferred stub for %q", tier2)
			continue
		}
		assertDeferredStubDef(t, td, "")
		stubCount++
	}
	if stubCount == 0 {
		t.Fatal("expected at least one deferred Tier-2 stub on Turn 1")
	}

	data, _ := sess.buildPromptData(sess.currentEnv())
	for _, deferred := range []string{"job_watch", "manage_worktree", "read_transcript", "web_fetch", "compact_context", "model_list", "doctor_evener"} {
		if slices.Contains(data.UnavailableProfileToolNames, deferred) {
			t.Errorf("UnavailableProfileToolNames must not list deferred tool %q: %v", deferred, data.UnavailableProfileToolNames)
		}
		if data.HasTool(deferred) {
			t.Errorf("promptData.HasTool(%q) = true for deferred stub, want false to preserve compact Turn-1 prompt", deferred)
		}
	}
	for _, tier1 := range []string{"tool_search", "delegate", "delegate_send", "task_list"} {
		if !data.HasTool(tier1) {
			t.Errorf("promptData.HasTool(%q) = false, want true for Tier-1 tool", tier1)
		}
	}
	if !data.HasDelegateTool() {
		t.Error("promptData.HasDelegateTool() = false while delegate is registered")
	}
	if !data.HasJobWatchTool() {
		t.Error("promptData.HasJobWatchTool() = false while job_watch is registered")
	}
	for _, skillName := range []string{"running-jobs", "using-worktrees", "reading-transcripts"} {
		if !sess.skillAvailableToSession(skillName) {
			t.Errorf("skillAvailableToSession(%q) = false, want true while deferred tools are registered", skillName)
		}
	}
	// Turn-1 system prompt renders delegate guidance when delegate is Tier-1 without mentioning tool_search indirection.
	promptBody, _, _ := strings.Cut(sess.cachedSystemPrompt, "\n<environment>\n")
	if strings.Contains(promptBody, "`tool_search`") {
		t.Errorf("Turn 1 prompt body must not tell the model to load Tier-1 delegate via tool_search:\n%s", promptBody)
	}
	_ = systemPromptSection(t, sess.cachedSystemPrompt, "## Delegating")
	if !strings.Contains(sess.cachedSystemPrompt, "<available_agents>") || !strings.Contains(sess.cachedSystemPrompt, "</available_agents>") {
		t.Errorf("Turn 1 system prompt missing compact <available_agents> when delegate is registered")
	}
	// <available_agents> must not carry per-role tool lists (authoritative tool lists live in delegate's agent_type schema).
	if idx := strings.Index(sess.cachedSystemPrompt, "<available_agents>"); idx >= 0 {
		end := strings.Index(sess.cachedSystemPrompt[idx:], "</available_agents>")
		block := sess.cachedSystemPrompt[idx : idx+end]
		if strings.Contains(block, "Default tools:") {
			t.Errorf("<available_agents> must not carry per-role tool lists:\n%s", block)
		}
	}

	// Headless OpenAI session keeps 13 Tier-1 tools upfront (including task_list, delegate, delegate_send, tool_search) plus minimal stubs.
	var headlessReq llm.Request
	hc := llm.NewClient()
	hc.Register(newFakeAdapter("openai", func(req llm.Request) llm.Response {
		headlessReq = req
		return agenttest.FinalResponse("ok")
	}))
	headless, err := NewSession(hc, withTestSessionNamer(hc, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(headless): %v", err)
	}
	defer headless.Close()
	if _, err := headless.ProcessInput(tripwireCtx(t), "hello", nil); err != nil {
		t.Fatalf("headless ProcessInput: %v", err)
	}
	headlessWire := toolNameSetFromDefinitions(headlessReq.Tools)
	if headlessWire["call_tool"] {
		t.Fatalf("headless Turn 1 req.Tools unexpectedly includes call_tool")
	}
	headlessByName := make(map[string]llm.ToolDefinition, len(headlessReq.Tools))
	for _, td := range headlessReq.Tools {
		headlessByName[td.Name] = td
	}
	for _, tier1 := range []string{"communicate", "exec_command", "read_file", "apply_patch", "write_file", "grep_files", "find_files", "list_dir", "use_skill", "tool_search", "delegate", "delegate_send", "task_list"} {
		td, ok := headlessByName[tier1]
		if !ok {
			t.Errorf("headless Turn 1 req.Tools missing Tier-1 tool %q", tier1)
			continue
		}
		assertFullTier1Def(t, td)
	}
	headlessStubs := 0
	for _, td := range headlessReq.Tools {
		if headless.isDeferredTool(td.Name) {
			assertDeferredStubDef(t, td, "")
			headlessStubs++
		}
	}
	if got, want := len(headlessReq.Tools), 13+headlessStubs; got != want {
		t.Errorf("headless Turn 1 req.Tools count = %d (%v), want %d (13 Tier-1 + %d stubs)",
			got, toolNamesFromDefinitions(headlessReq.Tools), want, headlessStubs)
	}
	t.Logf("openai headless Turn 1: tools=%d (tier1=13 stubs=%d) tools_B=%d sys_B=%d tot_B=%d names=%v",
		len(headlessReq.Tools), headlessStubs, llm.ToolsJSONBytes(headlessReq.Tools), len(headless.cachedSystemPrompt),
		llm.ToolsJSONBytes(headlessReq.Tools)+len(headless.cachedSystemPrompt), toolNamesFromDefinitions(headlessReq.Tools))

	// DeepSeek / openai-compatible headless Turn 1.
	var dsReq llm.Request
	dc := llm.NewClient()
	dc.Register(newFakeAdapter("deepseek", func(req llm.Request) llm.Response {
		dsReq = req
		return agenttest.FinalResponse("ok")
	}))
	dsSess, err := NewSession(dc, withTestSessionNamer(dc, newOpenAICompatProfile("deepseek", "deepseek-chat", 128000)), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(deepseek headless): %v", err)
	}
	defer dsSess.Close()
	if _, err := dsSess.ProcessInput(tripwireCtx(t), "hello", nil); err != nil {
		t.Fatalf("deepseek headless ProcessInput: %v", err)
	}
	dsStubs := 0
	for _, td := range dsReq.Tools {
		if dsSess.isDeferredTool(td.Name) {
			dsStubs++
		}
	}
	if got, want := len(dsReq.Tools), 13+dsStubs; got != want {
		t.Errorf("deepseek headless Turn 1 req.Tools count = %d (%v), want %d (13 Tier-1 + %d stubs)",
			got, toolNamesFromDefinitions(dsReq.Tools), want, dsStubs)
	}
	t.Logf("deepseek headless Turn 1: tools=%d (tier1=13 stubs=%d) tools_B=%d sys_B=%d tot_B=%d names=%v",
		len(dsReq.Tools), dsStubs, llm.ToolsJSONBytes(dsReq.Tools), len(dsSess.cachedSystemPrompt),
		llm.ToolsJSONBytes(dsReq.Tools)+len(dsSess.cachedSystemPrompt), toolNamesFromDefinitions(dsReq.Tools))

	// Anthropic headless Turn 1.
	var antReq llm.Request
	ac := llm.NewClient()
	ac.Register(newFakeAdapter("anthropic", func(req llm.Request) llm.Response {
		antReq = req
		return agenttest.FinalResponse("ok")
	}))
	antSess, err := NewSession(ac, withTestSessionNamer(ac, newAnthropicProfile("claude-sonnet-4-6")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(anthropic headless): %v", err)
	}
	defer antSess.Close()
	if _, err := antSess.ProcessInput(tripwireCtx(t), "hello", nil); err != nil {
		t.Fatalf("anthropic headless ProcessInput: %v", err)
	}
	antStubs := 0
	for _, td := range antReq.Tools {
		if antSess.isDeferredTool(td.Name) {
			antStubs++
		}
	}
	if got, want := len(antReq.Tools), 13+antStubs; got != want {
		t.Errorf("anthropic headless Turn 1 req.Tools count = %d (%v), want %d (13 Tier-1 + %d stubs)",
			got, toolNamesFromDefinitions(antReq.Tools), want, antStubs)
	}
	t.Logf("anthropic headless Turn 1: tools=%d (tier1=%d stubs=%d) tools_B=%d sys_B=%d tot_B=%d names=%v",
		len(antReq.Tools), len(antReq.Tools)-antStubs, antStubs, llm.ToolsJSONBytes(antReq.Tools), len(antSess.cachedSystemPrompt),
		llm.ToolsJSONBytes(antReq.Tools)+len(antSess.cachedSystemPrompt), toolNamesFromDefinitions(antReq.Tools))
}

func TestToolSearchSchemasStaticToolsAndOmission(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)

	// 1. Multi-round flow: tool_search loads exact schemas while req.Tools stays 100% static across turns.
	c := llm.NewClient()
	adapter := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "ts-1",
				Name:      "tool_search",
				Type:      "function",
				Arguments: json.RawMessage(`{"names":["job_watch","manage_worktree"],"intent":"Loading deferred tool schemas"}`),
			})
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "mw-1",
				Name:      "manage_worktree",
				Type:      "function",
				Arguments: json.RawMessage(`{"operation":"list"}`),
			})
		},
		func(llm.Request) llm.Response {
			return agenttest.FinalResponse("ok")
		},
	)
	c.Register(adapter)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(tripwireCtx(t), "inspect worktrees", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	if !reflect.DeepEqual(reqs[0].Tools, reqs[1].Tools) || !reflect.DeepEqual(reqs[1].Tools, reqs[2].Tools) {
		t.Fatalf("req.Tools mutated across tool_search turns (breaks KV cache prefix)")
	}
	// Verify tool_search returned valid JSON array of live {name, description, parameters} schemas.
	var tsOut string
	for _, msg := range reqs[1].Messages {
		if msg.Role != llm.RoleTool {
			continue
		}
		for _, part := range msg.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "ts-1" {
				tsOut, _ = part.ToolResult.Content.(string)
			}
		}
	}
	var decoded []liveToolSchema
	if err := json.Unmarshal([]byte(tsOut), &decoded); err != nil {
		t.Fatalf("tool_search output not valid schema JSON (%v):\n%s", err, tsOut)
	}
	if len(decoded) != 2 || decoded[0].Name != "job_watch" || decoded[1].Name != "manage_worktree" {
		t.Fatalf("decoded schemas = %+v, want [job_watch, manage_worktree]", decoded)
	}
	for _, item := range decoded {
		if item.Description == "" || item.Parameters == nil || item.Parameters["properties"] == nil {
			t.Errorf("tool_search schema for %q incomplete: %+v", item.Name, item)
		}
	}

	// 2. Query modes: keyword query, "all", omitted, already-Tier-1, and unknown names.
	qRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ts-query",
		Name:      "tool_search",
		Type:      "function",
		Arguments: json.RawMessage(`{"query":"worktree","intent":"Searching worktree tools"}`),
	}, "")
	if qRes.IsError {
		t.Fatalf("tool_search(query=worktree) failed: %s", qRes.Output)
	}
	var qSchemas []liveToolSchema
	if err := json.Unmarshal([]byte(qRes.Output), &qSchemas); err != nil || len(qSchemas) == 0 || qSchemas[0].Name != "manage_worktree" {
		t.Fatalf("tool_search(query=worktree) = %q (err=%v), want manage_worktree schema", qRes.Output, err)
	}

	for _, args := range []string{`{"intent":"Listing all deferred"}`, `{"query":"all","intent":"Listing all deferred"}`} {
		listRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
			ID:        "ts-all",
			Name:      "tool_search",
			Type:      "function",
			Arguments: json.RawMessage(args),
		}, "")
		if listRes.IsError {
			t.Fatalf("tool_search(%s) failed: %s", args, listRes.Output)
		}
		for _, want := range []string{"job_watch", "manage_worktree", "read_transcript", "web_fetch"} {
			if !strings.Contains(listRes.Output, want) {
				t.Errorf("tool_search(%s) missing %q:\n%s", args, want, listRes.Output)
			}
		}
		for _, upfront := range []string{"read_file", "delegate", "delegate_send", "task_list"} {
			if strings.Contains(listRes.Output, "- "+upfront+":") {
				t.Errorf("tool_search(%s) listed Tier-1 %q as deferred:\n%s", args, upfront, listRes.Output)
			}
		}
	}

	wfRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ts-wf",
		Name:      "tool_search",
		Type:      "function",
		Arguments: json.RawMessage(`{"names":["web_fetch"],"intent":"Loading web_fetch schema"}`),
	}, "")
	if wfRes.IsError {
		t.Fatalf("tool_search(names=[web_fetch]) failed: %s", wfRes.Output)
	}
	var wfSchemas []liveToolSchema
	if err := json.Unmarshal([]byte(wfRes.Output), &wfSchemas); err != nil || len(wfSchemas) != 1 || wfSchemas[0].Name != "web_fetch" {
		t.Fatalf("tool_search(names=[web_fetch]) = %q (err=%v), want web_fetch schema", wfRes.Output, err)
	}

	t1Res := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ts-t1",
		Name:      "tool_search",
		Type:      "function",
		Arguments: json.RawMessage(`{"names":["read_file","delegate","delegate_send","task_list"],"intent":"Checking Tier-1 tools"}`),
	}, "")
	for _, upfront := range []string{"read_file", "delegate", "delegate_send", "task_list"} {
		if t1Res.IsError || !strings.Contains(t1Res.Output, upfront) {
			t.Fatalf("tool_search(names=[Tier-1]) = isErr:%v out:%q, want non-error naming %s", t1Res.IsError, t1Res.Output, upfront)
		}
	}

	unkRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ts-unk",
		Name:      "tool_search",
		Type:      "function",
		Arguments: json.RawMessage(`{"names":["no_such_tool"],"intent":"Testing unknown"}`),
	}, "")
	if !unkRes.IsError || !strings.Contains(unkRes.Output, "no_such_tool") {
		t.Fatalf("tool_search(names=[no_such_tool]) = isErr:%v out:%q, want error naming no_such_tool", unkRes.IsError, unkRes.Output)
	}

	// 3. Session with ZERO deferred tools omits tool_search completely from registry, req.Tools, and prompt.
	noDefCfg := SessionConfig{
		StateDir:         t.TempDir(),
		DisableSubagents: true,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
	noDefCfg.spawn.allowedToolNames = []string{"read_file", "write_file", "communicate"}
	noDefCfg.spawn.deniedToolNames = []string{"read_transcript"}
	noDefClient := llm.NewClient()
	noDefClient.Register(&fakeAdapter{name: "openai"})
	noDefSess, err := NewSession(noDefClient, withTestSessionNamer(noDefClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), noDefCfg)
	if err != nil {
		t.Fatalf("NewSession(no deferred): %v", err)
	}
	defer noDefSess.Close()
	if noDefSess.reg.Get("tool_search") != nil {
		t.Fatalf("tool_search registered when session has zero deferred tools: %v", noDefSess.reg.Names())
	}
	if hasToolDef(noDefSess.ToolDefinitions(), "tool_search") {
		t.Fatalf("tool_search advertised when session has zero deferred tools: %v", toolNamesFromDefinitions(noDefSess.ToolDefinitions()))
	}
	if strings.Contains(noDefSess.cachedSystemPrompt, "tool_search") {
		t.Errorf("system prompt mentions unavailable tool_search:\n%s", noDefSess.cachedSystemPrompt)
	}
	if noDefData, _ := noDefSess.buildPromptData(noDefSess.currentEnv()); noDefData.HasJobWatchTool() {
		t.Error("promptData.HasJobWatchTool() = true when job_watch is unregistered")
	}
	noDefBody, _, _ := strings.Cut(noDefSess.cachedSystemPrompt, "\nCurrent role/session:\n")
	noDefBody, _, _ = strings.Cut(noDefBody, "\n<environment>\n")
	if strings.Contains(noDefBody, "job_watch") {
		t.Errorf("operational prompt mentions unavailable job_watch:\n%s", noDefSess.cachedSystemPrompt)
	}

	// 4. When tool_search is explicitly denied, delegate stub and prompt never mention tool_search.
	deniedTSCfg := SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
	deniedTSCfg.spawn.deniedToolNames = []string{"tool_search"}
	deniedTSSess, err := NewSession(noDefClient, withTestSessionNamer(noDefClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), deniedTSCfg)
	if err != nil {
		t.Fatalf("NewSession(denied tool_search): %v", err)
	}
	defer deniedTSSess.Close()
	if deniedTSSess.reg.Get("tool_search") != nil || hasToolDef(deniedTSSess.ToolDefinitions(), "tool_search") {
		t.Fatal("denied tool_search must not be registered or advertised")
	}
	if strings.Contains(deniedTSSess.cachedSystemPrompt, "tool_search") {
		t.Errorf("prompt mentions denied tool_search:\n%s", deniedTSSess.cachedSystemPrompt)
	}
	for _, td := range deniedTSSess.ToolDefinitions() {
		if td.Name == "delegate" && strings.Contains(td.Description, "tool_search") {
			t.Errorf("delegate stub mentions denied tool_search: %q", td.Description)
		}
	}
}

func TestDelegationCueAbsentWhenDelegateUnregistered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// DisableSubagents=true (allowance 0) removes delegate completely: no stub, no delegate cue, no <available_agents>.
	noDel, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		DisableSubagents: true,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(DisableSubagents): %v", err)
	}
	defer noDel.Close()
	if noDel.reg.Get("delegate") != nil || hasToolDef(noDel.ToolDefinitions(), "delegate") {
		t.Fatal("delegate must not be registered or advertised when DisableSubagents=true")
	}
	noDelBody, _, _ := strings.Cut(noDel.cachedSystemPrompt, "\nCurrent role/session:\n")
	noDelBody, _, _ = strings.Cut(noDelBody, "\n<environment>\n")
	if strings.Contains(noDelBody, "## Delegating") || strings.Contains(noDelBody, "`delegate`") {
		t.Errorf("prompt body named `delegate` when delegate is unregistered:\n%s", noDelBody)
	}
	if strings.Contains(noDel.cachedSystemPrompt, "<available_agents>") {
		t.Error("prompt rendered <available_agents> when delegate is unregistered")
	}

	// Leaf subagent (depth=1, allowance=0) also emits neither delegate cue nor <available_agents>.
	leafCfg := SessionConfig{
		StateDir:        t.TempDir(),
		NonInteractive:  true,
		TurnEndsProcess: true,
		AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
	leafCfg.spawn.depth = 1
	leafCfg.spawn.parentSessionID = "parent"
	leafCfg.spawn.delegationAllowance = 0
	leaf, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), leafCfg)
	if err != nil {
		t.Fatalf("NewSession(leaf): %v", err)
	}
	defer leaf.Close()
	if leaf.reg.Get("delegate") != nil || hasToolDef(leaf.ToolDefinitions(), "delegate") {
		t.Fatal("leaf subagent must not have delegate registered or advertised")
	}
	leafBody, _, _ := strings.Cut(leaf.cachedSystemPrompt, "\nCurrent role/session:\n")
	leafBody, _, _ = strings.Cut(leafBody, "\n<environment>\n")
	if strings.Contains(leafBody, "## Delegating") || strings.Contains(leafBody, "`delegate`") {
		t.Errorf("leaf subagent prompt body named `delegate`:\n%s", leafBody)
	}
	if strings.Contains(leaf.cachedSystemPrompt, "<available_agents>") {
		t.Error("leaf subagent prompt rendered <available_agents>")
	}
}

func TestTier1ToolsConfigPromotionRespectsCeilings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		Tier1Tools:       []string{"manage_worktree", "read_transcript", "web_fetch", "notes_agent_set", "delegate"},
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
	cfg.spawn.deniedToolNames = []string{"delegate"}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	byName := make(map[string]llm.ToolDefinition)
	for _, td := range sess.ToolDefinitions() {
		byName[td.Name] = td
	}

	// Promoted registered tools receive full Tier-1 schemas upfront.
	for _, promoted := range []string{"manage_worktree", "read_transcript", "web_fetch"} {
		td, ok := byName[promoted]
		if !ok {
			t.Fatalf("promoted tool %q missing from ToolDefinitions", promoted)
		}
		assertFullTier1Def(t, td)
		if sess.isDeferredTool(promoted) {
			t.Errorf("isDeferredTool(%q) = true, want false after Tier1Tools promotion", promoted)
		}
	}
	// Non-promoted deferred tools remain empty stubs.
	assertDeferredStubDef(t, byName["job_watch"], "")

	// Denied or one-shot-omitted tools MUST NEVER be revealed by Tier1Tools!
	for _, forbidden := range []string{"delegate", "notes_agent_set"} {
		if _, ok := byName[forbidden]; ok {
			t.Errorf("Tier1Tools revealed unavailable/denied tool %q", forbidden)
		}
		if sess.reg.Get(forbidden) != nil {
			t.Errorf("Tier1Tools registered unavailable/denied tool %q", forbidden)
		}
	}

	// Promoting ALL registered deferred tools omits tool_search completely.
	allDeferred := make([]string, 0, len(tier2DeferredGroups))
	for name := range tier2DeferredGroups {
		allDeferred = append(allDeferred, name)
	}
	fullSess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NonInteractive:   true,
		TurnEndsProcess:  true,
		Tier1Tools:       allDeferred,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(all Tier1): %v", err)
	}
	defer fullSess.Close()
	if fullSess.reg.Get("tool_search") != nil || hasToolDef(fullSess.ToolDefinitions(), "tool_search") {
		t.Fatal("tool_search must be omitted when Tier1Tools promotes every registered deferred tool")
	}

	// Provider-visible alias promotion: passing a provider-visible name in Tier1Tools resolves via ToolNameMap.
	if got := canonicalToolName("exec_command", NewOpenAIProfile("gpt-5.2").ToolNameMap()); got != "shell" {
		t.Fatalf("canonicalToolName(exec_command) = %q, want shell", got)
	}
	wired := wireDeferredToolDef(llm.ToolDefinition{
		Name:        "shell",
		Description: "run shell",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "intent": map[string]any{"type": "string"}}},
	}, NewOpenAIProfile("gpt-5.2").ToolNameMap())
	if wired.Name != "exec_command" {
		t.Fatalf("wireDeferredToolDef Name = %q, want exec_command", wired.Name)
	}
	if _, hasIntent := wired.Parameters["properties"].(map[string]any)["intent"]; hasIntent {
		t.Fatalf("wireDeferredToolDef kept intent property: %#v", wired.Parameters)
	}
}

func TestDeferredStubDirectDispatchHooksAndSchemaDisclosure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var hookMu sync.Mutex
	var hookEvents []string
	hookClient := llm.NewClient()
	hookStep := func(req llm.Request) llm.Response {
		hookMu.Lock()
		hookEvents = append(hookEvents, req.Messages[len(req.Messages)-1].Text())
		hookMu.Unlock()
		return agenttest.FinalResponse(`{"ok":true}`)
	}
	hookSteps := []func(req llm.Request) llm.Response{hookStep, hookStep}
	hookClient.Register(newFakeAdapter("openai", hookSteps...))
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "^job_list$",
		Type:    "prompt",
		Prompt:  "pre hook",
		Timeout: 5,
	})
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "^job_list$",
		Type:    "prompt",
		Prompt:  "post hook",
		Timeout: 5,
	})
	sess.hookRunner = runner

	// Direct native invocation of a deferred stub dispatches through prepareToolCall + hooks + ExecutePreparedCall (no intent required at validation).
	okCall := llm.ToolCallData{
		ID:        "stub-1",
		Name:      "job_list",
		Type:      "function",
		Arguments: json.RawMessage(`{}`),
	}
	res := sess.execTool(tripwireCtx(t), okCall, "")
	if res.IsError {
		t.Fatalf("direct job_list({}) failed: %s", res.Output)
	}
	hookMu.Lock()
	gotHooks := len(hookEvents)
	hookMu.Unlock()
	if gotHooks != 2 {
		t.Fatalf("direct pre+post hook dispatches = %d, want 2 (%v)", gotHooks, hookEvents)
	}

	// Unknown parameter on a succeeded deferred JSON tool preserves valid JSON output
	// and discloses its schema once via steering.
	rtBad := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "stub-rt-bad",
		Name:      "read_transcript",
		Type:      "function",
		Arguments: json.RawMessage(`{"bogus_param":true}`),
	}, "")
	if rtBad.IsError || !json.Valid([]byte(rtBad.Output)) {
		t.Fatalf("succeeded read_transcript output must stay valid JSON, got isErr=%v out=%s", rtBad.IsError, rtBad.Output)
	}
	rtMsgs := sess.drainSteeringForCommunicate()
	if len(rtMsgs) != 1 || !strings.Contains(rtMsgs[0].Text, "<system-notification>") || !strings.Contains(rtMsgs[0].Text, `"name":"read_transcript"`) {
		t.Fatalf("succeeded read_transcript with unknown param should disclose schema via steering once, got: %+v", rtMsgs)
	}
	rtBad2 := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "stub-rt-bad-2",
		Name:      "read_transcript",
		Type:      "function",
		Arguments: json.RawMessage(`{"bogus_param":true}`),
	}, "")
	if rtMsgs2 := sess.drainSteeringForCommunicate(); len(rtMsgs2) != 0 || strings.Contains(rtBad2.Output, `"name":"read_transcript"`) {
		t.Fatalf("repeat read_transcript with unknown param re-disclosed schema: steer=%+v out=%s", rtMsgs2, rtBad2.Output)
	}

	// Invalid args on an undisclosed deferred tool disclose its compact schema once and succeed on retry.
	badRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "stub-2",
		Name:      "manage_worktree",
		Type:      "function",
		Arguments: json.RawMessage(`{"operation":"bogus"}`),
	}, "")
	if !badRes.IsError {
		t.Fatalf("invalid manage_worktree unexpectedly succeeded: %s", badRes.Output)
	}
	for _, want := range []string{"<system-notification>", `"name":"manage_worktree"`, "call these tools directly by name"} {
		if !strings.Contains(badRes.Output, want) {
			t.Fatalf("invalid manage_worktree Output missing %q, got: %s", want, badRes.Output)
		}
		if !strings.Contains(badRes.FullOutput, want) {
			t.Fatalf("invalid manage_worktree FullOutput missing %q, got: %s", want, badRes.FullOutput)
		}
	}
	if strings.Contains(badRes.Output, "call_tool") {
		t.Fatalf("deferred schema disclosure must not mention call_tool: %s", badRes.Output)
	}
	retryRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "stub-2-retry",
		Name:      "manage_worktree",
		Type:      "function",
		Arguments: json.RawMessage(`{"operation":"list"}`),
	}, "")
	if retryRes.IsError {
		t.Fatalf("manage_worktree retry with valid args failed: %s", retryRes.Output)
	}

	// Calling deferred web_fetch with empty args fails validation and discloses its schema cleanly.
	inspectRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "stub-inspect-webfetch",
		Name:      "web_fetch",
		Type:      "function",
		Arguments: json.RawMessage(`{}`),
	}, "")
	if !inspectRes.IsError {
		t.Fatalf("web_fetch({}) unexpectedly succeeded: %s", inspectRes.Output)
	}
	if !strings.Contains(inspectRes.Output, "<system-notification>") || !strings.Contains(inspectRes.Output, `"name":"web_fetch"`) || !strings.Contains(inspectRes.Output, `"url"`) {
		t.Fatalf("web_fetch({}) should disclose schema cleanly, got:\n%s", inspectRes.Output)
	}

	// call_tool is unregistered and fails as an unknown tool.
	unknownRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "stub-call-tool",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"job_list"}`),
	}, "")
	if !unknownRes.IsError {
		t.Fatalf("call_tool unexpectedly succeeded: %s", unknownRes.Output)
	}
}

func TestUseSkillDisclosesDeferredSchemasOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// First use_skill("using-worktrees") discloses manage_worktree schema.
	res1 := sess.execTool(tripwireCtx(t), useSkillCall("s1", "using-worktrees"), "")
	if res1.IsError {
		t.Fatalf("use_skill(using-worktrees) failed: %s", res1.Output)
	}
	if !strings.Contains(res1.Output, "<system-notification>") || !strings.Contains(res1.Output, `"name":"manage_worktree"`) {
		t.Fatalf("first use_skill(using-worktrees) missing manage_worktree schema: %s", res1.Output)
	}
	if strings.Contains(res1.Output, "call_tool") || !strings.Contains(res1.Output, "call these tools directly by name") {
		t.Fatalf("use_skill disclosure should direct native calls without call_tool: %s", res1.Output)
	}
	// Second invocation is idempotent: schema is not injected again.
	res2 := sess.execTool(tripwireCtx(t), useSkillCall("s2", "using-worktrees"), "")
	if res2.IsError {
		t.Fatalf("second use_skill(using-worktrees) failed: %s", res2.Output)
	}
	if strings.Contains(res2.Output, `"name":"manage_worktree"`) {
		t.Fatalf("second use_skill(using-worktrees) re-injected schema: %s", res2.Output)
	}

	// use_skill("running-jobs") discloses deferred job tools without re-disclosing Tier-1 delegate or <available_agents>.
	jobsRes := sess.execTool(tripwireCtx(t), useSkillCall("s3", "running-jobs"), "")
	if jobsRes.IsError {
		t.Fatalf("use_skill(running-jobs) failed: %s", jobsRes.Output)
	}
	for _, want := range []string{`"name":"job_watch"`, `"name":"job_status"`, `"name":"job_stop"`, `"name":"job_list"`} {
		if !strings.Contains(jobsRes.Output, want) {
			t.Errorf("use_skill(running-jobs) missing %q in output:\n%s", want, jobsRes.Output)
		}
	}
	for _, alreadyUpfront := range []string{`"name":"delegate"`, `"name":"delegate_send"`, "<available_agents>"} {
		if strings.Contains(jobsRes.Output, alreadyUpfront) {
			t.Errorf("use_skill(running-jobs) re-disclosed upfront %q:\n%s", alreadyUpfront, jobsRes.Output)
		}
	}
}

func TestReactiveDisclosuresOnBackgroundShellTruncationAndCompactNudge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                t.TempDir(),
		DefaultCommandTimeoutMS: 200,
		AgentsDocPath:           filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Background shell command queues job_watch/job_status/job_stop/job_list via steering once without mutating tool_result.
	bg1 := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "sh-1",
		Name:      "shell",
		Type:      "function",
		Arguments: json.RawMessage(`{"command":"echo bg-job-one","mode":"background","intent":"Testing background shell disclosure"}`),
	}, "")
	if bg1.IsError {
		t.Fatalf("background shell failed: %s", bg1.Output)
	}
	if strings.Contains(bg1.Output, `"name":"job_watch"`) {
		t.Fatalf("background shell mutated tool_result output with schema:\n%s", bg1.Output)
	}
	msgs := sess.drainSteeringForCommunicate()
	if len(msgs) != 1 {
		t.Fatalf("steering messages after first background shell = %d, want 1 (%+v)", len(msgs), msgs)
	}
	for _, want := range []string{"<system-notification>", "Manage or inspect jobs directly:", `"name":"job_watch"`, `"name":"job_status"`, `"name":"job_stop"`, `"name":"job_list"`} {
		if !strings.Contains(msgs[0].Text, want) {
			t.Errorf("first background shell steering missing %q:\n%s", want, msgs[0].Text)
		}
	}
	if strings.Contains(msgs[0].Text, "call_tool") {
		t.Errorf("background shell steering must not mention call_tool:\n%s", msgs[0].Text)
	}

	bg2 := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "sh-2",
		Name:      "shell",
		Type:      "function",
		Arguments: json.RawMessage(`{"command":"echo bg-job-two","mode":"background","intent":"Testing second background shell"}`),
	}, "")
	if bg2.IsError {
		t.Fatalf("second background shell failed: %s", bg2.Output)
	}
	if msgs := sess.drainSteeringForCommunicate(); len(msgs) != 0 {
		t.Errorf("second background shell should not queue repeat disclosure, got %+v", msgs)
	}

	// Windowed/truncated shell result queues read_transcript via steering once without expanding tool_result.
	big := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "sh-3",
		Name:      "shell",
		Type:      "function",
		Arguments: json.RawMessage(`{"command":"seq 1 3000","intent":"Testing job ref read_transcript disclosure"}`),
	}, "")
	if strings.Contains(big.Output, `"name":"read_transcript"`) {
		t.Fatalf("windowed shell output mutated tool_result with schema:\n%s", big.Output)
	}
	msgs = sess.drainSteeringForCommunicate()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, `"name":"read_transcript"`) {
		t.Fatalf("windowed shell steering missing read_transcript schema: %+v", msgs)
	}

	// Artifact truncation on a fresh session queues read_transcript via steering once without mutating tool_result.
	sessArt, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:      t.TempDir(),
		AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(artifact): %v", err)
	}
	defer sessArt.Close()
	sessArt.registerTool("huge_probe", "test-only huge output", map[string]any{"type": "object", "properties": map[string]any{}}, func(context.Context, any) (any, error) {
		return strings.Repeat("x", 25_000), nil
	})
	artRes := sessArt.execTool(tripwireCtx(t), llm.ToolCallData{ID: "art-1", Name: "huge_probe", Type: "function", Arguments: json.RawMessage(`{"intent":"Testing artifact disclosure"}`)}, "")
	if !strings.Contains(artRes.Output, "artifact:") {
		t.Fatalf("truncated artifact result missing artifact ref:\n%s", artRes.Output)
	}
	if strings.Contains(artRes.Output, `"name":"read_transcript"`) {
		t.Fatalf("truncated artifact result mutated tool_result with schema:\n%s", artRes.Output)
	}
	artMsgs := sessArt.drainSteeringForCommunicate()
	if len(artMsgs) != 1 || !strings.Contains(artMsgs[0].Text, `"name":"read_transcript"`) {
		t.Fatalf("truncated artifact steering missing read_transcript schema: %+v", artMsgs)
	}

	// Low-context warning discloses compact_context inside the steering nudge.
	p := sess.currentProfile()
	win := p.ContextWindowSize()
	if win <= 0 {
		win = 128_000
	}
	sess.contextMgr.RecordInputTokens(int(float64(win)*0.85), len(sess.history), "")
	if !sess.maybeNudgeSelfCompact(len(sess.cachedSystemPrompt)) {
		t.Fatal("maybeNudgeSelfCompact returned false under high pressure")
	}
	msgs = sess.drainSteeringForCommunicate()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Text, `"name":"compact_context"`) {
		t.Fatalf("compact nudge missing compact_context schema: %+v", msgs)
	}
}

func TestReactiveDisclosureArrivesInNextRequestSteeringWhileToolResultStaysBounded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	call := llm.ToolCallData{
		ID:        "sh-bound",
		Name:      "shell",
		Type:      "function",
		Arguments: json.RawMessage(`{"command":"printf 'l0\\nl1\\nl2\\nl3\\nl4\\nl5\\nl6\\nl7\\nl8\\nl9\\n'","intent":"Testing bounded shell result"}`),
	}
	f := newFakeAdapter("openai",
		func(llm.Request) llm.Response { return agenttest.ToolCallResponse(call) },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("ok") },
	)
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:      t.TempDir(),
		AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"shell": {MaxChars: 100_000, MaxLines: 4, Strategy: schema.TruncHeadTail},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(tripwireCtx(t), "run bounded shell", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	var toolResult string
	var steeringText string
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleTool {
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					toolResult, _ = p.ToolResult.Content.(string)
				}
			}
		}
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "<system-notification>") {
			steeringText = m.Text()
		}
	}
	if strings.Contains(toolResult, `"name":"read_transcript"`) || strings.Count(toolResult, "\n") > 8 {
		t.Fatalf("tool_result exceeded bounds or embedded schema:\n%s", toolResult)
	}
	if !strings.Contains(steeringText, `"name":"read_transcript"`) {
		t.Fatalf("next request missing read_transcript schema in steering turn; messages=%+v", reqs[1].Messages)
	}
}

func TestManagerAndWatchParentKeepJobsUpfront(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	mgr, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		Manager:       true,
		StateDir:      t.TempDir(),
		AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatalf("NewSession(Manager): %v", err)
	}
	defer mgr.Close()
	mgrTools := toolNameSetFromDefinitions(mgr.ToolDefinitions())
	mgrByName := make(map[string]llm.ToolDefinition, len(mgr.ToolDefinitions()))
	for _, td := range mgr.ToolDefinitions() {
		mgrByName[td.Name] = td
	}
	for _, want := range []string{"job_list", "job_status", "job_watch", "job_stop", "delegate", "delegate_send", "task_list", "read_transcript"} {
		if !mgrTools[want] {
			t.Errorf("Manager session missing upfront tool %q", want)
			continue
		}
		assertFullTier1Def(t, mgrByName[want])
	}
	assertDeferredStubDef(t, mgrByName["web_fetch"], "")

	obsCfg := SessionConfig{
		StateDir:      t.TempDir(),
		AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}
	obsCfg.spawn.parentSessionID = "parent"
	obsCfg.spawn.depth = 1
	obsCfg.spawn.parentWatchGranted = true
	obs, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), obsCfg)
	if err != nil {
		t.Fatalf("NewSession(observer): %v", err)
	}
	defer obs.Close()
	obsTools := toolNameSetFromDefinitions(obs.ToolDefinitions())
	obsByName := make(map[string]llm.ToolDefinition, len(obs.ToolDefinitions()))
	for _, td := range obs.ToolDefinitions() {
		obsByName[td.Name] = td
	}
	for _, want := range []string{"job_list", "job_status", "job_watch", "job_stop", "delegate_send", "task_list"} {
		if !obsTools[want] {
			t.Errorf("watch_parent observer missing upfront tool %q", want)
			continue
		}
		assertFullTier1Def(t, obsByName[want])
	}
	assertDeferredStubDef(t, obsByName["web_fetch"], "")
}

func TestReactiveDisclosuresOnDelegateAndTimedOutShell(t *testing.T) {
	t.Parallel()
	wantJobAndTranscript := []string{
		"<system-notification>",
		`"name":"job_watch"`,
		`"name":"job_status"`,
		`"name":"job_stop"`,
		`"name":"job_list"`,
		`"name":"read_transcript"`,
	}

	t.Run("successful_delegate_discloses_once_in_steering", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		childClient := llm.NewClient()
		childClient.Register(newFakeAdapter("openai", repeatFinalResponse(8, "child done")...))
		registerTestSessionNamer(childClient)

		delArgs := json.RawMessage(`{"prompt":"return ok","delegation_allowance":0}`)
		c := llm.NewClient()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "d1", Name: "delegate", Arguments: delArgs, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "d2", Name: "delegate", Arguments: delArgs, Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("c1", "done"))
			},
		)
		c.Register(adapter)

		sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 2,
			NonInteractive:   true,
			TurnEndsProcess:  true,
			AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			testOnly:         testConfig{childClientFactory: func() *llm.Client { return childClient }},
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()

		if _, err := sess.ProcessInput(tripwireCtx(t), "run two delegates", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		reqs := adapter.Requests()
		if len(reqs) < 3 {
			t.Fatalf("requests = %d, want at least 3", len(reqs))
		}
		if !reflect.DeepEqual(reqs[0].Tools, reqs[1].Tools) || !reflect.DeepEqual(reqs[1].Tools, reqs[2].Tools) {
			t.Fatal("req.Tools mutated across delegate rounds")
		}
		d1Out := toolResultTextForCall(reqs[1], "d1")
		if !json.Valid([]byte(d1Out)) || strings.Contains(d1Out, `"name":"job_status"`) {
			t.Fatalf("delegate tool_result must remain clean JSON without embedded schema: %s", d1Out)
		}

		countMatches := func(msgs []llm.Message, needle string) int {
			n := 0
			for _, m := range msgs {
				if m.Role == llm.RoleUser && strings.Contains(m.Text(), "<system-notification>") && strings.Contains(m.Text(), needle) {
					n++
				}
			}
			return n
		}
		for _, want := range wantJobAndTranscript {
			if got := countMatches(reqs[1].Messages, want); got != 1 {
				t.Errorf("after first delegate: steering messages matching %q = %d, want 1", want, got)
			}
			if got := countMatches(reqs[2].Messages, want); got != 1 {
				t.Errorf("after second delegate: steering messages matching %q = %d, want 1 (must not re-emit)", want, got)
			}
		}
	})

	t.Run("failed_delegate_does_not_disclose", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		childClient := llm.NewClient()
		childClient.Register(newFakeAdapter("openai", repeatFinalResponse(4, "child done")...))
		registerTestSessionNamer(childClient)

		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 2,
			NonInteractive:   true,
			TurnEndsProcess:  true,
			AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			testOnly:         testConfig{childClientFactory: func() *llm.Client { return childClient }},
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()

		badRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
			ID:        "d-bad",
			Name:      "delegate",
			Type:      "function",
			Arguments: json.RawMessage(`{"prompt":""}`),
		}, "")
		if !badRes.IsError {
			t.Fatalf("invalid delegate unexpectedly succeeded: %s", badRes.Output)
		}
		if msgs := sess.drainSteeringForCommunicate(); len(msgs) != 0 {
			t.Fatalf("failed delegate queued unexpected steering disclosure: %+v", msgs)
		}

		okRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
			ID:        "d-ok",
			Name:      "delegate",
			Type:      "function",
			Arguments: json.RawMessage(`{"prompt":"return ok","delegation_allowance":0}`),
		}, "")
		if okRes.IsError {
			t.Fatalf("valid delegate failed: %s", okRes.Output)
		}
		msgs := sess.drainSteeringForCommunicate()
		if len(msgs) != 1 {
			t.Fatalf("steering messages after valid delegate = %d, want 1 (%+v)", len(msgs), msgs)
		}
		for _, want := range wantJobAndTranscript {
			if !strings.Contains(msgs[0].Text, want) {
				t.Errorf("valid delegate steering missing %q:\n%s", want, msgs[0].Text)
			}
		}
	})

	t.Run("auto_backgrounded_and_timed_out_shell_discloses_job_controls", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			StateDir:                t.TempDir(),
			DefaultCommandTimeoutMS: 100,
			MaxCommandTimeoutMS:     100,
			AgentsDocPath:           filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer sess.Close()
		clk := agenttest.NewFakeClock()
		sess.jobManager.clock = clk

		ctx := tripwireCtx(t)
		resCh := make(chan tool.ExecResult, 1)
		go func() {
			resCh <- sess.execTool(ctx, llm.ToolCallData{
				ID:        "sh-auto-bg",
				Name:      "shell",
				Type:      "function",
				Arguments: json.RawMessage(`{"command":"printf start; sleep 2; ls","intent":"Testing auto-backgrounded shell disclosure"}`),
			}, "")
		}()
		timerReady := make(chan struct{})
		go func() {
			clk.BlockUntil(1)
			close(timerReady)
		}()
		var res tool.ExecResult
		var gotEarly bool
		select {
		case <-timerReady:
			clk.Advance(time.Second)
		case res = <-resCh:
			gotEarly = true
		case <-ctx.Done():
			t.Fatal("shell tool neither armed block timer nor returned")
		}
		if !gotEarly {
			select {
			case res = <-resCh:
			case <-ctx.Done():
				t.Fatal("shell tool did not return after foreground timeout")
			}
		}
		if res.IsError {
			t.Fatalf("auto-backgrounded shell failed: %s", res.Output)
		}
		var st struct {
			JobID    string `json:"job_id"`
			TimedOut bool   `json:"timed_out"`
		}
		if err := json.Unmarshal(res.ToolState, &st); err != nil || st.JobID == "" || !st.TimedOut {
			t.Fatalf("auto-backgrounded ToolState = %s (err=%v), want job_id and timed_out=true", string(res.ToolState), err)
		}
		_, _ = sess.jobManager.stop(st.JobID)
		waitForShellDone(t, sess.jobManager, st.JobID)

		msgs := sess.drainSteeringForCommunicate()
		if len(msgs) != 1 {
			t.Fatalf("steering messages after auto-backgrounded shell = %d, want 1 (%+v)", len(msgs), msgs)
		}
		for _, want := range wantJobAndTranscript {
			if !strings.Contains(msgs[0].Text, want) {
				t.Errorf("auto-backgrounded shell steering missing %q:\n%s", want, msgs[0].Text)
			}
		}

		// TimedOut=true with JobID!="" must also disclose job control schemas even when mode is foreground.
		sessFG, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
			StateDir:      t.TempDir(),
			AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		})
		if err != nil {
			t.Fatalf("NewSession(timed out fg): %v", err)
		}
		defer sessFG.Close()
		sessFG.steerReactiveToolDisclosure("shell", tool.ExecResult{
			ToolState: json.RawMessage(`{"job_id":"job_timedout_fg","mode":"foreground","timed_out":true}`),
		}, "")
		fgMsgs := sessFG.drainSteeringForCommunicate()
		if len(fgMsgs) != 1 {
			t.Fatalf("steering messages after timed-out foreground shell = %d, want 1 (%+v)", len(fgMsgs), fgMsgs)
		}
		for _, want := range wantJobAndTranscript {
			if !strings.Contains(fgMsgs[0].Text, want) {
				t.Errorf("timed-out foreground shell steering missing %q:\n%s", want, fgMsgs[0].Text)
			}
		}
	})
}
