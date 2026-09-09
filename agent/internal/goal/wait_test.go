package goal_test

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/goal"
)

func TestWaitStatusInvariant(t *testing.T) {
	s := goal.NewStore()
	s.Set("ship it", time.Now())
	if _, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, time.Now()); !ok {
		t.Fatal("register should succeed")
	}
	snap, _ := s.Snapshot()
	if snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting", snap.Status)
	}
}

// fakeSubstrate is a deterministic in-memory predicate substrate for
// registration-validation tests (spec §2). Absent map entries model
// hallucinated targets (no record at all); present-but-terminal job/delegate
// entries model the retained-terminal catch-up route.
type fakeTarget struct {
	live     bool
	retained bool
	excerpt  string
}

type fakeSubstrate struct {
	jobs      map[string]fakeTarget
	delegates map[string]fakeTarget
	files     map[string]string
	approvals map[string]bool
	children  map[string]bool
}

func (f *fakeSubstrate) LookupJob(id string) (bool, bool, string, bool) {
	t, ok := f.jobs[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *fakeSubstrate) LookupDelegate(id string) (bool, bool, string, bool) {
	t, ok := f.delegates[id]
	if !ok {
		return false, false, "", false
	}
	return t.live, t.retained, t.excerpt, true
}

func (f *fakeSubstrate) StatFile(path string) (string, bool) {
	b, ok := f.files[path]
	return b, ok
}

func (f *fakeSubstrate) LookupApproval(contentKey, generation string) bool {
	return f.approvals[contentKey+"\x00"+generation]
}

func (f *fakeSubstrate) LookupChild(id string) bool { return f.children[id] }

func waveBClock() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func TestRegisterWaitRejectsHallucinatedJob(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", time.Now())
	if _, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_999"}, time.Now()); ok {
		t.Fatal("hallucinated job must be rejected")
	}
}

func TestRegisterWaitRejectsHallucinatedTargets(t *testing.T) {
	sub := &fakeSubstrate{}
	cases := []struct {
		name string
		req  goal.WaitKind
		want string // rejection reason must name this target
	}{
		{"job", goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_999", Timeout: time.Minute}, "job_999"},
		{"delegate", goal.WaitKind{Kind: goal.WaitUntilDelegate, Target: "dlg_nope", Timeout: time.Minute}, "dlg_nope"},
		{"child", goal.WaitKind{Kind: goal.WaitUntilChild, Target: "child_nope", Timeout: time.Minute}, "child_nope"},
		{"approval", goal.WaitKind{Kind: goal.WaitUntilApproval, Target: "ship it?", AskGeneration: "gen1", Timeout: time.Minute}, "ship it?"},
		{"file", goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/nope.md", Timeout: time.Minute}, "/sandbox/nope.md"},
		{"http-removed", goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventHTTPMatch, Target: "https://example.com/hook", Timeout: time.Minute}, "http_match"},
	}
	for _, tc := range cases {
		s := goal.NewStore()
		s.Set("x", waveBClock())
		s.SetSubstrate(sub)
		if _, ok := s.RegisterWait(tc.req, waveBClock()); ok {
			t.Errorf("%s: hallucinated target must be rejected", tc.name)
			continue
		}
		if reason := s.LastRejectReason(); !strings.Contains(reason, tc.want) {
			t.Errorf("%s: reject reason %q must name %q", tc.name, reason, tc.want)
		}
		if snap, _ := s.Snapshot(); snap.Status != goal.StatusActive {
			t.Errorf("%s: rejected registration must not park: %+v", tc.name, snap)
		}
	}
}

func TestRegisterWaitLiveJobParks(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	s.SetSubstrate(&fakeSubstrate{jobs: map[string]fakeTarget{"job_1": {live: true}}})
	w, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_1", Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatalf("live job must park: %q", s.LastRejectReason())
	}
	if w.Lease.WaitID == "" || w.Lease.Kind != goal.WaitUntilJob {
		t.Fatalf("bad lease: %+v", w)
	}
	snap, _ := s.Snapshot()
	if snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting", snap.Status)
	}
	if reason := s.LastRejectReason(); reason != "" {
		t.Fatalf("success must clear the reject reason, got %q", reason)
	}
}

func TestRegisterWaitRetainedTerminalCatchUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  goal.WaitKind
		sub  *fakeSubstrate
	}{
		{"job", goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_7", Timeout: time.Minute},
			&fakeSubstrate{jobs: map[string]fakeTarget{"job_7": {retained: true, excerpt: "job job_7 exited 0"}}}},
		{"delegate", goal.WaitKind{Kind: goal.WaitUntilDelegate, Target: "dlg_7", Timeout: time.Minute},
			&fakeSubstrate{delegates: map[string]fakeTarget{"dlg_7": {retained: true, excerpt: "delegate dlg_7 completed"}}}},
	} {
		s := goal.NewStore()
		s.Set("x", waveBClock())
		s.SetSubstrate(tc.sub)
		w, ok := s.RegisterWait(tc.req, waveBClock())
		if !ok {
			t.Fatalf("%s: retained-terminal must catch up, got reject %q", tc.name, s.LastRejectReason())
		}
		snap, _ := s.Snapshot()
		if snap.Status != goal.StatusActive {
			t.Fatalf("%s: catch-up must not park: %+v", tc.name, snap)
		}
		gsnap, _ := s.GoalSnapshot()
		if len(gsnap.Waits) != 0 {
			t.Fatalf("%s: catch-up leaves no live lease: %+v", tc.name, gsnap.Waits)
		}
		if len(gsnap.PendingWake) != 1 || gsnap.PendingWake[0].WaitID != w.Lease.WaitID {
			t.Fatalf("%s: want one pending wake for %q, got %+v", tc.name, w.Lease.WaitID, gsnap.PendingWake)
		}
		if !strings.Contains(gsnap.PendingWake[0].Trigger, "exited 0") && !strings.Contains(gsnap.PendingWake[0].Trigger, "completed") {
			t.Fatalf("%s: pending trigger must carry the terminal excerpt: %+v", tc.name, gsnap.PendingWake[0])
		}
	}
}

func TestRegisterWaitApprovalGenerationBinding(t *testing.T) {
	sub := &fakeSubstrate{approvals: map[string]bool{"ship it?\x00gen2": true}}
	s := goal.NewStore()
	s.Set("x", waveBClock())
	s.SetSubstrate(sub)
	stale := goal.WaitKind{Kind: goal.WaitUntilApproval, Target: "ship it?", AskGeneration: "gen1", Timeout: time.Minute}
	if _, ok := s.RegisterWait(stale, waveBClock()); ok {
		t.Fatal("dangling wait from an earlier same-text ask must not validate against a later generation")
	}
	if reason := s.LastRejectReason(); !strings.Contains(reason, "gen1") {
		t.Fatalf("reject reason %q must name the stale generation", reason)
	}
	fresh := goal.WaitKind{Kind: goal.WaitUntilApproval, Target: "ship it?", AskGeneration: "gen2", Timeout: time.Minute}
	if _, ok := s.RegisterWait(fresh, waveBClock()); !ok {
		t.Fatalf("live ask generation must park: %q", s.LastRejectReason())
	}
	if snap, _ := s.Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("status = %q, want waiting", snap.Status)
	}
}

func TestRegisterWaitFileBaselineAndURL(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	s.SetSubstrate(&fakeSubstrate{
		files: map[string]string{"/sandbox/plan.md": "sha:abc"},
	})
	w, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventFileModified, Target: "/sandbox/plan.md", Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatalf("stat-able file must park: %q", s.LastRejectReason())
	}
	if w.Lease.Predicate.Baseline != "sha:abc" {
		t.Fatalf("file lease must persist the stat baseline, got %+v", w.Lease.Predicate)
	}
}

// TestRegisterWaitHTTPMatchRemoved pins the http_match removal (issue
// #1061): any http_match registration rejects with the removal named —
// never parks, even for a well-formed URL.
func TestRegisterWaitHTTPMatchRemoved(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	s.SetSubstrate(&fakeSubstrate{})
	for _, target := range []string{"https://example.com/hook", "://bad"} {
		if _, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilEvent, EventSubtype: goal.EventHTTPMatch, Target: target, Timeout: time.Minute}, waveBClock()); ok {
			t.Fatalf("http_match %q must reject (removed, issue #1061)", target)
		}
		if reason := s.LastRejectReason(); !strings.Contains(reason, "1061") {
			t.Fatalf("http_match reject reason %q must name issue #1061", reason)
		}
		if snap, _ := s.Snapshot(); snap.Status != goal.StatusActive {
			t.Fatalf("rejected registration must not park: %+v", snap)
		}
	}
}

func TestRegisterWaitMax8(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	// until_time registers substrate-free, so it isolates the cap from
	// substrate wiring (external_label filled this role before its
	// removal in issue #1063). Same-target re-register replaces, so
	// slots differ by target.
	for i := 1; i <= goal.MaxLiveWaitsPerGoal; i++ {
		req := goal.WaitKind{Kind: goal.WaitUntilTime, Target: "timer-" + string(rune('0'+i)), Timeout: time.Minute}
		if _, ok := s.RegisterWait(req, waveBClock()); !ok {
			t.Fatalf("wait %d must register: %q", i, s.LastRejectReason())
		}
	}
	extra := goal.WaitKind{Kind: goal.WaitUntilTime, Target: "timer-9", Timeout: time.Minute}
	if _, ok := s.RegisterWait(extra, waveBClock()); ok {
		t.Fatal("9th live wait must be rejected")
	}
	if reason := s.LastRejectReason(); !strings.Contains(reason, "8") {
		t.Fatalf("reject reason %q must name the cap", reason)
	}
	gsnap, _ := s.GoalSnapshot()
	if len(gsnap.Waits) != goal.MaxLiveWaitsPerGoal {
		t.Fatalf("want %d live waits, got %d", goal.MaxLiveWaitsPerGoal, len(gsnap.Waits))
	}
	if !s.CancelWait(gsnap.Waits[0].Lease.WaitID, waveBClock()) {
		t.Fatal("cancel must free a slot")
	}
	if _, ok := s.RegisterWait(extra, waveBClock()); !ok {
		t.Fatalf("register after cancel must succeed: %q", s.LastRejectReason())
	}
}

func TestRegisterWaitDedupeAndReplace(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	first, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatal("register should succeed")
	}
	dup, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, waveBClock())
	if !ok || dup.Lease.WaitID != first.Lease.WaitID {
		t.Fatalf("identical re-register must dedupe to %q, got %+v ok=%v", first.Lease.WaitID, dup, ok)
	}
	gsnap, _ := s.GoalSnapshot()
	if len(gsnap.Waits) != 1 {
		t.Fatalf("dedupe must not mint a lease: %+v", gsnap.Waits)
	}
	repl, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: 2 * time.Minute}, waveBClock())
	if !ok || repl.Lease.WaitID == first.Lease.WaitID {
		t.Fatalf("same-target new-deadline must replace, got %+v ok=%v", repl, ok)
	}
	gsnap, _ = s.GoalSnapshot()
	if len(gsnap.Waits) != 1 || gsnap.Waits[0].Lease.WaitID != repl.Lease.WaitID {
		t.Fatalf("replace must leave exactly the new lease: %+v", gsnap.Waits)
	}
	if gsnap.Waits[0].Lease.Deadline != waveBClock().Add(2*time.Minute) {
		t.Fatalf("replaced lease must carry the new deadline: %+v", gsnap.Waits[0].Lease)
	}
}

func TestCancelWaitAndClaimFire(t *testing.T) {
	s := goal.NewStore()
	s.Set("x", waveBClock())
	w, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatal("register should succeed")
	}
	if s.CancelWait("wait_404", waveBClock()) {
		t.Fatal("cancel of an unknown id must fail")
	}
	if snap, _ := s.Snapshot(); snap.Status != goal.StatusWaiting {
		t.Fatalf("failed cancel must not disturb waiting: %+v", snap)
	}
	entry, ok := s.ClaimFire(w.Lease.WaitID, "timer fired", waveBClock())
	if !ok || entry.WaitID != w.Lease.WaitID || entry.Trigger != "timer fired" {
		t.Fatalf("claim = %+v ok=%v", entry, ok)
	}
	if _, ok := s.ClaimFire(w.Lease.WaitID, "timer fired", waveBClock()); ok {
		t.Fatal("double claim must collapse: second claim returns already-fired")
	}
	gsnap, _ := s.GoalSnapshot()
	if len(gsnap.Waits) != 0 || len(gsnap.PendingWake) != 1 {
		t.Fatalf("claim moves the lease exactly once: %+v", gsnap)
	}
	if snap, _ := s.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("last claim returns the goal to active: %+v", snap)
	}
	w2, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatal("register should succeed")
	}
	if !s.CancelWait(w2.Lease.WaitID, waveBClock()) {
		t.Fatal("cancel of the live lease must succeed")
	}
	if snap, _ := s.Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("cancel of the last live lease returns to active: %+v", snap)
	}
}
