package launchconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// ProjectID returns the 16-hex-char stable identifier used for the
// hub-side per-project state directory.
func ProjectID(cwd string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:])[:16]
}

// Paths bundles the canonical layer-file paths for a given hub state root
// and cwd.
type Paths struct {
	Global  string // <root>/launch.toml
	Repo    string // <cwd>/.serf/launch.toml
	Project string // <root>/projects/<id>/launch.toml
	Meta    string // <root>/projects/<id>/meta.toml
}

// PathsFor computes layer paths given the hub state root (typically
// ~/.serf) and the working directory.
func PathsFor(stateRoot, cwd string) Paths {
	id := ProjectID(cwd)
	projectDir := filepath.Join(stateRoot, "projects", id)
	return Paths{
		Global:  filepath.Join(stateRoot, "launch.toml"),
		Repo:    filepath.Join(cwd, ".serf", "launch.toml"),
		Project: filepath.Join(projectDir, "launch.toml"),
		Meta:    filepath.Join(projectDir, "meta.toml"),
	}
}

// ValidateRepoRelativePath ensures `path` (when resolved against repoRoot)
// stays inside repoRoot. Absolute paths and `..` escapes are rejected.
func ValidateRepoRelativePath(repoRoot, path string) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute path not allowed in repo layer: %q", path)
	}
	clean := filepath.Clean(filepath.Join(repoRoot, path))
	rel, err := filepath.Rel(repoRoot, clean)
	if err != nil {
		return fmt.Errorf("path resolution: %w", err)
	}
	slashRel := filepath.ToSlash(rel)
	if slashRel == ".." || strings.HasPrefix(slashRel, "../") {
		return fmt.Errorf("path escapes repo: %q", path)
	}
	return nil
}

// ValidateAbsolutePath errors when path is not absolute.
func ValidateAbsolutePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute path required: %q", path)
	}
	return nil
}
