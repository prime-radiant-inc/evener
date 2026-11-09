package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// stopReceiptIntersectsMembersLocked
// ---------------------------------------------------------------------------

func TestStopReceiptIntersectsMembersLockedNil(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	if c.stopReceiptIntersectsMembersLocked(nil, map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected false for nil receipt")
	}
}

func TestStopReceiptIntersectsMembersLockedSourceCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	receipt := &delegateWatchReceipt{sourceDelegateID: "dlg_1"}
	if !c.stopReceiptIntersectsMembersLocked(receipt, map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected true for source covered")
	}
}

func TestStopReceiptIntersectsMembersLockedReceiverCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	receipt := &delegateWatchReceipt{receiverDelegateID: "dlg_2"}
	if !c.stopReceiptIntersectsMembersLocked(receipt, map[string]struct{}{"dlg_2": {}}) {
		t.Fatalf("expected true for receiver covered")
	}
}

func TestStopReceiptIntersectsMembersLockedNeitherCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	receipt := &delegateWatchReceipt{sourceDelegateID: "dlg_1", receiverDelegateID: "dlg_2"}
	if c.stopReceiptIntersectsMembersLocked(receipt, map[string]struct{}{"dlg_3": {}}) {
		t.Fatalf("expected false for neither covered")
	}
}

// ---------------------------------------------------------------------------
// delegateDepthLocked
// ---------------------------------------------------------------------------

func TestDelegateDepthLockedEmpty(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{durable: map[string]*delegatestore.Aggregate{}}
	if c.delegateDepthLocked("dlg_missing") != 0 {
		t.Fatalf("expected 0 for missing delegate")
	}
}

func TestDelegateDepthLockedRoot(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Descriptor: delegatestore.Descriptor{ParentDelegateID: ""}},
		},
	}
	if c.delegateDepthLocked("dlg_1") != 1 {
		t.Fatalf("expected 1 for root delegate")
	}
}

func TestDelegateDepthLockedNested(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Descriptor: delegatestore.Descriptor{ParentDelegateID: ""}},
			"dlg_2": {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_1"}},
			"dlg_3": {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_2"}},
		},
	}
	if c.delegateDepthLocked("dlg_3") != 3 {
		t.Fatalf("expected 3 for depth-3 delegate")
	}
}

func TestDelegateDepthLockedNilAggregate(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": nil,
		},
	}
	if c.delegateDepthLocked("dlg_1") != 0 {
		t.Fatalf("expected 0 for nil aggregate")
	}
}

// ---------------------------------------------------------------------------
// classifyDelegateStopAdmission
// ---------------------------------------------------------------------------

func TestClassifyDelegateStopAdmissionAlreadyIdle(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{CurrentRunOpen: false},
	}
	members := map[string]struct{}{"dlg_1": {}}
	lifecycle, outcome := classifyDelegateStopAdmission(state, "dlg_1", members)
	if lifecycle != delegateLifecycleIdle {
		t.Fatalf("expected idle lifecycle, got %v", lifecycle)
	}
	if outcome != "already_idle" {
		t.Fatalf("expected 'already_idle', got %q", outcome)
	}
}

func TestClassifyDelegateStopAdmissionCancelledByRequest(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{CurrentRunOpen: true},
	}
	members := map[string]struct{}{"dlg_1": {}}
	lifecycle, outcome := classifyDelegateStopAdmission(state, "dlg_1", members)
	if lifecycle != delegateLifecycleRunning {
		t.Fatalf("expected running lifecycle, got %v", lifecycle)
	}
	if outcome != "cancelled_by_request" {
		t.Fatalf("expected 'cancelled_by_request', got %q", outcome)
	}
}

func TestClassifyDelegateStopAdmissionTargetRunningMemberIdle(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{
		"dlg_1": &delegatestore.Aggregate{CurrentRunOpen: true},
		"dlg_2": &delegatestore.Aggregate{CurrentRunOpen: false},
	}
	members := map[string]struct{}{"dlg_1": {}, "dlg_2": {}}
	lifecycle, outcome := classifyDelegateStopAdmission(state, "dlg_1", members)
	if lifecycle != delegateLifecycleRunning {
		t.Fatalf("expected running lifecycle (target is running), got %v", lifecycle)
	}
	if outcome != "cancelled_by_request" {
		t.Fatalf("expected 'cancelled_by_request' (target running), got %q", outcome)
	}
}

func TestClassifyDelegateStopAdmissionNilTarget(t *testing.T) {
	t.Parallel()
	state := delegatestore.State{}
	members := map[string]struct{}{"dlg_1": {}}
	lifecycle, outcome := classifyDelegateStopAdmission(state, "dlg_missing", members)
	if lifecycle != delegateLifecycleIdle {
		t.Fatalf("expected idle for nil target, got %v", lifecycle)
	}
	if outcome != "already_idle" {
		t.Fatalf("expected 'already_idle' for nil target, got %q", outcome)
	}
}

// ---------------------------------------------------------------------------
// stopCoversLocked
// ---------------------------------------------------------------------------

func TestStopCoversLockedNoStop(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	if c.stopCoversLocked("dlg_1") {
		t.Fatalf("expected false with no stop")
	}
}

func TestStopCoversLockedCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		stop: &delegateStopState{members: map[string]struct{}{"dlg_1": {}}},
	}
	if !c.stopCoversLocked("dlg_1") {
		t.Fatalf("expected true for covered delegate")
	}
}

func TestStopCoversLockedNotCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		stop: &delegateStopState{members: map[string]struct{}{"dlg_2": {}}},
	}
	if c.stopCoversLocked("dlg_1") {
		t.Fatalf("expected false for not covered")
	}
}

// ---------------------------------------------------------------------------
// deliveryIntersectsMembersLocked
// ---------------------------------------------------------------------------

func TestDeliveryIntersectsMembersLockedNil(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	if c.deliveryIntersectsMembersLocked(nil, map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected false for nil receipt")
	}
}

func TestDeliveryIntersectsMembersLockedSenderCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	receipt := &delegateDeliveryAdmission{delegateID: "dlg_1"}
	if !c.deliveryIntersectsMembersLocked(receipt, map[string]struct{}{"dlg_1": {}}) {
		t.Fatalf("expected true for sender covered")
	}
}

func TestDeliveryIntersectsMembersLockedOwnerCovered(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	receipt := &delegateDeliveryAdmission{ownerID: "dlg_2"}
	if !c.deliveryIntersectsMembersLocked(receipt, map[string]struct{}{"dlg_2": {}}) {
		t.Fatalf("expected true for owner covered")
	}
}

func TestDeliveryIntersectsMembersLockedNeither(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	receipt := &delegateDeliveryAdmission{delegateID: "dlg_1", ownerID: "dlg_2"}
	if c.deliveryIntersectsMembersLocked(receipt, map[string]struct{}{"dlg_3": {}}) {
		t.Fatalf("expected false for neither covered")
	}
}

// ---------------------------------------------------------------------------
// subtreeMembersLocked
// ---------------------------------------------------------------------------

func TestSubtreeMembersLockedSingle(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	members := c.subtreeMembersLocked("dlg_1")
	if len(members) != 1 || members["dlg_1"] != struct{}{} {
		t.Fatalf("expected just target, got %v", members)
	}
}

func TestSubtreeMembersLockedWithChildren(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Descriptor: delegatestore.Descriptor{ParentDelegateID: ""}},
			"dlg_2": {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_1"}},
			"dlg_3": {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_1"}},
			"dlg_4": {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_2"}},
		},
	}
	// Hand-seeded durable state must derive its children index the way the
	// constructor does for a folded journal.
	c.delegateChildren = deriveDelegateChildrenIndex(c.durable)
	members := c.subtreeMembersLocked("dlg_1")
	if len(members) != 4 {
		t.Fatalf("expected 4 members, got %d: %v", len(members), members)
	}
}

func TestSubtreeMembersLockedNilAggregates(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1":   {Descriptor: delegatestore.Descriptor{ParentDelegateID: ""}},
			"dlg_nil": nil,
		},
	}
	// Hand-seeded durable state must derive its children index the way the
	// constructor does for a folded journal.
	c.delegateChildren = deriveDelegateChildrenIndex(c.durable)
	members := c.subtreeMembersLocked("dlg_1")
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
}

// ---------------------------------------------------------------------------
// signalStopProgressLocked
// ---------------------------------------------------------------------------

func TestSignalStopProgressLockedNoStop(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	c.signalStopProgressLocked() // should be a no-op
}

func TestSignalStopProgressLocked(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		stop: &delegateStopState{progress: make(chan struct{}, 1)},
	}
	c.signalStopProgressLocked()
	select {
	case <-c.stop.progress:
		// good
	default:
		t.Fatalf("expected signal to be sent")
	}
}

func TestSignalStopProgressLockedFull(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		stop: &delegateStopState{progress: make(chan struct{}, 1)},
	}
	c.stop.progress <- struct{}{} // fill the channel
	c.signalStopProgressLocked()  // should not block
}

// ---------------------------------------------------------------------------
// delegateStopDone
// ---------------------------------------------------------------------------

func TestDelegateStopDoneNil(t *testing.T) {
	t.Parallel()
	if !delegateStopDone(nil) {
		t.Fatalf("expected true for nil stop")
	}
}

func TestDelegateStopDoneDone(t *testing.T) {
	t.Parallel()
	stop := &delegateStopState{done: make(chan struct{})}
	close(stop.done)
	if !delegateStopDone(stop) {
		t.Fatalf("expected true for done stop")
	}
}

func TestDelegateStopDoneNotDone(t *testing.T) {
	t.Parallel()
	stop := &delegateStopState{done: make(chan struct{})}
	if delegateStopDone(stop) {
		t.Fatalf("expected false for not-done stop")
	}
}

// ---------------------------------------------------------------------------
// stopResultLocked
// ---------------------------------------------------------------------------

func TestStopResultLocked(t *testing.T) {
	t.Parallel()
	stop := &delegateStopState{
		targetID:          "dlg_1",
		previousLifecycle: delegateLifecycleRunning,
		outcome:           "cancelled_by_request",
		requestSeq:        5,
		done:              make(chan struct{}),
	}
	c := &delegateTreeController{}
	result := c.stopResultLocked(stop)
	if result.id != "dlg_1" {
		t.Fatalf("id = %q", result.id)
	}
	if result.previousLifecycle != delegateLifecycleRunning {
		t.Fatalf("previousLifecycle = %v", result.previousLifecycle)
	}
	if result.lifecycle != delegateLifecycleIdle {
		t.Fatalf("lifecycle = %v", result.lifecycle)
	}
	if result.outcome != "cancelled_by_request" {
		t.Fatalf("outcome = %q", result.outcome)
	}
	if result.requestSeq != 5 {
		t.Fatalf("requestSeq = %d", result.requestSeq)
	}
}

// ---------------------------------------------------------------------------
// memberIDsLeafFirstLocked
// ---------------------------------------------------------------------------

func TestMemberIDsLeafFirstLockedEmpty(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{durable: map[string]*delegatestore.Aggregate{}}
	ids := c.memberIDsLeafFirstLocked(map[string]struct{}{})
	if len(ids) != 0 {
		t.Fatalf("expected 0 ids")
	}
}

func TestMemberIDsLeafFirstLockedSorted(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Descriptor: delegatestore.Descriptor{ParentDelegateID: ""}},
			"dlg_2": {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_1"}},
		},
	}
	ids := c.memberIDsLeafFirstLocked(map[string]struct{}{"dlg_1": {}, "dlg_2": {}})
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d", len(ids))
	}
	// dlg_2 (depth 2) should come before dlg_1 (depth 1)
	if ids[0] != "dlg_2" {
		t.Fatalf("expected dlg_2 first (deeper), got %q", ids[0])
	}
}

// ---------------------------------------------------------------------------
// executeDelegateCancelPlan
// ---------------------------------------------------------------------------

func TestExecuteDelegateCancelPlanEmpty(t *testing.T) {
	t.Parallel()
	executeDelegateCancelPlan(delegateCancelPlan{}) // should be a no-op
}

func TestExecuteDelegateCancelPlanWithCancel(t *testing.T) {
	t.Parallel()
	cancelled := false
	plan := delegateCancelPlan{
		cancel: []context.CancelFunc{func() { cancelled = true }},
	}
	executeDelegateCancelPlan(plan)
	if !cancelled {
		t.Fatalf("expected cancel to be called")
	}
}

func TestExecuteDelegateCancelPlanNilCancel(t *testing.T) {
	t.Parallel()
	plan := delegateCancelPlan{
		cancel: []context.CancelFunc{nil},
	}
	executeDelegateCancelPlan(plan) // should not panic
}

// ---------------------------------------------------------------------------
// waitForDelegateStopProgress
// ---------------------------------------------------------------------------

func TestWaitForDelegateStopProgressDone(t *testing.T) {
	t.Parallel()
	stop := &delegateStopState{done: make(chan struct{}), progress: make(chan struct{}, 1)}
	close(stop.done)
	if err := waitForDelegateStopProgress(context.Background(), stop); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForDelegateStopProgressProgress(t *testing.T) {
	t.Parallel()
	stop := &delegateStopState{done: make(chan struct{}), progress: make(chan struct{}, 1)}
	stop.progress <- struct{}{}
	if err := waitForDelegateStopProgress(context.Background(), stop); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Releasing a runtime claim untracks it from an in-flight stop and wakes the
// stop drain; releasing a claim the stop never tracked leaves the drain parked.
func TestClaimReleaseUntracksFromStopAndSignals(t *testing.T) {
	t.Parallel()
	const token = uint64(7)
	cases := []struct {
		name    string
		seed    func(*delegateTreeController)
		release func(*delegateTreeController, uint64)
		tracked func(*delegateStopState) bool
	}{
		{
			name:    "steering",
			seed:    func(c *delegateTreeController) { c.stop.steeringClaims[token] = struct{}{} },
			release: (*delegateTreeController).releaseSteeringClaimLocked,
			tracked: func(s *delegateStopState) bool { _, ok := s.steeringClaims[token]; return ok },
		},
		{
			name:    "model",
			seed:    func(c *delegateTreeController) { c.stop.modelClaims[token] = struct{}{} },
			release: (*delegateTreeController).releaseModelClaimLocked,
			tracked: func(s *delegateStopState) bool { _, ok := s.modelClaims[token]; return ok },
		},
		{
			name:    "settlement",
			seed:    func(c *delegateTreeController) { c.stop.settlementClaims[token] = struct{}{} },
			release: (*delegateTreeController).releaseSettlementClaimLocked,
			tracked: func(s *delegateStopState) bool { _, ok := s.settlementClaims[token]; return ok },
		},
	}
	newController := func() *delegateTreeController {
		return &delegateTreeController{
			steeringClaims:   map[uint64]*delegateSteeringClaim{},
			modelClaims:      map[uint64]*delegateModelRequestClaim{},
			settlementClaims: map[uint64]*delegateSettlementClaim{},
			stop: &delegateStopState{
				steeringClaims:   map[uint64]struct{}{},
				modelClaims:      map[uint64]struct{}{},
				settlementClaims: map[uint64]struct{}{},
				progress:         make(chan struct{}, 1),
			},
		}
	}
	for _, tc := range cases {
		t.Run(tc.name+"/tracked", func(t *testing.T) {
			t.Parallel()
			c := newController()
			tc.seed(c)
			tc.release(c, token)
			if tc.tracked(c.stop) {
				t.Fatal("claim still tracked by the stop after release")
			}
			select {
			case <-c.stop.progress:
			default:
				t.Fatal("release did not wake the stop drain")
			}
		})
		t.Run(tc.name+"/untracked", func(t *testing.T) {
			t.Parallel()
			c := newController()
			tc.release(c, token)
			select {
			case <-c.stop.progress:
				t.Fatal("release of an untracked claim woke the stop drain")
			default:
			}
		})
		t.Run(tc.name+"/no stop", func(t *testing.T) {
			t.Parallel()
			c := newController()
			c.stop = nil
			tc.release(c, token)
		})
	}
}

func TestUntrackStopLockedEachStopMap(t *testing.T) {
	t.Parallel()
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	work := delegateWorkToken{}
	delivery := delegateDeliveryToken{}
	newController := func() *delegateTreeController {
		return &delegateTreeController{stop: &delegateStopState{
			active:      map[delegateLease]struct{}{lease: {}},
			starts:      map[uint64]struct{}{1: {}},
			work:        map[delegateWorkToken]string{work: "job"},
			deliveries:  map[delegateDeliveryToken]struct{}{delivery: {}},
			quietClaims: map[uint64]struct{}{1: {}},
			progress:    make(chan struct{}, 1),
		}}
	}
	woke := func(c *delegateTreeController) bool {
		select {
		case <-c.stop.progress:
			return true
		default:
			return false
		}
	}
	t.Run("active", func(t *testing.T) {
		c := newController()
		untrackStopLocked(c, func(s *delegateStopState) map[delegateLease]struct{} { return s.active }, lease)
		if len(c.stop.active) != 0 || !woke(c) {
			t.Fatal("active lease not untracked and signalled")
		}
	})
	t.Run("starts", func(t *testing.T) {
		c := newController()
		untrackStopLocked(c, func(s *delegateStopState) map[uint64]struct{} { return s.starts }, 1)
		if len(c.stop.starts) != 0 || !woke(c) {
			t.Fatal("start not untracked and signalled")
		}
	})
	t.Run("work", func(t *testing.T) {
		c := newController()
		untrackStopLocked(c, func(s *delegateStopState) map[delegateWorkToken]string { return s.work }, work)
		if len(c.stop.work) != 0 || !woke(c) {
			t.Fatal("work not untracked and signalled")
		}
	})
	t.Run("deliveries", func(t *testing.T) {
		c := newController()
		untrackStopLocked(c, func(s *delegateStopState) map[delegateDeliveryToken]struct{} { return s.deliveries }, delivery)
		if len(c.stop.deliveries) != 0 || !woke(c) {
			t.Fatal("delivery not untracked and signalled")
		}
	})
	t.Run("quiet claims", func(t *testing.T) {
		c := newController()
		untrackStopLocked(c, func(s *delegateStopState) map[uint64]struct{} { return s.quietClaims }, 1)
		if len(c.stop.quietClaims) != 0 || !woke(c) {
			t.Fatal("quiet claim not untracked and signalled")
		}
	})
	t.Run("untracked key does not signal", func(t *testing.T) {
		c := newController()
		untrackStopLocked(c, func(s *delegateStopState) map[uint64]struct{} { return s.starts }, 99)
		if len(c.stop.starts) != 1 || woke(c) {
			t.Fatal("untracked key changed the stop")
		}
	})
	t.Run("no stop in flight", func(t *testing.T) {
		untrackStopLocked(&delegateTreeController{}, func(s *delegateStopState) map[uint64]struct{} { return s.starts }, 1)
	})
}
