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
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return agenttest.ToolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	// Anthropic profile renders skills in system prompt, so we can verify shadowing.
	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
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
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(skill) },
			func(req llm.Request) llm.Response {
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
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx := tripwireCtx(t)
	_, err = sess.ProcessInput(ctx, "use my skill", nil)
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
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(skill) },
			func(req llm.Request) llm.Response {
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
							skillResultIsError = part.ToolResult.IsError
						}
					}
				}
				return agenttest.ToolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx := tripwireCtx(t)
	_, err = sess.ProcessInput(ctx, "check my work", nil)
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
