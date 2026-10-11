package hub

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An explicit --config names a file the operator means the hub to load. These
// tests drive the real startup seam (runMain / runAttach over defaultMainDeps)
// with the step after config load replaced by a sentinel, so a pass proves the
// refusal happened at load instead of startup silently continuing on
// DefaultConfig().

func TestRunMainExplicitConfigMissingRefuses(t *testing.T) {
	deps := defaultMainDeps()
	reached := errors.New("startup continued past config load")
	deps.ensureDirs = func() error { return reached }

	missing := filepath.Join(t.TempDir(), "absent.toml")
	var stderr bytes.Buffer
	err := runMain([]string{"--config", missing}, &stderr, deps)
	if err == nil {
		t.Fatalf("runMain accepted a missing explicit --config; stderr=%s", stderr.String())
	}
	if errors.Is(err, reached) {
		t.Fatalf("runMain continued startup after a missing explicit --config; stderr=%s", stderr.String())
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal %v does not wrap os.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("refusal %v does not name %q", err, missing)
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr %q does not name %q", stderr.String(), missing)
	}
}

func TestRunMainExplicitConfigUnparseableRefuses(t *testing.T) {
	deps := defaultMainDeps()
	reached := errors.New("startup continued past config load")
	deps.ensureDirs = func() error { return reached }

	path := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(path, []byte("addr = \"unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	err := runMain([]string{"--config", path}, &stderr, deps)
	if err == nil {
		t.Fatalf("runMain accepted an unparseable explicit --config; stderr=%s", stderr.String())
	}
	if errors.Is(err, reached) {
		t.Fatalf("runMain continued startup after an unparseable explicit --config; stderr=%s", stderr.String())
	}
	if !strings.Contains(err.Error(), "parse config") || !strings.Contains(err.Error(), path) {
		t.Fatalf("refusal %v does not name the unparseable config %q", err, path)
	}
	if !strings.Contains(stderr.String(), path) {
		t.Fatalf("stderr %q does not name %q", stderr.String(), path)
	}
}

func TestRunAttachExplicitConfigMissingRefuses(t *testing.T) {
	// Hermetic: the attach bridge resolves its state root and token from the
	// config, so an implicit fallback here must not read the real user's state.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	deps := defaultMainDeps()
	deps.stdin = strings.NewReader("")
	deps.stdout = &bytes.Buffer{}

	missing := filepath.Join(t.TempDir(), "absent.toml")
	var stderr bytes.Buffer
	err := runAttach([]string{"--stdio", "--config", missing}, &stderr, deps)
	if err == nil {
		t.Fatalf("runAttach accepted a missing explicit --config; stderr=%s", stderr.String())
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("refusal %v does not name %q", err, missing)
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Fatalf("stderr %q does not name %q", stderr.String(), missing)
	}
}
