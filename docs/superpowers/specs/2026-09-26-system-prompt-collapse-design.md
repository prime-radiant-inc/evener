# System Prompt Collapse Design

Date: 2026-09-26
Status: Draft for Jesse's review
Program: part 1 of 3 in the system prompt cleanup

## Purpose

Evener assembles its system prompt from 24 section files, two top-level
templates, and a resolver that can layer every section by provider surface, by
agent, and across three override directories. The two templates have drifted
apart, the layering serves two files, and the prose reads like the output of a
long series of patches. Part 1 replaces all of this with one template file, so
that part 2 can rewrite the prose as one document, and it leaves behind tests
that check how the prompt is assembled without pinning any of its wording.

## The program

1. **Part 1, this document.** Collapse the infrastructure. The rendered prompt
   changes in exactly two places (see "Intended prompt changes").
2. **Part 2.** Rewrite the prose in the new template. It starts by turning the
   behaviors listed under "Behaviors for part 2" into live probes and recording
   a baseline before any prose changes.
3. **Part 3.** Rewrite the bundled role prompts (`internal/bundled/agents/`,
   `internal/bundled/plugins/coordinator-workflow/agents/`). Issue #2401, the
   prose-pinning tests on those files, belongs to this part.

Outside the program: tool descriptions. Issue #2353 (CORE-08, dropped append
files and the fallback prompt after a render failure) awaits Jesse's policy
decision; part 1 keeps today's behavior on both counts.

## Decisions

Jesse made these on 2026-09-26.

1. **Drop section overrides.** The project directory `.evener/prompts/sections/`,
   the global directory `~/.config/evener/prompts/sections/`, the
   `role.agent-<agent>.md` role override, and the surface and agent variant
   layering all go.
2. **One template file with the prose inline.** Every word of the base prompt
   lives in `agent/prompts/system.md.tmpl`, with `{{ if }}` blocks where root
   sessions and delegates differ.
3. **Remove `no_project_prompts` everywhere,** with no decode-only leftover.
   The consequences are listed under "Accepted consequences".
4. **Transcripts guidance moves to one position,** right after Tool usage, for
   root sessions and delegates alike.
5. **The coordinator's non-interactive text goes.** A headless coordinator
   gets the same non-interactive section as every other headless root.
6. **Tests cover machinery and agent behavior, never prose.** This follows
   `docs/developing-evener/testing.md` ("Prompt Prose Is Not a Test Oracle").
   Obsolete tests and prose assertions are both deleted; none is ported.

## Current state

On main at `5854983eb`:

- `agent/prompts/sections/` holds 24 files, about 5,400 words.
  `agent/prompts/templates/` holds `system.md.tmpl` (root sessions) and
  `subagent.md.tmpl` (delegates). `renderSystemPrompt` picks one by session
  depth.
- `agent/section_resolver.go` (270 lines) resolves each section by probing
  project, global, and embedded sources for surface and agent variants with
  prepend, body, and append parts. Two files use any variant:
  `tools.provider-openai_append.md.tmpl` and
  `non-interactive.agent-coordinator.md`. Its `Render` method, which loads a
  top-level template from disk, has no production caller.
- `agent/internal/promptpath/` exists only to locate the override directories.
- The templates differ as follows. Several differences are drift: a leaf
  delegate reads the "report the capability mismatch through
  your result tool" paragraph twice (once in `workflow.md.tmpl`, once in the
  subagent template), and the subagent template restates the allowance rules
  from `delegation.md`.

| Section | Root | Delegate |
| --- | --- | --- |
| identity, capabilities, workflow | yes | yes |
| allowance paragraph | no | when `CanDelegate` |
| delegation, background jobs | yes | when `CanDelegate` |
| delegated task limits | no | when not `CanDelegate` |
| transcripts | after background jobs, inside the override block | after tools |
| git safety, security, task tracking | yes | no |
| verification, context management, communicate | yes | yes |
| ask user | when `HasAskUser` | no |
| tools, environment, git, workspace, role, activated skills | yes | yes |
| skills catalog, available agents, project docs | yes | no |
| non-interactive | when `NonInteractive` | no |
| user instruction override | yes | yes |
| `--system-prompt-append` files | yes | no |

## Design

### The template

`agent/prompts/system.md.tmpl` contains the prose of all 24 sections and both
templates. Its order is today's root order, with transcripts moved after tools:

```
{{ if .BaseInstructionsOverride }}  the override text
{{ else }}
  identity, capabilities, workflow
  allowance paragraph                    if .IsSubagent and .CanDelegate
  delegation, background jobs            if not .IsSubagent, or .CanDelegate
  delegated task limits                  if .IsSubagent and not .CanDelegate
  git safety, security, task tracking    if not .IsSubagent
  verification, context management, communicate
  ask user                               if .HasAskUser
{{ end }}
tools, plus the openai tools append      if eq .Surface "openai"
transcripts                              (keeps its own HasTool gates)
environment, git, workspace, role, activated skills
skills catalog, available agents, project docs     if not .IsSubagent
non-interactive                          if not .IsSubagent and .NonInteractive
user instruction override                if set
--system-prompt-append files             if not .IsSubagent
```

The `HasTool` gates inside sections stay where they are. The file has no
`{{ define }}` blocks and no helper functions beyond the `HasTool` method.

### `promptData`

- Adds `IsSubagent` (session depth above zero), `Surface` (the profile's
  surface), and `Role` (the resolved role body).
- Drops `Provider`, `Agent`, `ProfileTools`, `MCPTools`, and `CustomTools`. No
  template reads them. `buildPromptData` stops computing them; any helper left
  without a caller goes too.
- Drops `RolePromptOverride`, which becomes an input to role resolution.

### Rendering

- The template is embedded with `//go:embed` and parsed once at package
  initialization with `template.Must`. A template that fails to parse fails
  every test in the package.
- `renderSystemPrompt` builds `promptData`, executes the template, and applies
  `collapseBlankLines` and `TrimSpace` as today. An execution error keeps
  today's behavior: a fallback prompt plus a warning that callers report after
  releasing `s.mu`. The lock contract documented on `refreshSystemPromptCache`
  does not change.
- Role resolution follows `resolveRole` without the disk lookup: the role
  prompt override when it is non-empty, otherwise the bundled agent body with
  its frontmatter stripped, otherwise nothing.
- `PROMPT_LOADED` events name the prompt's inputs: `cli:<path>` for
  `--system-prompt`, `embedded:prompts/system.md.tmpl` with the rendered
  size, `agent:<name>` or `config:role_prompt_override` for the role, and
  `append:<path>` for each append file. A session start shows one line per
  input where it showed one per section.
- Each append file is read once, and that single read supplies both the
  prompt text and its `PROMPT_LOADED` entry. Today two separate reads can
  disagree, one of the three problems #2353 names. Skipping an unreadable
  append stays as is.
- The test seam that injects render failures
  (`renderEmbeddedSystemPrompt`) gets an equivalent over the new render
  function.

### Environment swap

`session_env_swap.go` pre-warms the new environment's git-root cache before
taking `s.mu`. Its comment calls the pre-warm load-bearing because rendering
looked up the project prompt directory with `execenv.GitRootOrEmpty` while
holding the lock. Nothing in the locked step calls it after this change, so the
pre-warm goes and the lock-order comment is rewritten to match.

### Removing `no_project_prompts`

The setting goes from:

- `agent`: `SessionConfig.NoProjectPrompts`, `schema.ConfigSnapshot.NoProjectPrompts`,
  and the conversions between them.
- `cmd/evener`: the `--no-project-prompts` flag on `run` and `serve`.
- `appwire`: `LaunchConfigLayer.NoProjectPrompts`, the regenerated
  `appwire-client/typescript/types.gen.ts`, and the row in
  `docs/appwire-protocol.md`. The protocol version does not change: peers that
  still send the key have it ignored, and nothing reads it back.
- `cmd/evener-hub/internal/launchconfig`: the schema row (the spawn UI renders
  from it), `types.go`, `wire.go`, `args.go`, `merge.go`, and
  `runtime_defaults.go`; plus `cmd/evener-hub/internal/appsource/remote_hub_probe.go`.
- `cmd/evener-tui/internal/launchconfig`: `launch_schema.go` and
  `launch_settings_panel.go`.
- `tools/tool-fluency/cmd/evener-fluency/main.go`: two call sites.
- Docs: the row in `docs/developing-evener/conventions/naming.md`, the command
  in `test/scenarios/reasoning-effort-providers.md`, and the section path in
  `docs/tools/transcripts.md`.
- Tests: frontend tests that use `noProjectPrompts` as a sample boolean
  (`appwire-client/typescript/spawnSchema.test.ts`, `AdvancedOptions.test.tsx`,
  `Spawn.test.tsx`) switch to another launch boolean. Go tests that set
  `NoProjectPrompts: true` drop the line.

### Deleted files

- `agent/prompts/sections/` (24 files) and `agent/prompts/templates/` (2 files)
- `agent/section_resolver.go` and `agent/prompt_assets.go`
- `agent/internal/promptpath/`, including its tests

### Accepted consequences

- A delegate store (`delegates.jsonl`) that recorded
  `"no_project_prompts":true` in a delegate's config fails to open, because the
  store decodes strictly, and its root session cannot resume. Only sessions
  launched with the setting on and with delegates are affected; the hub's
  default is off. The one delegate store on Jesse's machine does not set it.
- Passing `--no-project-prompts` becomes an unknown-flag error. The
  terminal-bench adapter needs a change if it passes the flag.
- Session metas, hub TOML, and AppWire launch layers that still carry the key
  keep working; each decodes leniently and ignores it.
- Section override files that someone placed under `.evener/prompts/sections/`
  or `~/.config/evener/prompts/sections/` stop taking effect, silently.
- A root session started with `--system-prompt` now also gets the transcripts
  guidance when it has the transcript tools, as delegates already do.

## Intended prompt changes

Two, both made before the collapse (PR 2 below):

1. The transcripts section moves to directly after Tool usage in root prompts.
2. `non-interactive.agent-coordinator.md` is deleted, so a headless
   coordinator gets the generic non-interactive section.

Every other configuration renders byte for byte as it does today.

## Testing

### Machinery tests

These run in the default suite against real sessions built with the test kit
(`newSession`, plus `prepareSubagentRun` for delegates), in seven
configurations:

1. Root, interactive, anthropic surface.
2. Root, headless, openai surface, coordinator role.
3. Root with a `--system-prompt` file, an append file, and a user instruction
   override.
4. Delegate with the default subagent role and a positive allowance.
5. Leaf delegate (allowance 0).
6. Explorer delegate, whose read-only tool set turns the `HasTool` gates off.
7. Implementer delegate, whose role arrives as the plugin's role override.

text/template evaluates only the branches it takes, so a bad field reference
in an untaken branch goes unnoticed. The configurations therefore also vary
the session data behind the template's data branches: git repository or not,
workspace tree and build info, sandbox, resource caps, skills with and without
`use_skill`, agents with and without task lists, project docs, and preloaded
skills. The plan maps every `{{ if }}` and `{{ range }}` to the configurations
that run each side, and reports to Jesse any side that no real session can
reach.

Each test catches a failure that can actually happen:

1. **Typed inputs.** `buildPromptData` computes `IsSubagent`, `CanDelegate`,
   `HasAskUser`, `Surface`, `Role`, and `CLIAppends` correctly for each
   configuration. The template cannot notice a wrong input.
2. **The template executes.** Rendering each configuration succeeds with no
   warning. text/template reports a bad field or method reference only at
   execution time, and part 2 will edit this file heavily.
3. **Operator data arrives exactly once.** Opaque sentinels placed in project
   docs, a skill, an agent description, the role override, `--system-prompt`,
   the user instruction override, and an append file each appear once in the
   rendered prompt. This catches dropped and doubled inputs.
4. **The prompt reaches the provider.** A scripted provider receives the
   rendered prompt unchanged, as system instructions or, with
   `--system-prompt-as-user`, as the start of the first user message. If an
   existing test already proves this, it stays and no new one is added.
5. **No uncallable tool is named.** `TestShippedPromptsOnlyNameToolsTheSessionHas`
   stays as it is. Tool names are contract values, which the testing guide
   allows.

Also: `PROMPT_LOADED` labels, checked as structured event data, and the
existing unit test for `collapseBlankLines`.

No test asserts the presence, absence, order, or text of prompt prose.

### Deleted tests

- The resolver mechanism tests in `agent/section_resolver_test.go` (sources,
  variant layering, prepend and append parts, source priority, template
  precedence, disk and role overrides, source tracking),
  `agent/cov_s5_section_resolver_test.go`, and the `promptpath` tests. They go
  in PR 3, with the code they cover.
- Every assertion on prose from `agent/prompts/`, wherever it lives: phrases,
  headings, marker order, and absence checks. Where one of these was the only
  check on a typed input, a typed-input test replaces it. They go in PR 1.
- The parts of the two `evenerfuzz` files that build the resolver or stub the
  override directories: `workspace_prompt_program_fuzz_test.go` and
  `remaining_exact_coverage_fuzz_test.go`. The rest of each file stays and must
  vet under `-tags evenerfuzz` on the host, Linux, and Windows.

`agent/cov_s2_prompts_test.go` stays. It proves append content crosses into
the prompt and pins the missing-file behavior that #2353 may change.

### Proving the collapse changes nothing

This check runs once in the implementation lane and is not committed. A scratch
test renders the seven configurations into a directory. The lane runs it on the
commit before the change and on the change, then diffs the two directories. The
PR description includes the scratch test's source, the commands, and the diff:

- PR 2 shows the two intended changes and nothing else.
- PR 3 shows no difference at all.

### Behaviors for part 2

The deleted prose tests each pointed at a behavior someone cared about. Part 2
turns these into live, opt-in probes in `tools/tool-fluency`, which already runs
probes across models, providers, and roles, and records a baseline before the
prose changes:

1. A gate that timed out, failed to launch, hit a sandbox denial, or never ran
   is reported as incomplete verification, with its evidence.
2. Before a failure changes production behavior, the agent establishes whether
   it belongs to the product or to a fixture or environment. A parent reruns a
   child's incomplete gate itself.
3. A cross-model or cross-configuration comparison is read as a behavior
   failure only after one known-good smoke case passes on each participant.
4. Once the evidence supports one falsifiable hypothesis, the agent stops
   surveying code and runs the smallest test that could refute it.
   (`tools/tool-fluency/probes-nbcf/diagnostic_fix.seeded_config_path.yaml`
   already probes this.)
5. After two incomplete implement, review, and fix cycles on one task, the
   agent stops and reports, reslices, or asks for direction.
6. When a long or delegated task changes phase, the agent sends a checkpoint:
   what is done, the current plan, the next action, and any blockers.
7. Cleanup removes only scratch. Deliverables and files that existed before the
   task stay (#302).
8. The agent reads session history through the transcript tools, never from
   raw transcript files.
9. In a fresh worktree, the agent installs or copies dependencies before
   running the project's gates.
10. The agent asks the user only what evidence cannot settle and batches the
    questions for one stopping point.

## Delivery

Four PRs, in order. Each keeps every gate green.

1. **Tests.** Adds the machinery tests that apply to today's code and deletes
   the prose assertions. No production change.
2. **The two intended prompt changes,** made on today's structure: one line
   moves in `system.md.tmpl`, and one variant file is deleted. The render
   comparison shows exactly those changes.
3. **The collapse.** The new template and render path, the `promptData`
   changes, typed-input tests for the new fields, the environment-swap change,
   and the deleted files and tests. The render comparison shows no difference.
   After this PR `no_project_prompts` has no effect until PR 4 removes it.
4. **Removing `no_project_prompts`** from the CLI, AppWire, hub, TUI,
   tool-fluency, frontend fixtures, and docs.

Checks each PR runs locally before relying on CI: the touched packages' Go
tests, `make lint-evenerfuzz` when `evenerfuzz` files change, and for PR 4 the
generated-output check for `types.gen.ts` and `make test-web` for the frontend
fixtures.
