package promptpath

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/envvars"
)

// GlobalPromptsDir returns the path to the global prompts directory.
// Uses XDG_CONFIG_HOME if set, otherwise ~/.config.
func GlobalPromptsDir() string {
	return globalPromptsDir(envvars.XDGConfigHome.Getenv(), os.UserHomeDir)
}

// globalPromptsDir keeps environment and home-directory lookup at the boundary
// so path construction remains deterministic for callers that supply both.
func globalPromptsDir(xdgConfigHome string, userHomeDir func() (string, error)) string {
	dir := xdgConfigHome
	if dir == "" {
		home, err := userHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "prompts")
}

// ProjectPromptsDir returns the prompts directory for a project, given the git root.
func ProjectPromptsDir(gitRoot string) string {
	if gitRoot == "" {
		return ""
	}
	return filepath.Join(gitRoot, ".serf", "prompts")
}
