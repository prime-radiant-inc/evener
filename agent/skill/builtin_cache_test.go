package skill

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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

type embeddedSkillsCacheSnapshot struct {
	dir, digest, leasedDir, fallbackBase string
	skills                               map[string]SkillMeta
	verified, fallback                   bool
	fallbackUntil                        time.Time
	lease                                skillsLease
}

func saveEmbeddedSkillsCache() embeddedSkillsCacheSnapshot {
	embeddedSkillsCache.mu.Lock()
	defer embeddedSkillsCache.mu.Unlock()
	return embeddedSkillsCacheSnapshot{
		dir:           embeddedSkillsCache.dir,
		digest:        embeddedSkillsCache.digest,
		leasedDir:     embeddedSkillsCache.leasedDir,
		fallbackBase:  embeddedSkillsCache.fallbackBase,
		fallbackUntil: embeddedSkillsCache.fallbackUntil,
		skills:        embeddedSkillsCache.skills,
		verified:      embeddedSkillsCache.verified,
		fallback:      embeddedSkillsCache.fallback,
		lease:         embeddedSkillsCache.lease,
	}
}

func restoreEmbeddedSkillsCache(s embeddedSkillsCacheSnapshot) {
	embeddedSkillsCache.mu.Lock()
	defer embeddedSkillsCache.mu.Unlock()
	if embeddedSkillsCache.lease != nil && embeddedSkillsCache.lease != s.lease {
		_ = embeddedSkillsCache.lease.Release()
	}
	if s.lease != nil && !s.lease.Valid() {
		// A lease that was released while the test ran must not be reinstalled:
		// its file is closed and calling Release again could close a reused fd.
		s.lease = nil
		s.leasedDir = ""
	}
	embeddedSkillsCache.dir = s.dir
	embeddedSkillsCache.digest = s.digest
	embeddedSkillsCache.skills = s.skills
	embeddedSkillsCache.verified = s.verified
	embeddedSkillsCache.fallback = s.fallback
	embeddedSkillsCache.fallbackBase = s.fallbackBase
	embeddedSkillsCache.fallbackUntil = s.fallbackUntil
	embeddedSkillsCache.lease = s.lease
	embeddedSkillsCache.leasedDir = s.leasedDir
}

// pointEmbeddedSkillsAtBase sends the bundled-skills cache to base, clears the
// cached copy so the next call republishes, and restores every global it touched
// when the test ends.
func pointEmbeddedSkillsAtBase(t *testing.T, base string) {
	t.Helper()
	savedBase := embeddedSkillsBaseDir
	saved := saveEmbeddedSkillsCache()
	embeddedSkillsCache.mu.Lock()
	if embeddedSkillsCache.lease != nil {
		_ = embeddedSkillsCache.lease.Release()
	}
	// The released lease is the one the snapshot captured, so it must not be
	// reinstated by the restore.
	saved.lease = nil
	saved.leasedDir = ""
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.digest = ""
	embeddedSkillsCache.skills = nil
	embeddedSkillsCache.verified = false
	embeddedSkillsCache.fallback = false
	embeddedSkillsCache.fallbackBase = ""
	embeddedSkillsCache.lease = nil
	embeddedSkillsCache.leasedDir = ""
	embeddedSkillsCache.mu.Unlock()
	embeddedSkillsBaseDir = func() (string, error) { return base, nil }
	t.Cleanup(func() {
		restoreEmbeddedSkillsCache(saved)
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

// growingFS reports a file whose declared size is smaller than the bytes its
// Open yields, so the digest's growth detection can be exercised.
type growingFS struct{ inner fs.FS }

func (g growingFS) Open(name string) (fs.File, error) {
	f, err := g.inner.Open(name)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err == nil && !info.IsDir() {
		return &growingFile{File: f}, nil
	}
	return f, nil
}

func (g growingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(g.inner, name)
}

// growingFile yields one byte past the underlying file's declared size.
type growingFile struct {
	fs.File
	extra bool
}

func (f *growingFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	if n > 0 || !errors.Is(err, io.EOF) || f.extra {
		return n, err
	}
	f.extra = true
	if len(p) == 0 {
		return 0, io.EOF
	}
	p[0] = 'x'
	return 1, nil
}

// A file that grows after its size is read must not have its original prefix
// hashed and compare equal.
func TestDigestSkillsFS_RejectsFileThatGrewAfterStat(t *testing.T) {
	fsys := growingFS{inner: skillFSFixture(map[string]string{"SKILL.md": "body"})}
	if _, err := digestSkillsFS(fsys); err == nil {
		t.Fatal("digest accepted a file that grew after its size was read")
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

	first, gotDigest, err := materializeEmbeddedSkills(fsys, base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	if gotDigest != digest {
		t.Fatalf("materializeEmbeddedSkills digest = %q, want %q", gotDigest, digest)
	}
	want := filepath.Join(base, embeddedSkillsPrefix+digest)
	if first != want {
		t.Fatalf("published dir = %q, want %q", first, want)
	}
	if _, err := os.Stat(filepath.Join(first, "sample", "SKILL.md")); err != nil {
		t.Fatalf("published copy missing its skill: %v", err)
	}

	second, _, err := materializeEmbeddedSkills(fsys, base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills (second): %v", err)
	}
	if second != first {
		t.Fatalf("expected the published copy to be reused, got %q then %q", first, second)
	}

	// The lease machinery keeps a sibling .locks directory, so count only the
	// published copies rather than every entry in the base.
	published := 0
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), embeddedSkillsPrefix) {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("expected exactly one published copy, got %d in %v", published, entries)
	}
}

func TestMaterializeEmbeddedSkills_DistinctContentDistinctDirs(t *testing.T) {
	base := t.TempDir()
	first, _, err := materializeEmbeddedSkills(sampleSkillFS(), base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	other := skillFSFixture(map[string]string{"sample/SKILL.md": sampleSkillDocument + "other\n"})
	second, _, err := materializeEmbeddedSkills(other, base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills (other): %v", err)
	}
	if first == second {
		t.Fatalf("distinct content shared the directory %q", first)
	}
}

// A file occupying the published name is healed: it is not a directory, so it is
// removed and the name published into, rather than falling back forever.
func TestMaterializeEmbeddedSkills_HealsOccupiedPublishedName(t *testing.T) {
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

	dir, _, err := materializeEmbeddedSkills(fsys, base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	if dir != dest {
		t.Fatalf("expected the occupied name to be healed, got %q want %q", dir, dest)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample", "SKILL.md")); err != nil {
		t.Fatalf("published copy missing its skill: %v", err)
	}
}

// A directory occupying the published name with the wrong content can never be
// adopted, so it is removed under the exclusive lease and the name republished
// into rather than falling back forever.
func TestMaterializeEmbeddedSkills_HealsTamperedPublishedDirectory(t *testing.T) {
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

	dir, _, err := materializeEmbeddedSkills(fsys, base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	if dir != dest {
		t.Fatalf("expected the tampered directory to be healed, got %q want %q", dir, dest)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample", "SKILL.md")); err != nil {
		t.Fatalf("published copy missing its skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "evil.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("tampered content survived the republish: %v", err)
	}
}

// A symlinked occupant of the published name is removed with the link, never
// followed, and the name is published into.
func TestMaterializeEmbeddedSkills_HealsSymlinkedPublishedName(t *testing.T) {
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	target := t.TempDir()
	if err := os.Symlink(target, dest); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	dir, _, err := materializeEmbeddedSkills(fsys, base, "")
	if err != nil {
		t.Fatalf("materializeEmbeddedSkills: %v", err)
	}
	if dir != dest {
		t.Fatalf("expected the symlinked name to be healed, got %q want %q", dir, dest)
	}
	info, err := os.Lstat(dest)
	if err != nil || !info.IsDir() {
		t.Fatalf("published name is not a real directory: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sample", "SKILL.md")); err != nil {
		t.Fatalf("published copy missing its skill: %v", err)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) != 0 {
		t.Fatalf("symlink target was written through: %v", entries)
	}
}

func TestMaterializeEmbeddedSkills_StagingFailureErrors(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")
	if _, _, err := materializeEmbeddedSkills(sampleSkillFS(), base, ""); err == nil {
		t.Fatal("materializeEmbeddedSkills succeeded with an unusable base")
	}
}

type unreadableFS struct{}

func (unreadableFS) Open(string) (fs.File, error) { return nil, errors.New("unreadable") }

func TestMaterializeEmbeddedSkills_DigestFailureIsReported(t *testing.T) {
	if _, _, err := materializeEmbeddedSkills(unreadableFS{}, t.TempDir(), ""); err == nil {
		t.Fatal("expected a digest failure to propagate")
	}
}

// A cached copy is only reused while it still holds the content its digest
// promises; a tampered copy is not trusted, whatever it is named.
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
	if !cacheDirUsable(dir, digest) {
		t.Fatal("fresh published copy reported unusable")
	}

	retained := filepath.Join(base, retainedSkillsPrefix+"abc")
	if err := copyEmbeddedSkills(fsys, retained); err != nil {
		t.Fatalf("copyEmbeddedSkills (retained): %v", err)
	}
	if !cacheDirUsable(retained, digest) {
		t.Fatal("retained copy reported unusable")
	}

	if err := os.WriteFile(filepath.Join(dir, "sample", "SKILL.md"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper copy: %v", err)
	}
	if cacheDirUsable(dir, digest) {
		t.Fatal("tampered copy reported usable")
	}
	if cacheDirUsable(dir, "") {
		t.Fatal("copy with no expected digest reported usable")
	}
}

// Aging by the retained threshold also exceeds the shorter staging threshold.
func TestReapStaleCopies_RemovesAbandonedAndSuperseded(t *testing.T) {
	base := t.TempDir()
	oldStage := filepath.Join(base, embeddedSkillsPrefix+"stage-old")
	oldCopy := filepath.Join(base, retainedSkillsPrefix+"old")
	current := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("a", sha256.Size*2))
	superseded := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("b", sha256.Size*2))
	fresh := filepath.Join(base, embeddedSkillsPrefix+"stage-fresh")
	// A file squatting a cache name is healed, not skipped forever.
	squatter := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("c", sha256.Size*2))
	for _, dir := range []string{oldStage, oldCopy, current, superseded, fresh} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(squatter, []byte("squatting"), 0o600); err != nil {
		t.Fatalf("create squatter: %v", err)
	}
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	for _, path := range []string{oldStage, oldCopy, superseded, squatter} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatalf("age %s: %v", path, err)
		}
	}

	// current is this process's own copy and must survive even when it is also
	// the digest about to be published.
	reapStaleCopies(base, time.Now(), strings.Repeat("a", sha256.Size*2), current)

	for _, gone := range []string{oldStage, oldCopy, superseded, squatter} {
		if _, err := os.Stat(gone); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s not reaped: %v", gone, err)
		}
	}
	for _, keep := range []string{current, fresh} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s was reaped: %v", keep, err)
		}
	}
}

// Moving to a different copy must release the lease on the old one, not leave it
// held while the new copy goes unprotected.
func TestEmbeddedSkillsDir_MovesTheLeaseToTheNewCopy(t *testing.T) {
	firstBase := t.TempDir()
	pointEmbeddedSkillsAtBase(t, firstBase)
	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	embeddedSkillsCache.mu.Lock()
	firstLeased := embeddedSkillsCache.leasedDir
	embeddedSkillsCache.mu.Unlock()
	if firstLeased != first {
		t.Fatalf("leased dir = %q, want %q", firstLeased, first)
	}

	secondBase := t.TempDir()
	pointEmbeddedSkillsAtBase(t, secondBase)
	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (second base): %v", err)
	}
	if second == first {
		t.Fatalf("expected a different copy in the second base, got %q", second)
	}
	embeddedSkillsCache.mu.Lock()
	secondLeased := embeddedSkillsCache.leasedDir
	embeddedSkillsCache.mu.Unlock()
	if secondLeased != second {
		t.Fatalf("leased dir = %q, want the new copy %q", secondLeased, second)
	}

	// The old copy must now be reapable: its lease was released.
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	if err := os.Chtimes(first, past, past); err != nil {
		t.Fatalf("age old copy: %v", err)
	}
	reapStaleCopies(firstBase, time.Now(), "", "")
	if _, err := os.Stat(first); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old copy is still leased after moving: %v", err)
	}
}

// A copy that cannot be leased must not be handed out: the reaper could delete it
// mid-session. The stub blocks every lease, the private fallback's included, so
// resolution has nothing protected to return.
func TestEmbeddedSkillsDir_FailsWhenNoLeaseIsAvailable(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)
	saved := acquireSkillsLease
	acquireSkillsLease = func(string, bool) (skillsLease, bool, error) { return nil, true, nil }
	t.Cleanup(func() { acquireSkillsLease = saved })

	if _, err := EmbeddedSkillsDir(); err == nil {
		t.Fatal("EmbeddedSkillsDir returned a copy that could not be leased")
	}
}

// Forgetting a fallback copy must leave its files in place: sessions from the
// fallback window still read skill files by path, and the age-based reaper is
// what eventually collects the base.
func TestForgetEmbeddedSkillsLocked_KeepsFallbackCopyReadable(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "copy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create copy: %v", err)
	}
	saved := saveEmbeddedSkillsCache()
	t.Cleanup(func() { restoreEmbeddedSkillsCache(saved) })
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = dir
	embeddedSkillsCache.fallback = true
	embeddedSkillsCache.fallbackBase = base
	embeddedSkillsCache.mu.Unlock()

	forgetEmbeddedSkillsLocked()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fallback copy removed while readers may hold its paths: %v", err)
	}
	embeddedSkillsCache.mu.Lock()
	fallbackBase := embeddedSkillsCache.fallbackBase
	embeddedSkillsCache.mu.Unlock()
	if fallbackBase != "" {
		t.Fatalf("fallbackBase not cleared on forget: %q", fallbackBase)
	}
}

func TestPruneObsoleteLocks_RemovesOnlyOrphans(t *testing.T) {
	base := t.TempDir()
	locks := filepath.Join(base, skillsLockDirName)
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatalf("create lock dir: %v", err)
	}
	orphan := filepath.Join(locks, "orphan.lock")
	live := filepath.Join(locks, "live.lock")
	fresh := filepath.Join(locks, "fresh.lock")
	for _, path := range []string{orphan, live, fresh} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}
	if err := os.Mkdir(filepath.Join(base, "live"), 0o700); err != nil {
		t.Fatalf("create live dir: %v", err)
	}
	past := time.Now().Add(-2 * staleStagingMaxAge)
	for _, path := range []string{orphan, live} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatalf("age %s: %v", path, err)
		}
	}

	pruneObsoleteLocks(base, time.Now())

	if _, err := os.Stat(orphan); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan lock not pruned: %v", err)
	}
	for _, keep := range []string{live, fresh} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s was pruned: %v", keep, err)
		}
	}
}

// The private fallback copy must keep the lease it took, so another process's
// fallback reaper can tell the base is in use.
func TestEmbeddedSkillsDir_FallbackKeepsItsLease(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)
	saved := acquireSkillsLease
	acquireSkillsLease = func(path string, exclusive bool) (skillsLease, bool, error) {
		if strings.HasPrefix(path, filepath.Join(base, skillsLockDirName)) {
			return nil, true, nil
		}
		return saved(path, exclusive)
	}
	t.Cleanup(func() { acquireSkillsLease = saved })

	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	fallbackBase := filepath.Dir(dir)
	t.Cleanup(func() { _ = os.RemoveAll(fallbackBase) })

	embeddedSkillsCache.mu.Lock()
	lease := embeddedSkillsCache.lease
	leasedDir := embeddedSkillsCache.leasedDir
	embeddedSkillsCache.mu.Unlock()
	if lease == nil || leasedDir != dir {
		t.Fatalf("fallback copy has no lease: lease=%v leasedDir=%q want %q", lease, leasedDir, dir)
	}
	if !lease.Valid() {
		t.Fatal("fallback lease is not valid")
	}

	// Disable the reaper's own-base skips so only the lease can save the base.
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	if err := os.Chtimes(fallbackBase, past, past); err != nil {
		t.Fatalf("age fallback base: %v", err)
	}
	reapStaleFallbackBases(os.TempDir(), time.Now(), "", "")
	if _, err := os.Stat(fallbackBase); err != nil {
		t.Fatalf("leased fallback base was reaped: %v", err)
	}
}

// A valid copy of the digest being published must survive the reaper's pass.
func TestReapStaleCopies_KeepsValidDigestCopy(t *testing.T) {
	base := t.TempDir()
	fsys := sampleSkillFS()
	digest, err := digestSkillsFS(fsys)
	if err != nil {
		t.Fatalf("digestSkillsFS: %v", err)
	}
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	if err := copyEmbeddedSkills(fsys, dest); err != nil {
		t.Fatalf("copyEmbeddedSkills: %v", err)
	}

	reapStaleCopies(base, time.Now(), digest, "")

	if !cacheDirUsable(dest, digest) {
		t.Fatal("valid published copy was reaped")
	}
}

// A copy a live process holds a shared lease on is never reaped, however old it
// is; once the lease is dropped it becomes reapable.
func TestReapStaleCopies_SkipsLeasedDirectory(t *testing.T) {
	base := t.TempDir()
	name := embeddedSkillsPrefix + strings.Repeat("a", sha256.Size*2)
	dir := filepath.Join(base, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatalf("age cache dir: %v", err)
	}
	lockPath, err := skillsLockPath(base, name, true)
	if err != nil {
		t.Fatalf("skillsLockPath: %v", err)
	}
	lease, contended, err := acquireSkillsLease(lockPath, false)
	if err != nil || contended {
		t.Fatalf("acquire shared lease: %v (contended=%v)", err, contended)
	}

	reapStaleCopies(base, time.Now(), "", "")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("leased directory was reaped: %v", err)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	reapStaleCopies(base, time.Now(), "", "")
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unleased directory was not reaped: %v", err)
	}
}

// The copy this process resolved through the API is protected while it lives.
func TestReapStaleCopies_KeepsTheCopyInUse(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)
	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatalf("age resolved copy: %v", err)
	}

	reapStaleCopies(base, time.Now(), "", "")

	if _, err := os.Stat(filepath.Join(dir, "doctoring-evener", "SKILL.md")); err != nil {
		t.Fatalf("in-use copy was reaped: %v", err)
	}
}

func TestReapStaleCopies_HealsSquatterOfCurrentDigest(t *testing.T) {
	base := t.TempDir()
	digest := strings.Repeat("a", sha256.Size*2)
	path := filepath.Join(base, embeddedSkillsPrefix+digest)
	if err := os.WriteFile(path, []byte("squatting"), 0o600); err != nil {
		t.Fatalf("create squatter: %v", err)
	}

	reapStaleCopies(base, time.Now(), digest, "")

	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("squatter of the current digest survived: %v", err)
	}
}

func TestReapStaleFallbackBases_SkipsBaseWithLeasedCopy(t *testing.T) {
	tmp := t.TempDir()
	base := filepath.Join(tmp, embeddedSkillsPrefix+processOwnerTag()+"-live")
	name := embeddedSkillsPrefix + strings.Repeat("b", sha256.Size*2)
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create fallback base: %v", err)
	}
	lockPath, err := skillsLockPath(base, name, true)
	if err != nil {
		t.Fatalf("skillsLockPath: %v", err)
	}
	lease, contended, err := acquireSkillsLease(lockPath, false)
	if err != nil || contended {
		t.Fatalf("acquire shared lease: %v (contended=%v)", err, contended)
	}
	// Age the base only after the lock directory exists: creating .locks
	// refreshes its mtime, and the age check would otherwise skip the base
	// before the lease is ever consulted.
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	if err := os.Chtimes(base, past, past); err != nil {
		t.Fatalf("age fallback base: %v", err)
	}

	reapStaleFallbackBases(tmp, time.Now(), "", "")
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("in-use fallback base was reaped: %v", err)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	reapStaleFallbackBases(tmp, time.Now(), "", "")
	if _, err := os.Stat(base); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unleased fallback base was not reaped: %v", err)
	}
}

func TestReapStaleFallbackBases_RemovesOnlyThisUsersStaleBases(t *testing.T) {
	tmp := t.TempDir()
	prefix := embeddedSkillsPrefix + processOwnerTag() + "-"
	old := filepath.Join(tmp, prefix+"old")
	fresh := filepath.Join(tmp, prefix+"fresh")
	foreign := filepath.Join(tmp, embeddedSkillsPrefix+"someone-else")
	for _, dir := range []string{old, fresh, foreign} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("age base: %v", err)
	}

	reapStaleFallbackBases(tmp, time.Now(), "", "")

	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale fallback base not reaped: %v", err)
	}
	for _, keep := range []string{fresh, foreign} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s was reaped: %v", keep, err)
		}
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

func TestCloneSkillMetaMap_DeepCopiesMetadata(t *testing.T) {
	original := map[string]SkillMeta{"a": {Name: "a", Metadata: map[string]any{"k": []any{"v"}}}}
	clone := cloneSkillMetaMap(original)
	clone["a"].Metadata["k"].([]any)[0] = "mutated"
	if original["a"].Metadata["k"].([]any)[0] != "v" {
		t.Fatal("clone aliased Metadata")
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

	// Simulate a later process: drop the cached copy, keep the published one.
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.digest = ""
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
	// The lease machinery keeps a sibling .locks directory, so count only the
	// published copies rather than every entry in the base.
	published := 0
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), embeddedSkillsPrefix) {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("expected exactly one published copy, got %d in %v", published, entries)
	}
}

// A cached directory that has been removed (temp cleaners, a reboot) is
// republished rather than left dangling.
func TestEmbeddedSkillsDir_RepublishesWhenCachedDirDisappears(t *testing.T) {
	base := t.TempDir()
	savedBase := embeddedSkillsBaseDir
	saved := saveEmbeddedSkillsCache()
	embeddedSkillsBaseDir = func() (string, error) { return base, nil }
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = filepath.Join(t.TempDir(), "gone")
	embeddedSkillsCache.verified = false
	embeddedSkillsCache.mu.Unlock()
	t.Cleanup(func() {
		restoreEmbeddedSkillsCache(saved)
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

// Once this process has resolved and verified a copy, later calls trust it
// instead of re-walking an immutable directory under a private base.
func TestEmbeddedSkillsDir_TrustsVerifiedCopyWithinProcess(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first, "doctoring-evener", "SKILL.md"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper copy: %v", err)
	}
	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (second): %v", err)
	}
	if second != first {
		t.Fatalf("verified copy was not trusted within the process: %q then %q", first, second)
	}
}

// A verified copy that a temp cleaner removes mid-process must be republished,
// not returned as a dangling path.
func TestEmbeddedSkillsDir_RepublishesWhenVerifiedCopyDisappears(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatalf("remove verified copy: %v", err)
	}
	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (after removal): %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, "doctoring-evener", "SKILL.md")); err != nil {
		t.Fatalf("republished copy missing the bundled skill: %v", err)
	}
}
