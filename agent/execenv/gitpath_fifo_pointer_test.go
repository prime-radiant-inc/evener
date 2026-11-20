//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// gitPointerStallTripwire bounds the fresh resolution. A regular pointer resolves
// in microseconds, so anything near this is the blocked open the finding names.
const gitPointerStallTripwire = 10 * time.Second

// TestFreshGitRootDoesNotBlockOnAFIFOPointer pins that the FRESH structural
// lookup cannot hang on a .git entry that is not a regular file.
//
// Child construction asks for the root uncached, deliberately: a session
// launched before `git init` must still find the repository a child would see.
// That freshness is what makes the read unsafe — the parent's probe is bounded
// and can give up, but the child's lookup is synchronous and runs before any
// subprocess timeout, so a FIFO at ".git" holds delegate creation open past
// Stop. The capped reader opens non-blocking and admits only a regular file, so
// the pointer answers "unreadable", which the caller records as unresolved
// rather than as the absence of a repository.
func TestFreshGitRootDoesNotBlockOnAFIFOPointer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, ".git")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable here: %v", err)
	}

	// A readerless FIFO is the stall: a blocking open of the read end waits for
	// a writer that never comes. Because the resolution carries no context,
	// a failing implementation parks a goroutine for good, so the tripwire
	// attaches a writer to release it and let the binary exit cleanly instead
	// of hanging the whole package after the assertion already failed.
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			if f, err := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
	}

	resolved := make(chan struct{})
	go func() {
		_, _ = GitRootOrEmptyFreshDefinitive(NewLocalExecutionEnvironment(dir), dir)
		close(resolved)
	}()
	select {
	case <-resolved:
		release()
	case <-time.After(gitPointerStallTripwire): // TRIPWIRE: the pointer read blocked
		release()
		t.Fatal("the fresh git-root lookup blocked on a FIFO .git entry, which would hold child construction open past Stop")
	}
}
