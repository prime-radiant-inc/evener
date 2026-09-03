package agent

import "testing"

func timerTick(watchID string) jobNotification {
	n := watchNotification("", "repeat")
	n.WatchID, n.Fires, n.IntervalSeconds = watchID, 1, 300
	return n
}

func TestEnqueueJobNotification_TimerTicksFoldIntoOnePendingEntry(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	for range 3 {
		s.enqueueJobNotificationAndNotify(timerTick("w1"))
	}
	s.enqueueJobNotificationAndNotify(timerTick("w2"))
	s.pendingJobNotifsMu.Lock()
	pending := append([]jobNotification(nil), s.pendingJobNotifs...)
	s.pendingJobNotifsMu.Unlock()
	if len(pending) != 2 || pending[0].WatchID != "w1" || pending[0].Fires != 3 || pending[1].Fires != 1 {
		t.Fatalf("pending = %+v, want w1 folded to 3 fires and w2 separate", pending)
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
