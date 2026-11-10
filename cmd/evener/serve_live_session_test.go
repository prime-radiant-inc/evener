package main

import (
	"testing"

	"primeradiant.com/evener/agent"
)

func TestLiveSessionRetirementReservationIsOwnedByOneExit(t *testing.T) {
	root, other := &agent.Session{}, &agent.Session{}
	live := newLiveSession(root, nil)

	if live.reserveRetirementExit(other) {
		t.Fatal("reserved the exit for a session that is not the live root")
	}
	if !live.reserveRetirementExit(root) {
		t.Fatal("refused to reserve the exit for the live root")
	}
	if live.reserveRetirementExit(root) {
		t.Fatal("reserved the exit twice")
	}

	live.releaseRetirementExit()
	if !live.reserveRetirementExit(root) {
		t.Fatal("a released reservation was not available again")
	}
}

// A reservation held by retirement means shutdown's closing pass is the tail of
// that same exit: it must leave the released session alone. The zero Session
// would panic if closeForShutdown tried to close it.
func TestLiveSessionShutdownLeavesARetirementOwnedExitAlone(t *testing.T) {
	root := &agent.Session{}
	live := newLiveSession(root, nil)
	if !live.reserveRetirementExit(root) {
		t.Fatal("could not reserve the exit")
	}

	live.closeForShutdown()

	if !live.shutdownClosed() {
		t.Fatal("shutdown's pass was not recorded")
	}
	if live.reserveRetirementExit(root) {
		t.Fatal("retirement reserved an exit after shutdown's pass ran")
	}
}

func TestLiveSessionSetSwapsSessionAndEnvironmentTogether(t *testing.T) {
	first, next := &agent.Session{}, &agent.Session{}
	live := newLiveSession(first, nil)
	live.set(next, nil)
	if live.session() != next {
		t.Fatal("set did not install the replacement session")
	}
	if live.reserveRetirementExit(first) {
		t.Fatal("the replaced session can still be reserved")
	}
}
