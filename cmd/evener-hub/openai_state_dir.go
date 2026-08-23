package hub

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/envvars"
)

func openAIStateDirFromEnvList(env []string) string {
	return openAIStateDirFromLookup(runtime.GOOS, func(key string) (string, bool) {
		return envLookup(env, key)
	})
}

func openAIStateDirFromEnvMap(env map[string]string) string {
	return openAIStateDirFromLookup(runtime.GOOS, func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
}

func openAIStateDirFromLookup(goos string, lookup func(string) (string, bool)) string {
	if stateHome, ok := lookup(envvars.XDGStateHome.Name); ok && strings.TrimSpace(stateHome) != "" {
		return authopenai.DefaultStateDirWithStateHome(stateHome)
	}
	if goos == "windows" {
		if userProfile, ok := lookup(envvars.UserProfile.Name); ok && strings.TrimSpace(userProfile) != "" {
			return filepath.Join(strings.TrimSpace(userProfile), ".local", "state", "evener")
		}
		drive, hasDrive := lookup(envvars.HomeDrive.Name)
		path, hasPath := lookup(envvars.HomePath.Name)
		if hasDrive && hasPath && strings.TrimSpace(drive) != "" && strings.TrimSpace(path) != "" {
			return filepath.Join(strings.TrimSpace(drive)+strings.TrimSpace(path), ".local", "state", "evener")
		}
		return filepath.Join(os.TempDir(), ".local", "state", "evener")
	}
	if home, ok := lookup(envvars.Home.Name); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(strings.TrimSpace(home), ".local", "state", "evener")
	}
	return filepath.Join(os.TempDir(), ".local", "state", "evener")
}
