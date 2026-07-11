//go:build serffuzz

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzWatchAttachTerminalProgram exercises the level-triggered output-match
// paths that run while a watch is attached to existing output and while a job
// becomes terminal. It keeps all effects inside a test-owned job store: shell
// records are only durable fixtures and never launch a process.
//
// The program's oracles assert the delivery cardinality that distinguishes an
// attach scan from replay, and the durable pending state used by a terminal
// catch-up send. It intentionally leaves configuration validation, event
// evaluation, and general pending-send settlement to their dedicated programs.
func FuzzWatchAttachTerminalProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0},
		{1, 2, 3},
		{255, 1, 254, 2},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &watpReader{data: data}
		watpAttachNoSend(t, r)
		watpAttachSidecar(t, r)
		watpTerminalCatchup(t, r)
		watpTerminalFlush(t, r)
	})
}

type watpReader struct {
	data []byte
	pos  int
}

func (r *watpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *watpReader) word() string {
	return []string{"alpha", "beta", "gamma", "delta"}[int(r.next())%4]
}

func watpNewJM(t *testing.T) *jobManager {
	t.Helper()
	jm := newTestJM(t)
	t.Cleanup(func() {
		jm.abandonRunningJobs()
		_ = jm.close()
	})
	return jm
}

func watpCreateOutputJob(t *testing.T, jm *jobManager, output string) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "watch attach fixture"})
	if err != nil {
		t.Fatalf("create fixture shell: %v", err)
	}
	run := jm.running[rec.JobID]
	if run == nil || run.output == nil {
		t.Fatalf("fixture job %q has no running output", rec.JobID)
	}
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte(output)); err != nil {
		t.Fatalf("append fixture output: %v", err)
	}
	return rec
}

func watpAttachNoSend(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }

	label := r.word()
	rec := watpCreateOutputJob(t, jm, "noise\nneedle "+label+"\nneedle final-"+label+"\n")
	result, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "needle"})
	if err != nil {
		t.Fatalf("configure attach watch: %v", err)
	}
	if !result.Watching || !result.Fired {
		t.Fatalf("attach result = %+v, want live one-shot fire", result)
	}
	if len(notifications) != 1 || !strings.Contains(notifications[0].Reason, "needle final-"+label) {
		t.Fatalf("attach notifications = %+v, want one last-match notification", notifications)
	}

	// The attach scan is level-triggered, not replay. A new complete match fires
	// once more through the live feed path, while the retained matches do not.
	run := jm.running[rec.JobID]
	if run == nil || run.output == nil {
		t.Fatalf("live fixture job %q has no output", rec.JobID)
	}
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("needle live-"+label+"\n")); err != nil {
		t.Fatalf("append live fixture output: %v", err)
	}
	if len(notifications) != 2 || !strings.Contains(notifications[1].Reason, "needle live-"+label) {
		t.Fatalf("live feed notifications = %+v, want exactly one new match", notifications)
	}
}

func watpAttachSidecar(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_attach")
	label := r.word()
	rec := watpCreateOutputJob(t, jm, "before\nneedle sidecar-"+label+"\n")
	result, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "needle",
		Send:        &watchSendArgs{To: "dlg_attach", Message: "observe " + label},
	})
	if err != nil {
		t.Fatalf("configure sidecar attach watch: %v", err)
	}
	if !result.Watching || !result.Fired {
		t.Fatalf("sidecar attach result = %+v, want live one-shot fire", result)
	}
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 || pending[0].state.Key.ResolvedSendTo != "dlg_attach" || !strings.Contains(pending[0].state.TriggerReason, "needle sidecar-"+label) {
		t.Fatalf("sidecar attach pending = %+v", pending)
	}
}

func watpTerminalCatchup(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }
	label := r.word()
	rec := watpCreateOutputJob(t, jm, "terminal needle-"+label)
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture complete", nil); err != nil {
		t.Fatalf("finalize terminal catch-up fixture: %v", err)
	}

	result, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "needle"})
	if err != nil {
		t.Fatalf("configure terminal catch-up: %v", err)
	}
	if result.Watching || !result.TerminalCatchup || !result.Fired || result.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("terminal catch-up result = %+v", result)
	}
	if len(notifications) == 0 || !strings.Contains(notifications[len(notifications)-1].Reason, "terminal needle-"+label) {
		t.Fatalf("terminal catch-up notifications = %+v", notifications)
	}

	seedWatchSendDelegateTarget(t, jm, "dlg_terminal")
	sendResult, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "needle",
		Send:        &watchSendArgs{To: "dlg_terminal", Message: "terminal observe"},
	})
	if err != nil {
		t.Fatalf("configure terminal sidecar catch-up: %v", err)
	}
	if sendResult.Watching || !sendResult.TerminalCatchup || !sendResult.Fired {
		t.Fatalf("terminal sidecar catch-up result = %+v", sendResult)
	}
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 || pending[0].state.Key.ResolvedSendTo != "dlg_terminal" {
		t.Fatalf("terminal sidecar pending = %+v", pending)
	}
}

func watpTerminalFlush(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }
	label := r.word()
	rec := watpCreateOutputJob(t, jm, "unterminated needle flush-"+label)
	result, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "needle"})
	if err != nil {
		t.Fatalf("configure terminal-flush watch: %v", err)
	}
	if !result.Watching || result.Fired {
		t.Fatalf("unterminated attach result = %+v, want live no fire", result)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture complete", nil); err != nil {
		t.Fatalf("finalize terminal-flush fixture: %v", err)
	}
	if !watpHasReason(notifications, "needle flush-"+label) {
		t.Fatalf("terminal flush notifications = %+v", notifications)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("terminal flush retained a live watch: %d", jm.watchCount())
	}
}

func watpHasReason(notifications []jobNotification, want string) bool {
	for _, notification := range notifications {
		if strings.Contains(notification.Reason, want) {
			return true
		}
	}
	return false
}
