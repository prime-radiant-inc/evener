package skill

import (
	"fmt"
	"io/fs"
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
	dir, err := os.MkdirTemp("", "serf-skills-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir for embedded skills: %w", err)
	}

	skillsFS := bundled.Skills()
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
