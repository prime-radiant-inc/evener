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
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// HostTemp is a test binary's private temp root. While it is in place, TMPDIR
// names a directory inside it and so does the world-usable host temp base that
// session temp containers are minted in.
type HostTemp struct {
	root              string
	restoreHostTemp   func()
	previousTMPDIR    string
	hadPreviousTMPDIR bool
}

// RedirectHostTemp creates a root under the current temp dir, named with
// prefix, and points TMPDIR and the session temp container bases into it. Child
// processes inherit the TMPDIR. Call Discard once the tests have run.
func RedirectHostTemp(prefix string) (*HostTemp, error) {
	root, err := os.MkdirTemp("", prefix+"*")
	if err != nil {
		return nil, fmt.Errorf("sandboxtest: create host temp root: %w", err)
	}
	temp := filepath.Join(root, "tmp")
	hostTemp := filepath.Join(root, "host-temp")
	for _, dir := range []string{temp, hostTemp} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("sandboxtest: create %s: %w", dir, err), os.RemoveAll(root))
		}
	}
	// A session temp container is only minted in a base with /tmp's own mode:
	// world-writable, world-traversable and sticky.
	if err := os.Chmod(hostTemp, 0o777|os.ModeSticky); err != nil {
		return nil, errors.Join(fmt.Errorf("sandboxtest: open %s: %w", hostTemp, err), os.RemoveAll(root))
	}
	previous, had := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", temp); err != nil {
		return nil, errors.Join(fmt.Errorf("sandboxtest: set TMPDIR: %w", err), os.RemoveAll(root))
	}
	return &HostTemp{
		root:              root,
		restoreHostTemp:   sandbox.SetWorldTempBasesForTesting([]string{hostTemp}),
		previousTMPDIR:    previous,
		hadPreviousTMPDIR: had,
	}, nil
}

// Root is the directory that holds everything the redirect collects.
func (h *HostTemp) Root() string { return h.root }

// Discard puts TMPDIR and the session temp container bases back and removes the
// root with everything the tests left in it.
func (h *HostTemp) Discard() error {
	h.restoreHostTemp()
	var envErr error
	if h.hadPreviousTMPDIR {
		envErr = os.Setenv("TMPDIR", h.previousTMPDIR)
	} else {
		envErr = os.Unsetenv("TMPDIR")
	}
	if err := os.RemoveAll(h.root); err != nil {
		return errors.Join(envErr, fmt.Errorf("sandboxtest: remove host temp root %s: %w", h.root, err))
	}
	return envErr
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
