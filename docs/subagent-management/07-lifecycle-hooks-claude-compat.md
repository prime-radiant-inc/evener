# Lifecycle Hooks and Claude Compatibility

Status: Proposed evergreen spec. Serf currently supports a small Claude-compatible plugin hook subset; this spec defines the lifecycle hook contract, compatibility tiers, and phased implementation plan needed to make hook behavior explicit without claiming full Claude Code parity before it is implemented and tested.

## Purpose

Define one lifecycle hook model for Serf that can serve three distinct surfaces without conflating them:

1. **Plugin compatibility hooks** loaded from plugin/config JSON and executed as external handlers.
2. **Serf-native lifecycle/SDK hooks** exposed as typed in-process callbacks for embedders.
3. **Internal runtime events** emitted for session, tool, subagent, compaction, diagnostics, and client projections.

The design preserves existing Serf behavior where it works, fixes compatibility gaps that can break real Claude hook configurations, and reserves explicit extension points for current Claude Code hook semantics documented at <https://code.claude.com/docs/en/hooks>.

## Goals

- Keep current simple plugin hooks working: `hooks/hooks.json`, wrapper/direct JSON formats, `command` handlers, `prompt` handlers, and the currently recognized Serf hook events.
- Define the exact Claude-compatible config shape Serf should parse.
- Define the event set, matcher targets, handler fields, input JSON, output JSON, HTTP/API behavior, and exit-code semantics Serf should converge toward.
- Separate compatibility plugin hooks from Serf-native typed callbacks and non-blocking runtime events.
- Fix high-impact compatibility gaps first: matcher semantics, command exec form, common input fields, output parsing, additional context routing, and event-specific exit-code behavior.
- Provide compatibility tiers so docs, tests, CLI output, and API surfaces do not imply unsupported Claude parity.
- Keep implementation incremental, DRY, and YAGNI: add small tables/helpers near existing hook parser/runner code instead of introducing a broad event-bus or schema framework.
- Ensure hook diagnostics identify the event/plugin/source while redacting hook payloads, headers, env values, tool inputs, and provider secrets.

## Non-goals

- No full Claude Code parity claim until all advertised events, fields, handler types, matcher behavior, output schemas, timeouts, and environment variables are implemented and tested.
- No replacement of plugin hooks with SDK callbacks.
- No second internal event bus if existing `agent/events` and session publication can project the lifecycle spine.
- No hidden workflow scheduler, task graph runner, or agent-team behavior solely to support hook names that Serf does not otherwise implement.
- No execution of untrusted hooks during plugin validation.
- No JS-regex engine dependency unless exact Claude regex parity is deliberately required; Go RE2 matching may be an explicitly documented compatibility caveat.
- No silent half-support for unsupported events, handler types, fields, or decisions.

## Source anchors

### Official compatibility source

- Claude Code hooks documentation: <https://code.claude.com/docs/en/hooks>. This spec treats that page as the compatibility source of truth for Claude hook config shape, event names, matcher semantics, handler types, input/output rules, timeout defaults, and exit-code behavior.

### Current Serf code anchors

- `agent/plugin/hooks.go` defines the current hook event enum and recognizes only `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, `PreCompact`, and `Notification` (`agent/plugin/hooks.go:14-33`, `agent/plugin/hooks.go:51-62`).
- `agent/plugin/hooks.go` defines `RegisteredHook` with only `Matcher`, `Type`, `Command`, `Prompt`, `Timeout`, `Model`, `PluginName`, and `PluginDir` (`agent/plugin/hooks.go:64-74`).
- `agent/plugin/hooks.go` currently parses only `type`, `command`, `prompt`, `timeout`, and `model` for each hook handler (`agent/plugin/hooks.go:82-89`).
- `agent/plugin/hooks.go` already accepts Claude-style wrapper JSON (`{"hooks": {...}}`) and direct event-at-top-level JSON, expands plugin-root placeholders in command/prompt strings, accepts inline manifest hooks, manifest hook paths, and default `<pluginDir>/hooks/hooks.json` discovery (`agent/plugin/hooks.go:91-203`).
- `agent/internal/hooks/hooks.go` defines the current hook input JSON as `session_id`, `cwd`, `hook_event_name`, `tool_name`, `tool_input`, `tool_result`, `user_prompt`, `message`, and `reason` (`agent/internal/hooks/hooks.go:22-33`).
- `agent/internal/hooks/hooks.go` executes command hooks as `bash -c <command>`, sets `CLAUDE_PLUGIN_ROOT`, `PLUGIN_ROOT`, and `CLAUDE_PROJECT_DIR`, and does not support exec-form `args`, `shell`, HTTP, MCP, agent, async, or full Claude env behavior (`agent/internal/hooks/hooks.go:42-87`).
- `agent/internal/hooks/hooks.go` uses local prompt substitution variables such as `$TOOL_INPUT`, `$TOOL_RESULT`, `$USER_PROMPT`, `$MESSAGE`, and `$TOOL_NAME` (`agent/internal/hooks/hooks.go:104-135`).
- `agent/internal/hooks/hooks.go` matches hook matchers as Go regex except `*`, which diverges from Claude exact/list/JS-regex semantics (`agent/internal/hooks/hooks.go:214-232`).
- `agent/internal/hooks/hooks.go` currently aggregates different fields depending on event result type: generic runs collect `SystemMessages`, `PreToolUse` additionally collects `Denied` and `UpdatedInput`, and stop-style hooks collect `Blocked`. It does not separately route `additionalContext`, terminal sequences, permission-request decisions, retries, updated permissions, updated tool output, or event-specific schemas (`agent/internal/hooks/hooks.go:234-333`).
- `agent/internal/hooks/hooks.go` executes only `command` and `prompt` handlers and ignores other handler types (`agent/internal/hooks/hooks.go:335-350`).
- `agent/internal/hooks/hooks.go` emits `HOOK_START` and `HOOK_END` callbacks around each hook run with event, type, matcher, plugin name, exit code, and duration (`agent/internal/hooks/hooks.go:364-405`). Current `RegisteredHook` metadata does not retain source path/group/handler indexes, so runtime diagnostics cannot include those fields until the parser stores them.
- `agent/internal/hooks/hooks.go` has current runner methods for `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, `PreCompact`, and `Notification` (`agent/internal/hooks/hooks.go:407-523`).
- `agent/events/events.go` already includes public session event kinds for session, tool, subagent, plugin, hook, compaction, warning, and error projections (`agent/events/events.go:17-79`).
- `agent/events/payloads.go` defines current `HookStartData` and `HookEndData` payloads (`agent/events/payloads.go:207-223`).
- Cross-cutting compatibility vocabulary should align with `docs/subagent-management/10-runtime-contracts.md`, especially the tiers `serf-native`, `claude-compatible-subset`, `reserved-placeholder`, and `experimental`.

## Compatibility tiers

Every hook feature must be labeled with exactly one tier in docs, status output, diagnostics, and tests.

### `serf-native`

Typed or internal behavior owned by Serf. This includes internal lifecycle events, SDK callbacks, Serf-specific diagnostics, and plugin aliases such as `PLUGIN_ROOT` retained for backwards compatibility. Serf-native behavior may be stable without matching Claude exactly.

### `claude-compatible-subset`

Behavior implemented and tested to match Claude Code docs closely enough for normal plugin/config portability. A subset feature must include parser support, runtime support, diagnostics, tests, and documented caveats.

Currently implemented Serf hook support (not all of this is `claude-compatible-subset` yet):

- wrapper/direct hook JSON parsing;
- default plugin hook file discovery at `hooks/hooks.json`;
- event names currently recognized by Serf;
- `command` and Serf-native `prompt` handler execution where currently accepted; Claude-compatible handler support is event-specific per the handler support table;
- simple `SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionEnd`, `PreCompact`, and `Notification` dispatch.

Near-term subset targets:

- Claude-compatible matcher semantics; ✅ implemented (Phase 1) — `agent/internal/hooks/matcher.go`: empty/`*`=all, `[A-Za-z0-9_|]+`=exact/pipe-list, else Go RE2 regex; invalid regex skips+diagnoses, never panics.
- command `args` exec-form + explicit `shell` selection; ✅ implemented (Phase 1) — bash default; powershell and unknown shells produce an explicit error.
- official common input fields; ✅ implemented (Phase 1) — `transcript_path`, `permission_mode`, `tool_use_id`, `tool_response`, `agent_id`, `agent_type`, `effort` added; legacy aliases `user_prompt`/`tool_result` preserved; `permission_mode`/`agent_id`/`agent_type`/`CLAUDE_CODE_REMOTE` omitted (no reachable value in serf, never fabricated).
- `additionalContext`/`systemMessage` routed separately in the data model; ✅ implemented (Phase 1) — distinct delivery channel deferred to Phase B.
- event-specific exit-code behavior (central table for the 9 fired events); ✅ implemented (Phase 1) — exit-2 blocks for `PreToolUse`/`Stop`/`SubagentStop`/`UserPromptSubmit`/`PreCompact`; non-blocking for `PostToolUse`/`SessionStart`/`SessionEnd`/`Notification`; JSON parsed only on exit 0.
- official command env vars: `CLAUDE_EFFORT` (when known); ✅ implemented (Phase 1) — `PLUGIN_ROOT` alias retained (`serf-native`).
- tier-labeled `/status` hook diagnostics (`HookEventStatus` with Event/Count/Tier/Supported; recognized-but-unsupported surfaced); ✅ implemented (Phase 1) — `serf-native`; TUI/web rendering deferred.
- `http` handler support; ⏳ DEFERRED — Phase C.

### `reserved-placeholder`

Claude-documented behavior that Serf recognizes as a future compatibility target but does not yet execute. Reserved placeholders should parse or diagnose predictably. They must not be advertised as working.

Examples: `Setup`, `InstructionsLoaded`, `MessageDisplay`, `TaskCreated`, `TaskCompleted`, `TeammateIdle`, `CwdChanged`, `FileChanged`, `WorktreeCreate`, `WorktreeRemove`, `Elicitation`, `ElicitationResult`, advanced `updatedPermissions`, `watchPaths`, `reloadSkills`, async re-wake, and exact JS regex features unsupported by Go RE2.

The following items from the Phase A near-term subset list were NOT shipped in Phase 1 and remain deferred:

- `http`/`mcp_tool`/`agent` handler types — DEFERRED Phase C.
- `PreToolUse` preferred output schema (`permissionDecision`: `allow|deny|ask|defer`) — DEFERRED Phase B.
- `if` permission-rule evaluation — DEFERRED Phase B.
- Async command execution (`async`/`asyncRewake`) — DEFERRED Phase C.
- Distinct `additionalContext` delivery channel to model (data-model split is Phase 1; actual delivery is) — DEFERRED Phase B.
- SDK typed lifecycle hooks — DEFERRED Phase E.

New events not yet fired by Serf (`PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `UserPromptExpansion`, `StopFailure`, plus the lifecycle/team/worktree set) remain reserved-placeholder.

### `experimental`

Implemented but intentionally unstable behavior. Experimental hooks must be clearly marked in user-visible docs and diagnostics. Claude `agent` handler parity should start here even if Serf has subagent primitives, because handler behavior, timeout, model selection, file access, and `{ok, reason}` response handling need careful policy tests.

## Hook surface separation

### Plugin compatibility hooks

Plugin hooks are external compatibility hooks loaded from plugin/config JSON. They receive JSON input and may return exit-code, text, or JSON output. They are subject to plugin trust policy, timeout policy, event-specific blocking rules, and secret redaction.

Plugin hooks must not bypass:

- effective tool policy;
- parent-to-child subagent restrictions;
- provider/model feature policy;
- session cancellation/closed state;
- final execution policy after hook-updated inputs are applied.

### Serf-native SDK hooks

SDK hooks are typed in-process callbacks for embedders. Suggested shape:

```go
type LifecycleHooks struct {
    OnSessionStart  func(context.Context, SessionStartEvent) error
    OnSessionEnd    func(context.Context, SessionEndEvent) error
    OnToolPolicy    func(context.Context, ToolPolicyEvent) error
    OnToolStart     func(context.Context, ToolStartEvent) error
    OnToolEnd       func(context.Context, ToolEndEvent) error
    OnSubagentStart func(context.Context, SubagentStartEvent) error
    OnSubagentEnd   func(context.Context, SubagentEndEvent) error
    OnCompaction    func(context.Context, CompactionEvent) error
}
```

Rules:

- SDK hooks use Go types, not Claude JSON payloads.
- SDK hooks honor `context.Context` cancellation.
- SDK hooks do not parse Claude hook JSON.
- Blocking SDK hooks, if allowed, must have explicit ordering relative to plugin hooks and execution policy.
- SDK hooks cannot expand a child/subagent capability set beyond effective policy.

### Internal lifecycle events

Internal events are observations used by CLI/TUI/Hub/API projections, logs, metrics, and tests. They should extend the existing `agent/events` package additively where practical. Events are non-blocking unless a separate hook contract explicitly makes them blocking.

## Canonical lifecycle order

Existing event names may remain, but ordering must be stable and tested.

### Session lifecycle

1. Resolve session config/profile/provider policy.
2. Build effective root capability set.
3. Fire Serf-native `SessionStarting` observation if added.
4. Run Claude-compatible `SessionStart` hooks for matcher `startup`, `resume`, `clear`, or `compact` before the first model turn when they can add initial context.
5. Emit Serf `SESSION_START` / client event.
6. Process user/model/tool turns.
7. Run `PreCompact` before compaction and `PostCompact` after compaction once implemented.
8. Run `Stop` before allowing an assistant/session stop if configured and if not in a loop-guarded retry path.
9. Run `SessionEnd` for final cleanup/notification only.
10. Emit Serf `SESSION_END` or error/cancellation event.

### Tool lifecycle

1. Resolve effective tool catalog.
2. Apply visibility policy before model request.
3. Model emits a tool call.
4. Decode the tool name and parse arguments best-effort for hook input. Current Serf runs `PreToolUse` before registry schema validation so hooks can inspect or rewrite raw input; changing that ordering is a compatibility migration.
5. Run compatibility `PreToolUse` hooks matching the Claude tool name.
6. Apply hook `updatedInput`.
7. Validate the final arguments against schema.
8. Run final execution policy on the final validated input, currently represented by registry lookup, schema validation, and middleware/policy checks.
9. Run typed SDK `OnToolPolicy` only if it is explicitly designed to observe/block at this boundary.
10. Emit `TOOL_CALL_START`.
11. Execute the tool.
12. Emit `TOOL_CALL_END` or failure/error event.
13. Run `PostToolUse` or `PostToolUseFailure` hooks with sanitized result/error.
14. After a batch of tool calls, run `PostToolBatch` before the next model request once batching is implemented.

### Subagent lifecycle

1. Decode spawn request.
2. Resolve agent type and plugin/builtin/project agent definition.
3. Compute effective child policy.
4. Create child session/job so hook input can include stable child identity/session metadata.
5. Run `SubagentStart` compatibility/SDK hooks before the child model receives initial context if implemented.
6. Emit `SUBAGENT_START`. Current code differs by path: initial spawn starts the child goroutine before emitting `SUBAGENT_START`, while idle resume emits `SUBAGENT_START` before starting the resumed goroutine. Treat any ordering change as deliberate and regression-tested.
7. Run child turns.
8. On completion, failure, cancellation, or close, run `SubagentStop` if configured.
9. Finalize result/diagnostics/status.
10. Emit `SUBAGENT_END` and release waiters.

## Claude-compatible hook config contract

Serf should parse the Claude hook config shape exactly enough to preserve unsupported fields for diagnostics and support future phases.

### File locations and wrapper shape

Plugin hooks may be supplied inline in the manifest, referenced by a manifest-specified hook path, or discovered from default `hooks/hooks.json`. File/object contents may contain an optional top-level `description` and a `hooks` object:

```json
{
  "description": "Optional human description",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Edit",
        "hooks": [
          {
            "type": "command",
            "command": "${CLAUDE_PLUGIN_ROOT}/hooks/check.sh",
            "timeout": 30
          }
        ]
      }
    ]
  }
}
```

Serf may continue accepting direct format for compatibility:

```json
{
  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [
        {"type": "command", "command": "echo ok"}
      ]
    }
  ]
}
```

### Formal config shape

```go
type HookFile struct {
    Description string                 `json:"description,omitempty"`
    Hooks       map[HookEvent][]Group  `json:"hooks"`
}

type Group struct {
    Matcher string        `json:"matcher,omitempty"`
    Hooks   []HookHandler `json:"hooks"`
}

type HookHandler struct {
    Type string `json:"type"`

    // common fields
    If            string `json:"if,omitempty"`
    Timeout       int    `json:"timeout,omitempty"`
    StatusMessage string `json:"statusMessage,omitempty"`
    Once          bool   `json:"once,omitempty"`

    // command
    Command     string   `json:"command,omitempty"`
    Args        []string `json:"args,omitempty"`
    Async       bool     `json:"async,omitempty"`
    AsyncRewake bool     `json:"asyncRewake,omitempty"`
    Shell       string   `json:"shell,omitempty"`

    // http
    URL            string            `json:"url,omitempty"`
    Headers        map[string]string `json:"headers,omitempty"`
    AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"`

    // mcp_tool
    Server string         `json:"server,omitempty"`
    Tool   string         `json:"tool,omitempty"`
    Input  map[string]any `json:"input,omitempty"`

    // prompt and agent
    Prompt string `json:"prompt,omitempty"`
    Model  string `json:"model,omitempty"`

    Raw           json.RawMessage            `json:"-"`
    UnknownFields map[string]json.RawMessage `json:"-"`
}
```

Validation rules:

- `type` is required.
- `hooks` arrays must be arrays; malformed present hook config is an error with source path, event, matcher, and handler index.
- Unknown event names should be reported as unknown unless they are known Claude events not implemented by Serf, in which case report `recognized but unsupported`.
- Unknown handler fields should be preserved for diagnostics or ignored explicitly; do not fail on harmless future fields unless they create ambiguous behavior.
- Unsupported handler types should not abort plugin load by default if they are in a future/reserved tier; they should be visible in status/diagnostics as unsupported and skipped until implemented.

## Event contract

### Event set

Serf should track the full Claude-documented event vocabulary, but only fire events that have a real Serf runtime boundary.

| Event | Compatibility status | Matcher target | Notes |
|---|---|---|---|
| `SessionStart` | implemented, compatibility-incomplete | `startup`, `resume`, `clear`, `compact` | Currently implemented for startup-kind matching; command timeout/input parity remains future compatibility work. |
| `Setup` | reserved-placeholder | setup trigger such as `init` or `maintenance` | Only implement if Serf has an equivalent setup boundary. |
| `InstructionsLoaded` | reserved-placeholder | load reason | Context-only/notification until Serf has matching instruction reload semantics. |
| `UserPromptSubmit` | implemented, compatibility-incomplete | no matcher | Currently runs after `USER_INPUT` emit, transcript append, and namer launch; hook messages are queued via `Steer` and drained before the model request. Blocking/erase-prompt parity still needs a placement redesign. |
| `UserPromptExpansion` | reserved-placeholder | command name | Implement only if slash/prompt expansion has a hook boundary. |
| `MessageDisplay` | reserved-placeholder | no matcher | Display-only; must not mutate transcript/model context. |
| `PreToolUse` | implemented, compatibility-incomplete | tool name | Currently runs before registry schema validation and can rewrite input; Phase A defines the compatibility target. |
| `PermissionRequest` | reserved-placeholder | tool name | Requires Serf approval/permission request boundary. |
| `PostToolUse` | implemented, compatibility-incomplete | tool name | Cannot undo or block tool execution; can add context/messages. |
| `PostToolUseFailure` | reserved-placeholder | tool name | Use official `error`, optional `is_interrupt`, and `duration_ms` fields. |
| `PostToolBatch` | reserved-placeholder | no matcher | Runs before next model call after a tool batch. |
| `PermissionDenied` | reserved-placeholder | tool name | Exit code ignored; JSON `retry` controls retry. |
| `Notification` | implemented, compatibility-incomplete | notification type target not wired in current Serf | User-visible/logging side effects only; current runner matches with an empty target. |
| `SubagentStart` | reserved-placeholder | agent type | Implement at child setup boundary. |
| `SubagentStop` | implemented, compatibility-incomplete | agent type target not wired in current Serf | Currently shares stop runner; needs agent matcher target and stop-loop guard. |
| `TaskCreated` | reserved-placeholder | no matcher | Implement only if Serf task store exposes rollback-capable creation hook. |
| `TaskCompleted` | reserved-placeholder | no matcher | Implement only if completion can be blocked safely. |
| `Stop` | implemented, compatibility-incomplete | no matcher | Blocks stopping when requested by current JSON/exit semantics; full Claude output parity remains Phase A/B work. |
| `StopFailure` | reserved-placeholder | no matcher | Exit/output ignored per Claude; diagnostics only. |
| `TeammateIdle` | reserved-placeholder | no matcher | Only if Serf adds teammate/team runtime. |
| `ConfigChange` | reserved-placeholder | config source | Can block config except policy settings in Claude; Serf needs explicit config reload boundary. |
| `CwdChanged` | reserved-placeholder | no matcher | Requires current working directory change boundary and optional env-file behavior. |
| `FileChanged` | reserved-placeholder | literal filenames/watch list | Special matcher/watch semantics; do not use normal matcher rules. |
| `WorktreeCreate` | reserved-placeholder | no matcher | Any non-zero aborts creation in Claude; only implement with worktree feature. |
| `WorktreeRemove` | reserved-placeholder | no matcher | Failures debug/log only. |
| `PreCompact` | implemented, compatibility-incomplete | `manual`/`auto` target not wired in current Serf | Currently fires; blocking parity and matcher target need completion. |
| `PostCompact` | reserved-placeholder | `manual`, `auto` | Add when post-compaction boundary exists. |
| `SessionEnd` | implemented, compatibility-incomplete | session end reason target not wired in current Serf | Present in Serf current set; current runner matches with an empty target. |
| `Elicitation` | reserved-placeholder | MCP server name | Requires MCP elicitation support. |
| `ElicitationResult` | reserved-placeholder | MCP server name | Requires MCP elicitation support. |

### Event field names

For Claude-compatible JSON payloads, prefer official field names and keep old Serf aliases temporarily where already used.

Common fields:

```json
{
  "session_id": "string",
  "transcript_path": "string",
  "cwd": "string",
  "permission_mode": "default|plan|acceptEdits|auto|dontAsk|bypassPermissions",
  "effort": {"level": "low|medium|high|xhigh|..."},
  "hook_event_name": "PreToolUse",
  "agent_id": "string",
  "agent_type": "string"
}
```

Tool event fields:

```json
{
  "tool_name": "Bash",
  "tool_use_id": "call-id",
  "tool_input": {"key": "value"},
  "tool_response": "string or structured result",
  "tool_result": "legacy Serf alias during migration",
  "error": "error text for PostToolUseFailure",
  "is_interrupt": false,
  "duration_ms": 123
}
```

Prompt/message fields:

```json
{
  "prompt": "current user prompt",
  "user_prompt": "legacy Serf alias during migration",
  "message": "notification or display text"
}
```

Stop/compact/config/session fields should use official names where documented, for example `reason`, `trigger`, `source`, `error`, `error_details`, and `last_assistant_message` rather than older local guesses.

## Matcher semantics

Matcher compatibility is a Phase A requirement because it changes which hooks fire.

### General matcher rules

For all supported events except `FileChanged`:

1. Omitted matcher, empty string, or `"*"` matches all.
2. If the matcher contains only ASCII letters, digits, underscore, or pipe (`[A-Za-z0-9_|]+`), treat it as exact string matching or a pipe-separated exact list.
3. Otherwise, treat it as a regular expression.
4. Claude specifies JavaScript regular expressions. Serf may initially implement this with Go RE2 and document unsupported JS-only constructs such as lookbehind/backreferences, or add a JS regex engine if exact parity becomes required.
5. Invalid regex matchers must not panic. They should skip the hook and emit a sanitized diagnostic with plugin name, event, matcher, and source file.

Examples:

| Matcher | Target | Result |
|---|---:|---|
| omitted / `""` / `"*"` | any | match |
| `"Bash"` | `Bash` | match |
| `"Bash"` | `BashOutput` | no match; exact mode |
| `"Edit|Write|MultiEdit"` | `Write` | match exact alternative |
| `"mcp__memory__.*"` | `mcp__memory__search` | match regex |
| `"mcp__memory"` | `mcp__memory__search` | no match; exact mode |

### Event-specific matcher targets

- Tool events (`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`): Claude tool name, including `mcp__<server>__<tool>`.
- `SessionStart`: `startup`, `resume`, `clear`, or `compact`.
- `Setup`: CLI setup flag.
- `SessionEnd`: session end reason.
- `Notification`: notification type.
- `SubagentStart`, `SubagentStop`: agent type.
- `PreCompact`, `PostCompact`: `manual` or `auto`.
- `ConfigChange`: config source.
- `InstructionsLoaded`: load reason.
- `UserPromptExpansion`: command name.
- `Elicitation`, `ElicitationResult`: MCP server name.
- Events documented as not supporting matchers must ignore matchers silently only when matching Claude behavior; Serf diagnostics may still report that the matcher is unused in debug/status output.
- `FileChanged` is special and uses literal filenames/watch list behavior, not general matcher semantics.

## Handler types

### Common handler fields

All handler types share:

| Field | Meaning |
|---|---|
| `type` | Required handler type. |
| `if` | Optional permission-rule filter for tool events only. It is not a boolean expression language. |
| `timeout` | Optional seconds. Defaults depend on handler/event. |
| `statusMessage` | Optional user-visible status text while the hook runs. |
| `once` | Optional; only meaningful for skill-frontmatter hooks in Claude. Treat as reserved until Serf supports that scope. |

Default timeouts:

- `command`, `http`, and `mcp_tool`: 600 seconds.
- `prompt`: 30 seconds.
- `agent`: 60 seconds.
- `UserPromptSubmit`: `command`, `http`, and `mcp_tool` default to 30 seconds.
- `MessageDisplay`: default is 10 seconds.

### `command`

Fields:

- `type: "command"` required.
- `command` required.
- `args` optional. If present, spawn directly without shell interpretation. This is the preferred path for placeholders and paths with spaces.
- `async` optional; command-only.
- `asyncRewake` optional; command-only and implies async.
- `shell` optional: `bash` by default or `powershell`; ignored when `args` is present.

Serf requirements:

- Preserve existing shell-form command execution.
- Add exec-form support before claiming current Claude command parity.
- Expand `${CLAUDE_PROJECT_DIR}`, `${CLAUDE_PLUGIN_ROOT}`, and `${CLAUDE_PLUGIN_DATA}` in command/args where Claude supports placeholders.
- Retain `PLUGIN_ROOT` as a Serf compatibility env alias but prefer official env names.

### `http`

Fields:

- `type: "http"` required.
- `url` required.
- `headers` optional.
- `allowedEnvVars` optional for environment substitution in headers.

Behavior:

- POST the hook input JSON as the request body.
- `2xx` empty response means success/no output.
- `2xx` plain text is added as context.
- `2xx` JSON is parsed as hook JSON output.
- Non-`2xx`, connection failure, and timeout are non-blocking hook errors.
- HTTP cannot block by status code alone; blocking must be returned in `2xx` JSON output.

### `mcp_tool`

Fields:

- `type: "mcp_tool"` required.
- `server` required.
- `tool` required.
- `input` optional; string values support hook input substitution where documented.

Behavior:

- Invoke a configured MCP server/tool through Serf's MCP registry.
- Treat tool text output like command stdout.
- Disconnected server or MCP `isError` is non-blocking unless an explicit future policy says otherwise.

### `prompt`

Fields:

- `type: "prompt"` required.
- `prompt` required.
- `model` optional.

Behavior:

- Use `$ARGUMENTS` for hook input JSON. If `$ARGUMENTS` is absent, append the input JSON to the prompt for Claude compatibility.
- Prompt hook model output should be parsed as JSON:

```json
{"ok": true, "reason": "optional reason"}
```

- `ok: false` maps to event-appropriate block/deny behavior only where prompt handlers are allowed to control the event.
- Existing Serf prompt substitutions (`$TOOL_INPUT`, `$TOOL_RESULT`, `$USER_PROMPT`, `$MESSAGE`, `$TOOL_NAME`) may remain as legacy sugar but should not be the documented compatibility contract.

### `agent`

Fields:

- `type: "agent"` required.
- `prompt` required.
- `model` optional.

Behavior:

- Experimental tier until policy, file access, timeout, prompt construction, result parsing, and lifecycle interactions are tested.
- Choose one explicit runtime before implementation:
  - a visible, bounded subagent that uses normal subagent registry, policy, lifecycle events, and transcript semantics; or
  - a standalone LLM helper with no tools, no child session, no subagent job, and no subagent lifecycle events.
- Do not blur those two runtimes. Hidden helper-subagents are out of scope.
- Return the same `{ "ok": true|false, "reason": "..." }` schema as prompt hooks.

## Handler support by event

Claude documents all five handler types for these events:

- `PermissionDenied`
- `PermissionRequest`
- `PostToolBatch`
- `PostToolUse`
- `PostToolUseFailure`
- `PreToolUse`
- `Stop`
- `SubagentStop`
- `TaskCompleted`
- `TaskCreated`
- `TeammateIdle`
- `UserPromptExpansion`
- `UserPromptSubmit`

Claude documents only `command`, `http`, and `mcp_tool` for these events:

- `ConfigChange`
- `CwdChanged`
- `Elicitation`
- `ElicitationResult`
- `FileChanged`
- `InstructionsLoaded`
- `Notification`
- `PostCompact`
- `PreCompact`
- `SessionEnd`
- `StopFailure`
- `SubagentStart`
- `WorktreeCreate`
- `WorktreeRemove`

Claude documents only `command` and `mcp_tool` for:

- `SessionStart`
- `Setup`

Claude documents `MessageDisplay` as a display hook with a 10-second default timeout, but not in the prompt/agent support list above. Treat it as `command`/`http`/`mcp_tool`-only unless official support changes. Serf should enforce or diagnose these compatibility constraints when parsing and displaying hook status.

## Hook input API contract

### Command API

- Serf sends hook input JSON on stdin.
- The command writes plain text or JSON to stdout.
- The command writes error/block text to stderr when using exit-code semantics.
- Serf captures stdout, stderr, exit code, timeout, and duration.

### HTTP API

- Serf sends hook input JSON as the POST body.
- Serf parses the HTTP response body using the same plain-text/JSON output parser as command stdout.
- HTTP status alone never blocks a Serf action.

### MCP API

- Serf sends `input` through the named MCP server/tool.
- Text output is parsed like command stdout.
- Structured MCP output should be converted to a deterministic text/JSON form before the shared output parser sees it.

### Prompt/agent API

- Serf supplies hook input JSON as `$ARGUMENTS` or appended JSON.
- Prompt handlers currently return model text through the shared hook-output parser (`continue`, `systemMessage`, `hookSpecificOutput`, and top-level `decision`/`reason`). The `{ok, reason}` schema is a future prompt/agent compatibility target and must be implemented deliberately before docs or tests treat it as current behavior.
- Prompt/agent hooks should run through existing provider/client/cancellation paths, not a separate client stack. Retry behavior must match the chosen underlying helper API: high-level generate paths get generate retry, while direct `Client.Complete` calls need explicit retry wrapping if required.

### Common environment variables for command hooks

Set when available:

- `CLAUDE_PROJECT_DIR`
- `CLAUDE_PLUGIN_ROOT`
- `CLAUDE_PLUGIN_DATA`
- `CLAUDE_EFFORT`
- `CLAUDE_CODE_REMOTE`
- `CLAUDE_ENV_FILE` for events that support env-file mutation (`SessionStart`, `Setup`, `CwdChanged`, `FileChanged`)

Retain as Serf-native compatibility:

- `PLUGIN_ROOT`

Never put secrets in event diagnostics. If env/header substitution fails, report the variable name, not the value.

## Hook output contract

### General parsing rules

- Exit 0 with no stdout means success/no decision.
- Plain text stdout on success is context, not a block by itself.
- JSON stdout is parsed only on exit 0 for command hooks.
- Exit 2 is an event-specific blocking/error signal; JSON is ignored on exit 2.
- Other non-zero exit codes are non-blocking errors for most events.
- Output strings are capped at 10,000 characters for compatibility.

### Universal JSON fields

```json
{
  "continue": true,
  "stopReason": "optional reason when continue is false",
  "suppressOutput": false,
  "systemMessage": "message shown to user",
  "terminalSequence": "safe OSC/BEL sequence",
  "hookSpecificOutput": {}
}
```

Rules:

- `continue: false` stops further processing according to event semantics and should include `stopReason`.
- `suppressOutput: true` suppresses normal hook output display where supported.
- `systemMessage` is user-visible/system-facing display, not additional model context.
- `terminalSequence` must be restricted to safe terminal notification sequences supported by Claude docs.
- `hookSpecificOutput.additionalContext` is model context and must be routed separately from user-visible messages.

### Event-specific output fields

#### Top-level block decision

For events where Claude allows top-level blocking, support:

```json
{
  "decision": "block",
  "reason": "why"
}
```

Relevant events include `UserPromptSubmit`, `UserPromptExpansion`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `Stop`, `SubagentStop`, `ConfigChange`, and `PreCompact`. For `PostToolUse` and `PostToolUseFailure`, parse the documented top-level decision shape, but a block decision cannot retroactively undo an already completed tool execution; map it only to the event's supported feedback/loop behavior.

#### `PreToolUse`

Preferred current schema:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "allow|deny|ask|defer",
    "permissionDecisionReason": "why",
    "updatedInput": {"key": "value"},
    "additionalContext": "context for model"
  }
}
```

Deprecated compatibility mapping:

- top-level `decision: "approve"` maps to `permissionDecision: "allow"`.
- top-level `decision: "block"` maps to `permissionDecision: "deny"`.
- top-level `reason` maps to `permissionDecisionReason` when the preferred field is absent.

#### `PermissionRequest`

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {
      "behavior": "allow|deny",
      "updatedInput": {"key": "value"},
      "updatedPermissions": [
        {
          "destination": "session|localSettings|projectSettings|userSettings",
          "addRules": [],
          "replaceRules": [],
          "removeRules": [],
          "setMode": "optional mode",
          "addDirectories": [],
          "removeDirectories": []
        }
      ],
      "message": "message to show",
      "interrupt": false
    }
  }
}
```

#### `PermissionDenied`

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionDenied",
    "retry": true
  }
}
```

Exit codes are ignored for this event; JSON `retry` is the control surface.

#### `PostToolUse` and MCP output updates

Support event-specific fields when implemented:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "updatedToolOutput": "replacement or annotation",
    "updatedMCPToolOutput": {}
  }
}
```

#### `SessionStart`, `CwdChanged`, and `FileChanged`

Support context/watch fields when implemented:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "SessionStart",
    "additionalContext": "context for model",
    "initialUserMessage": "optional initial prompt",
    "sessionTitle": "optional title",
    "watchPaths": ["path"],
    "reloadSkills": true
  }
}
```

#### `MessageDisplay`

Display-only mutation:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "MessageDisplay",
    "displayContent": "replacement visible text"
  }
}
```

The transcript and model context keep the original text.

## Exit-code semantics

Implement a central table. Do not treat every exit-code-2 hook as generic denial.

| Event | Exit 0 | Exit 2 | Other non-zero |
|---|---|---|---|
| `PreToolUse` | parse output | block tool call | non-blocking error |
| `PermissionRequest` | parse output | deny permission | non-blocking error |
| `UserPromptSubmit` | parse output | block prompt and erase prompt | non-blocking error |
| `UserPromptExpansion` | parse output | block expansion | non-blocking error |
| `PostToolUse` | parse output | no undo; show/add stderr context | non-blocking error |
| `PostToolUseFailure` | parse output | no undo; show/add stderr context | non-blocking error |
| `PostToolBatch` | parse output | block before next model call | non-blocking error |
| `PermissionDenied` | parse JSON `retry` | ignored | ignored |
| `Stop` | parse output | prevent stopping | non-blocking error |
| `SubagentStop` | parse output | prevent subagent stopping | non-blocking error |
| `TeammateIdle` | parse output | prevent idle transition | non-blocking error |
| `TaskCreated` | parse output | roll back task creation | non-blocking error |
| `TaskCompleted` | parse output | prevent marking complete | non-blocking error |
| `ConfigChange` | parse output | block config change except policy settings | non-blocking error |
| `PreCompact` | parse output | block compaction | non-blocking error |
| `Elicitation` | parse output | deny elicitation | non-blocking error |
| `ElicitationResult` | parse output | block response | non-blocking error |
| `WorktreeCreate` | parse output | abort worktree creation | any non-zero aborts |
| `WorktreeRemove` | parse output | debug/log only | debug/log only |
| `Notification` | parse output | show stderr to user only | non-blocking error |
| `SubagentStart` | parse output | show stderr to user only | non-blocking error |
| `SessionStart` | parse output | show stderr to user only | non-blocking error |
| `Setup` | parse output | show stderr to user only | non-blocking error |
| `SessionEnd` | parse output | show stderr to user only | non-blocking error |
| `CwdChanged` | parse output | show stderr to user only | non-blocking error |
| `FileChanged` | parse output | show stderr to user only | non-blocking error |
| `PostCompact` | parse output | show stderr to user only | non-blocking error |
| `StopFailure` | ignored | ignored | ignored |
| `InstructionsLoaded` | parse context-only output if supported | exit code ignored | exit code ignored |
| `MessageDisplay` | parse display output | original text displayed | original text displayed |

## Async semantics

- `async` and `asyncRewake` are command-only.
- `asyncRewake` implies `async`.
- Async hooks cannot block or alter behavior for the action that already completed.
- On async completion, `additionalContext` is delivered on the next conversation turn where supported; `systemMessage` is user-visible only.
- Async hook cancellation/cleanup must be explicit when the session closes.

## Diagnostics and status

Hook diagnostics must include:

- plugin name;
- hook source path when known;
- event name;
- matcher;
- handler type;
- timeout;
- unsupported tier/reason;
- exit code and duration for completed runs;
- sanitized error category: parse_error, unsupported_event, unsupported_handler, invalid_matcher, timeout, cancelled, command_error, http_error, mcp_error, prompt_error, hook_blocked.

Diagnostics must not include:

- raw tool input/output unless explicitly known safe;
- API keys/tokens;
- HTTP header values;
- env var values;
- provider request/response bodies;
- full transcript bodies.

`/status`, debug logs, or equivalent surfaces should distinguish:

- active supported hooks;
- recognized Claude hooks skipped because unsupported;
- unknown events/handler types;
- invalid hook config that failed plugin load.

## Phased YAGNI / DRY implementation plan

### Phase A: make current hooks correctly compatible

1. Extend `RegisteredHook` and parser structs in `agent/plugin/hooks.go` with common fields and command exec fields: `If`, `Args`, `Shell`, `Async`, `AsyncRewake`, `StatusMessage`, source path/event/group-index/handler-index metadata, and raw/unsupported field capture.
2. Keep existing wrapper/direct parsing, inline manifest hooks, manifest path hooks, and default `hooks/hooks.json` discovery unchanged, but thread source path/event/matcher/handler-index context through parser and runtime diagnostics.
3. Replace regex-only matching with a small matcher helper implementing empty/star, exact/pipe-list, and regex modes. Reuse it everywhere; do not duplicate event-specific matching logic.
4. Add matcher target helpers per event: tool name, session start kind, compact trigger, notification type, subagent agent type.
5. Add command exec-form support: if `args` is present, spawn directly; otherwise use shell form with `shell` defaulting to `bash` and optional `powershell`.
6. Add official command env vars where values are known; keep `PLUGIN_ROOT` alias.
7. Expand hook input struct with official common fields. Preserve old `user_prompt` and `tool_result` aliases during migration.
8. Split run output aggregation into `SystemMessages`, `AdditionalContext`, `TerminalSequences`, `UpdatedInput`, `PermissionDecision`, `BlockReason`, and event-specific fields. Stop folding `additionalContext` into `SystemMessages`.
9. Implement central exit-code behavior table for existing events.
10. Update tests for current events before adding new events.

### Phase B: add core current Claude events and output schemas

1. Add parser recognition and runner methods for `PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `UserPromptExpansion`, and `StopFailure`.
2. Add event input structs/constructors at actual Serf runtime boundaries only.
3. Implement preferred `PreToolUse` output schema, deprecated `approve`/`block` mapping, `permissionDecisionReason`, `allow|deny|ask|defer`, and revalidation after `updatedInput`.
4. Implement `PermissionRequest` decision object and `PermissionDenied.retry` only when Serf approval flow has matching semantics.
5. Implement top-level `decision: "block"` for events where Claude supports it.
6. Add `PostToolUseFailure` fields using official names: `error`, optional `is_interrupt`, and `duration_ms`.
7. Add `PostToolBatch` with official `tool_calls` shape if Serf batches tool calls.
8. Keep unsupported events visible as reserved placeholders instead of pretending they fired.

### Phase C: add handler types beyond command/prompt

1. Implement `http` using the shared input/output parser and non-blocking HTTP failure semantics.
2. Implement `mcp_tool` only through the existing MCP registry/call path; do not start a parallel MCP subsystem.
3. Update `prompt` to use `$ARGUMENTS` and `{ok, reason}` while preserving Serf legacy substitutions as compatibility sugar.
4. Add `agent` as experimental after prompt hooks and subagent policy wiring are stable.
5. Add async command execution only after synchronous command behavior is fully tested.

### Phase D: modern lifecycle/watch/team events

1. Add `Setup`, `InstructionsLoaded`, `MessageDisplay`, `CwdChanged`, and `FileChanged` only if Serf has matching lifecycle boundaries.
2. Implement `watchPaths`, `reloadSkills`, `displayContent`, and env-file behavior where meaningful.
3. Add `TaskCreated`, `TaskCompleted`, `TeammateIdle`, `WorktreeCreate`, `WorktreeRemove`, `Elicitation`, and `ElicitationResult` only if the underlying Serf features exist.
4. For featureless compatibility names, parse-and-diagnose as reserved; do not fabricate behavior.

### Phase E: SDK lifecycle hooks

1. Add typed SDK hook structs only after plugin hook ordering is stable.
2. Keep SDK hooks in-process and typed; do not expose Claude JSON as the SDK API.
3. Use the same canonical lifecycle order as plugin hooks.
4. Add cancellation/timeout/error policy tests before documenting SDK hooks as stable.

## Acceptance criteria

- Existing plugin hooks using `hooks/hooks.json` with `SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionEnd`, `PreCompact`, or `Notification` continue to load and run.
- Wrapper and direct hook JSON formats remain supported.
- Parser diagnostics include source path, event, matcher, handler index/type, and sanitized reason for invalid configs.
- Matcher behavior follows current Claude semantics for empty/star, exact, pipe-list, and regex modes, with documented Go RE2 caveat if JS regex is not exact.
- `Bash` no longer regex-substring matches `BashOutput`; `Edit|Write` exact-list matches only those names.
- Negative matcher tests cover non-substring behavior for tools and MCP names, pipe-list matching for non-tool events, invalid regex diagnostics, and empty matcher behavior.
- MCP tool names such as `mcp__memory__search` match regex `mcp__memory__.*` but not exact matcher `mcp__memory`.
- Command hooks support shell form and exec-form `args` without requiring plugin authors to wrap every path in `bash -c`.
- Hook input includes official common fields when available and preserves legacy aliases during migration.
- `additionalContext` is routed separately from user-visible `systemMessage`.
- Exit-code 2 behavior is event-specific and table-driven.
- JSON output is parsed only on exit 0 for command hooks.
- HTTP hooks, once implemented, cannot block by status code alone.
- Unsupported Claude events/handler types are reported as unsupported/reserved, not silently treated as implemented.
- SDK hooks, if added, cannot bypass plugin trust mode, final execution policy, or child effective capability policy.

## Required tests

### Parser/config tests

- Wrapper format with top-level `description` and `hooks` object.
- Direct event-at-top-level format.
- Manifest hook path, inline hook object, and default `hooks/hooks.json` discovery.
- Unknown event vs recognized-but-unsupported Claude event diagnostics.
- Unsupported handler type diagnostics.
- Handler field parsing for `args`, `shell`, `async`, `asyncRewake`, `if`, `statusMessage`, `url`, `headers`, `allowedEnvVars`, `server`, `tool`, `input`, `prompt`, and `model`.
- Timeout default tests for command/http/mcp/prompt/agent plus `UserPromptSubmit` and `MessageDisplay` overrides.

### Matcher tests

- Omitted, empty, and `*` match all.
- Exact string matcher does not substring-match.
- Pipe-list exact alternatives.
- Regex matcher for MCP server namespace.
- Invalid regex produces diagnostic and skips hook.
- Event-specific matcher target tests for `SessionStart`, `PreCompact`, `Notification`, `SubagentStop`, and tool events.

### Command execution tests

- Shell-form command uses configured shell.
- Exec-form `args` bypasses shell and handles paths with spaces.
- Env vars include official names and `PLUGIN_ROOT` alias.
- Timeout/cancellation kills command and emits sanitized diagnostic.
- Exit code and stderr are captured accurately.

### Output parser tests

- Exit 0 empty stdout = no decision.
- Exit 0 plain text = context/message.
- Exit 0 universal JSON fields.
- Exit 2 ignores JSON and follows event-specific behavior.
- `PreToolUse` `allow`, `deny`, `ask`, `defer`, `updatedInput`, `permissionDecisionReason`, and deprecated `approve`/`block` mapping.
- Top-level `decision: "block"` for `Stop`, `SubagentStop`, `PreCompact`, and prompt events.
- `PermissionRequest` decision object and `updatedPermissions` parsing.
- `PermissionDenied.retry` parsing with ignored exit code.
- Separate routing for `systemMessage`, `additionalContext`, and `terminalSequence`.

### Ordering and policy tests

- Tool ordering: decoded/best-effort hook input -> `PreToolUse` -> updated-input merge -> final schema validation -> final execution policy -> tool execution -> post hook.
- Plugin `PreToolUse` cannot bypass final execution policy by changing input to an invalid or disallowed shape.
- `PostToolUse` cannot undo an already executed tool.
- `Stop`/`SubagentStop` blocking has a loop guard.
- `SessionStart` fires once for startup/resume kinds as applicable.
- `SessionEnd` fires on normal close and cancellation without blocking shutdown.
- SDK hook ordering, if implemented, does not bypass plugin trust or effective child policy.

### Handler-type tests

- HTTP `2xx` empty/plain/JSON behavior and non-`2xx` non-blocking error behavior.
- MCP tool output parsed through shared output parser.
- Prompt hook `$ARGUMENTS` behavior and `{ok, reason}` response parsing.
- Agent handler is either absent with clear unsupported diagnostics or experimental with bounded subagent policy tests.
- Async command cannot block an already completed action and delivers next-turn context only when implemented.

## Caveats

- **Go RE2 matcher is the active implementation (Phase 1).** The Claude-compatible matcher shipped in Phase 1 uses Go RE2, not JavaScript regular expressions. RE2 does not support lookbehind assertions (`(?<=...)`, `(?<!...)`) or backreferences (`\1`). A matcher containing either construct will be treated as an invalid regex: the hook is skipped and a sanitized diagnostic is emitted (plugin name, event, matcher, source file). It will never silently mis-match. Plugin authors who currently rely on JS-only regex features must rewrite their matchers to use RE2-compatible alternatives. If exact JS regex parity becomes required, a JS regex engine may be introduced deliberately; see §Non-goals.
- Claude docs specify JavaScript regular expressions; a Go RE2 implementation is a compatibility subset unless unsupported JS regex constructs are detected and handled explicitly.
- Some Claude events describe products/features Serf may not have. Those names should remain reserved placeholders until there is a real runtime boundary.
- Claude hook docs are updated over time. This evergreen spec should cite <https://code.claude.com/docs/en/hooks> as the authoritative current compatibility reference and update tables when that page changes.
- Current Serf hook code is already useful for simple plugins, but it is not full Claude Code hook parity. Status and diagnostics must say so.
