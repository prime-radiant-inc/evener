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
//
// A wide bound alone would leave one real hole: a departure closes the channel
// that exists at that moment and installs a replacement, so a supervisor that
// read the channel after a departure had already closed it would wait on the
// replacement and never learn the thread went idle. startRelay therefore takes
// the departure channel before it registers the subscription, which makes this
// test deterministic rather than merely slow to fail: the close below can only
// happen after startRelay returns, and by then the supervisor already holds the
// channel that close ends.
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

// TestHubRelaySeesADepartureThatLandsWhileItIsStarting pins the ordering the
// relay's departure capture depends on. A departure closes the channel that
// exists at that moment and installs a replacement, so a supervisor that read
// the channel only once its goroutine got scheduled would be watching the
// replacement and would miss a departure that landed while the relay was still
// being established. The backstop poll is an hour away here, so the only way
// this relay retires is the departure signal reaching the supervisor.
//
// The departure is fired from AfterReady, which publishRelay calls
// synchronously after the subscription is registered and before
// launchRelaySupervisor starts the supervisor goroutine: the exact window in
// which no supervisor exists yet to read the channel.
func TestHubRelaySeesADepartureThatLandsWhileItIsStarting(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = time.Hour
	t.Cleanup(func() { hubRelayIdleInterval = previousInterval })

	thread := appwire.Thread{ID: "thread", SessionID: "session", Source: "remote", Evener: appwire.EvenerThread{Ref: "remote:thread"}}
	source := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, canceled: make(chan struct{})}
	sources := appsource.NewRegistry()
	sources.Add(source)

	retired := make(chan struct{})
	var once bool
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir()}
	cfg.RelayHooks.AfterIdleDelete = func(gotThreadID string) {
		if gotThreadID == "thread" && !once {
			once = true
			close(retired)
		}
	}
	// The hook must be installed before the server is built: newHubAppServer
	// takes its config by value, so a hook assigned afterwards never reaches
	// the relay. It closes over a variable instead, so the subscriber it
	// depends on can be created after the server.
	depart := func() {}
	cfg.RelayHooks.AfterReady = func(gotThreadID string) {
		if gotThreadID != "thread" {
			return
		}
		depart()
	}
	var relays *hubRelay
	observeHubRelay = func(got *hubRelay) { relays = got }
	client := seedSubscriptionClient(t, newHubAppServer(cfg, sources), "remote:thread")
	observeHubRelay = nil
	depart = func() { _ = client.Close() }

	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, thread); err != nil {
		t.Fatalf("startRelay: %v", err)
	}

	select {
	case <-retired:
	case <-time.After(relayDepartureHangGuard):
		t.Fatal("the relay never retired though its only subscriber departed while the relay was starting: the supervisor captured the departure channel after that departure had already closed it, so it waits on the replacement and only the backstop poll, an hour away here, would ever end it")
	}
}
