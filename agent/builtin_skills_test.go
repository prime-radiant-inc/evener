package agent

import (
	"os"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

const embeddedDoctoringSkill = "doctoring-evener"

func TestEmbeddedSkillsDir_HasSkillDirectories(t *testing.T) {
	t.Parallel()
	dir, err := skill.EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one skill directory, got 0")
	}
}

func TestEmbeddedSkillsDir_IncludesDoctoringSkill(t *testing.T) {
	t.Parallel()
	dir, err := skill.EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}

	skills := make(map[string]skill.SkillMeta)
	skill.ScanSkillsDir(dir, skills)
	meta, ok := skills[embeddedDoctoringSkill]
	if !ok {
		t.Fatalf("expected embedded %q skill, got %v", embeddedDoctoringSkill, builtinSkillNames(skills))
	}
	if !strings.Contains(meta.Description, "evener") {
		t.Fatalf("embedded %q description = %q, want evener-specific description", embeddedDoctoringSkill, meta.Description)
	}
}

func TestDiscover_IncludesEmbeddedSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	catalog := skill.Discover(execenv.NewLocalExecutionEnvironment(root), skill.DiscoverOptions{})
	if _, ok := catalog.Entries[embeddedDoctoringSkill]; !ok {
		t.Fatalf("expected embedded %q skill in the catalog", embeddedDoctoringSkill)
	}
}

func TestEmbeddedSkills_InSystemPrompt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	sess := newSession(t, withAdapter(&fakeAdapter{name: "anthropic"}), withProfile(newAnthropicProfile("claude-test")), withDir(root))

	data, _ := sess.buildPromptData(sess.currentEnv())
	if !data.HasUseSkill {
		t.Fatal("HasUseSkill = false, want true")
	}
	if !slices.ContainsFunc(data.Skills, func(s skillEntry) bool { return s.CatalogNameOrName() == "doctoring-evener" }) {
		t.Fatalf("prompt skills = %+v, want the embedded doctoring-evener skill", data.Skills)
	}
}

func TestEmbeddedSkills_ProjectShadowsEmbedded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	// Project has a custom doctoring skill that should shadow the embedded one.
	writeSkillMD(t, root, embeddedDoctoringSkill,
		"---\nname: doctoring-evener\ndescription: \"Custom project doctoring\"\n---\nCustom doctoring body.\n")

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := newFakeAdapter("anthropic", func(req llm.Request) llm.Response {
		if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
			capturedSystem = req.Messages[0].Text()
		}
		return agenttest.ToolCallResponse(comm)
	})
	c.Register(f)

	// Anthropic profile renders skills in system prompt, so we can verify shadowing.
	sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(root), withConfig(SessionConfig{}))
	ctx := tripwireCtx(t)
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "Custom project doctoring") {
		t.Error("system prompt should show project doctoring description, not embedded")
	}
}

func TestEmbeddedSkills_UseSkillWithProjectSkill(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	// Create a project skill to test use_skill with.
	writeSkillMD(t, root, "my-skill",
		"---\nname: my-skill\ndescription: \"Test skill\"\n---\nSkill body content.\n")

	c := llm.NewClient()

	skill := useSkillCall("s1", "my-skill")
	comm := communicateCall("c1", "done")

	var skillResultContent string
	f := newFakeAdapter("anthropic", func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(skill) }, func(req llm.Request) llm.Response {
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolResult {
					if content, ok := part.ToolResult.Content.(string); ok {
						if strings.Contains(content, "Skill body content") {
							skillResultContent = content
						}
					}
				}
			}
		}
		return agenttest.ToolCallResponse(comm)
	})
	c.Register(f)

	sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(root), withConfig(SessionConfig{}))
	ctx := tripwireCtx(t)
	_, err := sess.ProcessInput(ctx, "use my skill", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if skillResultContent == "" {
		t.Fatal("use_skill('my-skill') did not return the skill body")
	}
}

func TestEmbeddedSkills_UseSkillUnknownReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	c := llm.NewClient()

	// Calling use_skill with a skill that doesn't exist should return an error.
	skill := useSkillCall("s1", "nonexistent-skill")
	comm := communicateCall("c1", "done")

	var skillResultIsError bool
	f := newFakeAdapter("anthropic", func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(skill) }, func(req llm.Request) llm.Response {
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
					skillResultIsError = part.ToolResult.IsError
				}
			}
		}
		return agenttest.ToolCallResponse(comm)
	})
	c.Register(f)

	sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(root), withConfig(SessionConfig{}))
	ctx := tripwireCtx(t)
	_, err := sess.ProcessInput(ctx, "check my work", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if !skillResultIsError {
		t.Error("use_skill with unknown skill should return an error result")
	}
}

func TestEmbeddedSkills_AllSkillsLoadable(t *testing.T) {
	t.Parallel()
	// Verify every embedded skill can be discovered and its body loaded.
	dir, err := skill.EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}

	skills := make(map[string]skill.SkillMeta)
	skill.ScanSkillsDir(dir, skills)

	if len(skills) == 0 {
		t.Fatal("expected at least one embedded skill")
	}
	for name, meta := range skills {
		body, err := loadSkillBody(meta)
		if err != nil {
			t.Fatalf("Load(%s): %v", name, err)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("embedded skill %q has empty body", name)
		}
	}
}

// builtinSkillNames returns a slice of skill names for test output.
func builtinSkillNames(skills map[string]skill.SkillMeta) []string {
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	return names
}

func TestBundledGuideSkills_LoadedAndUserHidden(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	catalog := skill.Discover(execenv.NewLocalExecutionEnvironment(root), skill.DiscoverOptions{})
	wantSkills := []string{
		skill.RunningJobsSkillName,
		skill.UsingWorktreesSkillName,
		skill.ReadingTranscriptsSkillName,
	}
	for _, name := range wantSkills {
		desc, ok := catalog.Entries[name]
		if !ok {
			t.Fatalf("missing bundled guide skill %q in catalog", name)
		}
		if desc.Unavailable {
			t.Fatalf("bundled guide skill %q is marked unavailable", name)
		}
		if desc.Controls.DisableModelInvocation {
			t.Fatalf("bundled guide skill %q must allow model invocation", name)
		}
		if desc.Controls.UserInvocable {
			t.Fatalf("bundled guide skill %q must set user-invocable: false", name)
		}
		body, err := loadSkillBody(desc.Meta)
		if err != nil {
			t.Fatalf("loadSkillBody(%q): %v", name, err)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("bundled guide skill %q has empty body", name)
		}
	}
}

func TestBundledGuideSkills_CapabilityGatedInPromptAndUseSkill(t *testing.T) {
	t.Parallel()
	hasPromptSkill := func(s *Session, name string) bool {
		data, _ := s.buildPromptData(s.currentEnv())
		return slices.ContainsFunc(data.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == name })
	}
	for _, tc := range []struct {
		skillName      string
		revokeTool     []string
		wantBodyMarker string
	}{
		{skill.RunningJobsSkillName, []string{"delegate", "job_watch", "job_list"}, "# Running jobs and delegates"},
		{skill.UsingWorktreesSkillName, []string{"manage_worktree"}, "# Using worktrees"},
		{skill.ReadingTranscriptsSkillName, []string{"read_transcript", "find_session_transcripts"}, "# Reading transcripts"},
	} {
		t.Run(tc.skillName, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			markGitRoot(t, root)
			capableClient := llm.NewClient()
			capableCall := useSkillCall("cap-s1", tc.skillName)
			capableComm := communicateCall("cap-c1", "done")
			var capableSawError bool
			var capableBody string
			capableClient.Register(newFakeAdapter("anthropic",
				func(llm.Request) llm.Response { return agenttest.ToolCallResponse(capableCall) },
				func(req llm.Request) llm.Response {
					for _, msg := range req.Messages {
						for _, part := range msg.Content {
							if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "cap-s1" {
								capableSawError = part.ToolResult.IsError
								if s, ok := part.ToolResult.Content.(string); ok {
									capableBody = s
								}
							}
						}
					}
					return agenttest.ToolCallResponse(capableComm)
				},
			))
			capableSess := newSession(t, withClient(capableClient), withProfile(newAnthropicProfile("claude-test")), withDir(root), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				StateDir:         t.TempDir(),
			}))
			if !hasPromptSkill(capableSess, tc.skillName) {
				t.Fatalf("capable root session missing %q in <skill-catalog>", tc.skillName)
			}
			if _, err := capableSess.ProcessInput(tripwireCtx(t), "activate", nil); err != nil {
				t.Fatalf("capable ProcessInput: %v", err)
			}
			if capableSawError || !strings.Contains(capableBody, tc.wantBodyMarker) {
				t.Fatalf("capable use_skill(%q) err=%v body=%q, want marker %q", tc.skillName, capableSawError, capableBody, tc.wantBodyMarker)
			}

			c := llm.NewClient()
			call := useSkillCall("s1", tc.skillName)
			comm := communicateCall("c1", "done")
			var sawError bool
			f := newFakeAdapter("anthropic",
				func(llm.Request) llm.Response { return agenttest.ToolCallResponse(call) },
				func(req llm.Request) llm.Response {
					for _, msg := range req.Messages {
						for _, part := range msg.Content {
							if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "s1" {
								sawError = part.ToolResult.IsError
							}
						}
					}
					return agenttest.ToolCallResponse(comm)
				},
			)
			c.Register(f)
			sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(root), withConfig(SessionConfig{
				MaxSubagentDepth: 1,
				StateDir:         t.TempDir(),
			}))
			for _, toolName := range tc.revokeTool {
				sess.reg.Remove(toolName)
			}
			refreshModelFacingCaches(sess)
			if hasPromptSkill(sess, tc.skillName) {
				t.Fatalf("session lacking %v still advertises %q in <skill-catalog>", tc.revokeTool, tc.skillName)
			}
			if _, err := sess.ProcessInput(tripwireCtx(t), "test", nil); err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if !sawError {
				t.Fatalf("use_skill(%q) succeeded despite lacking %v", tc.skillName, tc.revokeTool)
			}
		})
	}
}

func TestBundledGuideSkills_DisposeOnlyWorktreeExcludesUsingWorktrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	c := llm.NewClient()
	call := useSkillCall("s1", skill.UsingWorktreesSkillName)
	comm := communicateCall("c1", "done")
	var sawError bool
	f := newFakeAdapter("anthropic",
		func(llm.Request) llm.Response { return agenttest.ToolCallResponse(call) },
		func(req llm.Request) llm.Response {
			for _, msg := range req.Messages {
				for _, part := range msg.Content {
					if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "s1" {
						sawError = part.ToolResult.IsError
					}
				}
			}
			return agenttest.ToolCallResponse(comm)
		},
	)
	c.Register(f)
	sess := newSession(t, withClient(c), withProfile(newAnthropicProfile("claude-test")), withDir(root), withConfig(SessionConfig{
		MaxSubagentDepth: 3,
		StateDir:         t.TempDir(),
		spawn: spawnConfig{
			parentSessionID:     "parent-session",
			depth:               1,
			delegationAllowance: 1,
			isolation:           "worktree",
		},
	}))
	if !sess.worktreeDisposeOnlySurface() || sess.reg.Get("manage_worktree") == nil {
		t.Fatalf("expected dispose-only manage_worktree surface on isolated coordinator")
	}
	data, _ := sess.buildPromptData(sess.currentEnv())
	if !data.WorktreeDisposeOnly {
		t.Fatal("expected WorktreeDisposeOnly prompt input to be true")
	}
	if slices.ContainsFunc(data.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == skill.UsingWorktreesSkillName }) {
		t.Fatalf("dispose-only coordinator still advertises %q in <skill-catalog>", skill.UsingWorktreesSkillName)
	}
	prompt, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("renderSystemPrompt: %s", warning)
	}
	if strings.Contains(prompt, skill.UsingWorktreesSkillName) {
		t.Fatalf("dispose-only coordinator prompt still points to %q", skill.UsingWorktreesSkillName)
	}
	if _, err := sess.ProcessInput(tripwireCtx(t), "test", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !sawError {
		t.Fatalf("use_skill(%q) succeeded on dispose-only coordinator", skill.UsingWorktreesSkillName)
	}
}
