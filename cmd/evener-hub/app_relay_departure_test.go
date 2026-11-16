package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubRelayRetiresOnItsLastSubscribersDeparture pins prompt retirement: the
// backstop poll is an hour away here, so only the subscription registry's
// departure signal can end the relay. Before it, retirement waited for the
// 250 ms tick, and an idle relay paid a map scan four times a second until it
// came.
func TestHubRelayRetiresOnItsLastSubscribersDeparture(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Hour
	t.Cleanup(func() { hubRelayIdleInterval = previousInterval })

	thread := appwire.Thread{ID: "thread", SessionID: "session", Source: "remote", Evener: appwire.EvenerThread{Ref: "remote:thread"}}
	source := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, canceled: make(chan struct{})}
	sources := appsource.NewRegistry()
	sources.Add(source)

	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir()}
	retired := make(chan struct{})
	var once bool
	cfg.RelayHooks.AfterIdleDelete = func(gotThreadID string) {
		if gotThreadID == "thread" && !once {
			once = true
			close(retired)
		}
	}
	var relays *hubRelay
	observeHubRelay = func(got *hubRelay) { relays = got }
	server := newHubAppServer(cfg, sources)
	observeHubRelay = nil
	client := seedSubscriptionClient(t, server, "remote:thread")
	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, thread); err != nil {
		t.Fatalf("startRelay: %v", err)
	}

	// Closing the seeded connection is a departure: the server removes the
	// connection's subscriptions on disconnect, which takes this thread's live
	// interest to zero. Without the departure signal the relay would learn that
	// only from the backstop poll, an hour away here.
	if err := client.Close(); err != nil {
		t.Fatalf("close the only subscriber: %v", err)
	}

	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("the relay outlived its last subscriber by more than a second with the backstop poll an hour away: retirement still waits for the tick instead of the departure")
	}
}
