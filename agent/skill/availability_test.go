package skill

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

func TestSessionAvailability(t *testing.T) {
	t.Parallel()
	available := SessionAvailability{MemorySaveAvailable: true}
	unavailable := SessionAvailability{}
	if !available.SkillAvailable(GardeningMemorySkillName) {
		t.Fatal("memory-capable session must be offered gardening-memory")
	}
	if unavailable.SkillAvailable(GardeningMemorySkillName) {
		t.Fatal("session without save tools must not be offered gardening-memory")
	}
	for _, name := range []string{"plain", "doctoring-evener", "plugin:skill"} {
		if !unavailable.SkillAvailable(name) {
			t.Fatalf("session availability narrowed unrelated skill %q", name)
		}
	}

	for _, tc := range []struct {
		name      string
		available SessionAvailability
	}{
		{RunningJobsSkillName, SessionAvailability{RunningJobsAvailable: true}},
		{UsingWorktreesSkillName, SessionAvailability{WorktreeAvailable: true}},
		{ReadingTranscriptsSkillName, SessionAvailability{TranscriptsAvailable: true}},
	} {
		if !tc.available.SkillAvailable(tc.name) {
			t.Fatalf("capable session must be offered %q", tc.name)
		}
		if unavailable.SkillAvailable(tc.name) {
			t.Fatalf("session without capability must not be offered %q", tc.name)
		}
		if available.SkillAvailable(tc.name) {
			t.Fatalf("MemorySaveAvailable must not unlock %q", tc.name)
		}
	}
}

func TestIsAutoDisclosedGuideSkill(t *testing.T) {
	t.Parallel()
	for _, name := range []string{RunningJobsSkillName, UsingWorktreesSkillName, ReadingTranscriptsSkillName} {
		if !IsAutoDisclosedGuideSkill(Descriptor{CatalogName: name, Bundled: true}) {
			t.Fatalf("IsAutoDisclosedGuideSkill(bundled %q) = false, want true", name)
		}
		if IsAutoDisclosedGuideSkill(Descriptor{CatalogName: name, Bundled: false}) {
			t.Fatalf("IsAutoDisclosedGuideSkill(non-bundled override %q) = true, want false", name)
		}
	}
	for _, name := range []string{GardeningMemorySkillName, "doctoring-evener", "custom-skill"} {
		if IsAutoDisclosedGuideSkill(Descriptor{CatalogName: name, Bundled: true}) {
			t.Fatalf("IsAutoDisclosedGuideSkill(bundled %q) = true, want false", name)
		}
	}

	t.Run("discover_marks_only_bundled_source", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		userDir := t.TempDir()
		for _, spec := range []struct{ root, name string }{
			{filepath.Join(projectDir, ".agents", "skills"), UsingWorktreesSkillName},
			{userDir, RunningJobsSkillName},
		} {
			dir := filepath.Join(spec.root, spec.name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
				[]byte("---\nname: "+spec.name+"\ndescription: Override\n---\nBody.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cat := Discover(execenv.NewLocalExecutionEnvironment(projectDir), DiscoverOptions{UserSkillsDir: userDir})
		for _, overridden := range []string{UsingWorktreesSkillName, RunningJobsSkillName} {
			if d, ok := cat.Entries[overridden]; !ok || d.Bundled || IsAutoDisclosedGuideSkill(d) {
				t.Fatalf("overridden %q = %+v (ok=%v), want Bundled=false and not auto-disclosed", overridden, d, ok)
			}
		}
		if d, ok := cat.Entries[ReadingTranscriptsSkillName]; !ok || !d.Bundled || !IsAutoDisclosedGuideSkill(d) {
			t.Fatalf("bundled %q = %+v (ok=%v), want Bundled=true and auto-disclosed", ReadingTranscriptsSkillName, d, ok)
		}
	})
}

func TestUserEntriesForSession(t *testing.T) {
	t.Parallel()
	catalog := Catalog{Entries: map[string]Descriptor{
		"alpha":                  {CatalogName: "alpha", Controls: InvocationControls{UserInvocable: true}},
		GardeningMemorySkillName: {CatalogName: GardeningMemorySkillName, Controls: InvocationControls{UserInvocable: true}},
		"zeta":                   {CatalogName: "zeta", Controls: InvocationControls{UserInvocable: true}},
	}}
	names := func(entries []Descriptor) []string {
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.CatalogName)
		}
		return out
	}
	if got := names(catalog.UserEntriesForSession(SessionAvailability{})); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Fatalf("without save tools = %v, want canonical order without gardening-memory", got)
	}
	if got := names(catalog.UserEntriesForSession(SessionAvailability{MemorySaveAvailable: true})); !slices.Equal(got, []string{"alpha", GardeningMemorySkillName, "zeta"}) {
		t.Fatalf("with save tools = %v, want every user entry in canonical order", got)
	}
}
