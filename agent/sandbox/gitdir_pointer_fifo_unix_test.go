//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// gitPointerStallTripwire bounds sandbox classification of a FIFO .git entry.
// A regular pointer classifies in microseconds; anything near this is the
// blocked open the finding names.
const gitPointerStallTripwire = 10 * time.Second

// TestClassifyWorkspaceDoesNotBlockOnAFIFOPointer pins that sandbox policy
// classification answers instead of blocking when a workspace-controlled .git
// entry is not a regular file.
func TestClassifyWorkspaceDoesNotBlockOnAFIFOPointer(t *testing.T) {
	dir := clean(t.TempDir())
	fifoPath := filepath.Join(dir, ".git")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable here: %v", err)
	}
	// A readerless FIFO is the stall: a blocking open of the read end waits
	// for a writer that never comes. The tripwire attaches a writer on failure
	// so the parked goroutine is released and the package can exit cleanly.
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			if f, err := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
	}

	done := make(chan error, 1)
	go func() {
		_, err := ClassifyWorkspace(dir, nil)
		done <- err
	}()
	select {
	case err := <-done:
		release()
		if err == nil {
			t.Fatal("classified a FIFO .git entry; it must be refused")
		}
	case <-time.After(gitPointerStallTripwire): // TRIPWIRE: the pointer read blocked
		release()
		t.Fatal("sandbox classification blocked on a FIFO .git entry")
	}
}
