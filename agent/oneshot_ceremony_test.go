package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One-shot sessions (TurnEndsProcess) drop the tools whose only reader is a
// later session. The prompt sections and the advertised tool list read the
// registry, so this test checks all three views agree.
func TestOneShotOmitsCeremonyTools(t *testing.T) {
	t.Parallel()
	ceremonyTools := oneShotCeremonyToolNames

	t.Run("one-shot omits notes and URL tools", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: true,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		advertised := toolNameSetFromDefinitions(s.ToolDefinitions())
		for _, name := range ceremonyTools {
			if s.reg.Get(name) != nil {
				t.Errorf("registry still has %q under TurnEndsProcess=true", name)
			}
			if advertised[name] {
				t.Errorf("advertised tools still include %q under TurnEndsProcess=true", name)
			}
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Error("registry omitted manage_worktree under TurnEndsProcess=true")
		}
		if !advertised["manage_worktree"] {
			t.Error("advertised tools omitted manage_worktree under TurnEndsProcess=true")
		}

		data, _ := s.buildPromptData(s.env)
		for _, name := range ceremonyTools {
			if data.HasTool(name) {
				t.Errorf("promptData.HasTool(%q) = true under TurnEndsProcess=true", name)
			}
		}
		if !data.HasTool("manage_worktree") {
			t.Error("promptData.HasTool(\"manage_worktree\") = false under TurnEndsProcess=true")
		}

		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if strings.Contains(prompt, "\n## Session links\n") {
			t.Error("prompt renders ## Session links although no URL tool is callable")
		}
		finishing := systemPromptSection(t, prompt, "## Finishing")
		if strings.Contains(finishing, "|---|---|") {
			t.Errorf("## Finishing renders a table with no rows:\n%s", finishing)
		}
	})

	t.Run("one-shot keeps the finishing table when memory saves are enabled", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: true,
			MemoryStateRoot: t.TempDir(),
			MemoryProjectID: "oneshot-ceremony",
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		data, _ := s.buildPromptData(s.env)
		if !data.MemorySaves {
			t.Fatal("fixture precondition: MemorySaves = false, want true")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		finishing := systemPromptSection(t, prompt, "## Finishing")
		if !strings.Contains(finishing, "|---|---|") {
			t.Errorf("## Finishing lost its table although MemorySaves=true:\n%s", finishing)
		}
	})

	t.Run("persistent session keeps notes and URL tools", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			TurnEndsProcess: false,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		advertised := toolNameSetFromDefinitions(s.ToolDefinitions())
		for _, name := range ceremonyTools {
			if s.reg.Get(name) == nil {
				t.Errorf("registry missing %q under TurnEndsProcess=false", name)
			}
			if !advertised[name] {
				t.Errorf("advertised tools missing %q under TurnEndsProcess=false", name)
			}
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if !strings.Contains(prompt, "\n## Session links\n") {
			t.Error("prompt missing ## Session links under TurnEndsProcess=false")
		}
		finishing := systemPromptSection(t, prompt, "## Finishing")
		if !strings.Contains(finishing, "|---|---|") {
			t.Errorf("## Finishing missing table under TurnEndsProcess=false:\n%s", finishing)
		}
	})
}

// A one-shot session keeps manage_worktree, whose handler resolves the
// repository on every call. The launch snapshot must resolve symlinks too so a
// --dir symlink into a checkout reports IsGitRepo=true and renders ## Git and safety.
func TestOneShotWorktreeToolFollowsResolvedCheckout(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	subDir := filepath.Join(repoDir, "nested", "workspace")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link-into-checkout")
	if err := os.Symlink(subDir, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	oneShot := func(t *testing.T, dir string, snapshot bool) *Session {
		t.Helper()
		opts := []sessionOpt{
			withDir(dir),
			withConfig(SessionConfig{
				TurnEndsProcess: true,
				AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			}),
		}
		if snapshot {
			opts = append(opts, withEnvironmentSnapshot())
		}
		return newSession(t, opts...)
	}

	t.Run("symlink into a checkout keeps manage_worktree", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, link, false)
		if s.envInfo.IsGitRepo {
			t.Fatal("fixture precondition: the lexical launch snapshot claims a repository, so this case cannot prove symlink resolution")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted although the working directory resolves into a checkout through a symlink")
		}
		if !toolNameSetFromDefinitions(s.ToolDefinitions())["manage_worktree"] {
			t.Fatal("advertised tools omit manage_worktree although the directory resolves into a checkout")
		}
	})

	t.Run("confirmed non-repository keeps manage_worktree and renders Safety", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, t.TempDir(), true)
		if s.envInfo.IsGitRepo {
			t.Fatal("non-repository directory reported IsGitRepo=true")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted although its handler would serve a task that runs git init")
		}
		if !toolNameSetFromDefinitions(s.ToolDefinitions())["manage_worktree"] {
			t.Fatal("advertised tools omit manage_worktree in a confirmed non-repository")
		}
		data, _ := s.buildPromptData(s.env)
		if data.IsGitRepo {
			t.Fatal("promptData.IsGitRepo = true for a non-repository directory")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if strings.Contains(prompt, "\n## Git and safety\n") {
			t.Error("prompt renders ## Git and safety in a confirmed non-repository")
		}
		if !strings.Contains(prompt, "\n## Safety\n") {
			t.Error("prompt is missing ## Safety in a confirmed non-repository")
		}
	})

	t.Run("symlinked --dir into a checkout reports a repository", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, link, true)
		if !s.envInfo.IsGitRepo {
			t.Fatal("launch snapshot missed the checkout behind the symlinked --dir")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted although the working directory resolves into a checkout")
		}
		data, _ := s.buildPromptData(s.env)
		if !data.IsGitRepo {
			t.Fatal("promptData.IsGitRepo = false for a symlinked --dir into a checkout")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if !strings.Contains(prompt, "\n## Git and safety\n") {
			t.Error("prompt is missing ## Git and safety although the directory resolves into a checkout")
		}
		if strings.Contains(prompt, "\n## Safety\n") {
			t.Error("prompt renders ## Safety instead of ## Git and safety for a symlinked checkout")
		}
	})

	t.Run("real checkout keeps manage_worktree and Git and safety", func(t *testing.T) {
		t.Parallel()
		s := oneShot(t, repoDir, true)
		if !s.envInfo.IsGitRepo {
			t.Fatal("fixture precondition: launch snapshot did not detect the repository")
		}
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted inside a real checkout")
		}
		prompt, warning, _ := s.renderSystemPrompt(s.env)
		if warning != "" {
			t.Fatalf("renderSystemPrompt: %s", warning)
		}
		if !strings.Contains(prompt, "\n## Git and safety\n") {
			t.Error("prompt is missing ## Git and safety inside a real checkout")
		}
	})

	t.Run("persistent session keeps manage_worktree outside a repository", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
			TurnEndsProcess: false,
			AgentsDocPath:   filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		}))
		if s.reg.Get("manage_worktree") == nil {
			t.Fatal("manage_worktree omitted under TurnEndsProcess=false")
		}
	})
}

// manage_worktree spends nothing on a directory that is not a repository, and
// its handler resolves git on each call. A task in a fresh directory can run
// git init and then use it, and a resumed one-shot session can retire the
// isolated delegate lanes its saved run left behind.
func TestOneShotKeepsWorktreeToolOutsideCheckout(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	launched := newSession(t, withDir(stateDir), withSteps(repeatFinalResponse(4, "ok")...), withConfig(SessionConfig{
		StateDir:        stateDir,
		TurnEndsProcess: true,
		AgentsDocPath:   filepath.Join(stateDir, "AGENTS.md"),
		testOnly:        testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	if launched.reg.Get("manage_worktree") == nil {
		t.Fatal("fresh one-shot session outside a checkout omitted manage_worktree, so git init could not lead to a worktree")
	}
	sendOneUserInput(t, launched, "start")
	id := launched.ID()
	launched.Close()

	resumed := restoreQueuePersistTestSessionWith(t, stateDir, id, RestoreSessionConfig{
		TurnEndsProcess: true,
		testOnly:        launched.cfg.testOnly,
	})
	t.Cleanup(resumed.Close)
	if resumed.reg.Get("manage_worktree") == nil {
		t.Fatal("resumed one-shot session dropped manage_worktree, so its saved delegate lanes cannot be unlocked or disposed")
	}
}

// One-shot roots and one-shot subagents have no session link list and must omit
// ## Session links entirely, while persistent roots and persistent subagents
// keep their respective ## Session links sections.
func TestOneShotSessionLinksHeadingOmitted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		isSubagent      bool
		turnEndsProcess bool
		wantHeading     bool
	}{
		{"one-shot root", false, true, false},
		{"one-shot subagent", true, true, false},
		{"persistent root", false, false, true},
		{"persistent subagent", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
				TurnEndsProcess:  tc.turnEndsProcess,
				MaxSubagentDepth: 2,
				StateDir:         t.TempDir(),
				AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
				testOnly:         testConfig{skipGitSnapshot: true, noSyncJobStore: true},
			}))
			target := parent
			if tc.isSubagent {
				prepared := prepareStableChild(t, parent, delegateArgs{Task: "task"})
				target = prepared.sub.sess
				t.Cleanup(target.Close)
			}
			data, _ := target.buildPromptData(target.env)
			checkPromptInput(t, "IsSubagent", data.IsSubagent, tc.isSubagent)
			checkPromptInput(t, "TurnEndsProcess", data.TurnEndsProcess, tc.turnEndsProcess)

			prompt, warning, _ := target.renderSystemPrompt(target.env)
			if warning != "" {
				t.Fatalf("renderSystemPrompt: %s", warning)
			}
			gotHeading := strings.Contains(prompt, "\n## Session links\n")
			if gotHeading != tc.wantHeading {
				t.Errorf("prompt contains ## Session links = %v, want %v", gotHeading, tc.wantHeading)
			}
		})
	}
}
