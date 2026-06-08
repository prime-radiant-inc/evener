# Plugin Agent Validation

Status: Proposed evergreen spec. Current serf plugin loading supports Claude/Codex-style plugin manifests and markdown agent definitions, but validation is intentionally thin in several places. This spec hardens plugin and plugin-agent validation without changing the successful load path for valid existing plugins.

## Purpose

Make plugin and plugin-agent loading fail predictably, with diagnostics that tell plugin authors exactly which manifest or agent field is invalid. Validation should catch author mistakes at load/session startup instead of allowing silent overwrites, ambiguous names, unsupported shapes, or capability requests that are only discovered during child execution.

## Goals

- Preserve the current plugin load model and successful path for valid `.codex-plugin` and `.claude-plugin` packages.
- Validate plugin manifests and plugin agent definitions with file path and field context.
- Reuse existing plugin name kebab-case rules for plugin agent names.
- Reject duplicate agent names inside one plugin instead of silently overwriting definitions.
- Define the split between static package validation and session/effective-catalog validation.
- Ensure `tools: all` means all tools effective for the child, never a bypass of parent/session policy.
- Keep validation small, testable, and tied to existing parser/load code.
- Keep diagnostics useful while redacting secrets from MCP, env, headers, hook, and provider-related config.

## Non-goals

- No full JSON Schema validator unless the codebase already adopts one for plugin metadata.
- No marketplace, registry, remote plugin, or package signing validation.
- No plugin hook execution, MCP process startup, or network calls during validation.
- No change to plugin package namespacing: plugin package agent maps remain keyed as `plugin-name:agent-name`. Preserve the existing session-catalog exposure behavior, including the bundled `coordinator-workflow` bare-name compatibility exception, unless a separate migration removes it.
- No broad rewrite of plugin discovery, skill loading, hook loading, MCP config parsing, or subagent dispatch.
- No guarantee of full Claude Code compatibility beyond the currently implemented subset.

## Current implementation anchors

- `agent/plugin/plugin.go` defines `Manifest` with fixed string fields and raw JSON component fields for `author`, `commands`, `agents`, `hooks`, and `mcpServers` (`agent/plugin/plugin.go:19-35`).
- `agent/plugin/plugin.go` already has the canonical kebab-case validator for plugin names (`agent/plugin/plugin.go:37-50`) and applies it in `ParseManifest` (`agent/plugin/plugin.go:53-64`).
- `agent/plugin/plugin.go` loads `.codex-plugin/plugin.json` first, then falls back to `.claude-plugin/plugin.json` (`agent/plugin/plugin.go:158-183`).
- `agent/plugin/plugin.go` loads skills, agents, hooks, and MCP configs in `Load` (`agent/plugin/plugin.go:188-207`) and detects duplicate plugin names in `LoadAll` (`agent/plugin/plugin.go:212-230`).
- `agent/plugin/plugin.go` resolves component directories by adding the default directory plus string or string-list overrides (`agent/plugin/plugin.go:233-260`).
- `agent/plugin/agents.go` defines plugin agent fields: `Name`, `Description`, `Model`, `Color`, `AllTools`, `Tools`, `Skills`, `Tasks`, `SystemPrompt`, and `PluginName` (`agent/plugin/agents.go:16-28`).
- `agent/plugin/agents.go` currently requires only non-empty string `name` and `description` (`agent/plugin/agents.go:39-58`), defaults `model` and `color` to `inherit` and `blue` (`agent/plugin/agents.go:60-68`), parses `tools` as scalar `all` or a list (`agent/plugin/agents.go:70-101`), parses `skills` as a list of strings (`agent/plugin/agents.go:103-116`), and parses `tasks` from object lists with optional fields (`agent/plugin/agents.go:118-147`).
- `agent/plugin/agents.go` discovers `.md` files, parses each agent, and currently assigns `agents[pluginName+":"+agent.Name] = agent`, which can overwrite duplicate names silently (`agent/plugin/agents.go:163-201`).
- `agent/plugin/settings.go` loads plugin-local project settings from `.claude/<plugin-name>.local.md` frontmatter/body (`agent/plugin/settings.go:10-39`); diagnostics must avoid echoing sensitive settings values.

## Validation model

Validation has two phases. Phase 1 is static package validation performed while loading the plugin package from disk. Phase 2 is session/effective-catalog validation performed after the session knows its real tool, skill, MCP, provider, and parent-child policy environment.

### Phase 1: static plugin package validation

Phase 1 runs inside `plugin.Load` before returning `plugin.Instance`.

It may read plugin package files that are already part of plugin load: manifest, agent markdown files, hook config files, skill directories, and MCP config files. It must not execute hooks, start MCP servers, run commands, call providers, or inspect runtime-only session state.

#### Manifest validation

Validate the parsed manifest and component override shapes before or during component discovery:

- `name`: required and kebab-case using the existing rule from `validatePluginName`. Preserve current error behavior for invalid plugin names.
- `version`: if present, must be a JSON string. Do not require semver unless a separate compatibility spec requires it.
- `description`, `homepage`, `repository`, `license`: if present, must be JSON strings.
- `keywords`: if present, must be an array of non-empty strings.
- `author`: allow the Claude-compatible string form or an object. If object, allow string fields `name`, `email`, and `url`. Reject non-string values. Unknown object keys should produce validation warnings only when plugin loading has access to a user-visible warning sink; otherwise ignore them for now.
- `commands`: currently ignored by plugin loading. If present, validate only that the raw value is syntactically valid JSON and document that no command loading occurs yet; do not fail solely for supported Claude/Codex command shapes until a command-loading spec exists.
- `agents`: must be either a string path or an array of string paths. Resolve relative to the plugin root. Reject absolute paths and path traversal that resolves outside the plugin root.
- `hooks`: must be either a string path or an inline object accepted by current hook discovery. Malformed present hook configuration fails with path/field context.
- `mcpServers`: must be an inline object accepted by current MCP parsing. `mcpServers: {}` is valid and means no inline servers. Manifest string-path `mcpServers` is not currently supported; the file-based MCP source remains the default `<pluginDir>/.mcp.json`. Malformed present MCP configuration fails with path/field context instead of being silently ignored.

Missing default component directories remain tolerated. A plugin with no `agents/`, no hooks, or no MCP config can still be valid. Malformed present components are errors.

#### Agent definition validation

Validate every plugin agent markdown file after parsing frontmatter and before adding it to the plugin's agent map:

- `name`: required, non-empty, and kebab-case using the same rule as plugin names. Plugin package keys remain `plugin-name:agent-name`; the session catalog must preserve existing exposure behavior, including `coordinator-workflow` exposing bundled agents by bare `agent.Name` through `exposedAgentCatalogKey`.
- `description`: required non-empty string.
- Duplicate agent names within one plugin: error with both source file paths. Never silently overwrite an earlier definition.
- `model`: optional. Phase 1 should only validate basic string shape and the reserved `inherit` value, because the plugin package does not know the session's provider instances. Validate resolvability against the documented Serf model/profile reference grammar at the Phase 2 resolver boundary used for `spawn_agent` model overrides. Claude aliases such as `sonnet`, `opus`, and `haiku` may be accepted only if that resolver or a compatibility layer intentionally supports them.
- `color`: optional non-empty string. Treat it as a display hint. Keep it free-form unless a Claude-compatible color set is deliberately documented; if constrained later, make that a compatibility choice with tests.
- `tools`: either scalar `all` or a list of non-empty strings. Preserve current rejection of scalar `*` and list entries `all` or `*`. Continue mapping Claude tool names to Serf canonical names during load.
- `skills`: list of non-empty strings. Do not require global/session skills during Phase 1. Plugin-local skill references may be checked after plugin skills have been discovered if local naming rules are documented.
- `tasks`: list of task template objects. Each object must be either:
  - a concrete task with non-empty `title` and non-empty `prompt`, plus optional valid `type` and `reasoning_effort`; or
  - an insertion marker with `insert: parent_tasks`. A marker with no non-empty `title`/`prompt` is skipped when no parent tasks are supplied. A marker that also has non-empty `title` and `prompt` is a fallback concrete task when no parent tasks are supplied.
- Agent markdown body / `SystemPrompt`: recommend erroring on an empty body for spawnable plugin agents. If existing fixtures rely on empty bodies, downgrade to a warning only when plugin loading has access to a user-visible warning sink.
- Unsupported high-risk Claude fields such as `permissionMode`, per-agent `mcpServers`, per-agent `hooks`, `memory`, `background`, or custom isolation fields should be rejected or explicitly documented as ignored. Do not document unsupported fields as working.

### Phase 2: session/effective-catalog validation

Phase 2 runs after the session has resolved:

- built-in tools;
- plugin agents and plugin-provided MCP tools after MCP registration;
- MCP tools that are statically available;
- provider/profile feature policy;
- parent-to-child subagent restrictions;
- approval/permission mode;
- project, builtin, and plugin skills.

#### Tool grants

- `tools: all` means every tool available to this session and permitted for this child by parent-effective policy. It never bypasses parent/session restrictions and must still exclude root-only subagent-management tools from child visibility and forged execution.
- Explicit tool lists are intersected with the parent/session effective allowed tools before exposure to the child model.
- The effective result tool (`communicate` by default, or the configured `ResultToolName`) remains available to children even when an explicit tool list narrows the registry, matching `RestrictKeepingResultTool`.
- Current Serf treats plugin-agent `tools: all` as an unrestricted child registry request after child session initialization, with root-only tools stripped by depth. Implementing the parent/session effective-policy intersection is a deliberate Phase 2 migration and needs characterization tests for the current behavior plus target tests for hidden non-root tools.
- Built-in/static unknown tool names should fail fast at session startup for loaded agents.
- MCP-dependent names may be deferred to spawn time only if MCP discovery can be partial or dynamic. The chosen behavior must be deterministic and documented.
- A tool hidden from the child model must also be denied if a forged or stale model response attempts to call it.

#### Skills

- Agent `skills` entries resolve according to documented precedence among plugin-local, project, and builtin skills. Define the reference grammar before enforcing failures, including how bare names, `plugin-name:skill`, project names, builtin names, and shadowing are interpreted.
- Missing plugin-local skills should fail plugin load if they are unambiguously local references under that grammar.
- Missing project/session skills currently behave best-effort during spawn. Phase 2 must choose and document one deterministic behavior before enforcing validation: keep them best-effort with surfaced diagnostics, or make them session-startup/child-spawn errors and update neighboring skill-injection docs/tests accordingly.

## Public API and CLI shape

Keep public additions small. The implementation can keep helpers internal until SDK embedders need plugin linting.

### Go API shape

```go
type ValidationIssue struct {
    Source   string // manifest path, agent file path, or component path
    Field    string // e.g. "name", "agents[0]", "tools[2]", "tasks[1].prompt"
    Severity string // "error" or "warning"; omit warnings until surfaced
    Rule     string // concise human-readable rule
}

type ValidationError struct {
    Issues []ValidationIssue
}

func (e ValidationError) Error() string
```

Suggested package-private or public helpers:

```go
func ValidateManifestJSON(data []byte, source string, pluginRoot string) (Manifest, []ValidationIssue)
func ValidateAgent(a Agent, source string) []ValidationIssue
func ValidatePluginInstance(inst Instance, source string) []ValidationIssue
```

If validation remains internal to `plugin.Load`, expose only `ValidationError` formatting. If a future SDK or CLI linter needs reuse, make the helper functions public then.

### CLI shape

A minimal CLI linter is useful only if there is already a CLI command surface for plugin tooling. If added, keep it read-only:

```text
serf plugin validate <plugin-dir>
serf plugin validate --json <plugin-dir>
```

Text output:

```text
<plugin-dir>/.codex-plugin/plugin.json: agents[0]: path must be relative and remain inside plugin root
<plugin-dir>/agents/reviewer.md: name: must be kebab-case
```

JSON output:

```json
{
  "ok": false,
  "issues": [
    {
      "source": "agents/reviewer.md",
      "field": "name",
      "severity": "error",
      "rule": "must be kebab-case"
    }
  ]
}
```

Do not add a CLI command solely to justify a public API. The primary acceptance path is load-time validation and tests.

## Diagnostics and redaction rules

Validation diagnostics must include enough location context to fix the issue:

- plugin directory;
- manifest, agent, hook, MCP, or settings file path;
- field/path within the parsed object when known;
- concise rule text;
- duplicate-conflict peer paths for duplicate agent names.

Diagnostics must not include secret values. Redact or omit:

- API keys and provider tokens;
- env variable values;
- HTTP headers, especially `Authorization`, `Cookie`, and `X-*Token`/`*-Key` variants;
- MCP command environment values;
- plugin settings values loaded from `.claude/<plugin-name>.local.md`;
- hook payloads that may contain tool input with secrets.

It is acceptable to include secret-bearing field names and source paths. It is not acceptable to print the secret value itself.

Prefer plugin-relative diagnostic paths with a single plugin-root header. Absolute paths may be included only in debug/JSON modes that explicitly opt into them.

Preferred formatting for grouped validation errors:

```text
invalid plugin at "/path/to/plugin":
  .codex-plugin/plugin.json: agents[1]: path escapes plugin root
  agents/reviewer.md: name: must be kebab-case
  agents/a.md and agents/b.md: name: duplicate agent name "reviewer"
```

Group multiple issues for one plugin when practical so authors can fix them in one pass. Do not build a large diagnostics framework if a `[]ValidationIssue` and formatter are sufficient.

## YAGNI / DRY implementation plan

1. Reuse `kebabCaseRe` / `validatePluginName` for agent names instead of creating a second regex.
2. Add a small validation file in `agent/plugin` only if needed, for example `validation.go`, containing `ValidationIssue`, `ValidationError`, and helper functions.
3. Keep existing parsing functions responsible for shape decoding. Add validation checks adjacent to the parser logic where the data is already available.
4. Validate manifest raw JSON component shapes before passing them into discovery helpers. Reuse existing `resolveComponentDirs` behavior after validating that overrides are safe strings/arrays.
5. In `discoverPluginAgents`, track `name -> source path` before assigning to the output map. Return a duplicate-name validation error instead of overwriting.
6. Extend `ParseAgent` validation narrowly: kebab-case `name`, `model` string-shape/`inherit` checks, non-empty list entries, stricter task templates, and optional empty-body handling. Keep session-provider resolvability checks outside `plugin`.
7. Keep Phase 2 checks near existing session/subagent tool-scope logic. Do not move tool catalog policy into the plugin package, because the plugin package does not know the session's effective catalog.
8. Add redaction helper reuse only if an existing redactor exists. Otherwise format validation errors from field names and paths, not raw config values.
9. Use warnings only where plugin loading can report them through an existing user-visible warning channel. Prefer errors for invalid supported fields and documented ignore behavior for unsupported compatibility fields.
10. Keep tests table-driven and fixture-based. Do not add broad integration scaffolding unless the behavior crosses plugin load and session effective policy.

## Acceptance criteria

- Invalid plugin names still fail as they do today.
- Invalid plugin agent names fail during plugin load with source file context.
- Duplicate agent names within one plugin fail with both file paths or enough path context to identify both definitions.
- Invalid manifest field types fail with manifest path and field context.
- Invalid `agents`, `hooks`, and `mcpServers` component shapes fail early with path/field context.
- Path overrides for components cannot escape the plugin root.
- Invalid agent `model`, `tools`, `skills`, and `tasks` shapes fail deterministically.
- Valid existing plugin fixtures continue loading.
- `.codex-plugin/plugin.json` continues to take precedence over `.claude-plugin/plugin.json` if both exist.
- Missing component directories remain tolerated.
- `tools: all` never grants tools outside the parent/session effective catalog.
- Explicit tool restrictions preserve the effective result tool so children can report completion.
- Unknown tool names have deterministic documented behavior: fail fast for built-in/static names; defer MCP-dependent names only when partial MCP discovery requires it.
- Validation errors never print secret MCP env values, headers, plugin settings values, or provider/API tokens.

## Tests

- Manifest table tests:
  - valid minimal manifest;
  - missing/invalid plugin `name` preserves current behavior;
  - non-string `version`, `description`, `homepage`, `repository`, or `license` fails;
  - invalid `keywords` entries fail;
  - valid and invalid `author` string/object forms;
  - invalid `agents`, `hooks`, and `mcpServers` shapes;
  - `mcpServers: {}` is accepted as an empty inline-server set.
- Component path tests:
  - relative `agents` path loads;
  - absolute path is rejected;
  - `../` path escaping plugin root is rejected;
  - missing default component directories are ignored.
- Agent frontmatter table tests:
  - minimal valid agent;
  - invalid/kebab-case-breaking `name` fails;
  - missing or non-string `description` fails;
  - invalid `model` fails;
  - valid `tools: all` passes;
  - scalar `*`, list `all`, list `*`, empty tool name, and non-string tool entries fail;
  - invalid `skills` shape and empty skill entries fail;
  - invalid task object, empty concrete `title`/`prompt`, invalid `type`, invalid `reasoning_effort`, and invalid `insert` fail;
  - marker-only `insert: parent_tasks` with no supplied parent tasks is skipped rather than becoming an empty concrete task, while marker templates with non-empty fallback `title`/`prompt` become fallback tasks.
- Duplicate agent test with two `.md` files in one plugin and assertion that both paths are reported.
- Compatibility fixture test showing valid existing bundled/plugin fixtures still load.
- Precedence test showing `.codex-plugin` is chosen before `.claude-plugin` when both manifests exist.
- Phase 2 policy tests:
  - `tools: all` is intersected with parent/session effective tools;
  - child cannot obtain a tool unavailable to the parent;
  - explicit plugin-agent tool lists preserve the configured result tool;
  - unknown static tool names fail at the documented point;
  - forged execution of a hidden tool is denied.
- Skill resolution tests for plugin-local and project/builtin references according to final precedence rules.
- Diagnostic formatting test for multi-issue validation output.
- Redaction test using MCP env/header-like values and plugin settings-like values, asserting secret values are absent while field names and source paths remain.

## Compatibility notes

- The manifest path precedence currently implemented is `.codex-plugin/plugin.json` before `.claude-plugin/plugin.json`; keep this behavior unless a separate compatibility decision changes it.
- `tools` continues to support Claude tool-name mapping through `toolname.ClaudeToSerf` during agent parsing.
- Plugin package agent maps remain namespaced as `plugin-name:agent-name`; session exposure preserves the current `coordinator-workflow` bare-name compatibility exception.
- MCP server names remain prefixed as `plugin_<pluginName>_...`.
- Unsupported Claude fields should be clearly ignored or rejected; they must not be partially interpreted without tests.
