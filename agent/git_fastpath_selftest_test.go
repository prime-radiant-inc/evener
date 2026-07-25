package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The PATH reorder must never change WHICH git the suite runs, only how fast it
// starts. Same version before and after is the whole safety property.
func TestFastGitPathKeepsTheSameGitVersion(t *testing.T) {
	before, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Skip("git not available")
	}
	// TestMain already applied the reorder, so this asserts the post-state
	// against a git resolved without the injected directory.
	dir := fastGitDirForTest
	if dir == "" {
		t.Skip("no fast-git directory was injected on this platform/toolchain")
	}
	stripped := removePathEntry(os.Getenv("PATH"), dir)
	cmd := exec.Command("git", "--version")
	cmd.Env = append(os.Environ(), "PATH="+stripped)
	after, err := cmd.Output()
	if err != nil {
		t.Fatalf("git --version without the fast dir: %v", err)
	}
	if strings.TrimSpace(string(before)) != strings.TrimSpace(string(after)) {
		t.Fatalf("PATH reorder changed the git version: with=%q without=%q",
			strings.TrimSpace(string(before)), strings.TrimSpace(string(after)))
	}
}

// Off macOS the shim does not exist, so the helper must leave PATH alone.
func TestFastGitPathIsNoOpOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin has the CLT shim; this pins the other platforms")
	}
	original := os.Getenv("PATH")
	if got := prependFastGitToPath(); got != "" {
		t.Errorf("prependFastGitToPath returned %q off darwin, want no change", got)
	}
	if os.Getenv("PATH") != original {
		t.Error("prependFastGitToPath modified PATH off darwin")
	}
}

// A git that is not the /usr/bin shim is already the real binary, so there is
// nothing to reorder and PATH must be left as the developer set it.
func TestFastGitPathLeavesNonShimGitAlone(t *testing.T) {
	dir := t.TempDir()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	// A git outside /usr/bin: the shim test must reject it.
	link := filepath.Join(dir, "git")
	if err := os.Symlink(realGit, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if isXcodeCLTShim(link) {
		t.Errorf("isXcodeCLTShim(%q) = true, want false for a git outside /usr/bin", link)
	}

	t.Setenv("PATH", dir)
	original := os.Getenv("PATH")
	if got := prependFastGitToPath(); got != "" {
		t.Errorf("prependFastGitToPath returned %q for a non-shim git, want no change", got)
	}
	if os.Getenv("PATH") != original {
		t.Error("prependFastGitToPath modified PATH when git was already non-shim")
	}
}

// With no git on PATH the helper must not invent one: tests that need git skip
// themselves, and manufacturing a path would defeat that.
func TestFastGitPathNoOpWithoutGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	original := os.Getenv("PATH")
	if got := prependFastGitToPath(); got != "" {
		t.Errorf("prependFastGitToPath returned %q with no git on PATH, want no change", got)
	}
	if os.Getenv("PATH") != original {
		t.Error("prependFastGitToPath modified PATH with no git present")
	}
}

// xcrunGitPath must reject anything unusable rather than returning a path the
// caller would put on PATH.
func TestXcrunGitPathRejectsUnusable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("xcrun is macOS-only")
	}
	// With xcrun unreachable the helper must report nothing.
	t.Setenv("PATH", t.TempDir())
	if got := xcrunGitPath(); got != "" {
		t.Errorf("xcrunGitPath() = %q with xcrun unavailable, want empty", got)
	}
}

// removePathEntry drops every occurrence of dir from a PATH value.
func removePathEntry(path, dir string) string {
	parts := strings.Split(path, string(os.PathListSeparator))
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != dir {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, string(os.PathListSeparator))
}
