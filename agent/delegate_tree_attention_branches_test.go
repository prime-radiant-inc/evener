package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// attentionWakeIDsOf returns delegateID's owed attention wake IDs from its
// per-delegate attention state, nil when it owes none.
func attentionWakeIDsOf(c *delegateTreeController, delegateID string) map[string]struct{} {
	if state := c.attention[delegateID]; state != nil {
		return state.wakeIDs
	}
	return nil
}

// attentionRestoreFailuresOf returns delegateID's consecutive cold-restore
// failure count, zero when it has none.
func attentionRestoreFailuresOf(c *delegateTreeController, delegateID string) int {
	if state := c.attention[delegateID]; state != nil {
		return state.restoreFailures
	}
	return 0
}

// attentionParkedOf reports whether the drive parked delegateID.
func attentionParkedOf(c *delegateTreeController, delegateID string) bool {
	state := c.attention[delegateID]
	return state != nil && state.parked
}

// ---------------------------------------------------------------------------
// coldDelegateAttentionRef struct
// ---------------------------------------------------------------------------

func TestColdDelegateAttentionRefStruct(t *testing.T) {
	t.Parallel()
	r := coldDelegateAttentionRef{delegateID: "dlg_1", transcriptRef: "local:abc"}
	if r.delegateID != "dlg_1" || r.transcriptRef != "local:abc" {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionProjectionEligible
// ---------------------------------------------------------------------------

func TestDelegateAttentionProjectionEligibleNilAggregate(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{"dlg_1": nil}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestDelegateAttentionProjectionEligibleNotResumable(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: false},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for non-resumable")
	}
}

func TestDelegateAttentionProjectionEligiblePendingStop(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, PendingStopSeq: 5},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for pending stop")
	}
}

func TestDelegateAttentionProjectionEligiblePhaseClosed(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseClosed},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for closed phase")
	}
}

func TestDelegateAttentionProjectionEligiblePhaseStopping(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseStopping},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for stopping phase")
	}
}

func TestDelegateAttentionProjectionEligibleEligible(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseIdle},
	}
	if !delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected true for eligible delegate")
	}
}

func TestDelegateAttentionProjectionEligibleAncestorNotResumable(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{
			Resumable:  true,
			Phase:      delegatestore.PhaseIdle,
			Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_parent"},
		},
		"dlg_parent": &delegatestore.Aggregate{Resumable: false},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for non-resumable ancestor")
	}
}

func TestDelegateAttentionProjectionEligibleAncestorNil(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{
			Resumable:  true,
			Phase:      delegatestore.PhaseIdle,
			Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_missing"},
		},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for nil ancestor")
	}
}

func TestDelegateAttentionProjectionEligibleAncestorEligible(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{
			Resumable:  true,
			Phase:      delegatestore.PhaseIdle,
			Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_parent"},
		},
		"dlg_parent": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseIdle},
	}
	if !delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected true for eligible with resumable ancestor")
	}
}

func TestDelegateAttentionProjectionEligibleCycle(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{
			Resumable:  true,
			Phase:      delegatestore.PhaseIdle,
			Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_2"},
		},
		"dlg_2": &delegatestore.Aggregate{
			Resumable:  true,
			Phase:      delegatestore.PhaseIdle,
			Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_1"},
		},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for cycle")
	}
}

func TestDelegateAttentionProjectionEligibleEmptyParent(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{
			Resumable:  true,
			Phase:      delegatestore.PhaseIdle,
			Descriptor: delegatestore.Descriptor{ParentDelegateID: ""},
		},
	}
	if !delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected true for empty parent (root delegate)")
	}
}

// ---------------------------------------------------------------------------
// noteDelegateAttentionLocked
// ---------------------------------------------------------------------------

func TestNoteDelegateAttentionLockedEmptyDelegateID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	if c.noteDelegateAttentionLocked("", "att_1") {
		t.Fatalf("expected false for empty delegate ID")
	}
}

func TestNoteDelegateAttentionLockedEmptyAttentionID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{"dlg_1": {}},
	}
	if c.noteDelegateAttentionLocked("dlg_1", "") {
		t.Fatalf("expected false for empty attention ID")
	}
}

func TestNoteDelegateAttentionLockedNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if c.noteDelegateAttentionLocked("dlg_missing", "att_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestNoteDelegateAttentionLockedNew(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable:   map[string]*delegatestore.Aggregate{"dlg_1": {}},
		attention: map[string]*delegateAttentionState{},
	}
	if !c.noteDelegateAttentionLocked("dlg_1", "att_1") {
		t.Fatalf("expected true for new attention")
	}
	if len(attentionWakeIDsOf(c, "dlg_1")) != 1 {
		t.Fatalf("expected 1 attention ID")
	}
	if _, committed := c.attention["dlg_1"].committedWakeIDs["att_1"]; !committed {
		t.Fatal("noting new attention did not mark its wake committed")
	}
}

func TestNoteDelegateAttentionLockedDuplicate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{"dlg_1": {}},
		attention: map[string]*delegateAttentionState{
			"dlg_1": {
				wakeIDs:          map[string]struct{}{"att_1": {}},
				committedWakeIDs: map[string]struct{}{"att_1": {}},
			},
		},
	}
	if c.noteDelegateAttentionLocked("dlg_1", "att_1") {
		t.Fatalf("expected false for duplicate attention")
	}
}

// A start commit copies the rows its generation carries into the owed set
// alone. Committing one of them for the first time is new attention to the
// drive: it reports true, so the caller knows the wake published, and it takes
// the committed mark every other owed row it joins has.
func TestNoteDelegateAttentionLockedCommitsACopiedRider(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{"dlg_1": {}},
		attention: map[string]*delegateAttentionState{
			"dlg_1": {wakeIDs: map[string]struct{}{"att_1": {}}},
		},
	}
	if !c.noteDelegateAttentionLocked("dlg_1", "att_1") {
		t.Fatalf("expected true for a copied rider's first commit")
	}
	if _, committed := c.attention["dlg_1"].committedWakeIDs["att_1"]; !committed {
		t.Fatal("the copied rider was not marked committed")
	}
}

// ---------------------------------------------------------------------------
// forgetDelegateAttentionLocked
// ---------------------------------------------------------------------------

func TestForgetDelegateAttentionLocked(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {
				wakeIDs:          map[string]struct{}{"att_1": {}, "att_2": {}},
				committedWakeIDs: map[string]struct{}{"att_1": {}, "att_2": {}},
			},
		},
	}
	c.forgetDelegateAttentionLocked("dlg_1", "att_1")
	if _, exists := attentionWakeIDsOf(c, "dlg_1")["att_1"]; exists {
		t.Fatalf("expected att_1 to be deleted")
	}
	if _, exists := c.attention["dlg_1"].committedWakeIDs["att_1"]; exists {
		t.Fatalf("forgetting att_1 left its committed mark behind")
	}
	if state := c.attention["dlg_1"]; state == nil || len(state.wakeIDs) != 1 {
		t.Fatalf("expected dlg_1 to still owe att_2")
	}
}

func TestForgetDelegateAttentionLockedLastID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {wakeIDs: map[string]struct{}{"att_1": {}}},
		},
	}
	c.forgetDelegateAttentionLocked("dlg_1", "att_1")
	if state := c.attention["dlg_1"]; state != nil {
		t.Fatalf("expected dlg_1's attention state to be dropped when no IDs remain")
	}
}

func TestForgetDelegateAttentionLockedNonExistent(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{},
	}
	c.forgetDelegateAttentionLocked("dlg_missing", "att_1") // should be a no-op
}

// ---------------------------------------------------------------------------
// replaceDelegateAttentionLocked
// ---------------------------------------------------------------------------

func TestReplaceDelegateAttentionLockedEmpty(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {wakeIDs: map[string]struct{}{"att_1": {}}, driveTurn: 3},
		},
	}
	c.replaceDelegateAttentionLocked("dlg_1", nil)
	// A delegate that no longer owes attention leaves the drive's line: its
	// whole attention state, wake IDs and drive turn together.
	if _, exists := c.attention["dlg_1"]; exists {
		t.Fatalf("expected dlg_1's attention state to be dropped for empty list")
	}
}

func TestReplaceDelegateAttentionLockedWithIDs(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"att_1", "att_2"})
	if len(attentionWakeIDsOf(c, "dlg_1")) != 2 {
		t.Fatalf("expected 2 IDs")
	}
	if got := len(c.attention["dlg_1"].committedWakeIDs); got != 0 {
		t.Fatalf("the replacement marked %d newly copied riders committed, want none", got)
	}
}

// A replacement prunes the committed marks its new set no longer carries and
// keeps the ones it still does: the fold's pending set says what is owed, and a
// row it stops owing cannot keep a mark that would make its next arm look like a
// duplicate.
func TestReplaceDelegateAttentionLockedPrunesOnlyTheMarksItDrops(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {
				wakeIDs:          map[string]struct{}{"att_kept": {}, "att_dropped": {}},
				committedWakeIDs: map[string]struct{}{"att_kept": {}, "att_dropped": {}},
			},
		},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"att_kept", "att_new"})
	committed := c.attention["dlg_1"].committedWakeIDs
	if _, kept := committed["att_kept"]; !kept {
		t.Fatal("the replacement dropped a committed wake it still owed")
	}
	if _, dropped := committed["att_dropped"]; dropped {
		t.Fatal("the replacement kept a committed mark for a row it no longer owes")
	}
	if _, copied := committed["att_new"]; copied {
		t.Fatal("the replacement marked a newly copied rider committed: its first arm would be skipped")
	}
}

func TestReplaceDelegateAttentionLockedEmptyStringsFiltered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"att_1", "", "att_2", ""})
	if len(attentionWakeIDsOf(c, "dlg_1")) != 2 {
		t.Fatalf("expected 2 IDs (empty strings filtered)")
	}
}

func TestReplaceDelegateAttentionLockedAllEmptyStrings(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {driveTurn: 3},
		},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"", ""})
	if _, exists := c.attention["dlg_1"]; exists {
		t.Fatalf("expected dlg_1's attention state to be dropped when all IDs are empty")
	}
}

func TestReplaceDelegateAttentionLockedEmptyDelegateID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {wakeIDs: map[string]struct{}{"att_1": {}}},
		},
	}
	c.replaceDelegateAttentionLocked("", []string{"att_1"})
	// empty delegateID should delete from the map key ""
	// but dlg_1 should still exist
	if _, exists := c.attention["dlg_1"]; !exists {
		t.Fatalf("dlg_1 should still exist")
	}
}

// ---------------------------------------------------------------------------
// hasPendingDelegateAttention
// ---------------------------------------------------------------------------

func TestHasPendingDelegateAttentionNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for nil controller")
	}
}

func TestHasPendingDelegateAttentionEmpty(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{},
		durable:   map[string]*delegatestore.Aggregate{},
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for empty")
	}
}

func TestHasPendingDelegateAttentionWithPending(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {wakeIDs: map[string]struct{}{"att_1": {}}},
		},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Resumable: true, PendingStopSeq: 0, Phase: delegatestore.PhaseIdle},
		},
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatalf("expected true for pending attention")
	}
}

func TestHasPendingDelegateAttentionNotResumable(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {wakeIDs: map[string]struct{}{"att_1": {}}},
		},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Resumable: false},
		},
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for non-resumable")
	}
}

func TestHasPendingDelegateAttentionClosedPhase(t *testing.T) {
	t.Parallel()
	// The fold cannot produce {Resumable: true, Phase: PhaseClosed}: every
	// transition into PhaseClosed requires a non-resumable delegate. Build the
	// fixture through the controller's own append/fold path.
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	if !c.noteDelegateAttention("dlg_1", "att_1") {
		t.Fatal("note delegate attention")
	}
	closeDelegateControllerResumability(t, c, "dlg_1", "test-closure")
	c.mu.Lock()
	aggregate := c.durable["dlg_1"]
	c.mu.Unlock()
	if aggregate == nil || aggregate.Resumable || aggregate.Phase != delegatestore.PhaseClosed {
		t.Fatalf("folded closed aggregate = %#v, want closed and not resumable", aggregate)
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for a closed delegate's retained wake")
	}
}

func TestHasPendingDelegateAttentionEmptyIDs(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{
			"dlg_1": {},
		},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Resumable: true},
		},
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for empty IDs")
	}
}

// ---------------------------------------------------------------------------
// hasRunnableDelegateAttention
// ---------------------------------------------------------------------------

func TestHasRunnableDelegateAttentionNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	if c.hasRunnableDelegateAttention() {
		t.Fatalf("expected false for nil controller")
	}
}

func TestHasRunnableDelegateAttentionEmpty(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{},
		durable:   map[string]*delegatestore.Aggregate{},
	}
	if c.hasRunnableDelegateAttention() {
		t.Fatalf("expected false for empty")
	}
}

// ---------------------------------------------------------------------------
// retryDelegateAttentionLater
// ---------------------------------------------------------------------------

func TestRetryDelegateAttentionLaterNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	c.retryDelegateAttentionLater() // should be a no-op
}

func TestRetryDelegateAttentionLaterNilRoot(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	c.retryDelegateAttentionLater() // should be a no-op
}

// ---------------------------------------------------------------------------
// nextIdleDelegateAttention
// ---------------------------------------------------------------------------

func TestNextIdleDelegateAttentionNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	_, _, ok := c.nextIdleDelegateAttention()
	if ok {
		t.Fatalf("expected false for nil controller")
	}
}

func TestNextIdleDelegateAttentionEmpty(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		attention: map[string]*delegateAttentionState{},
		durable:   map[string]*delegatestore.Aggregate{},
	}
	_, _, ok := c.nextIdleDelegateAttention()
	if ok {
		t.Fatalf("expected false for empty")
	}
}

// ---------------------------------------------------------------------------
// permanentlyFencedDelegateAttention
// ---------------------------------------------------------------------------

func TestPermanentlyFencedDelegateAttentionNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	if c.permanentlyFencedDelegateAttention() != nil {
		t.Fatalf("expected nil for nil controller")
	}
}

// ---------------------------------------------------------------------------
// noteDelegateAttention (public, nil-safe)
// ---------------------------------------------------------------------------

func TestNoteDelegateAttentionNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	if c.noteDelegateAttention("dlg_1", "att_1") {
		t.Fatalf("expected false for nil controller")
	}
}

// ---------------------------------------------------------------------------
// delegateFencedAttentionEscalation struct
// ---------------------------------------------------------------------------

func TestDelegateFencedAttentionEscalationStruct(t *testing.T) {
	t.Parallel()
	e := delegateFencedAttentionEscalation{
		delegateID:    "dlg_1",
		transcriptRef: "local:abc",
		attentionIDs:  []string{"att_1", "att_2"},
	}
	if e.delegateID != "dlg_1" || e.transcriptRef != "local:abc" {
		t.Fatalf("struct wrong: %+v", e)
	}
	if len(e.attentionIDs) != 2 {
		t.Fatalf("expected 2 attention IDs")
	}
}

// ---------------------------------------------------------------------------
// reconcileDelegateAttentionFromTranscripts nil controller
// ---------------------------------------------------------------------------

func TestReconcileDelegateAttentionFromTranscriptsNil(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	err := c.reconcileDelegateAttentionFromTranscripts()
	if err == nil || err.Error() != "delegate attention controller is nil" {
		t.Fatalf("expected nil controller error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// tryOpenDelegateAttention nil/empty
// ---------------------------------------------------------------------------

func TestTryOpenDelegateAttentionNilController(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	_, _, _, _, _, err := c.tryOpenDelegateAttention("dlg_1", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionEmptyDelegateID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	_, _, _, _, _, err := c.tryOpenDelegateAttention("", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionEmptyAttentionID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	_, _, _, _, _, err := c.tryOpenDelegateAttention("dlg_1", "")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable:   map[string]*delegatestore.Aggregate{},
		attention: map[string]*delegateAttentionState{},
	}
	_, _, _, _, _, err := c.tryOpenDelegateAttention("dlg_missing", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable for nil aggregate, got %v", err)
	}
}

func TestTryOpenDelegateAttentionAlreadyExists(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: true},
		},
		attention: map[string]*delegateAttentionState{
			"dlg_1": {
				wakeIDs:          map[string]struct{}{"att_1": {}},
				committedWakeIDs: map[string]struct{}{"att_1": {}},
			},
		},
	}
	added, blocker, _, emit, _, err := c.tryOpenDelegateAttention("dlg_1", "att_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added {
		t.Fatalf("expected added=false for duplicate")
	}
	if blocker != nil {
		t.Fatalf("expected nil blocker")
	}
	if emit {
		t.Fatalf("expected emit=false")
	}
}

// A start commit copies the rows the generation carries into the delegate's
// wake set without any of them having been admitted. That copy is the transcript
// fold's word on what the generation owes, not evidence that a wake passed the
// gate: the first arm of such a row must still publish its wake and journal the
// release a settled user-stop quiet owes, exactly as an arm of any other owed
// row does. Only once it has committed is a repeat arm the duplicate it looks
// like.
func TestTryOpenDelegateAttentionCommitsACopiedPendingRider(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_rider", "")
	journalStopAtGeneration(t, c, "dlg_rider", 1)

	const attentionID = "delegate:copied-rider"
	c.mu.Lock()
	c.replaceDelegateAttentionLocked("dlg_rider", []string{attentionID})
	_, copied := attentionWakeIDsOf(c, "dlg_rider")[attentionID]
	_, committed := c.attention["dlg_rider"].committedWakeIDs[attentionID]
	c.mu.Unlock()
	if !copied || committed {
		t.Fatalf("premise broken: rider copied=%t committed=%t, want the copy alone", copied, committed)
	}

	added, blocker, _, _, generation, err := c.tryOpenDelegateAttention("dlg_rider", attentionID)
	if err != nil {
		t.Fatalf("arm the copied rider: %v", err)
	}
	if blocker != nil {
		t.Fatal("the copied rider's first arm was blocked")
	}
	if !added {
		t.Fatal("the copied rider's first arm did not commit it")
	}
	if generation != 1 {
		t.Fatalf("the copied rider's first arm superseded generation %d, want the settled generation 1", generation)
	}
	c.mu.Lock()
	floor := c.durable["dlg_rider"].AttentionHoldReleasedGeneration
	recorded := c.attention["dlg_rider"].releasedGeneration
	_, committed = c.attention["dlg_rider"].committedWakeIDs[attentionID]
	c.mu.Unlock()
	if floor != 1 {
		t.Fatalf("the copied rider's arm journaled no release (floor %d, want 1): a cold restore rebuilds the quiet over the row the wake freed", floor)
	}
	if recorded != 1 {
		t.Fatalf("the copied rider's arm recorded released generation %d, want 1", recorded)
	}
	if !committed {
		t.Fatal("the copied rider's arm did not mark the wake committed")
	}

	added, blocker, _, _, generation, err = c.tryOpenDelegateAttention("dlg_rider", attentionID)
	if err != nil {
		t.Fatalf("repeat the copied rider's arm: %v", err)
	}
	if added || blocker != nil || generation != 0 {
		t.Fatalf("repeat arm = added:%t blocked:%t generation:%d, want an idempotent no-op", added, blocker != nil, generation)
	}
	c.mu.Lock()
	repeat := c.durable["dlg_rider"].AttentionHoldReleasedGeneration
	c.mu.Unlock()
	if repeat != 1 {
		t.Fatalf("repeat arm moved the release floor to %d: a committed wake must not publish again", repeat)
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionOpenEventLocked
// ---------------------------------------------------------------------------

func TestDelegateAttentionOpenEventLockedNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	_, err := c.delegateAttentionOpenEventLocked("dlg_missing")
	if !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestDelegateAttentionOpenEventLockedNeedsAttention(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: true},
		},
	}
	event, err := c.delegateAttentionOpenEventLocked("dlg_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event != nil {
		t.Fatalf("expected nil event when already NeedsAttention")
	}
}

func TestDelegateAttentionOpenEventLockedNotResumable(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: false},
		},
	}
	event, err := c.delegateAttentionOpenEventLocked("dlg_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event != nil {
		t.Fatalf("expected nil event for non-resumable")
	}
}

func TestDelegateAttentionOpenEventLockedPendingStop(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: true, PendingStopSeq: 5},
		},
	}
	event, err := c.delegateAttentionOpenEventLocked("dlg_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event != nil {
		t.Fatalf("expected nil event for pending stop")
	}
}

func TestDelegateAttentionOpenEventLockedPhaseClosed(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: true, Phase: delegatestore.PhaseClosed},
		},
	}
	event, err := c.delegateAttentionOpenEventLocked("dlg_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event != nil {
		t.Fatalf("expected nil event for closed phase")
	}
}

func TestDelegateAttentionOpenEventLockedPhaseStopping(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: true, Phase: delegatestore.PhaseStopping},
		},
	}
	event, err := c.delegateAttentionOpenEventLocked("dlg_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event != nil {
		t.Fatalf("expected nil event for stopping phase")
	}
}

func TestDelegateAttentionOpenEventLockedEligible(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {
				NeedsAttention: false,
				Resumable:      true,
				Phase:          delegatestore.PhaseIdle,
			},
		},
	}
	event, err := c.delegateAttentionOpenEventLocked("dlg_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event == nil {
		t.Fatalf("expected non-nil event for eligible delegate")
	}
	if event.AttentionChanged == nil || !event.AttentionChanged.NeedsAttention {
		t.Fatalf("expected NeedsAttention=true in event")
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionWakeEligibleLocked
// ---------------------------------------------------------------------------

func TestDelegateAttentionWakeEligibleLockedClosing(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		closing: true,
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for closing controller")
	}
}

func TestDelegateAttentionWakeEligibleLockedNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_missing") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestDelegateAttentionWakeEligibleLockedNotIdle(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseRunning, Resumable: true},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for non-idle phase")
	}
}

func TestDelegateAttentionWakeEligibleLockedNotResumable(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: false},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for non-resumable")
	}
}

func TestDelegateAttentionWakeEligibleLockedPendingStop(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: true, PendingStopSeq: 3},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for pending stop")
	}
}

func TestDelegateAttentionWakeEligibleLockedWithBinding(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {binding: &delegateRuntimeBinding{}},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for live with binding")
	}
}

func TestDelegateAttentionWakeEligibleLockedWithRecovery(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: true},
		},
		live: map[string]*delegateLiveState{
			"dlg_1": {recoveryRequired: true},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for recoveryRequired")
	}
}

func TestDelegateAttentionWakeEligibleLockedEligible(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: true},
		},
	}
	if !c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected true for eligible delegate")
	}
}

func TestDelegateAttentionWakeEligibleLockedWithAttentionReservation(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: true},
		},
		reservations: map[uint64]*delegateStartRecord{
			1: {delegateID: "dlg_1", trigger: delegatestore.TriggerAttention},
		},
	}
	if !c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected true for attention reservation")
	}
}

func TestDelegateAttentionWakeEligibleLockedWithNonAttentionReservation(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Phase: delegatestore.PhaseIdle, Resumable: true},
		},
		reservations: map[uint64]*delegateStartRecord{
			1: {delegateID: "dlg_1", trigger: delegatestore.TriggerOwnerInput},
		},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_1") {
		t.Fatalf("expected false for non-attention reservation")
	}
}
