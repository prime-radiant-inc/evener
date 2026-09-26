package skill

import (
	"bytes"
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
	dir, digest, leasedDir string
	skills                 map[string]SkillMeta
	verified               bool
	lease                  skillsLease
	dirIdentity            fs.FileInfo
	failedErr              error
	failedAt               time.Time
}

func saveEmbeddedSkillsCache() embeddedSkillsCacheSnapshot {
	embeddedSkillsCache.mu.Lock()
	defer embeddedSkillsCache.mu.Unlock()
	return embeddedSkillsCacheSnapshot{
		dir:         embeddedSkillsCache.dir,
		digest:      embeddedSkillsCache.digest,
		leasedDir:   embeddedSkillsCache.leasedDir,
		skills:      embeddedSkillsCache.skills,
		verified:    embeddedSkillsCache.verified,
		lease:       embeddedSkillsCache.lease,
		dirIdentity: embeddedSkillsCache.dirIdentity,
		failedErr:   embeddedSkillsCache.failedErr,
		failedAt:    embeddedSkillsCache.failedAt,
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
	embeddedSkillsCache.lease = s.lease
	embeddedSkillsCache.leasedDir = s.leasedDir
	embeddedSkillsCache.dirIdentity = s.dirIdentity
	embeddedSkillsCache.failedErr = s.failedErr
	embeddedSkillsCache.failedAt = s.failedAt
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
	embeddedSkillsCache.lease = nil
	embeddedSkillsCache.leasedDir = ""
	embeddedSkillsCache.failedErr = nil
	embeddedSkillsCache.failedAt = time.Time{}
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
	current := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("a", sha256.Size*2))
	superseded := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("b", sha256.Size*2))
	fresh := filepath.Join(base, embeddedSkillsPrefix+"stage-fresh")
	// A file squatting a cache name is healed, not skipped forever.
	squatter := filepath.Join(base, embeddedSkillsPrefix+strings.Repeat("c", sha256.Size*2))
	for _, dir := range []string{oldStage, current, superseded, fresh} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(squatter, []byte("squatting"), 0o600); err != nil {
		t.Fatalf("create squatter: %v", err)
	}
	past := time.Now().Add(-2 * staleRetainedMaxAge)
	for _, path := range []string{oldStage, superseded, squatter} {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatalf("age %s: %v", path, err)
		}
	}

	// current is this process's own copy and must survive even when it is also
	// the digest about to be published.
	reapStaleCopies(base, time.Now(), strings.Repeat("a", sha256.Size*2), current)

	for _, gone := range []string{oldStage, superseded, squatter} {
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

// A copy replaced at its path must not be served on the strength of the lease
// held for the copy it replaced.
func TestEmbeddedSkillsDir_RejectsAReplacedCopy(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)
	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatalf("move copy aside: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("replace copy: %v", err)
	}

	again, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (replaced): %v", err)
	}
	// The replacement is rejected, so resolution rebuilds or republishes a copy
	// with real content; a served replacement would be empty.
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(again, skills)
	if len(skills) == 0 {
		t.Fatalf("replacement resolution returned no skills from %q", again)
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
	firstLeased, firstLease := embeddedSkillsCache.leasedDir, embeddedSkillsCache.lease
	embeddedSkillsCache.mu.Unlock()
	if firstLeased != first || firstLease == nil {
		t.Fatalf("leased dir = %q, lease = %v, want %q", firstLeased, firstLease, first)
	}

	// Switch bases without dropping the lease held for the first copy: moving
	// copies is what the production path has to handle.
	secondBase := t.TempDir()
	embeddedSkillsBaseDir = func() (string, error) { return secondBase, nil }
	embeddedSkillsCache.mu.Lock()
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.digest = ""
	embeddedSkillsCache.skills = nil
	embeddedSkillsCache.verified = false
	embeddedSkillsCache.mu.Unlock()

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
	if firstLease.Valid() {
		t.Fatal("moving copies left the old copy's lease held")
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

// A platform that cannot name a verified per-user cache root must fail rather
// than create the base under a shared temp root it cannot verify.
func TestDefaultEmbeddedSkillsBaseDir_FailsClosedWithoutARoot(t *testing.T) {
	saved := skillsBaseRoot
	skillsBaseRoot = func() (string, error) { return "", errors.New("no user cache directory") }
	t.Cleanup(func() { skillsBaseRoot = saved })

	if _, err := defaultEmbeddedSkillsBaseDir(); err == nil {
		t.Fatal("defaultEmbeddedSkillsBaseDir used an unverified root")
	}

	// A root the platform reports as empty must not become a relative path under
	// whatever the current directory happens to be.
	skillsBaseRoot = func() (string, error) { return "", nil }
	if _, err := defaultEmbeddedSkillsBaseDir(); err == nil {
		t.Fatal("defaultEmbeddedSkillsBaseDir joined an empty root")
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

// processCopyRootRefused reports whether the platform refuses root as the
// process-lifetime extraction root. It runs the same gate the degraded path runs
// on its temp root — ensureTrustedProcessRoot, which consults
// processCopyRootTrusted — so the skip travels with the platform's own decision
// instead of a hard-coded runtime.GOOS.
func processCopyRootRefused(root string) bool {
	_, err := resolveTrustedRoot(root, ensureTrustedProcessRoot)
	return err != nil
}

// skipIfProcessCopyRefused skips a test whose expectations require the degraded
// process-lifetime copy where this platform refuses its temp root: the copy
// fails closed there, so the test would fail for a platform decision rather than
// a defect.
func skipIfProcessCopyRefused(t *testing.T) {
	t.Helper()
	if processCopyRootRefused(os.TempDir()) {
		t.Skip("process-lifetime copy is refused on this platform")
	}
}

// The degraded-copy guard must track the platform's process-root gate, not a
// hard-coded GOOS, so the tests that need the copy skip exactly where the
// platform refuses the root.
func TestProcessCopyRootRefused_TracksThePlatformDecision(t *testing.T) {
	// A world-writable, non-sticky root is the root Unix rejects and Windows
	// refuses unconditionally; the platforms whose predicate accepts every root
	// (js, wasip1, plan9) accept it too. A root under the inherited temp chain is
	// checked for acceptance whatever that chain is.
	untrusted := filepath.Join(t.TempDir(), "untrusted")
	if err := os.Mkdir(untrusted, 0o700); err != nil {
		t.Fatalf("create untrusted root: %v", err)
	}
	if err := os.Chmod(untrusted, 0o777); err != nil {
		t.Fatalf("open untrusted root: %v", err)
	}
	assertRefusedMatchesGate(t, "untrusted", untrusted)

	assertRefusedMatchesGate(t, "trusted", t.TempDir())
}

// assertRefusedMatchesGate checks that processCopyRootRefused reports exactly
// what its gate decides for root's resolved directory: refused when the
// platform's process-root predicate rejects the final component, or when an
// ancestor can be replaced. The expected value is computed from those components,
// never from the guard itself, and it includes the ancestor check so the
// assertion holds whatever the inherited temp chain looks like.
func assertRefusedMatchesGate(t *testing.T, label, root string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("%s: resolve root: %v", label, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		t.Fatalf("%s: stat root: %v", label, err)
	}
	wantRefused := !processCopyRootTrusted(info) || ensureTrustedAncestors(resolved) != nil
	if got := processCopyRootRefused(root); got != wantRefused {
		t.Fatalf("%s: processCopyRootRefused = %v, want %v (processCopyRootTrusted = %v, ancestors trusted = %v)",
			label, got, wantRefused, processCopyRootTrusted(info), ensureTrustedAncestors(resolved) == nil)
	}
}

// The shared content-addressed cache is best-effort: when it cannot be resolved,
// the process keeps one private extraction of the bundled skills for the rest of
// its lifetime instead of failing resolution.
func TestEmbeddedSkillsDir_FallsBackToAProcessLifetimeCopy(t *testing.T) {
	skipIfProcessCopyRefused(t)
	// A base that cannot be staged into (it does not exist) fails
	// materialization, so there is nothing to publish or adopt.
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (unusable base): %v", err)
	}
	if !strings.HasPrefix(filepath.Base(first), embeddedSkillsPrefix+"process-") {
		t.Fatalf("degraded copy = %q, want a process-lifetime extraction", first)
	}
	// Nothing reaps or removes this copy, so the test leaves it in place: the
	// rest of the process keeps reading the paths inside it.
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(first, skills)
	if len(skills) == 0 {
		t.Fatalf("degraded copy %q holds no discoverable skills", first)
	}

	// Every later call returns the same directory, including once the shared
	// base cannot even be named.
	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (again): %v", err)
	}
	embeddedSkillsBaseDir = func() (string, error) { return "", errors.New("no usable base") }
	third, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (unnameable base): %v", err)
	}
	if second != first || third != first {
		t.Fatalf("process copy changed between calls: %q, %q, %q", first, second, third)
	}

	// EmbeddedSkills resolves the same copy's filesystem-backed metadata.
	meta, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills: %v", err)
	}
	if len(meta) == 0 {
		t.Fatal("EmbeddedSkills returned no skills from the process copy")
	}
	prefix := first + string(filepath.Separator)
	var name string
	for key, m := range meta {
		name = key
		if !strings.HasPrefix(m.SkillFile, prefix) {
			t.Fatalf("embedded skill %q resolves outside the process copy: %q", name, m.SkillFile)
		}
		if _, err := LoadSkillBody(m); err != nil {
			t.Fatalf("LoadSkillBody(%q): %v", name, err)
		}
	}

	// The tree is scanned once and every caller gets its own copy: a later call
	// still reports the skills a caller mutated out of its own result.
	delete(meta, name)
	againMeta, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills (again): %v", err)
	}
	if len(againMeta) != len(skills) {
		t.Fatalf("EmbeddedSkills (again) = %d entries, want the %d in %q", len(againMeta), len(skills), first)
	}
	if _, ok := againMeta[name]; !ok {
		t.Fatalf("EmbeddedSkills handed back a cached map alias: %q missing", name)
	}
}

// resetProcessSkills drops the process-lifetime extraction a test triggered, so
// the next test resolves it afresh. The directory is removed with it.
func resetProcessSkills() {
	processSkillsMu.Lock()
	dir := processSkillsDir
	processSkillsDir = ""
	processSkillsMeta = nil
	processSkillsMu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// A temp cleaner can remove the process copy while the process lives; the next
// resolution must extract a fresh one rather than hand back a dangling path.
func TestEmbeddedSkillsDir_ReExtractsWhenTheProcessCopyDisappears(t *testing.T) {
	skipIfProcessCopyRefused(t)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatalf("remove process copy: %v", err)
	}

	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (after removal): %v", err)
	}
	if second == first {
		t.Fatal("returned the removed process copy")
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(second, skills)
	if len(skills) == 0 {
		t.Fatalf("re-extracted copy %q holds no skills", second)
	}

	meta, err := EmbeddedSkills()
	if err != nil {
		t.Fatalf("EmbeddedSkills: %v", err)
	}
	if len(meta) != len(skills) {
		t.Fatalf("metadata after re-extraction has %d skills, want %d", len(meta), len(skills))
	}
}

// A cleaner that removes files but leaves the process directory must not leave
// an incomplete tree in place.
func TestEmbeddedSkillsDir_ReExtractsWhenTheProcessCopyIsIncomplete(t *testing.T) {
	skipIfProcessCopyRefused(t)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	entries, err := os.ReadDir(first)
	if err != nil {
		t.Fatalf("read process copy: %v", err)
	}
	var removed string
	for _, entry := range entries {
		if entry.IsDir() {
			removed = entry.Name()
			break
		}
	}
	if removed == "" {
		t.Fatalf("process copy %q has no skill directories", first)
	}
	if err := os.RemoveAll(filepath.Join(first, removed)); err != nil {
		t.Fatalf("remove skill %q: %v", removed, err)
	}

	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (incomplete copy): %v", err)
	}
	if second == first {
		t.Fatal("reused an incomplete process copy")
	}
	if _, err := os.Stat(filepath.Join(second, removed)); err != nil {
		t.Fatalf("re-extracted copy %q is missing %q: %v", second, removed, err)
	}
}

// A copy whose files a cleaner removed must not be served just because its
// directory is still the one this process validated.
func TestEmbeddedSkillsDir_RepublishesAGuttedCopy(t *testing.T) {
	base := t.TempDir()
	pointEmbeddedSkillsAtBase(t, base)
	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	var gutted string
	for _, entry := range entries {
		if entry.IsDir() {
			gutted = entry.Name()
			break
		}
	}
	if gutted == "" {
		t.Fatalf("copy %q has no skill directories", dir)
	}
	if err := os.Remove(filepath.Join(dir, gutted, "SKILL.md")); err != nil {
		t.Fatalf("remove %s/SKILL.md: %v", gutted, err)
	}

	again, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (gutted): %v", err)
	}
	if _, err := os.Stat(filepath.Join(again, gutted, "SKILL.md")); err != nil {
		t.Fatalf("served a gutted copy %q: %v", again, err)
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(again, skills)
	if !cacheDirComplete(skills) {
		t.Fatalf("copy %q is still missing skill files after republishing", again)
	}
}

// In a degraded environment the publish path fails on every call, but it must
// not be re-run on every call: a failed shared resolution is remembered so
// later calls go straight to the process-lifetime copy, and a call after the
// remembered failure lapses recovers onto the shared copy once the base is
// usable again.
func TestEmbeddedSkillsDir_DoesNotRepeatAFailedPublishPath(t *testing.T) {
	skipIfProcessCopyRefused(t)
	missingBase := filepath.Join(t.TempDir(), "missing-base")
	pointEmbeddedSkillsAtBase(t, missingBase)
	t.Cleanup(resetProcessSkills)

	// Count how often the publish path resolves a base. A remembered failure
	// must skip base selection and everything after it.
	resolutions := 0
	embeddedSkillsBaseDir = func() (string, error) {
		resolutions++
		return missingBase, nil
	}

	savedInterval := embeddedSkillsRetryInterval
	embeddedSkillsRetryInterval = time.Hour
	t.Cleanup(func() { embeddedSkillsRetryInterval = savedInterval })

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (degraded): %v", err)
	}
	if !strings.HasPrefix(filepath.Base(first), embeddedSkillsPrefix+"process-") {
		t.Fatalf("degraded copy = %q, want a process-lifetime extraction", first)
	}
	if resolutions != 1 {
		t.Fatalf("first resolution attempted the publish path %d times, want 1", resolutions)
	}
	for i := range 5 {
		again, err := EmbeddedSkillsDir()
		if err != nil {
			t.Fatalf("EmbeddedSkillsDir (degraded, %d): %v", i, err)
		}
		if again != first {
			t.Fatalf("degraded copy changed between calls: %q, then %q", first, again)
		}
	}
	if _, err := EmbeddedSkills(); err != nil {
		t.Fatalf("EmbeddedSkills (degraded): %v", err)
	}
	if resolutions != 1 {
		t.Fatalf("repeated degraded calls resolved the base %d times, want 1", resolutions)
	}

	// Once the remembered failure lapses and the base is usable, resolution
	// recovers onto the shared copy.
	if err := os.MkdirAll(missingBase, 0o700); err != nil {
		t.Fatalf("make base usable: %v", err)
	}
	embeddedSkillsRetryInterval = 0
	recovered, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (recovered): %v", err)
	}
	if recovered == first {
		t.Fatalf("resolution did not recover onto the shared copy: still %q", recovered)
	}
	if got := filepath.Dir(recovered); got != missingBase {
		t.Fatalf("recovered copy %q is not under the base %q", recovered, missingBase)
	}
	if resolutions != 2 {
		t.Fatalf("recovery resolved the base %d times, want 2", resolutions)
	}
}

// A base resolver that cannot name a usable root is a failed resolution too:
// repeated calls must go straight to the process copy, and a later call retries
// the resolver once the interval lapses.
func TestEmbeddedSkillsDir_DoesNotRepeatAnUnnameableBase(t *testing.T) {
	skipIfProcessCopyRefused(t)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	resolutions := 0
	embeddedSkillsBaseDir = func() (string, error) {
		resolutions++
		return "", errors.New("no usable base")
	}
	savedInterval := embeddedSkillsRetryInterval
	embeddedSkillsRetryInterval = time.Hour
	t.Cleanup(func() { embeddedSkillsRetryInterval = savedInterval })

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (unnameable base): %v", err)
	}
	if resolutions != 1 {
		t.Fatalf("first resolution ran the base resolver %d times, want 1", resolutions)
	}
	for i := range 5 {
		again, err := EmbeddedSkillsDir()
		if err != nil {
			t.Fatalf("EmbeddedSkillsDir (unnameable base, %d): %v", i, err)
		}
		if again != first {
			t.Fatalf("process copy changed between calls: %q, then %q", first, again)
		}
	}
	if resolutions != 1 {
		t.Fatalf("repeated calls ran the base resolver %d times, want 1", resolutions)
	}

	// After the interval lapses the resolver runs again, and a usable base is
	// published into.
	embeddedSkillsRetryInterval = 0
	base := t.TempDir()
	embeddedSkillsBaseDir = func() (string, error) { return base, nil }
	recovered, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (recovered): %v", err)
	}
	if recovered == first {
		t.Fatalf("resolution did not recover onto the shared copy: still %q", recovered)
	}
	if got := filepath.Dir(recovered); got != base {
		t.Fatalf("recovered copy %q is not under the base %q", recovered, base)
	}
}

// A cleaner that removes a bundled asset which is not a SKILL.md (a reference
// or runbook a skill body loads) must not leave an intact-looking copy in
// place: the copy is content-validated, so removing any file re-extracts it.
func TestEmbeddedSkillsDir_ReExtractsWhenANonSkillAssetDisappears(t *testing.T) {
	skipIfProcessCopyRefused(t)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	var asset string
	if err := filepath.WalkDir(first, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) == "SKILL.md" {
			return err
		}
		asset = path
		return fs.SkipAll
	}); err != nil {
		t.Fatalf("find a non-SKILL.md asset in %q: %v", first, err)
	}
	if asset == "" {
		t.Fatalf("process copy %q has no non-SKILL.md asset", first)
	}
	if err := os.Remove(asset); err != nil {
		t.Fatalf("remove %q: %v", asset, err)
	}

	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (missing asset): %v", err)
	}
	if second == first {
		t.Fatalf("reused a copy missing %q", asset)
	}
	rel, err := filepath.Rel(first, asset)
	if err != nil {
		t.Fatalf("rel %q to %q: %v", asset, first, err)
	}
	if _, err := os.Stat(filepath.Join(second, rel)); err != nil {
		t.Fatalf("re-extracted copy %q is missing %q: %v", second, rel, err)
	}
}

// Rewriting a regular file in place leaves the directory and every path
// present, so identity and presence alone would serve altered skill content.
// The process copy is content-validated, so it is re-extracted.
func TestEmbeddedSkillsDir_ReExtractsWhenTheProcessCopyIsModified(t *testing.T) {
	skipIfProcessCopyRefused(t)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	first, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir: %v", err)
	}
	var skillFile string
	if err := filepath.WalkDir(first, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != "SKILL.md" {
			return err
		}
		skillFile = path
		return fs.SkipAll
	}); err != nil {
		t.Fatalf("find a SKILL.md in %q: %v", first, err)
	}
	if skillFile == "" {
		t.Fatalf("process copy %q has no SKILL.md", first)
	}
	original, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("read %q: %v", skillFile, err)
	}
	// Overwrite in place with different bytes of the same length, so presence and
	// directory identity are unchanged and only the content differs.
	if err := os.WriteFile(skillFile, []byte(strings.Repeat("x", len(original))), 0o644); err != nil {
		t.Fatalf("overwrite %q: %v", skillFile, err)
	}

	second, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir (modified content): %v", err)
	}
	if second == first {
		t.Fatalf("reused a process copy with modified content: %q", second)
	}
	rel, err := filepath.Rel(first, skillFile)
	if err != nil {
		t.Fatalf("rel %q to %q: %v", skillFile, first, err)
	}
	restored, err := os.ReadFile(filepath.Join(second, rel))
	if err != nil {
		t.Fatalf("re-extracted copy %q is missing %q: %v", second, rel, err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("re-extracted copy did not restore %q", rel)
	}
}
