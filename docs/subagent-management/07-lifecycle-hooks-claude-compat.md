# Lifecycle Hooks — Claude Compatibility & Roadmap

The compatibility spec for serf's lifecycle hooks: the full Claude event
vocabulary, the matcher/handler/output/exit-code **contract across both shipped
and reserved behavior**, the compatibility tiers, the three hook surfaces, and the
deferred phases (B–E) as a roadmap. This document's job is to define the line
between what serf implements and what it only recognizes — so docs, tests, CLI
output, and APIs never imply Claude parity serf has not built.

**Shipped behavior lives elsewhere.** How hooks work **today** — discovery, the
matcher, the `command`/`prompt` handlers, the input/output fields serf populates
and honors, the env vars, the fired-event exit codes, diagnostics — plus a
practical authoring walkthrough is in **[Hooks](../hooks.md)**. That document is
the authoritative reference for the implemented subset; this one is the
forward-looking contract for everything not implemented yet. Where a reference
table below spans both (the full event set, the full exit-code table), the rows
serf fires are detailed in [Hooks](../hooks.md) and recapped here only for the
compatibility map.

Phase 1 — making the nine events serf already fires Claude-compatible in matcher,
exec form, input/output fields, exit-code behavior, and env vars — is **shipped**
(see [Hooks](../hooks.md)). It deliberately did **not** claim full Claude Code
parity: it completed no event end-to-end, nor added the events, handler types, or
output schemas Claude documents but serf does not yet implement. Those are the
subject of this spec.

> **Honesty over completeness.** Every feature here carries exactly one compatibility tier (`serf-native`, `claude-compatible-subset`, `reserved-placeholder`, `experimental`; see `10-runtime-contracts.md` §"Contract 4"). Only shipped behavior (tiers `serf-native` and `claude-compatible-subset`, detailed in [Hooks](../hooks.md)) is described as working. Everything else is marked reserved/roadmap and is parsed-and-diagnosed predictably rather than silently half-supported.

## Purpose

Serf runs one lifecycle hook model across three distinct surfaces without conflating them:

1. **Plugin compatibility hooks** loaded from plugin/config JSON and executed as external handlers. This is the **primary, implemented** surface.
2. **Serf-native lifecycle/SDK hooks** — typed in-process callbacks for embedders (`reserved-placeholder`; see [Roadmap](#roadmap-deferred-phases)).
3. **Internal runtime events** emitted for session, tool, subagent, compaction, diagnostics, and client projections (the `agent/events` stream).

The design preserves existing serf behavior where it works, fixes compatibility gaps that would otherwise break real Claude hook configurations, and reserves explicit extension points for the Claude Code hook semantics documented at <https://code.claude.com/docs/en/hooks>.

## Implementation status

Tiers `serf-native` and `claude-compatible-subset` are **shipped** and documented in full in [Hooks](../hooks.md): the Claude-compatible matcher (exact/pipe-list vs Go RE2, invalid-regex skip-and-diagnose; `Bash` no longer substring-matches `BashOutput`), the `command` exec-form (`args`) and explicit `shell` selection, the serf-native `prompt` handler, the official input fields and legacy aliases, the `additionalContext`→model (system reminder) / `systemMessage`→user delivery split, the `PreToolUse` `allow`/`deny` schema with `permissionDecisionReason` and the deprecated `approve`/`block` mapping, the event-specific exit-code table for the nine fired events, the `CLAUDE_EFFORT`/`PLUGIN_ROOT` env vars, tier-labeled `/status` diagnostics, and loud load-time warnings for unknown events, recognized-but-unsupported events, invalid-regex matchers, and unsupported handler types. The implementing packages are listed under [Source anchors](#serf-code-anchors).

Every other Claude hook capability — new events, the `http`/`mcp_tool`/`agent` handler types, the `PreToolUse` `ask`/`defer` decisions and `updatedInput` revalidation, `if` permission-rule evaluation, async execution, and typed SDK lifecycle hooks — is **deferred** and marked reserved/experimental below. See [Roadmap](#roadmap-deferred-phases).

## Goals

- Keep current simple plugin hooks working: `hooks/hooks.json`, wrapper/direct JSON formats, `command` handlers, `prompt` handlers, and the recognized serf hook events.
- Parse the Claude-compatible config shape exactly enough to preserve unsupported fields for diagnostics and support future phases.
- Define the event set, matcher targets, handler fields, input JSON, output JSON, HTTP/API behavior, and exit-code semantics serf supports or reserves.
- Separate compatibility plugin hooks from serf-native typed callbacks and non-blocking runtime events.
- Provide compatibility tiers so docs, tests, CLI output, and API surfaces never imply unsupported Claude parity.
- Keep the implementation incremental, DRY, and YAGNI: small tables/helpers next to the existing hook parser/runner code, not a broad event-bus or schema framework.
- Ensure hook diagnostics identify the event/plugin/source while redacting hook payloads, headers, env values, tool inputs, and provider secrets.

## Non-goals

- No full Claude Code parity claim until all advertised events, fields, handler types, matcher behavior, output schemas, timeouts, and environment variables are implemented and tested.
- No replacement of plugin hooks with SDK callbacks.
- No second internal event bus when existing `agent/events` and session publication can project the lifecycle spine.
- No hidden workflow scheduler, task graph runner, or agent-team behavior solely to support hook names serf does not otherwise implement.
- No execution of untrusted hooks during plugin validation.
- No JS-regex engine dependency unless exact Claude regex parity is deliberately required; Go RE2 matching is an explicitly documented compatibility caveat (see [Caveats](#caveats)).
- No silent half-support for unsupported events, handler types, fields, or decisions.

## Source anchors

### Official compatibility source

- Claude Code hooks documentation: <https://code.claude.com/docs/en/hooks>. This document treats that page as the compatibility source of truth for Claude hook config shape, event names, matcher semantics, handler types, input/output rules, timeout defaults, and exit-code behavior.

### Serf code anchors

- `agent/plugin/hooks.go` defines the hook event enum, the `validHookEvents` set (the 9 serf fires), the `recognizedClaudeEvents` set (every Claude event name), the `hookEventTier`/`EventTier` tier registry, `RegisteredHook` (matcher, type, command/prompt, timeout, model, plugin name/dir, the official handler fields `Args`/`Shell`/`If`/`Async`/`AsyncRewake`/`StatusMessage`, source metadata `SourcePath`/`Event`/`GroupIndex`/`HandlerIndex`, and `UnknownFields`), and the parser `parsePluginHooksDiag` (which returns the supported hooks map plus the `unsupported` and `unknown` event sets).
- `agent/plugin/plugin.go` defines `Instance` with `Hooks`, `UnsupportedHooks`, and `UnknownHooks`, and the `Load`/`LoadAll` entry points that read each plugin's manifest hooks, manifest-referenced hook path, or default `<dir>/hooks/hooks.json`.
- `agent/internal/hooks/matcher.go` implements `matchTarget` (empty/star, exact/pipe-list, RE2 regex).
- `agent/internal/hooks/exitcode.go` implements `exitBehavior` (the central exit-code table for the fired events).
- `agent/internal/hooks/hooks.go` defines `Input` (the JSON piped to command hooks), `executeCommandHook` (exec-form + shell selection + env), `parseHookOutput`/`parsedHookOutput` (output parsing with `additionalContext` routed separately), `MatchHooks` (matcher dispatch + invalid-regex diagnostic), `runAll`/`runHook`, and the per-event runner methods.
- `agent/internal/toolname` (`SerfToClaude`/`ClaudeToSerf`) translates between serf's canonical tool names and Claude's. **Matchers run against the Claude name**: serf's `shell` tool is presented to hooks as `Bash`. A matcher of `"shell"` would never fire (see [Caveats](#caveats)).
- `agent/session_init.go` (`initPlugins`) loads plugins into a session, builds the `hooks.Runner`, accumulates `unsupportedPluginHookEvents`, and emits the loud load-time warnings for unknown/unsupported/invalid-matcher hook declarations.
- `agent/events/events.go` includes the `EventWarning` kind (plus session, tool, subagent, plugin, hook, compaction, and error kinds); `agent/events/payloads.go` defines `WarningData`, `HookStartData`, and `HookEndData`.
- `agent/status.go` (`HookEventStatus`, `DetailedStatus`) surfaces tier-labeled hook diagnostics in `/status`.

## Compatibility tiers

Every hook feature carries exactly one tier in docs, status output, diagnostics, and tests. The vocabulary is shared with `10-runtime-contracts.md` §"Contract 4".

### `serf-native`

Typed or internal behavior owned by serf: internal lifecycle events, SDK callbacks, serf-specific diagnostics, the event-classification/tier machinery, and plugin aliases such as `PLUGIN_ROOT` retained for backwards compatibility. Serf-native behavior may be stable without matching Claude exactly.

### `claude-compatible-subset`

Behavior implemented and tested to match the Claude Code docs closely enough for normal plugin/config portability. A subset feature includes parser support, runtime support, diagnostics, tests, and documented caveats. The shipped `claude-compatible-subset` — wrapper/direct JSON parsing and file discovery, the nine fired events, matcher semantics, command `args` exec-form and `shell` selection, the official input fields and legacy aliases, the `additionalContext` model-delivery channel (system reminder) and `systemMessage` user-display channel, the `PreToolUse` `allow`/`deny` decisions with `permissionDecisionReason` and the deprecated `approve`/`block` mapping, the fired-event exit-code table, the `CLAUDE_EFFORT` env var, and `command`/`prompt` handler execution — is documented in full in [Hooks](../hooks.md). Claude-compatible handler support remains event-specific per the [handler support table](#handler-support-by-event).

### `reserved-placeholder`

Claude-documented behavior serf recognizes as a future compatibility target but does not yet execute. Reserved placeholders parse or diagnose predictably and are never advertised as working. They include:

- the events serf does not yet fire — `PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `UserPromptExpansion`, `StopFailure`, plus `Setup`, `InstructionsLoaded`, `MessageDisplay`, `TaskCreated`, `TaskCompleted`, `TeammateIdle`, `CwdChanged`, `FileChanged`, `WorktreeCreate`, `WorktreeRemove`, `Elicitation`, `ElicitationResult`;
- the `http` and `mcp_tool` handler types;
- the `PreToolUse` `ask`/`defer` decisions (no interactive permission prompt) and `updatedInput` revalidation;
- `if` permission-rule evaluation; `async`/`asyncRewake` execution; `once`/`statusMessage` behavior;
- advanced `updatedPermissions`, `watchPaths`, `reloadSkills`, async re-wake;
- exact JS-regex features unsupported by Go RE2.

### `experimental`

Implemented but intentionally unstable behavior, clearly marked in user-visible docs and diagnostics. No `experimental` hook behavior ships today. The Claude `agent` handler is reserved to start here (even though serf has subagent primitives) because its behavior, timeout, model selection, file access, and `{ok, reason}` response handling need careful policy tests before it is enabled.

## Hook surface separation

### Plugin compatibility hooks

Plugin hooks are external compatibility hooks loaded from plugin/config JSON. They receive JSON input and may return exit-code, text, or JSON output. They are subject to plugin trust policy, timeout policy, event-specific blocking rules, and secret redaction. This is the implemented surface.

Plugin hooks must not bypass:

- effective tool policy;
- parent-to-child subagent restrictions;
- provider/model feature policy;
- session cancellation/closed state;
- final execution policy after hook-updated inputs are applied.

### Serf-native SDK hooks (reserved-placeholder)

SDK hooks are typed in-process callbacks for embedders. They are **not implemented**; this is the reserved shape:

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

Reserved rules:

- SDK hooks use Go types, not Claude JSON payloads.
- SDK hooks honor `context.Context` cancellation.
- SDK hooks do not parse Claude hook JSON.
- Blocking SDK hooks, if allowed, must have explicit ordering relative to plugin hooks and execution policy.
- SDK hooks cannot expand a child/subagent capability set beyond effective policy.

### Internal lifecycle events

Internal events are observations used by CLI/TUI/Hub/API projections, logs, metrics, and tests. They extend the existing `agent/events` package additively where practical. Events are non-blocking unless a separate hook contract explicitly makes them blocking.

## Canonical lifecycle order

Existing event names remain; ordering is stable and tested. Steps for events serf does not yet fire are marked as such.

### Session lifecycle

1. Resolve session config/profile/provider policy.
2. Build effective root capability set.
3. (Reserved) Fire a serf-native `SessionStarting` observation if added.
4. Run `SessionStart` hooks for matcher `startup`, `resume`, `clear`, or `compact` before the first model turn when they can add initial context.
5. Emit serf `SESSION_START` / client event.
6. Process user/model/tool turns.
7. Run `PreCompact` before compaction (and the reserved `PostCompact` after compaction once implemented).
8. Run `Stop` before allowing an assistant/session stop, if configured and not in a loop-guarded retry path.
9. Run `SessionEnd` for final cleanup/notification only.
10. Emit serf `SESSION_END` or error/cancellation event.

### Tool lifecycle

1. Resolve effective tool catalog.
2. Apply visibility policy before the model request.
3. Model emits a tool call.
4. Decode the tool name and parse arguments best-effort for hook input. Serf runs `PreToolUse` **before** registry schema validation so hooks can inspect or rewrite raw input; changing that ordering is a compatibility migration.
5. Run `PreToolUse` hooks matching the Claude tool name.
6. Apply hook `updatedInput`.
7. Validate the final arguments against schema.
8. Run final execution policy on the final validated input (registry lookup, schema validation, middleware/policy checks).
9. (Reserved) Run typed SDK `OnToolPolicy` only if it is explicitly designed to observe/block at this boundary.
10. Emit `TOOL_CALL_START`.
11. Execute the tool.
12. Emit `TOOL_CALL_END` or failure/error event.
13. Run `PostToolUse` hooks with sanitized result/error (the reserved `PostToolUseFailure` on failure once implemented).
14. (Reserved) After a batch of tool calls, run `PostToolBatch` before the next model request once batching is implemented.

### Subagent lifecycle

1. Decode spawn request.
2. Resolve agent type and plugin/builtin/project agent definition.
3. Compute effective child policy.
4. Create child session/job so hook input can include stable child identity/session metadata.
5. (Reserved) Run `SubagentStart` compatibility/SDK hooks before the child model receives initial context once implemented.
6. Emit `SUBAGENT_START`. The current code differs by path: initial spawn starts the child goroutine before emitting `SUBAGENT_START`, while idle resume emits `SUBAGENT_START` before starting the resumed goroutine. Treat any ordering change as deliberate and regression-tested.
7. Run child turns.
8. On completion, failure, cancellation, or close, run `SubagentStop` if configured.
9. Finalize result/diagnostics/status.
10. Emit `SUBAGENT_END` and release waiters.

## Claude-compatible hook config contract

Serf parses the Claude hook config shape exactly enough to preserve unsupported fields for diagnostics and support future phases.

### File locations and wrapper shape

Plugin hooks may be supplied inline in the manifest, referenced by a manifest-specified hook path, or discovered from the default `hooks/hooks.json`. File/object contents may use the wrapper shape (an optional top-level `description` plus a `hooks` object keyed by event) or the direct shape (events at the top level). Both are implemented; see [Hooks → How serf discovers your hooks](../hooks.md#how-serf-discovers-your-hooks) for the discovery order and worked examples.

### Formal config shape

`Args`, `Shell`, `If`, `Async`, `AsyncRewake`, and `StatusMessage` are parsed today; the `http`, `mcp_tool`, and `agent` field groups are reserved (parsed into `UnknownFields` for diagnostics, never executed).

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

    // http (reserved)
    URL            string            `json:"url,omitempty"`
    Headers        map[string]string `json:"headers,omitempty"`
    AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"`

    // mcp_tool (reserved)
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
- Unknown event names are reported as unknown unless they are known Claude events not implemented by serf, in which case they are reported as `recognized but unsupported`. Both cases emit a loud load-time warning (see [Diagnostics and status](#diagnostics-and-status)).
- Unknown handler fields are preserved in `UnknownFields` for diagnostics; serf does not fail on harmless future fields.
- Unsupported handler types do not abort plugin load when they are in a future/reserved tier; they emit a loud load-time diagnostic warning (naming the plugin, event, and type) and are skipped at dispatch until implemented. They are not listed in `/status`, which surfaces only the recognized event landscape.

## Event contract

### Event set

Serf tracks the full Claude-documented event vocabulary, but only fires events that have a real serf runtime boundary. "implemented, compatibility-incomplete" means serf fires the event with the Phase-1 subset behavior, not full Claude parity.

| Event | Compatibility status | Matcher target | Notes |
|---|---|---|---|
| `SessionStart` | implemented, compatibility-incomplete | `startup`, `resume`, `clear`, `compact` | Implemented for startup-kind matching; command timeout/input parity remains future work. |
| `Setup` | reserved-placeholder | setup trigger such as `init` or `maintenance` | Only implement if serf has an equivalent setup boundary. |
| `InstructionsLoaded` | reserved-placeholder | load reason | Context-only/notification until serf has matching instruction reload semantics. |
| `UserPromptSubmit` | implemented, compatibility-incomplete | no matcher | Runs after `USER_INPUT` emit, transcript append, and namer launch; hook messages are queued via `Steer` and drained before the model request. Blocking/erase-prompt parity still needs a placement redesign. |
| `UserPromptExpansion` | reserved-placeholder | command name | Implement only if slash/prompt expansion has a hook boundary. |
| `MessageDisplay` | reserved-placeholder | no matcher | Display-only; must not mutate transcript/model context. |
| `PreToolUse` | implemented, compatibility-incomplete | tool name | Runs before registry schema validation and can rewrite input; `allow`/`deny` and deprecated mapping shipped; `ask`/`defer` and `updatedInput` revalidation reserved. |
| `PermissionRequest` | reserved-placeholder | tool name | Requires a serf approval/permission-request boundary. |
| `PostToolUse` | implemented, compatibility-incomplete | tool name | Cannot undo or block tool execution; can add context/messages. |
| `PostToolUseFailure` | reserved-placeholder | tool name | Uses official `error`, optional `is_interrupt`, and `duration_ms` fields. |
| `PostToolBatch` | reserved-placeholder | no matcher | Runs before the next model call after a tool batch. |
| `PermissionDenied` | reserved-placeholder | tool name | Exit code ignored; JSON `retry` controls retry. |
| `Notification` | implemented, compatibility-incomplete | notification type (not wired; current runner matches with an empty target) | User-visible/logging side effects only. |
| `SubagentStart` | reserved-placeholder | agent type | Implement at the child setup boundary. |
| `SubagentStop` | implemented, compatibility-incomplete | agent type (not wired; shares the stop runner) | Needs the agent matcher target and a stop-loop guard. |
| `TaskCreated` | reserved-placeholder | no matcher | Implement only if the serf task store exposes a rollback-capable creation hook. |
| `TaskCompleted` | reserved-placeholder | no matcher | Implement only if completion can be blocked safely. |
| `Stop` | implemented, compatibility-incomplete | no matcher | Blocks stopping when requested by the current JSON/exit semantics; full Claude output parity remains future work. |
| `StopFailure` | reserved-placeholder | no matcher | Exit/output ignored per Claude; diagnostics only. |
| `TeammateIdle` | reserved-placeholder | no matcher | Only if serf adds a teammate/team runtime. |
| `ConfigChange` | reserved-placeholder | config source | Can block config except policy settings in Claude; serf needs an explicit config-reload boundary. |
| `CwdChanged` | reserved-placeholder | no matcher | Requires a working-directory-change boundary and optional env-file behavior. |
| `FileChanged` | reserved-placeholder | literal filenames/watch list | Special matcher/watch semantics; does not use normal matcher rules. |
| `WorktreeCreate` | reserved-placeholder | no matcher | Any non-zero aborts creation in Claude; only implement with a worktree feature. |
| `WorktreeRemove` | reserved-placeholder | no matcher | Failures debug/log only. |
| `PreCompact` | implemented, compatibility-incomplete | `manual`/`auto` (not wired) | Fires; blocking parity and matcher target need completion. |
| `PostCompact` | reserved-placeholder | `manual`, `auto` | Add when a post-compaction boundary exists. |
| `SessionEnd` | implemented, compatibility-incomplete | session end reason (not wired; current runner matches with an empty target) | Present in serf's current set. |
| `Elicitation` | reserved-placeholder | MCP server name | Requires MCP elicitation support. |
| `ElicitationResult` | reserved-placeholder | MCP server name | Requires MCP elicitation support. |

### Event field names

For Claude-compatible JSON payloads, serf prefers official field names and keeps old serf aliases temporarily where already used.

Common fields:

```json
{
  "session_id": "string",
  "transcript_path": "string",
  "cwd": "string",
  "permission_mode": "default|plan|acceptEdits|auto|dontAsk|bypassPermissions",
  "effort": "low|medium|high|...",
  "hook_event_name": "PreToolUse",
  "agent_id": "string",
  "agent_type": "string"
}
```

Serf populates `session_id`, `cwd`, `hook_event_name`, `transcript_path` (when persistence is on), and `effort` (when configured). `permission_mode`, `agent_id`, and `agent_type` are part of the contract but **omitted on the wire** because serf has no real value for them today — it never fabricates one.

Tool event fields:

```json
{
  "tool_name": "Bash",
  "tool_use_id": "call-id",
  "tool_input": {"key": "value"},
  "tool_response": "string or structured result",
  "tool_result": "legacy serf alias during migration",
  "error": "error text for PostToolUseFailure (reserved)",
  "is_interrupt": false,
  "duration_ms": 123
}
```

Prompt/message fields:

```json
{
  "prompt": "current user prompt",
  "user_prompt": "legacy serf alias during migration",
  "message": "notification or display text"
}
```

Stop/compact/config/session fields use official names where documented — `reason`, `trigger`, `source`, `error`, `error_details`, `last_assistant_message`.

## Matcher semantics

Matcher semantics are `claude-compatible-subset` (implemented), because they change which hooks fire.

### General matcher rules

The matcher rules — empty/star matches all; a matcher of only `[A-Za-z0-9_|]+` is exact-name or pipe-list matching; anything else is a Go RE2 regex (with the documented lookbehind/backreference caveat); an invalid regex skips the hook and emits a sanitized diagnostic — are implemented and documented with worked examples in [Hooks → Matchers](../hooks.md#matchers). `FileChanged` is the documented exception (literal filename/watch-list semantics, not general matcher rules) and remains reserved.

### Event-specific matcher targets

- Tool events (`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`): the **Claude** tool name, including `mcp__<server>__<tool>`. Serf translates its canonical names to Claude names before matching (`shell` → `Bash`); a matcher must name the Claude tool.
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
- Events documented as not supporting matchers ignore matchers silently when matching Claude behavior; serf diagnostics may still report that the matcher is unused in debug/status output.
- `FileChanged` is special and uses literal filenames/watch-list behavior, not general matcher semantics.

## Handler types

### Common handler fields

All handler types share:

| Field | Meaning |
|---|---|
| `type` | Required handler type. |
| `if` | Optional permission-rule filter for tool events only. It is not a boolean expression language. Parsed; not enforced (reserved, Phase B). |
| `timeout` | Optional seconds. Defaults depend on handler/event. |
| `statusMessage` | Optional user-visible status text while the hook runs. Captured; surfacing reserved. |
| `once` | Optional; only meaningful for skill-frontmatter hooks in Claude. Reserved until serf supports that scope. |

Default timeouts (serf applies the `command`/`prompt` defaults today; the rest describe the Claude contract for reserved handler types):

- `command`, `http`, and `mcp_tool`: 600 seconds in Claude. Serf's current `command` default is 60 seconds.
- `prompt`: 30 seconds.
- `agent`: 60 seconds.
- `UserPromptSubmit`: `command`, `http`, and `mcp_tool` default to 30 seconds.
- `MessageDisplay`: default 10 seconds.

### `command` (implemented)

Shell-form (`bash -c`) and exec-form (`args`, direct spawn) execution, `shell` selection, and `${CLAUDE_PLUGIN_ROOT}`/`${PLUGIN_ROOT}` placeholder expansion are implemented; see [Hooks → The `command` handler](../hooks.md#the-command-handler). The command-only `async`/`asyncRewake` fields are parsed but their async execution is reserved ([Phase C](#phase-c--handler-types-beyond-commandprompt-reserved-placeholder-agent-is-experimental)); `${CLAUDE_PROJECT_DIR}`/`${CLAUDE_PLUGIN_DATA}` placeholder expansion inside command strings is also reserved (`CLAUDE_PROJECT_DIR` is available as an environment variable).

### `http` (reserved-placeholder)

Fields: `type: "http"`, `url` (required), optional `headers`, optional `allowedEnvVars`. Reserved behavior (Phase C):

- POST the hook input JSON as the request body.
- `2xx` empty response means success/no output; `2xx` plain text is added as context; `2xx` JSON is parsed as hook JSON output.
- Non-`2xx`, connection failure, and timeout are non-blocking hook errors.
- HTTP cannot block by status code alone; blocking must be returned in `2xx` JSON output.

### `mcp_tool` (reserved-placeholder)

Fields: `type: "mcp_tool"`, `server` (required), `tool` (required), optional `input`. Reserved behavior (Phase C):

- Invoke a configured MCP server/tool through serf's MCP registry.
- Treat tool text output like command stdout.
- A disconnected server or MCP `isError` is non-blocking unless a future policy says otherwise.

### `prompt` (implemented, serf-native sugar)

The serf-native `prompt` handler — legacy `$TOOL_INPUT`/`$TOOL_RESULT`/`$USER_PROMPT`/`$MESSAGE`/`$TOOL_NAME` substitutions run through the session's LLM client, with model text parsed by the shared output parser — is implemented; see [Hooks → The `prompt` handler](../hooks.md#the-prompt-handler-serf-native-sugar). The Claude-compatible `$ARGUMENTS` substitution and the `{ok, reason}` response schema are reserved ([Phase C](#phase-c--handler-types-beyond-commandprompt-reserved-placeholder-agent-is-experimental)) and must be implemented deliberately before docs or tests treat them as current behavior.

### `agent` (reserved/experimental)

Fields: `type: "agent"`, `prompt` (required), optional `model`. Reserved at the `experimental` tier until policy, file access, timeout, prompt construction, result parsing, and lifecycle interactions are tested. When implemented, it must choose one explicit runtime (a visible bounded subagent, or a standalone tool-less LLM helper) and not blur the two, and return the `{ "ok": true|false, "reason": "..." }` schema.

## Handler support by event

Claude documents all five handler types for: `PermissionDenied`, `PermissionRequest`, `PostToolBatch`, `PostToolUse`, `PostToolUseFailure`, `PreToolUse`, `Stop`, `SubagentStop`, `TaskCompleted`, `TaskCreated`, `TeammateIdle`, `UserPromptExpansion`, `UserPromptSubmit`.

Claude documents only `command`, `http`, and `mcp_tool` for: `ConfigChange`, `CwdChanged`, `Elicitation`, `ElicitationResult`, `FileChanged`, `InstructionsLoaded`, `Notification`, `PostCompact`, `PreCompact`, `SessionEnd`, `StopFailure`, `SubagentStart`, `WorktreeCreate`, `WorktreeRemove`.

Claude documents only `command` and `mcp_tool` for: `SessionStart`, `Setup`.

Claude documents `MessageDisplay` as a display hook with a 10-second default timeout, but not in the prompt/agent support list above; treat it as `command`/`http`/`mcp_tool`-only unless official support changes. Serf enforces or diagnoses these compatibility constraints when parsing and displaying hook status. (Serf currently executes only `command` and serf-native `prompt`; `http`/`mcp_tool`/`agent` are reserved and skipped-with-diagnostics.)

## Hook input API contract

### Command API (implemented)

Serf pipes the hook input JSON to the command on stdin and captures stdout, stderr, exit code, timeout, and duration; see [Hooks → The input your hook receives](../hooks.md#the-input-your-hook-receives) and [What your hook returns](../hooks.md#what-your-hook-returns).

### HTTP API (reserved)

- Serf sends hook input JSON as the POST body.
- Serf parses the HTTP response body with the same plain-text/JSON output parser as command stdout.
- HTTP status alone never blocks a serf action.

### MCP API (reserved)

- Serf sends `input` through the named MCP server/tool.
- Text output is parsed like command stdout.
- Structured MCP output is converted to a deterministic text/JSON form before the shared output parser sees it.

### Prompt/agent API

- Today, `prompt` handlers receive input via serf's legacy substitutions and return model text through the shared hook-output parser (`continue`, `systemMessage`, `hookSpecificOutput`, top-level `decision`/`reason`); see [Hooks → The `prompt` handler](../hooks.md#the-prompt-handler-serf-native-sugar). The Claude-compatible `$ARGUMENTS`/appended-JSON input and the `{ok, reason}` response schema are reserved (for both `prompt` and `agent`) and must be implemented deliberately before docs or tests treat them as current behavior.
- Prompt/agent hooks run through existing provider/client/cancellation paths, not a separate client stack.

### Common environment variables for command hooks

The env vars serf sets today — `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PROJECT_DIR`, `CLAUDE_EFFORT` (when configured), and the serf-native `PLUGIN_ROOT` alias — are documented in [Hooks → Command environment variables](../hooks.md#command-environment-variables).

Reserved (not set today): `CLAUDE_PLUGIN_DATA`, `CLAUDE_CODE_REMOTE` (serf has no remote/serve signal reachable at the hook exec site, so it is omitted rather than fabricated), and `CLAUDE_ENV_FILE` for events that support env-file mutation (`SessionStart`, `Setup`, `CwdChanged`, `FileChanged`).

Never put secrets in event diagnostics. If env/header substitution fails, report the variable name, not the value.

## Hook output contract

### General parsing rules and universal JSON fields

The general parsing rules (exit 0 = success / plain-text context / JSON decision; exit 2 = event-specific block with JSON ignored; other non-zero = non-blocking error) and the output JSON serf acts on (`systemMessage`, `terminalSequence`, `hookSpecificOutput.{permissionDecision, permissionDecisionReason, updatedInput, additionalContext}`, and top-level `decision`/`reason`) are implemented; see [Hooks → What your hook returns](../hooks.md#what-your-hook-returns).

Delivery channels (shipped): `hookSpecificOutput.additionalContext` is delivered to the **model** wrapped in a system reminder; the top-level `systemMessage` field is shown to the **user** (diagnostic-warning channel) and is **not** sent to the model. These are distinct channels, not interchangeable.

The remaining universal fields are part of the Claude output contract but not fully honored by serf yet:

- `continue` (default `true`) — Claude: a value of `false` halts further processing for the event. Serf parses it but does not act on it; a hook that returns `continue: false` still runs to completion.
- `stopReason` — Claude: the message surfaced to the user when `continue` is `false`. Serf does not parse or consume it.
- `suppressOutput` (default `false`) — Claude: suppress normal hook-output display. Serf parses it but does not act on it.

The event-specific output schemas below are the **reserved** part of the output contract — the structured decisions serf does not yet honor in full.

### Event-specific output fields

#### Top-level block decision

For events where Claude allows top-level blocking:

```json
{
  "decision": "block",
  "reason": "why"
}
```

Relevant events include `UserPromptSubmit`, `UserPromptExpansion`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `Stop`, `SubagentStop`, `ConfigChange`, and `PreCompact`. For `PostToolUse`/`PostToolUseFailure`, a block decision cannot retroactively undo an already-completed tool execution; it maps only to the event's supported feedback/loop behavior. Serf currently honors the top-level `decision: "block"` for `Stop`/`SubagentStop`.

#### `PreToolUse` (partially shipped; see [Hooks](../hooks.md#what-your-hook-returns))

Preferred schema:

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

**Shipped** (`claude-compatible-subset`): `permissionDecision: "allow"` and `"deny"` are honored, with the reason read from `permissionDecisionReason`. The deprecated compatibility mapping is accepted as a fallback: top-level `decision: "approve"` → `allow`; `decision: "block"` → `deny`; top-level `reason` → `permissionDecisionReason`. The preferred `permissionDecision` wins when both are present. `updatedInput` is applied before validation. `additionalContext` is delivered to the model; see [delivery channels](#general-parsing-rules-and-universal-json-fields).

**Reserved** (`reserved-placeholder`): `ask` and `defer` are recognized but not honored — serf has no interactive permission prompt, so the tool proceeds and a user-visible diagnostic names the unsupported decision. `updatedInput` revalidation (re-running schema validation after input rewrite) is also reserved. `PermissionRequest`/`PermissionDenied` are blocked on a nonexistent approval flow.

#### `PermissionRequest` (reserved)

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

#### `PermissionDenied` (reserved)

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionDenied",
    "retry": true
  }
}
```

Exit codes are ignored for this event; JSON `retry` is the control surface.

#### `PostToolUse` and MCP output updates (reserved)

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "updatedToolOutput": "replacement or annotation",
    "updatedMCPToolOutput": {}
  }
}
```

#### `SessionStart`, `CwdChanged`, and `FileChanged` (context/watch fields reserved)

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

#### `MessageDisplay` (reserved)

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

Serf implements a central table (`agent/internal/hooks/exitcode.go`, `exitBehavior`); it does not treat every exit-code-2 hook as generic denial. The table below is the full Claude contract. Serf **enforces** the exit-2 block only for the events whose dispatch site has a block path today — `PreToolUse`, `Stop`, and `SubagentStop`. For `UserPromptSubmit` (erase prompt) and `PreCompact` (block compaction) the Claude contract blocks, but serf's runners do not yet enforce it (deferred parity, marked ⚠ below); `exitBehavior` returns non-blocking for them so the table never claims a block nothing consumes. Every other event is reserved and defaults to non-blocking so an unimplemented event never blocks.

| Event | Exit 0 | Exit 2 | Other non-zero |
|---|---|---|---|
| `PreToolUse` | parse output | block tool call | non-blocking error |
| `PermissionRequest` | parse output | deny permission | non-blocking error |
| `UserPromptSubmit` | parse output | block prompt and erase prompt (⚠ Claude contract; serf does not yet enforce the block) | non-blocking error |
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
| `PreCompact` | parse output | block compaction (⚠ Claude contract; serf does not yet enforce the block) | non-blocking error |
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

## Async semantics (reserved)

`async`/`asyncRewake` are parsed but not executed today (Phase C). When implemented:

- `async` and `asyncRewake` are command-only; `asyncRewake` implies `async`.
- Async hooks cannot block or alter behavior for the action that already completed.
- On async completion, `additionalContext` is delivered on the next conversation turn where supported; `systemMessage` is user-visible only.
- Async hook cancellation/cleanup must be explicit when the session closes.

## Diagnostics and status

The implemented diagnostics — loud load-time `WARNING`s for unknown events, recognized-but-unsupported events, invalid-regex matchers, and unsupported handler types, plus the tier-labeled `/status` view — are documented in [Hooks → Misconfiguration warnings](../hooks.md#misconfiguration-warnings-loud-not-silent). `/status` reports the recognized event landscape and its tiers; it is not a misconfiguration report.

The full diagnostic field set spans the reserved handler types as well. Where available, a hook diagnostic carries: plugin name, hook source path, event name, matcher, handler type, timeout, unsupported tier/reason, exit code and duration for completed runs, and a sanitized error category (`parse_error`, `unsupported_event`, `unsupported_handler`, `invalid_matcher`, `timeout`, `cancelled`, `command_error`, `http_error`, `mcp_error`, `prompt_error`, `hook_blocked`).

**Redaction contract (all tiers).** Diagnostics must never include raw tool input/output (unless explicitly known safe), API keys/tokens, HTTP header values, env var values, provider request/response bodies, or full transcript bodies. If env/header substitution fails, report the variable name, not the value.

## Roadmap (deferred phases)

Phase 1 is the shipped `claude-compatible-subset`, documented in [Hooks](../hooks.md). The remaining phases are a roadmap, not a build plan; each item stays `reserved-placeholder` (parsed/diagnosed, never advertised as working) or `experimental` until it ships. The phase boundary is explicit: Phase 1 = "the hooks that already fire, made Claude-compatible in matcher / exec / IO / exit-code, with honest diagnostics for everything else."

### Phase B — core current Claude events and output schemas

**Shipped (Phase B partial):**
- `PreToolUse` `allow`/`deny` decisions, `permissionDecisionReason`, and the deprecated `approve`/`block` top-level mapping.
- `hookSpecificOutput.additionalContext` model-delivery channel (system reminder); `systemMessage` user-display channel.

**Remaining reserved (`reserved-placeholder`):**
- Parser recognition and runner methods for `PostToolUseFailure`, `PostToolBatch`, `SubagentStart`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`, `UserPromptExpansion`, `StopFailure` — wired only at real serf runtime boundaries.
- `PreToolUse` `ask`/`defer` decisions (no interactive permission prompt) and `updatedInput` revalidation after input rewrite.
- The `PermissionRequest` decision object and `PermissionDenied.retry`, gated on a real serf approval flow.
- Top-level `decision: "block"` for the broader event set; `if` permission-rule evaluation.

### Phase C — handler types beyond command/prompt (`reserved-placeholder`; `agent` is `experimental`)

- `http` (shared input/output parser, non-blocking HTTP failure semantics).
- `mcp_tool` (through the existing MCP registry only).
- `prompt` `$ARGUMENTS` + `{ok, reason}` (preserving serf legacy substitutions as sugar).
- `agent` as `experimental`, after prompt hooks and subagent policy wiring are stable.
- Async command execution (`async`/`asyncRewake`), after synchronous command behavior is fully tested.

### Phase D — modern lifecycle/watch/team events (`reserved-placeholder`)

- `Setup`, `InstructionsLoaded`, `MessageDisplay`, `CwdChanged`, `FileChanged` — only if serf has matching lifecycle boundaries.
- `watchPaths`, `reloadSkills`, `displayContent`, env-file behavior where meaningful.
- `TaskCreated`, `TaskCompleted`, `TeammateIdle`, `WorktreeCreate`, `WorktreeRemove`, `Elicitation`, `ElicitationResult` — only if the underlying serf features exist.
- Featureless compatibility names stay parse-and-diagnose; never fabricate behavior.

### Phase E — SDK lifecycle hooks (`reserved-placeholder`)

- Typed SDK hook structs (the `LifecycleHooks` shape above), in-process and typed, never exposing Claude JSON as the SDK API.
- The same canonical lifecycle order as plugin hooks.
- Cancellation/timeout/error policy tests before documenting SDK hooks as stable.

## Acceptance criteria

These are the contract the implemented behavior satisfies (Phase 1 — shipped and verified in [Hooks](../hooks.md) and the tests below) plus the invariants future phases must preserve.

- Existing plugin hooks using `hooks/hooks.json` with `SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionEnd`, `PreCompact`, or `Notification` continue to load and run.
- Wrapper and direct hook JSON formats remain supported.
- Parser diagnostics include source path, event, matcher, handler index/type, and a sanitized reason for invalid configs; misconfigured event names and invalid matchers warn loudly at load.
- Matcher behavior follows Claude semantics for empty/star, exact, pipe-list, and regex modes, with the documented Go RE2 caveat.
- `Bash` no longer regex-substring matches `BashOutput`; `Edit|Write` exact-list matches only those names.
- Negative matcher tests cover non-substring behavior for tools and MCP names, pipe-list matching for non-tool events, invalid-regex diagnostics, and empty-matcher behavior.
- MCP tool names such as `mcp__memory__search` match regex `mcp__memory__.*` but not exact matcher `mcp__memory`.
- Command hooks support shell form and exec-form `args` without requiring plugin authors to wrap every path in `bash -c`.
- Hook input includes official common fields when available and preserves legacy aliases during migration.
- `additionalContext` is delivered to the model (system reminder); `systemMessage` is shown to the user only — the two fields are distinct in data model and delivery channel.
- Exit-code 2 behavior is event-specific and table-driven; JSON output is parsed only on exit 0 for command hooks.
- HTTP hooks, once implemented, cannot block by status code alone.
- Unsupported Claude events/handler types are reported as unsupported/reserved, not silently treated as implemented.
- SDK hooks, if added, cannot bypass plugin trust mode, final execution policy, or child effective capability policy.

## Required tests

The implemented subset is covered by `agent/internal/hooks/*_test.go`, `agent/plugin/*_test.go`, the session-level hook/warning tests in `agent/`, and the live scenario card `test/scenarios/hooks-claude-compat-matcher.md` (recapped in [Hooks → How this is tested](../hooks.md#how-this-is-tested)). The matrix below is the spec's full verification plan; future phases extend it.

### Parser/config tests

- Wrapper format with top-level `description` and `hooks` object; direct event-at-top-level format.
- Manifest hook path, inline hook object, and default `hooks/hooks.json` discovery.
- Unknown event vs recognized-but-unsupported Claude event diagnostics (and the loud load-time warnings for each).
- Unsupported handler type diagnostics.
- Handler field parsing for `args`, `shell`, `async`, `asyncRewake`, `if`, `statusMessage`, and the reserved `url`/`headers`/`allowedEnvVars`/`server`/`tool`/`input`/`prompt`/`model`.
- Timeout defaults for command/prompt (and, when implemented, http/mcp/agent plus the `UserPromptSubmit`/`MessageDisplay` overrides).

### Matcher tests

- Omitted, empty, and `*` match all.
- Exact string matcher does not substring-match.
- Pipe-list exact alternatives.
- Regex matcher for an MCP server namespace.
- Invalid regex produces a diagnostic and skips the hook.
- Event-specific matcher target tests for `SessionStart`, `PreCompact`, `Notification`, `SubagentStop`, and tool events.

### Command execution tests

- Shell-form command uses the configured shell.
- Exec-form `args` bypasses the shell and handles paths with spaces.
- Env vars include official names and the `PLUGIN_ROOT` alias.
- Timeout/cancellation kills the command and emits a sanitized diagnostic.
- Exit code and stderr are captured accurately.

### Output parser tests

- Exit 0 empty stdout = no decision; exit 0 plain text = context/message; exit 0 universal JSON fields.
- Exit 2 ignores JSON and follows event-specific behavior.
- Separate routing for `systemMessage`, `additionalContext`, and `terminalSequence`.
- (Shipped) `PreToolUse` `allow`/`deny`, `permissionDecisionReason`, deprecated `approve`/`block` mapping, and `updatedInput` rewrite; `additionalContext` model delivery and `systemMessage` user display.
- (Reserved phases) `PreToolUse` `ask`/`defer` and `updatedInput` revalidation; top-level `decision: "block"` for the broader set; `PermissionRequest`/`PermissionDenied` schemas.

### Ordering and policy tests

- Tool ordering: decoded/best-effort hook input → `PreToolUse` → updated-input merge → final schema validation → final execution policy → tool execution → post hook.
- Plugin `PreToolUse` cannot bypass final execution policy by changing input to an invalid/disallowed shape.
- `PostToolUse` cannot undo an already-executed tool.
- `Stop`/`SubagentStop` blocking has a loop guard.
- `SessionStart` fires once for startup/resume kinds as applicable.
- `SessionEnd` fires on normal close and cancellation without blocking shutdown.

## Caveats

- **Go RE2 matcher, not JavaScript regex**, and **matchers run against the Claude tool name** (`shell` → `Bash`) are the two author-facing caveats, documented with examples in [Hooks → Go RE2, not JavaScript regex](../hooks.md#go-re2-not-javascript-regex) and [Hooks → The #1 mistake](../hooks.md#the-1-mistake-shell--bash). At the spec level: if exact JS regex parity ever becomes required, a JS regex engine may be introduced deliberately (see [Non-goals](#non-goals)); until then the RE2 subset is the contract.
- Some Claude events describe products/features serf may not have. Those names remain reserved placeholders until there is a real runtime boundary.
- Claude hook docs are updated over time. This document cites <https://code.claude.com/docs/en/hooks> as the authoritative current compatibility reference; update the tables when that page changes.
- Serf's hook subsystem is already useful for simple plugins, but it is not full Claude Code hook parity. Status and diagnostics say so.
