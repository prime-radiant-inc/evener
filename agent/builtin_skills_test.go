package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/llm"
)

const embeddedDoctoringSkill = "doctoring-serf"

func TestExtractEmbeddedSkills_CreatesDir(t *testing.T) {
	dir, err := skill.ExtractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// Should create a temp directory with skill subdirectories.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one skill directory, got 0")
	}
}

func TestExtractEmbeddedSkills_IncludesDoctoringSkill(t *testing.T) {
	dir, err := skill.ExtractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]skill.SkillMeta)
	skill.ScanSkillsDir(dir, skills)
	meta, ok := skills[embeddedDoctoringSkill]
	if !ok {
		t.Fatalf("expected embedded %q skill, got %v", embeddedDoctoringSkill, builtinSkillNames(skills))
	}
	if !strings.Contains(meta.Description, "serf") {
		t.Fatalf("embedded %q description = %q, want serf-specific description", embeddedDoctoringSkill, meta.Description)
	}
}

func TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills(t *testing.T) {
	dir, err := skill.ExtractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// The extracted dir should work as an extraDirs argument to DiscoverSkills.
	root := t.TempDir()
	initGitRepo(t, root)

	env := execenv.NewLocalExecutionEnvironment(root)
	skills := skill.DiscoverSkills(env, dir)

	if _, ok := skills[embeddedDoctoringSkill]; !ok {
		t.Fatalf("expected embedded %q skill from extracted dir, got %v", embeddedDoctoringSkill, builtinSkillNames(skills))
	}
}

func TestExtractEmbeddedSkills_FilesystemShadowsEmbedded(t *testing.T) {
	dir, err := skill.ExtractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	root := t.TempDir()
	initGitRepo(t, root)
	writeSkillMD(t, root, embeddedDoctoringSkill,
		"---\nname: doctoring-serf\ndescription: \"Project doctoring override\"\n---\nCustom doctoring.\n")

	env := execenv.NewLocalExecutionEnvironment(root)
	skills := make(map[string]skill.SkillMeta)
	skill.ScanSkillsDir(dir, skills)
	for name, meta := range skill.DiscoverSkills(env) {
		skills[name] = meta
	}

	doctoring := skills[embeddedDoctoringSkill]
	if doctoring.Description != "Project doctoring override" {
		t.Fatalf("expected project skill to shadow embedded skill, got description %q", doctoring.Description)
	}
}

func TestEmbeddedSkills_InSystemPrompt(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

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
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	// Anthropic profile has use_skill tool, so skills are listed in system prompt.
	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Fatal("system prompt should contain <skill-catalog> for embedded skills")
	}
	if !strings.Contains(capturedSystem, "- doctoring-serf:") {
		t.Fatal("system prompt should list embedded doctoring-serf skill")
	}
}

func TestOpenAI_SkillsWithUseSkillInSystemPrompt(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// Create a project skill so the skill catalog is populated.
	writeSkillMD(t, root, "my-skill",
		"---\nname: my-skill\ndescription: \"Test skill\"\n---\nBody.\n")

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Error("OpenAI system prompt should contain <skill-catalog> section")
	}
	if !strings.Contains(capturedSystem, "Load a skill by calling use_skill with its name") {
		t.Error("OpenAI system prompt should instruct model to use use_skill for skills")
	}
	if !strings.Contains(capturedSystem, "- my-skill: Test skill [") {
		t.Error("OpenAI system prompt should list skill directory for use_skill")
	}
}

func TestEmbeddedSkills_ProjectShadowsEmbedded(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// Project has a custom doctoring skill that should shadow the embedded one.
	writeSkillMD(t, root, embeddedDoctoringSkill,
		"---\nname: doctoring-serf\ndescription: \"Custom project doctoring\"\n---\nCustom doctoring body.\n")

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
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	// Anthropic profile renders skills in system prompt, so we can verify shadowing.
	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "Custom project doctoring") {
		t.Error("system prompt should show project doctoring description, not embedded")
	}
}

func TestEmbeddedSkills_UseSkillWithProjectSkill(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

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
			func(req llm.Request) llm.Response { return toolCallResponse(skill) },
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
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()

	// Calling use_skill with a skill that doesn't exist should return an error.
	skill := useSkillCall("s1", "nonexistent-skill")
	comm := communicateCall("c1", "done")

	var skillResultIsError bool
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(skill) },
			func(req llm.Request) llm.Response {
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
							skillResultIsError = part.ToolResult.IsError
						}
					}
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
	// Verify every embedded skill can be discovered and its body loaded.
	dir, err := skill.ExtractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]skill.SkillMeta)
	skill.ScanSkillsDir(dir, skills)

	if len(skills) == 0 {
		t.Fatal("expected at least one embedded skill")
	}
	for name, meta := range skills {
		body, err := skill.LoadSkillBody(meta)
		if err != nil {
			t.Fatalf("LoadSkillBody(%s): %v", name, err)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("embedded skill %q has empty body", name)
		}
	}
}

func TestNonInteractive_SystemPromptContainsGuidance(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{
		NonInteractive: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if !strings.Contains(capturedSystem, "non-interactive") {
		t.Error("system prompt missing non-interactive guidance")
	}
	if !strings.Contains(capturedSystem, "no human available") {
		t.Error("system prompt missing 'no human available' note")
	}
}

func TestNonInteractive_NotPresentWhenFalse(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{
		NonInteractive: false,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	if strings.Contains(capturedSystem, "no human available") {
		t.Error("system prompt should NOT contain non-interactive guidance when NonInteractive is false")
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
