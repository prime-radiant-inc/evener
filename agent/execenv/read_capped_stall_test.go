package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// stalledAferoFile stands in for the off arm's #4611 hole: a directly-named
// size-0 regular whose read never yields — the poll-registered kmsg shape,
// root-only in practice — so only a read deadline can end it. Read parks until
// the deadline SetReadDeadline schedules elapses, then reports the poller's
// timeout; Stat reports a regular file of size 0, the shape the off arm admits.
type stalledAferoFile struct {
	afero.File
	fired chan struct{}
	once  sync.Once
}

func newStalledAferoFile() *stalledAferoFile {
	return &stalledAferoFile{fired: make(chan struct{})}
}

func (f *stalledAferoFile) Read([]byte) (int, error) {
	<-f.fired
	return 0, os.ErrDeadlineExceeded
}

func (f *stalledAferoFile) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	go func() {
		if d > 0 {
			time.Sleep(d)
		}
		f.once.Do(func() { close(f.fired) })
	}()
	return nil
}

func (f *stalledAferoFile) Stat() (os.FileInfo, error) { return stalledAferoInfo{}, nil }
func (f *stalledAferoFile) Close() error               { return nil }

type stalledAferoInfo struct{}

func (stalledAferoInfo) Name() string       { return "kmsg-shaped" }
func (stalledAferoInfo) Size() int64        { return 0 }
func (stalledAferoInfo) Mode() os.FileMode  { return 0o644 }
func (stalledAferoInfo) ModTime() time.Time { return time.Time{} }
func (stalledAferoInfo) IsDir() bool        { return false }
func (stalledAferoInfo) Sys() any           { return nil }

// stalledAferoFS serves the stalled file; only OpenFile is reached.
type stalledAferoFS struct{ afero.Fs }

func (stalledAferoFS) OpenFile(string, int, os.FileMode) (afero.File, error) {
	return newStalledAferoFile(), nil
}

// TestReadFileCappedBoundsAStalledRead is #4611's off-arm half: with no sandbox
// the whole-file read is afero-backed and had no stall deadline, so a
// directly-named size-0 poll-registered never-ready regular blocked it
// indefinitely. The read must be bounded in time like the sandboxed arm's.
func TestReadFileCappedBoundsAStalledRead(t *testing.T) {
	// Not parallel: shrinks the package-wide grepReadStallTimeout.
	orig := grepReadStallTimeout
	grepReadStallTimeout = 100 * time.Millisecond
	t.Cleanup(func() { grepReadStallTimeout = orig })

	done := make(chan error, 1)
	go func() {
		_, err := readFileCapped(stalledAferoFS{}, "kmsg-shaped")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errGrepStreamStalled) {
			t.Fatalf("readFileCapped on a never-ready regular = %v, want the stall error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readFileCapped on a never-ready regular never returned: the off arm has no stall bound")
	}
}

// TestReadFileCappedCtxObservesCancellation pins the ctx half of the off arm's
// bounded read: an already-cancelled call answers with the cancellation rather
// than parking on the stream it would have read.
func TestReadFileCappedCtxObservesCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := readFileCappedCtx(ctx, stalledAferoFS{}, "kmsg-shaped")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readFileCappedCtx(cancelled) = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readFileCappedCtx(cancelled) did not observe the cancellation")
	}
}

// TestOffArmReadHandleIsAConcreteOSFile pins the mechanism readFileCappedCtx's
// stall bound depends on: the OS-backed afero handle must be a concrete
// *os.File, so readFileCappedCtx's readDeadlineSetter assertion engages on a
// pollable mount. A future afero layer that wrapped the handle in another type
// would silently drop the bound — the synthetic stall test above could not see
// it — so this fails first. A real procfs regular needs root, so the synthetic
// stalledAferoFile is the only deterministic behavioral check; this pins the
// type underneath it.
func TestOffArmReadHandleIsAConcreteOSFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := afero.NewOsFs().OpenFile(path, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, ok := f.(*os.File); !ok {
		t.Fatalf("OS-backed afero handle is %T, want *os.File: readFileCappedCtx's deadline assertion would not engage", f)
	}
	if _, ok := f.(readDeadlineSetter); !ok {
		t.Fatalf("OS-backed afero handle %T does not implement SetReadDeadline: the stall bound would not arm", f)
	}
}
