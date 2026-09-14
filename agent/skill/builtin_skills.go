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

// Bounds on what the digest will read from a tree. The bundled skills are a
// dozen small markdown files; these exist because the published name lives in a
// shared temp directory, so an occupant this process did not write could be a
// large or hostile tree that would otherwise be read before it is rejected.
const (
	maxEmbeddedSkillBytes   = 1 << 20 // one file
	maxEmbeddedSkillsBytes  = 8 << 20 // every file together
	maxEmbeddedSkillEntries = 256
	maxEmbeddedSkillDepth   = 8
	// staleStagingMaxAge is how long a staging directory abandoned by a failed
	// publish is left before a later publish reaps it.
	staleStagingMaxAge = 24 * time.Hour
	// staleRetainedMaxAge is how long a retained fallback copy, a superseded
	// published directory, or a randomized fallback base is left before a later
	// publish reaps it. It is deliberately long: a process that resolves a copy
	// once and keeps reading it refreshes mtime only when it resolves again, so
	// the age is a use signal, not a hard lifetime.
	staleRetainedMaxAge = 30 * 24 * time.Hour
)

// embeddedSkillsCache holds the published bundled-skills directory, the digest
// of its contents, and its scanned metadata, all process-wide under one mutex.
// verified records that this process has already published or re-digested dir,
// so later calls in the same process do not re-walk an immutable copy.
var embeddedSkillsCache struct {
	mu       sync.Mutex
	dir      string
	digest   string
	skills   map[string]SkillMeta
	verified bool
}

// embeddedSkillsBaseDir resolves the private directory the content-addressed
// cache lives in. It is a seam so tests can point the cache at a temporary
// directory; the default is defaultEmbeddedSkillsBaseDir.
var embeddedSkillsBaseDir = defaultEmbeddedSkillsBaseDir

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
	if embeddedSkillsCache.dir != "" && embeddedSkillsCache.skills != nil {
		if embeddedSkillsCache.verified && cacheDirExists(embeddedSkillsCache.dir) {
			touchDir(embeddedSkillsCache.dir)
			return embeddedSkillsCache.dir, nil
		}
		if !embeddedSkillsCache.verified && cacheDirUsable(embeddedSkillsCache.dir, embeddedSkillsCache.digest) {
			embeddedSkillsCache.verified = true
			touchDir(embeddedSkillsCache.dir)
			return embeddedSkillsCache.dir, nil
		}
		// The copy is gone, replaced, or unverified and unusable: forget it and
		// republish rather than hand back a dangling path.
		embeddedSkillsCache.dir = ""
		embeddedSkillsCache.digest = ""
		embeddedSkillsCache.skills = nil
		embeddedSkillsCache.verified = false
	}
	base, err := embeddedSkillsBaseDir()
	if err != nil {
		return "", err
	}
	dir, digest, err := materializeEmbeddedSkills(bundled.Skills(), base)
	if err != nil {
		return "", err
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(dir, skills)
	embeddedSkillsCache.dir = dir
	embeddedSkillsCache.digest = digest
	embeddedSkillsCache.skills = skills
	embeddedSkillsCache.verified = true
	return dir, nil
}

// cacheDirExists reports whether dir is still a real directory. It never
// follows a symlink, so a replaced cache path is not mistaken for the copy.
func cacheDirExists(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir()
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

// defaultEmbeddedSkillsBaseDir returns the private per-user directory the cache
// lives in. It sits under the temp dir because a session confined to its
// worktree can still read temp, while the config root is sandbox-denylisted and
// the cache root is outside a restricted session's readable roots. It is
// namespaced by user and verified private, so another user on a shared host
// cannot occupy the name or read the published copy.
func defaultEmbeddedSkillsBaseDir() (string, error) {
	dir := filepath.Join(os.TempDir(), embeddedSkillsPrefix+processOwnerTag())
	if err := ensurePrivateCacheDir(dir); err == nil {
		return dir, nil
	}
	// The predictable name can still be squatted on a shared host, and the
	// sticky bit prevents removing a foreign directory. A randomized private
	// directory keeps the bundled skills available instead of failing the
	// session; it is not shared between processes, which is the price of the
	// name being unusable.
	reapStaleFallbackBases(os.TempDir(), time.Now())
	fallback, err := os.MkdirTemp("", embeddedSkillsPrefix+processOwnerTag()+"-*")
	if err != nil {
		return "", fmt.Errorf("creating private skill cache: %w", err)
	}
	return fallback, nil
}

// reapStaleFallbackBases removes randomized fallback bases this user's earlier
// processes abandoned. Only this user's exact prefix is matched, and only when
// old enough that no live process is plausibly still reading it.
func reapStaleFallbackBases(tmpBase string, now time.Time) {
	prefix := embeddedSkillsPrefix + processOwnerTag() + "-"
	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleRetainedMaxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(tmpBase, entry.Name()))
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
	if !info.IsDir() {
		return fmt.Errorf("skill cache path %s is not a directory", dir)
	}
	if !cacheDirHasPrivatePermissions(info) {
		return fmt.Errorf("skill cache dir %s is accessible to other users", dir)
	}
	if !cacheDirOwnedByCurrentUser(info) {
		return fmt.Errorf("skill cache dir %s is not owned by the current user", dir)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
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
func materializeEmbeddedSkills(skillsFS fs.FS, base string) (string, string, error) {
	digest, err := digestSkillsFS(skillsFS)
	if err != nil {
		return "", "", fmt.Errorf("digesting embedded skills: %w", err)
	}
	reapStaleCopies(base, time.Now(), digest, embeddedSkillsCache.dir)
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

// cacheDirUsable reports whether a cached directory still holds the content it
// is supposed to. Lstat rejects a symlinked replacement, and the content is
// re-digested against the expected digest rather than trusted from the
// directory name, so a tampered or partial copy is republished.
func cacheDirUsable(dir, expected string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || expected == "" {
		return false
	}
	actual, err := digestSkillsFS(os.DirFS(dir))
	return err == nil && actual == expected
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
			if entry.IsDir() {
				continue
			}
			// A file or symlink squatting the name this process needs is removed
			// at once, whatever its age, so the next publish can take the name.
			_ = os.Remove(path)
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
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if entry.IsDir() {
			_ = os.RemoveAll(path)
			continue
		}
		// A file or symlink squatting a cache name is removed with Remove, which
		// deletes the link itself rather than anything it points at, so a later
		// publish can heal the name instead of falling back forever.
		_ = os.Remove(path)
	}
}

// publishedSkillsDir reports whether dest holds a complete copy of the content
// named by digest. Lstat, not Stat, so a symlink planted in the shared cache
// name is never followed; the content is then re-digested rather than trusted
// from the directory name.
func publishedSkillsDir(dest, digest string) bool {
	return cacheDirUsable(dest, digest)
}

// digestSkillsFS returns a stable hex digest over every path and byte in fsys.
// Paths are fs paths (slash-separated) and WalkDir visits in lexical order, so
// the digest is identical across platforms and processes.
func digestSkillsFS(fsys fs.FS) (string, error) {
	sum := sha256.New()
	entries := 0
	var total int64
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != "." {
			entries++
			if entries > maxEmbeddedSkillEntries {
				return fmt.Errorf("embedded skills: more than %d entries", maxEmbeddedSkillEntries)
			}
			if strings.Count(path, "/") > maxEmbeddedSkillDepth {
				return fmt.Errorf("embedded skills: %s is nested too deeply", path)
			}
		}
		if d.IsDir() {
			return nil
		}
		// Decided from the directory entry, before anything is opened: a
		// symlink points somewhere that can change, a FIFO blocks its open until
		// a writer arrives, and a device is not something to read. The
		// published name lives in a shared temp dir, so an occupant this process
		// did not write may be any of them.
		if !d.Type().IsRegular() {
			return fmt.Errorf("embedded skills: %s is not a regular file", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// Type() is zero when the filesystem does not report an entry type, and
		// IsRegular() treats zero as regular, so re-check the mode from Info
		// before opening: a FIFO must never reach the open below.
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
