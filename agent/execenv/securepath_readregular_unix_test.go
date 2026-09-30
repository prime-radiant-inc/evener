//go:build linux || darwin

package execenv

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// readFileOrFailFast runs s.readFile and returns its result, failing the test
// (with a clear message) if it does not return within a few seconds. A read-only
// open (O_RDONLY) of a FIFO blocks indefinitely waiting for a writer, so a
// regression that drops the nonblocking admission surfaces as a visible timeout
// rather than a stuck suite.
func readFileOrFailFast(t *testing.T, s *sandboxFS, tool, abs string) ([]byte, error) {
	t.Helper()
	type result struct {
		b   []byte
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		b, err := s.readFile(tool, abs)
		resCh <- result{b, err}
	}()
	select {
	case res := <-resCh:
		return res.b, res.err
	case <-time.After(5 * time.Second):
		t.Fatalf("sandbox readFile hung on %q (a non-regular input was opened without O_NONBLOCK)", abs)
		return nil, nil // unreachable
	}
}

// TestSandboxReadFileRefusesNonRegularWithoutBlocking pins SAFE-01's admission
// contract: an allowed path that is not a regular file (here a fixture-owned
// FIFO) is refused before any read, and the refusal does not block at open. The
// confined read shares the same descriptor for the admission check and the
// bytes, so the type cannot be swapped between them.
func TestSandboxReadFileRefusesNonRegularWithoutBlocking(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)

	fifo := filepath.Join(worktree, "pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	got, err := readFileOrFailFast(t, s, "read_file", fifo)
	if err == nil {
		t.Fatalf("readFile(FIFO) = %q, nil; want a not-regular refusal", got)
	}
	if !strings.Contains(err.Error(), "regular") {
		t.Fatalf("readFile(FIFO) err = %v, want a not-regular refusal", err)
	}
}
