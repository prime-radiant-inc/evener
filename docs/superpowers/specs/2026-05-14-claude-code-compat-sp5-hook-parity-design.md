# SP5 — Hook Parity (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Sibling spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-sp1-config-loader-design.md`

## 1. Goal

SP5 brings serf's hook surface up to the Claude Code A-tier. It adds nine new events at their correct serf integration points, three new hook handler types (`http`, `mcp_tool`, `agent`), six new hook-config fields, five new output fields, five new input fields, and three new environment variables. It also replaces the current `regexp.Compile`-on-every-call matcher with the documented dual-mode algorithm (exact-or-pipe-list vs JavaScript regex) so plugin authors can write `"Bash"` or `"Edit|Write"` and get the literal match they expect, while regex patterns like `"mcp__.*"` continue to work.

SP5 owns hook *machinery* — event firing, type dispatch, field plumbing, output parsing. It does not own permission rule grammar (SP2) or config loading (SP1). The `if` field is a permission rule string that SP5 passes to SP2's matcher for evaluation; SP5's only responsibility is wiring the filter into hook dispatch.

The deliverable is additive: existing nine events (`PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, `PreCompact`, `Notification`) and existing two hook types (`command`, `prompt`) keep working byte-identically.

## 2. Public API Surface

All new symbols live in package `agent`, alongside the existing `plugin_hooks.go` types.

### 2.1 HookEvent constants

```go
const (
    // Existing — unchanged.
    HookPreToolUse       HookEvent = "PreToolUse"
    HookPostToolUse      HookEvent = "PostToolUse"
    HookStop             HookEvent = "Stop"
    HookSubagentStop     HookEvent = "SubagentStop"
    HookUserPromptSubmit HookEvent = "UserPromptSubmit"
    HookSessionStart     HookEvent = "SessionStart"
    HookSessionEnd       HookEvent = "SessionEnd"
    HookPreCompact       HookEvent = "PreCompact"
    HookNotification     HookEvent = "Notification"

    // New A-tier events.
    HookPostToolUseFailure HookEvent = "PostToolUseFailure"
    HookPostToolBatch      HookEvent = "PostToolBatch"
    HookStopFailure        HookEvent = "StopFailure"
    HookSubagentStart      HookEvent = "SubagentStart"
    HookUserPromptExpansion HookEvent = "UserPromptExpansion"
    HookPostCompact        HookEvent = "PostCompact"
    HookPermissionRequest  HookEvent = "PermissionRequest"
    HookPermissionDenied   HookEvent = "PermissionDenied"
    HookConfigChange       HookEvent = "ConfigChange"
)
```

`validHookEvents` gains all nine new keys.

### 2.2 RegisteredHook additions

```go
type RegisteredHook struct {
    // Existing.
    Matcher    string
    Type       string // now: "command" | "prompt" | "http" | "mcp_tool" | "agent"
    Command    string
    Prompt     string
    Timeout    int
    Model      string
    PluginName string
    PluginDir  string

    // New.
    Args          []string   // exec form for type=command; nil → shell form
    Async         bool       // fire-and-forget; output discarded
    AsyncRewake   bool       // implies Async; exit-2 wakes the agent loop
    Shell         string     // "bash" | "powershell"; empty = platform default
    If           string     // SP2 permission-rule filter; empty = always run
    StatusMessage string     // UI spinner text while hook runs

    // type=http
    URL             string
    Headers         map[string]string
    AllowedEnvVars  []string

    // type=mcp_tool
    MCPServer string
    MCPTool   string
    MCPInput  map[string]any

    // type=agent
    AgentType string // optional, used as matcher dimension on SubagentStart-relevant variants
}
```

`hookSpec` (the JSON-parsed shape) gains the matching tag set. A new helper `parseHookSpec(raw json.RawMessage, pluginDir string) (RegisteredHook, error)` replaces the inline anonymous-struct dance in `ParsePluginHooks` so the four hook types can share field extraction.

### 2.3 HookInput additions

```go
type HookInput struct {
    // Existing.
    SessionID     string         `json:"session_id"`
    CWD           string         `json:"cwd"`
    HookEventName string         `json:"hook_event_name"`
    ToolName      string         `json:"tool_name,omitempty"`
    ToolInput     map[string]any `json:"tool_input,omitempty"`
    ToolResult    string         `json:"tool_result,omitempty"`
    UserPrompt    string         `json:"user_prompt,omitempty"`
    Reason        string         `json:"reason,omitempty"`

    // New common fields.
    TranscriptPath string         `json:"transcript_path,omitempty"`
    PermissionMode string         `json:"permission_mode,omitempty"`
    Effort         *EffortField   `json:"effort,omitempty"`
    AgentID        string         `json:"agent_id,omitempty"`
    AgentType      string         `json:"agent_type,omitempty"`
    ToolUseID      string         `json:"tool_use_id,omitempty"`

    // Event-specific fields. All marked omitempty so the JSON
    // piped to a hook never contains a field the event does not own.
    ToolError       string             `json:"tool_error,omitempty"`         // PostToolUseFailure
    ToolResults     []BatchToolResult  `json:"tool_results,omitempty"`       // PostToolBatch
    ErrorType       string             `json:"error_type,omitempty"`         // StopFailure
    ErrorMessage    string             `json:"error_message,omitempty"`      // StopFailure
    ExpansionType   string             `json:"expansion_type,omitempty"`     // UserPromptExpansion
    CommandName     string             `json:"command_name,omitempty"`       // UserPromptExpansion
    CommandArgs     string             `json:"command_args,omitempty"`       // UserPromptExpansion
    CommandSource   string             `json:"command_source,omitempty"`     // UserPromptExpansion
    Prompt          string             `json:"prompt,omitempty"`             // SubagentStart, UserPromptExpansion
    CompactTrigger  string             `json:"compact_trigger,omitempty"`    // PostCompact
    PermissionRule  string             `json:"permission_rule,omitempty"`    // PermissionRequest
    PermissionCat   string             `json:"permission_category,omitempty"`// PermissionRequest
    DenialReason    string             `json:"denial_reason,omitempty"`      // PermissionDenied
    ConfigSource    string             `json:"config_source,omitempty"`      // ConfigChange
    ConfigFile      string             `json:"config_file,omitempty"`        // ConfigChange
    ChangedKeys     []string           `json:"changed_keys,omitempty"`       // ConfigChange
}

type EffortField struct {
    Level string `json:"level"`
}

type BatchToolResult struct {
    ToolName     string         `json:"tool_name"`
    ToolUseID    string         `json:"tool_use_id"`
    ToolInput    map[string]any `json:"tool_input"`
    ToolResponse any            `json:"tool_response"`
    Succeeded    bool           `json:"succeeded"`
}
```

`Session.hookInput(event HookEvent) HookInput` is extended to populate `TranscriptPath`, `PermissionMode`, `Effort`, `AgentID`, and `AgentType` at construction time, from the live session state. §8 lists the sources.

### 2.4 ParsedHookOutput additions

```go
type ParsedHookOutput struct {
    // Existing.
    Continue       bool
    SuppressOutput bool
    SystemMessage  string
    Denied         bool
    UpdatedInput   map[string]any
    Blocked        bool
    BlockReason    string
    IsError        bool
    RawExitCode    int

    // New.
    Deferred                 bool                // permissionDecision == "defer"
    PermissionDecisionReason string              // accompanies allow/deny/ask/defer
    AdditionalContext        string              // hookSpecificOutput.additionalContext
    AdditionalContextOverflow string             // file path when context > 10,000 chars
    SessionTitle             string              // UserPromptSubmit only
    AddPermissionRule        string              // PermissionRequest only
    Retry                    bool                // PermissionDenied only
    StopReason               string              // top-level stopReason when continue=false
}
```

`Continue == false` is a directive to stop the agent loop entirely; existing call sites already honor it.

### 2.5 HookRunner — new dispatch methods

```go
// One Run<Event> per new event. Each takes the prepared HookInput and
// returns the per-event aggregated result.

func (r *HookRunner) RunPostToolUseFailure(ctx context.Context, input HookInput) HookRunResult
func (r *HookRunner) RunPostToolBatch(ctx context.Context, input HookInput) PostToolBatchResult
func (r *HookRunner) RunStopFailure(ctx context.Context, input HookInput) // no return: observability only
func (r *HookRunner) RunSubagentStart(ctx context.Context, input HookInput) HookRunResult
func (r *HookRunner) RunUserPromptExpansion(ctx context.Context, input HookInput) UserPromptExpansionResult
func (r *HookRunner) RunPostCompact(ctx context.Context, input HookInput) HookRunResult
func (r *HookRunner) RunPermissionRequest(ctx context.Context, input HookInput) PermissionRequestResult
func (r *HookRunner) RunPermissionDenied(ctx context.Context, input HookInput) PermissionDeniedResult
func (r *HookRunner) RunConfigChange(ctx context.Context, input HookInput) ConfigChangeResult
```

Aggregate types:

```go
type PostToolBatchResult struct {
    Blocked        bool
    BlockReason    string
    SystemMessages []string
}

type UserPromptExpansionResult struct {
    Blocked        bool
    BlockReason    string
    SystemMessages []string
}

type PermissionRequestResult struct {
    Behavior          string         // "allow" | "deny" | "" (no opinion)
    Reason            string
    UpdatedInput      map[string]any
    AddPermissionRule string
    SystemMessages    []string
}

type PermissionDeniedResult struct {
    Retry          bool
    SystemMessages []string
}

type ConfigChangeResult struct {
    Blocked        bool
    BlockReason    string
    SystemMessages []string
}
```

`PreToolUseResult` and `HookRunResult` gain an `AdditionalContext []string` field so the agent can route additionalContext separately from SystemMessages. The existing `SystemMessages` channel keeps its meaning ("show to user"); additionalContext is "feed to model only".

### 2.6 AsyncRewake wake channel

```go
// Session field, set during initPlugins.
asyncRewake chan AsyncRewakeSignal

type AsyncRewakeSignal struct {
    PluginName string
    HookType   string
    Event      HookEvent
    Stdout     string
    Stderr     string
    ExitCode   int
}
```

Async hooks with `asyncRewake: true` and exit code 2 push to this channel. The main agent loop's per-round select adds a non-blocking read; see §3.10 for the precise loop contract.

## 3. Per-Event Sections

Each event spec covers: integration point, input schema, output schema, decision semantics, and exit-code behavior. The schemas list only the *added* fields beyond the §2.3 common set. Every event input includes `session_id`, `cwd`, `hook_event_name`, `transcript_path`, and (where applicable) `permission_mode`, `effort`.

### 3.1 `PostToolUseFailure`

**Integration point.** `agent/session.go:execTool`, immediately after the `PostToolUse` block, gated on `res.IsError == true`. The existing `PostToolUse` run continues to fire for successful tool calls only; failures fork to this new event.

Behavioral change: the current code fires `PostToolUse` for both success and failure. SP5 keeps `PostToolUse` firing on success and adds `PostToolUseFailure` for failure. Plugins relying on `PostToolUse` seeing failures continue to work because nothing is *removed* from `PostToolUse`'s contract; SP5 ships a one-line behavior note in the changelog and the hook docs (this is the only Claude-Code-compat semantic change for existing events — every other event keeps its current trigger).

Wait — the safer behavior is to keep firing `PostToolUse` for both outcomes, and additionally fire `PostToolUseFailure` for failure. That preserves the existing serf contract verbatim and matches the Claude Code documented semantics (the docs do not say `PostToolUse` skips failures). SP5 ships with this dual-fire behavior; the `tool_response` field on `PostToolUse` carries the error text when applicable.

**Input fields (event-specific).**

| Field | Type | Source |
|---|---|---|
| `tool_name` | string | mapped via `MapSerfToolNameToClaude(call.Name)` |
| `tool_input` | object | `call.Arguments` unmarshaled |
| `tool_error` | string | `res.FullOutput` when `res.IsError` |
| `tool_use_id` | string | `call.ID` |

**Output schema.** Standard universal fields plus `hookSpecificOutput.additionalContext`. `decision: "block"` halts the agent loop after the failed tool (sets `Blocked=true`; honored by session loop as a turn break).

**Exit code.** 0 → parse JSON; 2 → `Blocked=true`, stderr fed to model as system message; non-zero non-2 → infra-error, logged as warning, loop continues.

### 3.2 `PostToolBatch`

**Integration point.** `agent/session.go` round loop, after the `for i := range calls { results[i] = ... }` block resolves, before the `appendTurn(TurnToolResults, ...)` call. Fires once per batch regardless of whether the batch ran in parallel or serial. Matcher is unused (always fires); the existing `runAll` path is bypassed via a new `runAllUnmatched` helper that skips tool-name matcher logic.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `tool_results` | `[]BatchToolResult` | derived from `results []ToolExecResult` |

For each `ToolExecResult`, `tool_response` is `r.FullOutput` (string for command/text tools; raw JSON when the tool returned structured output and a future tool registry change exposes it). `succeeded` is `!r.IsError`.

**Output schema.** Universal + `additionalContext` + top-level `decision: "block" | "allow"` + `reason`.

**Decision semantics.** A `block` decision (top-level `decision` field OR exit code 2) stops the agent loop after this batch: the next LLM call is skipped and the session transitions to `SessionIdle` with `reason` surfaced as a system message. This mirrors `Stop`'s blocking semantics, applied at batch boundary instead of turn boundary.

`additionalContext` from any hook in this event is forwarded into the next round's history as a synthetic user-role steering turn (same path as `RunUserPromptSubmit`'s system messages today, but routed through the additionalContext field on `HookRunResult`).

### 3.3 `StopFailure`

**Integration point.** Every existing place in `session.go` that returns from `processOneInput` due to an API error path — `llm` call returns a non-nil error after retry exhaustion, `contentFilterRetried` fails, `emptyResponsesExhaustedError` and `bareTextWithoutResultToolError` exits. A small helper `s.classifyAPIError(err) (errorType string)` maps Go errors to Claude Code's enumerated strings (`rate_limit`, `authentication_failed`, `oauth_org_not_allowed`, `billing_error`, `invalid_request`, `server_error`, `max_output_tokens`, `unknown`).

Matcher is the `error_type` string.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `error_type` | string | classifier output |
| `error_message` | string | `err.Error()` (truncated to 4 KB before piping) |

**Output schema.** Universal only. Return value is `()`; SystemMessages aggregated but the session is already terminating, so they are logged to stderr rather than steered.

**Exit code.** Ignored. This event is for observability; SP5 explicitly does not give hooks a way to recover a failed turn (matches CC doc).

### 3.4 `SubagentStart`

**Integration point.** `agent/subagents.go:spawnAgent`, between `NewSession` returning and the `s.emit(EventSubagentStart, ...)` call. Specifically: after `subSess` exists and `sub` is wired into `s.subagents`, before `go sub.run(...)`. Firing pre-run gives hooks the chance to inject `additionalContext` that the subagent sees on its first LLM call (subagent's own SessionStart event already runs first; SubagentStart fires *additionally* and its additionalContext is appended to the same context-injection path).

Matcher is the `agent_type` string (`"general-purpose"`, plugin agent names, etc.). Empty matcher matches any spawn.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `agent_id` | string | `sub.id` |
| `agent_type` | string | `agentType` parameter, or `"general-purpose"` if empty |
| `prompt` | string | `task` parameter |

**Output schema.** Universal + `additionalContext`. No decision control: a hook cannot block subagent startup, matching CC. `additionalContext` is injected into the subagent's history as a steering message before its first turn.

**Exit code.** 0 normal; 2 system-message-only (shown to user); non-zero non-2 warned.

### 3.5 `UserPromptExpansion`

**Integration point.** Slash command expansion happens in two future SP slots (commands invocation is deferred per parent spec), but serf already expands `:skill:` skill invocations and built-in slash forms inline before `ProcessInput`. SP5 wires `UserPromptExpansion` at the existing skill-activation site in `agent/session.go` (search for `ActivatedSkillBodies` / skill-resolution in `processOneInput`). The hook fires after the input string has been classified as an expansion candidate and the expansion has produced the final prompt text, but before the expanded text is appended to history as `TurnUserInput`.

For MCP prompts, the integration site is wherever MCP prompts are expanded — today this is not implemented; SP5 fires the hook at the future expansion site as a no-op when MCP prompts are absent.

Matcher is the `command_name` string. Empty matcher matches all expansions.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `expansion_type` | string | `"slash_command"` or `"mcp_prompt"` |
| `command_name` | string | skill/command name without leading slash |
| `command_args` | string | text after the command name |
| `command_source` | string | `"skill"`, `"plugin"`, `"builtin"`, or `"mcp"` |
| `prompt` | string | the expanded prompt text |

**Output schema.** Universal + `additionalContext` + top-level `decision: "block"` + `reason`.

**Decision semantics.** `decision: "block"` (or exit code 2) cancels the expansion: the original user input is dropped, a system message displays the reason, and `processOneInput` returns to `SessionAwaitingInput`. `additionalContext` is appended to the expanded prompt before the LLM sees it.

### 3.6 `PostCompact`

**Integration point.** `agent/context_strategy.go:CompactStrategy.ManageContext`, after `s.cm.MaybeCompact` returns successfully. The current code has a `PreCompact` hook firing before; SP5 adds `PostCompact` after. Detection of whether compaction actually occurred: `MaybeCompact` returns or sets a flag; SP5 introduces a `Compacted bool` return on the strategy interface (additive, default false).

Matcher is `compact_trigger`, one of `"manual"` (user-invoked, future) or `"auto"` (current MaybeCompact path).

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `compact_trigger` | string | `"auto"` for MaybeCompact-triggered, `"manual"` reserved |

**Output schema.** Universal only. No decision control; the compaction has already happened. Use cases are logging and follow-up cache warming.

**Exit code.** 0 normal; 2 logs stderr as warning; non-zero non-2 warned.

### 3.7 `PermissionRequest`

**Integration point.** Paired with SP2. SP2 owns the decision point ("is this tool call allowed?"). Before SP2 surfaces a dialog (i.e., when the rule set yields `ask`), SP2 calls `s.hookRunner.RunPermissionRequest(ctx, hi)` and uses the result to either short-circuit (if `Behavior` is `"allow"` or `"deny"`) or fall through to the user dialog.

Integration site is in SP2's enforcement layer (`agent/permissions.go`); SP5 defines the hook contract and the dispatch method, SP2 calls it.

Matcher is `tool_name`.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `tool_name` | string | the tool that triggered the dialog |
| `tool_input` | object | the tool's arguments |
| `tool_use_id` | string | the LLM-assigned call ID |
| `permission_rule` | string | SP2's matched rule string (e.g., `"Bash(rm:*)"`) |
| `permission_category` | string | SP2's classification (e.g., `"destructive"`) |

**Output schema.** Universal + nested `hookSpecificOutput.decision`:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {
      "behavior": "allow|deny",
      "updatedInput": { "...": "tool-specific" },
      "addPermissionRule": "Bash(ls:*)"
    }
  }
}
```

**Decision semantics.** When `behavior` is `"allow"`, SP2 allows the tool call without surfacing the dialog. When `"deny"`, SP2 denies it and shows the user the `permissionDecisionReason` (or `reason`). `updatedInput` rewrites the tool input before execution (same semantics as PreToolUse's `updatedInput`). `addPermissionRule` instructs SP2 to extend the live `permissions.allow` set with the new pattern for the rest of the session. The persisted-to-disk variant is deferred (no plugin should silently mutate user config).

If multiple hooks fire and disagree, deny wins; among allows, the first non-empty `addPermissionRule` wins; `updatedInput` merges by key with last-write-wins per key (matches PreToolUse).

**Exit code.** 0 → parse; 2 → treated as `deny` with stderr as `reason`; non-zero non-2 → infra error, fall through to user dialog.

### 3.8 `PermissionDenied`

**Integration point.** SP2 enforcement layer, immediately after `auto` / `dontAsk` / `bypassPermissions` modes deny a tool call without surfacing a dialog. The denied tool call has already returned an error to the model; this hook is a notification.

Matcher is `tool_name`.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `tool_name` | string | the denied tool |
| `tool_input` | object | the denied tool's arguments |
| `tool_use_id` | string | call ID |
| `denial_reason` | string | SP2's textual reason for denial |

**Output schema.**

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionDenied",
    "retry": true
  }
}
```

**Decision semantics.** Only `retry: true` is meaningful. When set, SP2 surfaces a system message to the model along the lines of "you may retry this tool call". The actual retry is the model's responsibility.

**Exit code.** Per CC: ignored. The denial has already happened. SP5 still captures stderr for the session log, but does not feed it back to the model.

### 3.9 `ConfigChange`

**Integration point.** Mid-session config-file reloading is not yet a serf feature. SP1 lays down the loader; SP8 wires it. SP5 fires `ConfigChange` at the future reload site, which is a file-watcher loop SP5 itself adds in `agent/config_watcher.go` (a small fsnotify-backed goroutine started by `Session` when `cfg.WatchConfig` is true; default off). The watcher emits a `ConfigChangeSignal` to a channel; the main session loop reads it and calls `RunConfigChange`.

Matcher is `config_source`, one of `"user_settings"`, `"project_settings"`, `"local_settings"`, `"policy_settings"`, `"skills"`. The first three are config-tier names mapped from SP1's `ConfigTier`. `"policy_settings"` is reserved (admin/MDM, not in scope for serf v1). `"skills"` covers skill-file changes when serf gains live-skill-reload.

**Input fields.**

| Field | Type | Source |
|---|---|---|
| `config_source` | string | the tier name |
| `config_file` | string | absolute path of changed file |
| `changed_keys` | `[]string` | top-level keys that differ from previous load |

**Output schema.** Universal + top-level `decision: "block"` + `reason`.

**Decision semantics.** `decision: "block"` (or exit code 2) rejects the reload: the new config is dropped, the old config stays in effect, and `reason` is surfaced as a system warning. Exception: `policy_settings` cannot be blocked even by hooks; for that source, a `block` decision becomes an advisory warning.

### 3.10 AsyncRewake — Main-Loop Integration

Open question 3 is resolved here. Async-with-rewake hooks must be able to interrupt the agent's quiescent or in-flight state.

**Mechanism.**

1. When a hook is dispatched and its `Async == true`, the goroutine running the hook is detached. Its `ParsedHookOutput` is discarded.
2. When `AsyncRewake == true` and the hook exits with code 2, the dispatch goroutine sends an `AsyncRewakeSignal` to `s.asyncRewake` (buffered channel, capacity 16; full → drop with warning).
3. The main agent loop in `processOneInput` adds a non-blocking select arm at the top of each round and before each `LLMCall` phase:

   ```go
   select {
   case sig := <-s.asyncRewake:
       s.Steer(formatRewakeMessage(sig)) // injects stderr as a steering message
   default:
   }
   ```

4. The same arm runs after the model returns and before tool dispatch, so an in-flight LLM call cannot delay rewake by more than one round.
5. When the session is in `SessionAwaitingInput` (no input pending), the rewake signal is dropped on the floor with a debug log. Rationale: a paused session has no one to steer; the hook author asked for a rewake on a running session.

`formatRewakeMessage` wraps the hook's stderr in `<async-hook-rewake plugin=... event=...>...</async-hook-rewake>` tags so the model can identify the source.

`Async: true, AsyncRewake: false` hooks have their result discarded entirely — neither stdout nor stderr is captured beyond a brief stderr-truncated log at debug level. Plugin authors who want side-channel effects (file writes, network notifications) use `async: true`. Plugin authors who want to interrupt use `asyncRewake: true`.

## 4. Per-Hook-Type Sections

### 4.1 `command` (existing, extended)

New supported fields: `args`, `async`, `asyncRewake`, `shell`, `if`, `statusMessage`. See §5.

**Exec form vs shell form.**

- `args` absent (or empty) → shell form. Current behavior: `bash -c <command>` on Unix. New: `shell` field selects shell. `"bash"` → `bash -c` (default); `"powershell"` → `powershell -NoProfile -Command` on Windows, falls back to `bash -c` on non-Windows with a one-time warning per plugin per hook.
- `args` non-empty → exec form. The `command` field is treated as the executable path or name (PATH-resolved). `args` is the argument vector. No shell involved. Globs, pipes, redirections do not expand. `${CLAUDE_PLUGIN_ROOT}` substitution still happens in `command` and in each `args[i]`.

This means a plugin can write:

```json
{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/check", "args": ["--strict", "${tool_input.file_path}"] }
```

…and get a shell-free spawn with the captured substitution. (The `${tool_input.field}` substitution applies to `args[i]` strings; see §5.6 for the substitution grammar.)

### 4.2 `http`

**Config shape.**

```json
{
  "type": "http",
  "url": "https://example.com/hooks/validate",
  "headers": { "Authorization": "Bearer $TOKEN" },
  "allowedEnvVars": ["TOKEN"],
  "timeout": 30,
  "if": "Bash(*)",
  "statusMessage": "Validating with policy server..."
}
```

**Execution semantics.**

1. POST to `URL` with the hook's `HookInput` JSON as the body. `Content-Type: application/json`.
2. Headers from `Headers` map are added. Header values undergo a restricted `$VAR` / `${VAR}` substitution: only env vars listed in `AllowedEnvVars` are expanded; everything else is passed verbatim. The default-deny posture protects against header smuggling.
3. `URL` undergoes `${CLAUDE_PLUGIN_ROOT}` substitution but not env-var substitution (avoid host-spoofing surprises).
4. Timeout default: 30 seconds.

**Response parsing.**

- 2xx with empty body → success, exit-code-0 equivalent.
- 2xx with `Content-Type: application/json` (or body parses as JSON) → fed through `parseHookOutput` exactly like command stdout.
- 2xx with non-JSON text body → `SystemMessage` = body.
- Non-2xx → infra error: capture body in stderr-equivalent slot, treat as exit-non-zero-non-2 (warned, loop continues).
- Connection failure, DNS error, TLS error, timeout → infra error.

**Implementation file.** `agent/plugin_hooks_http.go` with `executeHTTPHook(ctx context.Context, hook RegisteredHook, input HookInput) (HookResult, error)`. Uses the standard library's `net/http` with a per-hook `http.Client` (no connection sharing across hooks; hooks may run in parallel and TLS verification mode is fixed-strict). No additional dependency.

### 4.3 `mcp_tool`

**Config shape.**

```json
{
  "type": "mcp_tool",
  "server": "policy-server",
  "tool": "check_command",
  "input": {
    "command": "${tool_input.command}",
    "session": "${session_id}"
  },
  "timeout": 60,
  "if": "Bash(*)",
  "statusMessage": "Asking policy server..."
}
```

**Execution semantics.**

1. The `server` name resolves to a connected MCP server in `s.mcpMgr`. Unconnected server → infra error (loop continues; warned once per server per session).
2. The `tool` name is invoked on that server with `input` as the argument object. `input` values undergo a `${path}` substitution from the hook's input: `${tool_input.foo}`, `${session_id}`, `${cwd}`, etc.
3. The MCP tool's text output is treated like command stdout: parsed for JSON, falls back to plain `SystemMessage`.
4. If the MCP response sets `isError: true`, it's an infra error.

Timeout default: 60 seconds. Uses the existing MCP manager's call surface (`mcpMgr.CallTool(ctx, server, tool, input)`).

**Implementation file.** `agent/plugin_hooks_mcp.go`.

### 4.4 `agent`

**Config shape.**

```json
{
  "type": "agent",
  "prompt": "Verify $TOOL_INPUT does not touch /etc.",
  "model": "fast",
  "timeout": 60,
  "if": "Write(*)",
  "statusMessage": "Verifying..."
}
```

**Execution semantics.**

1. Spawns an *ephemeral* subagent (not via `spawnAgent` — that's tied to the session's subagent registry). Instead, a lightweight one-shot path in `agent/plugin_hooks_agent.go` creates a temporary `Session` rooted at the same `env` with tool registry restricted to `Read`, `Grep`, `Glob`. No subagent ID surfaces; no `SubagentStart` hook fires for this internal verification path (would be circular).
2. The `prompt` undergoes `$TOOL_INPUT`, `$TOOL_RESULT`, `$USER_PROMPT`, `$TOOL_NAME`, `$ARGUMENTS` substitution (same as `prompt` hooks; `$ARGUMENTS` = full HookInput JSON).
3. The subagent runs one turn (or until it calls a `decide(allow|deny|defer, reason)` tool that SP5 registers for this purpose). The decision is returned as a `ParsedHookOutput`.
4. Timeout default: 60 seconds. The subagent's tool budget is bounded by a hard cap of 5 tool rounds.

**Experimental status.** This hook type is marked experimental in CC docs. SP5 implements it and emits a one-time warning per plugin per hook at config-load time: `agent hook in plugin %q event %q: this hook type is experimental and its API may change`. The warning is non-fatal and fires from `ParsePluginHooks` after the hook is parsed.

**Implementation file.** `agent/plugin_hooks_agent.go`. Reuses the existing `llm` stub for tests (per the parent spec's testing conventions).

## 5. New Config Fields

### 5.1 `args` (exec form)

`[]string`. When present and non-empty, switches the hook from shell to exec form. `command` becomes the program; `args` are its argv. See §4.1.

Validation: `args` only valid on `type: "command"`. Setting it on other types produces a parse-time error: `hook in event %q: "args" is only valid for command hooks`.

### 5.2 `async`

`bool`, default false. When true:

- The hook is dispatched in a detached goroutine.
- Its result is *not* awaited by the agent loop.
- Its `ParsedHookOutput` is discarded.
- Exceptions: with `asyncRewake: true`, exit-code-2 routes through the rewake channel.

`async` is illegal on `PreToolUse`, `PermissionRequest`, and `UserPromptExpansion` because their results gate flow control; SP5 emits a config-time error in those cases.

### 5.3 `asyncRewake`

`bool`, default false. Implies `async: true`; SP5 sets `async = true` automatically when `asyncRewake` is set, and emits an informational note if the user set `async: false, asyncRewake: true`.

Semantics: §3.10. On exit-2, the hook's stderr is sent to the session's rewake channel, which the agent loop reads at safe points to steer the model.

### 5.4 `shell`

`string`, one of `"bash"` (default) or `"powershell"`. Selects the shell for shell-form command hooks. Ignored for exec-form. Ignored for non-command hook types.

On non-Windows when `shell: "powershell"` is requested, falls back to `bash` with a one-time warning per plugin per hook. PowerShell-on-Linux (`pwsh`) is *not* invoked even if present, to keep behavior deterministic.

### 5.5 `if` — permission-rule filter (SP2 interaction)

`string`, a permission rule pattern in SP2's grammar (e.g., `"Bash(git *)"`, `"Edit(*.ts)"`). Empty or omitted = always run.

**Evaluation timing.** Only meaningful on tool-bound events: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch` (evaluated per tool in the batch — hook fires once total but the input's `tool_results` is filtered), `PermissionRequest`, `PermissionDenied`. For other events, `if` is parsed but ignored (warning emitted at config-load time).

**Evaluation contract.** SP5 calls `permissions.EvaluateRule(rule string, toolName string, toolInput map[string]any) bool` — a function SP2 must export. SP5 does not parse the rule string itself.

**Fail-safe.** If SP2's evaluation returns an error (parse error in the rule), the hook *runs* (fail-open). Rationale: a typo in `if` should not silently disable a hook; the user will see the failure in hook output or in stderr.

### 5.6 `statusMessage`

`string`. UI surface only. Plumbed through to `HookStartData.StatusMessage` so consumers (`serf-tui`, `serf-hub`) can render a spinner caption while the hook runs. Empty = default behavior ("Running hook…"). Not interpreted; not substituted.

## 6. New Output Fields

### 6.1 `hookSpecificOutput.additionalContext`

`string`. Already partially honored for `SessionStart`; SP5 generalizes the path.

**Where it is injected.** Per event:

- `SessionStart` → existing path: appended to system prompt (unchanged).
- `UserPromptSubmit`, `UserPromptExpansion`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch` → injected as a steering turn before the next LLM call.
- `SubagentStart` → injected as the subagent's first steering turn before its first LLM call.

It is *not* shown to the user; it is fed to the model. `HookRunResult.AdditionalContext []string` carries it; the session loop calls `s.appendTurn(TurnSteering, llm.User(ctx))` for each.

**10,000-character cap.** §10 covers the overflow-to-file behavior.

### 6.2 `updatedInput` on `PreToolUse`

Existing field; SP5 documents its semantics precisely.

When a PreToolUse hook returns `hookSpecificOutput.updatedInput`, the map replaces `call.Arguments` for the actual tool execution. SP5 marshals the map to JSON, parses it back into the tool's argument shape via the existing registry path, and feeds the result to `s.reg.ExecuteCall`. The original tool call ID and tool name are preserved.

**Multiple-hook merging.** When multiple PreToolUse hooks return `updatedInput`, the maps are deep-merged by key with last-write-wins per key (in hook-firing order, which is config-tier order per SP1 §9.1). This matches CC's documented behavior.

**Validation.** SP5 does not validate that the updated input matches the tool's schema; the tool itself reports an argument error on execution.

### 6.3 `permissionDecision: "defer"`

`PreToolUse` and `PermissionRequest` may set `hookSpecificOutput.permissionDecision` to one of `"allow" | "deny" | "ask" | "defer"`.

**Defer chaining.** A `"defer"` decision from one hook is equivalent to that hook expressing no opinion. The session continues to evaluate remaining hooks in firing order. If *all* hooks defer (or stay silent), the decision falls through to SP2's static rule evaluation. If SP2 also returns "no opinion", the configured `defaultMode` resolves it.

**Precedence summary** (highest precedence first):

1. Any hook `deny` (any deny wins, immediately).
2. Otherwise any hook `allow` (first allow wins among non-deny outcomes).
3. Otherwise any hook `ask` (ask the user).
4. Otherwise all-defer or silent → SP2 static rules.
5. SP2's static rules' result → `defaultMode`.

`Deferred` is recorded on `ParsedHookOutput` so the orchestrator can audit which hooks abstained.

### 6.4 `permissionDecisionReason`

`string`. Accompanies any of allow/deny/ask/defer. Plumbed through to `ParsedHookOutput.PermissionDecisionReason`. Surfaced to the user in deny/ask paths, surfaced to the model in deny paths as the system message accompanying the denied tool result.

### 6.5 `sessionTitle` on `UserPromptSubmit`

`string`. Emitted only by `UserPromptSubmit` hooks. SP5 plumbs it to `HookRunResult.SessionTitle` and the session emits a new `EventSessionTitleChanged` event with the new title. Consumers can rename the conversation in their UI.

The session itself does not store the title (no persistence at SP5's layer); SP8 may persist later if needed.

## 7. New Input Fields

### 7.1 `transcript_path`

Set on every event. Source: `Session.TranscriptPath()`. Empty when state persistence is disabled.

### 7.2 `permission_mode`

Set on `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `UserPromptSubmit`, `UserPromptExpansion`, `PermissionRequest`. Source: SP2 reads `permissions.defaultMode` from the merged config plus any in-session override (`--permission-mode` flag or future `/mode` slash form). SP5 reads the resolved value via a new `permissions.CurrentMode(s *Session) string` accessor that SP2 exports. If SP2 is not loaded yet (during early-startup events), value is `""`.

### 7.3 `effort`

Set on events that have a `permission_mode` field plus `PostCompact` and `SubagentStart`. Source: `cfg.ReasoningEffort` (when set on the session) or a CLI flag.

Note: serf today stores reasoning effort as a plain string ("low", "medium", "high"). CC adds `"xhigh"` and `"max"`; SP5 passes the string through unchanged. Validation of allowed values belongs in the CLI parser.

Shape: `{ "level": "<string>" }` — wrapped so future fields (cost budget, token cap) can attach without a schema break.

### 7.4 `agent_id` and `agent_type`

Set on hooks fired from inside a subagent session: every event when `s.cfg.ParentSessionID != ""`. Source: `agent_id` = `s.id`; `agent_type` = `s.cfg.AgentName` (already populated by `spawnAgent`). Empty in root-session contexts.

Also set on `SubagentStart` events fired from the *parent* — there they identify the spawned child (per §3.4).

### 7.5 `tool_use_id`

Set on every tool-bound event. Source: `call.ID` (the LLM-assigned identifier). Necessary so multiple parallel hooks across `PreToolUse` / `PostToolUse` / `PostToolUseFailure` can correlate to one tool call.

## 8. New Environment Variables

Each is set in the spawned hook process environment (in addition to the existing `CLAUDE_PROJECT_DIR`, `CLAUDE_PLUGIN_ROOT`, plus the parent process environment).

### 8.1 `CLAUDE_PLUGIN_DATA`

Per-plugin persistent data directory. Source: `~/.config/serf/plugins/data/<marketplace>/<plugin>/` (created on first read, mode 0700). SP4 owns the directory creation; SP5 reads the path and exports it. For ad-hoc plugins loaded via `--plugin-dir` (no marketplace), value is `<plugin-dir>/.data/` (created lazily).

Survives plugin updates: SP4 preserves this directory across `serf plugin update`.

### 8.2 `CLAUDE_EFFORT`

The active reasoning effort level. Source: same as `effort.level` in §7.3. Empty when no effort is configured.

### 8.3 `CLAUDE_CODE_REMOTE`

Set to `"true"` when the session runs in `serf-hub` or any future remote/web embedding. Source: a session flag `cfg.IsRemote` populated by the embedder. Unset (not present in environment, not empty string) for CLI / TUI / SDK sessions.

The flag has no effect on hook semantics; it's a signal for plugins that need to choose between interactive and non-interactive output formats.

## 9. Matcher Dual-Mode Algorithm

The current `matchHooks` calls `regexp.Compile` on every matcher value. SP5 replaces this with the dual-mode behavior CC documents.

### 9.1 Algorithm

```
func compileMatcher(pattern string) MatcherFunc {
    case 1: pattern == "" || pattern == "*"  → matches everything
    case 2: pattern contains only [a-zA-Z0-9_|] characters
        → split on "|" into exact alternatives; match iff target equals any alternative
    case 3: otherwise
        → compile pattern as regex via regexp.Compile; match iff regex matches anywhere
                in the target. On compile failure, log a warning and never match.
}
```

Backed by a per-`RegisteredHook` cached compiled matcher: SP5 adds an unexported `matcherFunc func(string) bool` field on `RegisteredHook`, populated at `ParsePluginHooks` time after the JSON shape is fully read.

### 9.2 Corner cases

| Input | Mode | Behavior |
|---|---|---|
| `""` | case 1 | matches everything (current behavior is "no match" for empty strings via regex; SP5 changes this — matches CC's "no matcher = match all") |
| `"*"` | case 1 | matches everything (current behavior preserved) |
| `"Bash"` | case 2 | exact match: target == `"Bash"` |
| `"Bash\|Edit"` | case 2 | exact match: target == `"Bash"` or `"Edit"` |
| `"mcp__server__tool"` | case 2 | exact match (underscores allowed) |
| `"mcp__.*"` | case 3 | regex (contains `.` and `*`) |
| `"Bash(rm:*)"` | case 3 | regex; in practice will not compile cleanly since `(` is an open group with `:` inside; the test suite documents this as "invalid regex, never matches" with a warning |
| `"Edit"` for SessionStart matcher | case 2 | matches if SessionStart's matcher target is `"Edit"` (it isn't — SessionStart uses `"startup"`); no match |

### 9.3 JavaScript regex flavor caveat

CC documents the regex as JavaScript-flavor. Go's `regexp` package implements RE2, which is a strict subset. SP5 uses Go's `regexp.Compile` and documents the differences in a new `docs/superpowers/hooks.md` reference page (separate, non-spec deliverable).

Not supported in regex mode (will fail to compile and warn):

- Backreferences (`\1`).
- Lookbehinds (`(?<=…)`, `(?<!…)`).
- Lookaheads (`(?=…)`, `(?!…)`) — Go's RE2 lacks these.
- Named-group syntax `(?<name>…)` — RE2 uses `(?P<name>…)`. Plugin authors writing JS-style names get a compile error; documented workaround is to use unnamed groups.

Supported syntax covers everything CC plugins published to date use in practice (verified by audit of the anthropics/claude-code-plugins marketplace as of 2026-05-14). The audit list lives in the test fixture set, §11.

## 10. Output Capping

CC caps `additionalContext` at 10,000 characters and writes overflow to a file. SP5 matches this behavior.

### 10.1 Algorithm

For any `ParsedHookOutput.AdditionalContext` value exceeding 10,000 characters (byte count, not rune count, for simplicity and consistency with CC's behavior):

1. Truncate the in-memory value to the first 10,000 bytes (truncating mid-rune is fine — the file has the canonical copy).
2. Write the *full* original value to `<state_dir>/hooks/<session-id>/<event>-<hook-uniq-id>.txt`. Mode 0600. Directory created lazily.
3. Append a synthetic suffix to the truncated value: `\n\n[additionalContext truncated at 10000 bytes; full content at <path>]`.
4. Record the file path in `ParsedHookOutput.AdditionalContextOverflow` so observability events can surface it.

If `state_dir` is empty (no persistence), truncate without writing a file; the suffix becomes `[additionalContext truncated at 10000 bytes; persistence disabled]`.

### 10.2 Why 10,000

CC's documented value. Confirmed empirically by their docs. Configurable via `cfg.HookContextCap` (default 10000), exposed for future tuning but not surfaced as a CLI flag at SP5 time.

## 11. Backward Compatibility

The existing nine events keep their current input/output shape. SP5's HookInput struct adds fields with `omitempty` JSON tags, so the JSON piped to existing command hooks does not gain unexpected keys. SP5's ParsedHookOutput additions are zero-value-default; existing parse logic produces the same `Continue/SuppressOutput/SystemMessage/Denied/Blocked/UpdatedInput` for legacy hook outputs.

The existing two hook types (`command`, `prompt`) keep their current semantics. The new `args` field is optional on `command`; the new `shell` field defaults to `"bash"` which matches the current `bash -c` behavior.

The matcher change (§9) alters behavior in one observable way: a matcher of `""` (empty string) now matches everything, where the previous code compiled it as a regex and matched everything (since empty regex matches every string). Net effect: no behavior change for `""`. The change for `"*"` is also a no-op (existing special case). The change for exact-name matchers like `"Bash"` is a *semantic* shift from "regex-matches-substring" to "exact-equal" — a matcher of `"Bash"` no longer matches tool names that contain `"Bash"` as a substring. In practice, all current serf-tested matchers either are `"*"`, are full regex (`.*`), or are exact names — none rely on substring matching. The test suite (§13) includes a row that codifies the new behavior to catch any regression.

## 12. Package and File Layout

`agent/plugin_hooks.go` is currently 656 lines and will gain meaningful surface. Splitting:

- `agent/plugin_hooks.go` (existing) — keep types, `ParsePluginHooks`, `HookRunner` core, `runAll`, common parse + matcher helpers. Add: dual-mode matcher (§9), the 9 new `Run<Event>` methods, expanded `parseHookOutput`. Estimated +400 lines.
- `agent/plugin_hooks_http.go` (new) — `executeHTTPHook`. ~120 lines.
- `agent/plugin_hooks_mcp.go` (new) — `executeMCPToolHook`. ~80 lines.
- `agent/plugin_hooks_agent.go` (new) — `executeAgentHook`, ephemeral one-shot subagent. ~150 lines.
- `agent/plugin_hooks_async.go` (new) — async dispatch and rewake channel plumbing. ~80 lines.
- `agent/plugin_hooks_matcher.go` (new) — dual-mode matcher and its tests. ~60 lines.
- `agent/config_watcher.go` (new) — fsnotify-backed watcher for `ConfigChange`. ~100 lines, behind `cfg.WatchConfig` flag (default off).

Existing files modified:

- `agent/session.go` — fire 7 of the 9 new events (every site listed in §3 except those owned by SP2's enforcement layer for PermissionRequest/PermissionDenied). `hookInput` populates the new common fields.
- `agent/subagents.go` — fire `SubagentStart`.
- `agent/context_strategy.go` — fire `PostCompact`.
- `agent/plugin_hooks_test.go` — extended to cover the new dispatch methods.

New test files:

- `agent/plugin_hooks_http_test.go`.
- `agent/plugin_hooks_mcp_test.go`.
- `agent/plugin_hooks_agent_test.go`.
- `agent/plugin_hooks_matcher_test.go`.
- `agent/plugin_hooks_events_test.go` (one section per new event).
- `agent/plugin_hooks_async_test.go`.

Fixture additions:

- `agent/testdata/plugins/hooks_http/` — a minimal plugin defining one http hook.
- `agent/testdata/plugins/hooks_mcp/` — one mcp_tool hook.
- `agent/testdata/plugins/hooks_agent/` — one agent hook.
- `agent/testdata/plugins/hooks_matcher_corners/` — matchers covering every §9.2 row.

## 13. Testing Strategy

TDD: write all of §13 first, then implement §2–§10 until tests pass. Real filesystem via `t.TempDir()`; existing `llm` stub for `agent` and `prompt` hooks; `httptest.NewServer` for `http` hooks; the existing MCP stub patterns (`agent/mcp_*_test.go`) for `mcp_tool` hooks.

### 13.1 Per-event tests

One table-driven test per new event in `plugin_hooks_events_test.go`. Each table covers: minimal happy path (exit 0, no JSON), JSON allow/block/defer decisions, exit code 2 path, exit code non-zero non-2 path, additionalContext path (where applicable), 10,000-char overflow path.

Coverage table (each row is one test row, not one test function):

| Event | Rows |
|---|---|
| `PostToolUseFailure` | 6: happy, JSON-block, JSON-additionalContext, exit-2, exit-1, overflow |
| `PostToolBatch` | 7: happy, JSON-block, multi-tool batch shape, exit-2 stops loop, exit-1 ignored, additionalContext, matcher always-fire |
| `StopFailure` | 4: each `error_type` (rate_limit, server_error, unknown), exit ignored |
| `SubagentStart` | 5: happy, additionalContext-into-child, agent_type matcher hit/miss, two hooks fire in order |
| `UserPromptExpansion` | 6: skill expansion, MCP-prompt-stub, JSON-block aborts expansion, additionalContext appended, matcher hit by command_name, exit-2 path |
| `PostCompact` | 3: auto trigger, exit-0, exit-2 logged |
| `PermissionRequest` | 8: allow, deny, deny+reason, updatedInput, addPermissionRule, exit-2-as-deny, two-hooks-disagree (deny wins), defer-falls-through |
| `PermissionDenied` | 3: retry=true surfaces, retry=false silent, exit ignored |
| `ConfigChange` | 5: user_settings + project_settings + local_settings reload, JSON-block rejects reload, policy_settings block becomes advisory |

### 13.2 Per-hook-type harnesses

- `plugin_hooks_http_test.go` — spins `httptest.NewServer` returning canned responses (200+JSON, 200+text, 200+empty, 500, slow → timeout). Verifies request body shape, header substitution against `AllowedEnvVars`, decision parsing.
- `plugin_hooks_mcp_test.go` — uses the existing MCP stub (`agent/mcp_real_test.go` patterns) to register a `policy.check_command` tool, verifies arg substitution from `${tool_input.command}`, verifies error path when server is not connected, verifies isError-true path.
- `plugin_hooks_agent_test.go` — uses the `llm` stub. Stub returns a synthesized assistant turn ending in a `decide(allow, "ok")` tool call; SP5's agent-hook driver parses that into a `ParsedHookOutput`. Tests cover: allow, deny, defer, timeout, tool-budget exhaustion.
- `plugin_hooks_matcher_test.go` — table-driven per §9.2, plus a row for each invalid-regex caveat in §9.3.

### 13.3 Async + rewake tests

- A hook with `async: true` does not block dispatch (assert dispatch goroutine returns within 50 ms while the hook's command sleeps 5 s).
- A hook with `asyncRewake: true` exiting code 2 writes to `s.asyncRewake`. Test reads the channel directly.
- The session loop drains the rewake channel and steers the message on the next round-boundary check. Integration test runs a synthetic `processOneInput` round, fires an async-rewake hook mid-round, asserts the next round includes the steered context.
- A full rewake channel (16 entries) drops the next signal and logs a warning. Asserted by buffering 17 signals and inspecting the warning log.

### 13.4 Env-var presence tests

For each of `CLAUDE_PLUGIN_DATA`, `CLAUDE_EFFORT`, `CLAUDE_CODE_REMOTE`: run a command hook that prints `printenv`, parse stdout, assert presence and value. Test matrix: with/without effort set, in-CLI vs. in-Remote sessions, plugin loaded vs. unloaded.

### 13.5 End-to-end integration test

`agent/plugin_e2e_test.go` gains a new function `TestPluginE2E_HookParityScenarios`. Spins up a fixture plugin (`agent/testdata/plugins/hookparity/`) that declares:

- One `PostToolUseFailure` http hook (canned-response server).
- One `PostToolBatch` command hook with `args` exec form.
- One `SubagentStart` mcp_tool hook (using the MCP stub).
- One `PermissionRequest` agent hook (using llm stub).

Drives a synthesized turn that:

1. Triggers a tool that fails — asserts `PostToolUseFailure` fires.
2. Issues a batch of two parallel tools — asserts `PostToolBatch` fires once.
3. Spawns a subagent via the Agent tool — asserts `SubagentStart` fires.
4. Triggers a permission-required tool — asserts `PermissionRequest` fires and its allow decision lets the tool run.

The test runs with `go test -timeout 30s -run HookParityScenarios` and is gated behind the existing `-short` skip pattern.

### 13.6 Coverage gate

- Every exported function in §2 has at least one direct test.
- Every new HookEvent constant has at least one event test row.
- Every new hook-type has at least one harness test.
- Every new config field (§5) has at least one parse-test + one runtime-test row.
- Every new env var has a presence test.
- `go test ./agent/...` is green; race detector run is green; no new linter warnings.

## 14. Open Questions

### 14.1 Hook ordering across config tiers — settled by SP1

SP1 §9.1 settles this: global → project → CLI (in order) → plugin-provided, lowest-precedence-first, with within-file order preserved. SP5 inherits this contract. The merged hook array arrives in firing order; SP5's runner walks it head-to-tail.

No SP5-level re-decision. SP5 records the firing order in `EventHookStart` payloads (already present today) and adds a `ConfigTier` field so observability can see which tier supplied each hook.

### 14.2 `agent` hook type experimental status — addressed in §4.4

Implemented. One-time warning per plugin per hook at config-parse time. No runtime gate; users opting into the experimental hook accept the risk.

The warning is logged at `level: info` to stderr and emitted as a structured event (`EventPluginWarning` with payload identifying plugin + event + hook type) so `serf-hub` can surface it in the UI.

### 14.3 `asyncRewake` main-loop integration — addressed in §3.10

Wake channel (capacity 16), non-blocking select arms at two safe points per round (top of round, before LLM call). Signals during `SessionAwaitingInput` are dropped with a debug log. A full channel drops the new signal and warns.

### 14.4 Genuinely open at SP5 close

- **MCP-prompt expansion site.** Serf does not yet expand MCP prompts inline. The `UserPromptExpansion` integration point (§3.5) is wired in `processOneInput` for the skill-expansion case but the MCP-prompt branch is a stub that runs only when a future MCP-prompt feature lands. Decision: ship the hook and the stub; document the gap in the hooks reference doc. Resolution will land in a later SP when MCP prompt invocation arrives.
- **`config_source` for skills.** §3.9 declares a `"skills"` matcher value but live skill reload is not implemented today. SP5 includes the matcher value in the public surface so plugins can be authored against it; the actual firing happens once live skill reload lands (probably in SP7 or later).
- **`policy_settings` precedence.** CC enforces `policy_settings` as MDM-style admin config that cannot be overridden by user hooks. Serf has no policy tier today. The reserved value lives in the matcher set so future MDM work has a slot; behavior is "treat as if not present" until the tier is added.
- **`addPermissionRule` persistence.** §3.7 says the in-session permission set extends but no on-disk write. Long-term, users may want a hook decision to persist ("always allow this"). Defer to a future SP after SP2 settles its session-permission-mutation API.
