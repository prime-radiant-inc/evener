package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/evener/internal/bundled"
)

// embeddedSkillsPrefix names the content-addressed cache directories that hold
// the bundled skills: the published copy and the private staging directory a
// publish writes before renaming into place.
const embeddedSkillsPrefix = "evener-skills-"

// maxEmbeddedSkillBytes bounds a single file the digest will read. The bundled
// skills are small markdown files; the bound exists because the published name
// lives in the shared temp dir and an occupant this process did not write could
// be a device or a file that never ends.
const maxEmbeddedSkillBytes = 1 << 20

// embeddedSkillsCache holds the published bundled-skills directory and its
// scanned metadata, both process-wide under one mutex.
var embeddedSkillsCache struct {
	mu     sync.Mutex
	dir    string
	skills map[string]SkillMeta
}

// embeddedSkillsBaseDir is where the content-addressed bundled-skills cache
// lives. It defaults to the temp dir because a session confined to its worktree
// can still read temp, while the config root is sandbox-denylisted and the
// cache root is outside a restricted session's readable roots.
var embeddedSkillsBaseDir = os.TempDir

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
		if _, err := os.Stat(embeddedSkillsCache.dir); err == nil {
			return embeddedSkillsCache.dir, nil
		}
	}
	dir, err := materializeEmbeddedSkills(bundled.Skills(), embeddedSkillsBaseDir())
	if err != nil {
		return "", err
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(dir, skills)
	embeddedSkillsCache.dir = dir
	embeddedSkillsCache.skills = skills
	return dir, nil
}

// materializeEmbeddedSkills publishes skillsFS into base under a directory named
// for the digest of its contents and returns that directory. A copy already
// published for the same content is reused; otherwise the tree is staged in a
// private directory and renamed into place, so a concurrent publisher leaves a
// complete copy and readers never see a partial tree. An occupant of the
// published name is adopted only when its content matches the digest; anything
// else falls back to a private extraction, because a session without its bundled
// skills is worse than one that did not reuse the cache.
func materializeEmbeddedSkills(skillsFS fs.FS, base string) (string, error) {
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
	defer func() { _ = os.RemoveAll(staging) }() // no-op once the rename succeeds

	if err := copyEmbeddedSkills(skillsFS, staging); err != nil {
		return "", fmt.Errorf("extracting embedded skills: %w", err)
	}
	if err := os.Rename(staging, dest); err != nil {
		// A concurrent publisher for the same content may have won the rename.
		if publishedSkillsDir(dest, digest) {
			return dest, nil
		}
		return fallbackEmbeddedSkills(skillsFS, err)
	}
	return dest, nil
}

// publishedSkillsDir reports whether dest holds a complete copy of the content
// named by digest. It re-derives the digest rather than trusting the directory
// name, so a foreign or tampered occupant of the published name is rejected.
func publishedSkillsDir(dest, digest string) bool {
	info, err := os.Stat(dest)
	if err != nil || !info.IsDir() {
		return false
	}
	actual, err := digestSkillsFS(os.DirFS(dest))
	return err == nil && actual == digest
}

// fallbackEmbeddedSkills hands the caller a private copy when the published name
// cannot be used. cause is the publish failure; it is wrapped so the reason the
// shared cache was skipped is not lost.
func fallbackEmbeddedSkills(skillsFS fs.FS, cause error) (string, error) {
	dir, err := extractEmbeddedSkills(skillsFS, os.MkdirTemp)
	if err != nil {
		return "", fmt.Errorf("extracting embedded skills (published copy failed: %w): %w", cause, err)
	}
	return dir, nil
}

// digestSkillsFS returns a stable hex digest over every path and byte in fsys.
// Paths are fs paths (slash-separated) and WalkDir visits in lexical order, so
// the digest is identical across platforms and processes.
func digestSkillsFS(fsys fs.FS) (string, error) {
	sum := sha256.New()
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Decided from the directory entry, before anything is opened: a
		// symlink points somewhere that can change, a FIFO blocks its open until
		// a writer arrives, and a device is not something to read. The published
		// name lives in the shared temp dir, so an occupant this process did not
		// write may be any of them.
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
		file, err := fsys.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", path, info.Size())
		// Streamed and never read past the size the entry declared, so a file
		// growing under the walk cannot read unbounded and one that no longer
		// matches its own size is not the copy this is trying to recognize.
		read, err := io.Copy(sum, io.LimitReader(file, maxEmbeddedSkillBytes))
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

func cloneSkillMetaMap(in map[string]SkillMeta) map[string]SkillMeta {
	out := make(map[string]SkillMeta, len(in))
	maps.Copy(out, in)
	return out
}
