# Session Plugin Selection — Design

Date: 2026-08-26
Status: Approved
Branch: `main`

## Summary

Evener currently loads every valid plugin supplied through `--plugin-dir`, then
every valid installed plugin whose global registry entry is enabled. Users can
change that global registry state, but they cannot narrow the plugin set for one
new session.

This design adds an explicit, one-session plugin allow-list at startup. The web
launcher and terminal UI preview the exact plugins that would otherwise load,
show an always-visible count, and let the user select the set. The CLI gains the
same control:

```text
evener --enabled-plugins=superpowers,doctoring-evener "..."
evener --enabled-plugins= "..." # load no plugins
```

Omitting the field preserves current behavior. Once present, the list means
"load exactly these otherwise-loadable plugin manifest names." Globally disabled
plugins remain unavailable. The selected effective plugin directories become
part of the created session snapshot, so resumes, forks, and delegates keep the
same plugin set. The next new-session form resets to the current default set.

Approved visual artifacts:

- [placement alternatives](../mockups/2026-08-26-plugin-session-toggle-approaches.html)
- [desktop, mobile, and terminal surfaces](../mockups/2026-08-26-plugin-session-toggle-surfaces.html)

## Goals

- Show every valid plugin a new session would load after applying explicit
  directories, the installed-plugin registry, global enablement, validation,
  precedence, and manifest-name deduplication.
- Let users restrict one new session to an exact plugin set without changing
  global plugin state.
- Apply one selection to all contributions from a plugin: skills, agents,
  commands, hooks, MCP servers and tools, diagnostics, and sandbox roots.
- Preserve current startup behavior when no selection is supplied.
- Make an explicit empty set distinct from an omitted selection.
- Keep web, mobile, TUI, direct CLI, and hub-spawned sessions on the same
  resolution contract.
- Keep the effective set stable across the created session's resumes, forks,
  and delegates.
- Surface invalid candidates and stale explicit selections honestly.

## Non-goals

- Enabling a globally disabled plugin for one session.
- Changing installation, marketplace, auto-upgrade, or global enable/disable
  behavior.
- Saving plugin selections as global or project launch defaults.
- Selecting individual contributions inside a plugin.
- Changing standalone skill directories, evener-wide commands, or ordinary MCP
  configuration.
- Mutating the plugin set of an already-running session.
- Optimizing away the loader's existing validation pass.

## Current behavior

`internal/plugins.Manager.EnabledPluginDirs` receives explicit plugin directories
and then appends installed registry entries with `Enabled=true` and
`Broken=false`. It fully parses each candidate through `agent/plugin.Load`, keeps
the first candidate for each manifest name, gives explicit directories
precedence over registry entries, warns about invalid or duplicate candidates,
and returns directories only.

Both direct `evener` runs and `evener serve` call that function before
constructing `agent.SessionConfig`. Hub launch configuration can add
`plugin_dirs` at global, trusted-repository, project, and per-launch layers;
those lists append across layers and become repeated `--plugin-dir` arguments.

During session initialization, Evener loads plugin skills, agents, commands,
hooks, and MCP configuration before it finalizes tools and the system prompt.
`SessionConfig.PluginDirs` also supplies sandbox infrastructure roots. Filtering
inside `initPlugins` would therefore be too late: excluded directories would
still receive sandbox access, and other startup stages could already have seen
them.

The web and TUI plugin managers operate on the global installed-plugin registry.
They do not include arbitrary `plugin_dirs`, and changing an Installed row's
Enabled state changes every future session. The launch UIs can add directories
but cannot subtract a plugin.

## Product decisions

### One-session lifetime

The selection applies to the newly created session. The launch UI resets after a
successful start. The created session stores the effective filtered directories,
so its descendants and later resumes do not re-evaluate the current global
registry.

### Explicit allow-list

An allow-list is safer than a deny-list for this feature. After the user edits
the selection, a plugin installed between Preview and Start cannot enter the
session unnoticed.

The three states are:

| Wire state | Meaning |
|---|---|
| `enabledPlugins` omitted | Use every current otherwise-loadable plugin |
| `enabledPlugins: []` | Load no plugins |
| `enabledPlugins: ["a", "b"]` | Load exactly `a` and `b` from the otherwise-loadable set |

The CLI equivalents are an omitted flag, `--enabled-plugins=`, and a
comma-separated value.

### Manifest name as identity

The allow-list uses the plugin manifest name because the loader already treats
that name as the runtime uniqueness boundary. Registry references and directory
paths remain source metadata for display and diagnostics; they are not separate
runtime identities.

If two candidates declare the same name, the existing first-wins precedence
decides which candidate the name selects. An explicit directory continues to
win over a registry entry.

### Global state remains authoritative

The candidate set contains:

1. explicit plugin directories from the resolved launch configuration; then
2. installed, globally enabled registry plugins.

Globally disabled and broken registry entries do not become selectable. The
one-session allow-list cannot override global disablement.

## Data model

### Launch layer

Add a presence-sensitive field to the wire and internal launch layer:

```go
// nil means inherit the default set; non-nil empty means no plugins.
EnabledPlugins *[]string `json:"enabledPlugins,omitempty"`
```

The internal launch layer carries the same pointer-to-slice distinction. This
field is per-launch only:

- the launch schema reports `PerLaunch=true`;
- `DefaultableLayers` is empty;
- global, repository, and project layer writes reject it;
- persistent TOML does not serialize it.

The highest present per-launch value replaces rather than appends. This differs
intentionally from `plugin_dirs`, which continue to append.

### Preview protocol

Add a hub RPC:

```text
evener/plugin/preview
```

Request:

```go
type PluginPreviewParams struct {
    CWD             string             `json:"cwd"`
    LaunchOverrides *LaunchConfigLayer `json:"launchOverrides,omitempty"`
}
```

Response:

```go
type PluginPreviewResponse struct {
    Plugins     []PluginLaunchCandidate `json:"plugins"`
    Diagnostics []PluginDiagnostic      `json:"diagnostics,omitempty"`
}

type PluginLaunchCandidate struct {
    Name         string   `json:"name"`
    Version      string   `json:"version,omitempty"`
    Description  string   `json:"description,omitempty"`
    Source       string   `json:"source"` // installed | directory
    Marketplace  string   `json:"marketplace,omitempty"`
    Path         string   `json:"path,omitempty"`
    Selected     bool     `json:"selected"`
    SkillCount   int      `json:"skillCount"`
    AgentCount   int      `json:"agentCount"`
    CommandCount int      `json:"commandCount"`
    HookCount    int      `json:"hookCount"`
    MCPCount     int      `json:"mcpCount"`
}
```

The exact diagnostic shape should reuse existing launch diagnostic conventions:
field/source, plugin name or path when known, and a safe message. Responses never
include component bodies, command lines, credentials, or environment values.

Preview uses the same resolver as startup. It parses plugin files but does not
execute hooks, start MCP processes, or register tools.

### Manager resolution result

Refactor the current directory-only manager result into a shared resolver that
returns validated candidate metadata and selected effective directories. Its
logical input is:

```text
explicit directories + global registry + optional enabled manifest names
```

Its ordered stages are:

1. enumerate explicit candidates;
2. enumerate globally enabled, non-broken registry candidates;
3. parse candidates with the existing plugin loader;
4. preserve first-wins manifest-name deduplication;
5. if the allow-list is omitted, select every valid winner;
6. if present, validate that every requested name has a valid winner;
7. return candidate metadata, diagnostics, and selected directories.

`EnabledPluginDirs` may remain as a compatibility wrapper around this resolver.
Direct CLI startup, `serve`, preview, effective listing, and hub command catalog
construction must call the shared contract rather than reimplementing its
ordering or validation.

## End-to-end data flow

```text
Web or TUI new-session form
    |
    | cwd + current launch overrides
    v
evener/plugin/preview
    |
    v
resolve launch layers -> append plugin_dirs
    |
    v
shared plugin resolver
    | explicit dirs first
    | globally enabled registry entries second
    | parse + validate + dedupe by manifest name
    v
candidate list + diagnostics
    |
    | user edits selection
    v
thread/start launchOverrides.enabledPlugins = explicit [] or names
    |
    v
hub resolves launch layers again and spawns evener serve
    |
    | --plugin-dir ...
    | --enabled-plugins=name,... (including an explicit empty value)
    v
serve reruns shared resolution and validates exact requested names
    |
    v
filtered SessionConfig.PluginDirs
    |                    |
    |                    +-> sandbox infrastructure roots
    v
session snapshot -> plugin initialization
                    skills / agents / commands / hooks / MCP
```

The second resolution at Start is authoritative. Preview is an informed UI, not
a security boundary.

## CLI

### Startup flag

Add this top-level flag to direct run and `serve`:

```text
--enabled-plugins <name,...>
    load exactly these otherwise-enabled plugins for this session;
    an empty value loads none
```

Use a presence-aware flag type. A default Go string cannot distinguish an
omitted flag from `--enabled-plugins=`.

Names are trimmed; empty elements inside a non-empty list, duplicates, and
invalid manifest-name syntax are usage errors. Preserve user order only for
error reporting. Runtime load order remains candidate order, so selection does
not change plugin precedence.

### Effective listing

Extend the existing command:

```text
evener plugin list --effective [--plugin-dir DIR ...] [--json]
```

Without `--effective`, `evener plugin list` keeps its installed-registry
semantics. Effective mode prints the validated, deduplicated candidates that a
new direct CLI session would load by default, including source and component
counts plus diagnostics. Repeated `--plugin-dir` values use the same explicit
precedence as startup.

This command does not execute plugins, hooks, or MCP servers. Its JSON output is
the stable machine-readable way to obtain names for `--enabled-plugins`.

## Web UI

### Placement

Add an always-visible summary directly below the desktop Working directory /
Model / Effort row and above Advanced options:

```text
Plugins for this session
5 of 6 will load · session only                                      v
```

This choice is capability- and security-relevant, so it must not live only in
the already-long Advanced options panel.

### Expanded desktop disclosure

The disclosure contains:

- a Filter plugins field;
- All and None actions;
- one labeled switch per candidate;
- manifest name as the primary label;
- marketplace reference or directory path as source metadata;
- concise component counts;
- visible "off for session" state;
- nonfatal preview diagnostics with a details affordance.

The control follows existing switch accessibility rules. Every switch has a
visible name and `aria-checked`; status never relies on color alone.

### State transitions

The launcher maintains two distinct modes:

- **default:** preview rows appear selected, but `enabledPlugins` remains omitted;
- **explicit:** the first toggle, All, or None action materializes an explicit
  list, including `[]` for None.

Changing `cwd`, `pluginDirs`, or another input that affects launch resolution
refreshes Preview. In default mode, the new candidate set becomes selected. In
explicit mode, surviving selected names stay selected; removed or invalid names
remain a blocking stale-selection error until the user refreshes or changes the
selection. Newly appearing candidates remain unselected.

A successful Start clears the explicit selection and returns the singleton
launcher to default mode. Failed Start preserves it for correction.

### Mobile

Below the existing Harness, Working directory, Reasoning effort, and Access mode
rows, add:

```text
Plugins                                                   5 of 6  >
```

The row opens a sheet/dialog. Unlike the existing single-choice setting sheets,
the plugin sheet stays open across toggles and provides Done and Cancel. It
contains the same filter, All, None, row metadata, and diagnostics as desktop.
It restores focus to the Plugins row on close and meets the existing minimum
touch-target rules.

### Slash-command catalog

The hub's current `evener/command/list` is global and can include commands from a
plugin excluded by the active session. When a session is active, the web command
palette filters plugin command descriptors against
`Thread.Diagnostics.Plugins`. If diagnostics are temporarily unavailable, it
fails closed for plugin command suggestions while retaining built-in and
user-global commands. The session itself remains authoritative for slash-command
expansion.

## Terminal UI

Add a focusable field after Dir and before Prompt:

```text
  Dir:      ~/code/evener
> Plugins:  5/6 enabled
  Prompt (optional):
```

Enter opens a dedicated 80-column `Plugins for this session` overlay. Do not put
selection inside the global `/plugins` manager; that surface changes persistent
registry state. Do not bury it in the already-long generic launch-overrides
list.

The overlay uses the existing picker and overlay conventions:

```text
> [x] superpowers
  [x] doctoring-evener
  [ ] frontend-design
```

Keys:

- Up/Down: move;
- typing: filter by name, source, and description;
- Backspace: edit filter;
- Space: toggle selected row;
- `a`: select all visible candidates;
- `n`: select none;
- Enter: apply and close;
- Escape: cancel and restore the prior selection.

The footer exposes the active keys through `ActionBarForWidth`. Long lists use a
bounded scrolling window instead of rendering every row. Text markers carry
state independently of color.

The TUI sends the same `LaunchConfigLayer.EnabledPlugins` through
`ThreadStartParams.LaunchOverrides` and clears the one-shot value after a
successful start.

## Defaults and concurrency

- Existing users see no runtime behavior change until they edit the selection or
  pass the flag.
- Preview always reflects the current global registry and resolved plugin
  directories at request time.
- With selection untouched, a plugin installed before Start joins the default
  set, matching current behavior.
- With an explicit allow-list, a plugin installed after Preview does not join.
- Global enable/disable or plugin removal does not mutate an already-created
  session's snapshot.
- The Start path validates again; it never trusts preview freshness.

## Error handling

### Preview errors

If Preview fails, show `Couldn't inspect plugins` with retry. Keep an untouched
default launch available because omission preserves current behavior, but
disable selection editing until inspection succeeds. Never display a fabricated
zero count.

Nonfatal candidate diagnostics include invalid explicit directories, broken
registry entries already excluded by registry state, and duplicate losers. The
summary reports diagnostic presence without turning warnings into selected rows.

### Strict explicit selection

A present allow-list is a contract. Startup fails before session creation when:

- a requested manifest name is unknown;
- the selected winner disappeared;
- the selected candidate no longer parses;
- global state disabled the selected registry plugin;
- the selected name no longer wins deterministic candidate resolution.

The error names every affected manifest and tells the UI to refresh the plugin
selection. It contains no secrets or component payloads.

An explicit empty list is valid and must not collapse to omission during JSON,
TOML-layer conversion, argv generation, or flag parsing.

### Startup rollback

Resolution and exact-set validation occur before sandbox construction, session
IDs, transcripts, hooks, MCP processes, or any other durable session state. A
selection failure leaves no partial session.

Failures after the effective directory list is frozen use existing session
startup and rollback behavior.

## Testing

Before adding or changing tests, implementation must read
`docs/developing-evener/testing.md`. Default tests use local fixtures and
scripted boundaries; they never install from the network or call a provider.

### Resolver tests

- explicit directories precede registry entries;
- explicit candidates win manifest-name collisions;
- registry order remains deterministic;
- globally disabled and broken registry entries are unavailable;
- omitted selection chooses every valid winner;
- explicit empty selection chooses none;
- explicit names choose exactly those winners without changing load order;
- duplicate list names and malformed CLI elements are rejected;
- unknown or newly invalid selected names fail strictly;
- invalid unselected candidates remain diagnostics;
- source metadata and component counts match the loaded manifest;
- path and manifest data in diagnostics are safely bounded.

### Launch and wire tests

- omitted, empty, and non-empty lists round-trip distinctly through appwire;
- per-launch conversion preserves pointer presence;
- global/project layer persistence rejects the field;
- argv emits no flag for omission, `--enabled-plugins=` for empty, and one
  canonical comma-separated flag for names;
- direct run and `serve` resolve the same effective directories;
- `evener plugin list --effective --json` matches the shared resolver.

### Session integration tests

Build fixture plugins that contribute skills, agents, commands, hooks, and MCP
servers. Prove an excluded plugin contributes none of them, emits no
`PLUGIN_LOADED` event, runs no startup hook, starts no MCP process, and grants no
sandbox infrastructure root. Prove selected sibling plugins still load.

Prove the effective filtered directories persist through resume, fork, and
child/delegate construction even after global registry state changes.

### Hub tests

- Preview resolves the same cwd and launch overrides as Thread Start;
- Preview and Start agree without concurrent changes;
- registry or filesystem changes between them are caught by Start revalidation;
- selection failure occurs before spawn;
- explicit empty selection reaches `serve` intact;
- protocol catalog, both routers, generated docs, and TypeScript types include
  the new method and field.

### Web tests

- desktop summary and disclosure counts;
- default versus explicit state;
- All, None, individual toggle, and filtering;
- cwd and plugin-directory refresh behavior;
- stale selected-name blocking state;
- preview retry without a fabricated empty result;
- successful-start reset and failed-start preservation;
- mobile sheet Done/Cancel and focus restoration;
- switch names, `aria-checked`, keyboard operation, and diagnostic alerts;
- command palette hides commands from plugins absent in active thread
  diagnostics;
- desktop, narrow pane, phone, and touch-target geometry guards.

### TUI tests

- new Plugins field focus order and summary;
- open, filter, move, toggle, All, None, apply, and cancel;
- explicit empty versus omitted request payload;
- one-shot reset after successful start;
- stale-selection and Preview errors;
- 80-column and narrow-terminal wrapping;
- bounded scrolling for long lists;
- text-only state remains understandable without color.

### Gates

Run the repository's standard gates:

```text
make lint
make vet
make test
make test-web-browser
```

Run `npx biome check --write` on touched frontend files before the frontend gate.
Protocol and generated-output checks remain part of the normal generation and
lint flow.

## Alternatives considered

### Default-minus-disabled deny-list

This preserves default behavior with a smaller payload, but a plugin installed
between Preview and Start can enter the session without review. Rejected in
favor of an exact allow-list once the user edits the selection.

### Advanced options only

This reuses generic launch controls but hides a security-relevant choice in a
long panel. Rejected in favor of an always-visible summary and on-demand detail.

### Mandatory Start-time preflight

This guarantees visibility but adds a dialog to every launch, even when defaults
are correct. Rejected as unnecessary friction.

### Persistent project or global defaults

This would require subtraction and inheritance semantics across launch layers
and would overlap the global plugin manager. Rejected from this design; the user
selected one-session behavior.

### Registry reference as runtime key

`plugin@marketplace` disambiguates registry storage but cannot identify an
explicit plugin directory and does not match loader deduplication. Rejected in
favor of manifest name, with source metadata retained for display.

## Implementation boundaries

- Centralize enumeration, validation, deduplication, selection, metadata, and
  diagnostics in one manager resolver.
- Apply selection before sandbox roots and session construction.
- Keep global plugin mutation RPCs and settings surfaces unchanged.
- Keep `plugin_dirs` append semantics unchanged.
- Do not add contribution-level selectors.
- Do not make Preview execute hooks or connect MCP servers.
- Do not silently downgrade an explicit allow-list when selected names fail.
- Preserve unrelated launch, resume, and plugin behavior.
