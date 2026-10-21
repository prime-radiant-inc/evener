# Plugins

Use a plugin to package extensions and choose where to load them. For a reusable
prompt alone, start with [skills and commands](skills.md). A plugin may contain
executable commands, hooks, and MCP servers: review it as code before loading it.

## Load a local plugin

You need an Evener installation with a working provider. For the fixture files,
use the `git clone` command in [Optional disposable exercise](getting-started.md#optional-disposable-exercise),
then run the commands below from the resulting `evener-docs` directory on the
session host. You do not need to build Evener; use your installed executable.

The second command makes a real provider call and may incur charges. It uses a read-only sandbox, but
confinement does not remove the need to trust executable extensions. Replace
`provider/model` with an available model from your configured provider.

```bash
evener plugin list --effective --plugin-dir ./docs/examples/extensions/hello-plugin
evener --model provider/model --sandbox read-only --plugin-dir ./docs/examples/extensions/hello-plugin "/hello-plugin:hello world"
```

The first command inspects the effective launch inventory. If `hello-plugin` is
absent, check the fixture path and loading diagnostics before starting the model.
The second starts a session with the fixture and invokes its command with `world`
as the argument. Expect the startup-hook warning and expanded
instructions asking for a greeting to `world`. Inspect the session input to verify
expansion; the exact model response is not deterministic.

To author the same package, create this layout. The
[complete files](examples/extensions/README.md) are copyable examples.

```text
hello-plugin/
  .claude-plugin/plugin.json
  commands/hello.md
  hooks/hooks.json
  agents/reviewer.md
```

The manifest needs a name:

```json
{"name":"hello-plugin","version":"0.1.0","description":"A local extension example"}
```

`commands/hello.md` contains:

```markdown
---
description: Greet the named person.
argument-hint: name
---
Greet $ARGUMENTS in one sentence. Do not use tools.
```

`--plugin-dir` is repeatable. You can also configure `plugin_dirs` in Evener
configuration. Explicit directories are code-trust choices; they are not
marketplace installations. To stop loading one, remove its flag or configuration
entry. Start a new session after changing plugin declarations. Existing sessions
retain their loaded inventory; do not delete files they may still read.

## Install from a marketplace

A marketplace is a catalog of plugins. Adding a catalog does not install every
entry. Choose a source you have reviewed. In the following shell commands,
replace `OWNER/REPOSITORY`, `MARKETPLACE`, and `PLUGIN` with the real source and
names returned by list and browse; these are placeholders, not bundled names.
Put options before positional arguments.

```bash
evener plugin marketplace add --yes OWNER/REPOSITORY
evener plugin marketplace list
evener plugin marketplace browse MARKETPLACE
evener plugin install --yes PLUGIN@MARKETPLACE
evener plugin list
evener plugin list --effective
```

`--yes` confirms trust without prompting. Do not use it for an unreviewed source.
A marketplace source may be a supported repository URL, `owner/repo`, or a local
path. Installed state lives under `$XDG_CONFIG_HOME/evener/plugins/`, with
`~/.config/evener/plugins/` as the fallback. Use the CLI rather than editing the
registry or cache directly.

Verify both installation (`plugin list`) and launch selection (`plugin list
--effective`). A plugin can be installed but disabled. Use:

```bash
evener plugin disable PLUGIN@MARKETPLACE
evener plugin enable PLUGIN@MARKETPLACE
evener plugin upgrade PLUGIN@MARKETPLACE
evener plugin remove PLUGIN@MARKETPLACE
```

Installation enables the plugin by default and leaves auto-upgrade off.
Disable changes default enablement; remove uninstalls. Neither rewrites already
running sessions. Per-launch selection can explicitly select an installed plugin
whose default is off. An omitted selection uses defaults; a present selection
uses exactly its named candidates, including an empty selection for none.

Explicit directories are enumerated before installed candidates. Duplicate
manifest names use the first valid manifest, even when its unrelated component
later fails. A broken plugin produces diagnostics rather than hiding healthy
plugins. If installation or loading fails, run `evener plugin doctor`.
Upgrades publish new directories without replacing older published revisions.
Sessions, including stopped sessions and frozen delegates, can retain those
older paths and all their companion files even when the installed registry
points to a newer revision or no metadata was saved yet. Neither hub startup
nor `evener plugin gc` removes published revisions, so they accumulate on disk;
`gc` only reclaims leftover marketplace clones. Read its output before assuming
cleanup succeeded. Automatic marketplace-name migration rekeys logical ownership
without moving published revisions or changing valid recorded install paths.
Same-source alias merges follow that rule too. Recovery of an older interrupted
migration keeps surviving old paths, and reconciles a missing recorded path only
when its existing marker and physical ownership prove an already-moved destination.
New revisions may use the new marketplace namespace.
Explicit marketplace rename also preserves every published package path while
changing logical ownership. Rename-back and reuse of a name with only retained
cache packages are allowed without deleting or adopting those packages. Live
marketplace names, leftover clones and orphan installed-registry entries still
block a rename. Subsequent installs and upgrades publish fresh immutable paths,
including when the reused name has an occupied revision path. Marketplace clone
moves keep their existing source protection and rollback behavior.
Explicit plugin uninstall and marketplace removal retain their deletion semantics.
Externally edited directory sources remain live directories, not managed snapshots. `evener plugin marketplace remove
MARKETPLACE` removes a marketplace explicitly. Deleted selections are not
recovered by this retention rule.

## Git metadata and managed content

Managed installs publish only plugin content. A fetched or copied source is
prepared in the store's private `publishing` area, its `.git` entry is removed,
the copied tree skips `.git` entries and refuses a symlink into Git metadata,
and the prepared tree is audited before it enters `cache`. Bundled publication
audits its embedded payload the same way. A copied `directory` source (no SHA)
publishes a fresh revision on reinstall rather than reusing a single directory.
A copied source that selects a Git administration directory is refused before
copy, as is a relative or git-subdir path containing `.git`; in-place
directory-marketplace sources stay user-owned and are not copied. Plugin Git
commands ignore ambient `GIT_DIR`/`GIT_WORK_TREE` repository selection. Without
Git, detection is signature-based (HEAD ref or object id, `[core]`, object
layout), so a corrupt or nonstandard administrative layout may not be detected;
an unverifiable admin-looking directory is refused.

`store-format.json` records whether a whole-store audit found no Git metadata
under `cache` and `bundled`. Missing or unverified state keeps today's
per-spawn masking. A completed audit moves only entries named `.git` into the
private `managed-git-archive` area; it never moves a pointer's target, an
ordinary symlink target, or any other content path. A `.git` pointer into an
ordinary directory inside a managed root is left in place and the store stays
unverified. Migration runs from updated plugin writers under the store and
bundled locks, is retried on the next write after a failure, and never rewrites
the registry or a saved session path. Older hub or CLI writers that install
cache revisions must be updated before relying on the clean state; older
session daemons keep reading the same content paths.

## Maintain installed plugins

| Command | Purpose |
| --- | --- |
| `evener plugin marketplace refresh MARKETPLACE` | Refresh the catalog before browsing new entries. |
| `evener plugin list --json` | Inspect installed state as JSON. |
| `evener plugin upgrade --all` | Explicitly upgrade all installed plugins, regardless of auto-upgrade opt-in. |
| `evener plugin auto-upgrade PLUGIN@MARKETPLACE` | Opt a plugin into automatic upgrades. Review the source before allowing future code updates. |
| `evener plugin auto-upgrade --off PLUGIN@MARKETPLACE` | Disable that opt-in. |
| `evener plugin check-now` | Run one upgrade pass for opted-in plugins. |
| `evener plugin doctor` | Diagnose the store without repairing it. |
| `evener plugin gc` | Remove leftover marketplace clones without deleting published plugin revisions. |

## Package format reference

| Component | Location and behavior |
| --- | --- |
| Manifest | `.claude-plugin/plugin.json`, falling back to `.codex-plugin/plugin.json` only when the first is absent. A malformed first manifest does not fall back. |
| Name | Required, lowercase alphanumeric words separated by single hyphens. No leading or trailing hyphen. |
| Descriptive metadata | `version`, `description`, `author`, `homepage`, `repository`, `license`, `keywords`. |
| Skills | `skills/<directory>/SKILL.md`, catalog name `plugin:skill`. See [skill format](skills.md#skills). |
| Commands | `commands/*.md`, catalog name `plugin:command`. Manifest `commands` can add configured paths. See [command expansion](skills.md#plugin-commands). |
| Agent roles | `agents/*.md`, catalog name `plugin:agent`. Manifest `agents` can add configured paths. |
| Hooks | Manifest path or inline `hooks`, otherwise `hooks/hooks.json`. See [hook discovery](hooks.md#how-evener-discovers-your-hooks). |
| MCP | `.mcp.json` plus inline manifest `mcpServers`, with inline entries overriding file entries. See [MCP](mcp.md). |

The loader expands `${CLAUDE_PLUGIN_ROOT}` and `${PLUGIN_ROOT}` where supported
by the component. Do not assume every manifest field is implemented or that
another agent's plugin behavior transfers to Evener.

## Add a delegate role

Create `agents/reviewer.md` inside your plugin:

```markdown
---
name: reviewer
description: Review a change without editing it.
tools: [Read, Grep, Glob]
---
Inspect the requested change. Report defects with file locations and evidence.
Do not edit files. Report what you could not verify.
```

Load the plugin and start a new session. Ask the agent:

```text
Delegate a review of the current change using the hello-plugin:reviewer role.
Return the delegate's findings and what it did not check.
```

This is a request to the agent, not a shell command. It requires the session to
have delegation available. Verify that a delegate actually starts with that role
and returns a report; a parent response that merely describes a review is not
proof. See [job control](job-control.md) for delegate lifetime and inspection.

Roles require `name` and `description`; the Markdown body supplies instructions.
Optional `model`, `color`, `tools`, and `skills` metadata describe the role.
`tools` accepts a list of strings (`[Read, Grep]`), a comma-separated string
(`Read, Grep`), a single tool name (`Read`), or the scalar `all`.
Use `tools: all` for all tools available to the child. The parser rejects scalar
`*`, `all` or `*` inside a list or comma-separated sequence, and non-string entries.
Claude-style tool names map to Evener names. Tool selection cannot expand the
parent's effective policy. `skills` names skills to preload; preloads have the
frozen delegate/permanent-prompt lifetime, not ordinary skill activation history.

Edit or remove the role file and start a new session to change discovery. Already
created delegates keep their role configuration. To remove the whole example,
omit its plugin directory on future launches and end sessions that still use it.
