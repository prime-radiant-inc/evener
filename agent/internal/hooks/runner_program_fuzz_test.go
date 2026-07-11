//go:build serffuzz

package hooks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// FuzzHookRunnerProgram drives Runner through every supported event with a
// mutex-safe scripted prompt client. It exercises real matching, validation,
// prompt substitution, output parsing, routing, denial/block decisions, input
// merging, lifecycle event emission, and no-client behavior.
//
// SAFETY: command handlers route through a per-runner recording runtime. The
// default process runtime is never used, so this target cannot start a shell,
// process, Git command, network client, or provider request.
func FuzzHookRunnerProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{0x00},
		{0x01, 0x31},
		{0x7f, 0x72, 0x21},
		{0xff, 0x00, 0x4a, 0x91},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		reader := hookRunnerProgramReader{data: program}
		mode := reader.byte()
		token := reader.token()

		first := runHookRunnerProgram(t, mode, token)
		second := runHookRunnerProgram(t, mode, token)
		if !reflect.DeepEqual(first, second) {
			firstJSON, _ := json.Marshal(first)
			secondJSON, _ := json.Marshal(second)
			t.Fatalf("hook runner program is not deterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
		}
	})
}

type hookRunnerProgramReader struct {
	data []byte
	pos  int
}

func (r *hookRunnerProgramReader) byte() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[r.pos%len(r.data)]
	r.pos++
	return b
}

func (r *hookRunnerProgramReader) token() string {
	buf := make([]byte, int(r.byte()%16)+1)
	for i := range buf {
		buf[i] = r.byte()
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

type hookRunnerProgramTrace struct {
	Pre                PreToolUseResult
	PreRead            PreToolUseResult
	CommandPre         PreToolUseResult
	Post               RunResult
	CommandPost        RunResult
	Stop               StopResult
	SubagentStop       StopResult
	UserPrompt         RunResult
	Startup            RunResult
	BlankStartup       RunResult
	Resume             RunResult
	Clear              RunResult
	PreCompact         RunResult
	Notification       RunResult
	NoClient           RunResult
	PublicClient       RunResult
	Summary            []string
	SupportedSummary   []string
	Diagnostics        []string
	Calls              []hookRunnerProgramCall
	CommandPlans       []hookRunnerProgramCommandPlan
	Events             []hookRunnerProgramEvent
	ExitPolicies       []string
	EmptySubstitution  string
	OverrideModelCalls int
}

type hookRunnerProgramCommandPlan struct {
	Program string
	Args    []string
	Input   Input
	Env     []string
	Timeout time.Duration
}

type hookRunnerProgramCall struct {
	Model  string
	Prompt string
}

type hookRunnerProgramEvent struct {
	Kind       events.EventKind
	Event      string
	HookType   string
	Matcher    string
	PluginName string
	ExitCode   int
}

type hookRunnerProgramClient struct {
	mu    sync.Mutex
	calls []hookRunnerProgramCall
}

type hookRunnerProgramProviderAdapter struct {
	mu    sync.Mutex
	calls int
}

func (*hookRunnerProgramProviderAdapter) Name() string { return "program-provider" }

func (a *hookRunnerProgramProviderAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if len(req.Messages) != 1 || req.Messages[0].Text() != "program:public-client" {
		return llm.Response{}, errors.New("unexpected public client request")
	}
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return llm.Response{Message: llm.Assistant("public client output")}, nil
}

func (*hookRunnerProgramProviderAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("stream not used by hook runner program")
}

func (a *hookRunnerProgramProviderAdapter) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (c *hookRunnerProgramClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != llm.RoleUser {
		return llm.Response{}, errors.New("scripted prompt hook received malformed request")
	}
	prompt := req.Messages[0].Text()
	c.mu.Lock()
	c.calls = append(c.calls, hookRunnerProgramCall{Model: req.Model, Prompt: prompt})
	c.mu.Unlock()

	var output string
	switch {
	case strings.Contains(prompt, "program:pre-deny"):
		output = `{"systemMessage":"pre visible","terminalSequence":"pre-terminal","hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"deny reason","additionalContext":"pre context","updatedInput":{"source":"deny","shared":"first"}}}`
	case strings.Contains(prompt, "program:pre-ask"):
		output = `{"hookSpecificOutput":{"permissionDecision":"ask","additionalContext":"ask context","updatedInput":{"shared":"second","answer":"yes"}}}`
	case strings.Contains(prompt, "program:pre-read"):
		output = `{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"read":"ok"}}}`
	case strings.Contains(prompt, "program:post-json"):
		output = `{"systemMessage":"post json","terminalSequence":"post-terminal","hookSpecificOutput":{"additionalContext":"post context"}}`
	case strings.Contains(prompt, "program:post-plain"):
		output = "post plain"
	case strings.Contains(prompt, "program:post-error"):
		return llm.Response{}, errors.New("scripted prompt failure")
	case strings.Contains(prompt, "program:stop-block"):
		output = `{"decision":"block","reason":"stop reason","terminalSequence":"stop-terminal","systemMessage":"stop json"}`
	case strings.Contains(prompt, "program:subagent-stop"):
		output = `{"decision":"approve","reason":"approved","terminalSequence":"sub-terminal"}`
	case strings.Contains(prompt, "program:user-prompt"):
		output = "user context"
	case strings.Contains(prompt, "program:startup"):
		output = "startup context"
	case strings.Contains(prompt, "program:resume"):
		output = `{"systemMessage":"resume json"}`
	case strings.Contains(prompt, "program:session-end"):
		output = "end ignored"
	case strings.Contains(prompt, "program:pre-compact"):
		output = "compact user"
	case strings.Contains(prompt, "program:notification"):
		output = `{"systemMessage":"notify json","hookSpecificOutput":{"additionalContext":"notify context"}}`
	default:
		return llm.Response{}, errors.New("scripted prompt hook marker not recognized")
	}

	return llm.Response{Model: req.Model, Message: llm.Assistant(output)}, nil
}

func (c *hookRunnerProgramClient) snapshotCalls() []hookRunnerProgramCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := append([]hookRunnerProgramCall(nil), c.calls...)
	sort.Slice(calls, func(i, j int) bool {
		if calls[i].Model != calls[j].Model {
			return calls[i].Model < calls[j].Model
		}
		return calls[i].Prompt < calls[j].Prompt
	})
	return calls
}

// hookRunnerProgramCommandRuntime is the sole command-hook external boundary
// for this fuzz target. It returns scripted outcomes and records the fully
// prepared invocation, but never creates an exec.Cmd or consults host state.
type hookRunnerProgramCommandRuntime struct {
	mu      sync.Mutex
	baseEnv []string
	plans   []hookRunnerProgramCommandPlan
}

func (r *hookRunnerProgramCommandRuntime) Environ() []string {
	return append([]string(nil), r.baseEnv...)
}

func (r *hookRunnerProgramCommandRuntime) Run(ctx context.Context, invocation commandHookInvocation) (hookResult, error) {
	if err := ctx.Err(); err != nil {
		return hookResult{}, err
	}
	var input Input
	if err := json.Unmarshal(invocation.InputJSON, &input); err != nil {
		return hookResult{}, err
	}
	r.mu.Lock()
	r.plans = append(r.plans, hookRunnerProgramCommandPlan{
		Program: invocation.Program,
		Args:    append([]string(nil), invocation.Args...),
		Input:   input,
		Env:     append([]string(nil), invocation.Env...),
		Timeout: invocation.Timeout,
	})
	r.mu.Unlock()

	switch invocation.Program {
	case "fixture-pre":
		return hookResult{Stderr: "command pre denied", ExitCode: 2}, nil
	case "bash":
		if reflect.DeepEqual(invocation.Args, []string{"-c", "fixture-post"}) {
			return hookResult{Stdout: `{"systemMessage":"command post","terminalSequence":"command-post-terminal","hookSpecificOutput":{"additionalContext":"command post context"}}`}, nil
		}
	case "fixture-notification-error":
		return hookResult{}, errors.New("recorded executor fault")
	}
	return hookResult{}, errors.New("unexpected scripted command invocation")
}

func (r *hookRunnerProgramCommandRuntime) snapshotPlans() []hookRunnerProgramCommandPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	plans := append([]hookRunnerProgramCommandPlan(nil), r.plans...)
	for i := range plans {
		sort.Strings(plans[i].Env)
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Program != plans[j].Program {
			return plans[i].Program < plans[j].Program
		}
		return strings.Join(plans[i].Args, "\x00") < strings.Join(plans[j].Args, "\x00")
	})
	return plans
}

type hookRunnerProgramEventLog struct {
	mu     sync.Mutex
	events []hookRunnerProgramEvent
}

func (l *hookRunnerProgramEventLog) add(kind events.EventKind, data events.EventData) {
	var event hookRunnerProgramEvent
	switch value := data.(type) {
	case events.HookStartData:
		event = hookRunnerProgramEvent{
			Kind:       kind,
			Event:      value.Event,
			HookType:   value.HookType,
			Matcher:    value.Matcher,
			PluginName: value.PluginName,
		}
	case events.HookEndData:
		event = hookRunnerProgramEvent{
			Kind:       kind,
			Event:      value.Event,
			HookType:   value.HookType,
			Matcher:    value.Matcher,
			PluginName: value.PluginName,
			ExitCode:   value.ExitCode,
		}
	default:
		return
	}
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *hookRunnerProgramEventLog) snapshot() []hookRunnerProgramEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	values := append([]hookRunnerProgramEvent(nil), l.events...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].Kind != values[j].Kind {
			return values[i].Kind < values[j].Kind
		}
		if values[i].Event != values[j].Event {
			return values[i].Event < values[j].Event
		}
		if values[i].HookType != values[j].HookType {
			return values[i].HookType < values[j].HookType
		}
		return values[i].Matcher < values[j].Matcher
	})
	return values
}

func runHookRunnerProgram(t *testing.T, mode byte, token string) hookRunnerProgramTrace {
	t.Helper()
	ctx := context.Background()
	client := &hookRunnerProgramClient{}
	runner := newRunner(client, "default-"+token)
	commandRuntime := &hookRunnerProgramCommandRuntime{baseEnv: []string{
		"PATH=/deterministic/bin",
		"SAFE=kept",
		"API_KEY=must-not-leak",
		"PRIVATE_TOKEN=must-not-leak",
		"CLAUDE_EFFORT=parent-value",
	}}
	runner.commandHookRuntime = commandRuntime
	eventLog := &hookRunnerProgramEventLog{}
	runner.SetEventCallback(eventLog.add)
	runner.SetSandboxWrapper(nil)
	addHookRunnerProgramHooks(runner, token, mode)

	summary := summarizeHookRunnerProgramCounts(runner.Summary())
	supported := summarizeHookRunnerProgramCounts(runner.SupportedSummary())
	if !containsHookRunnerProgramCount(summary, string(plugin.HookPreToolUse)+"=6") ||
		!containsHookRunnerProgramCount(supported, string(plugin.HookPreToolUse)+"=4") {
		t.Fatalf("hook summaries = all:%v supported:%v", summary, supported)
	}
	diagnostics := runner.Validate()
	if len(diagnostics) != 1 || diagnostics[0].PluginName != "program-plugin" || diagnostics[0].Matcher != "[" ||
		!strings.Contains(diagnostics[0].Message, "program-plugin") || strings.Contains(diagnostics[0].Message, "program:never") {
		t.Fatalf("Validate() = %#v", diagnostics)
	}
	if matches := runner.MatchHooks(plugin.HookPreToolUse, "Bash"); len(matches) != 3 {
		t.Fatalf("MatchHooks(Bash) = %#v, want deny/ask/unsupported handlers", matches)
	}
	if matches := runner.MatchHooks(plugin.HookPreToolUse, "Read"); len(matches) != 1 || matches[0].Type != "prompt" {
		t.Fatalf("MatchHooks(Read) = %#v", matches)
	}
	if matches := runner.MatchHooks(plugin.HookPreToolUse, "Write"); len(matches) != 1 || matches[0].Type != "command" {
		t.Fatalf("MatchHooks(Write) = %#v, want one fake command hook", matches)
	}

	input := Input{
		SessionID:     "session-" + token,
		CWD:           "/fixture/" + token,
		HookEventName: "fixture",
		ToolName:      "shell",
		ToolInput:     map[string]any{"token": token, "mode": strconv.Itoa(int(mode))},
		Message:       "message-" + token,
		ToolResult:    "result-" + token,
		UserPrompt:    "user-" + token,
	}

	pre := runner.RunPreToolUse(ctx, input)
	assertHookRunnerProgramPre(t, pre)
	preRead := runner.RunPreToolUse(ctx, Input{ToolName: "read_file", ToolInput: map[string]any{"token": token}})
	if preRead.Denied || preRead.UpdatedInput["read"] != "ok" {
		t.Fatalf("Read PreToolUse = %#v", preRead)
	}
	commandInput := input
	commandInput.HookEventName = "PreToolUse"
	commandInput.ToolName = "write_file"
	commandInput.Effort = "high"
	commandPre := runner.RunPreToolUse(ctx, commandInput)
	if !commandPre.Denied || commandPre.DenyMessage != "command pre denied" || len(commandPre.ModelContext) != 0 {
		t.Fatalf("command PreToolUse = %#v", commandPre)
	}

	post := runner.RunPostToolUse(ctx, input)
	assertHookRunnerProgramPost(t, post)
	commandPost := runner.RunPostToolUse(ctx, commandInput)
	if !containsHookRunnerProgramString(commandPost.TerminalSequences, "command-post-terminal") ||
		!containsHookRunnerProgramString(commandPost.ModelContext, "command post context") ||
		!containsHookRunnerProgramString(commandPost.UserMessages, "command post") {
		t.Fatalf("command RunPostToolUse = %#v", commandPost)
	}
	stop := runner.RunStop(ctx, input)
	if !stop.Blocked || stop.BlockReason != "stop reason" || !containsHookRunnerProgramString(stop.TerminalSequences, "stop-terminal") || !containsHookRunnerProgramString(stop.UserMessages, "stop json") {
		t.Fatalf("RunStop = %#v", stop)
	}
	subagentStop := runner.RunSubagentStop(ctx, input)
	if subagentStop.Blocked || !containsHookRunnerProgramString(subagentStop.TerminalSequences, "sub-terminal") {
		t.Fatalf("RunSubagentStop = %#v", subagentStop)
	}

	userPrompt := runner.RunUserPromptSubmit(ctx, input)
	if !reflect.DeepEqual(userPrompt.ModelContext, []string{"user context"}) || len(userPrompt.UserMessages) != 0 {
		t.Fatalf("RunUserPromptSubmit = %#v", userPrompt)
	}
	startup := runner.RunSessionStart(ctx, input)
	blankStartup := runner.RunSessionStartFor(ctx, input, "")
	resume := runner.RunSessionStartFor(ctx, input, plugin.SessionStartKindResume)
	clear := runner.RunSessionStartFor(ctx, input, plugin.SessionStartKindClear)
	if !reflect.DeepEqual(startup, blankStartup) || !reflect.DeepEqual(startup.ModelContext, []string{"startup context"}) ||
		!reflect.DeepEqual(resume.UserMessages, []string{"resume json"}) || len(clear.ModelContext) != 0 || len(clear.UserMessages) != 0 {
		t.Fatalf("SessionStart results = startup:%#v blank:%#v resume:%#v clear:%#v", startup, blankStartup, resume, clear)
	}

	runner.RunSessionEnd(ctx, input)
	preCompact := runner.RunPreCompact(ctx, input)
	if !reflect.DeepEqual(preCompact.UserMessages, []string{"compact user"}) {
		t.Fatalf("RunPreCompact = %#v", preCompact)
	}
	notification := runner.RunNotification(ctx, input)
	if !reflect.DeepEqual(notification.UserMessages, []string{"notify json"}) ||
		!containsHookRunnerProgramString(notification.ModelContext, "notify context") ||
		!containsHookRunnerProgramString(notification.ModelContext, "recorded executor fault") {
		t.Fatalf("RunNotification = %#v", notification)
	}

	// NewRunner(nil) is the real public no-provider path. It must skip a prompt
	// hook locally rather than attempting any provider or process operation.
	noClientRunner := NewRunner(nil, "unused")
	noClientRunner.SetSandboxWrapper(nil)
	noClientRunner.Add(plugin.HookNotification, hookRunnerProgramHook("*", "prompt", "program:notification", "", ""))
	noClient := noClientRunner.RunNotification(ctx, input)
	if !reflect.DeepEqual(noClient.UserMessages, []string{"prompt hook skipped: no LLM client"}) {
		t.Fatalf("no-client prompt result = %#v", noClient)
	}
	if empty := noClientRunner.RunPostToolUse(ctx, input); len(empty.ModelContext) != 0 || len(empty.UserMessages) != 0 {
		t.Fatalf("no-hook RunPostToolUse = %#v", empty)
	}
	provider := &hookRunnerProgramProviderAdapter{}
	publicClient := llm.NewClient()
	publicClient.Register(provider)
	publicRunner := NewRunner(publicClient, "public-model")
	publicRunner.Add(plugin.HookNotification, hookRunnerProgramHook("*", "prompt", "program:public-client", "", ""))
	publicResult := publicRunner.RunNotification(ctx, input)
	if provider.callCount() != 1 || !reflect.DeepEqual(publicResult.UserMessages, []string{"public client output"}) {
		t.Fatalf("public scripted client result = %#v calls=%d", publicResult, provider.callCount())
	}

	emptySubstitution := substituteHookVariables("in=$TOOL_INPUT out=$TOOL_RESULT user=$USER_PROMPT msg=$MESSAGE tool=$TOOL_NAME", Input{})
	if emptySubstitution != "in=null out=null user=null msg= tool=" {
		t.Fatalf("zero Input substitution = %q", emptySubstitution)
	}
	for _, event := range []plugin.HookEvent{plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop} {
		if !exitBehavior(event).BlockOnExit2 {
			t.Fatalf("exitBehavior(%q) lost its enforced exit-2 policy", event)
		}
	}
	for _, event := range []plugin.HookEvent{plugin.HookPostToolUse, plugin.HookUserPromptSubmit, plugin.HookPreCompact, plugin.HookNotification, plugin.HookEvent("Future")} {
		if exitBehavior(event).BlockOnExit2 {
			t.Fatalf("exitBehavior(%q) unexpectedly blocks exit 2", event)
		}
	}

	calls := client.snapshotCalls()
	overrideModelCalls := 0
	for _, call := range calls {
		if strings.Contains(call.Prompt, "$TOOL_") || strings.Contains(call.Prompt, "$USER_PROMPT") || strings.Contains(call.Prompt, "$MESSAGE") {
			t.Fatalf("unsubstituted prompt request: %#v", call)
		}
		if strings.Contains(call.Prompt, "program:pre-deny") {
			overrideModelCalls++
			if call.Model != "override-"+token || !strings.Contains(call.Prompt, token) {
				t.Fatalf("override prompt request = %#v", call)
			}
		}
	}
	if overrideModelCalls != 1 {
		t.Fatalf("override-model call count = %d, calls=%#v", overrideModelCalls, calls)
	}
	commandPlans := commandRuntime.snapshotPlans()
	assertHookRunnerProgramCommandPlans(t, commandPlans, commandInput, input, token)
	assertHookRunnerProgramCommandPreparationErrors(t, commandRuntime)
	if merged := mergeHookInputMaps(map[string]any{"kept": "value"}, nil); merged["kept"] != "value" || len(merged) != 1 {
		t.Fatalf("mergeHookInputMaps(empty) = %#v", merged)
	}

	eventTrace := eventLog.snapshot()
	assertHookRunnerProgramEvents(t, eventTrace)
	return hookRunnerProgramTrace{
		Pre:                pre,
		PreRead:            preRead,
		CommandPre:         commandPre,
		Post:               post,
		CommandPost:        commandPost,
		Stop:               stop,
		SubagentStop:       subagentStop,
		UserPrompt:         userPrompt,
		Startup:            startup,
		BlankStartup:       blankStartup,
		Resume:             resume,
		Clear:              clear,
		PreCompact:         preCompact,
		Notification:       notification,
		NoClient:           noClient,
		PublicClient:       publicResult,
		Summary:            summary,
		SupportedSummary:   supported,
		Diagnostics:        []string{diagnostics[0].PluginName + "|" + diagnostics[0].Event + "|" + diagnostics[0].Matcher},
		Calls:              calls,
		CommandPlans:       commandPlans,
		Events:             eventTrace,
		ExitPolicies:       []string{"PreToolUse", "Stop", "SubagentStop"},
		EmptySubstitution:  emptySubstitution,
		OverrideModelCalls: overrideModelCalls,
	}
}

func addHookRunnerProgramHooks(runner *Runner, token string, mode byte) {
	runner.Add(plugin.HookPreToolUse,
		hookRunnerProgramHook("Bash", "prompt", "program:pre-deny tool=$TOOL_NAME input=$TOOL_INPUT result=$TOOL_RESULT user=$USER_PROMPT message=$MESSAGE", "override-"+token, ""),
		hookRunnerProgramHook("Bash", "prompt", "program:pre-ask", "", ""),
		hookRunnerProgramHook("Read", "prompt", "program:pre-read", "", ""),
		hookRunnerProgramHook("[", "prompt", "program:never", "", ""),
		hookRunnerProgramHook("Bash", "http", "program:unsupported", "", ""),
		hookRunnerProgramCommandHook("Write", "fixture-pre", []string{"--pre", "literal"}, "ignored-by-exec-form", 17, "/plugin/"+token),
	)
	runner.Add(plugin.HookPostToolUse,
		hookRunnerProgramHook("Bash|Read", "prompt", "program:post-json", "", ""),
		hookRunnerProgramHook("Bash", "prompt", "program:post-plain", "", ""),
		hookRunnerProgramHook("Bash", "prompt", "program:post-error", "", ""),
		hookRunnerProgramCommandHook("Write", "fixture-post", nil, "bash", 19, "/plugin/"+token),
	)
	runner.Add(plugin.HookStop, hookRunnerProgramHook("*", "prompt", "program:stop-block", "", ""))
	runner.Add(plugin.HookSubagentStop, hookRunnerProgramHook("*", "prompt", "program:subagent-stop", "", ""))
	runner.Add(plugin.HookUserPromptSubmit, hookRunnerProgramHook("", "prompt", "program:user-prompt", "", ""))
	runner.Add(plugin.HookSessionStart,
		hookRunnerProgramHook("startup", "prompt", "program:startup", "", ""),
		hookRunnerProgramHook("resume", "prompt", "program:resume", "", ""),
	)
	runner.Add(plugin.HookSessionEnd, hookRunnerProgramHook("*", "prompt", "program:session-end", "", ""))
	runner.Add(plugin.HookPreCompact, hookRunnerProgramHook("", "prompt", "program:pre-compact", "", ""))
	runner.Add(plugin.HookNotification,
		hookRunnerProgramHook("*", "prompt", "program:notification mode="+string(rune('a'+mode%26)), "", ""),
		hookRunnerProgramCommandHook("*", "fixture-notification-error", []string{"--notify"}, "", 23, "/plugin/"+token),
	)
}

func hookRunnerProgramHook(matcher, handlerType, prompt, model, command string) plugin.RegisteredHook {
	return plugin.RegisteredHook{
		Matcher:    matcher,
		Type:       handlerType,
		Prompt:     prompt,
		Model:      model,
		Command:    command,
		PluginName: "program-plugin",
	}
}

func hookRunnerProgramCommandHook(matcher, command string, args []string, shell string, timeout int, pluginDir string) plugin.RegisteredHook {
	return plugin.RegisteredHook{
		Matcher:    matcher,
		Type:       "command",
		Command:    command,
		Args:       args,
		Shell:      shell,
		Timeout:    timeout,
		PluginDir:  pluginDir,
		PluginName: "program-plugin",
	}
}

func assertHookRunnerProgramPre(t *testing.T, result PreToolUseResult) {
	t.Helper()
	if !result.Denied || result.DenyMessage != "deny reason" || !containsHookRunnerProgramString(result.TerminalSequences, "pre-terminal") {
		t.Fatalf("RunPreToolUse = %#v", result)
	}
	if result.UpdatedInput["source"] != "deny" || result.UpdatedInput["shared"] != "second" || result.UpdatedInput["answer"] != "yes" {
		t.Fatalf("PreToolUse merged input = %#v", result.UpdatedInput)
	}
	for _, want := range []string{"pre context", "ask context"} {
		if !containsHookRunnerProgramString(result.ModelContext, want) {
			t.Fatalf("PreToolUse model context = %#v, want %q", result.ModelContext, want)
		}
	}
	for _, want := range []string{"pre visible", `hook returned permissionDecision "ask" which serf does not support (no interactive permission prompt); the tool will proceed`} {
		if !containsHookRunnerProgramString(result.UserMessages, want) {
			t.Fatalf("PreToolUse user messages = %#v, want %q", result.UserMessages, want)
		}
	}
}

func assertHookRunnerProgramPost(t *testing.T, result RunResult) {
	t.Helper()
	if !containsHookRunnerProgramString(result.TerminalSequences, "post-terminal") || !containsHookRunnerProgramString(result.ModelContext, "post context") ||
		!containsHookRunnerProgramString(result.ModelContext, "prompt hook LLM call: scripted prompt failure") ||
		!containsHookRunnerProgramString(result.UserMessages, "post json") || !containsHookRunnerProgramString(result.UserMessages, "post plain") {
		t.Fatalf("RunPostToolUse = %#v", result)
	}
}

func assertHookRunnerProgramCommandPlans(t *testing.T, plans []hookRunnerProgramCommandPlan, commandInput, notificationInput Input, token string) {
	t.Helper()
	if len(plans) != 3 {
		t.Fatalf("recorded command plans = %#v, want pre/post/error", plans)
	}
	byProgram := make(map[string]hookRunnerProgramCommandPlan, len(plans))
	for _, plan := range plans {
		byProgram[plan.Program] = plan
	}
	pre, ok := byProgram["fixture-pre"]
	if !ok || !reflect.DeepEqual(pre.Args, []string{"--pre", "literal"}) || pre.Timeout != 17*time.Second || !reflect.DeepEqual(pre.Input, commandInput) {
		t.Fatalf("exec-form command plan = %#v", pre)
	}
	post, ok := byProgram["bash"]
	if !ok || !reflect.DeepEqual(post.Args, []string{"-c", "fixture-post"}) || post.Timeout != 19*time.Second || !reflect.DeepEqual(post.Input, commandInput) {
		t.Fatalf("shell-form command plan = %#v", post)
	}
	failure, ok := byProgram["fixture-notification-error"]
	if !ok || !reflect.DeepEqual(failure.Args, []string{"--notify"}) || failure.Timeout != 23*time.Second || !reflect.DeepEqual(failure.Input, notificationInput) {
		t.Fatalf("error command plan = %#v", failure)
	}

	for name, plan := range byProgram {
		env := hookRunnerProgramEnvMap(t, plan.Env)
		wantCWD := notificationInput.CWD
		if name != "fixture-notification-error" {
			wantCWD = commandInput.CWD
		}
		if env["PATH"] != "/deterministic/bin" || env["SAFE"] != "kept" || env["CLAUDE_PLUGIN_ROOT"] != "/plugin/"+token ||
			env["PLUGIN_ROOT"] != "/plugin/"+token || env["CLAUDE_PROJECT_DIR"] != wantCWD {
			t.Fatalf("%s command environment = %#v", name, env)
		}
		if name == "fixture-notification-error" {
			if _, hasEffort := env["CLAUDE_EFFORT"]; hasEffort {
				t.Fatalf("%s command environment inherited unexpected effort: %#v", name, env)
			}
		} else if env["CLAUDE_EFFORT"] != "high" {
			t.Fatalf("%s command environment effort = %#v", name, env)
		}
		for _, secret := range []string{"API_KEY", "PRIVATE_TOKEN"} {
			if _, leaked := env[secret]; leaked {
				t.Fatalf("%s command environment leaked %s: %#v", name, secret, env)
			}
		}
	}
}

func hookRunnerProgramEnvMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(env))
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed recorded environment entry %q", entry)
		}
		if _, duplicate := values[name]; duplicate {
			t.Fatalf("duplicate recorded environment entry %q in %v", name, env)
		}
		values[name] = value
	}
	if values["CLAUDE_EFFORT"] == "parent-value" {
		t.Fatalf("parent CLAUDE_EFFORT leaked into %v", env)
	}
	return values
}

func assertHookRunnerProgramCommandPreparationErrors(t *testing.T, runtime commandHookRuntime) {
	t.Helper()
	for _, shell := range []string{"powershell", "zsh"} {
		_, err := executeCommandHookWithRuntime(context.Background(), plugin.RegisteredHook{
			Type:    "command",
			Command: "never-run",
			Shell:   shell,
		}, Input{}, runtime)
		if err == nil {
			t.Fatalf("command shell %q unexpectedly reached the runtime", shell)
		}
	}
	_, err := prepareCommandHookInvocation(plugin.RegisteredHook{Type: "command", Command: "never-run", Args: []string{"--x"}}, Input{
		ToolInput: map[string]any{"unsupported": func() {}},
	}, []string{"PATH=/deterministic/bin"}, nil)
	if err == nil {
		t.Fatal("unsupported JSON hook input unexpectedly prepared a command invocation")
	}
}

func assertHookRunnerProgramEvents(t *testing.T, values []hookRunnerProgramEvent) {
	t.Helper()
	starts := 0
	ends := 0
	commandStarts := 0
	commandEnds := 0
	sawCommandExit2 := false
	for _, value := range values {
		switch value.Kind {
		case events.EventHookStart:
			starts++
			if value.HookType == "command" {
				commandStarts++
			}
		case events.EventHookEnd:
			ends++
			if value.HookType == "command" {
				commandEnds++
			}
			if value.Event == string(plugin.HookPreToolUse) && value.HookType == "command" && value.ExitCode == 2 {
				sawCommandExit2 = true
			}
			if value.ExitCode != 0 && !(value.Event == string(plugin.HookPreToolUse) && value.HookType == "command" && value.ExitCode == 2) {
				t.Fatalf("unexpected hook end exit %d: %#v", value.ExitCode, value)
			}
		default:
			t.Fatalf("unexpected hook lifecycle event %#v", value)
		}
		if value.Event == "" || value.HookType == "" || value.PluginName != "program-plugin" {
			t.Fatalf("incomplete hook lifecycle event %#v", value)
		}
	}
	if starts == 0 || starts != ends {
		t.Fatalf("unbalanced hook lifecycle events starts=%d ends=%d values=%#v", starts, ends, values)
	}
	if commandStarts != 3 || commandEnds != 3 || !sawCommandExit2 {
		t.Fatalf("command lifecycle events starts=%d ends=%d exit2=%v values=%#v", commandStarts, commandEnds, sawCommandExit2, values)
	}
}

func summarizeHookRunnerProgramCounts(values map[plugin.HookEvent]int) []string {
	counts := make([]string, 0, len(values))
	for event, count := range values {
		counts = append(counts, string(event)+"="+strconv.Itoa(count))
	}
	sort.Strings(counts)
	return counts
}

func containsHookRunnerProgramCount(values []string, want string) bool {
	return containsHookRunnerProgramString(values, want)
}

func containsHookRunnerProgramString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
