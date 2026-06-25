# Hooks

How serf's lifecycle hooks work **today**, and how to author them in a plugin.
Hooks are the **primary plugin surface**: a plugin ships a `hooks.json` (or inline
manifest hooks), serf discovers it, and the hooks fire at session, tool, and
compaction boundaries. This document is the authoritative reference for shipped
hook behavior and a practical authoring guide in one.

Serf's hooks are a **Claude-compatible subset**: a `hooks.json` written for Claude
Code mostly works as-is, but only the nine events serf actually fires do anything,
and a few semantics (notably the matcher and the tool names) have gotchas. Read
the [tool-name gotcha](#the-1-mistake-shell--bash) first — it is the most common
authoring mistake.

> **Scope.** Everything described here is implemented and tested. Claude hook
> capabilities serf recognizes but does **not** yet run — additional events, the
> `http`/`mcp_tool`/`agent` handler types, the `PreToolUse` `ask`/`defer` decisions
> and `updatedInput` revalidation, async execution, typed SDK hooks — are tracked,
> with the compatibility roadmap, in
> [`subagent-management/07-lifecycle-hooks-claude-compat.md`](subagent-management/07-lifecycle-hooks-claude-compat.md).
> Serf is honest about the line: a hook for something unimplemented is parsed and
> diagnosed loudly, never silently half-run.

## How serf discovers your hooks

Point serf at one or more plugin directories with `--plugin-dir`:

```bash
serf --plugin-dir ./my-plugin "do the thing"
```

For each plugin directory, serf reads hooks from the **first** of these it finds:

1. **A manifest-referenced path.** If the plugin manifest
   (`.claude-plugin/plugin.json`, or `.codex-plugin/plugin.json` as a fallback) has a `hooks`
   field that is a **string**, it is a path (relative to the plugin dir,
   `${CLAUDE_PLUGIN_ROOT}` expanded) to a hooks file.
2. **Inline manifest hooks.** If the manifest `hooks` field is a JSON **object**,
   it is parsed inline as the hooks config.
3. **The default file** `<plugin-dir>/hooks/hooks.json`.

Most plugins just use option 3 — drop a `hooks/hooks.json` in the plugin
directory and you are done.

### File shape

Serf accepts the Claude **wrapper** shape (an optional `description` plus a
`hooks` object keyed by event name):

```json
{
  "description": "What this plugin's hooks do",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Edit",
        "hooks": [
          { "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/check.sh" }
        ]
      }
    ]
  }
}
```

It also accepts the **direct** shape (events at the top level, no `hooks`
wrapper):

```json
{
  "PreToolUse": [
    { "matcher": "*", "hooks": [ { "type": "command", "command": "echo ok" } ] }
  ]
}
```

The structure is the same either way: each event maps to an array of **matcher
groups**; each group has a `matcher` and an array of **handlers**.

## Which events serf fires

Serf fires **nine** events. A hook on any other event name is parsed but never
runs (and warns loudly at load — see [Misconfiguration
warnings](#misconfiguration-warnings-loud-not-silent)).

| Event | When it fires | Matcher target |
|---|---|---|
| `SessionStart` | before the first model turn | `startup`, `resume`, `clear`, `compact` |
| `UserPromptSubmit` | after a user prompt is submitted | (none) |
| `PreToolUse` | before a tool runs (before schema validation) | tool name |
| `PostToolUse` | after a tool runs | tool name |
| `Stop` | before the assistant/session stops | (none) |
| `SubagentStop` | before a subagent stops | agent type (not yet wired) |
| `PreCompact` | before context compaction | `manual`/`auto` (not yet wired) |
| `SessionEnd` | at session teardown | session end reason (not yet wired) |
| `Notification` | on a user-visible notification | notification type (not yet wired) |

All nine are tier `claude-compatible-subset`: serf fires them with a
Claude-compatible **subset** of the behavior. None is a complete end-to-end
reimplementation of the Claude event — for example `PreToolUse` honors `allow` and
`deny` decisions but not `ask`/`defer` (no interactive permission prompt), and
several events ignore their matcher target today (noted above).

**Other Claude events are reserved, not fired.** Names like `PostToolUseFailure`,
`PermissionRequest`, `SubagentStart`, `PostCompact`, `Setup`, `FileChanged`, and
the rest of the Claude vocabulary are *recognized* (serf knows they are real
Claude events) but **reserved** — declaring a hook for one parses cleanly and
warns that the event is not fired yet. The full event vocabulary and the roadmap
live in
[07](subagent-management/07-lifecycle-hooks-claude-compat.md#event-contract).

**Unknown names are rejected loudly.** A typo like `PreToolUze` is neither a serf
event nor a Claude event, so serf warns that it is likely a typo and the hook
will never fire.

### When `PreToolUse` runs (input rewriting)

`PreToolUse` runs **before** registry schema validation, so a hook can inspect or
rewrite raw tool input. The order for each tool call is:

1. The model emits a tool call; serf decodes the name and best-effort-parses the
   arguments for hook input.
2. `PreToolUse` hooks matching the Claude tool name run.
3. Any `updatedInput` they return is merged.
4. The final arguments are validated against the tool's schema.
5. Final execution policy runs on the validated input.
6. The tool executes; `PostToolUse` runs on the result.

A `PreToolUse` hook can therefore rewrite arguments or deny the call, but it
**cannot** push an invalid or disallowed shape past validation and final policy —
those run on the post-hook input. `PostToolUse` runs after execution and cannot
undo a tool that already ran.

## The #1 mistake: `shell` → `Bash`

> **Matchers run against the _Claude_ tool name, not serf's name.** Serf's shell
> tool is presented to hooks as **`Bash`**. A matcher of `"shell"` silently never
> fires. Use `"Bash"`.

When serf loads Claude-style plugins, tool names meet two vocabularies: serf's
engine uses canonical names (`shell`, `read_file`, `edit_file`), while Claude
(and your hook matcher) names them `Bash`, `Read`, `Edit`. Before matching, serf
translates the tool to its Claude name (`agent/internal/toolname`), so your
matcher must name the **Claude** tool:

| You want to match… | serf canonical name | matcher you must write |
|---|---|---|
| the shell tool | `shell` | `Bash` |
| file reads | `read_file` | `Read` |
| file writes | `write_file` | `Write` |
| file edits | `edit_file` | `Edit` |
| grep / glob | `grep` / `glob` | `Grep` / `Glob` |
| starting a delegate job | `delegate` | `Task` |
| web fetch / search | `web_fetch` / `web_search` | `WebFetch` / `WebSearch` |
| notebook edits | `notebook_edit` | `NotebookEdit` |

MCP tools keep their `mcp__<server>__<tool>` name in both vocabularies.

A matcher that names a serf-canonical tool (`"shell"`, `"read_file"`) is not an
error — it is a valid exact matcher that simply never matches the Claude name the
hook is tested against. It will fail silently. Write `"Bash"`.

## Matchers

A matcher decides which targets a group's hooks fire for. The rules, in order:

1. **Empty, omitted, or `"*"` → matches everything.**
2. **Only `[A-Za-z0-9_|]` characters → exact name / pipe-list.** `"Bash"` matches
   only `Bash` (not `BashOutput`). `"Edit|Write|MultiEdit"` matches exactly
   `Edit`, `Write`, or `MultiEdit`.
3. **Anything else → a Go RE2 regular expression.** `"mcp__memory__.*"` matches
   `mcp__memory__search` by regex.

Worked examples:

| Matcher | Target | Result |
|---|---|---|
| `""` / `"*"` | anything | match |
| `"Bash"` | `Bash` | match |
| `"Bash"` | `BashOutput` | **no** match (exact mode, not substring) |
| `"Edit|Write"` | `Write` | match (pipe-list) |
| `"Edit|Write"` | `Read` | no match |
| `"mcp__memory__.*"` | `mcp__memory__search` | match (regex) |
| `"mcp__memory"` | `mcp__memory__search` | **no** match (exact mode, no substring) |

Two traps worth calling out:

- **Exact mode does not substring-match.** `"Bash"` will not catch `BashOutput`,
  and `"mcp__memory"` will not catch `mcp__memory__search`. If you want a prefix,
  write a regex: `"mcp__memory__.*"`.
- **The matcher is the Claude tool name** — see [the gotcha
  above](#the-1-mistake-shell--bash).

> **Serf-native nicety:** serf trims surrounding whitespace from the matcher
> before classifying it, so `" Bash "` is treated as the exact matcher `"Bash"`.
> Claude treats the matcher as a literal regex and would not trim. This is a
> minor, intentional divergence.

### Matcher targets by event

For the events serf fires, the matcher is tested against:

- **Tool events** (`PreToolUse`, `PostToolUse`): the **Claude tool name**,
  including `mcp__<server>__<tool>` (see the [gotcha](#the-1-mistake-shell--bash)).
- **`SessionStart`**: the start kind — `startup`, `resume`, `clear`, or
  `compact`.
- **`UserPromptSubmit`**, **`Stop`**: no matcher target; use `"*"` or omit.
- **`SubagentStop`**, **`SessionEnd`**, **`Notification`**, **`PreCompact`**:
  these fire, but their matcher target is **not yet wired** — the runner matches
  with an empty target today, so use `"*"` or omit the matcher. (The intended
  targets — agent type, end reason, notification type, `manual`/`auto` — are a
  reserved compatibility item.)

### Go RE2, not JavaScript regex

Claude documents JavaScript regular expressions; serf matches with **Go RE2**.
RE2 is a strict subset: **lookbehind** (`(?<=...)`, `(?<!...)`) and
**backreferences** (`\1`) are not supported. A matcher using either is treated as
an **invalid regex**: serf skips the hook and emits a warning naming the plugin,
event, and matcher — it never silently mis-matches. Rewrite such matchers with
RE2-compatible alternatives. (Almost all real-world Claude matchers are already
RE2-compatible.)

## Handlers

Each matcher group holds an array of **handlers**. Serf runs two handler types
today: `command` (a process) and `prompt` (an LLM call, serf-native sugar). The
`http`, `mcp_tool`, and `agent` types are **reserved** — they parse but are
skipped with a diagnostic; see
[07](subagent-management/07-lifecycle-hooks-claude-compat.md#handler-types).

### Common handler fields

Every handler shares these:

| Field | Status | Meaning |
|---|---|---|
| `type` | **required** | `command` or `prompt` today. |
| `timeout` | **active** | Seconds. Defaults: `command` 60s, `prompt` 30s. |
| `if` | parsed, reserved | A permission-rule filter for tool events. Captured but not yet enforced. |
| `statusMessage` | parsed, reserved | User-visible status text while the hook runs. Captured; surfacing reserved. |
| `once` | parsed, reserved | Run-once semantics (skill-frontmatter scope in Claude). Reserved. |

Reserved fields are preserved for diagnostics and forward-compatibility — they do
not cause a load error — but they have no runtime effect yet.

### The `command` handler

The implemented (and recommended) handler type is `command`. Serf runs it, pipes
the hook [input JSON](#the-input-your-hook-receives) to its **stdin**, and reads
its [output](#what-your-hook-returns) from stdout/stderr.

```json
{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/check.sh", "timeout": 30 }
```

#### Shell form vs. exec form

- **Shell form** (`command`, no `args`): serf runs `bash -c "<command>"`. The
  shell expands variables, globs, and pipes. This is the default.
- **Exec form** (`command` + `args`): serf spawns the program **directly, with no
  shell**. `command` is the executable; `args` are passed verbatim. Use this for
  paths with spaces and to avoid shell expansion.

The difference matters:

```json
{ "type": "command", "command": "printf", "args": ["%s", "$HOME"] }
```

In exec form, `$HOME` is passed **literally** — there is no shell to expand it, so
the program receives the four characters `$HOME`. A path with spaces works
without quoting gymnastics:

```json
{ "type": "command", "command": "/opt/my plugin/check", "args": ["--mode", "strict"] }
```

When `args` is present, `shell` is ignored.

> **`async` / `asyncRewake`** are command-only fields that serf parses but does
> not execute yet (reserved). A hook that sets them runs **synchronously** today.

#### Selecting the shell

`shell` selects the shell for **shell-form** commands:

- `"bash"` (or omitted) → `bash -c`. This is the only supported shell today.
- `"powershell"` → an explicit error (reserved; not supported on this platform).
- any other value → an explicit error.

Serf never silently falls back to bash for an unrecognized shell — a bad `shell`
value fails loudly rather than running something you did not ask for.

#### Command environment variables

Serf sets these in the command's environment:

| Variable | Value |
|---|---|
| `CLAUDE_PLUGIN_ROOT` | your plugin's directory (absolute) |
| `PLUGIN_ROOT` | serf-native alias of `CLAUDE_PLUGIN_ROOT` |
| `CLAUDE_PROJECT_DIR` | the session's working directory |
| `CLAUDE_EFFORT` | the session's reasoning-effort level — **only when configured** |

`${CLAUDE_PLUGIN_ROOT}` and `${PLUGIN_ROOT}` are also expanded inside the
`command` (and `args`) **strings** at parse time, so you can reference your
plugin's files in the path itself, as in the examples above.

Reserved (not set today): `CLAUDE_PLUGIN_DATA`, `CLAUDE_CODE_REMOTE` (serf has no
remote/serve signal to report, so it is omitted rather than fabricated), and
`CLAUDE_ENV_FILE`.

### The `prompt` handler (serf-native sugar)

`type: "prompt"` runs an LLM call instead of a process, substituting
`$TOOL_INPUT`, `$TOOL_RESULT`, `$USER_PROMPT`, `$MESSAGE`, and `$TOOL_NAME` into
the prompt and parsing the model's reply through the same output parser. An
optional `model` selects the provider/model; otherwise the session's client is
used.

```json
{ "type": "prompt", "prompt": "Is `$TOOL_INPUT` safe to run? Reply with JSON.", "model": "openai/gpt-5.4-mini" }
```

It works today but is serf-specific sugar; the Claude-compatible `$ARGUMENTS` /
`{ok, reason}` form is reserved. The `http`, `mcp_tool`, and `agent` handler
types are **reserved and skipped** (with a diagnostic) — they do not run yet.

## The input your hook receives

A command hook reads a single JSON object on stdin. The fields serf populates:

```json
{
  "session_id": "01ABC...",
  "cwd": "/path/to/project",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": { "command": "ls" },
  "tool_use_id": "call-123",
  "tool_response": "…",
  "transcript_path": "/path/to/session.transcript.jsonl",
  "effort": "high",
  "prompt": "…",
  "message": "…",
  "reason": "…",

  "tool_result": "…",
  "user_prompt": "…"
}
```

Notes:

- Fields are **omitted when empty** — `tool_name`/`tool_input` only appear for
  tool events, `transcript_path` only when persistence is on, `effort` only when
  configured, and so on.
- `tool_result` and `user_prompt` are **legacy serf aliases** kept during
  migration. Prefer the official `tool_response` and `prompt`; the aliases will
  not necessarily survive forever.
- `permission_mode`, `agent_id`, `agent_type`, and `CLAUDE_CODE_REMOTE` are part
  of the Claude contract but serf has **no real value** for them today, so it
  **omits** them rather than sending a fabricated value. Do not write a hook that
  depends on them.

(A `prompt` handler does not receive this JSON on stdin; it gets the legacy
`$TOOL_INPUT`/`$USER_PROMPT`/etc. substitutions described above.)

## What your hook returns

Serf reads the command's **stdout**, **stderr**, and **exit code**.

- **Exit 0, empty stdout** → success, no decision.
- **Exit 0, plain-text stdout** → reaches the **model** only for `SessionStart` and
  `UserPromptSubmit` (context events); for all other events it is shown to the
  **user** as a diagnostic warning. This is a deliberate divergence from Claude,
  which debug-logs non-context plain stdout to neither model nor user; serf surfaces
  it to the user so hook output never silently vanishes.
- **Exit 0, JSON stdout** → parsed as a structured decision (below).
- **Exit 2** → an event-specific block/error (see the [exit-code
  table](#exit-codes-per-event)). **JSON is ignored on exit 2** — only stderr is
  used.
- **Other non-zero** → a non-blocking error for the events serf fires.

> **JSON is parsed only on exit 0.** If your hook exits non-zero, any JSON it
> printed is discarded; serf uses the exit code and stderr. Print your decision
> JSON only when you exit 0.

The output JSON serf reads (exit 0):

```json
{
  "continue": true,
  "suppressOutput": false,
  "systemMessage": "shown to the user as a warning message",
  "terminalSequence": "",
  "decision": "block",
  "reason": "why (with decision:block)",
  "hookSpecificOutput": {
    "permissionDecision": "allow|deny|ask|defer",
    "permissionDecisionReason": "why",
    "updatedInput": { "command": "ls -la" },
    "additionalContext": "extra context for the model"
  }
}
```

- `continue` and `suppressOutput` are **parsed but not acted on today** — serf
  reads them for Claude compatibility, but they have no runtime effect yet (a
  deferred item); a hook that sets `continue: false` still runs to completion.
  `stopReason` and the rest of the structured output schema are reserved (see
  [07](subagent-management/07-lifecycle-hooks-claude-compat.md#hook-output-contract)).
- `hookSpecificOutput.additionalContext` is delivered to the **model**, wrapped in
  a `<SYSTEM-REMINDER>`.
- The top-level `systemMessage` field is shown to the **user** (a `[warning]`-style
  message via serf's diagnostic-warning channel) — it is **not** sent to the model.
- `terminalSequence` must be a safe terminal notification sequence.
- `decision: "block"` blocks where the event supports it (e.g. `Stop`,
  `SubagentStop`).
- For `PreToolUse`, `hookSpecificOutput.permissionDecision` honors `allow` and
  `deny`; the reason is read from `permissionDecisionReason`. Deprecated top-level
  form is accepted as a fallback: `decision: "approve"` → allow, `decision: "block"`
  → deny, top-level `reason` → the reason; the preferred `permissionDecision` wins
  when both are present. `updatedInput` rewrites the tool arguments before
  validation. `ask` and `defer` are recognized but **not honored** — serf has no
  interactive permission prompt, so the tool proceeds and a user-visible diagnostic
  names the unsupported decision. `updatedInput` revalidation is reserved.

### Exit codes per event

Exit 2 blocks for some events and is informational for others. The serf-fired
subset:

| Event | Exit 2 effect |
|---|---|
| `PreToolUse` | block the tool call — the stderr becomes the deny reason, surfaced to the model as the tool's error result |
| `Stop` | prevent stopping |
| `SubagentStop` | prevent the subagent stopping |
| `UserPromptSubmit` | **no block yet** — Claude erases the prompt here, but serf does not yet enforce the block; stderr is delivered to the model |
| `PreCompact` | **no block yet** — Claude blocks compaction here, but serf does not yet enforce the block; stderr is delivered to the model |
| `PostToolUse` | **no block** (cannot undo the tool) — stderr is delivered to the model as context |
| `SessionStart` | **no block** — stderr is delivered to the model |
| `SessionEnd` | **no block** — the session is ending, so the stderr is captured but not delivered (no following turn) |
| `Notification` | **no block** — stderr is delivered to the model |

(The full Claude table, including the reserved events, is in
[07 §Exit-code semantics](subagent-management/07-lifecycle-hooks-claude-compat.md#exit-code-semantics).)

## Misconfiguration warnings (loud, not silent)

Serf warns **loudly at plugin load** when a hook declaration is broken, so a typo
does not turn into silent non-execution. You will see a `[warning] …` line in the
CLI (and a warning in the TUI / web / hub) for each of:

- **An unknown event name** (e.g. `PreToolUze`) — "not a recognized Claude or serf
  event (likely a typo); this hook will never fire."
- **A recognized-but-unsupported event** (a real Claude event serf does not fire
  yet, e.g. `PostToolUseFailure`) — "declared for a reserved event serf does not
  yet fire; this hook will not run."
- **An invalid-regex matcher** (e.g. one using lookbehind) — names the plugin,
  event, and matcher; the hook is skipped.
- **An unsupported handler type** (`http`, `mcp_tool`, `agent`) — names the
  plugin, event, and type; the handler is skipped until implemented.

A handler missing its required `type`, or a malformed `hooks` array, is a **load
error** that names the source path, event, matcher, and handler index. Unknown
handler **fields** are not errors — they are preserved for diagnostics and
forward-compatibility.

The warnings and diagnostics carry **names and reasons only** — never your hook's
payload, env values, header values, tool input/output, or secrets. If env-var
substitution fails, serf reports the variable name, not its value. Completed runs
also record a sanitized error category when relevant: `parse_error`,
`unsupported_event`, `unsupported_handler`, `invalid_matcher`, `timeout`,
`cancelled`, `command_error`, `prompt_error`, or `hook_blocked`.

You can see the full picture in `/status`, which lists active supported hooks
(with their tier and count) alongside recognized-but-unsupported events (tier
`reserved-placeholder`, count 0). `/status` reports the recognized event
landscape; the load-time `[warning]` lines above are where misconfiguration shows
up.

## A complete example

A plugin that (1) bootstraps context at startup, (2) checks `Bash`/`Edit` calls
before they run, and (3) logs every tool result. Drop this at
`my-plugin/hooks/hooks.json` and run `serf --plugin-dir ./my-plugin …`.

```json
{
  "description": "Guardrails and logging for my-plugin",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume",
        "hooks": [
          { "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/bootstrap.sh" }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash|Edit",
        "hooks": [
          {
            "type": "command",
            "command": "${CLAUDE_PLUGIN_ROOT}/hooks/guard",
            "args": ["--strict"],
            "timeout": 20
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          { "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/log-result.sh" }
        ]
      }
    ]
  }
}
```

`hooks/guard` (exec form, so the path could contain spaces) receives the
`PreToolUse` input JSON on stdin; to deny a call it prints
`{"hookSpecificOutput":{"permissionDecision":"deny"}}` and exits 0, or simply
exits 2. `hooks/log-result.sh` reads the `PostToolUse` input and exits 0 — its
plain-text stdout is shown to the **user** as a warning message (not sent to the
model); to deliver context to the model from a hook, use
`hookSpecificOutput.additionalContext` in the JSON output. Remember: the
`PreToolUse` matcher is `Bash`, **not** `shell`.

## Hooks cannot bypass policy

However a plugin hook rewrites input or returns a decision, it cannot escape
serf's execution guarantees. A hook cannot:

- bypass effective tool policy;
- expand a child/subagent's capability set beyond effective policy;
- bypass provider/model feature policy;
- bypass session cancellation or closed state;
- push input past final execution policy — validation and policy run on the
  post-hook (`updatedInput`) arguments, not the originals.

## How this is tested

The shipped behavior is covered by `agent/internal/hooks/*_test.go` (matcher,
exec form, output parsing, exit codes), `agent/plugin/*_test.go` (config parsing
and diagnostics), the session-level hook/warning tests in `agent/`, and the live
scenario card `test/scenarios/hooks-claude-compat-matcher.md`, which loads a
plugin via `--plugin-dir` and exercises the matcher and exec-form behavior.

## See also

- [`subagent-management/07-lifecycle-hooks-claude-compat.md`](subagent-management/07-lifecycle-hooks-claude-compat.md)
  — the compatibility & roadmap spec: the full Claude event vocabulary, the
  reserved output schemas, the `http`/`mcp_tool`/`agent` handler types, async
  semantics, the typed SDK hook shape, the compatibility tiers, and the deferred
  phases (B–E).
