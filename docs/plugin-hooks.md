# Authoring plugin hooks

A practical guide to writing lifecycle hooks in a serf plugin. Hooks are the
**primary plugin surface**: a plugin ships a `hooks.json` (or inline manifest
hooks), serf discovers it, and the hooks fire at session/tool/compaction
boundaries. This guide is the how-to; the exact contract — every field, the full
event table, the exit-code semantics, and the compatibility tiers — lives in
[`subagent-management/07-lifecycle-hooks-claude-compat.md`](subagent-management/07-lifecycle-hooks-claude-compat.md).

Serf's hooks are a **Claude-compatible subset**: a `hooks.json` written for
Claude Code mostly works as-is, but only the nine events serf actually fires do
anything, and a few semantics (notably the matcher and the tool names) have
gotchas. Read the [tool-name gotcha](#the-1-mistake-shellbash) first — it is the
most common authoring mistake.

## How serf discovers your hooks

Point serf at one or more plugin directories with `--plugin-dir`:

```bash
serf --plugin-dir ./my-plugin "do the thing"
```

For each plugin directory, serf reads hooks from the **first** of these it finds:

1. **A manifest-referenced path.** If the plugin manifest
   (`.codex-plugin/plugin.json` or `.claude-plugin/plugin.json`) has a `hooks`
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
runs (and now warns loudly at load — see [Misconfiguration warnings](#misconfiguration-warnings-loud-not-silent)).

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
reimplementation of the Claude event — for example `PreToolUse` honors a `deny`
decision but not the full `allow|ask|defer` schema, and several events ignore
their matcher target today (noted above).

**Other Claude events are reserved, not fired.** Names like `PostToolUseFailure`,
`PermissionRequest`, `SubagentStart`, `PostCompact`, `Setup`, `FileChanged`, and
the rest of the Claude vocabulary are *recognized* (serf knows they are real
Claude events) but **reserved** — declaring a hook for one parses cleanly and
warns that the event is not fired yet. See the full event table and the roadmap
in [07](subagent-management/07-lifecycle-hooks-claude-compat.md#event-contract).

**Unknown names are rejected loudly.** A typo like `PreToolUze` is neither a serf
event nor a Claude event, so serf warns that it is likely a typo and the hook
will never fire.

## The #1 mistake: `shell` → `Bash`

> **Matchers run against the _Claude_ tool name, not serf's name.** Serf's shell
> tool is presented to hooks as **`Bash`**. A matcher of `"shell"` silently
> never fires. Use `"Bash"`.

When serf loads Claude-style plugins, tool names meet two vocabularies: serf's
engine uses canonical names (`shell`, `read_file`, `edit_file`), while Claude
(and your hook matcher) names them `Bash`, `Read`, `Edit`. Before matching,
serf translates the tool to its Claude name (`agent/internal/toolname`), so your
matcher must name the **Claude** tool:

| You want to match… | serf canonical name | matcher you must write |
|---|---|---|
| the shell tool | `shell` | `Bash` |
| file reads | `read_file` | `Read` |
| file writes | `write_file` | `Write` |
| file edits | `edit_file` | `Edit` |
| grep / glob | `grep` / `glob` | `Grep` / `Glob` |
| spawning a subagent | `spawn_agent` | `Task` |
| web fetch / search | `web_fetch` / `web_search` | `WebFetch` / `WebSearch` |
| notebook edits | `notebook_edit` | `NotebookEdit` |

MCP tools keep their `mcp__<server>__<tool>` name in both vocabularies.

A matcher that names a serf-canonical tool (`"shell"`, `"read_file"`) is not an
error — it is a valid exact matcher that simply never matches the Claude name
the hook is tested against. It will fail silently. Write `"Bash"`.

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
- **The matcher is the Claude tool name** — see [the gotcha above](#the-1-mistake-shellbash).

### Go RE2, not JavaScript regex

Claude documents JavaScript regular expressions; serf matches with **Go RE2**.
RE2 is a strict subset: **lookbehind** (`(?<=...)`, `(?<!...)`) and
**backreferences** (`\1`) are not supported. A matcher using either is treated as
an **invalid regex**: serf skips the hook and emits a warning naming the plugin,
event, and matcher — it never silently mis-matches. Rewrite such matchers with
RE2-compatible alternatives. (Almost all real-world Claude matchers are already
RE2-compatible.)

## The `command` handler

The implemented (and recommended) handler type is `command`. Serf runs it, pipes
the hook [input JSON](#the-input-your-hook-receives) to its **stdin**, and reads
its [output](#what-your-hook-returns) from stdout/stderr.

```json
{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/check.sh", "timeout": 30 }
```

### Shell form vs. exec form

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

### Selecting the shell

`shell` selects the shell for **shell-form** commands:

- `"bash"` (or omitted) → `bash -c`. This is the only supported shell today.
- `"powershell"` → an explicit error (reserved; not supported on this platform).
- any other value → an explicit error.

Serf never silently falls back to bash for an unrecognized shell — a bad `shell`
value fails loudly rather than running something you did not ask for.

### Command environment variables

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
the prompt and parsing the model's reply through the same output parser. It works
today but is serf-specific sugar; the Claude-compatible `$ARGUMENTS` /
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

## What your hook returns

Serf reads the command's **stdout**, **stderr**, and **exit code**.

- **Exit 0, empty stdout** → success, no decision.
- **Exit 0, plain-text stdout** → a user-visible system message (context).
- **Exit 0, JSON stdout** → parsed as a structured decision (below).
- **Exit 2** → an event-specific block/error (see the [exit-code table](#exit-codes-per-event)).
  **JSON is ignored on exit 2** — only stderr is used.
- **Other non-zero** → a non-blocking error for the events serf fires.

> **JSON is parsed only on exit 0.** If your hook exits non-zero, any JSON it
> printed is discarded; serf uses the exit code and stderr. Print your decision
> JSON only when you exit 0.

The JSON fields serf honors today:

```json
{
  "continue": true,
  "suppressOutput": false,
  "systemMessage": "shown to the user",
  "terminalSequence": "",
  "decision": "block",
  "reason": "why (with decision:block)",
  "hookSpecificOutput": {
    "permissionDecision": "deny",
    "updatedInput": { "command": "ls -la" },
    "additionalContext": "extra context for the model"
  }
}
```

- `systemMessage` is **user-visible**; `hookSpecificOutput.additionalContext` is
  **model context**, routed separately.
- `decision: "block"` blocks where the event supports it (e.g. `Stop`,
  `SubagentStop`).
- For `PreToolUse`, `hookSpecificOutput.permissionDecision: "deny"` denies the
  tool call; `updatedInput` rewrites the tool arguments before validation. The
  full `allow|ask|defer` schema is reserved.

### Exit codes per event

Exit 2 blocks for some events and is informational for others. The serf-fired
subset:

| Event | Exit 2 effect |
|---|---|
| `PreToolUse` | block the tool call |
| `Stop` | prevent stopping |
| `SubagentStop` | prevent the subagent stopping |
| `UserPromptSubmit` | **no block yet** — Claude erases the prompt here, but serf does not yet enforce the block; stderr shown to the user |
| `PreCompact` | **no block yet** — Claude blocks compaction here, but serf does not yet enforce the block; stderr shown to the user |
| `PostToolUse` | **no block** — stderr shown as context (cannot undo the tool) |
| `SessionStart` | **no block** — stderr shown to the user |
| `SessionEnd` | **no block** — stderr shown to the user |
| `Notification` | **no block** — stderr shown to the user |

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

The warnings carry names and reasons only — never your hook's payload, env values,
or secrets. You can also see the full picture in `/status`, which lists active
supported hooks (with their tier and count) alongside recognized-but-unsupported
events.

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
stdout becomes a user-visible note. Remember: the `PreToolUse` matcher is `Bash`,
**not** `shell`.

## See also

- [`subagent-management/07-lifecycle-hooks-claude-compat.md`](subagent-management/07-lifecycle-hooks-claude-compat.md)
  — the full contract: every field, the complete event table, the exit-code
  semantics, the compatibility tiers, and the roadmap for reserved features.
- `test/scenarios/hooks-claude-compat-matcher.md` — a live scenario that loads a
  plugin via `--plugin-dir` and exercises the matcher and exec-form behavior.
