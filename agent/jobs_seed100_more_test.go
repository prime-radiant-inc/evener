//go:build serffuzz

package agent

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func seed100JobsMore(t *testing.T) {
	t.Helper()
	want := errors.New("seed100 more fault")

	// Constructor restoration has two independently-failing durable phases.
	for failPhase := 1; failPhase <= 2; failPhase++ {
		calls := 0
		restore := func(*jobManager) error {
			calls++
			if calls == failPhase {
				return want
			}
			return nil
		}
		_, _ = newJobManagerWithRestore(t.TempDir(), "restore", nil, jobstore.OpenNoSync, jobstore.OpenOutputNoSync, restore, restore)
	}

	jm := newTestJM(t)
	freezeClock(jm)
	jm.running["live"] = &runningJob{rec: &jobstore.JobRecord{JobID: "live", Status: jobstore.StatusRunning}}
	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatal(err)
	}
	delete(jm.running, "live")

	started := frozenTestTime
	badOutput := filepath.Join(t.TempDir(), "bad-output")
	if err := os.WriteFile(badOutput, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badOutput+".meta.json", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "lost", Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, StartedAt: &started, OutputPath: badOutput}
	if err := jm.appendEvent(start); err != nil {
		t.Fatal(err)
	}
	if err := jm.reconcileLostJobs(); err == nil {
		t.Fatal("directory output unexpectedly reconciled")
	}

	jm2 := newTestJM(t)
	freezeClock(jm2)
	start.JobID, start.OutputPath, start.OwnerSessionID, start.VisibleToSession = "lost-append", filepath.Join(t.TempDir(), "missing"), jm2.sessionID, jm2.sessionID
	if err := jm2.appendEvent(start); err != nil {
		t.Fatal(err)
	}
	jm2.appendEvent = func(jobstore.Event) error { return want }
	jm2.appendEvents = nil
	if err := jm2.reconcileLostJobs(); !errors.Is(err, want) {
		t.Fatalf("reconcile append = %v", err)
	}

	// Existing terminals cover the kept-sync terminal path and both mismatch guards.
	jm3 := newTestJM(t)
	freezeClock(jm3)
	out, err := jm3.openOutput(filepath.Join(jm3.dir, "jobs", "kept.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	run := &runningJob{rec: &jobstore.JobRecord{JobID: "kept", Type: jobstore.JobShell, Status: jobstore.StatusCompleted}, output: out, done: make(chan struct{})}
	terminal := &terminalJob{status: jobstore.StatusCompleted, endedAt: started, generation: "tg"}
	run.terminal = terminal
	jm3.running[run.rec.JobID] = run
	jm3.forward = func(jobstore.Event) error { return want }
	jm3.parentJobID = "parent"
	if err := jm3.finalizeKeptSync(run, "", "", nil); !errors.Is(err, want) {
		t.Fatalf("kept forward = %v", err)
	}
	jm3.forward = func(jobstore.Event) error { delete(jm3.running, run.rec.JobID); return nil }
	if err := jm3.finalizeKeptSync(run, "", "", nil); err != nil {
		t.Fatal(err)
	}
	jm3.running[run.rec.JobID] = run
	jm3.forward = nil
	if err := jm3.finalizeWithRunNoNotification(run.rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) { return "", "", nil, nil }); err != nil {
		t.Fatal(err)
	}

	// The schema library's resource-load failure and panic containment are explicit seams.
	_ = validateStructuredResultWithAddResource(nil, map[string]any{}, func(*jsonschema.Compiler, string, io.Reader) error { return want })
	_ = validateStructuredResultWithAddResource(nil, map[string]any{}, func(*jsonschema.Compiler, string, io.Reader) error { panic(want) })

	// Restore ordering uses both the equal-time ID tiebreak and descending start time.
	jm4 := newTestJM(t)
	freezeClock(jm4)
	for i, id := range []string{"b", "a", "new"} {
		ts := started
		if i == 2 {
			ts = ts.Add(time.Second)
		}
		events := []jobstore.Event{
			{Kind: jobstore.EventJobStarted, TS: ts, JobID: id, Type: jobstore.JobShell, OwnerSessionID: jm4.sessionID, VisibleToSession: jm4.sessionID, StartedAt: &ts},
			{Kind: jobstore.EventJobFinished, TS: ts, JobID: id, Status: jobstore.StatusCompleted, TerminalGen: "tg-" + id},
		}
		if err := jm4.appendJobEvents(events); err != nil {
			t.Fatal(err)
		}
	}
	if err := jm4.armPendingTerminalNotifications(); err != nil {
		t.Fatal(err)
	}

	// File faults are driven below os.Open, keeping production defaults unchanged.
	info := seed100FileInfo{size: 3}
	cases := []struct {
		name string
		file *seed100ReadFile
	}{
		{"stat", &seed100ReadFile{Reader: bytes.NewReader([]byte("abc")), info: info, statErr: want}},
		{"seek", &seed100ReadFile{Reader: bytes.NewReader([]byte("abc")), info: info, seekErr: want}},
		{"read", &seed100ReadFile{Reader: bytes.NewReader(nil), info: info}},
		{"close", &seed100ReadFile{Reader: bytes.NewReader([]byte("abc")), info: info, closeErr: want}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open := func(string) (jobOutputReadFile, error) { return tc.file, nil }
			_, _, _, _ = tailOutputFileWithOpen("x", 2, 3, open)
			tc.file.Reader = bytes.NewReader([]byte("abc"))
			if tc.name == "read" {
				tc.file.Reader = bytes.NewReader(nil)
			}
			_, _, _, _ = headOutputFileWithOpen("x", 2, 3, open)
		})
	}
}

type seed100ReadFile struct {
	*bytes.Reader
	info                       seed100FileInfo
	statErr, seekErr, closeErr error
}

func (f *seed100ReadFile) Stat() (os.FileInfo, error) { return f.info, f.statErr }
func (f *seed100ReadFile) Close() error               { return f.closeErr }
func (f *seed100ReadFile) Seek(offset int64, whence int) (int64, error) {
	if f.seekErr != nil {
		return 0, f.seekErr
	}
	return f.Reader.Seek(offset, whence)
}

type seed100FileInfo struct{ size int64 }

func (i seed100FileInfo) Name() string       { return "seed" }
func (i seed100FileInfo) Size() int64        { return i.size }
func (i seed100FileInfo) Mode() os.FileMode  { return 0 }
func (i seed100FileInfo) ModTime() time.Time { return frozenTestTime }
func (i seed100FileInfo) IsDir() bool        { return false }
func (i seed100FileInfo) Sys() any           { return nil }
