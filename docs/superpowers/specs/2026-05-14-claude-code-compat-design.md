# Claude Code Plugin Compatibility — High-Level Design

Date: 2026-05-14
Status: approved high-level architecture; sub-specs to follow

## Goal

Make serf a drop-in host for Claude Code plugins. Any plugin published in a Claude Code marketplace must install, enable, and run under serf with no edits. Hooks, MCP servers, skills, agents, permissions, and user-config prompts must all behave per Claude Code's documented schemas.

This document is the architectural plan. Each numbered sub-project below gets its own detailed spec (written by a `claude-session-driver` worker) before implementation.

## Non-Goals

- Mirror Claude Code's filesystem layout. Serf keeps its own paths.
- Read Claude Code's own `~/.claude/` files. Strict separation.
- Reach feature parity with Claude Code's UI-only components (themes, output styles, status lines).
- Implement Language Server Protocol integration. Defer to a separate future spec.
- Wire slash command invocation. Plugins' `commands/` directories load but do not invoke. Defer.

## Storage Layout

```
~/.config/serf/
├── config.json                    (NEW: global serf config)
├── mcp.json                       (existing: mcp-only, still works)
└── plugins/
    ├── marketplaces/<name>/                       (NEW: cloned marketplace sources)
    ├── cache/<marketplace>/<plugin>/<version>/    (NEW: installed plugin payloads)
    ├── known_marketplaces.json                    (NEW: marketplace registry)
    ├── installed_plugins.json                     (NEW: plugin registry)
    └── trusted_projects.json                      (NEW: trust prompt persistence)

.serf/config.json                  (NEW: project serf config, committed)
.serf/mcp.json                     (existing: project mcp-only, still works)
```

All new state lives under `~/.config/serf/`. Existing serf paths and flags keep working unchanged. The `.claude/<plugin>.local.md` plugin-settings convention also keeps working — it is a contract between plugin author and user, not between tools.

## Config Schema

One `config.json` schema used identically at global and project scope:

```json
{
  "marketplaces":   { "name": { "source": { ... }, "autoUpdate": false } },
  "enabledPlugins": { "plugin@marketplace": true | { "version": "..." } },
  "hooks":          { "PreToolUse": [ ... ], "...": [...] },
  "mcpServers":     { "server-name": { ... } },
  "permissions":    { "allow": [...], "deny": [...], "defaultMode": "default" }
}
```

`hooks`, `mcpServers`, and `permissions` use Claude Code's schemas verbatim. That is what plugins emit and what users paste in.

## Sources and Layering

| Concern | Sources (lowest → highest precedence) |
|---|---|
| Hooks | global config.json → project config.json → `--config <path>` → plugin-provided |
| MCP servers | global config.json → global mcp.json → project config.json → project mcp.json → `--mcp-config` → `--mcp` → plugin-provided |
| Permissions | global config.json → project config.json → `--config <path>` |
| Marketplaces | global config.json → project config.json (project marketplaces require trust) |
| Enabled plugins | global config.json → project config.json |
| Ad-hoc plugin dirs | `--plugin-dir <path>` (orthogonal to enabledPlugins) |

Merge semantics per field: hooks arrays concatenate; map fields (`mcpServers`, `marketplaces`, `enabledPlugins`) replace by key; `permissions.allow` and `permissions.deny` concatenate; scalars overwrite.

The effective hook set at runtime is the union of all config-tier hooks and all enabled-plugin hooks. Same for MCP servers. Plugins do not declare permissions.

## Scope

### Hook Parity — A-tier

New events with direct serf analogs:

- `PostToolUseFailure` — fires after a tool call returns an error
- `PostToolBatch` — fires after a parallel tool batch resolves
- `StopFailure` — fires when a turn ends with an API error
- `SubagentStart` — fires when a subagent is spawned
- `UserPromptExpansion` — fires when a slash command or MCP prompt expands
- `PostCompact` — fires after context compaction completes
- `PermissionRequest` — fires before a permission dialog
- `PermissionDenied` — fires after auto-mode denial
- `ConfigChange` — fires when a config file changes mid-session

New hook types: `http`, `mcp_tool`, `agent`.

New hook config fields: `args` (exec form, no shell), `async`, `asyncRewake`, `shell`, `if`, `statusMessage`.

New output fields: `hookSpecificOutput.additionalContext`, `updatedInput` (PreToolUse), `permissionDecision: "defer"`, `permissionDecisionReason`, `sessionTitle` (UserPromptSubmit).

New common input fields: `transcript_path`, `permission_mode`, `effort`, `agent_id`, `agent_type`.

New environment variables: `CLAUDE_PLUGIN_DATA`, `CLAUDE_EFFORT`, `CLAUDE_CODE_REMOTE`.

Matcher dual-mode semantics: patterns containing only `[a-zA-Z0-9_|]` parse as exact-or-pipe-list; anything else parses as a JavaScript regex.

Deferred to later specs (B-tier and C-tier): `Setup`, `InstructionsLoaded`, `CwdChanged`, `FileChanged`, `Elicitation`, `ElicitationResult`, `WorktreeCreate`, `WorktreeRemove`, `TeammateIdle`, `TaskCreated`, `TaskCompleted`.

### MCP Parity — A-tier

- `streamable-http` accepted as alias for `http`
- `CLAUDE_PROJECT_DIR` automatically set in spawned stdio MCP server environment
- `${CLAUDE_PROJECT_DIR}` and `${user_config.KEY}` expansion in `command`, `args`, `env`, `url`, `headers`

Deferred: OAuth, `roots/list`, `npm` source type.

### Plugin Manifest Extensions — A-tier

- `userConfig` field: prompts user on plugin enable, stores values, exposes `${user_config.KEY}` substitution and `CLAUDE_PLUGIN_OPTION_*` environment variables
- `sensitive: true` values stored in OS keychain, fallback to `~/.config/serf/credentials.json` (mode 0600)
- `bin/` directory auto-added to Bash tool PATH while plugin is enabled (scoped per-Bash-invocation)
- Plugin-root `settings.json` honored for the `agent` default (activates a plugin-defined subagent as the main thread)
- `skills` custom paths (additive, alongside default `skills/`)

For unsupported manifest fields (`outputStyles`, `lspServers`, `experimental.themes`, `experimental.monitors`, `channels`, `dependencies`) and unsupported plugin-root `settings.json` keys (`subagentStatusLine` and any others), warn once per plugin per field at startup. Continue loading the plugin's supported components.

### Marketplace and Install — A-tier

`serf plugin marketplace add|remove|list|update <name>` manages marketplace sources. `serf plugin install|uninstall|update|list|enable|disable [plugin[@marketplace]]` manages installed plugins.

Marketplace source types supported for install: `directory`, `github`, `url` (git), `git-subdir` (with sparse partial clone). All four types appear in real-world marketplaces today.

Version management: explicit `version` in `plugin.json` wins; otherwise commit SHA of the plugin's git source serves as the version. For `directory` sources not inside a git repo, the version is recorded as `unknown`.

Project-declared marketplaces (`marketplaces` in `.serf/config.json`) require explicit user trust on first encounter. Trust persists in `~/.config/serf/plugins/trusted_projects.json` keyed by project path.

Deferred: `npm` source, `autoUpdate`, `dependencies`.

## Sub-Project Breakdown

Each sub-project becomes its own detailed spec.

**SP1 — Config loader.** `config.json` schema, three-source layering (global → project → `--config`), per-field merge semantics, validation with clear errors. Foundation for everything else.

**SP2 — Permissions matcher and enforcement.** Parse Claude Code permission patterns (`Bash(rm:*)`, `Skill(*)`, `mcp__server__tool`). Evaluate `allow`/`deny`/`ask` against tool calls. Honor `defaultMode`. Wire enforcement into the tool-call pipeline immediately after `PreToolUse` hooks.

**SP3 — Marketplace management.** Parse `marketplace.json` (all four supported source types). Resolve `metadata.pluginRoot`. Implement `serf plugin marketplace` subcommands. Maintain `known_marketplaces.json`. Run trust prompts for project-declared marketplaces.

**SP4 — Plugin install, uninstall, update.** Implement `serf plugin install|uninstall|update|list|enable|disable`. Maintain `installed_plugins.json`. Cache layout: `~/.config/serf/plugins/cache/<marketplace>/<plugin>/<version>/`. Resolve versions (semver from `plugin.json` → commit SHA fallback). Roll back partial installs.

**SP5 — Hook parity.** Add the nine A-tier events at their correct serf integration points. Implement the three new hook types (`http`, `mcp_tool`, `agent`). Add the six missing config fields, five missing output fields, five missing input fields, and three missing environment variables. Implement matcher dual-mode semantics.

**SP6 — MCP parity.** Accept `streamable-http` as an `http` alias. Inject `CLAUDE_PROJECT_DIR` into spawned stdio environments. Expand `${CLAUDE_PROJECT_DIR}` and `${user_config.KEY}` in all relevant config fields before transport creation.

**SP7 — Plugin manifest extensions.** Implement `userConfig` prompt-on-enable, secure storage for sensitive values, `CLAUDE_PLUGIN_OPTION_*` environment injection, `${user_config.KEY}` substitution. Auto-add `bin/` to the Bash tool's PATH. Honor plugin-root `settings.json`. Support additive `skills` custom paths. Warn once on unsupported fields.

**SP8 — Discovery integration.** Resolve `enabledPlugins` to cache paths and load alongside `--plugin-dir`. Merge config.json hooks/mcpServers/permissions with plugin-provided ones. Fire new lifecycle events at correct serf integration points. End-to-end test covers `marketplace add` → `install` → session triggers plugin hook → uninstall.

```
SP1 ─┬─ SP2 ──────────────────────────┐
     ├─ SP3 ── SP4 ──────────────────┐│
     ├─ SP5 (hook parity) ───────────┤│
     ├─ SP6 (MCP parity) ────────────┤│
     └─ SP7 (manifest extensions) ───┴┴── SP8 (integration)
```

SP1 blocks everything. SP2, SP5, SP6, SP7 can develop in parallel after SP1. SP4 depends on SP3. SP8 wires it all together last.

## Testing Strategy

TDD is the constraint. Each sub-spec lands tests before implementation.

| Sub-project | Test style | Key concerns |
|---|---|---|
| SP1 | Unit, table-driven | Schema parse, missing files OK, three-tier merge per field, validation errors with file/field location |
| SP2 | Unit + integration | Matcher patterns, allow/deny precedence, `defaultMode`, tool-call enforcement via middleware |
| SP3 | Unit + integration | marketplace.json parse for each source type, mocked git/HTTP fetch, registry round-trip, trust prompt |
| SP4 | Integration | Install from each source, version resolution, rollback on failure, enable/disable round-trip |
| SP5 | Unit + integration | One table-driven test per new event covering full I/O schema; harness test per new hook type (http via local server, mcp_tool via stub, agent via stub); matcher dual-mode; env var presence |
| SP6 | Unit + integration | `streamable-http` parses as http; spawned env contains `CLAUDE_PROJECT_DIR`; `${user_config.*}` substitutes before transport creation |
| SP7 | Unit + integration | `userConfig` schema validation, sensitive-vs-plain storage, env injection, `bin/` PATH visible to Bash tool, plugin settings.json default-agent activation, warn-on-unsupported fires exactly once per field |
| SP8 | End-to-end | Full lifecycle from `marketplace add` to plugin hook firing to uninstall |

Cross-cutting fixtures live under `agent/testdata/plugins/<scenario>/` and `agent/testdata/marketplaces/<scenario>/`. Each fixture is the smallest plugin or marketplace that exercises one feature. Stub git and HTTP servers live in `agent/internal/testsupport/`.

Conventions:

- No mocked filesystem. Use `t.TempDir()` and real files.
- No mocked LLM in unit tests beyond the existing `llm` stub.
- Real `git` binary for source fetching, with `t.Skip` when absent.
- Tests assert hook results, not order, where hooks fire in parallel.
- Tests for `prompt` and `agent` hooks use the existing `llm` stub.

Coverage gate per sub-project: all new exported functions have unit tests, at least one integration test exercises the primary integration point, SP5/SP6/SP7 features each have a fixture plugin that uses them, `go test ./...` is green.

## Cross-Cutting Concerns

### Security

Project-declared marketplaces require user trust on first encounter. Trust persists in `~/.config/serf/plugins/trusted_projects.json`. Untrusted projects' marketplaces are listed but not cloned.

`userConfig.sensitive: true` values live in the OS keychain. Where the keychain is unavailable, they fall back to `~/.config/serf/credentials.json` with mode 0600. Sensitive values never appear in `installed_plugins.json` or `config.json`.

Plugin `bin/` PATH injection is scoped to Bash tool invocations only. The injection does not propagate to the parent serf process or to other tools.

Hook commands run with serf's full privileges. No sandboxing. This matches Claude Code.

### Error Handling

Missing config files default silently, matching existing `mcp.json` behavior. Malformed config files fail fast at startup with the file path and offending field. Marketplace fetch failures abort the operation and leave registry state intact. Plugin install failures wipe the partial cache directory and remove any orphan registry entry. Hook failures follow Claude Code's documented exit-code semantics per event.

Unsupported manifest fields produce one warning per plugin per field and do not block the plugin's supported components from loading.

### Concurrency

Both registry files (`installed_plugins.json`, `known_marketplaces.json`) write atomically via tmp-file plus rename. A file lock on the registry serializes concurrent `serf plugin install` invocations.

### File Organization

New files in `agent/`:

- `config.go`, `config_test.go` (SP1)
- `permissions.go`, `permissions_test.go` (SP2)
- `plugin_userconfig.go`, `plugin_userconfig_test.go` (SP7)

New package `internal/plugins/`:

- `marketplace.go`, `marketplace_test.go` (SP3)
- `install.go`, `install_test.go` (SP4)
- `sources/{directory,github,url,gitsubdir}.go` (SP3)
- `registry.go`, `registry_test.go` (registry IO)

New subcommand tree `cmd/serf/plugin/`:

- `marketplace.go` (subcommands: add, remove, list, update)
- `install.go` (subcommands: install, uninstall, update, list, enable, disable)

Existing files touched, additive only:

- `agent/plugin.go` — read manifest extensions, warn-on-unsupported
- `agent/plugin_hooks.go` — A-tier events, new hook types, new fields
- `agent/mcp_config.go` — streamable-http alias, expansion additions
- `agent/mcp_manager.go` — `CLAUDE_PROJECT_DIR` env injection on stdio
- `cmd/serf/main.go`, `cmd/serf-tui/embedded.go`, `cmd/serf-hub/web.go`, `cmd/serfeval/main.go` — config loading at session init
- `agent/session.go` (or equivalent bootstrap) — fire new lifecycle events at correct integration points

## Open Questions

Each sub-spec resolves the questions tagged to it. None block this design.

1. **Hook ordering across config tiers.** When global, project, and plugin all declare `PreToolUse` matchers for the same tool, what order do they execute in? (SP1 + SP5)
2. **`enabledPlugins` value semantics.** Is `true` sufficient, or do we always require `{"version": "..."}` for reproducibility across machines? (SP4)
3. **Trust-prompt UX surfaces.** CLI prompts during interactive `serf plugin` invocation. What about `serf-tui` and `serf-hub`? Inline UI, or refuse until CLI-trusted? (SP3)
4. **`userConfig` UX surfaces.** Same triage. (SP7)

## Rollout

All changes additive. No existing path, file, or flag is removed. No migration script is required at v1. Existing serf users see no breakage.

A future `serf plugin migrate-config` helper can fold `.serf/mcp.json` into `.serf/config.json` if users request consolidation.
