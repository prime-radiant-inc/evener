package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func newTestRelayHandle() (*hubRelayHandle, *bool) {
	cancelled := false
	return &hubRelayHandle{
		ready:         make(chan struct{}),
		cancel:        func() { cancelled = true },
		done:          make(chan struct{}),
		relayKeys:     map[string]*relayKeyState{},
		pendingKeys:   map[string]*relayKeyState{},
		pendingStates: map[*relayKeyState]struct{}{},
		routes:        map[string]*relayKeyState{},
		routeChanged:  make(chan struct{}),
	}, &cancelled
}

func newTestRelayKeyState(relayKey string) *relayKeyState {
	return &relayKeyState{
		relayKey:    relayKey,
		routingKeys: map[string]struct{}{},
		done:        make(chan struct{}),
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// publishState registers state as the published owner of its relay key.
func publishState(r *hubRelay, handle *hubRelayHandle, state *relayKeyState) {
	r.relayed[state.relayKey] = handle
	handle.relayKeys[state.relayKey] = state
}

func TestHubRelayBindingARouteMovesItBetweenStates(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	handle, _ := newTestRelayHandle()
	first := newTestRelayKeyState("k1")
	second := newTestRelayKeyState("k2")

	r.bindStateRouteLocked(handle, "route", first)
	r.bindStateRouteLocked(handle, "", first)
	if handle.routes["route"] != first || len(handle.routes) != 1 {
		t.Fatalf("routes after first bind = %v", handle.routes)
	}

	r.bindStateRouteLocked(handle, "route", second)
	if handle.routes["route"] != second {
		t.Fatal("rebinding did not move the route to the new state")
	}
	if _, stale := first.routingKeys["route"]; stale {
		t.Fatal("previous state still claims the moved route")
	}

	r.removeStateRoutesLocked(handle, nil)
	r.removeStateRoutesLocked(handle, first)
	if handle.routes["route"] != second {
		t.Fatal("removing routes for a state that no longer owns them dropped another state's route")
	}
	r.removeStateRoutesLocked(handle, second)
	if len(handle.routes) != 0 || len(second.routingKeys) != 0 {
		t.Fatalf("routes not cleared: %v %v", handle.routes, second.routingKeys)
	}
}

func TestHubRelayPendingStatesAreTrackedPerOwner(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	owner, _ := newTestRelayHandle()
	other, _ := newTestRelayHandle()
	a := newTestRelayKeyState("k")
	b := newTestRelayKeyState("k")

	r.registerPendingStateLocked(owner, a)
	r.registerPendingStateLocked(owner, b)
	if len(r.pending["k"]) != 2 || owner.pendingKeys["k"] != b {
		t.Fatalf("pending = %v keys = %v", r.pending["k"], owner.pendingKeys)
	}

	if r.unregisterPendingStateLocked(other, a) {
		t.Fatal("a handle that does not own the pending state unregistered it")
	}
	if !r.unregisterPendingStateLocked(owner, a) {
		t.Fatal("owner failed to unregister its pending state")
	}
	if owner.pendingKeys["k"] != b {
		t.Fatal("unregistering one state cleared the other's key")
	}
	if !r.unregisterPendingStateLocked(owner, b) {
		t.Fatal("owner failed to unregister its last pending state")
	}
	if _, ok := r.pending["k"]; ok || len(owner.pendingStates) != 0 || len(owner.pendingKeys) != 0 {
		t.Fatalf("pending bookkeeping not emptied: %v %v %v", r.pending, owner.pendingStates, owner.pendingKeys)
	}
	if r.unregisterPendingStateLocked(owner, a) {
		t.Fatal("unregistering twice reported success")
	}
}

func TestHubRelayRemovingAKeyStateRequiresNoPublicationsAndOwnership(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	handle, _ := newTestRelayHandle()
	other, _ := newTestRelayHandle()
	state := newTestRelayKeyState("k")
	publishState(r, handle, state)

	state.publications = 1
	if r.removeRelayKeyStateLocked(handle, state) {
		t.Fatal("removed a state with a publication in flight")
	}
	state.publications = 0
	if r.removeRelayKeyStateLocked(other, state) {
		t.Fatal("removed a state through a handle that does not publish it")
	}
	if r.removeRelayKeyStateLocked(handle, nil) {
		t.Fatal("removed a nil state")
	}
	if !r.removeRelayKeyStateLocked(handle, state) {
		t.Fatal("did not remove an idle published state")
	}
	if !state.retiring || !isClosed(state.done) || r.relayed["k"] != nil || handle.relayKeys["k"] != nil {
		t.Fatal("removal left the state published")
	}
}

func TestHubRelayRemovingAHandleReleasesEveryRegistration(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	handle, _ := newTestRelayHandle()
	canonical := appwire.Ref{SourceID: "host", ThreadID: "thread"}
	handle.canonical = canonical
	r.canonical[canonical] = handle
	published := newTestRelayKeyState("published")
	publishState(r, handle, published)
	pending := newTestRelayKeyState("pending")
	r.registerPendingStateLocked(handle, pending)
	r.bindStateRouteLocked(handle, "route", published)

	published.publications = 1
	if r.removeRelayHandleLocked(handle) {
		t.Fatal("removed a handle with a publication in flight")
	}
	published.publications = 0
	if r.removeRelayHandleLocked(nil) {
		t.Fatal("removed a nil handle")
	}
	if !r.removeRelayHandleLocked(handle) {
		t.Fatal("did not remove an idle handle")
	}
	if !handle.removed || !isClosed(handle.done) || !isClosed(published.done) || !isClosed(pending.done) {
		t.Fatal("handle or its states were not finished")
	}
	if len(r.relayed) != 0 || len(r.pending) != 0 || len(r.canonical) != 0 || len(handle.routes) != 0 {
		t.Fatalf("registrations remain: %v %v %v %v", r.relayed, r.pending, r.canonical, handle.routes)
	}
	if r.removeRelayHandleLocked(handle) {
		t.Fatal("removed an already removed handle")
	}
}

func TestHubRelayFinishingAHandleWaitsForInitializationAndCommands(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	handle, _ := newTestRelayHandle()

	handle.initializing = true
	if r.maybeFinishHandleLocked(handle) {
		t.Fatal("finished a handle that is still initializing")
	}
	handle.initializing = false
	handle.commandOwners = 1
	if r.maybeFinishHandleLocked(handle) {
		t.Fatal("finished a handle with commands in flight")
	}
	handle.commandOwners = 0
	if r.maybeFinishHandleLocked(nil) {
		t.Fatal("finished a nil handle")
	}
	if !r.maybeFinishHandleLocked(handle) {
		t.Fatal("did not finish an idle handle")
	}
	if !isClosed(handle.ready) || !errors.Is(handle.err, context.Canceled) {
		t.Fatalf("ready closed=%v err=%v", isClosed(handle.ready), handle.err)
	}
	r.finishHandleLocked(handle, errors.New("later"))
	if handle.err == nil || errors.Is(handle.err, context.Canceled) {
		t.Fatalf("finishing twice should record the latest error, got %v", handle.err)
	}
}

func TestHubRelayStoppedStatesDrainOnlyWhenIdle(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	handle, _ := newTestRelayHandle()
	pending := newTestRelayKeyState("pending")
	r.registerPendingStateLocked(handle, pending)
	published := newTestRelayKeyState("published")
	publishState(r, handle, published)

	pending.commands = 1
	if r.removeStoppedStateLocked(handle, pending) {
		t.Fatal("removed a pending state with commands in flight")
	}
	pending.commands = 0
	if r.removeStoppedStateLocked(handle, nil) {
		t.Fatal("removed a nil state")
	}
	if !r.removeStoppedStateLocked(handle, pending) || !isClosed(pending.done) || len(r.pending) != 0 {
		t.Fatal("idle pending state was not unregistered")
	}
	published.publications = 1
	if r.removeStoppedStateLocked(handle, published) {
		t.Fatal("removed a published state with a publication in flight")
	}
	published.publications = 0
	if !r.removeStoppedStateLocked(handle, published) || r.relayed["published"] != nil {
		t.Fatal("idle published state was not removed")
	}
}

func TestHubRelayRetiringAHandleClosesItOnceIdle(t *testing.T) {
	r := newHubRelay(nil, hubcore.WebConfig{}, nil)
	handle, cancelled := newTestRelayHandle()
	published := newTestRelayKeyState("published")
	publishState(r, handle, published)
	pending := newTestRelayKeyState("pending")
	r.registerPendingStateLocked(handle, pending)

	published.publications = 1
	r.retireRelayHandle(handle)
	if !handle.stopping || !published.stopRequested || !published.retiring || !pending.stopRequested {
		t.Fatal("retirement did not flag the handle and its states")
	}
	if *cancelled || handle.removed {
		t.Fatal("handle closed while a publication was still in flight")
	}

	published.publications = 0
	r.retireRelayHandle(handle)
	if !*cancelled || !handle.removed || len(r.relayed) != 0 || len(r.pending) != 0 {
		t.Fatalf("idle handle not closed and removed: cancelled=%v removed=%v", *cancelled, handle.removed)
	}
}
