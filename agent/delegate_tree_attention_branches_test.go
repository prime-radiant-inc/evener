package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// attentionWakeIDsOf returns delegateID's durable attention queue IDs, nil when
// it owes none. It reads the aggregate without taking c.mu because its callers
// include tests that already hold the controller lock; callers must not race it
// with a mutation of the same controller.
func attentionWakeIDsOf(c *delegateTreeController, delegateID string) map[string]struct{} {
	if c == nil {
		return nil
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil || len(aggregate.PendingAttention) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(aggregate.PendingAttention))
	for _, row := range aggregate.PendingAttention {
		ids[row.AttentionID] = struct{}{}
	}
	return ids
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
// delegatestore.AttentionProjectionEligible
// ---------------------------------------------------------------------------

func TestDelegateAttentionProjectionEligibleNilAggregate(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{"dlg_1": nil}
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestDelegateAttentionProjectionEligibleNotResumable(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: false},
	}
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for non-resumable")
	}
}

func TestDelegateAttentionProjectionEligiblePendingStop(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, PendingStopSeq: 5},
	}
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for pending stop")
	}
}

func TestDelegateAttentionProjectionEligiblePhaseClosed(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseClosed},
	}
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for closed phase")
	}
}

func TestDelegateAttentionProjectionEligiblePhaseStopping(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseStopping},
	}
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for stopping phase")
	}
}

func TestDelegateAttentionProjectionEligibleEligible(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseIdle},
	}
	if !delegatestore.AttentionProjectionEligible(state, "dlg_1") {
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
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
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
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
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
	if !delegatestore.AttentionProjectionEligible(state, "dlg_1") {
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
	if delegatestore.AttentionProjectionEligible(state, "dlg_1") {
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
	if !delegatestore.AttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected true for empty parent (root delegate)")
	}
}

// ---------------------------------------------------------------------------
// noteDelegateAttentionLocked (through noteDelegateAttentionForTest)
// ---------------------------------------------------------------------------

func TestNoteDelegateAttentionLockedEmptyDelegateID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	if c.noteDelegateAttentionForTest("", "att_1") {
		t.Fatalf("expected false for empty delegate ID")
	}
}

func TestNoteDelegateAttentionLockedEmptyAttentionID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{"dlg_1": {}},
	}
	if c.noteDelegateAttentionForTest("dlg_1", "") {
		t.Fatalf("expected false for empty attention ID")
	}
}

func TestNoteDelegateAttentionLockedNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if c.noteDelegateAttentionForTest("dlg_missing", "att_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestNoteDelegateAttentionLockedNew(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	if !c.noteDelegateAttentionForTest("dlg_1", "att_1") {
		t.Fatalf("expected true for new attention")
	}
	if got := c.attentionWakeIDsForTest("dlg_1"); len(got) != 1 {
		t.Fatalf("queue holds %v, want 1 attention ID", got)
	}
	if _, committed := c.attentionCommittedWakeIDsForTest("dlg_1")["att_1"]; !committed {
		t.Fatal("noting new attention did not mark its wake committed")
	}
	c.mu.Lock()
	needsAttention := c.durable["dlg_1"].NeedsAttention
	c.mu.Unlock()
	if !needsAttention {
		t.Fatal("noting new attention did not derive NeedsAttention from its row")
	}
}

func TestNoteDelegateAttentionLockedDuplicate(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	c.seedAttentionRowForTest("dlg_1", "att_1", true)
	if c.noteDelegateAttentionForTest("dlg_1", "att_1") {
		t.Fatalf("expected false for duplicate attention")
	}
}

// A start commit copies the rows its generation carries into the durable queue
// uncommitted. Committing one of them for the first time is new attention to
// the drive: it reports true, so the caller knows the wake published, and the
// row takes the committed mark its arm's wake owns.
func TestNoteDelegateAttentionLockedCommitsACopiedRider(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	c.seedAttentionRowForTest("dlg_1", "att_1", false)
	if !c.noteDelegateAttentionForTest("dlg_1", "att_1") {
		t.Fatalf("expected true for a copied rider's first commit")
	}
	if _, committed := c.attentionCommittedWakeIDsForTest("dlg_1")["att_1"]; !committed {
		t.Fatal("the copied rider was not marked committed")
	}
	if got := c.attentionWakeIDsForTest("dlg_1"); len(got) != 1 {
		t.Fatalf("committing the rider changed the queue to %v, want its one row", got)
	}
}

// ---------------------------------------------------------------------------
// forgetDelegateAttentionLocked
// ---------------------------------------------------------------------------

func TestForgetDelegateAttentionLocked(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	c.seedAttentionRowForTest("dlg_1", "att_1", true)
	c.seedAttentionRowForTest("dlg_1", "att_2", true)
	c.mu.Lock()
	err := c.forgetDelegateAttentionLocked("dlg_1", "att_1")
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("forget att_1: %v", err)
	}
	if _, exists := attentionWakeIDsOf(c, "dlg_1")["att_1"]; exists {
		t.Fatalf("expected att_1 to be resolved")
	}
	if _, committed := c.attentionCommittedWakeIDsForTest("dlg_1")["att_1"]; committed {
		t.Fatalf("forgetting att_1 left its committed row behind")
	}
	if ids := c.attentionWakeIDsForTest("dlg_1"); len(ids) != 1 {
		t.Fatalf("queue = %v, want dlg_1 to still owe att_2", ids)
	}
}

func TestForgetDelegateAttentionLockedLastID(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	c.seedAttentionRowForTest("dlg_1", "att_1", true)
	c.mu.Lock()
	err := c.forgetDelegateAttentionLocked("dlg_1", "att_1")
	rows := len(c.durable["dlg_1"].PendingAttention)
	needsAttention := c.durable["dlg_1"].NeedsAttention
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("forget att_1: %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected an empty queue once the last ID is forgotten, got %d rows", rows)
	}
	if needsAttention {
		t.Fatal("forgetting the last row left NeedsAttention raised")
	}
}

func TestForgetDelegateAttentionLockedNonExistent(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	c.seedAttentionRowForTest("dlg_1", "att_1", true)
	c.mu.Lock()
	before := c.durable["dlg_1"].ProjectionRevision
	err := c.forgetDelegateAttentionLocked("dlg_1", "att_missing")
	after := c.durable["dlg_1"].ProjectionRevision
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("forgetting a missing ID: %v", err)
	}
	if after != before {
		t.Fatalf("forgetting a missing ID appended an event: revision %d -> %d", before, after)
	}
	if _, kept := attentionWakeIDsOf(c, "dlg_1")["att_1"]; !kept {
		t.Fatal("forgetting a missing ID resolved an existing row")
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
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {
				Resumable: true,
				Phase:     delegatestore.PhaseIdle,
				PendingAttention: []delegatestore.PendingAttention{
					{AttentionID: "att_1", Committed: true},
				},
			},
		},
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatalf("expected true for pending attention")
	}
}

func TestHasPendingDelegateAttentionNotResumable(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {
				Resumable: false,
				PendingAttention: []delegatestore.PendingAttention{
					{AttentionID: "att_1", Committed: true},
				},
			},
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

func TestHasPendingDelegateAttentionEmptyQueue(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Resumable: true},
		},
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for an empty queue")
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
	_, _, _, _, err := c.tryOpenDelegateAttention("dlg_1", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionEmptyDelegateID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	_, _, _, _, err := c.tryOpenDelegateAttention("", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionEmptyAttentionID(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	_, _, _, _, err := c.tryOpenDelegateAttention("dlg_1", "")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	_, _, _, _, err := c.tryOpenDelegateAttention("dlg_missing", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable for nil aggregate, got %v", err)
	}
}

func TestTryOpenDelegateAttentionAlreadyExists(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_1", "")
	c.seedAttentionRowForTest("dlg_1", "att_1", true)
	added, blocker, _, emit, err := c.tryOpenDelegateAttention("dlg_1", "att_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added {
		t.Fatalf("expected added=false for a committed row")
	}
	if blocker != nil {
		t.Fatalf("expected nil blocker")
	}
	if emit {
		t.Fatalf("expected emit=false")
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionWillRaiseLocked
// ---------------------------------------------------------------------------

func TestDelegateAttentionWillRaiseLockedNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if c.delegateAttentionWillRaiseLocked("dlg_missing") {
		t.Fatalf("expected false for a nil aggregate")
	}
}

func TestDelegateAttentionWillRaiseLockedNeedsAttention(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: true, Resumable: true, Phase: delegatestore.PhaseIdle},
		},
	}
	if c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected false when NeedsAttention is already raised")
	}
}

func TestDelegateAttentionWillRaiseLockedNotResumable(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: false},
		},
	}
	if c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected false for a non-resumable delegate")
	}
}

func TestDelegateAttentionWillRaiseLockedPendingStop(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: true, PendingStopSeq: 5},
		},
	}
	if c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected false for a pending stop")
	}
}

func TestDelegateAttentionWillRaiseLockedPhaseClosed(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: true, Phase: delegatestore.PhaseClosed},
		},
	}
	if c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected false for a closed phase")
	}
}

func TestDelegateAttentionWillRaiseLockedPhaseStopping(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: false, Resumable: true, Phase: delegatestore.PhaseStopping},
		},
	}
	if c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected false for a stopping phase")
	}
}

func TestDelegateAttentionWillRaiseLockedEligible(t *testing.T) {
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
	if !c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected true for an eligible delegate with an empty queue")
	}
}

// A queued row with the projection still false is exactly the inconsistent
// state the next queue-changing event repairs: the projection is derived from
// the unheld rows, so the raise is still owed.
func TestDelegateAttentionWillRaiseLockedRepairsAnUnraisedQueuedRow(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {
				NeedsAttention: false,
				Resumable:      true,
				Phase:          delegatestore.PhaseIdle,
				PendingAttention: []delegatestore.PendingAttention{
					{AttentionID: "att_1", Committed: true},
				},
			},
		},
	}
	if !c.delegateAttentionWillRaiseLocked("dlg_1") {
		t.Fatalf("expected true: the unraised pending row still owes its projection")
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
