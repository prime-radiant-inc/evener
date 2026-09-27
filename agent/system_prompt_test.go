package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/plugin"
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
// template; a new template branch needs a configuration that runs it. check
// pins the typed inputs that send the configuration down its branches (see the
// branch map in the collapse plan).
type promptConfig struct {
	name  string
	build func(t *testing.T) *Session
	check func(t *testing.T, d promptData)
}

func promptConfigs() []promptConfig {
	return []promptConfig{
		{"root interactive anthropic", buildRootInteractiveAnthropicSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
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
			checkPromptInput(t, "len(ProjectDocs)", len(d.ProjectDocs), 2)
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
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
			checkPromptInput(t, "NonInteractive", d.NonInteractive, true)
			checkPromptInput(t, "HasTool apply_patch", d.HasTool("apply_patch"), true)
			checkPromptInput(t, "IsGitRepo", d.IsGitRepo, false)
			checkPromptInput(t, "workspace tree present", d.WorkspaceTree != "", true)
			checkPromptInput(t, "build info present", d.BuildInfo != "", false)
		}},
		{"root with overrides", buildRootWithOverridesSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, sentinelBaseInstructions)
			checkPromptInput(t, "UserInstructionOverride", d.UserInstructionOverride, sentinelUserInstructions)
			checkPromptInput(t, "CLIAppends holds the append file", len(d.CLIAppends) == 1 && d.CLIAppends[0] == sentinelAppend+"\n", true)
			checkPromptInput(t, "workspace block empty", d.WorkspaceTree == "" && d.BuildInfo == "", true)
		}},
		{"delegate that can delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 1, "") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
			checkPromptInput(t, "CanDelegate", d.CanDelegate, true)
			checkPromptInput(t, "DelegationAllowance", d.DelegationAllowance, 1)
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
		}},
		{"leaf delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
			checkPromptInput(t, "CanDelegate", d.CanDelegate, false)
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
		}},
		{"explorer delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "explorer") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
			checkPromptInput(t, "CanDelegate", d.CanDelegate, false)
			checkPromptInput(t, "sandbox line present", d.Sandbox != "", true)
			checkPromptInput(t, "unavailable profile tools listed", len(d.UnavailableProfileToolNames) > 0, true)
		}},
		{"implementer delegate", func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "implementer") }, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
			checkPromptInput(t, "CanDelegate", d.CanDelegate, false)
		}},
		{"delegate with role override and preloaded skills", buildDelegateWithRoleOverrideSession, func(t *testing.T, d promptData) {
			checkPromptInput(t, "BaseInstructionsOverride", d.BaseInstructionsOverride, "")
			checkPromptInput(t, "HasAskUser", d.HasAskUser, false)
			checkPromptInput(t, "ActivatedSkillBodies holds the preloaded body",
				len(d.ActivatedSkillBodies) == 1 && d.ActivatedSkillBodies[0] == sentinelActivatedSkill, true)
		}},
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
	writeSkillDirect(t, skills, "fixture-skill", "---\nname: fixture-skill\ndescription: "+sentinelSkill+"\n---\nBody.\n")
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
// every body of the template still runs. A section that fails to execute
// renders empty without a warning and records an "ERROR:" source instead, so
// the source log is checked too.
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
			for _, source := range s.promptSourceLog {
				if strings.HasPrefix(source.Label, "ERROR:") {
					t.Errorf("section failed to render: %s", source.Label)
				}
			}
		})
	}
}

// TestPromptDataTypedInputs checks the typed inputs buildPromptData computes
// for each configuration. The template cannot notice a wrong input, and these
// values are also the premises that let the eight configurations run every
// template body (see the branch map in the collapse plan).
func TestPromptDataTypedInputs(t *testing.T) {
	t.Parallel()
	for _, cfg := range promptConfigs() {
		t.Run(cfg.name, func(t *testing.T) {
			t.Parallel()
			s := cfg.build(t)
			cfg.check(t, s.buildPromptData(s.env))
		})
	}
}

func checkPromptInput[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %#v, want %#v", name, got, want)
	}
}

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

// promptValue is one value the session computes for its prompt, named for the
// failure message.
type promptValue struct {
	name  string
	value string
}

// TestSystemPromptRendersTheSessionData proves each value the session computes
// for its prompt reaches the rendered prompt. These are Go-generated facts and
// contract names the model acts on (the working directory, the sandbox, a
// skill's catalog name, an agent_type for delegate), not prose; operator-written
// content is TestSystemPromptCarriesEachOperatorInputOnce's job. Each
// configuration lists the values its template branches render, per the branch
// map in the collapse plan. An empty value is skipped: TestPromptDataTypedInputs
// pins which values each configuration has.
func TestSystemPromptRendersTheSessionData(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		build  func(t *testing.T) *Session
		values func(d promptData) []promptValue
	}{
		{
			name: "root interactive anthropic",
			build: func(t *testing.T) *Session {
				s := buildRootInteractiveAnthropicSession(t)
				// No prose uses this name, so finding it proves the agents list
				// rendered it.
				s.pluginAgents["fixture-agent-6d2b"] = plugin.Agent{Name: "fixture-agent-6d2b", Description: "Fixture agent."}
				return s
			},
			values: func(d promptData) []promptValue {
				values := append(environmentPromptValues(d),
					promptValue{"git branch", d.GitBranch},
					promptValue{"workspace tree", d.WorkspaceTree},
					promptValue{"build info", d.BuildInfo},
				)
				for _, title := range d.GitRecentCommitTitles {
					values = append(values, promptValue{"recent commit", title})
				}
				for _, skill := range d.Skills {
					values = append(values,
						promptValue{"skill catalog name", skill.CatalogNameOrName()},
						promptValue{"skill directory", skill.Dir},
					)
				}
				for _, agent := range d.AvailableAgents {
					values = append(values,
						promptValue{"agent name", agent.Name},
						promptValue{"agent default tools", agent.DefaultTools},
					)
					for _, task := range agent.TaskList {
						values = append(values, promptValue{"agent task", task.Title})
					}
				}
				for _, doc := range d.ProjectDocs {
					values = append(values, promptValue{"project doc path", doc.Path})
				}
				return values
			},
		},
		{
			name:  "explorer delegate",
			build: func(t *testing.T) *Session { return buildPromptDelegate(t, 0, "explorer") },
			values: func(d promptData) []promptValue {
				values := environmentPromptValues(d)
				for _, name := range d.CallableToolNames {
					values = append(values, promptValue{"callable tool", name})
				}
				for _, name := range d.UnavailableProfileToolNames {
					values = append(values, promptValue{"unavailable profile tool", name})
				}
				return values
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.build(t)
			prompt, warning := s.renderSystemPrompt(s.env)
			if warning != "" {
				t.Fatalf("render failed: %s", warning)
			}
			for _, v := range tc.values(s.buildPromptData(s.env)) {
				if v.value != "" && !strings.Contains(prompt, v.value) {
					t.Errorf("%s %q is missing from the rendered prompt", v.name, v.value)
				}
			}
		})
	}
}

// environmentPromptValues lists the environment facts every configuration
// renders.
func environmentPromptValues(d promptData) []promptValue {
	values := []promptValue{
		{"working directory", d.WorkingDir},
		{"platform", d.Platform},
		{"OS version", d.OSVersion},
		{"date", d.Today},
		{"model", d.Model},
		{"knowledge cutoff", d.KnowledgeCutoff},
		{"resource caps", d.ResourceCapsJSON},
		{"sandbox", d.Sandbox},
	}
	for _, line := range d.Capabilities {
		values = append(values, promptValue{"capability line", line})
	}
	return values
}
