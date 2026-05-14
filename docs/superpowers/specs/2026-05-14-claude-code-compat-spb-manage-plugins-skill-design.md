# SP-B — `manage-plugins` Builtin Skill (Detailed Design)

Date: 2026-05-14
Status: ready for TDD implementation
Parent spec: `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`

## 1. Goal

Provide a builtin skill that teaches the agent how to install, update, and remove Claude Code plugins by manipulating the directories that SP-A discovers. The skill replaces the deferred Go-side marketplace/install tooling (SP3, SP4) with an LLM-driven workflow that uses serf's existing Bash, Read, Write, and WebFetch tools.

This sub-project is markdown only. No Go code beyond a one-line registration in `agent/builtin_skills.go`.

## 2. Skill File

Location: `agent/skills/manage-plugins/SKILL.md`

The file is loaded as a builtin skill (no plugin namespace), so the agent can invoke it as `manage-plugins`.

### 2.1 Frontmatter

```yaml
---
name: manage-plugins
description: Use when the user asks to install, update, remove, list, or inspect Claude Code plugins for serf. Handles marketplaces (cloning marketplace.json catalogs), plugin sources (github/url/git-subdir/directory/npm), and stages plugin directories into ~/.config/serf/plugins/ (user) or <project>/.serf/plugins/ (project).
---
```

The `description` line is what the LLM matches against user requests. It enumerates every triggering verb the user might use ("install", "update", "remove", etc.) and the canonical surfaces.

### 2.2 Body Outline

The body is structured for the LLM, not for human reading. Each section is a procedure that can be invoked directly.

```
## What this skill does

Stage plugin directories under serf's known plugin paths so SP-A's filesystem
discovery picks them up at next session start. There is no registry; presence
in the right directory IS the install state.

## Plugin storage layout

User-scope (default):  ~/.config/serf/plugins/<plugin-name>/
Project-scope:         <project-root>/.serf/plugins/<plugin-name>/

Each <plugin-name>/ must contain .claude-plugin/plugin.json. Match the plugin's
declared `name` in plugin.json to the directory basename for clarity, though
SP-A resolves by manifest `name` regardless.

## When you should invoke this skill

The user asks any of:
  install plugin X
  install <github-shorthand>            (e.g. anthropics/skills)
  install from marketplace Y
  remove plugin X
  uninstall plugin X
  update plugin X
  update all plugins
  list installed plugins
  show plugin sources

## Step-by-step: install a plugin from a marketplace

1. Resolve the marketplace source. Ask the user for one of:
   - A marketplace name they've already cloned (find it under ~/.config/serf/plugins/marketplaces/)
   - A git URL
   - A `owner/repo` GitHub shorthand
   - A path to a local marketplace.json
2. Fetch marketplace.json:
   - For a git URL or GitHub shorthand: run `git clone --depth=1 <url> /tmp/serf-marketplace-XXXX` then read .claude-plugin/marketplace.json
   - For a direct URL to marketplace.json: WebFetch it
   - For a local path: Read it
3. Parse marketplace.json. Find the plugin entry whose `name` matches what the user asked for. Show the user the entry's `description`, `source`, and confirm.
4. Resolve the plugin source per its `source` type:
   - "directory": `cp -R <marketplace-root>/<relative-path> <target>`
   - "github": `git clone https://github.com/<repo>.git --depth=1 <target>` (then `git checkout <ref/sha>` if pinned)
   - "url": `git clone <url> <target>` (with `--depth=1` if no `sha` pin)
   - "git-subdir": git clone --depth=1, then `cp -R <subdir> <target>`, then rm clone
   - "npm": `npm pack <package>` then unpack; or `npm install <package>` and find the install path. Less common; ask if the user wants to proceed.
5. Pick the target directory:
   - Default: ~/.config/serf/plugins/<plugin-name>/
   - If the user said "for this project" or you detect the user is in a project: <project>/.serf/plugins/<plugin-name>/
6. Verify <target>/.claude-plugin/plugin.json exists. If not, abort with a clear error.
7. Tell the user: "Installed <name> at <target>. Restart your serf session for the plugin to load."

## Step-by-step: update a plugin

1. Find the plugin's directory under ~/.config/serf/plugins/ or .serf/plugins/.
2. If it is a git checkout, run `git pull --ff-only` in that directory. Report the result.
3. If it is not a git checkout, ask the user where to re-fetch from (probably the original marketplace), then do the install flow into the same target.
4. Verify the manifest still parses. Tell the user to restart serf.

## Step-by-step: remove a plugin

1. Confirm the user wants to remove plugin <X>.
2. Find its directory under known plugin paths.
3. `rm -rf <dir>` (or move to a backup with `mv` if the user prefers).
4. Tell the user to restart serf.

## Step-by-step: list installed plugins

1. Read ~/.config/serf/plugins/*/ and <project>/.serf/plugins/*/.
2. For each, read its .claude-plugin/plugin.json. Print name, version, description.
3. Note which scope each is in (user / project).
4. Surface any directories that lack a manifest as "broken (no manifest)".

## Safety reminders

- Never `git clone` from a URL the user hasn't seen or approved.
- Marketplaces are arbitrary code repositories. The user should treat installing a plugin like installing any third-party package — read the source if it's untrusted.
- When updating with `git pull`, prefer `--ff-only` so you don't accidentally pull divergent history.
- When removing, double-check the directory path before `rm -rf`.

## What this skill does NOT do

- Maintain a registry of installed plugins (no installed_plugins.json).
- Resolve version constraints from `dependencies` (the field is warn-on-unsupported).
- Persist marketplace state in a known_marketplaces.json (you re-clone if needed).
- Auto-update on a schedule (the user invokes you).
- Enforce permission patterns (deferred).
```

## 3. Registration

`agent/builtin_skills.go` already exposes builtin skills via a registration function (see existing code). SP-B adds one entry pointing to `skills/manage-plugins/SKILL.md`.

Pseudocode for the registration site (adapt to existing pattern):

```go
//go:embed skills/manage-plugins/SKILL.md
var manageRpluginsSkillFile string

// in registerBuiltinSkills():
register("manage-plugins", manageRpluginsSkillFile)
```

The exact mechanism mirrors how other builtin skills register today. SP-B will conform to whatever pattern `agent/builtin_skills.go` already uses; if the file uses a directory walk via `embed.FS`, dropping the new SKILL.md into `agent/skills/manage-plugins/` is sufficient.

## 4. Validation

The skill body must parse as YAML-frontmatter + markdown via `frontmatter.Parse` (existing helper) and produce a `SkillMeta` with non-empty `name` and `description`.

The skill must be available without `--plugin-dir`. After registration it appears in the default skill list returned by `DiscoverSkills` for every session.

## 5. Error Contracts

The skill is content; failures happen at the agent's tool-call layer, not in skill loading. The skill's prose tells the agent what to do when:

- `git clone` fails → report the URL and the error, ask the user to verify it
- Manifest missing post-install → abort, leave the staged files in place, ask the user
- Target directory already exists → confirm overwrite before proceeding
- The marketplace lacks the named plugin → list the marketplace's available plugins instead

## 6. Package and File Layout

New files:

- `agent/skills/manage-plugins/SKILL.md` — the skill content
- One-line addition to `agent/builtin_skills.go`'s embed/registration

New tests:

- `agent/builtin_skills_test.go` — extend to verify `manage-plugins` is registered, parses, and surfaces via `DiscoverSkills` on a clean environment

## 7. Testing Strategy

| # | Name | Scenario | Expected |
|---|---|---|---|
| 1 | registration | Empty workspace, no `--plugin-dir` | `DiscoverSkills(env)` includes "manage-plugins" with correct name and non-empty description |
| 2 | frontmatter parses | Read the embedded SKILL.md | `frontmatter.Parse` returns `{name: "manage-plugins", description: <non-empty>}` |
| 3 | shadowing | A plugin defines its own `manage-plugins` skill | The plugin-namespaced skill (`plugin:manage-plugins`) wins for its namespace; the builtin remains under the bare name |
| 4 | description completeness | Frontmatter description mentions install, update, remove, marketplace, list, plugin | All six triggering verbs/nouns present (covers the LLM's matching surface) |
| 5 | body procedure links | Skill body's step-by-step sections each match the named user intents | Static check: each "install/update/remove/list" path exists as a section heading |

Tests 4 and 5 are guardrails against future edits silently breaking the LLM's invocation surface.

The skill is not end-to-end tested by running it (that would require an LLM round-trip). Behavior coverage comes from real-world usage; the tests above just ensure the file is well-formed and present.

## 8. Open Questions

1. **Should the skill prompt the user for `userConfig` values during install, or defer to first-load (per SP7)?**
   Recommendation: defer to first-load. The skill stages the plugin directory; SP7 handles the prompt when the plugin loads. Keeping responsibilities split means the skill stays surface-agnostic — it doesn't need to know about keychain access or prompt UX. The skill's "tell the user to restart serf" step is also where SP7 takes over.

2. **Should the skill maintain a cache of recently-cloned marketplaces under `~/.config/serf/plugins/marketplaces/`?**
   Recommendation: yes, opportunistically. The skill can clone marketplaces into `~/.config/serf/plugins/marketplaces/<name>/` and consult them on subsequent installs. No registry file required — directory presence is the cache. This is convenience, not a feature; document but don't require it.

3. **Does the skill need to handle `userConfig` defaults at install time (e.g., pre-create `~/.config/serf/plugins/<plugin>/.options.json` with defaults)?**
   Recommendation: no. SP7 owns the values file. The skill stages the manifest only.

4. **What about MCP server credentials baked into `mcpServers` env values that reference `${user_config.KEY}`?**
   These are resolved at MCP transport-creation time by SP6, using SP7's resolved-value provider. The skill does not touch them.
