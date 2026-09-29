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
// #22315). Writing to a FIFO with no reader blocks inside os.WriteFile while it
// holds syscall.ForkLock for reading, so a competing ForkLock.Lock — what
// fork/exec takes — cannot acquire the lock: if mustWrite stops holding it, the
// probe never sees a sustained acquisition failure and the test fails.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	// A sustained run of failed acquisitions, not one sample: ForkLock is
	// process-global, so a lone transient holder in a sibling parallel test
	// would otherwise read as "held". mustWrite holds its RLock for the entire
	// time it is blocked in open(), far longer than any such transient holder.
	const sustained = 50
	baseline := make(chan int, 1) // 0 free, -1 already held by something else
	result := make(chan int, 1)   // consecutive observations of the lock held
	release := make(chan struct{})
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		// mustWrite runs on the test goroutine; this probe only reports through
		// channels so no test method is called off the test goroutine.
		if syscall.ForkLock.TryLock() { // establish no other holder before the write
			syscall.ForkLock.Unlock()
			baseline <- 0
		} else {
			baseline <- -1
			result <- 0
			return
		}
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
		// Unblock the writer's open(O_WRONLY) by opening the read end without
		// blocking. The body is far smaller than the pipe buffer, so the write
		// completes without this end being drained; keep it open until the
		// writer returns so it never sees EPIPE.
		reader, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			result <- -2
			return
		}
		<-release
		_ = reader.Close()
		result <- held
	}()

	if <-baseline != 0 {
		t.Fatal("ForkLock was already held before the write; cannot attribute a later observation to mustWrite")
	}
	mustWrite(t, fifo, "body\n")
	close(release)
	<-probeDone
	if held := <-result; held < sustained {
		t.Fatalf("mustWrite did not hold syscall.ForkLock across the write (observed held for %d of %d consecutive probes); a concurrent fork could inherit the open write fd (golang/go#22315)", held, sustained)
	}
}
