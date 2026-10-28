package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
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
	adapter := newMessagingAdapter()
	parent := newMessagingRoot(t, adapter, nil)
	if _, err := parent.ProcessInput(context.Background(), "restart-route-init-638a", nil); err != nil {
		t.Fatal(err)
	}
	child := newTestSession(t)
	child.cfg.spawn.parentSessionID = parent.ID()
	child.cfg.spawn.parentSystemNotification = parent.routeSystemNotification
	parent.subagents.track(&subagent{id: child.ID(), sess: child})

	const notice, steer, mutationID = "restart-route-notice-8bb5", "restart-route-steer-294f", "restart-route-mutation-71f3"
	if _, err := parent.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: mutationID, ExpectedInstanceID: parent.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: steer}},
	}); err != nil {
		t.Fatal(err)
	}
	before := parent.clientMutations.snapshot()
	if _, ok := before.PendingExecutions[mutationID]; !ok {
		t.Fatal("client steer was not durably pending before routing")
	}
	woke := make(chan struct{}, 1)
	parent.SetNotifyFunc(func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	if !child.routeSystemNotification(parent.ID(), notice) {
		t.Fatal("child-to-parent system notification route was rejected")
	}
	waitForTestSignal(t, woke, "canonical restart notification wake")
	if !reflect.DeepEqual(parent.clientMutations.snapshot().PendingExecutions[mutationID], before.PendingExecutions[mutationID]) {
		t.Fatal("notification routing changed pending client steering")
	}

	path := transcriptPath(parent.stateDir, parent.ID())
	var id string
	for _, entry := range notificationRawEntries(t, path) {
		if entry.Turn.AttentionID == "" {
			continue
		}
		if id != "" || entry.Turn.Kind != schema.TurnSteering || entry.Turn.Message.Text() != notice || !entry.Turn.Message.Content[0].Machinery || !strings.HasPrefix(entry.Turn.AttentionID, "system:"+parent.ID()+":") {
			t.Fatalf("canonical restart notification = %+v", entry.Turn)
		}
		id = entry.Turn.AttentionID
		if len(entry.Turn.ObservedNotificationIDs) != 0 {
			t.Fatal("routing granted observation without a response")
		}
	}
	if id == "" {
		t.Fatal("restart notification has no canonical identity")
	}
	assertNotificationPending(t, parent, []string{id})
	// No notification is hidden in the retired steering queue, whose client
	// entries are owned independently by the durable mutation journal.
	queued, _, err := loadQueues(parent.stateDir, parent.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range queued {
		if entry.Text == notice {
			t.Fatal("system notice was stored as legacy steering")
		}
	}
	adapter.script("root", func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{notice, steer}, nil)
		return finalResponse("restart-route-response-cde0")
	})
	if _, err := parent.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationObservedOnce(t, path, id)
	assertNotificationPending(t, parent, []string{})
	if len(adapter.requestsFor("root")) != 2 {
		t.Fatal("restart route did not use exactly one presenting response")
	}
	steers := 0
	for _, entry := range notificationRawEntries(t, path) {
		if entry.Turn.ClientMutationID == mutationID {
			steers++
			if entry.Turn.Kind != schema.TurnSteering || entry.Turn.Message.Text() != steer {
				t.Fatalf("client steer changed: %+v", entry.Turn)
			}
		}
	}
	if steers != 1 {
		t.Fatalf("durable client steer presentations = %d, want 1", steers)
	}
}
