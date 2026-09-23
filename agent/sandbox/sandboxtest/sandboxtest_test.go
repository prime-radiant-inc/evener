package sandboxtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestRedirectHostTempContainsEverySessionTempAndIsRemoved proves the property a
// TestMain relies on: while the redirect is in place, the temp dir and the
// world-usable host temp a session temp container is minted in both sit under
// one root, and Discard removes that root and puts TMPDIR and the host temp
// bases back.
func TestRedirectHostTempContainsEverySessionTempAndIsRemoved(t *testing.T) {
	if !sandbox.SessionTmpSupported {
		t.Skip("session temp containers exist only where the platform supports them")
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(RootVar, "")
	outerTemp := os.TempDir()
	outerHostTemp := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Mkdir(outerHostTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outerHostTemp, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sandbox.SetWorldTempBasesForTesting([]string{outerHostTemp}))

	redirect, err := RedirectHostTemp("evener-sandboxtest-")
	if err != nil {
		t.Fatalf("RedirectHostTemp: %v", err)
	}
	t.Cleanup(func() { _ = redirect.Discard() })
	root := redirect.Root()
	// The user cache dir Windows resolves (os.UserCacheDir reads LocalAppData
	// there) must be a real directory: the bundled-skills cache refuses a base
	// it cannot resolve.
	if cache := os.Getenv("LocalAppData"); !within(root, cache) {
		t.Fatalf("LocalAppData = %q, want it under the redirect root %q", cache, root)
	} else if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("redirected user cache dir %q is not a directory: %v", cache, err)
	}
	// Traversable but not listable, so a command running as another user can
	// reach the world-usable host temp inside it, as it can reach /tmp.
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o711 {
		t.Fatalf("root %q mode = %v, want 0711", root, got)
	}
	if !within(outerTemp, root) {
		t.Fatalf("root %q is not under the temp dir it was created in %q", root, outerTemp)
	}
	if !within(root, os.TempDir()) {
		t.Fatalf("os.TempDir() = %q, want it under the redirect root %q", os.TempDir(), root)
	}
	scratch, err := os.MkdirTemp("", "leak-*")
	if err != nil {
		t.Fatal(err)
	}
	container, err := sandbox.NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp under the redirect: %v", err)
	}
	if !within(root, container.Dir) {
		t.Fatalf("session temp container %q escaped the redirect root %q", container.Dir, root)
	}
	// Retained, exactly as a session close leaves it.
	if err := container.Retain(); err != nil {
		t.Fatal(err)
	}

	if err := redirect.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	for _, leftover := range []string{root, scratch, container.Dir} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Errorf("%q survived Discard: %v", leftover, err)
		}
	}
	if got := os.TempDir(); got != outerTemp {
		t.Errorf("TMPDIR after Discard = %q, want the original %q", got, outerTemp)
	}
	after, err := sandbox.NewSessionTmp()
	if err != nil {
		t.Fatalf("NewSessionTmp after Discard: %v", err)
	}
	t.Cleanup(func() { _ = after.Remove() })
	if !within(outerHostTemp, after.Dir) {
		t.Errorf("session temp container after Discard = %q, want it back in the host temp the redirect replaced, %q", after.Dir, outerHostTemp)
	}
}

// TestRedirectHostTempInheritsTheEnclosingRoot covers a self-exec helper child,
// whose TestMain runs again inside the parent's redirect. A detached process the
// child starts can outlive it (a daemon whose Hub exits), so the child must use
// the parent's root and leave its removal to the parent rather than make a
// nested root and delete it, with that process's temp dir, when it exits.
func TestRedirectHostTempInheritsTheEnclosingRoot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(RootVar, "")
	outer, err := RedirectHostTemp("evener-sandboxtest-outer-")
	if err != nil {
		t.Fatalf("RedirectHostTemp (outer): %v", err)
	}
	t.Cleanup(func() { _ = outer.Discard() })
	if got := os.Getenv(RootVar); got != outer.Root() {
		t.Fatalf("%s = %q, want the outer root %q handed down to children", RootVar, got, outer.Root())
	}
	outerTemp := os.TempDir()

	inner, err := RedirectHostTemp("evener-sandboxtest-inner-")
	if err != nil {
		t.Fatalf("RedirectHostTemp (inner): %v", err)
	}
	if inner.Root() != outer.Root() {
		t.Fatalf("inner root = %q, want the inherited %q", inner.Root(), outer.Root())
	}
	if got := os.TempDir(); got != outerTemp {
		t.Fatalf("inner TMPDIR = %q, want the inherited %q", got, outerTemp)
	}
	survivor := filepath.Join(outerTemp, "detached-daemon-temp")
	if err := os.Mkdir(survivor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := inner.Discard(); err != nil {
		t.Fatalf("Discard (inner): %v", err)
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Fatalf("the inheriting child's Discard removed the enclosing root's contents: %v", err)
	}
	if err := outer.Discard(); err != nil {
		t.Fatalf("Discard (outer): %v", err)
	}
	if _, err := os.Stat(outer.Root()); !os.IsNotExist(err) {
		t.Fatalf("the outer Discard left its root %q: %v", outer.Root(), err)
	}
}

// within reports whether path lies strictly under root. Both are compared in
// canonical form where they resolve, because a session temp container reports
// its canonical path and the temp dir may sit behind a symlink (macOS's /var);
// a path that no longer exists is compared as given.
func within(root, path string) bool {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
