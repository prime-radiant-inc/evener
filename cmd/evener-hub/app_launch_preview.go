package hub

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/identifier"
)

// launchPreview keeps the discovery directory alive with its resolved layers.
// Successful callers defer cleanup for the entire preview/discovery lifetime.
type launchPreview struct {
	cwd      string
	resolved launchconfig.Resolved
	cleanup  func()
}

func prepareLaunchPreview(configRoot, cwd string, overrides launchconfig.Layer) (launchPreview, error) {
	if strings.TrimSpace(cwd) == "" {
		// Without a chosen directory only user layers and launch overrides exist.
		resolved, err := launchconfig.ResolveUserOnly(configRoot, overrides)
		if err != nil {
			return launchPreview{}, err
		}
		return launchPreview{resolved: resolved, cleanup: func() {}}, nil
	}
	previewCWD, project, cleanup, err := launchPreviewCWD(cwd)
	if err != nil {
		return launchPreview{}, err
	}
	resolved, err := launchconfig.ResolveWithProject(configRoot, previewCWD, project, overrides)
	if err != nil {
		cleanup()
		return launchPreview{}, err
	}
	return launchPreview{cwd: previewCWD, resolved: resolved, cleanup: cleanup}, nil
}

func launchPreviewCWD(path string) (string, identifier.Project, func(), error) {
	cwd, err := fspaths.CanonicalizeDir(path)
	if err == nil {
		project, projectErr := identifier.ResolveProject(cwd)
		if projectErr != nil {
			return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + projectErr.Error())
		}
		return cwd, project, func() {}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + err.Error())
	}

	// launchconfig.Resolve needs an existing directory to derive project
	// identity. Put the temporary resolver directory under the nearest existing
	// ancestor so project identity follows the eventual target, while the
	// target itself and its ancestor's local files remain untouched.
	requested := filepath.Clean(strings.TrimSpace(path))
	ancestor := requested
	for {
		info, statErr := os.Stat(ancestor)
		if statErr == nil {
			if !info.IsDir() {
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: nearest existing path is not a directory")
			}
			existingAncestor := ancestor
			ancestor, statErr = fspaths.CanonicalizeDir(ancestor)
			if statErr != nil {
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + statErr.Error())
			}
			missingSuffix, relErr := filepath.Rel(existingAncestor, requested)
			if relErr != nil {
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + relErr.Error())
			}
			requestedCanonical := filepath.Join(ancestor, missingSuffix)
			previewDir, mkdirErr := os.MkdirTemp(ancestor, "evener-plugin-preview-")
			if mkdirErr != nil {
				return "", identifier.Project{}, nil, mkdirErr
			}
			probeProject, projectErr := identifier.ResolveProject(previewDir)
			if projectErr != nil {
				_ = os.RemoveAll(previewDir)
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + projectErr.Error())
			}
			project := probeProject
			if probeProject.CanonicalPath == previewDir {
				project = identifier.ProjectFromCanonicalPath(requestedCanonical)
			}
			return previewDir, project, func() { _ = os.RemoveAll(previewDir) }, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + statErr.Error())
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: no existing ancestor")
		}
		ancestor = parent
	}
}
