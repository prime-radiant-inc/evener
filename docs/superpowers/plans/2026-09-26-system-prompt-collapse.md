# System Prompt Collapse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 24 prompt section files, the two top-level templates, and the layering resolver with one embedded template; remove the `no_project_prompts` setting; and leave tests that check how the prompt is assembled without asserting any of its prose.

**Architecture:** Four PRs, each branched from `main` after the previous one merges. PR 1 adds the assembly tests and deletes or rewrites every test that asserts prompt prose. PR 2 makes the two intended prompt changes on today's structure. PR 3 replaces the resolver with one template parsed at package initialization and deletes the old machinery. PR 4 removes `no_project_prompts` from the hub, TUI, AppWire, CLI, session config, and docs. A one-time render comparison, never committed, proves PR 2 changes exactly two things and PR 3 changes nothing.

**Tech Stack:** Go 1.27 (`text/template`, `embed`), the `agent` package test kit, AppWire's generated TypeScript types (`make generate`), the hub launch-config schema, vitest and Biome for the frontend fixtures.

**Spec:** `docs/superpowers/specs/2026-09-26-system-prompt-collapse-design.md`

## Global Constraints

- No test asserts the presence, absence, order, or text of prompt prose, including markup such as `<skill-catalog>` or `----- BEGIN path -----`. Tests may assert typed `promptData` fields, opaque sentinels, tool names, structured `PROMPT_LOADED` data, and render errors. (`docs/developing-evener/testing.md`, "Prompt Prose Is Not a Test Oracle".)
- Two kinds of prose assertion are out of scope and stay untouched: assertions on role prompt text (`internal/bundled/agents/`, `internal/bundled/plugins/*/agents/`, tracked by issue #2401) and assertions on lines Go code generates (the capability preamble, `sandboxPromptLine`, resource caps values).
- The rendered prompt changes only in PR 2's two places: the transcripts guidance moves directly after Tool usage in root prompts, and a headless coordinator gets the generic non-interactive text. The render comparison proves it.
- No backward-compatibility leftovers: no decode-only field, no no-op flag, no alias.
- Every new agent test starts with `t.Parallel()`, unless it swaps a package-level variable; such a test says why in a comment beginning `Not parallel:`.
- A test that writes files uses its own `t.TempDir()` as the working directory, never the shared default workspace. A test that passes `withConfig` sets `AgentsDocPath` itself.
- Some tests are called by name from `//go:build evenerfuzz` union files. Keep those names, or update the union in the same commit: `agent/session_init_worktree_seed100_fuzz_test.go`, `agent/session_init_seed100_exact_fuzz_test.go`, `cmd/evener/seed_coverage_fuzz_test.go`, `cmd/evener/serve_coverage_fuzz_test.go`.
- Format Go with `"$(go env GOROOT)/bin/gofmt" -w <files>`, never the `gofmt` on `PATH`.
- When a `//go:build evenerfuzz` file or a test called from one changes, run `make lint-evenerfuzz` (host, Linux, and Windows vet). `agent/resource_caps_sandbox_linux_test.go` compiles only on Linux, so also run `(cd agent && GOOS=linux go vet .)` when it or its helpers change.
- Stage named paths only, never `git add -A` or `git add .`. Before every commit, run `git status --short` and `git diff --cached --stat` and confirm only the intended files are staged.
- Never run `npx biome` from the repo root; run Biome from `cmd/evener-hub/frontend`. Never run `npm ci` through a symlinked `node_modules`.
- Commit messages carry no attribution lines.
- Open each PR as a regular PR against `main`, not a draft. Jesse decides merges. The next PR branches from `main` after the previous one merges.

## Review Focus

1. **A template body no configuration executes.** text/template reports a bad field or method reference only when it runs the branch, so a stale reference in an unexecuted `{{ if }}` body ships and fails one kind of session in production. The branch map below assigns every body to a configuration, and Task 2's typed-input test pins each premise the map relies on.
2. **Whitespace drift when inlining sections.** Two section files end in `{{ end -}}`, which eats the blank line before the next section once inlined, and line 3 of `workflow.md.tmpl` ends in a trailing space that editors strip. Task 9's assembly script handles both, and the render comparison catches anything else.
3. **`--system-prompt-as-user`.** The rendered prompt must arrive unchanged as the first text part of the first user message. Task 4 pins it with exact equality.
4. **The environment swap under `s.mu`.** Once the git-root pre-warm goes (Task 10), nothing in the locked step may start a subprocess. The render reads session state and files only; Task 10 records that constraint where the pre-warm was, and `TestSwapEnvAndRefresh_NoGitForkWhileLocked` keeps guarding it.
5. **Old session metas after `NoProjectPrompts` leaves `schema.ConfigSnapshot`.** A meta written by an older build carries `"no_project_prompts":true` and must still load. Task 12 adds that test.

## The eight configurations

`agent/system_prompt_test.go` (Task 1) defines eight real sessions. They are the fixtures for every assembly test and for the render comparison.

| # | Name | Built by | What it carries |
| --- | --- | --- | --- |
| C1 | root interactive anthropic | `buildRootInteractiveAnthropicSession` | anthropic surface; a state dir; real git repo with a commit; `go.mod` and `AGENTS.md` in the working dir; a personal `AGENTS.md`; one fixture skill; resource caps |
| C2 | root headless openai coordinator | `buildRootHeadlessCoordinatorSession` | openai surface; `NonInteractive`; coordinator role from the coordinator-workflow plugin; one non-marker file in the working dir |
| C3 | root with overrides | `buildRootWithOverridesSession` | `--system-prompt` file, one append file, a user instruction override; empty working dir |
| C4 | delegate that can delegate | `buildPromptDelegate(t, 1, "")` | default subagent role, allowance 1 |
| C5 | leaf delegate | `buildPromptDelegate(t, 0, "")` | default subagent role, allowance 0 |
| C6 | explorer delegate | `buildPromptDelegate(t, 0, "explorer")` | read-only tools; read-only sandbox from the parent's fake bwrap prober |
| C7 | implementer delegate | `buildPromptDelegate(t, 0, "implementer")` | coordinator-workflow plugin role |
| C8 | delegate with role override and preloaded skills | `buildDelegateWithRoleOverrideSession` | hand-built depth-1 delegate with a sentinel role override and a sentinel skill body |

## Branch map

Every body in the template runs in at least one configuration. An `{{ if }}` with no `{{ else }}` can only hide a bad reference in its body, so only its true side needs a configuration. Task 2's typed-input test (extended in Task 8) asserts each premise in the "Why" column.

| Template construct (after PR 3) | Runs in | Why |
| --- | --- | --- |
| `{{ if .BaseInstructionsOverride }}` body | C3 | `SystemPromptFile` set |
| `{{ else }}` (the behavioral block) | C1, C2, C4-C8 | no override |
| `{{ if and .IsSubagent .CanDelegate }}` allowance paragraph | C4 | delegate with allowance 1 |
| `{{ if or (not .IsSubagent) .CanDelegate }}` delegation, background jobs | C1, C4 | root; `CanDelegate` |
| `{{ if and .IsSubagent (not .CanDelegate) }}` delegated task limits | C5 | leaf |
| `{{ if not .IsSubagent }}` git safety, security, task tracking | C1 | root |
| `{{ if .HasAskUser }}` ask user | C1 | interactive root |
| workflow `{{ if .HasTool "read_transcript" }}` (twice), `{{ if .HasTool "job_watch" }}` | C1 | root has both tools |
| context management `{{ if .HasTool "compact_context" }}` | C1 | root has the tool |
| tools `{{ if .UnavailableProfileToolNames }}` and its two ranges | C6 | explorer lacks most profile tools |
| `{{ if eq .Surface "openai" }}` openai tools append | C2 | openai surface |
| openai append `{{ if .HasTool "apply_patch" }}` | C2 | every session registers `apply_patch` unless an allowlist removes it |
| transcripts outer `{{ if or ... }}`, `find_session_transcripts` and `read_transcript` bodies | C1 | root has both tools; `find_session_transcripts` needs the state dir |
| environment `{{- if .ResourceCapsJSON }}` | C1 | resource caps seam |
| environment `{{- if .Sandbox }}` | C6 | read-only delegate sandbox |
| environment `{{- range .Capabilities }}` | all | a local environment always reports `PATH` |
| git `{{ if .IsGitRepo }}`, `{{ if .GitRecentCommitTitles }}` and its range | C1 | real repo with one commit |
| git `{{ else }}` | C2 | not a repo |
| workspace outer `{{ if or ... }}`, `.WorkspaceTree`, `.BuildInfo` | C1 | files plus `go.mod` |
| `{{ .Role }}` | C2 (plugin override), C6 (bundled body), C8 (sentinel override) | |
| activated skills `{{ if .ActivatedSkillBodies }}` and its range | C8 | sentinel skill body |
| `{{ if not .IsSubagent }}` skills catalog, available agents, project docs | C1 | root |
| skills `{{ if .Skills }}`, `{{ if .HasUseSkill }}`, `{{ if $.HasUseSkill }}` | C1 | fixture skill; root registers `use_skill` |
| available agents `{{ if .AvailableAgents }}`, `{{ if .TaskList }}`, `{{ else }}`, `{{ if .ReplacedByParentTasks }}` | C1 | builtins include `explorer` (tasks, one inserting parent tasks) and agents with no tasks |
| project docs `{{ range .ProjectDocs }}`, `{{ if .Path }}` | C1 | project and personal `AGENTS.md` |
| `{{ if .NonInteractive }}` | C2 | headless root |
| `{{ if .UserInstructionOverride }}` | C3 | set |
| `{{ range .CLIAppends }}` | C3 | one append file |

**Unreachable, reported to Jesse:** the skills catalog's two `read_file` branches (the `{{ else }}` after `{{ if .HasUseSkill }}` and after `{{ if $.HasUseSkill }}`). Only root sessions render the catalog, root sessions never get a tool allowlist (`subagents.go` is the only place that sets `spawn.allowedToolNames`), and `registerCoreTools` always registers `use_skill`. Part 2 can delete them. This plan leaves them in place, because PR 3 must not change what the template can render.

## The render comparison

PR 2 and PR 3 prove their effect on the prompt with a scratch test that renders all eight configurations to files. It is never committed. Every temporary path in a render lives under the test binary's host temp root (`TestMain` redirects it), so the test collapses those paths to `<TMP>`, and two runs on the same day compare cleanly.

Scratch file, `agent/prompt_render_compare_scratch_test.go`:

```go
package agent

// Scratch file for the one-time render comparison in
// docs/superpowers/plans/2026-09-26-system-prompt-collapse.md. Never commit it.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestScratchRenderPromptConfigs(t *testing.T) {
	out := os.Getenv("PROMPT_RENDER_OUT")
	if out == "" {
		t.Skip("set PROMPT_RENDER_OUT to an existing directory")
	}
	roots := []string{regexp.QuoteMeta(os.TempDir())}
	if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		roots = append([]string{regexp.QuoteMeta(resolved)}, roots...)
	}
	tmpPath := regexp.MustCompile("(" + strings.Join(roots, "|") + ")[A-Za-z0-9._/-]*")
	for _, cfg := range promptConfigs() {
		s := cfg.build(t)
		prompt, warning := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("%s: %s", cfg.name, warning)
		}
		name := strings.ReplaceAll(cfg.name, " ", "-") + ".md"
		if err := os.WriteFile(filepath.Join(out, name), []byte(tmpPath.ReplaceAllString(prompt, "<TMP>")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
```

Procedure:

```bash
S="$(mktemp -d)" && mkdir -p "$S/before" "$S/after"
# Before any edit on the branch: write the scratch file above, then
(cd agent && PROMPT_RENDER_OUT="$S/before" go test -count=1 -run '^TestScratchRenderPromptConfigs$' .)
# After the change:
(cd agent && PROMPT_RENDER_OUT="$S/after" go test -count=1 -run '^TestScratchRenderPromptConfigs$' .)
diff -ru "$S/before" "$S/after"
# When the PR's work is done, delete the scratch file:
rm agent/prompt_render_compare_scratch_test.go
```

Run both renders on the same day, since the environment block carries today's date. If the diff shows noise that is not a prompt change (a path the normalizer missed), extend the normalizer and rerun both renders; never explain a real difference away. Paste the command and the diff (or "no differences") into the PR description, together with the scratch file's source.

---

## PR 1: assembly tests

Work on the current branch, `claude/evener-system-prompt-cleanup-69b0b3`, which already holds the spec and this plan.

### Task 1: The eight configurations and the render test

**Files:**
- Create: `agent/system_prompt_test.go`

**Interfaces:**
- Consumes (existing test kit, package `agent`): `newSession(t, opts...)`, `withAdapter`, `withProfile`, `withDir`, `withConfig` (`agent/testkit_test.go`); `newAnthropicProfile(model string) *provider.Profile` (`agent/profile_testhelpers_test.go`); `initGitRepo(t, dir)` (`agent/project_docs_test.go`); `writeTestFile(t, path, content)` (`agent/session_tools_doctor_test.go`); `coordinatorWorkflowSessionConfig(t, cfg) SessionConfig` and `coordinatorWorkflowAgentForTest(t, name) plugin.Agent` (`agent/coordinator_workflow_plugin_test_helpers_test.go`); `bwrapCapableProber(home string) sandbox.Prober` (`agent/session_sandbox_gate_test.go`); `releasePreparedTreeSlot(*preparedSubagentRun)` (`agent/tree_counter.go`); the context key `ctxDelegationAllowance` (`agent/session_tools.go`); `(*Session).prepareSubagentRun(ctx, task, model, workingDir string, maxTurns int, agentType, reasoningEffort string, parentTasks []task.TaskTemplate, grantTools []string) (*preparedSubagentRun, error)`.
- Produces: `type promptConfig struct{ name string; build func(t *testing.T) *Session }`; `promptConfigs() []promptConfig`; the builders `buildRootInteractiveAnthropicSession`, `buildRootHeadlessCoordinatorSession`, `buildRootWithOverridesSession`, `buildDelegateWithRoleOverrideSession` (each `func(t *testing.T) *Session`) and `buildPromptDelegate(t *testing.T, allowance int, agentType string) *Session`; the sentinel constants `sentinelProjectDoc`, `sentinelPersonalDoc`, `sentinelSkill`, `sentinelAgent`, `sentinelRole`, `sentinelActivatedSkill`, `sentinelBaseInstructions`, `sentinelUserInstructions`, `sentinelAppend`.

- [ ] **Step 1: Write the configurations and the render test**

Create `agent/system_prompt_test.go`:

```go
package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/schema"
)

// Opaque sentinels for the operator inputs the prompt must carry. They prove
// an input crossed into the rendered prompt without pinning any prose.
const (
	sentinelProjectDoc       = "SENTINEL-PROJECT-DOC-3c1f"
	sentinelPersonalDoc      = "SENTINEL-PERSONAL-DOC-8e20"
	sentinelSkill            = "SENTINEL-SKILL-5a7d"
	sentinelAgent            = "SENTINEL-AGENT-91b4"
	sentinelRole             = "SENTINEL-ROLE-2f6e"
	sentinelActivatedSkill   = "SENTINEL-ACTIVATED-SKILL-d03a"
	sentinelBaseInstructions = "SENTINEL-BASE-INSTRUCTIONS-4f1c"
	sentinelUserInstructions = "SENTINEL-USER-INSTRUCTIONS-7b3d"
	sentinelAppend           = "SENTINEL-APPEND-9a2e"
)

// promptConfig is one real session whose system prompt the assembly tests
// render. Together the configurations execute every body in the prompt
// template; a new template branch needs a configuration that runs it.
type promptConfig struct {
	name  string
	build func(t *testing.T) *Session
}

func promptConfigs() []promptConfig {
	return []promptConfig{
		{"root interactive anthropic", buildRootInteractiveAnthropicSession},
		{"root headless openai coordinator", buildRootHeadlessCoordinatorSession},
		{"root with overrides", buildRootWithOverridesSession},
		{"delegate that can delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 1, "") }},
		{"leaf delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "") }},
		{"explorer delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "explorer") }},
		{"implementer delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "implementer") }},
		{"delegate with role override and preloaded skills", buildDelegateWithRoleOverrideSession},
	}
}

// buildRootInteractiveAnthropicSession is an interactive root on the anthropic
// surface, in a real git repository with a build marker, a project doc, a
// personal doc, a skill, a state dir, and resource caps, so every data block
// renders.
func buildRootInteractiveAnthropicSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module promptfixture\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(dir, "AGENTS.md"), sentinelProjectDoc+"\n")
	personalDoc := filepath.Join(t.TempDir(), "AGENTS.md")
	writeTestFile(t, personalDoc, sentinelPersonalDoc+"\n")
	skills := t.TempDir()
	writeTestFile(t, filepath.Join(skills, "fixture-skill", "SKILL.md"),
		"---\nname: fixture-skill\ndescription: "+sentinelSkill+"\n---\nBody.\n")
	return newSession(t,
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withProfile(newAnthropicProfile("claude-test")),
		withDir(dir),
		withConfig(SessionConfig{
			MaxSubagentDepth: 2,
			StateDir:         t.TempDir(),
			AgentsDocPath:    personalDoc,
			SkillsDirs:       []string{skills},
			testOnly: testConfig{
				environmentInfo: func(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
					info := envInfoFromEnv(env, clk)
					info.Resources = &schema.ResourceCaps{CPUs: 4, MemoryMB: 8192}
					return info
				},
			},
		}))
}

// buildRootHeadlessCoordinatorSession is a headless root on the openai surface
// whose coordinator role arrives from the coordinator-workflow plugin. Its
// working directory holds one file and no build marker.
func buildRootHeadlessCoordinatorSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "notes.txt"), "fixture\n")
	return newSession(t, withDir(dir), withConfig(coordinatorWorkflowSessionConfig(t, SessionConfig{
		AgentName:        "coordinator",
		NonInteractive:   true,
		MaxSubagentDepth: 2,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		testOnly:         testConfig{skipGitSnapshot: true},
	})))
}

// buildRootWithOverridesSession is a root whose base instructions come from a
// --system-prompt file, with one append file and a user instruction override,
// in an empty working directory, so no workspace block renders.
func buildRootWithOverridesSession(t *testing.T) *Session {
	t.Helper()
	files := t.TempDir()
	base := filepath.Join(files, "base.md")
	appended := filepath.Join(files, "append.md")
	writeTestFile(t, base, sentinelBaseInstructions+"\n")
	writeTestFile(t, appended, sentinelAppend+"\n")
	return newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		MaxSubagentDepth:        2,
		AgentsDocPath:           filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		SystemPromptFile:        base,
		SystemPromptAppend:      []string{appended},
		UserInstructionOverride: sentinelUserInstructions,
		testOnly:                testConfig{skipGitSnapshot: true},
	}))
}

// buildPromptDelegate spawns a real delegate through prepareSubagentRun. An
// allowance above zero lets the delegate delegate in turn. The parent's fake
// bwrap prober lets a read-only delegate (explorer) resolve its sandbox
// without a bwrap binary on the host.
func buildPromptDelegate(t *testing.T, allowance int, agentType string) *Session {
	t.Helper()
	parent := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		MaxSubagentDepth: 2,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		testOnly: testConfig{
			skipGitSnapshot: true,
			sandboxProber:   bwrapCapableProber(t.TempDir()),
		},
	}))
	if agentType == "implementer" {
		parent.pluginAgents[agentType] = coordinatorWorkflowAgentForTest(t, agentType)
	}
	ctx := context.Background()
	if allowance > 0 {
		ctx = context.WithValue(ctx, ctxDelegationAllowance, allowance)
	}
	prepared, err := parent.prepareSubagentRun(ctx, "task", "", "", 0, agentType, "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun(%q): %v", agentType, err)
	}
	child := prepared.sub.sess
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		child.Close()
	})
	return child
}

// buildDelegateWithRoleOverrideSession is a hand-built depth-1 delegate
// carrying a role override and preloaded skill bodies, the inputs the spawn
// path hands a child.
func buildDelegateWithRoleOverrideSession(t *testing.T) *Session {
	t.Helper()
	return newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		spawn: spawnConfig{
			parentSessionID:      "01PARENT",
			depth:                1,
			subagentTask:         "perform the delegated task",
			rolePromptOverride:   sentinelRole,
			activatedSkillBodies: []string{sentinelActivatedSkill},
		},
		testOnly: testConfig{skipGitSnapshot: true},
	}))
}

// TestSystemPromptRendersForEveryConfiguration renders each configuration
// through the session's own render path. text/template reports a bad field or
// method reference only in a branch it executes, so this is the check that
// every body of the template still runs.
func TestSystemPromptRendersForEveryConfiguration(t *testing.T) {
	t.Parallel()
	for _, cfg := range promptConfigs() {
		t.Run(cfg.name, func(t *testing.T) {
			t.Parallel()
			s := cfg.build(t)
			prompt, warning := s.renderSystemPrompt(s.env)
			if warning != "" {
				t.Fatalf("render failed: %s", warning)
			}
			if strings.TrimSpace(prompt) == "" {
				t.Fatal("rendered an empty prompt")
			}
		})
	}
}
```

- [ ] **Step 2: Run it**

Run: `(cd agent && go test -count=1 -run '^TestSystemPromptRendersForEveryConfiguration$' .)`
Expected: PASS, eight subtests. If a builder fails, fix the builder; the test characterizes today's code, so it must pass without production changes.

- [ ] **Step 3: Prove it can fail**

Today's resolver swallows an error inside a section file (it renders the section empty and logs an `ERROR:` source), so put the sabotage in the top-level template, where errors propagate. In `agent/prompts/templates/system.md.tmpl`, change `{{ if .HasAskUser }}` to `{{ if .HasAskUser }}{{ .NoSuchField }}`.

Run: `(cd agent && go test -count=1 -run '^TestSystemPromptRendersForEveryConfiguration$' .)`
Expected: FAIL in `root_interactive_anthropic` with `render failed: template render failed: ... can't evaluate field NoSuchField`.

Undo the edit and confirm `git diff agent/prompts/templates/system.md.tmpl` is empty.

- [ ] **Step 4: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/system_prompt_test.go
git add agent/system_prompt_test.go
git status --short && git diff --cached --stat
git commit -m "test(agent): render the system prompt for eight real session configurations"
```

### Task 2: Typed inputs

**Files:**
- Modify: `agent/system_prompt_test.go`

**Interfaces:**
- Consumes: the builders from Task 1; `(*Session).buildPromptData(env execenv.ExecutionEnvironment) promptData`; `promptData.HasTool(name string) bool`.
- Produces: `TestPromptDataTypedInputs`; `checkPromptInput(t *testing.T, name string, got, want bool)`. Task 8 extends the test's `check` functions.

- [ ] **Step 1: Write the test**

Append to `agent/system_prompt_test.go`:

```go
// TestPromptDataTypedInputs checks the typed inputs buildPromptData computes
// for each configuration. The template cannot notice a wrong input, and these
// values are also the premises that let the eight configurations run every
// template body (see the branch map in the collapse plan).
func TestPromptDataTypedInputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func(*testing.T) *Session
		check func(*testing.T, promptData)
	}{
		{"root interactive anthropic", buildRootInteractiveAnthropicSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "HasAskUser", d.HasAskUser, true)
			checkPromptInput(t, "CanDelegate", d.CanDelegate, true)
			checkPromptInput(t, "NonInteractive", d.NonInteractive, false)
			checkPromptInput(t, "HasUseSkill", d.HasUseSkill, true)
			checkPromptInput(t, "skills present", len(d.Skills) > 0, true)
			checkPromptInput(t, "IsGitRepo", d.IsGitRepo, true)
			checkPromptInput(t, "commit titles present", len(d.GitRecentCommitTitles) > 0, true)
			checkPromptInput(t, "workspace tree present", d.WorkspaceTree != "", true)
			checkPromptInput(t, "build info present", d.BuildInfo != "", true)
			checkPromptInput(t, "resource caps present", d.ResourceCapsJSON != "", true)
			checkPromptInput(t, "capabilities present", len(d.Capabilities) > 0, true)
			checkPromptInput(t, "project and personal docs present", len(d.ProjectDocs) == 2, true)
			for _, doc := range d.ProjectDocs {
				checkPromptInput(t, "project doc path set", doc.Path != "", true)
			}
			var withTasks, withoutTasks, insertsParentTasks bool
			for _, a := range d.AvailableAgents {
				if len(a.TaskList) == 0 {
					withoutTasks = true
				}
				for _, entry := range a.TaskList {
					withTasks = true
					if entry.ReplacedByParentTasks {
						insertsParentTasks = true
					}
				}
			}
			checkPromptInput(t, "an available agent with tasks", withTasks, true)
			checkPromptInput(t, "an available agent without tasks", withoutTasks, true)
			checkPromptInput(t, "a task that inserts parent tasks", insertsParentTasks, true)
			for _, name := range []string{"read_transcript", "find_session_transcripts", "job_watch", "compact_context", "apply_patch"} {
				checkPromptInput(t, "HasTool "+name, d.HasTool(name), true)
			}
		}},
		{"root headless openai coordinator", buildRootHeadlessCoordinatorSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
			checkPromptInput(t, "NonInteractive", d.NonInteractive, true)
			checkPromptInput(t, "HasTool apply_patch", d.HasTool("apply_patch"), true)
			checkPromptInput(t, "IsGitRepo", d.IsGitRepo, false)
			checkPromptInput(t, "workspace tree present", d.WorkspaceTree != "", true)
			checkPromptInput(t, "build info present", d.BuildInfo != "", false)
		}},
		{"root with overrides", buildRootWithOverridesSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride is the file", d.BaseInstructionsOverride == sentinelBaseInstructions, true)
			checkPromptInput(t, "UserInstructionOverride is the config value", d.UserInstructionOverride == sentinelUserInstructions, true)
			checkPromptInput(t, "CLIAppends holds the append file", len(d.CLIAppends) == 1 && d.CLIAppends[0] == sentinelAppend+"\n", true)
			checkPromptInput(t, "workspace block empty", d.WorkspaceTree == "" && d.BuildInfo == "", true)
		}},
		{"delegate that can delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 1, "") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "CanDelegate", d.CanDelegate, true)
			checkPromptInput(t, "DelegationAllowance is 1", d.DelegationAllowance == 1, true)
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
		}},
		{"leaf delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "CanDelegate", d.CanDelegate, false)
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
		}},
		{"explorer delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "explorer") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "CanDelegate", d.CanDelegate, false)
			checkPromptInput(t, "sandbox line present", d.Sandbox != "", true)
			checkPromptInput(t, "unavailable profile tools listed", len(d.UnavailableProfileToolNames) > 0, true)
		}},
		{"implementer delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "implementer") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "CanDelegate", d.CanDelegate, false)
		}},
		{"delegate with role override and preloaded skills", buildDelegateWithRoleOverrideSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
			checkPromptInput(t, "ActivatedSkillBodies holds the preloaded body",
				len(d.ActivatedSkillBodies) == 1 && d.ActivatedSkillBodies[0] == sentinelActivatedSkill, true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.build(t)
			tc.check(t, s.buildPromptData(s.env))
		})
	}
}

func checkPromptInput(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
```

- [ ] **Step 2: Run it**

Run: `(cd agent && go test -count=1 -run '^TestPromptDataTypedInputs$' .)`
Expected: PASS. A failing premise means the configuration does not run the branch the map assigns it. Change the configuration's data (Task 1's builders), never the expected value, and update the branch map in this plan to match.

- [ ] **Step 3: Prove it can fail**

In the `leaf delegate` case, change `checkPromptInput(t, "CanDelegate", d.CanDelegate, false)` to `true`.
Run the same command. Expected: FAIL with `CanDelegate = false, want true`. Undo the change.

- [ ] **Step 4: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/system_prompt_test.go
git add agent/system_prompt_test.go
git status --short && git diff --cached --stat
git commit -m "test(agent): check the typed inputs behind every prompt branch"
```

### Task 3: Operator inputs arrive exactly once

**Files:**
- Modify: `agent/system_prompt_test.go`

**Interfaces:**
- Consumes: the builders and sentinels from Task 1; `plugin.Agent{Name, Description string}` (`agent/plugin/agents.go`); `(*Session).pluginAgents map[string]plugin.Agent`.
- Produces: `TestSystemPromptCarriesEachOperatorInputOnce`.

- [ ] **Step 1: Write the test**

Add `"primeradiant.com/evener/agent/plugin"` to the imports of `agent/system_prompt_test.go`, then append:

```go
// TestSystemPromptCarriesEachOperatorInputOnce proves each operator input
// crosses into the rendered prompt exactly once, using opaque sentinels, so a
// dropped input and a doubled one both fail.
func TestSystemPromptCarriesEachOperatorInputOnce(t *testing.T) {
	t.Parallel()
	root := buildRootInteractiveAnthropicSession(t)
	root.pluginAgents["sentinel-agent"] = plugin.Agent{Name: "sentinel-agent", Description: sentinelAgent}
	cases := []struct {
		name      string
		session   *Session
		sentinels []string
	}{
		{"root inputs", root, []string{sentinelProjectDoc, sentinelPersonalDoc, sentinelSkill, sentinelAgent}},
		{"root overrides", buildRootWithOverridesSession(t), []string{sentinelBaseInstructions, sentinelAppend, sentinelUserInstructions}},
		{"delegate spawn inputs", buildDelegateWithRoleOverrideSession(t), []string{sentinelRole, sentinelActivatedSkill}},
	}
	for _, tc := range cases {
		prompt, warning := tc.session.renderSystemPrompt(tc.session.env)
		if warning != "" {
			t.Fatalf("%s: render failed: %s", tc.name, warning)
		}
		for _, sentinel := range tc.sentinels {
			if got := strings.Count(prompt, sentinel); got != 1 {
				t.Errorf("%s: %s appears %d times in the rendered prompt, want 1", tc.name, sentinel, got)
			}
		}
	}
}
```

- [ ] **Step 2: Run it**

Run: `(cd agent && go test -count=1 -run '^TestSystemPromptCarriesEachOperatorInputOnce$' .)`
Expected: PASS.

- [ ] **Step 3: Prove it can fail**

In `agent/session_prompts.go` `buildPromptData`, change `data.CLIAppends = append(data.CLIAppends, string(b))` to `data.CLIAppends = append(data.CLIAppends, string(b), string(b))`.
Run the same command. Expected: FAIL with `root overrides: SENTINEL-APPEND-9a2e appears 2 times in the rendered prompt, want 1`. Undo the change and confirm `git diff agent/session_prompts.go` is empty.

- [ ] **Step 4: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/system_prompt_test.go
git add agent/system_prompt_test.go
git status --short && git diff --cached --stat
git commit -m "test(agent): each operator input reaches the system prompt exactly once"
```

### Task 4: The prompt reaches the provider unchanged as a user message

The system-role path is already proven by `TestSystemPromptConsistency_WithAndWithoutCache` (`agent/session_perf_test.go`), which compares the sent system message to `cachedSystemPrompt` byte for byte, and by `TestSystemPromptOccupiesOnlyTheOpeningMessage` (`cmd/evener/run_drain_prompt_literal_test.go`). They stay as they are. The two `--system-prompt-as-user` tests prove the prompt is present only through the `<environment>` marker. This task makes them exact.

**Files:**
- Modify: `agent/session_config_test.go` (`TestSession_SystemPromptAsUser_CombinesIntoOneMessage`, `TestSession_SystemPromptAsUserPreservesImageParts`)

**Interfaces:**
- Consumes: `prependSystemPromptToUserMessage(systemPrompt string, user llm.Message) llm.Message` (`agent/session_prompts.go`), which puts `systemPrompt + "\n\n"` in the first content part.

- [ ] **Step 1: Make `CombinesIntoOneMessage` exact**

In `TestSession_SystemPromptAsUser_CombinesIntoOneMessage`, directly after the `NewSession` error check, add:

```go
	wantLeading := sess.cachedSystemPrompt + "\n\n"
```

Then replace:

```go
	combined := msgs[0].Text()

	// System prompt should be present (environment block is always included).
	if !strings.Contains(combined, "<environment>") {
		t.Fatal("combined message missing system prompt content")
	}
```

with:

```go
	combined := msgs[0].Text()

	// The rendered system prompt arrives unchanged as the first text part of
	// the leading user message.
	if len(msgs[0].Content) == 0 || msgs[0].Content[0].Kind != llm.ContentText || msgs[0].Content[0].Text != wantLeading {
		t.Fatal("leading user message does not start with the rendered system prompt")
	}
```

- [ ] **Step 2: Make `PreservesImageParts` exact**

In `TestSession_SystemPromptAsUserPreservesImageParts`, add the same `wantLeading := sess.cachedSystemPrompt + "\n\n"` line directly after its `NewSession` error check, and replace:

```go
	for _, part := range envMsg.Content {
		if part.Kind == llm.ContentText && strings.Contains(part.Text, "<environment>") {
			sawSystem = true
		}
	}
```

with:

```go
	sawSystem = len(envMsg.Content) > 0 && envMsg.Content[0].Kind == llm.ContentText && envMsg.Content[0].Text == wantLeading
```

- [ ] **Step 3: Run them**

Run: `(cd agent && go test -count=1 -run '^TestSession_SystemPromptAsUser' .)`
Expected: PASS.

- [ ] **Step 4: Prove it can fail**

In `agent/session_prompts.go` `prependSystemPromptToUserMessage`, change `systemPrompt + "\n\n"` to `systemPrompt + "\n"`. Run the same command. Expected: both tests FAIL. Undo the change and confirm `git diff agent/session_prompts.go` is empty.

- [ ] **Step 5: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/session_config_test.go
git add agent/session_config_test.go
git status --short && git diff --cached --stat
git commit -m "test(agent): the as-user system prompt arrives unchanged"
```

### Task 5: Delete prose assertions in the resolver, profile, skills, and agent tests

Each step names what to delete. "Delete the whole test" means the function and its doc comment. Where a deleted assertion was the only check on a typed input, the replacement is given, or the note names the Task 2 check that covers it.

**Files:**
- Modify: `agent/section_resolver_test.go`, `agent/profile_test.go`, `agent/bundled_prompt_tool_mentions_test.go`, `agent/session_ask_test.go`, `agent/session_perf_test.go`, `agent/builtin_skills_test.go`, `agent/session_skills_test.go`, `agent/builtin_agents_test.go`, `agent/session_capabilities_test.go`, `tools/tool-fluency/probes-nbcf/README.md`

- [ ] **Step 1: `agent/section_resolver_test.go`**

Delete these whole tests: `TestEnvironmentSection_SandboxLine`, `TestGitSection_SingleSourceAndLabeled`, `TestSubagentTemplate_IncludesGitSection`, `TestSubagentTemplate_StructuralRegression`, `TestTranscriptsSection_TeachesToolsNotRawRead`, `TestIdentitySection_CleanupRuleScopedToDeliverables`, `TestTranscriptsSection_SilentWithoutTheTools`, `TestWorkflowSection_GatesItsToolMentions`, `TestWorkflowSectionDefinesRootCauseToActionTransition`, `TestReviewerTemplate_UsesCommunicateDecisionContract`, `TestVerificationSectionDefinesIncompleteGates`, `TestVerificationSectionRequiresSmokeCaseBeforeMatrix`, `TestContextManagementSectionIsAdvisoryAndBounded`, `TestContextManagementSection_SilentAboutAnUncallableCompactTool`, `TestCommunicateSectionReportsPhaseChangeCheckpoint`, `TestOrchestrationPostureTemplateInclusion`.

`TestReviewerTemplate_UsesCommunicateDecisionContract` also asserts reviewer-role prose (#2401's territory); it goes whole because what remains after its prose lines cannot survive PR 3's field removals.

In `TestSystemTemplate_StructuralRegression`, delete the `markers := []string{...}` slice and the loop that checks their order. Keep the rest (the git-safety source-label check and the source count); it tests the resolver and goes with the file in PR 3.

Delete `postureSectionResolver`; after these deletions nothing calls it (confirm with `rg -n 'postureSectionResolver' agent`, which must print only its definition). The remaining resolver tests stay until PR 3.

The tool-mention gates these tests checked stay covered by `TestShippedPromptsOnlyNameToolsTheSessionHas`. The `CanDelegate` and `IsSubagent` sections they checked are covered by Task 2 and, from PR 3, Task 8.

- [ ] **Step 2: `agent/profile_test.go`**

Delete these whole tests: `TestSystemPrompt_ImplementerWarnsOnUnavailableTools`, `TestBuildSystemPrompt_IncludesBackgroundJobsSection`, `TestBuildSystemPrompt_PinsAntiPollGuidance`, `TestSubagentPrompt_DoesNotIncludeBackgroundJobsSection`, `TestProviderProfiles_BuildSystemPrompt_IncludesEnvironment`, `TestAllProfiles_SystemPromptContainsSkillsGuidance`, `TestBuildSystemPrompt_IncludesSkillsList`, `TestBuildSystemPrompt_OpenAI_SkillsWithUseSkill`, `TestBuildSystemPrompt_NoSkills_NoSkillsSection`, `TestBuildSystemPrompt_ToolUsageBeforeProjectDocs`, `TestBuildSystemPrompt_EmptyWorkspace`, `TestBuildSystemPrompt_WorkspaceAnnotation`. The four skills tests render hand-built `promptData`, so all they checked was template formatting; C1 executes the skills block.

In `TestBuildSystemPrompt_DoesNotDuplicateProviderToolDescriptions`, delete the `if strings.Contains(prompt, "Tools:")` block. In `TestBuildSystemPrompt_DoesNotDuplicateMCPOrCustomToolDescriptions`, remove `"MCP tools:",` and `"Custom tools:",` from the `unwanted` list. In `TestBuildSystemPrompt_WorkspaceSection`, delete the `<workspace>` and `</workspace>` checks and the ordering block that computes `wsIdx`, `envIdx`, and `toolIdx`; keep the file-name checks, which test the Go-generated tree.

Leave `TestSystemPrompt_CoordinatorHasImpossibleDelegationException` and `TestSystemPrompt_DefaultAgentDoesNotUseCoordinatorRole` alone (role prose, #2401).

- [ ] **Step 3: `agent/bundled_prompt_tool_mentions_test.go`**

Delete `TestBundledDelegatePromptUsesStableControlIdentity` and `TestBundledDelegatePromptPreservesWatchSupervisionAndShellGuidance`. Keep `TestShippedPromptsOnlyNameToolsTheSessionHas` and `TestBundledPromptBodiesOnlyNameToolsTheirAgentHas`.

- [ ] **Step 4: `agent/session_ask_test.go`**

Delete `TestAskUser_PromptSectionVisibleForInteractiveRoot`, `TestAskUser_PromptSectionHiddenWhenNonInteractive`, and `TestAskUser_PromptSectionHiddenForSubagent`. Task 2 checks `HasAskUser` for an interactive root (C1), a headless root (C2), and delegates (C4, C5, C8).

- [ ] **Step 5: `agent/session_perf_test.go`**

Delete `TestCachedSystemPromptComponents_NonInteractiveGuidance` (Task 2 checks `NonInteractive`) and `TestCachedSystemPromptComponents_AgentSection` (Task 2 checks the available-agents entries). In `TestCachedSystemPromptComponents_DoesNotDuplicateMCPToolDescriptions`, delete the `"MCP tools:"` absence check.

In `TestCachedSystemPromptComponents_UsesProviderVisibleToolNames`, replace everything after `defer sess.Close()` with:

```go
	data := sess.buildPromptData(sess.currentEnv())
	if !slices.Contains(data.CallableToolNames, "exec_command") {
		t.Fatalf("CallableToolNames = %q, want the provider-visible exec_command", data.CallableToolNames)
	}
	if slices.Contains(data.CallableToolNames, "shell") {
		t.Fatalf("CallableToolNames = %q, must not list the canonical shell", data.CallableToolNames)
	}
	if slices.Contains(data.UnavailableProfileToolNames, "exec_command") {
		t.Fatalf("UnavailableProfileToolNames = %q, must not mark exec_command unavailable", data.UnavailableProfileToolNames)
	}
```

and add `"slices"` to the file's imports.

- [ ] **Step 6: `agent/builtin_skills_test.go`**

Delete `TestNonInteractive_SystemPromptContainsGuidance` and `TestNonInteractive_NotPresentWhenFalse` (Task 2 checks `NonInteractive` both ways).

Replace `TestEmbeddedSkills_InSystemPrompt` with:

```go
func TestEmbeddedSkills_InSystemPrompt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})
	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data := sess.buildPromptData(sess.currentEnv())
	if !data.HasUseSkill {
		t.Fatal("HasUseSkill = false, want true")
	}
	if !slices.ContainsFunc(data.Skills, func(s skillEntry) bool { return s.CatalogNameOrName() == "doctoring-evener" }) {
		t.Fatalf("prompt skills = %+v, want the embedded doctoring-evener skill", data.Skills)
	}
}
```

Replace `TestOpenAI_SkillsWithUseSkillInSystemPrompt` with:

```go
func TestOpenAI_SkillsWithUseSkillInSystemPrompt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "my-skill",
		"---\nname: my-skill\ndescription: \"Test skill\"\n---\nBody.\n")

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data := sess.buildPromptData(sess.currentEnv())
	if !data.HasUseSkill {
		t.Fatal("HasUseSkill = false, want true on the openai surface")
	}
	if !slices.ContainsFunc(data.Skills, func(s skillEntry) bool {
		return s.CatalogNameOrName() == "my-skill" && s.Description == "Test skill" && s.Dir != ""
	}) {
		t.Fatalf("prompt skills = %+v, want my-skill with its description and directory", data.Skills)
	}
}
```

Add `"slices"` to the imports and remove any the compiler reports unused.

- [ ] **Step 7: `agent/session_skills_test.go`**

Keep these three test names (a comment elsewhere in the file names the first). Replace the body of `TestUseSkill_SystemPromptContainsSkillList` after `writeSkillMD(...)` with:

```go
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})
	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data := sess.buildPromptData(sess.currentEnv())
	if !slices.ContainsFunc(data.Skills, func(s skillEntry) bool {
		return s.CatalogNameOrName() == "greet" && s.Description == "Greeting skill"
	}) {
		t.Fatalf("prompt skills = %+v, want greet with its description", data.Skills)
	}
```

Replace the body of `TestOpenAI_SkillsSectionUsesUseSkill` after `writeSkillMD(...)` with:

```go
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data := sess.buildPromptData(sess.currentEnv())
	if !data.HasUseSkill {
		t.Fatal("HasUseSkill = false, want true on the openai surface")
	}
	if !slices.ContainsFunc(data.Skills, func(s skillEntry) bool {
		return s.CatalogNameOrName() == "greet" && strings.HasSuffix(s.Dir, filepath.Join("skills", "greet"))
	}) {
		t.Fatalf("prompt skills = %+v, want greet with its skill directory", data.Skills)
	}
```

Replace the body of `TestOpenAI_PluginSkillCatalogUsesNamespacedName` after the `os.WriteFile(... "SKILL.md" ...)` check with:

```go
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data := sess.buildPromptData(sess.currentEnv())
	if !slices.ContainsFunc(data.Skills, func(s skillEntry) bool { return s.CatalogNameOrName() == "skill-plugin:my-skill" }) {
		t.Fatalf("prompt skills = %+v, want the namespaced skill-plugin:my-skill", data.Skills)
	}
	if slices.ContainsFunc(data.Skills, func(s skillEntry) bool { return s.CatalogNameOrName() == "my-skill" }) {
		t.Fatalf("prompt skills = %+v, advertised the bare plugin skill name", data.Skills)
	}
```

Add `"slices"` to the imports and remove any the compiler reports unused.

- [ ] **Step 8: `agent/builtin_agents_test.go`**

Delete `TestAvailableAgentsSection_UsesAvailableAgentsTag`. In `TestSpawnAgent_PluginAgentGetsComposedPrompt`, delete the `"communicate"` check. In `TestSpawnAgent_DefaultSubagentGetsComposedPrompt`, delete the `"communicate"` and `"Delegated task limits"` checks. In `TestSpawnAgent_SystemPromptFileDoesNotOverrideSubagentPrompt`, delete the `"Delegated task limits"` check. Keep the role-prose checks (#2401) and the `ROOT ONLY CUSTOM PROMPT` sentinel checks, and keep the test names: `agent/session_init_worktree_seed100_fuzz_test.go` calls two of them.

- [ ] **Step 9: `agent/session_capabilities_test.go`**

In `TestCapabilityPreambleRendersInEnvironmentSection`, delete the `"\nSandbox: restricted (network off) — fixed for this session\n",` entry. The remaining entries are Go-generated lines and stay.

- [ ] **Step 10: `tools/tool-fluency/probes-nbcf/README.md`**

Replace the paragraph's first two sentences ("The first — positive phase-transition prompting in ... is implemented and covered by deterministic tests in `agent/section_resolver_test.go`.") with:

```
The first half, positive phase-transition prompting in the system prompt's
workflow, communicate, and verification guidance, is implemented. Its
phrase-pinning tests were removed under the testing policy
(`docs/developing-evener/testing.md`, "Prompt Prose Is Not a Test Oracle"),
so the behavior itself is what this eval measures.
```

- [ ] **Step 11: Vet and run**

```bash
(cd agent && go vet . && go vet -tags evenerfuzz . && GOOS=linux go vet .)
(cd agent && go test -count=1 -run 'TestSystemTemplate|TestSectionResolver|TestProviderProfile|TestBuildSystemPrompt|TestSystemPrompt_|TestShippedPrompts|TestBundledPromptBodies|TestAskUser|TestCachedSystemPrompt|TestEmbeddedSkills|TestOpenAI_|TestUseSkill|TestSpawnAgent|TestSession_DefaultFallback|TestCapabilityPreamble' .)
make lint-evenerfuzz
```

Expected: PASS.

- [ ] **Step 12: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/section_resolver_test.go agent/profile_test.go agent/bundled_prompt_tool_mentions_test.go agent/session_ask_test.go agent/session_perf_test.go agent/builtin_skills_test.go agent/session_skills_test.go agent/builtin_agents_test.go agent/session_capabilities_test.go
git add agent/section_resolver_test.go agent/profile_test.go agent/bundled_prompt_tool_mentions_test.go agent/session_ask_test.go agent/session_perf_test.go agent/builtin_skills_test.go agent/session_skills_test.go agent/builtin_agents_test.go agent/session_capabilities_test.go tools/tool-fluency/probes-nbcf/README.md
git status --short && git diff --cached --stat
git commit -m "test(agent): drop prompt-prose assertions from resolver, profile, skills, and agent tests"
```

### Task 6: Rewrite the remaining prose-guarded tests as typed checks

**Files:**
- Modify: `agent/plugin_prompt_test.go`, `agent/plugin_real_test.go`, `agent/plugin_e2e_test.go`, `agent/plugin_integration_live_test.go`, `agent/session_surface_behavior_test.go`, `agent/provider_instance_integration_test.go`, `agent/provider_instance_1b_integration_test.go`, `agent/resource_caps_boundary_test.go`, `agent/resource_caps_sandbox_linux_test.go`, `agent/session_config_test.go`, `agent/transcript_test.go`, `cmd/evener/serve_test.go`, `cmd/evener/seed_coverage_fuzz_test.go`, `cmd/evener/serve_coverage_fuzz_test.go`

**Interfaces:**
- Produces: `availableAgentEntriesForTest(t *testing.T, agents map[string]plugin.Agent, allowance int, allowedTools []string) []agentEntry` and `depthOneSubagentForTest(t *testing.T, allowance int, allowedTools []string) *Session` in `agent/plugin_prompt_test.go`; `promptResourceCaps(t *testing.T, s *Session, env execenv.ExecutionEnvironment) (renderedResourceCaps, bool)` in `agent/resource_caps_boundary_test.go`; `projectDocPaths(s *Session) []string` in `agent/session_config_test.go`. Task 9 updates their `buildPromptData` calls.

- [ ] **Step 1: `agent/plugin_prompt_test.go` helpers**

Replace `renderAvailableAgentsSectionForTest`, `renderAvailableAgentsSectionWithAllowance`, and `renderAvailableAgentsSectionWithAllowanceAndTools` with:

```go
// availableAgentEntriesForTest returns the available-agents entries a root
// session builds for agents. allowance >= 0 overrides the session's
// delegation allowance; allowedTools, when set, restricts its tool surface.
func availableAgentEntriesForTest(t *testing.T, agents map[string]plugin.Agent, allowance int, allowedTools []string) []agentEntry {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	cfg := SessionConfig{}
	cfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if allowance >= 0 {
		sess.mu.Lock()
		sess.delegationAllowance = allowance
		sess.mu.Unlock()
	}
	sess.pluginAgents = make(map[string]plugin.Agent, len(agents))
	maps.Copy(sess.pluginAgents, agents)
	return sess.buildPromptData(sess.currentEnv()).AvailableAgents
}
```

Replace `renderSubagentPromptWithAllowance` and `renderSubagentPromptWithAllowanceAndTools` with:

```go
// depthOneSubagentForTest builds a depth-1 child session with the given
// delegation allowance and, when allowedTools is set, that tool surface.
func depthOneSubagentForTest(t *testing.T, allowance int, allowedTools []string) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = allowance
	cfg.spawn.allowedToolNames = append([]string(nil), allowedTools...)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	if sess.depth == 0 {
		t.Fatal("expected a depth > 0 subagent session, got depth 0")
	}
	return sess
}
```

Remove the `bundled` import if the compiler reports it unused.

- [ ] **Step 2: `agent/plugin_prompt_test.go` tests**

Delete `TestSubagentPromptUsesDelegateSendForFollowup`.

Replace `TestSubagentPromptStatesAllowance` with:

```go
// TestSubagentPromptStatesAllowance pins spec §5 as typed inputs: a child with
// allowance > 0 can delegate and carries its allowance; a leaf cannot.
func TestSubagentPromptStatesAllowance(t *testing.T) {
	t.Parallel()
	granting := depthOneSubagentForTest(t, 2, nil)
	if data := granting.buildPromptData(granting.env); !data.CanDelegate || data.DelegationAllowance != 2 {
		t.Errorf("allowance-2 child: CanDelegate=%v DelegationAllowance=%d, want true and 2", data.CanDelegate, data.DelegationAllowance)
	}
	leaf := depthOneSubagentForTest(t, 0, nil)
	if data := leaf.buildPromptData(leaf.env); data.CanDelegate {
		t.Error("allowance-0 child: CanDelegate = true, want false")
	}
}
```

Replace `TestSubagentPromptSuppressesDelegationWhenToolsUnavailable` with:

```go
func TestSubagentPromptSuppressesDelegationWhenToolsUnavailable(t *testing.T) {
	t.Parallel()
	sess := depthOneSubagentForTest(t, 1, []string{"communicate", "delegate", "job_watch"})
	if data := sess.buildPromptData(sess.env); data.CanDelegate {
		t.Fatal("CanDelegate = true with an incomplete delegation tool surface, want false")
	}
}
```

Replace `TestAvailableAgentsSection_NoAgents` with:

```go
func TestAvailableAgentsSection_NoAgents(t *testing.T) {
	t.Parallel()
	for _, agents := range []map[string]plugin.Agent{nil, {}} {
		if got := availableAgentEntriesForTest(t, agents, -1, nil); len(got) != 0 {
			t.Errorf("available agents for %v = %+v, want none", agents, got)
		}
	}
}
```

In `TestAvailableAgentsSection_WithAgents`, replace everything from `result := renderAvailableAgentsSectionForTest(t, agents)` to the end of the function with:

```go
	entries := availableAgentEntriesForTest(t, agents, -1, nil)
	byName := make(map[string]agentEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	reviewer, ok := byName["my-plugin:reviewer"]
	if !ok || reviewer.Description != "Reviews code for quality" {
		t.Fatalf("reviewer entry = %+v (present %v)", reviewer, ok)
	}
	if tester, ok := byName["my-plugin:tester"]; !ok || tester.Description != "Generates test cases" {
		t.Fatalf("tester entry = %+v (present %v)", tester, ok)
	}
	for _, want := range []string{"communicate", "grep_files", "read_file", "task_list"} {
		if !strings.Contains(reviewer.DefaultTools, "`"+want+"`") {
			t.Errorf("reviewer DefaultTools = %q, want %q", reviewer.DefaultTools, want)
		}
	}
	if len(reviewer.TaskList) != 2 || reviewer.TaskList[0].ReplacedByParentTasks || !reviewer.TaskList[1].ReplacedByParentTasks {
		t.Errorf("reviewer TaskList = %+v, want only the second task to insert parent tasks", reviewer.TaskList)
	}
```

In `TestAvailableAgentsSection_Sorted`, replace everything from `result := renderAvailableAgentsSectionForTest(t, agents)` to the end with:

```go
	entries := availableAgentEntriesForTest(t, agents, -1, nil)
	aIdx := slices.IndexFunc(entries, func(e agentEntry) bool { return e.Name == "a-plugin:agent" })
	zIdx := slices.IndexFunc(entries, func(e agentEntry) bool { return e.Name == "z-plugin:agent" })
	if aIdx < 0 || zIdx < 0 {
		t.Fatalf("expected both agents, got %+v", entries)
	}
	if aIdx >= zIdx {
		t.Errorf("agents should be sorted alphabetically: a at %d, z at %d", aIdx, zIdx)
	}
```

In `TestAvailableAgentsSection_OmitsTopLevelOnlyAgents`, replace everything after the `agents` map with:

```go
	// At allowance=0 (leaf/dark) no agent types are actionable through delegate.
	if got := availableAgentEntriesForTest(t, agents, 0, nil); len(got) != 0 {
		t.Fatalf("available agents should be hidden at allowance=0, got %+v", got)
	}
	// At allowance=1 (grantable) delegate-listing types are included.
	entries := availableAgentEntriesForTest(t, agents, 1, nil)
	for _, name := range []string{"coordinator", "reviewer"} {
		if !slices.ContainsFunc(entries, func(e agentEntry) bool { return e.Name == name }) {
			t.Fatalf("%s should be available at allowance=1, got %+v", name, entries)
		}
	}
```

In `TestAvailableAgentsSectionSuppressedWhenDelegationSurfaceUnavailable`, replace everything after the `agents` map with:

```go
	if got := availableAgentEntriesForTest(t, agents, 1, []string{"communicate", "delegate", "job_watch"}); len(got) != 0 {
		t.Fatalf("available agents should be hidden when the delegation surface is incomplete, got %+v", got)
	}
```

Add `"slices"` to the imports.

- [ ] **Step 3: The other available-agents callers**

In `agent/plugin_real_test.go` `TestRealPlugin_Superpowers_PromptFormatting`, replace everything from `prompt := renderAvailableAgentsSectionForTest(t, lp.Agents)` to the end with:

```go
	entries := availableAgentEntriesForTest(t, lp.Agents, -1, nil)
	if !slices.ContainsFunc(entries, func(e agentEntry) bool { return e.Name == "superpowers:code-reviewer" }) {
		t.Error("available agents should include superpowers:code-reviewer")
	}
```

In `TestRealPlugin_AggregateAgents`, replace the block from `// All agents should format correctly in prompt` to the end with:

```go
	// Every agent reaches the prompt's available-agents input.
	entries := availableAgentEntriesForTest(t, allAgents, -1, nil)
	for _, name := range expectedAgents {
		if !slices.ContainsFunc(entries, func(e agentEntry) bool { return e.Name == name }) {
			t.Errorf("available agents should include %q", name)
		}
	}
```

In `agent/plugin_e2e_test.go` `TestPlugin_EndToEnd`, replace the step 8 block (from `// 8. Plugin agent prompt formatting` through the `"Helps with tasks"` check) with:

```go
	// 8. Plugin agents reach the prompt's available-agents input
	entries := availableAgentEntriesForTest(t, lp.Agents, -1, nil)
	if !slices.ContainsFunc(entries, func(e agentEntry) bool {
		return e.Name == "e2e-plugin:helper" && e.Description == "Helps with tasks"
	}) {
		t.Errorf("available agents = %+v, want e2e-plugin:helper with its description", entries)
	}
```

Add `"slices"` to both files' imports if missing.

In `agent/plugin_integration_live_test.go` `TestLive_Session_PluginAgentsInSystemPrompt`, delete the `<available_agents>` check (the `if !strings.Contains(sysPrompt, "<available_agents>")` block); keep the two agent-name checks.

- [ ] **Step 4: `agent/session_surface_behavior_test.go`**

Delete the `openAISectionContent` variable with its `//go:embed` comment, `openAISectionLiteral`, and the `_ "embed"` import. Replace the two tests with:

```go
// TestSystemPromptSurfaceFollowsTheVendorForANamedInstance: a session on an
// openai instance under a user-assigned name (id "work") renders its prompt
// for the openai surface. The prompt keys on the surface, so it follows the
// vendor rather than the name.
func TestSystemPromptSurfaceFollowsTheVendorForANamedInstance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	renamedProfile := namedOpenAIInstanceProfile("work", "gpt-5.5")
	sess, err := NewSession(c, renamedProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if got := sess.profile.Surface(); got != registry.SurfaceOpenAI {
		t.Fatalf("prompt surface = %q, want %q for instance %q", got, registry.SurfaceOpenAI, renamedProfile.ID())
	}
}

// TestSystemPromptSurfaceIsGenericForACompatInstance: a chat-completions
// instance renders its prompt for the generic surface, so it gets none of the
// openai-only guidance.
func TestSystemPromptSurfaceIsGenericForACompatInstance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai-compatible"})
	compatProfile := testOpenAICompatProfile("openai-compatible", "gpt-4o", 128_000)
	sess, err := NewSession(c, compatProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if got := sess.profile.Surface(); got != registry.SurfaceGeneric {
		t.Fatalf("prompt surface = %q, want %q", got, registry.SurfaceGeneric)
	}
}
```

Remove the imports the compiler reports unused.

- [ ] **Step 5: The provider-instance tests**

In `agent/provider_instance_integration_test.go`:
- `TestProviderInstance_RenamedOpenAI_IdentityAndBehavior`: delete the "Assertion 3" block (the `const openAIMarker` line, the `renderSystemPrompt` call, and its check).
- Delete `TestProviderInstance_OpenAICompatible_NoOpenAIBehavior`; the generic-surface check lives in `TestSystemPromptSurfaceIsGenericForACompatInstance`.

In `agent/provider_instance_1b_integration_test.go` `TestPhase1b_CompatX_NoOpenAIBehavior`, delete `const openAIMarker = ...`, the `workPrompt` render and its check, and the `compatPrompt` render and its check. Keep the surface preconditions and the prompt-cache checks.

- [ ] **Step 6: Resource caps as a typed input**

In `agent/resource_caps_boundary_test.go`, replace `parseRenderedEnvironmentResourceCaps` with:

```go
// promptResourceCaps decodes the resource caps the environment block renders,
// from the typed prompt input; ok is false when the session renders none.
func promptResourceCaps(t *testing.T, s *Session, env execenv.ExecutionEnvironment) (renderedResourceCaps, bool) {
	t.Helper()
	data := s.buildPromptData(env)
	if data.ResourceCapsJSON == "" {
		return renderedResourceCaps{}, false
	}
	var caps renderedResourceCaps
	if err := json.Unmarshal([]byte(data.ResourceCapsJSON), &caps); err != nil {
		t.Fatalf("decode resource caps %q: %v", data.ResourceCapsJSON, err)
	}
	return caps, true
}
```

Replace each call `caps, ok := parseRenderedEnvironmentResourceCaps(t, prompt)` with `caps, ok := promptResourceCaps(t, sess, sess.env)` in this file, and with `caps, ok := promptResourceCaps(t, sess, local)` in `agent/resource_caps_sandbox_linux_test.go`. Keep each test's `renderSystemPrompt` call and its warning check (the render must still succeed); if the compiler then reports `prompt` unused, change `prompt, warning :=` to `_, warning :=`. Remove the `bufio`, `encoding/xml`, and `io` imports if unused.

- [ ] **Step 7: `agent/session_config_test.go`**

Add this helper:

```go
// projectDocPaths lists the instruction docs a session loaded, by path.
func projectDocPaths(s *Session) []string {
	paths := make([]string, 0, len(s.projectDocs))
	for _, doc := range s.projectDocs {
		paths = append(paths, doc.Path)
	}
	return paths
}
```

In `TestSession_NaturalCompletion_LoadsOnlyProfileDocs`, replace everything from `sys := reqs[0].Messages[0].Text()` to the end with:

```go
	paths := projectDocPaths(sess)
	if !slices.Contains(paths, "AGENTS.md") || !slices.Contains(paths, ".codex/instructions.md") ||
		slices.Contains(paths, "CLAUDE.md") || slices.Contains(paths, "GEMINI.md") {
		t.Fatalf("project doc selection = %q", paths)
	}
```

In `TestSession_NaturalCompletion_LoadsOnlyProfileDocs_Anthropic`, replace everything from `sys := reqs[0].Messages[0].Text()` to the end with:

```go
	paths := projectDocPaths(sess)
	if !slices.Contains(paths, "CLAUDE.md") || !slices.Contains(paths, "AGENTS.md") {
		t.Fatalf("Anthropic profile should load CLAUDE.md and AGENTS.md, got %q", paths)
	}
	if slices.Contains(paths, "GEMINI.md") || slices.Contains(paths, ".codex/instructions.md") {
		t.Fatalf("Anthropic profile should NOT load GEMINI.md or .codex/instructions.md, got %q", paths)
	}
```

In `TestSession_NaturalCompletion_LoadsOnlyProfileDocs_Gemini`, replace everything from `sys := reqs[0].Messages[0].Text()` to the end with:

```go
	paths := projectDocPaths(sess)
	if !slices.Contains(paths, "GEMINI.md") || !slices.Contains(paths, "AGENTS.md") {
		t.Fatalf("Gemini profile should load GEMINI.md and AGENTS.md, got %q", paths)
	}
	if slices.Contains(paths, "CLAUDE.md") || slices.Contains(paths, ".codex/instructions.md") {
		t.Fatalf("Gemini profile should NOT load CLAUDE.md or .codex/instructions.md, got %q", paths)
	}
```

In `TestSession_SystemPromptFile_OverridesBasePrompt`, delete the `"OpenAI profile"` absence check; keep the sentinel check.

In `TestSession_UserInstructionOverride_AppendedLastToSystemPrompt`, delete the block that looks up `"----- END AGENTS.md -----"`; `HasSuffix` already proves the order.

Replace `TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo` with the version below, keeping its name (`agent/session_init_seed100_exact_fuzz_test.go` calls it):

```go
func TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Make the repo dirty before session start so the snapshot reflects it.
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\nmore\n"), 0o644) // modified tracked file
	_ = os.WriteFile(filepath.Join(dir, "UNTRACKED.txt"), []byte("u\n"), 0o644)    // untracked file

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	data := sess.buildPromptData(sess.env)
	if !data.IsGitRepo || data.GitBranch == "" {
		t.Fatalf("IsGitRepo=%v GitBranch=%q, want a repo with a branch", data.IsGitRepo, data.GitBranch)
	}
	if data.GitModifiedFiles != 1 || data.GitUntrackedFiles != 1 {
		t.Fatalf("modified/untracked = %d/%d, want 1/1", data.GitModifiedFiles, data.GitUntrackedFiles)
	}
	if !slices.Equal(data.GitRecentCommitTitles, []string{"init"}) {
		t.Fatalf("GitRecentCommitTitles = %q, want [init]", data.GitRecentCommitTitles)
	}
}
```

Remove imports the compiler reports unused.

- [ ] **Step 8: `agent/transcript_test.go`**

In `TestSession_TranscriptHeaderContainsSystemPrompt`, replace the `"## Identity"` check with:

```go
	if header.SystemPrompt != sess.cachedSystemPrompt {
		t.Errorf("transcript header system_prompt differs from the rendered prompt (%d vs %d bytes)",
			len(header.SystemPrompt), len(sess.cachedSystemPrompt))
	}
```

and delete `truncStr`, which has no other caller.

- [ ] **Step 9: `cmd/evener/serve_test.go`**

Rename `TestRunServeNonInteractiveFlagControlsPromptAddendum` to `TestRunServeNonInteractiveFlagReachesTheSession`, and update its two callers: `cmd/evener/seed_coverage_fuzz_test.go` (`{"serve noninteractive", ...}`) and `cmd/evener/serve_coverage_fuzz_test.go` (`{"noninteractive", ...}`). In the renamed test, replace everything from `path := filepath.Join(stateDir, "sessions", entry.SessionID+".transcript.jsonl")` to the end of the subtest with:

```go
			meta, err := schema.LoadSessionMeta(stateDir, entry.SessionID)
			if err != nil {
				t.Fatalf("load session meta: %v", err)
			}
			if meta.Config.NonInteractive != tc.want {
				t.Fatalf("session config NonInteractive = %v, want %v", meta.Config.NonInteractive, tc.want)
			}
```

Remove imports the compiler reports unused.

- [ ] **Step 10: Vet and run**

```bash
(cd agent && go vet . && go vet -tags evenerfuzz . && GOOS=linux go vet .)
go vet ./cmd/evener/ && go vet -tags evenerfuzz ./cmd/evener/
(cd agent && go test -count=1 -run 'TestSubagentPrompt|TestAvailableAgentsSection|TestRealPlugin|TestPlugin_EndToEnd|TestSystemPromptSurface|TestProviderInstance|TestPhase1b|TestRenderedEnvironment|TestSession_NaturalCompletion|TestSession_SystemPromptFile|TestSession_UserInstructionOverride|TestSession_SystemPrompt_IncludesGitSnapshot|TestSession_TranscriptHeader' .)
go test -count=1 -run '^TestRunServeNonInteractiveFlagReachesTheSession$' ./cmd/evener/
make lint-evenerfuzz
```

Expected: PASS. `TestRealPlugin_*` skip on a machine without the real plugin cache; that is expected.

- [ ] **Step 11: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/plugin_prompt_test.go agent/plugin_real_test.go agent/plugin_e2e_test.go agent/plugin_integration_live_test.go agent/session_surface_behavior_test.go agent/provider_instance_integration_test.go agent/provider_instance_1b_integration_test.go agent/resource_caps_boundary_test.go agent/resource_caps_sandbox_linux_test.go agent/session_config_test.go agent/transcript_test.go cmd/evener/serve_test.go cmd/evener/seed_coverage_fuzz_test.go cmd/evener/serve_coverage_fuzz_test.go
git add agent/plugin_prompt_test.go agent/plugin_real_test.go agent/plugin_e2e_test.go agent/plugin_integration_live_test.go agent/session_surface_behavior_test.go agent/provider_instance_integration_test.go agent/provider_instance_1b_integration_test.go agent/resource_caps_boundary_test.go agent/resource_caps_sandbox_linux_test.go agent/session_config_test.go agent/transcript_test.go cmd/evener/serve_test.go cmd/evener/seed_coverage_fuzz_test.go cmd/evener/serve_coverage_fuzz_test.go
git status --short && git diff --cached --stat
git commit -m "test: check prompt inputs as typed data instead of rendered prose"
```

- [ ] **Step 12: Open PR 1**

Push and open a regular PR titled `test: assembly tests for the system prompt, no prose assertions`. The description links the spec and this plan, lists the eight configurations, and names what the deleted tests guarded and where that guard lives now.

---

## PR 2: the two intended prompt changes

Branch from `main` after PR 1 merges: `git fetch origin main && git switch -c claude/prompt-collapse-2-prompt-changes origin/main`.

### Task 7: Move transcripts and drop the coordinator's non-interactive text

**Files:**
- Modify: `agent/prompts/templates/system.md.tmpl`
- Delete: `agent/prompts/sections/non-interactive.agent-coordinator.md`

- [ ] **Step 1: Render the before state**

Write the scratch file from "The render comparison" and run its "before" command.
Expected: eight files in `$S/before`.

- [ ] **Step 2: Move the transcripts section**

In `agent/prompts/templates/system.md.tmpl`, delete these two lines (between `{{ section "background-jobs" }}` and `{{ section "git-safety" }}`):

```
{{ section "transcripts" }}

```

and insert the same two lines after `{{ section "tools" }}` and its blank line, so that part of the file reads:

```
{{ section "tools" }}

{{ section "transcripts" }}

{{ section "environment" }}
```

- [ ] **Step 3: Delete the coordinator variant**

```bash
git rm agent/prompts/sections/non-interactive.agent-coordinator.md
```

- [ ] **Step 4: Run the prompt tests**

Run: `(cd agent && go test -count=1 -run 'TestSystemPrompt|TestPromptData|TestShippedPrompts|TestSystemTemplate|TestSectionResolver' .)`
Expected: PASS. If a test fails on prose, it is an assertion Tasks 5 and 6 missed; delete it under the same rules and note it in the PR description.

- [ ] **Step 5: Render the after state and compare**

Run the "after" and `diff` commands from "The render comparison".
Expected differences, and nothing else:
- `root-interactive-anthropic.md`: the `## Session transcripts` block moves from after the background-jobs text to after the Tool usage text.
- `root-headless-openai-coordinator.md`: the same move, and the non-interactive section's text becomes the generic one.
- `root-with-overrides.md`: the `## Session transcripts` block appears after the Tool usage text. It used to sit inside the block `--system-prompt` replaces.
- No delegate file changes.

- [ ] **Step 6: Delete the scratch file and commit**

```bash
rm agent/prompt_render_compare_scratch_test.go
git add agent/prompts/templates/system.md.tmpl
git status --short && git diff --cached --stat
git commit -m "prompt: move transcripts guidance after tool usage; drop the coordinator's non-interactive variant"
```

`git diff --cached --stat` must show exactly the two files (the `git rm` staged the deletion).

- [ ] **Step 7: Open PR 2**

Push and open a regular PR titled `prompt: move transcripts guidance and drop the coordinator's non-interactive variant`. The description links the spec, and pastes the scratch file's source, the render comparison commands, and the diff from Step 5.

---

## PR 3: the collapse

Branch from `main` after PR 2 merges: `git fetch origin main && git switch -c claude/prompt-collapse-3-one-template origin/main`.

Before any edit, write the scratch file and run the render comparison's "before" command. Keep `$S/before` until Task 10 is done.

### Task 8: Surface, subagent flag, and role become typed inputs

**Files:**
- Modify: `agent/prompt_data.go`
- Modify: `agent/session_prompts.go` (`buildPromptData`)
- Modify: `agent/system_prompt_test.go`
- Create: `agent/prompt_data_role_test.go`

**Interfaces:**
- Produces: `promptData.IsSubagent bool`, `promptData.Surface string`, `promptData.Role string`; `resolveRolePrompt(override, agentName string, agents fs.FS) (string, *promptSource)` in `agent/prompt_data.go`. Task 9 consumes the returned `*promptSource`.

- [ ] **Step 1: Write the role-resolution unit test**

Create `agent/prompt_data_role_test.go`:

```go
package agent

import (
	"testing"
	"testing/fstest"
)

func TestResolveRolePrompt(t *testing.T) {
	t.Parallel()
	agents := fstest.MapFS{
		"scout.md":  {Data: []byte("---\nname: scout\n---\n\n  Scout body.  \n")},
		"empty.md":  {Data: []byte("---\nname: empty\n---\n")},
		"broken.md": {Data: []byte("---\n: bad: yaml: [unclosed\n---\nbody\n")},
	}
	cases := []struct {
		name       string
		override   string
		agent      string
		wantBody   string
		wantSource *promptSource
	}{
		{"override wins, trimmed", "  Override body. ", "scout", "Override body.", &promptSource{Label: "config:role_prompt_override", Size: len("Override body.")}},
		{"bundled body without frontmatter", "", "scout", "Scout body.", &promptSource{Label: "agent:scout", Size: len("Scout body.")}},
		{"an empty body keeps its source", "", "empty", "", &promptSource{Label: "agent:empty", Size: 0}},
		{"unparseable definition", "", "broken", "", nil},
		{"no definition", "", "missing", "", nil},
	}
	for _, tc := range cases {
		body, source := resolveRolePrompt(tc.override, tc.agent, agents)
		if body != tc.wantBody {
			t.Errorf("%s: body = %q, want %q", tc.name, body, tc.wantBody)
		}
		switch {
		case tc.wantSource == nil && source != nil:
			t.Errorf("%s: source = %+v, want none", tc.name, *source)
		case tc.wantSource != nil && (source == nil || *source != *tc.wantSource):
			t.Errorf("%s: source = %v, want %+v", tc.name, source, *tc.wantSource)
		}
	}
}
```

The empty-body case keeps its source because `TestSession_DefaultFallbackUsesDefaultAgentPrompt` expects an `agent:default` source, and the bundled `default.md` has an empty body.

- [ ] **Step 2: Extend the typed-input test**

In `agent/system_prompt_test.go`, add `"io/fs"`, `"primeradiant.com/evener/agent/internal/frontmatter"`, and `"primeradiant.com/evener/internal/bundled"` to the imports, add this helper:

```go
// bundledAgentBody is the body of a bundled agent definition with its
// frontmatter stripped, the role text a builtin agent renders.
func bundledAgentBody(t *testing.T, name string) string {
	t.Helper()
	raw, err := fs.ReadFile(bundled.Agents(), name+".md")
	if err != nil {
		t.Fatalf("read bundled agent %s: %v", name, err)
	}
	doc, err := frontmatter.Parse(string(raw))
	if err != nil {
		t.Fatalf("parse bundled agent %s: %v", name, err)
	}
	return strings.TrimSpace(doc.Body)
}
```

and add these lines to the `check` functions in `TestPromptDataTypedInputs`:

- `root interactive anthropic`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, false)
			checkPromptInput(t, "Surface is anthropic", d.Surface == "anthropic", true)
			checkPromptInput(t, "Role empty for the default agent", d.Role == "", true)
  ```
- `root headless openai coordinator`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, false)
			checkPromptInput(t, "Surface is openai", d.Surface == "openai", true)
			checkPromptInput(t, "Role is the coordinator plugin body",
				d.Role == strings.TrimSpace(coordinatorWorkflowAgentForTest(t, "coordinator").SystemPrompt), true)
  ```
- `root with overrides`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, false)
  ```
- `delegate that can delegate`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, true)
			checkPromptInput(t, "Role is the delegating subagent override",
				d.Role == strings.TrimSpace(defaultDelegatingSubagentInstructions), true)
  ```
- `leaf delegate`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, true)
			checkPromptInput(t, "Role is the bundled subagent body", d.Role == bundledAgentBody(t, "subagent"), true)
  ```
- `explorer delegate`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, true)
			checkPromptInput(t, "Role is the bundled explorer body", d.Role == bundledAgentBody(t, "explorer"), true)
  ```
- `implementer delegate`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, true)
			checkPromptInput(t, "Role is the implementer plugin body",
				d.Role == strings.TrimSpace(coordinatorWorkflowAgentForTest(t, "implementer").SystemPrompt), true)
  ```
- `delegate with role override and preloaded skills`:
  ```go
			checkPromptInput(t, "IsSubagent", d.IsSubagent, true)
			checkPromptInput(t, "Role is the override", d.Role == sentinelRole, true)
  ```

- [ ] **Step 3: Run them to see them fail**

Run: `(cd agent && go test -count=1 -run '^(TestPromptDataTypedInputs|TestResolveRolePrompt)$' .)`
Expected: FAIL to compile: `undefined: resolveRolePrompt`, `d.IsSubagent undefined`.

- [ ] **Step 4: Add the fields and `resolveRolePrompt`**

In `agent/prompt_data.go`, add to `promptData` directly after `RolePromptOverride string`:

```go
	// IsSubagent is true for a delegate session (depth above zero): delegates
	// get their own delegation guidance and none of the root-only sections.
	IsSubagent bool
	// Surface is the provider surface the session's profile speaks
	// ("openai", "anthropic", ...), for surface-specific guidance.
	Surface string
	// Role is the resolved role body: the role prompt override, or the bundled
	// agent definition's body without its frontmatter.
	Role string
```

Add `"io/fs"` and `"primeradiant.com/evener/agent/internal/frontmatter"` to its imports, and add:

```go
// resolveRolePrompt returns the role body and its PROMPT_LOADED source: the
// role prompt override when one is set, otherwise the bundled agent
// definition's body with its frontmatter stripped. A bundled definition
// reports its source even when its body is empty; an agent with no
// definition, or one that does not parse, has no role and no source.
func resolveRolePrompt(override, agentName string, agents fs.FS) (string, *promptSource) {
	if body := strings.TrimSpace(override); body != "" {
		return body, &promptSource{Label: "config:role_prompt_override", Size: len(body)}
	}
	raw, err := fs.ReadFile(agents, agentName+".md")
	if err != nil {
		return "", nil
	}
	doc, err := frontmatter.Parse(string(raw))
	if err != nil {
		return "", nil
	}
	body := strings.TrimSpace(doc.Body)
	return body, &promptSource{Label: "agent:" + agentName, Size: len(body)}
}
```

- [ ] **Step 5: Fill them in `buildPromptData`**

In `agent/session_prompts.go` `buildPromptData`, add to the `promptData{...}` literal:

```go
		IsSubagent:               s.depth > 0,
		Surface:                  s.profile.Surface(),
```

and after the literal:

```go
	data.Role, _ = resolveRolePrompt(s.cfg.spawn.rolePromptOverride, agentName, bundled.Agents())
```

- [ ] **Step 6: Run them to see them pass**

Run: `(cd agent && go test -count=1 -run '^(TestPromptDataTypedInputs|TestResolveRolePrompt)$' .)`
Expected: PASS.

- [ ] **Step 7: Format and commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/prompt_data.go agent/session_prompts.go agent/system_prompt_test.go agent/prompt_data_role_test.go
git add agent/prompt_data.go agent/session_prompts.go agent/system_prompt_test.go agent/prompt_data_role_test.go
git status --short && git diff --cached --stat
git commit -m "agent: resolve the prompt's surface, subagent flag, and role as typed inputs"
```

### Task 9: One template, parsed once

**Files:**
- Create: `agent/prompts/system.md.tmpl` (assembled by a one-off script)
- Create: `agent/prompt_template.go`, `agent/prompt_template_test.go`
- Modify: `agent/section_resolver.go` (move `promptSource` and `collapseBlankLines` out), `agent/section_resolver_test.go` (move `TestCollapseBlankLines` out)
- Modify: `agent/session_prompts.go` (`buildPromptData`, `renderSystemPrompt`, the `renderEmbeddedSystemPrompt` seam)
- Modify: `agent/session_prompt_warning_lock_test.go` (the seam), `agent/remaining_exact_coverage_fuzz_test.go` (the seam)
- Modify: the callers of `buildPromptData` (Step 5)
- Modify: `agent/system_prompt_test.go`

**Interfaces:**
- Consumes: `resolveRolePrompt` (Task 8).
- Produces: `systemPromptTemplateLabel = "embedded:prompts/system.md.tmpl"`; `executeSystemPromptTemplate func(promptData) (string, error)`, a package variable that tests swap; `promptSource` and `collapseBlankLines` in `agent/prompt_template.go`; `(*Session).buildPromptData(env) (promptData, []promptSource)`.

- [ ] **Step 1: Write the PROMPT_LOADED test**

Append to `agent/system_prompt_test.go` (add `"slices"` and `"primeradiant.com/evener/agent/events"` to its imports):

```go
// TestSystemPromptLoadedSources checks the PROMPT_LOADED sources: one per
// input to the prompt, in order, with the template's size equal to the
// rendered prompt's.
func TestSystemPromptLoadedSources(t *testing.T) {
	t.Parallel()
	overrides := buildRootWithOverridesSession(t)
	cases := []struct {
		name string
		s    *Session
		want []string
	}{
		{"root with overrides", overrides, []string{
			"cli:" + overrides.cfg.SystemPromptFile,
			systemPromptTemplateLabel,
			"agent:default",
			"append:" + overrides.cfg.SystemPromptAppend[0],
		}},
		{"root headless openai coordinator", buildRootHeadlessCoordinatorSession(t), []string{
			systemPromptTemplateLabel,
			"config:role_prompt_override",
		}},
		{"explorer delegate", buildPromptDelegate(t, 0, "explorer"), []string{
			systemPromptTemplateLabel,
			"agent:explorer",
		}},
	}
	for _, tc := range cases {
		var got []string
		for _, src := range tc.s.promptSourceLog {
			got = append(got, src.Label)
			if src.Label == systemPromptTemplateLabel && src.Size != len(tc.s.cachedSystemPrompt) {
				t.Errorf("%s: template source size %d, want the rendered prompt's %d", tc.name, src.Size, len(tc.s.cachedSystemPrompt))
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: sources = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := drainPromptLoadedLabels(overrides); !slices.Equal(got, cases[0].want) {
		t.Errorf("PROMPT_LOADED events = %q, want %q", got, cases[0].want)
	}
}

// drainPromptLoadedLabels reads the buffered startup events without
// blocking and returns the PROMPT_LOADED labels in emission order.
func drainPromptLoadedLabels(s *Session) []string {
	var labels []string
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return labels
			}
			if d, isPromptLoaded := ev.Data.(events.PromptLoadedData); isPromptLoaded {
				labels = append(labels, d.Label)
			}
		default:
			return labels
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `(cd agent && go test -count=1 -run '^TestSystemPromptLoadedSources$' .)`
Expected: FAIL to compile: `undefined: systemPromptTemplateLabel`.

- [ ] **Step 3: Assemble the template**

Save this skeleton as `$S/system.md.tmpl.skel` (`$S` from the render comparison). Each `@@include <file>@@` line is replaced by that section file with its trailing newlines trimmed:

```
{{ if .BaseInstructionsOverride }}
{{ .BaseInstructionsOverride }}
{{ else }}
@@include identity.md@@

@@include capabilities.md@@

@@include workflow.md.tmpl@@

{{ if and .IsSubagent .CanDelegate }}
Your `delegation_allowance` is {{ .DelegationAllowance }}: you may delegate, and
each delegate you start may itself delegate with an allowance strictly smaller
than yours (pass `delegation_allowance` to `delegate`; allowance 0 is a leaf).
Delegate control uses stable `dlg_...` identities; `job_...` identities are
reserved for shell jobs.
{{ end }}

{{ if or (not .IsSubagent) .CanDelegate }}
@@include delegation.md@@

@@include background-jobs.md@@
{{ end }}

{{ if and .IsSubagent (not .CanDelegate) }}
## Delegated task limits

If a delegated task explicitly requires tools or capabilities unavailable in this
session, do not thrash or pretend to perform the missing capability.

Report the mismatch promptly through `{{ .ResultToolName }}`. State what capability
is missing and, when it is obvious from the task, what kind of agent or tool would
be better suited.
{{ end }}

{{ if not .IsSubagent }}
@@include git-safety.md@@

@@include security.md@@

@@include task-tracking.md@@
{{ end }}

@@include verification.md@@

@@include context-management.md.tmpl@@

@@include communicate.md.tmpl@@

{{ if .HasAskUser }}
@@include ask-user.md.tmpl@@
{{ end }}
{{ end }}

@@include tools.md.tmpl@@
{{ if eq .Surface "openai" }}

@@include tools.provider-openai_append.md.tmpl@@
{{ end }}

@@include transcripts.md.tmpl@@

@@include environment.md.tmpl@@

@@include git.md.tmpl@@

@@include workspace.md.tmpl@@

{{ .Role }}

@@include activated-skills.md.tmpl@@

{{ if not .IsSubagent }}
@@include skills.md.tmpl@@

@@include available-agents.md.tmpl@@

@@include project-docs.md.tmpl@@

{{ if .NonInteractive }}
@@include non-interactive.md.tmpl@@
{{ end }}
{{ end }}
{{ if .UserInstructionOverride }}

{{ .UserInstructionOverride }}
{{ end }}
{{ if not .IsSubagent }}{{ range .CLIAppends }}

{{ . }}
{{ end }}{{ end }}
```

Then assemble. The `sed` turns the two section-final `{{ end -}}` markers (in `transcripts.md.tmpl` and `tools.provider-openai_append.md.tmpl`, the only two in the section files) into `{{ end }}`, so they stop eating the blank line before the next section:

```bash
cd agent/prompts && awk '
/^@@include [^ ]+@@$/ {
  path = "sections/" substr($2, 1, length($2) - 2)
  content = ""
  while ((getline line < path) > 0) content = content line "\n"
  close(path)
  sub(/\n+$/, "", content)
  print content
  next
}
{ print }
' "$S/system.md.tmpl.skel" | sed 's/^{{ end -}}$/{{ end }}/' > system.md.tmpl && cd ../..
grep -c '@@include' agent/prompts/system.md.tmpl        # expect 0
grep -c '^{{ end -}}$' agent/prompts/system.md.tmpl     # expect 0
grep -n ' $' agent/prompts/system.md.tmpl               # expect exactly one line, from workflow.md.tmpl
```

Do not let an editor strip that trailing space; the render comparison would show it.

- [ ] **Step 4: Create the template runtime**

Create `agent/prompt_template.go`:

```go
package agent

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"
)

// systemPromptTemplateLabel names the embedded template in PROMPT_LOADED
// events.
const systemPromptTemplateLabel = "embedded:prompts/system.md.tmpl"

//go:embed prompts/system.md.tmpl
var systemPromptTemplateSource string

// systemPromptTemplate is parsed once, at package initialization: a template
// that does not parse fails every test in the package instead of failing a
// session at render time.
var systemPromptTemplate = template.Must(template.New("system.md.tmpl").Parse(systemPromptTemplateSource))

// executeSystemPromptTemplate renders the system prompt for data. It is a
// variable so a test can force a render failure.
var executeSystemPromptTemplate = func(data promptData) (string, error) {
	var buf bytes.Buffer
	if err := systemPromptTemplate.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(collapseBlankLines(buf.String())), nil
}

// promptSource describes one input to the composed system prompt, reported in
// a PROMPT_LOADED event.
type promptSource struct {
	Label string
	Size  int
}

// collapseBlankLines reduces runs of 3+ consecutive newlines to 2
// (one blank line between sections).
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
```

In `agent/section_resolver.go`, delete its `promptSource` type and its `collapseBlankLines` function. Move `TestCollapseBlankLines` from `agent/section_resolver_test.go` into a new `agent/prompt_template_test.go` (package `agent`, importing `"testing"`), unchanged.

- [ ] **Step 5: Return the input sources from `buildPromptData`**

In `agent/session_prompts.go`, change the signature and doc comment of `buildPromptData`:

```go
// buildPromptData assembles a promptData from session state for template
// rendering, plus the PROMPT_LOADED sources for the inputs it read: the role
// and each append file. env is the ALREADY-RESOLVED execution environment
// (passed by renderSystemPrompt, which runs under a held s.mu): it must not be
// re-fetched via s.currentEnv(), which would re-lock the non-reentrant s.mu
// and deadlock.
func (s *Session) buildPromptData(env execenv.ExecutionEnvironment) (promptData, []promptSource) {
```

Replace Task 8's `data.Role, _ = resolveRolePrompt(...)` line with:

```go
	var sources []promptSource
	var roleSource *promptSource
	data.Role, roleSource = resolveRolePrompt(s.cfg.spawn.rolePromptOverride, agentName, bundled.Agents())
	if roleSource != nil {
		sources = append(sources, *roleSource)
	}
```

Replace the CLI-appends loop:

```go
	// CLI appends: read file paths into contents
	for _, p := range s.cfg.SystemPromptAppend {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		data.CLIAppends = append(data.CLIAppends, string(b))
	}

	return data
```

with:

```go
	// CLI appends: each file is read once, and that read supplies both the
	// prompt text and its PROMPT_LOADED source. An unreadable file is skipped.
	for _, p := range s.cfg.SystemPromptAppend {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		data.CLIAppends = append(data.CLIAppends, string(b))
		sources = append(sources, promptSource{Label: "append:" + p, Size: len(strings.TrimRight(string(b), "\n"))})
	}

	return data, sources
```

Then run `(cd agent && go vet . && go vet -tags evenerfuzz . && GOOS=linux go vet .)` and change every call the compiler reports to take the first result: `data := s.buildPromptData(env)` becomes `data, _ := s.buildPromptData(env)`, and a chained call such as `s.buildPromptData(env).Skills` becomes two lines. Expect calls in `agent/system_prompt_test.go` (Task 2's `tc.check(t, s.buildPromptData(s.env))` becomes `data, _ := s.buildPromptData(s.env)` then `tc.check(t, data)`), `agent/session_skills_test.go`, `agent/session_perf_test.go`, `agent/session_tools_test.go`, `agent/plugin_prompt_test.go`, `agent/builtin_skills_test.go`, `agent/session_config_test.go`, `agent/resource_caps_boundary_test.go`, and `agent/workspace_prompt_program_fuzz_test.go` (evenerfuzz).

- [ ] **Step 6: Render through the template**

In `agent/session_prompts.go`, delete the `renderEmbeddedSystemPrompt` variable and replace `renderSystemPrompt` with:

```go
// renderSystemPrompt renders the system prompt from the embedded template. It
// returns the prompt and, when the render failed, the diagnostic its caller
// must report; see refreshSystemPromptCache for why that is returned rather
// than emitted here, and for the env-locking contract.
func (s *Session) renderSystemPrompt(env execenv.ExecutionEnvironment) (string, string) {
	data, inputSources := s.buildPromptData(env)
	result, err := executeSystemPromptTemplate(data)
	if err != nil {
		// The template parsed at package initialization, so an execution error
		// is a bug. Report it and hand the model a minimal prompt that says so.
		return fmt.Sprintf("Template rendering failed: %v. Please report this bug.", err),
			fmt.Sprintf("template render failed: %v", err)
	}
	sources := make([]promptSource, 0, len(inputSources)+2)
	if data.BaseInstructionsOverride != "" {
		sources = append(sources, promptSource{Label: "cli:" + s.cfg.SystemPromptFile, Size: len(data.BaseInstructionsOverride)})
	}
	sources = append(sources, promptSource{Label: systemPromptTemplateLabel, Size: len(result)})
	sources = append(sources, inputSources...)
	s.promptSourceLog = sources
	return result, ""
}
```

Remove the imports the compiler now reports unused in `agent/session_prompts.go` (at least `embed`). Leave `projectPromptDir`, `globalPromptDir`, and `promptSectionDirExists` in place; Task 10 deletes them with the resolver.

- [ ] **Step 7: Move the render-failure seam**

In `agent/session_prompt_warning_lock_test.go`, replace `forceSystemPromptRenderFailure` with the version below and drop the `"embed"` import. The tests that call it stay serial, as they are today: the seam is a package variable.

```go
func forceSystemPromptRenderFailure(t *testing.T) {
	old := executeSystemPromptTemplate
	executeSystemPromptTemplate = func(promptData) (string, error) {
		return "", errors.New("forced render failure")
	}
	t.Cleanup(func() { executeSystemPromptTemplate = old })
}
```

In `agent/remaining_exact_coverage_fuzz_test.go` (evenerfuzz), replace `remainingPromptCoverage` with:

```go
func remainingPromptCoverage(t *testing.T) {
	t.Helper()
	s := &Session{
		profile: NewOpenAIProfile("gpt-5.2"),
		reg:     tool.NewRegistry(),
	}
	oldExecute := executeSystemPromptTemplate
	executeSystemPromptTemplate = func(promptData) (string, error) {
		return "", errors.New("forced render failure")
	}
	t.Cleanup(func() { executeSystemPromptTemplate = oldExecute })
	got, warning := s.renderSystemPrompt(execenv.NewLocalExecutionEnvironment(t.TempDir()))
	if !strings.Contains(got, "forced render failure") {
		t.Fatalf("render failure prompt = %q", got)
	}
	// The diagnostic is RETURNED, never emitted from in here: renderSystemPrompt
	// runs under s.mu at three call sites and emit takes s.mu to stamp
	// provenance, so emitting would self-deadlock. Assert the caller is actually
	// handed something to report, or the failure would be silent.
	if !strings.Contains(warning, "forced render failure") {
		t.Fatalf("render failure warning = %q, want it to name the failure", warning)
	}
}
```

and remove the `embed`, `os`, and `path/filepath` imports if the tagged vet reports them unused.

- [ ] **Step 8: Build and run the prompt tests**

```bash
(cd agent && go build ./... && go vet . && go vet -tags evenerfuzz . && GOOS=linux go vet .)
(cd agent && go test -count=1 -run 'TestSystemPrompt|TestPromptData|TestResolveRolePrompt|TestCollapseBlankLines|TestSession_SystemPromptAsUser|TestSystemPromptConsistency|RenderFailure|TestSession_DefaultFallback' .)
```

Expected: PASS, including `TestSystemPromptLoadedSources`.

- [ ] **Step 9: Compare renders**

Run the render comparison's "after" command and `diff -ru "$S/before" "$S/after"`.
Expected: no differences. A difference means the assembled template does not match the old composition. Fix the template (usually whitespace at a section boundary), never the comparison, and rerun until clean.

- [ ] **Step 10: Run the agent package and commit**

```bash
(cd agent && go test -count=1 .)
make lint-evenerfuzz
"$(go env GOROOT)/bin/gofmt" -w agent/prompt_template.go agent/prompt_template_test.go $(git diff --name-only -- 'agent/*.go')
git add agent/prompts/system.md.tmpl agent/prompt_template.go agent/prompt_template_test.go
git add $(git diff --name-only -- agent)
git status --short && git diff --cached --stat
git commit -m "agent: render the system prompt from one template parsed at init"
```

`git diff --name-only` lists tracked files only, so the untracked scratch file stays out of the commit. `git status --short` must still show it as untracked; keep it for Task 10.

### Task 10: Delete the resolver, the section files, and the override directories

**Files:**
- Delete: `agent/section_resolver.go`, `agent/prompt_assets.go`, `agent/prompts/sections/` (23 files after PR 2), `agent/prompts/templates/` (2 files), `agent/internal/promptpath/`, `agent/section_resolver_test.go`, `agent/cov_s5_section_resolver_test.go`
- Modify: `agent/session_prompts.go`, `agent/prompt_data.go`, `agent/session_env_swap.go`, `agent/session_worktree_swap_scratch_unix_test.go`
- Modify (ports): `agent/profile_test.go`, `agent/session_surface_behavior_test.go`, `agent/session_capabilities_test.go`, `agent/session_tools_test.go`, `agent/cov_s5_helpers_test.go`, `agent/workspace_prompt_program_fuzz_test.go`
- Modify: `scripts/fuzz/fuzz-targets.txt`, `agent/plugin/evenerwide.go`, `docs/tools/transcripts.md`, `.gitignore`, `test/scenarios/job-delegate-wait-no-poll.md`, `test/scenarios/ask-noninteractive-invisible.md`

- [ ] **Step 1: Delete the old machinery**

```bash
git rm -r agent/section_resolver.go agent/prompt_assets.go agent/prompts/sections agent/prompts/templates agent/internal/promptpath agent/section_resolver_test.go agent/cov_s5_section_resolver_test.go
```

In `agent/session_prompts.go`, delete the `projectPromptDir` and `globalPromptDir` variables and the `promptSectionDirExists` function, then remove the imports the compiler reports unused (`embed`, `path/filepath` if nothing else uses it, and `primeradiant.com/evener/agent/internal/promptpath`).

Delete the line `native:agent:./internal/promptpath:FuzzPromptPaths` from `scripts/fuzz/fuzz-targets.txt`.

- [ ] **Step 2: Delete the dead `promptData` fields**

In `agent/prompt_data.go`, delete the fields `Provider`, `Agent`, `RolePromptOverride`, `ProfileTools`, `MCPTools`, and `CustomTools` (with their comments), the `toolEntry` type, and `toolEntriesFromDefinitions`. In `buildPromptData`, delete the `Provider:`, `Agent:`, and `RolePromptOverride:` lines from the literal, the `data.ProfileTools = ...` and `data.MCPTools = ...` assignments, and the whole custom-tools block that builds `customToolDefs` and assigns `data.CustomTools`. Keep `profileDefs := s.profileWireToolDefs()`; `UnavailableProfileToolNames` still uses it.

- [ ] **Step 3: Port the remaining tests**

- `agent/profile_test.go`: replace `renderPromptForTest` with the version below. Port `TestSystemPrompt_CoordinatorHasImpossibleDelegationException` by replacing `promptData{Agent: "coordinator"}` with `promptData{Role: strings.TrimSpace(coordinatorWorkflowAgentForTest(t, "coordinator").SystemPrompt)}`. Delete `TestBuildSystemPrompt_DoesNotDuplicateProviderToolDescriptions` and `TestBuildSystemPrompt_DoesNotDuplicateMCPOrCustomToolDescriptions`: the fields they set are gone, and nothing renders tool descriptions. Remove the `bundled` import if unused.

  ```go
  func renderPromptForTest(t *testing.T, p *provider.Profile, data promptData) string {
  	t.Helper()
  	if data.Surface == "" {
  		data.Surface = p.Surface()
  	}
  	if data.Model == "" {
  		data.Model = p.Model()
  	}
  	if data.ResultToolName == "" {
  		data.ResultToolName = "communicate"
  	}
  	result, err := executeSystemPromptTemplate(data)
  	if err != nil {
  		t.Fatalf("render system prompt: %v", err)
  	}
  	return result
  }
  ```

- `agent/session_surface_behavior_test.go`: in both tests, replace `sess.profile.Surface()` with the typed input:

  ```go
  	data, _ := sess.buildPromptData(sess.env)
  	if got := data.Surface; got != registry.SurfaceOpenAI {
  ```

  (and `registry.SurfaceGeneric` in the second test). Then delete each test's `rendersOpenAIToolsSection` check, the `rendersOpenAIToolsSection` helper, and the `openAIToolsSectionSource` constant: the section file they name is gone, and `data.Surface` is now the input the template branches on. Remove the `slices` import if unused.

- `agent/session_capabilities_test.go` `TestCapabilityPreambleRendersInEnvironmentSection`: delete the `resolver := &sectionResolver{...}` literal and replace `out, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)` with `out, err := executeSystemPromptTemplate(data)`. Remove the `bundled` import if unused.

- `agent/session_tools_test.go` `TestDelegateSurfaceUsesAgentRegistryCapabilities`: delete the `const availableAgentsSection = ...` line and the `slices.ContainsFunc(sess.promptSourceLog, ...)` check that uses it.

- `agent/cov_s5_helpers_test.go` `TestS5Cov_ToolDefinitionHelpers`: delete the `entries := toolEntriesFromDefinitions(defs)` line and its check.

- `agent/workspace_prompt_program_fuzz_test.go` (evenerfuzz): delete the `wppSectionResolver(...)` call in `FuzzWorkspacePromptProgram` and the functions `wppSectionResolver` and `wppMemorySource`. In `wppPromptDataAndRender`, delete the `MCPTools`/`CustomTools` check (and then `wppHasTool`, its only user), the `promptSectionDirExists` checks, and the `toolEntriesFromDefinitions` checks. Remove the imports the tagged vet reports unused (`testing/fstest`, `primeradiant.com/evener/internal/bundled`).

- [ ] **Step 4: Remove the environment-swap pre-warm**

In `agent/session_env_swap.go`, replace the Step 1 comment:

```go
	// Step 1 — OUTSIDE s.mu: compute the new EnvInfo and its git snapshot, and
	// pre-warm next's git-root cache. The git snapshot forks several `git`
	// subprocesses and `git status` can take seconds on a big repo; s.mu must
	// never be held across a subprocess (it would stall every event emit,
	// Meta() autosave, and hub poll while forking). The pre-warm is load
	// bearing, not an optimization: step 2's refreshSystemPromptCache calls
	// renderSystemPrompt, which calls execenv.GitRootOrEmpty(next, ...) again —
	// next's memoization cache starts empty (WithWorkingDirectory gives it a
	// fresh gitRoots cache), so without this call step 2 would fork
	// `git rev-parse --show-toplevel` while holding s.mu.
```

with:

```go
	// Step 1 — OUTSIDE s.mu: compute the new EnvInfo and its git snapshot. The
	// git snapshot forks several `git` subprocesses and `git status` can take
	// seconds on a big repo; s.mu must never be held across a subprocess (it
	// would stall every event emit, Meta() autosave, and hub poll while
	// forking). Step 2's refreshSystemPromptCache runs under s.mu, so the
	// prompt render must never start a subprocess either: it reads session
	// state and files only.
```

and delete the pre-warm block:

```go
	if !s.cfg.NoProjectPrompts {
		// Pre-warm next's git-root cache; see the lock-order comment above.
		_ = execenv.GitRootOrEmptyContext(refreshCtx, next, newWD)
	}
```

In `agent/session_worktree_swap_scratch_unix_test.go`, change the first sentence of the comment above `TestWorktreeSwap_SnapshotOnTheEnteredCloneUsesTheSessionScratch` from "The swap's git snapshot and prompt pre-warm run commands on the entered clone before it is installed" to "The swap's git snapshot runs commands on the entered clone before it is installed".

- [ ] **Step 5: Update docs, comments, and `.gitignore`**

- `docs/tools/transcripts.md`: replace `` `agent/prompts/sections/transcripts.md`. `` with `` the transcripts guidance in `agent/prompts/system.md.tmpl`. ``
- `agent/plugin/evenerwide.go`: in the `globalCommandsDir` comment, delete the sentence "Mirrors promptpath.globalPromptsDir."
- `test/scenarios/job-delegate-wait-no-poll.md`: replace "confirm the running binary embeds the edited `background-jobs.md`" with "confirm the running binary embeds the edited system prompt template (`agent/prompts/system.md.tmpl`)".
- `test/scenarios/ask-noninteractive-invisible.md`: replace "(`agent/prompts/sections/non-interactive.md.tmpl`)" with "(from `agent/prompts/system.md.tmpl`)".
- `.gitignore`: delete the line `/.evener/prompts/`.

- [ ] **Step 6: Build, vet, and test**

```bash
(cd agent && go build ./... && go vet . && go vet -tags evenerfuzz . && GOOS=linux go vet .)
(cd agent && go test -count=1 .)
make lint-evenerfuzz
make lint-fuzz-registry
```

Expected: PASS. `TestSwapEnvAndRefresh_TestConfigSkipsGitDiscovery` and `TestSwapEnvAndRefresh_NoGitForkWhileLocked` still pass: no git runs under the lock once the pre-warm is gone.

- [ ] **Step 7: Final render comparison**

Run the render comparison's "after" command into a fresh directory (`mkdir "$S/after2"` and `PROMPT_RENDER_OUT="$S/after2"`) and `diff -ru "$S/before" "$S/after2"`.
Expected: no differences. Then `rm agent/prompt_render_compare_scratch_test.go`.

- [ ] **Step 8: Commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w agent/session_prompts.go agent/prompt_data.go agent/session_env_swap.go agent/session_worktree_swap_scratch_unix_test.go agent/profile_test.go agent/session_surface_behavior_test.go agent/session_capabilities_test.go agent/session_tools_test.go agent/cov_s5_helpers_test.go agent/workspace_prompt_program_fuzz_test.go agent/plugin/evenerwide.go
git add agent/session_prompts.go agent/prompt_data.go agent/session_env_swap.go agent/session_worktree_swap_scratch_unix_test.go agent/profile_test.go agent/session_surface_behavior_test.go agent/session_capabilities_test.go agent/session_tools_test.go agent/cov_s5_helpers_test.go agent/workspace_prompt_program_fuzz_test.go agent/plugin/evenerwide.go scripts/fuzz/fuzz-targets.txt docs/tools/transcripts.md .gitignore test/scenarios/job-delegate-wait-no-poll.md test/scenarios/ask-noninteractive-invisible.md
git status --short && git diff --cached --stat
git commit -m "agent: delete the section resolver, section files, and override directories"
```

`git status --short` must show no untracked scratch file.

- [ ] **Step 9: Open PR 3**

Push and open a regular PR titled `agent: one system prompt template`. The description links the spec, lists the eight configurations, pastes the scratch file's source, the render comparison commands, and the result (no differences), and repeats the branch map's note on the unreachable skills branches.

---

## PR 4: remove `no_project_prompts`

Branch from `main` after PR 3 merges: `git fetch origin main && git switch -c claude/prompt-collapse-4-remove-no-project-prompts origin/main`.

The hub builds `evener` argument lists that can include `--no-project-prompts`, so the hub stops emitting the flag (Task 11) before the CLI stops accepting it (Task 12).

### Task 11: Remove the setting from AppWire, the hub, the TUI, and the frontend fixtures

**Files (root module unless noted):**
- Modify: `appwire/types.go`, `appwire/wiretypes_fuzz_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`
- Modify: `cmd/evener-hub/internal/launchconfig/{schema,types,wire,args,merge,runtime_defaults}.go` and their tests `{schema,types,wire,args,merge,runtime_defaults}_test.go`
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_probe.go`, `remote_hub_probe_test.go`
- Modify: `cmd/evener-tui/internal/launchconfig/{launch_schema,launch_settings_panel}.go` and `launch_settings_panel_test.go`, `launch_settings_panel_covtest_test.go`, `launch_settings_panel_fuzz_test.go`, `cov_rtui_launchconfig_test.go`, `coverage_fuzz_test.go` (evenerfuzz)
- Modify (TypeScript): `appwire-client/typescript/spawnSchema.test.ts`, `cmd/evener-hub/frontend/src/panes/spawn/AdvancedOptions.test.tsx`, `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx`
- Modify: `docs/developing-evener/conventions/naming.md`

- [ ] **Step 1: AppWire**

In `appwire/types.go` `LaunchConfigLayer`, delete `NoProjectPrompts *bool \`json:"noProjectPrompts,omitempty"\``. In `appwire/wiretypes_fuzz_test.go` `launchConfigLayerSchema`, delete the `"noProjectPrompts": map[string]any{"type": []string{"boolean", "null"}},` line.

Regenerate: `make generate`. Do not hand-edit `types.gen.ts` or `docs/appwire-protocol.md`.

- [ ] **Step 2: Hub launch config**

- `schema.go`: delete the `{Field: "no_project_prompts", ...}` option line.
- `types.go`: delete `NoProjectPrompts *bool \`toml:"no_project_prompts,omitempty"\``.
- `wire.go`: delete `NoProjectPrompts: copyBoolPtr(in.NoProjectPrompts),` in both `FromWire` and `ToWire`.
- `args.go` `ToArgs`: delete the three-line `if e.NoProjectPrompts != nil && *e.NoProjectPrompts { out = append(out, "--no-project-prompts") }` block.
- `merge.go` `mergeLayers`: delete the `if l.NoProjectPrompts != nil { ... }` block.
- `runtime_defaults.go` `ApplyRuntimeDefaults`: delete the `case "noProjectPrompts":` block.
- `schema_test.go`: remove `"no_project_prompts", ` from the `want` field list.
- `types_test.go`: delete `no_project_prompts = false` from the TOML fixture and the `NoProjectPrompts` assertion.
- `wire_test.go`: delete `noProjectPrompts := true`, the `NoProjectPrompts: &noProjectPrompts,` field, and its assertion.
- `args_test.go`: delete the `NoProjectPrompts: new(true),` fixture line; in `checkToArgs_BoolFalseDoesNotEmitFlag`, drop `NoProjectPrompts: new(false), ` from the `Layer{...}` literal and delete the `if a == "--no-project-prompts"` block.
- `merge_test.go`: delete the two `NoProjectPrompts:` fixture lines and the `NoProjectPrompts` assertion.
- `runtime_defaults_test.go`: delete the `no_project_prompts` schema option line, the builtin-false assertion, the `"no_project_prompts": LayerBuiltin,` provenance entry, the `no_project_prompts` builtin check, and the clause `, no_project_prompts=false` from the "Bool fields" comment.

- [ ] **Step 3: Remote hub probe**

In `cmd/evener-hub/internal/appsource/remote_hub_probe.go` `cloneLaunchConfigLayer`, delete `out.NoProjectPrompts = ptrClone(l.NoProjectPrompts)`. In `remote_hub_probe_test.go`, delete the `NoProjectPrompts: new(false),` line in `launchLayerFixture()` and the `scalarIsolationCase("LaunchGlobal.NoProjectPrompts", ...)` entry.

- [ ] **Step 4: TUI launch config**

- `launch_schema.go` `launchOptionLayerValue`: delete the `case "no_project_prompts":` and its return.
- `launch_settings_panel.go`: delete the `{"no_project_prompts", ...}` row in `layerRows` and the `case "no_project_prompts":` block in `applyEdit`.
- `launch_settings_panel_test.go` `TestLayerRows_ResolvedDefaultLabels`: drop `, NoProjectPrompts: &tru` from the `layerRows(...)` call and delete the `no_project_prompts` row check (remove `tru` if it becomes unused).
- `launch_settings_panel_covtest_test.go`: drop `"no_project_prompts"` from the field slice, `, NoProjectPrompts: &b` from the `layerRows(...)` call (remove `b` if unused), and `"no_project_prompts": "true", ` from the map literal.
- `launch_settings_panel_fuzz_test.go`: delete `"no_project_prompts", ` from `applyEditFields` and renumber that block's index comments to match the new positions; delete the four `{"no_project_prompts", ...}` seed tuples. The saved corpus under `testdata/fuzz/FuzzApplyEdit/` stores field indexes, so each file now replays a neighboring field; that is expected and stays green.
- `cov_rtui_launchconfig_test.go`: delete the `NoProjectPrompts: new(true),` line, the `{"no_project_prompts", "true", "true"},` row, and `"no_project_prompts", ` from the field slice.
- `coverage_fuzz_test.go` (evenerfuzz): on the line holding `{"no_project_prompts", "bad"}, {"no_project_prompts", "true"}, {"sandbox_net", "bad"}, {"sandbox_net", "true"},`, delete only the two `no_project_prompts` tuples; drop `, NoProjectPrompts: &tr` from the `layerRows(...)` call (remove `tr` if unused).

- [ ] **Step 5: Frontend fixtures**

The three tests use the field only as a sample boolean. Switch them to `apiLog` (wire) / `api_log` (field) / `"Log API requests"` (label), which exists in both the hub schema and `LaunchConfigLayer` and is unused in these files:
- `appwire-client/typescript/spawnSchema.test.ts`: every `noProjectPrompts` becomes `apiLog`.
- `cmd/evener-hub/frontend/src/panes/spawn/AdvancedOptions.test.tsx`: every `noProjectPrompts` becomes `apiLog`, and every `"No project prompts"` becomes `"Log API requests"`.
- `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx`: `field: "no_project_prompts"` becomes `field: "api_log"`, `noProjectPrompts` becomes `apiLog`, and `"No project prompts"` becomes `"Log API requests"`.

Format them from the frontend directory:

```bash
(cd cmd/evener-hub/frontend && npx biome check --write src/panes/spawn/AdvancedOptions.test.tsx src/panes/spawn/Spawn.test.tsx ../../../appwire-client/typescript/spawnSchema.test.ts)
```

- [ ] **Step 6: Naming doc**

In `docs/developing-evener/conventions/naming.md`, delete the row `| Suppress \`.evener/prompts/\` loading | \`--no-project-prompts\` | \`no_project_prompts\` |`.

- [ ] **Step 7: Format and verify**

```bash
"$(go env GOROOT)/bin/gofmt" -w $(git diff --name-only -- '*.go')
go build ./... && go vet ./appwire/... ./cmd/evener-hub/... ./cmd/evener-tui/...
go test -count=1 ./appwire/... ./cmd/evener-hub/internal/launchconfig/... ./cmd/evener-hub/internal/appsource/... ./cmd/evener-tui/internal/launchconfig/...
make lint-evenerfuzz
make lint-generated
make test-web
make test-api-package
rg -n 'noProjectPrompts|no_project_prompts' appwire appwire-client cmd/evener-hub cmd/evener-tui docs/developing-evener docs/appwire-protocol.md
```

Expected: every command passes, and the final `rg` prints nothing.

- [ ] **Step 8: Commit**

```bash
git add appwire/types.go appwire/wiretypes_fuzz_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md cmd/evener-hub/internal/launchconfig cmd/evener-hub/internal/appsource/remote_hub_probe.go cmd/evener-hub/internal/appsource/remote_hub_probe_test.go cmd/evener-tui/internal/launchconfig appwire-client/typescript/spawnSchema.test.ts cmd/evener-hub/frontend/src/panes/spawn/AdvancedOptions.test.tsx cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx docs/developing-evener/conventions/naming.md
git status --short && git diff --cached --stat
git commit -m "hub, tui, appwire: remove the no_project_prompts launch setting"
```

`make generate` may also refresh other generated docs; stage only files this task changed, and if `git status` shows another regenerated file, stop and check why before committing.

### Task 12: Remove the flag and the session field

**Files:**
- Modify: `cmd/evener/main.go`, `cmd/evener/run.go`, `cmd/evener/serve.go`
- Modify (argv): `install_test.go`, `cmd/evener/serve_model_switch_test.go`, `cmd/evener/serve_resume_identity_test.go`, `cmd/evener/serve_state_test.go`, `cmd/evener/serve_tool_result_test.go`, `cmd/evener/serve_verbose_e2e_test.go`
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go`, `test/scenarios/reasoning-effort-providers.md`
- Modify: `agent/session_config.go`, `agent/schema/config_snapshot.go`
- Modify (special tests): `agent/turn_ends_process_test.go`, `agent/session_parity_test.go`, `agent/snapshot_golden_test.go`, `agent/session_env_swap_test.go`
- Modify (fixtures): every other test file that sets `NoProjectPrompts`, about 100 in `agent/` plus `cmd/evener-hub/app_threadread_tasks_test.go`
- Create: `agent/schema/snapshot_retired_key_test.go`

- [ ] **Step 1: Write the old-meta test**

Create `agent/schema/snapshot_retired_key_test.go`:

```go
package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadSessionMetaIgnoresTheRetiredNoProjectPromptsKey pins that a meta
// written by a build that still had the no_project_prompts setting keeps
// loading after the setting's removal.
func TestLoadSessionMetaIgnoresTheRetiredNoProjectPromptsKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := "retiredkey1"
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Config: ConfigSnapshot{NonInteractive: true}}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	path := filepath.Join(dir, sessionsSubdir, id+".meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	config, ok := doc["config"].(map[string]any)
	if !ok {
		t.Fatalf("meta has no config object: %s", raw)
	}
	config["no_project_prompts"] = true
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if !got.Config.NonInteractive {
		t.Fatalf("config = %+v, want the rest of the meta intact", got.Config)
	}
}
```

Run: `(cd agent && go test -count=1 -run '^TestLoadSessionMetaIgnoresTheRetiredNoProjectPromptsKey$' ./schema/)`
Expected: PASS today (the field still exists). It must still pass after Step 3 removes the field; that is its job.

- [ ] **Step 2: The CLI**

- `cmd/evener/main.go`: delete the `noProjectPrompts *bool` flags field, the `noProjectPrompts: *flags.noProjectPrompts,` line, and the `flags.noProjectPrompts = fs.Bool("no-project-prompts", ...)` registration.
- `cmd/evener/run.go`: delete the `noProjectPrompts bool` field and the `NoProjectPrompts: cfg.noProjectPrompts,` line.
- `cmd/evener/serve.go`: delete `noProjectPrompts := fs.Bool("no-project-prompts", ...)` and the `NoProjectPrompts: *noProjectPrompts,` line.
- Delete the `"--no-project-prompts",` argv line in `install_test.go` and the five `cmd/evener/serve_*_test.go` files listed above. Each starts a real `evener serve`, which would now die on the unknown flag.
- `tools/tool-fluency/cmd/evener-fluency/main.go`: delete the two `NoProjectPrompts: true,` lines.
- `test/scenarios/reasoning-effort-providers.md`: drop ` --no-project-prompts` from the sample command.

- [ ] **Step 3: The session field**

- `agent/session_config.go`: delete the `NoProjectPrompts` field with its two comment lines, and the `NoProjectPrompts:` lines in `toSnapshot` and `configFromSnapshot`.
- `agent/schema/config_snapshot.go`: delete the `NoProjectPrompts` field.

- [ ] **Step 4: The special test cases, by hand**

- `agent/turn_ends_process_test.go`: the test uses the field as a marker that survives the frozen-descriptor merge. Replace `NoProjectPrompts: true` with `NonInteractive: true` in the three literals, and `if !got.NoProjectPrompts` with `if !got.NonInteractive`.
- `agent/session_parity_test.go`: delete the `if !cfg.NoProjectPrompts { cfg.NoProjectPrompts = true }` block.
- `agent/snapshot_golden_test.go`: delete `"no_project_prompts":true,` from `goldenMetaJSON` (keep the comma of the neighboring field), and change the comment's "all 27 SessionConfig wire fields" to "every SessionConfig wire field".
- `agent/session_env_swap_test.go` `TestSwapEnvAndRefresh_TestConfigSkipsGitDiscovery`: change the failure message "despite skipGitSnapshot+NoProjectPrompts" to "despite skipGitSnapshot".

- [ ] **Step 5: The fixture sweep**

Every remaining use is `NoProjectPrompts: true` (or `false`) inside a struct literal, or a `x.NoProjectPrompts = true` statement:

```bash
files=$(rg -l --glob '*.go' 'NoProjectPrompts' agent cmd/evener-hub/app_threadread_tasks_test.go)
perl -0pi -e 's/^[ \t]*NoProjectPrompts:[ \t]*(?:true|false),[ \t]*\n//mg' $files
perl -pi -e 's/\bNoProjectPrompts:\s*(?:true|false),\s*//g; s/,\s*NoProjectPrompts:\s*(?:true|false)(?=\s*\})//g; s/\{\s*NoProjectPrompts:\s*(?:true|false)\s*\}/{}/g' $files
perl -ni -e 'print unless /^\s*[\w.]+\.NoProjectPrompts\s*=\s*(?:true|false)\s*$/' $files
rg -n 'NoProjectPrompts|noProjectPrompts|no_project_prompts|no-project-prompts' --glob '!docs/superpowers/**' --glob '!docs/design/**' .
```

Expected: the final `rg` prints only `agent/schema/snapshot_retired_key_test.go` (its JSON key). Anything else is a shape the sweep missed; fix it by hand.

- [ ] **Step 6: Format, vet, and test**

```bash
"$(go env GOROOT)/bin/gofmt" -w $(git diff --name-only -- '*.go') agent/schema/snapshot_retired_key_test.go
go build ./... && go vet ./cmd/evener/ ./tools/...
(cd agent && go build ./... && go vet ./... && go vet -tags evenerfuzz ./... && GOOS=linux go vet ./...)
make lint-evenerfuzz
(cd agent && go test -count=1 ./schema/ && go test -count=1 -run 'TestFrozenDescriptor|TestSwapEnvAndRefresh|TestSnapshotGolden|TestParity' .)
go test -count=1 ./cmd/evener/ ./tools/tool-fluency/...
```

Expected: PASS. CI runs the full agent suite on the merged tree.

- [ ] **Step 7: Commit**

```bash
git add cmd/evener/main.go cmd/evener/run.go cmd/evener/serve.go install_test.go cmd/evener/serve_model_switch_test.go cmd/evener/serve_resume_identity_test.go cmd/evener/serve_state_test.go cmd/evener/serve_tool_result_test.go cmd/evener/serve_verbose_e2e_test.go tools/tool-fluency/cmd/evener-fluency/main.go test/scenarios/reasoning-effort-providers.md agent/schema/snapshot_retired_key_test.go
git add $(git diff --name-only -- agent cmd/evener-hub/app_threadread_tasks_test.go)
git status --short && git diff --cached --stat
git commit -m "agent, cli: remove the no_project_prompts setting"
```

- [ ] **Step 8: Open PR 4**

Push and open a regular PR titled `remove the no_project_prompts setting`. The description repeats the spec's accepted consequences: a delegate store that recorded the field no longer opens; `--no-project-prompts` is now an unknown flag, which matters for the terminal-bench adapter if it passes it; old session metas, hub TOML, and AppWire layers still load.
