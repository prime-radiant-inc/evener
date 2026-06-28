package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintHubEnvVars(t *testing.T) {
	var buf bytes.Buffer
	printHubEnvVars(&buf)
	out := buf.String()

	// Each documented var must appear on its own line alongside a
	// non-empty Summary; otherwise dropping the description from the
	// format string would silently pass.
	wantSummary := map[string]string{
		"SERF_PROVIDERS_CONFIG":        "Path to providers.toml.",
		"SERF_STATE_DIR":               "Overrides the Serf state root.",
		"SERF_HUB_EDITOR_URL_TEMPLATE": "Open-in-editor URL template; use {path} for the encoded path.",
		"OPENAI_API_KEY":               "OpenAI API key.",
		"ANTHROPIC_API_KEY":            "Anthropic API key.",
		"GEMINI_API_KEY":               "Google Gemini API key; checked before GOOGLE_API_KEY.",
		"GOOGLE_API_KEY":               "Google Gemini API key fallback.",
		"OPENROUTER_API_KEY":           "OpenRouter API key.",
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for name, summary := range wantSummary {
		var line string
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), name) {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("output missing %q:\n%s", name, out)
			continue
		}
		if !strings.Contains(line, summary) {
			t.Errorf("line for %q missing summary %q: got %q", name, summary, line)
		}
		// The description must follow the name, not just appear somewhere
		// on the line: trimming the name must leave non-empty help text.
		idx := strings.Index(line, name)
		rest := strings.TrimSpace(line[idx+len(name):])
		if rest == "" {
			t.Errorf("line for %q has no description text: got %q", name, line)
		}
	}
}

func TestCurrentExecutable(t *testing.T) {
	// The documented contract is to prefer os.Executable(), which always
	// returns an absolute path. Verify absoluteness so that a mutation
	// that drops the os.Executable() branch and falls back to os.Args[0]
	// (which may be relative) is detected.
	exe := currentExecutable()
	if exe == "" {
		t.Fatal("currentExecutable() returned empty string")
	}
	if !filepath.IsAbs(exe) {
		t.Fatalf("currentExecutable() = %q, want an absolute path (os.Executable() preference)", exe)
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
			return "", errors.New("should not call lookPath")
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
			return "", errors.New("not found")
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
