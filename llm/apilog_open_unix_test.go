//go:build darwin || linux

package llm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A target that is not a regular file is refused up front, before the open
// locks, re-permissions or reads the device.
func TestOpenPrivateAPILogFileRejectsANonRegularFile(t *testing.T) {
	file, err := openPrivateAPILogFile("/dev/null")
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAPILogFile(/dev/null) returned a file")
	}
	if err == nil {
		t.Fatal("openPrivateAPILogFile(/dev/null) error = nil, want a refusal")
	}
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		t.Fatalf("err = %v: a later operation on the device failed, want the regular-file refusal", err)
	}
}

// Closing through the private close path after the file is already closed
// must report both halves — the flock unlock of a dead descriptor and the
// double close — while the same close on a live file is clean.
func TestClosePrivateAPILogFileReportsClosedFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	live, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open live file: %v", err)
	}
	if err := closePrivateAPILogFile(live); err != nil {
		t.Fatalf("close of a live file = %v, want clean", err)
	}

	dead, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open second file: %v", err)
	}
	if err := dead.Close(); err != nil {
		t.Fatalf("pre-close: %v", err)
	}
	err = closePrivateAPILogFile(dead)
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("err = %v, want the double close's os.ErrClosed in the join", err)
	}
	if !errors.Is(err, syscall.EBADF) {
		t.Fatalf("err = %v, want the dead descriptor's unlock EBADF in the join", err)
	}
}
