package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/provenance"
)

func timerTick(watchID string) jobNotification {
	n := watchNotification("", "repeat")
	n.WatchID, n.Fires, n.IntervalSeconds = watchID, 1, 300
	return n
}

func TestEnqueueJobNotification_TimerTicksFoldIntoOnePendingEntry(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	for i := range 3 {
		tick := timerTick("w1")
		// Each fire carries its own lineage; the fold must keep all of them,
		// because the notification turn stamps the union of what it delivers.
		tick.Provenance = provenance.WithWatch(nil, "w1", fmt.Sprintf("wg_%d", i), fmt.Sprintf("wd_%d", i), "session_1", "caller")
		s.enqueueJobNotificationAndNotify(tick)
	}
	s.enqueueJobNotificationAndNotify(timerTick("w2"))
	s.pendingJobNotifsMu.Lock()
	pending := append([]jobNotification(nil), s.pendingJobNotifs...)
	s.pendingJobNotifsMu.Unlock()
	if len(pending) != 2 || pending[0].WatchID != "w1" || pending[0].Fires != 3 || pending[1].Fires != 1 {
		t.Fatalf("pending = %+v, want w1 folded to 3 fires and w2 separate", pending)
	}
	for i := range 3 {
		if !provenance.ContainsWatch(pending[0].Provenance, "w1", fmt.Sprintf("wg_%d", i)) {
			t.Fatalf("folded provenance = %+v, want the lineage of every folded tick (missing wg_%d)", pending[0].Provenance, i)
		}
	}
}

func TestRequeueJobNotifications_FoldsIntoPendingTick(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.enqueueJobNotificationAndNotify(timerTick("w1"))
	drained := timerTick("w1")
	drained.Fires = 2
	s.requeueJobNotifications([]jobNotification{drained})
	s.pendingJobNotifsMu.Lock()
	pending := append([]jobNotification(nil), s.pendingJobNotifs...)
	s.pendingJobNotifsMu.Unlock()
	if len(pending) != 1 || pending[0].Fires != 3 {
		t.Fatalf("pending = %+v, want one entry with 3 fires", pending)
	}
}

func TestRequeueJobNotifications_KeepsRequeuedBatchFirst(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.enqueueJobNotificationAndNotify(jobNotification{JobID: "job_pending"})
	requeued := []jobNotification{
		{JobID: "job_first"},
		{JobID: "job_second"},
	}
	s.requeueJobNotifications(requeued)
	s.pendingJobNotifsMu.Lock()
	pending := append([]jobNotification(nil), s.pendingJobNotifs...)
	s.pendingJobNotifsMu.Unlock()
	if len(pending) != 3 {
		t.Fatalf("pending = %+v, want three entries", pending)
	}
	if pending[0].JobID != "job_first" || pending[1].JobID != "job_second" || pending[2].JobID != "job_pending" {
		t.Fatalf("order = %q/%q/%q, want the requeued batch first, in order, ahead of what was pending",
			pending[0].JobID, pending[1].JobID, pending[2].JobID)
	}
}

func TestEnqueueJobNotification_NonTimerAppendsWithoutFolding(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.enqueueJobNotificationAndNotify(watchNotification("", "progress_tick"))
	s.enqueueJobNotificationAndNotify(watchNotification("", "progress_tick"))
	s.pendingJobNotifsMu.Lock()
	n := len(s.pendingJobNotifs)
	s.pendingJobNotifsMu.Unlock()
	if n != 2 {
		t.Fatalf("non-timer notifications must not fold: pending=%d", n)
	}
}

func TestFilterDeliverableJobNotifications_DropsOrphanedTickKeepsTerminal(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	live, err := s.jobManager.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	orphan := timerTick("w-gone")
	fired := timerTick("w-fired")
	fired.Reason, fired.Terminal = "after", true
	survivors, _, _ := s.filterDeliverableJobNotifications([]jobNotification{timerTick(live.WatchID), orphan, fired})
	if len(survivors) != 2 {
		t.Fatalf("survivors = %+v, want the live tick and the terminal fire", survivors)
	}
	for _, d := range survivors {
		if d.notification.WatchID == "w-gone" {
			t.Fatal("orphaned tick must be dropped before the batch gate")
		}
	}
}
