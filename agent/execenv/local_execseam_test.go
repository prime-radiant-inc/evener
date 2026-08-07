package execenv

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveOSVersionFallsBackWhenProbeFails forces the uname/ver probe command
// to fail and asserts resolveOSVersion returns the GOOS/GOARCH fallback string.
// This drives the previously-uncovered probe-error path + fallback return
// through the execCommandContext seam, without depending on the host's uname.
func TestResolveOSVersionFallsBackWhenProbeFails(t *testing.T) {
	orig := execCommandContext
	t.Cleanup(func() { execCommandContext = orig })
	missing := filepath.Join(t.TempDir(), "no-such-probe-binary")
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// A path that cannot exist, so .Output() returns a start error and the
		// switch arm falls through to the GOOS/GOARCH fallback.
		return exec.CommandContext(ctx, missing)
	}

	got := resolveOSVersion()
	want := runtime.GOOS + "/" + runtime.GOARCH
	if got != want {
		t.Fatalf("resolveOSVersion() = %q, want fallback %q", got, want)
	}
}

// TestGrepFallsBackToNativeWhenRipgrepAbsent forces ripgrep to appear absent so
// Grep takes the native-Go-regex fallback branch, exercising the real fallback
// path (path resolution + grepNative) rather than the ripgrep subprocess.
func TestGrepFallsBackToNativeWhenRipgrepAbsent(t *testing.T) {
	orig := execLookPath
	t.Cleanup(func() { execLookPath = orig })
	execLookPath = func(string) (string, error) { return "", errors.New("ripgrep absent") }

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello needle world\nother line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)

	out, err := env.Grep("needle", "", "", false, 100, "")
	if err != nil {
		t.Fatalf("Grep native fallback: %v", err)
	}
	if !strings.Contains(out, "needle") {
		t.Fatalf("Grep native fallback missing match; got %q", out)
	}
}

// TestGrepNativeFallbackRelativePath covers the fallback's relative-path join:
// an empty path defaults to RootDir and a relative path is joined onto it.
func TestGrepNativeFallbackRelativePath(t *testing.T) {
	orig := execLookPath
	t.Cleanup(func() { execLookPath = orig })
	execLookPath = func(string) (string, error) { return "", errors.New("ripgrep absent") }

	dir := t.TempDir()
	sub := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("find_me here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)

	out, err := env.Grep("find_me", "pkg", "", false, 100, "")
	if err != nil {
		t.Fatalf("Grep native fallback (relative path): %v", err)
	}
	if !strings.Contains(out, "find_me") {
		t.Fatalf("Grep native fallback (relative path) missing match; got %q", out)
	}
}
