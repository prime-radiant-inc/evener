package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// ---------------------------------------------------------------------------
// delegateWatchSourceBinding struct
// ---------------------------------------------------------------------------

func TestDelegateWatchSourceBindingStruct(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	b := delegateWatchSourceBinding{lease: &lease, runtime: nil}
	if b.lease == nil || b.lease.delegateID != "dlg_1" {
		t.Fatalf("struct wrong: %+v", b)
	}
}

// ---------------------------------------------------------------------------
// delegateWatchReceipt struct
// ---------------------------------------------------------------------------

func TestDelegateWatchReceiptStruct(t *testing.T) {
	r := delegateWatchReceipt{
		token:              42,
		sourceDelegateID:   "dlg_src",
		sourceGeneration:   3,
		receiverDelegateID: "dlg_recv",
		deliveryID:         "del_1",
		updateSeq:          10,
	}
	if r.token != 42 || r.sourceDelegateID != "dlg_src" {
		t.Fatalf("struct wrong: %+v", r)
	}
	if r.sourceGeneration != 3 || r.receiverDelegateID != "dlg_recv" {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// validateWatchReceiptLocked
// ---------------------------------------------------------------------------

func TestValidateWatchReceiptLockedClosing(t *testing.T) {
	c := &delegateTreeController{closing: true}
	if err := c.validateWatchReceiptLocked("dlg_1", 1, "dlg_2", false); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("expected errDelegateTargetBusy for closing, got %v", err)
	}
}

func TestValidateWatchReceiptLockedSourceNotFound(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	if err := c.validateWatchReceiptLocked("dlg_missing", 1, "", false); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for missing source, got %v", err)
	}
}

func TestValidateWatchReceiptLockedSourceGenerationMismatch(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 2},
		},
	}
	if err := c.validateWatchReceiptLocked("dlg_1", 1, "", false); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for generation mismatch, got %v", err)
	}
}

func TestValidateWatchReceiptLockedSourceNotRunning(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: false},
		},
	}
	if err := c.validateWatchReceiptLocked("dlg_1", 1, "", false); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for non-running source, got %v", err)
	}
}

func TestValidateWatchReceiptLockedTerminalSource(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: false},
		},
	}
	// terminalSource=true bypasses CurrentRunOpen check
	if err := c.validateWatchReceiptLocked("dlg_1", 1, "", true); err != nil {
		t.Fatalf("expected no error for terminal source, got %v", err)
	}
}

func TestValidateWatchReceiptLockedReceiverNotFound(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
		},
	}
	if err := c.validateWatchReceiptLocked("dlg_1", 1, "dlg_missing", false); !errors.Is(err, errDelegateNotControllable) {
		t.Fatalf("expected errDelegateNotControllable for missing receiver, got %v", err)
	}
}

func TestValidateWatchReceiptLockedValid(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {Generation: 1, CurrentRunOpen: true},
			"dlg_2": {Generation: 1},
		},
	}
	if err := c.validateWatchReceiptLocked("dlg_1", 1, "dlg_2", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateWatchReceiptLockedEmptySourceAndReceiver(t *testing.T) {
	c := &delegateTreeController{}
	if err := c.validateWatchReceiptLocked("", 0, "", false); err != nil {
		t.Fatalf("expected no error for empty source and receiver, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// stopIntersectsWatchLocked
// ---------------------------------------------------------------------------

func TestStopIntersectsWatchLockedNoStop(t *testing.T) {
	c := &delegateTreeController{}
	if c.stopIntersectsWatchLocked("dlg_1", "dlg_2") {
		t.Fatalf("expected false with no stop")
	}
}

func TestStopIntersectsWatchLockedSourceCovered(t *testing.T) {
	c := &delegateTreeController{
		stop: &delegateStopState{
			members: map[string]struct{}{"dlg_1": {}},
		},
	}
	if !c.stopIntersectsWatchLocked("dlg_1", "dlg_2") {
		t.Fatalf("expected true for source covered by stop")
	}
}

func TestStopIntersectsWatchLockedReceiverCovered(t *testing.T) {
	c := &delegateTreeController{
		stop: &delegateStopState{
			members: map[string]struct{}{"dlg_2": {}},
		},
	}
	if !c.stopIntersectsWatchLocked("dlg_1", "dlg_2") {
		t.Fatalf("expected true for receiver covered by stop")
	}
}

func TestStopIntersectsWatchLockedNeitherCovered(t *testing.T) {
	c := &delegateTreeController{
		stop: &delegateStopState{
			members: map[string]struct{}{"dlg_other": {}},
		},
	}
	if c.stopIntersectsWatchLocked("dlg_1", "dlg_2") {
		t.Fatalf("expected false for neither covered")
	}
}

// ---------------------------------------------------------------------------
// CompleteWatchDelivery
// ---------------------------------------------------------------------------

func TestCompleteWatchDeliveryNilReceipt(t *testing.T) {
	c := &delegateTreeController{}
	if err := c.CompleteWatchDelivery(nil); err != nil {
		t.Fatalf("expected no error for nil receipt, got %v", err)
	}
}

func TestCompleteWatchDeliveryWrongController(t *testing.T) {
	c := &delegateTreeController{}
	receipt := &delegateWatchReceipt{controller: &delegateTreeController{}, token: 1}
	if err := c.CompleteWatchDelivery(receipt); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease, got %v", err)
	}
}

func TestCompleteWatchDeliveryNotFound(t *testing.T) {
	c := &delegateTreeController{
		watchDeliveries: map[uint64]*delegateWatchReceipt{},
	}
	receipt := &delegateWatchReceipt{controller: c, token: 1}
	if err := c.CompleteWatchDelivery(receipt); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for not found, got %v", err)
	}
}

func TestCompleteWatchDeliveryFound(t *testing.T) {
	c := &delegateTreeController{
		watchDeliveries: map[uint64]*delegateWatchReceipt{},
	}
	receipt := &delegateWatchReceipt{controller: c, token: 1}
	c.watchDeliveries[1] = receipt
	if err := c.CompleteWatchDelivery(receipt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := c.watchDeliveries[1]; exists {
		t.Fatalf("expected receipt to be deleted")
	}
}

func TestCompleteWatchDeliveryWithStop(t *testing.T) {
	c := &delegateTreeController{
		watchDeliveries: map[uint64]*delegateWatchReceipt{},
		stop: &delegateStopState{
			watchDeliveries: map[uint64]struct{}{1: {}},
		},
	}
	receipt := &delegateWatchReceipt{controller: c, token: 1}
	c.watchDeliveries[1] = receipt
	if err := c.CompleteWatchDelivery(receipt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := c.stop.watchDeliveries[1]; exists {
		t.Fatalf("expected receipt to be deleted from stop.watchDeliveries")
	}
}

// ---------------------------------------------------------------------------
// AbortWatchEnqueue
// ---------------------------------------------------------------------------

func TestAbortWatchEnqueueNil(t *testing.T) {
	c := &delegateTreeController{}
	c.AbortWatchEnqueue(nil) // should be a no-op
}

func TestAbortWatchEnqueueWrongController(t *testing.T) {
	c := &delegateTreeController{
		watchEnqueues: map[uint64]*delegateWatchReceipt{},
	}
	receipt := &delegateWatchReceipt{controller: &delegateTreeController{}, token: 1}
	c.AbortWatchEnqueue(receipt) // should be a no-op (wrong controller)
}

func TestAbortWatchEnqueueNotFound(t *testing.T) {
	c := &delegateTreeController{
		watchEnqueues: map[uint64]*delegateWatchReceipt{},
	}
	receipt := &delegateWatchReceipt{controller: c, token: 1}
	c.AbortWatchEnqueue(receipt) // should be a no-op (not in map)
}

func TestAbortWatchEnqueueFound(t *testing.T) {
	c := &delegateTreeController{
		watchEnqueues: map[uint64]*delegateWatchReceipt{},
	}
	receipt := &delegateWatchReceipt{controller: c, token: 1}
	c.watchEnqueues[1] = receipt
	c.AbortWatchEnqueue(receipt)
	if _, exists := c.watchEnqueues[1]; exists {
		t.Fatalf("expected receipt to be deleted")
	}
}

func TestAbortWatchEnqueueWithStop(t *testing.T) {
	c := &delegateTreeController{
		watchEnqueues: map[uint64]*delegateWatchReceipt{},
		stop: &delegateStopState{
			watchEnqueues: map[uint64]struct{}{1: {}},
		},
	}
	receipt := &delegateWatchReceipt{controller: c, token: 1}
	c.watchEnqueues[1] = receipt
	c.AbortWatchEnqueue(receipt)
	if _, exists := c.stop.watchEnqueues[1]; exists {
		t.Fatalf("expected receipt to be deleted from stop.watchEnqueues")
	}
}

// ---------------------------------------------------------------------------
// watchSourceSessions
// ---------------------------------------------------------------------------

func TestWatchSourceSessionsEmpty(t *testing.T) {
	c := &delegateTreeController{}
	sessions := c.watchSourceSessions()
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestWatchSourceSessionsWithRoot(t *testing.T) {
	c := &delegateTreeController{}
	sessions := c.watchSourceSessions()
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions (rootRuntime is nil), got %d", len(sessions))
	}
}

// ---------------------------------------------------------------------------
// stableWatchDeliveryIdentity struct
// ---------------------------------------------------------------------------

func TestStableWatchDeliveryIdentityStruct(t *testing.T) {
	id := stableWatchDeliveryIdentity{deliveryID: "del_1", updateSeq: 5}
	if id.deliveryID != "del_1" || id.updateSeq != 5 {
		t.Fatalf("struct wrong: %+v", id)
	}
}

// ---------------------------------------------------------------------------
// stableWatchBootstrapSnapshot struct
// ---------------------------------------------------------------------------

func TestStableWatchBootstrapSnapshotStruct(t *testing.T) {
	s := stableWatchBootstrapSnapshot{
		stateDir:   "/state",
		storePaths: []string{"/path1", "/path2"},
		receiverByID: map[string]stableWatchReceiverSnapshot{
			"dlg_1": {sessionID: "sess_1", transcriptRef: "local:sess_1"},
		},
	}
	if s.stateDir != "/state" || len(s.storePaths) != 2 {
		t.Fatalf("struct wrong: %+v", s)
	}
}

func TestStableWatchReceiverSnapshotStruct(t *testing.T) {
	s := stableWatchReceiverSnapshot{
		sessionID:     "sess_1",
		transcriptRef: "local:sess_1",
	}
	if s.sessionID != "sess_1" || s.transcriptRef != "local:sess_1" {
		t.Fatalf("struct wrong: %+v", s)
	}
}

// ---------------------------------------------------------------------------
// exactUnacknowledgedStableWatchSends
// ---------------------------------------------------------------------------

func TestExactUnacknowledgedStableWatchSendsEmpty(t *testing.T) {
	result := exactUnacknowledgedStableWatchSends(nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 results, got %d", len(result))
	}
}

func TestExactUnacknowledgedStableWatchSendsNoStableReceiver(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: false,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 0 {
		t.Fatalf("expected 0 results for non-stable receiver, got %d", len(result))
	}
}

func TestExactUnacknowledgedStableWatchSendsPendingOnly(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0].DeliveryID != "del_1" {
		t.Fatalf("deliveryID = %q", result[0].DeliveryID)
	}
}

func TestExactUnacknowledgedStableWatchSendsDelivered(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
		{
			Kind: jobstore.EventWatchSendDelivered,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 0 {
		t.Fatalf("expected 0 results for delivered, got %d", len(result))
	}
}

func TestExactUnacknowledgedStableWatchSendsDropped(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
		{
			Kind: jobstore.EventWatchSendDropped,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID: "del_1",
				UpdateSeq:  1,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 0 {
		t.Fatalf("expected 0 results for dropped, got %d", len(result))
	}
}

func TestExactUnacknowledgedStableWatchSendsEvicted(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
		{
			Kind: jobstore.EventWatchSendEvicted,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID: "del_1",
				UpdateSeq:  1,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 0 {
		t.Fatalf("expected 0 results for evicted, got %d", len(result))
	}
}

func TestExactUnacknowledgedStableWatchSendsMultiplePending(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_2",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "del_1",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// Results should be sorted by deliveryID
	if result[0].DeliveryID != "del_1" {
		t.Fatalf("expected del_1 first, got %q", result[0].DeliveryID)
	}
}

func TestExactUnacknowledgedStableWatchSendsSkipEmptyDeliveryID(t *testing.T) {
	events := []jobstore.Event{
		{
			Kind: jobstore.EventWatchSendPending,
			WatchSend: &jobstore.WatchSendState{
				DeliveryID:     "",
				UpdateSeq:      1,
				StableReceiver: true,
			},
		},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 0 {
		t.Fatalf("expected 0 results for empty delivery ID, got %d", len(result))
	}
}

func TestExactUnacknowledgedStableWatchSendsNilWatchSend(t *testing.T) {
	events := []jobstore.Event{
		{Kind: jobstore.EventWatchSendPending, WatchSend: nil},
	}
	result := exactUnacknowledgedStableWatchSends(events)
	if len(result) != 0 {
		t.Fatalf("expected 0 results for nil WatchSend, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// repairStableWatchDeliveriesForBootstrap nil controller
// ---------------------------------------------------------------------------

func TestRepairStableWatchDeliveriesForBootstrapNil(t *testing.T) {
	if err := repairStableWatchDeliveriesForBootstrap(nil); err == nil {
		t.Fatalf("expected error for nil controller")
	}
	if err := repairStableWatchDeliveriesForBootstrap(nil); err.Error() != "stable watch bootstrap controller is nil" {
		t.Fatalf("expected nil controller error, got %v", err)
	}
}
