package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
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

func TestHubWatchDeliveryStampsSelfInfluenceAndTripsRunawayFuse(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	watch, err := jm.configureHubWatch("local:worker", "worker-instance", "7@worker", "local:manager", []string{"communicate"}, false)
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watch.WatchID)
	if !ok {
		jm.mu.Unlock()
		t.Fatal("configured hub watch is missing")
	}
	priorIDs := make(map[string]struct{}, runawaySelfInfluenceDepth)
	chain := make([]provenance.Entry, 0, runawaySelfInfluenceDepth)
	for i := range runawaySelfInfluenceDepth {
		id := fmt.Sprintf("wd_hub_prior_%d", i)
		priorIDs[id] = struct{}{}
		chain = append(chain, provenance.Entry{Kind: "watch", WatchID: watch.WatchID, WatchGeneration: cfg.generation, DeliveryID: id})
	}
	jm.deliveredWatchSendIDs = priorIDs
	jm.mu.Unlock()

	causal := &provenance.Causal{
		WatchKeys: []provenance.WatchKey{{WatchID: watch.WatchID, WatchGeneration: cfg.generation}},
		Chain:     chain,
	}
	if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{
		Event: "communicate",
		Metadata: appwire.SessionObservationMetadata{
			Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker",
			EntryID: "hub-self-influenced-entry", CausalKnown: true, Causal: causal,
		},
	}); err != nil {
		t.Fatal(err)
	}

	var dropped *jobstore.WatchSendState
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil && event.WatchSend.DiagnosticReason == "runaway" {
			state := *event.WatchSend
			dropped = &state
		}
	}
	if dropped == nil {
		t.Fatal("hub self-influenced delivery did not trip the runaway drop at eight prior hops")
	}
	if dropped.SelfInfluenceDepth != runawaySelfInfluenceDepth {
		t.Fatalf("dropped SelfInfluenceDepth = %d, want %d", dropped.SelfInfluenceDepth, runawaySelfInfluenceDepth)
	}
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
				if event.WatchSend.TriggerIdentity == ref+"#interruption" && !event.WatchSend.EndNotice {
					interruptions++
				}
				if event.WatchSend.EndNotice {
					ends++
				}
			}
			// No episode delivers an interruption frame: the rebind episode
			// recovers inside the outage budget, and the closed rebound stream
			// ends through the durable re-register notice (job-control.md).
			if interruptions != 0 || ends != 1 {
				t.Fatalf("watch notices: interruptions=%d, want 0; ends=%d, want 1", interruptions, ends)
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
		if event.WatchSend != nil && event.WatchSend.Key.WatchID != "" && event.WatchSend.TriggerIdentity == ref+"#ended" && event.WatchSend.EndNotice {
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
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.TriggerIdentity == "worker-instance/7@worker/accepted-done::completed" && !event.WatchSend.EndNotice {
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
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.TriggerIdentity == instance+"/"+generation+"/result:communicate:completed" && !event.WatchSend.EndNotice {
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
		event string
		known bool
	}{{"old-unstamped", "status", false}, {"current-stamped", "communicate", true}} {
		if err := jm.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Event: tc.event, Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: "worker-instance", GenerationID: generation, EntryID: tc.id, CausalKnown: tc.known}}); err != nil {
			t.Fatal(err)
		}
	}
	events := loadJobStoreEvents(t, jm)
	qualified, gaps := 0, 0
	for _, event := range events {
		if event.WatchSend == nil {
			continue
		}
		if event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.TriggerIdentity == "worker-instance/7@worker/old-unstamped:status" && !event.WatchSend.EndNotice {
			gaps++
		}
		if event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.TriggerIdentity == "worker-instance/7@worker/current-stamped:communicate" && !event.WatchSend.EndNotice {
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
	jm.suspendHubWatch(watch.WatchID)
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
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.TriggerIdentity == "worker-instance/7@worker/terminal-1:communicate:completed" && !event.WatchSend.EndNotice {
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

// TestHubWatchEndPersistsClearWhenTheCompletionDescendsFromAStoppedChain proves
// the stopped-chain guard suppresses ordinary fires only. A terminal
// observation that descends from a chain the runaway guard already stopped
// still has to retire its watch: endHubWatch treats an unpersisted end as a
// persistence failure, and endHubWatchUntilPersisted retries that end forever,
// so a suppressed completion leaves the watch registered with no consumer.
func TestHubWatchEndPersistsClearWhenTheCompletionDescendsFromAStoppedChain(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// An earlier runaway in this watch stopped a chain. The terminal completion
	// now arrives descending from one of the fires in that chain.
	const stoppedFire = "wd_stopped_chain_fire"
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watch.WatchID)
	if !ok {
		jm.mu.Unlock()
		t.Fatal("the watch is not registered")
	}
	cfg.rememberStoppedRunawayChain([]string{stoppedFire})
	jm.mu.Unlock()

	completion := jm.watchSendSnapshot(cfg, ref, "completed: accepted terminal done", events.SessionEvent{
		SessionID:  jm.sessionID,
		Kind:       events.EventCommunicate,
		Provenance: provenance.WithWatch(nil, watch.WatchID, cfg.generation, stoppedFire, jm.sessionID, ref),
	})
	const endReason = "completed"
	if err := jm.endHubWatch(watch.WatchID, endReason, &completion); err != nil {
		t.Fatalf("ending a watch whose completion descends from a stopped chain failed: %v", err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("the stopped chain suppressed the end: the watch stayed registered")
	}
	ends := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Watch != nil && event.WatchID == watch.WatchID && event.Watch.EndReason == endReason {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("end records=%d, want 1", ends)
	}
}

func TestHubWatchCompletionRetriesTerminalSendUntilJournalRecovers(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// The journal refuses the terminal group's first write, then recovers. The
	// completion's end must retry its durable send instead of surfacing the
	// failure to the consumer, whose fallback end would leave the registered
	// watch with no consumer and drop the completion it had already verified.
	realAppendEvents := jm.appendEvents
	var failedWrite atomic.Bool
	jm.appendEvents = func(events []jobstore.Event) error {
		if !failedWrite.Swap(true) {
			return errors.New("journal write failed")
		}
		return realAppendEvents(events)
	}
	accepted := true
	completion := appwire.SessionWatchUpdate{Event: "communicate", Metadata: appwire.SessionObservationMetadata{
		Ref: ref, InstanceID: instance, GenerationID: generation,
		EntryID: "terminal-1", EndTurn: true, EndReason: "done",
		TerminalAccepted: &accepted, CausalKnown: true,
	}}
	if err := jm.onHubObservation(watch.WatchID, completion); err != nil {
		t.Fatalf("completion must ride out a transient journal failure: %v", err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("the completed watch stayed registered")
	}
	ends, deliveries := 0, 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Watch != nil && event.WatchID == watch.WatchID && event.Watch.EndReason == "completed" {
			ends++
		}
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID &&
			event.WatchSend.TriggerReason == "completed: accepted terminal done" && !event.WatchSend.EndNotice {
			deliveries++
		}
	}
	if ends != 1 {
		t.Fatalf("completed end records=%d, want 1", ends)
	}
	if deliveries != 1 {
		t.Fatalf("completion deliveries=%d, want 1", deliveries)
	}
}

func TestHubWatchEndRetriesUntilJournalRecovers(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	jm.testOnlyTerminalSendRetryDelay = 10 * time.Millisecond
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// The journal refuses the end transaction's first write, then recovers. A
	// non-completion end must retry its durable end the way the completion
	// path does: the consumer that started this end has already exited, so a
	// single failed attempt would strand the registered watch with no
	// consumer and no re-registration notice.
	realAppendEvents := jm.appendEvents
	var failedWrite atomic.Bool
	jm.appendEvents = func(events []jobstore.Event) error {
		if !failedWrite.Swap(true) {
			return errors.New("journal write failed")
		}
		return realAppendEvents(events)
	}
	const endReason = "observation failed: synthetic"
	if err := jm.endHubWatchUntilPersisted(watch.WatchID, endReason, nil); err != nil {
		t.Fatalf("end must ride out a transient journal failure: %v", err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("the ended watch stayed registered")
	}
	ends, notices := 0, 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Watch != nil && event.WatchID == watch.WatchID && event.Watch.EndReason == endReason {
			ends++
		}
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.EndNotice {
			notices++
		}
	}
	if ends != 1 {
		t.Fatalf("end records=%d, want 1", ends)
	}
	if notices != 1 {
		t.Fatalf("end notices=%d, want 1", notices)
	}
}

func TestHubWatchEndRetryYieldsToReplacement(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	jm.testOnlyTerminalSendRetryDelay = 10 * time.Millisecond
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// The journal keeps refusing this end's clear record while a
	// re-registration replaces the watch. The retrying end must yield to the
	// replacement: it stops retrying, and it never ends the replacement,
	// whose own registration already retired the old entry.
	const endReason = "observation failed: synthetic"
	realAppendEvents := jm.appendEvents
	var refused atomic.Int32
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Watch != nil && event.WatchID == watch.WatchID && event.Watch.EndReason == endReason {
				refused.Add(1)
				return errors.New("journal write failed")
			}
		}
		return realAppendEvents(events)
	}
	ended := make(chan error, 1)
	go func() {
		ended <- jm.endHubWatchUntilPersisted(watch.WatchID, endReason, nil)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for refused.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the end never attempted a journal write")
		}
		time.Sleep(time.Millisecond)
	}
	replacement, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate", "status"}, true)
	if err != nil {
		t.Fatalf("replacement registration failed: %v", err)
	}
	select {
	case err := <-ended:
		// A replacement landing mid-retry exits the loop through either
		// outcome: the next end attempt finds nothing to end and reports
		// nil, or the loop's presence check notices the replacement first
		// and returns the last write failure. Both are a yield — the
		// assertions below pin that it yielded without ending the
		// replacement.
		if err != nil && !strings.Contains(err.Error(), "journal write failed") {
			t.Fatalf("a yielded end reported an unexpected error: %v", err)
		}
		// TRIPWIRE: the retry delay is 10ms in this test, so a yielding end
		// returns within one or two attempts; only a broken retry that never
		// yields (the mutation this test guards against) reaches the bound.
	case <-time.After(10 * time.Second):
		t.Fatal("the end retry did not yield to the replacement")
	}
	if replacement.WatchID == watch.WatchID {
		t.Fatal("the replacement must be a new watch")
	}
	if !managerWatchActive(jm, replacement.WatchID) {
		t.Fatal("the replacement watch was ended by the old watch's retry")
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("the old watch stayed registered after the replacement")
	}
}

func TestHubWatchCompletionDedupWaitsForTerminalPersistence(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// While the terminal send is failing, the completion's dedup marker must
	// not be committed: the watch stays registered when the end fails, and a
	// committed marker would suppress the replay that re-delivers the
	// completion once journal writes return.
	realAppendEvents := jm.appendEvents
	firstWriteFailed := make(chan struct{})
	var failedWrite atomic.Bool
	jm.appendEvents = func(events []jobstore.Event) error {
		if !failedWrite.Swap(true) {
			close(firstWriteFailed)
			return errors.New("journal write failed")
		}
		return realAppendEvents(events)
	}
	accepted := true
	completion := appwire.SessionWatchUpdate{Event: "communicate", Metadata: appwire.SessionObservationMetadata{
		Ref: ref, InstanceID: instance, GenerationID: generation,
		EntryID: "terminal-1", EndTurn: true, EndReason: "done",
		TerminalAccepted: &accepted, CausalKnown: true,
	}}
	observed := make(chan error, 1)
	go func() {
		observed <- jm.onHubObservation(watch.WatchID, completion)
	}()
	<-firstWriteFailed
	jm.mu.Lock()
	_, cfg, present := jm.watchConfigByIDLocked(watch.WatchID)
	seen := 0
	if present && cfg.hubSeen != nil {
		seen = len(cfg.hubSeen)
	}
	jm.mu.Unlock()
	if !present || seen != 0 {
		t.Fatalf("dedup committed before the terminal send persisted: present=%v seen=%d", present, seen)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("the completed watch stayed registered")
	}
}

func TestHubWatchCompletionKeepsTerminalOwnershipPastRetryWindow(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// The journal stays down longer than the terminal send's bounded retry
	// window. The completion's end must keep owning the send until
	// persistence returns, not abandon the completion after a fixed number
	// of failed attempts and leave the registered watch with neither a
	// consumer nor the completion it already verified.
	jm.testOnlyTerminalSendRetryDelay = 10 * time.Millisecond
	realAppendEvents := jm.appendEvents
	var attempts atomic.Int32
	jm.appendEvents = func(events []jobstore.Event) error {
		if attempts.Add(1) <= 6 {
			return errors.New("journal write failed")
		}
		return realAppendEvents(events)
	}
	accepted := true
	completion := appwire.SessionWatchUpdate{Event: "communicate", Metadata: appwire.SessionObservationMetadata{
		Ref: ref, InstanceID: instance, GenerationID: generation,
		EntryID: "terminal-1", EndTurn: true, EndReason: "done",
		TerminalAccepted: &accepted, CausalKnown: true,
	}}
	if err := jm.onHubObservation(watch.WatchID, completion); err != nil {
		t.Fatalf("completion must keep its terminal ownership past the retry window: %v", err)
	}
	if managerWatchActive(jm, watch.WatchID) {
		t.Fatal("the completed watch stayed registered")
	}
	ends, deliveries := 0, 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Watch != nil && event.WatchID == watch.WatchID && event.Watch.EndReason == "completed" {
			ends++
		}
		if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID &&
			event.WatchSend.TriggerReason == "completed: accepted terminal done" && !event.WatchSend.EndNotice {
			deliveries++
		}
	}
	if ends != 1 {
		t.Fatalf("completed end records=%d, want 1", ends)
	}
	if deliveries != 1 {
		t.Fatalf("completion deliveries=%d, want 1", deliveries)
	}
}

func TestHubCompletionFromRecordedRootReplies(t *testing.T) {
	for _, tc := range []struct {
		name           string
		steps          []func(llm.Request) llm.Response
		legacyReason   string
		stopResult     string
		wantTerminal   string
		delegates, job bool
		wantMessages   int
		wantCompletion bool
	}{
		{name: "quiet with three running delegates", delegates: true, steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet", `{"message":"waiting on delegates","next":"log_quietly_and_end_turn"}`)
			},
		}},
		{name: "explicit reply with running job", job: true, wantMessages: 1, wantCompletion: true, wantTerminal: "Server up", steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"serve","mode":"background"}`)})
			},
			func(llm.Request) llm.Response { return quietYieldResponse("reply", `{"message":"Server up"}`) },
		}},
		{name: "bare-text reply", wantMessages: 1, wantCompletion: true, wantTerminal: "Server up", steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return bareTextResponse("Server up", llm.FinishReasonStop) },
			func(llm.Request) llm.Response { return agenttest.FinalResponse("Unexpected retry") },
		}},
		{name: "bare-text Stop replacement", wantMessages: 2, wantCompletion: true, wantTerminal: "Replacement", stopResult: `{"continue":false,"stopReason":"Replacement"}`, steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return bareTextResponse("Vetoed draft", llm.FinishReasonStop) },
		}},
		{name: "historical waiting_on_work", legacyReason: "waiting_on_work", wantMessages: 1},
		{name: "historical needs_response", legacyReason: "needs_response", wantMessages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newHeldJobEnvironment(t.TempDir())
			watched := newSession(t, withEnv(env), withSteps(tc.steps...), withStateDir(t.TempDir()))
			t.Cleanup(env.releaseJob)
			if tc.delegates {
				for i := range 3 {
					child := newSession(t)
					id := fmt.Sprintf("child-%d", i)
					watched.subagents.track(&subagent{id: id, sess: child, running: true})
					t.Cleanup(func() { watched.subagents.remove(id) })
				}
			}
			open := func() *transcriptindex.Index {
				t.Helper()
				idx, err := transcriptindex.Open(watched.TranscriptPath(), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := idx.Close(); err != nil {
						t.Error(err)
					}
				})
				return idx
			}
			live := open()
			if tc.stopResult != "" {
				installCommunicateStopHook(watched, func() string { return tc.stopResult })
			}
			if tc.legacyReason != "" {
				if err := watched.deliverCommunicate(events.CommunicateData{CallID: "legacy", EndTurn: true, Message: "Historical reply", EndReason: tc.legacyReason}); err != nil {
					t.Fatal(err)
				}
				state := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true}`)
				watched.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "legacy", Name: "communicate", ToolState: state}}}})
			} else {
				reply, err := watched.ProcessInput(tripwireCtx(t), "Question", nil)
				if err != nil {
					t.Fatal(err)
				}
				if tc.delegates && (!watched.quietlyYielded() || reply != "") {
					t.Fatalf("quiet reply=%q, yielded=%v", reply, watched.quietlyYielded())
				}
				if tc.job && len(sessionRunningJobIDs(watched)) != 1 {
					t.Fatal("background job not running when reply was delivered")
				}
			}
			const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
			for _, read := range []struct {
				idx    *transcriptindex.Index
				replay bool
			}{{live, false}, {open(), true}} {
				manager := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
				watch, err := manager.jobManager.configureHubWatch(ref, instance, generation, "local:manager", []string{"communicate"}, true)
				if err != nil {
					t.Fatal(err)
				}
				var window transcriptindex.Window
				if err := read.idx.ReadObserved(time.Now, func(s *transcriptindex.Snapshot, _ time.Time) error { window, err = s.Latest(40); return err }); err != nil {
					t.Fatal(err)
				}
				var turns []appwire.Turn
				var accepted []string
				for _, candidate := range window.Candidates {
					item := candidate.Item
					if item.Type != "agentMessage" {
						continue
					}
					if obs := item.Observation; obs != nil && obs.TerminalAccepted != nil && *obs.TerminalAccepted {
						accepted = append(accepted, item.Text)
						if tc.legacyReason != "" && obs.EndReason != tc.legacyReason {
							t.Errorf("replay=%t: end reason=%q, want %q", read.replay, obs.EndReason, tc.legacyReason)
						}
					}
					turn := candidate.Turn
					turn.Items = []appwire.ThreadItem{item}
					turns = append(turns, turn)
					if !read.replay && item.Observation != nil {
						obs := *item.Observation
						obs.Ref, obs.InstanceID, obs.GenerationID, obs.EntryID = ref, instance, generation, item.ID
						if err := manager.jobManager.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Metadata: obs, Event: "communicate"}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if len(turns) != tc.wantMessages {
					t.Errorf("replay=%t: agent messages=%d, want %d", read.replay, len(turns), tc.wantMessages)
				}
				wantTerminal := tc.wantTerminal
				if tc.legacyReason != "" {
					wantTerminal = "Historical reply"
				}
				if wantTerminal != "" && (len(accepted) != 1 || accepted[0] != wantTerminal) || wantTerminal == "" && len(accepted) != 0 {
					t.Errorf("replay=%t: accepted=%q, want %q", read.replay, accepted, wantTerminal)
				}
				if read.replay {
					snapshot := &appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}, Turns: turns}, BootGeneration: generation, LatestExecutionTurnID: window.LatestExecutionTurnID}
					if err := manager.jobManager.onHubObservation(watch.WatchID, appwire.SessionWatchUpdate{Snapshot: snapshot}); err != nil {
						t.Fatal(err)
					}
				}
				if active := managerWatchActive(manager.jobManager, watch.WatchID); active == tc.wantCompletion {
					t.Errorf("replay=%t: active=%t, want completion=%t", read.replay, active, tc.wantCompletion)
				}
			}
		})
	}
}

func TestHubCompletionWaitsForExplicitReplyStopHook(t *testing.T) {
	var readMidTurn func()
	watched := newSession(t, withSteps(
		func(llm.Request) llm.Response { return quietYieldResponse("first", `{"message":"First winner"}`) },
		func(llm.Request) llm.Response {
			readMidTurn()
			return quietYieldResponse("second", `{"message":"Second call"}`)
		},
	), withStateDir(t.TempDir()))
	idx, err := transcriptindex.Open(watched.TranscriptPath(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := idx.Close(); err != nil {
			t.Error(err)
		}
	}()
	check := func(want bool) {
		t.Helper()
		if err := idx.ReadObserved(time.Now, func(s *transcriptindex.Snapshot, _ time.Time) error {
			window, err := s.Latest(40)
			if err != nil {
				return err
			}
			for _, candidate := range window.Candidates {
				if candidate.Item.CallID != "first" || candidate.Item.Type != "agentMessage" {
					continue
				}
				obs := candidate.Item.Observation
				if got := obs != nil && obs.TerminalAccepted != nil && *obs.TerminalAccepted; got != want {
					t.Errorf("first call terminal acceptance=%t, want %t", got, want)
				}
				return nil
			}
			return errors.New("first communicate card not found")
		}); err != nil {
			t.Error(err)
		}
	}
	var sawBlock bool
	readMidTurn = func() { sawBlock = true; check(false) }
	stops := 0
	installCommunicateStopHook(watched, func() string {
		stops++
		if stops == 1 {
			return `{"decision":"block","reason":"Continue"}`
		}
		return `{}`
	})
	got, err := watched.ProcessInput(tripwireCtx(t), "Question", nil)
	if err != nil || !sawBlock || got != "First winner" {
		t.Fatalf("reply=%q continued=%t err=%v", got, sawBlock, err)
	}
	check(true)
}
