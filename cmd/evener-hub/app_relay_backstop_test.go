package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubRelayBackstopRetiresAnIdleThread covers the safety net #6423 keeps on
// purpose: prompt retirement rides the registry's departure signal, and the
// ticker is what still ends a relay whose departure never signalled. The
// interval is short here and the subscriber is dropped without going through
// the signal's paths, so the poll is the only thing that can retire it.
func TestHubRelayBackstopRetiresAnIdleThread(t *testing.T) {
	previousInterval := hubRelayIdleInterval
	hubRelayIdleInterval = 20 * time.Millisecond
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
	var relays *hubRelay
	observeHubRelay = func(got *hubRelay) { relays = got }
	newHubAppServer(cfg, sources)
	observeHubRelay = nil

	// Start the relay with no subscriber at all: nothing first subscribed and
	// nothing departs, so no departure signal ever fires and only the backstop
	// poll can see the thread idle.
	if err := relays.startRelay(context.Background(), source, appwire.ThreadReadParams{}, thread); err != nil {
		t.Fatalf("startRelay: %v", err)
	}
	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("a relay with no subscriber was never retired by the backstop poll: a departure the signal missed would leak the relay")
	}
}
