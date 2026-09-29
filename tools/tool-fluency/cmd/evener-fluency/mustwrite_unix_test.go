//go:build unix

package main

import (
	"io"
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
// probe observes the lock free and the test fails.
func TestMustWriteHoldsForkLockAcrossTheWrite(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		mustWrite(t, fifo, "body\n")
	}()

	// Probe until the write's lock is observed held. Before mustWrite reaches
	// its ForkLock.RLock the lock is briefly free, so an observation of it held
	// (TryLock failing) is the deterministic signal; if the write never takes
	// the lock the probe only ever sees it free and the deadline trips.
	locked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !syscall.ForkLock.TryLock() {
			locked = true
			break
		}
		syscall.ForkLock.Unlock()
		time.Sleep(time.Millisecond)
	}
	// Release the blocked writer by opening the far end of the FIFO and draining
	// it, so mustWrite returns and the process is left clean either way.
	reader, err := os.OpenFile(fifo, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, reader)
	_ = reader.Close()
	<-done
	if !locked {
		t.Fatal("mustWrite did not hold syscall.ForkLock across the write; a concurrent fork could inherit the open write fd (golang/go#22315)")
	}
}
