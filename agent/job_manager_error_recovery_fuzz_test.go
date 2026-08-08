//go:build serffuzz

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJobManagerErrorRecoveryProgram exercises job-manager construction,
// durable shell-start failure, parent-forward failure, and restart
// reconciliation. All injected failures sit at filesystem/store boundaries;
// createShell only creates the durable record and output holder and never
// starts a process.
func FuzzJobManagerErrorRecoveryProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4},
		[]byte("job manager durable recovery"),
		{255, 0, 255, 0, 127},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		p := decodeJobManagerErrorProgram(data)
		runAgentJobsSeed100(t, p.output)
		first := runJobManagerErrorProgram(t, p)
		if replay := runJobManagerErrorProgram(t, p); !reflect.DeepEqual(first, replay) {
			t.Fatalf("job-manager error replay changed:\nfirst:  %#v\nreplay: %#v", first, replay)
		}
	})
}

type jobManagerErrorProgram struct {
	command     string
	description string
	output      string
}

type jobManagerErrorTrace struct {
	ForwardedStatus jobstore.Status
	ForwardedReason string
	RecoveredStatus jobstore.Status
	RecoveredReason string
	RecoveredBytes  int64
	Notifications   int
}

func decodeJobManagerErrorProgram(data []byte) jobManagerErrorProgram {
	word := func(start, limit int) string {
		if start >= len(data) {
			return "empty"
		}
		end := start + limit
		if end > len(data) {
			end = len(data)
		}
		var b strings.Builder
		for _, c := range data[start:end] {
			const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789_-"
			b.WriteByte(alphabet[int(c)%len(alphabet)])
		}
		return b.String()
	}
	return jobManagerErrorProgram{
		command:     "command-" + word(0, 24),
		description: "description-" + word(3, 16),
		output:      "output-" + word(1, 48) + "\n",
	}
}

func runJobManagerErrorProgram(t *testing.T, p jobManagerErrorProgram) jobManagerErrorTrace {
	t.Helper()
	assertJobManagerConstructionFailures(t)
	assertJobManagerCreateFailures(t, p)
	forwardedStatus, forwardedReason := assertJobManagerStartForwardFailure(t, p)
	recoveredStatus, recoveredReason, recoveredBytes, notifications := assertJobManagerLostRuntimeRecovery(t, p)
	return jobManagerErrorTrace{
		ForwardedStatus: forwardedStatus,
		ForwardedReason: forwardedReason,
		RecoveredStatus: recoveredStatus,
		RecoveredReason: recoveredReason,
		RecoveredBytes:  recoveredBytes,
		Notifications:   notifications,
	}
}

func assertJobManagerConstructionFailures(t *testing.T) {
	t.Helper()

	t.Run("session_dir", func(t *testing.T) {
		stateFile := filepath.Join(t.TempDir(), "state-file")
		if err := os.WriteFile(stateFile, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write state file: %v", err)
		}
		if jm, err := newJobManagerNoSync(stateFile, "mkdir-error", nil); jm != nil || err == nil {
			t.Fatalf("constructor session-dir failure = (%p, %v)", jm, err)
		}
	})

	t.Run("jobs_dir", func(t *testing.T) {
		stateDir := t.TempDir()
		dir := jobsDir(stateDir, "jobs-dir-error")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "jobs"), []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write jobs file: %v", err)
		}
		if jm, err := newJobManagerNoSync(stateDir, "jobs-dir-error", nil); jm != nil || err == nil {
			t.Fatalf("constructor jobs-dir failure = (%p, %v)", jm, err)
		}
	})

	want := errors.New("scripted store open failure")
	outputOpened := false
	jm, err := newJobManagerWithStoreOpen(t.TempDir(), "constructor-error", nil,
		func(string) (*jobstore.Store, error) { return nil, want },
		func(string, int64) (*jobstore.OutputStore, error) {
			outputOpened = true
			return nil, errors.New("unexpected output open")
		},
		func(string, int64) (*jobstore.OutputStore, error) {
			outputOpened = true
			return nil, errors.New("unexpected output create")
		})
	if jm != nil || !errors.Is(err, want) || outputOpened {
		t.Fatalf("constructor store failure = (%p, %v, output=%v)", jm, err, outputOpened)
	}

	if got := jobsDir("", "fallback-session"); got != filepath.Join(os.TempDir(), "serf-jobs", "fallback-session") {
		t.Fatalf("fallback jobs dir = %q", got)
	}
	if err := (*jobManager)(nil).closeStoreOnly(); err != nil {
		t.Fatalf("nil manager close = %v", err)
	}
}

func assertJobManagerCreateFailures(t *testing.T, p jobManagerErrorProgram) {
	t.Helper()

	t.Run("output_open", func(t *testing.T) {
		jm := newTestJM(t)
		defer jm.closeStoreOnly()
		want := errors.New("scripted output open failure")
		jm.createOutput = func(string, int64) (*jobstore.OutputStore, error) { return nil, want }
		if rec, err := jm.createShell(createShellOpts{Command: p.command}); rec != nil || !errors.Is(err, want) {
			t.Fatalf("create with output failure = (%+v, %v)", rec, err)
		}
		if len(jm.running) != 0 || len(jm.list(listFilter{})) != 0 {
			t.Fatal("output-open failure left a job behind")
		}
	})

	t.Run("closing", func(t *testing.T) {
		jm := newTestJM(t)
		defer jm.closeStoreOnly()
		jm.closing = true
		if rec, err := jm.createShell(createShellOpts{Command: p.command}); rec != nil || !errors.Is(err, errJobManagerClosing) {
			t.Fatalf("create while closing = (%+v, %v)", rec, err)
		}
		entries, err := os.ReadDir(filepath.Join(jm.dir, "jobs"))
		if err != nil {
			t.Fatalf("read output dir: %v", err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".log") {
				t.Fatalf("closing create retained output %q", entry.Name())
			}
		}
	})

	t.Run("start_append", func(t *testing.T) {
		jm := newTestJM(t)
		defer jm.closeStoreOnly()
		want := errors.New("scripted job_started append failure")
		jm.appendEvent = func(jobstore.Event) error { return want }
		if rec, err := jm.createShell(createShellOpts{Command: p.command}); rec != nil || !errors.Is(err, want) {
			t.Fatalf("create with append failure = (%+v, %v)", rec, err)
		}
		if len(jm.running) != 0 || len(jm.list(listFilter{})) != 0 {
			t.Fatal("start-append failure left a durable or running job")
		}
	})
}

func assertJobManagerStartForwardFailure(t *testing.T, p jobManagerErrorProgram) (jobstore.Status, string) {
	t.Helper()
	jm := newTestJM(t)
	defer jm.closeStoreOnly()
	jm.parentJobID = "parent-job"
	want := errors.New("scripted parent forward failure")
	jm.forward = func(jobstore.Event) error { return want }
	rec, err := jm.createShell(createShellOpts{Command: p.command, Description: p.description})
	if rec != nil || !errors.Is(err, want) || !errors.Is(err, errDelayedShellStartForwardFailed) {
		t.Fatalf("forwarded create = (%+v, %v)", rec, err)
	}
	recs, loadErr := jm.store.Load()
	if loadErr != nil {
		t.Fatalf("load forward-failed job: %v", loadErr)
	}
	if len(recs) != 1 {
		t.Fatalf("forward-failed records = %d, want 1", len(recs))
	}
	for _, got := range recs {
		if got.Status != jobstore.StatusFailed || got.Reason != "forward_failed" || got.Command != p.command {
			t.Fatalf("forward-failed record = %+v", got)
		}
		return got.Status, got.Reason
	}
	t.Fatal("missing forward-failed record")
	return "", ""
}

func assertJobManagerLostRuntimeRecovery(t *testing.T, p jobManagerErrorProgram) (jobstore.Status, string, int64, int) {
	t.Helper()
	stateDir := t.TempDir()
	jm, err := newJobManagerNoSync(stateDir, testOwnerSessionID, nil)
	if err != nil {
		t.Fatalf("new recovery source: %v", err)
	}
	rec, err := jm.createShell(createShellOpts{Command: p.command})
	if err != nil {
		t.Fatalf("create recovery source: %v", err)
	}
	run := jm.running[rec.JobID]
	if _, err := run.output.Append([]byte(p.output)); err != nil {
		t.Fatalf("append recovery output: %v", err)
	}
	if err := run.output.Close(); err != nil {
		t.Fatalf("close recovery output: %v", err)
	}
	delete(jm.running, rec.JobID)
	if err := jm.closeStoreOnly(); err != nil {
		t.Fatalf("close recovery source store: %v", err)
	}

	var notifications []jobNotification
	restarted, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(n jobNotification) {
		notifications = append(notifications, n)
	})
	if err != nil {
		t.Fatalf("new recovery target: %v", err)
	}
	defer restarted.closeStoreOnly()
	restarted.now = func() time.Time { return time.Unix(123, 0).UTC() }
	if err := restarted.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost job: %v", err)
	}
	recs, err := restarted.store.Load()
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	got := recs[rec.JobID]
	if got == nil || got.Status != jobstore.StatusStopped || got.Reason != "runtime_lost" || got.OutputBytes != int64(len(p.output)) {
		t.Fatalf("reconciled record = %+v", got)
	}
	if len(notifications) != 1 || notifications[0].JobID != rec.JobID || notifications[0].OutputBytes != int64(len(p.output)) {
		t.Fatalf("recovery notifications = %+v", notifications)
	}
	assertJobEventBatchAndWatchFiltering(t, restarted)
	return got.Status, got.Reason, got.OutputBytes, len(notifications)
}

func assertJobEventBatchAndWatchFiltering(t *testing.T, jm *jobManager) {
	t.Helper()
	if err := jm.appendJobEvents(nil); err != nil {
		t.Fatalf("append empty event batch: %v", err)
	}

	var single, batch int
	jm.appendEvent = func(jobstore.Event) error { single++; return nil }
	jm.appendEvents = func(events []jobstore.Event) error { batch += len(events); return nil }
	events := []jobstore.Event{{Kind: jobstore.EventWatchCleared}, {Kind: jobstore.EventWatchCleared}}
	if err := jm.appendJobEvents(events[:1]); err != nil {
		t.Fatalf("append single event: %v", err)
	}
	if err := jm.appendJobEvents(events); err != nil {
		t.Fatalf("append event batch: %v", err)
	}
	if single != 1 || batch != 2 {
		t.Fatalf("append dispatch = (single=%d, batch=%d)", single, batch)
	}

	watches := map[string]*jobstore.WatchRecord{
		"nil":      nil,
		"inactive": {Active: false, Generation: "inactive"},
		"no-gen":   {Active: true},
		"excluded": {Active: true, Generation: "excluded", Target: "other"},
		"z-active": {Active: true, Generation: "z", Target: "job"},
		"a-active": {Active: true, Generation: "a", Target: "job"},
	}
	clears := recoveredTerminalWatchClearEvents(watches, "job", time.Unix(9, 0).UTC())
	if len(clears) != 2 || clears[0].WatchID != "a-active" || clears[1].WatchID != "z-active" {
		t.Fatalf("filtered watch clears = %+v", clears)
	}
	for _, event := range clears {
		if event.Watch == nil || event.Watch.EndReason != "auto_removed_terminal" {
			t.Fatalf("watch clear payload = %+v", event)
		}
	}
}
