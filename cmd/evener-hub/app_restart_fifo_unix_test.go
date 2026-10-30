//go:build unix

package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestVerifyPinnedBinaryRejectsFIFOSwap proves a target swapped for a FIFO
// after the restart was scheduled fails verification instead of parking the
// restart goroutine (which holds hubUpdateMu) in an open for a writer.
func TestVerifyPinnedBinaryRejectsFIFOSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evener")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pin, err := pinInstalledBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Release a verification parked in a blocking open, so a regression
		// fails the test instead of leaking a goroutine.
		if w, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})

	done := make(chan error, 1)
	go func() { done <- verifyPinnedBinary(path, pin) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("verified a FIFO as the pinned binary")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("verification blocked on a FIFO")
	}
}

// TestLaunchBinaryDigestRejectsFIFO pins that the Settings digest path fails
// on a FIFO instead of parking a read while launchDigestMu is held.
func TestLaunchBinaryDigestRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evener")
	if err := unix.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := launchBinaryDigest(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("digested a FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("digest blocked on a FIFO")
	}
}
