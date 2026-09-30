package launchconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/identifier"
)

// Paths bundles the canonical layer-file paths for a given hub state root
// and cwd.
type Paths struct {
	Global        string             // <root>/launch.toml
	Repo          string             // <cwd>/.evener/launch.toml
	Project       identifier.Project // resolved canonical project identity
	ProjectFile   string             // <cwd>/.evener/launch.local.toml
	LegacyProject string             // <root>/projects/<id>/launch.toml
	Meta          string             // <root>/projects/<id>/meta.toml
}

// PathsFor computes layer paths given the hub state root (typically
// ~/.evener) and the working directory. Repo/ProjectFile point at the active
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
	return pathsForProject(stateRoot, cwd, project), nil
}

func pathsForProject(stateRoot, cwd string, project identifier.Project) Paths {
	return Paths{
		// The user-level layers live under the user config root; an empty root
		// (unresolvable) must yield no path rather than a relative one, so
		// joining goes through userdirs.Subdir. The repo and project layers are
		// anchored on cwd, which is always an absolute working directory.
		Global:        userdirs.Subdir(stateRoot, "launch.toml"),
		Repo:          filepath.Join(cwd, ".evener", "launch.toml"),
		Project:       project,
		ProjectFile:   filepath.Join(cwd, ".evener", "launch.local.toml"),
		LegacyProject: userdirs.Subdir(stateRoot, filepath.Join("projects", project.ID, "launch.toml")),
		Meta:          userdirs.Subdir(stateRoot, filepath.Join("projects", project.ID, "meta.toml")),
	}
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
