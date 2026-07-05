# Plugin Marketplaces & Lifecycle — Design

**Date:** 2026-07-04
**Status:** Approved design (v3 — adversarial review + YAGNI cuts folded in); ready
for implementation planning
**Scope:** Give serf — CLI, web hub, and TUI — full support for Claude Code plugin
marketplaces: add/remove/list marketplaces, explore plugins in a marketplace,
and install / upgrade / auto-upgrade / disable / remove plugins. Bundle a couple
of standard marketplaces. Integrate plugin health into `serf-doctor`.

**v2 revision (adversarial review).** Two competing reviewers verified this spec
against the code; 15 legitimate findings were folded in. Material changes from v1:
the source-type discriminator is **`url`, not `git`** (§5–§7); the seam model is
corrected — the hub injects plugin dirs into **spawned subprocess argv**, not
`SessionConfig` (§8); the loader is made **fail-soft via manager-side
pre-validation + dedup** (§7–§8); upgrade cache dirs are keyed by **sha** (§5, §7);
slash-command execution is **not** free for the TUI (§10); the confirmation/trust
model is specified (§7). Builds on the repo's prior `sp3-marketplace-design.md`
(§2), which already fixed the source types.

**v3 revision (YAGNI cuts).** Two scope reductions for v1: (1) **project scope is
cut — v1 is user-scope only** (a repo-local plugin can still be hand-pointed with
`--plugin-dir`); this removes the second registry, `projectRoot` threading, and
cross-scope dedup. (2) **GC is a dumb sweep, not live-session-aware** — upgrade
never deletes, and a sweep on hub start (plus `serf plugin gc`) reclaims dirs no
registry entry references (§12). Retained by explicit request: `git-subdir`
**sparse/partial clone** (§6) and **`renames` tracking** across upgrade (§6).

---

## 1. Goal & non-goals

**Goal.** A user can, from any of serf's three surfaces, register a plugin
marketplace, browse its catalog, install a plugin, keep it up to date (manually
or automatically), disable it without removing it, and remove it — with serf
loading the plugin's hooks, skills, subagents, MCP servers, **and slash
commands** into sessions.

**Non-goals (v1).**
- Sharing plugin state with an installed Claude Code (serf keeps its own store;
  see §3).
- Live hot-reload of plugins into a running session (activation is next-session;
  see §12).
- `npm` plugin sources (parsed, warned as unsupported; see §7).
- **Project-scope plugins** — v1 is **user-scope only**. A repo-local or
  in-development plugin can still be hand-pointed with `--plugin-dir` (that path
  already exists and is unchanged). A first-class project scope is a documented
  future extension (§5.3).
- The `/` slash-command **autocomplete menu** UI — v1 makes typing `/name` work;
  discoverability polish (the web `/` menu, TUI palette merge) is deferred. The
  `serf/command/list` RPC still ships (the TUI needs it to route `/name`).
- Interactive permission prompts for plugin-declared hooks beyond serf's existing
  hook policy (out of scope; unchanged).

---

## 2. Context: what already exists vs. what is greenfield

serf is **not** starting from zero. `agent/plugin` explicitly models Claude Code
plugins: its package doc states it "loads Claude Code-style plugins from disk —
their manifest, skills, subagents, hooks, and MCP server configs."

**Already implemented (the consumption foundation):**
- `--plugin-dir` points serf at a plugin directory (`cmd/serf/serve.go` →
  `SessionConfig.PluginDirs`, `agent/session_config.go`).
- `plugin.Load(dir)` / `plugin.LoadAll(dirs)` (`agent/plugin/plugin.go`) read the
  `.claude-plugin/plugin.json` manifest (with `.codex-plugin/` fallback), expand
  `${CLAUDE_PLUGIN_ROOT}`, and produce a `plugin.Instance` holding
  `Skills`, `Agents`, `Hooks`, `MCPConfigs`.
- The session load loop (`agent/session_init.go`, ~`:704`–`:723`) wires those four
  component types into session state: `s.skills`, `s.pluginAgents`,
  `s.hookRunner`, `s.pluginMCPConfigs`.
- Hook parsing/execution is Claude-compatible (`agent/plugin/hooks.go`,
  `agent/internal/hooks`); MCP config matches Claude's `.mcp.json`
  (`agent/mcpconfig`); skills use `SKILL.md` + frontmatter (`agent/skill`); agents
  use `agents/*.md` + frontmatter (`agent/plugin/agents.go`, which already imports
  `agent/internal/frontmatter`).
- Both UIs already have a proven management surface — the `/credentials`
  provider CRUD (PRI-1880) — and a read-only **Extensions** settings group
  (Plugins / Skills / MCP).
- The hub already hosts long-running background goroutines on the signal context
  (`cmd/serf-hub/main.go`), and spawns `serf serve` **subprocess** daemons that
  outlive the spawning call (`cmd/serf-hub/spawn.go`); sessions receive their
  config as CLI argv, not an in-process `SessionConfig` (see §8).

**Greenfield (this design):**
- Any marketplace concept: registry, catalog parsing, git fetch, install/upgrade.
- Enable/disable state and loader gating (today serf loads *every* dir it is
  pointed at, and aborts session init on any bad or duplicate-named plugin — §7).
- Auto-upgrade.
- **Slash-command loading** — the one component type serf does not load at all.
  `Manifest.Commands` (`agent/plugin/plugin.go:31`) is parsed and dropped; there is
  no `discoverPluginCommands`, no `Commands` field on `Instance`, no `commands/`
  scan, and no slash-command execution model anywhere in serf.
- Web + TUI management surfaces for all of the above.
- `serf-doctor` plugin health checks.

**Prior art (designed, never built).** A Claude-Code-compat effort
(`docs/superpowers/{plans,specs}/2026-05-14-claude-code-compat-*`) planned but did
not implement the lifecycle:
- `sp3-marketplace-design.md` — the marketplace resolver: `SourceKind` =
  {`directory`, `github`, `url`, `git-subdir`}, `known_marketplaces.json`, catalog
  parsing, and the `cmd/serf/plugin/` scaffolding. **This design adopts its source
  types verbatim** (the v1 draft's `git` was wrong).
- `sp4-install-plan.md` — the lifecycle/registry: an `internal/plugins` package
  with `Registry` (matching Claude's `installed_plugins.json`), atomic IO, file
  lock, version resolution, enable/disable, and a `serf plugin` CLI.
- `spb-manage-plugins-skill-plan.md` — a deliberately minimal stopgap (a markdown
  skill, no registry, no enable/disable, no auto-update, no UI). This design
  supersedes it.

This design revives sp3 + sp4, adds slash-command execution, auto-upgrade, both
UIs, doctoring, and bundling, corrected by the adversarial review.

---

## 3. Approach: native, serf-owned store

**Decision:** serf implements marketplaces and the plugin lifecycle itself. State
lives under serf's own config roots — never `~/.claude`. The on-disk *formats*
mirror Claude Code so plugins and marketplaces authored for Claude Code drop in
unchanged, but a plugin installed in serf is invisible to Claude Code and vice
versa.

Rejected alternatives: *wrap the `claude` CLI / share `~/.claude/plugins`* (hard
dependency on `claude`, shared mutable state, coupling to a private format, no fit
for bundling or scheduled auto-upgrade); *native + read-only import of `~/.claude`*
(a confusing second source of truth — deferrable to a later one-shot import). This
matches how serf owns all its other config and carries no runtime dependency on
the `claude` binary.

---

## 4. Architecture: one manager, three drivers, loader stays the consumer

All lifecycle logic lives in **one new package in the root module**:
`internal/plugins` (importable by `cmd/serf`, `cmd/serf-hub`, `cmd/serf-doctor`,
all in the root module `primeradiant.com/serf`). It is the single source of truth
for on-disk plugin state, serialized by one file lock. Three things drive it; none
reimplement it:

- **`serf plugin` CLI** (`cmd/serf/plugin/`) — calls the package directly.
- **Web + TUI** — call it over new appwire RPCs the hub exposes
  (`serf/marketplace/*`, `serf/plugin/*`), the same way `/credentials` uses
  `serf/instance/*` today.
- **The existing `agent/plugin` loader stays the *consumer*.** The manager
  *materializes plugin directories and computes the validated, enabled dir set*
  (§7–§8); the loader *reads dirs it is handed*. The **only** change to the
  `agent` module is a new `commands/` loader (§10) — enable-gating, dedup, and
  fail-soft validation all live in the manager, outside the agent module.

```
internal/plugins/            NEW — the manager (root module)
  registry.go                installed_plugins.json  (sp4 design, revived)
  marketplaces.go            known_marketplaces.json + resolve / clone / pull
  source.go                  SourceKind {directory,github,url,git-subdir}; resolve → dir
  install.go                 Install / Upgrade / Remove / Enable / Disable / List / UpdateAll
  enabled.go                 EnabledPluginDirs() — validated + deduped enabled dirs (§8)
  doctor.go                  read-only health checks (§13)
  git.go                     git shell-out (clone / partial-clone / pinned checkout / pull)
  locks.go / atomic.go       flock + tmp-rename writes
  seed.go                    first-run default-marketplace seed (§11)

cmd/serf/plugin/             NEW — `serf plugin …` CLI (§9.5)
cmd/serf-doctor/             MODIFY — add a `plugins` subcommand (§13)
agent/plugin/commands.go     NEW — commands/*.md discovery (§10)
agent/plugin/plugin.go       MODIFY — Instance.Commands; agent/session_init.go registers them
appwire/ + cmd/serf-hub/     NEW — methods + hubPluginsController (§9.2)
cmd/serf-hub/assets/         NEW — plugins.js, template partial (§9.3)
cmd/serf-tui/                NEW — PluginsPanel overlay (§9.4)
```

The `agent` module never imports the manager, preserving the module boundary; the
hub/CLI compute dirs with the manager and hand them to the agent as argv/config.

---

## 5. On-disk state & formats

Serf-native, Claude-*shaped*. A single user-scope store (§5.3 on why v1 is
user-scope only):

```
~/.config/serf/plugins/
  known_marketplaces.json
  installed_plugins.json
  marketplaces/<name>/                   cloned marketplace repo (.claude-plugin/marketplace.json)
  cache/<marketplace>/<plugin>/<sha>/    materialized plugin (.claude-plugin/plugin.json)
```

### 5.1 `known_marketplaces.json` (user scope)

```json
{
  "<name>": {
    "source": { "source": "github", "repo": "owner/repo" },
    "installLocation": "<abs path to marketplaces/<name>/>",
    "lastUpdated": "<RFC3339>"
  }
}
```

`source.source` is one of the four **sp3** kinds (§6): `directory`, `github`,
`url`, `git-subdir`. (`git` is accepted as a **read-only legacy alias** for `url`
on the marketplace container, because real `known_marketplaces.json` files written
by older Claude Code contain it; serf never writes `git`.) A single user-level
registry, matching Claude Code's model.

### 5.2 `installed_plugins.json`

A single user-scope registry. The Claude `{version, plugins}` shape is retained
for familiarity:

```json
{
  "version": 2,
  "plugins": {
    "<plugin>@<marketplace>": [
      {
        "installPath": "<abs path to cache/…/<sha>/, or the referenced dir for directory sources>",
        "version": "1.2.0",
        "gitCommitSha": "…",
        "installedAt": "<RFC3339>",
        "lastUpdated": "<RFC3339>",
        "enabled": true,
        "autoUpgrade": false,
        "source": { … resolved plugin source … }
      }
    ]
  }
}
```

The value is a one-element **array** (Claude's shape) rather than a bare object,
for schema drop-in familiarity; v1 writes one entry per plugin. `enabled` and
`autoUpgrade` are folded into the entry (serf has no `settings.json` to hold a
separate `enabledPlugins` map). Mutations go through atomic tmp-rename writes under
a flock.

### 5.3 Scope: user-only in v1

v1 has a **single user-scope store**. Project scope (a repo-local
`<git-root>/.serf/plugins/`) is cut for v1 to avoid a second registry,
`projectRoot` threading, cross-scope dedup, and the fact that the hub serves many
working directories at once and a global settings page cannot disambiguate "which
project." A repo-local or in-development plugin is still fully supported via
`--plugin-dir` (unchanged). A first-class project scope — scoped in the hub to a
selected session/project — is a documented future extension.

---

## 6. Marketplace resolver & fetcher

**Sources** — the four sp3 kinds:

| `source.source` | Fields | Resolution |
|---|---|---|
| `github` | `repo` (`owner/repo`), opt `ref`/`sha` | clone `https://github.com/owner/repo.git` → `marketplaces/<name>/` |
| `url` | `url` (https/http/`git@…`), opt `ref`/`sha` | clone url → `marketplaces/<name>/` |
| `directory` | `path` | reference in place, no clone |
| `git-subdir` | `url`+`path`, opt `ref`/`sha` | **sparse/partial** clone (`--filter=blob:none` + sparse-checkout of `path`), use the subdir |

**Operations:** **add** (resolve → clone/reference → read
`.claude-plugin/marketplace.json` → validate → write registry entry; name from the
manifest); **remove** (unregister + delete the clone; never touches a `directory`
source's contents); **list**; **refresh** (`git pull --ff-only`, update
`lastUpdated`); **browse** (parse `plugins[]` → `{name, description, author,
category, source, homepage}` — "explore plugins in a marketplace").

**Fetching = shell out to `git`** (present on dev machines; Claude Code does the
same; avoids a `go-git` dependency). Missing `git` → clear error, reported by
`serf-doctor` (§13). `git.go` centralizes clone, **partial/sparse clone** for
`git-subdir`, pinned checkout (`git checkout <ref|sha>`), and `pull --ff-only`.
The manifest's `renames` map (old→new plugin name) is recorded and followed across
an upgrade.

---

## 7. Plugin lifecycle

A marketplace plugin entry's `source` is a string (`./subdir`, relative to the
marketplace root) or an object (`url`, `github`, or `git-subdir`). **Install**
resolves it and materializes it:

| Plugin `source` | Materialization |
|---|---|
| `./subdir` (string) | copy from the marketplace clone (or, for a `directory` marketplace, from its referenced root at `installLocation`) into `cache/<mkt>/<plugin>/<sha>/` |
| `git-subdir` {url,path,ref,sha} | sparse/partial clone pinned → copy subdir → discard clone |
| `github` / `url` | clone (pinned to `sha` if given) into `cache/<mkt>/<plugin>/<sha>/` |
| a `directory`-marketplace plugin used for local dev | **referenced in place** (no copy) so edits are live; `installPath` points at the source dir; "upgrade" is inherently current |
| `npm` | **deferred** — parsed, warned "unsupported in v1", no install |

**Cache dirs are keyed by resolved `<sha>`, not `<version>`.** Claude treats every
commit of a version-less plugin as a new version, so `version` is often static or
absent; keying by sha guarantees each upgrade materializes a **distinct** dir
(fixing the v1 draft's swap-then-GC-onto-the-same-dir bug). Directory sources,
which have no sha, are referenced in place rather than cached.

Install then **fully validates** the materialized plugin — not just that
`plugin.json` parses, but that **every component parses** (`agents/*.md`,
`hooks.json`, `.mcp.json`, `skills`, `commands/*.md`), because the session loader
parses all of them and fails hard on any error (§8). Only a fully-valid plugin is
committed to the registry, with `version` (manifest `version`, or `unknown`),
resolved `gitCommitSha`, `enabled:true`, `autoUpgrade:false`.

**Verbs** (the full requested set), all in `internal/plugins`, one flock, atomic
writes:

| Verb | Behavior |
|---|---|
| install | fetch + materialize + **full validate** + register |
| upgrade / upgrade --all | re-resolve, re-fetch to a **new sha-dir**, full-validate; if the sha changed and validates, repoint the registry entry to the new dir; **the superseded dir is left in place and reclaimed by the sweep** (§12) |
| remove | delete the registry entry and its cache dir (takes effect next session) |
| disable / enable | flip `enabled` — no fetch, no delete |
| auto-upgrade on/off | flip `autoUpgrade` (§9.1 daemon acts on it) |
| gc | sweep cache dirs no registry entry references (§12) |
| list | version, enabled, autoUpgrade, marketplace, broken? |

**Integrity, resilience & trust:**
- Materialize → **full-validate** → atomic-rename into cache; a failed or invalid
  fetch never enters the registry.
- Because a plugin can still break *after* install (a mutated `directory` source,
  or an auto-upgrade to bad code), the **session-load path is fail-soft**: the
  manager pre-validates each enabled dir and skips broken or duplicate-named ones
  with a loud warning (§8), rather than letting one bad plugin abort the session.
- Honor `ref`/`sha` pins; record the resolved sha. Broken plugins surface in
  `list` and `doctor`, never silently succeed.
- **Confirmation/trust is a surface responsibility, not a manager primitive.**
  `internal/plugins` exposes unconditional `AddMarketplace`/`Install`; each surface
  gates them: the web shows a confirm dialog with the source URL, the TUI a
  confirm key, the CLI an interactive prompt (or an explicit `--yes` for
  `--json`/non-interactive use). **Auto-upgrade needs no per-run confirmation
  because enabling `autoUpgrade` on an already-installed, git-backed plugin *is*
  the standing consent.** (serf's existing repo-trust precedent
  `LaunchConfigTrustRepo`, `cmd/serf-hub/app_launch.go`, is the model to reuse if a
  future project scope needs per-repo marketplace trust.)
- flock serializes concurrent CLI / hub / TUI mutations.

---

## 8. Loader gating & the real injection seams

Today serf loads **every** dir in `SessionConfig.PluginDirs`, and the loader is
**fail-hard**: `plugin.LoadAll` (`agent/plugin/plugin.go:238`) returns on the first
`Load` error *and* on any duplicate plugin **name**, and `agent/session_init.go`
(~`:574`, `:704`) propagates that so `NewSession` fails. A single broken or
duplicate-named enabled plugin would otherwise brick every new session.

**Gating + resilience live in the manager**, keeping `agent/plugin` unchanged
(except the new commands loader). `EnabledPluginDirs() []string`:
1. Reads the (user-scope) registry and filters to `enabled` entries.
2. **Pre-validates** each dir (a dry-run `plugin.Load`) and **skips** any that fail
   to parse, emitting a loud warning surfaced to `doctor`.
3. **Dedups by plugin name** (explicit `--plugin-dir` wins over a registry entry),
   so the loader's fail-hard duplicate check never fires.
4. Returns the surviving dirs.

**The injection seams differ by surface (corrected from v1):**
- **CLI** (`cmd/serf`) builds a `SessionConfig` in-process, so it sets
  `SessionConfig.PluginDirs = EnabledPluginDirs() + explicit --plugin-dir`.
- **Hub** does **not** build a `SessionConfig` — it **spawns a `serf serve`
  subprocess** (`cmd/serf-hub/spawn.go`) and passes config as **argv**. Enabled
  dirs are injected as `--plugin-dir` arguments via the launch-config
  `plugin_dirs` → `ToArgs` path (`cmd/serf-hub/internal/launchconfig/args.go`).
  The `~/.config/serf/plugins/*` scan in `web_settings.go` is **display-only**
  (`discoverPluginsForSettings`) and is *not* a session seam; the unused
  `hubcore/config.go` `PluginDirs` default is not the mechanism either.

Explicit `--plugin-dir` and launch-config `pluginDirs` remain an *additional*
dev/power-user source (and the supported way to load a repo-local plugin, §5.3),
merged and deduped with registry-enabled dirs.

---

## 9. Auto-upgrade daemon & the surfaces

### 9.1 Auto-upgrade daemon

Lives in the **hub** (`serf-hub`) — the only persistent process; it already hosts
background goroutines on the signal context (`cmd/serf-hub/main.go`).

- A background goroutine on a configurable interval (proposed default ~12h, plus
  once on hub start) refreshes marketplaces (`git pull --ff-only`), then for each
  installed plugin with `autoUpgrade:true` **and** a git-backed source runs the
  §7 upgrade flow (fetch to a new sha-dir → full-validate → repoint the registry).
- **The daemon never deletes.** It only writes new sha-dirs and repoints the
  registry, so a live session (which holds absolute paths into its materialized
  dir — skills read lazily via `os.ReadFile(meta.SkillFile)` in `agent/skill`,
  hook/MCP commands bake the dir at load in `agent/plugin/hooks.go`) keeps using
  its dir; new sessions pick up the new one. Superseded dirs are reclaimed only by
  the sweep (§12). A successful upgrade updates `lastUpdated` and emits
  `serf/plugin/updated`. Failure-isolated: one plugin's failure never blocks
  others; logged and surfaced in `doctor`.
- Global on/off + interval in `hub.toml`; per-plugin opt-in via `autoUpgrade`;
  manual "check now" from any surface runs the same code path.

### 9.2 Shared RPC surface

One method set the hub exposes; web and TUI both call it (as `/credentials` shares
`serf/instance/*`). Declared in `appwire/protocol.go`, registered in
`cmd/serf-hub/app_rpc.go` via `HandleTyped`, delegating to a `hubPluginsController`
that mirrors `app_instances.go` (reload → mutate → atomic write, mutex; delegates
to `internal/plugins`). All operate on the single user-scope store.

- `serf/marketplace/{list, add, remove, refresh, browse}`
- `serf/plugin/{list, install, upgrade, remove, enable, disable, setAutoUpgrade, doctor}`
- `serf/command/list` (slash-command catalog for autocomplete — **owned by P3**, §10)
- Notifications: `serf/marketplace/updated`, `serf/plugin/updated`.

### 9.3 Web surface (`cmd/serf-hub`)

Clones the `/credentials` end-to-end pattern (appwire RPC + htmx settings section +
inline controller). A new **"Marketplaces & Plugins"** section in the *Extensions*
nav group (`templates/partials/settings.html`), one page, three areas built from
the existing `settings-collection*` / `btn*` / `status-badge` classes:
**Marketplaces** (list + add + refresh/remove), **Browse** (pick marketplace →
catalog with search + install), **Installed** (enable/disable, auto-upgrade,
upgrade, remove; broken flagged). Files: a template partial modeled on
`templates/partials/credentials.html`; a new `assets/plugins.js` mirroring
`assets/launchconfig.js`; routing in `web.go`; section registration in
`web_settings.go`.

### 9.4 TUI surface (`cmd/serf-tui`)

A tabbed `PluginsPanel` overlay modeled on `LaunchSettingsPanel` (**Marketplaces │
Browse │ Installed** tabs, single-key actions like `CredentialsPanel`). Opened via
a `/plugins` (`/marketplaces` alias) command in `hub_command_registry.go`; wired
through `focus_trap.go` precedence, `hub_update_config.go` handlers, and `Cmd*`
wrappers in `launchconfig_client.go` calling the same RPCs as web.

### 9.5 CLI (`cmd/serf/plugin/`)

The `serf plugin` tree (sp4 design), thin over the same manager:
`marketplace add|remove|list|refresh`, `install <plugin>@<mkt>`, `upgrade [--all]`,
`remove`, `enable`, `disable`, `list`, `gc`, `doctor` — each with `--json`, and
`--yes` (for non-interactive install/add).

---

## 10. Slash-command execution

The one component type serf cannot load today. serf's prompt-template engine
(`agent/section_resolver.go`) is a *system-prompt assembler* and supports none of
`$ARGUMENTS` / `$1` / `` !`cmd` `` / `@file`, so command expansion is net-new.

**1. Discovery.** `discoverPluginCommands` (new `agent/plugin/commands.go`) mirrors
`discoverPluginAgents` (`agent/plugin/agents.go`): scan `commands/*.md`, parse
with `agent/internal/frontmatter.Parse` into `{name, description, argument-hint,
allowed-tools, model, body}`. Add a `Commands` field to `plugin.Instance`; register
in the `session_init.go` load loop alongside skills/agents/hooks. Namespaced like
skills (`plugin:command`) with collision handling.

**2. Expansion engine** (net-new, self-contained). Given a command and a raw
argument string: `$ARGUMENTS` → full args; `$1`..`$9` → positional (shell-split);
`` !`cmd` `` → run in the session's execution environment
(`agent/session.go` `env execenv.ExecutionEnvironment`, `currentEnv()`), substitute
bounded stdout; `@file` → inline bounded file contents (session-relative). Honor
`allowed-tools`/`model` per turn **subject to §14** (degrade to parsed-but-warned
if the per-turn seams don't exist).

**3. Invocation seam — NOT free for both UIs (corrected from v1).** The expander
lives where the session is, and both surfaces must reach it — but they differ:
- **Web** already forwards `/`-text verbatim through `turn/start` → `inputCh` →
  `Session.ProcessInput` (`agent/session_lifecycle.go:388`; `renderer.js`
  composer). So server-side interception at the `ProcessInput` boundary gives web
  execution nearly for free; its only addition is a `/` autocomplete menu.
- **TUI** does **not**. `cmd/serf-tui/hub_session_keys.go` intercepts *all*
  `/`-input client-side, routes it to the static `hubCommandRegistry`
  (`runHubSlashCommand`), and dead-ends an unrecognized command with "Unknown
  command" — it **never** forwards to `turn/start`. P6 must add a real dispatch
  change: after the built-in registry misses, consult the plugin-command catalog
  (`serf/command/list`) and, on a match, forward `/name args` to the session
  (`sendHubInput`/`turn/start`) instead of erroring.

**Precedence is therefore split by construction:** built-in UI control commands
(`/model`, `/compact`, `/plugins`, …) are handled client-side in each surface and
win; only unrecognized-but-known-plugin commands are forwarded to the session
expander. The server-side expander also runs for headless `serf /name args`
(`cmd/serf/main.go` → `run` prompt path).

**Security.** `` !`cmd` `` and the command's tools run under serf's existing
execution-environment / permission model — no extra privilege. A plugin running
bash is the trust decision `install` already surfaced (§7).

---

## 11. Bundled marketplaces

On first run — gated by the **user-scope** `known_marketplaces.json` not yet
existing — seed the registry with two standard marketplaces as **pointers**,
cloned lazily on first browse:
- `claude-plugins-official` → `github: anthropics/claude-plugins-official`
- `superpowers-marketplace` → `github: obra/superpowers-marketplace`

Seeding is **user-scope only** and first-run only; project scopes are never seeded
(so a fresh repo never re-adds defaults, and removing a seeded marketplace keeps it
removed). The seed list is a small embedded Go constant (`seed.go`) — pointers
only, not vendored contents. A `--no-default-marketplaces` flag / config opt-out
lets tests and power users start clean. (Offline out-of-the-box operation would
require vendoring contents into the binary; explicitly not chosen for v1.)

---

## 12. Activation & GC semantics

Plugin discovery runs once per session init (`agent/session_init.go`); there is no
file-watch or hot-reload. So install / enable / disable / upgrade take effect on
the **next session**.

**GC is a dumb sweep, not live-session refcounting.** `install`/`upgrade` only ever
*add* sha-dirs and repoint the registry; they **never delete**. So a background
auto-upgrade can never remove the dir a running session was spawned against — the
superseded dir just lingers on disk. Reclamation is a separate sweep that deletes
any `cache/` dir no registry entry points at, run **only when it is safe**: on hub
start (before any session exists) and on demand via `serf plugin gc` (the user runs
it when idle). Superseded dirs therefore accumulate at most across one hub uptime.
The one eager delete is a user-initiated `remove`, by intent — a live session using
that plugin may error on lazy access until restarted (acceptable, as it is
explicit). This is the simplification of v2's live-session-aware GC (no per-session
dir refcounting). Live hot-reload remains a future extension; the store and manager
API do not need to change for it.

---

## 13. Doctoring (`serf-doctor plugins`)

`serf-doctor` is a read-only forensic inspector (`locate` / `transcript` /
`apilog` / `watches` / `tree`), a thin `main` over a checker package, human summary
+ `--json`. Plugin doctoring lands as a new **`serf-doctor plugins`** subcommand
backed by `internal/plugins/doctor.go` (reusing `plugin.Load` for component
validity — the root module already imports `agent/plugin`), plus a
`serf plugin doctor` alias. Read-only; not session-scoped (takes `--json`, not a
session selector). Each finding OK/WARN/FAIL with a remediation hint:
- **Registry ↔ disk drift** — orphaned entries (missing dir), orphaned cache dirs
  (no entry), registry version ≠ manifest version.
- **Marketplaces** — clone exists + valid git repo, `marketplace.json` parses,
  staleness (age since last pull).
- **Component validity** (per installed+enabled plugin) — `hooks.json` parses +
  referenced scripts exist & executable + `${CLAUDE_PLUGIN_ROOT}` resolves;
  `.mcp.json` parses; skills/agents/commands frontmatter valid. **This is the same
  validation the fail-soft loader (§8) runs — doctor surfaces proactively what the
  loader would skip.**
- **Auto-upgrade sanity** — plugins marked `autoUpgrade` but backed by a non-git
  `directory` source (cannot upgrade).
- **Environment** — `git` on PATH; store writable.

---

## 14. Plan-time verifications (do not assume)

Two seams the exploration did not confirm; verify before relying on them, degrade
gracefully if absent rather than blocking:
- **Per-turn `model` override** for a slash command's `model` frontmatter.
- **Per-turn tool restriction** for a slash command's `allowed-tools` frontmatter.

If either seam does not exist, that field is parsed, warned, and not enforced in
v1 (the command runs with the session's default model / tools).

---

## 15. Phasing

One design spec, built in independently-shippable phases; each gets its own plan.

| Phase | Deliverable | Depends on |
|---|---|---|
| **P1 Backend core** | `internal/plugins`: registry, `known_marketplaces.json`, sp3 source resolution (incl. partial clone), git fetcher, Install/Upgrade/Remove/Enable/Disable/List/UpdateAll, **full-validate on install/upgrade**, sha-keyed cache, flock + atomic IO | — |
| **P2 CLI + gating + bundling** | `serf plugin` tree (`--yes`); `EnabledPluginDirs` (validate + dedup) feeding CLI `SessionConfig` **and** hub spawn argv (`--plugin-dir` via `ToArgs`); user-scope first-run seeding | P1 |
| **P3 Slash commands** | `commands.go` discovery + `Instance.Commands`; expander; server-side `ProcessInput` interception; **`serf/command/list`**; `serf /name` headless | independent of P1 |
| **P4 Auto-upgrade** | hub daemon + `hub.toml` config + sweep-based GC (`serf plugin gc` + hub-start sweep) + `serf/plugin/updated` + manual check-now | P1 |
| **P5 Web** | appwire methods + `hubPluginsController` + Marketplaces & Plugins page + `plugins.js` (autocomplete menu deferred, §1) | P1–P4 RPCs, **P3** |
| **P6 TUI** | `PluginsPanel`; **plugin-command dispatch/forwarding change** (§10) so `/name` routes to the session (palette-merge polish deferred, §1) | P1–P4 RPCs, **P3** |
| **P7 Doctoring** | `serf-doctor plugins` + `internal/plugins/doctor.go` + `serf plugin doctor` alias | P1 |

---

## 16. Testing strategy

TDD, real files, real `git`, no mocks:
- **Backend** — table-driven over `t.TempDir()` + **local git-repo fixtures** for
  each source type (`github` / `url` / `git-subdir` / `directory`); **no network**;
  partial-clone path exercised against a local bare repo; concurrency tests for
  flock / atomic writes; `t.Skip` if `git` absent.
- **Loader resilience** — a broken enabled plugin and a duplicate-named pair ⇒
  session still initializes, offenders skipped with a captured warning (not a
  fatal `NewSession` error).
- **Upgrade/GC** — advance a fixture HEAD ⇒ a new sha-dir is materialized and the
  registry repointed while the old dir remains; the `gc` sweep then deletes the old
  dir (no registry entry) but leaves the current one.
- **Slash commands** — expander unit tests (`$ARGUMENTS`, `$1..$9`, `` !`cmd` ``,
  `@file`, `allowed-tools`, `model`); discovery mirroring the agents-loader tests;
  install-fixture-plugin-with-a-command → invoke → assert expanded turn; a TUI test
  asserting an unknown-but-known-plugin command is forwarded (not "unknown").
- **`--plugin-dir` coexistence** — a hand-pointed dir and a registry-enabled plugin
  both load; a name collision dedups (explicit `--plugin-dir` wins).
- **Doctor** — fixture store with injected drift (orphan entry/dir, broken
  manifest, non-git `autoUpgrade`) ⇒ assert findings.
- **UIs** — e2e scenario cards driving the real web + TUI: add marketplace → browse
  → install → enable/disable → upgrade → remove, falsifiable assertions.
- **Pristine output** — capture + assert the fail-soft load-time warnings.

---

## 17. Summary of new/changed files

**New:** `internal/plugins/{registry,marketplaces,source,install,enabled,doctor,git,locks,atomic,seed}.go`;
`cmd/serf/plugin/`; `agent/plugin/commands.go`; the slash-command expander package;
`cmd/serf-hub/assets/plugins.js` + a template partial; `cmd/serf-tui` PluginsPanel.

**Modified:** `agent/plugin/plugin.go` (`Instance.Commands`), `agent/session_init.go`
(register commands), `appwire/protocol.go` (method catalog),
`cmd/serf-hub/{web.go,web_settings.go,app_rpc.go}` + new controller,
`cmd/serf-hub/internal/launchconfig/args.go` (enabled dirs → `--plugin-dir` argv),
`cmd/serf-tui/{hub_command_registry.go,hub_session_keys.go,focus_trap.go,hub_update_config.go,hub_model.go}`
(plugin-command forwarding), `cmd/serf-doctor/main.go` (`plugins` subcommand),
`cmd/serf/main.go` (`plugin` command), `hub.toml` (auto-upgrade config).
