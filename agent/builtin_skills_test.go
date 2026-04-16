package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestExtractEmbeddedSkills_CreatesDir(t *testing.T) {
	dir, err := extractEmbeddedSkills()
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

func TestExtractEmbeddedSkills_EmptyAfterOpsTaskRemoval(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// No embedded skills remain after ops-task removal.
	// The skill catalog is populated by filesystem-discovered project skills only.
	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)
	if len(skills) != 0 {
		t.Fatalf("expected 0 embedded skills, got %d", len(skills))
	}
}

func TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// The extracted dir should work as an extraDirs argument to DiscoverSkills,
	// even when empty (no embedded skills remain).
	root := t.TempDir()
	initGitRepo(t, root)

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, dir)

	// No embedded skills remain, so only project skills (none here) are found.
	if len(skills) != 0 {
		t.Fatalf("expected 0 skills from empty embedded dir, got %d: %v", len(skills), builtinSkillNames(skills))
	}
}

func TestExtractEmbeddedSkills_FilesystemShadowsEmbedded(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// With no embedded skills, project skills are the only source.
	root := t.TempDir()
	initGitRepo(t, root)
	writeSkillMD(t, root, "test-driven-development",
		"---\nname: test-driven-development\ndescription: \"Project TDD\"\n---\nCustom TDD.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, dir)

	tdd := skills["test-driven-development"]
	if tdd.Name != "test-driven-development" {
		t.Fatalf("expected TDD skill, got %q", tdd.Name)
	}
}

func TestEmbeddedSkills_InSystemPrompt(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()
	comm := submitResultCall("c1", "done")

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
	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi")
	sess.Close()

	// With no embedded skills and no project skills, the skill catalog
	// should be absent from the system prompt.
	if strings.Contains(capturedSystem, "\n<skill-catalog>\n") {
		t.Error("system prompt should not contain <skill-catalog> when no skills exist")
	}
}

func TestOpenAI_SkillsWithFilePathsInSystemPrompt(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// Create a project skill so the skill catalog is populated.
	writeSkillMD(t, root, "my-skill",
		"---\nname: my-skill\ndescription: \"Test skill\"\n---\nBody.\n")

	c := llm.NewClient()
	comm := submitResultCall("c1", "done")

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

	// OpenAI profile lists skills with file paths (not use_skill) so the
	// model can load them via read_file.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi")
	sess.Close()

	// OpenAI should have <skill-catalog> section with file paths and read_file guidance.
	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Error("OpenAI system prompt should contain <skill-catalog> section with file paths")
	}
	if !strings.Contains(capturedSystem, "read_file") {
		t.Error("OpenAI system prompt should instruct model to use read_file for skills")
	}
	// OpenAI should NOT have use_skill tool.
	if strings.Contains(capturedSystem, "- use_skill:") {
		t.Error("OpenAI should not have use_skill tool listed")
	}
}

func TestEmbeddedSkills_ProjectShadowsEmbedded(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// Project has a custom TDD skill that should shadow the embedded one.
	writeSkillMD(t, root, "test-driven-development",
		"---\nname: test-driven-development\ndescription: \"Custom project TDD\"\n---\nCustom TDD body.\n")

	c := llm.NewClient()
	comm := submitResultCall("c1", "done")

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
	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi")
	sess.Close()

	// Should have the project override description, not the embedded one.
	if !strings.Contains(capturedSystem, "Custom project TDD") {
		t.Error("system prompt should show project TDD description, not embedded")
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
	comm := submitResultCall("c1", "done")

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

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "use my skill")
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
	comm := submitResultCall("c1", "done")

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

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "check my work")
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
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)

	// No embedded skills remain after ops-task removal.
	// This test verifies the extraction still works without error.
	if len(skills) != 0 {
		t.Fatalf("expected 0 embedded skills, got %d", len(skills))
	}
}

func TestNonInteractive_SystemPromptContainsGuidance(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()
	comm := submitResultCall("c1", "done")

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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(root), SessionConfig{
		NonInteractive: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi")
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
	comm := submitResultCall("c1", "done")

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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(root), SessionConfig{
		NonInteractive: false,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi")
	sess.Close()

	if strings.Contains(capturedSystem, "no human available") {
		t.Error("system prompt should NOT contain non-interactive guidance when NonInteractive is false")
	}
}

// builtinSkillNames returns a slice of skill names for test output.
func builtinSkillNames(skills map[string]SkillMeta) []string {
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	return names
}
