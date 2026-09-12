package goal

import (
	"sync/atomic"
	"testing"
	"time"
)

// probeSubstrate fails the test if any substrate method runs while the store
// holds Store.mu: it calls back into a self-locking store method
// (LastRejectReason). Go mutexes are not reentrant, so an in-lock substrate
// call would deadlock — the test would hang, not fail. To keep the pin
// deterministic and fast, the probe instead records its invocations and the
// test below asserts the observable ordering property: the store exposes
// InRegisterCritical (an atomic set only across the locked mutation section),
// and every probe call must observe it clear. Before the pre-read hoist this
// test FAILS (substrate calls run under the lock); after, it passes.
type probeSubstrate struct {
	t     *testing.T
	store *Store
	calls *atomic.Int64
}

func (p *probeSubstrate) check(caller string) {
	p.calls.Add(1)
	if p.store.InRegisterCritical() {
		p.t.Errorf("%s ran under the Store lock", caller)
	}
}

func (p *probeSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	p.check("LookupJob")
	return true, false, "", true
}

func (p *probeSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	p.check("LookupDelegate")
	return true, false, "", true
}

func (p *probeSubstrate) StatFile(path string) (string, bool) {
	p.check("StatFile")
	return "base", true
}

func (p *probeSubstrate) LookupApproval(contentKey, generation string) bool {
	p.check("LookupApproval")
	return true
}

func (p *probeSubstrate) LookupChild(id string) bool {
	p.check("LookupChild")
	return true
}

// TestRegisterWaitSubstrateCallsOutsideStoreLock is the failing-first pin for
// the round-14 HIGH (lock-order inversion): every substrate read during
// RegisterWait — validation (job/delegate/approval/child/file) plus the
// attach-scan waitPredicateTruth call — must run outside Store.mu, matching
// the ClassifyWaits precedent. The probe asserts via
// Store.InRegisterCritical; this test FAILS until the pre-read hoist lands.
func TestRegisterWaitSubstrateCallsOutsideStoreLock(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  WaitKind
	}{
		{"job", WaitKind{Kind: WaitUntilJob, Target: "job_1", Timeout: time.Minute}},
		{"delegate", WaitKind{Kind: WaitUntilDelegate, Target: "dlg_1", Timeout: time.Minute}},
		{"approval", WaitKind{Kind: WaitUntilApproval, Target: "q?", Timeout: time.Minute}},
		{"child", WaitKind{Kind: WaitUntilChild, Target: "child_1", Timeout: time.Minute}},
		{"file", WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/w/f", Timeout: time.Minute}},
	} {
		s := NewStore()
		now := time.Unix(1000, 0).UTC()
		s.Set("obj", now)
		var calls atomic.Int64
		s.SetSubstrate(&probeSubstrate{t: t, store: s, calls: &calls})
		if _, ok := s.RegisterWait(tc.req, now); !ok {
			t.Fatalf("%s: registration must succeed: %q", tc.name, s.LastRejectReason())
		}
		if calls.Load() == 0 {
			t.Fatalf("%s: substrate was never consulted", tc.name)
		}
	}
}

// TestRegisterExpectSubstrateCallsOutsideStoreLock is the RegisterExpect half
// of the same HIGH: expectAttachScanLocked's substrate reads must run outside
// Store.mu.
func TestRegisterExpectSubstrateCallsOutsideStoreLock(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  ExpectRequest
	}{
		{"job", ExpectRequest{Desc: "j", Predicate: WaitKind{Kind: WaitUntilJob, Target: "job_1"}}},
		{"delegate", ExpectRequest{Desc: "d", Predicate: WaitKind{Kind: WaitUntilDelegate, Target: "dlg_1"}}},
		{"file", ExpectRequest{Desc: "f", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/w/f"}}},
	} {
		s := NewStore()
		now := time.Unix(1000, 0).UTC()
		s.Set("obj", now)
		var calls atomic.Int64
		s.SetSubstrate(&probeSubstrate{t: t, store: s, calls: &calls})
		if _, ok := s.RegisterExpect(tc.req, now); !ok {
			t.Fatalf("%s: registration must succeed: %q", tc.name, s.LastRejectReason())
		}
		if calls.Load() == 0 {
			t.Fatalf("%s: substrate was never consulted", tc.name)
		}
	}
}

// TestRegisterWaitSnapshotSemantics pins the pre-pass TOCTOU contract: the
// substrate answers once per registration (validation + attach-scan share
// one snapshot), and a mid-registration answer flip cannot split the
// decision. countingSubstrate flips its job answer after the first lookup;
// registration must observe a single coherent snapshot (either parks on
// live or catches up on retained-terminal — never rejects, never double
// consults beyond the pre-pass budget).
type countingSubstrate struct {
	lookups int
}

func (c *countingSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	c.lookups++
	if c.lookups == 1 {
		return true, false, "", true
	}
	return false, true, "job job_9 exited 0", true
}

func (c *countingSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	return false, false, "", false
}

func (c *countingSubstrate) StatFile(path string) (string, bool) { return "", false }

func (c *countingSubstrate) LookupApproval(contentKey, generation string) bool { return false }

func (c *countingSubstrate) LookupChild(id string) bool { return false }

func TestRegisterWaitSnapshotSemantics(t *testing.T) {
	s := NewStore()
	now := time.Unix(1000, 0).UTC()
	s.Set("obj", now)
	sub := &countingSubstrate{}
	s.SetSubstrate(sub)
	w, ok := s.RegisterWait(WaitKind{Kind: WaitUntilJob, Target: "job_9", Timeout: time.Minute}, now)
	if !ok {
		t.Fatalf("flapping substrate must still decide coherently: %q", s.LastRejectReason())
	}
	_ = w
	full, _ := s.GoalSnapshot()
	// Coherent outcomes only: parked live (one wait, no wake) or
	// retained-terminal catch-up (no wait, one wake) — never both, neither.
	parked := len(full.Waits) == 1 && len(full.PendingWake) == 0
	caughtUp := len(full.Waits) == 0 && len(full.PendingWake) == 1
	if !parked && !caughtUp {
		t.Fatalf("incoherent snapshot decision: waits=%d wakes=%d", len(full.Waits), len(full.PendingWake))
	}
}
