package skill

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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

// embeddedSkillsCacheState reads the guarded cache for save/restore in tests.
func embeddedSkillsCacheState() (string, map[string]SkillMeta) {
	embeddedSkillsCache.mu.Lock()
	defer embeddedSkillsCache.mu.Unlock()
	return embeddedSkillsCache.dir, embeddedSkillsCache.skills
}

// pointEmbeddedSkillsAtBase sends the bundled-skills cache to base, clears the
// cached directory so the next call republishes, and restores every global it
// touched when the test ends.
func pointEmbeddedSkillsAtBase(t *testing.T, base string) {
	t.Helper()
	savedBase := embeddedSkillsBaseDir
	savedDir, savedSkills := embeddedSkillsCacheState()
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.skills = nil
	embeddedSkillsCache.mu.Unlock()
	embeddedSkillsBaseDir = func() (string, error) { return base, nil }
	t.Cleanup(func() {
		embeddedSkillsCache.mu.Lock()
		embeddedSkillsCache.dir = savedDir
		embeddedSkillsCache.skills = savedSkills
		embeddedSkillsCache.mu.Unlock()
		embeddedSkillsBaseDir = savedBase
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

// The published name lives in a shared temp dir, so an occupant this process
// did not write must be rejected before it is opened.
func TestDigestSkillsFS_RejectsIrregularEntry(t *testing.T) {
	fsys := fstest.MapFS{
		"sample/SKILL.md": &fstest.MapFile{Data: []byte(sampleSkillDocument)},
		"sample/link":     &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("/etc/passwd")},
	}
	if _, err := digestSkillsFS(fsys); err == nil {
		t.Fatal("digest accepted a non-regular entry")
	}
}

func TestDigestSkillsFS_RejectsOversizedEntry(t *testing.T) {
	fsys := fstest.MapFS{
		"sample/SKILL.md": &fstest.MapFile{Data: make([]byte, maxEmbeddedSkillBytes+1)},
	}
	if _, err := digestSkillsFS(fsys); err == nil {
		t.Fatal("digest accepted an oversized file")
	}
}

func TestDigestSkillsFS_RejectsTooManyEntries(t *testing.T) {
	files := make(map[string]string, maxEmbeddedSkillEntries+1)
	for i := 0; i <= maxEmbeddedSkillEntries; i++ {
		files[fmt.Sprintf("bundle/file-%d.md", i)] = "x"
	}
	if _, err := digestSkillsFS(skillFSFixture(files)); err == nil {
		t.Fatal("digest accepted more entries than the bound")
	}
}

func TestDigestSkillsFS_RejectsDeeplyNestedEntry(t *testing.T) {
	deep := strings.Repeat("d/", maxEmbeddedSkillDepth+1) + "SKILL.md"
	if _, err := digestSkillsFS(skillFSFixture(map[string]string{deep: "x"})); err == nil {
		t.Fatal("digest accepted a too-deeply nested entry")
	}
}

func TestDigestSkillsFS_RejectsOversizedTree(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= maxEmbeddedSkillsBytes/maxEmbeddedSkillBytes; i++ {
		files[fmt.Sprintf("bundle/file-%d.bin", i)] = strings.Repeat("x", maxEmbeddedSkillBytes)
	}
	if _, err := digestSkillsFS(skillFSFixture(files)); err == nil {
		t.Fatal("digest accepted a tree over the total-size bound")
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

// A symlinked occupant of the published name is not followed.
func TestMaterializeEmbeddedSkills_FallsBackWhenPublishedNameIsSymlink(t *testing.T) {
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	if err := os.Symlink(t.TempDir(), dest); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	dir, err := materializeEmbeddedSkills(fsys, base)
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)
	if dir == dest {
		t.Fatalf("adopted the symlinked name %q", dest)
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

type unreadableFS struct{}

func (unreadableFS) Open(string) (fs.File, error) { return nil, errors.New("unreadable") }

func TestMaterializeEmbeddedSkills_DigestFailureIsReported(t *testing.T) {
	if _, err := materializeEmbeddedSkills(unreadableFS{}, t.TempDir()); err == nil {
		t.Fatal("expected a digest failure to propagate")
	}
}

// A cached copy is only reused while it still holds the content its name
// promises; a tampered copy is not trusted.
func TestCacheDirUsable_RejectsTamperedCopy(t *testing.T) {
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	dir := filepath.Join(base, embeddedSkillsPrefix+digest)
	if err := copyEmbeddedSkills(fsys, dir); err != nil {
		t.Fatalf("copyEmbeddedSkills: %v", err)
	}
	if !cacheDirUsable(dir) {
		t.Fatal("fresh published copy reported unusable")
	}
	if err := os.WriteFile(filepath.Join(dir, "sample", "SKILL.md"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper copy: %v", err)
	}
	if cacheDirUsable(dir) {
		t.Fatal("tampered copy reported usable")
	}

	// A private fallback copy is not digest-named and has nothing to verify.
	fallback := filepath.Join(base, embeddedSkillsPrefix+"stage-test")
	if err := os.Mkdir(fallback, 0o700); err != nil {
		t.Fatalf("create fallback dir: %v", err)
	}
	if !cacheDirUsable(fallback) {
		t.Fatal("private fallback copy reported unusable")
	}
}

func TestReapStaleStaging_RemovesOnlyOldStaging(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, embeddedSkillsPrefix+"stage-old")
	fresh := filepath.Join(base, embeddedSkillsPrefix+"stage-fresh")
	published := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("a", sha256.Size*2))
	for _, dir := range []string{old, fresh, published} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	past := time.Now().Add(-2 * staleStagingMaxAge)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("age staging dir: %v", err)
	}

	reapStaleStaging(base, time.Now())

	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale staging dir not reaped: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh staging dir reaped: %v", err)
	}
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("published dir reaped: %v", err)
	}
}

func TestCloneSkillMetaMap_DeepCopiesAllowedTools(t *testing.T) {
	original := map[string]SkillMeta{"a": {Name: "a", AllowedTools: []string{"read_file"}}}
	clone := cloneSkillMetaMap(original)
	clone["a"].AllowedTools[0] = "write_file"
	if original["a"].AllowedTools[0] != "read_file" {
		t.Fatal("clone aliased AllowedTools")
	}
}

// A later process starts with no in-process cache but finds the copy published
// by an earlier run under the same content digest.
func TestEmbeddedSkillsDir_ReusesPublishedCopyAfterCacheReset(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}

	// Simulate a later process: drop the cached directory, keep the published copy.
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.mu.Unlock()

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

// A cached directory that has been removed (temp cleaners, a reboot) is
// republished rather than left dangling.
func TestEmbeddedSkillsDir_RepublishesWhenCachedDirDisappears(t *testing.T) {
	base := t.TempDir()
	savedBase := embeddedSkillsBaseDir
	savedDir, savedSkills := embeddedSkillsCacheState()
	embeddedSkillsBaseDir = func() (string, error) { return base, nil }
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = filepath.Join(t.TempDir(), "gone")
	embeddedSkillsCache.mu.Unlock()
	t.Cleanup(func() {
		embeddedSkillsCache.mu.Lock()
		embeddedSkillsCache.dir = savedDir
		embeddedSkillsCache.skills = savedSkills
		embeddedSkillsCache.mu.Unlock()
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
