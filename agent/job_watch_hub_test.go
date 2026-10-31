package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type managerWatchClient struct {
	view     appwire.ManagerView
	updates  chan appwire.SessionWatchUpdate
	requests chan appwire.SessionWatchRequest
}

type managerRecoverClient struct {
	mu           sync.Mutex
	view         appwire.ManagerView
	offline      bool
	streams      chan chan appwire.SessionWatchUpdate
	watches      chan struct{}
	offlineReads chan struct{}
	ended        chan struct{}
}

type managerRejectedRebindClient struct {
	*managerRecoverClient
	attempts atomic.Int32
}

func (c *managerRejectedRebindClient) Watch(ctx context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	if c.attempts.Add(1) > 1 {
		return nil, appwire.InvalidParams("observation is no longer supported")
	}
	return c.managerRecoverClient.Watch(ctx, request)
}

func (c *managerRecoverClient) State(context.Context) (appwire.ManagerView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offline {
		select {
		case c.offlineReads <- struct{}{}:
		default:
		}
		return appwire.ManagerView{}, errors.New("binding disconnected")
	}
	return c.view, nil
}
func (*managerRecoverClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (c *managerRecoverClient) Watch(ctx context.Context, _ appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	stream := <-c.streams
	c.watches <- struct{}{}
	go func() { <-ctx.Done(); c.ended <- struct{}{} }()
	return stream, nil
}

func TestManagerWatchRuntimeRebindAndCallerReplacement(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	snapshot := appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}}, Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}}
	for _, replaceCaller := range []bool{false, true} {
		name := "rebind"
		if replaceCaller {
			name = "caller replaced during outage"
		}
		t.Run(name, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
			first, second := make(chan appwire.SessionWatchUpdate, 2), make(chan appwire.SessionWatchUpdate, 2)
			first <- snapshot
			second <- snapshot
			client := &managerRecoverClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, streams: make(chan chan appwire.SessionWatchUpdate, 2), watches: make(chan struct{}, 2), offlineReads: make(chan struct{}, 1), ended: make(chan struct{}, 2)}
			client.streams <- first
			client.streams <- second
			root.SetHubClient(client)
			result, err := root.configureHubWatchRuntime(t.Context(), map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"communicate"}, "completion": true}})
			if err != nil {
				t.Fatal(err)
			}
			_ = result
			<-client.watches
			client.mu.Lock()
			client.offline = true
			client.mu.Unlock()
			close(first)
			select {
			case <-client.offlineReads: // The consumer suspended and checked State.
			case <-t.Context().Done():
				t.Fatal("consumer did not suspend on lost connection")
			}
			jm := root.jobManager
			if jm == nil {
				t.Fatal("missing journal owner")
			}
			jm.mu.Lock()
			var watchID string
			for _, cfg := range jm.watches {
				if cfg.hubRef == ref {
					watchID = cfg.watchID
					if !cfg.hubSuspended {
						jm.mu.Unlock()
						t.Fatal("connection loss did not suspend existing watch")
					}
				}
			}
			jm.mu.Unlock()
			if watchID == "" {
				t.Fatal("connection loss destroyed watch")
			}
			client.mu.Lock()
			client.offline = false
			if replaceCaller {
				client.view.InstanceID = "replacement-manager"
			}
			client.mu.Unlock()
			root.HubBindingChanged()
			if replaceCaller {
				select {
				case <-client.ended:
				case <-t.Context().Done():
					t.Fatal("caller replacement did not end old watch")
				}
				if managerWatchActive(jm, watchID) {
					t.Fatal("caller replacement rearmed old watch")
				}
				select {
				case <-client.watches:
					t.Fatal("changed caller resubscribed old watch")
				default:
				}
			} else {
				select {
				case <-client.watches:
				case <-t.Context().Done():
					t.Fatal("matching authority did not reissue watch")
				}
				if !managerWatchActive(jm, watchID) {
					t.Fatal("rebind replaced the original watch ID")
				}
				close(second)
				select {
				case <-client.ended:
				case <-t.Context().Done():
					t.Fatal("closed rebound subscription did not end watch")
				}
			}
			interruptions, ends := 0, 0
			for _, event := range loadJobStoreEvents(t, jm) {
				if event.WatchSend == nil || event.WatchSend.Key.WatchID != watchID {
					continue
				}
				if strings.Contains(event.WatchSend.TriggerReason, "interrupted") {
					interruptions++
				}
				if event.WatchSend.EndNotice {
					ends++
				}
			}
			wantedInterruptions := 1
			if !replaceCaller {
				wantedInterruptions = 2 // Closing the rebound stream is a second loss episode.
			}
			if interruptions != wantedInterruptions || ends != 1 {
				t.Fatalf("watch notices: interruptions=%d, want %d; ends=%d, want 1", interruptions, wantedInterruptions, ends)
			}
		})
	}
}

func TestManagerWatchTargetReplacementAndUnsupportedObservation(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	for _, tc := range []struct {
		name   string
		update appwire.SessionWatchUpdate
	}{
		{"target replaced", appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: "8@worker", Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: "replacement"}}}, Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: "replacement", GenerationID: "8@worker"}}},
		{"unsupported observation", appwire.SessionWatchUpdate{Event: "unsupported", Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "bad", CausalKnown: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
			stream := make(chan appwire.SessionWatchUpdate, 2)
			stream <- appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}}, Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}}
			client := &managerRecoverClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, streams: make(chan chan appwire.SessionWatchUpdate, 1), watches: make(chan struct{}, 1), ended: make(chan struct{}, 1)}
			client.streams <- stream
			root.SetHubClient(client)
			if _, err := root.configureHubWatchRuntime(t.Context(), map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"status"}}}); err != nil {
				t.Fatal(err)
			}
			<-client.watches
			stream <- tc.update
			select {
			case <-client.ended:
			case <-t.Context().Done():
				t.Fatal("replacement/unsupported observation left watch running")
			}
			for _, event := range loadJobStoreEvents(t, root.jobManager) {
				if event.WatchSend != nil && event.WatchSend.EndNotice {
					if managerWatchActive(root.jobManager, event.WatchSend.Key.WatchID) {
						t.Fatal("ended watch is still active")
					}
					return
				}
			}
			t.Fatal("missing durable end notice")
		})
	}
}

func TestManagerWatchUnrecoverableRebindEndsDurably(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	ctx := t.Context()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	stream := make(chan appwire.SessionWatchUpdate, 1)
	stream <- appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}}, Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation}}
	base := &managerRecoverClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, streams: make(chan chan appwire.SessionWatchUpdate, 1), watches: make(chan struct{}, 1), offlineReads: make(chan struct{}, 1), ended: make(chan struct{}, 1)}
	base.streams <- stream
	client := &managerRejectedRebindClient{managerRecoverClient: base}
	root.SetHubClient(client)

	if _, err := root.configureHubWatchRuntime(t.Context(), map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"status"}}}); err != nil {
		t.Fatal(err)
	}
	<-base.watches
	base.mu.Lock()
	base.offline = true
	base.mu.Unlock()
	close(stream)
	select {
	case <-base.offlineReads:
	case <-ctx.Done():
		t.Fatal("watch did not suspend")
	}
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	root.HubBindingChanged()
	select {
	case <-base.ended:
	case <-ctx.Done():
		t.Fatal("unrecoverable subscription refusal did not end watch")
	}
	if got := client.attempts.Load(); got != 2 {
		t.Fatalf("subscription attempts=%d, want 2", got)
	}
	for _, event := range loadJobStoreEvents(t, root.jobManager) {
		if event.WatchSend != nil && event.WatchSend.EndNotice && strings.Contains(event.WatchSend.TriggerReason, "re-register") {
			return
		}
	}
	t.Fatal("unrecoverable subscription refusal did not queue a durable end notice")
}

func TestManagerWatchSubscriptionFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{"invalid parameters", appwire.InvalidParams("unsupported selector"), true},
		{"invalid request", appwire.InvalidRequest("watch not supported"), true},
		{"unknown method", appwire.WireError{Code: appwire.CodeMethodNotFound, Message: "watch missing"}, true},
		{"temporarily unavailable", appwire.Unavailable("binding changed"), false},
		{"transport failure with invalid params code", appwire.TransportFailureError{WireError: appwire.InvalidParams("transport closed")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := permanentHubWatchError(tc.err); got != tc.permanent {
				t.Fatalf("permanentHubWatchError(%v)=%t, want %t", tc.err, got, tc.permanent)
			}
		})
	}
}

func (c *managerWatchClient) State(context.Context) (appwire.ManagerView, error) {
	return c.view, nil
}
func (c *managerWatchClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (c *managerWatchClient) Watch(_ context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	c.requests <- request
	return c.updates, nil
}

func TestManagerWatchRuntimeToolRegistersAndObserves(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerWatchClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, updates: make(chan appwire.SessionWatchUpdate, 8), requests: make(chan appwire.SessionWatchRequest, 2)}
	client.updates <- appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:worker", InstanceID: "worker-instance"}}, BootGeneration: "7@worker"}, Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker"}}
	root.SetHubClient(client)

	result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: "watch", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","hub":{"ref":"local:worker","events":["communicate"],"completion":true},"intent":"Watching worker completion"}`)})
	if result.IsError {
		t.Fatalf("Manager job_watch rejected: %+v", result)
	}
	select {
	case got := <-client.requests:
		if got.Ref != "local:worker" || !got.Completion {
			t.Fatalf("request=%+v", got)
		}
	case <-t.Context().Done():
		t.Fatal("watch not subscribed")
	}
}

// An accepted terminal winner, not status or request intent, completes a root
// watch. This drives the job manager's real durable watch journal and wake rail.
func TestManagerWatchCompletionAndRestart(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	const ref, generation = "local:worker", "7@worker"
	watch, err := jm.configureHubWatch(ref, "worker-instance", generation, "local:manager", []string{"question", "communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	done := true
	rejected := false
	observations := []struct {
		name      string
		metadata  appwire.SessionObservationMetadata
		completed bool
	}{
		{"question", appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: "question", CausalKnown: true}, false},
		{"wait", appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: "wait", CausalKnown: true, EndTurn: true, EndReason: "wait", TerminalAccepted: &done}, false},
		{"stale generation", appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: "6@worker", EntryID: "stale", CausalKnown: true, EndTurn: true, EndReason: "done", TerminalAccepted: &done}, false},
		{"rejected done", appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: "rejected", CausalKnown: true, EndTurn: true, EndReason: "done", TerminalAccepted: &rejected}, false},
		{"accepted waiting", appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: "accepted-waiting", CausalKnown: true, EndTurn: true, EndReason: "wait", TerminalAccepted: &done}, false},
		{"accepted done", appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: "accepted-done", CausalKnown: true, EndTurn: true, EndReason: "done", TerminalAccepted: &done}, true},
	}
	for _, tc := range observations {
		if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Metadata: tc.metadata}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		present := managerWatchActive(jm, watch.WatchID)
		if present == tc.completed {
			t.Fatalf("%s: watch present=%t, completed=%t", tc.name, present, tc.completed)
		}
	}
	events := loadJobStoreEvents(t, jm)
	completions := 0
	for _, event := range events {
		if event.WatchSend != nil && strings.Contains(event.WatchSend.TriggerReason, "completed") {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("completion deliveries=%d, want 1", completions)
	}
}

func TestManagerWatchCompletionAfterEarlierSameEntryObservation(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	first := appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation, EntryID: "result", CausalKnown: true}
	if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: "communicate", Metadata: first}); err != nil {
		t.Fatal(err)
	}
	if !managerWatchActive(jm, watch.WatchID) {
		t.Fatal("an unaccepted entry completed the watch")
	}
	accepted := true
	terminal := first
	terminal.EndTurn, terminal.EndReason, terminal.TerminalAccepted = true, "done", &accepted
	for range 2 {
		if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: "communicate", Metadata: terminal}); err != nil {
			t.Fatal(err)
		}
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("accepted terminal evidence for an earlier entry did not complete the watch")
	}
	completions := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend != nil && strings.Contains(event.WatchSend.TriggerReason, "completed") {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("terminal evidence delivered %d completions, want exactly one", completions)
	}
}

func TestManagerWatchUnknownCausalityReplay(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	const ref, generation = "local:worker", "7@worker"
	watch, err := jm.configureHubWatch(ref, "worker-instance", generation, "local:manager", []string{"communicate"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id    string
		known bool
	}{{"old-unstamped", false}, {"current-stamped", true}} {
		if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: tc.id, CausalKnown: tc.known}}); err != nil {
			t.Fatal(err)
		}
	}
	events := loadJobStoreEvents(t, jm)
	qualified, gaps := 0, 0
	for _, event := range events {
		if event.WatchSend == nil {
			continue
		}
		if strings.Contains(event.WatchSend.TriggerReason, "replay gap") {
			gaps++
		}
		if strings.Contains(event.WatchSend.TriggerReason, "communicate") {
			qualified++
		}
	}
	if gaps != 1 || qualified != 1 {
		t.Fatalf("unknown replay or later delivery mishandled: gaps=%d qualified=%d", gaps, qualified)
	}
}

func (jm *jobManager) endHubWatches(reason string) error {
	jm.mu.Lock()
	ids := []string{}
	for _, cfg := range jm.watches {
		if cfg.hubRef != "" {
			ids = append(ids, cfg.watchID)
		}
	}
	jm.mu.Unlock()
	for _, id := range ids {
		if err := jm.endHubWatch(id, reason, nil); err != nil {
			return err
		}
	}
	return nil
}

func TestManagerWatchRebindAndDisable(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", []string{"status"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.suspendHubWatch(watch.WatchID); err != nil {
		t.Fatal(err)
	}
	present := managerWatchActive(jm, watch.WatchID)
	if !present {
		t.Fatal("transport loss destroyed watch")
	}
	if err := jm.endHubWatches("manager disabled"); err != nil {
		t.Fatal(err)
	}
	present = managerWatchActive(jm, watch.WatchID)
	if present {
		t.Fatal("disabled watch remains active")
	}
	events := loadJobStoreEvents(t, jm)
	ended := 0
	for _, event := range events {
		if event.WatchSend != nil && event.WatchSend.EndNotice {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("ended notices=%d, want 1", ended)
	}
	// A later enabled setting cannot recreate a watch that left the journal.
	if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker", CausalKnown: true, EntryID: "later"}}); err != nil {
		t.Fatal(err)
	}
	present = managerWatchActive(jm, watch.WatchID)
	if present {
		t.Fatal("off/on resurrected old watch")
	}
}

func TestManagerWatchRuntimeRestartAfterEarlierDelivery(t *testing.T) {
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, testOwnerSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	freezeClock(original)
	watch, err := original.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", []string{"communicate"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := original.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: "communicate", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker", EntryID: "first", CausalKnown: true}}); err != nil {
		t.Fatal(err)
	}
	before := loadWatchSendRecord(t, original)
	if len(before.Pending) == 0 {
		t.Fatal("earlier delivery was not durably pending before restart")
	}
	crashJobManager(t, original)
	restarted := restartJobManager(t, stateDir, testOwnerSessionID, func(jobNotification) {})
	after := loadWatchSendRecord(t, restarted)
	for key, send := range before.Pending {
		if got := after.Pending[key]; got == nil || got.DeliveryID != send.DeliveryID {
			t.Fatalf("pending ID lost for %+v: before=%+v after=%+v", key, send, got)
		}
	}
	ends := 0
	for _, event := range loadJobStoreEvents(t, restarted) {
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.EndNotice {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("restart ended notices=%d want=1", ends)
	}
}

func TestManagerWatchSnapshotLiveRaceAndPendingIdentity(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	accepted := true
	metadata := appwire.SessionObservationMetadata{EntryID: "terminal-1", EndTurn: true, EndReason: "done", TerminalAccepted: &accepted, CausalKnown: true}
	initial := appwire.SessionWatchUpdate{
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
		Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{
			Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance},
			Turns:  []appwire.Turn{{Items: []appwire.ThreadItem{{ID: "terminal-1", Observation: &metadata}}}},
		}},
	}
	if err := jm.onHubObservation(watch.WatchID, initial); err != nil {
		t.Fatal(err)
	}
	live := appwire.SessionWatchUpdate{Event: "communicate", Metadata: metadata}
	live.Metadata.Ref, live.Metadata.InstanceID, live.Metadata.GenerationID = ref, instance, generation
	if err := jm.onHubObservation(watch.WatchID, live); err != nil {
		t.Fatal(err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("completed watch remained active at wake")
	}
	completions := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend != nil && strings.Contains(event.WatchSend.TriggerReason, "completed") {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("snapshot/live completion deliveries=%d, want one", completions)
	}
}

func managerWatchActive(jm *jobManager, id string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, _, active := jm.watchConfigByIDLocked(id)
	return active
}
