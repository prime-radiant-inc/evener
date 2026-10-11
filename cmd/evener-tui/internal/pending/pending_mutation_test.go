package pending

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestCoordinator() (*PendingCoordinator, chan tea.Msg) {
	messages := make(chan tea.Msg, 8)
	coord := NewPendingCoordinator(&fuzzClock{}, nil)
	coord.SetSend(func(msg tea.Msg) { messages <- msg })
	return coord, messages
}

func TestTryReconcileByMutationIDConfirmsByIDNotText(t *testing.T) {
	coord, messages := newTestCoordinator()
	coord.Register("turn/steer", "steered text the daemon may rewrite", "local:t1", "mut-1")
	if _, ok := receivePending(t, messages).(PendingRegisteredMsg); !ok {
		t.Fatal("expected registration message")
	}

	// The daemon echoes a DIFFERENT text (e.g. an image placeholder
	// substitution) but the same mutation id: matching by id must still work.
	if !coord.TryReconcileByMutationID("turn/steer", "mut-1", "local:t1") {
		t.Fatal("expected mutation id match")
	}
	msg, ok := receivePending(t, messages).(PendingConfirmedMsg)
	if !ok {
		t.Fatalf("expected confirmation, got %T", msg)
	}
	if msg.Entry.ClientMutationID != "mut-1" {
		t.Fatalf("confirmed entry mutation id = %q", msg.Entry.ClientMutationID)
	}
}

func TestTryReconcileByMutationIDReturnsFalseWithoutID(t *testing.T) {
	coord, _ := newTestCoordinator()
	coord.Register("turn/steer", "x", "local:t1", "mut-1")
	if coord.TryReconcileByMutationID("turn/steer", "", "local:t1") {
		t.Fatal("an empty mutation id must never match")
	}
}

func TestTryReconcileByMutationIDDoesNotMatchWrongID(t *testing.T) {
	coord, _ := newTestCoordinator()
	coord.Register("turn/steer", "x", "local:t1", "mut-1")
	if coord.TryReconcileByMutationID("turn/steer", "mut-2", "local:t1") {
		t.Fatal("a different mutation id must not match")
	}
}
