package jobstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// TestRetirementReadyReadsOriginal proves retirement readiness reads the
// primary journal bytes rather than a cached cursor or fold: a valid store is
// ready, replacing the journal with malformed JSON makes readiness fail, and
// restoring the original bytes makes it ready again without reopening.
func TestRetirementReadyReadsOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Append(Event{Kind: EventJobStarted, JobID: "job_A"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.CheckRetirementReady(); err != nil {
		t.Fatalf("ready store reported not ready: %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckRetirementReady(); err == nil {
		t.Fatal("readiness accepted a corrupt original journal")
	}
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckRetirementReady(); err != nil {
		t.Fatalf("readiness did not recover after the original bytes were restored: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// failingReadFS is the filesystem fault seam for the sticky-read case: stat is
// forwarded to the wrapped store so the store sees the real file identity, but
// the read itself always fails. It stands in for an unreadable primary.
type failingReadFS struct{ afero.Fs }

func (f failingReadFS) Open(string) (afero.File, error) {
	return nil, errors.New("injected read failure")
}

// TestRetirementReadySurfacesStickyReadFailure proves readiness reports an
// unreadable primary instead of trusting the previously cached bytes.
func TestRetirementReadySurfacesStickyReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Append(Event{Kind: EventJobStarted, JobID: "job_A"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	store.fs = failingReadFS{store.fs}
	if err := store.CheckRetirementReady(); err == nil {
		t.Fatal("readiness trusted cached bytes when the primary became unreadable")
	}
}
