//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMustWriteHoldsForkLockAcrossTheWrite pins the guard that keeps a sibling
// parallel test's fork from inheriting the fixture's still-open write fd and
// making execve of that fixture fail with ETXTBSY ("text file busy", Go issue
// #22315). Writing to a FIFO with no reader blocks inside writeFixture while it
// holds syscall.ForkLock for reading, so a competing ForkLock.Lock — what
// fork/exec takes — cannot acquire the lock. The guard is a defer around the
// whole os.WriteFile call, so observing it held while the write is blocked in
// open proves it was acquired before the write and (since the test does not
// release it) is held across the write too; removing the RLock fails the test.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// A sibling parallel test can hold ForkLock fleetingly; skip rather than
	// attribute its hold to writeFixture.
	if !acquireFree() {
		t.Skip("ForkLock was already held before the write; a sibling test is using it")
	}
	written := make(chan error, 1)
	go func() { written <- writeFixture(fifo, "body\n") }()

	// fork/exec takes ForkLock for writing; writeFixture's RLock blocks it for
	// the whole time the write is open, far longer than any transient user.
	const sustained = 50
	held := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && held < sustained {
		if syscall.ForkLock.TryLock() {
			syscall.ForkLock.Unlock()
			held = 0
		} else {
			held++
		}
		time.Sleep(time.Millisecond)
	}

	// A read end unblocks the writer's open(O_WRONLY). Open it non-blocking so
	// it cannot hang if writeFixture returned before opening, and hold it until
	// the writer returns (the body fits the pipe buffer, so nothing to drain).
	release := make(chan struct{})
	go func() {
		reader, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return
		}
		<-release
		_ = reader.Close()
	}()
	select {
	case err := <-written:
		if err != nil {
			close(release)
			t.Fatalf("writeFixture: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("writeFixture did not return after the FIFO was released")
	}
	close(release)

	if held < sustained {
		t.Fatalf("writeFixture did not hold syscall.ForkLock across the write (observed held for %d of %d consecutive probes); a concurrent fork could inherit the open write fd (golang/go#22315)", held, sustained)
	}
	// A sibling's fork/exec can transiently hold the write lock now that
	// writeFixture has released it; that contention is not a failure.
	if !acquireFree() {
		t.Skip("ForkLock is still contended after the write; cannot attribute the hold to writeFixture")
	}
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
