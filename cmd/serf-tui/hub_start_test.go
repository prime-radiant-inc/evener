package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHubAddressNormalization(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		baseURL  string
		bindAddr string
		local    bool
	}{
		{
			name:     "host port",
			raw:      "127.0.0.1:9999",
			baseURL:  "http://127.0.0.1:9999",
			bindAddr: "127.0.0.1:9999",
			local:    true,
		},
		{
			name:     "localhost url",
			raw:      "http://localhost:9180/",
			baseURL:  "http://localhost:9180",
			bindAddr: "localhost:9180",
			local:    true,
		},
		{
			name:     "remote url",
			raw:      "http://example.com:9180",
			baseURL:  "http://example.com:9180",
			bindAddr: "example.com:9180",
			local:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeHubAddress(tt.raw)
			if err != nil {
				t.Fatalf("normalizeHubAddress: %v", err)
			}
			if got.BaseURL != tt.baseURL || got.BindAddr != tt.bindAddr || got.IsLocal != tt.local {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestResolveHubBinaryPrefersExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "custom-hub")
	writeExecutable(t, explicit)
	sibling := filepath.Join(dir, "serf-hub")
	writeExecutable(t, sibling)

	got, err := resolveHubBinary(explicit, filepath.Join(dir, "serf-tui"), func(string) (string, error) {
		return "", errors.New("should not search PATH")
	})
	if err != nil {
		t.Fatalf("resolveHubBinary: %v", err)
	}
	if got != explicit {
		t.Fatalf("got %q, want %q", got, explicit)
	}
}

func TestResolveHubBinaryPrefersSiblingBeforePath(t *testing.T) {
	dir := t.TempDir()
	sibling := filepath.Join(dir, "serf-hub")
	writeExecutable(t, sibling)
	pathHub := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, pathHub)

	got, err := resolveHubBinary("", filepath.Join(dir, "serf-tui"), func(string) (string, error) {
		return pathHub, nil
	})
	if err != nil {
		t.Fatalf("resolveHubBinary: %v", err)
	}
	if got != sibling {
		t.Fatalf("got %q, want %q", got, sibling)
	}
}

func TestResolveHubBinaryFallsBackToPath(t *testing.T) {
	pathHub := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, pathHub)

	got, err := resolveHubBinary("", filepath.Join(t.TempDir(), "serf-tui"), func(string) (string, error) {
		return pathHub, nil
	})
	if err != nil {
		t.Fatalf("resolveHubBinary: %v", err)
	}
	if got != pathHub {
		t.Fatalf("got %q, want %q", got, pathHub)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
