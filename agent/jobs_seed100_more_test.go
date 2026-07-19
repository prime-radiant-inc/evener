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
	jm.appendAbandonSnapshots = func([]watchSendTerminalSnapshot) ([]watchSendTerminalSnapshot, error) { return nil, want }
	abandoned := &runningJob{rec: &jobstore.JobRecord{JobID: "abandoned"}, done: make(chan struct{})}
	jm.running[abandoned.rec.JobID] = abandoned
	jm.abandonRunningJobs()
	jm.appendAbandonSnapshots = jm.appendWatchSendTerminalSnapshots
	closeJM := newTestJM(t)
	closeJM.appendTeardown = func([]watchSendTerminalSnapshot, []watchConfigTerminalSnapshot) error { return want }
	_ = closeJM.closeRuntimeState()
	jm.appendAbandonSnapshots = func([]watchSendTerminalSnapshot) ([]watchSendTerminalSnapshot, error) { return nil, want }
	jm.running["abandoned-one"] = &runningJob{rec: &jobstore.JobRecord{JobID: "abandoned-one"}, done: make(chan struct{})}
	jm.abandonRunningJob("abandoned-one")
	jm.appendAbandonSnapshots = jm.appendWatchSendTerminalSnapshots
	jm.running["nil-rec"] = &runningJob{}
	jm.stampLastActivityLocked("nil-rec")
	delete(jm.running, "nil-rec")
	jm.running["shell-dir"] = &runningJob{rec: &jobstore.JobRecord{JobID: "shell-dir", Type: jobstore.JobShell, WorkingDir: "/tmp/shell"}}
	jm.running["delegate-dir"] = &runningJob{rec: &jobstore.JobRecord{JobID: "delegate-dir", Type: jobstore.JobDelegate, DelegateRestore: &jobstore.DelegateRestoreDescriptor{WorkingDir: "/tmp/delegate"}}}
	_ = jm.liveWorkHandles()
	delete(jm.running, "shell-dir")
	delete(jm.running, "delegate-dir")

	// A forwarded start whose compensating terminal append also fails is kept
	// live for durable retry. Suppress only the asynchronous retry in this test.
	appendCalls := 0
	jm.appendEvent = func(event jobstore.Event) error {
		appendCalls++
		if appendCalls == 1 {
			return jm.store.Append(event)
		}
		return want
	}
	jm.forward = func(jobstore.Event) error { return want }
	jm.parentJobID = "parent"
	jm.finalizeShellAsync = func(string, jobstore.Status, string, *int) {}
	if _, err := jm.createShell(createShellOpts{Command: "true"}); !errors.Is(err, want) {
		t.Fatalf("double start failure = %v", err)
	}
	jm.appendEvent = jm.store.Append
	jm.forward = nil

	jm.running["live"] = &runningJob{rec: &jobstore.JobRecord{JobID: "live", Status: jobstore.StatusRunning}}
	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatal(err)
	}
	delete(jm.running, "live")
	foreignStarted := frozenTestTime
	if err := jm.appendEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: foreignStarted, JobID: "foreign", Type: jobstore.JobShell, OwnerSessionID: "other", StartedAt: &foreignStarted}); err != nil {
		t.Fatal(err)
	}
	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatal(err)
	}

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
	if err := jm2.reconcileLostJobsWithLoad(jm2.store.Load, func() (map[string]*jobstore.WatchRecord, error) { return nil, want }); !errors.Is(err, want) {
		t.Fatalf("reconcile watches = %v", err)
	}
	_ = includeAllWatchRecords(nil)

	closedJM := newTestJM(t)
	if err := closedJM.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = closedJM.readOutput("x", 1)
	_, _, _, _ = closedJM.readOutputHead("x", 1)
	_, _ = closedJM.outputDropped("x")
	_, _ = closedJM.grepOutput("x", nil)
	missingJM := newTestJM(t)
	_, _, _, _ = missingJM.readOutput("x", 1)
	_, _, _, _ = missingJM.readOutputHead("x", 1)
	_, _ = missingJM.outputDropped("x")
	_, _ = missingJM.grepOutput("x", nil)
	_, _, _, _, _ = missingJM.readJobWindow("x", 1, true)
	_, _, _, _, _ = missingJM.readJobWindow("x", 1, false)
	badMetaJM := newTestJM(t)
	badMetaPath := filepath.Join(t.TempDir(), "bad-meta.log")
	if err := os.WriteFile(badMetaPath, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	badMetaStart := frozenTestTime
	if err := badMetaJM.appendJobEvents([]jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: badMetaStart, JobID: "bad-meta", Type: jobstore.JobShell, OwnerSessionID: badMetaJM.sessionID, StartedAt: &badMetaStart, OutputPath: badMetaPath},
		{Kind: jobstore.EventJobFinished, TS: badMetaStart, JobID: "bad-meta", Status: jobstore.StatusCompleted, OutputBytes: 99, TerminalGen: "tg-bad"},
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = badMetaJM.readOutput("bad-meta", 1)
	_, _, _, _ = badMetaJM.readOutputHead("bad-meta", 1)
	_, _ = badMetaJM.grepOutput("bad-meta", nil)

	listJM := newTestJM(t)
	listOut, err := listJM.openOutput(filepath.Join(listJM.dir, "jobs", "list.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = listOut.Append([]byte("abc"))
	listJM.running["skip"] = &runningJob{rec: &jobstore.JobRecord{JobID: "skip"}}
	for _, id := range []string{"b", "a"} {
		listJM.running[id] = &runningJob{rec: &jobstore.JobRecord{JobID: id, Type: jobstore.JobShell, Status: jobstore.StatusRunning, StartedAt: started}, output: listOut, durableStarted: true}
	}
	if _, _, err := listJM.listWithError(listFilter{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := listJM.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _ = listJM.listWithError(listFilter{})

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

	// Removing the runtime during pending forwarding covers the stale-arm guard.
	out2, err := jm3.openOutput(filepath.Join(jm3.dir, "jobs", "stale.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	stale := &runningJob{rec: &jobstore.JobRecord{JobID: "stale", Type: jobstore.JobShell}, output: out2, done: make(chan struct{})}
	staleTerminal := &terminalJob{status: jobstore.StatusCompleted, endedAt: started, generation: "tg-stale", notificationPendingAppended: true, notificationPending: jobstore.Event{}}
	stale.terminal = staleTerminal
	jm3.running[stale.rec.JobID] = stale
	jm3.parentJobID = "parent"
	jm3.forward = func(jobstore.Event) error { delete(jm3.running, stale.rec.JobID); return nil }
	if err := jm3.armFinalizedJob(stale, staleTerminal); err != nil {
		t.Fatal(err)
	}
	terminalRun := &runningJob{rec: &jobstore.JobRecord{JobID: "terminal-stop", Status: jobstore.StatusCompleted}, terminal: &terminalJob{}}
	jm3.running[terminalRun.rec.JobID] = terminalRun
	_, _ = jm3.stop(terminalRun.rec.JobID)
	delete(jm3.running, terminalRun.rec.JobID)
	doneAttempt := &finalizeAttempt{done: make(chan struct{}), err: want}
	close(doneAttempt.done)
	attemptRun := &runningJob{rec: &jobstore.JobRecord{JobID: "attempt"}, finalize: doneAttempt}
	jm3.running[attemptRun.rec.JobID] = attemptRun
	_ = jm3.finalizeWithRunMode(attemptRun.rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) { return "", "", nil, nil }, true)
	delete(jm3.running, attemptRun.rec.JobID)

	registryRun := &runningJob{rec: &jobstore.JobRecord{JobID: "registry", Type: jobstore.JobShell}, output: out, done: make(chan struct{})}
	registryTerminal := &terminalJob{status: jobstore.StatusCompleted, endedAt: started, generation: "tg-registry"}
	registryRun.terminal = registryTerminal
	jm3.running[registryRun.rec.JobID] = registryRun
	jm3.appendRegistry = func([]jobstore.Event) error { return want }
	_ = jm3.finalizeKeptSync(registryRun, "", "", nil)
	_ = jm3.armFinalizedJob(registryRun, registryTerminal)
	jm3.appendRegistry = jm3.appendWatchRegistryEvents

	pendingRun := &runningJob{rec: &jobstore.JobRecord{JobID: "pending", Type: jobstore.JobShell}, output: out, done: make(chan struct{})}
	pendingTerminal := &terminalJob{status: jobstore.StatusCompleted, endedAt: started, generation: "tg-pending"}
	pendingRun.terminal = pendingTerminal
	jm3.running[pendingRun.rec.JobID] = pendingRun
	jm3.appendEvent = func(jobstore.Event) error { return want }
	_ = jm3.armFinalizedJob(pendingRun, pendingTerminal)
	jm3.appendEvent = jm3.store.Append

	forwardRun := &runningJob{rec: &jobstore.JobRecord{JobID: "forward-terminal"}, terminal: &terminalJob{finished: jobstore.Event{}, notificationPending: jobstore.Event{}, notificationPendingAppended: true}}
	jm3.running[forwardRun.rec.JobID] = forwardRun
	jm3.parentJobID = "parent"
	jm3.forward = func(jobstore.Event) error { return want }
	_ = jm3.finalizeWithRunMode(forwardRun.rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) { return "", "", nil, nil }, true)
	_ = jm3.armFinalizedJob(forwardRun, forwardRun.terminal)
	_ = jm3.forwardPendingJobNotification(forwardRun, forwardRun.terminal)
	forwardRun.forwardDisabled = true
	_ = jm3.forwardFinishedJob(forwardRun, forwardRun.terminal)
	_ = jm3.forwardPendingJobNotification(forwardRun, forwardRun.terminal)
	forwardRun.fromWatch.Store(true)
	jm3.markWatchOriginCallerCallbackDelivered(forwardRun.rec.JobID)
	delete(jm3.running, forwardRun.rec.JobID)
	writeOut, err := jm3.openOutput(filepath.Join(jm3.dir, "jobs", "write-forward.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	writeRun := &runningJob{rec: &jobstore.JobRecord{JobID: "write-forward", Type: jobstore.JobShell, Status: jobstore.StatusRunning}, output: writeOut, done: make(chan struct{})}
	jm3.running[writeRun.rec.JobID] = writeRun
	jm3.forward = func(jobstore.Event) error { return want }
	_, _ = jm3.writeFinishJob(writeRun, jobstore.StatusCompleted, "done", nil)
	delete(jm3.running, writeRun.rec.JobID)
	successOut, err := jm3.openOutput(filepath.Join(jm3.dir, "jobs", "success-terminal.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	successRun := &runningJob{rec: &jobstore.JobRecord{JobID: "success-terminal", Type: jobstore.JobShell}, output: successOut, done: make(chan struct{})}
	successTerminal := &terminalJob{status: jobstore.StatusCompleted, endedAt: started, generation: "tg-success", finished: jobstore.Event{}, notificationPendingAppended: true, notificationPending: jobstore.Event{}}
	successRun.terminal = successTerminal
	jm3.running[successRun.rec.JobID] = successRun
	jm3.forward = nil
	_ = jm3.finalizeWithRunMode(successRun.rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) { return "", "", nil, nil }, true)

	// The schema library's resource-load failure and panic containment are explicit seams.
	_ = validateStructuredResultWithAddResource(nil, map[string]any{}, func(*jsonschema.Compiler, string, io.Reader) error { return want })
	_ = validateStructuredResultWithAddResource(nil, map[string]any{}, func(*jsonschema.Compiler, string, io.Reader) error { panic(want) })

	closed, err := jobstore.OpenOutputNoSync(filepath.Join(t.TempDir(), "closed.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = tailOutput(closed, 1)
	_, _, _, _ = headOutput(closed, 1)
	_, _, _, _ = stringOutputResult([]byte("partial"), 7, true, want)
	_, _, _ = validatedOutputStatsForRecord(filepath.Join(t.TempDir(), "missing"), nil)

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
	jm5 := newTestJM(t)
	freezeClock(jm5)
	if err := jm5.appendJobEvents([]jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "rearm-fail", Type: jobstore.JobShell, OwnerSessionID: jm5.sessionID, StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: started, JobID: "rearm-fail", Status: jobstore.StatusCompleted, TerminalGen: "tg-rearm"},
	}); err != nil {
		t.Fatal(err)
	}
	jm5.appendEvent = func(jobstore.Event) error { return want }
	_ = jm5.armPendingTerminalNotifications()

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
