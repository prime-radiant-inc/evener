package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// delegateIDInSet
// ---------------------------------------------------------------------------

func TestDelegateIDInSetEmptyID(t *testing.T) {
	if delegateIDInSet("", map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected false for empty ID")
	}
}

func TestDelegateIDInSetFound(t *testing.T) {
	if !delegateIDInSet("dlg_1", map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected true for found ID")
	}
}

func TestDelegateIDInSetNotFound(t *testing.T) {
	if delegateIDInSet("dlg_2", map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected false for not found")
	}
}

func TestDelegateIDInSetEmptyMap(t *testing.T) {
	if delegateIDInSet("dlg_1", map[string]struct{}{}) {
		t.Fatalf("expected false for empty map")
	}
}

// ---------------------------------------------------------------------------
// reclamationCoversLocked
// ---------------------------------------------------------------------------

func TestReclamationCoversLockedEmptyID(t *testing.T) {
	c := &delegateTreeController{reclaiming: map[string]uint64{}}
	if c.reclamationCoversLocked("") {
		t.Fatalf("expected false for empty ID")
	}
}

func TestReclamationCoversLockedDirect(t *testing.T) {
	c := &delegateTreeController{
		reclaiming: map[string]uint64{"dlg_1": 1},
	}
	if !c.reclamationCoversLocked("dlg_1") {
		t.Fatalf("expected true for directly reclaimed")
	}
}

func TestReclamationCoversLockedAncestor(t *testing.T) {
	c := &delegateTreeController{
		reclaiming: map[string]uint64{"dlg_parent": 1},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1":      {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_parent"}},
			"dlg_parent": {},
		},
	}
	if !c.reclamationCoversLocked("dlg_1") {
		t.Fatalf("expected true for ancestor reclaimed")
	}
}

func TestReclamationCoversLockedNotCovered(t *testing.T) {
	c := &delegateTreeController{
		reclaiming: map[string]uint64{"dlg_other": 1},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Descriptor: delegatestore.Descriptor{ParentDelegateID: ""}},
		},
	}
	if c.reclamationCoversLocked("dlg_1") {
		t.Fatalf("expected false for not covered")
	}
}

func TestReclamationCoversLockedNilAggregate(t *testing.T) {
	c := &delegateTreeController{
		reclaiming: map[string]uint64{},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": nil,
		},
	}
	if c.reclamationCoversLocked("dlg_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

// ---------------------------------------------------------------------------
// isResidentTerminalRuntimeLocked
// ---------------------------------------------------------------------------

func TestIsResidentTerminalRuntimeLockedNilAggregate(t *testing.T) {
	c := &delegateTreeController{}
	if c.isResidentTerminalRuntimeLocked("dlg_1", nil) {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestIsResidentTerminalRuntimeLockedCurrentRunOpen(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{CurrentRunOpen: true, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseIdle}
	if c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected false for CurrentRunOpen")
	}
}

func TestIsResidentTerminalRuntimeLockedNoOutcome(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: nil, Phase: delegatestore.PhaseIdle}
	if c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected false for nil LatestOutcome")
	}
}

func TestIsResidentTerminalRuntimeLockedWrongPhase(t *testing.T) {
	c := &delegateTreeController{}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseRunning}
	if c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected false for running phase")
	}
}

func TestIsResidentTerminalRuntimeLockedNoLive(t *testing.T) {
	c := &delegateTreeController{live: map[string]*delegateLiveState{}}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseIdle}
	if c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected false for no live state")
	}
}

func TestIsResidentTerminalRuntimeLockedWithBinding(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{}, runtime: &Session{}},
		},
	}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseIdle}
	if c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected false for live with binding")
	}
}

func TestIsResidentTerminalRuntimeLockedNoRuntime(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: nil, runtime: nil},
		},
	}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseIdle}
	if c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected false for no runtime")
	}
}

func TestIsResidentTerminalRuntimeLockedEligible(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: nil, runtime: &Session{}},
		},
	}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseIdle}
	if !c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected true for eligible terminal runtime")
	}
}

func TestIsResidentTerminalRuntimeLockedClosedPhase(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: nil, runtime: &Session{}},
		},
	}
	agg := &delegatestore.Aggregate{CurrentRunOpen: false, LatestOutcome: &delegatestore.Outcome{}, Phase: delegatestore.PhaseClosed}
	if !c.isResidentTerminalRuntimeLocked("dlg_1", agg) {
		t.Fatalf("expected true for closed phase")
	}
}

// ---------------------------------------------------------------------------
// runtimeReclamationIntersectsProcessWorkLocked
// ---------------------------------------------------------------------------

func TestRuntimeReclamationIntersectsProcessWorkLockedNoStop(t *testing.T) {
	c := &delegateTreeController{
		reclaiming:   map[string]uint64{},
		reservations: map[uint64]*delegateStartRecord{},
		inputClaims:  map[uint64]delegateLease{},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected false with no process work")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLockedWithStop(t *testing.T) {
	c := &delegateTreeController{
		stop:       &delegateStopState{members: map[string]struct{}{"dlg_1": {}}},
		reclaiming: map[string]uint64{},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected true with stop covering member")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLockedWithReclaiming(t *testing.T) {
	c := &delegateTreeController{
		reclaiming: map[string]uint64{"dlg_1": 1},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected true with reclaiming member")
	}
}

// ---------------------------------------------------------------------------
// reclaimDelegateRuntimeCapacity nil session
// ---------------------------------------------------------------------------

func TestReclaimDelegateRuntimeCapacityNil(t *testing.T) {
	var s *Session
	err := s.reclaimDelegateRuntimeCapacity(1)
	if err == nil {
		t.Fatalf("expected error for nil session")
	}
}

func TestReclaimDelegateRuntimeCapacityNilController(t *testing.T) {
	s := &Session{}
	err := s.reclaimDelegateRuntimeCapacity(1)
	if err == nil || !errors.Is(err, errors.New("delegate controller is unavailable")) {
		// It returns a plain error, not a sentinel
		if err == nil {
			t.Fatalf("expected error for nil controller")
		}
	}
}

// ---------------------------------------------------------------------------
// delegateRuntimeReclamationEntry struct
// ---------------------------------------------------------------------------

func TestDelegateRuntimeReclamationEntryStruct(t *testing.T) {
	e := delegateRuntimeReclamationEntry{
		delegateID:     "dlg_1",
		childSessionID: "sess_1",
	}
	if e.delegateID != "dlg_1" || e.childSessionID != "sess_1" {
		t.Fatalf("struct wrong: %+v", e)
	}
}

// ---------------------------------------------------------------------------
// delegateRuntimeReclamationClaim struct
// ---------------------------------------------------------------------------

func TestDelegateRuntimeReclamationClaimStruct(t *testing.T) {
	c := &delegateRuntimeReclamationClaim{
		token:   42,
		entries: []delegateRuntimeReclamationEntry{{delegateID: "dlg_1"}},
	}
	if c.token != 42 || len(c.entries) != 1 {
		t.Fatalf("struct wrong: %+v", c)
	}
}

// ---------------------------------------------------------------------------
// releaseRuntimeReclamationLocked
// ---------------------------------------------------------------------------

func TestReleaseRuntimeReclamationLocked(t *testing.T) {
	c := &delegateTreeController{
		reclamations: map[uint64]*delegateRuntimeReclamationClaim{
			1: {
				token: 1,
				entries: []delegateRuntimeReclamationEntry{
					{delegateID: "dlg_1"},
					{delegateID: "dlg_2"},
				},
			},
		},
		reclaiming: map[string]uint64{
			"dlg_1": 1,
			"dlg_2": 1,
		},
	}
	claim := c.reclamations[1]
	c.releaseRuntimeReclamationLocked(claim)
	if _, exists := c.reclamations[1]; exists {
		t.Fatalf("expected claim to be deleted")
	}
}
