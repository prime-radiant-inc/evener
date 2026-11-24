package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

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
	if !wireNames["call_tool"] {
		t.Fatalf("Turn 1 req.Tools missing call_tool: %v", toolNamesFromDefinitions(captured.Tools))
	}
	for _, tier1 := range []string{"communicate", "exec_command", "read_file", "apply_patch", "write_file", "grep_files", "find_files", "use_skill"} {
		if !wireNames[tier1] {
			t.Errorf("Turn 1 req.Tools missing Tier-1 tool %q", tier1)
		}
	}
	for tier2 := range tier2DeferredGroups {
		if wireNames[tier2] {
			t.Errorf("Turn 1 req.Tools unexpectedly includes Tier-2 tool %q", tier2)
		}
	}
	var callToolDef *llm.ToolDefinition
	for i := range captured.Tools {
		if captured.Tools[i].Name == "call_tool" {
			callToolDef = &captured.Tools[i]
			break
		}
	}
	if callToolDef == nil {
		t.Fatal("Turn 1 req.Tools missing call_tool definition")
	}
	for _, deferred := range []string{"delegate", "job_watch", "manage_worktree", "read_transcript", "task_list", "web_fetch"} {
		if !strings.Contains(callToolDef.Description, deferred) {
			t.Errorf("call_tool description missing deferred tool %q: %s", deferred, callToolDef.Description)
		}
	}

	data, _ := sess.buildPromptData(sess.currentEnv())
	for _, deferred := range []string{"delegate", "job_watch", "manage_worktree", "read_transcript", "task_list", "web_fetch"} {
		if slices.Contains(data.UnavailableProfileToolNames, deferred) {
			t.Errorf("UnavailableProfileToolNames must not list deferred tool %q: %v", deferred, data.UnavailableProfileToolNames)
		}
	}
	for _, skillName := range []string{"running-jobs", "using-worktrees", "reading-transcripts"} {
		if !sess.skillAvailableToSession(skillName) {
			t.Errorf("skillAvailableToSession(%q) = false, want true while deferred tools are registered", skillName)
		}
	}
	if strings.Contains(sess.cachedSystemPrompt, "<available_agents>") {
		t.Errorf("Turn 1 system prompt should omit <available_agents> while delegate is deferred")
	}

	// Headless session keeps the 10 Tier-1 tools upfront (ask_user omitted when NonInteractive=true).
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
	if got, want := len(headlessReq.Tools), 10; got != want {
		t.Errorf("headless Turn 1 req.Tools count = %d (%v), want %d", got, toolNamesFromDefinitions(headlessReq.Tools), want)
	}
	headlessWire := toolNameSetFromDefinitions(headlessReq.Tools)
	for _, tier1 := range []string{"communicate", "exec_command", "read_file", "apply_patch", "write_file", "grep_files", "find_files", "list_dir", "use_skill", "call_tool"} {
		if !headlessWire[tier1] {
			t.Errorf("headless Turn 1 req.Tools missing Tier-1 tool %q", tier1)
		}
	}
	t.Logf("openai headless Turn 1: tools=%d tools_B=%d sys_B=%d tot_B=%d names=%v",
		len(headlessReq.Tools), llm.ToolsJSONBytes(headlessReq.Tools), len(headless.cachedSystemPrompt),
		llm.ToolsJSONBytes(headlessReq.Tools)+len(headless.cachedSystemPrompt), toolNamesFromDefinitions(headlessReq.Tools))

	// DeepSeek / openai-compatible headless Turn 1 keeps the 10 canonical Tier-1 tools upfront.
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
	if got, want := len(dsReq.Tools), 10; got != want {
		t.Errorf("deepseek headless Turn 1 req.Tools count = %d (%v), want %d", got, toolNamesFromDefinitions(dsReq.Tools), want)
	}
	t.Logf("deepseek headless Turn 1: tools=%d tools_B=%d sys_B=%d tot_B=%d names=%v",
		len(dsReq.Tools), llm.ToolsJSONBytes(dsReq.Tools), len(dsSess.cachedSystemPrompt),
		llm.ToolsJSONBytes(dsReq.Tools)+len(dsSess.cachedSystemPrompt), toolNamesFromDefinitions(dsReq.Tools))
}

func TestCallToolDispatchesValidationHooksAndExecution(t *testing.T) {
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
	hookClient.Register(newFakeAdapter("openai", hookStep, hookStep))
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "^task_list$",
		Type:    "prompt",
		Prompt:  "pre hook",
		Timeout: 5,
	})
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "^task_list$",
		Type:    "prompt",
		Prompt:  "post hook",
		Timeout: 5,
	})
	sess.hookRunner = runner

	// Valid call_tool invocation dispatches through prepareToolCall + hooks + ExecutePreparedCall.
	okCall := llm.ToolCallData{
		ID:        "ct-1",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"task_list","arguments":{"add":[{"description":"Check build","prompt":"Run go test"}]},"intent":"Adding task via call_tool"}`),
	}
	res := sess.execTool(tripwireCtx(t), okCall, "")
	if res.IsError {
		t.Fatalf("valid call_tool(task_list) failed: %s", res.Output)
	}
	hookMu.Lock()
	gotHooks := len(hookEvents)
	hookMu.Unlock()
	if gotHooks != 2 {
		t.Fatalf("inner pre+post hook dispatches = %d, want 2 (%v)", gotHooks, hookEvents)
	}
	if tasks := sess.Tasks(); len(tasks) != 1 || tasks[0].Description != "Check build" {
		t.Fatalf("sess.Tasks() = %+v, want 1 task 'Check build'", tasks)
	}

	// Invalid args on an undisclosed deferred tool disclose its compact schema once.
	badCall := llm.ToolCallData{
		ID:        "ct-2",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"manage_worktree","arguments":{"operation":"bogus"},"intent":"Testing validation disclosure"}`),
	}
	badRes := sess.execTool(tripwireCtx(t), badCall, "")
	if !badRes.IsError {
		t.Fatalf("invalid call_tool(manage_worktree) unexpectedly succeeded: %s", badRes.Output)
	}
	if !strings.Contains(badRes.Output, "<system-notification>") || !strings.Contains(badRes.Output, `"name":"manage_worktree"`) {
		t.Fatalf("invalid undisclosed call_tool should include compact schema in error, got: %s", badRes.Output)
	}

	// Calling a directly callable or unknown tool via call_tool returns an error naming the target or available tools.
	tier1Call := llm.ToolCallData{
		ID:        "ct-3",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"read_file","arguments":{"file_path":"README.md"},"intent":"Testing Tier-1 rejection"}`),
	}
	tier1Res := sess.execTool(tripwireCtx(t), tier1Call, "")
	if !tier1Res.IsError || !strings.Contains(tier1Res.Output, "read_file") {
		t.Fatalf("call_tool(read_file) = isErr:%v out:%q, want error naming read_file", tier1Res.IsError, tier1Res.Output)
	}

	// Discovery query via call_tool(name="list") summarizes deferred tools cleanly.
	listRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ct-list",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"list","intent":"Listing deferred tools"}`),
	}, "")
	if listRes.IsError {
		t.Fatalf("call_tool(list) failed: %s", listRes.Output)
	}
	for _, want := range []string{"delegate", "job_watch", "manage_worktree", "read_transcript", "task_list", "web_fetch"} {
		if !strings.Contains(listRes.Output, want) {
			t.Errorf("call_tool(list) output missing %q:\n%s", want, listRes.Output)
		}
	}

	// Omitting arguments on a tool with required params returns its schema/hint cleanly.
	inspectRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ct-inspect-delegate",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"delegate","intent":"Inspecting delegate schema"}`),
	}, "")
	if !inspectRes.IsError {
		t.Fatalf("call_tool(delegate) with omitted arguments unexpectedly succeeded: %s", inspectRes.Output)
	}
	if !strings.Contains(inspectRes.Output, "<system-notification>") || !strings.Contains(inspectRes.Output, `"name":"delegate"`) || !strings.Contains(inspectRes.Output, `"prompt"`) {
		t.Fatalf("call_tool(delegate) with omitted arguments should disclose schema cleanly, got:\n%s", inspectRes.Output)
	}

	// Unknown tool error still fails and names available deferred tools.
	unknownRes := sess.execTool(tripwireCtx(t), llm.ToolCallData{
		ID:        "ct-unknown",
		Name:      "call_tool",
		Type:      "function",
		Arguments: json.RawMessage(`{"name":"no_such_tool","intent":"Testing unknown deferred tool"}`),
	}, "")
	if !unknownRes.IsError || !strings.Contains(unknownRes.Output, "delegate") {
		t.Fatalf("call_tool(no_such_tool) = isErr:%v out:%q, want available deferred tool list", unknownRes.IsError, unknownRes.Output)
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
	// Second invocation is idempotent: schema is not injected again.
	res2 := sess.execTool(tripwireCtx(t), useSkillCall("s2", "using-worktrees"), "")
	if res2.IsError {
		t.Fatalf("second use_skill(using-worktrees) failed: %s", res2.Output)
	}
	if strings.Contains(res2.Output, `"name":"manage_worktree"`) {
		t.Fatalf("second use_skill(using-worktrees) re-injected schema: %s", res2.Output)
	}

	// use_skill("running-jobs") discloses jobs tools + <available_agents>.
	jobsRes := sess.execTool(tripwireCtx(t), useSkillCall("s3", "running-jobs"), "")
	if jobsRes.IsError {
		t.Fatalf("use_skill(running-jobs) failed: %s", jobsRes.Output)
	}
	for _, want := range []string{`"name":"job_watch"`, `"name":"job_status"`, `"name":"delegate"`, "<available_agents>"} {
		if !strings.Contains(jobsRes.Output, want) {
			t.Errorf("use_skill(running-jobs) missing %q in output:\n%s", want, jobsRes.Output)
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
	for _, want := range []string{"<system-notification>", `"name":"job_watch"`, `"name":"job_status"`, `"name":"job_stop"`, `"name":"job_list"`} {
		if !strings.Contains(msgs[0].Text, want) {
			t.Errorf("first background shell steering missing %q:\n%s", want, msgs[0].Text)
		}
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
	for _, want := range []string{"job_list", "job_status", "job_watch", "job_stop", "delegate", "delegate_send", "read_transcript"} {
		if !mgrTools[want] {
			t.Errorf("Manager session missing upfront tool %q", want)
		}
	}

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
	for _, want := range []string{"job_list", "job_status", "job_watch", "job_stop", "delegate_send"} {
		if !obsTools[want] {
			t.Errorf("watch_parent observer missing upfront tool %q", want)
		}
	}
}
