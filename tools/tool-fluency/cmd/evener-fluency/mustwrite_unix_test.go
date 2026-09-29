//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMustWriteHoldsForkLockAcrossTheWrite pins the guard that keeps a sibling
// parallel test's fork from inheriting the fixture's still-open write fd and
// making execve of that fixture fail with ETXTBSY ("text file busy", Go issue
// #22315). The readiness hook fires with syscall.ForkLock held for reading, so
// the probe starts only once the guard is actually held; a competing
// ForkLock.Lock — what fork/exec takes — cannot acquire it until the write
// returns. Removing the RLock fails the test.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Open the read end before the writer exists, so the writer's
	// open(O_WRONLY) always pairs and no later open can fail and leave the
	// writer blocked holding the lock. Open non-blocking (a blocking open would
	// wait for the writer that has not started yet), then clear O_NONBLOCK so a
	// plain read below blocks until data or EOF.
	rfd, err := syscall.Open(fifo, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rfd)
	if err := syscall.SetNonblock(rfd, false); err != nil {
		t.Fatal(err)
	}

	// A sibling parallel test can hold ForkLock fleetingly; skip rather than
	// attribute its hold to writeFixture.
	if !acquireFree() {
		t.Skip("ForkLock was already held before the write; a sibling test is using it")
	}

	// A payload larger than the FIFO pipe buffer keeps writeFixture inside its
	// guarded write, and so holding the RLock, until the test drains it below.
	body := strings.Repeat("x", 1<<20)
	acquired := make(chan struct{})
	written := make(chan error, 1)
	go func() { written <- writeFixture(fifo, body, func() { close(acquired) }) }()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("writeFixture never reached its guarded write")
	}
	// The hook fired with ForkLock held for reading; a fork/exec's Lock cannot
	// acquire it until writeFixture returns.
	held := !syscall.ForkLock.TryLock()
	if !held {
		syscall.ForkLock.Unlock()
	}
	drainUntilWritten(t, rfd, written) // drain so the blocked write can finish
	if !held {
		t.Fatal("writeFixture did not hold syscall.ForkLock across the write; a concurrent fork could inherit the open write fd (golang/go#22315)")
	}
}

// drainUntilWritten reads a blocking FIFO fd until writeFixture returns. A read
// of 0 before then only means the writer has not connected yet (the readiness
// hook fires before its open), so the loop keeps reading until the write
// finishes.
func drainUntilWritten(t *testing.T, fd int, written <-chan error) {
	t.Helper()
	buf := make([]byte, 1<<16)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n, err := syscall.Read(fd, buf)
		if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
			t.Fatalf("drain FIFO: %v", err)
		}
		if n > 0 {
			continue
		}
		select {
		case werr := <-written:
			if werr != nil {
				t.Fatalf("writeFixture: %v", werr)
			}
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("writeFixture did not return while its write was drained")
}

// acquireFree reports whether it can take and immediately release ForkLock
// within a short window, retrying past a sibling test's transient hold.
func acquireFree() bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.ForkLock.TryLock() {
			syscall.ForkLock.Unlock()
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
