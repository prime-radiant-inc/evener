package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

// writeSkill lays out a skill directory <root>/<dir>/SKILL.md with the given
// frontmatter + body.
func writeSkill(t *testing.T, root, dir, content string) string {
	t.Helper()
	sd := filepath.Join(root, dir)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sd, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validSkill = `---
name: tdd
description: Test-driven development discipline
allowed-tools:
  - read_file
  - edit_file
---
Write the test first, watch it fail, then make it pass.
`

func TestScanSkillsDir_ParsesValidSkillAndSkipsInvalid(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "tdd", validSkill)
	// Missing required fields → skipped.
	writeSkill(t, root, "nope", "---\nname: only-name\n---\nbody\n")
	// Not a directory-with-SKILL.md → skipped (a stray file).
	if err := os.WriteFile(filepath.Join(root, "loose.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := map[string]SkillMeta{}
	ScanSkillsDir(root, out)

	if len(out) != 1 {
		t.Fatalf("expected 1 valid skill, got %d: %v", len(out), out)
	}
	meta, ok := out["tdd"]
	if !ok {
		t.Fatal("tdd skill not discovered")
	}
	if meta.Description != "Test-driven development discipline" {
		t.Errorf("description = %q", meta.Description)
	}
	if len(meta.AllowedTools) != 2 || meta.AllowedTools[0] != "read_file" {
		t.Errorf("allowed-tools = %v, want [read_file edit_file]", meta.AllowedTools)
	}
	if meta.SkillFile == "" || meta.Dir == "" {
		t.Errorf("meta paths not set: %+v", meta)
	}
}

func TestScanSkillsDir_MissingDirIsNoop(t *testing.T) {
	out := map[string]SkillMeta{}
	ScanSkillsDir(filepath.Join(t.TempDir(), "does-not-exist"), out)
	if len(out) != 0 {
		t.Errorf("missing dir should yield no skills, got %v", out)
	}
}

func TestLoadSkillBody(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "tdd", validSkill)
	body, err := LoadSkillBody(SkillMeta{SkillFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Write the test first") {
		t.Errorf("body = %q", body)
	}
	// Missing file → error.
	if _, err := LoadSkillBody(SkillMeta{SkillFile: filepath.Join(root, "gone.md")}); err == nil {
		t.Error("missing skill file should error")
	}
}

func TestResolveSkillContent_ExactAndUnnamespaced(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "tdd", validSkill)
	skills := map[string]SkillMeta{"myplugin:tdd": {Name: "myplugin:tdd", SkillFile: path}}

	// Unnamespaced "tdd" resolves the namespaced key.
	body, err := ResolveSkillContent(skills, "tdd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Write the test first") {
		t.Errorf("unnamespaced resolve returned %q", body)
	}
	// Exact key resolves too.
	if body, err = ResolveSkillContent(skills, "myplugin:tdd"); err != nil || body == "" {
		t.Errorf("exact resolve failed: %v %q", err, body)
	}
	// Unknown → ("", nil).
	if body, err = ResolveSkillContent(skills, "unknown"); err != nil || body != "" {
		t.Errorf("unknown skill should be empty/nil, got %q %v", body, err)
	}
}

// DiscoverSkills walks the env's working directory tree plus extra dirs, with
// extra dirs shadowing project skills of the same name.
func TestDiscoverSkills_WalkAndShadow(t *testing.T) {
	proj := t.TempDir()
	writeSkill(t, filepath.Join(proj, "skills"), "tdd", validSkill)

	extra := t.TempDir()
	writeSkill(t, extra, "tdd", strings.Replace(validSkill, "Write the test first", "SHADOWED body", 1))

	env := execenv.NewLocalExecutionEnvironment(proj)
	skills := DiscoverSkills(env, extra)

	meta, ok := skills["tdd"]
	if !ok {
		t.Fatal("tdd not discovered")
	}
	body, err := LoadSkillBody(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "SHADOWED body") {
		t.Errorf("extra dir should shadow the project skill, got %q", body)
	}
}

func TestDiscoverSkills_NilEnv(t *testing.T) {
	if got := DiscoverSkills(nil); got != nil {
		t.Errorf("nil env should return nil, got %v", got)
	}
}

// ExtractEmbeddedSkills materializes the bundled skills to disk; the result is a
// directory of skill subdirs usable as a DiscoverSkills extraDir.
func TestExtractEmbeddedSkills(t *testing.T) {
	dir, err := ExtractEmbeddedSkills()
	if err != nil {
		t.Fatalf("ExtractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	out := map[string]SkillMeta{}
	ScanSkillsDir(dir, out)
	if len(out) == 0 {
		t.Errorf("expected at least one embedded skill under %s", dir)
	}
}

// EmbeddedSkillsDir extracts the immutable bundled skills exactly once per
// process and hands every caller the same shared, read-only directory. The
// embedded content is identical for every session, so re-extracting it per
// session (temp dir + file writes + teardown) is pure overhead.
func TestEmbeddedSkillsDir_CachesAcrossCalls(t *testing.T) {
	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (second): %v", err)
	}
	if first != second {
		t.Fatalf("expected the same cached dir, got %q then %q", first, second)
	}

	out := map[string]SkillMeta{}
	ScanSkillsDir(first, out)
	if len(out) == 0 {
		t.Errorf("expected at least one embedded skill under cached dir %s", first)
	}
}
