package skill

import (
	"os"
	"path/filepath"
	"testing"
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
	if meta.Metadata["name"] != "tdd" || meta.Metadata["description"] != "Test-driven development discipline" {
		t.Errorf("frontmatter metadata not preserved: %+v", meta.Metadata)
	}
	if !filepath.IsAbs(meta.SkillFile) || !filepath.IsAbs(meta.Dir) || meta.Dir != filepath.Dir(meta.SkillFile) {
		t.Errorf("meta paths not absolute and paired: %+v", meta)
	}
}

func TestScanSkillsDir_MissingDirIsNoop(t *testing.T) {
	out := map[string]SkillMeta{}
	ScanSkillsDir(filepath.Join(t.TempDir(), "does-not-exist"), out)
	if len(out) != 0 {
		t.Errorf("missing dir should yield no skills, got %v", out)
	}
}

// EmbeddedSkillsDir extracts the immutable bundled skills exactly once per
// process and hands every caller the same shared, read-only directory. The
// embedded content is identical for every session, so re-extracting it per
// session is pure overhead.
func TestEmbeddedSkillsDir_CachesAcrossCalls(t *testing.T) {
	pointEmbeddedSkillsAtBase(t, t.TempDir())
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
