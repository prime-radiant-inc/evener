package sandboxtest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
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
	t.Setenv(envvars.EVENERHostTempBases.Name, outerHostTemp)

	t.Setenv("GOCACHE", "")
	os.Unsetenv("GOCACHE") //nolint:errcheck // t.Setenv above restores it
	outerUserCache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := RedirectHostTemp("evener-sandboxtest-")
	if err != nil {
		t.Fatalf("RedirectHostTemp: %v", err)
	}
	t.Cleanup(func() { _ = redirect.Discard() })
	root := redirect.Root()
	// Moving the user cache dir must not move Go's build cache with it.
	if got, want := os.Getenv("GOCACHE"), filepath.Join(outerUserCache, "go-build"); got != want {
		t.Fatalf("GOCACHE under the redirect = %q, want it pinned to %q", got, want)
	}
	// The user cache dir Windows resolves (os.UserCacheDir reads LocalAppData
	// there) must be a real directory: the bundled-skills cache refuses a base
	// it cannot resolve.
	// The crashed-scratch sweep also walks os.UserCacheDir, so wherever a
	// variable decides it (not macOS, where it follows HOME) it must land in the
	// root too.
	if runtime.GOOS != "darwin" {
		if cache, err := os.UserCacheDir(); err != nil || !within(root, cache) {
			t.Fatalf("os.UserCacheDir() = %q, %v; want it under the redirect root %q", cache, err, root)
		}
	}
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
	// Exported, not only set in this process: the evener binaries a test starts
	// read it, and their startup sweep must walk this root instead of /tmp.
	if got, want := envvars.EVENERHostTempBases.Getenv(), filepath.Join(root, "host-temp"); got != want {
		t.Fatalf("%s = %q, want the redirect's host temp %q", envvars.EVENERHostTempBases.Name, got, want)
	}
	if !Redirected(envvars.EVENERHostTempBases) {
		t.Fatalf("Redirected(%s) = false while the redirect is in force", envvars.EVENERHostTempBases.Name)
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
	if got := envvars.EVENERHostTempBases.Getenv(); got != outerHostTemp {
		t.Errorf("%s after Discard = %q, want the original %q", envvars.EVENERHostTempBases.Name, got, outerHostTemp)
	}
	if Redirected(envvars.EVENERHostTempBases) {
		t.Errorf("Redirected(%s) = true after Discard", envvars.EVENERHostTempBases.Name)
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
	if got, want := envvars.EVENERHostTempBases.Getenv(), filepath.Join(outer.Root(), "host-temp"); got != want {
		t.Fatalf("inner %s = %q, want the inherited root's host temp %q", envvars.EVENERHostTempBases.Name, got, want)
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

// TestRedirectedOwnsOnlyTheValueTheRedirectSet pins what a TestMain's scrub of
// product variables may leave behind: the host temp bases the redirect set, and
// nothing else. A developer's own value for the same variable, or any other
// product variable, is not the redirect's and must still be cleared.
func TestRedirectedOwnsOnlyTheValueTheRedirectSet(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(RootVar, "")
	t.Setenv(envvars.EVENERHostTempBases.Name, "/developer/own/value")
	if Redirected(envvars.EVENERHostTempBases) {
		t.Fatal("Redirected reports a developer's value with no redirect in force")
	}
	redirect, err := RedirectHostTemp("evener-sandboxtest-owned-")
	if err != nil {
		t.Fatalf("RedirectHostTemp: %v", err)
	}
	t.Cleanup(func() { _ = redirect.Discard() })
	if !Redirected(envvars.EVENERHostTempBases) {
		t.Fatalf("Redirected(%s) = false for the value the redirect set", envvars.EVENERHostTempBases.Name)
	}
	t.Setenv(envvars.EVENERHostTempBases.Name, "/developer/own/value")
	if Redirected(envvars.EVENERHostTempBases) {
		t.Fatal("Redirected reports a value the redirect did not set")
	}
	t.Setenv(envvars.EVENERStateDir.Name, filepath.Join(redirect.Root(), "host-temp"))
	if Redirected(envvars.EVENERStateDir) {
		t.Fatalf("Redirected reports %s, which the redirect never sets", envvars.EVENERStateDir.Name)
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

// TestKeptTempDirStaysOutsideTheRedirect pins where a test keeps evidence it
// means to outlive the run (a failing browser guard's screenshots and logs):
// the temp dir that was in force before the redirect, which Discard does not
// remove, handed down unchanged to an inheriting child.
func TestKeptTempDirStaysOutsideTheRedirect(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(RootVar, "")
	t.Setenv(KeptTempVar, "")
	outerTemp := os.TempDir()
	outer, err := RedirectHostTemp("evener-sandboxtest-kept-")
	if err != nil {
		t.Fatalf("RedirectHostTemp: %v", err)
	}
	t.Cleanup(func() { _ = outer.Discard() })
	if got := KeptTempDir(); got != outerTemp || within(outer.Root(), got) {
		t.Fatalf("KeptTempDir() = %q, want the pre-redirect temp dir %q outside the root %q", got, outerTemp, outer.Root())
	}
	inner, err := RedirectHostTemp("evener-sandboxtest-kept-inner-")
	if err != nil {
		t.Fatalf("RedirectHostTemp (inner): %v", err)
	}
	if got := KeptTempDir(); got != outerTemp {
		t.Fatalf("KeptTempDir() in an inheriting child = %q, want the outermost temp dir %q", got, outerTemp)
	}
	if err := inner.Discard(); err != nil {
		t.Fatal(err)
	}
}
