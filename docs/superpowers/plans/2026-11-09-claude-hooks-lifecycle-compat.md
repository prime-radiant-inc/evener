# Claude Hook Lifecycle Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fire `SubagentStart`, `PostToolUseFailure`, `PostToolBatch`, `StopFailure`, and `PostCompact` with Claude-compatible contracts, and correct the matcher target, input fields, and `continue: false` handling of the events Evener already fires.

**Architecture:** Each new event gets a runner method in `agent/internal/hooks` and is fired from the one session-lifecycle boundary that already exists for it. Shared plumbing (agent identity, `effort`, `continue: false`, matcher exact set) is built once, first, so every event task is a small addition.

**Tech Stack:** Go 1.x, `encoding/json`, `regexp` (RE2), `testing`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-11-09-claude-hooks-lifecycle-compat-design.md`

## Global Constraints

- Every feature carries exactly one tier: `evener-native`, `claude-compatible-subset`, `reserved-placeholder`, or `experimental` (`docs/subagent-management/10-runtime-contracts.md` §Contract 4).
- No test may assert on human-readable copy. Assert structured effects: which hook ran, what the model or user received, what session state changed (`docs/developing-evener/testing.md`).
- Tests must be deterministic: no provider credentials, network, or model behavior. Hooks are driven with real `bash -c` scripts; the model is a scripted `llm.ProviderAdapter`.
- Hooks never run under `s.mu`.
- `continue: false` is honored everywhere Evener fires EXCEPT `StopFailure`, `Notification`, `PreCompact`, `PostCompact`, `SessionEnd`, where Claude discards it.
- `PreCompact` decision control stays dead (exit 2 non-blocking). It is the one event where the docs must not claim parity.
- Go RE2, not JavaScript regex, stays the documented matcher caveat.
- Additive wire changes only, except the four in the spec's §5.5.

---

## File Structure

New packages/files:

- `agent/internal/hooks/errorname.go` — `llm.Kind`/status → Claude `error` name. Pure function, no session state.
- `agent/internal/hooks/hookinput.go` — `Input`, `Effort`, `BatchToolCall`. Split out of `hooks.go` so the field set has one home.

Existing files touched, by responsibility:

- `agent/plugin/hooks.go` — the event vocabulary and tiers. The only place a name is declared.
- `agent/internal/hooks/hooks.go` — runner dispatch. One method per event.
- `agent/internal/hooks/exitcode.go` — the per-event exit-code table.
- `agent/internal/hooks/matcher.go` — the exact/pipe-list/regex classifier.
- `agent/internal/hooks/command_runtime.go` — the hook process environment.
- `agent/internal/tool/registry.go` — `ExecResult`, and the pre-execution refusals that set the new `Rejected` marker.
- `agent/session_events.go` — `hookInput`, the single builder for common input fields.
- `agent/session_tools.go` — `execTool`, the tool boundary.
- `agent/session_lifecycle.go` — the round loop and session close.
- `agent/session_tool_round.go` — `execToolBatch`, `injectPostToolSteering`.
- `agent/session_model_call.go` — `handleModelError`.
- `agent/session_compaction.go` — `Compact`, `stageCompactionEffects`, `flush`, `foldWithForceCompact`.
- `agent/subagents.go` — `launchSubagentRun`, the subagent funnel.
- `agent/delegate_runtime.go` — the descriptor's agent type.
- `agent/session_config.go` — `spawnConfig`.

---

### Task 1: Event registry, tiers, and the matcher exact set

**Files:**
- Modify: `agent/plugin/hooks.go:15-128`
- Modify: `agent/internal/hooks/matcher.go:29-70`
- Test: `agent/plugin/hooks_test.go`, `agent/internal/hooks/matcher_test.go`

**Interfaces:**
- Produces: `plugin.HookSubagentStart`, `plugin.HookPostToolUseFailure`, `plugin.HookPostToolBatch`, `plugin.HookStopFailure`, `plugin.HookPostCompact` (all `HookEvent`). `validHookEvents` includes them; `recognizedClaudeEvents` gains `"DirectoryAdded"`, `"PreModelSwitch"`, `"PostModelSwitch"`. `matchTarget(matcher, target string) (bool, error)` unchanged signature; `isExactMatcher` widened.

- [ ] **Step 1: Write the failing tests**

`agent/plugin/hooks_test.go`, add:

```go
func TestEventRegistry_NewEventsAreFired(t *testing.T) {
	for _, e := range []HookEvent{
		HookSubagentStart, HookPostToolUseFailure, HookPostToolBatch,
		HookStopFailure, HookPostCompact,
	} {
		if !validHookEvents[e] {
			t.Errorf("%s must be in validHookEvents", e)
		}
		if got := EventTier(e); got != "claude-compatible-subset" {
			t.Errorf("%s tier = %q, want claude-compatible-subset", e, got)
		}
	}
}

func TestEventRegistry_NewlyRecognizedClaudeEventsAreReserved(t *testing.T) {
	for _, e := range []HookEvent{"DirectoryAdded", "PreModelSwitch", "PostModelSwitch"} {
		if !recognizedClaudeEvents[e] {
			t.Errorf("%s must be recognized", e)
		}
		if validHookEvents[e] {
			t.Errorf("%s must not be fired yet", e)
		}
		if got := EventTier(e); got != "reserved-placeholder" {
			t.Errorf("%s tier = %q, want reserved-placeholder", e, got)
		}
	}
}

func TestParsePluginHooks_DirectoryAddedIsReservedNotUnknown(t *testing.T) {
	data := []byte(`{"hooks":{"DirectoryAdded":[{"hooks":[{"type":"command","command":"true"}]}]}}`)
	hooks, unsupported, unknown, err := parsePluginHooksDiag(data, "/p", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 0 {
		t.Errorf("hooks = %v, want none", hooks)
	}
	if !unsupported["DirectoryAdded"] {
		t.Errorf("unsupported = %v, want DirectoryAdded", unsupported)
	}
	if unknown["DirectoryAdded"] {
		t.Errorf("unknown = %v, want none", unknown)
	}
}
```

`agent/internal/hooks/matcher_test.go`, add:

```go
func TestMatchTarget_CommaAndSpaceSeparator(t *testing.T) {
	cases := []struct {
		matcher, target string
		want            bool
	}{
		{"Edit, Write", "Write", true},
		{"Edit,Write", "Edit", true},
		{"Edit, Write", "Read", false},
		{" Edit , Write ", "Write", true},
	}
	for _, c := range cases {
		got, err := matchTarget(c.matcher, c.target)
		if err != nil {
			t.Fatalf("matchTarget(%q,%q): %v", c.matcher, c.target, err)
		}
		if got != c.want {
			t.Errorf("matchTarget(%q,%q) = %v, want %v", c.matcher, c.target, got, c.want)
		}
	}
}

func TestMatchTarget_HyphenatedNameIsExact(t *testing.T) {
	if got, err := matchTarget("code-reviewer", "code-reviewer"); err != nil || !got {
		t.Errorf("code-reviewer must match itself, got %v err %v", got, err)
	}
	if got, _ := matchTarget("code-reviewer", "code-reviewers"); got {
		t.Errorf("code-reviewer must not match code-reviewers")
	}
}
```

- [ ] **Step 2: Run the tests, expect failure**

`go test ./agent/plugin/ -run 'TestEventRegistry|TestParsePluginHooks_DirectoryAdded' -v` → FAIL: undefined constants.
`go test ./agent/internal/hooks/ -run TestMatchTarget_ -v` → FAIL: `Edit, Write` does not match.

- [ ] **Step 3: Add the constants, registry entries, and the widened classifier**

In `agent/plugin/hooks.go`, add the five constants beside the existing ones with the same doc-comment style, add them to `validHookEvents`, and add the three names to `recognizedClaudeEvents` under `reserved-placeholder`. `hookEventTier` needs no change: it derives from the two maps.

In `agent/internal/hooks/matcher.go`, widen `isExactMatcher` to accept ASCII letters, digits, `_`, `-`, `,`, and space, and make the split consume both `|` and `,` with each alternative trimmed:

```go
func splitExactMatcher(m string) []string {
	return strings.FieldsFunc(m, func(r rune) bool { return r == '|' || r == ',' })
}
```

Use `splitExactMatcher` in `matchTarget`'s exact branch, trimming each alternative first (so `" Edit , Write "` yields `Edit`, `Write`).

- [ ] **Step 4: Run the tests, expect pass**

`go test ./agent/plugin/ ./agent/internal/hooks/ -v` → PASS, and the existing matcher tests still pass.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin/hooks.go agent/plugin/hooks_test.go agent/internal/hooks/matcher.go agent/internal/hooks/matcher_test.go
git commit -m "feat(hooks): register five lifecycle events and widen the matcher exact set"
```

---

### Task 2: Shared input, effort, agent identity, and `continue: false`

**Files:**
- Create: `agent/internal/hooks/hookinput.go`
- Modify: `agent/internal/hooks/hooks.go`, `agent/internal/hooks/command_runtime.go`
- Modify: `agent/session_events.go:604-622`, `agent/session_config.go:804`, `agent/subagents.go:848-995`
- Modify: `agent/delegate_runtime.go:2350`
- Test: `agent/internal/hooks/hooks_test.go`, `agent/session_hooks_input_test.go` (new)

**Interfaces:**
- Produces: `hooks.Input` gains `Trigger string`, `CustomInstructions *string`, `CompactSummary string`, `StopHookActive *bool`, `LastAssistantMessage string`, `Error string`, `ErrorDetails string`, `IsInterrupt *bool`, `DurationMS *int64`, `ToolCalls []BatchToolCall`, `NotificationType string`, `Title string`. `Input.Effort` becomes `*Effort` where `type Effort struct { Level string \`json:"level"\` }`.
- Produces: `RunResult.Stopped bool`, `RunResult.StopReason string`; same two fields on `PreToolUseResult` and `StopResult`. `hooks.HonorsContinue(event plugin.HookEvent) bool`.
- Produces: `subagentHookAgentType(requested, agentName string) string` in package `agent`; `SessionConfig.spawn.agentType`.

- [ ] **Step 1: Write the failing tests**

`agent/internal/hooks/hooks_test.go`, add:

```go
func TestInput_EffortSerializesAsLevelObject(t *testing.T) {
	b, err := json.Marshal(Input{Effort: &Effort{Level: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	eff, ok := m["effort"].(map[string]any)
	if !ok || eff["level"] != "high" {
		t.Fatalf("effort = %v, want {\"level\":\"high\"}", m["effort"])
	}
}

func TestInput_EffortAbsentWhenUnset(t *testing.T) {
	b, _ := json.Marshal(Input{})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, present := m["effort"]; present {
		t.Fatalf("effort must be absent, got %v", m["effort"])
	}
}

func TestRunNotification_ContinueFalseIsDiscarded(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookNotification, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"no more"}'`})
	out := r.RunNotification(context.Background(), Input{HookEventName: "Notification"})
	if out.Stopped {
		t.Fatalf("Notification must discard continue:false")
	}
}

func TestRunPostToolUse_ContinueFalseStops(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolUse, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"continue":false,"stopReason":"stop here"}'`})
	out := r.RunPostToolUse(context.Background(), Input{HookEventName: "PostToolUse"})
	if !out.Stopped {
		t.Fatalf("PostToolUse must honor continue:false")
	}
	if out.StopReason == "" {
		t.Fatalf("StopReason must be carried")
	}
}
```

`agent/session_hooks_input_test.go` (new):

```go
func TestHookInput_SubagentIdentity(t *testing.T) {
	parent := newSession(t)
	child, err := parent.prepareSubagentRun(context.Background(), "task", "", parent.currentEnv().WorkingDirectory(), 0, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.disposeUnadopted()
	in := child.sub.sess.hookInput(plugin.HookStop)
	if in.AgentID != child.sub.sess.ID() {
		t.Errorf("AgentID = %q, want %q", in.AgentID, child.sub.sess.ID())
	}
	if in.AgentType != "subagent" {
		t.Errorf("AgentType = %q, want subagent", in.AgentType)
	}
	if root := parent.hookInput(plugin.HookStop); root.AgentID != "" || root.AgentType != "" {
		t.Errorf("root identity must be empty, got %q/%q", root.AgentID, root.AgentType)
	}
}

func TestSubagentHookAgentType(t *testing.T) {
	cases := []struct{ requested, name, want string }{
		{"toolsmith:toolsmith", "toolsmith", "toolsmith:toolsmith"},
		{"", "subagent", "subagent"},
		{"default", "subagent", "subagent"},
	}
	for _, c := range cases {
		if got := subagentHookAgentType(c.requested, c.name); got != c.want {
			t.Errorf("subagentHookAgentType(%q,%q) = %q, want %q", c.requested, c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests, expect failure**

`go test ./agent/internal/hooks/ -run 'TestInput_Effort|TestRunNotification_Continue|TestRunPostToolUse_Continue' -v` → FAIL: `Effort` is a string; `Stopped` undefined.
`go test ./agent/ -run 'TestHookInput_SubagentIdentity|TestSubagentHookAgentType' -v` → FAIL: undefined.

- [ ] **Step 3: Implement**

Move `Input` into `agent/internal/hooks/hookinput.go` with the new fields; keep the legacy aliases (`ToolResult`, `UserPrompt`) exactly as they are. Add:

```go
// Effort is the Claude-documented effort object: the level under "level".
type Effort struct {
	Level string `json:"level"`
}

// BatchToolCall is one entry of PostToolBatch's tool_calls array.
type BatchToolCall struct {
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input,omitempty"`
	ToolUseID    string         `json:"tool_use_id"`
	ToolResponse string         `json:"tool_response"`
}

// continueDiscardedEvents are the events where Claude discards continue/stopReason.
var continueDiscardedEvents = map[plugin.HookEvent]bool{
	plugin.HookStopFailure: true, plugin.HookNotification: true,
	plugin.HookPreCompact: true, plugin.HookPostCompact: true,
	plugin.HookSessionEnd: true,
}

// HonorsContinue reports whether continue:false stops the action on this event.
func HonorsContinue(e plugin.HookEvent) bool { return !continueDiscardedEvents[e] }
```

In `agent/internal/hooks/hooks.go`, add `Stopped`/`StopReason` to the three result types and a helper `applyStopped(event, o, stopped, reason *bool, stopReason *string)` called from `collectSystemMessages` and `runStopEvent` before routing. `RunPreToolUse` calls it too.

In `agent/internal/hooks/command_runtime.go`, replace the `input.Effort != ""` check with a nil-safe read of `input.Effort.Level`.

In `agent/session_config.go`, add `agentType string` to `spawnConfig`. In `agent/subagents.go`, add:

```go
// subagentHookAgentType is the agent type a SubagentStart/SubagentStop matcher
// targets and the value the same hooks receive as agent_type input. A plugin
// agent is named by its catalog key (plugin:agent); an unnamed delegate's
// descriptor carries the sentinel "default" and must report its resolved name.
func subagentHookAgentType(requested, agentName string) string {
	if r := strings.TrimSpace(requested); r != "" && r != "default" {
		return r
	}
	return agentName
}
```

Set `subCfg.spawn.agentType = subagentHookAgentType(agentType, agentName)` immediately after `agentName` is resolved (`agent/subagents.go:987`), and on the frozen branch from `frozen.AgentType`.

In `agent/session_events.go`'s `hookInput`, when `s.cfg.spawn.parentSessionID != ""`, set `AgentID = s.id` and `AgentType = s.cfg.spawn.agentType`. Replace the `Effort` assignment with `&hooks.Effort{Level: effort}` when non-empty.

- [ ] **Step 4: Run the tests, expect pass**

`go test ./agent/internal/hooks/ ./agent/ -run 'Hook|Effort|Continue|Session' -count=1` → PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/hookinput.go agent/internal/hooks/hooks.go agent/internal/hooks/command_runtime.go agent/internal/hooks/hooks_test.go agent/session_config.go agent/subagents.go agent/delegate_runtime.go agent/session_events.go agent/session_hooks_input_test.go
git commit -m "feat(hooks): shared input, effort object, agent identity, and continue:false"
```

---

### Task 3: `SubagentStart`

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (runner), `agent/internal/hooks/hooks.go` (`runAll` variant)
- Modify: `agent/subagents.go:1713` (`launchSubagentRun`)
- Test: `agent/internal/hooks/hooks_test.go`, `agent/subagent_start_hook_test.go` (new)

**Interfaces:**
- Consumes: `hooks.HonorsContinue`, `Session.hookInput`'s agent identity (Task 2).
- Produces: `func (r *Runner) RunSubagentStartFor(ctx context.Context, input Input, agentType string) RunResult`.

- [ ] **Step 1: Write the failing tests**

`agent/internal/hooks/hooks_test.go`:

```go
func TestRunSubagentStartFor_MatchesAgentType(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Matcher: "^toolsmith:toolsmith$", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"injected"}}'`})
	got := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "toolsmith:toolsmith")
	if len(got.ModelContext) != 1 || got.ModelContext[0] != "injected" {
		t.Fatalf("ModelContext = %v, want the injected context", got.ModelContext)
	}
	miss := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "other")
	if len(miss.ModelContext) != 0 {
		t.Fatalf("another agent type must not match, got %v", miss.ModelContext)
	}
}

func TestRunSubagentStartFor_Exit2IsUserOnly(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookSubagentStart, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo "spawn warning" >&2; exit 2`})
	got := r.RunSubagentStartFor(context.Background(), Input{HookEventName: "SubagentStart"}, "subagent")
	if len(got.ModelContext) != 0 {
		t.Fatalf("exit 2 must not reach the model, got %v", got.ModelContext)
	}
	if len(got.UserMessages) != 1 {
		t.Fatalf("exit 2 stderr must reach the user, got %v", got.UserMessages)
	}
}
```

`agent/subagent_start_hook_test.go` (new), driving the real `delegate` tool:

```go
// TestSubagentStart_FiresOnceForADelegateToolSpawn proves the hook fires for a
// delegate the model created through the delegate tool, not only the legacy
// spawnAgent path, and that its context reaches the child's first request.
func TestSubagentStart_FiresOnceForADelegateToolSpawn(t *testing.T) {
	// Build a root session whose plugin declares a SubagentStart command hook
	// that prints a sentinel additionalContext, then run the delegate tool.
	// Assert: one hook ran, and the child's first llm.Request contains the sentinel.
}

// TestSubagentStart_SecondLaunchDoesNotDuplicateContext proves the delivery
// dedupe: a child that already holds the hook's steering turn receives no copy.
func TestSubagentStart_SecondLaunchDoesNotDuplicateContext(t *testing.T) {}
```

Fill both bodies using the existing `newSession` helper plus a scripted provider; assert on the recorded `llm.Request` messages, not on text produced by the hook beyond a sentinel.

- [ ] **Step 2: Run, expect failure**

`go test ./agent/internal/hooks/ -run TestRunSubagentStartFor -v` → FAIL undefined.
`go test ./agent/ -run TestSubagentStart_ -v` → FAIL: only one of the two spawns fires, or none.

- [ ] **Step 3: Implement**

Add `RunSubagentStartFor`, which calls `r.runAll(ctx, plugin.HookSubagentStart, agentType, input)` and `collectSystemMessages`.

In `agent/subagents.go`, add:

```go
// runSubagentStartHook fires the parent's SubagentStart hooks for one child run
// launch. additionalContext reaches the child as steering; a copy the child
// already holds is not delivered twice.
func (s *Session) runSubagentStartHook(ctx context.Context, sub *subagent, agentType string) {
	if s.hookRunner == nil || sub == nil || sub.sess == nil {
		return
	}
	in := s.hookInput(plugin.HookSubagentStart)
	in.AgentID = sub.sess.ID()
	in.AgentType = agentType
	res := s.hookRunner.RunSubagentStartFor(s.apiLogContext(ctx), in, agentType)
	for _, m := range res.ModelContext {
		if sub.sess.hookContextAlreadyPresent(m) {
			continue
		}
		if err := sub.sess.deliverHookContext(m); err != nil {
			s.emitDiagnosticWarning(events.WarningData{Message: err.Error()})
		}
	}
	for _, m := range res.UserMessages {
		s.deliverHookUserMessage(m)
	}
}
```

Add `hookContextAlreadyPresent(text string) bool`, which scans `s.history` under `s.mu` for a `TurnSteering` with `SteeringKind == events.SteeringKindHookContext` and equal wrapped text.

Call `s.runSubagentStartHook(runCtx, sub, sub.agentTypeName())` as the first statement inside the `go func()` in `launchSubagentRun` (`agent/subagents.go:1713`), before `sub.run`. Use the spawn-config value, not `sub.agentType`, so a default subagent reports `subagent`.

- [ ] **Step 4: Run, expect pass**

`go test ./agent/internal/hooks/ -run TestRunSubagentStartFor -v` → PASS.
`go test ./agent/ -run 'TestSubagentStart_|TestSubagentStop' -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go agent/subagents.go agent/subagent_start_hook_test.go
git commit -m "feat(hooks): fire SubagentStart for every delegate run launch"
```

---

### Task 4: `PostToolUseFailure`, and the `PostToolUse` success gate

**Files:**
- Modify: `agent/internal/tool/registry.go:297-316`, `:741-800`
- Modify: `agent/internal/hooks/hooks.go`, `agent/internal/hooks/exitcode.go`
- Modify: `agent/session_tools.go:1010-1038`
- Test: `agent/internal/tool/registry_test.go`, `agent/internal/hooks/hooks_test.go`, `agent/post_tool_use_failure_test.go` (new)

**Interfaces:**
- Produces: `tool.ExecResult.Rejected bool`. `func (r *Runner) RunPostToolUseFailure(ctx context.Context, input Input) RunResult`.
- `exitBehavior` gains no blocking entry for this event.

- [ ] **Step 1: Write the failing tests**

`agent/internal/tool/registry_test.go`:

```go
func TestRegistry_MarksPreExecutionRejections(t *testing.T) {
	reg := tool.NewRegistry()
	cases := []struct{ name, args, wantRejected string }{
		{"unknown_tool", `{}`, "unknown"},
		{"read_file", `{"path":`, "invalid JSON"},
	}
	for _, c := range cases {
		res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{Name: c.name, ID: "id", Arguments: json.RawMessage(c.args)})
		if !res.IsError {
			t.Fatalf("%s: expected IsError", c.name)
		}
		if !res.Rejected {
			t.Errorf("%s: Rejected must be true for a pre-execution refusal", c.name)
		}
	}
}
```

`agent/post_tool_use_failure_test.go` (new), real session:

```go
func TestPostToolUseFailure_FiresForExecutedFailureOnly(t *testing.T) {
	// Session with a hook on PostToolUseFailure and one on PostToolUse, each
	// appending a sentinel to a file. Run three calls through execTool:
	//   (a) `bash -c 'exit 3'`        -> PostToolUseFailure only
	//   (b) `bash -c 'echo ok'`       -> PostToolUse only
	//   (c) an unknown tool name      -> neither
	// Assert the file's lines are exactly the two expected sentinels.
}
```

Assert via the sentinel file, and assert `input.Error` carries the model-facing text by having the hook write `$INPUT` to a second file and decoding it.

- [ ] **Step 2: Run, expect failure**

`go test ./agent/internal/tool/ -run TestRegistry_MarksPreExecutionRejections -v` → FAIL: `Rejected` undefined.
`go test ./agent/ -run TestPostToolUseFailure_ -v` → FAIL: three sentinels, not two.

- [ ] **Step 3: Implement**

Add `Rejected bool` to `ExecResult` with a doc comment naming exactly which paths set it: the breaker park, unknown tool, `ValidateRawArguments`, invalid arguments JSON, `PreValidate`, schema validation, and middleware refusal. Set it at each of those returns in `executeCall`.

Add `RunPostToolUseFailure` → `collectSystemMessages(plugin.HookPostToolUseFailure, r.runAll(...))`. Add `plugin.HookPostToolUseFailure` to `exitBehavior`'s default (non-blocking) arm and note it in the comment.

In `agent/session_tools.go`, gate `PostToolUse` on `!res.IsError` and add:

```go
if s.hookRunner != nil && res.IsError && !res.PrevalOnly && !res.Rejected {
	hi := s.hookInput(plugin.HookPostToolUseFailure)
	hi.ToolName = toolname.EvenerToClaude(call.Name)
	hi.ToolUseID = call.ID
	hi.Error = res.Output
	hi.DurationMS = &res.DurationMS
	if !prep.RawArgumentsRejected && len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &hi.ToolInput)
	}
	// same ModelContext / UserMessages delivery loop as PostToolUse
}
```

- [ ] **Step 4: Run, expect pass**

`go test ./agent/internal/tool/ ./agent/internal/hooks/ -count=1` → PASS.
`go test ./agent/ -run 'TestPostToolUseFailure_|Hook|ExecTool' -count=1` → PASS.
Existing tests that rely on `PostToolUse` firing for a failure must move to
`PostToolUseFailure`. Check `agent/cov_intg_hooks_test.go` and
`agent/plugin_integration_test.go` first, and name every test you change in the
commit message.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/registry.go agent/internal/tool/registry_test.go agent/internal/hooks/hooks.go agent/internal/hooks/exitcode.go agent/internal/hooks/hooks_test.go agent/session_tools.go agent/post_tool_use_failure_test.go
git commit -m "feat(hooks): fire PostToolUseFailure and gate PostToolUse on success"
```

---

### Task 5: `PostToolBatch`

**Files:**
- Modify: `agent/internal/hooks/hooks.go`, `agent/internal/hooks/exitcode.go`
- Modify: `agent/session_lifecycle.go:2490-2545`
- Test: `agent/internal/hooks/hooks_test.go`, `agent/post_tool_batch_hook_test.go` (new)

**Interfaces:**
- Produces: `func (r *Runner) RunPostToolBatch(ctx context.Context, input Input) RunResult`. It ignores `Matcher` entirely.
- `exitBehavior(plugin.HookPostToolBatch).BlockOnExit2 == true`.

- [ ] **Step 1: Write the failing tests**

`agent/internal/hooks/hooks_test.go`:

```go
func TestRunPostToolBatch_IgnoresConfiguredMatcher(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Matcher: "Bash", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"PostToolBatch","additionalContext":"batch-note"}}'`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if len(got.ModelContext) != 1 {
		t.Fatalf("a matcher must not skip PostToolBatch; ModelContext = %v", got.ModelContext)
	}
}

func TestRunPostToolBatch_ToolCallsMarshal(t *testing.T) {
	// Assert the marshaled input carries tool_calls[i] with tool_name,
	// tool_use_id and tool_response, and that exit 2 marks the batch blocked.
}

func TestRunPostToolBatch_Exit2Blocks(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostToolBatch, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo "stop the loop" >&2; exit 2`})
	got := r.RunPostToolBatch(context.Background(), Input{HookEventName: "PostToolBatch"})
	if !got.Stopped {
		t.Fatalf("exit 2 must stop the loop, got %+v", got)
	}
}
```

`agent/post_tool_batch_hook_test.go` (new):

```go
// TestPostToolBatch_ContextReachesTheSameNextRequest proves the hook's
// additionalContext is in the model request that follows the batch.
func TestPostToolBatch_ContextReachesTheSameNextRequest(t *testing.T) {}

// TestPostToolBatch_BlockDoesNotSwallowCommunicateReply proves a block on a
// round that also called communicate returns the reply, not a failure boundary.
func TestPostToolBatch_BlockDoesNotSwallowCommunicateReply(t *testing.T) {}

// TestPostToolBatch_BlockDoesNotStrandPendingAsk proves a round posting ask_user
// stays awaiting when the batch hook blocks.
func TestPostToolBatch_BlockDoesNotStrandPendingAsk(t *testing.T) {}
```

- [ ] **Step 2: Run, expect failure**

`go test ./agent/internal/hooks/ -run TestRunPostToolBatch -v` → FAIL undefined.
`go test ./agent/ -run TestPostToolBatch_ -v` → FAIL.

- [ ] **Step 3: Implement**

Add `RunPostToolBatch`, which builds the matched set directly rather than through `MatchHooks`:

```go
// RunPostToolBatch dispatches PostToolBatch hooks. Claude documents no matcher
// for this event, so a configured matcher never narrows it.
func (r *Runner) RunPostToolBatch(ctx context.Context, input Input) RunResult {
	outputs := r.runMatched(ctx, plugin.HookPostToolBatch, r.hooks[plugin.HookPostToolBatch], input)
	return collectSystemMessages(plugin.HookPostToolBatch, outputs)
}
```

Refactor `runAll` into `runAll` (matches, then calls `runMatched`) plus the new `runMatched(ctx, event, matched, input)` so both share the execution and event callbacks.

Add the blocking row to `exitBehavior` and extend
`TestExitBehavior_BlockEntriesAreEnforced` (`agent/internal/hooks/exitcode_test.go:37`)
with `PostToolBatch`. `TestExitBehavior_PerEvent` (`:17`) enumerates every fired
event and fails until the new one is listed.

In `agent/session_lifecycle.go`, inside the round loop, immediately after `persistToolResults` and BEFORE `injectPostToolSteering`:

```go
batchStop := ""
if s.hookRunner != nil {
	hi := s.hookInput(plugin.HookPostToolBatch)
	hi.ToolCalls = batchToolCalls(calls, results)
	batchRes := s.hookRunner.RunPostToolBatch(s.apiLogContext(ctx), hi)
	for _, m := range batchRes.ModelContext {
		if err := s.deliverHookContext(m); err != nil {
			return "", progressed, err
		}
	}
	for _, m := range batchRes.UserMessages {
		s.deliverHookUserMessage(m)
	}
	if batchRes.Stopped {
		batchStop = batchRes.StopReason
	}
}
```

Then, after `done, text, deliverErr := s.deliverIfCommunicated(ctx, askedThisRound)` returns with `done == false` and only then:

```go
if batchStop != "" {
	s.deliverHookUserMessage(batchStop)
	s.appendSteeringTurn(batchStop, events.SteeringKindHookContext)
	s.finishProcessingAtFailureBoundary(ctx)
	return "", progressed, nil
}
```

- [ ] **Step 4: Run, expect pass**

`go test ./agent/internal/hooks/ -count=1` → PASS.
`go test ./agent/ -run 'TestPostToolBatch_|TestDeliverIfCommunicated' -count=1` → PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/exitcode.go agent/internal/hooks/exitcode_test.go agent/internal/hooks/hooks_test.go agent/session_lifecycle.go agent/post_tool_batch_hook_test.go
git commit -m "feat(hooks): fire PostToolBatch before the pre-request steering drain"
```

---

### Task 6: `StopFailure`

**Files:**
- Create: `agent/internal/hooks/errorname.go`
- Modify: `agent/internal/hooks/hooks.go`, `agent/internal/hooks/exitcode.go`
- Modify: `agent/session_model_call.go:898-916`
- Test: `agent/internal/hooks/errorname_test.go` (new), `agent/stop_failure_hook_test.go` (new)

**Interfaces:**
- Produces: `func (r *Runner) RunStopFailureFor(ctx context.Context, input Input, errorName string) RunResult`, discarded output.
- Produces: `func ClaudeErrorName(err error) string` in `agent/internal/hooks` (takes `error` so callers need no `llm` import beyond the value they already hold).

- [ ] **Step 1: Write the failing tests**

`agent/internal/hooks/errorname_test.go`:

```go
func TestClaudeErrorName(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"rate limit", llm.ErrorFromHTTPStatus("p", 429, "slow", nil, nil), "rate_limit"},
		{"overloaded 529", llm.ErrorFromHTTPStatus("p", 529, "slow", nil, nil), "overloaded"},
		{"auth", llm.ErrorFromHTTPStatus("p", 401, "bad key", nil, nil), "authentication_failed"},
		{"quota", llm.ErrorFromHTTPStatus("p", 403, "quota exceeded", nil, nil), "billing_error"},
		{"not found", llm.ErrorFromHTTPStatus("p", 404, "no model", nil, nil), "model_not_found"},
		{"invalid", llm.ErrorFromHTTPStatus("p", 400, "bad", nil, nil), "invalid_request"},
		{"server", llm.ErrorFromHTTPStatus("p", 500, "boom", nil, nil), "server_error"},
		{"unknown", errors.New("weird"), "unknown"},
	}
	for _, c := range cases {
		if got := ClaudeErrorName(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
```

`ErrorFromHTTPStatus` is the only exported constructor (`llm/errors.go:415`). `Kind` re-derives the category from the concrete error type, so the quota row must use a message the classifier reads as quota/billing. Verify that row against `llm/errors.go`'s classifier; if the classifier does not produce `KindQuotaExceeded` for it, change the fixture, not the mapping.

`agent/stop_failure_hook_test.go` (new):

```go
// TestStopFailure_FiresOnceAndNotStop proves a provider 429 ends the turn with
// error=rate_limit, fires StopFailure exactly once, fires no Stop, and keeps
// the salvaged draft.
func TestStopFailure_FiresOnceAndNotStop(t *testing.T) {}
```

- [ ] **Step 2: Run, expect failure**

`go test ./agent/internal/hooks/ -run TestClaudeErrorName -v` → FAIL undefined.
`go test ./agent/ -run TestStopFailure_ -v` → FAIL.

- [ ] **Step 3: Implement**

`agent/internal/hooks/errorname.go`:

```go
// ClaudeErrorName maps an evener provider failure onto the Claude StopFailure
// error vocabulary. Anything not provable maps to "unknown": a plausible-looking
// wrong name is worse than an honest one, because a hook matcher keys on it.
func ClaudeErrorName(err error) string {
	if err == nil {
		return "unknown"
	}
	if e, ok := err.(llm.Error); ok {
		if e.StatusCode() == 529 || e.ErrorCode() == "overloaded_error" {
			return "overloaded"
		}
	}
	switch llm.Kind(err) {
	case llm.KindRateLimit:
		return "rate_limit"
	case llm.KindAuthentication:
		return "authentication_failed"
	case llm.KindQuotaExceeded:
		return "billing_error"
	case llm.KindNotFound:
		return "model_not_found"
	case llm.KindInvalidRequest:
		return "invalid_request"
	case llm.KindServer:
		return "server_error"
	case llm.KindAccessDenied:
		if e, ok := err.(llm.Error); ok && e.ErrorCode() == "oauth_org_not_allowed" {
			return "oauth_org_not_allowed"
		}
		return "unknown"
	default:
		return "unknown"
	}
}
```

Add `RunStopFailureFor`, which runs the hooks, drops all output and the exit code, and only reports `Stopped` for nothing (never stops). Add `HookStopFailure` to `exitBehavior`'s non-blocking arm.

In `handleModelError`, after `emitTurnFailure`:

```go
if s.hookRunner != nil {
	hi := s.hookInput(plugin.HookStopFailure)
	hi.Error = hooks.ClaudeErrorName(err)
	hi.ErrorDetails = hookErrorDetails(err)
	hi.LastAssistantMessage = errData.Error
	_ = s.hookRunner.RunStopFailureFor(s.apiLogContext(ctx), hi, hi.Error)
}
```

- [ ] **Step 4: Run, expect pass**

`go test ./agent/internal/hooks/ -run 'TestClaudeErrorName|TestRunStopFailure' -v` → PASS.
`go test ./agent/ -run 'TestStopFailure_|TestHandleModelError|TestClassifier' -count=1` → PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/errorname.go agent/internal/hooks/errorname_test.go agent/internal/hooks/hooks.go agent/internal/hooks/exitcode.go agent/session_model_call.go agent/stop_failure_hook_test.go
git commit -m "feat(hooks): fire StopFailure when a turn ends on a provider error"
```

---

### Task 7: `PostCompact`, and `PreCompact`'s trigger and instructions

**Files:**
- Modify: `agent/internal/hooks/hooks.go`
- Modify: `agent/session_compaction.go:379-420` (`foldWithForceCompact`), `:575` (`stageCompactionEffects`), `:725-761` (`flush`), `:21-70` (`Compact`)
- Modify: `agent/session_self_compact.go:242-280`, `agent/session_model_call.go:1578-1595`
- Test: `agent/internal/hooks/hooks_test.go`, `agent/compaction_hooks_test.go` (new)

**Interfaces:**
- Produces: `func (r *Runner) RunPreCompactFor(ctx context.Context, input Input, trigger string) RunResult` (replacing the target-less call site) and `func (r *Runner) RunPostCompactFor(ctx context.Context, input Input, trigger string) RunResult`.
- Produces: `type compactionTrigger string` with constants `triggerManual`, `triggerAuto`, threaded as a parameter of `foldWithForceCompact` and `stageCompactionEffects`.

- [ ] **Step 1: Write the failing tests**

`agent/internal/hooks/hooks_test.go`:

```go
func TestRunPreCompactFor_MatchesTrigger(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreCompact, plugin.RegisteredHook{Matcher: "manual", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"hookEventName":"PreCompact","additionalContext":"manual-note"}}'`})
	if got := r.RunPreCompactFor(context.Background(), Input{}, "manual"); len(got.ModelContext) != 1 {
		t.Fatalf("manual must match, got %v", got.ModelContext)
	}
	if got := r.RunPreCompactFor(context.Background(), Input{}, "auto"); len(got.ModelContext) != 0 {
		t.Fatalf("auto must not match a manual hook, got %v", got.ModelContext)
	}
}

func TestRunPostCompactFor_DiscardsOutputAndContinue(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPostCompact, plugin.RegisteredHook{Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"x","continue":false,"hookSpecificOutput":{"hookEventName":"PostCompact","additionalContext":"y"}}'`})
	got := r.RunPostCompactFor(context.Background(), Input{}, "auto")
	if len(got.ModelContext) != 0 || len(got.UserMessages) != 0 || got.Stopped {
		t.Fatalf("PostCompact output must be discarded, got %+v", got)
	}
}
```

`agent/compaction_hooks_test.go` (new):

```go
// TestPostCompact_FiresOncePerRealCompaction proves a request-time fold that
// compacted nothing fires no PostCompact, while /compact fires manual and a
// pressure fold fires auto.
func TestPostCompact_FiresOncePerRealCompaction(t *testing.T) {}

// TestPreCompact_CarriesTriggerAndInstructions proves the input's trigger and
// custom_instructions match the fold: manual + the instructions for /compact,
// auto + absent for a pressure fold.
func TestPreCompact_CarriesTriggerAndInstructions(t *testing.T) {}
```

- [ ] **Step 2: Run, expect failure**

`go test ./agent/internal/hooks/ -run 'TestRunPreCompactFor|TestRunPostCompactFor' -v` → FAIL undefined.
`go test ./agent/ -run 'TestPostCompact_|TestPreCompact_Carries' -v` → FAIL.

- [ ] **Step 3: Implement**

Thread the trigger:

```go
// compactionTrigger is the PreCompact/PostCompact matcher value (Claude's
// vocabulary): "manual" means the user ran /compact; everything else is "auto".
type compactionTrigger string

const (
	triggerManual compactionTrigger = "manual"
	triggerAuto   compactionTrigger = "auto"
)
```

`foldWithForceCompact(ctx, instructions, captured, trigger)`; `stageCompactionEffects(ctx, history, trigger, instructions)`. Every existing caller passes `triggerAuto`; `Compact` passes `triggerManual`.

In `stageCompactionEffects`, store the trigger and instructions on `foldCommit` so `flush` can use them, and build the `PreCompact` input with `Trigger` and `CustomInstructions`. In `flush`, after the existing supersession check and the compaction-event loop:

```go
if hook := s.cfg.testOnly.afterFoldSideEffectsFlush; hook != nil { hook() }
if s.hookRunner != nil && commit.actualCompaction {
	hi := s.hookInput(plugin.HookPostCompact)
	hi.Trigger = string(commit.trigger)
	hi.CompactSummary = commit.compactSummary
	_ = s.hookRunner.RunPostCompactFor(s.apiLogContext(context.Background()), hi, hi.Trigger)
}
```

`commit.compactSummary` is filled in `commitTranscriptsLocked` from the last `TurnSummary` (else `TurnCheckpoint`) in `pendingCompactionTurns`.

- [ ] **Step 4: Run, expect pass**

`go test ./agent/internal/hooks/ -count=1` → PASS.
`go test ./agent/ -run 'Compact|Compaction|PreCompact|PostCompact' -count=1` → PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go agent/session_compaction.go agent/session_self_compact.go agent/session_model_call.go agent/compaction_hooks_test.go
git commit -m "feat(hooks): fire PostCompact and wire the PreCompact trigger"
```

---

### Task 8: `Stop`, `SubagentStop`, `SessionEnd`, and `Notification` corrections

**Files:**
- Modify: `agent/internal/hooks/hooks.go`
- Modify: `agent/session_events.go:604-622`
- Modify: `agent/session_lifecycle.go:86-100`, `:674-680`
- Modify: `agent/session_tool_round.go:580-598`
- Modify: `agent/subagents.go:2469-2500`
- Modify: `cmd/evener/serve.go:2012-2018`
- Test: `agent/hook_field_corrections_test.go` (new), `cmd/evener/serve_test.go`

**Interfaces:**
- Produces: `func (r *Runner) RunSubagentStopFor(ctx context.Context, input Input, agentType string) StopResult`.
- Produces: `func (s *Session) CloseWithReason(reason string)`; `closeOptions.reason string`.

- [ ] **Step 1: Write the failing tests**

`agent/hook_field_corrections_test.go` (new):

```go
// TestStop_HookInputFields proves Stop carries stop_hook_active (false first,
// true after a block) and last_assistant_message equal to the delivered reply.
func TestStop_HookInputFields(t *testing.T) {}

// TestSubagentStop_MatchesAgentTypeAndCarriesClosingText proves the agent-type
// matcher and that last_assistant_message is the child's last assistant turn,
// not the delivered report.
func TestSubagentStop_MatchesAgentTypeAndCarriesClosingText(t *testing.T) {}

// TestSessionEnd_ReasonVocabulary proves a clear close sends "clear" and a plain
// close sends "other", and a hook matcher for each fires only on its own.
func TestSessionEnd_ReasonVocabulary(t *testing.T) {}

// TestNotification_CarriesTypeAndTitle proves a diagnostic warning fires the
// "warning"-matched hook with notification_type present, and an
// "idle_prompt"-matched hook does not fire.
func TestNotification_CarriesTypeAndTitle(t *testing.T) {}
```

- [ ] **Step 2: Run, expect failure**

`go test ./agent/ -run 'TestStop_HookInputFields|TestSubagentStop_Matches|TestSessionEnd_Reason|TestNotification_Carries' -v` → FAIL.

- [ ] **Step 3: Implement**

- `hookInput` gains nothing new for these events; each call site fills its own fields.
- `Stop`: in `deliverIfCommunicated`, set `hi.StopHookActive = &s.stopHookActive` and `hi.LastAssistantMessage = text`; set `s.stopHookActive = true` when a Stop hook blocks, and clear it in `execTool` before dispatch.
- `SubagentStop`: add `RunSubagentStopFor`; `runSubagentStopHook` passes the child's agent type and fills `LastAssistantMessage` from the last `TurnAssistant` in the child's history, `StopHookActive` from a per-run flag set in `resetSubagentForRunLocked`.
- `SessionEnd`: `closeOptions` gains `reason`; `CloseWithReason(reason)` sets it; `session_lifecycle.go`'s `SessionEnd` dispatch sets `hi.Reason` to the Claude value (`"clear"` when `reason == "clear"`, else `"other"`) and passes that as the matcher target. `cmd/evener/serve.go`'s `closeSupersededSession` calls `sess.CloseWithReason("clear")` on the non-shutdown path.
- `Notification`: `runNotificationHook` sets `hi.NotificationType = "warning"` and `hi.Title` from the warning's title, and passes `"warning"` as the matcher target.

- [ ] **Step 4: Run, expect pass**

`go test ./agent/ -run 'Stop|SessionEnd|Notification|Subagent' -count=1` → PASS.
`go test ./cmd/evener/ -run TestClear -count=1` → PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/session_events.go agent/session_lifecycle.go agent/session_tool_round.go agent/subagents.go cmd/evener/serve.go agent/hook_field_corrections_test.go cmd/evener/serve_test.go
git commit -m "fix(hooks): correct Stop, SubagentStop, SessionEnd and Notification fields and matchers"
```

---

### Task 9: Documentation

**Files:**
- Modify: `docs/hooks.md:120-161` (events table, reserved list), `:251-270` (matcher targets), `:385-424` (input fields), `:509-528` (exit codes), plus a new caveats block
- Modify: `docs/subagent-runtime-contracts.md:166-175`
- Modify: `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` (event table, tier lists, the "nine" occurrences)
- Modify: `docs/product/subsystems.md` — only if the S17–S19 hooks row's claim changes

**Interfaces:** none; prose and tables only.

- [ ] **Step 1: Update `docs/hooks.md`**

- The events table becomes fourteen events, correct matcher targets for every row, and the two new "no target"/bypassed rows.
- The reserved list drops the five.
- Add the four deliberate changes as a "Behavior changes in this version" block: `PostToolUse` success-only, `continue:false` honored, `effort` object, matcher `,`/space.
- Add the named caveats: `PreCompact` cannot block; `Notification`'s only type is `warning`; `SessionEnd` sends only `clear`/`other`; retirement skips `SessionEnd`; `terminalSequence` has no delivery site; `background_tasks`/`session_crons`/`agent_transcript_path` absent; Go RE2.
- `PostToolUseFailure.error` is the model-facing text; `PostToolBatch` has no matcher; `SubagentStart` reports `subagent` for a default subagent.

- [ ] **Step 2: Update `docs/subagent-runtime-contracts.md`**

Replace the "Nine events fire" line with the fourteen and their tiers, and the matcher list.

- [ ] **Step 3: Update `07`**

- The event table: five rows move from `reserved-placeholder` to `implemented, compatibility-incomplete` with accurate notes.
- The `reserved-placeholder` bullet list loses the five.
- Every "nine" becomes the right number; leave the dated Phase-1 narrative ("the nine events evener already fires") intact where it describes history.

- [ ] **Step 4: Check for stale counts**

```bash
rg -n 'nine (fired |)events|Nine events' docs
rg -n 'not yet wired' docs
```

Every hit is either updated or is a dated document under `docs/superpowers/`.

- [ ] **Step 5: Commit**

```bash
git add docs/hooks.md docs/subagent-runtime-contracts.md docs/subagent-management/07-lifecycle-hooks-claude-compat.md
git commit -m "docs(hooks): document the eleven fired events and their caveats"
```

---

### Task 10: Scenario card and the full gate

**Files:**
- Modify: `test/scenarios/hooks-claude-compat-matcher.md` (or create a sibling card)
- Test: the card's own runner

- [ ] **Step 1: Add the live scenario**

Load the real `toolsmith` plugin via `--plugin-dir` and assert the `SubagentStart` warning is absent from the session log and its `inject` hook ran for a delegate. This is the end-to-end proof of the reported bug.

- [ ] **Step 2: Run the targeted suite**

```bash
go test ./agent/internal/hooks/ ./agent/plugin/ ./agent/internal/tool/ ./agent/ ./cmd/evener/ -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the gates, one at a time**

```bash
make lint
make vet
```

- [ ] **Step 4: Commit**

```bash
git add test/scenarios/hooks-claude-compat-matcher.md
git commit -m "test(hooks): prove the toolsmith SubagentStart hook runs"
```

---

## Review Focus

1. A `SubagentStart` hook that exits 1: the spawn still happens and the child still answers. Pinned in Task 3's exit-2 test plus the boundary test's surviving-child assertion.
2. A `PostToolUseFailure` hook that itself fails: the tool's failure result reaches the model unchanged. Task 4's sentinel-file test reads the model-facing result.
3. `PostToolBatch` on a round whose tools were cancelled: no hook or a partial batch, never a hang. Task 5's boundary test includes a cancelled round.
4. Two compactions back to back: no duplicate `PostCompact` for the first fold. Task 7's `actualCompaction` gate plus its test.
5. `StopFailure` on a turn that already streamed text: the draft survives and the hook fires once. Task 6.
