package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintHubEnvVars(t *testing.T) {
	var buf bytes.Buffer
	printHubEnvVars(&buf)
	out := buf.String()

	for _, want := range []string{
		"SERF_PROVIDERS_CONFIG",
		"SERF_STATE_DIR",
		"SERF_HUB_EDITOR_URL_TEMPLATE",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"OPENROUTER_API_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCurrentExecutable(t *testing.T) {
	// Normal case: os.Executable() should succeed and return a non-empty path.
	exe := currentExecutable()
	if exe == "" {
		t.Fatal("currentExecutable() returned empty string")
	}
	if !strings.Contains(exe, string(os.PathSeparator)) {
		t.Fatalf("expected an absolute or relative path with separator, got %q", exe)
	}
}

func TestResolveSerfBinaryPath(t *testing.T) {
	t.Run("explicit wins", func(t *testing.T) {
		got := resolveSerfBinaryPath("/usr/bin/serf", "", nil)
		if got != "/usr/bin/serf" {
			t.Fatalf("explicit = %q, want /usr/bin/serf", got)
		}
	})

	t.Run("sibling resolution", func(t *testing.T) {
		dir := t.TempDir()
		serfPath := filepath.Join(dir, "serf")
		if err := os.WriteFile(serfPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		// currentExecutable is the hub binary in the same directory.
		hubPath := filepath.Join(dir, "serf-hub")
		if err := os.WriteFile(hubPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		got := resolveSerfBinaryPath("", hubPath, func(string) (string, error) {
			return "", fmt.Errorf("should not call lookPath")
		})
		if got != serfPath {
			t.Fatalf("sibling resolution = %q, want %q", got, serfPath)
		}
	})

	t.Run("PATH resolution", func(t *testing.T) {
		got := resolveSerfBinaryPath("", "/no/such/hub", func(name string) (string, error) {
			if name != "serf" {
				t.Fatalf("lookPath called with %q, want serf", name)
			}
			return "/usr/local/bin/serf", nil
		})
		if got != "/usr/local/bin/serf" {
			t.Fatalf("PATH resolution = %q, want /usr/local/bin/serf", got)
		}
	})

	t.Run("lookPath error returns empty", func(t *testing.T) {
		got := resolveSerfBinaryPath("", "/no/such/hub", func(string) (string, error) {
			return "", fmt.Errorf("not found")
		})
		if got != "" {
			t.Fatalf("lookPath error = %q, want empty", got)
		}
	})

	t.Run("nil lookPath uses exec.LookPath", func(t *testing.T) {
		// Create a temp directory with a "serf" binary and put it on PATH.
		bindir := t.TempDir()
		serfPath := filepath.Join(bindir, "serf")
		if err := os.WriteFile(serfPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldPath := os.Getenv("PATH")
		defer os.Setenv("PATH", oldPath)
		os.Setenv("PATH", bindir)

		got := resolveSerfBinaryPath("", "/no/such/hub", nil)
		if got != serfPath {
			t.Fatalf("nil lookPath resolution = %q, want %q", got, serfPath)
		}
	})
}
