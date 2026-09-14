package skill

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const sampleSkillDocument = "---\nname: sample\ndescription: \"A sample skill\"\n---\nBody.\n"

func skillFSFixture(files map[string]string) fstest.MapFS {
	m := make(fstest.MapFS, len(files))
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func sampleSkillFS() fstest.MapFS {
	return skillFSFixture(map[string]string{
		"sample/SKILL.md":               sampleSkillDocument,
		"sample/references/notes.md":    "notes\n",
		"sample/runbooks/check-name.md": "runbook\n",
	})
}

func TestDigestSkillsFS_TracksContent(t *testing.T) {
	first, err := digestSkillsFS(sampleSkillFS())
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	again, err := digestSkillsFS(sampleSkillFS())
	if err != nil {
		t.Fatalf("digestSkillsFS (again): %v", err)
	}
	if first != again {
		t.Fatalf("digest is not stable: %q then %q", first, again)
	}

	changed, err := digestSkillsFS(skillFSFixture(map[string]string{
		"sample/SKILL.md": sampleSkillDocument + "changed\n",
	}))
	if err != nil {
		t.Fatalf("digestSkillsFS (changed): %v", err)
	}
	if changed == first {
		t.Fatal("digest ignored changed content")
	}
}

// The published directory is named for the digest of its contents, so a later
// process with the same embedded content reuses it instead of copying again.
func TestMaterializeEmbeddedSkills_PublishesOnceAndReuses(t *testing.T) {
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}

	first, err := materializeEmbeddedSkills(fsys, base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	want := filepath.Join(base, embeddedSkillsPrefix+digest)
	if first != want {
		t.Fatalf("published dir = %q, want %q", first, want)
	}
	if _, err := os.Stat(filepath.Join(first, "sample", "SKILL.md")); err != nil {
		t.Fatalf("published copy missing its skill: %v", err)
	}

	second, err := materializeEmbeddedSkills(fsys, base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills (second): %v", err)
	}
	if second != first {
		t.Fatalf("expected the published copy to be reused, got %q then %q", first, second)
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected exactly one published copy, got %d: %v", len(entries), names)
	}
}

func TestMaterializeEmbeddedSkills_DistinctContentDistinctDirs(t *testing.T) {
	base := t.TempDir()
	first, err := materializeEmbeddedSkills(sampleSkillFS(), base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	other := skillFSFixture(map[string]string{"sample/SKILL.md": sampleSkillDocument + "other\n"})
	second, err := materializeEmbeddedSkills(other, base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills (other): %v", err)
	}
	if first == second {
		t.Fatalf("distinct content shared the directory %q", first)
	}
}

// A foreign or tampered occupant of the published name must never be adopted:
// the content is verified against the digest, and the caller still gets a
// readable private copy.
func TestMaterializeEmbeddedSkills_FallsBackWhenPublishedNameIsOccupied(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	if err := os.WriteFile(dest, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("occupy published name: %v", err)
	}

	dir, err := materializeEmbeddedSkills(fsys, base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)
	if dir == dest {
		t.Fatalf("adopted the occupied name %q", dest)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample", "SKILL.md")); err != nil {
		t.Fatalf("fallback copy missing its skill: %v", err)
	}
}

func TestMaterializeEmbeddedSkills_FallsBackWhenPublishedCopyIsTampered(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatalf("create tampered copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "evil.md"), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("write tampered copy: %v", err)
	}

	dir, err := materializeEmbeddedSkills(fsys, base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)
	if dir == dest {
		t.Fatalf("adopted the tampered copy %q", dest)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample", "SKILL.md")); err != nil {
		t.Fatalf("fallback copy missing its skill: %v", err)
	}
}

func TestMaterializeEmbeddedSkills_StagingFailureErrors(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")
	if _, err := materializeEmbeddedSkills(sampleSkillFS(), base); err == nil {
		t.Fatal("materializeEmbeddedSkills succeeded with an unusable base")
	}
}

// A later process starts with no in-process cache but finds the copy published
// by an earlier run under the same content digest.
func TestEmbeddedSkillsDir_ReusesPublishedCopyAfterCacheReset(t *testing.T) {
	embeddedSkillsMu.Lock()
	savedDir := embeddedSkillsDir
	embeddedSkillsDir = ""
	embeddedSkillsMu.Unlock()
	savedBase := embeddedSkillsBaseDir
	base := t.TempDir()
	embeddedSkillsBaseDir = func() string { return base }
	t.Cleanup(func() {
		embeddedSkillsMu.Lock()
		embeddedSkillsDir = savedDir
		embeddedSkillsMu.Unlock()
		embeddedSkillsBaseDir = savedBase
	})

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}

	embeddedSkillsMu.Lock()
	embeddedSkillsDir = ""
	embeddedSkillsMu.Unlock()

	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (after cache reset): %v", err)
	}
	if first != second {
		t.Fatalf("expected the published copy to be reused across cache resets, got %q then %q", first, second)
	}
	if _, err := os.Stat(filepath.Join(second, "doctoring-evener", "SKILL.md")); err != nil {
		t.Fatalf("reused copy missing the bundled skill: %v", err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one published copy, got %d", len(entries))
	}
}

type unreadableFS struct{}

func (unreadableFS) Open(string) (fs.File, error) { return nil, errors.New("unreadable") }

func TestMaterializeEmbeddedSkills_DigestFailureIsReported(t *testing.T) {
	if _, err := materializeEmbeddedSkills(unreadableFS{}, t.TempDir()); err == nil {
		t.Fatal("expected a digest failure to propagate")
	}
}

// A cached directory that has been removed (temp cleaners, a reboot) is
// republished rather than left dangling.
func TestEmbeddedSkillsDir_RepublishesWhenCachedDirDisappears(t *testing.T) {
	embeddedSkillsMu.Lock()
	savedDir := embeddedSkillsDir
	embeddedSkillsDir = filepath.Join(t.TempDir(), "gone")
	embeddedSkillsMu.Unlock()
	savedBase := embeddedSkillsBaseDir
	base := t.TempDir()
	embeddedSkillsBaseDir = func() string { return base }
	t.Cleanup(func() {
		embeddedSkillsMu.Lock()
		embeddedSkillsDir = savedDir
		embeddedSkillsMu.Unlock()
		embeddedSkillsBaseDir = savedBase
	})

	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "doctoring-evener", "SKILL.md")); err != nil {
		t.Fatalf("republished copy missing the bundled skill: %v", err)
	}
}
