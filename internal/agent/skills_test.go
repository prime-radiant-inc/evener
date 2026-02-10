package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillMD(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestDiscoverSkills_FindsSkillsDir(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nHello instructions.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %v", len(skills), skills)
	}
	s, ok := skills["greet"]
	if !ok {
		t.Fatal("expected skill named 'greet'")
	}
	if s.Description != "Greeting skill" {
		t.Errorf("description = %q, want %q", s.Description, "Greeting skill")
	}
	if s.SkillFile == "" {
		t.Error("SkillFile should be set")
	}
}

func TestDiscoverSkills_DeeperShadows(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// Skill at root level.
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Root greeting\"\n---\nRoot.\n")

	// Skill at deeper level with same name.
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeSkillMD(t, sub, "greet", "---\nname: greet\ndescription: \"Sub greeting\"\n---\nSub.\n")

	// cwd is the deeper directory.
	env := NewLocalExecutionEnvironment(sub)
	skills := DiscoverSkills(env)

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (shadowed), got %d: %v", len(skills), skills)
	}
	s := skills["greet"]
	if s.Description != "Sub greeting" {
		t.Errorf("description = %q, want %q (deeper should shadow)", s.Description, "Sub greeting")
	}
}

func TestDiscoverSkills_NoSkillsDir(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscoverSkills_MissingName(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// SKILL.md without name field — should be skipped.
	writeSkillMD(t, root, "bad", "---\ndescription: \"No name\"\n---\nBody.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	if len(skills) != 0 {
		t.Errorf("expected 0 skills (no name), got %d: %v", len(skills), skills)
	}
}

func TestDiscoverSkills_MissingDescription(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// SKILL.md without description field — should be skipped.
	writeSkillMD(t, root, "bad", "---\nname: bad\n---\nBody.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	if len(skills) != 0 {
		t.Errorf("expected 0 skills (no description), got %d: %v", len(skills), skills)
	}
}

func TestLoadSkillBody_ReturnsBody(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nHello instructions.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	s, ok := skills["greet"]
	if !ok {
		t.Fatal("expected skill named 'greet'")
	}

	body, err := LoadSkillBody(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Hello instructions.\n" {
		t.Errorf("body = %q, want %q", body, "Hello instructions.\n")
	}
}

func TestDiscoverSkills_AllowedTools(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "deploy", "---\nname: deploy\ndescription: \"Deploy skill\"\nallowed-tools:\n  - shell\n  - read_file\n---\nDeploy instructions.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	s, ok := skills["deploy"]
	if !ok {
		t.Fatal("expected skill named 'deploy'")
	}
	if len(s.AllowedTools) != 2 {
		t.Fatalf("AllowedTools length = %d, want 2", len(s.AllowedTools))
	}
	if s.AllowedTools[0] != "shell" || s.AllowedTools[1] != "read_file" {
		t.Errorf("AllowedTools = %v, want [shell read_file]", s.AllowedTools)
	}
}

func TestDiscoverSkills_MultipleSkills(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting\"\n---\nGreet.\n")
	writeSkillMD(t, root, "deploy", "---\nname: deploy\ndescription: \"Deploy\"\n---\nDeploy.\n")

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d: %v", len(skills), skills)
	}
	if _, ok := skills["greet"]; !ok {
		t.Error("expected skill 'greet'")
	}
	if _, ok := skills["deploy"]; !ok {
		t.Error("expected skill 'deploy'")
	}
}
