//go:build unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMustWriteHoldsForkLockAcrossTheWrite pins the guard that keeps a sibling
// parallel test's fork from inheriting the fixture's still-open write fd and
// making execve of that fixture fail with ETXTBSY ("text file busy", Go issue
// #22315). Writing to a FIFO with no reader blocks inside os.WriteFile while it
// holds syscall.ForkLock for reading, so a competing ForkLock.Lock — what
// fork/exec takes — cannot acquire the lock: if mustWrite stops holding it, the
// probe never sees a sustained acquisition failure and the test fails.
//
// The observation is attributed to mustWrite: the lock must read as free before
// the write starts, as held for a sustained run while mustWrite blocks, and as
// free again once mustWrite returns. A stray holder in a sibling parallel test
// cannot satisfy all three.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	// fork/exec takes ForkLock for writing; mustWrite's RLock blocks it for the
	// whole time the write is open, far longer than any sibling's transient use.
	const sustained = 50
	baseline := make(chan bool, 1)
	result := make(chan string, 1)
	written := make(chan struct{}) // test -> probe: mustWrite returned
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		// mustWrite runs on the test goroutine; this probe reports only through
		// channels, so no test method is called off the test goroutine.
		if !acquireFree() { // a sibling parallel test can hold it fleetingly
			baseline <- false
			result <- "ForkLock was already held before the write"
			return
		}
		baseline <- true

		held := 0
		for i := 0; i < 5000 && held < sustained; i++ {
			if syscall.ForkLock.TryLock() {
				syscall.ForkLock.Unlock()
				held = 0
			} else {
				held++
			}
			time.Sleep(time.Millisecond)
		}
		// Unblock the writer's open(O_WRONLY) by opening the read/write end
		// without blocking: O_RDWR returns immediately on a FIFO and satisfies a
		// blocked O_WRONLY opener, so the writer can never be left hanging even
		// on this error path. The body is far smaller than the pipe buffer, so
		// the write completes without this end being drained; keep it open until
		// the writer returns so it never sees EPIPE.
		reader, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			result <- fmt.Sprintf("open FIFO reader: %v", err)
			return
		}
		<-written
		freed := acquireFree()
		_ = reader.Close()
		if held < sustained {
			result <- fmt.Sprintf("mustWrite did not hold syscall.ForkLock across the write (observed held for %d of %d consecutive probes); a concurrent fork could inherit the open write fd (golang/go#22315)", held, sustained)
			return
		}
		if !freed {
			result <- "ForkLock stayed held after mustWrite returned; the observed hold did not belong to mustWrite"
			return
		}
		result <- ""
	}()

	if !<-baseline {
		t.Fatal("ForkLock was already held before the write; cannot attribute a later observation to mustWrite")
	}
	mustWrite(t, fifo, "body\n")
	close(written)
	<-probeDone
	if reason := <-result; reason != "" {
		t.Fatal(reason)
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
