package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionString_NotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("Version should not be empty")
	}
}

func TestResolveSerfBinaryPathExplicitWins(t *testing.T) {
	// When --serf is set explicitly, the resolver returns it untouched
	// even if no sibling exists and PATH lookup would fail.
	got := resolveSerfBinaryPath("/custom/serf", "", func(string) (string, error) {
		return "", errors.New("should not be called")
	})
	if got != "/custom/serf" {
		t.Fatalf("got %q, want /custom/serf", got)
	}
}

func TestResolveSerfBinaryPathPrefersSiblingOverPath(t *testing.T) {
	dir := t.TempDir()
	sibling := filepath.Join(dir, "serf")
	writeHubExecutable(t, sibling)

	pathSerf := filepath.Join(t.TempDir(), "serf")
	writeHubExecutable(t, pathSerf)

	got := resolveSerfBinaryPath("", filepath.Join(dir, "serf-hub"), func(string) (string, error) {
		return pathSerf, nil
	})
	if got != sibling {
		t.Fatalf("got %q, want sibling %q", got, sibling)
	}
}

func TestResolveSerfBinaryPathFallsBackToPath(t *testing.T) {
	// No sibling exists next to the hub binary; resolver should defer
	// to the lookPath hook.
	dir := t.TempDir()
	hub := filepath.Join(dir, "serf-hub")
	writeHubExecutable(t, hub)

	pathSerf := filepath.Join(t.TempDir(), "serf")
	writeHubExecutable(t, pathSerf)

	got := resolveSerfBinaryPath("", hub, func(name string) (string, error) {
		if name != "serf" {
			t.Fatalf("lookPath called with %q, want %q", name, "serf")
		}
		return pathSerf, nil
	})
	if got != pathSerf {
		t.Fatalf("got %q, want PATH-resolved %q", got, pathSerf)
	}
}

func TestResolveSerfBinaryPathReturnsEmptyWhenNothingFound(t *testing.T) {
	// When neither a sibling nor a PATH entry exists, the resolver
	// returns "" so HubSpawner falls back to invoking plain "serf"
	// and lets exec.Command surface the runtime error.
	got := resolveSerfBinaryPath("", filepath.Join(t.TempDir(), "serf-hub"), func(string) (string, error) {
		return "", os.ErrNotExist
	})
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

func writeHubExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
