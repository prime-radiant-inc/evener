// Package sandboxtest gives a test binary a host temp namespace of its own.
//
// Sessions retain their scratch directory and their world-usable temp container
// when they close, and leave both for the crashed-scratch sweep's 24h reclaim
// (sandbox.SweepCrashedSessionScratch): a detached command may still be using
// them. A test binary never runs that reclaim, so every session a test closes
// would otherwise leave directories behind in the developer's temp dir and in
// /tmp, which no TMPDIR setting moves. The evener binaries a test starts do run
// the reclaim, at startup, and without a redirect it walks the developer's /tmp
// and /var/tmp and removes other sessions' abandoned scratch. A TestMain that
// routes the run through RedirectHostTemp collects all of it in one root,
// confines those children's sweep to it, and removes that root on exit.
// The same serves anything else a test leaves in the temp dir on purpose, such
// as the per-user bundled-skills cache every Evener process shares.
package sandboxtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/envvars"
)

// RootVar hands the root down to self-exec helper children, whose own TestMain
// runs RedirectHostTemp again. A child inherits the root instead of making one
// and leaves its removal to the process that made it: a detached process the
// child starts can outlive the child (a daemon whose Hub exits), and a root of
// the child's own would be deleted, with that process's temp dir, when the child
// exits.
//
// Like the other EVENER_-prefixed names the test rig owns, it describes the rig
// rather than the product, so it is absent from envvars.All() and the TestMain
// scrubs of product variables leave it alone.
const RootVar = "EVENER_SANDBOXTEST_ROOT"

// KeptTempVar carries the temp dir that was in force before the outermost
// redirect, so an inheriting child's KeptTempDir names the same place. Like
// RootVar it describes the rig, not the product.
const KeptTempVar = "EVENER_SANDBOXTEST_KEPT_TMPDIR"

// KeptTempDir is where a test keeps evidence it means to outlive the run, such
// as a failing browser guard's screenshots and logs: the temp dir in force
// before the redirect, which Discard does not remove. Under the make test gate
// that is the gate's own scratch, kept when the gate fails; on CI it is the
// runner's /tmp. Outside a redirect it is simply os.TempDir.
func KeptTempDir() string {
	if kept := os.Getenv(KeptTempVar); kept != "" {
		return kept
	}
	return os.TempDir()
}

// HostTemp is a test binary's private temp root. While it is in place, TMPDIR
// names a directory inside it and so does EVENER_HOST_TEMP_BASES, the world-
// usable host temp base that session temp containers are minted in and the
// crashed-scratch sweep walks.
type HostTemp struct {
	root       string
	inherited  bool
	restoreEnv []func() error
}

// RedirectHostTemp points the temp dir (TMPDIR; TMP and TEMP on Windows), the
// user cache dir (except on macOS) and the session temp container bases
// (EVENER_HOST_TEMP_BASES) into a root: the one RootVar names when an enclosing
// test process made it, or else a new one under the current temp dir, named
// with prefix. Child processes inherit the variables and RootVar. Call Discard
// once the tests have run.
func RedirectHostTemp(prefix string) (*HostTemp, error) {
	h := &HostTemp{}
	if root := os.Getenv(RootVar); root != "" {
		if info, err := os.Stat(hostTempBase(root)); err == nil && info.IsDir() {
			h.root, h.inherited = root, true
		}
	}
	if !h.inherited {
		if err := h.setenv(KeptTempVar, os.TempDir()); err != nil {
			return nil, err
		}
		root, err := newHostTempRoot(prefix)
		if err != nil {
			return nil, errors.Join(err, h.Discard())
		}
		h.root = root
	}
	// TMP and TEMP are what os.TempDir reads on Windows. The user cache dir
	// comes from XDG_CACHE_HOME on Linux and LocalAppData (AppData as a
	// fallback) on Windows: the crashed-scratch sweep walks it, and on Windows
	// it holds the bundled-skills cache. macOS derives it from HOME, which is
	// not moved here: a process-wide HOME moves every config home with it.
	temp := filepath.Join(h.root, "tmp")
	cache := filepath.Join(temp, "cache")
	// Go's build cache defaults to a directory under the user cache dir. Pin it
	// where it is now, or every go build a test runs would start cold.
	if _, set := os.LookupEnv("GOCACHE"); !set {
		if userCache, err := os.UserCacheDir(); err == nil {
			if err := h.setenv("GOCACHE", filepath.Join(userCache, "go-build")); err != nil {
				return nil, errors.Join(err, h.Discard())
			}
		}
	}
	for name, value := range map[string]string{
		"TMPDIR": temp, "TMP": temp, "TEMP": temp,
		"XDG_CACHE_HOME": cache, "LocalAppData": cache, "AppData": cache,
		RootVar:                          h.root,
		envvars.EVENERHostTempBases.Name: hostTempBase(h.root),
	} {
		if err := h.setenv(name, value); err != nil {
			return nil, errors.Join(err, h.Discard())
		}
	}
	return h, nil
}

// newHostTempRoot creates a root holding the TMPDIR, the user cache dir and the
// host temp base.
func newHostTempRoot(prefix string) (string, error) {
	root, err := os.MkdirTemp("", prefix+"*")
	if err != nil {
		return "", fmt.Errorf("sandboxtest: create host temp root: %w", err)
	}
	// The root is traversable but not listable, so tmp below stays this user's
	// own while host-temp keeps /tmp's mode. That mode is what the session temp
	// container requires of its base; the tests run every command as this user,
	// and a private TMPDIR above the root would still keep other users out, so
	// the redirect makes no promise of reach to another user.
	if err := os.Chmod(root, 0o711); err != nil {
		return "", errors.Join(fmt.Errorf("sandboxtest: open %s: %w", root, err), os.RemoveAll(root))
	}
	temp := filepath.Join(root, "tmp")
	hostTemp := hostTempBase(root)
	// cache inside tmp is the user cache dir RedirectHostTemp names.
	for _, dir := range []string{temp, filepath.Join(temp, "cache"), hostTemp} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", errors.Join(fmt.Errorf("sandboxtest: create %s: %w", dir, err), os.RemoveAll(root))
		}
	}
	// A session temp container is only minted in a base with /tmp's own mode:
	// world-writable, world-traversable and sticky.
	if err := os.Chmod(hostTemp, 0o777|os.ModeSticky); err != nil {
		return "", errors.Join(fmt.Errorf("sandboxtest: open %s: %w", hostTemp, err), os.RemoveAll(root))
	}
	return root, nil
}

// setenv sets name and records how to put back the value it replaced.
func (h *HostTemp) setenv(name, value string) error {
	previous, had := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil {
		return fmt.Errorf("sandboxtest: set %s: %w", name, err)
	}
	h.restoreEnv = append(h.restoreEnv, func() error {
		if had {
			return os.Setenv(name, previous)
		}
		return os.Unsetenv(name)
	})
	return nil
}

// Root is the directory that holds everything the redirect collects.
func (h *HostTemp) Root() string { return h.root }

// Discard puts TMPDIR, RootVar and the session temp container bases back and,
// unless the root was inherited, removes it with everything the tests left in
// it.
func (h *HostTemp) Discard() error {
	var errs []error
	for _, restore := range slices.Backward(h.restoreEnv) {
		errs = append(errs, restore())
	}
	h.restoreEnv = nil
	if !h.inherited && h.root != "" {
		if err := os.RemoveAll(h.root); err != nil {
			errs = append(errs, fmt.Errorf("sandboxtest: remove host temp root %s: %w", h.root, err))
		}
	}
	return errors.Join(errs...)
}

// Redirected reports whether v is a product variable holding the value a
// redirect in force set: the host temp bases, which RedirectHostTemp exports so
// the evener processes a test starts create their temp containers in, and
// confine their startup crashed-scratch sweep to, its root rather than /tmp and
// /var/tmp. A TestMain that clears every product variable after
// RedirectHostTemp must leave that one alone. Any other value, a developer's
// own for the same variable included, is not the redirect's.
func Redirected(v envvars.Var) bool {
	root := os.Getenv(RootVar)
	return root != "" && v.Name == envvars.EVENERHostTempBases.Name &&
		envvars.EVENERHostTempBases.Getenv() == hostTempBase(root)
}

// hostTempBase is the world-usable host temp base inside root.
func hostTempBase(root string) string { return filepath.Join(root, "host-temp") }

// Run is a whole TestMain for a package that needs nothing else: it runs m
// inside a RedirectHostTemp named with prefix and returns the exit code. A root
// that cannot be created or removed fails the run with the reason on stderr.
func Run(m *testing.M, prefix string) int {
	redirect, err := RedirectHostTemp(prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	if err := redirect.Discard(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}
