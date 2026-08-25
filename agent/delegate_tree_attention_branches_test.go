package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// coldDelegateAttentionRef struct
// ---------------------------------------------------------------------------

func TestColdDelegateAttentionRefStruct(t *testing.T) {
	r := coldDelegateAttentionRef{delegateID: "dlg_1", transcriptRef: "local:abc"}
	if r.delegateID != "dlg_1" || r.transcriptRef != "local:abc" {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionProjectionEligible
// ---------------------------------------------------------------------------

func TestDelegateAttentionProjectionEligibleNilAggregate(t *testing.T) {
	state := delegatestore.State{"dlg_1": nil}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestDelegateAttentionProjectionEligibleNotResumable(t *testing.T) {
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: false},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for non-resumable")
	}
}

func TestDelegateAttentionProjectionEligiblePendingStop(t *testing.T) {
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, PendingStopSeq: 5},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for pending stop")
	}
}

func TestDelegateAttentionProjectionEligiblePhaseClosed(t *testing.T) {
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseClosed},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for closed phase")
	}
}

func TestDelegateAttentionProjectionEligiblePhaseStopping(t *testing.T) {
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseStopping},
	}
	if delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected false for stopping phase")
	}
}

func TestDelegateAttentionProjectionEligibleEligible(t *testing.T) {
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{Resumable: true, Phase: delegatestore.PhaseIdle},
	}
	if !delegateAttentionProjectionEligible(state, "dlg_1") {
		t.Fatalf("expected true for eligible delegate")
	}
}

func TestDelegateAttentionProjectionEligibleAncestorNotResumable(t *testing.T) {
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
	c := &delegateTreeController{}
	if c.noteDelegateAttentionLocked("", "att_1") {
		t.Fatalf("expected false for empty delegate ID")
	}
}

func TestNoteDelegateAttentionLockedEmptyAttentionID(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{"dlg_1": {}},
	}
	if c.noteDelegateAttentionLocked("dlg_1", "") {
		t.Fatalf("expected false for empty attention ID")
	}
}

func TestNoteDelegateAttentionLockedNilAggregate(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if c.noteDelegateAttentionLocked("dlg_missing", "att_1") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestNoteDelegateAttentionLockedNew(t *testing.T) {
	c := &delegateTreeController{
		durable:          map[string]*delegatestore.Aggregate{"dlg_1": {}},
		attentionWakeIDs: map[string]map[string]struct{}{},
	}
	if !c.noteDelegateAttentionLocked("dlg_1", "att_1") {
		t.Fatalf("expected true for new attention")
	}
	if len(c.attentionWakeIDs["dlg_1"]) != 1 {
		t.Fatalf("expected 1 attention ID")
	}
}

func TestNoteDelegateAttentionLockedDuplicate(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{"dlg_1": {}},
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
		},
	}
	if c.noteDelegateAttentionLocked("dlg_1", "att_1") {
		t.Fatalf("expected false for duplicate attention")
	}
}

// ---------------------------------------------------------------------------
// forgetDelegateAttentionLocked
// ---------------------------------------------------------------------------

func TestForgetDelegateAttentionLocked(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}, "att_2": {}},
		},
	}
	c.forgetDelegateAttentionLocked("dlg_1", "att_1")
	if _, exists := c.attentionWakeIDs["dlg_1"]["att_1"]; exists {
		t.Fatalf("expected att_1 to be deleted")
	}
	if _, exists := c.attentionWakeIDs["dlg_1"]; !exists {
		t.Fatalf("expected dlg_1 to still exist (has att_2)")
	}
}

func TestForgetDelegateAttentionLockedLastID(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
		},
	}
	c.forgetDelegateAttentionLocked("dlg_1", "att_1")
	if _, exists := c.attentionWakeIDs["dlg_1"]; exists {
		t.Fatalf("expected dlg_1 to be deleted when no IDs remain")
	}
}

func TestForgetDelegateAttentionLockedNonExistent(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
	}
	c.forgetDelegateAttentionLocked("dlg_missing", "att_1") // should be a no-op
}

// ---------------------------------------------------------------------------
// replaceDelegateAttentionLocked
// ---------------------------------------------------------------------------

func TestReplaceDelegateAttentionLockedEmpty(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
		},
	}
	c.replaceDelegateAttentionLocked("dlg_1", nil)
	if _, exists := c.attentionWakeIDs["dlg_1"]; exists {
		t.Fatalf("expected dlg_1 to be deleted for empty list")
	}
}

func TestReplaceDelegateAttentionLockedWithIDs(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"att_1", "att_2"})
	if len(c.attentionWakeIDs["dlg_1"]) != 2 {
		t.Fatalf("expected 2 IDs")
	}
}

func TestReplaceDelegateAttentionLockedEmptyStringsFiltered(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"att_1", "", "att_2", ""})
	if len(c.attentionWakeIDs["dlg_1"]) != 2 {
		t.Fatalf("expected 2 IDs (empty strings filtered)")
	}
}

func TestReplaceDelegateAttentionLockedAllEmptyStrings(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
	}
	c.replaceDelegateAttentionLocked("dlg_1", []string{"", ""})
	if _, exists := c.attentionWakeIDs["dlg_1"]; exists {
		t.Fatalf("expected dlg_1 to be deleted when all IDs are empty")
	}
}

func TestReplaceDelegateAttentionLockedEmptyDelegateID(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
		},
	}
	c.replaceDelegateAttentionLocked("", []string{"att_1"})
	// empty delegateID should delete from the map key ""
	// but dlg_1 should still exist
	if _, exists := c.attentionWakeIDs["dlg_1"]; !exists {
		t.Fatalf("dlg_1 should still exist")
	}
}

// ---------------------------------------------------------------------------
// hasPendingDelegateAttention
// ---------------------------------------------------------------------------

func TestHasPendingDelegateAttentionNil(t *testing.T) {
	var c *delegateTreeController
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for nil controller")
	}
}

func TestHasPendingDelegateAttentionEmpty(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
		durable:          map[string]*delegatestore.Aggregate{},
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for empty")
	}
}

func TestHasPendingDelegateAttentionWithPending(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
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
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
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
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
		},
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Resumable: true, Phase: delegatestore.PhaseClosed},
		},
	}
	if c.hasPendingDelegateAttention() {
		t.Fatalf("expected false for closed phase")
	}
}

func TestHasPendingDelegateAttentionEmptyIDs(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{
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
	var c *delegateTreeController
	if c.hasRunnableDelegateAttention() {
		t.Fatalf("expected false for nil controller")
	}
}

func TestHasRunnableDelegateAttentionEmpty(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
		durable:          map[string]*delegatestore.Aggregate{},
	}
	if c.hasRunnableDelegateAttention() {
		t.Fatalf("expected false for empty")
	}
}

// ---------------------------------------------------------------------------
// retryDelegateAttentionLater
// ---------------------------------------------------------------------------

func TestRetryDelegateAttentionLaterNil(t *testing.T) {
	var c *delegateTreeController
	c.retryDelegateAttentionLater() // should be a no-op
}

func TestRetryDelegateAttentionLaterNilRoot(t *testing.T) {
	c := &delegateTreeController{}
	c.retryDelegateAttentionLater() // should be a no-op
}

// ---------------------------------------------------------------------------
// nextIdleDelegateAttention
// ---------------------------------------------------------------------------

func TestNextIdleDelegateAttentionNil(t *testing.T) {
	var c *delegateTreeController
	_, _, ok := c.nextIdleDelegateAttention()
	if ok {
		t.Fatalf("expected false for nil controller")
	}
}

func TestNextIdleDelegateAttentionEmpty(t *testing.T) {
	c := &delegateTreeController{
		attentionWakeIDs: map[string]map[string]struct{}{},
		durable:          map[string]*delegatestore.Aggregate{},
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
	var c *delegateTreeController
	if c.permanentlyFencedDelegateAttention() != nil {
		t.Fatalf("expected nil for nil controller")
	}
}

// ---------------------------------------------------------------------------
// noteDelegateAttention (public, nil-safe)
// ---------------------------------------------------------------------------

func TestNoteDelegateAttentionNil(t *testing.T) {
	var c *delegateTreeController
	if c.noteDelegateAttention("dlg_1", "att_1") {
		t.Fatalf("expected false for nil controller")
	}
}

// ---------------------------------------------------------------------------
// delegateFencedAttentionEscalation struct
// ---------------------------------------------------------------------------

func TestDelegateFencedAttentionEscalationStruct(t *testing.T) {
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
	var c *delegateTreeController
	_, _, _, _, err := c.tryOpenDelegateAttention("dlg_1", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionEmptyDelegateID(t *testing.T) {
	c := &delegateTreeController{}
	_, _, _, _, err := c.tryOpenDelegateAttention("", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionEmptyAttentionID(t *testing.T) {
	c := &delegateTreeController{}
	_, _, _, _, err := c.tryOpenDelegateAttention("dlg_1", "")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestTryOpenDelegateAttentionNilAggregate(t *testing.T) {
	c := &delegateTreeController{
		durable:          map[string]*delegatestore.Aggregate{},
		attentionWakeIDs: map[string]map[string]struct{}{},
	}
	_, _, _, _, err := c.tryOpenDelegateAttention("dlg_missing", "att_1")
	if err == nil || !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable for nil aggregate, got %v", err)
	}
}

func TestTryOpenDelegateAttentionAlreadyExists(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {NeedsAttention: true},
		},
		attentionWakeIDs: map[string]map[string]struct{}{
			"dlg_1": {"att_1": {}},
		},
	}
	added, blocker, _, emit, err := c.tryOpenDelegateAttention("dlg_1", "att_1")
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

// ---------------------------------------------------------------------------
// delegateAttentionOpenEventLocked
// ---------------------------------------------------------------------------

func TestDelegateAttentionOpenEventLockedNilAggregate(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	_, err := c.delegateAttentionOpenEventLocked("dlg_missing")
	if !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable, got %v", err)
	}
}

func TestDelegateAttentionOpenEventLockedNeedsAttention(t *testing.T) {
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
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if c.delegateAttentionWakeEligibleLocked("dlg_missing") {
		t.Fatalf("expected false for nil aggregate")
	}
}

func TestDelegateAttentionWakeEligibleLockedNotIdle(t *testing.T) {
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
