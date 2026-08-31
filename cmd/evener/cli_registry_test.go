package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantStrayOAuthRecord leaves an auth/<name>.json under the state root for a
// name that is no instance on the Codex transport. Nothing reads such a
// record, so every command that loads the registry must say so (spec §9.5,
// §14.1). It returns the file it wrote.
func plantStrayOAuthRecord(t *testing.T, name string) string {
	t.Helper()
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		t.Fatal("the test environment must pin XDG_STATE_HOME")
	}
	dir := filepath.Join(stateHome, "evener", "auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Both registry-reading commands share one loader, so both announce the
// registry's notices once, on their own stderr.
func TestRegistryNoticesReachEveryCommandsStderr(t *testing.T) {
	t.Run("models", func(t *testing.T) {
		modelsTestEnv(t)
		path := plantStrayOAuthRecord(t, "left-behind")
		var stdout, stderr bytes.Buffer
		if err := runModels([]string{"list", "--provider", "anthropic"}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("models list: %v (%s)", err, stderr.String())
		}
		assertStrayNotice(t, stderr.String(), path)
	})

	t.Run("providers", func(t *testing.T) {
		providersTestEnv(t, nil)
		path := plantStrayOAuthRecord(t, "left-behind")
		var stdout, stderr bytes.Buffer
		if err := runProviders([]string{"list"}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("providers list: %v (%s)", err, stderr.String())
		}
		assertStrayNotice(t, stderr.String(), path)
	})
}

func assertStrayNotice(t *testing.T, stderr, path string) {
	t.Helper()
	if !strings.Contains(stderr, "warning: stray OAuth record "+path) {
		t.Fatalf("the stray-record notice must reach stderr (spec §9.5):\n%s", stderr)
	}
	if !strings.Contains(stderr, "evener openai logout --instance left-behind") {
		t.Fatalf("the notice must say how to remove it:\n%s", stderr)
	}
}
