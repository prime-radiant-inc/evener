package agent

import (
	"testing"
)

// TestRuntimeReclamationIntersectsProcessWorkLocked_SteeringClaims covers the
// steeringClaims branch (lines 274-278).
func TestRuntimeReclamationIntersectsProcessWorkLocked_SteeringClaims(t *testing.T) {
	c := &delegateTreeController{
		steeringClaims: map[uint64]*delegateSteeringClaim{
			1: {delegateID: "dlg_1"},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for steeringClaims match")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_SteeringClaimsNil covers the
// nil-claim skip within the steeringClaims loop.
func TestRuntimeReclamationIntersectsProcessWorkLocked_SteeringClaimsNil(t *testing.T) {
	c := &delegateTreeController{
		steeringClaims: map[uint64]*delegateSteeringClaim{
			1: nil,
		},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for nil steeringClaim")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_ModelClaims covers the
// modelClaims branch (lines 279-283).
func TestRuntimeReclamationIntersectsProcessWorkLocked_ModelClaims(t *testing.T) {
	c := &delegateTreeController{
		modelClaims: map[uint64]*delegateModelRequestClaim{
			1: {lease: delegateLease{delegateID: "dlg_1"}},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for modelClaims match")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLocked_ModelClaimsNil(t *testing.T) {
	c := &delegateTreeController{
		modelClaims: map[uint64]*delegateModelRequestClaim{
			1: nil,
		},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for nil modelClaim")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_SettlementClaims covers the
// settlementClaims branch (lines 284-288).
func TestRuntimeReclamationIntersectsProcessWorkLocked_SettlementClaims(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{
			1: {lease: delegateLease{delegateID: "dlg_1"}},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for settlementClaims match")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLocked_SettlementClaimsNil(t *testing.T) {
	c := &delegateTreeController{
		settlementClaims: map[uint64]*delegateSettlementClaim{
			1: nil,
		},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for nil settlementClaim")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_Work covers the work
// branch (lines 289-293).
func TestRuntimeReclamationIntersectsProcessWorkLocked_Work(t *testing.T) {
	c := &delegateTreeController{
		work: map[uint64]*delegateShellWork{
			1: {owner: delegateLease{delegateID: "dlg_1"}},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for work match")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLocked_WorkNil(t *testing.T) {
	c := &delegateTreeController{
		work: map[uint64]*delegateShellWork{
			1: nil,
		},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for nil work")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_Deliveries covers the
// deliveries branch (lines 294-298), testing both delegateID and ownerID.
func TestRuntimeReclamationIntersectsProcessWorkLocked_Deliveries(t *testing.T) {
	t.Run("delegateID match", func(t *testing.T) {
		c := &delegateTreeController{
			deliveries: map[uint64]*delegateDeliveryAdmission{
				1: {delegateID: "dlg_1"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected true for deliveries delegateID match")
		}
	})
	t.Run("ownerID match", func(t *testing.T) {
		c := &delegateTreeController{
			deliveries: map[uint64]*delegateDeliveryAdmission{
				1: {ownerID: "dlg_parent"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_parent": {}}) {
			t.Fatal("expected true for deliveries ownerID match")
		}
	})
	t.Run("nil receipt", func(t *testing.T) {
		c := &delegateTreeController{
			deliveries: map[uint64]*delegateDeliveryAdmission{
				1: nil,
			},
		}
		if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected false for nil delivery receipt")
		}
	})
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_DeliveryClaims covers the
// deliveryClaims branch (lines 299-303), testing both delegateID and ownerID.
func TestRuntimeReclamationIntersectsProcessWorkLocked_DeliveryClaims(t *testing.T) {
	t.Run("delegateID match", func(t *testing.T) {
		c := &delegateTreeController{
			deliveryClaims: map[string]*delegateDeliveryClaim{
				"claim-1": {delegateID: "dlg_1"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected true for deliveryClaims delegateID match")
		}
	})
	t.Run("ownerID match", func(t *testing.T) {
		c := &delegateTreeController{
			deliveryClaims: map[string]*delegateDeliveryClaim{
				"claim-1": {ownerID: "dlg_parent"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_parent": {}}) {
			t.Fatal("expected true for deliveryClaims ownerID match")
		}
	})
	t.Run("nil claim", func(t *testing.T) {
		c := &delegateTreeController{
			deliveryClaims: map[string]*delegateDeliveryClaim{
				"claim-1": nil,
			},
		}
		if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected false for nil delivery claim")
		}
	})
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_QuietClaims covers the
// quietClaims branch (lines 304-308).
func TestRuntimeReclamationIntersectsProcessWorkLocked_QuietClaims(t *testing.T) {
	c := &delegateTreeController{
		quietClaims: map[uint64]*delegateQuietAttentionClaim{
			1: {lease: delegateLease{delegateID: "dlg_1"}},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for quietClaims match")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLocked_QuietClaimsNil(t *testing.T) {
	c := &delegateTreeController{
		quietClaims: map[uint64]*delegateQuietAttentionClaim{
			1: nil,
		},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for nil quietClaim")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_WatchEnqueues covers the
// watchEnqueues branch (lines 309-313), testing both source and receiver.
func TestRuntimeReclamationIntersectsProcessWorkLocked_WatchEnqueues(t *testing.T) {
	t.Run("source match", func(t *testing.T) {
		c := &delegateTreeController{
			watchEnqueues: map[uint64]*delegateWatchReceipt{
				1: {sourceDelegateID: "dlg_1"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected true for watchEnqueues source match")
		}
	})
	t.Run("receiver match", func(t *testing.T) {
		c := &delegateTreeController{
			watchEnqueues: map[uint64]*delegateWatchReceipt{
				1: {receiverDelegateID: "dlg_1"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected true for watchEnqueues receiver match")
		}
	})
	t.Run("nil receipt", func(t *testing.T) {
		c := &delegateTreeController{
			watchEnqueues: map[uint64]*delegateWatchReceipt{
				1: nil,
			},
		}
		if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected false for nil watch enqueue receipt")
		}
	})
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_WatchDeliveries covers the
// watchDeliveries branch (lines 314-318).
func TestRuntimeReclamationIntersectsProcessWorkLocked_WatchDeliveries(t *testing.T) {
	t.Run("source match", func(t *testing.T) {
		c := &delegateTreeController{
			watchDeliveries: map[uint64]*delegateWatchReceipt{
				1: {sourceDelegateID: "dlg_1"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected true for watchDeliveries source match")
		}
	})
	t.Run("receiver match", func(t *testing.T) {
		c := &delegateTreeController{
			watchDeliveries: map[uint64]*delegateWatchReceipt{
				1: {receiverDelegateID: "dlg_1"},
			},
		}
		if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected true for watchDeliveries receiver match")
		}
	})
	t.Run("nil receipt", func(t *testing.T) {
		c := &delegateTreeController{
			watchDeliveries: map[uint64]*delegateWatchReceipt{
				1: nil,
			},
		}
		if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
			t.Fatal("expected false for nil watch delivery receipt")
		}
	})
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_ReconcileOrder covers the
// reconcileOrder branch (lines 319-323).
func TestRuntimeReclamationIntersectsProcessWorkLocked_ReconcileOrder(t *testing.T) {
	c := &delegateTreeController{
		reconcileOrder: []delegateLease{
			{delegateID: "dlg_other"},
			{delegateID: "dlg_1"},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for reconcileOrder match")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_Reservations covers the
// reservations branch (lines 264-268).
func TestRuntimeReclamationIntersectsProcessWorkLocked_Reservations(t *testing.T) {
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{
			1: {delegateID: "dlg_1"},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for reservations match")
	}
}

func TestRuntimeReclamationIntersectsProcessWorkLocked_ReservationsNil(t *testing.T) {
	c := &delegateTreeController{
		reservations: map[uint64]*delegateStartRecord{
			1: nil,
		},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for nil reservation")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_InputClaims covers the
// inputClaims branch (lines 269-273).
func TestRuntimeReclamationIntersectsProcessWorkLocked_InputClaims(t *testing.T) {
	c := &delegateTreeController{
		inputClaims: map[uint64]delegateLease{
			1: {delegateID: "dlg_1"},
		},
	}
	if !c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected true for inputClaims match")
	}
}

// TestRuntimeReclamationIntersectsProcessWorkLocked_StopNoMatch covers the
// stop branch with non-matching members.
func TestRuntimeReclamationIntersectsProcessWorkLocked_StopNoMatch(t *testing.T) {
	c := &delegateTreeController{
		stop: &delegateStopState{members: map[string]struct{}{"dlg_other": {}}},
	}
	if c.runtimeReclamationIntersectsProcessWorkLocked(map[string]struct{}{"dlg_1": {}}) {
		t.Fatal("expected false for non-matching stop")
	}
}
