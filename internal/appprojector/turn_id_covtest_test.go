package appprojector

import (
	"testing"
	"time"
)

// TestCovReserveStableTurnID covers ReserveStableTurnID
// (appwire_projection.go:1767), which sets the reserved turn id and clears the
// active turn id so durable mutation state is authoritative.
func TestCovReserveStableTurnID(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")

	// Initially no reserved turn id.
	if got := p.ReservedTurnID(); got != "" {
		t.Fatalf("initial ReservedTurnID = %q, want empty", got)
	}

	// ReserveStableTurnID sets the reserved id.
	p.ReserveStableTurnID("turn_m42")
	if got := p.ReservedTurnID(); got != "turn_m42" {
		t.Fatalf("after ReserveStableTurnID, ReservedTurnID = %q, want turn_m42", got)
	}

	// ActiveTurnID falls back to the reserved id when no active id is set.
	if got := p.ActiveTurnID(); got != "turn_m42" {
		t.Fatalf("ActiveTurnID = %q, want turn_m42 (reserved fallback)", got)
	}
}

// TestCovReserveStableTurnIDClearsActive covers the branch where an active
// turn id is already set when ReserveStableTurnID is called: the active id
// must be cleared so durable state wins.
func TestCovReserveStableTurnIDClearsActive(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")

	// Start a turn to set activeTurnID.
	p.ensureTurn(time.Now())
	activeBefore := p.ActiveTurnID()
	if activeBefore == "" {
		t.Fatal("ensureTurn should have set an active turn id")
	}

	// ReserveStableTurnID must clear the active id.
	p.ReserveStableTurnID("turn_m99")
	if got := p.ActiveTurnID(); got != "turn_m99" {
		t.Fatalf("after ReserveStableTurnID, ActiveTurnID = %q, want turn_m99 (reserved)", got)
	}
	if p.reservedTurnID != "turn_m99" {
		t.Fatalf("reservedTurnID = %q, want turn_m99", p.reservedTurnID)
	}
	if p.activeTurnID != "" {
		t.Fatalf("activeTurnID = %q, want empty (cleared by ReserveStableTurnID)", p.activeTurnID)
	}
}

// TestCovReservedTurnIDEmpty covers ReservedTurnID (appwire_projection.go:1776)
// when no reservation has been made.
func TestCovReservedTurnIDEmpty(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	if got := p.ReservedTurnID(); got != "" {
		t.Fatalf("ReservedTurnID on fresh projector = %q, want empty", got)
	}
}
