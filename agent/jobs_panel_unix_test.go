//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

func TestLoadSessionJobOutputTail(t *testing.T) {
	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobsPath := filepath.Join(jobsDir(dir, sessionID), "jobs.jsonl")
	// Write a store with one finished job whose OutputPath points at a file.
	logDir := filepath.Join(jobsDir(dir, sessionID), "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(logDir, "job_x.log")
	if err := os.WriteFile(outPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.OpenNoSync(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &now, OutputPath: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_x", Status: jobstore.StatusCompleted, OutputBytes: 10, TerminalGen: "tg-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(jobsPath, 0o400); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(12345, 0).UTC()
	if err := os.Chtimes(jobsPath, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	wantJournal := mustReadonlyFileState(t, jobsPath)
	tail, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_x", 0, 4)
	if err != nil || !found {
		t.Fatalf("tail: found=%v err=%v", found, err)
	}
	if tail.Tail != "6789" || tail.TotalBytes != 10 || !tail.Truncated {
		t.Errorf("tail: %+v", tail)
	}
	if got := mustReadonlyFileState(t, jobsPath); !reflect.DeepEqual(got, wantJournal) {
		t.Fatalf("historical output-tail read changed journal bytes or metadata:\n got=%#v\nwant=%#v", got, wantJournal)
	}
	if _, found, err := LoadSessionJobOutputTail(dir, sessionID, "job_nope", 0, 4); err != nil || found {
		t.Errorf("unknown job: found=%v err=%v", found, err)
	}
}
