package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// crashJobManager simulates a daemon crash: the runtime disappears with jobs
// still running and watches still armed in the durable registry, and nothing
// gets a chance to tear either down. Only the store is closed — abandonRunningJobs
// would durably DROP the pending watch sends of the jobs it abandons, which is a
// teardown a crash never gets to perform.
func crashJobManager(t *testing.T, jm *jobManager) {
	t.Helper()
	if err := jm.closeStoreOnly(); err != nil {
		t.Fatalf("close crashed job store: %v", err)
	}
}

// restartJobManager opens a fresh manager over a crashed session's durable state
// and runs the job-manager half of the session restore sequence in the order
// session_init.go runs it.
func restartJobManager(t *testing.T, stateDir, sessionID string, enqueue func(jobNotification)) *jobManager {
	t.Helper()
	jm, err := newJobManagerNoSync(stateDir, sessionID, enqueue)
	if err != nil {
		t.Fatalf("restart job manager: %v", err)
	}
	jm.notifySystem = func(_ string, message string) bool {
		if enqueue != nil {
			enqueue(jobNotification{Status: jobNotificationEventWatch, Reason: message})
		}
		return true
	}
	freezeClock(jm)
	t.Cleanup(func() { _ = jm.close() })
	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}
	if err := jm.noticeUnrestoredWatchEnds(); err != nil {
		t.Fatalf("notice unrestored watch ends: %v", err)
	}
	return jm
}

// endNoticePendingCount counts the end-notice frames a manager's store has ever
// been asked to send, which the folded pending state cannot report: two notices
// for one watch coalesce into a single pending slot.
func endNoticePendingCount(t *testing.T, jm *jobManager) int {
	t.Helper()
	count := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind != jobstore.EventWatchSendPending || event.WatchSend == nil {
			continue
		}
		if strings.Contains(event.WatchSend.TriggerReason, "watch ended") {
			count++
		}
	}
	return count
}

// A restart ends every armed watch (runtime_lost) with no live config to deliver
// from. A send watch that never fired has no owner-side backstop — its target is
// another session's mailbox — so without an end notice on the send rail it waits
// on a condition that died with the daemon.
func TestWatchEndNoticeSurvivesRestartForNeverFiredSendWatch(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
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

	restarted := restartJobManager(t, stateDir, testOwnerSessionID, func(jobNotification) {})

	if pending := loadWatchSendRecord(t, restarted).Pending; len(pending) != 1 {
		t.Fatalf("pending watch sends after restart = %d (%+v), want exactly one end notice", len(pending), pending)
	}
	var sent []sendMessageArgs
	if err := drainWatchSendsVia(t, restarted, func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("deliveries to send target = %d (%+v), want exactly one end notice", len(sent), sent)
	}
	if sent[0].Target != "dlg_obs" {
		t.Errorf("end notice target = %q, want dlg_obs", sent[0].Target)
	}
	// The target's terminal facts are the restart's own: reconcileLostJobs ended
	// it stopped/runtime_lost, and the notice must name that outcome.
	for _, want := range []string{"Watch frame", "watch ended", rec.JobID, "status=stopped", "reason=runtime_lost", "output_bytes=0", "condition never matched"} {
		if !strings.Contains(sent[0].Message, want) {
			t.Errorf("end notice frame missing %q; got:\n%s", want, sent[0].Message)
		}
	}
}

// The end notice is for watches that ended unheard. A send watch that fired
// before the crash already spoke — and its frame is still durably pending — so
// the restart owes it nothing.
func TestNoRestartEndNoticeForSendWatchThatAlreadyFired(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	seedCommonWatchSendTargets(t, original)
	rec, err := original.createShell(createShellOpts{Command: "serve"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := original.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("install watch: %v", err)
	}
	feedJob(original, rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, original).Pending; len(pending) != 1 {
		t.Fatalf("pending watch sends before crash = %d (%+v), want the match", len(pending), pending)
	}
	crashJobManager(t, original)

	restarted := restartJobManager(t, stateDir, testOwnerSessionID, func(jobNotification) {})

	if pending := loadWatchSendRecord(t, restarted).Pending; len(pending) != 1 {
		t.Fatalf("pending watch sends after restart = %d (%+v), want only the match", len(pending), pending)
	}
	var sent []sendMessageArgs
	if err := drainWatchSendsVia(t, restarted, func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("deliveries to send target = %d (%+v), want only the match", len(sent), sent)
	}
	if !strings.Contains(sent[0].Message, "output_match: server ready") {
		t.Errorf("delivery = %q, want only the output_match fire", sent[0].Message)
	}
	if strings.Contains(sent[0].Message, "watch ended") {
		t.Errorf("delivery carries an end notice for a watch that fired:\n%s", sent[0].Message)
	}
}

// One notice, once. A second restart re-reads the same durable registry, where
// the watch is now inactive and its notice already pending, and must not stack a
// duplicate on top of it.
func TestRestartEndNoticeIsNotRepeatedOnASecondRestart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
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

	restarted := restartJobManager(t, stateDir, testOwnerSessionID, func(jobNotification) {})
	crashJobManager(t, restarted)

	again := restartJobManager(t, stateDir, testOwnerSessionID, func(jobNotification) {})

	pending := loadWatchSendRecord(t, again).Pending
	if len(pending) != 1 {
		t.Fatalf("pending watch sends after two restarts = %d (%+v), want the one end notice", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.TriggerReason, "watch ended") {
			t.Errorf("pending trigger = %q, want the end notice", state.TriggerReason)
		}
	}
	// Counted in the durable log, not in the folded pending state: a second notice
	// for the same watch would coalesce into the same pending slot and be
	// invisible above, while still costing the watcher a second frame.
	if appended := endNoticePendingCount(t, again); appended != 1 {
		t.Errorf("appended end notices = %d, want exactly one across both restarts", appended)
	}
}

// There are two restore-side clears in the code, but a restart only ever reaches
// one: the constructor's blanket clear deactivates every armed watch before
// reconcileLostJobs looks for watches on the jobs it just ended, so a watch lost
// with its runtime always ends runtime_lost and never auto_removed_terminal.
// That ordering is why one end-notice seam covers both.
func TestRestartEndsWatchesRuntimeLostBeforeReconcileCanSeeThem(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	rec, err := original.createShell(createShellOpts{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	installed, err := original.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(FAIL|ok  |PASS)"})
	if err != nil {
		t.Fatalf("install watch: %v", err)
	}
	crashJobManager(t, original)

	restarted := restartJobManager(t, stateDir, testOwnerSessionID, func(jobNotification) {})

	watches, err := restarted.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	watch := watches[installed.WatchID]
	if watch == nil {
		t.Fatalf("watch %q missing from the durable registry after restart", installed.WatchID)
	}
	if watch.Active || watch.EndReason != "runtime_lost" {
		t.Errorf("watch after restart = %+v, want inactive runtime_lost", watch)
	}
}

// A no-send callback watch is process-local. Restore cancels it and tells the
// watcher through the standard system-notification route, while the target's
// own job-stopped notification remains on the ordinary job-notification rail.
func TestRestartSendsSystemNotificationForNoSendWatch(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	rec, err := original.createShell(createShellOpts{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := original.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(FAIL|ok  |PASS)"}); err != nil {
		t.Fatalf("install watch: %v", err)
	}
	crashJobManager(t, original)

	var notified []jobNotification
	restarted := restartJobManager(t, stateDir, testOwnerSessionID, func(n jobNotification) { notified = append(notified, n) })

	if notices := watchChannelNotices(notified); len(notices) != 1 || notices[0].Reason != callbackWatchesCancelledAtRestartMessage {
		t.Errorf("watch-channel notices after restart = %+v, want one callback-cancellation system notification", notices)
	}
	if pending := loadWatchSendRecord(t, restarted).Pending; len(pending) != 0 {
		t.Errorf("pending watch sends = %+v, want none for a no-send watch", pending)
	}
	// The backstop the decision above leans on: the owner still learns its target
	// died, so it is not left waiting in silence.
	if len(notified) != 2 || notified[0].JobID != rec.JobID {
		t.Fatalf("owner queue = %+v, want the target's job-stopped notification and cancellation notice", notified)
	}
}
