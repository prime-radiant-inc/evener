package goal

import (
	"testing"
	"time"
)

// TestRegisterExpectValidationMirrorsWait pins §6/D-I6 at the store: unknown
// job targets reject with the reason named; until_time predicates reject as
// non-verifiable; empty descs reject.
func TestRegisterExpectValidationMirrorsWait(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := NewStore()
	s.Set("obj", now)
	s.SetSubstrate(&expectTestSubstrate{})
	if _, ok := s.RegisterExpect(ExpectRequest{Desc: "job done", Predicate: WaitKind{Kind: WaitUntilJob, Target: "job_999"}}, now); ok {
		t.Fatal("hallucinated job condition must reject")
	}
	if reason := s.LastRejectReason(); reason == "" {
		t.Fatal("rejection must name the failed check")
	}
	if _, ok := s.RegisterExpect(ExpectRequest{Desc: "timer", Predicate: WaitKind{Kind: WaitUntilTime, Target: ""}}, now); ok {
		t.Fatal("until_time is not a verifiable condition kind")
	}
	if _, ok := s.RegisterExpect(ExpectRequest{Desc: "", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/work/f"}}, now); ok {
		t.Fatal("empty desc must reject")
	}
}

// TestRegisterExpectFileBaselineAndDedupe pins the attach-scan snapshot: a
// present file registers with its baseline; an identical re-register dedupes.
func TestRegisterExpectFileBaselineAndDedupe(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := NewStore()
	s.Set("obj", now)
	s.SetSubstrate(&expectTestSubstrate{files: map[string]string{"/work/f": "base-v1"}})
	c, ok := s.RegisterExpect(ExpectRequest{Desc: "f changed", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/work/f"}}, now)
	if !ok {
		t.Fatalf("present file must register: %q", s.LastRejectReason())
	}
	if c.Baseline != "base-v1" {
		t.Fatalf("baseline = %q, want the attach-scan snapshot base-v1", c.Baseline)
	}
	c2, ok := s.RegisterExpect(ExpectRequest{Desc: "f changed", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/work/f"}}, now)
	if !ok || c2.Desc != c.Desc {
		t.Fatal("identical re-register must dedupe")
	}
	full, _ := s.GoalSnapshot()
	if len(full.Conditions) != 1 {
		t.Fatalf("conditions = %d, want 1 after dedupe", len(full.Conditions))
	}
}

// TestVerifyConditionsNamesFailure pins the check-on-claim verifier: empty
// conditions self-declare; a failing check names its desc; a missing check
// fails closed.
func TestVerifyConditionsNamesFailure(t *testing.T) {
	conds := []Condition{{Desc: "a"}, {Desc: "b"}}
	if ok, failing := VerifyConditions(nil, nil); !ok || failing != "" {
		t.Fatal("condition-free goals self-declare")
	}
	if ok, failing := VerifyConditions(conds, []ConditionCheck{{Desc: "a", Satisfied: true}, {Desc: "b", Satisfied: false}}); ok || failing != "b" {
		t.Fatalf("verdict = (%v,%q), want (false,b)", ok, failing)
	}
	if ok, failing := VerifyConditions(conds, []ConditionCheck{{Desc: "a", Satisfied: true}}); ok || failing != "b" {
		t.Fatalf("missing check must fail closed naming it, got (%v,%q)", ok, failing)
	}
	if ok, failing := VerifyConditions(conds, []ConditionCheck{{Desc: "a", Satisfied: true}, {Desc: "b", Satisfied: true}}); !ok || failing != "" {
		t.Fatal("all-satisfied must verify")
	}
}

// TestEvaluateExpectationsFileFlip pins claim-time file evaluation: changed
// baselines satisfy, unchanged do not.
func TestEvaluateExpectationsFileFlip(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := NewStore()
	s.Set("obj", now)
	sub := &expectTestSubstrate{files: map[string]string{"/work/f": "base-v1"}}
	s.SetSubstrate(sub)
	c, ok := s.RegisterExpect(ExpectRequest{Desc: "f changed", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/work/f"}}, now)
	if !ok {
		t.Fatalf("register: %q", s.LastRejectReason())
	}
	if got := s.EvaluateExpectations([]Condition{c}); len(got) != 1 || got[0].Satisfied {
		t.Fatalf("unchanged file must evaluate unsatisfied: %+v", got)
	}
	sub.files["/work/f"] = "base-v2"
	if got := s.EvaluateExpectations([]Condition{c}); len(got) != 1 || !got[0].Satisfied {
		t.Fatalf("changed file must evaluate satisfied: %+v", got)
	}
}

// TestStallLooksLikeWaitingContract pins the stage-2 routing predicate:
// waiting-shaped classes park, others block.
func TestStallLooksLikeWaitingContract(t *testing.T) {
	for _, class := range []string{"external-unchanged", "approval-pending", "timeout"} {
		s := LedgerSummary{Entries: []LedgerEntry{{Fingerprint: "f", Class: class}}}
		if !StallLooksLikeWaiting(s) {
			t.Fatalf("class %q must route to the auto-park", class)
		}
	}
	for _, class := range []string{"ok", "empty", "error"} {
		s := LedgerSummary{Entries: []LedgerEntry{{Fingerprint: "f", Class: class}}}
		if StallLooksLikeWaiting(s) {
			t.Fatalf("class %q must block, not park", class)
		}
	}
	if StallLooksLikeWaiting(LedgerSummary{}) {
		t.Fatal("empty summary must not route to the auto-park")
	}
}

// TestRecordContinuationAutoParkBound pins the §5 re-park bound at the store:
// nudged + wait-shaped stalls park 3× (AutoReparks 1..3), then block.
func TestRecordContinuationAutoParkBound(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := NewStore()
	s.Set("obj", now)
	// Fresh tier (K=6): five identical stalls accrue, the 6th nudges.
	stall := TurnOutcome{ActionFingerprint: "poll x", ObservationClass: "external-unchanged", ObservationHash: "same", StateDigest: "steady"}
	for range RepetitionThresholdFresh - 1 {
		s.RecordContinuation(stall, false, now)
	}
	snap, active := s.RecordContinuation(stall, false, now)
	if !active {
		t.Fatal("K-th stall must nudge, not block")
	}
	_ = snap
	nudged, _ := s.GoalSnapshot()
	if nudged.LedgerSummary.Stage != StageNudged {
		t.Fatalf("after nudge: stage = %q, want nudged", nudged.LedgerSummary.Stage)
	}
	for i := range MaxConsecutiveAutoReparks {
		full, _ := s.GoalSnapshot()
		if full.LedgerSummary.Stage != StageNudged && full.LedgerSummary.Stage != StageAutoPark {
			t.Fatalf("pre-park %d: stage = %q", i+1, full.LedgerSummary.Stage)
		}
		snap, active = s.RecordContinuation(stall, false, now)
		if !active {
			t.Fatalf("auto-park %d must stay active", i+1)
		}
	}
	full, _ := s.GoalSnapshot()
	if full.AutoReparks != MaxConsecutiveAutoReparks || full.LedgerSummary.Stage != StageAutoPark {
		t.Fatalf("after 3 parks: %+v", full.LedgerSummary)
	}
	snap, active = s.RecordContinuation(stall, false, now)
	if active || snap.Status != StatusBlocked || snap.StopReason != VerdictNoProgress {
		t.Fatalf("4th stall = %+v active=%v, want blocked/no progress", snap, active)
	}
}

// expectTestSubstrate is the store-level deterministic substrate.
type expectTestSubstrate struct {
	files map[string]string
	jobs  map[string]expectJobTarget
	dlgs  map[string]expectDlgTarget
}

type expectJobTarget struct {
	live     bool
	retained bool
	excerpt  string
}

type expectDlgTarget struct {
	live     bool
	retained bool
	excerpt  string
}

func (f *expectTestSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	t, ok := f.jobs[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *expectTestSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	t, ok := f.dlgs[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *expectTestSubstrate) StatFile(path string) (string, bool) {
	b, ok := f.files[path]
	return b, ok
}

func (f *expectTestSubstrate) LookupApproval(contentKey, generation string) bool { return false }

func (f *expectTestSubstrate) LookupChild(id string) bool { return false }

func (f *expectTestSubstrate) CheckURL(rawURL string, timeout time.Duration) bool { return false }

// TestRegisterExpectKindRestriction pins fix-1/4 I1: only file/job/delegate
// predicates register; approval/child/http/external-label reject with the
// reason named (substrate extension is out of scope for v1).
func TestRegisterExpectKindRestriction(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	newStore := func() *Store {
		s := NewStore()
		s.Set("obj", now)
		s.SetSubstrate(&expectTestSubstrate{
			files: map[string]string{"/work/f": "base-v1"},
			jobs:  map[string]expectJobTarget{"job_1": {live: true}},
			dlgs:  map[string]expectDlgTarget{"dlg_1": {live: true}},
		})
		return s
	}
	// Registrable: file, job, delegate.
	for _, req := range []ExpectRequest{
		{Desc: "f", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventFileModified, Target: "/work/f"}},
		{Desc: "j", Predicate: WaitKind{Kind: WaitUntilJob, Target: "job_1"}},
		{Desc: "d", Predicate: WaitKind{Kind: WaitUntilDelegate, Target: "dlg_1"}},
	} {
		s := newStore()
		if _, ok := s.RegisterExpect(req, now); !ok {
			t.Fatalf("kind %q must register: %q", req.Predicate.Kind, s.LastRejectReason())
		}
	}
	// Rejected: approval, child, http, external-label — each names its reason.
	for _, req := range []ExpectRequest{
		{Desc: "a", Predicate: WaitKind{Kind: WaitUntilApproval, Target: "q?"}},
		{Desc: "c", Predicate: WaitKind{Kind: WaitUntilChild, Target: "child_1"}},
		{Desc: "h", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventHTTPMatch, Target: "https://example.com/hook"}},
		{Desc: "e", Predicate: WaitKind{Kind: WaitUntilEvent, EventSubtype: EventExternalLabel, Target: ""}},
	} {
		s := newStore()
		if _, ok := s.RegisterExpect(req, now); ok {
			t.Fatalf("kind %q/%q must reject under the v1 restriction", req.Predicate.Kind, req.Predicate.EventSubtype)
		}
		if reason := s.LastRejectReason(); reason == "" {
			t.Fatalf("kind %q/%q rejection must name its reason", req.Predicate.Kind, req.Predicate.EventSubtype)
		}
	}
}
