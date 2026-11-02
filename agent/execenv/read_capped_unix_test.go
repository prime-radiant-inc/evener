//go:build unix

package execenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"
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
	sparseOversize(t, path)
	_, raw, err := env.ReadFileAndBytesCtx(context.Background(), path, nil, nil)
	if err == nil {
		t.Fatalf("ReadFileAndBytesCtx(oversize) = %d bytes, nil; want the read-limit refusal", len(raw))
	}
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("ReadFileAndBytesCtx(oversize) err = %v, want a *ReadLimitError", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
	if raw != nil {
		t.Fatalf("ReadFileAndBytesCtx(oversize) returned %d bytes alongside the refusal", len(raw))
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

// Not parallel: the allocation measurement must exclude sibling test activity.
// TestReadFileOffArmRefusesOversizeBeforeReadingIt pins the off arm's refusal
// BEFORE any ingest: the fixture is dense NUL-free bytes past the cap, so a
// regression that reads first would both stream content past the cap and
// allocate for it, where the sparse fixtures cannot tell refusal-before-read
// from read-then-refuse (issue #4556).
func TestReadFileOffArmRefusesOversizeBeforeReadingIt(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	path := filepath.Join(env.WorkingDirectory(), "oversize-dense.log")
	writeDenseOversize(t, path)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err := env.ReadFileAndBytesCtx(context.Background(), path, nil, nil)
	runtime.ReadMemStats(&after)
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		t.Fatalf("ReadFileAndBytesCtx(oversize) err = %v, want the read-limit refusal", err)
	}
	if rle.Limit != MaxWholeFileReadBytes {
		t.Fatalf("ReadLimitError.Limit = %d, want %d", rle.Limit, MaxWholeFileReadBytes)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("refusing an oversize file allocated %d bytes, want the refusal before any read", allocated)
	}
}

// writeDenseOversize writes a real (non-sparse) file of NUL-free bytes one
// past the whole-file read cap, so no later NUL-byte or truncation check can
// masquerade as the cap's refusal.
func writeDenseOversize(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create dense oversize: %v", err)
	}
	defer f.Close()
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for written := int64(0); written < MaxWholeFileReadBytes; written += 1 << 20 {
		if _, err := f.Write(chunk); err != nil {
			t.Fatalf("write dense oversize: %v", err)
		}
	}
	if _, err := f.WriteString("a"); err != nil {
		t.Fatalf("write dense oversize tail: %v", err)
	}
}

// wrappingFS wraps every file it opens, the SetFs-supported shape that hides
// the bare *os.File handle a type assertion would key on. The admission the
// off arm enforces must hold through it exactly as for a bare handle.
type wrappingFS struct{ afero.Fs }

func (w wrappingFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := w.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return wrappedFile{f}, nil
}

type wrappedFile struct{ afero.File }

// newWrappedEnv returns an off-arm environment whose OS-backed filesystem
// wraps every handle.
func newWrappedEnv(t *testing.T) (*LocalExecutionEnvironment, string) {
	t.Helper()
	dir := t.TempDir()
	return NewLocalExecutionEnvironment(dir).SetFs(wrappingFS{afero.NewOsFs()}), dir
}

// TestReadFileOffArmRefusesADeviceThroughAWrappedFilesystem pins the admission
// through a wrapped handle: /dev/null must be refused exactly as it is through
// a bare one, not read back as empty content (issue #4556).
func TestReadFileOffArmRefusesADeviceThroughAWrappedFilesystem(t *testing.T) {
	t.Parallel()
	env, _ := newWrappedEnv(t)
	_, err := env.ReadFile(os.DevNull, nil, nil)
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("ReadFile(%s) through a wrapped handle err = %v, want the not-regular refusal", os.DevNull, err)
	}
}

// TestReadFileOffArmRefusesAFIFOThroughAWrappedFilesystem pins the same
// admission for a writerless named pipe through a wrapped handle: it must be
// refused, not read back as empty content.
func TestReadFileOffArmRefusesAFIFOThroughAWrappedFilesystem(t *testing.T) {
	t.Parallel()
	env, dir := newWrappedEnv(t)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	_, err := env.ReadFile(fifo, nil, nil)
	if !errors.Is(err, errNotRegularFile) {
		t.Fatalf("ReadFile(FIFO) through a wrapped handle err = %v, want the not-regular refusal", err)
	}
}

// Not parallel: the allocation measurement must exclude sibling test activity.
// TestReadFileOffArmRefusesOversizeThroughAWrappedFilesystemBeforeReadingIt
// pins the stat-size refusal through a wrapped handle: an oversized regular
// file fails before a byte is read, not by reading to the cap first.
func TestReadFileOffArmRefusesOversizeThroughAWrappedFilesystemBeforeReadingIt(t *testing.T) {
	env, dir := newWrappedEnv(t)
	path := filepath.Join(dir, "oversize.log")
	sparseOversize(t, path)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err := env.ReadFileAndBytesCtx(context.Background(), path, nil, nil)
	runtime.ReadMemStats(&after)
	if _, capped := errors.AsType[*ReadLimitError](err); !capped {
		t.Fatalf("ReadFileAndBytesCtx(oversize, wrapped) err = %v, want the read-limit refusal", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("refusing an oversize file through a wrapped handle allocated %d bytes, want the refusal before any read", allocated)
	}
}
