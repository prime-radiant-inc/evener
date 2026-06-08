# Phase B Hook Output Contract — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the output contract of the nine hook events serf already fires match Claude: the `PreToolUse` preferred decision schema, and a split so `additionalContext` reaches the model (as a system-reminder) while the `systemMessage` field is shown to the user.

**Architecture:** All changes live in `agent/internal/hooks` (parser + runner), `agent/internal/diagnostic` (a new `SourceHook` label), and the eight session delivery sites in `agent/`. The runner owns routing-by-event and returns two buckets (`ModelContext`, `UserMessages`); the session delivers them through two helpers. The struct-field rename is done expand-contract (add new buckets, migrate sites, remove old) so every commit compiles and tests stay green.

**Tech Stack:** Go (multi-module go.work workspace). Tests: `go test`. Gates: `make test`, `make lint` — both loop over `. agent llm auth`. Run package-scoped tests during tasks; run the full gates at the end.

**Design spec:** `docs/superpowers/specs/2026-06-08-phase-b-hook-output-contract-design.md` (read it before starting).

**Working directory:** the worktree root `/Users/jesse/prime-radiant/toil-suite/serf/.claude/worktrees/phase-b-hooks`. Run `go test` from inside the `agent/` module directory (it is its own Go module).

---

## Task 1: PreToolUse preferred decision schema (parser + RunPreToolUse)

Part 1 of the spec. Self-contained: this task does NOT touch the result-struct field names or delivery; it only fixes how `PreToolUse` decisions are parsed and aggregated. The deny reason moves out of the overloaded `SystemMessage` into a dedicated `PermissionReason` field.

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (`parsedHookOutput` struct ~393-405; `parseHookOutput` ~448-473; `RunPreToolUse` ~557-581)
- Test: `agent/internal/hooks/hooks_test.go`

- [ ] **Step 1: Write failing parser tests for the new fields**

Add to `agent/internal/hooks/hooks_test.go`:

```go
func TestParseHookOutput_PermissionDecisionAllow(t *testing.T) {
	o := parseHookOutput(`{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"looks fine"}}`, 0)
	if o.PermissionDecision != "allow" {
		t.Fatalf("PermissionDecision = %q, want allow", o.PermissionDecision)
	}
	if o.PermissionReason != "looks fine" {
		t.Fatalf("PermissionReason = %q, want \"looks fine\"", o.PermissionReason)
	}
	if o.Denied {
		t.Fatal("allow must not set Denied")
	}
}

func TestParseHookOutput_PermissionDecisionReason(t *testing.T) {
	o := parseHookOutput(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"dangerous"}}`, 0)
	if o.PermissionDecision != "deny" {
		t.Fatalf("PermissionDecision = %q, want deny", o.PermissionDecision)
	}
	if o.PermissionReason != "dangerous" {
		t.Fatalf("PermissionReason = %q, want dangerous", o.PermissionReason)
	}
	// The deny reason must NOT leak into SystemMessage (it is delivered as the
	// deny message, not as a user/model system message).
	if o.SystemMessage != "" {
		t.Fatalf("SystemMessage = %q, want empty (reason routes to PermissionReason)", o.SystemMessage)
	}
}

func TestParseHookOutput_DeprecatedTopLevelDecision(t *testing.T) {
	approve := parseHookOutput(`{"decision":"approve","reason":"ok"}`, 0)
	if approve.PermissionDecision != "allow" {
		t.Fatalf("approve -> PermissionDecision = %q, want allow", approve.PermissionDecision)
	}
	if approve.PermissionReason != "ok" {
		t.Fatalf("top-level reason -> PermissionReason = %q, want ok", approve.PermissionReason)
	}
	// Preferred hookSpecificOutput.permissionDecision wins over deprecated top-level.
	both := parseHookOutput(`{"decision":"approve","hookSpecificOutput":{"permissionDecision":"deny"}}`, 0)
	if both.PermissionDecision != "deny" {
		t.Fatalf("preferred should win: PermissionDecision = %q, want deny", both.PermissionDecision)
	}
}

func TestParseHookOutput_SystemMessageIsJSONField(t *testing.T) {
	plain := parseHookOutput("hello", 0)
	if plain.SystemMessageIsJSONField {
		t.Fatal("plain stdout must have SystemMessageIsJSONField=false")
	}
	jsonField := parseHookOutput(`{"systemMessage":"hi"}`, 0)
	if !jsonField.SystemMessageIsJSONField {
		t.Fatal("JSON systemMessage field must have SystemMessageIsJSONField=true")
	}
	errOut := parseHookOutput("boom", 2)
	if errOut.SystemMessageIsJSONField {
		t.Fatal("exit!=0 stderr must have SystemMessageIsJSONField=false")
	}
}
```

- [ ] **Step 2: Run the new tests; verify they fail to compile**

Run: `cd agent && go test ./internal/hooks/ -run 'TestParseHookOutput_(PermissionDecisionAllow|PermissionDecisionReason|DeprecatedTopLevelDecision|SystemMessageIsJSONField)' -v`
Expected: build failure — `o.PermissionDecision`, `o.PermissionReason`, `o.SystemMessageIsJSONField` undefined.

- [ ] **Step 3: Add the parser fields and parsing logic**

In `agent/internal/hooks/hooks.go`, add fields to `parsedHookOutput` (after `TerminalSequence`):

```go
	// PermissionDecision is the PreToolUse hookSpecificOutput.permissionDecision
	// ("allow"|"deny"|"ask"|"defer"), or "" if absent. RunPreToolUse interprets it.
	PermissionDecision string
	// PermissionReason is permissionDecisionReason (preferred) or the deprecated
	// top-level reason. Kept out of SystemMessage so the deny reason is not also
	// delivered as a system message.
	PermissionReason string
	// SystemMessageIsJSONField is true only when SystemMessage came from the JSON
	// "systemMessage" field (user-visible), false for plain stdout or error stderr.
	SystemMessageIsJSONField bool
```

In `parseHookOutput`, in the JSON `systemMessage` branch, set the flag:

```go
	if s, ok := parsed["systemMessage"].(string); ok {
		result.SystemMessage = s
		result.SystemMessageIsJSONField = true
	}
```

Replace the `hookSpecificOutput` deny block and the top-level `decision` block with:

```go
	// Extract hookSpecificOutput
	if hso, ok := parsed["hookSpecificOutput"].(map[string]any); ok {
		if pd, ok := hso["permissionDecision"].(string); ok {
			result.PermissionDecision = pd
			if pd == "deny" {
				result.Denied = true
			}
		}
		if r, ok := hso["permissionDecisionReason"].(string); ok {
			result.PermissionReason = r
		}
		if ui, ok := hso["updatedInput"].(map[string]any); ok {
			result.UpdatedInput = ui
		}
		// additionalContext is model context; route separately from user-visible systemMessage.
		if ac, ok := hso["additionalContext"].(string); ok {
			result.AdditionalContext = ac
		}
	}

	// Deprecated top-level decision form. Preferred hookSpecificOutput.permissionDecision
	// (parsed above) wins; only fill in when it is absent. "block" also drives
	// Stop/SubagentStop blocking via Blocked.
	switch parsed["decision"] {
	case "block":
		result.Blocked = true
		if result.PermissionDecision == "" {
			result.PermissionDecision = "deny"
		}
	case "approve":
		if result.PermissionDecision == "" {
			result.PermissionDecision = "allow"
		}
	}
	if reason, ok := parsed["reason"].(string); ok {
		result.BlockReason = reason
		if result.PermissionReason == "" {
			result.PermissionReason = reason
		}
	}
```

Note: `Denied` stays set for an explicit `permissionDecision:"deny"`; the deprecated `decision:"block"`→deny mapping for PreToolUse is applied in `RunPreToolUse` via `PermissionDecision`/`Blocked` (Step 5), so `Stop`/`SubagentStop` (which read `Blocked`) are unaffected.

- [ ] **Step 4: Run the new parser tests; verify pass**

Run: `cd agent && go test ./internal/hooks/ -run 'TestParseHookOutput_(PermissionDecisionAllow|PermissionDecisionReason|DeprecatedTopLevelDecision|SystemMessageIsJSONField)' -v`
Expected: PASS.

- [ ] **Step 5: Write failing RunPreToolUse decision tests, then update RunPreToolUse**

Add tests to `agent/internal/hooks/hooks_test.go`:

```go
func TestRunPreToolUse_DenyReasonFromPermissionDecisionReason(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"nope"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied {
		t.Fatal("expected deny")
	}
	if r.DenyMessage != "nope" {
		t.Fatalf("DenyMessage = %q, want nope", r.DenyMessage)
	}
}

func TestRunPreToolUse_DeprecatedBlockDenies(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"decision":"block","reason":"legacy"}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied {
		t.Fatal("deprecated top-level block must deny PreToolUse")
	}
	if r.DenyMessage != "legacy" {
		t.Fatalf("DenyMessage = %q, want legacy", r.DenyMessage)
	}
}

func TestRunPreToolUse_AskProceeds(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`,
	})
	r := runner.RunPreToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PreToolUse", ToolName: "Bash"})
	if r.Denied {
		t.Fatal("ask must not deny (serf has no permission prompt)")
	}
}
```

Then update `RunPreToolUse` so the per-output loop computes the deny decision from the new fields. Replace the deny/deny-message logic in the loop with:

```go
		denied := false
		switch o.PermissionDecision {
		case "deny":
			denied = true
		case "ask", "defer":
			// Recognized but not honored: serf has no interactive permission prompt.
			// The tool proceeds; the user-visible diagnostic is added in Task 3
			// (it needs the UserMessages bucket, which does not exist yet).
		}
		if exitBehavior(plugin.HookPreToolUse).BlockOnExit2 && o.RawExitCode == 2 {
			denied = true
		}
		if denied {
			result.Denied = true
			if result.DenyMessage == "" {
				switch {
				case o.PermissionReason != "":
					result.DenyMessage = o.PermissionReason
				case o.IsError:
					result.DenyMessage = o.SystemMessage
				}
			}
		}
```

Keep the existing `SystemMessages`/`AdditionalContext`/`TerminalSequences`/`UpdatedInput` aggregation in the loop unchanged for now. (The `ask`/`defer` user diagnostic and the bucket rename come in Task 3.)

- [ ] **Step 6: Update the two tests that used the non-standard `reason` key**

In `agent/internal/hooks/hooks_test.go`:
- `TestParseHookOutput_PreToolUseDeny` (~556): change the JSON to use `permissionDecisionReason` and assert `o.PermissionReason == "dangerous operation"` (not `o.SystemMessage`).
- The runner test at ~429 (the hook command with `"reason":"not allowed"`): change to `"permissionDecisionReason":"not allowed"` and assert `DenyMessage == "not allowed"`.

Confirm `TestHookRunner_PreToolUse_ExitCode2Denies` (~447) still passes unchanged (exit-2 stderr → `DenyMessage` via the `o.IsError` fallback).

- [ ] **Step 7: Run the hooks package tests**

Run: `cd agent && go test ./internal/hooks/ -v`
Expected: PASS (all, including the updated deny tests).

- [ ] **Step 8: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go
git commit -m "feat(hooks): PreToolUse preferred decision schema (allow/deny/ask/defer + permissionDecisionReason)"
```

---

## Task 2: Add a `hook` diagnostic source

`deliverHookUserMessage` (Task 4) emits an `EventWarning`. Enrichment overwrites the warning's `Source` unless it is a recognized value, so add `SourceHook`.

**Files:**
- Modify: `agent/internal/diagnostic/diagnostic.go` (const block ~12-17; `normalizeSource` ~68-81; `defaultForSource` ~83-103)
- Test: `agent/internal/diagnostic/diagnostic_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create/append `agent/internal/diagnostic/diagnostic_test.go`:

```go
package diagnostic

import "testing"

func TestFromFields_HookSourcePreserved(t *testing.T) {
	info := FromFields("hook", "", "", "rate limit exceeded")
	if info.Source != SourceHook {
		t.Fatalf("Source = %q, want hook (must not be reclassified by message content)", info.Source)
	}
}
```

- [ ] **Step 2: Run; verify it fails**

Run: `cd agent && go test ./internal/diagnostic/ -run TestFromFields_HookSourcePreserved -v`
Expected: FAIL — `SourceHook` undefined (build error).

- [ ] **Step 3: Add `SourceHook`**

In `agent/internal/diagnostic/diagnostic.go`, add to the const block:

```go
	SourceHook Source = "hook"
```

Add a case to `normalizeSource`:

```go
	case SourceHook:
		return SourceHook
```

Add a case to `defaultForSource` (before `default:`):

```go
	case SourceHook:
		return Info{
			Source: SourceHook,
			Title:  "Hook message",
			Hint:   "A plugin hook returned a user-facing message.",
		}
```

- [ ] **Step 4: Run; verify pass**

Run: `cd agent && go test ./internal/diagnostic/ -run TestFromFields_HookSourcePreserved -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/diagnostic/
git commit -m "feat(diagnostic): add SourceHook so hook warnings keep their source"
```

---

## Task 3: Runner routing into ModelContext / UserMessages buckets (expand)

Add the two routed buckets to the result structs and populate them via event-aware routing, **keeping** the existing `SystemMessages`/`AdditionalContext` fields populated (sites still read them until Task 4). This is the "expand" half of the migration.

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (`RunResult` ~366-371, `PreToolUseResult` ~373-381, `StopResult` ~383-390; `RunPreToolUse`; `runStopEvent`; `collectSystemMessages` ~676-690 and its five callers ~597-672)
- Test: `agent/internal/hooks/hooks_test.go`

- [ ] **Step 1: Write failing routing tests**

Add to `agent/internal/hooks/hooks_test.go`:

```go
func TestRouting_AdditionalContextToModel_SystemMessageToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo '{"systemMessage":"to-user","hookSpecificOutput":{"additionalContext":"to-model"}}'`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "Bash"})
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "to-model" {
		t.Fatalf("ModelContext = %v, want [to-model]", r.ModelContext)
	}
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "to-user" {
		t.Fatalf("UserMessages = %v, want [to-user]", r.UserMessages)
	}
}

func TestRouting_PostToolUseExit2StderrToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo "boom" >&2; exit 2`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "Bash"})
	if len(r.ModelContext) == 0 || !strings.Contains(r.ModelContext[0], "boom") {
		t.Fatalf("PostToolUse exit-2 stderr must reach the model: ModelContext = %v", r.ModelContext)
	}
	if len(r.UserMessages) != 0 {
		t.Fatalf("PostToolUse exit-2 stderr must not go to the user: UserMessages = %v", r.UserMessages)
	}
}

func TestRouting_StopExit2StderrToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: `echo "stay" >&2; exit 2`,
	})
	r := runner.RunStop(context.Background(), Input{CWD: "/tmp", HookEventName: "Stop"})
	if !r.Blocked {
		t.Fatal("Stop exit 2 must block")
	}
	if len(r.ModelContext) == 0 || !strings.Contains(r.ModelContext[0], "stay") {
		t.Fatalf("Stop exit-2 stderr must reach the model: ModelContext = %v", r.ModelContext)
	}
}

func TestRouting_ContextEventPlainStdoutToModel(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookSessionStart, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo bootstrap`,
	})
	r := runner.RunSessionStart(context.Background(), Input{CWD: "/tmp", HookEventName: "SessionStart"})
	if len(r.ModelContext) != 1 || r.ModelContext[0] != "bootstrap" {
		t.Fatalf("SessionStart plain stdout must reach the model: ModelContext = %v", r.ModelContext)
	}
}

func TestRouting_NonContextPlainStdoutToUser(t *testing.T) {
	runner := newRunner(nil, "")
	runner.Add(plugin.HookPostToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5, Command: `echo logged`,
	})
	r := runner.RunPostToolUse(context.Background(), Input{CWD: "/tmp", HookEventName: "PostToolUse", ToolName: "Bash"})
	if len(r.UserMessages) != 1 || r.UserMessages[0] != "logged" {
		t.Fatalf("PostToolUse plain stdout must go to the user: UserMessages = %v", r.UserMessages)
	}
	if len(r.ModelContext) != 0 {
		t.Fatalf("PostToolUse plain stdout must not reach the model: ModelContext = %v", r.ModelContext)
	}
}
```

- [ ] **Step 2: Run; verify they fail to compile**

Run: `cd agent && go test ./internal/hooks/ -run TestRouting -v`
Expected: build failure — `r.ModelContext` / `r.UserMessages` undefined.

- [ ] **Step 3: Add the bucket fields (keep the old ones)**

In `agent/internal/hooks/hooks.go`, add to `RunResult`, `PreToolUseResult`, and `StopResult` (alongside the existing fields):

```go
	// ModelContext is delivered to the model (additionalContext, context-event
	// plain stdout, and non-deny error stderr).
	ModelContext []string
	// UserMessages is shown to the user (the JSON systemMessage field and
	// non-context plain stdout).
	UserMessages []string
```

Add a doc comment to the kept `TerminalSequences` field on each struct:

```go
	// TerminalSequences is parsed but currently has no delivery-site consumer.
	TerminalSequences []string
```

- [ ] **Step 4: Implement event-aware routing**

Add a routing helper to `agent/internal/hooks/hooks.go`:

```go
// routeOutput places one parsed hook output into the model/user buckets for the
// given event (07 §"Hook output contract"; design spec §Routing). It does NOT
// handle the PreToolUse deny reason or the Stop/SubagentStop block reason — the
// blocking runners consume those before/instead of calling this.
func routeOutput(event plugin.HookEvent, o parsedHookOutput, model, user *[]string) {
	if o.AdditionalContext != "" {
		*model = append(*model, o.AdditionalContext)
	}
	if o.SystemMessage == "" {
		return
	}
	isContext := event == plugin.HookSessionStart || event == plugin.HookUserPromptSubmit
	switch {
	case o.IsError:
		*model = append(*model, o.SystemMessage) // error stderr -> model (preserves today)
	case o.SystemMessageIsJSONField:
		*user = append(*user, o.SystemMessage) // JSON systemMessage field -> user
	case isContext:
		*model = append(*model, o.SystemMessage) // context-event plain stdout -> model
	default:
		*user = append(*user, o.SystemMessage) // non-context plain stdout -> user
	}
}
```

Change `collectSystemMessages` to take the event and populate both old and new fields:

```go
func collectSystemMessages(event plugin.HookEvent, outputs []parsedHookOutput) RunResult {
	var result RunResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			result.AdditionalContext = append(result.AdditionalContext, o.AdditionalContext)
		}
		if o.TerminalSequence != "" {
			result.TerminalSequences = append(result.TerminalSequences, o.TerminalSequence)
		}
		routeOutput(event, o, &result.ModelContext, &result.UserMessages)
	}
	return result
}
```

Update the five callers to pass the event:
- `RunPostToolUse`: `return collectSystemMessages(plugin.HookPostToolUse, outputs)`
- `RunUserPromptSubmit`: `return collectSystemMessages(plugin.HookUserPromptSubmit, outputs)`
- `RunSessionStartFor`: `return collectSystemMessages(plugin.HookSessionStart, outputs)`
- `RunPreCompact`: `return collectSystemMessages(plugin.HookPreCompact, outputs)`
- `RunNotification`: `return collectSystemMessages(plugin.HookNotification, outputs)`

In `RunPreToolUse`, also populate the buckets. For a denying output, the reason is the `DenyMessage` (do not route its stderr to the model). For `ask`/`defer`, append the diagnostic to `UserMessages`. Add inside the loop (keeping the existing old-field aggregation):

```go
		if o.PermissionDecision == "ask" || o.PermissionDecision == "defer" {
			result.UserMessages = append(result.UserMessages,
				"hook returned permissionDecision \""+o.PermissionDecision+"\" which serf does not support (no interactive permission prompt); the tool will proceed")
		}
		if denied {
			// reason consumed as DenyMessage above; do not route this output's stderr
		} else {
			routeOutput(plugin.HookPreToolUse, o, &result.ModelContext, &result.UserMessages)
		}
```

In `runStopEvent`, append `routeOutput(event, o, &result.ModelContext, &result.UserMessages)` inside the loop (Stop/SubagentStop are not the deny case — their stderr should reach the model, which `routeOutput`'s `IsError` branch does).

- [ ] **Step 5: Run the routing tests + full hooks package**

Run: `cd agent && go test ./internal/hooks/ -v`
Expected: PASS (new routing tests pass; old tests still green — old fields remain populated).

- [ ] **Step 6: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go
git commit -m "feat(hooks): event-aware routing into ModelContext/UserMessages buckets"
```

---

## Task 4: Session delivery helpers + migrate the eight sites

Replace the eight `s.Steer(...)` `TODO(phase-B)` pairs with two helpers reading the new buckets. The old result fields still exist (removed in Task 5), so this compiles throughout.

**Files:**
- Create helpers in: `agent/session_queue.go` (near `Steer`)
- Modify: `agent/session_tools.go` (~226-233, ~378-385), `agent/session_tool_round.go` (~377-384), `agent/subagents.go` (~692-698), `agent/session_lifecycle.go` (~681-688), `agent/session_init.go` (~666-672), `agent/session_events.go` (~136-142), `agent/session_compaction.go` (~70-85)
- Test: `agent/plugin_integration_test.go` (~112, ~127)

- [ ] **Step 1: Add the three helpers**

In `agent/session_queue.go`:

```go
// wrapHookContext frames hook-provided model context as a system reminder so the
// model treats it as context, not as user speech (matches Claude's "wrapped in a
// system reminder" delivery of additionalContext).
func wrapHookContext(text string) string {
	return "<SYSTEM-REMINDER>" + text + "</SYSTEM-REMINDER>"
}

// deliverHookContext enqueues hook model-context as a steering turn (survives to
// the next model turn for Stop/SubagentStop).
func (s *Session) deliverHookContext(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.Steer(wrapHookContext(text))
}

// deliverHookUserMessage surfaces a hook's user-visible message via the
// diagnostic-warning channel (CLI/TUI/hub), WITHOUT firing the Notification hook
// (plain emit would re-enter it and recurse).
func (s *Session) deliverHookUserMessage(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.emitDiagnosticWarning(events.WarningData{Source: "hook", Message: text})
}
```

Confirm `agent/session_queue.go` imports `events` (`primeradiant.com/serf/agent/events`); add it if missing.

- [ ] **Step 2: Migrate the seven `Steer` sites**

At each of these sites, replace the two `for _, msg := range result.SystemMessages { s.Steer(msg) }` / `... result.AdditionalContext ...` loops (and the `TODO(phase-B)` comment) with:

```go
	for _, m := range <result>.ModelContext {
		s.deliverHookContext(m)
	}
	for _, m := range <result>.UserMessages {
		s.deliverHookUserMessage(m)
	}
```

Sites and their `<result>` variable / receiver:
- `agent/session_tools.go` PreToolUse (~226): `preResult` (receiver `s`).
- `agent/session_tools.go` PostToolUse (~378): `postResult` (receiver `s`).
- `agent/session_tool_round.go` Stop (~377): `stopResult` (receiver `s`).
- `agent/subagents.go` SubagentStop (~692): `stopResult`, receiver `a.sess` (use `a.sess.deliverHookContext` / `a.sess.deliverHookUserMessage`).
- `agent/session_lifecycle.go` UserPromptSubmit (~681): `result` (receiver `s`).
- `agent/session_init.go` SessionStart (~666): `result` (receiver `s`).
- `agent/session_events.go` Notification (~136): `result` (receiver `s`).

- [ ] **Step 3: Migrate PreCompact (history-append path)**

In `agent/session_compaction.go` `runPreCompactHook`, replace the `messages = append(...SystemMessages...)` / `...AdditionalContext...` block with:

```go
	if s.hookRunner != nil {
		compactResult := s.hookRunner.RunPreCompact(ctx, s.hookInput(plugin.HookPreCompact))
		for _, m := range compactResult.ModelContext {
			messages = append(messages, wrapHookContext(m))
		}
		for _, m := range compactResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
	}
```

(The goal steering appended afterward stays unchanged and unwrapped.)

- [ ] **Step 4: Update the SessionStart bootstrap assertions (now wrapped)**

In `agent/plugin_integration_test.go`, the fresh-session assertion (~112) now expects the wrapped text. Change:

```go
	if got := fresh.SteeringQueueSnapshot(); len(got) != 1 || got[0].Text != "<SYSTEM-REMINDER>startup-bootstrap</SYSTEM-REMINDER>" {
		t.Fatalf("fresh session bootstrap steering = %+v, want wrapped startup-bootstrap", got)
	}
```

The restored-session assertion (~127, asserts `len(got) != 0`) is unchanged.

- [ ] **Step 5: Build and run the agent package tests**

Run: `cd agent && go build ./... && go test . ./internal/hooks/ -run 'Plugin|Hook|Notification|Compact|SessionStart' -v`
Expected: PASS (compiles; migrated sites deliver via the new helpers; bootstrap test sees wrapped text).

- [ ] **Step 6: Commit**

```bash
git add agent/session_queue.go agent/session_tools.go agent/session_tool_round.go agent/subagents.go agent/session_lifecycle.go agent/session_init.go agent/session_events.go agent/session_compaction.go agent/plugin_integration_test.go
git commit -m "feat(hooks): deliver additionalContext to the model and systemMessage to the user"
```

---

## Task 5: Remove the old result fields (contract)

Drop `SystemMessages` and `AdditionalContext` from the result structs and stop populating them, now that all sites read the buckets. Update the remaining tests that referenced the old fields.

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (struct fields + `collectSystemMessages` + `RunPreToolUse` + `runStopEvent` old-field appends)
- Test: `agent/internal/hooks/exitcode_test.go` (~108-123), `agent/internal/hooks/hooks_test.go` (SessionStart/PreToolUse/prompt assertions), `agent/plugin_integration_live_test.go`, `agent/plugin_real_test.go`

- [ ] **Step 1: Remove the fields and their population**

In `agent/internal/hooks/hooks.go`: delete `SystemMessages []string` and `AdditionalContext []string` from `RunResult`, `PreToolUseResult`, `StopResult`. In `collectSystemMessages`, `RunPreToolUse`, and `runStopEvent`, delete the `result.SystemMessages = append(...)` and `result.AdditionalContext = append(...)` lines (keep `TerminalSequences`, `routeOutput`, `Denied`/`DenyMessage`, `Blocked`/`BlockReason`).

- [ ] **Step 2: Update the hooks-package tests to the buckets**

- `agent/internal/hooks/exitcode_test.go` (~108-123): the PostToolUse exit-2 test — replace `result.SystemMessages` with `result.ModelContext` (exit-2 stderr now routes to the model). Keep the "no Denied field on RunResult" comment.
- `agent/internal/hooks/hooks_test.go`: every remaining `.SystemMessages` / `.AdditionalContext` reference (e.g. the `RunSessionStartFor` startup/resume/clear assertions ~412-419, the prompt-hook stdin test ~60, the PreToolUse `len(preResult.SystemMessages)` checks ~479/489) — change to the correct bucket. For `RunSessionStartFor` (a context event), plain-stdout messages land in `ModelContext`. For `RunPreToolUse` with no output, both buckets are empty (assert `len(r.ModelContext)==0 && len(r.UserMessages)==0`). For the prompt-hook test (~60), the prompt-hook stdout is plain text on a `PreToolUse` (non-context) → `UserMessages`.

- [ ] **Step 3: Update the live/integration tests (compile + semantics)**

- `agent/plugin_integration_live_test.go`: `startResult.AdditionalContext` (~373-384, SessionStart context) → `startResult.ModelContext`; `dangerousResult.SystemMessages` (~408-415, a PreToolUse deny) → assert `dangerousResult.Denied` / `dangerousResult.DenyMessage`; the prompt-hook `result.SystemMessages` (~503-506) → the appropriate bucket for that event.
- `agent/plugin_real_test.go`: `result.AdditionalContext` (~218-223) → `result.ModelContext` (SessionStart); `result.SystemMessages` (~378-389, a PostToolUse security warning via plain stdout) → `result.UserMessages`.

- [ ] **Step 4: Run the full agent + hooks test suite**

Run: `cd agent && go build ./... && go test . ./internal/hooks/ ./internal/diagnostic/ -v`
Expected: PASS. (Live tests that require network skip themselves; they must still compile.)

- [ ] **Step 5: Commit**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/exitcode_test.go agent/internal/hooks/hooks_test.go agent/plugin_integration_live_test.go agent/plugin_real_test.go
git commit -m "refactor(hooks): drop the pre-split SystemMessages/AdditionalContext result fields"
```

---

## Task 6: Update `docs/hooks.md`

Make the shipped reference correct for the new delivery and the PreToolUse schema.

**Files:**
- Modify: `docs/hooks.md`

- [ ] **Step 1: Update "What your hook returns"**

In the output-JSON section (~387-420): state that `hookSpecificOutput.additionalContext` is delivered to the **model** wrapped in a system reminder; the top-level `systemMessage` field is shown to the **user** (not the model); for `PreToolUse`, `permissionDecision` now honors `allow`/`deny` with `permissionDecisionReason`, and `ask`/`defer` are recognized but not honored (the tool proceeds, with a diagnostic). Remove the line claiming `systemMessage` is "delivered to the model in Phase 1."

- [ ] **Step 2: Update the plain-stdout and exit-code notes**

In "What your hook returns" (~375-381) and the exit-codes table (~422-440): exit-0 plain stdout reaches the **model** only for `SessionStart`/`UserPromptSubmit`; for other events it is shown to the **user**. Exit≠0 stderr is unchanged (model for `PostToolUse`; deny/block reason for `PreToolUse`/`Stop`/`SubagentStop`). Add a one-line note that non-context plain stdout being user-visible is a deliberate divergence from Claude (which debug-logs it).

- [ ] **Step 3: Update the complete example**

In the example prose (~519-524): the `PostToolUse` `log-result.sh` stdout is now shown to the **user** (a `[warning]`-style system message), not delivered to the model; to send context to the model from a hook, use `additionalContext`.

- [ ] **Step 4: Commit**

```bash
git add docs/hooks.md
git commit -m "docs(hooks): document the additionalContext/systemMessage delivery split + PreToolUse schema"
```

---

## Task 7: Update `docs/subagent-management/07-lifecycle-hooks-claude-compat.md`

Move the now-shipped items out of "reserved (Phase B)", keeping `updatedInput` revalidation and the interactive `ask`/`defer` reserved.

**Files:**
- Modify: `docs/subagent-management/07-lifecycle-hooks-claude-compat.md`

- [ ] **Step 1: Update the PreToolUse reserved schema note (~501-517)**

State that `permissionDecision: allow|deny`, `permissionDecisionReason`, and the deprecated top-level `approve`/`block` mapping are **shipped** (link `../hooks.md`); `ask`/`defer` and `updatedInput` **revalidation** remain reserved (no permission prompt / no post-rewrite re-validation).

- [ ] **Step 2: Update the universal-output-fields + additionalContext notes (~470-482)**

`additionalContext` now has its distinct model-delivery channel (system reminder) — move it from reserved to shipped; the `systemMessage`→user split is shipped. Keep `continue`/`stopReason`/`suppressOutput` reserved as before.

- [ ] **Step 3: Update the Phase B roadmap bullet (~657-663)**

Split the first bullets: the PreToolUse `allow|deny`/`permissionDecisionReason`/deprecated mapping and the `additionalContext` delivery channel are done; explicitly keep `ask`/`defer`, `updatedInput` revalidation, `PermissionRequest`/`PermissionDenied`, the new events, and `if` evaluation reserved.

- [ ] **Step 4: Commit**

```bash
git add docs/subagent-management/07-lifecycle-hooks-claude-compat.md
git commit -m "docs(07): move shipped PreToolUse schema + additionalContext channel out of reserved"
```

---

## Task 8: Full gates

- [ ] **Step 1: Run the full test + lint gates across all modules**

Run: `make test && make lint`
Expected: PASS across `. agent llm auth`. (If `make test` needs network for live tests, they skip; the rest must pass.)

- [ ] **Step 2: Grep-verify no stale references remain**

Run: `grep -rn "\.SystemMessages\|TODO(phase-B)" agent --include="*.go"`
Expected: no `TODO(phase-B)` anchors remain; no `.SystemMessages` references (the field is gone). Investigate any hit.

- [ ] **Step 3: Final commit if anything was fixed**

```bash
git add -A
git commit -m "chore(hooks): finalize Phase B output-contract gates"
```

(Only if Step 1/2 required fixes; otherwise skip.)
