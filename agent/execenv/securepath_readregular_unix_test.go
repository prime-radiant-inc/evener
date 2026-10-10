//go:build linux || darwin

package execenv

import (
	"context"
	"os"
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

// TestSandboxGrepSkipsNonRegularWithoutBlocking pins the same admission contract
// on the confined browse surface: secureDirFS.Open backs grep's per-file read
// (and its .gitignore reads), so a FIFO entry in an allowed directory must be
// refused at open rather than blocking the whole walk until a writer appears.
func TestSandboxGrepSkipsNonRegularWithoutBlocking(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)

	if err := os.WriteFile(filepath.Join(worktree, "real.txt"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(worktree, "pipe"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	out, err := grepOrFailFast(t, s, worktree)
	if err != nil {
		t.Fatalf("grepNative over a tree with a FIFO entry: %v", err)
	}
	if !strings.Contains(out, "needle here") {
		t.Fatalf("grepNative output = %q, want the real file's match", out)
	}
}

// TestSandboxGrepSkipsANamedFIFOWithoutBlocking: a grep whose path names a
// FIFO opens it nonblocking and skips it, the way the walk skips a FIFO entry.
func TestSandboxGrepSkipsANamedFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	pipe := filepath.Join(worktree, "pipe")
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	out, err := grepOrFailFast(t, s, pipe)
	if err != nil || out != "" {
		t.Fatalf("grepNative of a named FIFO = %q, %v; want no result", out, err)
	}
}

// grepOrFailFast runs the confined grep native walk and returns its result,
// failing the test if it does not return within a few seconds so a FIFO entry
// that blocks the walk surfaces as a visible timeout rather than a stuck suite.
func grepOrFailFast(t *testing.T, s *sandboxFS, base string) (string, error) {
	t.Helper()
	type result struct {
		out string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := s.grepNative(context.Background(), "needle", base, "", false, 100, "", 0, nil)
		resCh <- result{out, err}
	}()
	select {
	case res := <-resCh:
		return res.out, res.err
	case <-time.After(5 * time.Second):
		t.Fatalf("sandbox grepNative hung walking %q (a non-regular entry blocked the file open)", base)
		return "", nil // unreachable
	}
}

// TestSandboxBrowseOpenAdmitsDirectories pins the fs.FS contract on the confined
// browse fs: the non-regular refusal must not reject a directory, which a walk
// opens for its root and subdirectories.
func TestSandboxBrowseOpenAdmitsDirectories(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	if err := os.MkdirAll(filepath.Join(worktree, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseFd, canonical, err := s.openReadBaseFd("glob", worktree)
	if err != nil {
		t.Fatalf("openReadBaseFd: %v", err)
	}
	defer func() { _ = unix.Close(baseFd) }()
	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s, budget: newGlobBudget("glob")}
	for _, name := range []string{".", "sub"} {
		f, err := fsys.Open(name)
		if err != nil {
			t.Fatalf("secureDirFS.Open(%q) on a directory = %v, want success", name, err)
		}
		_ = f.Close()
	}
}

// TestSandboxGlobSkipsNonRegularWithoutBlocking pins the admission contract on
// the glob path: glob stats every candidate through secureDirFS.Stat, so a FIFO
// entry must not block that stat. The walk must still return the real file's
// match.
func TestSandboxGlobSkipsNonRegularWithoutBlocking(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	if err := os.WriteFile(filepath.Join(worktree, "real.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(worktree, "pipe"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	matches, _, err := globOrFailFast(t, s, worktree)
	if err != nil {
		t.Fatalf("glob over a tree with a FIFO entry: %v", err)
	}
	found := false
	for _, m := range matches {
		if filepath.Base(m) == "real.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("glob matches = %v, want the real file", matches)
	}
}

// globOrFailFast runs the confined glob and returns its result, failing the test
// if it does not return within a few seconds so a FIFO entry that blocks a stat
// surfaces as a visible timeout rather than a stuck suite.
func globOrFailFast(t *testing.T, s *sandboxFS, base string) ([]string, int, error) {
	t.Helper()
	type result struct {
		matches  []string
		excluded int
		err      error
	}
	resCh := make(chan result, 1)
	go func() {
		m, excluded, err := s.glob(context.Background(), "glob", base, "*", false, newGlobBudget("glob"))
		resCh <- result{m, excluded, err}
	}()
	select {
	case res := <-resCh:
		return res.matches, res.excluded, res.err
	case <-time.After(5 * time.Second):
		t.Fatalf("sandbox glob hung walking %q (a non-regular entry blocked a stat)", base)
		return nil, 0, nil // unreachable
	}
}
