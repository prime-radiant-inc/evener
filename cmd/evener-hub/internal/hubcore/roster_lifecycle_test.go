package hubcore

import (
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// startLifecycleProbeDaemon mirrors startProbeDaemon but installs a daemon
// lifecycle status hook (nil = capability absent, the pre-Task-7 daemon
// shape), so tests can contrast a daemon that answers evener/daemon/status
// with one that refuses it.
func startLifecycleProbeDaemon(t *testing.T, sessionID string, status func() appwire.DaemonLifecycle) (*StatusProber, rendezvous.Entry) {
	t.Helper()
	srv := server.NewServer(server.ServerConfig{})
	srv.SetAppIdentity("local", sessionID)
	srv.SetState(appwire.ThreadStatusIdle)
	srv.SetThreadEnvelopeSource(wireProbeEnvelopeSource{})
	if status != nil {
		srv.SetDaemonLifecycle(status, nil)
	}
	srv.RefreshThreadEnvelope()
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	return &StatusProber{client: httpSrv.Client()}, rendezvous.Entry{
		Endpoint: "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/rpc",
	}
}

// TestFailedLifecycleProbeClearsCapabilityNotOwnership pins the plan line 920
// probe semantic: a failed lifecycle probe clears current capability
// (Lifecycle nil, LifecycleFresh false), never process ownership — OK and the
// residency verdict from the thread snapshots are untouched. The failing peer
// is a real AppWire server with no lifecycle hooks installed, which refuses
// evener/daemon/status with CodeUnavailable.
func TestFailedLifecycleProbeClearsCapabilityNotOwnership(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_lc_fail",
		state:     appwire.ThreadStatusIdle,
		descendants: map[string]string{
			"child-lc": appwire.ThreadStatusActive,
		},
	})
	got := prober.Probe(entry)
	if !got.OK {
		t.Fatal("lifecycle probe failure must not fail the residency probe")
	}
	if got.SessionID != "th_lc_fail" {
		t.Errorf("session ownership lost: session_id = %q", got.SessionID)
	}
	if got.Status != appwire.ThreadStatusIdle {
		t.Errorf("status ownership lost: state = %q", got.Status)
	}
	if len(got.RunningSubagentIDs) != 1 || got.RunningSubagentIDs[0] != "child-lc" {
		t.Errorf("descendant projection lost: %v", got.RunningSubagentIDs)
	}
	if got.RunningSubagentStates["child-lc"] != appwire.ThreadStatusActive {
		t.Errorf("descendant state lost: %v", got.RunningSubagentStates)
	}
	if got.Lifecycle != nil {
		t.Errorf("failed lifecycle probe must clear capability: %+v", got.Lifecycle)
	}
	if got.LifecycleFresh {
		t.Error("LifecycleFresh must be false when daemon/status did not answer")
	}

	// Contrast: the same probe against a daemon that answers daemon/status
	// reports the capability fresh. This proves the nil above comes from the
	// refusal, not from the probe never wiring the lifecycle through.
	live := appwire.DaemonLifecycle{
		Phase:         "preparing",
		TimeoutMillis: 90000,
		Blockers:      []appwire.DaemonBlocker{{Category: "delegate", SessionID: "sess_b", DelegateID: "dlg_b"}},
	}
	okProber, okEntry := startLifecycleProbeDaemon(t, "th_lc_ok", func() appwire.DaemonLifecycle { return live })
	answered := okProber.Probe(okEntry)
	if !answered.OK {
		t.Fatal("probe against lifecycle-capable daemon failed")
	}
	if !answered.LifecycleFresh || answered.Lifecycle == nil {
		t.Fatalf("answering daemon must report fresh lifecycle: %+v fresh=%v", answered.Lifecycle, answered.LifecycleFresh)
	}
	if answered.Lifecycle.Phase != "preparing" || answered.Lifecycle.TimeoutMillis != 90000 ||
		len(answered.Lifecycle.Blockers) != 1 || answered.Lifecycle.Blockers[0].DelegateID != "dlg_b" {
		t.Fatalf("lifecycle did not round-trip the daemon's answer: %+v", answered.Lifecycle)
	}
}

// TestCloneDaemonLifecycleIsolation pins that cloneDaemonLifecycle deep-copies
// the blocker slice: neither side of the clone can corrupt the other, and nil
// (capability unknown) stays nil.
func TestCloneDaemonLifecycleIsolation(t *testing.T) {
	if cloneDaemonLifecycle(nil) != nil {
		t.Fatal("nil lifecycle must clone to nil so capability-unknown survives")
	}
	src := &appwire.DaemonLifecycle{
		Phase:    "preparing",
		Blockers: []appwire.DaemonBlocker{{Category: "session", SessionID: "sess_a"}},
	}
	clone := cloneDaemonLifecycle(src)
	clone.Blockers[0].Category = "corrupted"
	clone.Blockers[0].SessionID = "corrupted"
	if src.Blockers[0] != (appwire.DaemonBlocker{Category: "session", SessionID: "sess_a"}) {
		t.Fatalf("mutating the clone corrupted the source: %+v", src.Blockers[0])
	}
	src.Blockers[0].Category = "mutated-src"
	if clone.Blockers[0].Category != "corrupted" {
		t.Fatalf("mutating the source corrupted the clone: %+v", clone.Blockers[0])
	}
}

// TestRosterLifecycleSnapshotCloneIsolation pins that the roster never aliases
// a lifecycle it ingested or handed out: mutating a probe result after Refresh
// must not corrupt the roster (liveEntryFromProbe clones), and mutating a
// listed snapshot must not corrupt the roster (List/Find clone).
func TestRosterLifecycleSnapshotCloneIsolation(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID: "01PARENT",
		Status:    "idle",
		Lifecycle: &appwire.DaemonLifecycle{
			Phase:    "preparing",
			Blockers: []appwire.DaemonBlocker{{Category: "delegate", SessionID: "sess_a", DelegateID: "dlg_a"}},
		},
		LifecycleFresh: true,
		OK:             true,
	}}
	r := NewRoster(dir, prober)
	r.Refresh()

	// Ingestion direction: corrupt the prober's own copy after Refresh.
	prober.result.Lifecycle.Blockers[0].Category = "corrupted"
	prober.result.Lifecycle.Phase = "corrupted"
	entry, ok := r.Find("01PARENT")
	if !ok || entry.Lifecycle == nil {
		t.Fatalf("roster entry missing after refresh: %+v ok=%v", entry, ok)
	}
	if entry.Lifecycle.Phase != "preparing" ||
		entry.Lifecycle.Blockers[0] != (appwire.DaemonBlocker{Category: "delegate", SessionID: "sess_a", DelegateID: "dlg_a"}) {
		t.Fatalf("probe-result mutation corrupted the roster: %+v", entry.Lifecycle)
	}

	// Hand-out direction: corrupt the listed snapshot.
	entry.Lifecycle.Blockers[0].Category = "corrupted"
	entry.Lifecycle.Phase = "corrupted"
	again, ok := r.Find("01PARENT")
	if !ok || again.Lifecycle == nil {
		t.Fatalf("roster entry missing on second find: %+v ok=%v", again, ok)
	}
	if again.Lifecycle.Phase != "preparing" ||
		again.Lifecycle.Blockers[0] != (appwire.DaemonBlocker{Category: "delegate", SessionID: "sess_a", DelegateID: "dlg_a"}) {
		t.Fatalf("listed-snapshot mutation corrupted the roster: %+v", again.Lifecycle)
	}
}

// TestRosterLifecycleTransitionsBumpFingerprint pins plan line 920's roster
// property: lifecycle phase and blocker transitions hash into
// rosterFingerprint, so Refresh fires onChange exactly on a transition and
// never on a no-op probe.
func TestRosterLifecycleTransitionsBumpFingerprint(t *testing.T) {
	base := func() map[string]LiveEntry {
		return map[string]LiveEntry{"01PARENT": {
			SessionID:      "01PARENT",
			Status:         "idle",
			LifecycleFresh: true,
			Lifecycle: &appwire.DaemonLifecycle{
				Phase:    "resident",
				Blockers: []appwire.DaemonBlocker{},
			},
		}}
	}
	fpBase := rosterFingerprint(base())

	phaseChanged := base()
	phaseChanged["01PARENT"].Lifecycle.Phase = "preparing"
	if rosterFingerprint(phaseChanged) == fpBase {
		t.Fatal("phase transition did not bump rosterFingerprint")
	}

	blockerChanged := base()
	blockerChanged["01PARENT"].Lifecycle.Blockers = []appwire.DaemonBlocker{{Category: "delegate", DelegateID: "dlg_a"}}
	if rosterFingerprint(blockerChanged) == fpBase {
		t.Fatal("blocker transition did not bump rosterFingerprint")
	}

	freshnessChanged := base()
	stale := freshnessChanged["01PARENT"]
	stale.LifecycleFresh = false
	freshnessChanged["01PARENT"] = stale
	if rosterFingerprint(freshnessChanged) == fpBase {
		t.Fatal("losing lifecycle freshness did not bump rosterFingerprint")
	}

	// The observable half: onChange fires on each transition and stays silent
	// on a no-op refresh.
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID:      "01PARENT",
		Status:         "idle",
		Lifecycle:      &appwire.DaemonLifecycle{Phase: "resident", Blockers: []appwire.DaemonBlocker{}},
		LifecycleFresh: true,
		OK:             true,
	}}
	r := NewRoster(dir, prober)
	r.Refresh()

	changes := 0
	r.SetOnChange(func() { changes++ })
	r.Refresh()
	if changes != 0 {
		t.Fatalf("no-op refresh fired onChange %d times", changes)
	}

	prober.result.Lifecycle = &appwire.DaemonLifecycle{Phase: "preparing", Blockers: []appwire.DaemonBlocker{}}
	r.Refresh()
	if changes != 1 {
		t.Fatalf("phase transition fired onChange %d times, want 1", changes)
	}

	prober.result.Lifecycle = &appwire.DaemonLifecycle{
		Phase:    "preparing",
		Blockers: []appwire.DaemonBlocker{{Category: "delegate", DelegateID: "dlg_a"}},
	}
	r.Refresh()
	if changes != 2 {
		t.Fatalf("blocker transition fired onChange %d times total, want 2", changes)
	}
}
