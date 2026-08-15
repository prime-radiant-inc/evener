package agent

import "testing"

func crashJobManager(t *testing.T, jm *jobManager) {
	t.Helper()
	if err := jm.closeStoreOnly(); err != nil {
		t.Fatalf("close crashed job store: %v", err)
	}
}

func restartJobManager(t *testing.T, stateDir, sessionID string, enqueue func(jobNotification)) *jobManager {
	t.Helper()
	jm, err := newJobManagerNoSync(stateDir, sessionID, enqueue)
	if err != nil {
		t.Fatalf("restart job manager: %v", err)
	}
	jm.notifySystem = func(_ string, message string) bool {
		if enqueue != nil {
			enqueue(jobNotification{Kind: jobNotificationKindWatch, Status: jobNotificationEventWatch, Reason: message})
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
		t.Fatalf("watch %q missing from durable registry after restart", installed.WatchID)
	}
	if watch.Active || watch.EndReason != "runtime_lost" {
		t.Errorf("watch after restart = %+v, want inactive runtime_lost", watch)
	}
}

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
	if len(notified) != 2 || notified[0].JobID != rec.JobID {
		t.Fatalf("owner queue = %+v, want the target's job-stopped notification and cancellation notice", notified)
	}
}
