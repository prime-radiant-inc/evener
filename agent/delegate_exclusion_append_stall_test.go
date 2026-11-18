//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
)

// appendStallTripwire bounds how long the rebuild may take before the test
// declares it wedged. It is generous: the assertion is "reaches an answer at
// all", and a passing run finishes in milliseconds.
const appendStallTripwire = 10 * time.Second

// TestExclusionRefreshDoesNotRereadStalledAppendFiles pins that the
// delegate-reachability rebuild reuses the append contents it already has.
//
// The defect is the same class as the bounded MCP probe and sits on the same
// path: request preparation resolved the freshness verdict under a bound, then
// rebuilt the prompt while holding s.mu, and that rebuild read every
// --system-prompt-append file with os.ReadFile. A file readable at startup
// whose mount has since stalled blocks that read forever, and the block lands
// INSIDE the session lock — so the session does not merely run slow, it wedges,
// and Stop cannot interrupt a read that never observes cancellation.
//
// A FIFO with no writer is exactly that shape without needing a stalled mount.
// The file is loaded as a real file first so the snapshot exists, then replaced
// by the FIFO, and the rebuild under test must finish from the snapshot.
func TestExclusionRefreshDoesNotRereadStalledAppendFiles(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	dir := realTempDirForTest(t)
	appendPath := filepath.Join(dir, "append.md")
	const body = "operator instructions"
	if err := os.WriteFile(appendPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{appendPath},
		AgentsDocPath:      filepath.Join(dir, "absent-AGENTS.md"),
	}))
	if s.appendSnapshot == nil {
		t.Fatal("fixture is wrong: the startup render did not record an append snapshot, so there is nothing to reuse")
	}
	if got := s.appendSnapshot.contents; len(got) != 1 || got[0] != body {
		t.Fatalf("append snapshot = %q, want the startup contents", got)
	}

	// The mount stops answering: a reader of this path blocks until a writer
	// appears, and none will.
	if err := os.Remove(appendPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(appendPath, 0o600); err != nil {
		t.Skipf("named pipes unavailable here: %v", err)
	}

	// A blocked reader is released by opening the write end. That matters for a
	// FAILING run: the read happens while s.mu is held, so leaving it parked
	// would deadlock the session's own Close at cleanup and turn a clean
	// assertion failure into a hung test binary.
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			// O_NONBLOCK is load-bearing: a blocking write-open waits for a
			// reader, and on the PASSING path the rebuild never opened one, so
			// it would hang the very test that proved the fix works.
			// Non-blocking answers ENXIO when nobody is reading, which is
			// exactly the case to leave alone.
			if f, err := os.OpenFile(appendPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
	}

	// The exclusion decision flips, which is what marks both caches stale and
	// drives the rebuild down the reuse path.
	s.delegateExclusionsMu.Lock()
	s.cliMCPExclusionKnown = true
	s.cliMCPExclusionApplied = true
	s.cliMCPExclusionVersion++
	s.delegateExclusionsMu.Unlock()

	returned := make(chan string, 1)
	go func() {
		var timings events.RoundTimings
		_, sys, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
		if err != nil {
			returned <- "error: " + err.Error()
			return
		}
		returned <- sys
	}()

	select {
	case sys := <-returned:
		release()
		if sys == "" {
			t.Error("the rebuilt request carried no system prompt")
		}
	case <-time.After(appendStallTripwire): // TRIPWIRE: the rebuild read the stalled file under s.mu
		release()
		t.Fatal("the request path blocked on a stalled --system-prompt-append read while holding s.mu")
	}
}
