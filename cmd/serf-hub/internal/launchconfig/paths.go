package launchconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/identifier"
)

// Paths bundles the canonical layer-file paths for a given hub state root
// and cwd.
type Paths struct {
	Global        string             // <root>/launch.toml
	Repo          string             // <cwd>/.serf/launch.toml
	Project       identifier.Project // resolved canonical project identity
	ProjectFile   string             // <cwd>/.serf/launch.local.toml
	LegacyProject string             // <root>/projects/<id>/launch.toml
	Meta          string             // <root>/projects/<id>/meta.toml
}

// PathsFor computes layer paths given the hub state root (typically
// ~/.serf) and the working directory. Repo/ProjectFile point at the active
// content root (cwd, the checked-out worktree) so config content always
// reflects the active branch's checkout. LegacyProject/Meta point at the
// stable identity root so trust metadata and legacy
// project state survive switching between a repo's linked worktrees. See
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Active content root vs stable identity root").
func PathsFor(stateRoot, cwd string) (Paths, error) {
	project, err := identifier.ResolveProject(cwd)
	if err != nil {
		return Paths{}, err
	}
	projectDir := filepath.Join(stateRoot, "projects", project.ID)
	return Paths{
		Global:        filepath.Join(stateRoot, "launch.toml"),
		Repo:          filepath.Join(cwd, ".serf", "launch.toml"),
		Project:       project,
		ProjectFile:   filepath.Join(cwd, ".serf", "launch.local.toml"),
		LegacyProject: filepath.Join(projectDir, "launch.toml"),
		Meta:          filepath.Join(projectDir, "meta.toml"),
	}, nil
}

// ValidateRepoRelativePath ensures `path` (when resolved against repoRoot)
// stays inside repoRoot. Absolute paths and `..` escapes are rejected.
func ValidateRepoRelativePath(repoRoot, path string) error {
	return validateRepoRelativePath(repoRoot, path, filepath.Rel)
}

func validateRepoRelativePath(repoRoot, path string, relPath func(string, string) (string, error)) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute path not allowed in repo layer: %q", path)
	}
	clean := filepath.Clean(filepath.Join(repoRoot, path))
	rel, err := relPath(repoRoot, clean)
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
