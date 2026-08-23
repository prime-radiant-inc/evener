package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// ---------------------------------------------------------------------------
// errDelegateDeliveryReceiverUnavailable
// ---------------------------------------------------------------------------

func TestErrDelegateDeliveryReceiverUnavailable(t *testing.T) {
	if errDelegateDeliveryReceiverUnavailable == nil {
		t.Fatalf("expected non-nil error")
	}
	if errDelegateDeliveryReceiverUnavailable.Error() != "delegate delivery receiver is unavailable" {
		t.Fatalf("error = %q", errDelegateDeliveryReceiverUnavailable.Error())
	}
}

// ---------------------------------------------------------------------------
// delegateAttentionID
// ---------------------------------------------------------------------------

func TestDelegateAttentionID(t *testing.T) {
	if id := delegateAttentionID("del_123"); id != "delegate:del_123" {
		t.Fatalf("attentionID = %q, want 'delegate:del_123'", id)
	}
	if id := delegateAttentionID(""); id != "delegate:" {
		t.Fatalf("attentionID = %q, want 'delegate:'", id)
	}
}

// ---------------------------------------------------------------------------
// delegateWaiterToken struct
// ---------------------------------------------------------------------------

func TestDelegateWaiterTokenStruct(t *testing.T) {
	token := delegateWaiterToken{id: 42}
	if token.id != 42 {
		t.Fatalf("id = %d", token.id)
	}
}

// ---------------------------------------------------------------------------
// delegateInlineWaiter struct
// ---------------------------------------------------------------------------

func TestDelegateInlineWaiterStruct(t *testing.T) {
	w := &delegateInlineWaiter{
		token:      delegateWaiterToken{id: 1},
		generation: 3,
		resolution: make(chan delegateInlineResolution, 1),
	}
	if w.token.id != 1 || w.generation != 3 {
		t.Fatalf("struct wrong: %+v", w)
	}
}

// ---------------------------------------------------------------------------
// delegateInlineResolution struct
// ---------------------------------------------------------------------------

func TestDelegateInlineResolutionStruct(t *testing.T) {
	r := delegateInlineResolution{fallback: true}
	if !r.fallback {
		t.Fatalf("expected fallback=true")
	}
}

// ---------------------------------------------------------------------------
// delegateToolResultCommit
// ---------------------------------------------------------------------------

func TestDelegateToolResultCommitNil(t *testing.T) {
	var commit *delegateToolResultCommit
	_, err := commit.Complete(true)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for nil commit, got %v", err)
	}
}

func TestDelegateToolResultCommitNilController(t *testing.T) {
	commit := &delegateToolResultCommit{
		controller: nil,
		token:      delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
		deliveryID: "del_1",
	}
	_, err := commit.Complete(true)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for nil controller, got %v", err)
	}
}

func TestDelegateToolResultCommitMismatchedDeliveryID(t *testing.T) {
	commit := &delegateToolResultCommit{
		controller: &delegateTreeController{},
		token:      delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
		deliveryID: "del_different",
	}
	_, err := commit.Complete(true)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for mismatched deliveryID, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// delegateDeliveryToken struct
// ---------------------------------------------------------------------------

func TestDelegateDeliveryTokenStruct(t *testing.T) {
	token := delegateDeliveryToken{processID: 5, deliveryID: "del_1"}
	if token.processID != 5 || token.deliveryID != "del_1" {
		t.Fatalf("struct wrong: %+v", token)
	}
}

// ---------------------------------------------------------------------------
// delegateDeliveryClaimToken struct
// ---------------------------------------------------------------------------

func TestDelegateDeliveryClaimTokenStruct(t *testing.T) {
	token := delegateDeliveryClaimToken{processID: 10, deliveryID: "del_2"}
	if token.processID != 10 || token.deliveryID != "del_2" {
		t.Fatalf("struct wrong: %+v", token)
	}
}

// ---------------------------------------------------------------------------
// committedCallerDeliveryReceiver
// ---------------------------------------------------------------------------

func TestCommittedCallerDeliveryReceiver(t *testing.T) {
	r := committedCallerDeliveryReceiver{}
	ok, err := r.appendDelegateNotificationDurably("att_1", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false from committedCallerDeliveryReceiver")
	}
}

// ---------------------------------------------------------------------------
// coldDelegateDeliveryReceiver.appendDelegateNotificationDurably
// ---------------------------------------------------------------------------

func TestColdDelegateDeliveryReceiverInvalidRef(t *testing.T) {
	r := coldDelegateDeliveryReceiver{
		stateDir:      "/nonexistent",
		transcriptRef: "invalid-ref",
	}
	_, err := r.appendDelegateNotificationDurably("att_1", "content")
	if err == nil {
		t.Fatalf("expected error for invalid ref")
	}
}

// ---------------------------------------------------------------------------
// resolveDelegateInlineClaim
// ---------------------------------------------------------------------------

func TestResolveDelegateInlineClaimNil(t *testing.T) {
	// nil waiter should be a no-op
	resolveDelegateInlineClaim(nil, delegateInlineResolution{fallback: true})
}

func TestResolveDelegateInlineClaimOnce(t *testing.T) {
	waiter := &delegateInlineWaiter{
		resolution: make(chan delegateInlineResolution, 1),
	}
	// First resolve should succeed
	resolveDelegateInlineClaim(waiter, delegateInlineResolution{fallback: true})
	// Wait for resolution — buffered channel, no need for timeout
	r := <-waiter.resolution
	if !r.fallback {
		t.Fatalf("expected fallback=true")
	}
	// Second resolve should be a no-op (sync.Once)
	resolveDelegateInlineClaim(waiter, delegateInlineResolution{fallback: false})
	select {
	case <-waiter.resolution:
		t.Fatalf("expected no second resolution")
	default:
		// Good — no second resolution
	}
}

// ---------------------------------------------------------------------------
// waitForDelegateInline nil waiter
// ---------------------------------------------------------------------------

func TestWaitForDelegateInlineNilWaiter(t *testing.T) {
	c := &delegateTreeController{}
	result := c.waitForDelegateInline(context.Background(), nil)
	if !result.fallback {
		t.Fatalf("expected fallback=true for nil waiter")
	}
}

// ---------------------------------------------------------------------------
// delegateNotificationContent
// ---------------------------------------------------------------------------

func TestDelegateNotificationContent(t *testing.T) {
	plan := delegateDeliveryPlan{
		delegateID: "dlg_1",
		packet: delegatestore.TerminalPacket{
			Kind:    delegatestore.PacketTerminalError,
			Message: json.RawMessage(`"test message"`),
		},
	}
	content, err := delegateNotificationContent(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(content, "<delegate-notification") {
		t.Fatalf("expected delegate-notification tag: %q", content)
	}
	if !contains(content, "dlg_1") {
		t.Fatalf("expected delegate ID in content: %q", content)
	}
	if !contains(content, "test message") {
		t.Fatalf("expected message in content: %q", content)
	}
}

func TestDelegateNotificationContentHTMLEscape(t *testing.T) {
	plan := delegateDeliveryPlan{
		delegateID: `dlg_<script>alert("xss")</script>`,
		packet: delegatestore.TerminalPacket{
			Kind:    delegatestore.PacketTerminalError,
			Message: json.RawMessage(`"msg"`),
		},
	}
	content, err := delegateNotificationContent(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The delegate ID should be HTML-escaped
	if contains(content, "<script>") {
		t.Fatalf("expected HTML escaping in content: %q", content)
	}
	if !contains(content, "&lt;script&gt;") {
		t.Fatalf("expected escaped HTML in content: %q", content)
	}
}

func TestDelegateNotificationContentMarshalError(t *testing.T) {
	plan := delegateDeliveryPlan{
		delegateID: "dlg_1",
		packet: delegatestore.TerminalPacket{
			Message: json.RawMessage("invalid\x00json"),
		},
	}
	_, err := delegateNotificationContent(plan)
	if err == nil {
		t.Fatalf("expected marshal error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// hasPendingDelegateDeliveries nil session
// ---------------------------------------------------------------------------

func TestHasPendingDelegateDeliveriesNil(t *testing.T) {
	var s *Session
	if s.hasPendingDelegateDeliveries() {
		t.Fatalf("expected false for nil session")
	}
}

// ---------------------------------------------------------------------------
// notifyStableDelegateAttention nil controller
// ---------------------------------------------------------------------------

func TestNotifyStableDelegateAttentionNil(t *testing.T) {
	var c *delegateTreeController
	c.notifyStableDelegateAttention() // should be a no-op
}

// ---------------------------------------------------------------------------
// hasDeliveryWorkForOwnerLocked
// ---------------------------------------------------------------------------

func TestHasDeliveryWorkForOwnerLockedEmpty(t *testing.T) {
	c := &delegateTreeController{
		deliveryClaims: map[string]*delegateDeliveryClaim{},
		deliveries:     map[uint64]*delegateDeliveryAdmission{},
	}
	if c.hasDeliveryWorkForOwnerLocked("dlg_1") {
		t.Fatalf("expected false with empty claims and deliveries")
	}
}

func TestHasDeliveryWorkForOwnerLockedEmptyOwner(t *testing.T) {
	c := &delegateTreeController{}
	if c.hasDeliveryWorkForOwnerLocked("") {
		t.Fatalf("expected false for empty owner ID")
	}
}

func TestHasDeliveryWorkForOwnerLockedWithClaim(t *testing.T) {
	c := &delegateTreeController{
		deliveryClaims: map[string]*delegateDeliveryClaim{
			"del_1": {ownerID: "dlg_owner"},
		},
	}
	if !c.hasDeliveryWorkForOwnerLocked("dlg_owner") {
		t.Fatalf("expected true for matching claim owner")
	}
}

func TestHasDeliveryWorkForOwnerLockedWithDelivery(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{
			1: {ownerID: "dlg_owner"},
		},
	}
	if !c.hasDeliveryWorkForOwnerLocked("dlg_owner") {
		t.Fatalf("expected true for matching delivery owner")
	}
}

// ---------------------------------------------------------------------------
// hasColdDeliveryWorkForOwnerLocked
// ---------------------------------------------------------------------------

func TestHasColdDeliveryWorkForOwnerLockedEmpty(t *testing.T) {
	c := &delegateTreeController{
		deliveryClaims: map[string]*delegateDeliveryClaim{},
		deliveries:     map[uint64]*delegateDeliveryAdmission{},
	}
	if c.hasColdDeliveryWorkForOwnerLocked("dlg_1") {
		t.Fatalf("expected false with empty claims and deliveries")
	}
}

func TestHasColdDeliveryWorkForOwnerLockedWithColdClaim(t *testing.T) {
	c := &delegateTreeController{
		deliveryClaims: map[string]*delegateDeliveryClaim{
			"del_1": {ownerID: "dlg_owner", cold: true},
		},
	}
	if !c.hasColdDeliveryWorkForOwnerLocked("dlg_owner") {
		t.Fatalf("expected true for matching cold claim")
	}
}

func TestHasColdDeliveryWorkForOwnerLockedWithNonColdClaim(t *testing.T) {
	c := &delegateTreeController{
		deliveryClaims: map[string]*delegateDeliveryClaim{
			"del_1": {ownerID: "dlg_owner", cold: false},
		},
	}
	if c.hasColdDeliveryWorkForOwnerLocked("dlg_owner") {
		t.Fatalf("expected false for non-cold claim")
	}
}

func TestHasColdDeliveryWorkForOwnerLockedWithColdDelivery(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{
			1: {ownerID: "dlg_owner", cold: true},
		},
	}
	if !c.hasColdDeliveryWorkForOwnerLocked("dlg_owner") {
		t.Fatalf("expected true for matching cold delivery")
	}
}

// ---------------------------------------------------------------------------
// hasDeliveryReceiptLocked / deliveryReceiptLocked
// ---------------------------------------------------------------------------

func TestDeliveryReceiptLockedEmpty(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{},
	}
	if c.deliveryReceiptLocked("del_1") != nil {
		t.Fatalf("expected nil for empty deliveries")
	}
	if c.hasDeliveryReceiptLocked("del_1") {
		t.Fatalf("expected false for empty deliveries")
	}
}

func TestDeliveryReceiptLockedFound(t *testing.T) {
	receipt := &delegateDeliveryAdmission{
		token: delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
	}
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{1: receipt},
	}
	if c.deliveryReceiptLocked("del_1") != receipt {
		t.Fatalf("expected to find receipt")
	}
	if !c.hasDeliveryReceiptLocked("del_1") {
		t.Fatalf("expected true for existing receipt")
	}
}

func TestDeliveryReceiptLockedNotFound(t *testing.T) {
	receipt := &delegateDeliveryAdmission{
		token: delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
	}
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{1: receipt},
	}
	if c.deliveryReceiptLocked("del_other") != nil {
		t.Fatalf("expected nil for non-existent delivery ID")
	}
}

// ---------------------------------------------------------------------------
// retryDeliveryPlanLocked
// ---------------------------------------------------------------------------

func TestRetryDeliveryPlanLockedNilReceipt(t *testing.T) {
	c := &delegateTreeController{}
	if c.retryDeliveryPlanLocked(nil) != nil {
		t.Fatalf("expected nil for nil receipt")
	}
}

func TestRetryDeliveryPlanLockedNonRetryable(t *testing.T) {
	c := &delegateTreeController{}
	receipt := &delegateDeliveryAdmission{retryable: false}
	if c.retryDeliveryPlanLocked(receipt) != nil {
		t.Fatalf("expected nil for non-retryable receipt")
	}
}

func TestRetryDeliveryPlanLockedNilAggregate(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{},
	}
	receipt := &delegateDeliveryAdmission{
		retryable:  true,
		delegateID: "dlg_1",
	}
	if c.retryDeliveryPlanLocked(receipt) != nil {
		t.Fatalf("expected nil for nil aggregate")
	}
}

func TestRetryDeliveryPlanLockedEmptyPendingDeliveries(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"dlg_1": {PendingDeliveries: nil},
		},
	}
	receipt := &delegateDeliveryAdmission{
		retryable:  true,
		delegateID: "dlg_1",
	}
	if c.retryDeliveryPlanLocked(receipt) != nil {
		t.Fatalf("expected nil for empty pending deliveries")
	}
}

// ---------------------------------------------------------------------------
// claimDelegateWaiterLocked
// ---------------------------------------------------------------------------

func TestClaimDelegateWaiterLockedNilLive(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{},
	}
	if c.claimDelegateWaiterLocked("dlg_1", 1) != nil {
		t.Fatalf("expected nil for no live state")
	}
}

func TestClaimDelegateWaiterLockedNoWaiters(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {waiters: nil},
		},
	}
	if c.claimDelegateWaiterLocked("dlg_1", 1) != nil {
		t.Fatalf("expected nil for nil waiters")
	}
}

func TestClaimDelegateWaiterLockedFound(t *testing.T) {
	waiter := &delegateInlineWaiter{generation: 1}
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {waiters: map[uint64]*delegateInlineWaiter{1: waiter}},
		},
	}
	result := c.claimDelegateWaiterLocked("dlg_1", 1)
	if result != waiter {
		t.Fatalf("expected to find waiter")
	}
	// Waiter should be deleted from the map
	if c.live["dlg_1"].waiters[1] != nil {
		t.Fatalf("expected waiter to be deleted from map")
	}
}

func TestClaimDelegateWaiterLockedNotFound(t *testing.T) {
	c := &delegateTreeController{
		live: map[string]*delegateLiveState{
			"dlg_1": {waiters: map[uint64]*delegateInlineWaiter{2: {}}},
		},
	}
	if c.claimDelegateWaiterLocked("dlg_1", 1) != nil {
		t.Fatalf("expected nil for non-matching generation")
	}
}

// ---------------------------------------------------------------------------
// deliverDelegatePacket nil controller
// ---------------------------------------------------------------------------

func TestDeliverDelegatePacketNilController(t *testing.T) {
	plan := delegateDeliveryPlan{controller: nil}
	_, err := deliverDelegatePacket(plan, nil)
	if !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("expected errDelegateStaleLease for nil controller, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// executeDelegateMutationPlans nil session
// ---------------------------------------------------------------------------

func TestExecuteDelegateMutationPlansNilSession(t *testing.T) {
	var s *Session
	if err := s.executeDelegateMutationPlans(delegateMutationPlans{}); err == nil {
		t.Fatalf("expected error for nil session")
	}
	if !errors.Is(s.executeDelegateMutationPlans(delegateMutationPlans{}), errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("expected errDelegateDeliveryReceiverUnavailable")
	}
}

func TestExecuteDelegateMutationPlansNilController(t *testing.T) {
	s := &Session{}
	if err := s.executeDelegateMutationPlans(delegateMutationPlans{}); !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("expected errDelegateDeliveryReceiverUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// requeueReplayableDelegateDeliveries nil session
// ---------------------------------------------------------------------------

func TestRequeueReplayableDelegateDeliveriesNil(t *testing.T) {
	var s *Session
	s.requeueReplayableDelegateDeliveries(nil) // should be a no-op
}

func TestRequeueReplayableDelegateDeliveriesNilController(t *testing.T) {
	s := &Session{}
	s.requeueReplayableDelegateDeliveries(nil) // should be a no-op
}

// ---------------------------------------------------------------------------
// flushPendingDelegateDeliveries nil session
// ---------------------------------------------------------------------------

func TestFlushPendingDelegateDeliveriesNil(t *testing.T) {
	var s *Session
	if err := s.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("expected nil error for nil session, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// acceptDelegateDeliveryPlan nil session
// ---------------------------------------------------------------------------

func TestAcceptDelegateDeliveryPlanNil(t *testing.T) {
	var s *Session
	_, _, err := s.acceptDelegateDeliveryPlan(delegateDeliveryPlan{})
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("expected errDelegateDeliveryReceiverUnavailable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// deliveryReceiptIsInline
// ---------------------------------------------------------------------------

func TestDeliveryReceiptIsInlineEmpty(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{},
	}
	if c.deliveryReceiptIsInline(delegateDeliveryToken{processID: 1, deliveryID: "del_1"}) {
		t.Fatalf("expected false for empty deliveries")
	}
}

func TestDeliveryReceiptIsInlineFound(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{
			1: {
				token:  delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
				inline: true,
			},
		},
	}
	if !c.deliveryReceiptIsInline(delegateDeliveryToken{processID: 1, deliveryID: "del_1"}) {
		t.Fatalf("expected true for inline receipt")
	}
}

func TestDeliveryReceiptIsInlineNotInline(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{
			1: {
				token:  delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
				inline: false,
			},
		},
	}
	if c.deliveryReceiptIsInline(delegateDeliveryToken{processID: 1, deliveryID: "del_1"}) {
		t.Fatalf("expected false for non-inline receipt")
	}
}

func TestDeliveryReceiptIsInlineWrongToken(t *testing.T) {
	c := &delegateTreeController{
		deliveries: map[uint64]*delegateDeliveryAdmission{
			1: {
				token:  delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
				inline: true,
			},
		},
	}
	if c.deliveryReceiptIsInline(delegateDeliveryToken{processID: 2, deliveryID: "del_1"}) {
		t.Fatalf("expected false for wrong processID")
	}
}

// ---------------------------------------------------------------------------
// struct type verification
// ---------------------------------------------------------------------------

func TestDelegateDeliveryAdmissionStruct(t *testing.T) {
	a := delegateDeliveryAdmission{
		token:      delegateDeliveryToken{processID: 1, deliveryID: "del_1"},
		delegateID: "dlg_1",
		ownerID:    "dlg_owner",
		cold:       true,
		inline:     false,
		retryable:  true,
	}
	if a.delegateID != "dlg_1" || !a.cold || !a.retryable {
		t.Fatalf("struct wrong: %+v", a)
	}
}

func TestDelegateDeliveryClaimStruct(t *testing.T) {
	c := delegateDeliveryClaim{
		token:      delegateDeliveryClaimToken{processID: 1, deliveryID: "del_1"},
		delegateID: "dlg_1",
		ownerID:    "dlg_owner",
		cold:       false,
	}
	if c.delegateID != "dlg_1" || c.cold {
		t.Fatalf("struct wrong: %+v", c)
	}
}

func TestDelegateDeliveryPlanStruct(t *testing.T) {
	p := delegateDeliveryPlan{
		delegateID:      "dlg_1",
		deliveryID:      "del_1",
		ownerDelegateID: "dlg_owner",
		packet:          delegatestore.TerminalPacket{Kind: delegatestore.PacketReported},
		callerCommitted: true,
	}
	if p.delegateID != "dlg_1" || !p.callerCommitted {
		t.Fatalf("struct wrong: %+v", p)
	}
}

// ---------------------------------------------------------------------------
// helper: contains
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsByte(s, substr))
}

func containsByte(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// fmt usage
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
var _ = sync.Once{}
