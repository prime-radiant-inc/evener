package server

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

// TestSubmitNotification_DeliveredOnceTheSlotFrees pins that a notification
// wake is guaranteed, not best-effort.
//
// The input channel holds one message. A notification kick that arrives while
// that slot is occupied used to be dropped outright: for a session that is
// mid-turn the drain loop's tail gate catches it, but an IDLE session has no
// tail to run, so the job that just finished waits for the next unrelated wake
// to be noticed at all. The kick is now re-armed and lands the moment the slot
// frees — no timer, no poll, just the send completing.
func TestSubmitNotification_DeliveredOnceTheSlotFrees(t *testing.T) {
	srv := NewServer(ServerConfig{})

	// Occupy the single slot with unrelated input.
	srv.SubmitContinuation("keep the slot busy")
	srv.SubmitNotification()

	occupant := <-srv.InputCh()
	if occupant.Kind != agent.EntryContinuation {
		t.Fatalf("first message Kind = %v, want EntryContinuation (the occupant)", occupant.Kind)
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryNotification {
			t.Fatalf("Kind = %v, want EntryNotification", msg.Kind)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dropped notification kick was never redelivered: an idle session is never told its job finished")
	}
}

// TestSubmitNotification_RepeatedDropsWakeOnce pins that the re-arm coalesces.
// The queue the wake drains is drained whole, so one wake settles any number of
// dropped kicks; re-arming per drop would push a burst of empty notification
// turns onto the session behind the real one.
func TestSubmitNotification_RepeatedDropsWakeOnce(t *testing.T) {
	srv := NewServer(ServerConfig{})

	srv.SubmitContinuation("keep the slot busy")
	for range 5 {
		srv.SubmitNotification()
	}

	if occupant := <-srv.InputCh(); occupant.Kind != agent.EntryContinuation {
		t.Fatalf("first message Kind = %v, want EntryContinuation (the occupant)", occupant.Kind)
	}
	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryNotification {
			t.Fatalf("Kind = %v, want EntryNotification", msg.Kind)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dropped notification kicks were never redelivered")
	}

	// Negative assertion: nothing to poll for, so this waits a fixed moment and
	// asserts the absence of a second wake.
	time.Sleep(250 * time.Millisecond)
	select {
	case extra := <-srv.InputCh():
		t.Fatalf("a second wake arrived (%v): repeated drops must coalesce into one re-arm", extra.Kind)
	default:
	}
}
