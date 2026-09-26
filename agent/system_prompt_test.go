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
