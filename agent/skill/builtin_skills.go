package skill

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/serf/internal/bundled"
)

var (
	embeddedSkillsOnce sync.Once
	embeddedSkillsDir  string
	errEmbeddedSkills  error
)

// EmbeddedSkillsDir returns a directory containing the embedded skills,
// extracting them exactly once per process. The embedded content is immutable
// and identical for every caller, so the extracted tree is shared and lives for
// the process lifetime — callers MUST treat it as read-only and MUST NOT remove
// it. Use this instead of ExtractEmbeddedSkills on hot paths (e.g. per-session
// initialization) where re-materializing the same files is pure overhead.
func EmbeddedSkillsDir() (string, error) {
	embeddedSkillsOnce.Do(func() {
		embeddedSkillsDir, errEmbeddedSkills = ExtractEmbeddedSkills()
	})
	return embeddedSkillsDir, errEmbeddedSkills
}

// ExtractEmbeddedSkills writes the embedded skills to a temporary directory
// and returns the path. The caller is responsible for cleanup (os.RemoveAll).
// The returned directory contains skill subdirectories (e.g. test-driven-development/SKILL.md)
// and is suitable for use as an extraDirs argument to DiscoverSkills.
func ExtractEmbeddedSkills() (string, error) {
	return extractEmbeddedSkills(bundled.Skills(), os.MkdirTemp)
}

// extractEmbeddedSkills is the filesystem-backed extraction implementation.
// Keeping its immutable source and temporary-directory factory explicit lets
// tests exercise filesystem failures without changing the exported bundled
// asset behavior.
func extractEmbeddedSkills(skillsFS fs.FS, mkdirTemp func(string, string) (string, error)) (string, error) {
	dir, err := mkdirTemp("", "serf-skills-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir for embedded skills: %w", err)
	}

	err = fs.WalkDir(skillsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == "." {
			return nil
		}

		outPath := filepath.Join(dir, path)

		if d.IsDir() {
			return os.MkdirAll(outPath, 0o755)
		}

		data, err := fs.ReadFile(skillsFS, path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		return os.WriteFile(outPath, data, 0o644)
	})

	if err != nil {
		_ = os.RemoveAll(dir) // best-effort cleanup of partial extraction; the extract error is what matters
		return "", fmt.Errorf("extracting embedded skills: %w", err)
	}

	return dir, nil
}

var embeddedSkillsCache struct {
	mu     sync.Mutex
	dir    string
	skills map[string]SkillMeta
}

// EmbeddedSkills returns the bundled skills as filesystem-backed metadata.
// The materialized tree is shared within the process so session creation does
// not repeatedly extract the same embedded files.
func EmbeddedSkills() (map[string]SkillMeta, error) {
	embeddedSkillsCache.mu.Lock()
	defer embeddedSkillsCache.mu.Unlock()

	if embeddedSkillsCache.dir != "" {
		if _, err := os.Stat(embeddedSkillsCache.dir); err == nil {
			return cloneSkillMetaMap(embeddedSkillsCache.skills), nil
		}
	}

	dir, err := ExtractEmbeddedSkills()
	if err != nil {
		return nil, err
	}
	skills := make(map[string]SkillMeta)
	ScanSkillsDir(dir, skills)
	embeddedSkillsCache.dir = dir
	embeddedSkillsCache.skills = skills
	return cloneSkillMetaMap(skills), nil
}

func cloneSkillMetaMap(in map[string]SkillMeta) map[string]SkillMeta {
	out := make(map[string]SkillMeta, len(in))
	maps.Copy(out, in)
	return out
}
