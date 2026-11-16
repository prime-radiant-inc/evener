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
//
// The bound below is a hang guard, not a latency assertion. With the backstop
// an hour away, retirement happening at all proves the departure path ran, so
// what this test pins is which path retires the relay and not how quickly.
// A tight deadline would measure the scheduler instead: a contended CI worker
// can take longer than a second to schedule the fan-out goroutine after the
// departure closes its channel, and that delay says nothing about the code
// under test.
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
	case <-time.After(relayDepartureHangGuard):
		t.Fatal("the relay never retired after its last subscriber departed, with the backstop poll an hour away: retirement still waits for the tick instead of the departure")
	}
}

// relayDepartureHangGuard bounds only "the relay never retired at all". The
// backstop interval is an hour in this test, so a retirement inside this window
// cannot be the tick's: it is the departure path or nothing.
const relayDepartureHangGuard = 30 * time.Second
