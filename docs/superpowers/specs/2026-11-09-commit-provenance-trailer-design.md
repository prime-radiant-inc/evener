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
exact line. This needs no `core.hooksPath`, no hook dispatcher and no
`launch.toml` entry. The hub keeps its value in the existing settings store
pattern, not a TOML file.

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

Three surfaces resolve one value:

| Surface | Where | Scope |
| --- | --- | --- |
| Hub global | Settings → Agent setup → Git commits | User-wide on that host |
| Hub per session | New session dialog | One session |
| CLI | `--commit-provenance` on `evener` or `evener serve` | One invocation |

Precedence: the per-session toggle, then the hub global, then `coauthor`.

### Hub global

Use the existing hub settings store facility, the same pattern as keybindings
and conversation display: a small JSON state file under the state root with a
get/set RPC and a changed broadcast. This is a hub setting, not a new TOML file
and not a `launch.toml` entry.

- `hubcore.CommitProvenanceStore` stores `<state-root>/commit-provenance/state.json`.
  The default is `coauthor`; a malformed snapshot loads the default with a
  diagnostic, as the keybindings and display stores do.
- `evener/settings/commitProvenance/get`, `/set` and `/changed` carry the value.
- Settings → Agent setup → Git commits is host-scoped and has one select. Each
  host's hub owns its own value; Evener does not sync it between hosts.
- `HubSpawner` and the launch preview read the store. A new hub session gets
  the global value unless the per-session toggle overrides it, and the hub
  passes `--commit-provenance=<resolved>` to `evener serve` so every host
  agrees with the setting surface the person used.
- A resume passes the session's saved value unless the resume request supplies
  an override.

### Hub per session

The New session dialog gains a `Commit provenance` select: `(inherit)`,
`Record session details` (`coauthor`) and `Off`. It is a per-launch-only launch
option: `commit_provenance` in the launch schema with no defaultable layers and
a `toml:"-"` field, so it can never be written to `launch.toml`. The hub
resolves it in memory and `ToArgs` emits `--commit-provenance`.

### CLI

```sh
evener --commit-provenance=off 'fix the test'
evener serve --commit-provenance=off ...
```

The flag is added to `launchFlags`, so `evener` and `evener serve` share it.
Values are `coauthor` and `off`. A direct CLI launch does not read the hub
store; the flag or the default `coauthor` applies. This is the optional
command-line setting; there is no `evener config` command.

### Persistence and precedence

`SessionConfig.CommitProvenance` persists the resolved value, like the model and
sandbox settings. A new hub session resolves the per-session toggle, then the
hub global, then `coauthor`. A direct CLI session resolves the flag, then
`coauthor`. A resume keeps the saved value unless the caller supplies an
explicit flag or toggle. A session already running keeps its value.

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
- A malformed hub store loads the default `coauthor` with a load diagnostic; a
  write failure is reported by the settings RPC and does not change the value.
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
| A new TOML settings file or a `launch.toml` entry | Duplicates or disturbs existing configuration; rejected by Jesse |

The standalone `tools/commit-provenance` writer stays as the record of the
custom-header experiment. This design does not wire it into the product.

## Implementation surface

| Area | Work |
| --- | --- |
| Prompt | `CommitTrailer` in `promptData`, computed in `buildPromptData`, rendered by `agent/prompts/system.md.tmpl` |
| Session config | `SessionConfig.CommitProvenance`, default on, persisted |
| Hub global | `hubcore.CommitProvenanceStore` on the shared state-file facility, `evener/settings/commitProvenance/{get,set,changed}`, and the host-scoped Git commits settings section |
| Hub per session | Per-launch-only `commit_provenance` launch option (`toml:"-"`, no defaultable layers), New session select, and `ToArgs` emission |
| Hub spawn | `HubSpawner` and the launch preview read the store and pass the resolved flag to `evener serve` |
| CLI | `--commit-provenance` in `launchFlags`, passed by `run` and `serve`; no `evener config` command |

No Git hook, no `core.hooksPath`, no sandbox grant, no environment overlay, no
new TOML file and no `launch.toml` field. The hub store is the existing settings
store pattern; the per-session field is memory-only.

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
6. The hub store defaults to `coauthor`, round-trips `off`, rejects other
   values, broadcasts changes, and reports a malformed snapshot.
7. A hub spawn with no per-session toggle passes the stored global value; a
   per-session `off` overrides it; a resume passes the saved value.
8. The New session field never appears in the global or project launch forms
   and is never written to `launch.toml`.

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
- `docs/web-ui/README.md`: Settings → Agent setup → Git commits and the New
  session override.
- `docs/configuration.md`: the hub global lives in the hub settings store, not
  `launch.toml`; the CLI flag is per invocation.

## Decisions

Approved by Jesse in review:

- Use the prompt instruction, not a Git hook.
- Default is on.
- The trailer text is as shown.
- The coverage limit: only commits whose message the model writes.
- The hub has a global setting and a per-session toggle.
- No `launch.toml` and no new TOML settings file; the CLI flag is optional.

Still open:

1. Confirm the hub global uses the existing settings store pattern
   (keybindings/conversation display) rather than `hub.toml`. This design adds
   a store instance on that shared state-file facility.
2. Confirm the per-session toggle may ride the existing launch-override channel
   as a memory-only, `toml:"-"` field, so it never writes `launch.toml`.
