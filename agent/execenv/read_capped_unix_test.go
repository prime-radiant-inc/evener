//go:build unix

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The off (not fd-anchored) arm of the file tools reads through the injected
// afero filesystem, so its reads need the admission the sandboxed arm's
// admitReadFD already enforces: a FIFO is refused before the open can block on
// a writer, a device is refused rather than read, and an oversized regular
// file fails at the byte cap instead of loading whole (issue #4556).

// offArmReadOrFailFast runs read, failing the test when it does not return
// within seconds, so a regression that opens a FIFO with a blocking open
// surfaces as a visible timeout instead of a stuck suite (the same tripwire
// shape as readFileOrFailFast on the sandboxed arm).
func offArmReadOrFailFast(t *testing.T, read func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- read() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("read did not return (a FIFO was opened without O_NONBLOCK)")
		return nil // unreachable
	}
}

// TestReadFileOffArmRefusesFIFOWithoutBlocking pins the off arm's FIFO
// admission: a read-only open of a named pipe with no writer used to block the
// call indefinitely, so the pipe must be refused before any byte, without
// blocking at the open.
func TestReadFileOffArmRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	err := offArmReadOrFailFast(t, func() error {
		_, err := env.ReadFile(fifo, nil, nil)
		return err
	})
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("ReadFile(FIFO) err = %v, want the not-regular refusal", err)
	}
}

// TestReadFileOffArmRefusesDeviceFile pins the off arm's device admission: a
// device file — /dev/null, whose read used to come back empty — is refused
// rather than read, the same contract the sandboxed arm already enforces.
func TestReadFileOffArmRefusesDeviceFile(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment(t.TempDir())
	_, err := env.ReadFile(os.DevNull, nil, nil)
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("ReadFile(%s) err = %v, want the not-regular refusal", os.DevNull, err)
	}
}

// TestReadFileOffArmCapsWholeFileRead pins the off arm's byte cap: a regular
// file past MaxWholeFileReadBytes fails with the limit refusal instead of
// loading whole — the OOM of issue #4556. The fixture is sparse, so reaching
// the refusal costs no I/O.
func TestReadFileOffArmCapsWholeFileRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	path := filepath.Join(dir, "oversize.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create oversize: %v", err)
	}
	if err := os.Truncate(path, MaxWholeFileReadBytes+1); err != nil {
		t.Fatalf("truncate oversize: %v", err)
	}
	_, raw, err := env.ReadFileAndBytes(path, nil, nil)
	if err == nil {
		t.Fatalf("ReadFileAndBytes(oversize) = %d bytes, nil; want the read-limit refusal", len(raw))
	}
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("ReadFileAndBytes(oversize) err = %v, want a *ReadLimitError", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
	if raw != nil {
		t.Fatalf("ReadFileAndBytes(oversize) returned %d bytes alongside the refusal", len(raw))
	}
}

// TestEditFileOffArmRefusesFIFOWithoutBlocking pins edit_file's read: an edit
// loads the whole target before matching, so a FIFO target must be refused at
// that read rather than blocking on it.
func TestEditFileOffArmRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	err := offArmReadOrFailFast(t, func() error {
		_, err := env.EditFile(fifo, "old", "new", false)
		return err
	})
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("EditFile(FIFO) err = %v, want the not-regular refusal", err)
	}
}

// TestReadFileRawOffArmRefusesFIFOWithoutBlocking pins apply_patch's read
// seam: ReadFileRaw feeds every file a patch names, so its off arm must refuse
// a FIFO rather than blocking on it.
func TestReadFileRawOffArmRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	err := offArmReadOrFailFast(t, func() error {
		_, err := env.ReadFileRaw(fifo)
		return err
	})
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("ReadFileRaw(FIFO) err = %v, want the not-regular refusal", err)
	}
}
