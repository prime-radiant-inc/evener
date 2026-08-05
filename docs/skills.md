# Skills and Slash Commands

Serf has two kinds of reusable markdown prompts: **skills** and **slash
commands**. Both are plain text templates. What differs is where they come
from, how they are invoked, and — critically — whether they can execute
shell commands.

## The trust model, up front

A markdown template that can run shell commands is code. Serf's rule:

- **Templates you explicitly installed** (plugins) may execute shell at
  expansion time. Installing a plugin is a trust decision, like installing
  software.
- **Templates serf discovers automatically** (project `skills/` and
  `.serf/commands/` directories, your user-global commands directory) never
  execute shell and never read files at expansion time. Anything in a repo
  you merely cloned is inert text.

This mirrors codex's posture: in codex, skills and custom prompts are always
inert, and skill bodies reach the model only through the model's own
permission-checked tool calls. Claude Code instead allows `!`cmd``
execution in skills and commands, but gates project-sourced content behind a
workspace trust dialog and offers a `disableSkillShellExecution` kill
switch. Serf keeps Claude Code-compatible execution for explicitly installed
plugins, and adopts the codex posture for everything it discovers on its
own.

| Source | Discovered from | `$ARGUMENTS` | `!`cmd`` | `@file` |
|---|---|---|---|---|
| Skill | skills bundled with serf; `skills/` dirs (git root→cwd); `skills_dirs`; plugins | no | never | never |
| Serf-wide command | `.serf/commands/` (git root→cwd), `~/.config/serf/commands/` | yes, inert text | never — stays literal | never — stays literal |
| Plugin command | plugins you installed or configured | yes, inert text | executes (10s timeout, output bounded) | inlines files at cwd-relative paths (symlinks followed) |

Argument substitution is safe in every row: `$ARGUMENTS` and `$1..$9` are
always substituted as inert text and can never become a live directive, even
in plugin commands.

## Skills

A skill is a directory containing a `SKILL.md` with YAML frontmatter
(`name` and `description` required, `allowed-tools` optional). Serf
discovers skills from `skills/` directories walking the git root down to
your cwd, from any `skills_dirs` launch-config entries, and from plugins —
layered on top of the skills bundled with serf itself, which form the base
layer everything else shadows. Later sources shadow earlier ones by name.

Skill bodies are loaded as text and injected for the model to follow. Serf
performs no expansion on them: no shell execution, no file inclusion, no
argument substitution.

## Serf-wide slash commands

> Availability: serf-wide commands are specified in
> `docs/superpowers/specs/2026-08-04-serf-wide-slash-commands-design.md` and
> land with that implementation. On older builds only plugin commands exist.

A serf-wide slash command is a markdown file — frontmatter optional — in one
of two places:

- `<any dir from git root to cwd>/.serf/commands/name.md` (project commands)
- `$XDG_CONFIG_HOME/serf/commands/name.md` or `~/.config/serf/commands/name.md`
  (user-global commands)

The filename is the command name — choose a name without spaces (invocation
parses the name up to the first space, so a spaced name can never run).
Invoke it by typing `/name args` in a session. Optional frontmatter:
`description`, `argument-hint`, `model`, `allowed-tools` (the last two are
parsed but not enforced; serf warns when they appear).

Expansion substitutes `$ARGUMENTS` and `$1..$9` as inert text. `!`cmd``
spans and `@file` references in a serf-wide command body never execute or
read anything — they remain in the expanded text verbatim except that
argument substitution still applies inside them as inert text — and serf
warns at load time if a serf-wide command contains `!`` spans. If you want
an executable template, package it as a plugin command instead.

**Precedence:** project > user-global > plugin. A serf-wide command shadows
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
  source) alongside the built-ins, and if your typed `/name` matches
  nothing, Enter sends it to the session as-is. Project commands invoke
  through that fallthrough.

## Plugin commands

Plugins (installed via the marketplace or configured via `plugin_dirs`) may
ship `commands/*.md` files. These are Claude Code-compatible: in addition
to `$ARGUMENTS` substitution, ``!`cmd` `` spans execute in the session
environment and `@file` inlines files at working-directory-relative paths
(the constraint is lexical; symlinks are followed). Only install
plugins you trust — a plugin command's body runs shell commands with the
same permissions as the session.

Plugin commands are namespaced: `/plugin:name`. The bare `/name` form
resolves to the plugin command when no serf-wide command shadows it and no
client built-in intercepts it (the client caveats above apply to plugin
commands too).

## Security checklist for command authors

- Treat every `.serf/commands/` file in a repo you did not write as
  untrusted text. Serf guarantees it cannot execute, but its contents still
  become prompt text for the model — read it before invoking it.
- Never put secrets in command bodies; they are sent to the model verbatim.
- If you need shell output in a prompt, prefer a plugin command (explicit
  trust) over asking users to paste output manually.
