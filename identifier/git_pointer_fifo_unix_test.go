//go:build unix

package identifier

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// pointerStallTripwire bounds a pointer read. A regular pointer resolves in
// microseconds, so anything near this is the blocked open a readerless FIFO at
// ".git" causes.
const pointerStallTripwire = 10 * time.Second

// withReaderlessFIFOAtGit runs probe in a directory whose .git is a FIFO with
// no writer, and fails if probe does not return before the tripwire.
//
// A blocking open of the read end waits for a writer that never comes, so a
// guess-and-check implementation would park a goroutine until the process
// exits. The tripwire attaches a writer to release it once the assertion has
// already failed, so the test reports a hang instead of hanging the package.
func withReaderlessFIFOAtGit(t *testing.T, probe func(dir string) error) {
	t.Helper()
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, ".git")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable here: %v", err)
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			if f, err := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
	}

	done := make(chan error, 1)
	go func() { done <- probe(dir) }()
	select {
	case err := <-done:
		release()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(pointerStallTripwire): // TRIPWIRE: the pointer read blocked
		release()
		t.Fatal("the .git pointer read blocked on a readerless FIFO")
	}
}

// TestGitEntryResolvesToCommonRefusesFIFOPointer pins that the main-checkout
// check answers instead of blocking when .git is not a regular file.
func TestGitEntryResolvesToCommonRefusesFIFOPointer(t *testing.T) {
	withReaderlessFIFOAtGit(t, func(dir string) error {
		if GitEntryResolvesToCommon(dir, filepath.Join(dir, ".git")) {
			return errors.New("a FIFO .git entry resolved to a common dir")
		}
		return nil
	})
}

// TestMainCheckoutLocalRefusesFIFOPointer pins that startup-time project
// resolution answers instead of blocking when .git is not a regular file.
func TestMainCheckoutLocalRefusesFIFOPointer(t *testing.T) {
	withReaderlessFIFOAtGit(t, func(dir string) error {
		root, isGit, err := mainCheckoutLocal(dir)
		if err == nil {
			return fmt.Errorf("mainCheckoutLocal accepted a FIFO .git entry: root=%q isGit=%v", root, isGit)
		}
		if !isGit {
			return errors.New("a FIFO .git entry must be reported as a Git entry that could not be resolved, not as absence")
		}
		return nil
	})
}
