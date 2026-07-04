# Plugin Marketplaces & Lifecycle — Design

**Date:** 2026-07-04
**Status:** Approved design; ready for implementation planning
**Scope:** Give serf — CLI, web hub, and TUI — full support for Claude Code plugin
marketplaces: add/remove/list marketplaces, explore plugins in a marketplace,
and install / upgrade / auto-upgrade / disable / remove plugins. Bundle a couple
of standard marketplaces. Integrate plugin health into `serf-doctor`.

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
  (`agent/mcpconfig`); skills use `SKILL.md` + frontmatter; agents use
  `agents/*.md` + frontmatter.
- Both UIs already have a proven management surface — the `/credentials`
  provider CRUD (PRI-1880) — and a read-only **Extensions** settings group
  (Plugins / Skills / MCP).

**Greenfield (this design):**
- Any marketplace concept: registry, catalog parsing, git fetch, install/upgrade.
- Enable/disable state and loader gating (today serf loads *every* dir it is
  pointed at).
- Auto-upgrade.
- **Slash-command loading** — the one component type serf does not load at all.
  `Manifest.Commands` (`agent/plugin/plugin.go`) is parsed and dropped; there is
  no `discoverPluginCommands`, no `Commands` field on `Instance`, no `commands/`
  scan, and no slash-command execution model anywhere in serf.
- Web + TUI management surfaces for all of the above.
- `serf-doctor` plugin health checks.

**Prior art (designed, never built).** A Claude-Code-compat effort
(`docs/superpowers/plans/2026-05-14-claude-code-compat-*`) planned but did not
implement the lifecycle:
- `sp4-install-plan.md` — a full lifecycle/registry design: an `internal/plugins`
  package with `Registry` (matching Claude's `installed_plugins.json`), atomic IO,
  file lock, version resolution, enable/disable, and a `serf plugin` CLI. It
  references an "SP3" `MarketplaceResolver`/`PluginSource` collaborator that was
  never planned in detail.
- `spb-manage-plugins-skill-plan.md` — a deliberately minimal stopgap: a markdown
  skill teaching the agent to install/remove/list by shelling out, with **no
  registry, no enable/disable, no auto-update, no UI**. This design supersedes
  that stopgap and builds the pieces it punted on.

This design revives the sp4 lifecycle design, builds the missing SP3 resolver,
adds slash-command execution, auto-upgrade, both UIs, doctoring, and bundling.

---

## 3. Approach: native, serf-owned store

**Decision:** serf implements marketplaces and the plugin lifecycle itself. State
lives under serf's own config roots — never `~/.claude`. The on-disk *formats*
mirror Claude Code so plugins and marketplaces authored for Claude Code drop in
unchanged, but a plugin installed in serf is invisible to Claude Code and vice
versa.

Rejected alternatives:
- *Wrap the `claude` CLI / share `~/.claude/plugins`*: least code, but hard-depends
  on `claude` being installed, shares mutable state (concurrent runs can corrupt
  it), couples serf to Claude's private undocumented format, and neither
  "bundle-with-the-product" nor scheduled auto-upgrade fit Claude's model.
- *Native + read-only import of `~/.claude`*: adds a confusing second source of
  truth ("why is this plugin here, how do I remove it"). A one-shot import can be
  added later if wanted; it is not in v1.

This matches how serf owns all its other config (`~/.serf`, `~/.config/serf`) and
carries no runtime dependency on the `claude` binary.

---

## 4. Architecture: one manager, three drivers, loader stays the consumer

All lifecycle logic lives in **one new package in the root module**:
`internal/plugins`. It is the single source of truth for on-disk plugin state,
serialized by one file lock. Three things drive it; none reimplement it:

- **`serf plugin` CLI** (`cmd/serf/plugin/`) — calls the package directly.
- **Web + TUI** — call it over new appwire RPCs the hub exposes
  (`serf/marketplace/*`, `serf/plugin/*`), the same way `/credentials` uses
  `serf/instance/*` today.
- **The existing `agent/plugin` loader stays the *consumer*.** The manager
  *materializes plugin directories*; the loader *reads* them. Clean split: manager
  writes dirs, agent loads dirs. `agent/plugin` needs only two additions — a
  `commands/` loader (§10) and accepting the enabled-dir set (§8) — it gains no
  marketplace/install knowledge.

```
internal/plugins/            NEW — the manager (root module)
  registry.go                installed_plugins.json  (sp4 design, revived)
  marketplaces.go            known_marketplaces.json + resolve / clone / pull
  source.go                  resolve a plugin `source` → materialized dir
  install.go                 Install / Upgrade / Remove / Enable / Disable / List / UpdateAll
  enabled.go                 EnabledPluginDirs(scopes…) — feeds the loader (§8)
  doctor.go                  read-only health checks (§13)
  git.go                     git shell-out (clone/pull, pinned checkout)
  locks.go / atomic.go       flock + tmp-rename writes
  seed.go                    first-run default-marketplace seed (§11)

cmd/serf/plugin/             NEW — `serf plugin …` CLI (§9)
cmd/serf-doctor/             MODIFY — add a `plugins` subcommand (§13)
agent/plugin/                MODIFY — add commands/ discovery; accept enabled dirs
appwire/ + cmd/serf-hub/     NEW — methods + hubPluginsController (§9)
cmd/serf-hub/assets/         NEW — plugins.js, template partial (§9)
cmd/serf-tui/                NEW — PluginsPanel overlay (§9)
```

**Module note.** `internal/plugins` sits in the root module and is importable by
`cmd/serf`, `cmd/serf-hub`, and `cmd/serf-doctor`. The `agent` module remains the
*consumer* of materialized directories and does not import the manager, preserving
the existing module boundary.

---

## 5. On-disk state & formats

Serf-native, Claude-*shaped*. Two scopes, matching serf's existing user/project
config split:

```
~/.config/serf/plugins/                 USER scope
  known_marketplaces.json
  installed_plugins.json
  marketplaces/<name>/                   cloned marketplace repo (.claude-plugin/marketplace.json)
  cache/<marketplace>/<plugin>/<ver>/    materialized plugin (.claude-plugin/plugin.json)

<project-git-root>/.serf/plugins/       PROJECT scope (same layout)
```

**`known_marketplaces.json`** — marketplace registry, keyed by marketplace name:

```json
{
  "<name>": {
    "source": { "source": "github", "repo": "owner/repo" },
    "installLocation": "<abs path to marketplaces/<name>/>",
    "lastUpdated": "<RFC3339>"
  }
}
```

`source` is one of the four real Claude-Code source shapes (§6). `directory`
sources set `installLocation` to the referenced path (no clone).

**`installed_plugins.json`** — install registry (revived from sp4, extended):

```json
{
  "version": 2,
  "plugins": {
    "<plugin>@<marketplace>": [
      {
        "scope": "user",
        "installPath": "<abs path to cache/…/<ver>/>",
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

Two deliberate departures from a literal Claude clone:
1. `enabled` and `autoUpgrade` are folded into each entry rather than living in a
   separate `enabledPlugins` map in a `settings.json`. serf has no `settings.json`
   to hang them on; one file, one lock, is simpler.
2. The tree is entirely serf's; there is no interop with `~/.claude`.

The value is an **array** of entries (matching Claude's schema) to allow the same
plugin at multiple scopes. Registry mutations go through atomic tmp-rename writes
under a flock (`locks.go`, `atomic.go`).

---

## 6. Marketplace resolver & fetcher

**Sources** — exactly the four seen in real `known_marketplaces.json` /
`marketplace.json`:

| Source | Resolution |
|---|---|
| `github` (`owner/repo`) | clone `https://github.com/owner/repo.git` → `marketplaces/<name>/` |
| `git` (url) | clone url → `marketplaces/<name>/` |
| `directory` (path) | reference in place, no clone |
| `git-subdir` (url + path + ref/sha) | clone pinned, use the named subdir |

**Operations:**
- **add** — resolve source → clone (or reference) → read
  `.claude-plugin/marketplace.json` → validate → write the `known_marketplaces.json`
  entry. The marketplace *name* comes from the manifest.
- **remove** — unregister and delete the clone (never touches a `directory`
  source's contents).
- **list** — enumerate registered marketplaces.
- **refresh** — `git pull --ff-only` the clone, update `lastUpdated`.
- **browse** — parse the manifest's `plugins[]` into
  `{name, description, author, category, source, homepage}`. This is "explore
  plugins in a marketplace."

**Fetching = shell out to `git`.** serf targets developer machines where `git` is
present, and Claude Code itself shells to git. This avoids a heavyweight `go-git`
dependency. If `git` is absent, add/install/upgrade fail with a clear, actionable
error (and `serf-doctor` reports it — §13). `git.go` centralizes clone, pinned
checkout (`git checkout <ref|sha>`), and `pull --ff-only`.

The manifest's optional `renames` map (old plugin name → new) is recorded and used
to follow a plugin rename across an upgrade; a v1 nicety, not required for correct
install.

---

## 7. Plugin lifecycle

A marketplace plugin entry carries a `source` that is either a string
(`./subdir`, relative to the marketplace root) or an object (`git-subdir`,
`github`, or `git`). **Install** resolves it and materializes into
`cache/<marketplace>/<plugin>/<version>/`:

| Plugin source | Materialization |
|---|---|
| `./subdir` (string) | copy from the cloned marketplace repo |
| `git-subdir` {url,path,ref,sha} | clone pinned → copy subdir → discard clone |
| `github` / `git` | clone (pinned to sha if given) into cache |
| `npm` | **deferred** — parsed, warned "unsupported in v1", no install |

Install then verifies `<dir>/.claude-plugin/plugin.json` parses, records `version`
(manifest `version`, or `unknown`) and resolved `gitCommitSha`, and writes the
registry entry with `enabled:true, autoUpgrade:false`.

**Verbs** (the full requested set), all in `internal/plugins`, one flock, atomic
writes:

| Verb | Behavior |
|---|---|
| install | fetch + materialize + register |
| upgrade / upgrade --all | re-resolve source, re-fetch to a temp dir, verify, compare sha/version; if changed, atomic-swap to a new version dir, GC the old |
| remove | delete cache dir + registry entry |
| disable / enable | flip `enabled` — no fetch, no delete (installed-but-not-loaded) |
| auto-upgrade on/off | flip `autoUpgrade` (§9 daemon acts on it) |
| list | installed plugins: scope, version, enabled, autoUpgrade, marketplace, broken? |

**Integrity & safety:**
- Materialize → verify manifest → atomic-rename into cache; a failed fetch never
  leaves a half-installed plugin.
- Honor `ref`/`sha` pins for reproducibility; record the resolved sha.
- Broken plugins (missing/invalid manifest) surface in `list` and `doctor`, never
  silently dropped.
- Marketplaces and plugins are arbitrary code — `add` and `install` show the
  source URL and require confirmation (the prior spb design's stance). Installing a
  plugin is a trust decision equivalent to installing any third-party package.
- flock serializes concurrent CLI / hub / TUI mutations.

---

## 8. Loader gating: making enable/disable real

Today serf loads every dir in `SessionConfig.PluginDirs`. The change is small and
lives entirely in the manager: a new `EnabledPluginDirs(scopes…) []string` returns
the **installed-AND-enabled** plugin dirs from the registry (both scopes), and
that set feeds `SessionConfig.PluginDirs`. Disabled plugins are simply omitted.

The `agent/plugin` loader is otherwise **unchanged** — it still just loads the dirs
it is handed; enable-gating never enters the agent module. Explicit `--plugin-dir`
and the existing `pluginDirs` launch-config field remain an *additional* source
(dev / power-user), merged with registry-enabled dirs, so hand-pointed and
marketplace-installed plugins coexist. The hub's current default of scanning
`~/.config/serf/plugins/*` is replaced by "enabled entries from the registry."

---

## 9. Auto-upgrade daemon & the surfaces

### 9.1 Auto-upgrade daemon

Lives in the **hub** (`serf-hub`) — the only persistent process; CLI and TUI are
ephemeral.

- A background goroutine on a configurable interval (proposed default: every ~12h,
  plus once on hub start) refreshes marketplaces (`git pull --ff-only`), then for
  each installed plugin with `autoUpgrade:true` **and** a git-backed source runs
  the normal upgrade flow.
- Failure-isolated: one plugin's failure never blocks others; logged and surfaced
  in `doctor`. A successful upgrade updates the entry's `lastUpdated`.
- Emits an appwire `serf/plugin/updated` notification so both UIs can show
  "X upgraded to vY." Takes effect next session (§12).
- Global on/off + interval in `hub.toml`; per-plugin opt-in via `autoUpgrade`. A
  manual "check now" from any surface runs the same code path.

### 9.2 Shared RPC surface

The hub exposes one method set; web and TUI both call it (as `/credentials` shares
`serf/instance/*`). Methods are declared in `appwire/protocol.go` (the catalog the
protocol doc is generated from) and registered in `cmd/serf-hub/app_rpc.go` via
`HandleTyped`, delegating to a `hubPluginsController` that mirrors
`app_instances.go` (reload-from-disk → mutate → atomic write, mutex; delegates to
`internal/plugins`).

- `serf/marketplace/{list, add, remove, refresh, browse}`
- `serf/plugin/{list, install, upgrade, remove, enable, disable, setAutoUpgrade, doctor}`
- `serf/command/list` (for slash-command autocomplete — §10)
- Notifications: `serf/marketplace/updated`, `serf/plugin/updated`.

### 9.3 Web surface (`cmd/serf-hub`)

Clones the `/credentials` end-to-end pattern (appwire RPC + htmx settings section +
inline controller). A new **"Marketplaces & Plugins"** section in the existing
*Extensions* nav group (`templates/partials/settings.html`), one page, three areas
built from the existing `settings-collection*` / `btn*` / `status-badge`
design-system classes:

1. **Marketplaces** — list (name, source, last-updated) + "Add marketplace"
   (URL / `owner/repo` / path) + per-row refresh / remove.
2. **Browse** — pick a marketplace → its catalog (name, description, category,
   author) with search/filter and an install button showing installed/enabled
   state.
3. **Installed** — per plugin: version, marketplace, scope; enable/disable toggle,
   auto-upgrade toggle, upgrade button (flags update-available), remove; broken
   plugins flagged.

Files: a template partial (modeled on `templates/partials/credentials.html`), a new
`assets/plugins.js` mirroring `assets/launchconfig.js` (thin wrappers over
`SerfAppwire.request`), routing in `web.go`, section registration in
`web_settings.go`.

### 9.4 TUI surface (`cmd/serf-tui`)

Clones the CredentialsPanel / LaunchSettingsPanel overlay pattern: a tabbed
`PluginsPanel` (in `internal/launchconfig` or a new sibling package) modeled on
`LaunchSettingsPanel`'s tabbed structure, with **Marketplaces │ Browse │ Installed**
tabs and single-key actions (CredentialsPanel style): add/remove/refresh;
`enter`=install on Browse; `e`=enable/disable, `u`=upgrade, `a`=auto-upgrade,
`x`=remove on Installed. Opened via a `/plugins` (`/marketplaces` alias) command in
`hub_command_registry.go`; wired through `focus_trap.go` precedence,
`hub_update_config.go` handlers, and `Cmd*` wrappers in `launchconfig_client.go`
calling the same RPCs as web.

### 9.5 CLI (`cmd/serf/plugin/`)

The `serf plugin` tree (sp4 design), thin over the same manager:
`marketplace add|remove|list|refresh`, `install <plugin>@<mkt>`,
`upgrade [--all]`, `remove`, `enable`, `disable`, `list`, `doctor` — each with
`--json` and `--scope user|project`.

---

## 10. Slash-command execution

The one component type serf cannot load today. serf's existing prompt-template
engine (`agent/section_resolver.go`) is a *system-prompt assembler* and supports
none of `$ARGUMENTS` / `$1` / `` !`cmd` `` / `@file`, so command expansion is
net-new. Three parts:

**1. Discovery.** `discoverPluginCommands` mirrors the existing
`discoverPluginAgents` (`agent/plugin/agents.go`) almost verbatim: scan
`commands/*.md`, parse frontmatter + body with the existing `frontmatter.Parse`
into `{name, description, argument-hint, allowed-tools, model, body}`. Add a
`Commands` field to `plugin.Instance`; register in the same `session_init.go` load
loop as skills/agents/hooks. Namespaced like skills (`plugin:command`) with
collision handling.

**2. Expansion engine** (net-new, ~200–400 LOC, self-contained). Given a command
and a raw argument string, produce the expanded prompt:
- `$ARGUMENTS` → the full argument string; `$1`..`$9` → positional (shell-split).
- `` !`cmd` `` → run in the session's execution environment, substitute stdout
  (bounded output, timeout).
- `@file` → inline file contents (session-relative path, bounded).
- Honor `allowed-tools` (restrict that turn's tool set) and `model` (override the
  model for that turn) from frontmatter — subject to §14 verification.

**3. Invocation seam** (shared — this is why per-surface work stays small).
Intercept `/name args` **at the session input boundary** (where input becomes a
turn — `Session.ProcessInput` / `ProcessInputKind`, `agent/session_lifecycle.go`).
If the input matches a loaded plugin command → expand → submit as the user turn
(with that command's model/tools). If it matches a built-in UI control command
(TUI/web) → that wins. Otherwise → today's "unknown command" behavior. Because both
UIs already submit input through this seam, they get execution largely for free;
their only additions are UX — the hub exposes `serf/command/list`, the TUI merges
it into its palette, the web composer gains a `/` menu. Headless `serf /name args`
runs the same interception.

**Precedence & security.** Built-in commands outrank plugin commands; cross-plugin
name collisions are namespaced. `` !`cmd` `` and the command's tools run under
serf's existing execution-environment / permission model — no extra privilege. A
plugin running bash is exactly the trust decision `install` already surfaced.

---

## 11. Bundled marketplaces

On first run (no `known_marketplaces.json` yet), seed the registry with two
standard marketplaces as **pointers**, cloned lazily on first browse:
- `claude-plugins-official` → `github: anthropics/claude-plugins-official`
- `superpowers-marketplace` → `github: obra/superpowers-marketplace`

The seed list is a small embedded Go constant (`seed.go`) — pointers only, not
vendored plugin contents. Seeding is first-run only, gated by the registry file's
existence: removing a seeded marketplace keeps it removed. A
`--no-default-marketplaces` flag / config opt-out lets tests and power users start
clean.

(If offline out-of-the-box operation is later required, the alternative is
vendoring marketplace contents into the binary; explicitly not chosen for v1 —
bigger binary, goes stale.)

---

## 12. Activation semantics

Plugin discovery runs once per session init (`agent/session_init.go`); there is no
file-watch or hot-reload today. Therefore install / enable / disable / upgrade take
effect on the **next session**. The hub can offer "reload" by spawning a fresh
session; the CLI notes it; this matches Claude Code's own restart-to-load behavior.
Live hot-reload is out of scope for v1 and can be added later without changing the
store or the manager API.

---

## 13. Doctoring (`serf-doctor plugins`)

`serf-doctor` is a read-only forensic inspector (`locate` / `transcript` /
`apilog` / `watches` / `tree`), a thin `main` over a checker package, human summary
+ `--json`. Plugin doctoring lands as a new **`serf-doctor plugins`** subcommand
backed by health-check logic in `internal/plugins/doctor.go` (so a
registry/manifest schema change flows through or fails to compile — matching the
tool's philosophy), plus a `serf plugin doctor` alias for discoverability.
Read-only, no mutation. Not session-scoped: takes `--scope` / `--project` and
`--json` rather than a session selector.

Checks, each OK/WARN/FAIL with a remediation hint:
- **Registry ↔ disk drift** — orphaned entries (registry points at a missing dir),
  orphaned cache dirs (no registry entry), registry version ≠ manifest version.
- **Marketplaces** — clone exists + is a valid git repo, `marketplace.json`
  parses, staleness (age since last pull).
- **Component validity** (per installed+enabled plugin) — `hooks.json` parses +
  referenced scripts exist & executable + `${CLAUDE_PLUGIN_ROOT}` resolves;
  `.mcp.json` parses; skills / agents / commands frontmatter valid. Surfaces
  serf's existing load-time warnings proactively.
- **Auto-upgrade sanity** — plugins marked `autoUpgrade` but backed by a non-git
  `directory` source (cannot upgrade).
- **Environment** — `git` on PATH; store is writable.

---

## 14. Plan-time verifications (do not assume)

Two seams the exploration did not confirm; verify before relying on them, and
degrade gracefully if absent rather than blocking the feature:
- **Per-turn `model` override** for a slash command's `model` frontmatter.
- **Per-turn tool restriction** for a slash command's `allowed-tools` frontmatter.

If either seam does not exist, that frontmatter field is parsed, warned, and not
enforced in v1 (the command still runs with the session's default model / tools).

---

## 15. Phasing

One design spec, built in independently-shippable phases. Each phase gets its own
implementation plan (writing-plans).

| Phase | Deliverable | Depends on |
|---|---|---|
| **P1 Backend core** | `internal/plugins`: registry, `known_marketplaces.json`, source resolution, git fetcher, Install/Upgrade/Remove/Enable/Disable/List/UpdateAll, flock + atomic IO | — |
| **P2 CLI + gating + bundling** | `serf plugin` tree; `EnabledPluginDirs` feeding the loader; first-run seeding | P1 |
| **P3 Slash commands** | `discoverPluginCommands` + `Instance.Commands`; expander; session-boundary interception; `serf /name` headless | independent of P1 (parallelizable) |
| **P4 Auto-upgrade** | hub daemon + `hub.toml` config + `serf/plugin/updated` + manual check-now | P1 |
| **P5 Web** | appwire methods + `hubPluginsController` + Marketplaces & Plugins page + `plugins.js` | P1–P4 RPCs |
| **P6 TUI** | `PluginsPanel` + command wiring | P1–P4 RPCs |
| **P7 Doctoring** | `serf-doctor plugins` + `internal/plugins/doctor.go` + `serf plugin doctor` alias | P1 |

---

## 16. Testing strategy

TDD, real files, real `git`, no mocks (the sp4 stance and project rules):
- **Backend** — table-driven over `t.TempDir()` + **local git-repo fixtures** for
  each source type (`github` / `git` / `git-subdir` / `directory`); **no network**;
  concurrency tests for flock / atomic writes; `t.Skip` if `git` absent.
- **Slash commands** — expander unit tests (`$ARGUMENTS`, `$1..$9`, `` !`cmd` ``,
  `@file`, `allowed-tools`, `model`); discovery tests mirroring the agents-loader
  tests; an install-fixture-plugin-with-a-command → invoke → assert-expanded-turn
  e2e.
- **Loader gating** — enabled/disabled ⇒ session sees / does-not-see the plugin.
- **Auto-upgrade** — point at a local fixture, advance its HEAD, one tick ⇒ assert
  upgrade + notification.
- **Doctor** — fixture store with injected drift (orphan entry, orphan dir, broken
  manifest, non-git autoUpgrade) ⇒ assert findings.
- **UIs** — e2e scenario cards driving the real web + TUI against a freshly built
  instance: add marketplace → browse → install → enable/disable → upgrade → remove,
  with falsifiable assertions.
- **Pristine output** — capture + assert load-time warnings for broken plugins.

---

## 17. Summary of new/changed files

**New:** `internal/plugins/{registry,marketplaces,source,install,enabled,doctor,git,locks,atomic,seed}.go`;
`cmd/serf/plugin/`; `cmd/serf-hub/assets/plugins.js`; a hub template partial;
`cmd/serf-tui` PluginsPanel; `agent/plugin/commands.go` (discovery); the
slash-command expander package.

**Modified:** `agent/plugin/plugin.go` (`Instance.Commands`), `agent/session_init.go`
(register commands; consume enabled dirs), `appwire/protocol.go` (method catalog),
`cmd/serf-hub/{web.go,web_settings.go,app_rpc.go}` + new controller,
`cmd/serf-tui/{hub_command_registry.go,focus_trap.go,hub_update_config.go,hub_model.go}`,
`cmd/serf-doctor/main.go` (`plugins` subcommand), `cmd/serf/main.go` (`plugin`
command), `hub.toml` config (auto-upgrade).
