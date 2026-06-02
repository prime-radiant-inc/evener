package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:skills
var embeddedSkills embed.FS

// ExtractEmbeddedSkills writes the embedded skills to a temporary directory
// and returns the path. The caller is responsible for cleanup (os.RemoveAll).
// The returned directory contains skill subdirectories (e.g. test-driven-development/SKILL.md)
// and is suitable for use as an extraDirs argument to DiscoverSkills.
func ExtractEmbeddedSkills() (string, error) {
	dir, err := os.MkdirTemp("", "serf-skills-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir for embedded skills: %w", err)
	}

	err = fs.WalkDir(embeddedSkills, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Strip the "skills/" prefix to get the relative path within the output dir.
		rel, err := filepath.Rel("skills", path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		outPath := filepath.Join(dir, rel)

		if d.IsDir() {
			return os.MkdirAll(outPath, 0o755)
		}

		data, err := embeddedSkills.ReadFile(path)
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
