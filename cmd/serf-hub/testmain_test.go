package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "serf-hub-test-env-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf-hub test env: %v\n", err)
		os.Exit(1)
	}
	for _, dir := range []string{"home", "config", "state", "cache", "codex"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "serf-hub test env: %v\n", err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}

	_ = os.Setenv("HOME", filepath.Join(root, "home"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	_ = os.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	_ = os.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	for _, key := range []string{
		"SERF_MODEL",
		"SERF_REASONING_EFFORT",
		"SERF_API_TOKEN",
		"SERF_HUB_EDITOR_URL_TEMPLATE",
		"SERF_RUN_DIR",
		"SERF_STATE_DIR",
		"SERF_HUB_TOKEN",
		"SERF_HUB_SPAWNED",
		"SERF_HUB_SPAWNED_CODEX",
	} {
		_ = os.Unsetenv(key)
	}

	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := fspaths.CanonicalizeDir(dir)
	if err != nil {
		t.Fatalf("canonicalize temp dir %s: %v", dir, err)
	}
	return resolved
}
