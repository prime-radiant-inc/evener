package agent

import (
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// No-send watch delivery remains live-only by design. The durable registry names
// the receiver so restore can cancel the lost callback and route one explicit
// system notification to that agent; it does not reconstruct individual fires.
func TestNoSendReceiverWatchFiresWithoutLeavingDurableEvidence(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "npm run dev"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	var received []jobNotification
	installed, err := jm.configureWatch(watchArgs{
		Target:            rec.JobID,
		OutputMatch:       "ready",
		ReceiverSessionID: "S-parent",
		ReceiverNotify:    func(n jobNotification) { received = append(received, n) },
	})
	if err != nil {
		t.Fatalf("install receiver watch: %v", err)
	}

	feedJob(jm, rec.JobID, []byte("server ready\n"))

	// It matched, and only the live callback knows.
	if len(watchChannelNotices(received)) != 1 {
		t.Fatalf("receiver notifications = %+v, want the one output_match fire", received)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	watch := watches[installed.WatchID]
	if watch == nil {
		t.Fatalf("watch %q missing from the durable registry", installed.WatchID)
	}
	// The registry names the receiver, so a restart can ask "whose watch was
	// this".
	if watch.ReceiverSessionID != "S-parent" {
		t.Errorf("registry receiver = %q, want S-parent", watch.ReceiverSessionID)
	}
	// No durable trace of the fire exists. The registry rows are the only events
	// this watch ever wrote.
	registryRows := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchID != installed.WatchID {
			continue
		}
		switch event.Kind {
		case jobstore.EventWatchRegistered, jobstore.EventWatchCleared:
			registryRows++
		default:
			t.Errorf("unexpected durable event %q for a no-send watch — callback cancellation does not reconstruct individual fires", event.Kind)
		}
	}
	// Guards the guard: a scan that matched nothing would pass the loop above
	// while proving nothing at all.
	if registryRows == 0 {
		t.Fatalf("scanned no events for watch %q; the evidence check above is vacuous", installed.WatchID)
	}
}

func TestRestartCancelsNoSendReceiverWatchWithSystemNotification(t *testing.T) {
	t.Parallel()
	const wantMessage = "<system-notification>All your callback watches were cancelled because the agent restarted. No further deliveries will occur. If you still want a callback, re-register it with the job_watch tool.</system-notification>"
	if callbackWatchesCancelledAtRestartMessage != wantMessage {
		t.Fatalf("restart cancellation message = %q, want exact system notification", callbackWatchesCancelledAtRestartMessage)
	}
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	freezeClock(original)
	rec, err := original.createShell(createShellOpts{Command: "npm run dev"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := original.configureWatch(watchArgs{
		Target:            rec.JobID,
		OutputMatch:       "ready",
		ReceiverSessionID: "S-parent",
		ReceiverNotify:    func(jobNotification) {},
	}); err != nil {
		t.Fatalf("install receiver watch: %v", err)
	}
	crashJobManager(t, original)

	var received []jobNotification
	restartJobManager(t, stateDir, testOwnerSessionID, func(n jobNotification) {
		received = append(received, n)
	})

	notices := watchChannelNotices(received)
	if len(notices) != 1 {
		t.Fatalf("restart cancellation notices = %+v, want exactly one", received)
	}
	if notices[0].Reason != wantMessage {
		t.Fatalf("restart cancellation notice = %q, want %q", notices[0].Reason, wantMessage)
	}
}

func TestRestartCancellationRoutesThroughParentSystemNotificationQueue(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	parent.stateDir = t.TempDir()
	child := newTestSession(t)
	child.cfg.spawn.parentSessionID = parent.ID()
	child.cfg.spawn.parentSystemNotification = parent.routeSystemNotification
	parent.subagents.track(&subagent{id: child.ID(), sess: child})

	if !child.routeSystemNotification(parent.ID(), callbackWatchesCancelledAtRestartMessage) {
		t.Fatal("child-to-parent system notification route was rejected")
	}

	parent.mu.Lock()
	if len(parent.steeringQueue) != 1 {
		parent.mu.Unlock()
		t.Fatalf("parent steering queue = %+v, want one system notification", parent.steeringQueue)
	}
	message := parent.steeringQueue[0]
	parent.mu.Unlock()
	if message.Text != callbackWatchesCancelledAtRestartMessage {
		t.Errorf("system notification text = %q, want %q", message.Text, callbackWatchesCancelledAtRestartMessage)
	}
	if message.Kind != events.SteeringKindNotification {
		t.Errorf("system notification kind = %q, want %q", message.Kind, events.SteeringKindNotification)
	}
	steering, _, err := loadQueues(parent.stateDir, parent.ID())
	if err != nil {
		t.Fatalf("load persisted system notification: %v", err)
	}
	if len(steering) != 1 || steering[0].Text != callbackWatchesCancelledAtRestartMessage {
		t.Fatalf("persisted steering queue = %+v, want one system notification", steering)
	}
}
