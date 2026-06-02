package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

// writeSkillDirect writes a SKILL.md directly into <dir>/<name>/SKILL.md.
// Use for extra dirs where the dir itself is the skills directory.
func writeSkillDirect(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// writeSkillMD writes a SKILL.md into <dir>/skills/<name>/SKILL.md.
// Use for project directories where skills live in a skills/ subdirectory.
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

	env := execenv.NewLocalExecutionEnvironment(root)
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
	env := execenv.NewLocalExecutionEnvironment(sub)
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

	env := execenv.NewLocalExecutionEnvironment(root)
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

	env := execenv.NewLocalExecutionEnvironment(root)
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

	env := execenv.NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env)

	if len(skills) != 0 {
		t.Errorf("expected 0 skills (no description), got %d: %v", len(skills), skills)
	}
}

func TestLoadSkillBody_ReturnsBody(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting skill\"\n---\nHello instructions.\n")

	env := execenv.NewLocalExecutionEnvironment(root)
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

	env := execenv.NewLocalExecutionEnvironment(root)
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

func TestDiscoverSkills_ExtraDirs(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	// No skills in the project itself. Extra dir points directly at a
	// directory whose subdirectories contain SKILL.md files.
	extraDir := t.TempDir()
	writeSkillDirect(t, extraDir, "external", "---\nname: external\ndescription: \"External skill\"\n---\nExternal instructions.\n")

	env := execenv.NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, extraDir)

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %v", len(skills), skills)
	}
	if _, ok := skills["external"]; !ok {
		t.Error("expected skill 'external' from extra dir")
	}
}

func TestDiscoverSkills_ExtraDirShadows(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Project greeting\"\n---\nProject.\n")

	extraDir := t.TempDir()
	writeSkillDirect(t, extraDir, "greet", "---\nname: greet\ndescription: \"External greeting\"\n---\nExternal.\n")

	env := execenv.NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, extraDir)

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %v", len(skills), skills)
	}
	s := skills["greet"]
	if s.Description != "External greeting" {
		t.Errorf("description = %q, want %q (extra dir should shadow)", s.Description, "External greeting")
	}
}

func TestDiscoverSkills_ExtraDirMissing(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting\"\n---\nGreet.\n")

	env := execenv.NewLocalExecutionEnvironment(root)
	// Nonexistent extra dir should be silently skipped.
	skills := DiscoverSkills(env, "/nonexistent/path/that/does/not/exist")

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %v", len(skills), skills)
	}
	if _, ok := skills["greet"]; !ok {
		t.Error("expected skill 'greet'")
	}
}

func TestDiscoverSkills_MultipleSkills(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: \"Greeting\"\n---\nGreet.\n")
	writeSkillMD(t, root, "deploy", "---\nname: deploy\ndescription: \"Deploy\"\n---\nDeploy.\n")

	env := execenv.NewLocalExecutionEnvironment(root)
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
