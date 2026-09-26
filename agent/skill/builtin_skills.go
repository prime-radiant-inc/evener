package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/internal/bundled"
)

// embeddedSkillsPrefix names every directory this package creates under the temp
// or cache root: the content-addressed cache directories (the published copy and
// the private staging directory a publish writes before renaming into place) and
// the one private extraction a process keeps for its lifetime.
const embeddedSkillsPrefix = "evener-skills-"

// Bounds on what the digest will read from a tree. They exist to stop a hostile
// occupant of a shared cache name from being read without limit, so they are
// generous ceilings rather than a shape the bundled tree must fit: the embedded
// tree is a dozen small files today, and a future release that outgrows these
// should raise them rather than silently lose its skills.
const (
	maxEmbeddedSkillBytes   = 4 << 20  // one file
	maxEmbeddedSkillsBytes  = 64 << 20 // every file together
	maxEmbeddedSkillEntries = 4096
	maxEmbeddedSkillDepth   = 16
	// staleStagingMaxAge is how long a staging directory abandoned by a failed
	// publish is left before a later publish reaps it.
	staleStagingMaxAge = 24 * time.Hour
	// staleRetainedMaxAge is how long a superseded published directory is left
	// before a later publish reaps it. It is deliberately long: a process that
	// resolves a copy once and keeps reading it refreshes mtime only when it
	// resolves again, so the age is a use signal, not a hard lifetime.
	staleRetainedMaxAge = 30 * 24 * time.Hour
)

// embeddedSkillsCache holds the published bundled-skills directory, the digest
// of its contents, its scanned metadata, and the shared lease that keeps the
// copy from being reaped, all process-wide under one mutex. verified records
// that this process has already published or re-digested dir, so later calls in
// the same process do not re-walk an immutable copy.
var embeddedSkillsCache struct {
	mu        sync.Mutex
	dir       string
	digest    string
	skills    map[string]SkillMeta
	verified  bool
	lease     skillsLease
	leasedDir string
	// dirIdentity is the directory this process validated for dir. Comparing
	// identities rejects a replacement at the same path without re-digesting the
	// tree on every resolution.
	dirIdentity fs.FileInfo
	// failedErr and failedAt remember a shared resolution that failed, so a run
	// of calls in a degraded environment goes straight to the process copy
	// instead of repeating the publish path. The entry lapses once
	// embeddedSkillsRetryInterval has passed, so a cache root that later becomes
	// usable is retried; base selection is fixed for the process lifetime, so a
	// remembered failure never hides a different base.
	failedErr error
	failedAt  time.Time
}

// embeddedSkillsBaseDir resolves the private directory the content-addressed
// cache lives in. It is a seam so tests can point the cache at a temporary
// directory; the default is defaultEmbeddedSkillsBaseDir.
var embeddedSkillsBaseDir = defaultEmbeddedSkillsBaseDir

// skillsBaseRoot names the per-user root the default base directory lives under.
// It is a seam so tests can exercise a platform that cannot name that root.
var skillsBaseRoot = defaultSkillsBaseRoot

// embeddedSkillsRetryInterval bounds how long a failed shared resolution is
// remembered before the publish path is attempted again. It keeps a degraded
// environment — an unusable or persistently contended cache root — from paying
// for the digest, reap, staging, and publish attempts on every call, while the
// retry that follows the interval still recovers once the root becomes usable.
// It is a seam so tests can exercise the retry without waiting it out.
var embeddedSkillsRetryInterval = 30 * time.Second

// The shared content-addressed cache is best-effort: when no copy can be
// resolved, the process keeps one private extraction of the bundled skills for
// the rest of its lifetime, exactly as a process did before the shared cache
// existed. Nothing reaps this copy and it is never removed by this package:
// sessions read the SkillFile paths inside it for as long as the process runs.
// It is revalidated rather than created once, because a system temp cleaner can
// remove it while the process lives, and a dangling path would silently cost
// every later resolution its skills.
var (
	processSkillsMu   sync.Mutex
	processSkillsDir  string
	processSkillsMeta map[string]SkillMeta
)

// processSkillsTempPattern names the process-lifetime extraction. It is
// deliberately not the staging name that reapStaleCopies collects, because
// nothing in this package reaps it.
const processSkillsTempPattern = embeddedSkillsPrefix + "process-*"

// bundledSkillsDigest is the digest of the embedded tree. The embedded content
// cannot change while the process runs, so it is computed at most once and the
// process-lifetime copy is validated against it.
var bundledSkillsDigest = sync.OnceValues(func() (string, error) {
	return digestSkillsFS(bundled.Skills())
})

// embeddedProcessSkillsDir returns the process-lifetime private extraction,
// creating or recreating it as needed. It extracts through the same
// implementation as ExtractEmbeddedSkills, under its own temp name so the copy
// that outlives every caller is not confused with the staging directories the
// cache reaps by age, and only into a temp root the shared cache would accept:
// the degraded path must not read skill content out of a root that was just
// refused as replaceable by other users, or one this platform cannot verify at
// all.
func embeddedProcessSkillsDir() (string, error) {
	processSkillsMu.Lock()
	defer processSkillsMu.Unlock()
	return embeddedProcessSkillsDirLocked()
}

// embeddedProcessSkillsDirLocked is embeddedProcessSkillsDir for a caller that
// already holds processSkillsMu, so one caller can hold the mutex across the
// directory selection and the metadata scan that belongs to the directory it
// selected. The caller must hold the mutex.
func embeddedProcessSkillsDirLocked() (string, error) {
	digest, err := bundledSkillsDigest()
	if err != nil {
		return "", err
	}
	// The whole tree is validated, not just its directory: a cleaner that removed
	// files but left the directory would otherwise be served as an incomplete set
	// of skills whose cached metadata points at paths that no longer exist. The
	// copy is content-validated, not trusted by identity alone: a truncated or
	// overwritten file must re-extract, and the digest is what catches it.
	if processSkillsDir != "" && cacheDirUsable(processSkillsDir, digest) {
		return processSkillsDir, nil
	}
	root, err := resolveTrustedRoot(os.TempDir(), ensureTrustedProcessRoot)
	if err != nil {
		return "", err
	}
	// The extraction is created inside the resolved root, never through a link
	// the temp root can be: every path this process creates and hands out names
	// the real directory that was validated.
	dir, err := extractEmbeddedSkills(bundled.Skills(), func(_, _ string) (string, error) {
		return os.MkdirTemp(root, processSkillsTempPattern)
	})
	if err != nil {
		return "", err
	}
	processSkillsDir = dir
	processSkillsMeta = nil
	return dir, nil
}

// embeddedProcessSkills returns the metadata of the process-lifetime extraction,
// caching the scan so later callers reuse it. The mutex is held across the
// directory selection and the scan: the selection clears processSkillsMeta
// whenever it replaces the directory, so a scan that ran after the selection
// released the mutex could cache an empty or partial map of the copy that just
// went away, and no later call would refresh it.
func embeddedProcessSkills() (map[string]SkillMeta, error) {
	processSkillsMu.Lock()
	defer processSkillsMu.Unlock()
	// Holding the mutex makes the re-check below unreachable, because nothing
	// else can replace the directory under the scan. It is kept anyway: a map
	// scanned from a copy that is no longer this process's own must never be
	// stored against the copy that replaced it, so such a scan is taken again,
	// and one that keeps losing its directory is handed back uncached.
	const scanAttempts = 3
	for attempt := 0; ; attempt++ {
		dir, err := embeddedProcessSkillsDirLocked()
		if err != nil {
			return nil, err
		}
		if processSkillsMeta != nil {
			return cloneSkillMetaMap(processSkillsMeta), nil
		}
		scanned := make(map[string]SkillMeta)
		ScanSkillsDir(dir, scanned)
		if processSkillsDir != dir {
			if attempt < scanAttempts-1 {
				continue
			}
			return cloneSkillMetaMap(scanned), nil
		}
		processSkillsMeta = scanned
		return cloneSkillMetaMap(processSkillsMeta), nil
	}
}

// EmbeddedSkillsDir returns a directory holding the bundled skills, published
// once per distinct embedded content and shared by every caller and every later
// process. The published copy is immutable: a binary whose embedded content
// changed publishes beside the old copy under its own digest, so a session
// already reading the old directory keeps a stable path. When no shared copy can
// be resolved, the process-lifetime extraction is returned instead, so the
// bundled skills survive an unusable cache root. Callers MUST treat the
// directory as read-only and MUST NOT remove it.
func EmbeddedSkillsDir() (string, error) {
	embeddedSkillsCache.mu.Lock()
	dir, cacheErr := ensureEmbeddedSkillsLocked()
	embeddedSkillsCache.mu.Unlock()
	if cacheErr == nil {
		return dir, nil
	}
	processDir, processErr := embeddedProcessSkillsDir()
	if processErr != nil {
		return "", fmt.Errorf("bundled skills cache unavailable (last: %w): %w", cacheErr, processErr)
	}
	return processDir, nil
}

// ExtractEmbeddedSkills writes the embedded skills to a fresh temporary
// directory and returns the path. The caller owns the directory and is
// responsible for cleanup (os.RemoveAll). The returned directory contains skill
// subdirectories (e.g. test-driven-development/SKILL.md) and is suitable for use
// as an extraDirs argument to DiscoverSkills. Use EmbeddedSkillsDir for the
// shared, content-addressed copy.
func ExtractEmbeddedSkills() (string, error) {
	return extractEmbeddedSkills(bundled.Skills(), os.MkdirTemp)
}

// EmbeddedSkills returns the bundled skills as filesystem-backed metadata, from
// the same shared content-addressed copy EmbeddedSkillsDir returns.
func EmbeddedSkills() (map[string]SkillMeta, error) {
	embeddedSkillsCache.mu.Lock()
	_, cacheErr := ensureEmbeddedSkillsLocked()
	skills := cloneSkillMetaMap(embeddedSkillsCache.skills)
	embeddedSkillsCache.mu.Unlock()
	if cacheErr == nil {
		return skills, nil
	}
	processSkills, processErr := embeddedProcessSkills()
	if processErr != nil {
		return nil, fmt.Errorf("bundled skills cache unavailable (last: %w): %w", cacheErr, processErr)
	}
	return processSkills, nil
}

// ensureEmbeddedSkillsLocked publishes the bundled skills when the cached copy
// is gone and refreshes the cached metadata. The caller holds the cache mutex.
func ensureEmbeddedSkillsLocked() (string, error) {
	if embeddedSkillsCache.dir != "" && embeddedSkillsCache.skills != nil {
		cached := embeddedSkillsCache.dir
		switch {
		case embeddedSkillsCache.verified && cacheDirUnchanged(cached, embeddedSkillsCache.dirIdentity) &&
			cacheDirComplete(embeddedSkillsCache.skills):
			if err := claimEmbeddedSkillsLocked(cached); err == nil &&
				cacheDirUnchanged(cached, embeddedSkillsCache.dirIdentity) {
				touchDir(cached)
				return cached, nil
			}
			// The copy was reaped or cannot be leased: forget it and publish
			// another rather than hand back one whose files may vanish.
			forgetEmbeddedSkillsLocked()
		case !embeddedSkillsCache.verified && cacheDirUsable(cached, embeddedSkillsCache.digest):
			// The content is checked again once the lease is held: the copy can be
			// replaced between the check above and the claim, and a re-check under
			// the lease is what rules that out.
			if err := claimEmbeddedSkillsLocked(cached); err == nil &&
				cacheDirUsable(cached, embeddedSkillsCache.digest) {
				embeddedSkillsCache.verified = true
				rememberCacheDirLocked(cached)
				touchDir(cached)
				return cached, nil
			}
			forgetEmbeddedSkillsLocked()
		default:
			forgetEmbeddedSkillsLocked()
		}
	}
	// A shared resolution that just failed is remembered for
	// embeddedSkillsRetryInterval, so a degraded environment does not re-run the
	// digest, reap, staging, and publish attempts on every call. The retry that
	// follows the interval still recovers once the cache root becomes usable.
	if embeddedSkillsCache.failedErr != nil &&
		time.Since(embeddedSkillsCache.failedAt) < embeddedSkillsRetryInterval {
		return "", embeddedSkillsCache.failedErr
	}
	base, err := embeddedSkillsBaseDir()
	if err != nil {
		// A root that cannot even be named is remembered like any other failed
		// resolution, so repeated calls skip the base resolver too until the
		// interval lapses.
		return "", rememberEmbeddedSkillsFailureLocked(err)
	}
	const publishAttempts = 4
	var lastErr error
	for range publishAttempts {
		dir, digest, err := materializeEmbeddedSkills(bundled.Skills(), base, embeddedSkillsCache.dir)
		if err != nil {
			return "", rememberEmbeddedSkillsFailureLocked(err)
		}
		if err := claimEmbeddedSkillsLocked(dir); err != nil {
			// Contended means the copy is being reaped, and any other error means
			// it cannot be protected; publish another either way.
			lastErr = err
			continue
		}
		if !cacheDirUsable(dir, digest) {
			lastErr = fmt.Errorf("bundled skills copy %s was reaped while it was claimed", dir)
			forgetEmbeddedSkillsLocked()
			continue
		}
		skills := make(map[string]SkillMeta)
		ScanSkillsDir(dir, skills)
		embeddedSkillsCache.dir = dir
		embeddedSkillsCache.digest = digest
		embeddedSkillsCache.skills = skills
		embeddedSkillsCache.verified = true
		rememberCacheDirLocked(dir)
		clearEmbeddedSkillsFailureLocked()
		return dir, nil
	}
	// The shared cache stayed unusable through every attempt. No private copy is
	// published: EmbeddedSkillsDir falls back to the process-lifetime extraction.
	return "", rememberEmbeddedSkillsFailureLocked(fmt.Errorf("bundled skills cache unavailable (last: %w)", lastErr))
}

// rememberEmbeddedSkillsFailureLocked records err as the shared resolution's
// failure for the retry interval and returns it. The caller holds the cache
// mutex.
func rememberEmbeddedSkillsFailureLocked(err error) error {
	embeddedSkillsCache.failedErr = err
	embeddedSkillsCache.failedAt = time.Now()
	return err
}

// clearEmbeddedSkillsFailureLocked drops a remembered failure, so a resolution
// that succeeds is never afterward short-circuited by an earlier failure. The
// caller holds the cache mutex.
func clearEmbeddedSkillsFailureLocked() {
	embeddedSkillsCache.failedErr = nil
	embeddedSkillsCache.failedAt = time.Time{}
}

// errSkillsLeaseContended reports that another process holds the exclusive lease
// on a copy, which means that copy is being reaped and must not be used.
var errSkillsLeaseContended = errors.New("bundled skills copy is being reaped")

// claimEmbeddedSkillsLocked takes a shared lease on the copy this process will
// read, releasing the lease held for a previous copy once the new copy is
// protected. The caller holds the cache mutex. A copy that cannot be leased is
// not returned to callers: it could be removed while they are reading it.
func claimEmbeddedSkillsLocked(dir string) error {
	if embeddedSkillsCache.lease != nil && embeddedSkillsCache.leasedDir == dir {
		if embeddedSkillsCache.lease.Valid() {
			return nil
		}
		// The lock file was replaced underneath the lease, so it guards nothing.
		releaseSkillsLeaseLocked()
	}
	path, err := skillsLockPath(filepath.Dir(dir), filepath.Base(dir), true)
	if err != nil {
		return err
	}
	lease, contended, err := acquireSkillsLease(path, false)
	if contended {
		return errSkillsLeaseContended
	}
	if err != nil {
		return err
	}
	// The lease held for the previous copy is dropped only once the new copy is
	// protected, so a failed claim never leaves the copy being read unprotected.
	releaseSkillsLeaseLocked()
	embeddedSkillsCache.lease = lease
	embeddedSkillsCache.leasedDir = dir
	return nil
}

// releaseSkillsLeaseLocked drops the held lease, if any. The caller holds the
// cache mutex.
func releaseSkillsLeaseLocked() {
	if embeddedSkillsCache.lease != nil {
		_ = embeddedSkillsCache.lease.Release()
		embeddedSkillsCache.lease = nil
	}
	embeddedSkillsCache.leasedDir = ""
}

// forgetEmbeddedSkillsLocked drops the cached copy and its lease, so the next
// resolution publishes or adopts a copy again. The caller holds the cache mutex.
func forgetEmbeddedSkillsLocked() {
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.digest = ""
	embeddedSkillsCache.skills = nil
	embeddedSkillsCache.dirIdentity = nil
	embeddedSkillsCache.verified = false
	releaseSkillsLeaseLocked()
}

// defaultEmbeddedSkillsBaseDir returns the private per-user directory the cache
// lives in. It sits under the temp dir because a session confined to its
// worktree can still read temp, while the config root is sandbox-denylisted and
// the cache root is outside a restricted session's readable roots. It is
// namespaced by user and verified private, so another user on a shared host
// cannot occupy the name or read the published copy. A root or a per-user name
// that cannot be verified fails the resolution rather than publishing a copy
// into a directory this process does not own; EmbeddedSkillsDir then serves the
// process-lifetime extraction instead.
func defaultEmbeddedSkillsBaseDir() (string, error) {
	root, err := skillsBaseRoot()
	if err != nil {
		// A platform that cannot name a verified per-user root must not fall back
		// to a shared temp root it cannot verify.
		return "", err
	}
	if root == "" {
		// A root the platform cannot name must not become a predictable path under
		// a shared temp root: joining an empty root would produce a relative name.
		return "", errors.New("skill cache root unavailable")
	}
	// The root is resolved before it is verified, so a temp root that is itself a
	// symlink is served rather than refused, and the per-user base is created
	// under the directory the link names rather than through the link.
	resolved, err := resolveTrustedRoot(root, ensureTrustedRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(resolved, embeddedSkillsPrefix+processOwnerTag())
	if err := ensurePrivateCacheDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// resolveTrustedRoot resolves root through symlinks and verifies the directory
// it names with verify, returning that resolved directory. os.TempDir returns
// TMPDIR verbatim, and a temp root that is itself a symlink — macOS /tmp, or a
// TMPDIR pointed at one — is a shape the platform produces rather than an
// attack, so verifying the link itself would refuse a usable root and silently
// leave the process without bundled skills. The resolved path is what is
// verified and what every caller creates directories under, so the link is never
// the path this package reads or writes through.
func resolveTrustedRoot(root string, verify func(string) error) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving skills root %s: %w", root, err)
	}
	if err := verify(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// ensureTrustedRoot reports whether root may hold a per-user cache. Root itself
// must be a real directory, never a symlink, that is either sticky or owned
// privately by this process's user: that is what stops another user replacing
// the per-user entry between validation and use. The chain above root is checked
// too, because a writable ancestor lets another user replace the directories on
// the way to it; an ancestor may be owned by this user or by root as long as no
// other user can write to it, since the platform's temp chain is normally
// root-owned, and ancestors are resolved through symlinks the way path lookup
// resolves them.
func ensureTrustedRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("checking skill cache root %s: %w", root, err)
	}
	if !dirInfo(info) {
		return fmt.Errorf("skill cache root %s is not a directory", root)
	}
	if !tempRootTrusted(info) {
		return fmt.Errorf("skill cache root %s can be replaced by other users", root)
	}
	return ensureTrustedAncestors(root)
}

// ensureTrustedProcessRoot is ensureTrustedRoot for the temp root the
// process-lifetime extraction is created in. It differs only in the predicate
// applied to the final component: on Windows the ownership and permission checks
// cannot tell who created a directory, so that root is refused and the degraded
// path fails closed instead of reading skill content out of a root the process
// cannot vouch for. The chain above the root is checked exactly as it is for the
// cache root.
func ensureTrustedProcessRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("checking process skills root %s: %w", root, err)
	}
	if !dirInfo(info) {
		return fmt.Errorf("process skills root %s is not a directory", root)
	}
	if !processCopyRootTrusted(info) {
		return fmt.Errorf("process skills root %s cannot be verified", root)
	}
	return ensureTrustedAncestors(root)
}

// ensureTrustedAncestors verifies the chain above root, which must already have
// been verified by the caller. A writable ancestor lets another user replace the
// directories on the way to root, so each one must carry the sticky bit or be
// owned by this user or by root with no group or other write; ownership by root
// is accepted, unlike for the root itself, because the platform's temp chain
// above a user's own directory is normally root-owned. Ancestors are resolved
// through symlinks the way path lookup resolves them.
func ensureTrustedAncestors(root string) error {
	for dir := filepath.Dir(root); ; {
		parent := filepath.Dir(dir)
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("checking skill cache ancestor %s: %w", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("skill cache ancestor %s is not a directory", dir)
		}
		if !ancestorDirTrusted(info) {
			return fmt.Errorf("skill cache ancestor %s can be replaced by other users", dir)
		}
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// ensurePrivateCacheDir creates dir mode 0700 if it is absent and verifies that
// what is there is a real directory, owned by this user, with no group or other
// access. A directory that fails any check is refused rather than trusted: a
// shared temp dir lets another user create this name first.
func ensurePrivateCacheDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("creating skill cache dir: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("checking skill cache dir: %w", err)
	}
	if !dirInfo(info) {
		return fmt.Errorf("skill cache path %s is not a directory", dir)
	}
	if !cacheDirHasPrivatePermissions(info) {
		return fmt.Errorf("skill cache dir %s is accessible to other users", dir)
	}
	if !cacheDirOwnedByCurrentUser(info) {
		return fmt.Errorf("skill cache dir %s is not owned by the current user", dir)
	}
	if !cacheDirOwnerCanWrite(info) {
		// Without owner write and execute this process cannot create the staging
		// directory inside it, so it must not be selected as the cache root.
		return fmt.Errorf("skill cache dir %s is not usable by its owner", dir)
	}
	return nil
}

// materializeEmbeddedSkills publishes skillsFS into base under a directory named
// for the digest of its contents and returns that directory and digest. A copy
// already published for the same content is reused; otherwise the tree is staged
// in a private directory and renamed into place, so a concurrent publisher
// leaves a complete copy and readers never see a partial tree. An occupant of
// the published name is adopted only when its content matches the digest; any
// other occupant fails the publish, and the caller serves the process-lifetime
// extraction instead.
func materializeEmbeddedSkills(skillsFS fs.FS, base, skipDir string) (string, string, error) {
	digest, err := digestSkillsFS(skillsFS)
	if err != nil {
		return "", "", fmt.Errorf("digesting embedded skills: %w", err)
	}
	reapStaleCopies(base, time.Now(), digest, skipDir)
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	if publishedSkillsDir(dest, digest) {
		touchDir(dest)
		return dest, digest, nil
	}

	staging, err := os.MkdirTemp(base, embeddedSkillsPrefix+"stage-*")
	if err != nil {
		return "", "", fmt.Errorf("staging embedded skills: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := copyEmbeddedSkills(skillsFS, staging); err != nil {
		return "", "", fmt.Errorf("extracting embedded skills: %w", err)
	}
	// A concurrent publisher for the same content may have finished while this
	// copy was made.
	if publishedSkillsDir(dest, digest) {
		touchDir(dest)
		return dest, digest, nil
	}
	// Never rename onto an existing entry: on some platforms that replaces a
	// symlink or file this process just rejected. Re-check first, because a
	// concurrent publisher can publish between the check above and this one.
	if _, err := os.Lstat(dest); err == nil {
		if publishedSkillsDir(dest, digest) {
			touchDir(dest)
			return dest, digest, nil
		}
		return "", "", fmt.Errorf("published skills name %s is occupied by other content", dest)
	}
	if err := os.Rename(staging, dest); err != nil {
		if publishedSkillsDir(dest, digest) {
			touchDir(dest)
			return dest, digest, nil
		}
		return "", "", fmt.Errorf("publishing embedded skills to %s: %w", dest, err)
	}
	return dest, digest, nil
}

// publishedSkillsDir reports whether dest holds a complete copy of the content
// named by digest. It delegates to cacheDirUsable, which uses Lstat rather than
// Stat so a symlink planted in the shared cache name is never followed, and
// re-digests the content rather than trusting the directory name.
func publishedSkillsDir(dest, digest string) bool {
	return cacheDirUsable(dest, digest)
}

// cacheDirUsable reports whether a cached directory still holds the content it
// is supposed to. Lstat rejects a symlinked replacement, and the content is
// re-digested against the expected digest rather than trusted from the
// directory name, so a tampered or partial copy is republished.
func cacheDirUsable(dir, expected string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !dirInfo(info) || expected == "" {
		return false
	}
	actual, err := digestSkillsFS(os.DirFS(dir))
	return err == nil && actual == expected
}

// dirInfo reports whether info describes a real directory, never a symlink or a
// reparse point: on Windows Lstat reports a directory junction or symlink with
// both ModeDir and ModeSymlink set, so IsDir alone would accept it.
func dirInfo(info fs.FileInfo) bool {
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

// rememberCacheDirLocked records the directory the cache resolved, with the
// identity later resolutions compare against. The caller holds the cache mutex.
func rememberCacheDirLocked(dir string) {
	embeddedSkillsCache.dirIdentity = dirIdentity(dir)
}

// dirIdentity returns dir's directory identity for a later os.SameFile
// comparison, or nil when dir is not a real directory this process can compare:
// a symlink or Windows reparse point is refused for the same reason dirInfo
// refuses it.
func dirIdentity(dir string) fs.FileInfo {
	info, err := os.Lstat(dir)
	if err != nil || !dirInfo(info) {
		return nil
	}
	return info
}

// cacheDirUnchanged reports whether dir is still the directory this process
// validated, rather than a replacement at the same path. os.SameFile compares
// the platform's directory identity, so a swapped copy is rejected without
// re-digesting the tree on every resolution.
func cacheDirUnchanged(dir string, identity fs.FileInfo) bool {
	if identity == nil {
		return false
	}
	info, err := os.Lstat(dir)
	return err == nil && dirInfo(info) && os.SameFile(info, identity)
}

// cacheDirComplete reports whether every skill the cache handed out is still
// present. An age-based temp cleaner removes stale files without removing the
// directory that holds them, and the directory's identity is unchanged by that,
// so without this check a gutted copy is served for the rest of the process and
// the bundled skills silently disappear. Only the files the cache itself
// recorded are checked, so the cost is one Lstat per skill.
func cacheDirComplete(skills map[string]SkillMeta) bool {
	if len(skills) == 0 {
		return false
	}
	for _, meta := range skills {
		info, err := os.Lstat(meta.SkillFile)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// touchDir refreshes a cache directory's modification time, and its base's, so
// the reaper can tell a directory a live process keeps resolving from one
// nobody uses. The mtime does not otherwise change when skills are read.
func touchDir(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	_ = os.Chtimes(filepath.Dir(dir), now, now)
}

// reapStaleCopies removes cache entries an earlier publish abandoned, so a
// machine does not accumulate a copy of the embedded tree per run or per binary.
// Only this package's prefixes are matched: staging directories are transient,
// while superseded published directories are kept until they are old enough that
// no live process is plausibly still reading them. keepDigest is the digest
// about to be published and skipDir is this process's own resolved copy; neither
// is reaped.
func reapStaleCopies(base string, now time.Time, keepDigest, skipDir string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	// removeDir takes the entry's exclusive lease and re-checks it afterwards: a
	// publisher can replace the entry between the pass above and the lock, so
	// age is decided again while the lease is held.
	removeDir := func(name, path string, maxAge time.Duration) {
		lockPath, err := skillsLockPath(base, name, true)
		if err != nil {
			return
		}
		lease, ok := tryExclusiveLease(lockPath)
		if !ok {
			return
		}
		defer func() { _ = lease.Release() }()
		info, err := os.Lstat(path)
		if err != nil {
			return
		}
		if maxAge > 0 && now.Sub(info.ModTime()) < maxAge {
			return
		}
		_ = os.RemoveAll(path)
	}
	// removeRejected removes an occupant of the digest name this process needs
	// that cannot be adopted. A concurrent publisher may have healed the name
	// between the check above and the lock, so the rejection is rechecked while
	// the exclusive lease is held.
	removeRejected := func(name, path string) {
		lockPath, err := skillsLockPath(base, name, true)
		if err != nil {
			return
		}
		lease, ok := tryExclusiveLease(lockPath)
		if !ok {
			return
		}
		defer func() { _ = lease.Release() }()
		if publishedSkillsDir(path, keepDigest) {
			return
		}
		info, err := os.Lstat(path)
		if err != nil {
			return
		}
		if dirInfo(info) {
			_ = os.RemoveAll(path)
			return
		}
		// A file, symlink, or Windows reparse point is removed with Remove, which
		// deletes the link itself rather than anything it points at.
		_ = os.Remove(path)
	}
	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		if path == skipDir {
			continue
		}
		rest, ok := strings.CutPrefix(entry.Name(), embeddedSkillsPrefix)
		if !ok {
			continue
		}
		if rest == keepDigest {
			// A rejected occupant of the name this process needs can never be
			// adopted, so it is removed under the exclusive lease so the name
			// heals instead of every run staging another private copy.
			removeRejected(entry.Name(), path)
			continue
		}
		var maxAge time.Duration
		switch {
		case strings.HasPrefix(rest, "stage-"):
			maxAge = staleStagingMaxAge
		case len(rest) == sha256.Size*2:
			maxAge = staleRetainedMaxAge
		default:
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !dirInfo(info) {
			if now.Sub(info.ModTime()) < maxAge {
				continue
			}
			// A file, symlink, or Windows reparse point squatting a cache name is
			// removed with Remove, which deletes the link itself rather than
			// anything it points at, so a later publish can heal the name instead
			// of falling back forever.
			_ = os.Remove(path)
			continue
		}
		removeDir(entry.Name(), path, maxAge)
	}
	pruneObsoleteLocks(base, now)
}

// digestSkillsFS returns a stable hex digest over every path and byte in fsys.
// Paths are fs paths (slash-separated) and entries are visited in lexical order,
// so the digest is identical across platforms and processes.
func digestSkillsFS(fsys fs.FS) (string, error) {
	sum := sha256.New()
	entries := 0
	var total int64
	err := walkSkillsFS(fsys, func(path string, d fs.DirEntry, isDir bool) error {
		// Every visited entry counts, directories included: a tree of nested
		// directories must not be able to bypass the bound that files obey.
		entries++
		if entries > maxEmbeddedSkillEntries {
			return fmt.Errorf("embedded skills: more than %d entries", maxEmbeddedSkillEntries)
		}
		if strings.Count(path, "/") > maxEmbeddedSkillDepth {
			return fmt.Errorf("embedded skills: %s is nested too deeply", path)
		}
		if isDir {
			// Directories are part of the tree being verified, so their names are
			// hashed too.
			_, _ = fmt.Fprintf(sum, "dir %s\x00", path)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// Decided from the resolved mode, not just the directory entry: a
		// symlink points somewhere that can change, a FIFO blocks its open until
		// a writer arrives, and a device is not something to read. The published
		// name lives in a shared temp dir, so an occupant this process did not
		// write may be any of them, and some filesystems do not report a type at
		// all.
		if !info.Mode().IsRegular() {
			return fmt.Errorf("embedded skills: %s is not a regular file", path)
		}
		if info.Size() > maxEmbeddedSkillBytes {
			return fmt.Errorf("embedded skills: %s exceeds %d bytes", path, maxEmbeddedSkillBytes)
		}
		total += info.Size()
		if total > maxEmbeddedSkillsBytes {
			return fmt.Errorf("embedded skills: total size exceeds %d bytes", maxEmbeddedSkillsBytes)
		}
		file, err := fsys.Open(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", path, info.Size())
		// Read one byte past the size the entry declared: a file that grew after
		// the size was read would otherwise have its original prefix hashed and
		// still compare equal, and a file that no longer matches its own size is
		// not the copy this is trying to recognize. Closed here rather than
		// deferred so each file is released before the walk moves on.
		read, err := io.Copy(sum, io.LimitReader(file, info.Size()+1))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if read != info.Size() {
			return fmt.Errorf("embedded skills: %s changed while reading", path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// walkSkillsFS visits every entry in fsys in lexical order, depth first, and
// reports whether each entry is a directory. Unlike fs.WalkDir it never reads a
// whole directory listing before the entry bound can apply, and it resolves an
// entry's kind from Info when the filesystem does not report a type (NFS, FUSE),
// so a directory is still recognized and recursed into there.
func walkSkillsFS(fsys fs.FS, visit func(path string, d fs.DirEntry, isDir bool) error) error {
	var walk func(name string, depth int) error
	walk = func(name string, depth int) error {
		if depth > maxEmbeddedSkillDepth {
			return fmt.Errorf("embedded skills: %s is nested too deeply", name)
		}
		entries, err := readDirBounded(fsys, name)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			child := entry.Name()
			if name != "." {
				child = name + "/" + child
			}
			isDir, err := entryIsDir(entry)
			if err != nil {
				return err
			}
			if err := visit(child, entry, isDir); err != nil {
				return err
			}
			if isDir {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(".", 0)
}

// readDirBounded lists one directory in lexical order. A directory file that
// supports incremental reads is read in chunks and rejected as soon as it
// exceeds the entry bound; an fs.FS whose opened directory cannot be read
// incrementally falls back to fs.ReadDir, which lists the whole directory first
// and is only bounded after the fact, so for those filesystems the bound is a
// post-check rather than a guarantee. Every untrusted path this package reads
// (os.DirFS, embed.FS) supports incremental reads.
func readDirBounded(fsys fs.FS, name string) ([]fs.DirEntry, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	dir, ok := file.(fs.ReadDirFile)
	if !ok {
		entries, err := fs.ReadDir(fsys, name)
		if err != nil {
			return nil, err
		}
		if len(entries) > maxEmbeddedSkillEntries {
			return nil, fmt.Errorf("embedded skills: more than %d entries", maxEmbeddedSkillEntries)
		}
		sortDirEntries(entries)
		return entries, nil
	}
	var entries []fs.DirEntry
	for {
		batch, readErr := dir.ReadDir(maxEmbeddedSkillEntries - len(entries) + 1)
		entries = append(entries, batch...)
		if len(entries) > maxEmbeddedSkillEntries {
			return nil, fmt.Errorf("embedded skills: more than %d entries", maxEmbeddedSkillEntries)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
		if len(batch) == 0 {
			break
		}
	}
	sortDirEntries(entries)
	return entries, nil
}

func sortDirEntries(entries []fs.DirEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
}

// entryIsDir reports whether entry names a real directory, resolving it from
// Info when the filesystem reports no entry type and rejecting symlinks and
// reparse points.
func entryIsDir(entry fs.DirEntry) (bool, error) {
	if entry.Type()&os.ModeSymlink != 0 {
		return false, nil
	}
	if entry.IsDir() {
		return true, nil
	}
	if entry.Type() != 0 {
		return false, nil
	}
	info, err := entry.Info()
	if err != nil {
		return false, err
	}
	return dirInfo(info), nil
}

// extractEmbeddedSkills is the filesystem-backed extraction implementation.
// Keeping its immutable source and temporary-directory factory explicit lets
// tests exercise filesystem failures without changing the exported bundled
// asset behavior.
func extractEmbeddedSkills(skillsFS fs.FS, mkdirTemp func(string, string) (string, error)) (string, error) {
	dir, err := mkdirTemp("", "evener-skills-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir for embedded skills: %w", err)
	}

	if err := copyEmbeddedSkills(skillsFS, dir); err != nil {
		_ = os.RemoveAll(dir) // best-effort cleanup of partial extraction; the extract error is what matters
		return "", fmt.Errorf("extracting embedded skills: %w", err)
	}

	return dir, nil
}

// copyEmbeddedSkills writes every file in skillsFS under dir, creating the
// directories on the way.
func copyEmbeddedSkills(skillsFS fs.FS, dir string) error {
	return fs.WalkDir(skillsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}

		outPath := filepath.Join(dir, filepath.FromSlash(path))

		if d.IsDir() {
			return os.MkdirAll(outPath, 0o755)
		}

		data, err := fs.ReadFile(skillsFS, path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		return os.WriteFile(outPath, data, 0o644)
	})
}

// cloneSkillMetaMap copies the map, each entry's AllowedTools slice, and each
// entry's Metadata, so a caller mutating the returned metadata cannot reach into
// the process-wide cache.
func cloneSkillMetaMap(in map[string]SkillMeta) map[string]SkillMeta {
	out := make(map[string]SkillMeta, len(in))
	for name, meta := range in {
		meta.AllowedTools = append([]string(nil), meta.AllowedTools...)
		meta.Metadata = cloneMetadata(meta.Metadata)
		out[name] = meta
	}
	return out
}
