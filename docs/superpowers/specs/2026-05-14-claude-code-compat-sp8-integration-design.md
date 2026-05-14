# SP8 — Discovery Integration (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
Depends on: SP1, SP2, SP3, SP4, SP5, SP6, SP7

## 1. Goal

SP8 is the seam. It owns the order in which every prior sub-project's output is composed at session startup, so that one call to `agent.NewSession` produces a session whose hooks, MCP servers, permissions, skills, agents, plugin binaries, and user-config substitutions all reflect the merged Claude-Code-shaped configuration. SP8 makes no new architectural decisions of its own — every behavior here is the contract between two earlier sub-projects' surfaces. What SP8 owns:

- The exact call sequence at session startup that turns a `SerfConfig` ([ref: SP1 §2]) plus a set of CLI flags into a fully wired `Session`.
- A new typed field set on `SessionConfig` that lets the four CLI binaries hand the merged config to `agent.NewSession` without forcing each binary to re-implement loader composition.
- The CLI-side wiring in `cmd/serf`, `cmd/serf-tui`, `cmd/serf-hub`, and `cmd/serfeval` that calls `DiscoverSerfConfig` ([ref: SP1 §2]) and threads the result into `SessionConfig`.
- The fire sites in `agent/session.go` and `agent/subagents.go` for the seven new lifecycle events SP5 ships, plus the two SP2 fires from its enforcement layer.
- The end-to-end test: marketplace add ([ref: SP3 §5.1]) → install ([ref: SP4 §4]) → session start fires plugin hook ([ref: SP5 §3]) → uninstall ([ref: SP4 §5]) leaves no residue.

What SP8 does not own:

- Any internal logic of SP1..SP7. SP8 only composes their exported surfaces.
- The CLI subcommand tree for `serf plugin` ([ref: SP3 §5], [ref: SP4 §8]).
- The matcher, evaluator, or fire-site implementations of new hook events ([ref: SP5 §3]) — SP8 lists which events fire where; SP5 implements them.

## 2. Session-Startup Pipeline

Every entry point follows the same sequence. SP8 ships one helper, `agent.BuildSessionConfig(env, flags) (SessionConfig, error)`, that runs steps 1–4 and produces the `SessionConfig` value handed to `agent.NewSession`. Steps 5–11 run inside `NewSession` and `initPlugins`.

```
 #  Step                                          Owner
 1  Load SerfConfig (global → project → --config) SP1   DiscoverSerfConfig
 2  Resolve enabledPlugins → cache paths           SP4   Installer.List / ResolvePlugin
 3  Apply --plugin-dir overrides                   SP8   merge into resolved plugin set
 4  Construct PermissionMatcher                    SP2   NewPermissionMatcher
 5  Build SessionConfig with merged values         SP8   per binary
 6  agent.NewSession                               agent existing
 7  initPlugins: LoadPlugin per cache path         SP7   LoadPlugin (extended)
 8  Merge plugin-provided hooks with config-tier   SP8   merged HookRunner
 9  Resolve UserConfig per plugin                  SP7   ResolveUserConfig + prompter
10  initMCP: merge plugin mcpServers + config tier SP6   MergeMCPConfigs (extended)
11  Fire SessionStart + ConfigChange watchers      SP5   existing + new fire sites
```

### 2.1 Per-step responsibilities

**Step 1.** `DiscoverSerfConfig(env, cliConfigPaths)` returns the merged `SerfConfig`. Failure aborts startup with the file path that failed ([ref: SP1 §5]). SP8 always calls this even when no `--config` flag is set — the global and project tiers still apply.

**Step 2.** For each key in `cfg.EnabledPlugins`, split `"plugin@marketplace"` and call `installer.ResolveCachePath(plugin, marketplace)` ([ref: SP4 §2]). The installer reads `installed_plugins.json`; a missing entry yields the "plugin not installed" warning of §10. The result is an ordered slice of `(pluginID, cachePath, version)`.

**Step 3.** Each `--plugin-dir` path is appended to the plugin set with `pluginID = filepath.Base(path)`, `cachePath = path`, `version = "ad-hoc"`. Order: config-tier `enabledPlugins` first (in `Sources` order [ref: SP1 §4]), then `--plugin-dir` entries in CLI order. Name collisions follow §13.

**Step 4.** `NewPermissionMatcher(cfg.Permissions, env)` ([ref: SP2 §2]). Parse errors abort startup with the rule string ([ref: SP2 §8.1]).

**Step 5.** SP8 assembles a `SessionConfig` whose new fields (§4) carry the merged values forward.

**Step 6.** `agent.NewSession` is unchanged except for the new field plumbing.

**Step 7.** `initPlugins` walks the plugin set from step 3 and calls `LoadPlugin(cachePath)` for each. `LoadedPlugin` ([ref: SP7 §2]) carries `BinDir`, `DefaultAgent`, `UserConfigOptions`, `Warnings`, plus the existing `Skills`, `Agents`, `Hooks`, `MCPConfigs`.

**Step 8.** SP8 builds the effective hook table by walking, in order: `cfg.Hooks` ([ref: SP1 §2]) — already merged across config tiers in firing order ([ref: SP1 §9.1]) — followed by every loaded plugin's `Hooks` map in load order. The `HookRunner` is fed one event at a time so the order of `runner.Add(event, ...)` calls is what determines per-event firing order ([ref: SP5 §14.1]).

**Step 9.** For each loaded plugin, `ResolveUserConfig(pluginID, opts, plainStore, secureStore)` ([ref: SP7 §6.1]) produces a `*ResolvedUserConfig`. SP8 stores the map `pluginID → *ResolvedUserConfig` on the session and binds the lookup function `userConfigLookup` ([ref: SP6 §2.2]) for that plugin into its MCP and hook expansions.

**Step 10.** `initMCP` discovers MCP layers in the existing layering order ([ref: parent spec §Sources]) and now also merges `cfg.MCPServers` (from SP1's loader) and each plugin's `MCPConfigs`. The merge function is the existing `MergeMCPConfigs`; expansion uses the per-plugin `expansionContext` from SP6 ([ref: SP6 §5]).

**Step 11.** `SessionStart` hooks fire ([ref: existing]). If `cfg.WatchConfig` is true, `agent/config_watcher.go` starts ([ref: SP5 §3.9]); the resulting `ConfigChange` events fire mid-session.

### 2.2 Subagent inheritance

A subagent's `SessionConfig` inherits the parent's `Permissions`, `PermissionAskFallback` ([ref: SP2 §9]), `MergedConfig`, and resolved plugin set verbatim. `spawnAgent` does not re-run steps 1–4; it copies the parent's pre-resolved structures into the child's `SessionConfig`. The child still re-runs step 7 (LoadPlugin) so plugin state is not shared across goroutines, but the cache-path list is the parent's.

## 3. Effective Configuration Assembly

This section pins the union rules across tiers and plugins, by field. Every rule is settled by an earlier SP; SP8 enforces the composition.

### 3.1 Hooks

`SerfConfig.Hooks` is already merged across global → project → CLI in firing order ([ref: SP1 §4, §9.1]). SP8 appends plugin-provided hook arrays after the config-tier array for each event:

```
finalHooks[event] = cfg.Hooks[event] ++ plugin1.Hooks[event] ++ plugin2.Hooks[event] ++ ...
```

Plugins are walked in load order (config-tier `enabledPlugins` first, then `--plugin-dir`). Within a single plugin's manifest, the array order is what `agent/plugin.go` already produces.

A hook declared in both `config.json` and a plugin both fire — the union is the contract, not deduplication. The config-tier hook runs first, so users can short-circuit plugin behavior with a `permissionDecision: "deny"` in their own config ([ref: SP5 §9.1 rationale]).

### 3.2 MCP servers

The existing layering ([ref: parent spec §Sources]) is: global `config.json` → global `mcp.json` → project `config.json` → project `mcp.json` → `--mcp-config` → `--mcp` → plugin-provided. SP1 supplies the two `config.json` tiers; the rest is unchanged. SP8's contribution:

1. `cfg.MCPServers` (from SP1) is inserted into the layer order at the two `config.json` slots before `initMCP` calls `MergeMCPConfigs` ([ref: SP6 §2.3]).
2. Plugin MCP configs are appended as the highest-precedence layer per the existing code (`MergeMCPConfigs(s.pluginMCPConfigs, configs)` reversed to put plugins last). SP8 inverts the current ordering to put plugin configs last so they win on key collision, matching the parent spec.

Merge semantics: map replace by key ([ref: SP1 §4]). Same key in two layers — the higher-precedence layer's value replaces the lower.

`${user_config.KEY}` and `${CLAUDE_PROJECT_DIR}` expansion happens at the per-layer load step using each layer's plugin-binding ([ref: SP6 §5]). Top-level `config.json` mcpServers have `userConfig: nil`; plugin-bundled mcpServers have the per-plugin lookup bound.

### 3.3 Permissions

Permissions come from `cfg.Permissions` ([ref: SP1 §2]) only — global → project → `--config`, allow/deny concatenate, `defaultMode` scalar-overwrites. Plugins do not declare permissions ([ref: parent spec §Sources]).

SP8 passes the merged `PermissionsConfig` into `NewPermissionMatcher` ([ref: SP2 §6.2]) and onto `SessionConfig.Permissions`. The matcher is constructed once per session and never re-built mid-session (SP5's `ConfigChange` event allows a future SP to refresh).

### 3.4 Skills

Existing merge: built-in skills, `cfg.SkillsDirs`, and per-plugin skills (default `skills/` plus `manifest.skills` overrides, [ref: SP7 §10]) all flow through `s.skills`. SP8 adds nothing — the existing `initPlugins` loop already takes the union with last-write-wins on name collision.

### 3.5 Agents

Same as skills. `s.pluginAgents` is the merged map; SP7's `DefaultAgent` (from plugin-root `settings.json`) feeds the active-agent selection in the existing session init ([ref: SP7 §9.1]) with last-non-empty-value-wins across loaded plugins.

### 3.6 enabledPlugins resolution

For each key `"plugin@marketplace"` in `cfg.EnabledPlugins`:

1. Split on the last `@`. A bare `"plugin"` with no `@` is an error at SP8 boundary (SP4 already enforces this on install [ref: SP4 §4.1]; SP8 fails closed for safety).
2. Call `installer.ResolveCachePath(pluginName, marketplaceName)` which reads `installed_plugins.json` and returns the per-scope install entry. Scope priority: project → user (project wins for the session).
3. A missing entry warns and skips ([ref: §10]).
4. Trust enforcement for project-tier marketplaces runs before resolution ([ref: SP3 §7.1]) — `EnforceTrustOnConfig(cfg, env, prompter)` is called by SP8 between steps 1 and 2 of the pipeline.

The value side of `enabledPlugins` (`true` vs `{"version": "..."}`) does not affect SP8's resolution — the version was pinned at install time and lives in the registry. The pinned-version object is consulted only by `serf plugin update` ([ref: SP4 §6]).

## 4. SessionConfig Schema Changes

`SessionConfig` ([ref: existing `agent/session.go:72`]) gains these fields. All are zero-value-safe; existing tests pass unchanged.

```go
// Permissions is the merged PermissionsConfig from SP1, used by SP2 to
// construct the session's PermissionMatcher.
Permissions PermissionsConfig

// PermissionAskFallback dictates what happens when a permission rule
// yields "ask" on a surface that cannot prompt. Per-surface defaults in
// SP2 §9; CLI binaries pick at construction.
PermissionAskFallback AskFallback

// MergedConfig is the full SerfConfig produced by SP1's loader. Carried
// on the session for ConfigChange diffing (SP5 §3.9) and observability.
MergedConfig SerfConfig

// EnabledPluginPaths is the ordered list of (pluginID, cachePath,
// version) tuples produced by SP4's resolver. NewSession reads it to
// know which plugin directories to load. Replaces ad-hoc use of
// PluginDirs when populated.
EnabledPluginPaths []ResolvedPlugin

// PluginConfigStore and SecureStore are SP7's persistence layers. SP8
// injects them so NewSession can build the per-plugin
// ResolvedUserConfig values without each binary owning the construction.
PluginConfigStore PluginConfigStore
SecureStore       SecureStore

// UserConfigPrompter is the surface-specific prompter (SP7 §4.2). The
// CLI binary chooses one based on its UX.
UserConfigPrompter UserConfigPrompter

// WatchConfig, when true, starts the fsnotify watcher that fires
// ConfigChange events (SP5 §3.9). Default false.
WatchConfig bool

// IsRemote signals serf-hub-style embedding; SP5 reads this when setting
// the CLAUDE_CODE_REMOTE env var on spawned hook processes.
IsRemote bool
```

`ResolvedPlugin` is a new struct:

```go
type ResolvedPlugin struct {
    PluginID   string // "plugin@marketplace" or just "plugin" for --plugin-dir
    CachePath  string // absolute path to the plugin root
    Version    string // resolved version per SP4 §9
    Source     PluginSourceKind // "enabled" | "plugin-dir"
}
```

**Deprecation.** `SessionConfig.PluginDirs` ([ref: `agent/session.go:104`]) remains and continues to work. SP8 maps it onto `EnabledPluginPaths` with `Source = "plugin-dir"` when `EnabledPluginPaths` is empty. New code populates `EnabledPluginPaths` directly. The two are not allowed to both be populated; SP8 errors at `NewSession` time with `cannot set both PluginDirs and EnabledPluginPaths`. The intent is to let existing tests and the existing `--plugin-dir` flag keep working while SP8 routes the marketplace-installed plugins through `EnabledPluginPaths`.

### 4.1 `--config` and `--plugin-dir` interaction

Both are CLI-level concerns and orthogonal:

- `--config <path>` (new, SP1 wires the parser) is the third tier of `SerfConfig` ([ref: SP1 §4]). Repeatable. Each path is loaded in CLI order.
- `--plugin-dir <path>` (existing) bypasses the marketplace and adds an ad-hoc plugin payload directly. Repeatable. Per §13, a `--plugin-dir` plugin shadowing an `enabledPlugins` entry wins for that session.

The two flags do not conflict. Both populate distinct fields on `SessionConfig`.

## 5. Bootstrap Changes per Binary

Each binary owns one well-defined construction site for `SessionConfig`. SP8's changes are mechanical and additive.

### 5.1 `cmd/serf` (`run.go`)

Files touched:

- `cmd/serf/main.go` — add `--config <path>` repeatable flag; add `--trust-marketplace <name>` repeatable flag ([ref: SP3 §7.4]); add `--plugin-option <plugin>.<key>=<value>` repeatable flag ([ref: SP7 §4.2]).
- `cmd/serf/run.go` — `runConfig` grows the four new field slices; `run()` calls `BuildSessionConfig(env, flags)` before constructing the existing `SessionConfig` literal and merges the returned fields into it.

Functions touched:

- `run()` (`run.go:68`) — between the env construction and the existing `agent.SessionConfig{...}` literal, call `agent.BuildSessionConfig(env, flags)` and copy the resulting `Permissions`, `PermissionAskFallback`, `MergedConfig`, `EnabledPluginPaths`, `PluginConfigStore`, `SecureStore`, `UserConfigPrompter`, `WatchConfig` into the literal.
- `serve()` (`cmd/serf/serve.go:63`) — same change, applied to the daemon-mode `SessionConfig`.

Prompter selection: `CLIPrompter` ([ref: SP7 §4.2]) when stdin is a TTY; `NonInteractivePrompter` otherwise.

`PermissionAskFallback` selection: `AskFallbackInteractive` for TTY foreground; `AskFallbackDeny` for `-p` and piped stdin ([ref: SP2 §9]).

### 5.2 `cmd/serf-tui` (`embedded.go`)

Files touched: `cmd/serf-tui/embedded.go`.

Functions touched: the `SessionConfig` literal at line 126; the `agent.NewSession` call at line 174; the resume-path `agent.NewSession` at line 325.

Prompter selection: `TUIPrompter` ([ref: SP7 §4.2]).

`PermissionAskFallback`: `AskFallbackInteractive`.

`IsRemote`: false.

### 5.3 `cmd/serf-hub` (`web.go`)

Files touched: `cmd/serf-hub/web.go`.

Functions touched: `WebConfig` grows `ConfigPaths []string` and the routes that spawn or resume sessions thread the merged config into `SessionConfig`. The Hub does not call `agent.NewSession` directly today (it spawns serf processes); the wiring is in the Spawner's `SpawnRequest` formation. SP8 adds the new flags to that request.

Prompter selection: `HubPrompter` ([ref: SP7 §4.2]). The Hub's prompter is a web form bound to the plugin-enable endpoint and is not used in already-spawned sessions; it runs only when `serf plugin enable` is invoked from the Hub UI.

`PermissionAskFallback`: `AskFallbackInteractive` (Hub blocks tool calls on a permission API call [ref: SP2 §11.1]).

`IsRemote`: true.

### 5.4 `cmd/serfeval` (`main.go`)

Files touched: `cmd/serfeval/main.go`.

Functions touched: the `agent.SessionConfig` literal at line 206; `agent.NewSession` call at line 231.

Prompter selection: `NonInteractivePrompter`.

`PermissionAskFallback`: `AskFallbackDeny`.

`IsRemote`: false. Plugin install is out of band; serfeval just consumes the resolved set.

### 5.5 Shared helper

`agent.BuildSessionConfig(env ExecutionEnvironment, flags BootstrapFlags) (SessionConfig, error)` lives in a new file `agent/session_bootstrap.go`. `BootstrapFlags` carries `ConfigPaths []string`, `PluginDirs []string`, `TrustMarketplaces []string`, `PluginOptions map[string]map[string]string`, `Prompter UserConfigPrompter`, `AskFallback AskFallback`, `IsRemote bool`, `WatchConfig bool`. The function runs steps 1–4 of §2 and returns a `SessionConfig` whose §4 fields are populated.

Existing fields on `SessionConfig` (`MCPConfigFiles`, `MCPInline`, `PluginDirs`, etc.) are still set by each binary as before. `BuildSessionConfig` only owns the new fields.

## 6. New Lifecycle Event Fire Sites

SP5 owns the events; SP8 lists where they fire in serf source. Two of the nine (PermissionRequest, PermissionDenied) fire from SP2's enforcement layer, which is itself wired by SP8 into `Session.execTool`.

| SP5 event | File:function | When it fires | Input fields populated where |
| --- | --- | --- | --- |
| `PostToolUseFailure` | `agent/session.go:execTool` after the existing `PostToolUse` block, gated on `res.IsError` | After a tool call returns an error | `tool_name`, `tool_input`, `tool_error`, `tool_use_id` populated by `execTool` before the call to `RunPostToolUseFailure` |
| `PostToolBatch` | `agent/session.go` round-loop, after the `for i := range calls { results[i] = ... }` block, before `appendTurn(TurnToolResults,...)` (existing pattern near `session.go:2065-2095`) | Once per batch of parallel tool calls | `tool_results` populated from the `results []ToolExecResult` slice via a new `toBatchToolResults(results)` helper |
| `StopFailure` | `agent/session.go` in every error-return path of `processOneInput` after retry exhaustion | When the turn ends with an API error | `error_type` populated by a new `classifyAPIError(err) string` helper; `error_message` from `err.Error()` truncated to 4 KB |
| `SubagentStart` | `agent/subagents.go:spawnAgent` between `NewSession` returning and the `s.emit(EventSubagentStart, ...)` call, before `go sub.run(...)` | When a subagent is spawned | `agent_id` from `sub.id`; `agent_type` from `agentType` param or `"general-purpose"`; `prompt` from the `task` param |
| `UserPromptExpansion` | `agent/session.go:processOneInput` at the skill-resolution site (after `ActivatedSkillBodies` is populated, before the expanded text becomes `TurnUserInput`) | When a slash command or MCP prompt expands | `expansion_type`, `command_name`, `command_args`, `command_source`, `prompt` populated by the expansion helper |
| `PostCompact` | `agent/context_strategy.go:CompactStrategy.ManageContext` after `s.cm.MaybeCompact` returns and the strategy's new `Compacted` return is true | After context compaction completes | `compact_trigger = "auto"` (manual reserved) |
| `PermissionRequest` | `agent/permissions.go:Session.resolveAsk` ([ref: SP2 §6.3]), before the surface prompt | Before a permission dialog | `tool_name`, `tool_input`, `tool_use_id`, `permission_rule` from the matched `decision.Rule`, `permission_category` reserved |
| `PermissionDenied` | `agent/permissions.go:Session.permissionDeniedResult` ([ref: SP2 §6.3]), immediately before returning the deny result | After auto-mode denial | `tool_name`, `tool_input`, `tool_use_id`, `denial_reason` from `decision.Reason` |
| `ConfigChange` | `agent/config_watcher.go` ([ref: SP5 §3.9]) — new file behind `cfg.WatchConfig` | When a config file changes mid-session | `config_source` from the `ConfigTier` of the changed file ([ref: SP1 §2]); `config_file` from the watcher; `changed_keys` from a diff of pre/post `SerfConfig` top-level fields |

Each fire site reuses `s.hookInput(event)` to populate the common fields ([ref: SP5 §7]); SP5 extends `hookInput` to fill `transcript_path`, `permission_mode`, `effort`, `agent_id`, `agent_type`, `tool_use_id`.

## 7. Permission Enforcement Wire-In

SP2 §6.1 prescribes the placement; SP8 lands it. The enforcement block goes into `Session.execTool` at `agent/session.go:1275`, between the existing `PreToolUse` hook block (lines 1278–1300) and the `argsJSON, _ := json.Marshal(call.Arguments)` line (1303).

Order of evaluation inside `execTool`:

1. `RunPreToolUse` ([ref: existing], line 1284). If the hook denies, return the existing hook-deny result.
2. **SP2 enforcement.** `s.permissionMatcher.Evaluate(claudeName, toolInput)` ([ref: SP2 §6.1]).
   - `PermissionDeny` → `RunPermissionDenied` → `s.permissionDeniedResult(call, decision)` returns.
   - `PermissionAsk` → `s.resolveAsk(ctx, call, decision)` which fires `RunPermissionRequest` then consults `cfg.PermissionAskFallback` if the hook returns `defer` or no opinion. The resolved decision may still collapse to deny, in which case `permissionDeniedResult` returns.
   - `PermissionAllow` → fall through to step 3.
3. `s.reg.ExecuteCall` ([ref: existing]).
4. `RunPostToolUse` on success; **new** `RunPostToolUseFailure` on `res.IsError`.

The matcher reads `call.Arguments` *after* PreToolUse's `updatedInput` rewrite, so a hook rewriting `{"command": "rm /tmp"}` to `{"command": "rm /"}` is correctly denied by `Bash(rm /*)`. SP2 §6.1 names this contract; SP8 places the block to preserve it.

`Session.permissionMatcher` is constructed in `NewSession` from `cfg.Permissions` and `cfg.PermissionAskFallback`. Nil matcher means no rules were configured — `execTool` skips step 2 entirely.

## 8. User-Config Provider Wire-Up

SP7's `ResolvedUserConfig` is the per-plugin lookup. SP6 and SP5 consume it; SP8 routes it.

### 8.1 MCP expansion (SP6)

`initMCP` ([ref: existing, `session.go:2829`]) walks each MCP layer in precedence order. For plugin-provided layers, SP8 binds the per-plugin lookup:

```
for each loaded plugin p:
    resolved := s.pluginUserConfigs[p.ID]
    ctx := expansionContext{
        ProjectDir: resolveProjectDir(s.env),
        PluginRoot: p.Dir,
        PluginData: pluginDataDir(p.ID),
        UserConfig: func(k string) (string, bool) { return resolved.Lookup(k) },
    }
    cfgs := loadPluginMCPFile(p.MCPConfigPath, p.Dir, ctx)
```

For top-level (`mcp.json`, `--mcp-config`, `--mcp`) layers, `expansionContext.UserConfig` is `nil` ([ref: SP6 §5.4]); `${user_config.*}` references without a default error out.

### 8.2 Hook command expansion (SP5)

`HookRunner.Add` ([ref: existing]) records each hook's owning plugin via `RegisteredHook.PluginName` and `PluginDir`. SP5's hook executor calls `ExpandUserConfig(cmd, resolved)` before exec; SP8 wires the lookup map via:

```
runner.SetUserConfigResolver(func(pluginName string) *ResolvedUserConfig {
    return s.pluginUserConfigs[pluginName]
})
```

`UserConfigEnvVars(resolved)` ([ref: SP7 §7]) is merged into the spawn env for `command`, `agent`, and `mcp_tool` hook types ([ref: SP5 §4]).

### 8.3 Lookup map ownership

`s.pluginUserConfigs map[string]*ResolvedUserConfig` is a new field on `Session`. Populated in `initPlugins` after `LoadPlugin` returns, by calling `ResolveUserConfig(pluginID, lp.UserConfigOptions, cfg.PluginConfigStore, cfg.SecureStore)`. Missing required keys ([ref: SP7 §4.1]) are surfaced via the `UserConfigPrompter`; if the prompter is `NonInteractivePrompter` and a required key is missing, `initPlugins` returns the error, which aborts `NewSession`.

## 9. enabledPlugins Resolution

§3.6 names the contract; this section names the calling sequence.

```
for pluginAtMarket, value := range cfg.EnabledPlugins:
    plugin, marketplace, err := splitPluginSpec(pluginAtMarket)
    if err != nil: warn-and-skip
    entry, err := installer.Lookup(plugin, marketplace)
    if err == ErrNotInstalled: warn-and-skip
    if err != nil: return err
    resolved = append(resolved, ResolvedPlugin{
        PluginID:  pluginAtMarket,
        CachePath: entry.InstallPath,
        Version:   entry.Version,
        Source:    PluginSourceEnabled,
    })
```

`installer.Lookup` is the cross-scope reader: it reads `installed_plugins.json` and returns the scope's entry, preferring project over user when both exist ([ref: SP4 §2.2 — added as a thin wrapper for SP8's use]). SP4 already owns the registry IO; SP8 just exports the read.

After this loop runs, SP8 appends `cfg.PluginDirs` entries:

```
for _, dir := range cfg.PluginDirs:
    resolved = append(resolved, ResolvedPlugin{
        PluginID:  filepath.Base(dir),
        CachePath: dir,
        Version:   "ad-hoc",
        Source:    PluginSourceCLI,
    })
```

Collisions across the two slices are handled at load time per §13.

## 10. Error Handling at Startup

| Error class | Behavior |
| --- | --- |
| `DiscoverSerfConfig` returns error (malformed JSON, missing CLI file) | Fatal. Abort startup with the file path. SP1 §6 contract. |
| `NewPermissionMatcher` returns error (bad rule string) | Fatal. Abort startup. Error names the rule and the source file. SP2 §8.1. |
| `installer.Lookup` returns `ErrNotInstalled` for an `enabledPlugins` entry | Warning. Skip the plugin. Log `serf: plugin "<id>" is enabled but not installed; run 'serf plugin install <id>' to install`. Continue startup. |
| Marketplace declared by project but not trusted ([ref: SP3 §7]) | Warning. Skip the marketplace and any plugins from it. The plugin's `enabledPlugins` entry effectively skips too. |
| `LoadPlugin` fails for one cache path | Warning. Skip the plugin. Other plugins continue loading. |
| `ResolveUserConfig` reports missing required keys, interactive surface | Run the prompter. If the prompter cancels, skip the plugin with a warning. |
| `ResolveUserConfig` reports missing required keys, non-interactive surface | Fatal. Abort startup. Error names the plugin and the keys. |
| `NewSecureStore` returns error | Fatal only if any enabled plugin has `sensitive: true` userConfig keys. Else log once and continue with a no-op secure store. |
| MCP layer expansion fails (e.g., `${user_config.MISSING}` no default) | Fatal. The relevant `initMCP` step returns error ([ref: SP6 §8]). |
| MCP server connection times out at `initMCP` | Warning (existing behavior). The session continues without that server. |
| Hook execution fails at runtime | Per-hook semantics ([ref: SP5 §3]). Not a startup concern. |
| `config_watcher.go` fails to start | Warning. Session runs without live reload. Not fatal. |

The pattern: **config parse is fatal; missing resource is a warning; runtime is deferred to the runtime owner.**

## 11. Package and File Layout

No new packages. Files touched:

| File | Change |
| --- | --- |
| `agent/session_bootstrap.go` (new) | `BuildSessionConfig`, `BootstrapFlags`, the plumbing helpers `splitPluginSpec`, `installerLookup`. |
| `agent/session.go` | Add fields per §4; in `NewSession` build the `PermissionMatcher`; in `initPlugins` resolve `EnabledPluginPaths` to `LoadPlugin` calls; populate `s.pluginUserConfigs`; place §6 fire sites for `PostToolUseFailure`, `PostToolBatch`, `StopFailure`, `UserPromptExpansion`; insert §7 enforcement block in `execTool`. |
| `agent/subagents.go` | Place `SubagentStart` fire site (§6) and copy parent session's resolved plugins, `MergedConfig`, `Permissions`. |
| `agent/context_strategy.go` | Place `PostCompact` fire site (§6). |
| `agent/permissions.go` (SP2 file) | SP2 owns. SP8 adds nothing. |
| `agent/config_watcher.go` (new, owned by SP5) | SP5 owns. SP8 references it. |
| `cmd/serf/main.go` | Register `--config`, `--trust-marketplace`, `--plugin-option`. |
| `cmd/serf/run.go` | Call `BuildSessionConfig`; copy fields. |
| `cmd/serf/serve.go` | Same. |
| `cmd/serf-tui/embedded.go` | Same. |
| `cmd/serf-hub/web.go` | Extend `WebConfig`; thread into Spawner. |
| `cmd/serfeval/main.go` | Same. |

No file is moved or renamed. Every existing test path is preserved. The `--plugin-dir` codepath continues to work via the `PluginDirs` → `EnabledPluginPaths` fallback in §4.

## 12. End-to-End Test Plan

The integration test suite lives in `agent/integration_sp8_test.go` (new file). Each test uses real fixtures from SP3, SP4, SP5, SP7 — no mocks beyond the existing `llm` stub. Tests are TDD: each case lands as a failing test before any SP8 wiring lands.

### 12.1 Test cases

1. **TestSP8_MarketplaceAddInstallRunUninstall.** Adds a directory-source marketplace ([ref: SP3 §5.1]), installs a plugin from it ([ref: SP4 §4]), starts a session, fires a tool call that triggers a `PreToolUse` hook from the plugin, asserts the hook ran (sentinel file written), then uninstalls and re-starts a session and asserts the hook does *not* fire. Uses `agent/testdata/marketplaces/sp8-basic/` fixture.

2. **TestSP8_HookEventFires_NewEvents.** For each of the seven new events SP8 fires (`PostToolUseFailure`, `PostToolBatch`, `StopFailure`, `SubagentStart`, `UserPromptExpansion`, `PostCompact`, `ConfigChange`), uses a fixture plugin that declares a `command` hook for the event, drives a session through the trigger condition, and asserts the hook ran by reading a sentinel file. Each event is one subtest.

3. **TestSP8_PermissionsEnforcedFromConfig.** Writes `~/.config/serf/config.json` with `permissions.deny: ["Bash(rm:*)"]`, starts a session, model emits `Bash{command: "rm /tmp/x"}`, asserts the call is denied without executing. Verifies `PermissionDenied` hook fires.

4. **TestSP8_PermissionsAskFallback_NonInteractive.** Same as #3 but with no rule and `defaultMode: "default"`, on a `serf -p` surface. Asserts the call is denied (AskFallbackDeny) and `PermissionRequest` hook fires.

5. **TestSP8_HookUnion_ConfigAndPlugin.** Both `config.json` and a plugin declare a `PreToolUse` hook. Both run. Asserts firing order: config first, then plugin.

6. **TestSP8_MCPUnion_ConfigAndPlugin.** Plugin and `config.json` both declare an `mcpServers.foo`. Plugin wins (highest precedence per §3.2). Asserts the resulting server's `command` is the plugin's value.

7. **TestSP8_UserConfigExpansion_MCP.** Plugin declares a `userConfig` key and an MCP server with `${user_config.KEY}` in its URL. Test pre-populates the value via `PluginConfigStore`, starts a session, asserts the MCP server connects to the expanded URL.

8. **TestSP8_UserConfigExpansion_Hook.** Same as #7 but the substitution is in a hook command. Hook fires; sentinel file content is the expanded value.

9. **TestSP8_CLAUDE_PROJECT_DIR_Injected.** A plugin's stdio MCP server reads `CLAUDE_PROJECT_DIR` from its env at startup and writes it to a sentinel. The sentinel matches the git root of the test directory.

10. **TestSP8_BinDirOnBashPath.** Plugin ships `bin/my-tool` (`#!/bin/sh; echo hi`). Session executes `shell` tool with `command: "my-tool"`. Asserts stdout contains `hi`. Asserts the tool is *not* on PATH for other tools (e.g., `read_file`'s grep subprocess).

11. **TestSP8_PluginDirShadowsEnabledPlugin.** `enabledPlugins` references `foo@market`; `--plugin-dir <path>` provides a plugin also named `foo`. Asserts the `--plugin-dir` version wins ([ref: §13]).

12. **TestSP8_PluginMissingWarnsNotFatal.** `enabledPlugins` references `not-installed@x`. Startup succeeds with a warning logged; other plugins still load.

13. **TestSP8_MalformedConfigFatal.** Project `.serf/config.json` has invalid JSON. `NewSession` returns an error naming the file.

14. **TestSP8_TrustPrompt_ProjectMarketplace.** `.serf/config.json` declares a marketplace; test injects a `TrustPrompter` that records the call and returns "always". After session, `trusted_projects.json` has the entry.

15. **TestSP8_SubagentInheritsConfig.** Parent session has `permissions.deny: ["Bash(rm:*)"]`. Subagent attempts `Bash{rm}`. Subagent's tool call is denied.

16. **TestSP8_BackwardCompat_PluginDirOnly.** Session created with only `cfg.PluginDirs` set (no `EnabledPluginPaths`, no `MergedConfig`). Behaves exactly like the current code path: plugin loads, hooks fire, tests at `agent/plugin_e2e_test.go` continue to pass without modification.

17. **TestSP8_BackwardCompat_McpJsonOnly.** Session with only `.serf/mcp.json` (no `config.json`). MCP server loads via the existing path. Asserts no regression.

18. **TestSP8_AdditionalContext_Plumbed.** PostToolUse hook returns `hookSpecificOutput.additionalContext`. Next LLM round's history contains the steering turn. Verifies SP5's plumbing path through SP8's wiring.

19. **TestSP8_ConfigChange_Reload.** With `WatchConfig: true`, mutate `.serf/config.json` mid-session. Asserts `ConfigChange` hook fires and `s.cfg.MergedConfig.Sources` reflects the new file.

### 12.2 Fixture reuse

- Marketplaces: `internal/plugins/testdata/marketplaces/valid_full/` (SP3) supplies the marketplace shape.
- Plugins: `agent/testdata/plugins/sp8-hookparity/` (new) bundles one plugin that exercises every new event. Each event has a `command` hook that writes a sentinel file.
- MCP fixtures: `agent/testdata/mcp/sp6/` (SP6).
- UserConfig fixtures: `agent/testdata/plugins/userconfig-basic/` (SP7).

### 12.3 Conventions

- `t.TempDir()` and `t.Setenv` for every config and registry path.
- `git init` in each test that needs a project tier; `t.Skip` if `git` is absent.
- Real `bash` for `command`-type hooks; `t.Skip` if `bash` is absent.
- `httptest.NewServer` for `http` hooks ([ref: SP5 §13.2]).
- `llm` stub for the existing test conventions.
- Each test asserts on observable state — sentinel files, registry contents, session events — not on internal implementation.

## 13. Backward Compatibility

Every existing flag, file, and behavior continues to work.

- `--plugin-dir <path>` (existing) populates `SessionConfig.PluginDirs` ([ref: existing]). When `EnabledPluginPaths` is empty, SP8 maps `PluginDirs` onto it transparently. Existing `agent/plugin_e2e_test.go` continues to pass without modification.
- `--mcp-config <path>` and `--mcp <spec>` (existing) populate `SessionConfig.MCPConfigFiles` and `MCPInline`. SP8 does not touch the existing MCP discovery; it only adds two new layers (global+project `config.json` mcpServers, and plugin-provided) on either side.
- `~/.config/serf/mcp.json` and `.serf/mcp.json` (existing) continue to load via the existing `DiscoverMCPConfigs` ([ref: existing `agent/mcp_config.go`]). SP8 does not consolidate them — they remain separate input slots.
- A session built without any SP8-new fields populated (i.e., `Permissions`, `EnabledPluginPaths`, etc. are zero) behaves exactly like the current pre-SP8 session. Tests pinning this invariant are in §12.16 and §12.17.

**Name collision rule.** When `enabledPlugins` resolves a plugin `foo@market` to a cache directory, and `--plugin-dir <path>` loads a plugin whose `plugin.json.name == "foo"`, the `--plugin-dir` plugin wins for that session. Rationale: matches Claude Code's documented behavior at [code.claude.com/docs/en/plugins](https://code.claude.com/docs/en/plugins) for the `--plugin-dir` flag, which is the dev-loop equivalent of `claude --plugin <dir>`. SP8 enforces this in `initPlugins` by walking `EnabledPluginPaths` first, recording each `pluginID`, then walking `--plugin-dir` entries and *replacing* any prior entry with the same name. A warning logs the override: `serf: --plugin-dir <path> overrides enabledPlugins entry "foo@market" for this session`.

No deprecation warnings are emitted for any existing flag.

## 14. Open Questions

### 14.1 Settled here

**Missing plugin in enabledPlugins.** Warn at startup and skip the plugin. Other plugins continue to load. Documented in §10. Rationale: a typo or stale lockfile should not block the entire session; the user sees the warning and runs `serf plugin install` to fix it.

**Hook union ordering.** Both fire. Config-tier first (global → project → CLI in SP1's order), plugin-provided last. Settled by SP1 §9.1 and re-affirmed by §3.1.

**--plugin-dir vs enabledPlugins same name.** `--plugin-dir` wins for that session (dev mode). Documented in §13. Matches Claude Code's `--plugin` flag behavior at https://code.claude.com/docs/en/plugins.

### 14.2 Genuinely open at SP8 close

- **Mid-session permission re-build.** `ConfigChange` fires when `config.json` changes mid-session ([ref: SP5 §3.9]). SP8 does not rebuild `PermissionMatcher` mid-session — the matcher is captured at `NewSession`. A future SP can wire `ConfigChange` into a matcher rebuild; the integration point is `s.permissionMatcher = NewPermissionMatcher(newCfg.Permissions, env)` invoked from the watcher's callback.
- **Subagent re-prompt for new userConfig keys.** When a parent session's plugin updates and a new required `userConfig` key appears, the parent prompts; subagents inherit the resolved values. If a subagent spawns after the update but before the parent prompts (unlikely with current locking but possible), the subagent inherits a missing-required state. SP8 ships the parent-side resolution; subagent re-prompt is a follow-up.
- **Persistence of `addPermissionRule` across sessions.** SP5 §3.7 extends the live session's allow set but does not persist. SP8 inherits that contract; persistence is a future SP after SP2 settles its rule-mutation API.
- **`serf plugin enable` mid-session reload.** Enabling a plugin via `serf plugin enable` from a separate shell does not affect the running session. The user must restart. `WatchConfig: true` makes the change visible via `ConfigChange` events but does not load the new plugin's components — that requires session restart. Hot plugin load is deferred.
