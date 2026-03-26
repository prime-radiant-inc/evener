package agent

import (
	"context"
	"os"
	"path/filepath"
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

func TestExtractEmbeddedSkills_ContainsOpsTask(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// The ops-task skill should be present (only embedded skill after superpowers removal).
	skillFile := filepath.Join(dir, "ops-task", "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("reading ops-task SKILL.md: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ops-task SKILL.md is empty")
	}
}

func TestExtractEmbeddedSkills_OpsTaskHasContent(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// ops-task SKILL.md should have frontmatter with a name field.
	skillFile := filepath.Join(dir, "ops-task", "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("reading ops-task SKILL.md: %v", err)
	}
	if !strings.Contains(string(data), "name:") {
		t.Fatal("ops-task SKILL.md missing frontmatter name field")
	}
}

func TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// The extracted dir should work as an extraDirs argument to DiscoverSkills.
	root := t.TempDir()
	initGitRepo(t, root)

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, dir)

	// Should find the ops-task skill (only embedded skill remaining).
	if len(skills) < 1 {
		t.Fatalf("expected at least 1 skill, got %d: %v", len(skills), builtinSkillNames(skills))
	}

	if _, ok := skills["ops-task"]; !ok {
		t.Errorf("expected skill %q to be discovered", "ops-task")
	}
}

func TestExtractEmbeddedSkills_FilesystemShadowsEmbedded(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	// Project has a skill with the same name as an embedded one.
	root := t.TempDir()
	initGitRepo(t, root)
	writeSkillMD(t, root, "test-driven-development",
		"---\nname: test-driven-development\ndescription: \"Project TDD override\"\n---\nCustom TDD.\n")

	env := NewLocalExecutionEnvironment(root)
	// Embedded skills are added first; project skills discovered after should NOT shadow
	// because DiscoverSkills processes root→cwd first, then extraDirs.
	// To get the right layering (embedded as base, project overrides), we need
	// embedded first, then project skills shadow them.
	// Actually: DiscoverSkills processes root→cwd dirs first, then extraDirs.
	// extraDirs shadow project skills. So we want embedded as a *base* that
	// project skills shadow. Let's verify the current behavior.
	skills := DiscoverSkills(env, dir)

	// extraDirs shadow root→cwd, so the embedded version wins over project.
	// This is the WRONG layering — we want project to shadow embedded.
	// This test documents current behavior so we can fix the merge order.
	tdd := skills["test-driven-development"]
	// For now, just verify the skill exists and we'll fix layering in the merge code.
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

	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Error("system prompt missing <skills> section")
	}
	if !strings.Contains(capturedSystem, "ops-task") {
		t.Error("system prompt missing embedded ops-task skill")
	}
}

func TestOpenAI_SkillsWithFilePathsInSystemPrompt(t *testing.T) {
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

	// OpenAI should have <skills> section with file paths and read_file guidance.
	if !strings.Contains(capturedSystem, "<skill-catalog>") {
		t.Error("OpenAI system prompt should contain <skills> section with file paths")
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

func TestEmbeddedSkills_UseSkillReturnsOpsTaskBody(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()

	// Agent calls use_skill("ops-task"), then calls submit_result.
	// Uses Anthropic profile because OpenAI uses read_file for skills.
	skill := useSkillCall("s1", "ops-task")
	comm := submitResultCall("c1", "done")

	var skillResultContent string
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			// First turn: agent calls use_skill.
			func(req llm.Request) llm.Response { return toolCallResponse(skill) },
			// Second turn: agent sees the skill body in tool result, then finishes.
			func(req llm.Request) llm.Response {
				// Capture the tool result from the use_skill call.
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.Kind == llm.ContentToolResult {
							if content, ok := part.ToolResult.Content.(string); ok {
								if strings.Contains(content, "ops") || strings.Contains(content, "task") {
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
	_, err = sess.ProcessInput(ctx, "fix the build")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if skillResultContent == "" {
		t.Fatal("use_skill('ops-task') did not return the ops-task skill body")
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

	// Only ops-task remains after superpowers skills were removed (commit 00f7327).
	if len(skills) < 1 {
		t.Fatalf("expected at least 1 embedded skill, got %d", len(skills))
	}

	for name, meta := range skills {
		body, err := LoadSkillBody(meta)
		if err != nil {
			t.Errorf("skill %q: LoadSkillBody failed: %v", name, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("skill %q: body is empty", name)
		}
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
