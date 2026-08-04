# Serf-Wide Slash Commands — Design

Date: 2026-08-04
Status: Approved design, pre-plan

## Summary

Serf loads slash commands only from plugins (`agent/plugin/commands.go`): each
plugin's `commands/*.md` files become commands namespaced as `plugin:name`.
This design adds **serf-wide commands** — markdown command files that live
outside any plugin, discovered from the project and the user config dir, and
invoked by bare name. Plugin commands keep their `plugin:name` namespacing;
serf-wide commands have none.

## Goals

- Discover commands from `<dir>/.serf/commands/*.md` walking git-root→cwd, and
  from the global config dir (`$XDG_CONFIG_HOME/serf/commands/` or
  `~/.config/serf/commands/`).
- Precedence: **project > user-global > plugin**. A serf-wide command shadows
  a plugin command of the same bare name; the plugin command stays reachable
  as `/plugin:name`.
- Identical file format, expansion semantics, and unenforced-field warnings as
  plugin commands.
- Invocation parity across clients (TUI, web, headless) — automatic, because
  expansion is server-side (`agent/session_slash_command.go`).
- `serf/command/list` entries gain a `source` label (`plugin` / `project` /
  `user`).

## Non-goals (YAGNI)

- Enforcing `model` / `allowed-tools` frontmatter. Both stay parsed-but-
  unenforced, warned about, deferred to design §14 of the plugin-marketplaces
  spec — for plugin and serf-wide commands alike.
- A `commands_dirs` launch-config field for arbitrary extra directories. The
  two auto-discovered locations cover the ask; the field is easy to add later
  by analogy with `skills_dirs`.
- Per-command enable/disable, command aliases, or a first-class command
  registry type. The existing `map[string]plugin.Command` plus bare-key merge
  delivers the required precedence with no refactor.
- Project commands in the hub-wide `serf/command/list` catalog (see
  §Catalog boundary).

## Current state

- `plugin.Command` (`agent/plugin/commands.go`) carries Name, Description,
  ArgumentHint, Model, AllowedTools, Body, PluginName. `ParseCommand` accepts
  bare markdown (no frontmatter required); the filename is the authoritative
  name.
- Plugin commands are keyed `plugin:name` in `Session.pluginCommands`
  (`agent/session_init.go` merges each plugin's `Commands`).
- `plugin.ResolveCommand` tries an exact key match, then falls back to
  matching the unqualified suffix of each key.
- `Session.expandSlashCommand` resolves `/name args` against
  `pluginCommands` and expands the body via `command.Expand` (`$ARGUMENTS`,
  `$1..$9`, backtick substitution, `@file` inclusion). Unknown `/words` pass
  through as ordinary text.
- `skill.DiscoverSkills` (`agent/skill/skills.go`) is the discovery analog:
  walk git-root→cwd scanning `skills/` dirs, then extra dirs; later entries
  shadow earlier ones by name; cwd is symlink-resolved.
- `agent/internal/promptpath` is the config-dir analog: `GlobalPromptsDir`
  resolves `$XDG_CONFIG_HOME/serf/prompts` or `~/.config/serf/prompts` via
  `envvars.XDGConfigHome` with home lookup injected at the boundary.
- `hubCommandList` (`cmd/serf-hub/app_rpc.go`) answers `serf/command/list`
  by loading `plugins.Manager.EnabledPluginDirs(cfg.PluginDirs)` through
  `plugin.LoadAllFailSoft` and flattening into
  `appwire.CommandDescriptor{Name, PluginName, Description, ArgumentHint}`.
- The agent module must not import `primeradiant.com/serf/internal/plugins`
  (root-module internal). It already imports `primeradiant.com/serf/envvars`,
  which is the sanctioned XDG seam inside the agent module.

## Design

### Discovery

New file `agent/plugin/serfwide.go`:

```go
// DiscoverSerfWideCommands scans the user-global commands dir, then walks
// git-root→cwd scanning <dir>/.serf/commands, returning commands keyed by
// bare name. Later scans shadow earlier ones, so the deepest project dir
// wins and every project command shadows the user-global one.
func DiscoverSerfWideCommands(env execenv.ExecutionEnvironment) (map[string]Command, []events.WarningData)
```

- User-global dir: a `globalCommandsDir(xdgConfigHome string, userHomeDir
  func() (string, error))` helper mirroring `promptpath.globalPromptsDir`,
  returning `<config>/serf/commands`. Environment and home lookup stay at the
  boundary so path construction is deterministic in tests.
- Project walk: reuse the skill discovery shape — resolve symlinks on cwd,
  find git root via `execenv.GitRootOrEmpty`, iterate
  `execenv.DirsFromRootToCwd`, scan `<dir>/.serf/commands` in each. A nil env
  or empty cwd skips the project walk but still scans the user-global dir
  (the hub catalog relies on this; see §Catalog boundary).
- Each scan reads immediate `.md` files only (same semantics as plugin
  command discovery) and parses with the existing `ParseCommand`.
- Two filename rejections, each with a warning, never fatal:
  - a basename containing `:` — a bare key with a colon could forge the
    `plugin:name` namespace and shadow a plugin's exact key;
  - malformed frontmatter — `ParseCommand`'s only error path; skip the file,
    warn, continue.
- Shadowing between serf-wide sources is silent and deterministic, matching
  skills.

The function returns warnings as data rather than emitting them, so both
callers (session init, which emits `EventWarning`; the hub, which drops
them) stay consistent with how plugin load failures are handled today.

### Command model

`plugin.Command` gains:

```go
Source string // "plugin", "project", or "user"
```

`discoverPluginCommands` sets `Source: "plugin"`. Serf-wide discovery sets
`"project"` or `"user"` per origin dir. `PluginName` stays empty for
serf-wide commands. No other consumer of `Command` changes.

### Merge and precedence

Session init (`initPlugins`) merges after the plugin loop:

```go
serfwide, warnings := plugin.DiscoverSerfWideCommands(s.currentEnv())
maps.Copy(s.pluginCommands, serfwide) // bare-name keys
s.pendingHookWarnings-style queue += warnings
```

Precedence needs no resolution changes. `ResolveCommand` exact-matches first:
a bare serf-wide key `review` wins over every `plugin:review` suffix-fallback
candidate. `/plugin:review` still exact-matches the plugin's key. Serf-wide
keys never collide with plugin keys because plugin keys always contain `:`
and serf-wide keys never do (enforced at discovery).

Expansion is untouched: `expandSlashCommand` already resolves through
`pluginCommands`, so every client (TUI, web, headless `serf run`) inherits
serf-wide invocation with no client work.

### Shared loader

Today session init and `hubCommandList` each assemble the command set on
their own. Extract one path in `agent/plugin`:

```go
// LoadAllCommands returns the full command map a session in env's directory
// would see: plugin commands (namespaced) plus serf-wide commands (bare).
func LoadAllCommands(dirs []string, env execenv.ExecutionEnvironment) (map[string]Command, []events.WarningData)
```

Session init and the hub both call it. (`agent/plugin` already depends on
neither `execenv` nor `events`; both are leaf-ward of it — verify no import
cycle at implementation time; if one exists, the loader moves to the `agent`
package and takes `plugin` pieces as parameters.)

### Unenforced-field warnings

`commandUnenforcedFieldWarnings` (`agent/session_init.go`) warns when a
plugin command declares `model` or `allowed-tools`. Extend the same warning
to serf-wide commands, labeled by source and file, so a user who writes
`allowed-tools` in `~/.config/serf/commands/review.md` learns it is not
enforced.

### Catalog

`appwire.CommandDescriptor` gains:

```go
Source string `json:"source,omitempty"` // "plugin" | "project" | "user"
```

`appwire/protocol.go`'s `serf/command/list` description and
`docs/appwire-protocol.md` are updated to match. The web catalog badges or
groups commands by source; TUI autocomplete may show it. Frontend changes
follow the AGENTS.md gates (`npx biome check --write`, `make test-web`).

### Catalog boundary

The hub is multi-project: `WebConfig` has no single working directory, and
`serf/command/list` takes `EmptyParams`. The hub-wide catalog therefore
reports **plugin and user-global commands only**; project commands are
cwd-dependent and appear in the catalog of whatever session-scoped surface
exists later. This is a deliberate boundary, not an oversight: invocation
parity is unaffected (expansion is session-side), and a project-scoped
catalog param is a clean future extension.

### Lifecycle and persistence

Serf-wide commands are discovered live at session init, exactly like skills.
Nothing is added to `SessionConfig`, the snapshot schema, or launch config.
Resumed and forked sessions re-discover from the current filesystem.

## Error handling

Fail-soft everywhere; loud warnings; never block spawn.

| Condition | Behavior |
|---|---|
| Commands dir absent | Silent (default state on most machines) |
| Commands dir present but unreadable | Warning, continue with other dirs |
| Malformed frontmatter in a `.md` | Skip file, warning naming file and error, continue |
| Filename containing `:` | Skip file, warning (namespace forgery guard) |
| Serf-wide vs serf-wide name collision | Silent deterministic shadowing (deepest project dir > user-global) |
| Serf-wide shadows plugin command | Silent; plugin reachable via `/plugin:name`; catalog lists both with distinct sources |
| Expansion failure | Unchanged: warning + literal-text fallback in `expandSlashCommand` |
| `model`/`allowed-tools` declared | Unenforced-field warning, as plugin commands today |

## Testing

Per `docs/testing.md`: deterministic, scripted provider at the LLM boundary,
no live requests.

- **Unit — discovery**: table tests over fixture trees: user-global only;
  project walk only; ordering (user-global scanned first, deepest project dir
  shadows); symlinked cwd; non-`.md` files ignored; subdirectories not
  descended; malformed frontmatter skipped with warning; colon filenames
  skipped with warning; nil env scans user-global only.
- **Unit — precedence**: `ResolveCommand` exact-matches a bare serf-wide key
  before plugin suffix fallback; `/plugin:name` still resolves explicitly;
  two plugins sharing a command name keep first-found fallback when no
  serf-wide command shadows.
- **Session (scripted provider)**: `/review args` from project and user
  sources expands through `command.Expand` including `$ARGUMENTS` and
  `@file`; a shadowed plugin command expands only when invoked as
  `/plugin:name`; a malformed command file yields the warning event and the
  session spawns normally; unenforced-frontmatter warnings fire for
  serf-wide commands.
- **Hub**: `serf/command/list` returns plugin and user-global entries with
  correct `source` values; shared-loader parity — the catalog for a nil-env
  hub matches what session init loads from the same plugin dirs plus the
  user-global dir.
- **Fuzz**: discovery over fuzzed directory trees and frontmatter, following
  `agent/plugin/loader_program_fuzz_test.go` patterns; register targets per
  the fuzz registry (`make fuzz-registry-check`).
- **Frontend**: source badge/grouping in the command catalog; Biome +
  `make test-web` gates.

## File-by-file change list

| File | Change |
|---|---|
| `agent/plugin/commands.go` | Add `Source` to `Command`; set `"plugin"` in `discoverPluginCommands` |
| `agent/plugin/serfwide.go` | New: `DiscoverSerfWideCommands`, dir-scan helper, `globalCommandsDir`, filename guards |
| `agent/plugin/plugin.go` (home of `LoadAllFailSoft`; or the `agent` package) | New: `LoadAllCommands` shared loader |
| `agent/session_init.go` | Merge serf-wide commands after plugin loop; extend unenforced-field warnings |
| `appwire/types.go` | `CommandDescriptor.Source` |
| `appwire/protocol.go` | `serf/command/list` description mentions source |
| `cmd/serf-hub/app_rpc.go` | `hubCommandList` uses the shared loader (nil env) |
| web catalog (`cmd/serf-hub` web assets) | Badge/group commands by source |
| `docs/appwire-protocol.md` | Document the `source` field |
| `docs/commands.md` (new user doc; no command doc exists today) | Document serf-wide command locations, precedence, format |

## Open questions

None. Precedence (project > user > plugin), the catalog boundary, and
deferred frontmatter enforcement were decided during design review.
