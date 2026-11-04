package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func rosterTestWatch(t *testing.T) (*jobManager, string) {
	t.Helper()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	result, err := root.jobManager.configureHubRosterWatch(context.Background(), "local:manager")
	if err != nil {
		t.Fatal(err)
	}
	return root.jobManager, result.WatchID
}

func rosterTestEvents(t *testing.T, jm *jobManager, id string) []jobstore.WatchSendState {
	t.Helper()
	var got []jobstore.WatchSendState
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == id {
			got = append(got, *event.WatchSend)
		}
	}
	return got
}

func rosterUpdate(revision uint64, initial bool, entries ...appwire.RosterEvent) appwire.SessionWatchUpdate {
	return appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: revision, Initial: initial, Events: entries}}
}
func rosterEntry(ref, kind string) appwire.RosterEvent {
	return appwire.RosterEvent{Ref: ref, Level: "needs_you", Kind: kind}
}

func TestHubRosterWatchSnapshot(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	registered := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchRegistered && event.Watch != nil && event.Watch.Config != nil {
			registered++
			if event.Watch.Target != hubRosterWatchKey || event.Watch.Config.Source != "top_level" {
				t.Fatalf("aggregate watch identity=%+v", event.Watch)
			}
		}
	}
	if registered != 1 {
		t.Fatalf("registered watches=%d, want one", registered)
	}
	if err := jm.onHubRoster(id, rosterUpdate(1, true, rosterEntry("local:a", "new"), rosterEntry("local:b", "needs_you"))); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, jm, id)
	if len(got) != 1 || len(rosterFrameEntries(t, got[0].Frame)) != 2 {
		t.Fatalf("snapshot deliveries=%+v", got)
	}
	frame := got[0].Frame
	const marker = "roster_events: "
	line := strings.SplitN(frame[strings.Index(frame, marker)+len(marker):], "\n", 2)[0]
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"askPending", "approvalPending", "needsResponse"} {
		if string(raw[0][flag]) != "false" {
			t.Fatalf("snapshot omitted explicit false %s: %s", flag, line)
		}
	}
}

func rosterFrameEntries(t *testing.T, frame string) []appwire.RosterEvent {
	t.Helper()
	const marker = "roster_events: "
	i := strings.Index(frame, marker)
	if i < 0 {
		t.Fatalf("missing structured roster events: %s", frame)
	}
	var entries []appwire.RosterEvent
	if err := json.Unmarshal([]byte(strings.SplitN(frame[i+len(marker):], "\n", 2)[0]), &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestHubRosterWatchDeliveredFrame(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	jm := root.jobManager
	jm.enqueue = root.enqueueJobNotification
	result, err := jm.configureHubRosterWatch(t.Context(), "local:manager")
	if err != nil {
		t.Fatal(err)
	}
	want := appwire.RosterEvent{AttentionEntry: appwire.AttentionEntry{Ref: "local:worker", Title: "A worker", Level: "needs_you", AskPending: true, ApprovalPending: true, NeedsResponse: true}, Kind: "needs_you"}
	if err := jm.onHubRoster(result.WatchID, rosterUpdate(1, true, want)); err != nil {
		t.Fatal(err)
	}
	raw := root.drainJobNotifications()
	delivered, _, _ := root.filterDeliverableJobNotifications(raw)
	if len(delivered) != 1 {
		t.Fatalf("delivered notifications=%d, want 1", len(delivered))
	}
	n := delivered[0].notification
	entries := rosterFrameEntries(t, n.watchSendFrame)
	if len(entries) != 1 || entries[0] != want {
		t.Fatalf("delivered entries=%+v, want %+v", entries, want)
	}
	if block := formatJobNotificationBlock(n, notificationExcerpt{}, false); len(rosterFrameEntries(t, block)) != 1 || rosterFrameEntries(t, block)[0] != want {
		t.Fatalf("model-visible frame=%s", block)
	}
}

func TestHubRosterWatchQueuedNotifications(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	notifications := make(chan jobNotification, 3)
	jm.enqueue = func(n jobNotification) { notifications <- n }
	for _, update := range []appwire.SessionWatchUpdate{
		rosterUpdate(1, true, rosterEntry("local:a", "new")),
		rosterUpdate(2, false, rosterEntry("local:b", "error")),
		rosterUpdate(2, false, rosterEntry("local:c", "new")),
	} {
		if err := jm.onHubRoster(id, update); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifications) != 2 {
		t.Fatalf("queued notifications=%d, want snapshot and delta only", len(notifications))
	}
	for i := range 2 {
		n := <-notifications
		if n.Kind != jobNotificationKindWatch || n.WatchSend == nil {
			t.Fatalf("notification %d is not a watch-send wake: %+v", i, n)
		}
	}
}

func TestHubRosterWatchCoalescesUndeliveredChanges(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	root.jobManager.enqueue = root.enqueueJobNotification
	result, err := root.jobManager.configureHubRosterWatch(t.Context(), "local:manager")
	if err != nil {
		t.Fatal(err)
	}
	a := rosterEntry("local:a", "new")
	b := rosterEntry("local:b", "needs_you")
	for i, entry := range []appwire.RosterEvent{a, b} {
		if err := root.jobManager.onHubRoster(result.WatchID, rosterUpdate(uint64(i+1), i == 0, entry)); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := root.jobManager.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Pending) != 1 {
		t.Fatalf("durable pending frames=%d, want one", len(pending.Pending))
	}
	for _, state := range pending.Pending {
		if len(rosterFrameEntries(t, state.Frame)) != 2 {
			t.Fatalf("durable pending entries=%s", state.Frame)
		}
	}
	delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(delivered) != 1 {
		t.Fatalf("delivered=%d, want one coalesced frame", len(delivered))
	}
	entries := rosterFrameEntries(t, formatJobNotificationBlock(delivered[0].notification, notificationExcerpt{}, false))
	if len(entries) != 2 || entries[0].Ref != a.Ref || entries[1].Ref != b.Ref {
		t.Fatalf("undelivered entries=%+v", entries)
	}
	if err := root.jobManager.settleWatchSendDelivered(delivered[0].watchCfg, delivered[0].watchState); err != nil {
		t.Fatal(err)
	}
	latest := rosterEntry("local:a", "error")
	if err := root.jobManager.onHubRoster(result.WatchID, rosterUpdate(3, false, latest)); err != nil {
		t.Fatal(err)
	}
	delivered, _, _ = root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(delivered) != 1 || len(rosterFrameEntries(t, delivered[0].notification.watchSendFrame)) != 1 || rosterFrameEntries(t, delivered[0].notification.watchSendFrame)[0].Kind != "error" {
		t.Fatalf("latest entry did not replace settled entry: %+v", delivered)
	}
}

func TestHubRosterWatchPreservesResyncAcrossQueuedDelta(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	root.jobManager.enqueue = root.enqueueJobNotification
	result, err := root.jobManager.configureHubRosterWatch(t.Context(), "local:manager")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.jobManager.onHubRoster(result.WatchID, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 1, Resync: true}}); err != nil {
		t.Fatal(err)
	}
	if err := root.jobManager.onHubRoster(result.WatchID, rosterUpdate(2, false, rosterEntry("local:b", "new"))); err != nil {
		t.Fatal(err)
	}
	pending, err := root.jobManager.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Pending) != 1 {
		t.Fatalf("durable pending frames=%d, want one", len(pending.Pending))
	}
	for _, state := range pending.Pending {
		if !strings.Contains(state.Frame, "roster_resync: true") || len(rosterFrameEntries(t, state.Frame)) != 1 {
			t.Fatalf("durable recovery lost: %s", state.Frame)
		}
	}
	delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(delivered) != 1 {
		t.Fatalf("delivered=%d, want one coalesced frame", len(delivered))
	}
	frame := formatJobNotificationBlock(delivered[0].notification, notificationExcerpt{}, false)
	if len(rosterFrameEntries(t, frame)) != 1 || rosterFrameEntries(t, frame)[0].Ref != "local:b" || !strings.Contains(frame, "roster_resync: true") {
		t.Fatalf("recovery lost in surviving frame: %s", frame)
	}
}

func TestHubRosterWatchLargeRecoveryFrame(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	root.jobManager.enqueue = root.enqueueJobNotification
	result, err := root.jobManager.configureHubRosterWatch(t.Context(), "local:manager")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.jobManager.onHubRoster(result.WatchID, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 1, Resync: true}}); err != nil {
		t.Fatal(err)
	}
	entries := make([]appwire.RosterEvent, 300)
	for i := range entries {
		entries[i] = rosterEntry(fmt.Sprintf("local:worker-%03d", i), "new")
	}
	if err := root.jobManager.onHubRoster(result.WatchID, rosterUpdate(2, true, entries...)); err != nil {
		t.Fatal(err)
	}
	delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(delivered) != 1 {
		t.Fatalf("delivered=%d, want one recovered snapshot", len(delivered))
	}
	frame := formatJobNotificationBlock(delivered[0].notification, notificationExcerpt{}, false)
	got := rosterFrameEntries(t, frame)
	frameBytes := len([]byte(frame))
	if frameBytes <= watchFrameMaxChars || frameBytes >= 64*1024 {
		t.Fatalf("300-entry model-visible frame is %d bytes, want above the ordinary %d-character cap and below 64 KiB", frameBytes, watchFrameMaxChars)
	}
	t.Logf("300-entry model-visible frame: %d bytes", frameBytes)
	if len(got) != len(entries) || got[0].Ref != entries[0].Ref || got[len(got)-1].Ref != entries[len(entries)-1].Ref || strings.Contains(frame, "roster_resync: true") {
		t.Fatalf("large recovery has %d entries, want %d", len(got), len(entries))
	}
}

func TestHubRosterWatchRuntimeSubscription(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	notifications := make(chan jobNotification, 1)
	root.jobManager.enqueue = func(n jobNotification) { notifications <- n }
	client := &managerSetupCancelClient{
		view:     appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		watchCtx: make(chan context.Context, 1),
		updates:  make(chan appwire.SessionWatchUpdate, 1),
	}
	client.updates <- rosterUpdate(1, true, rosterEntry("local:a", "new"))
	root.SetHubClient(client)
	if _, err := root.configureHubWatchRuntime(t.Context(), map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}); err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: the queued notification normally arrives immediately; ten seconds only guards a hung subscription.
	awaitWithin(t, 10*time.Second, "roster notification", func() {
		n := <-notifications
		if n.Kind != jobNotificationKindWatch || n.WatchSend == nil {
			t.Fatalf("runtime notification=%+v", n)
		}
	})
	root.jobManager.mu.Lock()
	if len(root.jobManager.watches) != 1 {
		root.jobManager.mu.Unlock()
		t.Fatalf("runtime registered watches=%d, want one", len(root.jobManager.watches))
	}
	for _, cfg := range root.jobManager.watches {
		if cfg.hubRef != hubRosterWatchKey || cfg.rosterRevision != 1 || cfg.rosterBaseline["local:a"].Ref != "local:a" {
			root.jobManager.mu.Unlock()
			t.Fatalf("runtime roster watch=%+v", cfg)
		}
	}
	root.jobManager.mu.Unlock()
}

func TestHubRosterWatchDelta(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	if err := jm.onHubRoster(id, rosterUpdate(1, true, rosterEntry("local:a", "new"))); err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubRoster(id, rosterUpdate(2, false, rosterEntry("local:b", "needs_you"))); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, jm, id)
	if len(got) != 2 || len(rosterFrameEntries(t, got[1].Frame)) != 2 || rosterFrameEntries(t, got[1].Frame)[1].Ref != "local:b" {
		t.Fatalf("delta deliveries=%+v", got)
	}
}

func TestHubRosterWatchActionableDeltaSameEntry(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	if err := jm.onHubRoster(id, rosterUpdate(1, true, rosterEntry("local:a", "new"))); err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubRoster(id, rosterUpdate(2, false, rosterEntry("local:a", "needs_you"))); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, jm, id)
	if len(got) != 2 || len(rosterFrameEntries(t, got[1].Frame)) != 1 || rosterFrameEntries(t, got[1].Frame)[0].Kind != "needs_you" {
		t.Fatalf("actionable delta deliveries=%+v", got)
	}
}

func TestHubRosterWatchDedup(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	if err := jm.onHubRoster(id, rosterUpdate(2, true, rosterEntry("local:a", "new"))); err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubRoster(id, rosterUpdate(2, false, rosterEntry("local:b", "error"))); err != nil {
		t.Fatal(err)
	}
	if got := rosterTestEvents(t, jm, id); len(got) != 1 {
		t.Fatalf("duplicate revision deliveries=%+v", got)
	}
}

func TestHubRosterWatchRebind(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	if err := jm.onHubRoster(id, rosterUpdate(10, true, rosterEntry("local:a", "new"), rosterEntry("local:gone", "idle"))); err != nil {
		t.Fatal(err)
	}
	next, err := jm.configureHubRosterWatch(context.Background(), "local:manager")
	if err != nil || next.WatchID != id {
		t.Fatalf("rebind config=%+v err=%v", next, err)
	}
	jm.resetHubRosterRevision(id)
	if err := jm.onHubRoster(id, rosterUpdate(1, true, rosterEntry("local:a", "new"), rosterEntry("local:b", "new"))); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, jm, id)
	if len(got) != 2 || len(rosterFrameEntries(t, got[1].Frame)) != 3 {
		t.Fatalf("rebind deliveries=%+v", got)
	}
	refs := map[string]string{}
	for _, entry := range rosterFrameEntries(t, got[1].Frame) {
		refs[entry.Ref] = entry.Kind
	}
	if refs["local:a"] != "new" || refs["local:b"] != "new" || refs["local:gone"] != "disappeared" || len(refs) != 3 {
		t.Fatalf("rebind changes=%+v", refs)
	}
}

func TestHubRosterWatchResync(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	if err := jm.onHubRoster(id, rosterUpdate(1, true, rosterEntry("local:a", "new"))); err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubRoster(id, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 2, Resync: true}}); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, jm, id)
	if len(got) != 2 || strings.Contains(got[1].Frame, "roster_events:") || strings.Contains(got[1].Frame, "local:a") {
		t.Fatalf("resync deliveries=%+v", got)
	}
	if err := jm.onHubRoster(id, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 4, Resync: true}}); err != nil {
		t.Fatal(err)
	}
	if got := rosterTestEvents(t, jm, id); len(got) != 2 {
		t.Fatalf("repeated resync notices=%d, want one", len(got)-1)
	}
	if err := jm.onHubRoster(id, rosterUpdate(5, false, rosterEntry("local:b", "new"))); err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(id)
	baselineCleared := ok && cfg.rosterNeedsResync && cfg.rosterBaseline == nil
	jm.mu.Unlock()
	if !baselineCleared {
		t.Fatal("a partial delta rebuilt the roster baseline after resync")
	}
	if err := jm.onHubRoster(id, rosterUpdate(6, true, rosterEntry("local:restored", "new"))); err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, ok = jm.watchConfigByIDLocked(id)
	restored := ok && !cfg.rosterNeedsResync && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline["local:restored"].Ref == "local:restored"
	jm.mu.Unlock()
	if !restored {
		t.Fatal("initial snapshot did not replace baseline after resync")
	}
}

type rosterRecoveryClient struct {
	*managerSetupCancelClient
	streams    []chan appwire.SessionWatchUpdate
	onSecond   func()
	failSecond bool
}

type rosterConcurrentClient struct {
	*managerSetupCancelClient
	calls atomic.Int32
	watch func(int32) (<-chan appwire.SessionWatchUpdate, error)
}

func (c *rosterConcurrentClient) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return c.watch(c.calls.Add(1))
}

func TestHubRosterWatchConcurrentFailedReplacement(t *testing.T) {
	t.Parallel()
	testHubRosterConcurrentReplacement(t, true)
}

func TestHubRosterWatchConcurrentReplacementRevisionOwnership(t *testing.T) {
	t.Parallel()
	testHubRosterConcurrentReplacement(t, false)
}

func testHubRosterConcurrentReplacement(t *testing.T, failFirst bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
		jm := root.jobManager
		jm.enqueue = root.enqueueJobNotification
		old := make(chan appwire.SessionWatchUpdate, 1)
		delayed := make(chan appwire.SessionWatchUpdate, 1)
		winner := make(chan appwire.SessionWatchUpdate, 2)
		old <- rosterUpdate(50, true, rosterEntry("local:old", "new"))
		delayed <- rosterUpdate(1000, true, rosterEntry("local:rejected", "error"))
		winner <- rosterUpdate(1, true, rosterEntry("local:fresh", "new"))
		entered := make(chan struct{})
		release := make(chan struct{})
		unavailable := errors.New("scripted replacement failure")
		client := &rosterConcurrentClient{
			managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
			watch: func(call int32) (<-chan appwire.SessionWatchUpdate, error) {
				switch call {
				case 1:
					return old, nil
				case 2:
					close(entered)
					<-release
					if failFirst {
						return nil, unavailable
					}
					return delayed, nil
				case 3:
					return winner, nil
				default:
					return nil, fmt.Errorf("unexpected Watch call %d", call)
				}
			},
		}
		root.SetHubClient(client)
		args := map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}
		if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		firstResult := make(chan error, 1)
		go func() {
			_, err := root.configureHubWatchRuntime(t.Context(), args)
			firstResult <- err
		}()
		<-entered // The first replacement has joined the old consumer and opened Watch.
		secondResult := make(chan error, 1)
		go func() {
			_, err := root.configureHubWatchRuntime(t.Context(), args)
			secondResult <- err
		}()
		// Without serialization the second replacement finishes here, before the
		// delayed failure or revision-1000 snapshot can damage its revision-1 stream.
		// With serialization it is blocked on the replacement gate instead.
		synctest.Wait()
		close(release)
		if err := <-firstResult; (failFirst && !errors.Is(err, unavailable)) || (!failFirst && err != nil) {
			t.Fatalf("first replacement error=%v, scripted failure=%t", err, failFirst)
		}
		if err := <-secondResult; err != nil {
			t.Fatalf("winning replacement failed: %v", err)
		}
		synctest.Wait()
		jm.mu.Lock()
		cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: hubRosterWatchKey, SendTo: runtimeMessageAliasCaller}]
		live := cfg != nil
		fresh := live && cfg.rosterBound && cfg.rosterRevision == 1 && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline["local:fresh"].Ref == "local:fresh"
		jm.mu.Unlock()
		if !live {
			t.Fatal("failed concurrent replacement ended the successful replacement's watch")
		}
		if !fresh {
			t.Fatal("delayed concurrent replacement corrupted the winning revision-1 initial snapshot")
		}
		select {
		case <-cfg.rosterConsumerDone:
			t.Fatal("winning consumer stopped")
		default:
		}
		winner <- rosterUpdate(2, false, rosterEntry("local:later", "needs_you"))
		synctest.Wait()
		jm.mu.Lock()
		updated := cfg.rosterRevision == 2 && len(cfg.rosterBaseline) == 2 && cfg.rosterBaseline["local:later"].Ref == "local:later"
		jm.mu.Unlock()
		if !updated {
			t.Fatal("winning stream's revision-2 delta did not apply")
		}
		delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
		if len(delivered) != 1 {
			t.Fatalf("delivered frames=%d, want one accumulated frame", len(delivered))
		}
		found := false
		for _, entry := range rosterFrameEntries(t, delivered[0].notification.watchSendFrame) {
			found = found || entry.Ref == "local:later"
		}
		if !found {
			t.Fatal("winning stream's later update was not delivered")
		}
	})
}

func (c *rosterRecoveryClient) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	if len(c.streams) == 1 && c.onSecond != nil {
		c.onSecond()
	}
	if len(c.streams) == 1 && c.failSecond {
		return nil, fmt.Errorf("replacement unavailable")
	}
	stream := c.streams[0]
	c.streams = c.streams[1:]
	return stream, nil
}

func TestHubRosterWatchFailedReplacementEndsWatch(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	notified := make(chan jobNotification, 3)
	root.jobManager.enqueue = func(n jobNotification) { notified <- n }
	first := make(chan appwire.SessionWatchUpdate, 1)
	first <- rosterUpdate(10, true, rosterEntry("local:old", "new"))
	client := &rosterRecoveryClient{
		managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
		streams:                  []chan appwire.SessionWatchUpdate{first, make(chan appwire.SessionWatchUpdate)},
		failSecond:               true,
	}
	root.SetHubClient(client)
	args := map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "first snapshot", func() { <-notified })
	root.jobManager.mu.Lock()
	var id string
	var oldDone <-chan struct{}
	for _, cfg := range root.jobManager.watches {
		id, oldDone = cfg.watchID, cfg.rosterConsumerDone
	}
	root.jobManager.mu.Unlock()
	if oldDone == nil {
		t.Fatal("first consumer has no completion signal")
	}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	select {
	case <-oldDone:
	default:
		t.Fatal("replacement failed before the old consumer stopped")
	}
	root.jobManager.mu.Lock()
	_, _, live := root.jobManager.watchConfigByIDLocked(id)
	root.jobManager.mu.Unlock()
	if live {
		t.Fatal("failed replacement left a live roster watch without a consumer")
	}
	got := rosterTestEvents(t, root.jobManager, id)
	if len(got) != 2 || !got[1].EndNotice {
		t.Fatalf("failed replacement has no explicit terminal delivery: %+v", got)
	}
	var terminal jobNotification
	awaitWithin(t, 10*time.Second, "end notice", func() { terminal = <-notified })
	delivered, _, _ := root.filterDeliverableJobNotifications([]jobNotification{terminal})
	if len(delivered) != 1 || !delivered[0].watchState.EndNotice {
		t.Fatalf("failed replacement did not deliver its end notice: %+v", delivered)
	}
}

func TestHubRosterWatchReplacementAcceptsLowerRevision(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	notified := make(chan jobNotification, 4)
	root.jobManager.enqueue = func(n jobNotification) { notified <- n }
	first := make(chan appwire.SessionWatchUpdate, 1)
	second := make(chan appwire.SessionWatchUpdate, 2)
	first <- rosterUpdate(50, true, rosterEntry("local:old", "new"))
	second <- rosterUpdate(1, true, rosterEntry("local:fresh", "new"))
	client := &rosterRecoveryClient{
		managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
		streams:                  []chan appwire.SessionWatchUpdate{first, second},
	}
	root.SetHubClient(client)
	args := map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "first snapshot", func() { <-notified })
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "lower-revision fresh snapshot", func() { <-notified })
	second <- rosterUpdate(2, false, rosterEntry("local:later", "needs_you"))
	awaitWithin(t, 10*time.Second, "lower-revision delta", func() { <-notified })
	root.jobManager.mu.Lock()
	var fresh bool
	for _, cfg := range root.jobManager.watches {
		fresh = cfg.rosterBound && cfg.rosterRevision == 2 && !cfg.rosterNeedsResync && len(cfg.rosterBaseline) == 2 && cfg.rosterBaseline["local:fresh"].Ref == "local:fresh" && cfg.rosterBaseline["local:later"].Ref == "local:later"
	}
	root.jobManager.mu.Unlock()
	if !fresh {
		t.Fatal("lower-revision initial and delta did not replace the old baseline")
	}
}

func TestHubRosterWatchReregisterRecoversLiveSubscription(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	notified := make(chan jobNotification, 3)
	root.jobManager.enqueue = func(n jobNotification) { notified <- n }
	first := make(chan appwire.SessionWatchUpdate, 2)
	second := make(chan appwire.SessionWatchUpdate, 1)
	first <- rosterUpdate(1, true, rosterEntry("local:old", "new"))
	second <- rosterUpdate(1, true, rosterEntry("local:fresh", "new"))
	client := &rosterRecoveryClient{
		managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
		streams:                  []chan appwire.SessionWatchUpdate{first, second},
	}
	root.SetHubClient(client)
	args := map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "initial roster", func() {
		<-notified
	})
	root.jobManager.mu.Lock()
	var id string
	for _, cfg := range root.jobManager.watches {
		id = cfg.watchID
	}
	root.jobManager.mu.Unlock()
	if id == "" {
		t.Fatal("roster watch was not registered")
	}
	if err := root.jobManager.onHubRoster(id, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 2, Resync: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, root.jobManager, id)
	if len(got) != 3 || len(rosterFrameEntries(t, got[2].Frame)) != 1 || rosterFrameEntries(t, got[2].Frame)[0].Ref != "local:fresh" {
		t.Fatalf("re-registration did not deliver fresh initial snapshot: %+v", got)
	}
}

func TestHubRosterWatchRecoveryJoinsOldConsumerBeforeSnapshot(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	notified := make(chan jobNotification, 3)
	root.jobManager.enqueue = func(n jobNotification) { notified <- n }
	first := make(chan appwire.SessionWatchUpdate, 2)
	second := make(chan appwire.SessionWatchUpdate, 1)
	first <- rosterUpdate(1, true, rosterEntry("local:old", "new"))
	second <- rosterUpdate(3, true, rosterEntry("local:fresh", "new"))
	client := &rosterRecoveryClient{managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}}, streams: []chan appwire.SessionWatchUpdate{first, second}}
	root.SetHubClient(client)
	args := map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "first snapshot", func() { <-notified })
	root.jobManager.mu.Lock()
	var id string
	var oldDone <-chan struct{}
	for _, cfg := range root.jobManager.watches {
		id, oldDone = cfg.watchID, cfg.rosterConsumerDone
	}
	root.jobManager.mu.Unlock()
	if oldDone == nil {
		t.Fatal("first consumer has no completion signal")
	}
	if err := root.jobManager.onHubRoster(id, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 2, Resync: true}}); err != nil {
		t.Fatal(err)
	}
	client.onSecond = func() {
		select {
		case <-oldDone:
		default:
			t.Fatal("new snapshot opened before old consumer stopped")
		}
		// Even a queued old-stream delta at a higher revision cannot race ahead.
		first <- rosterUpdate(100, false, rosterEntry("local:stale", "error"))
	}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	root.jobManager.mu.Lock()
	_, cfg, ok := root.jobManager.watchConfigByIDLocked(id)
	recovered := ok && !cfg.rosterNeedsResync && cfg.rosterRevision == 3 && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline["local:fresh"].Ref == "local:fresh"
	root.jobManager.mu.Unlock()
	if !recovered {
		t.Fatal("old-stream delta overrode fresh initial snapshot")
	}
}

func TestHubRosterWatchLateResyncJoinsBeforeReplacementWatch(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	notified := make(chan jobNotification, 3)
	root.jobManager.enqueue = func(n jobNotification) { notified <- n }
	first := make(chan appwire.SessionWatchUpdate, 2)
	second := make(chan appwire.SessionWatchUpdate, 1)
	first <- rosterUpdate(1, true, rosterEntry("local:old", "new"))
	second <- rosterUpdate(3, true, rosterEntry("local:fresh", "new"))
	client := &rosterRecoveryClient{
		managerSetupCancelClient: &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}},
		streams:                  []chan appwire.SessionWatchUpdate{first, second},
	}
	root.SetHubClient(client)
	args := map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "first snapshot", func() { <-notified })
	root.jobManager.mu.Lock()
	var id string
	var oldDone <-chan struct{}
	for _, cfg := range root.jobManager.watches {
		id, oldDone = cfg.watchID, cfg.rosterConsumerDone
	}
	root.jobManager.mu.Unlock()
	if oldDone == nil {
		t.Fatal("first consumer has no completion signal")
	}
	client.onSecond = func() {
		// This callback is after the early resync check, at the new Watch call.
		if err := root.jobManager.onHubRoster(id, appwire.SessionWatchUpdate{Roster: &appwire.SessionRosterUpdate{Revision: 2, Resync: true}}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-oldDone:
		default:
			t.Fatal("late resync can race the still-running old consumer")
		}
		first <- rosterUpdate(100, false, rosterEntry("local:stale", "error"))
	}
	if _, err := root.configureHubWatchRuntime(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	root.jobManager.mu.Lock()
	_, cfg, ok := root.jobManager.watchConfigByIDLocked(id)
	recovered := ok && !cfg.rosterNeedsResync && cfg.rosterRevision == 3 && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline["local:fresh"].Ref == "local:fresh"
	root.jobManager.mu.Unlock()
	if !recovered {
		t.Fatal("late resync left the baseline cleared or let the old delta win")
	}
}

func TestHubRosterWatchLargeSnapshotKeepsEntries(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	want := rosterEntry("local:"+strings.Repeat("a", watchTriggerMaxChars), "new")
	if err := jm.onHubRoster(id, rosterUpdate(1, true, want)); err != nil {
		t.Fatal(err)
	}
	got := rosterTestEvents(t, jm, id)
	if len(got) != 1 || len([]rune(got[0].TriggerReason)) > watchTriggerMaxChars || len(rosterFrameEntries(t, got[0].Frame)) != 1 || rosterFrameEntries(t, got[0].Frame)[0].Ref != want.Ref {
		t.Fatalf("oversized snapshot notification=%+v", got)
	}
}
