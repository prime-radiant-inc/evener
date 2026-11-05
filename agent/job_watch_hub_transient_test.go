package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
)

// flakyStateClient fails its first state read after the watch suspends, then
// answers normally. No rebind ever happens: only the watch's own retry moves it.
type flakyStateClient struct {
	*managerRecoverClient
	mu       sync.Mutex
	failures int
	failed   chan struct{}
}

func (c *flakyStateClient) State(ctx context.Context) (appwire.ManagerView, error) {
	c.mu.Lock()
	if c.failures > 0 {
		c.failures--
		c.mu.Unlock()
		select {
		case c.failed <- struct{}{}:
		default:
		}
		return appwire.ManagerView{}, appwire.Unavailable("temporarily overloaded")
	}
	c.mu.Unlock()
	return c.managerRecoverClient.State(ctx)
}

func (c *flakyStateClient) failNext(n int) {
	c.mu.Lock()
	c.failures = n
	c.mu.Unlock()
}

// TestManagerWatchResolvesTransientFailureWithoutRebind pins that one failed
// state read does not park a watch forever.
//
// The recovery loop waited only on a binding signal captured before the read.
// That signal fires on a rebind, not when a transient error clears, so a single
// two-second state timeout against an otherwise healthy connection left the
// watch suspended with nobody to notice. The loop's own "ended unexpectedly" arm
// is the right answer once the connection is proven healthy again; the defect
// was never resolving the question.
func TestManagerWatchResolvesTransientFailureWithoutRebind(t *testing.T) {
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	ctx := t.Context()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	first := make(chan appwire.SessionWatchUpdate, 1)
	first <- appwire.SessionWatchUpdate{
		Snapshot: &appwire.ThreadReadResponse{BootGeneration: generation, Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: ref, InstanceID: instance}}},
		Metadata: appwire.SessionObservationMetadata{Ref: ref, InstanceID: instance, GenerationID: generation},
	}
	base := &managerRecoverClient{
		view:         appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		streams:      make(chan chan appwire.SessionWatchUpdate, 2),
		watches:      make(chan struct{}, 2),
		offlineReads: make(chan struct{}, 1),
		ended:        make(chan struct{}, 2),
	}
	base.streams <- first
	client := &flakyStateClient{managerRecoverClient: base, failed: make(chan struct{}, 1)}
	root.SetHubClient(client)
	result, err := root.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"status"}}})
	if err != nil {
		t.Fatal(err)
	}
	<-base.watches

	// Drop the stream on a healthy connection, and make the first read of the
	// recovery loop fail. The connection stays up for everything after it.
	base.mu.Lock()
	base.offline = true
	base.mu.Unlock()
	close(first)
	select {
	case <-base.offlineReads:
	case <-ctx.Done():
		t.Fatal("watch did not suspend")
	}
	base.mu.Lock()
	base.offline = false
	base.mu.Unlock()
	client.failNext(1)

	select {
	case <-client.failed:
	case <-ctx.Done():
		t.Fatal("watch never retried after a transient failure")
	}
	// The retry proves the connection healthy again, so the watch must resolve
	// into a durable end notice rather than staying suspended.
	select {
	case <-base.ended:
	case <-ctx.Done():
		t.Fatal("watch stayed suspended after a transient failure; it waits for a rebind that may never come")
	}
	jm := root.jobManager
	noticed := false
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchSend != nil && event.WatchSend.TriggerIdentity == ref+"#ended" && event.WatchSend.EndNotice {
			noticed = true
		}
	}
	if !noticed {
		t.Fatalf("watch ended without a durable re-register notice: %+v", result)
	}
}
