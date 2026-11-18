//go:build unix

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// TestLoadSessionJobGetRejectsFIFOJournal proves the past-session reader does
// not hand a FIFO to jobstore's plain os.Open, which blocks until a writer
// connects. The symlink half of the same guard is portable and lives in
// TestLoadSessionJobGetRejectsUnsafeJournal.
func TestLoadSessionJobGetRejectsFIFOJournal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	journalDir := jobsDir(dir, sessionID)
	if err := os.MkdirAll(journalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(journalDir, "jobs.jsonl")
	if err := syscall.Mkfifo(journal, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, found, err := LoadSessionJobGet(dir, sessionID, "job_x", nil)
		if found {
			err = errors.New("FIFO journal was read as a job")
		}
		done <- err
	}()
	select {
	case err := <-done:
		_ = err // not-found or an error both reject the FIFO safely.
	// TRIPWIRE: this short bound is the assertion that os.Open did not block on the FIFO.
	case <-time.After(100 * time.Millisecond):
		// Pair with a writer to release the baseline's blocked os.Open, then
		// fail explicitly instead of leaving a goroutine behind.
		writerDone := make(chan error, 1)
		go func() {
			f, err := os.OpenFile(journal, os.O_WRONLY, 0)
			if err == nil {
				err = f.Close()
			}
			writerDone <- err
		}()
		select {
		case <-done:
		// TRIPWIRE: this bound prevents a broken FIFO-release guard from hanging the suite.
		case <-time.After(time.Second):
			t.Fatal("LoadSessionJobGet remained blocked after opening FIFO writer")
		}
		if err := <-writerDone; err != nil {
			t.Fatalf("release FIFO reader: %v", err)
		}
		t.Fatal("LoadSessionJobGet blocked opening FIFO journal")
	}
}
