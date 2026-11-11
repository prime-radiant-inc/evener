//go:build unix

package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnsurePrivateCacheDir_CreatesPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := ensurePrivateCacheDir(dir); err != nil {
		t.Fatalf("ensurePrivateCacheDir: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat cache dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir mode = %o, want 700", info.Mode().Perm())
	}
	if err := ensurePrivateCacheDir(dir); err != nil {
		t.Fatalf("ensurePrivateCacheDir (again): %v", err)
	}
}

func TestEnsurePrivateCacheDir_RefusesUnsafeDirectories(t *testing.T) {
	root := t.TempDir()

	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ensurePrivateCacheDir(link); err == nil {
		t.Fatal("accepted a symlinked cache dir")
	}

	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatalf("create open dir: %v", err)
	}
	// Chmod, not the Mkdir mode, so the ambient umask cannot make the directory
	// private and hide the case under test.
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatalf("open up dir: %v", err)
	}
	if err := ensurePrivateCacheDir(open); err == nil {
		t.Fatal("accepted a group/other-accessible cache dir")
	}

	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatalf("create locked dir: %v", err)
	}
	// Chmod, not the Mkdir mode, so the ambient umask cannot leave it usable.
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("lock dir: %v", err)
	}
	if err := ensurePrivateCacheDir(locked); err == nil {
		t.Fatal("accepted a cache dir its owner cannot write")
	}
}

// A temp root another user could replace entries in must not be trusted with the
// per-user cache; resolution moves to a verified fallback instead.
func TestDefaultEmbeddedSkillsBaseDir_DoesNotUseAnUntrustedTempRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	fallback := t.TempDir()
	savedFallback := skillsFallbackRoot
	t.Cleanup(func() { skillsFallbackRoot = savedFallback })
	skillsFallbackRoot = func() string { return fallback }
	resetSkillsFallback(t)
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("open up temp root: %v", err)
	}
	dir, err := defaultEmbeddedSkillsBaseDir()
	if err != nil {
		t.Fatalf("defaultEmbeddedSkillsBaseDir with an untrusted temp root: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	if strings.HasPrefix(dir, resolvedRoot+string(os.PathSeparator)) {
		t.Fatalf("cache base %q landed in the untrusted temp root", dir)
	}
	if diagnostics := EmbeddedSkillsDiagnostics(); len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one naming the refused root", diagnostics)
	}
	if err := os.Chmod(root, os.ModeSticky|0o777); err != nil {
		t.Fatalf("make temp root sticky: %v", err)
	}
	dir, err = defaultEmbeddedSkillsBaseDir()
	if err != nil {
		t.Fatalf("refused a sticky temp root: %v", err)
	}
	if !strings.HasPrefix(dir, resolvedRoot+string(os.PathSeparator)) {
		t.Fatalf("cache base %q is not under the now-trusted temp root %q", dir, resolvedRoot)
	}
}

// The whole chain above the cache root decides whether another user can replace
// the per-user entry: a world-writable, non-sticky ancestor is refused, while a
// sticky or non-writable chain is accepted.
func TestEnsureTrustedRoot_ChecksTheAncestorChain(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatalf("create inner: %v", err)
	}
	if err := ensureTrustedRoot(inner); err != nil {
		t.Fatalf("refused a private chain: %v", err)
	}

	// Chmod rather than Mkdir, so the ambient umask cannot hide the case.
	if err := os.Chmod(outer, 0o777); err != nil {
		t.Fatalf("open up ancestor: %v", err)
	}
	if err := ensureTrustedRoot(inner); err == nil {
		t.Fatal("accepted a world-writable ancestor")
	}
	if err := os.Chmod(outer, 0o770); err != nil {
		t.Fatalf("make ancestor group-writable: %v", err)
	}
	if err := ensureTrustedRoot(inner); err == nil {
		t.Fatal("accepted a group-writable ancestor")
	}

	// The sticky bit stops another user replacing the entry named inside it.
	if err := os.Chmod(outer, os.ModeSticky|0o777); err != nil {
		t.Fatalf("make ancestor sticky: %v", err)
	}
	if err := ensureTrustedRoot(inner); err != nil {
		t.Fatalf("refused a sticky ancestor: %v", err)
	}

	// A non-writable ancestor is accepted, and the final component still obeys
	// its own rule: the cache root itself must not be group- or other-accessible.
	if err := os.Chmod(outer, 0o700); err != nil {
		t.Fatalf("restore ancestor: %v", err)
	}
	if err := os.Chmod(inner, 0o770); err != nil {
		t.Fatalf("make root group-writable: %v", err)
	}
	if err := ensureTrustedRoot(inner); err == nil {
		t.Fatal("accepted a group-accessible cache root")
	}
}

// A read-only mount refuses writes through this namespace, but a writable
// ancestor is still replaceable through the host filesystem, so the narrow
// acceptance for an unverifiable owner does not extend to one other users can
// write to. A group- or other-writable ancestor is refused whichever mount it
// sits on, and an unreadable mount table is no evidence at all.
func TestEnsureTrustedAncestors_RefusesAReplaceableAncestorOnAReadOnlyMount(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatalf("create inner: %v", err)
	}
	// Chmod rather than Mkdir, so the ambient umask cannot hide the case.
	if err := os.Chmod(outer, 0o777); err != nil {
		t.Fatalf("open up ancestor: %v", err)
	}
	saved := readMountInfo
	t.Cleanup(func() { readMountInfo = saved })

	readMountInfo = func() []byte { return nil }
	if err := ensureTrustedAncestors(inner); err == nil {
		t.Fatal("accepted a world-writable ancestor with no mount evidence")
	}

	readMountInfo = func() []byte {
		return []byte("36 25 0:32 / " + outer + " rw,relatime - tmpfs tmpfs rw\n")
	}
	if err := ensureTrustedAncestors(inner); err == nil {
		t.Fatal("accepted a world-writable ancestor on a writable mount")
	}

	// The shape the finding describes: a world-writable, non-sticky ancestor and
	// a writable temp directory beneath it, on the sandbox's read-only root bind.
	readMountInfo = func() []byte {
		// A complete fake namespace, including the read-only root the sandbox's
		// host-root bind presents, so the ordinary ancestor is the only component
		// under test.
		return []byte("1 0 0:1 / / ro - tmpfs tmpfs rw\n36 1 0:32 / " + outer + " ro,relatime - tmpfs tmpfs rw\n")
	}
	if err := ensureTrustedAncestors(inner); err == nil {
		t.Fatal("accepted a replaceable ancestor on a read-only mount")
	}
}

// Issue #5809 review: an unmapped owner is not evidence that host root owns an
// ancestor. Every host user the namespace does not map appears as the same
// overflow uid, so another user could own the directory and rename it through a
// writable mount of the same host filesystem, substituting the cache tree after
// verification. An ordinary ancestor whose owner this process cannot verify is
// therefore refused even on a read-only mount; resolution falls back to a root
// whose ownership it can verify instead.
func TestEnsureTrustedAncestors_RefusesAnUnverifiableOwnerOnAReadOnlyMount(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatalf("create inner: %v", err)
	}
	savedStat := statAncestor
	t.Cleanup(func() { statAncestor = savedStat })
	savedMounts := readMountInfo
	t.Cleanup(func() { readMountInfo = savedMounts })

	// The seam is not broken: a fully trusted chain passes.
	if err := ensureTrustedAncestors(inner); err != nil {
		t.Fatalf("refused a trusted chain: %v", err)
	}

	otherUser := uint32(os.Getuid()) + 1
	statAncestor = func(name string) (fs.FileInfo, error) {
		if name == outer {
			return fakeFileInfo{mode: os.ModeDir | 0o755, uid: otherUser}, nil
		}
		return os.Stat(name)
	}
	// A complete read-only mount table is still not evidence of the owner.
	readMountInfo = func() []byte {
		return []byte("1 0 0:1 / / ro - tmpfs tmpfs rw\n2 1 0:2 / " + outer + " ro,relatime - tmpfs tmpfs rw\n")
	}
	if err := ensureTrustedAncestors(inner); err == nil {
		t.Fatal("accepted an unverifiable owner on a read-only mount; an unmapped uid does not prove host root owns the ancestor")
	}
}

// The read-only filesystem-root rule belongs to the filesystem root alone: the
// mount pins that inode and no parent directory holds its name. An ordinary
// ancestor gets no exemption at all, so this rule still refuses it. This is the
// shape of the reviewer's scenario: a
// world-writable, non-sticky /shared/public on the sandbox's read-only root bind
// must not let another user rename a directory beneath it.
func TestReadOnlyFilesystemRoot_OnlyTheFilesystemRootIsExempt(t *testing.T) {
	otherUser := uint32(os.Getuid()) + 1
	// The owner is the unmapped overflow uid, which reads as another user here,
	// so only the root's mount and mode can vouch for it.
	unverifiableOwner := fakeFileInfo{mode: os.ModeDir | 0o755, uid: otherUser}
	mounts := parseMountPoints("" +
		"1 0 0:1 / / ro - tmpfs tmpfs rw\n" +
		"2 1 0:2 / /work ro - tmpfs tmpfs rw\n")
	for _, tc := range []struct {
		name string
		dir  string
		want bool
	}{
		{"filesystem root", "/", true},
		{"ordinary ancestor on the root mount", "/shared/public", false},
		{"ordinary ancestor on a nested read-only mount", "/work/user", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ancestorDirTrusted(unverifiableOwner) {
				t.Fatal("ownership predicate trusted an unverifiable owner")
			}
			if got := readOnlyFilesystemRoot(unverifiableOwner, mounts, tc.dir); got != tc.want {
				t.Fatalf("readOnlyFilesystemRoot(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
	// A root other users can write to is replaceable through the host mount, so
	// it gets no exemption; neither does a writable mount.
	worldWritableRoot := fakeFileInfo{mode: os.ModeDir | 0o777, uid: otherUser}
	if readOnlyFilesystemRoot(worldWritableRoot, mounts, "/") {
		t.Fatal("trusted a world-writable filesystem root")
	}
	writableRoot := parseMountPoints("1 0 0:1 / / rw - tmpfs tmpfs rw\n")
	if readOnlyFilesystemRoot(unverifiableOwner, writableRoot, "/") {
		t.Fatal("trusted a replaceable ancestor under a writable filesystem root")
	}
	// A read-only bind of a directory at / pins the source dentry: renaming the
	// host source pathname does not change what the mount points at, so the
	// namespace root is still stable and the mode check above is the remaining
	// gate.
	subdirectoryBind := parseMountPoints("1 0 252:0 /srv/shared/rootfs / ro - ext4 /dev/sda rw\n")
	if !readOnlyFilesystemRoot(unverifiableOwner, subdirectoryBind, "/") {
		t.Fatal("refused a read-only bind of a directory pinned at the namespace root")
	}
	// A Btrfs root subvolume reports its subvolume path as the mountinfo fsRoot;
	// the mount still pins the namespace root, so the spelling "/@" must not
	// defeat the exemption.
	subvolumeRoot := parseMountPoints("1 0 0:32 /@ / ro - btrfs /dev/sda rw\n")
	if !readOnlyFilesystemRoot(unverifiableOwner, subvolumeRoot, "/") {
		t.Fatal("refused a read-only Btrfs root subvolume")
	}
}

// The degraded extraction must not land in a temp root the shared cache refuses;
// it moves to the verified fallback instead.
func TestEmbeddedSkillsDir_ProcessCopyAvoidsAnUntrustedTempRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	fallback := t.TempDir()
	savedFallback := skillsFallbackRoot
	t.Cleanup(func() { skillsFallbackRoot = savedFallback })
	skillsFallbackRoot = func() string { return fallback }
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)
	resetSkillsFallback(t)

	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("open up temp root: %v", err)
	}
	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir with an untrusted temp root: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	if strings.HasPrefix(dir, resolvedRoot+string(os.PathSeparator)) {
		t.Fatalf("process copy %q landed in the untrusted temp root", dir)
	}

	resetProcessSkills()
	if err := os.Chmod(root, os.ModeSticky|0o777); err != nil {
		t.Fatalf("make temp root sticky: %v", err)
	}
	dir, err = EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("refused a sticky temp root: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(dir), embeddedSkillsPrefix+"process-") {
		t.Fatalf("degraded copy = %q, want a process-lifetime extraction", dir)
	}
	if !strings.HasPrefix(dir, resolvedRoot+string(os.PathSeparator)) {
		t.Fatalf("process copy %q is not under the now-trusted temp root %q", dir, resolvedRoot)
	}
}

// resetEmbeddedSkillsCache drops the shared copy a previous test resolved, so a
// test that wants the default base resolves it again, and restores the cache it
// found when the test ends.
func resetEmbeddedSkillsCache(t *testing.T) {
	t.Helper()
	saved := saveEmbeddedSkillsCache()
	embeddedSkillsCache.mu.Lock()
	forgetEmbeddedSkillsLocked()
	embeddedSkillsCache.mu.Unlock()
	skillsFallbackMu.Lock()
	savedFallback := skillsFallback
	skillsFallback = nil
	skillsFallbackMu.Unlock()
	// forgetEmbeddedSkillsLocked released the lease the snapshot captured, so the
	// restore must not reinstall it.
	saved.lease = nil
	saved.leasedDir = ""
	t.Cleanup(func() {
		restoreEmbeddedSkillsCache(saved)
		skillsFallbackMu.Lock()
		skillsFallback = savedFallback
		skillsFallbackMu.Unlock()
	})
}

// os.TempDir returns TMPDIR verbatim, and a temp root that is itself a symlink
// (macOS /tmp, or a TMPDIR pointed at one) is a shape the platform produces
// rather than an attack: resolving the root before verifying it must leave the
// bundled skills reachable through the shared cache, in a directory under the
// real root rather than a path that runs through the link.
func TestEmbeddedSkillsDir_ResolvesASymlinkedTempRoot(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("TMPDIR", link)
	resetEmbeddedSkillsCache(t)

	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir through a symlinked temp root: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("resolve the real temp root: %v", err)
	}
	if !strings.HasPrefix(dir, resolved+string(os.PathSeparator)) {
		t.Fatalf("bundled skills are not under the real temp root %s: %q", resolved, dir)
	}
	if strings.HasPrefix(dir, link+string(os.PathSeparator)) || dir == link {
		t.Fatalf("bundled skills were created through the symlink %s: %q", link, dir)
	}
	// The per-user base under the real root is what the shared cache publishes
	// into; the degraded path would hand back a process- extraction instead.
	if base := filepath.Base(filepath.Dir(dir)); base != embeddedSkillsPrefix+processOwnerTag() {
		t.Fatalf("bundled skills were not served from the shared cache: %q lives under %q", dir, base)
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(dir, skills)
	if len(skills) == 0 {
		t.Fatalf("resolved copy %q holds no skills", dir)
	}
}

// The degraded path resolves the temp root too, and creates its extraction
// inside the resolved directory rather than through the link.
func TestEmbeddedSkillsDir_ProcessCopyUnderASymlinkedTempRoot(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("TMPDIR", link)
	pointEmbeddedSkillsAtBase(t, filepath.Join(t.TempDir(), "missing-base"))
	t.Cleanup(resetProcessSkills)

	dir, err := EmbeddedSkillsDir()
	if err != nil {
		t.Fatalf("EmbeddedSkillsDir through a symlinked temp root: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(dir), embeddedSkillsPrefix+"process-") {
		t.Fatalf("degraded copy = %q, want a process-lifetime extraction", dir)
	}
	resolved, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("resolve the real temp root: %v", err)
	}
	if !strings.HasPrefix(dir, resolved+string(os.PathSeparator)) {
		t.Fatalf("process copy is not under the real temp root %s: %q", resolved, dir)
	}
	if strings.HasPrefix(dir, link+string(os.PathSeparator)) {
		t.Fatalf("process copy was created through the symlink %s: %q", link, dir)
	}
}

// A sticky root only protects this process's entries from users who do not own
// it, so a sticky root owned by another user must not be trusted.
func TestTempRootTrusted_RejectsAStickyRootOwnedByAnotherUser(t *testing.T) {
	fake := func(mode fs.FileMode, uid uint32) fs.FileInfo {
		return fakeFileInfo{mode: mode, uid: uid}
	}
	other := uint32(os.Getuid()) + 1
	sticky := os.ModeSticky | 0o777

	for _, trust := range []struct {
		name  string
		trust func(fs.FileInfo) bool
	}{
		{"tempRootTrusted", tempRootTrusted},
		{"ancestorDirTrusted", ancestorDirTrusted},
	} {
		if trust.trust(fake(sticky, other)) {
			t.Fatalf("%s trusted a sticky root owned by another user", trust.name)
		}
		for _, uid := range []uint32{0, uint32(os.Getuid())} {
			if !trust.trust(fake(sticky, uid)) {
				t.Fatalf("%s refused a sticky root owned by uid %d", trust.name, uid)
			}
		}
	}
	// A non-sticky private root owned by this user is still trusted, and one
	// another user owns is not.
	if !tempRootTrusted(fake(0o700, uint32(os.Getuid()))) {
		t.Fatal("refused a private root owned by this user")
	}
	if tempRootTrusted(fake(0o700, other)) {
		t.Fatal("trusted a private root owned by another user")
	}
}

type fakeFileInfo struct {
	mode fs.FileMode
	uid  uint32
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return true }
func (f fakeFileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }
