//go:build unix

// Package orphanpipetest stages a subprocess that leaves a grandchild holding
// its output pipe, so a test can prove a WaitDelay bound structurally rather
// than with a stopwatch.
//
// The grandchild blocks on a FIFO that only the test releases. A call that
// waits for the grandchild therefore cannot return until the test releases it,
// so "the call returned while the grandchild still lived" is an observable
// fact, not a timing guess. A second FIFO tells the test when the grandchild
// holds the pipe, so a test that cancels the call's context cannot race the
// grandchild's creation.
package orphanpipetest

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/internal/shellquote"
)

// awaitTripwire is how long Await lets a call run before concluding it is
// waiting on the grandchild. It is a tripwire, not the mechanism: a correct
// call returns on its own, and one that waits on the grandchild would wait
// forever without it.
const awaitTripwire = 10 * time.Second

// Holder owns one pipe-holding grandchild's FIFOs.
type Holder struct {
	dir         string
	started     string
	release     string
	startedEnd  *os.File
	releaseEnd  *os.File
	releaseOnce sync.Once
}

// New creates the FIFOs in a fresh temporary directory. The test holds both
// open read-write, which never blocks, so no setup failure can hang it: the
// script's opens succeed at once, the grandchild reads the release FIFO until
// Release closes the test's end, and cleanup closes both even when the script
// never ran.
func New(t testing.TB) *Holder {
	t.Helper()
	dir := t.TempDir()
	h := &Holder{dir: dir, started: filepath.Join(dir, "started"), release: filepath.Join(dir, "release")}
	for _, fifo := range []string{h.started, h.release} {
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatalf("mkfifo %s: %v", fifo, err)
		}
	}
	var err error
	if h.startedEnd, err = os.OpenFile(h.started, os.O_RDWR, 0); err != nil {
		t.Fatalf("open %s: %v", h.started, err)
	}
	t.Cleanup(func() { _ = h.startedEnd.Close() })
	if h.releaseEnd, err = os.OpenFile(h.release, os.O_RDWR, 0); err != nil {
		t.Fatalf("open %s: %v", h.release, err)
	}
	t.Cleanup(h.Release)
	return h
}

// Dir is the holder's temporary directory, a place for the test's fake
// executables.
func (h *Holder) Dir() string { return h.dir }

// Spawn is a POSIX shell fragment that backgrounds a grandchild holding the
// script's stdout and stderr until Release. The grandchild opens the release
// FIFO before it announces itself on the started FIFO, so once AwaitStarted
// returns it is blocked reading the release while holding the pipe.
func (h *Holder) Spawn() string {
	return "{ exec 3<" + shellquote.Literal(h.release) + "; echo started >" + shellquote.Literal(h.started) + "; cat <&3; } &"
}

// WriteScript writes an executable shell script named name into Dir and
// returns its path. It holds syscall.ForkLock while the file is open for
// writing: a concurrent fork elsewhere in the test binary would otherwise
// inherit the write descriptor and make exec fail with ETXTBSY.
func (h *Holder) WriteScript(t testing.TB, name, body string) string {
	t.Helper()
	path := filepath.Join(h.dir, name)
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// AwaitStarted blocks until the grandchild holds the pipe. It also returns
// when cleanup closes the started FIFO, so a goroutine blocked here never
// outlives its test.
func (h *Holder) AwaitStarted() {
	_, _ = h.startedEnd.Read(make([]byte, 1))
}

// Release ends the grandchild. It is safe to call more than once.
func (h *Holder) Release() {
	h.releaseOnce.Do(func() { _ = h.releaseEnd.Close() })
}

// Await returns the call's result from done while the grandchild still holds
// the pipe, then releases the grandchild. A call that waits for the grandchild
// cannot return on its own; after the tripwire Await releases it so the call
// can finish, and fails the test.
func Await[T any](t testing.TB, h *Holder, done <-chan T) T {
	t.Helper()
	select {
	case result := <-done:
		h.Release()
		return result
	case <-time.After(awaitTripwire):
		h.Release()
		result := <-done
		t.Fatal("the call did not return while an orphaned grandchild held its output pipe")
		return result
	}
}
