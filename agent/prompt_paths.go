package agent

import (
	"os"
	"path/filepath"
)

// GlobalPromptsDir returns the path to the global prompts directory.
// Uses XDG_CONFIG_HOME if set, otherwise ~/.config.
func GlobalPromptsDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
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
