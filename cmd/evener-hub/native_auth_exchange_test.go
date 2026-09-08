package hub

import (
	"errors"
	"sync"
	"testing"
)

var errNativeAuthExchangeClaimed = errors.New("fixture exchange already claimed")

// nativeAuthExchangeGate is a deterministic, one-shot external-exchange hold. Exactly
// one exchange may claim an armed hold; other concurrent exchanges fail rather
// than joining a gate whose release belongs to another flow.
type nativeAuthExchangeGate struct {
	mu            sync.Mutex
	held          bool
	claimed       bool
	released      bool
	release       chan struct{}
	claimedSignal chan struct{}
}

func (g *nativeAuthExchangeGate) arm() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		return errors.New("fixture exchange already held")
	}
	g.held, g.claimed, g.released = true, false, false
	g.release = make(chan struct{})
	g.claimedSignal = make(chan struct{})
	return nil
}

func (g *nativeAuthExchangeGate) wait() error {
	g.mu.Lock()
	if !g.held {
		g.mu.Unlock()
		return nil
	}
	if g.claimed {
		g.mu.Unlock()
		return errNativeAuthExchangeClaimed
	}
	g.claimed = true
	close(g.claimedSignal)
	release := g.release
	g.mu.Unlock()
	<-release
	g.mu.Lock()
	g.held, g.claimed, g.released = false, false, true
	g.mu.Unlock()
	return nil
}

func (g *nativeAuthExchangeGate) claimedEvent() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.claimedSignal
}

func (g *nativeAuthExchangeGate) releaseHold() {
	g.mu.Lock()
	if g.held && !g.released {
		g.released = true
		close(g.release)
	}
	g.mu.Unlock()
}

func (g *nativeAuthExchangeGate) status() (held, claimed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held, g.claimed
}

func TestNativeAuthExchangeGateCompetingWaiters(t *testing.T) {
	var gate nativeAuthExchangeGate
	if err := gate.arm(); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan error, 1)
	go func() { claimed <- gate.wait() }()
	<-gate.claimedEvent()
	if err := gate.wait(); !errors.Is(err, errNativeAuthExchangeClaimed) {
		t.Fatalf("second waiter error = %v", err)
	}
	gate.releaseHold()
	if err := <-claimed; err != nil {
		t.Fatal(err)
	}
}

func TestNativeAuthExchangeGateReleaseAndRearm(t *testing.T) {
	var gate nativeAuthExchangeGate
	if err := gate.arm(); err != nil {
		t.Fatal(err)
	}
	gate.releaseHold()
	if err := gate.wait(); err != nil {
		t.Fatal(err)
	}
	if err := gate.arm(); err != nil {
		t.Fatalf("rearm after release = %v", err)
	}
	if err := gate.arm(); err == nil {
		t.Fatal("rearm while held succeeded")
	}
	gate.releaseHold()
}

func TestNativeAuthExchangeGateCleanupReleasesClaimedWaiter(t *testing.T) {
	var gate nativeAuthExchangeGate
	if err := gate.arm(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- gate.wait() }()
	<-gate.claimedEvent()
	gate.releaseHold()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if held, claimed := gate.status(); held || claimed {
		t.Fatalf("gate after cleanup = held:%v claimed:%v", held, claimed)
	}
}
