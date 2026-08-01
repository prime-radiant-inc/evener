package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// seedForeignOwnedWatchedJob writes the shape reconcileLostJobs deliberately
// leaves alone: a job record OWNED by another session and merely forwarded into
// this one, plus an armed send watch on it. The child owner recovers the job on
// its own restore, so from this session's side the target is still running after
// the restart while the watch on it is not.
func seedForeignOwnedWatchedJob(t *testing.T, jm *jobManager, jobID, watchID, generation, sendTo string) {
	t.Helper()
	started := time.Unix(1, 0).UTC()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		OwnerSessionID:   "CHILD",
		VisibleToSession: jm.sessionID,
		ParentJobID:      "job_delegate",
		StartedAt:        &started,
	}); err != nil {
		t.Fatalf("seed foreign-owned job: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:    jobstore.EventWatchRegistered,
		TS:      started,
		WatchID: watchID,
		Watch: &jobstore.WatchEvent{
			Generation:       generation,
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Target:           jobID,
			SendTo:           sendTo,
			ConfigHash:       "sha256:foreign-owned-watch",
			Condition:        "output_match: ready",
			Config:           &jobstore.WatchConfigSnapshot{Target: jobID, OutputMatch: "ready", SendTo: sendTo},
		},
	}); err != nil {
		t.Fatalf("seed watch on foreign-owned job: %v", err)
	}
}

// A restart ends EVERY armed watch runtime_lost, including watches on jobs that
// outlive the restart because another session owns them. The watcher hears
// nothing and has no reason to suspect its watch is gone, so it waits on a
// condition that may still occur with nobody left watching for it.
func TestRestartTellsSendWatchItDidNotSurviveWhileItsTargetRuns(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	seedCommonWatchSendTargets(t, original)
	seedForeignOwnedWatchedJob(t, original, "job_child_owned", "watch_foreign", "wg_foreign", "dlg_obs")
	crashJobManager(t, original)

	restarted := restartJobManager(t, stateDir, "PARENT", func(jobNotification) {})

	// The premise: the child owner keeps the job, so this restore leaves it running.
	recs, err := restarted.store.Load()
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	if rec := recs["job_child_owned"]; rec == nil || rec.Status != jobstore.StatusRunning {
		t.Fatalf("target after restart = %+v, want still running (foreign owner recovers it)", rec)
	}

	var sent []sendMessageArgs
	if err := drainWatchSendsVia(t, restarted, func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("deliveries to send target = %d (%+v), want exactly one restart notice", len(sent), sent)
	}
	if sent[0].Target != "dlg_obs" {
		t.Errorf("notice target = %q, want dlg_obs", sent[0].Target)
	}
	for _, want := range []string{"watch ended", "did not survive", "job_child_owned", "still running", "re-arm"} {
		if !strings.Contains(sent[0].Message, want) {
			t.Errorf("restart notice missing %q; got:\n%s", want, sent[0].Message)
		}
	}
	// The terminal notice's claim is about the TARGET, and this target is still
	// going: saying its condition can never match would be a lie.
	if strings.Contains(sent[0].Message, "condition never matched") {
		t.Errorf("restart notice borrowed the terminal frame's claim; got:\n%s", sent[0].Message)
	}
}

// The two frames stay distinct. A target that really is terminal keeps the
// notice that explains why the condition can never match again.
func TestRestartKeepsTheTerminalFrameForATerminalTarget(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	seedCommonWatchSendTargets(t, original)
	rec, err := original.createShell(createShellOpts{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := original.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(FAIL|ok  |PASS)",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "tell me when the tests land"},
	}); err != nil {
		t.Fatalf("install watch: %v", err)
	}
	crashJobManager(t, original)

	restarted := restartJobManager(t, stateDir, "S1", func(jobNotification) {})

	var sent []sendMessageArgs
	if err := drainWatchSendsVia(t, restarted, func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("deliveries = %d (%+v), want exactly one end notice", len(sent), sent)
	}
	if !strings.Contains(sent[0].Message, "condition never matched") {
		t.Errorf("terminal target lost its own end-notice frame; got:\n%s", sent[0].Message)
	}
	if strings.Contains(sent[0].Message, "did not survive") {
		t.Errorf("terminal target got the restart-lost frame; got:\n%s", sent[0].Message)
	}
}

// One notice, once. A second restart re-reads a registry where this watch is
// already inactive, so it is not owed anything — and the frame the first restart
// left pending must not be stacked on.
func TestRestartLostNoticeIsNotRepeatedOnASecondRestart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	seedCommonWatchSendTargets(t, original)
	seedForeignOwnedWatchedJob(t, original, "job_child_owned", "watch_foreign", "wg_foreign", "dlg_obs")
	crashJobManager(t, original)

	restarted := restartJobManager(t, stateDir, "PARENT", func(jobNotification) {})
	crashJobManager(t, restarted)

	again := restartJobManager(t, stateDir, "PARENT", func(jobNotification) {})

	pending := loadWatchSendRecord(t, again).Pending
	if len(pending) != 1 {
		t.Fatalf("pending watch sends after two restarts = %d (%+v), want the one notice", len(pending), pending)
	}
	if appended := endNoticePendingCount(t, again); appended != 1 {
		t.Errorf("appended end notices = %d, want exactly one across both restarts", appended)
	}
}

// A watch whose target this log never recorded gets nothing: with no record
// there is no honest thing to say about the target, and the notice's whole
// content is what became of it.
func TestRestartSaysNothingForAWatchWhoseTargetIsUnrecorded(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	seedCommonWatchSendTargets(t, original)
	if err := original.appendEvent(jobstore.Event{
		Kind:    jobstore.EventWatchRegistered,
		TS:      original.now(),
		WatchID: "watch_orphan",
		Watch: &jobstore.WatchEvent{
			Generation:       "wg_orphan",
			OwnerSessionID:   "S1",
			VisibleSessionID: "S1",
			Target:           "job_never_recorded",
			SendTo:           "dlg_obs",
			ConfigHash:       "sha256:orphan",
		},
	}); err != nil {
		t.Fatalf("seed orphan watch: %v", err)
	}
	crashJobManager(t, original)

	restarted := restartJobManager(t, stateDir, "S1", func(jobNotification) {})

	if pending := loadWatchSendRecord(t, restarted).Pending; len(pending) != 0 {
		t.Errorf("pending watch sends = %+v, want none for a watch with no target record", pending)
	}
}
