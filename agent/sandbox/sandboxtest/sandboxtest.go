// Package sandboxtest gives a test binary a host temp namespace of its own.
//
// Sessions retain their scratch directory and their world-usable temp container
// when they close, and leave both for the crashed-scratch sweep's 24h reclaim
// (sandbox.SweepCrashedSessionScratch): a detached command may still be using
// them. A test binary never runs that reclaim, so every session a test closes
// would otherwise leave directories behind in the developer's temp dir and in
// /tmp, which no TMPDIR setting moves. A TestMain that routes the run through
// RedirectHostTemp collects all of it in one root and removes that root on exit.
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

	"primeradiant.com/evener/agent/sandbox"
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

// HostTemp is a test binary's private temp root. While it is in place, TMPDIR
// names a directory inside it and so does the world-usable host temp base that
// session temp containers are minted in.
type HostTemp struct {
	root            string
	inherited       bool
	restoreHostTemp func()
	restoreEnv      []func() error
}

// RedirectHostTemp points TMPDIR (TMP and TEMP on Windows) and the session
// temp container bases into a root: the one RootVar names when an enclosing test process made it, or else a
// new one under the current temp dir, named with prefix. Child processes inherit
// the TMPDIR and RootVar. Call Discard once the tests have run.
func RedirectHostTemp(prefix string) (*HostTemp, error) {
	h := &HostTemp{}
	if root := os.Getenv(RootVar); root != "" {
		if info, err := os.Stat(filepath.Join(root, "host-temp")); err == nil && info.IsDir() {
			h.root, h.inherited = root, true
		}
	}
	if !h.inherited {
		root, err := newHostTempRoot(prefix)
		if err != nil {
			return nil, err
		}
		h.root = root
	}
	// TMP and TEMP are what os.TempDir reads on Windows.
	temp := filepath.Join(h.root, "tmp")
	for name, value := range map[string]string{"TMPDIR": temp, "TMP": temp, "TEMP": temp, RootVar: h.root} {
		if err := h.setenv(name, value); err != nil {
			return nil, errors.Join(err, h.Discard())
		}
	}
	h.restoreHostTemp = sandbox.SetWorldTempBasesForTesting([]string{filepath.Join(h.root, "host-temp")})
	return h, nil
}

// newHostTempRoot creates a root holding the TMPDIR and the host temp base.
func newHostTempRoot(prefix string) (string, error) {
	root, err := os.MkdirTemp("", prefix+"*")
	if err != nil {
		return "", fmt.Errorf("sandboxtest: create host temp root: %w", err)
	}
	// The root is traversable but not listable, as /tmp's parent is to any user:
	// a command running as another user must reach the world-usable host-temp
	// inside it, while tmp below stays this user's own.
	if err := os.Chmod(root, 0o711); err != nil {
		return "", errors.Join(fmt.Errorf("sandboxtest: open %s: %w", root, err), os.RemoveAll(root))
	}
	temp := filepath.Join(root, "tmp")
	hostTemp := filepath.Join(root, "host-temp")
	for _, dir := range []string{temp, hostTemp} {
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
	if h.restoreHostTemp != nil {
		h.restoreHostTemp()
		h.restoreHostTemp = nil
	}
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
