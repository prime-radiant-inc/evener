package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/skill"
)

func TestSkillSourcesMetadataOnlyFirstManifestReservation(t *testing.T) {
	// SkillSources reports these back resolved, so the fixture builds its
	// expectations from the resolved spelling.
	first, second := fixtureRoot(t), fixtureRoot(t)
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"probe"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "skills", "selected"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "skills", "selected", "SKILL.md"), []byte("---\nname: selected\ndescription: fixture\n---\nSELECTED_1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(first, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "hooks", "hooks.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(first); err == nil {
		t.Fatal("fixture must fail full component loading")
	}
	missing := filepath.Join(t.TempDir(), "absent")
	sources, diagnostics := SkillSources([]string{first, second, missing})
	if len(sources) != 1 || sources[0].Name != "probe" || sources[0].Dir != first {
		t.Fatalf("sources=%+v", sources)
	}
	if len(diagnostics) != 2 || diagnostics[0].Category != "collision" || diagnostics[0].Source != first || diagnostics[0].OtherSource != second || diagnostics[1].Category != "unreadable_source" {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
	c := skill.Discover(nil, skill.DiscoverOptions{HomeDir: t.TempDir(), Plugins: sources})
	if d, err := c.ResolveExact("probe:selected"); err != nil || d.Meta.SkillFile != filepath.Join(first, "skills", "selected", "SKILL.md") {
		t.Fatalf("selected=%+v err=%v", d, err)
	}
}

func TestSkillSourcesSelectsASymlinkedPluginByItsRealDirectory(t *testing.T) {
	root := fixtureRoot(t)
	target := filepath.Join(root, "revision")
	if err := os.MkdirAll(filepath.Join(target, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".claude-plugin", "plugin.json"), []byte(`{"name":"probe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "skills", "selected"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "skills", "selected", "SKILL.md"), []byte("---\nname: selected\ndescription: fixture\n---\nBODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	sources, diagnostics := SkillSources([]string{link})
	if len(diagnostics) != 0 || len(sources) != 1 || sources[0].Dir != target {
		t.Fatalf("sources=%+v diagnostics=%+v, want the target directory %q", sources, diagnostics, target)
	}
	c := skill.Discover(nil, skill.DiscoverOptions{HomeDir: t.TempDir(), Plugins: sources})
	if d, err := c.ResolveExact("probe:selected"); err != nil || d.Meta.SkillFile != filepath.Join(target, "skills", "selected", "SKILL.md") {
		t.Fatalf("selected=%+v err=%v", d, err)
	}
}
