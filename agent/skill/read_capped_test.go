package skill

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// A skill source is read whole at both discovery and load; a SKILL.md past the
// whole-file read cap must fail fast with that refusal instead of loading
// (issue #4556). The fixtures are sparse, so reaching the refusal costs no I/O.

// stretchOversize extends path to one byte past the whole-file read cap.
func stretchOversize(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s for stretch: %v", path, err)
	}
	defer f.Close()
	if err := f.Truncate(execenv.MaxWholeFileReadBytes + 1); err != nil {
		t.Fatalf("stretch %s oversize: %v", path, err)
	}
}

// TestSkillLoadRefusesAnOversizedSource pins Load: a SKILL.md past the cap
// fails the load with the read-limit refusal, not by parsing a huge body.
func TestSkillLoadRefusesAnOversizedSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	descriptor := parseLoadFixture(t, path, []byte("---\nname: probe\ndescription: fixture\n---\nbody\n"))
	stretchOversize(t, path)

	_, _, err := Load(descriptor)
	if _, capped := errors.AsType[*execenv.ReadLimitError](err); !capped {
		t.Fatalf("Load(oversized SKILL.md) err = %v, want the read-limit refusal", err)
	}
}

// TestParseSkillFileSkipsAnOversizedSource pins discovery: a SKILL.md past the
// cap reads as no skill at all, the way any unreadable source does.
func TestParseSkillFileSkipsAnOversizedSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: probe\ndescription: fixture\n---\nbody\n"), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	stretchOversize(t, path)
	if _, ok := parseSkillFile(path); ok {
		t.Fatal("parseSkillFile(oversized SKILL.md) = ok, want the skill skipped")
	}
}

// TestDiscoverSkipsAnOversizedSource pins the live discovery path (session
// start, plugin resolution) against an oversized SKILL.md: the source is
// skipped with an unreadable-source diagnostic instead of being ingested
// whole (issue #4556).
func TestDiscoverSkipsAnOversizedSource(t *testing.T) {
	t.Parallel()
	extra := t.TempDir()
	source := portableWriteSkill(t, extra, "probe", "---\nname: probe\ndescription: fixture\n---\nbody\n")
	stretchOversize(t, source)

	catalog := Discover(execenv.NewLocalExecutionEnvironment(t.TempDir()), DiscoverOptions{ExtraDirs: []string{extra}})
	if _, ok := catalog.Entries["probe"]; ok {
		t.Fatalf("Discover admitted an oversized SKILL.md at %s", source)
	}
	for _, d := range catalog.Diagnostics {
		if d.Category == "unreadable_source" && d.Source == source {
			return
		}
	}
	t.Fatalf("Discover reported no unreadable-source diagnostic for %s: %+v", source, catalog.Diagnostics)
}
