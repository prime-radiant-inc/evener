package execenv

import (
	"errors"
	"os"
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
