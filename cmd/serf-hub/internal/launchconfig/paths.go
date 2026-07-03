package launchconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/internal/gitpath"
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
	Global        string // <root>/launch.toml
	Repo          string // <cwd>/.serf/launch.toml
	Project       string // <cwd>/.serf/launch.local.toml
	LegacyProject string // <root>/projects/<id>/launch.toml
	Meta          string // <root>/projects/<id>/meta.toml
}

// PathsFor computes layer paths given the hub state root (typically
// ~/.serf) and the working directory. Repo/Project point at the active
// content root (cwd, the checked-out worktree) so config content always
// reflects the active branch's checkout. LegacyProject/Meta point at the
// stable identity root (identityProjectDir) so trust metadata and legacy
// project state survive switching between a repo's linked worktrees. See
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Active content root vs stable identity root").
func PathsFor(stateRoot, cwd string) Paths {
	projectDir := identityProjectDir(stateRoot, cwd)
	return Paths{
		Global:        filepath.Join(stateRoot, "launch.toml"),
		Repo:          filepath.Join(cwd, ".serf", "launch.toml"),
		Project:       filepath.Join(cwd, ".serf", "launch.local.toml"),
		LegacyProject: filepath.Join(projectDir, "launch.toml"),
		Meta:          filepath.Join(projectDir, "meta.toml"),
	}
}

// identityProjectDir returns <stateRoot>/projects/<id>, where <id> is the
// ProjectID of the stable identity root: the main repository root when cwd
// resolves to one (so every linked worktree of the same repo shares one
// trust record and one legacy-project state directory), falling back to cwd
// itself when it is not inside a git repository.
func identityProjectDir(stateRoot, cwd string) string {
	identityRoot := gitpath.ResolveMainRepoRootLocal(cwd)
	if identityRoot == "" {
		identityRoot = cwd
	}
	return filepath.Join(stateRoot, "projects", ProjectID(identityRoot))
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
