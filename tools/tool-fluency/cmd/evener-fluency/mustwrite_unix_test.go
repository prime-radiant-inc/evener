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
//
// This test does not call t.Parallel(), so it runs in the package's sequential
// phase while every t.Parallel test is paused: no other fixture write can hold
// ForkLock, and the observed hold is attributable to this writeFixture.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Open the read end before the writer exists, so the writer's
	// open(O_WRONLY) always pairs and no later open can fail and leave the
	// writer blocked holding the lock. Non-blocking so the drain below never
	// blocks and the writer's failure is caught by the loop's deadline.
	rfd, err := syscall.Open(fifo, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rfd)

	// A payload larger than any default FIFO pipe buffer (64 KiB, up to 1 MiB
	// with F_SETPIPE_SZ) keeps writeFixture inside its guarded write, and so
	// holding the RLock, until the test drains it below.
	body := strings.Repeat("x", 4<<20)
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
	// The guard must also be released once the write returns; a leaked RLock
	// would wedge every later fork.
	if !acquireFree() {
		t.Fatal("writeFixture leaked syscall.ForkLock after the write")
	}
}

// drainUntilWritten reads a non-blocking FIFO fd until writeFixture returns. A
// read of 0 before then only means the writer has not connected yet (the
// readiness hook fires before its open); EAGAIN means it is writing but the
// buffer is empty. Neither blocks, so the deadline and written channel are
// polled every iteration.
func drainUntilWritten(t *testing.T, fd int, written <-chan error) {
	t.Helper()
	buf := make([]byte, 1<<16)
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, err := syscall.Read(fd, buf)
		switch {
		case n > 0:
			continue
		case err == nil, err == syscall.EAGAIN: // no writer yet, or no data yet
		case err == syscall.EINTR:
			continue
		default:
			t.Fatalf("drain FIFO: %v", err)
		}
		select {
		case werr := <-written:
			if werr != nil {
				t.Fatalf("writeFixture: %v", werr)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("writeFixture did not return while its write was drained")
		}
		time.Sleep(time.Millisecond)
	}
}

// acquireFree reports whether it can take and immediately release ForkLock
// within a short window, retrying past transient contention.
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
