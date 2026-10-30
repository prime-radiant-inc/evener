package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

type managerSetupCancelClient struct {
	view       appwire.ManagerView
	watchCtx   chan context.Context
	updates    chan appwire.SessionWatchUpdate
	watchCalls int
	blockSetup bool
}

func (c *managerSetupCancelClient) State(context.Context) (appwire.ManagerView, error) {
	return c.view, nil
}
func (*managerSetupCancelClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (c *managerSetupCancelClient) Watch(ctx context.Context, _ appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	c.watchCalls++
	c.watchCtx <- ctx
	if c.blockSetup {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.updates, nil
}

func TestManagerWatchSetupCancellationStopsBlockedWatchWithoutRegistration(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerSetupCancelClient{
		view:       appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true},
		watchCtx:   make(chan context.Context, 1),
		updates:    make(chan appwire.SessionWatchUpdate),
		blockSetup: true,
	}
	root.SetHubClient(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := root.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": map[string]any{"ref": "local:worker", "events": []string{"status"}}})
		result <- err
	}()
	var watchCtx context.Context
	awaitWithin(t, 10*time.Second, "client.Watch setup beginning", func() { watchCtx = <-client.watchCtx })
	cancel()
	awaitWithin(t, 10*time.Second, "blocked Watch context cancellation", func() { <-watchCtx.Done() })
	awaitWithin(t, 10*time.Second, "cancelled watch setup returning", func() {
		err := <-result
		if !errors.Is(err, context.Canceled) {
			t.Errorf("setup error = %v, want context.Canceled", err)
		}
	})
	if client.watchCalls != 1 {
		t.Fatalf("Watch calls = %d, want 1", client.watchCalls)
	}
	if jm := root.jobManager; jm != nil {
		jm.mu.Lock()
		defer jm.mu.Unlock()
		for _, cfg := range jm.watches {
			if cfg.hubRef == "local:worker" {
				t.Fatal("cancelled setup left a durable watch registration")
			}
		}
	}
}

func TestManagerWatchEstablishedSubscriptionOutlivesToolContext(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	updates := make(chan appwire.SessionWatchUpdate, 2)
	updates <- appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: "7@worker", Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:worker", InstanceID: "worker-instance"}}}, Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker"}}
	client := &managerSetupCancelClient{view: appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, watchCtx: make(chan context.Context, 1), updates: updates}
	root.SetHubClient(client)
	tokens := make(chan jobNotification, 1)
	root.jobManager.enqueue = func(n jobNotification) { tokens <- n }
	ctx, cancel := context.WithCancel(context.Background())
	_, err := root.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": map[string]any{"ref": "local:worker", "events": []string{"status"}}})
	if err != nil {
		t.Fatal(err)
	}
	awaitWithin(t, 10*time.Second, "client.Watch subscription start", func() { <-client.watchCtx })
	cancel()
	updates <- appwire.SessionWatchUpdate{Event: "status", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "worker-instance", GenerationID: "7@worker", EntryID: "after-tool-return", CausalKnown: true}}
	var token jobNotification
	awaitWithin(t, 10*time.Second, "observation after originating tool cancellation", func() { token = <-tokens })
	if token.WatchSend == nil {
		t.Fatalf("post-cancellation observation token has no watch send: %#v", token)
	}
	jm := root.jobManager
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		if cfg.hubRef == "local:worker" {
			return
		}
	}
	t.Fatal("established watch did not survive originating tool context")
}
