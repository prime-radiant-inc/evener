package hubcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func writeRendezvous(t *testing.T, dir string, e rendezvous.Entry) {
	t.Helper()
	if _, err := rendezvous.Write(dir, e); err != nil {
		t.Fatalf("write rendezvous: %v", err)
	}
}

func fuzzScenarioRoster_LoadFromDir(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:        1001,
		Address:    "127.0.0.1:50001",
		WorkingDir: "/tmp/a",
		Model:      "gpt-5.2",
		Provider:   "openai",
		StartedAt:  time.Now().UTC(),
		SpawnedBy:  "user",
	})
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:        1002,
		Address:    "127.0.0.1:50002",
		WorkingDir: "/tmp/b",
		Model:      "claude-opus-4-7",
		Provider:   "anthropic",
		StartedAt:  time.Now().UTC(),
		SpawnedBy:  "hub",
	})

	r := NewRoster(dir, nil) // nil prober skips liveness for this test
	r.Refresh()
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
}

func fuzzScenarioRoster_FindBySessionID(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	r := NewRoster(dir, fakeProber{
		sessionID: "02wMz5Txv1C3Hut0M8GCeB",
	})
	r.Refresh()
	got, ok := r.Find("02wMz5Txv1C3Hut0M8GCeB")
	if !ok {
		t.Fatal("expected to find session")
	}
	if got.Address != "127.0.0.1:50001" {
		t.Errorf("Address: got %q", got.Address)
	}
}

func fuzzScenarioRosterListOrdersByStartedAtAndID(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	r := NewRoster(t.TempDir(), nil)
	r.byPID = map[int]LiveEntry{
		2: {Entry: rendezvous.Entry{PID: 2, StartedAt: base.Add(-time.Hour)}, SessionID: "02OLD"},
		1: {Entry: rendezvous.Entry{PID: 1, StartedAt: base}, SessionID: "01NEW"},
		4: {Entry: rendezvous.Entry{PID: 4, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "04TIEB"},
		3: {Entry: rendezvous.Entry{PID: 3, StartedAt: base.Add(-2 * time.Hour)}, SessionID: "03TIEA"},
	}

	got := r.List()
	gotIDs := make([]string, 0, len(got))
	for _, entry := range got {
		gotIDs = append(gotIDs, entry.SessionID)
	}
	want := []string{"01NEW", "02OLD", "03TIEA", "04TIEB"}
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", gotIDs, want)
	}
}

func fuzzScenarioRosterListDedupesSessionIDPreferringAppWireEntry(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	r := NewRoster(t.TempDir(), nil)
	r.byPID = map[int]LiveEntry{
		1: {
			Entry:     rendezvous.Entry{PID: 1, StartedAt: base.Add(time.Hour)},
			SessionID: "01SAME",
		},
		2: {
			Entry: rendezvous.Entry{
				PID:       2,
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws://127.0.0.1:2/rpc",
				ThreadID:  "01SAME",
				SessionID: "01SAME",
				StartedAt: base,
			},
			SessionID: "01SAME",
		},
	}

	got := r.List()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].PID != 2 {
		t.Fatalf("pid=%d, want appwire pid 2", got[0].PID)
	}
}

// fuzzScenarioRoster_PrunesUnreachableDeadProcess covers a dead process whose
// rendezvous file never resolved a session id (no probe ever succeeded before
// it died) - there is nothing to attribute a crash marker to, so it is still
// dropped entirely. Contrast with fuzzScenarioRoster_SurfacesCrashedProcessAsErrored
// below, where a resolved session id turns the same "dead process, stale file"
// situation into a retained "errored" entry instead of a silent drop.
func fuzzScenarioRoster_PrunesUnreachableDeadProcess(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	r := NewRoster(dir, fakeProber{shouldFail: true})
	r.procAlive = func(int) bool { return false } // process is gone → stale file
	r.Refresh()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("expected a dead daemon's stale rendezvous entry with no session id to be pruned, got %d", len(got))
	}
}

// fuzzScenarioRoster_SurfacesCrashedProcessAsErrored is the regression test for
// kata zm6s: a session that was genuinely live (probe succeeded, session id
// resolved) and then had its process SIGKILLed must not silently disappear
// from the roster the same way a gracefully-finished session does - rendezvous
// files are only removed on graceful shutdown (rendezvous package doc comment;
// rvreg.Registration.Remove), so a stale file with a confirmed-dead PID means a
// crash, not a normal exit. The entry is retained with Status forced to
// "errored" (hubcore.NormalizeState already treats that string as a first-class
// error lane) rather than dropped, so BuildTree's stateFor finds it and reports
// "errored" instead of falling back to the generic "ended" every normally-
// completed session also reports.
func fuzzScenarioRoster_SurfacesCrashedProcessAsErrored(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       1001,
		Address:   "127.0.0.1:50001",
		SessionID: "01CRASHED",
	})

	prober := &flakyProber{sessionID: "01CRASHED"}
	r := NewRoster(dir, prober)
	r.procAlive = func(int) bool { return true } // process starts out alive
	r.Refresh()
	if live, ok := r.Find("01CRASHED"); !ok || live.Crashed {
		t.Fatalf("reachable entry = %+v, want present and not crashed", live)
	}

	// kill -9: the probe now fails AND the process is confirmed gone.
	prober.fail = true
	r.procAlive = func(int) bool { return false }
	r.Refresh()

	got, ok := r.Find("01CRASHED")
	if !ok {
		t.Fatal("a crashed session must remain in the roster, marked errored - not silently dropped")
	}
	if got.Status != "errored" {
		t.Fatalf("crashed session status = %q, want %q", got.Status, "errored")
	}
	if !got.Crashed {
		t.Fatal("retained dead-process entry is not marked crashed")
	}

	// Stable across subsequent refreshes: it must not flip back to something
	// else, nor eventually get pruned, once marked as crashed.
	r.Refresh()
	got, ok = r.Find("01CRASHED")
	if !ok || got.Status != "errored" || !got.Crashed {
		t.Fatalf("crashed marker did not persist across a later refresh: ok=%v entry=%+v", ok, got)
	}
}

// fuzzScenarioRoster_SurfacesStaleCrashOnFreshRoster proves the crash marker
// does not depend on the roster's own in-memory history: a BRAND NEW Roster
// (as after a hub restart) that discovers an already-stale rendezvous file -
// dead PID, resolved session id, first refresh ever - must surface it as
// "errored" too, not just a roster that watched the crash happen live. The
// durable signal is the file itself (still on disk because the daemon never
// got to run its graceful-shutdown Remove()), not anything the roster
// remembered from a previous Refresh.
func fuzzScenarioRoster_SurfacesStaleCrashOnFreshRoster(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       1002,
		Address:   "127.0.0.1:50002",
		SessionID: "01ALREADYDEAD",
	})

	r := NewRoster(dir, fakeProber{shouldFail: true})
	r.procAlive = func(int) bool { return false } // never seen alive by THIS roster
	r.Refresh()

	got, ok := r.Find("01ALREADYDEAD")
	if !ok {
		t.Fatal("a stale rendezvous file for a resolved session id must surface as errored even on a fresh roster")
	}
	if got.Status != "errored" {
		t.Fatalf("status = %q, want %q", got.Status, "errored")
	}
	if !got.Crashed {
		t.Fatal("fresh roster's retained dead-process entry is not marked crashed")
	}
}

func TestRoster_FailedProbeDoesNotAdmitColdEntryWithReusedPID(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       1001,
		Endpoint:  "ws://127.0.0.1:50001/rpc",
		ThreadID:  "01STALE",
		SessionID: "01STALE",
	})
	r := NewRoster(dir, fakeProber{shouldFail: true})
	r.procAlive = func(int) bool { return true } // PID was reused by an unrelated process.

	r.Refresh()

	if got := r.List(); len(got) != 0 {
		t.Fatalf("failed probe admitted an unverified cold entry: %+v", got)
	}
}

// TestRoster_KeepsAliveDaemonThroughProbeFailures is the regression test for the
// "flash of no sessions" bug: a live daemon that transiently fails its /status
// probe (busy daemon / overloaded host) must stay in the roster, not blank the
// sidebar.
func fuzzScenarioRoster_KeepsAliveDaemonThroughProbeFailures(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       1001,
		Address:   "127.0.0.1:50001",
		SessionID: "01ALIVE",
	})

	// First, a successful probe seeds the entry.
	prober := &flakyProber{sessionID: "01ALIVE"}
	r := NewRoster(dir, prober)
	r.procAlive = func(int) bool { return true } // process stays alive throughout
	r.Refresh()
	if _, ok := r.Find("01ALIVE"); !ok {
		t.Fatal("entry should be present after a successful probe")
	}

	// Now the daemon goes unresponsive for several consecutive refreshes. It
	// must remain in the roster the entire time (the bug pruned it after two).
	prober.fail = true
	for i := range 5 {
		r.Refresh()
		if got := r.List(); len(got) != 1 {
			t.Fatalf("refresh %d: live daemon dropped on probe failure (flash), got %d entries", i, len(got))
		}
	}

	// When the process actually dies, the next failed probe retains it,
	// marked "errored" (kata zm6s) rather than pruning it - a crash must
	// read differently from a session that simply finished.
	r.procAlive = func(int) bool { return false }
	r.Refresh()
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("a crashed daemon should be retained as errored, not pruned, got %d entries", len(got))
	}
	if got[0].Status != "errored" {
		t.Fatalf("crashed daemon status = %q, want %q", got[0].Status, "errored")
	}
}

func fuzzScenarioRoster_FindMissing(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	if _, ok := r.Find("missing"); ok {
		t.Fatal("expected missing to return false")
	}
}

func fuzzScenarioRoster_DefaultRunDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	want := filepath.Join("/tmp/fakehome", ".serf", "run") //nolint:gocritic // filepathJoin: base is a full home path; mirrors rendezvous.DefaultDir
	if got := rendezvous.DefaultDir(); got != want {
		t.Fatalf("DefaultDir: got %q want %q", got, want)
	}
}

func fuzzScenarioRoster_Watch_PicksUpNewFile(t *testing.T) {
	dir := t.TempDir()
	r := NewRoster(dir, fakeProber{sessionID: "02wMz5Txv1C3Hut0M8GCeB"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// watchReady is closed by Watch immediately after w.Add(runDir) returns, so
	// we know the fsnotify watcher is registered before we create the rendezvous
	// file. This replaces the old 100 ms sleep, which was a race: on a loaded
	// scheduler the goroutine might not have reached w.Add yet.
	watchReady := make(chan struct{})
	r.watchReadyFn = func() { close(watchReady) }
	go r.Watch(ctx)
	<-watchReady // guaranteed: watcher is active before the file is written

	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.Find("02wMz5Txv1C3Hut0M8GCeB"); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("roster did not pick up the new rendezvous file")
}

// fakeProber implements liveness check for tests without real network calls.
type fakeProber struct {
	sessionID  string
	status     string
	pendingAsk bool
	shouldFail bool
}

type runningSubagentProber struct {
	result ProbeResult
}

func (p *runningSubagentProber) Probe(rendezvous.Entry) ProbeResult {
	return p.result
}

func fuzzScenarioRoster_CarriesRunningSubagentsWithoutRoutingThem(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID:          "01PARENT",
		Status:             "idle",
		RunningSubagentIDs: []string{"01CHILD"},
		OK:                 true,
	}}
	r := NewRoster(dir, prober)
	r.Refresh()

	entries := r.List()
	if len(entries) != 1 || len(entries[0].RunningSubagentIDs) != 1 || entries[0].RunningSubagentIDs[0] != "01CHILD" {
		t.Fatalf("roster entries = %+v, want parent carrying 01CHILD", entries)
	}
	if !r.IsSubagentActive("01CHILD") {
		t.Fatal("running child must be discoverable as active")
	}
	if _, ok := r.Find("01CHILD"); ok {
		t.Fatal("running child must not become a routable daemon entry")
	}

	changes := 0
	r.SetOnChange(func() { changes++ })
	prober.result.RunningSubagentIDs = nil
	r.Refresh()
	if changes != 1 {
		t.Fatalf("onChange calls after child stopped = %d, want 1", changes)
	}
	if r.IsSubagentActive("01CHILD") {
		t.Fatal("stopped child must no longer be active")
	}
}

func (p fakeProber) Probe(rendezvous.Entry) ProbeResult {
	if p.shouldFail {
		return ProbeResult{}
	}
	return ProbeResult{SessionID: p.sessionID, Status: p.status, PendingAsk: p.pendingAsk, OK: true}
}

func fuzzScenarioRoster_CarriesPendingAskFromProber(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	prober := fakeProber{sessionID: "01A", status: "awaiting", pendingAsk: true}
	r := NewRoster(dir, prober)
	r.Refresh()
	entries := r.List()
	if len(entries) != 1 || !entries[0].PendingAsk {
		t.Fatalf("expected one live entry with PendingAsk=true, got %+v", entries)
	}
}

func TestRosterRunningSubagent(t *testing.T) {
	fuzzScenarioRoster_CarriesRunningSubagentsWithoutRoutingThem(t)
}

func TestRosterSubagentUnresolvedOwner(t *testing.T) {
	r := NewRosterWithEntries(LiveEntry{
		RunningSubagentIDs: []string{"child-unresolved-owner"},
	})
	if !r.IsSubagentActive("child-unresolved-owner") {
		t.Fatal("running child must be active even when its owner has no resolved session ID")
	}
}

func fuzzScenarioRoster_ListReturnsDefensiveRunningIDs(t *testing.T) {
	r := NewRosterWithEntries(LiveEntry{
		SessionID:          "parent",
		RunningSubagentIDs: []string{"child"},
	})
	got := r.List()
	got[0].RunningSubagentIDs[0] = "mutated"
	if r.List()[0].RunningSubagentIDs[0] != "child" {
		t.Fatal("List must return a defensive copy of running subagent IDs")
	}
}

func TestRosterListReturnsDefensiveSubagentIDs(t *testing.T) {
	fuzzScenarioRoster_ListReturnsDefensiveRunningIDs(t)
}

func fuzzScenarioRoster_FingerprintIncludesRunningIDs(t *testing.T) {
	base := map[string]LiveEntry{"parent": {RunningSubagentIDs: []string{"child-a"}}}
	changed := map[string]LiveEntry{"parent": {RunningSubagentIDs: []string{"child-b"}}}
	if rosterFingerprint(base) == rosterFingerprint(changed) {
		t.Fatal("roster fingerprint must change when only running IDs change")
	}
	crashed := map[string]LiveEntry{"parent": {RunningSubagentIDs: []string{"child-a"}, Crashed: true}}
	if rosterFingerprint(base) == rosterFingerprint(crashed) {
		t.Fatal("roster fingerprint must change when only crash provenance changes")
	}
}

func TestRosterFingerprint(t *testing.T) { fuzzScenarioRoster_FingerprintIncludesRunningIDs(t) }

type overlappingRefreshProber struct {
	calls         atomic.Int32
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
}

func (p *overlappingRefreshProber) Probe(rendezvous.Entry) ProbeResult {
	switch p.calls.Add(1) {
	case 1:
		close(p.firstStarted)
		<-p.releaseFirst
		return ProbeResult{SessionID: "parent", Status: "old", RunningSubagentIDs: []string{"old-child"}, OK: true}
	case 2:
		close(p.secondStarted)
		return ProbeResult{SessionID: "parent", Status: "new", RunningSubagentIDs: []string{"new-child"}, OK: true}
	default:
		return ProbeResult{SessionID: "parent", Status: "unexpected", OK: true}
	}
}

func TestRoster_RefreshRejectsStaleConcurrentCommit(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:1"})
	prober := &overlappingRefreshProber{
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	r := NewRoster(dir, prober)
	var callbacks atomic.Int32
	r.SetOnChange(func() { callbacks.Add(1) })
	oldDone := make(chan struct{})
	go func() { r.Refresh(); close(oldDone) }()
	<-prober.firstStarted
	newDone := make(chan struct{})
	go func() { r.Refresh(); close(newDone) }()
	<-prober.secondStarted
	close(prober.releaseFirst)
	<-oldDone
	<-newDone

	entry, ok := r.Find("parent")
	if !ok || entry.Status != "new" || len(entry.RunningSubagentIDs) != 1 || entry.RunningSubagentIDs[0] != "new-child" {
		t.Fatalf("final roster = %+v, found=%v; want newer status and running child", entry, ok)
	}
	if got := callbacks.Load(); got != 1 {
		t.Fatalf("onChange callbacks = %d, want one committed refresh callback", got)
	}
}

// gateProber blocks each probe on a channel, so a test can hold a Refresh in
// the middle of its probe pass and assert List() stays responsive.
type gateProber struct {
	sessionID string
	gate      chan struct{}
	started   chan struct{}
}

func (p *gateProber) Probe(rendezvous.Entry) ProbeResult {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.gate
	return ProbeResult{SessionID: p.sessionID, OK: true}
}

// TestRoster_ListStaysResponsiveDuringSlowProbe is the regression test for the
// startup/refresh hang: Refresh must probe without holding the roster lock, so
// List() returns the last good snapshot instead of blocking on a slow probe.
func fuzzScenarioRoster_ListStaysResponsiveDuringSlowProbe(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:1", SessionID: "01S"})
	started := make(chan struct{}, 1)

	// Seed a good snapshot (first probe is let straight through).
	open := make(chan struct{})
	close(open)
	r := NewRoster(dir, &gateProber{sessionID: "01S", gate: open, started: started})
	r.procAlive = func(int) bool { return true }
	r.Refresh()
	if _, ok := r.Find("01S"); !ok {
		t.Fatal("seed refresh did not populate the roster")
	}

	// Now a refresh blocks mid-probe. List() must not wait for it.
	blocked := make(chan struct{})
	r.prober = &gateProber{sessionID: "01S", gate: blocked, started: started}
	go r.Refresh()
	<-started // the probe is now blocked, with no roster lock held

	done := make(chan int, 1)
	go func() { done <- len(r.List()) }()
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("List returned %d during a blocked probe, want the prior snapshot (1)", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("List blocked while a probe was in flight (roster lock held during probing)")
	}
	close(blocked) // let the background refresh finish
}

// flakyProber can be flipped from succeeding to failing mid-test (pointer
// receiver), to simulate a daemon that goes transiently unresponsive.
type flakyProber struct {
	sessionID string
	status    string
	fail      bool
}

func (p *flakyProber) Probe(rendezvous.Entry) ProbeResult {
	if p.fail {
		return ProbeResult{}
	}
	return ProbeResult{SessionID: p.sessionID, Status: p.status, OK: true}
}

func fuzzScenarioPreferLiveEntry(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		candidate LiveEntry
		current   LiveEntry
		want      bool
	}{
		{
			name:      "appwire beats non-appwire",
			candidate: LiveEntry{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://1", ThreadID: "t1"}},
			current:   LiveEntry{Entry: rendezvous.Entry{Protocol: "v0", Endpoint: "", ThreadID: ""}},
			want:      true,
		},
		{
			name:      "non-appwire loses to appwire",
			candidate: LiveEntry{Entry: rendezvous.Entry{Protocol: "v0", Endpoint: "", ThreadID: ""}},
			current:   LiveEntry{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://1", ThreadID: "t1"}},
			want:      false,
		},
		{
			// ProtocolVersion alone is not enough: an empty Endpoint must not
			// count as appwire, so this falls through to the PID tiebreak (lower
			// PID loses) rather than winning on protocol.
			name:      "protocol set but empty endpoint is not appwire",
			candidate: LiveEntry{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "", ThreadID: "t1", PID: 1, StartedAt: base}},
			current:   LiveEntry{Entry: rendezvous.Entry{Protocol: "v0", Endpoint: "", ThreadID: "", PID: 2, StartedAt: base}},
			want:      false,
		},
		{
			// Likewise an empty ThreadID disqualifies appwire status, so the
			// lower-PID candidate loses on the tiebreak instead of winning.
			name:      "protocol set but empty thread id is not appwire",
			candidate: LiveEntry{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://1", ThreadID: "", PID: 1, StartedAt: base}},
			current:   LiveEntry{Entry: rendezvous.Entry{Protocol: "v0", Endpoint: "", ThreadID: "", PID: 2, StartedAt: base}},
			want:      false,
		},
		{
			name:      "same protocol, newer started wins",
			candidate: LiveEntry{Entry: rendezvous.Entry{StartedAt: base.Add(time.Hour)}},
			current:   LiveEntry{Entry: rendezvous.Entry{StartedAt: base}},
			want:      true,
		},
		{
			name:      "same protocol, older started loses",
			candidate: LiveEntry{Entry: rendezvous.Entry{StartedAt: base}},
			current:   LiveEntry{Entry: rendezvous.Entry{StartedAt: base.Add(time.Hour)}},
			want:      false,
		},
		{
			name:      "same started, higher PID wins",
			candidate: LiveEntry{Entry: rendezvous.Entry{PID: 2, StartedAt: base}},
			current:   LiveEntry{Entry: rendezvous.Entry{PID: 1, StartedAt: base}},
			want:      true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := preferLiveEntry(c.candidate, c.current); got != c.want {
				t.Errorf("preferLiveEntry() = %v, want %v", got, c.want)
			}
		})
	}
}

func fuzzScenarioProcessAlive(t *testing.T) {
	if processAlive(0) {
		t.Fatal("processAlive(0) should be false")
	}
	if processAlive(-1) {
		t.Fatal("processAlive(-1) should be false")
	}
	// Current process should be alive.
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive(current) should be true")
	}
}

// statusProber returns a fixed session id and status for every entry probed;
// swapping .status between Refresh calls simulates a daemon's state
// transition (e.g. "working" -> "idle") for TestRoster_OnStatusChange tests.
type statusProber struct {
	sessionID string
	status    string
}

func (p *statusProber) Probe(rendezvous.Entry) ProbeResult {
	return ProbeResult{SessionID: p.sessionID, Status: p.status, OK: true}
}

// TestRoster_OnStatusChangeFiresForTransitioningSession is the regression
// test for the tree-freshness fix: a session's Status changing between two
// consecutive Refresh snapshots must fire the per-session hook with that
// session's id, so the hub can re-read just that session's on-disk meta
// instead of waiting for the next full past-index rebuild.
func fuzzScenarioRoster_OnStatusChangeFiresForTransitioningSession(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})

	prober := &statusProber{sessionID: "02wMz5Txv2enqVTitaig6F", status: "working"}
	r := NewRoster(dir, prober)
	r.Refresh() // seed: no prior snapshot, so no transition to report

	var got []string
	r.SetOnStatusChange(func(sessionID string) { got = append(got, sessionID) })

	prober.status = "idle"
	r.Refresh()

	if len(got) != 1 || got[0] != "02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("expected onStatusChange(02wMz5Txv2enqVTitaig6F) once, got %v", got)
	}
}

// TestRoster_OnStatusChangeNotFiredWhenStatusUnchanged pins the other half:
// a Refresh whose per-session status set is identical to the prior snapshot
// must not fire the hook, so a targeted re-read isn't triggered on every
// roster poll (only genuine transitions).
func fuzzScenarioRoster_OnStatusChangeNotFiredWhenStatusUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})

	prober := &statusProber{sessionID: "02wMz5Txv2enqVTitaig6F", status: "working"}
	r := NewRoster(dir, prober)
	r.Refresh()

	fired := false
	r.SetOnStatusChange(func(sessionID string) { fired = true })

	r.Refresh() // same status both times
	if fired {
		t.Fatal("onStatusChange fired for an unchanged status")
	}
}

// TestRoster_StatusChangeDrivesPastIndexRefreshAndVersionBump exercises the
// full tree-freshness fix end to end, mirroring how cmd/serf-hub/main.go
// wires the pieces together: a session's status transition (as detected by
// Roster.Refresh) drives PastIndex.RefreshOne, which re-reads the session's
// on-disk meta and, on a genuine content delta, bumps the shared
// InputsVersion counter the /api/tree memo keys on. Before this fix, that
// bump only happened on PastIndex's own 60s Rebuild ticker.
func fuzzScenarioRoster_StatusChangeDrivesPastIndexRefreshAndVersionBump(t *testing.T) {
	stateRoot := t.TempDir()
	proj := filepath.Join(stateRoot, "project-test-0123456789")
	base := time.Unix(1_700_000_000, 0)
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv2enqVTitaig6F",
		UpdatedAt: base,
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/w"},
	})

	past := NewPastIndex(filepath.Join(stateRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	rendezvousDir := t.TempDir()
	writeRendezvous(t, rendezvousDir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &statusProber{sessionID: "02wMz5Txv2enqVTitaig6F", status: "working"}
	roster := NewRoster(rendezvousDir, prober)

	inputs := &InputsVersion{}
	past.SetOnChange(inputs.Bump)
	roster.SetOnChange(inputs.Bump)
	roster.SetOnStatusChange(func(sessionID string) { past.RefreshOne(sessionID) })

	roster.Refresh() // seed: membership change alone bumps the version once
	seeded := inputs.Load()
	if seeded == 0 {
		t.Fatal("expected the seeding refresh to bump the version at least once")
	}

	// Out-of-process rewrite of the daemon's own meta.json, exactly like
	// maybeAutoSave, paired with the daemon's status transitioning.
	writeMeta(t, proj, schema.SessionMeta{
		ID:        "02wMz5Txv2enqVTitaig6F",
		UpdatedAt: base.Add(time.Minute),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/w"},
	})
	prober.status = "idle"

	roster.Refresh()

	if got := inputs.Load(); got <= seeded {
		t.Fatalf("expected version to bump again after the status transition, got %d (seeded=%d)", got, seeded)
	}
	entry, ok := past.Find("02wMz5Txv2enqVTitaig6F")
	if !ok {
		t.Fatal("expected 02wMz5Txv2enqVTitaig6F to remain indexed")
	}
	if !entry.Meta.UpdatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("expected the past index to reflect the re-read UpdatedAt, got %v", entry.Meta.UpdatedAt)
	}
}

func fuzzScenarioNewRosterWithEntries(t *testing.T) {
	r := NewRosterWithEntries(
		LiveEntry{Entry: rendezvous.Entry{PID: 1, Address: "127.0.0.1:1"}, SessionID: "01A"},
		LiveEntry{Entry: rendezvous.Entry{PID: 2, Address: "127.0.0.1:2"}, SessionID: "01B"},
		LiveEntry{Entry: rendezvous.Entry{PID: 3, Address: "127.0.0.1:3"}, SessionID: ""},
	)
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List = %d, want 3", len(got))
	}
	// The session-less entry is indexed by PID and surfaces in List() under its
	// own (empty session) identity.
	var byPID = make(map[int]LiveEntry, len(got))
	for _, e := range got {
		byPID[e.PID] = e
	}
	if _, ok := byPID[3]; !ok {
		t.Fatal("expected session-less entry (PID 3) in List")
	}

	found, ok := r.Find("01A")
	if !ok {
		t.Fatal("expected to find 01A")
	}
	if found.PID != 1 || found.Address != "127.0.0.1:1" {
		t.Fatalf("Find(01A) = {PID:%d Address:%q}, want {PID:1 Address:127.0.0.1:1}", found.PID, found.Address)
	}

	// The empty SessionID must not be indexed for lookup; the guard in
	// NewRosterWithEntries keeps bySess free of empty keys.
	if e, ok := r.Find(""); ok {
		t.Fatalf("Find(\"\") = {PID:%d}, want not found", e.PID)
	}
}
