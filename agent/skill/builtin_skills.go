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
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/internal/bundled"
)

// embeddedSkillsPrefix names the content-addressed cache directories that hold
// the bundled skills: the published copy and the private staging directory a
// publish writes before renaming into place.
const embeddedSkillsPrefix = "evener-skills-"

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
)

// embeddedSkillsCache holds the published bundled-skills directory and its
// scanned metadata, both process-wide under one mutex.
var embeddedSkillsCache struct {
	mu     sync.Mutex
	dir    string
	skills map[string]SkillMeta
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
		if cacheDirUsable(embeddedSkillsCache.dir) {
			return embeddedSkillsCache.dir, nil
		}
	}
	base, err := embeddedSkillsBaseDir()
	if err != nil {
		return "", err
	}
	dir, err := materializeEmbeddedSkills(bundled.Skills(), base)
	if err != nil {
		return "", err
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(dir, skills)
	embeddedSkillsCache.dir = dir
	embeddedSkillsCache.skills = skills
	return dir, nil
}

// defaultEmbeddedSkillsBaseDir returns the private per-user directory the cache
// lives in. It sits under the temp dir because a session confined to its
// worktree can still read temp, while the config root is sandbox-denylisted and
// the cache root is outside a restricted session's readable roots. It is
// namespaced by user and verified private, so another user on a shared host
// cannot occupy the name or read the published copy.
func defaultEmbeddedSkillsBaseDir() (string, error) {
	dir := filepath.Join(os.TempDir(), embeddedSkillsPrefix+processOwnerTag())
	if err := ensurePrivateCacheDir(dir); err != nil {
		return "", err
	}
	return dir, nil
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
	return nil
}

// materializeEmbeddedSkills publishes skillsFS into base under a directory named
// for the digest of its contents and returns that directory. A copy already
// published for the same content is reused; otherwise the tree is staged in a
// private directory and renamed into place, so a concurrent publisher leaves a
// complete copy and readers never see a partial tree. An occupant of the
// published name is adopted only when its content matches the digest; anything
// else leaves the private staging copy in place, because a session without its
// bundled skills is worse than one that did not reuse the cache.
func materializeEmbeddedSkills(skillsFS fs.FS, base string) (string, error) {
	reapStaleStaging(base, time.Now())
	digest, err := digestSkillsFS(skillsFS)
	if err != nil {
		return "", fmt.Errorf("digesting embedded skills: %w", err)
	}
	dest := filepath.Join(base, embeddedSkillsPrefix+digest)
	if publishedSkillsDir(dest, digest) {
		return dest, nil
	}

	staging, err := os.MkdirTemp(base, embeddedSkillsPrefix+"stage-*")
	if err != nil {
		return "", fmt.Errorf("staging embedded skills: %w", err)
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := copyEmbeddedSkills(skillsFS, staging); err != nil {
		return "", fmt.Errorf("extracting embedded skills: %w", err)
	}
	// A concurrent publisher for the same content may have finished while this
	// copy was made.
	if publishedSkillsDir(dest, digest) {
		return dest, nil
	}
	// Never rename onto an existing entry: on some platforms that replaces a
	// symlink or file this process just rejected. The base is private, so an
	// occupant that is not a valid copy is abandoned and the caller uses the
	// private copy already staged rather than extracting the tree again.
	if _, err := os.Lstat(dest); err == nil {
		keepStaging = true
		return staging, nil
	}
	if err := os.Rename(staging, dest); err != nil {
		if publishedSkillsDir(dest, digest) {
			return dest, nil
		}
		keepStaging = true
		return staging, nil
	}
	return dest, nil
}

// cacheDirUsable reports whether a cached directory is still the published copy
// it claims to be. Lstat rejects a symlinked replacement, and the digest encoded
// in the directory name is re-derived, so a tampered or stale copy is
// republished rather than trusted.
func cacheDirUsable(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	digest, ok := embeddedSkillsDigestFromDirName(filepath.Base(dir))
	if !ok {
		// A private fallback copy is not digest-named; there is no digest to
		// check it against.
		return true
	}
	actual, err := digestSkillsFS(os.DirFS(dir))
	return err == nil && actual == digest
}

// embeddedSkillsDigestFromDirName returns the content digest a published
// directory's name carries, when the name is a published-copy name.
func embeddedSkillsDigestFromDirName(name string) (string, bool) {
	digest, ok := strings.CutPrefix(name, embeddedSkillsPrefix)
	if !ok || len(digest) != sha256.Size*2 {
		return "", false
	}
	return digest, true
}

// reapStaleStaging removes staging directories earlier publishes abandoned, so a
// machine where the published name stays unusable does not accumulate one copy
// per run. A staging directory younger than the threshold may still belong to a
// live process and is left alone.
func reapStaleStaging(base string, now time.Time) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	prefix := embeddedSkillsPrefix + "stage-"
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleStagingMaxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, entry.Name()))
	}
}

// publishedSkillsDir reports whether dest holds a complete copy of the content
// named by digest. Lstat, not Stat, so a symlink planted in the shared cache
// name is never followed; the content is then re-digested rather than trusted
// from the directory name.
func publishedSkillsDir(dest, digest string) bool {
	info, err := os.Lstat(dest)
	if err != nil || !info.IsDir() {
		return false
	}
	actual, err := digestSkillsFS(os.DirFS(dest))
	return err == nil && actual == digest
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
		// Streamed and never read past the size the entry declared, so a file
		// growing under the walk cannot read unbounded and one that no longer
		// matches its own size is not the copy this is trying to recognize.
		// Closed here rather than deferred so each file is released before the
		// walk moves on.
		read, err := io.Copy(sum, io.LimitReader(file, maxEmbeddedSkillBytes))
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

// cloneSkillMetaMap copies the map and each entry's AllowedTools slice, so a
// caller mutating the returned metadata cannot reach into the process-wide
// cache.
func cloneSkillMetaMap(in map[string]SkillMeta) map[string]SkillMeta {
	out := make(map[string]SkillMeta, len(in))
	for name, meta := range in {
		meta.AllowedTools = append([]string(nil), meta.AllowedTools...)
		out[name] = meta
	}
	return out
}
