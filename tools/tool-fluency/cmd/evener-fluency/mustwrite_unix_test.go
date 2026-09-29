//go:build unix

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMustWriteHoldsForkLockAcrossTheWrite pins the guard that keeps a sibling
// parallel test's fork from inheriting the fixture's still-open write fd and
// making execve of that fixture fail with ETXTBSY ("text file busy", Go issue
// #22315). Writing a payload larger than the FIFO pipe buffer with no reader
// blocks inside os.WriteFile while it holds syscall.ForkLock for reading: first
// opening (no reader), then writing (buffer full). A competing ForkLock.Lock —
// what fork/exec takes — cannot acquire the lock during either, so the whole
// open/write/close window is observed, not just the open.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 256<<10) // exceeds the default 64KiB pipe buffer

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
		// Release the writer. The writer is blocked in open(O_WRONLY) with no
		// reader, so this read end pairs with it immediately and cannot block;
		// draining to EOF lets the larger-than-buffer payload finish and the
		// blocked write complete before mustWrite returns.
		reader, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err == nil {
			_, _ = io.Copy(io.Discard, reader)
			_ = reader.Close()
		}
		<-written
		freed := acquireFree()
		if held < sustained {
			result <- fmt.Sprintf("mustWrite did not hold syscall.ForkLock across the write (observed held for %d of %d consecutive probes); a concurrent fork could inherit the open write fd (golang/go#22315)", held, sustained)
			return
		}
		if err != nil {
			result <- fmt.Sprintf("open FIFO reader: %v", err)
			return
		}
		if !freed {
			result <- "ForkLock stayed held after mustWrite returned; the observed hold did not belong to mustWrite"
			return
		}
		result <- ""
	}()

	if !<-baseline {
		t.Skip("ForkLock was already held before the write; a sibling test is using it")
	}
	mustWrite(t, fifo, body)
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
