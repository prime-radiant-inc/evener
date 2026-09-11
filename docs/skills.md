# Skills and Slash Commands

Evener has two kinds of reusable markdown prompts: **skills** and **slash
commands**. Both are plain text templates. What differs is where they come
from, how they are invoked, and — critically — whether they can execute
shell commands.

## The trust model, up front

A markdown template that can run shell commands is code. Evener's rule:

- **Templates you explicitly installed** (plugins) may execute shell at
  expansion time. Installing a plugin is a trust decision, like installing
  software.
- **Templates evener discovers automatically** (project and user-global
  `skills/` directories, project `.evener/commands/`, and your user-global
  commands directory) never execute shell and never read files at expansion
  time. Anything in a repo you merely cloned is inert text.

This mirrors codex's posture: in codex, skills and custom prompts are always
inert, and skill bodies reach the model only through the model's own
permission-checked tool calls. Claude Code instead allows `!`cmd``
execution in skills and commands, but gates project-sourced content behind a
workspace trust dialog and offers a `disableSkillShellExecution` kill
switch. Evener keeps Claude Code-compatible execution for explicitly installed
plugins, and adopts the codex posture for everything it discovers on its
own.

| Source | Discovered from | `$ARGUMENTS` | `!`cmd`` | `@file` |
|---|---|---|---|---|
| Skill | bundled skills; home `.agents/skills/`; Evener user skills; project `.agents/skills/` and `skills/` (git root→cwd); `skills_dirs`; plugins | no | never | never |
| Evener-wide command | `.evener/commands/` (git root→cwd), `$XDG_CONFIG_HOME/evener/commands/` (`~/.config` fallback) | yes, inert text | never — stays literal | never — stays literal |
| Plugin command | plugins you installed or configured | yes, inert text | executes (10s timeout, output bounded) | inlines files at cwd-relative paths (symlinks followed) |

Argument substitution is safe in every row: `$ARGUMENTS` and `$1..$9` are
always substituted as inert text and can never become a live directive, even
in plugin commands.

## Skills

A skill is a directory containing a `SKILL.md` with YAML frontmatter.
`name` and `description` are required strings. `disable-model-invocation` and
`user-invocable` are optional strict YAML booleans, defaulting to `false` and
`true` respectively. Quoted booleans and loose values such as `yes` are invalid;
Evener marks the skill unavailable and reports a machine-readable diagnostic
with its source and invalid field. Other descriptive metadata remains inert
data. Unsupported behavioral metadata such as `context: fork` is diagnosed but
not acted on.

`allowed-tools` may be one string or an ordered array of strings. Evener
preserves it and diagnoses that it is not enforced: it neither grants nor
restricts tool access.

Live sessions and cold client catalog reads share this discovery order, from
lowest to highest precedence:

1. Skills bundled with Evener.
2. `~/.agents/skills/`.
3. `$XDG_CONFIG_HOME/evener/skills/` (`~/.config/evener/skills/` fallback).
4. Repository root through cwd, at each level `.agents/skills/` then `skills/`.
5. Explicit `skills_dirs` entries, in their configured order.
6. Configured plugin skills, qualified as `plugin:skill`.

Deeper project directories win over shallower ones; `skills/` wins over
`.agents/skills/` at the same level. Without a repository, only cwd is scanned.
Configured roots remain discoverable when a cold session has no recorded cwd.
Plugins use first-valid-manifest reservation: later manifests with the same
plugin name are skipped even if the selected plugin has malformed unrelated
components. Skill discovery does not load those commands, agents, hooks, or MCP
components. Plugin qualification preserves the declared name separately from
the canonical catalog name. Exact names win; a bare suffix resolves only when
it uniquely identifies a plugin skill. Ambiguous suffixes have sorted candidates.

Every collision reports the winner in diagnostic `source` and the displaced
source in `other_source`. A known higher-priority name with invalid metadata
reserves its key as unavailable; it never exposes a permissive lower-priority
skill instead. Missing automatic directories are normal; unreadable sources,
malformed existing skills, and absent configured roots produce diagnostics.
Startup warnings and full session inspection expose diagnostics; source paths
are excluded from completion and metadata inspection entries.

The model advertisement omits unavailable skills and those with
`disable-model-invocation: true`, including the `read_file` fallback when
`use_skill` is absent. User completion independently omits unavailable skills
and those with `user-invocable: false`. Full metadata inspection retains hidden
and unavailable entries, their controls, and both declared and canonical names.
These are advertisement views, not the runtime catalog: lookup retains every
known winner for runtime validation and authorization. Neither hiding nor
advertising a skill grants permissions or establishes activation history.

Portable directories have the same trust policy as other skills: metadata is
discovered, activation reads inert instructions, and subsequent actions still
require normal tool permissions and sandbox access. Skills remain untrusted
input. Evener does not automatically discover `.claude/skills/`, execute skill
templates, substitute shell directives, or grant tools. Raw file reads do not
become tracked skill activations.

When a skill is activated, Evener reads its recorded `SKILL.md` source,
revalidates its current metadata, and delivers the complete instruction body in
a structured, data-safe context. The context includes `base_directory`, the
skill directory against which relative references to scripts, references, or
other collateral are resolved. Referenced resources are loaded lazily with the
ordinary permission-checked tools only when the task needs them; they are not
eagerly included with the skill body.

Skill bodies are inert instructions, not executable templates. Evener performs
no expansion on them: no shell execution, no file inclusion, no argument
substitution. Template-like syntax and context-delimiter text in the body are
delivered verbatim as instruction data.

## Evener-wide slash commands

A evener-wide slash command is a markdown file — frontmatter optional — in one
of two places:

- `<any dir from git root to cwd>/.evener/commands/name.md` (project commands)
- `$XDG_CONFIG_HOME/evener/commands/name.md` or `~/.config/evener/commands/name.md`
  (user-global commands)

The filename is the command name. Names cannot contain whitespace
(invocation parses the name up to the first space, so a spaced name can
never run) or colons (`:` is the plugin-namespace separator; evener skips
such files with a warning). Invoke it by typing `/name args` in a session. Optional
frontmatter: `description`, `argument-hint`, `model`, `allowed-tools` (the
last two are parsed but not enforced; evener warns when they appear).

Expansion substitutes `$ARGUMENTS` and `$1..$9` as inert text. `!`cmd``
spans and `@file` references in a evener-wide command body never execute or
read anything — they remain in the expanded text verbatim except that
argument substitution still applies inside them as inert text — and evener
warns at load time if a evener-wide command contains `!`` spans. If you want
an executable template, package it as a plugin command instead.

**Precedence:** project > user-global > plugin. A evener-wide command shadows
a plugin command of the same bare name; the plugin command stays reachable
as `/plugin:name`. Within project commands, the directory closest to your
cwd wins.

### Client caveats

- Both desktop surfaces intercept their own built-in slash commands before
  your input reaches the session: the TUI has its registry (`/status`,
  `/model`, `/help`, ...) and the web palette has its own, partly different
  set (`/status`, `/model`, `/help`, `/steer`, `/queue`, ...). A command
  whose name collides with a client's built-ins is unreachable in that
  client's typed input — pick another name. Headless input always works.
- The web UI opens its command palette when you type `/` into an empty
  composer. The palette lists plugin and user-global commands (badged by
  source) alongside the built-ins. If the name you typed isn't exactly one
  of the palette's commands, Enter sends it to the session as-is — a fuzzy
  near-miss (say `/stat` for `status`) still reaches your command. Project
  commands invoke through that fallthrough.
- Standalone skills are not command-file entries in the command catalog, but an
  exact `/skill-name` token is recognized by the session when that skill is
  loaded and activates the skill body. Skill names and descriptions are shown
  in the model's skill catalog.

## Plugin commands

Plugins (installed via the marketplace or configured via `plugin_dirs`) may
ship `commands/*.md` files. These are Claude Code-compatible: in addition
to `$ARGUMENTS` substitution, ``!`cmd` `` spans execute in the session
environment and `@file` inlines files at working-directory-relative paths
(the constraint is lexical; symlinks are followed). Only install
plugins you trust — a plugin command's body runs shell commands with the
same permissions as the session.

Plugin commands are namespaced: `/plugin:name`. The bare `/name` form
resolves to the plugin command when no evener-wide command shadows it and no
client built-in intercepts it (the client caveats above apply to plugin
commands too). If two plugins define the same command name, the bare form
resolves to one of them — which one is not guaranteed; use the qualified
form to be sure.

## Security checklist for command authors

- Treat every `.evener/commands/` file in a repo you did not write as
  untrusted text. Evener guarantees it cannot execute, but its contents still
  become prompt text for the model — read it before invoking it.
- Never put secrets in command bodies; they are sent to the model verbatim.
- If you need shell output in a prompt, prefer a plugin command (explicit
  trust) over asking users to paste output manually.
