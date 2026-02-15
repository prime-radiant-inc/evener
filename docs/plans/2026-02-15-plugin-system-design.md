# Plugin System Design

Date: 2026-02-15
Target: 100% drop-in compatibility with Claude Code plugins (local directory mode)

## Overview

Add support for Claude Code plugins specified via `--plugin-dir <path>` (repeatable).
A plugin is a local directory containing a `.claude-plugin/plugin.json` manifest and
optional component directories: skills, agents, hooks, MCP servers, and slash commands
(commands are loaded but ignored in this phase since serf is non-interactive).

## Architecture

A new `PluginManager` in the `agent/` package, initialized during `initSessionState()`
alongside existing skills discovery and MCP init. The session gains hook dispatch calls
at lifecycle points. Plugin-provided skills, agents, and MCP servers merge into existing
infrastructure with namespacing to prevent collisions.

```
CLI (--plugin-dir)
  │
  ▼
PluginManager.Load()
  ├── parse plugin.json manifests
  ├── discover skills → merge into session skill map (namespaced)
  ├── discover agents → register as spawnable agent types (namespaced)
  ├── discover hooks → build HookRunner
  ├── discover MCP configs → merge into MCP discovery
  └── discover commands → skip (non-interactive)
  │
  ▼
Session integration
  ├── HookRunner called at 9 lifecycle points
  ├── Plugin skills available via use_skill tool
  ├── Plugin agents available via spawn_agent tool
  └── Plugin MCP tools available via standard MCP path
```

## 1. Plugin Discovery & Manifest

### CLI

New `--plugin-dir <path>` flag in `cmd/serf/main.go` (repeatable `stringSliceFlag`).
Passed through `runConfig` → `SessionConfig.PluginDirs []string`.

### Loading

During `initSessionState()`, after skills discovery, before MCP init:

1. For each plugin dir, read `<dir>/.claude-plugin/plugin.json`
2. Validate: `name` required, kebab-case, unique across loaded plugins
3. Resolve component paths (custom paths from manifest supplement defaults)
4. Expand `${CLAUDE_PLUGIN_ROOT}` in all string values to the plugin's absolute dir path

### Manifest Schema

```go
type PluginManifest struct {
    Name        string `json:"name"`
    Version     string `json:"version,omitempty"`
    Description string `json:"description,omitempty"`
    Author      any    `json:"author,omitempty"`     // string or {name,email,url}
    Homepage    string `json:"homepage,omitempty"`
    Repository  string `json:"repository,omitempty"`
    License     string `json:"license,omitempty"`
    Keywords    []string `json:"keywords,omitempty"`

    // Component path overrides (supplement defaults, don't replace)
    Commands   any `json:"commands,omitempty"`   // string or []string, relative paths
    Agents     any `json:"agents,omitempty"`     // string or []string, relative paths
    Hooks      any `json:"hooks,omitempty"`      // string (path to hooks.json) or inline object
    MCPServers any `json:"mcpServers,omitempty"` // string (path to .mcp.json) or inline object
}
```

### Loaded Plugin

```go
type LoadedPlugin struct {
    Manifest  PluginManifest
    Dir       string // absolute path = CLAUDE_PLUGIN_ROOT
    Skills    map[string]SkillMeta
    Agents    map[string]PluginAgent
    Hooks     *PluginHooks
    MCPConfig map[string]MCPServerConfig
    Commands  []PluginCommand // loaded but not wired
}
```

## 2. Namespacing

All plugin components are namespaced at runtime to prevent cross-plugin collisions.
This matches Claude Code's behavior.

| Component | Runtime Name | Example |
|-----------|-------------|---------|
| Skill | `plugin-name:skill-name` | `code-review:tdd` |
| Agent | `plugin-name:agent-name` | `code-review:reviewer` |
| MCP tool | `mcp__plugin_<plugin>_<server>__<tool>` | `mcp__plugin_myplug_db__query` |
| Hook | No namespace needed (hooks don't have user-facing names) | — |
| Command | `plugin-name:command-name` | (loaded but unused) |

Skill namespacing: the `use_skill` tool accepts both `plugin-name:skill-name` and bare
`skill-name` (for non-plugin skills). If a bare name collides between a plugin skill
and a project skill, the project skill wins (project-local takes priority).

## 3. Skills from Plugins

Each plugin's `skills/` directory (plus any custom paths from manifest) is scanned
using the existing `scanSkillsDir()` function. Results are merged into the session's
`skills` map with namespaced keys:

```go
for name, meta := range plugin.Skills {
    namespacedName := plugin.Manifest.Name + ":" + name
    s.skills[namespacedName] = meta
}
```

The system prompt's skill listing includes plugin skills with their namespaced names.
The `use_skill` tool resolves namespaced names.

## 4. Agents from Plugins

### Agent File Format

Plugin agents are `.md` files in `agents/` with YAML frontmatter:

```yaml
name: agent-identifier       # required, kebab-case, 3-50 chars
description: ...              # required, triggering conditions + examples
model: inherit                # required: inherit|sonnet|opus|haiku
color: blue                   # required (for Claude Code UI; informational in serf)
tools: ["Read", "Write"]      # optional, restricts tool access
```

The markdown body is the agent's system prompt.

### Tool Name Mapping

Agent frontmatter `tools` arrays use Claude Code tool names. At load time, these are
mapped to serf's canonical tool names:

```go
var claudeToSerfToolNames = map[string]string{
    "Read":         "read_file",
    "Write":        "write_file",
    "Edit":         "edit_file",
    "Bash":         "shell",
    "Grep":         "grep",
    "Glob":         "glob",
    "Task":         "spawn_agent",
    "WebFetch":     "web_fetch",
    "WebSearch":    "web_search",
    "NotebookEdit": "notebook_edit",
}
```

Unmapped names are passed through as-is (for MCP tools, custom tools, etc.).

### Agent Registration

Plugin agents are stored in a session-level map. The `spawn_agent` tool is enhanced:
when the `task` argument references a known plugin agent (by namespaced name), the
subagent is created with:

- The agent's markdown body as a system prompt override (prepended to the profile prompt)
- The agent's `model` field (if not `inherit`) selecting a different model
- The agent's `tools` list restricting the subagent's tool registry

```go
type PluginAgent struct {
    Name         string
    Description  string
    Model        string
    Color        string
    Tools        []string // serf canonical names (mapped at load time)
    SystemPrompt string   // markdown body
    PluginName   string   // owning plugin (for namespacing)
}
```

The system prompt's agent listing includes plugin agents with descriptions so the LLM
knows when to spawn them.

## 5. Hook System

### HookRunner

A new `HookRunner` struct aggregates hooks from all plugins. It provides a single
dispatch method per event type. All matching hooks for an event run in parallel
(matching Claude Code behavior).

```go
type HookRunner struct {
    hooks map[HookEvent][]RegisteredHook
}

type HookEvent string

const (
    HookPreToolUse       HookEvent = "PreToolUse"
    HookPostToolUse      HookEvent = "PostToolUse"
    HookStop             HookEvent = "Stop"
    HookSubagentStop     HookEvent = "SubagentStop"
    HookUserPromptSubmit HookEvent = "UserPromptSubmit"
    HookSessionStart     HookEvent = "SessionStart"
    HookSessionEnd       HookEvent = "SessionEnd"
    HookPreCompact       HookEvent = "PreCompact"
    HookNotification     HookEvent = "Notification"
)

type RegisteredHook struct {
    Matcher    string // regex pattern, "*" = match all
    Type       string // "command" or "prompt"
    Command    string // for command hooks
    Prompt     string // for prompt hooks
    Timeout    int    // seconds (default: 60 for command, 30 for prompt)
    Model      string // for prompt hooks (optional, default: session model)
    PluginName string // owning plugin
    PluginDir  string // CLAUDE_PLUGIN_ROOT
}
```

### Hook Config Parsing

Plugin hooks are in `hooks/hooks.json` with wrapper format:

```json
{
  "description": "optional",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write|Edit",
        "hooks": [
          { "type": "command", "command": "...", "timeout": 30 },
          { "type": "prompt", "prompt": "...", "timeout": 30 }
        ]
      }
    ]
  }
}
```

Also supports inline hooks in `plugin.json` via the `hooks` field, and the direct
format (no wrapper) for settings-style configs.

### Hook Input

All hooks receive JSON on stdin matching Claude Code's format:

```json
{
  "session_id": "...",
  "cwd": "/working/dir",
  "hook_event_name": "PreToolUse",
  "tool_name": "write_file",
  "tool_input": { "file_path": "...", "content": "..." }
}
```

Event-specific fields:
- PreToolUse/PostToolUse: `tool_name`, `tool_input`, `tool_result` (post only)
- UserPromptSubmit: `user_prompt`
- Stop/SubagentStop: `reason`
- SessionStart/SessionEnd/PreCompact/Notification: common fields only

Tool names in hook input use Claude Code names (reverse-mapped from serf canonical)
so plugin scripts see the names they expect.

### Hook Output

Command hooks: exit code determines behavior.
- Exit 0: stdout is shown in transcript
- Exit 2: stderr is fed back as error
- Other: non-blocking error

Both command and prompt hooks can return structured JSON:

```json
{
  "continue": true,
  "suppressOutput": false,
  "systemMessage": "Message for the LLM"
}
```

PreToolUse hooks can additionally return:
```json
{
  "hookSpecificOutput": {
    "permissionDecision": "allow|deny|ask",
    "updatedInput": { "field": "modified_value" }
  }
}
```

Stop/SubagentStop hooks can return:
```json
{
  "decision": "approve|block",
  "reason": "..."
}
```

### Hook Execution

**Command hooks**: Run via `exec.Command` with environment variables:
- `CLAUDE_PROJECT_DIR` = working directory
- `CLAUDE_PLUGIN_ROOT` = plugin directory
- `CLAUDE_ENV_FILE` = path to env file (SessionStart only)

Hook input JSON is piped to stdin. Timeout enforced via context.

**Prompt hooks**: Call the session's LLM client with the hook's prompt text.
Variable substitution: `$TOOL_INPUT`, `$TOOL_RESULT`, `$USER_PROMPT` replaced with
JSON-encoded values from the hook input. Use the hook's `model` field if specified,
otherwise the session's main model.

### Hook Feedback

When a hook returns a `systemMessage` or stdout text:
- Injected via `s.Steer()` so the LLM sees it as a steering message next round
- This matches Claude Code's behavior of showing hook feedback in the transcript

When a PreToolUse hook returns `deny`: the tool call is blocked and an error message
is returned to the LLM as the tool result.

When a Stop hook returns `block`: the communicate(result) is suppressed and the
agentic loop continues with the block reason injected as a steering message.

### Matcher Semantics

Matchers are regex patterns tested against the tool name (for tool events) or `"*"`
for all events. Claude Code tool names are used for matching (not serf canonical names),
so a plugin matcher of `"Write|Edit"` matches write_file and edit_file after
reverse-mapping.

For non-tool events (SessionStart, Stop, etc.), the matcher typically matches against
`"*"` (all).

## 6. MCP from Plugins

Each plugin's `.mcp.json` (or inline `mcpServers` in manifest) is merged into the
MCP config discovery pipeline. Plugin MCP servers are namespaced with the existing
MCP namespacing scheme: `mcp__plugin_<pluginname>_<servername>__<toolname>`.

The `${CLAUDE_PLUGIN_ROOT}` variable is expanded in command, args, env, url, and
headers before the config is passed to the existing MCP manager.

Integration point: `DiscoverMCPConfigs()` is extended to accept plugin MCP configs
alongside project/global/CLI configs.

## 7. Plugin Settings

Support the `.claude/plugin-name.local.md` pattern for per-project plugin config.

At plugin load time, check for `<workdir>/.claude/<plugin-name>.local.md`. If present,
parse YAML frontmatter as key-value settings and make the body available as markdown
content. Settings are passed to the plugin's hooks as additional context in the hook
input JSON.

The settings file is not committed to git (plugin docs should advise adding it to
`.gitignore`).

```go
type PluginSettings struct {
    Frontmatter map[string]any
    Body        string
}
```

## 8. Session Integration Points

### initSessionState() changes

After existing skill discovery, before MCP init:

```
1. Load plugins from cfg.PluginDirs
2. Merge plugin skills into s.skills (namespaced)
3. Store plugin agents in s.pluginAgents
4. Build HookRunner from all plugin hooks
5. Merge plugin MCP configs into MCP discovery
6. Fire SessionStart hooks
```

### processOneInput() changes

```
existing: emit UserInput
new:      fire UserPromptSubmit hooks (on the input text)
existing: ...
existing: for each round:
new:        before MaybeCompact: fire PreCompact hooks
existing:   LLM request
existing:   tool calls
new:          before execTool: fire PreToolUse hooks (can block)
existing:     execTool
new:          after execTool: fire PostToolUse hooks
existing:   communicate(result) check
new:          before returning result: fire Stop hooks (can block)
```

### Close() changes

```
existing: cancel, cleanup, emit SESSION_END
new:      fire SessionEnd hooks (before closing event channel)
```

### spawn_agent changes

When spawning a subagent, if the task references a plugin agent name:
- Apply the agent's system prompt
- Apply model override
- Apply tool restrictions

On subagent completion, fire SubagentStop hooks.

## 9. Events

New event types for observability:

```go
EventPluginLoaded    // emitted per plugin during init
EventHookStart       // emitted when a hook begins execution
EventHookEnd         // emitted when a hook completes (with output/error)
```

## 10. What We Skip (For Now)

- **Slash commands**: Loaded from `commands/` directories but not wired to any
  invocation mechanism. Stored in `LoadedPlugin.Commands` for future use.
- **`/hooks` introspection command**: No interactive command system.
- **Plugin marketplace/installation**: Plugins are local directories only.
- **Hot-reload**: Plugins load at session start. Changes require new session.
- **`CLAUDE_CODE_REMOTE` env var**: Not applicable to serf.

## 11. File Organization

New files in `agent/`:

```
agent/
  plugin.go           // PluginManifest, LoadedPlugin, PluginManager
  plugin_test.go
  plugin_hooks.go     // HookRunner, hook dispatch, command/prompt execution
  plugin_hooks_test.go
  plugin_agents.go    // PluginAgent, agent discovery, tool name mapping
  plugin_agents_test.go
  plugin_settings.go  // PluginSettings, .local.md parsing
  plugin_settings_test.go
```

Changes to existing files:

```
agent/session.go      // Add pluginMgr, hookRunner fields; hook call sites
agent/skills.go       // No changes (reuse scanSkillsDir)
agent/mcp_config.go   // Extend DiscoverMCPConfigs to accept plugin configs
agent/subagents.go    // Enhance spawnAgent for plugin agent types
agent/profile.go      // Add plugin agents to system prompt building
cmd/serf/main.go      // Add --plugin-dir flag
cmd/serf/run.go       // Pass PluginDirs through to SessionConfig
```
