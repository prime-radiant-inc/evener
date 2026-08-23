package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunBindsAndServes exercises run() which creates the server, logs the
// address, and calls serve() under a signal-notify context. The context is
// cancelled to end the serve loop cleanly.
func TestRunBindsAndServes(t *testing.T) {
	// Use a real port the kernel assigns.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run("127.0.0.1:0", 0, 1, "")
	}()

	// Send SIGINT to ourselves to trigger signal.NotifyContext cancellation.
	// The signal handler catches it regardless of whether the server has
	// finished binding, so no sleep is needed.
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	_ = p.Signal(os.Interrupt)

	select {
	case err := <-done:
		if err != nil {
			// run() returns the error from serve(); a clean shutdown via
			// signal returns nil.
			t.Fatalf("run returned error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("run did not exit after signal")
	}
}

// TestRunNewServerError covers the path where fakellm.NewOn fails.
func TestRunNewServerError(t *testing.T) {
	// An invalid address that cannot be bound.
	err := run("not-a-valid-addr:bad", 0, 1, "")
	if err == nil {
		t.Fatal("run with invalid address should return error")
	}
}

// TestStageNoteCreatesFile exercises stageNote() which writes a numbered
// note file into a directory.
func TestStageNoteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path, err := stageNote(dir)
	if err != nil {
		t.Fatalf("stageNote: %v", err)
	}
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, dir) {
		t.Fatalf("path %q should be in dir %q", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged note: %v", err)
	}
	if !strings.HasPrefix(string(data), "note ") {
		t.Fatalf("note content should start with 'note ', got %q", data)
	}
}

// TestStageNoteMultipleCalls verifies the sequence counter increments.
func TestStageNoteMultipleCalls(t *testing.T) {
	dir := t.TempDir()
	p1, err := stageNote(dir)
	if err != nil {
		t.Fatalf("stageNote 1: %v", err)
	}
	p2, err := stageNote(dir)
	if err != nil {
		t.Fatalf("stageNote 2: %v", err)
	}
	if p1 == p2 {
		t.Fatalf("two calls should produce different paths: %q == %q", p1, p2)
	}
}
