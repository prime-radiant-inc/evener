package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	authopenai "primeradiant.com/serf/internal/auth/openai"
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
	if stateHome, ok := lookup("XDG_STATE_HOME"); ok && strings.TrimSpace(stateHome) != "" {
		return authopenai.DefaultStateDirWithStateHome(stateHome)
	}
	if goos == "windows" {
		if userProfile, ok := lookup("USERPROFILE"); ok && strings.TrimSpace(userProfile) != "" {
			return filepath.Join(strings.TrimSpace(userProfile), ".local", "state", "serf")
		}
		drive, hasDrive := lookup("HOMEDRIVE")
		path, hasPath := lookup("HOMEPATH")
		if hasDrive && hasPath && strings.TrimSpace(drive) != "" && strings.TrimSpace(path) != "" {
			return filepath.Join(strings.TrimSpace(drive)+strings.TrimSpace(path), ".local", "state", "serf")
		}
		return filepath.Join(os.TempDir(), ".local", "state", "serf")
	}
	if home, ok := lookup("HOME"); ok && strings.TrimSpace(home) != "" {
		return filepath.Join(strings.TrimSpace(home), ".local", "state", "serf")
	}
	return filepath.Join(os.TempDir(), ".local", "state", "serf")
}
