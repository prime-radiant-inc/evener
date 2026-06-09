# Job Control — Phase 3: delegate jobs + `job_send_message` (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `delegate` jobs — independent agentic child conversations fronted by durable `jobstore.JobRecord`s — and `job_send_message`, the single follow-up surface that steers a running delegate, resumes a finished one, or delivers advisory commentary to a session alias. Reuse the existing child-session runtime (`spawnAgent`/`run`/`runCancel`/`communicate`) under the Phase 2 `JobManager`; add the net-new `result_schema` → `structured_result` **capture** path.

**Architecture:** A `delegate` job is a Phase 2 `runningJob` whose `signal` cancels the child run's context (`runCancel`) and whose finalize calls `jm.finalize(...)`. The child session and its run goroutine are produced by the salvaged `spawnAgent` machinery (`agent/subagents.go`); Phase 3 wraps a `jobstore.JobRecord` (Type=delegate, `transcript_ref = local:<childSessionID>`) around it and streams the child's `communicate` result into the per-job log. The child's prose result is `communicate`'s top-level `message`; its structured result is the **raw** `args["output"]` object, which today is dropped by `normalizeNodeOutput` — Phase 3 preserves it via a new `communicateResult.structured` field + a `setCommunicateStructured` setter and threads it to the job record as `structured_result`. `result_schema` is injected with the existing `Profile.WithCommunicateOutputSchema` (which *replaces* the child `communicate` `output` property wholesale) and validated for free at the communicate call boundary, so `structured_result_valid` is true-by-construction. `job_send_message` reuses `sendInput`'s running-steer / idle-resume mechanics, gating concrete-`job_id` resume/steer to the root session by `s.depth`.

**Tech Stack:** Go, `agent/internal/jobstore` (Phase 1), the Phase 2 `JobManager` (`agent/jobs.go`: `jobManager`, `runningJob`, `jobNotification`, `finalize`, `enqueueJobNotification`, `jobsDir`), the existing child-session runtime (`agent/subagents.go`), the tool registry (`tool.RegisteredTool`/`Exec`), `Profile.WithCommunicateOutputSchema`. Module: `primeradiant.com/serf/agent`.

This is **Phase 3 of 6**, implementing spec `docs/superpowers/specs/2026-06-08-job-control-design.md` §5.4 (delegate), §5.4.1 (delegate result production), §5.5 (`job_send_message`), §5.10 (errors), §5.11 (discovery), §6 (notifications). It depends on **Phase 1** (`agent/internal/jobstore` merged) and **Phase 2** (the `JobManager`, `job_read_output`/`job_list`/`job_stop` tools, `capabilityJobControl`, the durable notification bridge, all merged). The new `delegate` and `job_send_message` tools register **alongside** the legacy subagent tools (`spawn_agent`/`resume_agent`/`wait`/…) — a temporary parallel surface; Phase 6 removes the legacy one.

**Conventions for every task below:**
- Work in the `agent` module: run Go commands from `/Users/jesse/prime-radiant/toil-suite/serf/agent`.
- TDD: write the failing test first, watch it fail, write the minimal implementation, watch it pass, commit.
- Commit messages use the repo's `type(scope): subject` style, e.g. `feat(agent): ...`.
- Full `make test` + `make lint` from repo root (`/Users/jesse/prime-radiant/toil-suite/serf`) before the final task.

---

## What Phase 3 reuses (verified against the code — do not rebuild)

- **Child-session runtime** (`agent/subagents.go`): `spawnAgent(ctx, task, model, workingDir string, maxTurns int, agentType, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string) (any, error)` (`subagents.go:155`). Its run goroutine (`subagents.go:384-393`) is `runCtx, runCancel := context.WithCancel(context.Background()); go func(){ defer s.sendersWG.Done(); defer runCancel(); sub.run(runCtx, task) }()`. `spawnAgent` returns JSON `{"agent_id": sub.id, "status":"running"}`; `sub.id == subSess.id`, which is the child session id used to build `transcript_ref`.
- **The `run` finalize block** (`subagents.go:599-679`): after `a.sess.ProcessInput(ctx, input, nil)` returns, it stores `a.result = res`, maps status (cancel+`context.Canceled`→cancelled, err→failed, else completed), closes `a.done`, and arms one notification. Phase 3 hooks the **delegate-job** finalize here without changing the subagent path (the two coexist until Phase 6).
- **Result path** (`agent/session_tools_communicate.go`, `agent/session.go`): the child's `communicate` Exec calls `deps.setCommunicateResult(awaitReply, message, resultText, structuredText)` (`session_tools_communicate.go:85`), storing into the child session's `s.comm` (`session_tool_registry.go:169-179`). `CommunicateOutput()` returns `s.comm.output` (`session.go:479`). `s.comm` is **reset at the top of every `ProcessInput`** (`session_lifecycle.go:435`), so any structured capture must read the child's `s.comm` immediately after its run's `ProcessInput` returns.
- **Steer / resume** (`agent/subagents.go`): `sendInput(ctx, agentID, input)` (`subagents.go:399`) steers a running child (`sub.sess.Steer(input)`) or starts a new run on an idle child. `job_send_message` reuses this.
- **`result_schema` injection** (`agent/provider/profile_overrides.go`): `WithCommunicateOutputSchema(p, schema)` (`profile_overrides.go:23`) clones the profile and calls `replaceCommunicateOutputSchema`, which does `props["output"] = copied` (`profile_overrides.go:137`) — it **replaces** the `output` property wholesale; it does NOT nest the schema under `data`. The emitted `output` is validated at the call boundary (`agent/internal/tool/registry.go:424`, `t.Schema.Validate(args)`) before `Exec` runs, so a non-conforming `output` is rejected as "tool args schema validation failed" and the model retries — `structured_result_valid` is true-by-construction.
- **`registerSubagentTools(reg, s, deps)`** (`agent/session_tools_subagent.go:14`): the exact tool-registration precedent (capture `s` directly; `reg.Register(tool.RegisteredTool{Tool: llm.Tool{Definition: tool.DefX(...)}, Exec: func(ctx, env, args){...}})`). `registerCoreTools` (`agent/session_tool_registry.go:213`) calls the `registerXxxTools` helpers.
- **`DefTaskList(effortLevels []string)`** (`agent/internal/tool/definitions.go:444`): the build-time enum-interpolation precedent — build a schema map, conditionally set `schema["enum"]` when the slice is non-empty. `DefDelegate(agentTypes)` mirrors this for `agent_type` and `reasoning_effort`.
- **`availableAgentEntries()`** (`agent/session_tools.go:496`): the agent-type roster, filtered by `agentUsesRootOnlyManagementTools`. Phase 3 adds a sibling that returns just the names for the `agent_type` enum.
- **`capabilityJobControl`** (`agent/provider/profile.go`, added in Phase 2): the capability block under which `job_read_output`/`job_list`/`job_stop` already register. Phase 3 adds `DefDelegate(nil)` + `DefJobSendMessage()` to that block.

---

## File structure

```
agent/
  job_delegate.go          NEW — delegate runtime: createDelegate over spawnAgent + a runningJob;
                                 the child-run finalize→jm.finalize bridge; structured-result capture;
                                 sendDelegateMessage (steer/resume) for job_send_message
  job_delegate_test.go     NEW
  session_tools_jobs.go    EDIT — add the delegate + job_send_message handlers (registerJobTools, from Phase 2)
  session_tools_jobs_test.go  EDIT — add delegate / job_send_message tool tests
  session_tools_communicate.go  EDIT — capture the raw args["output"] via deps.setCommunicateStructured
  session_tool_registry.go EDIT — add setCommunicateStructured to toolDeps + newToolDeps
  session.go               EDIT — communicateResult.structured field; CommunicateStructured() accessor
  session_tools.go         EDIT — delegateAgentTypeNames(); inject the agent_type enum onto the
                                 advertised delegate def in rebuildToolDefsCache
  internal/tool/definitions.go  EDIT — DefDelegate(agentTypes []string), DefJobSendMessage()
  internal/tool/definitions_test.go  EDIT — Def tests
  provider/profile.go      EDIT — capabilityJobControl block adds DefDelegate(nil) + DefJobSendMessage()
```

The legacy `spawn_agent`/`resume_agent`/etc. defs and `agent/subagents.go` machinery are **untouched** (Phase 6 removes the legacy tool surface and re-points the salvaged run/cancel/finalize internals).

---

## Task 1: `communicateResult.structured` + capture seam

**Files:**
- Modify: `agent/session.go` (add `structured any` to `communicateResult`; add `CommunicateStructured()` accessor)
- Modify: `agent/session_tool_registry.go` (`toolDeps.setCommunicateStructured` + wire in `newToolDeps`)
- Modify: `agent/session_tools_communicate.go` (call `deps.setCommunicateStructured(args["output"])` in the communicate Exec)
- Test: `agent/session_tools_communicate_test.go` (create — capture test)

This is the **net-new** part of §5.4.1: today the communicate Exec funnels `output` through `normalizeNodeOutput` (`session_tools_communicate.go:42,130`), whose `nodeOutput` struct keeps only `{decision,message,data,artifacts}` and **drops every other field**; `CommunicateOutput()` returns that canonicalized envelope, so a `result_schema` of e.g. `{summary,files}` is silently lost. We add a path that preserves the **raw** `args["output"]` object so a delegate job can surface it as `structured_result`.

- [ ] **Step 1: Write the failing test** — `agent/session_tools_communicate_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// The communicate Exec must preserve the RAW output object (including schema
// fields normalizeNodeOutput drops) so a delegate job can surface it as
// structured_result.
func TestCommunicateCapturesRawStructuredOutput(t *testing.T) {
	var captured any
	deps := &toolDeps{
		abort:          func(context.Context) error { return nil },
		emit:           func(events_kind any, _ any) {}, // replaced below
		drainSteering:  func() []steeringMessage { return nil },
		prependSteering: func([]steeringMessage) {},
		resultToolName: func() string { return "communicate" },
		setCommunicateResult: func(bool, string, string, string) {},
		setCommunicateStructured: func(raw any) { captured = raw },
	}
	// emit takes a typed signature; use a no-op matching the real one.
	deps.emit = func(_ eventsKindShim, _ eventsDataShim) {}

	reg := tool.NewRegistry()
	registerCommunicateTool(reg, deps)
	rt := reg.Get("communicate")
	if rt == nil {
		t.Fatal("communicate not registered")
	}

	args := map[string]any{
		"message":     "report",
		"await_reply": false,
		"output": map[string]any{
			"summary": "did the thing",
			"files":   []any{"a.go", "b.go"},
		},
	}
	if _, err := rt.Exec(context.Background(), execenv.ExecutionEnvironment(nil), args); err != nil {
		t.Fatalf("exec: %v", err)
	}

	want := map[string]any{"summary": "did the thing", "files": []any{"a.go", "b.go"}}
	if !reflect.DeepEqual(captured, want) {
		got, _ := json.Marshal(captured)
		t.Fatalf("captured structured = %s, want raw output preserved", got)
	}
}
```

NOTE for the implementer: the two `eventsKindShim`/`eventsDataShim` placeholders above are only there because `toolDeps.emit` has a concrete typed signature (`func(events.EventKind, events.EventData)`). Do **not** invent shim types — instead build the test the way the existing communicate tests do: import `primeradiant.com/serf/agent/events` and set `emit: func(events.EventKind, events.EventData) {}`. Drop the `eventsKindShim` lines and the `deps.emit = ...` reassignment; set `emit` directly in the struct literal with the real signature. (Verify the field type at `agent/session_tool_registry.go:29`: `emit func(kind events.EventKind, data events.EventData)`.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestCommunicateCapturesRawStructuredOutput -v`. Expected: FAIL to compile (`toolDeps has no field setCommunicateStructured`).

- [ ] **Step 3: Implement.**

In `agent/session.go`, add a `structured any` field to `communicateResult` (struct at `session.go:262`):

```go
type communicateResult struct {
	called     bool   // communicate/result was invoked this turn
	awaitReply bool   // the call expects a user reply rather than completing
	text       string // the message shown to the user
	reply      string // the text handed back to the caller
	output     string // canonical structured output (CommunicateOutput)
	structured any    // raw output object as the model emitted it (delegate structured_result)
}
```

And add an accessor next to `CommunicateOutput` (`session.go:477`):

```go
// CommunicateStructured returns the raw structured output object from the most
// recent communicate call (the args["output"] value as the model emitted it,
// before normalizeNodeOutput canonicalization). nil when none was provided. Used
// by delegate jobs to surface structured_result.
func (s *Session) CommunicateStructured() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comm.structured
}
```

In `agent/session_tool_registry.go`, add the setter to `toolDeps` (after the `setCommunicateResult` field at line 63):

```go
	// setCommunicateStructured records the raw output object the model emitted
	// (pre-canonicalization), for delegate structured_result capture.
	setCommunicateStructured func(raw any)
```

and wire it in `newToolDeps` (alongside `setCommunicateResult` at `session_tool_registry.go:169`):

```go
		setCommunicateStructured: func(raw any) {
			s.mu.Lock()
			s.comm.structured = raw
			s.mu.Unlock()
		},
```

NOTE: `setCommunicateResult` (`session_tool_registry.go:169-179`) assigns a fresh `communicateResult{...}` literal. To avoid that literal clobbering `structured`, the communicate Exec must call `setCommunicateStructured` **after** `setCommunicateResult` (see below); the setter then writes onto the already-stored struct's field. Both run under `s.mu`, sequentially in the same goroutine, so ordering holds.

In `agent/session_tools_communicate.go`, in the communicate Exec, capture the raw output. The existing line `deps.setCommunicateResult(awaitReply, message, resultText, structuredText)` is at `session_tools_communicate.go:85`; add the structured capture immediately after it:

```go
			deps.setCommunicateResult(awaitReply, message, resultText, structuredText)
			if explicitStructuredOutput {
				deps.setCommunicateStructured(args["output"])
			}
```

`explicitStructuredOutput` is already computed at `session_tools_communicate.go:49` (`hasMeaningfulNodeOutput(originalOutput)`). Capturing only when explicit keeps an empty `{message:"",data:{},artifacts:[]}` conversational envelope from registering as a structured result.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestCommunicateCapturesRawStructuredOutput -v`. Expected: PASS. Then `cd agent && go test ./ -run TestCommunicate -v` to confirm no regression in the existing communicate tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session.go agent/session_tool_registry.go agent/session_tools_communicate.go agent/session_tools_communicate_test.go
git commit -m "feat(agent): capture raw communicate output for delegate structured_result"
```

---

## Task 2: `DefDelegate` + `DefJobSendMessage` tool definitions

**Files:**
- Modify: `agent/internal/tool/definitions.go` (add `DefDelegate(agentTypes []string)`, `DefJobSendMessage()`)
- Modify: `agent/internal/tool/definitions_test.go` (add tests)

- [ ] **Step 1: Write the failing test** — add to `agent/internal/tool/definitions_test.go`:

```go
func TestDefDelegateParamsAndEnum(t *testing.T) {
	def := DefDelegate([]string{"explorer", "implementer"})
	if def.Name != "delegate" {
		t.Fatalf("name = %q, want delegate", def.Name)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"task", "background", "agent_type", "model", "reasoning_effort", "block_timeout_ms", "result_schema"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefDelegate missing param %q", p)
		}
	}
	req := toStringSlice(def.Parameters["required"])
	if len(req) != 1 || req[0] != "task" {
		t.Errorf("required = %v, want [task]", req)
	}
	at := props["agent_type"].(map[string]any)
	enum := toStringSlice(at["enum"])
	if len(enum) != 2 || enum[0] != "explorer" || enum[1] != "implementer" {
		t.Errorf("agent_type enum = %v, want [explorer implementer]", enum)
	}
}

func TestDefDelegateNoEnumWhenNoTypes(t *testing.T) {
	def := DefDelegate(nil)
	props := def.Parameters["properties"].(map[string]any)
	at := props["agent_type"].(map[string]any)
	if _, ok := at["enum"]; ok {
		t.Errorf("agent_type must have no enum when no types are available")
	}
}

func TestDefJobSendMessageParams(t *testing.T) {
	def := DefJobSendMessage()
	if def.Name != "job_send_message" {
		t.Fatalf("name = %q, want job_send_message", def.Name)
	}
	props := def.Parameters["properties"].(map[string]any)
	for _, p := range []string{"target", "message", "on_finished", "background", "block_timeout_ms"} {
		if _, ok := props[p]; !ok {
			t.Errorf("DefJobSendMessage missing param %q", p)
		}
	}
	req := toStringSlice(def.Parameters["required"])
	if len(req) != 2 {
		t.Errorf("required = %v, want [target message]", req)
	}
	of := props["on_finished"].(map[string]any)
	enum := toStringSlice(of["enum"])
	if len(enum) != 2 || enum[0] != "resume" || enum[1] != "fail" {
		t.Errorf("on_finished enum = %v, want [resume fail]", enum)
	}
}
```

(`toStringSlice` is the existing helper in `agent/provider/profile_overrides.go` — but it is package `provider`, not `tool`. Inside `internal/tool/definitions_test.go` use a small local helper instead, or read `def.Parameters["required"].([]string)` directly since `DefDelegate` builds `required` as a `[]string` literal. Prefer the direct `[]string` assertion: `req, _ := def.Parameters["required"].([]string)`. Same for `enum`, which `DefDelegate` sets as `[]string`.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./internal/tool/ -run 'TestDefDelegate|TestDefJobSendMessage' -v`. Expected: FAIL to compile (`undefined: DefDelegate`, `undefined: DefJobSendMessage`).

- [ ] **Step 3: Implement** in `agent/internal/tool/definitions.go`. Mirror the `DefTaskList(effortLevels)` enum pattern (`definitions.go:444-452`). The description strings are copied **verbatim** from spec §5.4 and §5.5.

```go
// DefDelegate defines the delegate tool, which starts a NEW delegate
// conversation (independent agentic work) and returns a job_id. agentTypes
// constrains the agent_type enum to the session's available roles; pass nil to
// omit the enum (free-form). reasoning_effort is a separate enum; v1 leaves it
// free-form here and lets the handler resolve it (the prompt's agents section
// and the provider's effort levels remain the human-readable roster).
func DefDelegate(agentTypes []string) llm.ToolDefinition {
	agentTypeSchema := map[string]any{
		"type":        "string",
		"description": "Role for the delegate. Choose from the enum; the roles are described in your agents section.",
	}
	if len(agentTypes) > 0 {
		agentTypeSchema["enum"] = append([]string(nil), agentTypes...)
	}
	return llm.ToolDefinition{
		Name: "delegate",
		Description: "Start a NEW delegate conversation to do independent agentic work, and get back a `job_id`. " +
			"It runs in the background by default; omit `background` unless you mean to wait inline. " +
			"`delegate` never resumes or steers an existing delegate — to follow up on one you already " +
			"started, use `job_send_message`. Optional: `agent_type` to pick a role (choose from the enum; " +
			"the roles are described in your agents section); `model` and `reasoning_effort` overrides; a " +
			"`result_schema` to request a validated structured result; or `background=false` to wait up to " +
			"`block_timeout_ms` (a timeout leaves the job running). Judge the task from the output, not from " +
			"`status=\"completed\"`.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task":             map[string]any{"type": "string"},
				"background":       map[string]any{"type": "boolean", "description": "Default true. Set false to wait inline up to block_timeout_ms."},
				"agent_type":       agentTypeSchema,
				"model":            map[string]any{"type": "string", "description": "Model override (default: parent model)."},
				"reasoning_effort": map[string]any{"type": "string", "description": "Reasoning effort for this delegate (e.g. low, medium, high, xhigh). Default inherits from parent."},
				"block_timeout_ms": map[string]any{"type": "integer", "description": "Foreground wait bound when background=false. A timeout leaves the job running."},
				"result_schema": map[string]any{
					"type":                 "object",
					"description":          "JSON-Schema-like object for a structured result. Becomes the delegate's structured communicate output; Serf validates it and surfaces structured_result.",
					"additionalProperties": true,
				},
			},
			"required": []string{"task"},
		},
	}
}

// DefJobSendMessage defines the job_send_message tool, the single follow-up
// surface for delegate jobs and observer/sidecar commentary.
func DefJobSendMessage() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "job_send_message",
		Description: "Send a follow-up message to a delegate by `job_id`. If that delegate is still running, your " +
			"message steers the live run; if it has finished, Serf resumes the same conversation as a new " +
			"job and returns the new `job_id`. Set `on_finished=\"fail\"` to require a live target — if the " +
			"delegate has already finished, the call then fails (`target_terminal`) instead of resuming. " +
			"The same tool delivers observer commentary to a session alias (`caller`, `main`, `watched`).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"target":  map[string]any{"type": "string", "description": "A delegate job_id, or a session alias: caller | main | watched."},
				"message": map[string]any{"type": "string"},
				"on_finished": map[string]any{
					"type":        "string",
					"enum":        []string{"resume", "fail"},
					"description": "Default resume: a finished delegate is resumed as a new job. fail: require a live target (target_terminal if finished).",
				},
				"background":       map[string]any{"type": "boolean", "description": "Default true for newly resumed jobs."},
				"block_timeout_ms": map[string]any{"type": "integer", "description": "Foreground wait bound when background=false."},
			},
			"required": []string{"target", "message"},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./internal/tool/ -run 'TestDefDelegate|TestDefJobSendMessage' -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git commit -m "feat(tool): DefDelegate (agent_type enum) and DefJobSendMessage"
```

---

## Task 3: wire `delegate` + `job_send_message` into the profile capability

**Files:**
- Modify: `agent/provider/profile.go` (`toolDefinitionsForCapabilities`: add `DefDelegate(nil)` + `DefJobSendMessage()` under the `capabilityJobControl` block Phase 2 added)
- Test: `agent/provider/profile_test.go` (add or extend) — assert the two defs appear for a job-control-capable profile

NOTE: this task assumes Phase 2 already added `capabilityJobControl` (the constant + the `if enabled[capabilityJobControl] { add(tool.DefJobReadOutput()); add(tool.DefJobList()); add(tool.DefJobStop()) }` block) and added it to the three provider capability sets (`openAICodexCapabilities`, `anthropicStyleCapabilities`, `geminiStyleCapabilities`). If for any reason that block is absent, STOP — Phase 2 is the prerequisite; do not invent it here.

- [ ] **Step 1: Write the failing test** — add to `agent/provider/profile_test.go`:

```go
func TestJobControlCapabilityIncludesDelegateAndSendMessage(t *testing.T) {
	defs := toolDefinitionsForCapabilities([]toolCapability{capabilityJobControl}, nil)
	have := map[string]bool{}
	for _, d := range defs {
		have[d.Name] = true
	}
	for _, name := range []string{"delegate", "job_send_message", "job_read_output", "job_list", "job_stop"} {
		if !have[name] {
			t.Errorf("capabilityJobControl missing %q", name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./provider/ -run TestJobControlCapabilityIncludesDelegateAndSendMessage -v`. Expected: FAIL (`delegate`/`job_send_message` absent).

- [ ] **Step 3: Implement** — in `agent/provider/profile.go`, in the `capabilityJobControl` block inside `toolDefinitionsForCapabilities` (added by Phase 2), add the two defs alongside the existing three:

```go
	if enabled[capabilityJobControl] {
		add(tool.DefJobReadOutput())
		add(tool.DefJobList())
		add(tool.DefJobStop())
		add(tool.DefDelegate(nil))       // agent_type enum injected per-session in rebuildToolDefsCache
		add(tool.DefJobSendMessage())
	}
```

`DefDelegate(nil)` is the static placeholder advertised at the profile level; the per-session `agent_type` enum is injected onto the advertised copy in Task 4. The registry's compiled schema validates everything except the `agent_type` enum (the handler validates `agent_type` against `s.pluginAgents`, exactly as `spawnAgent` does at `subagents.go:172`), so the placeholder-vs-live skew is intentional and safe.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./provider/ -run TestJobControlCapabilityIncludesDelegateAndSendMessage -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/provider/profile.go agent/provider/profile_test.go
git commit -m "feat(provider): wire delegate + job_send_message under capabilityJobControl"
```

---

## Task 4: inject the live `agent_type` enum onto the advertised `delegate` def

**Files:**
- Modify: `agent/session_tools.go` (`delegateAgentTypeNames()`; inject the enum in `rebuildToolDefsCache`)
- Test: `agent/session_tools_test.go` (add) — a session with plugin agents advertises a `delegate` whose `agent_type` enum lists those agents

The profile def carries `DefDelegate(nil)` (no enum). `rebuildToolDefsCache` (`session_tools.go:544`) builds `cachedToolDefs` from the profile defs (loop 1, `session_tools.go:554-565`). Since the profile def is static, we substitute the live enum onto the advertised `delegate` def there. The agent-type set mirrors `availableAgentEntries()` (`session_tools.go:496-504`): the keys of `s.pluginAgents`, filtered by `agentUsesRootOnlyManagementTools`, sorted.

- [ ] **Step 1: Write the failing test** — `agent/session_tools_test.go` (or extend an existing one in that file):

```go
func TestDelegateAdvertisesAgentTypeEnum(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.pluginAgents = map[string]plugin.Agent{
		"explorer":    {Name: "explorer", Description: "explore"},
		"implementer": {Name: "implementer", Description: "implement"},
	}
	sess.rebuildToolDefsCache()

	var delegate *llm.ToolDefinition
	for i := range sess.cachedToolDefs {
		if sess.cachedToolDefs[i].Name == "delegate" {
			delegate = &sess.cachedToolDefs[i]
			break
		}
	}
	if delegate == nil {
		t.Fatal("delegate not advertised")
	}
	props := delegate.Parameters["properties"].(map[string]any)
	at := props["agent_type"].(map[string]any)
	enum, _ := at["enum"].([]string)
	if len(enum) != 2 || enum[0] != "explorer" || enum[1] != "implementer" {
		t.Fatalf("agent_type enum = %v, want sorted [explorer implementer]", at["enum"])
	}
}
```

NOTE: depending on whether Phase 2 left `delegate` callable in a fresh root session, `rebuildToolDefsCache` loop 1 only advertises a profile tool when `registered[td.Name]` is true. `delegate` is registered by `registerJobTools` (Task 5). For this test to see `delegate` in `cachedToolDefs`, the session must have registered it — `NewSession` runs `registerCoreTools` → `registerJobTools` (wired in Task 5). If you run Task 4 before Task 5's registration wiring lands, register `delegate` in a throwaway way in the test or reorder so Task 5's `registerCoreTools` call exists. Simplest: implement Task 5's `registerJobTools` delegate registration first if the build complains; the two tasks are adjacent. (The enum-injection code itself is independent and is what this task tests.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestDelegateAdvertisesAgentTypeEnum -v`. Expected: FAIL (enum absent — the advertised def is the static `DefDelegate(nil)`).

- [ ] **Step 3: Implement** in `agent/session_tools.go`.

Add the name helper near `availableAgentEntries` (`session_tools.go:496`):

```go
// delegateAgentTypeNames returns the sorted agent-type names available to the
// delegate tool's agent_type enum: the plugin agents that are not top-level-only
// (the same filter availableAgentEntries uses for the prompt roster).
func (s *Session) delegateAgentTypeNames() []string {
	names := make([]string, 0, len(s.pluginAgents))
	for name, agent := range s.pluginAgents {
		if agentUsesRootOnlyManagementTools(agent) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
```

In `rebuildToolDefsCache` (`session_tools.go:544`), in loop 1 where each profile def is wired (`session_tools.go:554-565`), substitute the live-enum delegate def. Change the loop body so that when `td.Name == "delegate"` the canonical def passed to `wireToolDef` is the enum-bearing one:

```go
	for _, td := range s.profile.ToolDefinitions() {
		if registered[td.Name] {
			if td.Name == "delegate" {
				td = tool.DefDelegate(s.delegateAgentTypeNames())
			}
			wire := wireToolDef(td, nameMap)
			defs = append(defs, wire)
			included[td.Name] = true // canonical
			included[wire.Name] = true
		}
	}
```

This is the single, named special-case (parallel to the `communicate` dynamic-schema handling). `wireToolDef` then renames (`delegate` is not in any `ToolNameMap`, so the name is unchanged) and adds the purpose param. The advertised enum now reflects the live plugin agents.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestDelegateAdvertisesAgentTypeEnum -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_tools.go agent/session_tools_test.go
git commit -m "feat(agent): advertise live agent_type enum on the delegate tool"
```

---

## Task 5: delegate runtime — `createDelegate` over `spawnAgent` + a `runningJob`

**Files:**
- Create: `agent/job_delegate.go`
- Create: `agent/job_delegate_test.go`

This is the core of §5.4 / §5.4.1. A delegate job:
1. spawns a child session via the salvaged `spawnAgent` machinery,
2. commits a `jobstore.JobRecord` (Type=delegate, `transcript_ref = local:<childSessionID>`) + a `job_started` event, then a `job_session_assigned` event carrying the transcript ref,
3. registers a Phase 2 `runningJob` whose `signal` cancels the child run and whose finalize is driven by the child run's completion,
4. on terminal, streams the child's prose result into `jobs/<job_id>.log` and finalizes via `jm.finalize(...)`, capturing `structured_result` from the child's `CommunicateStructured()`.

Because `spawnAgent` already owns the run goroutine + `runCancel` + `sub.run` finalize (`subagents.go:384-393, 599-679`), Phase 3 does **not** start its own goroutine. Instead it bridges the child's terminal into the JobManager. The cleanest seam that reuses (not rebuilds) the runtime: spawn the child, then start one **bridge goroutine** that waits on the child's `done` channel (the subagent's `done`, closed by `sub.run` at `subagents.go:666`), reads the child's status/result/structured output, streams the result into the job log, and calls `jm.finalize`. The bridge does not re-run the child; it observes the existing run.

> **Design note (verify the `done`/status accessors before coding).** The subagent's terminal state lives on the `subagent` struct (`subagents.go:58-83`): `done chan struct{}`, `status SubagentStatus`, `result string`, `sess *Session`. These are unexported fields in package `agent`, so `job_delegate.go` (same package) can read them under `sub.mu`. Use `s.subagents.get(childSessionID)` (`subagents.go:574`, `getSub`) to fetch the `*subagent` after spawn. The child's structured output is on the **child** session: `sub.sess.CommunicateStructured()` (Task 1). The prose result is `sub.result` (== `a.result`, the `communicate` message, set at `subagents.go:631`).

- [ ] **Step 1: Write the failing test** — `agent/job_delegate_test.go`. Script a child that finishes by calling `communicate` with structured `output`; assert the delegate job finalizes `completed`, surfaces the prose as the job log, and captures `structured_result`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// communicateWithStructured scripts a single communicate tool call carrying a
// structured output object (mirrors communicateResponse in
// communicate_test_helpers_test.go, with a non-empty output).
func communicateWithStructured(message string, output map[string]any) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":     message,
		"await_reply": false,
		"output":      output,
	})
	return llm.Response{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: args, Type: "function"},
			}},
		},
	}
}

func TestCreateDelegateForegroundCapturesResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return communicateWithStructured("delegate report", map[string]any{"summary": "ok", "files": []any{"x.go"}})
			},
		},
	})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res := parent.createDelegate(ctx, delegateArgs{Task: "do it", BlockTimeoutMS: 8000}) // foreground
	if res.JobID == "" {
		t.Fatalf("expected a job_id: %+v", res)
	}
	if res.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %q, want completed; res=%+v", res.Status, res)
	}
	if res.TranscriptRef == "" {
		t.Errorf("transcript_ref should be set")
	}
	if res.Output != "delegate report" {
		t.Errorf("output = %q, want the communicate message", res.Output)
	}
	sr, _ := res.StructuredResult.(map[string]any)
	if sr["summary"] != "ok" {
		t.Errorf("structured_result = %+v, want summary=ok", res.StructuredResult)
	}
	if !res.StructuredResultValid {
		t.Errorf("structured_result_valid should be true-by-construction")
	}
	// The job is terminal and listable.
	jobs := parent.jobs.list(listFilter{})
	if len(jobs) != 1 || jobs[0].Type != jobstore.JobDelegate {
		t.Fatalf("list = %+v", jobs)
	}
}

func TestCreateDelegateBackgroundReturnsRunning(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Child blocks: no scripted communicate, default "done" assistant text loops
	// until we cancel. Keep it simple — the child will keep running.
	c.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	res := parent.createDelegate(context.Background(), delegateArgs{Task: "long", Background: true, BlockTimeoutMS: 120000})
	if res.JobID == "" || !res.RunningInBackground || res.Status != jobstore.StatusRunning {
		t.Fatalf("res = %+v, want background/running with job_id", res)
	}
	_, _ = parent.jobs.stop(res.JobID) // cleanup: cancel the child run
}
```

NOTE on the background test: a `fakeAdapter` with no `steps` returns `Assistant("done")` for every call (`session_test.go:64-65`). Whether that drives the child to a quick natural finish or a loop depends on the session's bare-text retry/nudge behavior; the test only asserts the **synchronous** background return shape (`job_id`, running, background) and then stops the job, so it does not depend on the child's eventual terminal. If `createDelegate(background=true)` blocks, that is a bug to fix in step 3 (background must return after the child is spawned + the record committed, never waiting on completion).

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestCreateDelegate -v`. Expected: FAIL to compile (`createDelegate`, `delegateArgs`, `delegateResult` undefined).

- [ ] **Step 3: Implement** `agent/job_delegate.go`.

Types:

```go
// delegateArgs are the parsed inputs to the delegate tool.
type delegateArgs struct {
	Task            string
	AgentType       string
	Model           string
	ReasoningEffort string
	Background      bool
	BlockTimeoutMS  int
	ResultSchema    map[string]any
}

// delegateResult is the delegate tool's return payload (spec §5.4 return shapes).
type delegateResult struct {
	JobID                 string
	Type                  string
	Status                jobstore.Status
	Reason                string
	RunningInBackground   bool
	TimedOut              bool
	TranscriptRef         string
	Output                string
	Truncated             bool
	StructuredResult      any
	StructuredResultValid bool
	Err                   error // synchronous tool error (no job record) — surfaced by the handler
}
```

`createDelegate(ctx context.Context, args delegateArgs) delegateResult`:

1. **Clamp `BlockTimeoutMS`** with the same bounds as shell (default 120000, min 1000, max 600000). Reuse the Phase 2 clamp helper if one exists (`job_shell.go`); otherwise a local `clampBlockTimeout(ms int) int`.
2. **Inject `result_schema`** if present, by applying `provider.WithCommunicateOutputSchema` to the child's profile **inside `spawnAgent`**. `spawnAgent` resolves `subProfile` from `s.currentProfile()` plus `model`/`agent_type` overrides (`subagents.go:180-202`) and there is no existing parameter to pass a communicate-output-schema override through, and the schema is per-*call* (not per parent-session), so the right seam is a **new parameter on `spawnAgent`**:
   - Add a trailing parameter `communicateOutputSchema map[string]any` to `spawnAgent` (`subagents.go:155`). The legacy `spawn_agent` handler (`session_tools_subagent.go:71`, the only other caller) passes `nil`; `createDelegate` passes `args.ResultSchema`.
   - In `spawnAgent`, immediately before `NewSession` (`subagents.go:302`), apply it: `if len(communicateOutputSchema) > 0 { subProfile = provider.WithCommunicateOutputSchema(subProfile, communicateOutputSchema) }`.
   - Add the import `"primeradiant.com/serf/agent/provider"` to `subagents.go` (it does not import `provider` today; verify with `grep -n "agent/provider" agent/subagents.go`).

   Validation is then free: the child's `communicate` tool's `output` property *is* the schema, and the registry validates it at the call boundary (`registry.go:424`) before the child's `communicate` Exec runs. Do not re-validate. (`WithCommunicateOutputSchema` returns the profile unchanged for a nil/empty schema — `profile_overrides.go:24` — so passing `nil` is a safe no-op.)
3. **Spawn the child:** call `s.spawnAgent(ctx, args.Task, args.Model, "", 0, args.AgentType, args.ReasoningEffort, nil, nil, args.ResultSchema)`. On error, return `delegateResult{Err: err}` (synchronous tool error, no job record — §5.10). Parse the returned JSON to get `agent_id` (== child session id): `var sp map[string]any; json.Unmarshal([]byte(result.(string)), &sp); childID := sp["agent_id"].(string)`.
4. **Commit the durable record:** mint `jobstore.NewJobID()`; build a `jobstore.JobRecord{JobID, Type: JobDelegate, Status: StatusRunning, Task: args.Task, OwnerSessionID: s.id, VisibleToSession: s.id, TranscriptRef: encodeRef("", childID), StartedAt: jm.now(), OutputPath: <dir>/jobs/<job_id>.log}` and `Resumable` left nil for now. Append `EventJobStarted` (carrying type/task/description/owner/visible/started_at) and `EventJobSessionAssigned` (carrying `TranscriptRef`). Open the `OutputStore`. Register a Phase 2 `runningJob` whose `signal` cancels the child run.

   **Wiring `signal` to cancel the child run.** The child's per-run `runCancel` is private to `spawnAgent`'s goroutine. The model-facing cancel of a running delegate run is `cancelAgent` (`subagents.go:544`), which sets `cancelRequested`, calls `sub.cancel()`, and waits for `done`. For `job_stop`/`job_send_message` to cancel the **run** (not destroy the session), `signal` should call `sub.cancel()` after setting `cancelRequested` so finalize maps `context.Canceled`→cancelled. Set:
   ```go
   signal := func() {
       if sub := s.subagents.get(childID); sub != nil {
           sub.mu.Lock()
           sub.cancelRequested = true
           cancel := sub.cancel
           sub.mu.Unlock()
           if cancel != nil {
               cancel()
           }
       }
   }
   ```
   Register `runningJob{rec: &rec, output: out, signal: signal, done: make(chan struct{})}` into `jm.running[jobID]` (use the Phase 2 manager's registration helper / direct map write under `jm.mu` — match whatever Phase 2 exposed; if Phase 2 has no exported registration method, add `jm.track(jobID, *runningJob)` mirroring `createShell`).
5. **Start the bridge goroutine** (observe, do not re-run): fetch `sub := s.subagents.get(childID)`; read `done := sub.done` under `sub.mu`. Then:
   ```go
   go func() {
       <-done // the subagent's run finalize closed it (subagents.go:666)
       s.finalizeDelegate(jobID, childID)
   }()
   ```
   `finalizeDelegate(jobID, childID string)`: fetch `sub`; under `sub.mu` read `status := sub.status`, `prose := sub.result`; read `structured := sub.sess.CommunicateStructured()`. Stream the prose into the job log (`jm.running[jobID].output.Append([]byte(prose))`) BEFORE finalizing (so `job_read_output` sees it). Map `SubagentStatus`→`jobstore.Status`: `SubagentCompleted`→`StatusCompleted` (reason ""), `SubagentFailed`→`StatusFailed` (reason ""), `SubagentCancelled`→`StatusCancelled` (reason "stopped_by_parent"). Stash `structured` so the manager's finalize/notification can carry it — extend `jm.finalize` is unnecessary if the structured result only needs to reach the **foreground** return and the durable record; for the durable record, the simplest is to keep `structured_result` in the **in-memory `runningJob`** (add a `structured any` field) read by the foreground path; it does not need a jobstore event in v1 (job output log + the durable record suffice; `structured_result` re-derivation after restart is out of scope — the runtime is gone, and `job_read_output` returns the prose log). Then call `jm.finalize(jobID, mappedStatus, reason, nil)` (exitCode nil for delegate). `jm.finalize` writes `EventJobFinished` + arms the terminal notification (Phase 2).

   **Capture timing (verified safe).** Reading `sub.sess.CommunicateStructured()` at finalize is correct because `s.comm` is reset only at the top of each `ProcessInput` (`session_lifecycle.go:435`), and the auto-nudge re-run that would reset it fires only when the child did NOT communicate (`!a.sess.Communicated()`, `subagents.go:616`) — a child that emitted structured output has `Communicated() == true`, so no nudge re-run clobbers it. This is the same lifetime `sub.result` already relies on, so the prose and structured channels stay consistent.
6. **Foreground vs background wait** (mirror `runShell`'s select, Phase 2 §5.3 pattern):
   - `Background == true`: return immediately after step 5 with `delegateResult{JobID, Type:"delegate", Status: StatusRunning, RunningInBackground: true, TranscriptRef: rec.TranscriptRef}`. Never wait.
   - `Background == false`: `select { case <-done: ...terminal...; case <-time.After(blockTimeout): ...timeout... }` using the child's `done`.
     - terminal: the bridge goroutine will (or already did) finalize; wait for finalize to complete (it closes `runningJob.done`), then read the finalized record + the captured structured result + the prose tail. Return `delegateResult{JobID, Status: <finalized>, RunningInBackground:false, TranscriptRef, Output: prose, StructuredResult: structured, StructuredResultValid: structured != nil}`.
     - timeout: return `delegateResult{JobID, Status: StatusRunning, Reason: "foreground_timeout", RunningInBackground:true, TimedOut:true, TranscriptRef, Output: <tail so far>}`. The job keeps running; the bridge finalizes later and arms the terminal notification.

   **Race guard:** the bridge goroutine and the foreground `<-done` both observe the same `done`. Finalize must be idempotent (guarded by `runningJob.done` / a `sync.Once`, exactly as Phase 2's `finalizeShell` is guarded). The foreground path must not finalize itself — it only reads the record after the bridge finalizes. Synchronize with the `runningJob.done` channel: after `<-done` (subagent done), block on `<-runningJob.done` (manager finalize done) with the remaining budget, then read.

`structured_result_valid` is `structured != nil` (true-by-construction when a `result_schema` was supplied and the child emitted conforming `output`; informational per §5.4.1). Do NOT re-validate against the schema (the call-boundary validator already enforced it; `compileSchema` is unexported in `agent/internal/tool` and not importable from package `agent`).

- [ ] **Step 4: Run tests to verify they pass** — `cd agent && go test ./ -run TestCreateDelegate -v`. Expected: PASS (both). Also run `cd agent && go test ./ -run 'TestSpawnAgent|TestBuiltinAgents' -v` to confirm the `spawnAgent` signature change did not break the legacy path.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_delegate.go agent/job_delegate_test.go agent/subagents.go agent/session_tools_subagent.go agent/session_config.go
git commit -m "feat(agent): delegate jobs over the child-session runtime + structured_result capture"
```

(The commit includes `subagents.go`/`session_tools_subagent.go`/`session_config.go` because `spawnAgent` gained the `communicateOutputSchema` parameter and the legacy caller passes `nil`. Verify with `grep -n "func (s \*Session) spawnAgent" agent/subagents.go` that the new parameter landed, and `grep -n "s.spawnAgent(" agent/` that BOTH callers compile.)

---

## Task 6: the `delegate` tool handler

**Files:**
- Modify: `agent/session_tools_jobs.go` (add the `delegate` handler to `registerJobTools`)
- Modify: `agent/session_tools_jobs_test.go` (add a tool-level delegate test)

`registerJobTools(reg, s, deps)` is the Phase 2 register that already wires `job_read_output`/`job_list`/`job_stop`. Add `delegate` (and, in Task 7, `job_send_message`) here, mirroring `registerSubagentTools` (`session_tools_subagent.go:14`).

- [ ] **Step 1: Write the failing test** — add to `agent/session_tools_jobs_test.go`. Drive the registered `delegate` tool's `Exec` directly (as the existing job-tool tests do), scripting a child that communicates a structured result, and assert the returned JSON has `job_id`, `type:"delegate"`, `status:"completed"`, `transcript_ref`, and `structured_result`:

```go
func TestDelegateToolForegroundReturnsStructuredResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return communicateWithStructured("done report", map[string]any{"summary": "fixed"})
			},
		},
	})
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rt := s.reg.Get("delegate")
	if rt == nil {
		t.Fatal("delegate tool not registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := rt.Exec(ctx, s.env, map[string]any{"task": "fix the bug", "background": false, "block_timeout_ms": 8000})
	if err != nil {
		t.Fatalf("delegate exec: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out.(string)), &parsed); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if parsed["type"] != "delegate" || parsed["status"] != "completed" {
		t.Errorf("result = %v, want delegate/completed", parsed)
	}
	if parsed["job_id"] == nil || parsed["transcript_ref"] == nil {
		t.Errorf("missing job_id/transcript_ref: %v", parsed)
	}
	sr, _ := parsed["structured_result"].(map[string]any)
	if sr["summary"] != "fixed" {
		t.Errorf("structured_result = %v, want summary=fixed", parsed["structured_result"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestDelegateToolForeground -v`. Expected: FAIL (`delegate` not registered).

- [ ] **Step 3: Implement** the `delegate` handler in `registerJobTools` (`agent/session_tools_jobs.go`). Parse args, call `createDelegate`, marshal `delegateResult` to the spec §5.4 return shapes:

```go
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefDelegate(s.delegateAgentTypeNames())},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			a := delegateArgs{
				Task:            stringArg(args, "task"),
				AgentType:       stringArg(args, "agent_type"),
				Model:           stringArg(args, "model"),
				ReasoningEffort: stringArg(args, "reasoning_effort"),
			}
			if v, ok := args["background"].(bool); ok {
				a.Background = v
			} else {
				a.Background = true // default true (§5.4)
			}
			if v, ok := args["block_timeout_ms"].(float64); ok {
				a.BlockTimeoutMS = int(v)
			}
			if m, ok := args["result_schema"].(map[string]any); ok {
				a.ResultSchema = m
			}
			res := s.createDelegate(ctx, a)
			if res.Err != nil {
				return nil, res.Err // synchronous tool error, no job record (§5.10)
			}
			return marshalDelegateResult(res), nil
		},
	})
```

Register `delegate` with `DefDelegate(s.delegateAgentTypeNames())` so the **registry** def (used for schema validation and as a fallback) carries the same enum the advertised def does. (`stringArg` is the existing helper used by the transcript/jobs handlers — verify with `grep -n "func stringArg" agent/`. If absent in package scope, use `fmt.Sprint` guarded by presence, like `registerSubagentTools` does.)

`marshalDelegateResult(res delegateResult) string` builds the spec §5.4 JSON. Omit `output`/`structured_result`/`truncated` for the pure-background shape; include them for the foreground-terminal shape; include `reason:"foreground_timeout"` for the timeout shape:

```go
func marshalDelegateResult(res delegateResult) string {
	m := map[string]any{
		"job_id":                res.JobID,
		"type":                  "delegate",
		"status":                string(res.Status),
		"running_in_background": res.RunningInBackground,
		"timed_out":             res.TimedOut,
		"transcript_ref":        res.TranscriptRef,
	}
	if res.Reason != "" {
		m["reason"] = res.Reason
	}
	if !res.RunningInBackground || res.TimedOut {
		m["output"] = res.Output
		m["truncated"] = res.Truncated
	}
	if res.StructuredResult != nil {
		m["structured_result"] = res.StructuredResult
		m["structured_result_valid"] = res.StructuredResultValid
	}
	b, _ := json.Marshal(m)
	return string(b)
}
```

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestDelegateTool -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_tools_jobs.go agent/session_tools_jobs_test.go
git commit -m "feat(agent): delegate tool handler"
```

---

## Task 7: `job_send_message` — running steer / idle resume

**Files:**
- Modify: `agent/job_delegate.go` (add `sendDelegateMessage` — steer a running delegate, resume a finished one)
- Test: `agent/job_delegate_test.go` (extend) — running target → `sent`; terminal target → `resumed` + `resumed_from_job_id`

`job_send_message` to a concrete delegate `job_id` reuses `sendInput` (`subagents.go:399`): a running child is steered (`action:"sent"`, same `job_id`); a terminal/resumable child is resumed as a **new** delegate job in the same session (`action:"resumed"`, new `job_id`, `resumed_from_job_id` set). `on_finished="fail"` against a terminal target → `target_terminal` synchronous error.

- [ ] **Step 1: Write the failing test** — extend `agent/job_delegate_test.go`:

```go
func TestSendDelegateMessageResumesTerminal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			// first run finishes
			func(llm.Request) llm.Response { return communicateWithStructured("first", map[string]any{"step": "1"}) },
			// resumed run finishes
			func(llm.Request) llm.Response { return communicateWithStructured("second", map[string]any{"step": "2"}) },
		},
	})
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := s.createDelegate(ctx, delegateArgs{Task: "start", BlockTimeoutMS: 8000})
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first delegate not completed: %+v", first)
	}

	res := s.sendDelegateMessage(ctx, sendMessageArgs{Target: first.JobID, Message: "keep going", OnFinished: "resume", Background: false, BlockTimeoutMS: 8000})
	if res.Err != nil {
		t.Fatalf("send: %v", res.Err)
	}
	if res.Action != "resumed" || res.ResumedFromJobID != first.JobID {
		t.Errorf("action=%q resumed_from=%q, want resumed/%s", res.Action, res.ResumedFromJobID, first.JobID)
	}
	if res.JobID == "" || res.JobID == first.JobID {
		t.Errorf("resumed job must get a new job_id, got %q", res.JobID)
	}
}

func TestSendDelegateMessageFailOnTerminal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateWithStructured("done", map[string]any{"k": "v"}) },
		},
	})
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	first := s.createDelegate(ctx, delegateArgs{Task: "x", BlockTimeoutMS: 8000})
	res := s.sendDelegateMessage(ctx, sendMessageArgs{Target: first.JobID, Message: "more", OnFinished: "fail"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("want target_terminal error, got %+v", res)
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestSendDelegateMessage -v`. Expected: FAIL (`sendDelegateMessage`, `sendMessageArgs`, `sendMessageResult` undefined).

- [ ] **Step 3: Implement** in `agent/job_delegate.go`.

```go
type sendMessageArgs struct {
	Target         string
	Message        string
	OnFinished     string // "resume" (default) | "fail"
	Background     bool
	BlockTimeoutMS int
}

type sendMessageResult struct {
	Target           string
	JobID            string
	Type             string
	Status           jobstore.Status
	RunningInBackground bool
	Action           string // "sent" | "resumed"
	ResumedFromJobID string
	TranscriptRef    string
	Delivered        bool   // alias targets
	MessageType      string // "runtime" for alias targets
	Err              error
}
```

`sendDelegateMessage(ctx, args) sendMessageResult`:

1. **Alias target** (`caller`/`main`/`watched`): inject a runtime/advisory message and return `{Target, Delivered:true, Action:"sent", MessageType:"runtime"}`. Alias delivery for a subagent advising back is the §9 observer path; for v1 wire it to `s.Steer(args.Message)` on the appropriate session (for `caller`/`main` at the root this is the current session; full alias resolution is refined in Phase 4 with watches). Alias targets are permitted for subagents (§5.1). Do not impersonate the user (`message_type:"runtime"`).
2. **Concrete delegate `job_id`** — **root-only**: if `s.depth > 0`, return `{Err: errors.New("not_controllable: concrete delegate targets are root-only")}` (§5.1, §13: subagents may target only aliases). Look up the job record (`jm.list`/`store.Load` by `job_id`); if not a delegate or unknown → `{Err: target_not_found}` / `{Err: target_not_messageable}` (a shell job is `target_not_messageable`, §5.10). Resolve the child session id from `rec.TranscriptRef` (`decodeRef`).
   - The child session is fetched via `s.subagents.get(childSessionID)`. Determine running vs terminal under `sub.mu` (`sub.running`).
   - **Running** target: `sub.sess.Steer(args.Message)` (or `s.sendInput(ctx, childSessionID, args.Message)`, which steers a running child — `subagents.go:408-412`). Return `{Target, JobID: rec.JobID, Type:"delegate", Status: StatusRunning, RunningInBackground:true, Action:"sent", TranscriptRef: rec.TranscriptRef}`. No new terminal notification (§6).
   - **Terminal** target, `OnFinished=="fail"` → `{Err: errors.New("target_terminal: delegate has finished and on_finished=fail")}`.
   - **Terminal** target, `OnFinished` omitted/`"resume"`: create a **new delegate job in the same child session** — call `s.sendInput(ctx, childSessionID, args.Message)` (which starts a new run on the idle child, `subagents.go:417-455`), then commit a **new** `jobstore.JobRecord` (new `job_id`, same `TranscriptRef`, Type=delegate) + `job_started`/`job_session_assigned`, register a `runningJob` + bridge goroutine (reuse the Task 5 finalize bridge), and return `{Target: args.Target, JobID: newJobID, Type:"delegate", Status: StatusRunning, RunningInBackground:true, Action:"resumed", ResumedFromJobID: rec.JobID, TranscriptRef: rec.TranscriptRef}`. Factor the "commit record + register runningJob + start bridge" out of `createDelegate` into a shared helper `attachDelegateJob(childID string, task string) (jobID string, ...)` so resume and create share it (no duplication).

   `OnFinished` default: empty → `"resume"`.

NOTE on the §5.5 `delegate_session_busy` case (another job already running in that delegate session, and not the target): resolving "is another run in flight in this child session" reduces to `sub.running` being true for a non-targeted resume. For v1, a resume against a child whose latest run is still running steers it (the running branch above) rather than erroring; the explicit `delegate_session_busy` error is reserved for the concurrent-child-turns design and need not be wired in Phase 3 unless a test exercises it. Document this in a code comment so the reviewer knows it is a deliberate deferral, not an omission.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestSendDelegateMessage -v`. Expected: PASS (both).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_delegate.go agent/job_delegate_test.go
git commit -m "feat(agent): job_send_message steer/resume for delegate jobs"
```

---

## Task 8: the `job_send_message` tool handler + role gating

**Files:**
- Modify: `agent/session_tools_jobs.go` (add the `job_send_message` handler to `registerJobTools`)
- Modify: `agent/session_tools_jobs_test.go` (add a tool-level test; assert a shell-job target → `target_not_messageable`)

- [ ] **Step 1: Write the failing test** — add to `agent/session_tools_jobs_test.go`:

```go
func TestJobSendMessageToShellJobNotMessageable(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Start a background shell job to get a shell job_id (Phase 2 path).
	shellOut, err := s.reg.Get("shell").Exec(context.Background(), s.env, map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	var sj map[string]any
	_ = json.Unmarshal([]byte(shellOut.(string)), &sj)
	shellJobID, _ := sj["job_id"].(string)
	if shellJobID == "" {
		t.Fatal("no shell job_id")
	}

	rt := s.reg.Get("job_send_message")
	if rt == nil {
		t.Fatal("job_send_message not registered")
	}
	_, err = rt.Exec(context.Background(), s.env, map[string]any{"target": shellJobID, "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "target_not_messageable") {
		t.Errorf("want target_not_messageable, got %v", err)
	}
	_, _ = s.reg.Get("job_stop").Exec(context.Background(), s.env, map[string]any{"job_id": shellJobID}) // cleanup
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestJobSendMessageToShellJob -v`. Expected: FAIL (`job_send_message` not registered).

- [ ] **Step 3: Implement** the `job_send_message` handler in `registerJobTools`:

```go
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefJobSendMessage()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			a := sendMessageArgs{
				Target:     stringArg(args, "target"),
				Message:    stringArg(args, "message"),
				OnFinished: stringArg(args, "on_finished"),
			}
			if v, ok := args["background"].(bool); ok {
				a.Background = v
			} else {
				a.Background = true
			}
			if v, ok := args["block_timeout_ms"].(float64); ok {
				a.BlockTimeoutMS = int(v)
			}
			res := s.sendDelegateMessage(ctx, a)
			if res.Err != nil {
				return nil, res.Err
			}
			return marshalSendMessageResult(res), nil
		},
	})
```

`marshalSendMessageResult(res)` builds the spec §5.5 return shapes (running → `{target,job_id,type,status,running_in_background,action:"sent",transcript_ref}`; resumed → `{target,job_id,type,status,running_in_background,action:"resumed",resumed_from_job_id,transcript_ref}`; alias → `{target,delivered,action:"sent",message_type:"runtime"}`):

```go
func marshalSendMessageResult(res sendMessageResult) string {
	if res.MessageType == "runtime" { // alias target
		b, _ := json.Marshal(map[string]any{
			"target":       res.Target,
			"delivered":    res.Delivered,
			"action":       res.Action,
			"message_type": res.MessageType,
		})
		return string(b)
	}
	m := map[string]any{
		"target":                res.Target,
		"job_id":                res.JobID,
		"type":                  "delegate",
		"status":                string(res.Status),
		"running_in_background": res.RunningInBackground,
		"action":                res.Action,
		"transcript_ref":        res.TranscriptRef,
	}
	if res.ResumedFromJobID != "" {
		m["resumed_from_job_id"] = res.ResumedFromJobID
	}
	b, _ := json.Marshal(m)
	return string(b)
}
```

The role gate (concrete `job_id` resume/steer = root-only) is enforced inside `sendDelegateMessage` via `s.depth` (Task 7). `job_send_message` stays **present** for subagents (it is NOT added to the root-only tool-presence set — that set is `{delegate, job_watch}` per §5.1/§13, retargeted in Phase 6); a subagent's call is allowed only for alias targets.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestJobSendMessage -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_tools_jobs.go agent/session_tools_jobs_test.go
git commit -m "feat(agent): job_send_message tool handler"
```

---

## Task 9: delegate cancellation maps to `cancelled` via `job_stop`

**Files:**
- Test: `agent/job_delegate_test.go` (extend) — a background delegate stopped via `job_stop` finalizes `cancelled`
- Modify (only if the test fails for a real reason): `agent/job_delegate.go` (the `signal` cancel path)

Spec §14 requires: "reuse of the child-session runtime (cancel maps to `cancelled`)". Task 5 wired `signal` to set `cancelRequested` + call `sub.cancel()`. The subagent finalize (`subagents.go:636-643`) maps `cancelRequested && context.Canceled` → `SubagentCancelled`, which `finalizeDelegate` maps to `StatusCancelled`. This task proves the whole chain through `job_stop`.

- [ ] **Step 1: Write the failing test** — `agent/job_delegate_test.go`:

```go
func TestDelegateStopMapsToCancelled(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Child loops on bare "done" text (no communicate) so it keeps running until cancelled.
	c.Register(&fakeAdapter{name: "openai"})
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res := s.createDelegate(context.Background(), delegateArgs{Task: "loop", Background: true, BlockTimeoutMS: 120000})
	if res.JobID == "" {
		t.Fatal("no job_id")
	}
	rec, err := s.jobs.stop(res.JobID)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if rec.Status != jobstore.StatusCancelled {
		t.Errorf("status = %q, want cancelled", rec.Status)
	}
}
```

NOTE: `jm.stop` (Phase 2 §5.8) calls `running[jobID].signal()` then awaits/finalizes. For a delegate, `signal()` cancels the run; the bridge goroutine observes the child `done`, maps `cancelRequested→cancelled`, and finalizes. `jm.stop`'s confirmation wait (Phase 2: `block_timeout_ms` default 5000) must see the finalize land (`runningJob.done` closes). If Phase 2's `stop` returns the record after confirming finalize, this passes. If the delegate's two-stage finalize (subagent `done` → bridge → `jm.finalize`) outruns `stop`'s wait, `stop` returns `running/stop_pending` — in which case widen the test's tolerance OR ensure `stop` waits on `runningJob.done`. Verify Phase 2's `stop` semantics before asserting strictly; if `stop` is non-blocking on delegates, assert eventual `cancelled` by polling `jm.list` for up to a few seconds instead.

- [ ] **Step 2: Run test to verify it fails (or passes immediately).** `cd agent && go test ./ -run TestDelegateStopMapsToCancelled -v`. If it passes immediately, the chain already works (Task 5 wired it correctly) — record that and proceed. If it fails, debug the `signal`/finalize mapping (do NOT weaken the cancelled mapping; fix the wiring).

- [ ] **Step 3: Implement / fix** only what the failure reveals (most likely: ensure `finalizeDelegate` maps `SubagentCancelled→StatusCancelled` with reason `stopped_by_parent`, and that `signal` sets `cancelRequested` BEFORE calling `cancel()` so the subagent finalize takes the cancelled branch at `subagents.go:637`).

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestDelegateStopMapsToCancelled -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_delegate.go agent/job_delegate_test.go
git commit -m "test(agent): delegate job_stop maps to cancelled"
```

---

## Task 10: delegate terminal notification carries `transcript_ref`

**Files:**
- Test: `agent/job_delegate_test.go` (extend) — a background delegate that finishes enqueues a `jobNotification` with `JobType="delegate"` and a non-empty `TranscriptRef`
- Modify (if needed): `agent/job_delegate.go` (`finalizeDelegate` must pass `transcript_ref` into `jm.finalize`'s notification) / `agent/jobs.go` (if `jm.finalize` does not already source `TranscriptRef` from the record)

Per §6, the `<job-notification>` for a delegate carries `transcript_ref`. Phase 2's `jobNotification` struct already has a `TranscriptRef` field. The terminal notification payload must be built from the **durable `JobRecord`** (which has `TranscriptRef` from `job_session_assigned`), not the in-memory overlay — this is the §6 "payload from the JobRecord" rule. Confirm `jm.finalize` reads `TranscriptRef` from the record when arming.

- [ ] **Step 1: Write the failing test** — `agent/job_delegate_test.go`:

```go
func TestDelegateNotificationCarriesTranscriptRef(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateWithStructured("fin", map[string]any{"ok": true}) },
		},
	})

	var queued []jobNotification
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxSubagentDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Intercept the manager's enqueue to observe the armed notification.
	s.jobs.enqueue = func(n jobNotification) { queued = append(queued, n) }

	res := s.createDelegate(context.Background(), delegateArgs{Task: "x", Background: true, BlockTimeoutMS: 120000})
	if res.JobID == "" {
		t.Fatal("no job_id")
	}
	// Wait for the bridge to finalize and arm.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(queued) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(queued) != 1 {
		t.Fatalf("expected 1 armed notification, got %d", len(queued))
	}
	n := queued[0]
	if n.JobType != "delegate" || n.TranscriptRef == "" {
		t.Errorf("notification = %+v, want delegate + transcript_ref", n)
	}
}
```

NOTE: `s.jobs.enqueue` is the Phase 2 field name on `jobManager` (`enqueue func(jobNotification)`). Reassigning it after `NewSession` overrides the default `s.enqueueJobNotification` wiring purely for the test's observation. Verify the field name with `grep -n "enqueue " agent/jobs.go`; if Phase 2 named it differently, adjust.

- [ ] **Step 2: Run test to verify it fails (or passes).** `cd agent && go test ./ -run TestDelegateNotificationCarriesTranscriptRef -v`. Expected: FAIL if `jm.finalize` does not populate `TranscriptRef` from the record; PASS if it already does.

- [ ] **Step 3: Implement / fix** only if needed: in `finalizeDelegate`, before calling `jm.finalize`, ensure the record's `TranscriptRef` is set (it is, from `job_session_assigned`), and that `jm.finalize` builds the `jobNotification` with `JobType: string(rec.Type)` and `TranscriptRef: rec.TranscriptRef`. If Phase 2's `finalize` hard-coded an empty `TranscriptRef` (shell jobs have none), extend it to read `rec.TranscriptRef` from the durable record. Do NOT special-case delegate in `finalize` beyond sourcing the field from the record.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestDelegateNotificationCarriesTranscriptRef -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_delegate.go agent/jobs.go agent/job_delegate_test.go
git commit -m "feat(agent): delegate terminal notification carries transcript_ref"
```

---

## Task 11: full-suite green + live smoke

**Files:** none (verification)

- [ ] **Step 1: Grep-verify the `spawnAgent` signature change landed at both call sites** (per the file-state-race rule):

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
grep -n "func (s \*Session) spawnAgent" agent/subagents.go
grep -rn "\.spawnAgent(" agent/ | grep -v "_test.go"
```
Confirm `spawnAgent` has the new `communicateOutputSchema map[string]any` parameter and BOTH the legacy `spawn_agent` handler (`session_tools_subagent.go`) and `createDelegate` (`job_delegate.go`) pass it (legacy passes `nil`).

- [ ] **Step 2: Run the full module test + lint**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && make test && make lint`
Expected: all modules PASS; lint clean (golangci ×4 + `serf-namingcheck`/`internalcheck`/`docscheck`). Fix any fallout. Likely touch points: the `spawnAgent` signature change may ripple to other tests that call it (update them to pass `nil`); the new `delegate`/`job_send_message` tools may appear in tool-count/parity/snapshot tests (update the expected tool inventory to include them — they are additive, alongside the legacy subagent tools).

- [ ] **Step 3: Live smoke** (per `reference_serf_live_run` recipe — build a standalone binary, do NOT touch a running serve):

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go build -o /tmp/serf ./cmd/serf
. "$PWD/.env"
# In a scratch dir, run serf with a real model (e.g. --model oai-work/<model>) and ask it to:
#  1. delegate a small task with background=false → confirm a job_id + completed + output;
#  2. delegate with background=true → confirm a job_id + running, then job_read_output/job_list;
#  3. job_send_message to the finished delegate's job_id → confirm action:"resumed" + a new job_id;
#  4. delegate with a result_schema, e.g. {"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}
#     → confirm structured_result is populated and structured_result_valid:true.
```
Expected: each step returns the spec §5.4/§5.5 shapes; the resumed job preserves the conversation (the second run sees the first run's history).

- [ ] **Step 4: Commit any test/lint fixups**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git status   # review before adding
git add -A
git commit -m "test(job-control): phase 3 delegate + job_send_message suite green"
```

---

## Self-review (run against the spec)

- **Spec coverage:**
  - §5.4 (delegate tool: params, `background` default true, `agent_type` enum, `model`/`reasoning_effort`, `block_timeout_ms`, `result_schema`, return shapes, "never resumes") → Tasks 2 (`DefDelegate`), 4 (live enum), 5 (`createDelegate` foreground/background/timeout), 6 (handler + return shapes).
  - §5.4.1 (delegate result production — read word for word) → Task 1 (preserve raw `args["output"]` past `normalizeNodeOutput` via `setCommunicateStructured`/`communicateResult.structured`/`CommunicateStructured()`); Task 5 (`result_schema` injected via `WithCommunicateOutputSchema`, validated by construction at the call boundary, `structured_result` captured from the child's `CommunicateStructured()`, prose = `communicate.message` kept separate from structured; `structured_result_valid` true-by-construction, no re-validation). The plan does NOT claim the schema constrains `data` — it replaces `output` wholesale (`profile_overrides.go:137`).
  - §5.5 (`job_send_message`: target = job_id or alias; running→`sent`; terminal/resumable→`resumed` + `resumed_from_job_id`; `on_finished="fail"`→`target_terminal`; alias→runtime `message_type:"runtime"`; shell job→`target_not_messageable`; concrete-job_id root-only) → Tasks 2 (`DefJobSendMessage`), 7 (`sendDelegateMessage` + `s.depth` gate), 8 (handler + return shapes). `delegate_session_busy` deferral documented (Task 7).
  - §5.10 (errors create no job record; synchronous) → `createDelegate`/`sendDelegateMessage` return `Err` and the handlers `return nil, res.Err` before any record for the validation/lookup/routing failures.
  - §5.11 (discovery: `agent_type` enum via `DefDelegate(agentTypes)` + the agents prompt section; not spliced into prose) → Tasks 2/4. The roster prose is the existing `available-agents` section (unchanged).
  - §6 (terminal notification automatic, payload from the durable `JobRecord`, carries `transcript_ref`) → Task 10 + the Phase 2 bridge (`jm.finalize` arms; payload sourced from the record).
- **Reuse, not rebuild:** the run loop / `runCancel` / `sub.run` finalize / `communicate` result path are reused from `subagents.go` (the bridge goroutine observes the existing `sub.done`; it does not start a second run). The delegate job is a Phase 2 `runningJob` whose `signal` cancels the child run and whose finalize calls `jm.finalize` — exactly the Phase 2 §self-review contract ("the delegate runtime registers a `runningJob` whose `signal` cancels the child run instead of a process group, and calls the same `jm.finalize`").
- **Legacy untouched:** `spawn_agent`/`resume_agent`/`wait`/`cancel_agent`/`close_agent`/`list_agents`/`subagent_output` defs + `registerSubagentTools` are unchanged (the only edit to `subagents.go` is the additive `spawnAgent` `communicateOutputSchema` parameter; the legacy caller passes `nil`). Phase 6 deletes the legacy surface. `delegate`/`job_send_message` register alongside under `capabilityJobControl`.
- **Dynamic-enum decision (verified):** the profile carries static `DefDelegate(nil)`; `rebuildToolDefsCache` (`session_tools.go:554`) substitutes `DefDelegate(s.delegateAgentTypeNames())` onto the advertised `delegate` def (single named special-case, parallel to the existing `communicate` dynamic-schema handling). The handler validates `agent_type` against `s.pluginAgents` like `spawnAgent` (`subagents.go:172`), so the registry's no-enum compiled schema does not need the enum.
- **Placeholder scan:** every step shows complete Go and an exact run command. The only flagged-for-verification items are Phase 2 API names (`jm.enqueue`, `jm.finalize`, `jm.stop` semantics, `stringArg`, the `runningJob` registration helper) — each carries a `grep`-to-confirm note, because Phase 2 is the prerequisite and its exact symbols must match.
- **Type consistency:** `delegateArgs`/`delegateResult`/`sendMessageArgs`/`sendMessageResult` (Tasks 5/7) are consumed by the handlers (Tasks 6/8) and marshalled by `marshalDelegateResult`/`marshalSendMessageResult`. `communicateResult.structured`/`CommunicateStructured()`/`setCommunicateStructured` (Task 1) are the single capture seam threaded to `finalizeDelegate` (Task 5). `attachDelegateJob` is shared by `createDelegate` (Task 5) and the resume branch (Task 7) — no duplication.
- **Determinism:** delegate tests script the child's LLM via `fakeAdapter.steps` (no real provider) and assert structural outcomes; background/cancel tests use bounded polling, not wall-clock equality.
