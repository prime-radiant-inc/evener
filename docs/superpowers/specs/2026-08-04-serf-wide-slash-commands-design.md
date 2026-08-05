# Serf-Wide Slash Commands — Design

Date: 2026-08-04 (revised after adversarial review, same day)
Status: Approved design, pre-plan

## Summary

Serf loads slash commands only from plugins (`agent/plugin/commands.go`):
each plugin's `commands/*.md` files become commands namespaced as
`plugin:name`. This design adds **serf-wide commands** — markdown command
files that live outside any plugin, discovered from the project and the user
config dir, and invoked by bare name. Plugin commands keep their `plugin:name`
namespacing and their full Claude Code-compatible expansion; serf-wide
commands have no namespace and expand **inert** (codex posture, §Threat
model).

## Goals

- Discover commands from `<dir>/.serf/commands/*.md` walking git-root→cwd,
  and from the global config dir (`$XDG_CONFIG_HOME/serf/commands/` or
  `~/.config/serf/commands/`).
- Precedence: **project > user-global > plugin**. A serf-wide command
  shadows a plugin command of the same bare name; the plugin command stays
  reachable as `/plugin:name`.
- Same file format as plugin commands (bare markdown, optional frontmatter,
  filename is the name).
- Serf-wide commands never execute shell commands or read files at expansion
  time. Arguments substitute as inert text; `!`cmd`` spans and `@file`
  directives in a serf-wide body stay literal.
- `serf/command/list` entries gain a `source` label (`plugin` or `user`
  in the initial implementation; `project` is reserved for a future
  project-scoped catalog — see §Catalog boundary).
- Web invocation parity: the web palette lists plugin and user-global
  commands and forwards unmatched slash input to the session (§Web
  invocation).

## Non-goals (YAGNI)

- Enforcing `model` / `allowed-tools` frontmatter. Both stay parsed-but-
  unenforced, warned about, deferred to design §14 of the plugin-marketplaces
  spec — for plugin and serf-wide commands alike.
- Changing plugin command expansion. Plugin commands keep `!`cmd`` execution
  and `@file` inclusion: they are explicitly installed by the user and exist
  for Claude Code compatibility.
- A `commands_dirs` launch-config field for arbitrary extra directories. The
  two auto-discovered locations cover the ask; the field is easy to add later
  by analogy with `skills_dirs`.
- Per-command enable/disable, command aliases, or a first-class command
  registry type.
- Project commands in the hub-wide `serf/command/list` catalog (see
  §Catalog boundary).
- A client built-in collision guard. TUI clients intercept their own
  built-in slash names before input reaches the session (§Web invocation);
  this design documents the behavior rather than reserving names.

## Current state

- `plugin.Command` (`agent/plugin/commands.go`) carries Name, Description,
  ArgumentHint, Model, AllowedTools, Body, PluginName. `ParseCommand` accepts
  bare markdown (no frontmatter required); the filename is the authoritative
  name.
- Plugin commands are keyed `plugin:name` in `Session.pluginCommands`
  (`agent/session_init.go:1139` merges each plugin's `Commands`).
- `plugin.ResolveCommand` tries an exact key match, then falls back to
  matching the unqualified suffix of each key (commands.go:121-131); a bare
  name matching two plugins resolves nondeterministically by map iteration
  order (documented at commands.go:118-120).
- `Session.expandSlashCommand` resolves `/name args` against
  `pluginCommands` and expands via `command.Expand`: `$ARGUMENTS`, `$1..$9`,
  **`!`cmd`` executes via `env.ExecCommand`** (agent/command/expand.go:86-91,
  160-163), and **`@file` inlines file contents** (expand.go:175-188;
  `filepath.IsLocal`-constrained but symlink-following). Unknown `/words`
  pass through as ordinary text.
- `command.Expand`'s safety invariant (expand.go:6-12): only directives in
  the template execute; argument text can never open a directive.
- `skill.DiscoverSkills` (`agent/skill/skills.go:26-55`) is the discovery
  analog: walk git-root→cwd scanning `skills/` dirs, then extra dirs; later
  entries shadow earlier; cwd is symlink-resolved. Skill bodies are loaded as
  pure text — no expansion of any kind.
- `agent/internal/promptpath` is the config-dir analog:
  `globalPromptsDir` resolves `$XDG_CONFIG_HOME/serf/prompts` or
  `~/.config/serf/prompts` via `envvars.XDGConfigHome`, with environment and
  home lookup injected at the boundary for deterministic tests.
- `initPlugins` (`agent/session_init.go:1121`) **early-returns when
  `cfg.PluginDirs` is empty** (line 1122) — serf-wide discovery must not
  live behind that gate.
- `hubCommandList` (`cmd/serf-hub/app_rpc.go:815-839`) answers
  `serf/command/list` via `plugins.Manager.EnabledPluginDirs` +
  `plugin.LoadAllFailSoft`, flattened into
  `appwire.CommandDescriptor{Name, PluginName, Description, ArgumentHint}`;
  it **early-returns empty when there are no plugin dirs** (lines 817-819).
- The agent module must not import `primeradiant.com/serf/internal/plugins`
  (root-module internal). It already imports
  `primeradiant.com/serf/envvars`, the sanctioned XDG seam. `agent/plugin`
  imports neither `execenv` nor `events` today; neither imports `plugin`, so
  the additions below create no import cycle.
- Client interception: the TUI resolves typed `/name` against ~27 built-in
  commands before forwarding to the session (cmd/serf-tui/
  hub_session_keys.go:439-468; registry in hub_command_registry.go). The web
  composer routes a leading `/` in an empty composer to its local command
  palette (cmd/serf-hub/frontend/src/panes/session/composer/
  Composer.tsx:679-682).

## Threat model

Today every executable command template comes from a plugin dir the user
explicitly configured or installed. This feature auto-discovers templates
from the worked-on repo (`.serf/commands/`), which is attacker-controlled in
any cloned repository. Combined with `command.Expand`'s `!`cmd`` execution,
a user typing `/setup` in a malicious repo would run repo-supplied shell
with no confirmation. Skills — the discovery analog — have no such exposure
because skill bodies never expand.

Reference postures:

- **Claude Code** executes `!`cmd`` in skills/commands, but gates
  project-sourced content behind a workspace trust dialog and offers a
  `disableSkillShellExecution` settings kill switch.
- **Codex** never executes: skills and custom prompts are inert text; skill
  bodies reach the model only through the model's own tool calls, inheriting
  normal permission and sandbox checks (`inspo/codex/codex-rs/core-skills`,
  `ext/skills/src/render.rs`).

**Decision: codex's posture.** Serf-wide commands expand inert regardless of
source (project or user-global): `$ARGUMENTS`/`$1..$9` substitute as inert
text; `!`cmd`` spans and `@file` directives remain literal text in the
expanded body. A user who wants executable templates already has the plugin
path, which is explicit and Claude Code-compatible. This also matches serf
skills, which are inert today. Rationale and cross-tool comparison are
documented for users in `docs/skills.md`.

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
  returning `<config>/serf/commands`.
- Project walk: reuse the skill discovery shape — resolve symlinks on cwd,
  find git root via `execenv.GitRootOrEmpty`, iterate
  `execenv.DirsFromRootToCwd`, scan `<dir>/.serf/commands` in each. A nil env
  or empty cwd skips the project walk but still scans the user-global dir
  (the hub catalog relies on this; see §Catalog boundary).
- Each scan reads immediate `.md` files only (same semantics as plugin
  command discovery) and parses with the existing `ParseCommand`.
- Per-file rejections, each producing a warning, never fatal:
  - a basename containing `:` — a bare key with a colon could forge the
    `plugin:name` namespace and shadow a plugin's exact key;
  - a basename containing whitespace — name/args parsing splits at the
    first space (session_slash_command.go:26), so a space-containing name
    can never be invoked; other whitespace is rejected as defense-in-depth.
    Skip the file and warn (the same defect exists for plugin commands
    today; this guard covers the new discovery path only);
  - an empty basename (a file named exactly `.md`) — the name would be the
    empty string, which `expandSlashCommand` never resolves
    (session_slash_command.go:27-29). Skip the file and warn;
  - malformed frontmatter — `ParseCommand`'s only error path; skip the
    file, warn, continue.
- Per-file advisory warning: a serf-wide body containing a `!`` span is
  flagged once at discovery ("execution directives are inert in serf-wide
  commands; use a plugin command for executable templates"). `@file` text is
  not scanned — `@` appears too often in ordinary prose (emails, mentions)
  for a useful signal.
- Shadowing between serf-wide sources is silent and deterministic, matching
  skills.

The function returns warnings as data rather than emitting them, so both
callers (session init, which emits `EventWarning`; the hub, which drops
them) stay consistent with how plugin load failures are handled today.

### Command model

`plugin.Command` gains:

```go
Source string // "plugin", "project", or "user"
File   string // absolute path of the defining .md
```

`discoverPluginCommands` sets `Source: "plugin"` and the file path.
Serf-wide discovery sets `"project"` or `"user"` and the path. `PluginName`
stays empty for serf-wide commands. The `File` field exists so warnings can
name the offending file, which the review found undeliverable otherwise.

### Expansion

New function in `agent/command`:

```go
// ExpandArgs substitutes $ARGUMENTS and $1..$9 only. !`cmd` spans and
// @file directives remain literal text; nothing executes or reads.
func ExpandArgs(body, args string) string
```

It reuses the existing `substituteArguments` over the whole body — argument
substitution is already inert by the package safety invariant, so no
directive scanning is needed at all.

`Session.expandSlashCommand` branches on the resolved command's `Source`:
`"plugin"` commands expand via `command.Expand` (unchanged); serf-wide
commands expand via `command.ExpandArgs`.

### Merge and precedence

Serf-wide discovery must run for every session, including sessions with no
plugin dirs, so it does **not** live in `initPlugins` (which early-returns
on empty `PluginDirs`). Session init discovers immediately after the
`initPlugins` call (which is also where every plugin instance is loaded,
empty set included), then assembles the whole command map in one place:

```go
serfwide, warnings := plugin.DiscoverSerfWideCommands(s.currentEnv())
s.pluginCommands = plugin.MergeCommands(s.plugins, serfwide)
// warnings join the session-start warning queue
```

`initPlugins` stops merging `p.Commands` per plugin (today at
session_init.go:1139); `MergeCommands` becomes the single assembly point.

Precedence needs no resolution changes. `ResolveCommand` exact-matches
first: a bare serf-wide key `review` wins over every `plugin:review`
suffix-fallback candidate. `/plugin:review` still exact-matches the plugin's
key. Serf-wide keys never collide with plugin keys because plugin keys
always contain `:` and serf-wide keys never do (enforced at discovery).

Expansion is server-side (`EntryUserInput`, session_lifecycle.go:925-933),
so any client that forwards `/name args` as input gets serf-wide invocation
with no client work. See §Client parity for the cases where clients don't
forward.

### Shared loader

Session init and `hubCommandList` must agree on the command set, but the
session already holds loaded `plugin.Instance`s — a shared loader taking
dirs would double-load. Share the flatten-and-merge, not the load:

```go
// MergeCommands flattens plugin instances' commands (namespaced keys) and
// overlays serf-wide commands (bare keys), returning the unified map.
func MergeCommands(instances []Instance, serfwide map[string]Command) map[string]Command
```

Session init calls it with its already-loaded instances; `hubCommandList`
calls `plugin.LoadAllFailSoft` first, then `MergeCommands`, then flattens
for the wire. `hubCommandList`'s `len(dirs) == 0` early return is removed so
user-global commands appear in the catalog even with zero plugins.

### Unenforced-field warnings

`commandUnenforcedFieldWarnings` (`agent/session_init.go:1624`) takes a
`plugin.Instance` and interpolates its manifest name, so it cannot cover
serf-wide commands. Serf-wide `model`/`allowed-tools` warnings are generated
inside discovery instead — where the file path is known — and returned in
the discovery warnings slice, labeled by source and file.

### Catalog

`appwire.CommandDescriptor` gains:

```go
Source string `json:"source,omitempty"` // "plugin" or "user"; "project" reserved for a future project-scoped catalog
```

`appwire/protocol.go`'s `serf/command/list` description and
`docs/appwire-protocol.md` are updated to match. The web catalog badges or
groups commands by source. Frontend changes follow the AGENTS.md gates
(`npx biome check --write`, `make test-web`).

### Catalog boundary

The hub is multi-project: `WebConfig` has no single working directory, and
`serf/command/list` takes `EmptyParams`. The hub-wide catalog therefore
reports **plugin and user-global commands only**; project commands are
cwd-dependent and resolve per session. Invocation is unaffected (expansion
is session-side). A project-scoped catalog param is a clean future
extension.

Consequence for the error table below: when a **user-global** command
shadows a plugin command, the catalog lists both with distinct sources; when
a **project** command shadows one, the catalog lists only the plugin entry.

### Web invocation (closing the parity gap)

Today serf-wide and plugin commands are **unreachable from the web
composer by typing**: a leading `/` in an empty composer opens the local
palette (Composer.tsx:679-682), the palette lists only built-in UI commands
(no frontend consumer of `serf/command/list` exists outside generated
types), and Enter on an unmatched query does nothing
(CommandPalette.tsx:409-411 — no fallthrough). The only escapes are
accidental: the gate fires only on an *empty* composer, and the server
`TrimSpace`s input (session_slash_command.go:22), so pasting `/name` or
typing a leading space reaches the session. The TUI, by contrast,
forwards unmatched slash names to the session
(hub_session_keys.go:440-457). This design closes the gap rather than
documenting it:

1. **Palette fallthrough.** The web palette's command-filter mode matches
   fuzzily (subsequence scoring, commands.ts:580-597), so "no match" must
   be defined precisely or near-miss names get captured by built-ins (a
   command named `stat` fuzzy-matches `status`). The fallthrough rule is:
   **when the query's first token exactly equals no registry command id,
   Enter sends the raw text (`/name args`) as session input** on session
   pages — TUI parity, since the TUI forwards on exact-name miss
   (hubCommandByName, hub_session_keys.go:439-441). Exact equality with a
   built-in id still runs the built-in (the collision rule); fuzzy
   near-misses fall through. Expansion stays server-side; the web client
   needs no template logic. Both clients intercept their own built-in sets
   — the web palette's 23 commands (commands.ts; including `status`,
   `model`, `help`, `steer`, `queue`) and the TUI's 27 — and only headless
   input is interception-free. When a typed name exactly equals both a
   built-in id and a catalog command name, the built-in wins; the catalog
   entry remains reachable via arrow-key selection.
2. **Palette command listing.** The palette consumes `serf/command/list`
   and lists plugin and user-global commands alongside built-ins, badged by
   `source`, showing `description`/`argument-hint`. Selecting one enters
   the palette's existing args flow where applicable, and submits the
   invocation as session input. **Plugin-source entries submit the
   qualified `/plugin:name` form**, never the bare name: the catalog
   deliberately lists a shadowed plugin command alongside its serf-wide
   shadow (§Catalog boundary), and a bare submission would resolve to the
   serf-wide command regardless of which entry the user picked. User-source
   entries submit the bare form. Project commands are not in the hub
   catalog (§Catalog boundary); they invoke via the fallthrough, and a
   project command shadows any same-named catalog entry submitted bare —
   the same resolution rule the TUI and headless input already live with.

The TUI's built-in interception stays: a serf-wide command named `status`
or `model` remains unreachable in the TUI (identical for plugin commands
today), and the equivalent holds for web built-ins in the web UI.
`docs/skills.md` documents this. A reserved-name warning at discovery is a
possible follow-up; it requires sharing the clients' built-in name lists
and is out of scope here.

### Lifecycle and persistence

Serf-wide commands are discovered once at session init, exactly like skills;
nothing is added to `SessionConfig`, the snapshot schema, or launch config.
Resumed and forked sessions re-discover from the current filesystem. A
mid-session worktree swap (`swapEnvAndRefresh`,
`agent/session_env_swap.go:17`) changes the working directory without
re-running discovery, so project commands reflect the tree the session
started in until restart — the same staleness skills have today; documented,
not fixed here.

## Error handling

Fail-soft everywhere; loud warnings; never block spawn.

| Condition | Behavior |
|---|---|
| Commands dir absent | Silent (default state on most machines) |
| Commands dir present but unreadable | Warning, continue with other dirs |
| Command file unreadable after a successful dir scan | Skip file, warning naming file, continue (plugin discovery fails hard here, commands.go:97-100; serf-wide discovery follows the fail-soft rule instead) |
| Malformed frontmatter in a `.md` | Skip file, warning naming file and error, continue |
| Filename containing `:` | Skip file, warning (namespace forgery guard) |
| Filename containing whitespace | Skip file, warning (space names are uninvokable; other whitespace is defense-in-depth) |
| Empty filename (`.md` exactly) | Skip file, warning (empty name never resolves) |
| Serf-wide body contains `!`` spans | Advisory warning at discovery; spans stay literal in expansion |
| Serf-wide vs serf-wide name collision | Silent deterministic shadowing (deepest project dir > user-global) |
| Serf-wide shadows plugin command | Silent; plugin reachable via `/plugin:name`; catalog lists both only when the shadowing command is user-global (§Catalog boundary) |
| Command name matches a client built-in (TUI registry or web palette) | Unreachable in that client's typed input; works headless; documented (§Web invocation) |
| Expansion failure (plugin commands) | Unchanged: warning + literal-text fallback in `expandSlashCommand` |
| `model`/`allowed-tools` declared | Unenforced-field warning naming source and file, generated at discovery |

## Testing

Per `docs/testing.md`: deterministic, scripted provider at the LLM boundary,
no live requests.

- **Unit — discovery**: table tests over fixture trees: user-global only;
  project walk only; ordering (user-global scanned first, deepest project
  dir shadows); symlinked cwd; non-`.md` files ignored; subdirectories not
  descended; malformed frontmatter skipped with warning; colon filenames
  skipped with warning; whitespace filenames skipped with warning; an empty
  name (`.md`) skipped with warning; `!`` advisory warning fires; nil env
  scans user-global only.
- **Unit — precedence**: `ResolveCommand` exact-matches a bare serf-wide key
  before plugin suffix fallback; `/plugin:name` still resolves explicitly;
  when two plugins share a command name with no serf-wide shadow, assert the
  result is one of the two (the fallback is nondeterministic map iteration
  by design — do not assert a specific winner).
- **Unit — expansion**: `ExpandArgs` substitutes `$ARGUMENTS`/`$1..$9` and
  never executes or reads: `!`cmd`` and `@file` spans pass through
  unexpanded (argument substitution still applies inside them as inert
  text — `!`echo $1`` with arg `foo` yields the literal text
  `!`echo foo``; assert inertness, not byte-for-byte identity).
  `expandSlashCommand` routes `"plugin"` sources to `Expand` and serf-wide
  sources to `ExpandArgs` (assert a `!`…`` span in a serf-wide body
  produces no `ExecCommand` call, via a scripted env that records them).
- **Session (scripted provider)**: serf-wide commands load and expand with
  **`PluginDirs: nil`** (the default configuration); `/review args` expands
  from project and user sources; a shadowed plugin command expands only
  when invoked as `/plugin:name`; a malformed command file yields the
  warning event and the session spawns normally; unenforced-frontmatter
  warnings fire for serf-wide commands with file labels.
- **Hub**: `serf/command/list` with zero plugin dirs still returns
  user-global entries with `source: "user"`; with a project
  `.serf/commands/` present on disk, the hub catalog returns **no**
  project-sourced entries (the hub calls discovery with a nil env — the
  test that pins §Catalog boundary); when a user-global command shadows a
  plugin command, the catalog lists both entries with distinct sources;
  shared-loader parity — the catalog matches what session init loads from
  the same plugin dirs plus the user-global dir.
- **Fuzz**: discovery over fuzzed directory trees and frontmatter, following
  `agent/plugin/loader_program_fuzz_test.go` patterns; register targets per
  the fuzz registry (`make fuzz-registry-check`).
- **Frontend**: palette command-filter fallthrough — Enter sends the raw
  `/name args` as session input when the first token **exactly** equals no
  registry command id (a fuzzy near-miss like `/stat` falls through to the
  session; an exact built-in id runs the built-in; an exact tie between a
  built-in and a catalog command activates the built-in, with the catalog
  entry selectable by arrow key). The palette lists `serf/command/list`
  entries with source badges; selecting a **plugin-source entry submits
  the qualified `/plugin:name` form** (a shadowed plugin entry must not
  run its serf-wide shadow), and selecting a user-source entry submits the
  bare form; fetch failure degrades to built-ins only (fallthrough still
  works). Biome + `make test-web` gates; `make test-web-browser` on
  Chrome-capable hosts.

## File-by-file change list

| File | Change |
|---|---|
| `agent/plugin/commands.go` | Add `Source` and `File` to `Command`; set both in `discoverPluginCommands` |
| `agent/plugin/serfwide.go` | New: `DiscoverSerfWideCommands`, dir-scan helper, `globalCommandsDir`, filename guards, directive advisory, unenforced-field warnings |
| `agent/plugin/plugin.go` | New: `MergeCommands` shared flatten-and-overlay |
| `agent/command/expand.go` | New: `ExpandArgs` (argument substitution only) |
| `agent/session_slash_command.go` | Branch expansion on `Command.Source` |
| `agent/session_init.go` | `initPlugins` stops merging `p.Commands`; after it returns, discover serf-wide commands and set `s.pluginCommands = MergeCommands(s.plugins, serfwide)`; queue discovery warnings |
| `appwire/types.go` | `CommandDescriptor.Source`; update the type's doc comment (currently says "plugin-provided") |
| generated AppWire outputs | Run `make generate` (`types.gen.ts`, protocol doc); `make lint-generated` must pass |
| `appwire/protocol.go` | `serf/command/list` description mentions source |
| `cmd/serf-hub/app_rpc.go` | `hubCommandList` uses `MergeCommands`; remove the empty-dirs early return |
| `cmd/serf-hub/frontend` palette (`CommandPalette.tsx`, command registry) | Command-filter fallthrough sends unmatched `/name args` as session input; palette lists `serf/command/list` commands badged by source |
| `docs/appwire-protocol.md` | Document the `source` field |
| `docs/skills.md` | Document skills and command sources, the inert-expansion posture, and the trust model |

## Open questions

None. Precedence (project > user > plugin), the inert-expansion posture
(codex model), the catalog boundary, client-parity documentation, and
deferred frontmatter enforcement were all decided during design review and
adversarial re-review.
