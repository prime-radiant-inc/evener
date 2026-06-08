# Lifecycle Hooks — Phase 1 (Claude-compatible matcher, exec-form, input/output fields) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Dispatch a fresh subagent per task plus a review pass.

**Goal:** Close the highest-impact Claude hook compatibility gaps in serf's existing plugin-hook subsystem **without** introducing a new event bus, schema framework, or any new package. Phase 1 is "make current hooks correctly compatible" (spec 07 §"Phase A", scoped down): Claude-compatible matcher semantics, command `args` exec-form, the official common/tool input fields, separate routing of `additionalContext` from user-visible `systemMessage`, a central event-specific exit-code table for the events serf already fires, and the official command env vars. Every change is additive and tier-labeled; the nine currently-recognized events must keep loading and running exactly as today.

**Architecture:** All work lives next to the existing hook code — `agent/plugin/hooks.go` (parser + `RegisteredHook` + event enum) and `agent/internal/hooks/hooks.go` (runner: matcher, command/prompt exec, output parser, aggregation, env). One new tiny same-package file `agent/internal/hooks/matcher.go` holds the shared matcher helper; one new same-package file `agent/internal/hooks/exitcode.go` holds the exit-code table. No new packages, no new top-level dependency. Integration call sites in `agent/` (`session_tools.go`, `session_events.go`, `session_init.go`) are touched only to pass the new official input fields where the values are already in hand; their behavior does not otherwise change.

**Tech Stack:** Go (go.work multi-module; the `agent` and `llm` modules). Tests are standard `go test` table tests alongside the code: `agent/internal/hooks/*_test.go` and `agent/plugin/*_test.go`. Both test files already exist and are extended, not replaced. Gates: `make test` and `make lint` (golangci across all modules, plus `serf-namingcheck`/`serf-internalcheck`/`serf-docscheck`).

**Read before starting:**
- `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` — the contract. Where this plan says "per 07 §X" the spec holds the exhaustive list.
- `docs/subagent-management/10-runtime-contracts.md` §"Contract 4: compatibility tiers" — the tier vocabulary (`serf-native`, `claude-compatible-subset`, `reserved-placeholder`, `experimental`) every feature must be labeled with.
- Official source: <https://code.claude.com/docs/en/hooks> — matcher table, exit-code table, default timeouts. The spec transcribes these; the page is authoritative if they diverge.

**Compatibility-tier discipline (applies to every task):** Each behavior added or changed here is labeled in a doc comment on the symbol AND in the task's commit body with exactly one tier. Phase 1 ships only `claude-compatible-subset` and `serf-native` behavior. Anything Claude documents that this phase does NOT implement is left as `reserved-placeholder` (parsed/diagnosed predictably, never advertised as working). No `experimental` behavior is introduced in Phase 1.

**Conventions for every task:** run the single new test with `go test ./agent/internal/hooks/ -run <TestName> -v` (or `./agent/plugin/`); before each commit run `go build ./...` and `go test ./agent/internal/hooks/... ./agent/plugin/...`; commit only the files the task names. Do not run `git add -A`. Do not commit until the task's test is green and `go build ./...` is clean.

---

## What is IN Phase 1 (and why)

| Item | Tier | Why in Phase 1 |
|---|---|---|
| Characterization test of current hook behavior (parser + matcher + output + env + integration field-passing) | n/a (safety net) | Locks today's bytes so nothing below regresses. Front-loaded. |
| Claude-compatible matcher: empty/`*` = all, `[A-Za-z0-9_\|]+` = exact / pipe-list, else regex; invalid regex skips + diagnoses | `claude-compatible-subset` | **Highest impact.** Today `Bash` regex-substring-matches `BashOutput`; `mcp__memory` wrongly matches `mcp__memory__search`. This changes *which hooks fire* and breaks real configs. 07 calls it a "Phase A requirement." |
| Command `args` exec-form (spawn directly, no shell) + `shell` (`bash` default, reject unknown) | `claude-compatible-subset` | Lets plugin authors stop wrapping every path-with-spaces in `bash -c`. Self-contained, deterministic. |
| Parser captures `Args`, `Shell`, `If`, `Async`, `AsyncRewake`, `StatusMessage` + source/event/group-index/handler-index metadata + unknown-field capture | `claude-compatible-subset` (fields) / `serf-native` (metadata) | Prereq for exec-form + diagnostics; preserves harmless future fields instead of dropping them. |
| Official common + tool input fields on `Input` (`transcript_path`, `permission_mode`, `tool_use_id`, `tool_response`, `agent_id`, `agent_type`), keep `user_prompt`/`tool_result` aliases | `claude-compatible-subset` | Real hooks read these. Additive; old aliases stay during migration. Only populated where the value is already in hand. |
| Split aggregation: route `additionalContext` (model context) separately from `systemMessage` (user-visible). Add `AdditionalContext` + `TerminalSequence` to results | `claude-compatible-subset` | 07 acceptance criterion: "`additionalContext` is routed separately from user-visible `systemMessage`." Today they are folded together (hooks.go:314-321). |
| Central exit-code table for the **events serf already fires** (`PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, `PreCompact`, `Notification`) | `claude-compatible-subset` | 07 acceptance: "Exit-code-2 behavior is event-specific and table-driven … Do not treat every exit-code-2 hook as generic denial." JSON parsed only on exit 0. |
| Official command env vars where known (`CLAUDE_EFFORT`, `CLAUDE_CODE_REMOTE`), keep `PLUGIN_ROOT` alias | `claude-compatible-subset` (official) / `serf-native` (`PLUGIN_ROOT`) | Cheap, high-compat. Only set vars whose values serf actually has. |
| Tier labeling surfaced in `/status` hook diagnostics + a doc-comment tier on each event const | `serf-native` (the surfacing) | 07: "Every hook feature must be labeled with exactly one tier in docs, status output, diagnostics, and tests." |

## What is DEFERRED (and to which tier/phase)

- **New events** — `PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `UserPromptExpansion`, `StopFailure`, and all of `Setup`/`InstructionsLoaded`/`MessageDisplay`/`CwdChanged`/`FileChanged`/`TaskCreated`/`TaskCompleted`/`TeammateIdle`/`WorktreeCreate`/`WorktreeRemove`/`Elicitation`/`ElicitationResult` → **`reserved-placeholder`**, spec 07 Phase B/D. Phase 1 makes them *parse-and-diagnose as recognized-but-unsupported* (Task 2) but does not fire them.
- **`http`, `mcp_tool`, `agent` handler types** → Phase C. `http`/`mcp_tool` are `reserved-placeholder` until implemented; `agent` is `experimental` per 07. Phase 1 makes them parse and show as unsupported/skipped, never silently dropped.
- **`PreToolUse` preferred output schema** (`allow|deny|ask|defer`, `permissionDecisionReason`, deprecated `approve`/`block` mapping, revalidation after `updatedInput`) → Phase B. Phase 1 keeps the *current* `deny`-only behavior (already tested) but routes it through the new exit-code table so the deny path is unchanged.
- **`PermissionRequest` decision object, `PermissionDenied.retry`, `updatedPermissions`** → Phase B, gated on a real serf approval boundary.
- **`async`/`asyncRewake` execution** → Phase C (parse-only in Phase 1; recognized field, not executed).
- **`if` permission-rule evaluation** → Phase B (parse-only in Phase 1; captured, not evaluated; diagnosed as recognized-but-not-enforced).
- **`once`, `statusMessage` behavior** → reserved/cosmetic; captured, not acted on.
- **SDK typed lifecycle hooks** → Phase E.
- **JS-regex exact parity** (lookbehind/backreferences) → `reserved-placeholder`; Phase 1 documents the Go RE2 caveat (07 §Caveats) and ensures invalid regex never panics.

The phase boundary is explicit: Phase 1 = "the hooks that already fire, made Claude-compatible in matcher / exec / IO / exit-code, with honest diagnostics for everything else."

---

## File Structure

| File | Responsibility | Change |
| --- | --- | --- |
| `agent/plugin/hooks.go` | event enum + tier registry, `RegisteredHook`, `hookSpec`, `hookMatcherGroup`, `parsePluginHooks`, `discoverPluginHooks` | Modify (new fields, metadata, recognized-but-unsupported event set, tier map, unknown-field capture) |
| `agent/internal/hooks/matcher.go` | **new** shared matcher helper `matchTarget(matcher, target) (bool, error)` (empty/star, exact/pipe-list, regex) | Create |
| `agent/internal/hooks/exitcode.go` | **new** central exit-code table `exitBehavior(event) eventExitPolicy` | Create |
| `agent/internal/hooks/hooks.go` | `Input`, `parsedHookOutput`, `parseHookOutput`, `MatchHooks`, `runHook`, `runAll`, command exec, env, result structs, runner methods | Modify (matcher delegation, exec-form, official fields, additionalContext split, exit-table use, env vars) |
| `agent/session_tools.go` | `execTool` PreToolUse/PostToolUse input build | Modify (set `tool_use_id`, `tool_response`; no behavior change) |
| `agent/session_events.go` | `hookInput` constructor | Modify (set `transcript_path`, `permission_mode` when available) |
| `agent/status.go` | `DetailedStatus.Hooks` | Modify (additive: surface tier + recognized-but-unsupported counts) |
| Tests | `agent/internal/hooks/hooks_test.go`, `agent/internal/hooks/matcher_test.go` (new), `agent/internal/hooks/exitcode_test.go` (new), `agent/plugin/hooks_test.go` | Create/modify |

**Stale-plan note:** `docs/superpowers/plans/2026-05-14-claude-code-compat-sp5-hook-parity-plan.md` predates the hook code's move into `agent/plugin/` + `agent/internal/hooks/` (it targets `agent/plugin_hooks.go` in package `agent` and depends on never-landed SP1/SP2 siblings). Do **not** follow it. Spec 07 is the current contract; this plan supersedes SP5 for the Phase-1 scope.

---

## Task 1: Characterization test — lock current hook behavior

**Spec:** 07 §"Phase A" step 10 ("Update tests for current events before adding new events") + the global "characterization tests first" rule (10 §YAGNI/DRY plan step 6). This is the regression net for every later task.

**Files:**
- Test: `agent/internal/hooks/hooks_test.go` (append), `agent/plugin/hooks_test.go` (append)

- [ ] **Step 1: Characterize the CURRENT matcher (the behavior Task 3 will deliberately change).** This test documents today's substring-regex semantics so the diff in Task 3 is visible and intentional.

```go
func TestMatchHooks_CurrentRegexSubstring_Characterization(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "Bash", Type: "command", Command: "echo x"})
	// TODAY: "Bash" is compiled as a regex and substring-matches "BashOutput".
	// Task 3 makes this exact-mode (no match). When Task 3 lands, this test is
	// updated in the SAME commit to assert the new behavior; until then it pins today's bug.
	if got := r.MatchHooks(plugin.HookPreToolUse, "BashOutput"); len(got) != 1 {
		t.Fatalf("current behavior: Bash regex-substring-matches BashOutput; got %d", len(got))
	}
}
```

- [ ] **Step 2: Characterize the CURRENT exit-code / output behavior for the events that fire.** Lock the deny-on-exit-2 for PreToolUse, no-block for PostToolUse, block-on-`decision:block` for Stop, and additionalContext-folded-into-SystemMessage.

```go
func TestParseHookOutput_CurrentContracts_Characterization(t *testing.T) {
	// exit 2 => IsError, stdout becomes SystemMessage (current parseHookOutput).
	if o := parseHookOutput("boom", 2); !o.IsError || o.SystemMessage != "boom" {
		t.Fatalf("exit2 contract drifted: %+v", o)
	}
	// additionalContext currently folds into SystemMessage (07 says this must change in Task 5).
	o := parseHookOutput(`{"hookSpecificOutput":{"additionalContext":"ctx"}}`, 0)
	if o.SystemMessage != "ctx" {
		t.Fatalf("current: additionalContext folds into SystemMessage; got %q", o.SystemMessage)
	}
}
```

- [ ] **Step 3: Characterize current `Input` JSON wire shape** (the fields present today; later tasks add fields, never remove). Append to the existing `TestHookInput_JSON` neighborhood:

```go
func TestHookInput_CurrentWireShape_Characterization(t *testing.T) {
	b, _ := json.Marshal(Input{SessionID: "s", CWD: "/w", HookEventName: "PreToolUse", ToolName: "Write"})
	for _, want := range []string{`"session_id":"s"`, `"cwd":"/w"`, `"hook_event_name":"PreToolUse"`, `"tool_name":"Write"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
	// Aliases that MUST survive migration (07 §Event field names): assert they are still the emitted names today.
	bb, _ := json.Marshal(Input{UserPrompt: "p", ToolResult: "r", HookEventName: "X"})
	for _, want := range []string{`"user_prompt":"p"`, `"tool_result":"r"`} {
		if !strings.Contains(string(bb), want) {
			t.Fatalf("alias dropped: missing %s in %s", want, bb)
		}
	}
}
```

- [ ] **Step 4: Characterize current parser field set** (in `agent/plugin/hooks_test.go`) — that `type/command/prompt/timeout/model` parse and that an unknown handler field is currently silently dropped (Task 2 changes the drop into capture).

```go
func TestParsePluginHooks_CurrentlyDropsUnknownFields_Characterization(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo x","args":["a"],"shell":"bash"}]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil { t.Fatal(err) }
	// TODAY args/shell are not on RegisteredHook; this test asserts the command still parses.
	// Task 2 adds the fields; this test is updated in that commit to assert they are captured.
	if hooks[HookPreToolUse][0].Command != "echo x" {
		t.Fatalf("command parse drifted: %+v", hooks[HookPreToolUse][0])
	}
}
```

- [ ] **Step 5: Run all four** → PASS (they describe reality). `go build ./...`.

- [ ] **Step 6: Commit.**

```bash
git add agent/internal/hooks/hooks_test.go agent/plugin/hooks_test.go
git commit -m "test(hooks): characterize current matcher/output/input/parser behavior (phase-1 net)"
```

---

## Task 2: Parser — capture official fields, metadata, and recognized-but-unsupported events

**Spec:** 07 §"Phase A" steps 1-2, §"Formal config shape", §"Compatibility tiers". Tier: command/common fields = `claude-compatible-subset`; source metadata + recognized-but-unsupported registry = `serf-native`.

**Files:**
- Modify: `agent/plugin/hooks.go` (`RegisteredHook`, `hookSpec`, `parsePluginHooks`, new tier/recognized maps)
- Test: `agent/plugin/hooks_test.go`

- [ ] **Step 1: Failing test — new fields parse and metadata is threaded.**

```go
func TestParsePluginHooks_CapturesArgsShellAndMetadata(t *testing.T) {
	data := []byte(`{"PreToolUse":[{"matcher":"Bash","hooks":[
		{"type":"command","command":"x","args":["-c","y"],"shell":"bash","if":"Bash(rm *)","statusMessage":"checking","async":true,"asyncRewake":true}
	]}]}`)
	hooks, err := parsePluginHooks(data, "/p", "n")
	if err != nil { t.Fatal(err) }
	h := hooks[HookPreToolUse][0]
	if len(h.Args) != 2 || h.Args[0] != "-c" { t.Fatalf("args: %+v", h.Args) }
	if h.Shell != "bash" || h.If != "Bash(rm *)" || h.StatusMessage != "checking" { t.Fatalf("fields: %+v", h) }
	if !h.Async || !h.AsyncRewake { t.Fatalf("async flags: %+v", h) }
	if h.Event != HookPreToolUse || h.GroupIndex != 0 || h.HandlerIndex != 0 { t.Fatalf("metadata: %+v", h) }
}
```

- [ ] **Step 2: Failing test — recognized-but-unsupported Claude events are classified, not silently dropped.**

```go
func TestParsePluginHooks_RecognizedButUnsupportedEvent(t *testing.T) {
	data := []byte(`{"PostToolUseFailure":[{"matcher":"*","hooks":[{"type":"command","command":"x"}]}],
		"TotallyBogusEvent":[{"hooks":[{"type":"command","command":"y"}]}]}`)
	hooks, unsupported, unknown, err := parsePluginHooksDiag(data, "/p", "n")
	if err != nil { t.Fatal(err) }
	if len(hooks) != 0 { t.Fatalf("no supported events expected: %v", hooks) }
	if !unsupported[HookEvent("PostToolUseFailure")] { t.Fatal("PostToolUseFailure should be recognized-but-unsupported") }
	if !unknown["TotallyBogusEvent"] { t.Fatal("bogus event should be unknown") }
}
```

- [ ] **Step 3: Implement.** In `agent/plugin/hooks.go`:
  - Add to `RegisteredHook` (additive, after existing fields): `Args []string`, `Shell string`, `If string`, `Async bool`, `AsyncRewake bool`, `StatusMessage string`, and `serf-native` metadata `SourcePath string`, `Event HookEvent`, `GroupIndex int`, `HandlerIndex int`, plus `UnknownFields map[string]json.RawMessage` for harmless future fields. Doc-comment the field block with its tier.
  - Add to `hookSpec`: `Args []string`, `Shell string`, `If string`, `Async bool`, `AsyncRewake bool`, `StatusMessage string` with json tags matching 07 §"Formal config shape".
  - Add a `recognizedClaudeEvents` set (07 §"Event set" table — every Claude event name) and keep `validHookEvents` as the *serf-fires-these* set. An event in `recognizedClaudeEvents` but not `validHookEvents` is "recognized but unsupported."
  - Add `var hookEventTier = map[HookEvent]string{...}` mapping each `validHookEvents` entry to `"claude-compatible-subset"` (these fire) and recognized-but-unsupported to `"reserved-placeholder"`. Expose `func EventTier(e HookEvent) string`.
  - Add `parsePluginHooksDiag(data, dir, name) (map[HookEvent][]RegisteredHook, unsupported map[HookEvent]bool, unknown map[string]bool, error)`. Make `parsePluginHooks` a thin wrapper returning just the map+err (existing callers unchanged). Thread `SourcePath` (the file path when known; empty for inline), `Event`, `GroupIndex`, `HandlerIndex` into each `RegisteredHook`. Capture unknown handler keys into `UnknownFields` by re-unmarshalling each handler into `map[string]json.RawMessage` and removing known keys.
  - `discoverPluginHooks` passes the resolved file path as `sourcePath` so file-based hooks carry it.

- [ ] **Step 4: Update the Task-1 characterization test** `TestParsePluginHooks_CurrentlyDropsUnknownFields_Characterization` in the same commit to now assert `Args`/`Shell` are captured (rename it to `..._NowCaptured`). This is the deliberate behavior flip; the commit body records it.

- [ ] **Step 5: Run** both new tests + the full `./agent/plugin/...` suite → PASS. `go build ./...`.

- [ ] **Step 6: Commit.**

```bash
git add agent/plugin/hooks.go agent/plugin/hooks_test.go
git commit -m "feat(plugin/hooks): capture Claude handler fields + source metadata; classify recognized-but-unsupported events [claude-compatible-subset]"
```

---

## Task 3: Claude-compatible matcher semantics

**Spec:** 07 §"Matcher semantics", §"Phase A" step 3, acceptance criteria ("`Bash` no longer regex-substring matches `BashOutput`"). Tier: `claude-compatible-subset`. **Highest-impact task.**

**Files:**
- Create: `agent/internal/hooks/matcher.go`, `agent/internal/hooks/matcher_test.go`
- Modify: `agent/internal/hooks/hooks.go` (`MatchHooks` delegates to the helper; emit invalid-regex diagnostic)
- Test: update `agent/internal/hooks/hooks_test.go` characterization from Task 1

- [ ] **Step 1: Failing table test for the helper** (`matcher_test.go`), covering every 07 §"Matcher semantics" example row:

```go
func TestMatchTarget(t *testing.T) {
	cases := []struct{ matcher, target string; want bool; wantErr bool }{
		{"", "Bash", true, false},
		{"*", "anything", true, false},
		{"Bash", "Bash", true, false},
		{"Bash", "BashOutput", false, false},          // exact mode — the headline fix
		{"Edit|Write|MultiEdit", "Write", true, false},
		{"Edit|Write", "Read", false, false},
		{"mcp__memory__.*", "mcp__memory__search", true, false}, // regex (has '.')
		{"mcp__memory", "mcp__memory__search", false, false},    // exact, no substring
		{"mcp__.*__write.*", "mcp__fs__write_file", true, false},
		{"(", "Bash", false, true},                    // invalid regex => no match + error
	}
	for _, c := range cases {
		got, err := matchTarget(c.matcher, c.target)
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("matchTarget(%q,%q)=%v,err=%v want %v,err=%v", c.matcher, c.target, got, err, c.want, c.wantErr)
		}
	}
}
```

- [ ] **Step 2: Implement `matchTarget`** in `matcher.go`:
  1. trimmed matcher `""` or `"*"` → `(true, nil)`.
  2. matcher matches `^[A-Za-z0-9_|]+$` → split on `|`, exact-equal any segment → `(match, nil)`.
  3. else `regexp.Compile`; on error `(false, err)`; else `(re.MatchString(target), nil)`. Use `regexp` (Go RE2). Doc-comment the RE2 caveat (07 §Caveats) and the tier.

- [ ] **Step 3: Rewire `MatchHooks`** in `hooks.go` to call `matchTarget(hook.Matcher, target)`. On a non-nil error (invalid regex), skip the hook AND, if `r.onEvent` is set, emit a sanitized diagnostic carrying `event`, `matcher`, `hook.PluginName`, `hook.SourcePath` and error category `invalid_matcher` (reuse `HookStartData`/`HookEndData` shape or a minimal warning — see Task 7 for the diagnostic surface; here just ensure the skip is non-panicking and the matcher string is the only matcher data exposed). Delete the inline `regexp.Compile` and the `== "*"` short-circuit (now inside the helper).

- [ ] **Step 4: Flip the Task-1 matcher characterization** `TestMatchHooks_CurrentRegexSubstring_Characterization` → `TestMatchHooks_ExactModeNoSubstring`: assert `MatchHooks(PreToolUse, "BashOutput")` with matcher `"Bash"` now returns **0**. Same commit.

- [ ] **Step 5: Add negative-matcher coverage** to `hooks_test.go` per 07 acceptance ("Negative matcher tests cover non-substring behavior for tools and MCP names, pipe-list matching, invalid regex, empty matcher"): a pipe-list non-tool case, an MCP regex-vs-exact case, and an invalid-regex case asserting the hook is skipped (not run).

- [ ] **Step 6: Run** `./agent/internal/hooks/...` → PASS. Confirm `TestHookRunner_MatcherRegex` and `TestHookRunner_WildcardMatcher` still pass (they use `Write|Edit` and `*` — both still match correctly). `go build ./...`.

- [ ] **Step 7: Commit.**

```bash
git add agent/internal/hooks/matcher.go agent/internal/hooks/matcher_test.go agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go
git commit -m "feat(hooks): Claude-compatible matcher (empty/star, exact/pipe-list, regex) with RE2 caveat [claude-compatible-subset]"
```

---

## Task 4: Command `args` exec-form and `shell` selection

**Spec:** 07 §"`command`", §"Phase A" step 5, acceptance ("Command hooks support shell form and exec-form `args` without requiring authors to wrap every path in `bash -c`"). Tier: `claude-compatible-subset`.

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (`executeCommandHook`)
- Test: `agent/internal/hooks/hooks_test.go`

- [ ] **Step 1: Failing test — exec-form bypasses the shell and handles spaces.**

```go
func TestExecuteCommandHook_ExecFormArgs(t *testing.T) {
	hook := plugin.RegisteredHook{Type: "command", Command: "printf", Args: []string{"%s", "a b c"}, Timeout: 5, PluginDir: "/tmp"}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "PreToolUse"})
	if err != nil { t.Fatal(err) }
	if res.Stdout != "a b c" { t.Fatalf("exec-form stdout=%q want %q", res.Stdout, "a b c") }
}

func TestExecuteCommandHook_ExecForm_NoShellExpansion(t *testing.T) {
	// In exec form, $HOME must NOT be expanded by a shell.
	hook := plugin.RegisteredHook{Type: "command", Command: "printf", Args: []string{"%s", "$HOME"}, Timeout: 5, PluginDir: "/tmp"}
	res, _ := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "X"})
	if res.Stdout != "$HOME" { t.Fatalf("exec-form expanded shell var: %q", res.Stdout) }
}

func TestExecuteCommandHook_UnknownShellRejected(t *testing.T) {
	hook := plugin.RegisteredHook{Type: "command", Command: "echo x", Shell: "fish", Timeout: 5, PluginDir: "/tmp"}
	_, err := executeCommandHook(context.Background(), hook, Input{CWD: "/tmp", HookEventName: "X"})
	if err == nil { t.Fatal("unknown shell should error") }
}
```

- [ ] **Step 2: Implement** in `executeCommandHook`:
  - If `len(hook.Args) > 0`: `exec.CommandContext(ctx, hook.Command, hook.Args...)` — direct spawn, no shell. (07: `shell` is ignored when `args` is present.)
  - Else shell form: select shell from `hook.Shell` — `""`/`"bash"` → `bash -c <command>` (preserve today's exact behavior); `"powershell"` → reserved-placeholder, return a `command_error` "powershell shell not supported on this platform" rather than silently running bash (07 forbids silent half-support); any other value → error. Keep `sh`/`bash` choice exactly as current default so existing tests pass.
  - Env/stdin/stdout/stderr/exit-code/timeout handling is unchanged for both forms.

- [ ] **Step 3: Run** the three new tests + the existing `TestExecuteCommandHook*` (shell-form path must be untouched) → PASS. `go build ./...`.

- [ ] **Step 4: Commit.**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go
git commit -m "feat(hooks): command exec-form via args; explicit shell selection [claude-compatible-subset]"
```

---

## Task 5: Official input fields + split additionalContext from systemMessage

**Spec:** 07 §"Phase A" steps 7-8, §"Event field names", §"Hook output contract", acceptance ("Hook input includes official common fields … preserves legacy aliases"; "`additionalContext` is routed separately from user-visible `systemMessage`"). Tier: `claude-compatible-subset`.

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (`Input`, `parsedHookOutput`, `parseHookOutput`, result structs, `collectSystemMessages`, runner aggregation)
- Modify: `agent/session_events.go` (`hookInput`), `agent/session_tools.go` (`execTool`)
- Test: `agent/internal/hooks/hooks_test.go`

- [ ] **Step 1: Failing test — `Input` carries official fields and keeps aliases.**

```go
func TestHookInput_OfficialFields(t *testing.T) {
	b, _ := json.Marshal(Input{
		SessionID: "s", CWD: "/w", HookEventName: "PreToolUse", ToolName: "Bash",
		TranscriptPath: "/t.jsonl", PermissionMode: "default", ToolUseID: "call-1",
		ToolResponse: "ok", AgentID: "ag1", AgentType: "Explore",
		ToolResult: "ok", // legacy alias preserved
	})
	for _, w := range []string{`"transcript_path":"/t.jsonl"`, `"permission_mode":"default"`, `"tool_use_id":"call-1"`, `"tool_response":"ok"`, `"agent_id":"ag1"`, `"agent_type":"Explore"`, `"tool_result":"ok"`} {
		if !strings.Contains(string(b), w) { t.Fatalf("missing %s in %s", w, b) }
	}
}
```

- [ ] **Step 2: Failing test — additionalContext routed separately.**

```go
func TestParseHookOutput_AdditionalContextSeparate(t *testing.T) {
	o := parseHookOutput(`{"systemMessage":"user-visible","hookSpecificOutput":{"additionalContext":"model-ctx"}}`, 0)
	if o.SystemMessage != "user-visible" { t.Fatalf("systemMessage=%q", o.SystemMessage) }
	if o.AdditionalContext != "model-ctx" { t.Fatalf("additionalContext=%q", o.AdditionalContext) }
	// terminalSequence captured too
	o2 := parseHookOutput(`{"terminalSequence":""}`, 0)
	if o2.TerminalSequence != "" { t.Fatalf("terminalSequence=%q", o2.TerminalSequence) }
}
```

- [ ] **Step 3: Implement `Input` additions** (additive, json tags per 07 §"Event field names"): `TranscriptPath`, `PermissionMode`, `ToolUseID`, `ToolResponse`, `AgentID`, `AgentType`, all `,omitempty`. Keep `UserPrompt`/`ToolResult` exactly as-is (aliases). Doc-comment the alias block.

- [ ] **Step 4: Implement output split.** Add `AdditionalContext string` and `TerminalSequence string` to `parsedHookOutput`. In `parseHookOutput`, extract `hookSpecificOutput.additionalContext` into `result.AdditionalContext` (NOT folded into `SystemMessage`) and top-level `terminalSequence` into `result.TerminalSequence`. Add `AdditionalContext []string` and `TerminalSequences []string` to `RunResult`, `PreToolUseResult`, `StopResult`; populate them in the runner aggregation loops alongside `SystemMessages`. Update `collectSystemMessages` to also collect additionalContext/terminalSequence (rename internally if clearer, but keep `RunResult.SystemMessages` populated for existing callers).

- [ ] **Step 5: Route additionalContext at integration sites.** The existing call sites `s.Steer(msg)` user-visible system messages. Keep that for `SystemMessages`. For `AdditionalContext`, also `s.Steer(...)` for now (serf has one steering channel today) BUT add a `// TODO(phase-B): additionalContext is model-context; route distinctly from user-visible systemMessage once a context channel exists` and keep them as separate slices so the data is not lost. **Key:** the split is in the *data model* now (acceptance criterion met); the distinct *delivery channel* is Phase B. Confirm with Jesse if a separate channel is wanted in Phase 1 (see Open Questions).

- [ ] **Step 6: Populate official fields where values are in hand.** In `agent/session_events.go` `hookInput`: set `TranscriptPath` from `s.TranscriptPath()` (exists at `agent/session.go:544`; may be empty when persistence is off — fine). Set `Effort` from `s.cfg.ReasoningEffort` (reachable, see `agent/session_model_call.go:115`) when non-empty. Leave `PermissionMode` UNSET — serf has no permission-mode field on `Session` today; do not invent one (deferred, Open Question 2). In `agent/session_tools.go` `execTool`: set `hi.ToolUseID = call.ID` for PreToolUse and PostToolUse, and `hi.ToolResponse = res.FullOutput` for PostToolUse (alongside the existing `ToolResult`). No behavior change — these are additive fields the hook may read.

- [ ] **Step 7: Update the Task-1 characterization** `TestParseHookOutput_CurrentContracts_Characterization` — the additionalContext-folding assertion now flips to "separate"; update it in this commit (delete the folding sub-assertion, point to the new test).

- [ ] **Step 8: Run** `./agent/internal/hooks/...` and `go build ./...` (the integration edits must compile) → PASS. Run `go test ./agent/ -run 'Hook|Tool'` to confirm no integration regressions.

- [ ] **Step 9: Commit.**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go agent/session_events.go agent/session_tools.go
git commit -m "feat(hooks): official input fields + route additionalContext separately from systemMessage [claude-compatible-subset]"
```

---

## Task 6: Central exit-code table for currently-fired events

**Spec:** 07 §"Exit-code semantics", §"Phase A" step 9, acceptance ("Exit-code 2 behavior is event-specific and table-driven … JSON output is parsed only on exit 0 for command hooks"). Tier: `claude-compatible-subset`.

**Files:**
- Create: `agent/internal/hooks/exitcode.go`, `agent/internal/hooks/exitcode_test.go`
- Modify: `agent/internal/hooks/hooks.go` (`runHook` consults the table; command JSON parsed only on exit 0)
- Test: `agent/internal/hooks/hooks_test.go`

- [ ] **Step 1: Failing test — the table classifies exit 2 per event.** Only the events serf fires are in scope.

```go
func TestExitBehavior_PerEvent(t *testing.T) {
	// Blockable on exit 2:
	for _, e := range []plugin.HookEvent{plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop, plugin.HookUserPromptSubmit, plugin.HookPreCompact} {
		if !exitBehavior(e).BlockOnExit2 { t.Fatalf("%s should block on exit 2", e) }
	}
	// Non-blocking (stderr-to-user) on exit 2:
	for _, e := range []plugin.HookEvent{plugin.HookPostToolUse, plugin.HookSessionStart, plugin.HookSessionEnd, plugin.HookNotification} {
		if exitBehavior(e).BlockOnExit2 { t.Fatalf("%s must NOT block on exit 2", e) }
	}
}
```

- [ ] **Step 2: Failing test — command JSON is honored only on exit 0.**

```go
func TestRunHook_CommandJSONOnlyOnExit0(t *testing.T) {
	r := newRunner(nil, "")
	// exit 2 with a JSON body that *would* set continue:false — must be ignored; stderr path wins.
	h := plugin.RegisteredHook{Type: "command", Matcher: "*", Timeout: 5,
		Command: `echo '{"continue":false,"systemMessage":"json"}'; echo "stderr-msg" >&2; exit 2`}
	out := r.runHook(context.Background(), h, Input{HookEventName: "PreToolUse"})
	if !out.IsError { t.Fatal("exit2 should be IsError") }
	if out.SystemMessage == "json" { t.Fatal("JSON must be ignored on exit 2") }
	if out.Continue == false { /* must remain true: continue:false came from ignored JSON */ t.Fatal("continue must not be set by ignored JSON") }
}
```

- [ ] **Step 3: Implement the table** in `exitcode.go`:

```go
// eventExitPolicy is the exit-code contract for one event (07 §Exit-code semantics).
// Phase 1 covers only events serf currently fires; everything else defaults to
// non-blocking (reserved-placeholder) so an unimplemented event never blocks.
type eventExitPolicy struct {
	BlockOnExit2 bool // exit 2 blocks the action (else stderr shown to user only)
}

func exitBehavior(e plugin.HookEvent) eventExitPolicy {
	switch e {
	case plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop,
		plugin.HookUserPromptSubmit, plugin.HookPreCompact:
		return eventExitPolicy{BlockOnExit2: true}
	default: // PostToolUse, SessionStart, SessionEnd, Notification, and all reserved events
		return eventExitPolicy{BlockOnExit2: false}
	}
}
```

  Doc-comment the tier and that the full Claude table (07 §Exit-code semantics) is the source; Phase 1 implements the serf-fired subset.

- [ ] **Step 4: Thread the policy into the runner.** `runHook` currently parses on every exit code. Change `parseHookOutput` (or `runHook`) so that for **command** hooks JSON is parsed only when `ExitCode == 0`; on `ExitCode == 2`, set `IsError`, use stderr (existing behavior), and DO NOT attempt JSON parse. `runHook` needs the event to consult `exitBehavior`; pass `event` into `runHook`/`runAll` (it already has `event` in `runAll`). The PreToolUse deny-on-exit-2 (today `RunPreToolUse` checks `o.RawExitCode == 2`) is preserved because `PreToolUse.BlockOnExit2 == true`; keep `RunPreToolUse`/`runStopEvent` mapping but source the "is this a block" decision from `exitBehavior(event).BlockOnExit2` rather than a hard-coded `== 2`. Net effect: PostToolUse exit 2 no longer accidentally behaves like a deny; PreToolUse/Stop unchanged.

- [ ] **Step 5: Confirm the existing exit-2 tests still pass** — `TestHookRunner_PreToolUse_ExitCode2Denies` (PreToolUse, must still deny) and `TestParseHookOutput_ExitCode2` (still IsError). Add a `TestHookRunner_PostToolUse_ExitCode2_DoesNotBlock` asserting PostToolUse exit 2 produces a system message but no deny/block.

- [ ] **Step 6: Run** `./agent/internal/hooks/...` → PASS. `go build ./...`.

- [ ] **Step 7: Commit.**

```bash
git add agent/internal/hooks/exitcode.go agent/internal/hooks/exitcode_test.go agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go
git commit -m "feat(hooks): central exit-code table; command JSON parsed only on exit 0 [claude-compatible-subset]"
```

---

## Task 7: Official command env vars + tier-labeled hook diagnostics in /status

**Spec:** 07 §"Phase A" step 6, §"Common environment variables for command hooks", §"Diagnostics and status", §"Compatibility tiers" ("labeled … in status output, diagnostics"). Tier: official env = `claude-compatible-subset`; `PLUGIN_ROOT` + the status surfacing = `serf-native`.

**Files:**
- Modify: `agent/internal/hooks/hooks.go` (`executeCommandHook` env)
- Modify: `agent/status.go` (`DetailedStatus` hook section: add tier + recognized-but-unsupported counts)
- Modify: `agent/session_init.go` (thread unsupported/unknown event diagnostics from `parsePluginHooksDiag` into the runner/status — minimal)
- Test: `agent/internal/hooks/hooks_test.go`, `agent/status` test if one exists (else a focused new test)

- [ ] **Step 1: Failing test — official env vars are set, alias retained.**

```go
func TestExecuteCommandHook_OfficialEnv(t *testing.T) {
	hook := plugin.RegisteredHook{Type: "command", Timeout: 5, PluginDir: "/pd",
		Command: "echo EFFORT=$CLAUDE_EFFORT REMOTE=$CLAUDE_CODE_REMOTE PLUGIN=$PLUGIN_ROOT CR=$CLAUDE_PLUGIN_ROOT"}
	res, err := executeCommandHook(context.Background(), hook, Input{CWD: "/proj", HookEventName: "X", Effort: "high"})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(res.Stdout, "PLUGIN=/pd") || !strings.Contains(res.Stdout, "CR=/pd") {
		t.Fatalf("plugin env missing: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "EFFORT=high") { t.Fatalf("CLAUDE_EFFORT missing: %q", res.Stdout) }
}
```

  (This requires an `Effort string` field on `Input`, json `effort` per 07 — add it additively; 07 models `effort` as `{level}` but serf may carry a flat string. If serf has no effort value at the hook site, set the env only when non-empty and have the test pass `Effort: "high"` directly.)

- [ ] **Step 2: Implement env additions** in `executeCommandHook`: append `CLAUDE_EFFORT` (when `input.Effort != ""` — the value is threaded from `s.cfg.ReasoningEffort` via Task 5 step 6). Do NOT set `CLAUDE_CODE_REMOTE` in Phase 1: serf has no remote/serve signal reachable at the hook exec site, and 07/Diagnostics forbid fabrication — leave it deferred (note in the commit). Keep `CLAUDE_PLUGIN_ROOT`, `PLUGIN_ROOT`, `CLAUDE_PROJECT_DIR` exactly as today. Do not set `CLAUDE_PLUGIN_DATA`/`CLAUDE_ENV_FILE` (those belong to deferred features — 07 reserved). **Never** put secrets in env diagnostics (07 §Diagnostics).

- [ ] **Step 3: Failing test — /status reports tier + unsupported counts.** Build a `DetailedStatus` from a session whose plugin registered a supported hook and a recognized-but-unsupported one; assert the supported event shows tier `claude-compatible-subset` and the unsupported event is listed as reserved/unsupported (not as an active hook). (Model on however `DetailedStatus` is currently tested; if there is no test, add a minimal one constructing the runner + status directly.)

- [ ] **Step 4: Implement status surfacing** (additive to `DetailedStatus`): keep `Hooks map[plugin.HookEvent]int` for back-compat; add a parallel field, e.g. `HookEvents []HookEventStatus` where `HookEventStatus{Event, Count, Tier, Supported bool}`. Populate `Tier` from `plugin.EventTier`. Surface recognized-but-unsupported events (from the diag parse threaded via `session_init.go`) with `Supported:false`, `Count:0`, tier `reserved-placeholder`. This satisfies 07's "active supported hooks / recognized Claude hooks skipped because unsupported / unknown events" distinction at the data layer; the TUI/web rendering of the new field is out of scope for Phase 1 (note it in the commit).

- [ ] **Step 5: Run** the env test, the status test, and `go build ./...` → PASS. `make lint` (naming/internal/docs gates — ensure new exported `HookEventStatus`/`EventTier` have doc comments).

- [ ] **Step 6: Commit.**

```bash
git add agent/internal/hooks/hooks.go agent/internal/hooks/hooks_test.go agent/status.go agent/session_init.go
git commit -m "feat(hooks): official command env vars; tier-labeled hook diagnostics in /status [claude-compatible-subset/serf-native]"
```

---

## Task 8: Phase-1 documentation pass on spec 07 status + caveats

**Spec:** 07 §"Compatibility tiers" (label in docs), §"Caveats", §Acceptance criteria. Tier: documentation only.

**Files:**
- Modify: `docs/subagent-management/07-lifecycle-hooks-claude-compat.md` (mark Phase-A items shipped; record the Go RE2 caveat as now-active; note deferred items remain reserved)

- [ ] **Step 1: Update 07** — in the §"Compatibility tiers" "Near-term subset targets" list and the §"Event set" table, mark as **implemented (`claude-compatible-subset`)**: Claude-compatible matcher semantics, command `args` exec-form, official common input fields, the additionalContext/systemMessage split, the event-specific exit-code table (for fired events), and official command env vars. Leave `http`/`mcp_tool`/`agent`, new events, and `PreToolUse` preferred output schema explicitly marked deferred to Phase B/C with their tiers. Add one line under §Caveats confirming the Go RE2 matcher is the active implementation with the documented JS-regex caveat.

- [ ] **Step 2: Verify** `make lint` (`serf-docscheck`) passes on the edited doc. Do NOT change line-reference anchors that other docs depend on unless they are now wrong.

- [ ] **Step 3: Commit.**

```bash
git add docs/subagent-management/07-lifecycle-hooks-claude-compat.md
git commit -m "docs(hooks): mark phase-1 Claude-compat items shipped; record RE2 caveat"
```

---

## Final verification

- [ ] `go build ./...` clean.
- [ ] `go test ./agent/internal/hooks/... ./agent/plugin/...` green; `go test ./agent/ -run 'Hook|Tool|Status'` green (integration sites unregressed).
- [ ] `make test` green across all modules.
- [ ] `make lint` clean across all modules (golangci + namingcheck + internalcheck + docscheck). New exported symbols (`EventTier`, `HookEventStatus`, `parsePluginHooksDiag` is unexported) carry doc comments.
- [ ] Acceptance walk-through against 07 §"Acceptance criteria": tick each Phase-A line that is in scope —
  - existing 9-event hooks still load+run ✔ (Task 1 net + every task preserves them);
  - wrapper/direct JSON still parse ✔;
  - `Bash` no longer substring-matches `BashOutput`, `Edit|Write` exact-list ✔ (Task 3);
  - MCP `mcp__memory__.*` matches but `mcp__memory` does not ✔ (Task 3);
  - command shell-form + exec-form `args` ✔ (Task 4);
  - input has official fields + legacy aliases ✔ (Task 5);
  - `additionalContext` routed separately (data model) ✔ (Task 5);
  - exit-2 event-specific + table-driven, JSON only on exit 0 ✔ (Task 6);
  - unsupported events reported as reserved, not pretended-fired ✔ (Task 2 + Task 7);
  - invalid regex diagnoses + skips, no panic ✔ (Task 3).
- [ ] Deferred items confirmed still `reserved-placeholder`/Phase-B/C and NOT advertised as working (07 doc reflects this — Task 8).
- [ ] No new package, no new third-party dependency, no event bus introduced (architecture constraint).

## Out of scope (later phases)

- Phase B: new events (`PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `UserPromptExpansion`, `StopFailure`); `PreToolUse` preferred output schema (`allow|deny|ask|defer`, deprecated mapping, revalidation); `PermissionRequest`/`PermissionDenied` decision objects; top-level `decision:block` for the broader event set; `if` permission-rule evaluation; distinct model-context delivery channel for `additionalContext`.
- Phase C: `http`, `mcp_tool`, `agent` handler types; `prompt` `$ARGUMENTS` + `{ok,reason}`; async/asyncRewake execution.
- Phase D: lifecycle/watch/team events gated on real serf boundaries.
- Phase E: typed SDK lifecycle hooks.

## Open questions for Jesse (resolve before/within Task 5 and Task 7)

1. **additionalContext delivery (Task 5 step 5):** Phase 1 splits additionalContext from systemMessage in the *data model* (meets the acceptance criterion) but still delivers both via the single `Steer` channel. Is a distinct model-context delivery path wanted in Phase 1, or is the data-model split sufficient until Phase B? (Recommendation: data-model split now, distinct channel in Phase B — keeps Phase 1 additive and low-risk.)
2. **Fields serf cannot source today (Tasks 5, 7):** `transcript_path` and `effort` ARE reachable (`s.TranscriptPath()`, `s.cfg.ReasoningEffort`) and will be populated. `permission_mode` and `CLAUDE_CODE_REMOTE` have NO reachable value on `Session` today, so Phase 1 omits them (never fabricates) rather than threading new session plumbing. Confirm "omit when unknown, plumb later in Phase B" is acceptable. (Recommendation: omit now — serf's permission model is not Claude's, and inventing a `permission_mode` value would be a lie the hook reads.)
