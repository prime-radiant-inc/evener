# Claude Code Plugin Compatibility — High-Level Design

Date: 2026-05-14
Revised: 2026-05-14 (scope reduction — defer marketplace/install/permissions; agent-skill plugin management)
Status: approved high-level architecture; sub-specs to follow

## Goal

Make serf a drop-in host for Claude Code plugins. Any plugin published in a Claude Code marketplace must run under serf with no edits to the plugin's source. Hooks, MCP servers, skills, agents, and user-config prompts must behave per Claude Code's documented schemas.

We achieve compatibility for the things *plugins use* (hooks, MCPs, skills, agents, manifest fields). We defer the operational *tooling around plugins* (marketplaces, install/update, permissions enforcement) and replace marketplace/install work with an agent-driven skill — the LLM clones marketplaces and stages plugins into known directories using its existing Bash/Read/Write/WebFetch tools.

This document is the architectural plan. Each numbered sub-project below gets its own detailed spec before implementation.

## Non-Goals

- Mirror Claude Code's filesystem layout. Serf keeps its own paths.
- Read Claude Code's own `~/.claude/` files. Strict separation.
- Reach feature parity with Claude Code's UI-only components (themes, output styles, status lines).
- Implement Language Server Protocol integration. Defer.
- Wire slash command invocation. Plugins' `commands/` directories load but do not invoke. Defer.
- Build a Go-side marketplace client, plugin installer, version resolver, or trust-prompt UI. Replaced by SP-B (manage-plugins skill). The agent does this work.
- Enforce Claude Code permission patterns at runtime. Defer; serf trusts the user.

## Storage Layout

```
~/.config/serf/
├── config.json                    (NEW: global serf config — hooks + mcpServers)
├── mcp.json                       (existing: mcp-only; still works)
└── plugins/
    └── <plugin-name>/             (NEW: per-user plugin directory; auto-loaded)
        └── .claude-plugin/plugin.json
        └── ...

.serf/
├── config.json                    (NEW: project serf config)
├── mcp.json                       (existing: project mcp-only; still works)
└── plugins/<plugin-name>/         (NEW: per-project plugin directory; auto-loaded)
```

A plugin is "installed" when its directory exists under one of those `plugins/` parents. To remove a plugin, delete or rename the directory. To pin a version, the agent (via SP-B) clones at a specific ref or sha. No registry, no lockfile, no trust file — presence in the directory *is* the state.

The `--plugin-dir <path>` flag continues to work for ad-hoc local loading (dev mode). Symlinking into one of the known directories also works.

## Config Schema

One `config.json` schema, used identically at global and project scope:

```json
{
  "hooks":     { "PreToolUse": [...], "...": [...] },
  "mcpServers":{ "server-name": { ... } }
}
```

`hooks` and `mcpServers` use Claude Code's schemas verbatim. That is what plugins emit and what users paste in.

Three other top-level fields are recognized but produce a warning that they are not yet enforced: `marketplaces`, `enabledPlugins`, `permissions`. Users can declare them today without breakage; honoring them is deferred future work. (Implementations may parse and ignore.)

## Sources and Layering

| Concern | Sources (lowest → highest precedence) |
|---|---|
| Hooks | global config.json → project config.json → `--config <path>` → plugin-provided |
| MCP servers | global config.json → global mcp.json → project config.json → project mcp.json → `--mcp-config` → `--mcp` → plugin-provided |
| Plugin loading | global `~/.config/serf/plugins/*/` → project `.serf/plugins/*/` → `--plugin-dir <path>` |

Merge semantics: hooks arrays concatenate; `mcpServers` map entries replace by key; later plugin-dir paths shadow earlier ones by plugin name.

The effective hook set at runtime is the union of all config-tier hooks and all loaded-plugin hooks. Same for MCP servers.

## Scope

### Hook Parity — A-tier (unchanged)

Nine new events: `PostToolUseFailure`, `PostToolBatch`, `StopFailure`, `SubagentStart`, `UserPromptExpansion`, `PostCompact`, `PermissionRequest`, `PermissionDenied`, `ConfigChange`.

Three new hook types: `http`, `mcp_tool`, `agent`.

Six new config fields: `args` (exec form), `async`, `asyncRewake`, `shell`, `if`, `statusMessage`.

Five new output fields: `hookSpecificOutput.additionalContext`, `updatedInput` (PreToolUse), `permissionDecision: "defer"`, `permissionDecisionReason`, `sessionTitle` (UserPromptSubmit).

Five new common input fields: `transcript_path`, `permission_mode`, `effort`, `agent_id`, `agent_type`.

Three new environment variables: `CLAUDE_PLUGIN_DATA`, `CLAUDE_EFFORT`, `CLAUDE_CODE_REMOTE`.

Matcher dual-mode semantics: patterns containing only `[a-zA-Z0-9_|]` parse as exact-or-pipe-list; anything else parses as a JavaScript regex.

Note: `PermissionRequest` and `PermissionDenied` still fire as lifecycle events because plugins may listen for them, but serf does not yet *enforce* a permission decision — the events fire with `default-allow` semantics until permissions enforcement lands as a follow-up.

### MCP Parity — A-tier (unchanged)

- `streamable-http` accepted as alias for `http`
- `CLAUDE_PROJECT_DIR` automatically set in spawned stdio MCP server environment
- `${CLAUDE_PROJECT_DIR}` and `${user_config.KEY}` expansion in `command`, `args`, `env`, `url`, `headers`

### Plugin Manifest Extensions — A-tier (unchanged surface; simpler trigger)

- `userConfig` field: prompts user **on first load** (no install flow, so first-load is the trigger), persists values, exposes `${user_config.KEY}` substitution and `CLAUDE_PLUGIN_OPTION_*` environment variables
- `sensitive: true` values stored in OS keychain, fallback to `~/.config/serf/credentials.json` (mode 0600)
- `bin/` directory auto-added to Bash tool PATH while plugin is enabled (scoped per-Bash-invocation)
- Plugin-root `settings.json` honored for the `agent` default
- `skills` custom paths (additive, alongside default `skills/`)
- Warn-once-per-plugin-per-field for unsupported manifest fields (`outputStyles`, `lspServers`, `experimental.themes`, `experimental.monitors`, `channels`, `dependencies`)

### Plugin Auto-Discovery — NEW

- Walk `~/.config/serf/plugins/*/` for user-level plugins
- Walk `<project>/.serf/plugins/*/` for project-level plugins
- Each subdirectory containing `.claude-plugin/plugin.json` loads as a plugin (existing `LoadPlugin` logic)
- Symlinks are followed
- Plus existing `--plugin-dir <path>` (repeatable) for ad-hoc loading
- All three paths union into the loaded plugin set; later sources shadow earlier on plugin-name collision

### Plugin Management — Agent Skill, NEW

A builtin skill `agent/skills/manage-plugins/SKILL.md` documents how the agent installs, updates, and removes plugins by manipulating the known directories with its existing tools.

When a user asks "install plugin X from marketplace Y," the agent:
1. Fetches `marketplace.json` from Y (Bash + git, or WebFetch)
2. Locates plugin X's source entry
3. Clones or downloads the plugin into `~/.config/serf/plugins/<X>/` (or `.serf/plugins/<X>/` if project-scoped)
4. Verifies `.claude-plugin/plugin.json` exists
5. Reports the install path and asks the user to reload serf

Updates: re-clone or `git pull` in place. Uninstall: `rm -rf` the directory.

This is one skill file (markdown). No Go code, no new CLI surface.

### Deferred (not in this release)

- **Permissions enforcement** (was SP2). `permissions` field parsed but ignored. Add when there's user demand. Specs and plan committed for future use.
- **Marketplace tooling** (was SP3). Go-side marketplace client + `serf plugin marketplace` subcommands. Replaced by SP-B in this release. Spec and plan committed for future use.
- **Plugin install/uninstall tooling** (was SP4). Go-side `serf plugin install|uninstall|update`. Replaced by SP-B. Specs and plan committed for future use.
- **B-tier / C-tier hook events, OAuth MCP, npm sources, dependencies, channels, monitors, output styles, themes, LSP servers.** Unchanged from original design.

## Sub-Project Breakdown

In-scope sub-projects:

**SP1 — Config loader.** Schema (`hooks`, `mcpServers`; tolerant of `marketplaces`/`enabledPlugins`/`permissions` with a deferred warning), three-source layering (global → project → `--config`), per-field merge semantics, validation with clear errors.

**SP5 — Hook parity.** Nine A-tier events at correct integration points. Three new hook types. Missing config/output/input fields. New env vars. Matcher dual-mode semantics.

**SP6 — MCP parity.** `streamable-http` alias. `CLAUDE_PROJECT_DIR` env injection. `${CLAUDE_PROJECT_DIR}` and `${user_config.KEY}` expansion.

**SP7 — Plugin manifest extensions.** `userConfig` prompt-on-first-load, secure storage, `CLAUDE_PLUGIN_OPTION_*` env injection, `${user_config.KEY}` substitution, `bin/` PATH, plugin-root `settings.json`, additive `skills` custom paths, warn-once-on-unsupported.

**SP-A — Filesystem plugin discovery.** Walk `~/.config/serf/plugins/*/` and `<project>/.serf/plugins/*/` at session startup. Load each as a plugin. Resolve collisions with `--plugin-dir`. Symlink-friendly.

**SP-B — Manage-plugins agent skill.** Builtin skill documenting plugin install/update/remove via the agent's existing tools. Markdown only.

**SP8 — Discovery integration.** Session-startup pipeline that wires SP1 + SP-A + SP5 + SP6 + SP7. Fire new lifecycle events at correct points. End-to-end test (load plugin from a plugins/ directory → trigger plugin hook → confirm hook fires with new event/field).

Deferred but specs already written: **SP2 (permissions), SP3 (marketplace), SP4 (install).**

```
SP1 ──┬─ SP5 (hook parity) ─────┐
      ├─ SP6 (MCP parity) ──────┤
      ├─ SP7 (manifest ext) ────┤
      └─ SP-A (filesystem disc) ┤
                                ├── SP-B (skill, parallel; markdown only)
                                └── SP8 (integration)
```

Implementation ordering (walking-skeleton; see ordering rationale below):

1. **SP1** — foundation
2. **SP8 partial** — minimal integration: all four binaries load `config.json`; existing `--plugin-dir` still works; smoke test
3. **SP-A** — auto-discovery from known directories; second integration of SP1's seam
4. **SP6** — MCP parity (smallest new feature; validates the seam)
5. **SP7** — manifest extensions (userConfig + bin/ + plugin settings.json)
6. **SP-B** — manage-plugins skill (parallel with SP7; markdown only)
7. **SP5** — hook parity (largest payload, last; benefits from real installed plugins to test against)
8. **SP8 full** — fire all new lifecycle events; full end-to-end suite

Rationale for "SP5 last": with no installed plugins, hook parity is implemented against docs. With real plugins present (via SP-A + SP-B), hook implementation can be prioritized by what plugins actually use. The ordering also stages risk — each step validates the previous integration before adding to it.

## Testing Strategy

TDD is the constraint. Each sub-spec lands tests before implementation.

| Sub-project | Test style | Key concerns |
|---|---|---|
| SP1 | Unit, table-driven | Schema parse, missing files OK, three-tier merge per field, validation errors with file/field location, tolerance of deferred fields |
| SP5 | Unit + integration | One table-driven test per new event covering full I/O schema; harness test per new hook type; matcher dual-mode; env var presence |
| SP6 | Unit + integration | `streamable-http` parses as http; spawned env contains `CLAUDE_PROJECT_DIR`; `${user_config.*}` substitutes before transport creation |
| SP7 | Unit + integration | `userConfig` schema validation, sensitive-vs-plain storage, env injection, `bin/` PATH visible to Bash tool, plugin settings.json default-agent activation, warn-on-unsupported fires exactly once per field |
| SP-A | Unit + integration | Walks each known directory, follows symlinks, surfaces correct collision precedence, empty / missing directories OK |
| SP-B | Skill validation only | The skill file exists, parses as frontmatter+markdown, is registered in builtin_skills.go, and is selectable via the existing skill loader |
| SP8 | End-to-end | Drop a plugin in `~/.config/serf/plugins/foo/`, start a session, send a tool call, observe its hook fires with the expected new fields |

Cross-cutting fixtures live under `agent/testdata/plugins/<scenario>/`. Conventions are unchanged from the original design: real filesystem via `t.TempDir()`; no mocked LLM beyond the existing `llm` stub; `httptest.NewServer` for HTTP hooks; existing MCP stub for `mcp_tool` hooks.

## Cross-Cutting Concerns

### Security

`userConfig.sensitive: true` values live in the OS keychain; fallback `~/.config/serf/credentials.json` mode 0600. Sensitive values never appear in `config.json` or anywhere on disk in plaintext.

Plugin `bin/` PATH injection is scoped to Bash tool invocations only. Not exported to the parent process or other tools.

Hook commands run with serf's full privileges. No sandboxing. This matches Claude Code.

Marketplace cloning happens through the SP-B skill, which means **the user reads the marketplace source URL before approving the agent's `git clone`** — natural trust gate without a registry-and-prompt system.

### Error Handling

Missing config files default silently (matches existing `mcp.json` behavior). Malformed config files fail fast at startup with the file path and offending field. Plugin discovery failures (corrupt manifest in one plugin directory) skip that plugin and warn, but other plugins still load. Hook failures follow Claude Code's documented exit-code semantics per event.

Unsupported manifest fields produce one warning per plugin per field. Deferred top-level config fields (`marketplaces`, `enabledPlugins`, `permissions`) produce a single warning each per session.

### File Organization

New files in `agent/`:

- `config.go`, `config_test.go` (SP1)
- `plugin_userconfig.go`, `plugin_userconfig_test.go` (SP7)
- `plugin_discovery.go`, `plugin_discovery_test.go` (SP-A)
- `skills/manage-plugins/SKILL.md` (SP-B)

Existing files touched, additive only:

- `agent/plugin.go` — manifest extensions, warn-on-unsupported
- `agent/plugin_hooks.go` — A-tier events, new hook types, new fields
- `agent/mcp_config.go` — streamable-http alias, expansion additions
- `agent/mcp_manager.go` — `CLAUDE_PROJECT_DIR` env injection on stdio
- `agent/builtin_skills.go` — register SP-B
- `cmd/serf/main.go`, `cmd/serf-tui/embedded.go`, `cmd/serf-hub/web.go`, `cmd/serfeval/main.go` — config loading + auto-discovery at session init
- `agent/session.go` (or equivalent bootstrap) — fire new lifecycle events at correct integration points

## Open Questions

1. **Hook ordering across config tiers.** When global, project, and CLI all declare `PreToolUse` matchers for the same tool, what order do they execute in? Settled in SP1: global → project → CLI → plugin-provided.
2. **`userConfig` UX surfaces.** CLI prompts inline. `serf-tui` prompts inline. `serf-hub` web form. For non-interactive runs, `--plugin-option <plugin>.<key>=<value>` flag or refuse-with-clear-error. (SP7)
3. **Project-vs-user plugin precedence.** When the same plugin name exists in both `~/.config/serf/plugins/foo/` and `<project>/.serf/plugins/foo/`, the project copy wins. `--plugin-dir` wins over both. (SP-A)

## Rollout

All changes additive. No existing path, file, or flag is removed. Existing `.serf/mcp.json`, `~/.config/serf/mcp.json`, `--mcp-config`, `--mcp`, and `--plugin-dir` keep working. Existing serf users see no breakage.

## Deferred Work — Reference Material

The following specs and plans are committed and ready when the work surfaces:

- `2026-05-14-claude-code-compat-sp2-permissions-design.md` + plan
- `2026-05-14-claude-code-compat-sp3-marketplace-design.md`  (plan not written; deferred)
- `2026-05-14-claude-code-compat-sp4-install-design.md` + plan

When permissions or operational install tooling becomes a real ask, those specs are the starting point.
