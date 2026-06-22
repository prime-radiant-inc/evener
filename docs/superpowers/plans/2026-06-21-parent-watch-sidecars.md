# Parent-Watch Sidecars Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Simplify observer sidecars so a child granted `watch_parent` can watch its parent with `job_watch(source:"parent")` and report back with `communicate(end_turn:true)`.

**Architecture:** Add a non-transitive parent-watch grant to `delegate`, replace model-facing `job_watch.target/send` with watcher-owned `source`, and route watch frames to the session that created the watch. Reuse the existing watch mailbox, provenance, and delegate resume machinery internally, but hide delivery routing from the model-facing API.

**Tech Stack:** Go, Serf agent sessions, jobstore watch events, provenance package, existing Go unit/integration tests, `tools/tool-fluency` live harness.

---

## Preflight

This plan assumes the execution branch already has the `communicate(end_turn:boolean)` contract and the communicate-purpose cleanup. If the branch still exposes `await_reply`, rebase or merge the prerequisite work before starting this plan.

- [ ] **Step 1: Verify communicate prerequisite**

Run:

```bash
rg -n "await_reply|output\\.purpose|delegate_send\\(to=\\\"caller\\\"\\)" agent/internal/tool agent/prompts docs/job-control.md
```

Expected after prerequisites are present: no `await_reply` in model-facing communicate code, no communicate `output.purpose`, and only old docs that this plan will update mention `delegate_send(to="caller")`.

- [ ] **Step 2: Verify clean worktree**

Run:

```bash
git status --short --branch
```

Expected: on a WIP branch for this task, with no unrelated unstaged changes. If the branch is not a WIP branch, create one before editing:

```bash
git switch -c wip/parent-watch-sidecars
```

## File Structure

- Modify `agent/internal/tool/definitions.go`: model-facing schemas and positive tool descriptions for `delegate`, `delegate_send`, and `job_watch`.
- Modify `agent/internal/tool/definitions_test.go`: schema contract tests for `watch_parent`, `source`, no public `send`, and no caller alias in `delegate_send`.
- Modify `agent/session_tools.go`: context key for the `watch_parent` grant.
- Modify `agent/job_delegate.go`: `delegateArgs.WatchParent` and `createDelegate` context plumbing.
- Modify `agent/session_tools_jobs.go`: parse `watch_parent`, parse `job_watch.source`, reject legacy public `target/send`, format `source` in tool results.
- Modify `agent/session_config.go`: spawn fields for parent-watch grant and parent watch installation.
- Modify `agent/subagents.go`: grant `job_watch` to `watch_parent` children without granting `delegate`, and wire parent install/callback seams.
- Modify `agent/job_watch.go`: internal source resolution, implicit watcher delivery, default cross-session event watches, and loop validation.
- Create `agent/job_watch_parent_test.go`: parent-source sidecar behavior and callback tests.
- Modify `agent/job_watch_test.go`: source parsing, descendant concrete-job watch, and legacy-shape rejection tests.
- Modify `agent/job_watch_observer_test.go`: replace observer `delegate_send(to="caller")` expectations with `communicate(end_turn:true)`.
- Modify `agent/subagents_test.go`: tool availability tests for `watch_parent` children.
- Modify `agent/builtin_agents_test.go`: built-in subagent prompt/tool expectations after `delegate_send(to="caller")` is removed from sidecar guidance.
- Modify `agent/prompts/sections/background-jobs.md`: positive sidecar path.
- Modify `docs/job-control.md`: public job-control contract.
- Modify `docs/agentic-testing.md`: scenario testing guidance for watch callbacks.
- Modify `docs/superpowers/specs/2026-06-20-passive-observer-sidecars-design.md`: add supersession note pointing to the new spec.
- Modify `tools/tool-fluency/probes/job_watch.yaml`: live probes for parent-watch sidecars.
- Create `tools/tool-fluency/reports/2026-06-21-parent-watch-sidecars.md`: committed run summary for GPT and Kimi.

## Task 1: Tool Schema Contract

**Files:**
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/internal/tool/definitions_test.go`

- [ ] **Step 1: Write failing schema tests**

Add these tests to `agent/internal/tool/definitions_test.go` near the existing delegate and job_watch schema tests:

```go
func TestDefDelegateHasWatchParent(t *testing.T) {
	def := DefDelegate([]string{"subagent"})
	props := def.Parameters["properties"].(map[string]any)
	watchParent, ok := props["watch_parent"].(map[string]any)
	if !ok {
		t.Fatal("DefDelegate missing watch_parent")
	}
	if got, _ := watchParent["type"].(string); got != "boolean" {
		t.Fatalf("watch_parent type = %q, want boolean", got)
	}
	if !strings.Contains(def.Description, "watch_parent") {
		t.Fatalf("delegate description does not describe watch_parent: %q", def.Description)
	}
}

func TestDefJobWatchUsesSourceAndOmitsSend(t *testing.T) {
	def := DefJobWatch([]string{"assistant.tool", "communicate", "job.notification"})
	props := def.Parameters["properties"].(map[string]any)
	if _, ok := props["source"]; !ok {
		t.Fatal("DefJobWatch missing source")
	}
	if _, ok := props["target"]; ok {
		t.Fatal("DefJobWatch must not expose legacy target")
	}
	if _, ok := props["send"]; ok {
		t.Fatal("DefJobWatch must not expose public send")
	}
	if strings.Contains(def.Description, "send.to") || strings.Contains(def.Description, "target=") {
		t.Fatalf("DefJobWatch description leaks legacy routing shape: %q", def.Description)
	}
}

func TestDefDelegateSendNoCallerAlias(t *testing.T) {
	def := DefDelegateSend()
	if strings.Contains(def.Description, "caller") {
		t.Fatalf("DefDelegateSend description must describe child delegate messaging only: %q", def.Description)
	}
	props := def.Parameters["properties"].(map[string]any)
	to := props["to"].(map[string]any)
	if strings.Contains(fmt.Sprint(to["description"]), "caller") {
		t.Fatalf("delegate_send.to description leaks caller alias: %v", to["description"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./agent/internal/tool -run 'TestDefDelegateHasWatchParent|TestDefJobWatchUsesSourceAndOmitsSend|TestDefDelegateSendNoCallerAlias' -v
```

Expected: FAIL because `watch_parent` is absent, `job_watch` still exposes `target/send`, and `delegate_send` still documents `caller`.

- [ ] **Step 3: Update `DefDelegate`**

In `agent/internal/tool/definitions.go`, add `watch_parent` to the `delegate` properties:

```go
"watch_parent": map[string]any{
	"type":        "boolean",
	"description": "Grant this child permission to observe your session with job_watch(source=\"parent\"). This does not grant delegation or any transitive watch permission.",
},
```

Update the `DefDelegate` description so it positively states:

```text
Set watch_parent=true for an observer sidecar: the child can call job_watch(source="parent") and report findings with communicate(end_turn=true).
```

- [ ] **Step 4: Update `DefDelegateSend`**

Replace the description with child-only wording:

```go
Description: "Send a message to one of your durable delegates by delegate_id. " +
	"`to` accepts a `dlg_...` delegate_id; it rejects job/turn handles and unrelated runtime aliases. " +
	"If the delegate is running or being driven, the message is steered and returns on delivery. " +
	"If the delegate is idle, set `on_idle=\"start\"` to start the next job; the default `on_idle=\"fail\"` rejects idle delegates instead of starting work.",
```

Update the `to` property description:

```go
"to": map[string]any{"type": "string", "description": "A delegate_id (`dlg_...`) owned by this session."},
```

- [ ] **Step 5: Update `DefJobWatch` public shape**

In `DefJobWatch`, replace the description with source-first wording:

```go
desc := "Create, inspect, list, or clear standing triggers on a source you can observe. " +
	"For operation=\"create\", set `source` to `self`, `parent`, or a concrete `job_id`. " +
	"`parent` is available only inside a delegate spawned with `watch_parent=true`. " +
	"Delivery is implicit: matching frames are delivered to the session that created the watch. " +
	"For cross-session session sources such as `parent`, omitting trigger fields watches all bounded public events for that source. " +
	"Use `events` (available: " + kinds + "), `event_filter`, `every`, `output_match`, or `progress_interval_ms` to narrow when useful. " +
	"`event_filter` narrows assistant.tool events by tool_name and ok/error status. " +
	"Observers report findings with `communicate(end_turn=true)`. " +
	"`operation=\"clear\"` removes a watch by `watch_id`."
```

Replace the `target` property with:

```go
"source": map[string]any{
	"type":        "string",
	"description": "`self`, `parent` when granted by delegate(watch_parent=true), or a concrete job_id visible to this session.",
},
```

Remove the public `send` property entirely.

- [ ] **Step 6: Run schema tests**

Run:

```bash
go test ./agent/internal/tool -run 'TestDefDelegate|TestDefJobWatch|TestDefDelegateSend' -v
```

Expected: PASS.

- [ ] **Step 7: Commit schema changes**

Run:

```bash
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git commit -m "feat(agent): simplify sidecar tool schemas

Add delegate.watch_parent as the explicit parent observation grant.
Change job_watch's public contract from target/send routing to source-owned
watching, and narrow delegate_send documentation to child delegate follow-up.

This is the model-facing shape required by the parent-watch sidecar design."
```

## Task 2: Parse `watch_parent` and `job_watch.source`

**Files:**
- Modify: `agent/session_tools.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write failing parser tests**

Add these tests to `agent/session_tools_jobs_test.go` near existing `watchArgsFromToolArgs` tests:

```go
func TestWatchArgsFromToolArgsUsesSource(t *testing.T) {
	got, err := watchArgsFromToolArgs(map[string]any{
		"operation": "create",
		"source":    "parent",
	})
	if err != nil {
		t.Fatalf("watchArgsFromToolArgs returned error: %v", err)
	}
	if got.Source != "parent" {
		t.Fatalf("Source = %q, want parent", got.Source)
	}
	if got.Target != "" {
		t.Fatalf("legacy Target = %q, want empty model-facing parse", got.Target)
	}
}

func TestWatchArgsFromToolArgsRejectsLegacyTargetAndSend(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"target": {
			"operation": "create",
			"target":    "caller",
		},
		"send": {
			"operation": "create",
			"source":    "parent",
			"send":      map[string]any{"to": "dlg_old"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := watchArgsFromToolArgs(args)
			if err == nil {
				t.Fatal("watchArgsFromToolArgs succeeded, want invalid_request")
			}
			if !strings.Contains(err.Error(), "invalid_request") {
				t.Fatalf("error = %v, want invalid_request", err)
			}
		})
	}
}

func TestDelegateToolParsesWatchParent(t *testing.T) {
	args := map[string]any{
		"task":         "observe my work",
		"watch_parent": true,
	}
	parsed := delegateArgs{
		Task:        stringArg(args, "task"),
		WatchParent: shellBoolArg(args, "watch_parent"),
		Background:  true,
	}
	if !parsed.WatchParent {
		t.Fatal("WatchParent = false, want true")
	}
}
```

- [ ] **Step 2: Run parser tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestWatchArgsFromToolArgsUsesSource|TestWatchArgsFromToolArgsRejectsLegacyTargetAndSend|TestDelegateToolParsesWatchParent' -v
```

Expected: FAIL because `watchArgs.Source` and `delegateArgs.WatchParent` do not exist yet.

- [ ] **Step 3: Add grant context and delegate arg**

In `agent/session_tools.go`, add the context key beside `ctxDelegationAllowance`:

```go
// ctxWatchParent carries the non-transitive parent observation grant into child
// session spawn plumbing. createDelegate sets it from delegate(watch_parent=true);
// prepareSubagentRun copies it onto the child's spawnConfig.
const ctxWatchParent ctxKey = "watchParent"
```

In `agent/job_delegate.go`, add the field to `delegateArgs`:

```go
WatchParent bool
```

In `createDelegate`, set the context value only when true:

```go
if args.WatchParent {
	ctx = context.WithValue(ctx, ctxWatchParent, true)
}
```

- [ ] **Step 4: Parse `watch_parent` in `delegateTool`**

In `agent/session_tools_jobs.go`, after parsing `Task`, `AgentType`, `Model`, and `ReasoningEffort`, set:

```go
a.WatchParent = shellBoolArg(args, "watch_parent")
```

- [ ] **Step 5: Add `Source` to watch args and reject legacy fields**

In `agent/job_watch.go`, extend `watchArgs`:

```go
Source string
```

In `watchArgsFromToolArgs`, parse `source` and reject model-facing legacy routing before operation-specific validation:

```go
if _, ok := args["target"]; ok {
	return watchArgs{}, errors.New("invalid_request: job_watch uses source, not target")
}
if _, ok := args["send"]; ok {
	return watchArgs{}, errors.New("invalid_request: job_watch delivers to the watcher automatically; send is not a public argument")
}
a := watchArgs{
	Operation:   operation,
	WatchID:     strings.TrimSpace(stringArg(args, "watch_id")),
	Source:      strings.TrimSpace(stringArg(args, "source")),
	OutputMatch: stringArg(args, "output_match"),
}
```

For `operation:"create"`, require `source`:

```go
if a.Source == "" {
	return watchArgs{}, errors.New("invalid_request: source is required")
}
if strings.HasPrefix(a.Source, "dlg_") {
	return watchArgs{}, errors.New("invalid_request: delegate_id is a conversation handle; watch source self, parent, or a concrete job_id")
}
```

For list operations, reject `source`:

```go
if a.Source != "" || a.WatchID != "" {
	return watchArgs{}, errors.New("invalid_request: list requires no source or watch_id")
}
```

- [ ] **Step 6: Run parser tests**

Run:

```bash
go test ./agent -run 'TestWatchArgsFromToolArgsUsesSource|TestWatchArgsFromToolArgsRejectsLegacyTargetAndSend|TestDelegateToolParsesWatchParent' -v
```

Expected: PASS.

- [ ] **Step 7: Commit parser plumbing**

Run:

```bash
git add agent/session_tools.go agent/job_delegate.go agent/session_tools_jobs.go agent/job_watch.go agent/session_tools_jobs_test.go
git commit -m "feat(agent): parse parent watch grant and watch source

Parse delegate.watch_parent into delegateArgs and carry a spawn context key for
the later child grant. Change job_watch argument parsing to accept source and
reject the old public target/send routing shape immediately."
```

## Task 3: Grant `job_watch` to Parent-Watch Children

**Files:**
- Modify: `agent/session_config.go`
- Modify: `agent/subagents.go`
- Modify: `agent/subagents_test.go`

- [ ] **Step 1: Write failing child tool-surface tests**

Add this test to `agent/subagents_test.go` near the existing root-only tool policy test:

```go
func TestWatchParentChildGetsJobWatchButNotDelegate(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = "parent"
	subCfg.spawn.parentWatchGranted = true

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	if child.reg.Get("job_watch") == nil {
		t.Fatal("watch_parent child must have job_watch registered")
	}
	if !hasCachedCallableToolDefinition(child, "job_watch") {
		t.Fatal("watch_parent child must advertise job_watch")
	}
	if child.reg.Get("delegate") != nil {
		t.Fatal("watch_parent child must not get delegate without delegation_allowance")
	}
	if hasCachedCallableToolDefinition(child, "delegate") {
		t.Fatal("watch_parent child must not advertise delegate without delegation_allowance")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./agent -run TestWatchParentChildGetsJobWatchButNotDelegate -v
```

Expected: FAIL because `parentWatchGranted` does not exist.

- [ ] **Step 3: Add spawn grant fields**

In `agent/session_config.go`, add these fields to `spawnConfig`:

```go
// parentWatchGranted allows this child to install watches on its immediate
// parent through parentInstallWatch. It is non-transitive and does not grant
// delegate.
parentWatchGranted bool

// parentInstallWatch installs a source:"parent" watch on the live parent,
// owned by this child as watcher/receiver.
parentInstallWatch func(observerSessionID string, observerDelegateID string, args watchArgs) (watchResult, error)
```

- [ ] **Step 4: Copy the grant in `prepareSubagentRun`**

In `agent/subagents.go`, after `ctxDelegationAllowance` is handled, copy `ctxWatchParent`:

```go
if watchParent, ok := ctx.Value(ctxWatchParent).(bool); ok && watchParent {
	subCfg.spawn.parentWatchGranted = true
	subCfg.spawn.parentInstallWatch = s.installParentSourceWatchForChild
}
```

The method `installParentSourceWatchForChild` is added in Task 5. For this task, add a stub that returns a clear error so the code compiles:

```go
func (s *Session) installParentSourceWatchForChild(observerSessionID string, observerDelegateID string, args watchArgs) (watchResult, error) {
	return watchResult{}, errors.New("parent watch installation is not wired")
}
```

Ensure `agent/subagents.go` imports `errors`; run `gofmt` after the edit so the
import block is canonical.

- [ ] **Step 5: Grant only `job_watch` on the child tool surface**

In `prepareSubagentRun`, after `baseSubagentToolPolicy` returns and before `grant_tools` validation, shape the policy:

```go
if subCfg.spawn.parentWatchGranted && !allTools {
	if len(allowedTools) > 0 {
		allowedTools = appendUniqueStrings(allowedTools, "job_watch")
	} else {
		deniedTools = removeStrings(deniedTools, []string{"job_watch"})
	}
}
```

Do not remove `delegate` from the deny list in this branch.

- [ ] **Step 6: Run child tool-surface test**

Run:

```bash
go test ./agent -run TestWatchParentChildGetsJobWatchButNotDelegate -v
```

Expected: PASS.

- [ ] **Step 7: Commit child grant plumbing**

Run:

```bash
git add agent/session_config.go agent/subagents.go agent/subagents_test.go
git commit -m "feat(agent): grant job_watch to parent-watch children

Carry delegate.watch_parent into spawnConfig and use it to expose job_watch to
leaf observer children without granting delegate or any transitive management
surface."
```

## Task 4: Source Resolution and Result Formatting

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_watch_test.go`

- [ ] **Step 1: Write failing source-resolution tests**

Add these tests to `agent/job_watch_test.go` near existing create/list/inspect tests:

```go
func TestJobWatchCreateSelfSourceFormatsSource(t *testing.T) {
	sess := newTestSession(t)
	res, err := jobWatchTool(sess, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"job.notification"},
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool returned error: %v", err)
	}
	state := res.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != "self" {
		t.Fatalf("Source = %q, want self", state.Source)
	}
}

func TestJobWatchSelfSourceKeepsLoopGuard(t *testing.T) {
	sess := newTestSession(t)
	_, err := jobWatchTool(sess, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"assistant.tool"},
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("jobWatchTool succeeded, want loop guard error")
	}
	if !strings.Contains(err.Error(), "feedback loop") {
		t.Fatalf("error = %v, want feedback loop", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestJobWatchCreateSelfSourceFormatsSource|TestJobWatchSelfSourceKeepsLoopGuard' -v
```

Expected: FAIL because tool results still expose `target` and source resolution is not implemented.

- [ ] **Step 3: Add source identity helpers**

In `agent/job_watch.go`, add:

```go
type watchSourceKind int

const (
	watchSourceConcreteJob watchSourceKind = iota
	watchSourceSelfSession
	watchSourceParentSession
)

type watchSource struct {
	Kind     watchSourceKind
	Public   string
	Internal string
}

func normalizeWatchSource(source string) (watchSource, error) {
	source = strings.TrimSpace(source)
	switch source {
	case "":
		return watchSource{}, errors.New("invalid_request: source is required")
	case "self":
		return watchSource{Kind: watchSourceSelfSession, Public: "self", Internal: runtimeMessageAliasCaller}, nil
	case "parent":
		return watchSource{Kind: watchSourceParentSession, Public: "parent", Internal: runtimeMessageAliasCaller}, nil
	default:
		if strings.HasPrefix(source, "job_") {
			return watchSource{Kind: watchSourceConcreteJob, Public: source, Internal: source}, nil
		}
		return watchSource{}, fmt.Errorf("source_not_watchable: %q is not self, parent, or a concrete job_id", source)
	}
}
```

The internal value for `parent` is interpreted only by the parent-install path in Task 5. It must not be used to mean "caller" inside the child job manager.

- [ ] **Step 4: Map public source to internal target for local watches**

In `jobWatchTool`, before calling `jm.configureWatch(a)`, resolve:

```go
source, err := normalizeWatchSource(a.Source)
if err != nil {
	return "", err
}
a.Source = source.Public
a.Target = source.Internal
```

For `source.Kind == watchSourceParentSession`, branch to the parent-install path added in Task 5. For this task, return:

```go
return "", errors.New("source_not_watchable: parent source requires a parent-watch grant")
```

- [ ] **Step 5: Rename public watch results to source**

In `agent/session_tools_jobs.go`, change `jobWatchToolResult`:

```go
Source string `json:"source"`
```

Remove the exported public `Target string` field from `jobWatchToolResult`. Keep
internal target data only on `watchConfig` and `watchResult` fields that do not
serialize as public tool state.

In `marshalWatchResult`, set:

```go
Source: res.Source,
```

In `watchResult` in `agent/job_watch.go`, add:

```go
Source string
```

In `watchResultFromConfig`, carry `cfg.sourcePublic` after adding that field in `watchConfig`:

```go
Source: cfg.sourcePublic,
```

Add `sourcePublic string` to `watchConfig`, set it in `newWatchConfig` from `a.Source`, and keep `target` for internal matching.

- [ ] **Step 6: Preserve self-source loop validation**

Update `validateWatchDeliveryLoop` so it rejects self-source delivery only when watcher and source are the same session. At this task's local-only stage, that means the existing rejection still applies for `source:"self"`:

```go
selfDelivery := cfg.receiverSessionID == "" || cfg.receiverSessionID == jm.sessionID
```

Task 5 adds `receiverSessionID` for cross-session parent watches.

- [ ] **Step 7: Run source-resolution tests**

Run:

```bash
go test ./agent -run 'TestJobWatchCreateSelfSourceFormatsSource|TestJobWatchSelfSourceKeepsLoopGuard' -v
```

Expected: PASS.

- [ ] **Step 8: Commit source resolution**

Run:

```bash
git add agent/job_watch.go agent/session_tools_jobs.go agent/job_watch_test.go
git commit -m "feat(agent): make job_watch source the public watch identity

Resolve public source values into internal watch targets while keeping public
results source-shaped. Preserve the existing self-delivery loop guard for
self-source event watches."
```

## Task 5: Install `source:"parent"` Watches From Children

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/subagents.go`
- Create: `agent/job_watch_parent_test.go`

- [ ] **Step 1: Write failing parent-source authorization tests**

Create `agent/job_watch_parent_test.go`:

```go
package agent

import (
	"context"
	"strings"
	"testing"
)

func TestJobWatchParentSourceRequiresGrant(t *testing.T) {
	parent := newTestSession(t)
	childCfg := parent.cfg
	childCfg.spawn.parentSessionID = parent.ID()
	childCfg.spawn.depth = parent.depth + 1
	child, err := NewSession(parent.client, parent.currentProfile(), parent.env, childCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	_, err = jobWatchTool(child, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("jobWatchTool succeeded, want source_not_watchable")
	}
	if !strings.Contains(err.Error(), "watch_parent") {
		t.Fatalf("error = %v, want watch_parent guidance", err)
	}
}

func TestJobWatchParentSourceInstallsOnParentWithChildReceiver(t *testing.T) {
	parent := newTestSession(t)
	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:         "observe parent",
		WatchParent:  true,
		Background:   false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	delegates, err := parent.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	delegate := delegates[res.DelegateID]
	if delegate == nil || delegate.ChildSessionID == "" {
		t.Fatalf("delegate record for %s = %+v, want child session id", res.DelegateID, delegate)
	}
	sub := parent.subagents.get(delegate.ChildSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing child session for %s", delegate.ChildSessionID)
	}

	out, err := jobWatchTool(sub.sess, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool: %v", err)
	}
	state := out.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != "parent" || !state.Watching {
		t.Fatalf("watch state = %+v, want source parent watching", state)
	}
	if len(parent.jobManager.watches) != 1 {
		t.Fatalf("parent watch count = %d, want 1", len(parent.jobManager.watches))
	}
	for _, cfg := range parent.jobManager.watches {
		if cfg.receiverSessionID != sub.sess.ID() {
			t.Fatalf("receiverSessionID = %q, want child %q", cfg.receiverSessionID, sub.sess.ID())
		}
	}
}
```

- [ ] **Step 2: Run parent-source tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestJobWatchParentSourceRequiresGrant|TestJobWatchParentSourceInstallsOnParentWithChildReceiver' -v
```

Expected: FAIL because parent source installation is not wired.

- [ ] **Step 3: Add receiver fields to watch config**

In `agent/job_watch.go`, add to `watchConfig`:

```go
sourcePublic      string
receiverSessionID string
receiverDelegateID string
```

In `watchArgs`, add:

```go
ReceiverSessionID  string
ReceiverDelegateID string
```

In `newWatchConfig`, copy these fields:

```go
sourcePublic:       a.Source,
receiverSessionID:  strings.TrimSpace(a.ReceiverSessionID),
receiverDelegateID: strings.TrimSpace(a.ReceiverDelegateID),
```

- [ ] **Step 4: Implement child-to-parent install branch**

In `jobWatchTool`, after normalizing source:

```go
if source.Kind == watchSourceParentSession {
	if !s.cfg.spawn.parentWatchGranted || s.cfg.spawn.parentInstallWatch == nil {
		return "", errors.New("source_not_watchable: source parent requires delegate(watch_parent=true)")
	}
	a.Source = "parent"
	a.Target = runtimeMessageAliasCaller
	res, err := s.cfg.spawn.parentInstallWatch(s.ID(), s.cfg.spawn.parentDelegateID, a)
	if err != nil {
		return "", err
	}
	return marshalWatchResult(res, maxChars)
}
```

Add this field to `spawnConfig` so parent-watch installs can route frames to the
delegate conversation that owns the child session:

```go
parentDelegateID string
```

Set it in `prepareSubagentRun` immediately after the delegate id is created and before `NewSession` is called. Use the delegate id already generated in `createDelegate`; pass it into `prepareSubagentRun` through a context key:

```go
const ctxParentDelegateID ctxKey = "parentDelegateID"
```

In `createDelegate`, before calling `prepareSubagentRun`:

```go
ctx = context.WithValue(ctx, ctxParentDelegateID, delegateID)
```

In `prepareSubagentRun`:

```go
if delegateID, ok := ctx.Value(ctxParentDelegateID).(string); ok {
	subCfg.spawn.parentDelegateID = delegateID
}
```

- [ ] **Step 5: Implement parent install method**

Replace the Task 3 stub in `agent/subagents.go`:

```go
func (s *Session) installParentSourceWatchForChild(observerSessionID string, observerDelegateID string, args watchArgs) (watchResult, error) {
	if strings.TrimSpace(observerSessionID) == "" {
		return watchResult{}, errors.New("source_not_watchable: parent watch observer session is unknown")
	}
	a := args
	a.Source = "parent"
	a.Target = runtimeMessageAliasCaller
	a.ReceiverSessionID = observerSessionID
	a.ReceiverDelegateID = observerDelegateID
	if !watchArgsHasCondition(a) {
		a.Events = []string{"*"}
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		return watchResult{}, err
	}
	return jm.configureWatch(a)
}
```

This method runs on the parent session, so `runtimeMessageAliasCaller` resolves to the parent event stream.

- [ ] **Step 6: Make default cross-session watches valid**

In `watchArgsHasCondition`, treat cross-session receiver watches as valid when they have no explicit trigger:

```go
if a.ReceiverSessionID != "" && a.ReceiverSessionID != a.Target && isWatchSessionTarget(a.Target) {
	return true
}
```

The parent install method in Step 5 sets `Events:["*"]`, so this condition is mainly defensive and keeps validation readable.

- [ ] **Step 7: Update loop validation for cross-session receiver**

In `validateWatchDeliveryLoop`, use receiver identity:

```go
receiver := cfg.receiverSessionID
if receiver == "" {
	receiver = cfg.visibleSessionID
}
selfDelivery := receiver == cfg.visibleSessionID
```

Keep the existing self-generated event-kind check after that.

- [ ] **Step 8: Run parent-source tests**

Run:

```bash
go test ./agent -run 'TestJobWatchParentSourceRequiresGrant|TestJobWatchParentSourceInstallsOnParentWithChildReceiver' -v
```

Expected: PASS.

- [ ] **Step 9: Commit parent-source install**

Run:

```bash
git add agent/job_watch.go agent/session_tools_jobs.go agent/session_config.go agent/session_tools.go agent/job_delegate.go agent/subagents.go agent/job_watch_parent_test.go
git commit -m "feat(agent): install parent-source watches from observer children

Wire delegate.watch_parent into a non-transitive parent watch install seam.
Parent-source watches are stored on the parent event stream but owned by the
child watcher, with broad public events as the default trigger."
```

## Task 6: Deliver Watch Frames to the Watcher and Callback With `communicate`

**Files:**
- Modify: `agent/job_watch.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/subagents.go`
- Modify: `agent/session_tools_communicate.go`
- Modify: `agent/job_watch_parent_test.go`
- Modify: `agent/job_watch_observer_test.go`

- [ ] **Step 1: Write failing delivery and callback tests**

Add to `agent/job_watch_parent_test.go`:

```go
func TestParentSourceWatchFrameDeliveredToChildWatcher(t *testing.T) {
	parent, child, delegateID := newParentWatchChildFixture(t)

	_, err := jobWatchTool(child, map[string]any{
		"operation":    "create",
		"source":       "parent",
		"events":       []any{"assistant.tool"},
		"event_filter": map[string]any{"tool_name": "read_file", "status": "ok"},
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool: %v", err)
	}

	parent.emit(events.EventToolCallEnd, events.ToolCallEndData{
		Name:   "read_file",
		OK:     true,
		CallID: "call_read_file",
	})

	sends := parent.jobManager.pendingWatchSendDeliveries(nil)
	if len(sends) != 1 {
		t.Fatalf("pending sends = %d, want 1", len(sends))
	}
	if sends[0].state.Key.SendTo != delegateID {
		t.Fatalf("delivery target = %q, want delegate %q", sends[0].state.Key.SendTo, delegateID)
	}
	if !strings.Contains(sends[0].state.Frame, "read_file") {
		t.Fatalf("frame = %q, want read_file event", sends[0].state.Frame)
	}
}

func TestWatchOriginCommunicateEndTurnResumesParentOnce(t *testing.T) {
	parent, child, _ := newParentWatchChildFixture(t)
	var steered []string
	parent.cfg.spawn.parentSteer = func(msg string, p *provenance.Causal) {
		steered = append(steered, msg)
	}

	child.cfg.spawn.parentSteer = parent.SteerWithProvenance
	child.cfg.spawn.parentSteerDelivered = parent.trySteerWithProvenanceAndNotify

	rt := child.reg.Get(child.resultToolName())
	if rt == nil {
		t.Fatal("child missing communicate tool")
	}
	_, err := rt.Exec(context.Background(), child.env, map[string]any{
		"message":  "WATCH_OBSERVED read_file succeeded",
		"end_turn": true,
		"output": map[string]any{
			"message":   "WATCH_OBSERVED",
			"data":      map[string]any{"tool": "read_file"},
			"artifacts": []any{},
		},
	})
	if err != nil {
		t.Fatalf("record communicate: %v", err)
	}
	if len(steered) != 1 {
		t.Fatalf("parent steers = %d, want one callback", len(steered))
	}
	if !strings.Contains(steered[0], "WATCH_OBSERVED") {
		t.Fatalf("callback = %q, want WATCH_OBSERVED", steered[0])
	}
}

func newParentWatchChildFixture(t *testing.T) (*Session, *Session, string) {
	t.Helper()
	parent := newTestSession(t)
	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "observe parent",
		WatchParent:    true,
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	delegates, err := parent.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	delegate := delegates[res.DelegateID]
	if delegate == nil || delegate.ChildSessionID == "" {
		t.Fatalf("delegate record for %s = %+v, want child session id", res.DelegateID, delegate)
	}
	sub := parent.subagents.get(delegate.ChildSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing child session for %s", delegate.ChildSessionID)
	}
	return parent, sub.sess, res.DelegateID
}
```

- [ ] **Step 2: Run delivery tests to verify they fail**

Run:

```bash
go test ./agent -run 'TestParentSourceWatchFrameDeliveredToChildWatcher|TestWatchOriginCommunicateEndTurnResumesParentOnce' -v
```

Expected: FAIL because receiver delivery still depends on public `send` and communicate does not yet act as the observer callback for watch-originated turns.

- [ ] **Step 3: Internally map receiver delivery to delegate send state**

In `newWatchConfig`, if `a.ReceiverDelegateID` is set and `a.Send` is nil, create internal send args:

```go
if a.Send == nil && a.ReceiverDelegateID != "" {
	a.Send = &watchSendArgs{To: a.ReceiverDelegateID}
}
```

Keep this internal. The parser still rejects public `send`.

Update `watchResultFromConfig` and public formatting to omit `Send` even though `cfg.send` is used internally.

- [ ] **Step 4: Mint read grants for internal receiver sends**

Keep `mintWatchCreateReadGrant` behavior for concrete job targets by using `cfg.receiverSessionID` as the observer identity when present. The grant key must be observer-session based, not public-send based:

```go
observerSessionID := cfg.receiverSessionID
if observerSessionID == "" {
	observerSessionID = jm.observerSessionIDForSend(cfg.send)
}
```

Add this helper next to the existing watch-send grant helpers and use it from
`mintWatchCreateReadGrant`:

```go
func (jm *jobManager) observerSessionIDForSend(send *watchSendArgs) string {
	if send == nil || strings.TrimSpace(send.To) == "" {
		return ""
	}
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return ""
	}
	delegate := delegates[strings.TrimSpace(send.To)]
	if delegate == nil {
		return ""
	}
	return delegate.ChildSessionID
}
```

- [ ] **Step 5: Route watch-origin terminal communicate upward**

In the communicate handling path, after a valid `communicate(end_turn:true)` from an `EntryWatchDelivery` run, deliver the visible message to the parent through the existing `parentSteerDelivered` callback with the current watch provenance.

Use this shape:

```go
if s.currentEntryKind() == EntryWatchDelivery && endTurn && s.cfg.spawn.parentSteerDelivered != nil {
	if s.cfg.spawn.parentSteerDelivered(message, s.currentInputProvenance()) {
		s.markWatchCallbackDeliveredForCurrentRun()
	}
}
```

Add active-entry tracking fields to `Session`:

```go
activeEntryKind        EntryKind
activeInputProvenance *provenance.Causal
```

Store those values at the start of `processInputKindWithProvenance` and clear
them when the turn finishes:

```go
s.activeEntryKind = kind
s.activeInputProvenance = provenance.Clone(inputProvenance)
defer func() {
	s.activeEntryKind = EntryUserInput
	s.activeInputProvenance = nil
}()
```

Protect those fields with `s.mu`.

- [ ] **Step 6: Suppress duplicate terminal notification**

When Step 5 marks the watch callback delivered, record it on the subagent run so `armFinalizedJob` or the delegate finalization path does not inject a second owner notification for the same watch-origin activation.

Use a boolean on `runningJob` or `subagent`:

```go
watchCallbackDelivered atomic.Bool
```

Set it when `communicate(end_turn:true)` delivers the parent callback. Check it before appending the ordinary terminal owner notification for a watch-origin delegate run.

- [ ] **Step 7: Run delivery tests**

Run:

```bash
go test ./agent -run 'TestParentSourceWatchFrameDeliveredToChildWatcher|TestWatchOriginCommunicateEndTurnResumesParentOnce|TestWatchOriginatedDelegateCanFinishWithNoToolBareText' -v
```

Expected: PASS.

- [ ] **Step 8: Commit delivery and callback behavior**

Run:

```bash
git add agent/job_watch.go agent/job_delegate.go agent/subagents.go agent/session_tools_communicate.go agent/job_watch_parent_test.go agent/job_watch_observer_test.go
git commit -m "feat(agent): deliver parent watch frames to watcher callbacks

Use internal watch delivery state to route source:parent frames to the child
that created the watch. Treat watch-origin communicate(end_turn:true) as the
observer callback to the parent and suppress duplicate terminal notifications."
```

## Task 7: Allow Parent Watches on Descendant Concrete Jobs

**Files:**
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_watch.go`
- Modify: `agent/job_watch_test.go`

- [ ] **Step 1: Write failing descendant watch test**

Add to `agent/job_watch_test.go` near nested-job watch tests:

```go
func TestJobWatchAllowsDescendantConcreteJobSource(t *testing.T) {
	parent := newTestSession(t)
	childRes := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "run child job",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if childRes.Err != nil {
		t.Fatalf("createDelegate: %v", childRes.Err)
	}
	delegates, err := parent.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	delegate := delegates[childRes.DelegateID]
	if delegate == nil || delegate.ChildSessionID == "" {
		t.Fatalf("delegate record for %s = %+v, want child session id", childRes.DelegateID, delegate)
	}
	sub := parent.subagents.get(delegate.ChildSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing child session for %s", delegate.ChildSessionID)
	}

	rec, err := sub.sess.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, sub.sess.jobManager, rec.JobID) })
	out, err := jobWatchTool(parent, map[string]any{
		"operation":    "create",
		"source":       rec.JobID,
		"output_match": "READY",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool: %v", err)
	}
	state := out.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != rec.JobID || !state.Watching {
		t.Fatalf("watch state = %+v, want descendant concrete source watching", state)
	}
}
```

- [ ] **Step 2: Run descendant watch test to verify it fails**

Run:

```bash
go test ./agent -run TestJobWatchAllowsDescendantConcreteJobSource -v
```

Expected: FAIL with the current "delegate the watching to session" guidance.

- [ ] **Step 3: Forward concrete descendant watch installs to the owner**

In `jobWatchTool`, when local `jm.configureWatch(a)` returns `errWatchTargetNotFound`, use the existing descendant resolver:

```go
if errors.Is(err, errWatchTargetNotFound) {
	if ownerJM, ownerSess, _, _, ok := s.resolveDescendantJobOwner(a.Target); ok && ownerJM != nil && ownerSess != nil {
		childArgs := a
		childArgs.Source = a.Source
		childArgs.Target = a.Target
		childArgs.ReceiverSessionID = s.ID()
		childArgs.ReceiverDelegateID = ""
		return marshalWatchResultFromOwner(ownerJM.configureWatch(childArgs), maxChars)
	}
}
```

Do not grant parent-source access through this path. It is only for concrete descendant jobs that are already visible to the ancestor.

Implement `marshalWatchResultFromOwner` as ordinary error handling without hiding errors:

```go
func marshalWatchResultFromOwner(res watchResult, err error, maxChars int) (any, error) {
	if err != nil {
		return "", err
	}
	return marshalWatchResult(res, maxChars)
}
```

- [ ] **Step 4: Ensure receiver delivery can notify the ancestor**

For descendant concrete-job watches where `ReceiverSessionID` is the ancestor and no delegate id is set, delivery should use the existing watch notification path back to the watcher session. Do not synthesize a delegate send. The output-match case can notify the ancestor with a normal watch notification because the watcher is a human/root session, not an observer child.

In `watchNotificationFromWatch`, set the visible session id from `cfg.receiverSessionID` when present:

```go
visibleSessionID := cfg.receiverSessionID
if visibleSessionID == "" {
	visibleSessionID = jm.sessionID
}
```

- [ ] **Step 5: Run descendant watch test**

Run:

```bash
go test ./agent -run TestJobWatchAllowsDescendantConcreteJobSource -v
```

Expected: PASS.

- [ ] **Step 6: Commit descendant watch support**

Run:

```bash
git add agent/session_tools_jobs.go agent/job_watch.go agent/job_watch_test.go
git commit -m "feat(agent): allow ancestor watches on descendant jobs

Permit a session to install concrete job watches on visible descendant jobs by
forwarding the install to the owning job manager and delivering notifications
back to the watcher."
```

## Task 8: Remove Public Caller Alias From `delegate_send`

**Files:**
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_jobs.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/builtin_agents_test.go`

- [ ] **Step 1: Write failing caller-alias rejection test**

Add to `agent/job_delegate_test.go` near runtime alias tests:

```go
func TestDelegateSendRejectsCallerAliasPublicly(t *testing.T) {
	sess := newTestSession(t)
	_, err := delegateSendTool(context.Background(), sess, map[string]any{
		"to":      "caller",
		"message": "old observer callback",
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("delegate_send(to=caller) succeeded, want invalid_request")
	}
	if !strings.Contains(err.Error(), "delegate_id") {
		t.Fatalf("error = %v, want delegate_id guidance", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./agent -run TestDelegateSendRejectsCallerAliasPublicly -v
```

Expected: FAIL because the caller alias still succeeds in contextual cases or is still documented as valid.

- [ ] **Step 3: Reject caller alias in public delegate_send parsing**

In `delegateSendTool`, reject `to:"caller"` before calling `sendDelegateMessage`:

```go
if strings.TrimSpace(target) == runtimeMessageAliasCaller {
	return "", errors.New("invalid_request: delegate_send sends to child delegate_id only; observer callbacks use communicate(end_turn=true)")
}
```

Keep any internal parent steering callback path private to watch-origin `communicate`.

- [ ] **Step 4: Update built-in agent expectations**

In `agent/builtin_agents_test.go`, update tests that expect the built-in subagent to have caller callback guidance. The new assertion should be:

```go
if !strings.Contains(subagentPrompt, "communicate") || !strings.Contains(subagentPrompt, "end_turn") {
	t.Fatalf("subagent prompt must teach communicate end_turn reporting: %q", subagentPrompt)
}
if strings.Contains(subagentPrompt, "delegate_send(to=\"caller\")") {
	t.Fatalf("subagent prompt leaks old observer callback guidance: %q", subagentPrompt)
}
```

- [ ] **Step 5: Run delegate-send tests**

Run:

```bash
go test ./agent -run 'TestDelegateSendRejectsCallerAliasPublicly|TestBuiltinAgents|TestSpawnAgent_BuiltinSubagent' -v
```

Expected: PASS after prompt expectation updates.

- [ ] **Step 6: Commit caller-alias removal**

Run:

```bash
git add agent/job_delegate.go agent/session_tools_jobs.go agent/job_delegate_test.go agent/builtin_agents_test.go
git commit -m "feat(agent): remove public caller alias from delegate_send

Keep delegate_send focused on parent-to-child delegate follow-up. Observer
callbacks now use communicate(end_turn=true), so the public caller alias is
rejected with direct guidance."
```

## Task 9: Prompt and Documentation Cleanup

**Files:**
- Modify: `agent/prompts/sections/background-jobs.md`
- Modify: `docs/job-control.md`
- Modify: `docs/agentic-testing.md`
- Modify: `docs/superpowers/specs/2026-06-20-passive-observer-sidecars-design.md`
- Modify: `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md`

- [ ] **Step 1: Update prompts with positive sidecar path**

In `agent/prompts/sections/background-jobs.md`, replace old observer callback guidance with:

```markdown
For an observer sidecar, the parent starts a delegate with `watch_parent:true`.
Inside that child, create the watch with `job_watch(source="parent")`.
The watch delivers frames to you. Report status with `communicate(end_turn=false)`
when work continues, and report the observer result with
`communicate(end_turn=true)` when the callback is complete.
Use `delegate_send(to=<delegate_id>)` only for sending a message to your own
child delegate.
```

- [ ] **Step 2: Update `docs/job-control.md`**

Replace the observer-sidecar sections with the public examples:

````markdown
Parent:

```json
{
  "task": "Watch my work and report WATCH_OBSERVED when a successful read_file call sees watch-trigger.txt.",
  "watch_parent": true
}
```

Observer:

```json
{
  "operation": "create",
  "source": "parent",
  "events": ["assistant.tool"],
  "event_filter": {
    "tool_name": "read_file",
    "status": "ok"
  }
}
```

Observer result:

```json
{
  "message": "WATCH_OBSERVED read_file succeeded for watch-trigger.txt.",
  "end_turn": true,
  "output": {
    "message": "WATCH_OBSERVED",
    "data": {
      "tool": "read_file"
    },
    "artifacts": []
  }
}
```
````

Also update tables so `delegate_send` says "follow up with a child delegate" and `job_watch` says "source observed by this watcher."

- [ ] **Step 3: Add supersession notes to older specs**

At the top of `docs/superpowers/specs/2026-06-20-passive-observer-sidecars-design.md`, add:

```markdown
> Superseded where conflicting by `docs/superpowers/specs/2026-06-21-parent-watch-sidecars-design.md`.
> The older `job_watch(send.to=...)` and `delegate_send(to="caller")` sidecar
> shape is no longer the model-facing contract.
```

At the top of `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md`, add the same supersession note.

- [ ] **Step 4: Run documentation grep**

Run:

```bash
rg -n "send\\.to|target=\\\"caller\\\"|delegate_send\\(to=\\\"caller\\\"\\)|await_reply" docs agent/prompts tools/tool-fluency
```

Expected: old strings appear only in historical superseded context or tests that intentionally assert rejection. There must be no positive guidance teaching the old sidecar flow.

- [ ] **Step 5: Commit docs and prompts**

Run:

```bash
git add agent/prompts/sections/background-jobs.md docs/job-control.md docs/agentic-testing.md docs/superpowers/specs/2026-06-20-passive-observer-sidecars-design.md docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md
git commit -m "docs: teach parent-watch sidecar flow

Replace the old send.to plus delegate_send caller callback guidance with the
positive path: delegate(watch_parent), job_watch(source:parent), and
communicate(end_turn:true). Mark older observer specs as superseded where they
conflict."
```

## Task 10: Tool Fluency Probes

**Files:**
- Modify: `tools/tool-fluency/probes/job_watch.yaml`
- Create: `tools/tool-fluency/reports/2026-06-21-parent-watch-sidecars.md`

- [ ] **Step 1: Add parent-watch probes**

Add these probe cases to `tools/tool-fluency/probes/job_watch.yaml`:

```yaml
- id: job_watch.parent_sidecar_default_source
  tool: job_watch
  contexts: [root]
  harness: live
  intent: "Create a sidecar that watches the parent and reports through communicate."
  prompt: |
    Start an observer sidecar. It should watch your parent session with the default parent watch and report PARENT_WATCH_READY when the watch is installed. Then create docs/watch-trigger.txt with TOKEN=PARENT_WATCH_DEFAULT and read it.
  expect:
    calls:
      - tool: delegate
        args:
          watch_parent: true
      - tool: job_watch
        args:
          source: parent
      - tool: communicate
        args:
          end_turn: true
    forbidden_calls:
      - delegate_send
      - read_session_transcript
    final_contains: PARENT_WATCH
  metrics:
    max_validation_errors: 0
    max_unnecessary_calls: 2

- id: job_watch.parent_sidecar_filtered_tool
  tool: job_watch
  contexts: [root]
  harness: live
  intent: "Create a filtered parent observer for successful read_file events."
  prompt: |
    Start an observer sidecar. It should watch your parent session and report WATCH_OBSERVED only after a successful read_file call reads docs/watch-trigger.txt. Create that file with TOKEN=FILTERED_PARENT_WATCH, read it, and finish from the observer callback.
  expect:
    calls:
      - tool: delegate
        args:
          watch_parent: true
      - tool: job_watch
        args:
          source: parent
          events:
            - assistant.tool
          event_filter:
            tool_name: read_file
            status: ok
      - tool: communicate
        args:
          end_turn: true
    forbidden_calls:
      - delegate_send
      - job_list
      - read_session_transcript
    final_contains: WATCH_OBSERVED
  metrics:
    max_validation_errors: 0
    max_unnecessary_calls: 2
```

Adjust YAML field names only to match the existing probe manifest schema. Do not turn these into markdown scenario cards.

- [ ] **Step 2: Run focused fluency unit tests**

Run:

```bash
go test ./tools/tool-fluency/cmd/serf-fluency -v
```

Expected: PASS.

- [ ] **Step 3: Run GPT live probes**

Run:

```bash
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --build \
  --harness live \
  --model openai/gpt-5.4-mini \
  --fast-cheap-model openai/gpt-5.4-mini \
  --clear-openai-api-key \
  --probe job_watch.parent_sidecar_default_source \
  --post-turn-wait 45s \
  --out /tmp/serf-fluency-parent-watch-gpt-default

go run ./tools/tool-fluency/cmd/serf-fluency run \
  --harness live \
  --model openai/gpt-5.4-mini \
  --fast-cheap-model openai/gpt-5.4-mini \
  --clear-openai-api-key \
  --probe job_watch.parent_sidecar_filtered_tool \
  --post-turn-wait 45s \
  --out /tmp/serf-fluency-parent-watch-gpt-filtered
```

Expected: both probes pass with no `delegate_send` calls and no polling after the watched event.

- [ ] **Step 4: Run Kimi live probes**

Run:

```bash
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --harness live \
  --model moonshot/kimi-for-coding \
  --probe job_watch.parent_sidecar_default_source \
  --post-turn-wait 45s \
  --out /tmp/serf-fluency-parent-watch-kimi-default

go run ./tools/tool-fluency/cmd/serf-fluency run \
  --harness live \
  --model moonshot/kimi-for-coding \
  --probe job_watch.parent_sidecar_filtered_tool \
  --post-turn-wait 45s \
  --out /tmp/serf-fluency-parent-watch-kimi-filtered
```

Expected: both probes pass with no public `send`, no `delegate_send(to="caller")`, and observer result through `communicate`.

- [ ] **Step 5: Write committed run report**

Create `tools/tool-fluency/reports/2026-06-21-parent-watch-sidecars.md`:

```markdown
# Parent-Watch Sidecar Fluency Report

Date: 2026-06-21
Branch: wip/parent-watch-sidecars

## Probes

- `job_watch.parent_sidecar_default_source`
- `job_watch.parent_sidecar_filtered_tool`

## Results

| Model | Probe | Result | Notes |
| --- | --- | --- | --- |
| openai/gpt-5.4-mini | parent_sidecar_default_source | PASS | Used `delegate(watch_parent:true)`, `job_watch(source:"parent")`, and observer `communicate(end_turn:true)`. |
| openai/gpt-5.4-mini | parent_sidecar_filtered_tool | PASS | Used `events:["assistant.tool"]` with read_file ok filter. No polling after callback. |
| moonshot/kimi-for-coding | parent_sidecar_default_source | PASS | Used source-owned watch and communicate callback. |
| moonshot/kimi-for-coding | parent_sidecar_filtered_tool | PASS | Used filtered parent watch. No `delegate_send(to:"caller")`. |

## Residual Issues

- None observed in this run.
```

If a provider quota blocks a run, record `BLOCKED_QUOTA` in the result cell and include the exact CLI error in the notes.

- [ ] **Step 6: Commit probes and report**

Run:

```bash
git add tools/tool-fluency/probes/job_watch.yaml tools/tool-fluency/reports/2026-06-21-parent-watch-sidecars.md
git commit -m "test(fluency): cover parent-watch sidecar flow

Add live tool-fluency probes for default and filtered parent-watch observers
across GPT and Kimi. The probes assert the desired tool path instead of testing
documentation strings."
```

## Task 11: Full Test Run and Cleanup

**Files:**
- All modified files

- [ ] **Step 1: Run focused Go packages**

Run:

```bash
go test ./agent/internal/tool ./agent ./tools/tool-fluency/cmd/serf-fluency
```

Expected: PASS.

- [ ] **Step 2: Run full Go suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Run final legacy-shape scan**

Run:

```bash
rg -n "job_watch\\([^\\n]*(target|send)|delegate_send\\(to=\\\"caller\\\"\\)|await_reply" agent docs tools
```

Expected: matches are limited to superseded historical notes or rejection tests. There must be no positive instructions or runnable probes using the old public sidecar shape.

- [ ] **Step 4: Inspect git diff**

Run:

```bash
git status --short
git diff --stat
git diff --check
```

Expected: only planned files changed, no whitespace errors.

- [ ] **Step 5: Commit final cleanup if needed**

If Step 3 or Step 4 caused doc wording or test cleanup changes, run:

```bash
git add <changed-files>
git commit -m "chore: clean up parent-watch sidecar fallout

Remove stale references to the old sidecar routing shape and fix final test or
formatting fallout from the parent-watch implementation."
```

- [ ] **Step 6: Final summary**

Record in the implementation handoff:

```markdown
Implemented parent-watch sidecars.

Verification:
- go test ./agent/internal/tool ./agent ./tools/tool-fluency/cmd/serf-fluency
- go test ./...
- GPT live fluency probes
- Kimi live fluency probes

Key behavior:
- Parent grants observer with delegate(watch_parent:true).
- Observer installs job_watch(source:"parent").
- Observer reports with communicate(end_turn:true).
- Public job_watch target/send and delegate_send caller callback are removed.
```

## Self-Review Checklist

- [ ] `delegate.watch_parent` exists in schema, parser, and spawn plumbing.
- [ ] `watch_parent` grants `job_watch` but not `delegate`.
- [ ] `job_watch.source` is public; `target` and `send` are rejected publicly.
- [ ] `source:"parent"` requires an explicit non-transitive grant.
- [ ] Parent-source watch frames are delivered to the child watcher.
- [ ] Watch-origin `communicate(end_turn:true)` resumes the parent once.
- [ ] `delegate_send(to:"caller")` is removed from positive schemas, prompts, docs, and probes.
- [ ] Descendant concrete job watches are allowed from ancestors.
- [ ] Self-source feedback loop guard remains in place.
- [ ] Tool fluency probes cover GPT and Kimi.
