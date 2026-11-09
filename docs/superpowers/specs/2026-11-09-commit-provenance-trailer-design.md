# Commit provenance: prompt design

**Status:** proposed. This document is a design for approval; no production
code exists yet. Jesse chose the prompt instruction used by Claude Code over a
Git hook. The hub keeps a global setting and a per-session toggle; the CLI flag
stays optional.

## Intent

Every commit whose message an Evener session writes should name the Evener
version, model, host and session that made it, so a person who worked on the
commit can find the contributing session. The record is a breadcrumb, not a
claim about who wrote every line and not a cryptographic signature.

## What changes for the user

With provenance on, a commit message written by the model ends with:

```text
Co-authored-by: Evener 0.9.12 (anthropic/claude-opus-4.6) <034uOsahfdjKENyvpnJexm@magic-kingdom>
```

The four fields are the runtime version, the session's selected model as
`instance/model`, the executing host, and the session ID. A person can use the
session ID to find the transcript in Evener. GitHub keeps the trailer in the
commit message; the synthetic address is not associated with a GitHub account,
so it grants no contribution credit.

The trailer is part of the commit message. It is visible in every Git UI and in
public repositories. Hostnames and session IDs are disclosed with the commit;
that is the feature, and the hub `Off` control or `--commit-provenance=off` is
the way to withhold it.

## Why this mechanism

Claude Code adds its co-author trailer the same way. I inspected the installed
Claude Code 2.1.321 build:

- Its `# Committing changes with git` prompt section tells the model to end
  commit messages with `Co-Authored-By: <model> <noreply@anthropic.com>`.
- The prompt shows the model writing that line inside the
  `git commit -m "$(cat <<'EOF' ... EOF)"` heredoc.
- `includeCoAuthoredBy` (deprecated) and `attribution.commit` change or hide
  that text; `includeGitInstructions` removes the commit and PR instructions
  entirely.
- There is no Git hook and no automatic amendment. The prompt explicitly says to
  create new commits rather than amend, and to fix a failed pre-commit hook and
  create a new commit.

Evener already renders a per-session system prompt, so it can give the model the
exact line. This needs no `core.hooksPath` and no hook dispatcher. The global
value uses the existing launch settings facility, alongside the model, sandbox
and limit defaults.

## How it works

`promptData` gains a `CommitTrailer` field, computed while the system prompt is
rendered:

| Field | Source |
| --- | --- |
| Version | `agent.BuildVersion`, set from `buildinfo.Version()` |
| Model | The selected profile's `ID()` and `Model()` |
| Host | `os.Hostname()` of the process running the session |
| Session | `Session.ID()` of the executing session |

The template renders a section only when the value is present:

```text
{{ if .CommitTrailer }}## Commit provenance
When you create a git commit, end the commit message with this exact line:
{{ .CommitTrailer }}
Do not add it to pull request descriptions. Do not amend a commit only to add
it.
{{ end }}
```

`SetModel` already refreshes the cached system prompt, so a mid-session model
switch updates the model in the trailer on the next render. A delegate gets its
own session ID and selected model, and its prompt carries them.

If a required field is empty or contains a control character, the session omits
the section and reports a diagnostic. The design does not invent a host,
version, model or parent session to fill a gap.

## Configuration

The setting rides the existing launch settings facility. The global value is a
field in the global launch layer; the per-session value is a launch-layer
override.

| Surface | Where | Scope |
| --- | --- | --- |
| Hub global | Settings → Agent setup → Session defaults, the global launch layer | User-wide on that host |
| Hub per session | New session dialog, the launch layer | One session |
| CLI | `--commit-provenance` on `evener` or `evener serve` | One invocation |

`launchconfig.Layer` gains `CommitProvenance string` with TOML tag
`commit_provenance,omitempty`. The schema offers it in the global layer
(`DefaultableLayers: [global]`) and as a per-launch override (`PerLaunch:
true`); a repository or project layer value is ignored with a diagnostic. The
field is saved and loaded through the existing `launchconfig` loader and the
existing `SetLayer` writer. No new store, RPC or settings section is added.

### Hub global

Settings → Agent setup → Session defaults already edits
`<config-root>/launch.toml` through `evener/launch/setLayer`. The form gains a
`Commit provenance` select with `Record session details` (`coauthor`) and
`Off`. A new hub session gets that value; the hub passes
`--commit-provenance=<resolved>` to `evener serve`, so remote hosts agree with
the selected host's setting.

### Hub per session

The New session dialog gains the same select as a launch-layer override:
`(inherit)`, `Record session details` and `Off`. It overrides the global layer
for that session, and `ToArgs` emits `--commit-provenance`.

### CLI

```sh
evener --commit-provenance=off 'fix the test'
evener serve --commit-provenance=off ...
```

The flag is added to `launchFlags`, so `evener` and `evener serve` share it.
Values are `coauthor` and `off`. A standalone CLI launch does not read
`launch.toml`; it uses the flag or the default `coauthor`. This keeps direct CLI
and CI behavior unchanged. The hub passes the flag to the daemons it launches.

### Persistence and precedence

`SessionConfig.CommitProvenance` persists the resolved value, like the model and
sandbox settings. For a hub launch the existing launch resolver merges the
global `launch.toml` value and the per-session override; for a standalone CLI
launch the flag or `coauthor` applies. A resume keeps the saved value unless the
caller supplies an explicit override. A session already running keeps its
value.

## Coverage and limits

The trailer is best-effort and depends on the model following the prompt.

- Commits whose message the model writes get the trailer.
- Scripted commits, `git merge --no-edit`, rebases, cherry-picks, `commit-tree`
  and commits GitHub creates itself do not.
- The model can forget or reformat the line. The prompt says "this exact line";
  that is the same guarantee Claude Code offers.
- The model name is the selected model at prompt render. A fallback model that
  answered a particular request may differ, as with Claude Code.
- The trailer is public wherever the commit is public.
- A commit made without the trailer is not repaired. The design does not amend
  it; the user can ask for an amend, and the instruction says not to amend only
  to add the trailer.

## Failure and recovery

- A missing field or a control character omits the section and reports a
  diagnostic; the session still runs and commits normally.
- A model that ignores the instruction leaves the commit without a trailer. No
  hook repairs it, and no post-commit rewrite exists.
- `--commit-provenance=off` removes the instruction; existing commits keep
  whatever trailers they already have.
- A malformed `launch.toml` follows the launch resolver's existing failure
  handling; a save failure is reported by `evener/launch/setLayer` and leaves
  the previous value.
- A hub global change applies to the next launch; a running session keeps its
  value.
- Signatures, hooks, author identity, staging state and refs are untouched.

## Approaches considered

| Approach | Why it loses |
| --- | --- |
| Prompt instruction (chosen) | Only commits whose message the model writes; the model can ignore it |
| Compiled Evener hook helper | Covers every porcelain commit, but manages `core.hooksPath` and forwards existing hooks; rejected by Jesse |
| Hand-written shell hook dispatcher | Fragile shell parsing and `PATH` sensitivity; rejected by Jesse |
| `git` wrapper on `PATH` | Bypassed by absolute paths, libraries and changed environments; misses merge, cherry-pick and rebase |
| Custom commit header (prototyped) | No native insertion point before signing; post-commit rewriting invalidates signatures and reports stale IDs; native rebase, cherry-pick and squash drop it; GitHub squash dropped it |
| Git notes | A separate ref that ordinary clone and push do not carry without extra configuration |
| A new TOML settings file | Duplicates the existing launch settings facility; rejected by Jesse |
| Global launch.toml (chosen) | Existing facility, edited with the model and sandbox defaults; direct CLI does not read it |

The standalone `tools/commit-provenance` writer stays as the record of the
custom-header experiment. This design does not wire it into the product.

## Implementation surface

| Area | Work |
| --- | --- |
| Prompt | `CommitTrailer` in `promptData`, computed in `buildPromptData`, rendered by `agent/prompts/system.md.tmpl` |
| Session config | `SessionConfig.CommitProvenance`, default on, persisted |
| Launch config | `CommitProvenance` in `launchconfig.Layer`, the schema (`DefaultableLayers: [global]`, `PerLaunch: true`), merge, wire and `ToArgs` |
| Hub global | The existing Session defaults form writes the global `launch.toml` layer through `evener/launch/setLayer`; no new section or store |
| Hub per session | New session launch override; `ToArgs` emits `--commit-provenance` |
| Hub spawn | The existing launch resolver and `HubSpawner` pass the resolved flag to `evener serve` |
| CLI | `--commit-provenance` in `launchFlags`, passed by `run` and `serve`; no `evener config` command |

No Git hook, no `core.hooksPath`, no sandbox grant, no environment overlay and
no new TOML file. The field is an ordinary entry in the existing global
`launch.toml` layer.

## Testing and qualification

Unit tests:

1. The system prompt contains the exact trailer with the version, model, host
   and session ID; it omits the section when the flag is `off`.
2. A delegate's prompt carries its own session ID and model.
3. A model switch refreshes the prompt's model line.
4. A missing or control-character field omits the section and reports a
   diagnostic.
5. The flag parses `coauthor` and `off`, rejects other values, and overrides a
   resumed session's saved value.
6. Launch config: the field loads from the global layer, saves through
   `SetLayer`, rejects invalid values, and a repository or project value is
   ignored with a diagnostic.
7. A hub spawn with no per-session override passes the global value; a
   per-session `off` overrides it; a resume passes the saved value.
8. A standalone CLI launch ignores `launch.toml` and uses its flag or the
   default.

Live, opt-in qualification:

1. Start a real session with a provider and ask it to make a commit; inspect the
   commit message with `git show -s --format=%B`.
2. Confirm a scripted commit in the same session does not gain a trailer, which
   is the stated coverage limit.
3. Run the same request with `--commit-provenance=off` and confirm the prompt
   carries no instruction and the commit has no trailer.

## Rollout and documentation

The default is `coauthor`. The release note must say that commit messages
written by the model will change and that `--commit-provenance=off` disables the
instruction for a session.

Documentation updates in the same change:

- `docs/cli.md`: the `--commit-provenance` flag.
- `docs/product/subsystems.md`: prompt composition and session configuration.
- `docs/web-ui/README.md`: Settings → Agent setup → Session defaults and the New
  session override.
- `docs/configuration.md`: the `commit_provenance` field in the global launch
  layer, and the standalone CLI exception.

## Decisions

Approved by Jesse in review:

- Use the prompt instruction, not a Git hook.
- Default is on.
- The trailer text is as shown.
- The coverage limit: only commits whose message the model writes.
- The hub has a global setting and a per-session toggle.
- The global value lives in `launch.toml`, alongside the model and sandbox
  defaults.
- The standalone CLI does not read `launch.toml`; it keeps the optional flag.
- No new TOML settings file.

No open decisions.
