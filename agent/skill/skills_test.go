package skill

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCatalogEntriesUsesCanonicalMapKeys(t *testing.T) {
	skills := map[string]SkillMeta{
		"plugin:simplify": {Name: "simplify", Description: "plugin copy", AllowedTools: []string{"read_file"}, Dir: "/tmp/plugin", SkillFile: "/tmp/plugin/SKILL.md"},
		"simplify":        {Name: "simplify", Description: "project copy"},
	}
	wantSource := map[string]SkillMeta{
		"plugin:simplify": {Name: "simplify", Description: "plugin copy", AllowedTools: []string{"read_file"}, Dir: "/tmp/plugin", SkillFile: "/tmp/plugin/SKILL.md"},
		"simplify":        {Name: "simplify", Description: "project copy"},
	}
	got := CatalogEntries(skills)
	if len(got) != 2 || got[0].Name != "plugin:simplify" || got[1].Name != "simplify" {
		t.Fatalf("catalog names = %+v", got)
	}
	if !reflect.DeepEqual(skills, wantSource) {
		t.Fatal("CatalogEntries mutated the source map")
	}
	if got[0].Dir != "" || got[0].SkillFile != "" || !reflect.DeepEqual(got[0].AllowedTools, []string{"read_file"}) {
		t.Fatalf("catalog exposed filesystem metadata or aliased tools: %+v", got[0])
	}
	got[0].AllowedTools[0] = "mutated"
	if skills["plugin:simplify"].AllowedTools[0] != "read_file" {
		t.Fatal("CatalogEntries aliased AllowedTools")
	}
}

func TestResolveSkillRejectsAmbiguousUnqualifiedName(t *testing.T) {
	skills := map[string]SkillMeta{
		"one:review": {Name: "review"},
		"two:review": {Name: "review"},
	}
	if _, _, ok := ResolveSkill(skills, "review"); ok {
		t.Fatal("ambiguous unqualified skill resolved")
	}
	if key, _, ok := ResolveSkill(skills, "one:review"); !ok || key != "one:review" {
		t.Fatalf("qualified resolution = %q, %v", key, ok)
	}
}

func TestIsSlashAddressableName(t *testing.T) {
	long := make([]byte, 128)
	for i := range long {
		long[i] = 'a'
	}
	tooLong := append(append([]byte{}, long...), 'a')
	cases := []struct {
		name string
		want bool
	}{
		{"a", true},
		{"skill-2_name", true},
		{"plugin:skill", true},
		{string(long), true},
		{"", false},
		{"-skill", false},
		{"_skill", true},
		{"plugin:one:skill", false},
		{"skill name", false},
		{"Skill", true},
		{string(tooLong), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSlashAddressableName(tc.name); got != tc.want {
				t.Fatalf("IsSlashAddressableName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestResolveSkillContentRejectsAmbiguousSuffix(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(path, []byte("---\nname: review\ndescription: test\n---\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	skills := map[string]SkillMeta{
		"one:review": {Name: "review", SkillFile: write("one", "one")},
		"two:review": {Name: "review", SkillFile: write("two", "two")},
	}
	for range 20 {
		body, err := ResolveSkillContent(skills, "review")
		if err != nil || body != "" {
			t.Fatalf("ambiguous resolution = %q, %v", body, err)
		}
	}
}

func TestResolveSkillUsesUniqueSuffixAndExactPrecedence(t *testing.T) {
	project := SkillMeta{Name: "project", Description: "project"}
	plugin := SkillMeta{Name: "plugin:review", Description: "plugin"}
	skills := map[string]SkillMeta{
		"review":        project,
		"plugin:review": plugin,
	}
	if key, meta, ok := ResolveSkill(skills, "review"); !ok || key != "review" || meta.Description != "project" {
		t.Fatalf("exact resolution = %q, %+v, %v", key, meta, ok)
	}

	delete(skills, "review")
	if key, meta, ok := ResolveSkill(skills, "review"); !ok || key != "plugin:review" || meta.Description != "plugin" {
		t.Fatalf("unique suffix resolution = %q, %+v, %v", key, meta, ok)
	}
}
