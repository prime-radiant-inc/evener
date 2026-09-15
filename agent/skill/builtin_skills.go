package skill

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/internal/bundled"
)

// embeddedSkillsPrefix names the content-addressed cache directories that hold
// the bundled skills: the published copy and the private staging directory a
// publish writes before renaming into place.
const embeddedSkillsPrefix = "evener-skills-"

// retainedSkillsPrefix names a private fallback copy that could not take the
// published name and is handed to the caller for the process lifetime. It is
// deliberately not the staging prefix: staging directories are reaped after a
// day, while a retained copy is left for much longer because a live process
// reads it for its lifetime.
const retainedSkillsPrefix = embeddedSkillsPrefix + "copy-"

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
	// fallbackRetryInterval is how long a private fallback copy is reused before
	// the shared cache is tried again.
	fallbackRetryInterval = 5 * time.Minute
	// staleRetainedMaxAge is how long a retained fallback copy, a superseded
	// published directory, or a randomized fallback base is left before a later
	// publish reaps it. It is deliberately long: a process that resolves a copy
	// once and keeps reading it refreshes mtime only when it resolves again, so
	// the age is a use signal, not a hard lifetime.
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
	// fallback marks a private copy created when the shared cache could not be
	// leased, along with the private base it was published into. It is reaped by
	// reapStaleFallbackBases and removed when replaced.
	fallback      bool
	fallbackBase  string
	fallbackUntil time.Time
	// heldLeases keeps leases on fallback copies this process has moved on from.
	// Sessions created while a fallback was current hold SkillFile paths inside
	// it and read them on demand, so its lease is retained rather than dropped
	// when the cache moves on. Retention is bounded by protectUntil, the same
	// staleness window the age reaper uses as its "no reader remains" assumption.
	// That assumes a reader older than the window is gone; tracking exact reader
	// lifetimes would mean wiring every session into this cache, which is the
	// accepted tradeoff. A copy is added only when the cache moves off a fallback
	// it was serving, so a retry that keeps the same copy adds nothing.
	heldLeases []heldSkillsLease
}

// heldSkillsLease is the lease this process keeps on a fallback copy it no
// longer serves, and the time after which the age reaper may collect it.
type heldSkillsLease struct {
	lease        skillsLease
	protectUntil time.Time
}

// embeddedSkillsBaseDir resolves the private directory the content-addressed
// cache lives in. It is a seam so tests can point the cache at a temporary
// directory; the default is defaultEmbeddedSkillsBaseDir.
var embeddedSkillsBaseDir = defaultEmbeddedSkillsBaseDir

// skillsBaseRoot names the per-user root the default base directory lives under.
// It is a seam so tests can exercise a platform that cannot name that root.
var skillsBaseRoot = defaultSkillsBaseRoot

// EmbeddedSkillsDir returns a directory holding the bundled skills, published
// once per distinct embedded content and shared by every caller and every later
// process. The published copy is immutable: a binary whose embedded content
// changed publishes beside the old copy under its own digest, so a session
// already reading the old directory keeps a stable path. Callers MUST treat the
// directory as read-only and MUST NOT remove it.
func EmbeddedSkillsDir() (string, error) {
	embeddedSkillsCache.mu.Lock()
	defer embeddedSkillsCache.mu.Unlock()
	return ensureEmbeddedSkillsLocked()
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
	defer embeddedSkillsCache.mu.Unlock()
	if _, err := ensureEmbeddedSkillsLocked(); err != nil {
		return nil, err
	}
	return cloneSkillMetaMap(embeddedSkillsCache.skills), nil
}

// ensureEmbeddedSkillsLocked publishes the bundled skills when the cached copy
// is gone and refreshes the cached metadata. The caller holds the cache mutex.
func ensureEmbeddedSkillsLocked() (string, error) {
	releaseExpiredHeldLeasesLocked(time.Now())
	// retryFallbackDir is a live fallback copy whose retry window has passed. It
	// is kept while the shared cache is retried and reused if that retry fails,
	// so an outage does not accumulate a copy and a lease per retry.
	retryFallbackDir := ""
	if embeddedSkillsCache.dir != "" && embeddedSkillsCache.skills != nil {
		cached := embeddedSkillsCache.dir
		switch {
		case embeddedSkillsCache.fallback && embeddedSkillsCache.verified && cacheDirExists(cached):
			// A fallback copy is only usable while its own lease is held, and it
			// is only reused until the shared cache is due for another try.
			if embeddedSkillsCache.lease != nil && embeddedSkillsCache.lease.Valid() &&
				time.Now().Before(embeddedSkillsCache.fallbackUntil) {
				touchDir(cached)
				return cached, nil
			}
			retryFallbackDir = cached
		case embeddedSkillsCache.verified && cacheDirExists(cached):
			if err := claimEmbeddedSkillsLocked(cached); err == nil && cacheDirExists(cached) {
				touchDir(cached)
				return cached, nil
			}
			// The copy was reaped or cannot be leased: forget it and publish
			// another rather than hand back one whose files may vanish.
			forgetEmbeddedSkillsLocked()
		case !embeddedSkillsCache.verified && cacheDirUsable(cached, embeddedSkillsCache.digest):
			if err := claimEmbeddedSkillsLocked(cached); err == nil && cacheDirExists(cached) {
				embeddedSkillsCache.verified = true
				touchDir(cached)
				return cached, nil
			}
			forgetEmbeddedSkillsLocked()
		default:
			forgetEmbeddedSkillsLocked()
		}
	}
	// reuseFallbackLocked keeps serving the fallback copy this process still
	// leases, extending its retry window. It reports false when that copy is gone
	// or can no longer be protected.
	reuseFallbackLocked := func() (string, bool) {
		if retryFallbackDir == "" || embeddedSkillsCache.dir != retryFallbackDir ||
			embeddedSkillsCache.skills == nil || !cacheDirExists(retryFallbackDir) ||
			embeddedSkillsCache.lease == nil || !embeddedSkillsCache.lease.Valid() {
			return "", false
		}
		embeddedSkillsCache.fallback = true
		embeddedSkillsCache.verified = true
		embeddedSkillsCache.fallbackBase = filepath.Dir(retryFallbackDir)
		embeddedSkillsCache.fallbackUntil = time.Now().Add(fallbackRetryInterval)
		touchDir(retryFallbackDir)
		return retryFallbackDir, true
	}
	// Fallback bases are created by MkdirTemp, so they live under the temp dir
	// even where the cache base does not. The caller holds the cache mutex.
	reapStaleFallbackBases(os.TempDir(), time.Now(), embeddedSkillsCache.fallbackBase, embeddedSkillsCache.dir)
	base, err := embeddedSkillsBaseDir()
	if err != nil {
		if dir, ok := reuseFallbackLocked(); ok {
			return dir, nil
		}
		return "", err
	}
	const publishAttempts = 4
	var lastErr error
	for range publishAttempts {
		dir, digest, err := materializeEmbeddedSkills(bundled.Skills(), base, embeddedSkillsCache.dir)
		if err != nil {
			if fallbackDir, ok := reuseFallbackLocked(); ok {
				return fallbackDir, nil
			}
			return "", err
		}
		if err := claimEmbeddedSkillsLocked(dir); err != nil {
			// Contended means the copy is being reaped, and any other error means
			// it cannot be protected; publish another either way.
			lastErr = err
			continue
		}
		if !cacheDirExists(dir) {
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
		embeddedSkillsCache.fallback = false
		embeddedSkillsCache.fallbackBase = ""
		embeddedSkillsCache.fallbackUntil = time.Time{}
		// A replaced fallback base is deliberately not removed here: sessions
		// from its window may still read skill files inside it, and the age-based
		// reaper collects it once nothing holds it.
		return dir, nil
	}
	// The shared cache is still unusable, so keep serving the copy this process
	// already leased instead of publishing a replacement.
	if dir, ok := reuseFallbackLocked(); ok {
		return dir, nil
	}
	// The shared cache kept being reaped. Publish a private base under the reaped
	// per-user prefix instead, so the copy has the same lease protection and age
	// bound as every other fallback base, and let a later call retry the shared
	// cache.
	base, err = os.MkdirTemp("", embeddedSkillsPrefix+processOwnerTag()+"-*")
	if err != nil {
		return "", fmt.Errorf("creating private skills cache: %w", err)
	}
	dir, digest, err := materializeEmbeddedSkills(bundled.Skills(), base, "")
	if err != nil {
		_ = os.RemoveAll(base)
		return "", fmt.Errorf("bundled skills cache unavailable (last: %w): %w", lastErr, err)
	}
	// Tear down the previous copy first: forget releases any lease held for it
	// (and removes an old fallback base), so the lease taken below survives.
	forgetEmbeddedSkillsLocked()
	if err := claimEmbeddedSkillsLocked(dir); err != nil {
		// A copy that cannot be leased cannot be protected from the reaper, so it
		// is refused rather than cached and returned.
		_ = os.RemoveAll(base)
		return "", fmt.Errorf("leasing private skills cache (last: %w): %w", lastErr, err)
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(dir, skills)
	embeddedSkillsCache.dir = dir
	embeddedSkillsCache.digest = digest
	embeddedSkillsCache.skills = skills
	embeddedSkillsCache.verified = true
	embeddedSkillsCache.fallback = true
	embeddedSkillsCache.fallbackBase = base
	embeddedSkillsCache.fallbackUntil = time.Now().Add(fallbackRetryInterval)
	return dir, nil
}

// errSkillsLeaseContended reports that another process holds the exclusive lease
// on a copy, which means that copy is being reaped and must not be used.
var errSkillsLeaseContended = errors.New("bundled skills copy is being reaped")

// claimEmbeddedSkillsLocked takes a shared lease on the copy this process will
// read, releasing the lease held for a previous copy. The caller holds the cache
// mutex. A copy that cannot be leased is not returned to callers: it could be
// removed while they are reading it.
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
	// protected, so a failed claim leaves a fallback copy leased and a retry can
	// keep serving it rather than publish a replacement.
	releasePreviousSkillsLeaseLocked()
	embeddedSkillsCache.lease = lease
	embeddedSkillsCache.leasedDir = dir
	return nil
}

// releasePreviousSkillsLeaseLocked drops the lease held for a copy this process
// is leaving behind. A fallback copy's lease is retained instead: sessions
// created while it was current read its files on demand, so the base must stay
// leased until the reaper's staleness window has passed. The caller holds the
// cache mutex.
func releasePreviousSkillsLeaseLocked() {
	if embeddedSkillsCache.lease == nil {
		return
	}
	if embeddedSkillsCache.fallback && embeddedSkillsCache.lease.Valid() {
		embeddedSkillsCache.heldLeases = append(embeddedSkillsCache.heldLeases, heldSkillsLease{
			lease:        embeddedSkillsCache.lease,
			protectUntil: time.Now().Add(staleRetainedMaxAge),
		})
		embeddedSkillsCache.lease = nil
		embeddedSkillsCache.leasedDir = ""
		// The superseded copy is no longer the cached fallback: if the replacement
		// turns out to be unusable, forget must release its lease rather than
		// retain it as if it protected a fallback.
		embeddedSkillsCache.fallback = false
		embeddedSkillsCache.fallbackBase = ""
		embeddedSkillsCache.fallbackUntil = time.Time{}
		return
	}
	releaseSkillsLeaseLocked()
}

// releaseExpiredHeldLeasesLocked drops the retained leases whose protection
// window has passed, so the age reaper can collect those bases. The caller holds
// the cache mutex.
func releaseExpiredHeldLeasesLocked(now time.Time) {
	kept := make([]heldSkillsLease, 0, len(embeddedSkillsCache.heldLeases))
	for _, held := range embeddedSkillsCache.heldLeases {
		if now.Before(held.protectUntil) {
			kept = append(kept, held)
			continue
		}
		_ = held.lease.Release()
	}
	embeddedSkillsCache.heldLeases = kept
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

// forgetEmbeddedSkillsLocked drops the cached copy and its lease. A private
// fallback copy is NOT removed with it: sessions created while it was current
// hold SkillFile paths inside it and read them on demand, so it stays until
// reapStaleFallbackBases collects it by age. The caller holds the cache mutex.
func forgetEmbeddedSkillsLocked() {
	fallback := embeddedSkillsCache.fallback
	embeddedSkillsCache.dir = ""
	embeddedSkillsCache.digest = ""
	embeddedSkillsCache.skills = nil
	embeddedSkillsCache.verified = false
	embeddedSkillsCache.fallback = false
	embeddedSkillsCache.fallbackBase = ""
	embeddedSkillsCache.fallbackUntil = time.Time{}
	if fallback && embeddedSkillsCache.lease != nil && embeddedSkillsCache.lease.Valid() {
		// Sessions created while this fallback was current may still read its
		// files, so its lease is retained until the reaper's staleness window has
		// passed.
		embeddedSkillsCache.heldLeases = append(embeddedSkillsCache.heldLeases, heldSkillsLease{
			lease:        embeddedSkillsCache.lease,
			protectUntil: time.Now().Add(staleRetainedMaxAge),
		})
		embeddedSkillsCache.lease = nil
		embeddedSkillsCache.leasedDir = ""
		return
	}
	releaseSkillsLeaseLocked()
}

// defaultEmbeddedSkillsBaseDir returns the private per-user directory the cache
// lives in. It sits under the temp dir because a session confined to its
// worktree can still read temp, while the config root is sandbox-denylisted and
// the cache root is outside a restricted session's readable roots. It is
// namespaced by user and verified private, so another user on a shared host
// cannot occupy the name or read the published copy.
func defaultEmbeddedSkillsBaseDir() (string, error) {
	root, err := skillsBaseRoot()
	if err != nil {
		// A platform that cannot name a verified per-user root must not fall back
		// to a shared temp root it cannot verify.
		return "", err
	}
	// A root the platform cannot name must not become a predictable path under a
	// shared temp root: joining an empty root would produce a relative name, so
	// the randomized private base below is used instead.
	if root != "" {
		dir := filepath.Join(root, embeddedSkillsPrefix+processOwnerTag())
		if err := ensurePrivateCacheDir(dir); err == nil {
			return dir, nil
		}
	}
	// The predictable name can still be squatted on a shared host, and the
	// sticky bit prevents removing a foreign directory. A randomized private
	// directory keeps the bundled skills available instead of failing the
	// session; it is not shared between processes, which is the price of the
	// name being unusable.
	fallback, err := os.MkdirTemp("", embeddedSkillsPrefix+processOwnerTag()+"-*")
	if err != nil {
		return "", fmt.Errorf("creating private skill cache: %w", err)
	}
	return fallback, nil
}

// reapStaleFallbackBases removes randomized fallback bases this user's earlier
// processes abandoned. Only this user's exact prefix is matched, and only when
// old enough that no live process is plausibly still reading it; a base holding
// a leased cache directory is left alone. liveBase and liveDir are this
// process's own copy, whose base is never removed.
func reapStaleFallbackBases(tmpBase string, now time.Time, liveBase, liveDir string) {
	prefix := embeddedSkillsPrefix + processOwnerTag() + "-"
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !dirInfo(info) {
			// A symlink or Windows reparse point under a fallback base name is
			// never a base: unlink the entry itself rather than traverse it, so it
			// can neither be removed through nor shadow a real base.
			_ = os.Remove(filepath.Join(tmpBase, entry.Name()))
			continue
		}
		if now.Sub(info.ModTime()) < staleRetainedMaxAge {
			continue
		}
		// Only a directory this user owns, with no group or other access, is ours
		// to remove: the name is predictable enough for someone else to create.
		if !cacheDirOwnedByCurrentUser(info) || !cacheDirHasPrivatePermissions(info) {
			continue
		}
		path := filepath.Join(tmpBase, entry.Name())
		if path == liveBase || (liveDir != "" && (path == filepath.Dir(liveDir) || path == liveDir)) {
			// Never remove this process's own live copy.
			continue
		}
		leases, ok := leaseFallbackBase(path)
		if !ok {
			continue
		}
		removeFallbackBase(path, leases)
	}
}

// leaseFallbackBase takes the exclusive lease on every cache copy inside base,
// reporting whether the whole base is free to remove. The leases stay held until
// the caller has removed the base, so a reader cannot acquire one in the gap
// between the check and the removal.
func leaseFallbackBase(base string) ([]skillsLease, bool) {
	locks := filepath.Join(base, skillsLockDirName)
	entries, err := os.ReadDir(locks)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, true
		}
		// A lock directory that cannot be read must not be treated as unleased:
		// the base could still be held.
		return nil, false
	}
	var leases []skillsLease
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lease, ok := tryExclusiveLease(filepath.Join(locks, entry.Name()))
		if !ok {
			for _, held := range leases {
				_ = held.Release()
			}
			return nil, false
		}
		leases = append(leases, lease)
	}
	return leases, true
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
// other occupant leaves the staged private copy in place, because a session
// without its bundled skills is worse than one that did not reuse the cache.
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
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

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
		dir, keptDigest := retainStagedCopy(staging, base, digest, &keepStaging)
		return dir, keptDigest, nil
	}
	if err := os.Rename(staging, dest); err != nil {
		if publishedSkillsDir(dest, digest) {
			touchDir(dest)
			return dest, digest, nil
		}
		dir, keptDigest := retainStagedCopy(staging, base, digest, &keepStaging)
		return dir, keptDigest, nil
	}
	return dest, digest, nil
}

// retainStagedCopy hands back the private copy already staged when the published
// name is unusable. It renames the staging directory under the retained prefix,
// which reapStaleCopies leaves alone for far longer than the staging age, so a
// live process's copy is not reaped out from under it. A rename that cannot
// reserve a name leaves the staging directory in place (and keepStaging set)
// rather than failing the caller, because the caller having its bundled skills
// matters more than where they came from.
//
// The caller caches a retained copy as a verified copy rather than a fallback:
// it is content-addressed and leased like a published one, and retrying the
// published name would re-stage the whole tree and keep a fresh copy per retry
// while the name stays occupied. The accepted consequence is that this process
// keeps serving the retained copy even if the published name heals.
func retainStagedCopy(staging, base, digest string, keepStaging *bool) (string, string) {
	// Renaming onto a fresh random name avoids the placeholder race and works
	// where a rename cannot replace an existing directory.
	for range 8 {
		candidate := filepath.Join(base, retainedSkillsPrefix+randomToken())
		if err := os.Rename(staging, candidate); err == nil {
			return candidate, digest
		}
	}
	*keepStaging = true
	return staging, digest
}

// randomToken returns a short random name component for a retained copy.
func randomToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
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

// cacheDirExists reports whether dir is still a real directory. It never
// follows a symlink, so a replaced cache path is not mistaken for the copy.
func cacheDirExists(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && dirInfo(info)
}

// touchDir refreshes a cache directory's modification time, and its base's, so
// the reaper can tell a directory a live process keeps resolving from one
// nobody uses. The mtime does not otherwise change when skills are read, and a
// randomized fallback base is aged by its own mtime.
func touchDir(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	_ = os.Chtimes(filepath.Dir(dir), now, now)
}

// reapStaleCopies removes cache entries an earlier publish abandoned, so a
// machine does not accumulate a copy of the embedded tree per run or per binary.
// Only this package's prefixes are matched: staging directories are transient,
// while retained fallback copies and superseded published directories are kept
// until they are old enough that no live process is plausibly still reading
// them. keepDigest is the digest about to be published and skipDir is this
// process's own resolved copy; neither is reaped.
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
		case strings.HasPrefix(rest, "copy-"):
			maxAge = staleRetainedMaxAge
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
