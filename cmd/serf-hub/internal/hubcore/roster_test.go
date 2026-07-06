package hubcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func writeRendezvous(t *testing.T, dir string, e rendezvous.Entry) {
	t.Helper()
	if _, err := rendezvous.Write(dir, e); err != nil {
		t.Fatalf("write rendezvous: %v", err)
	}
}

func TestRoster_LoadFromDir(t *testing.T) {
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

func TestRoster_FindBySessionID(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	r := NewRoster(dir, fakeProber{
		sessionID: "01SESS001",
	})
	r.Refresh()
	got, ok := r.Find("01SESS001")
	if !ok {
		t.Fatal("expected to find session")
	}
	if got.Address != "127.0.0.1:50001" {
		t.Errorf("Address: got %q", got.Address)
	}
}

func TestRosterListOrdersByStartedAtAndID(t *testing.T) {
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

func TestRosterListDedupesSessionIDPreferringAppWireEntry(t *testing.T) {
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

func TestRoster_PrunesUnreachableDeadProcess(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	r := NewRoster(dir, fakeProber{shouldFail: true})
	r.procAlive = func(int) bool { return false } // process is gone → stale file
	r.Refresh()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("expected a dead daemon's stale rendezvous entry to be pruned, got %d", len(got))
	}
}

// TestRoster_KeepsAliveDaemonThroughProbeFailures is the regression test for the
// "flash of no sessions" bug: a live daemon that transiently fails its /status
// probe (busy daemon / overloaded host) must stay in the roster, not blank the
// sidebar.
func TestRoster_KeepsAliveDaemonThroughProbeFailures(t *testing.T) {
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
	for i := 0; i < 5; i++ {
		r.Refresh()
		if got := r.List(); len(got) != 1 {
			t.Fatalf("refresh %d: live daemon dropped on probe failure (flash), got %d entries", i, len(got))
		}
	}

	// When the process actually dies, the next failed probe prunes it.
	r.procAlive = func(int) bool { return false }
	r.Refresh()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("a dead daemon should be pruned, got %d entries", len(got))
	}
}

func TestRoster_FindMissing(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	if _, ok := r.Find("missing"); ok {
		t.Fatal("expected missing to return false")
	}
}

func TestRoster_DefaultRunDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	want := filepath.Join("/tmp/fakehome", ".serf", "run") //nolint:gocritic // filepathJoin: base is a full home path; mirrors rendezvous.DefaultDir
	if got := rendezvous.DefaultDir(); got != want {
		t.Fatalf("DefaultDir: got %q want %q", got, want)
	}
}

func TestRoster_Watch_PicksUpNewFile(t *testing.T) {
	dir := t.TempDir()
	r := NewRoster(dir, fakeProber{sessionID: "01SESS001"})

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
		if _, ok := r.Find("01SESS001"); ok {
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

func (p fakeProber) Probe(rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool) {
	if p.shouldFail {
		return "", "", false, false
	}
	return p.sessionID, p.status, p.pendingAsk, true
}

func TestRoster_CarriesPendingAskFromProber(t *testing.T) {
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

// gateProber blocks each probe on a channel, so a test can hold a Refresh in
// the middle of its probe pass and assert List() stays responsive.
type gateProber struct {
	sessionID string
	gate      chan struct{}
	started   chan struct{}
}

func (p *gateProber) Probe(rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.gate
	return p.sessionID, "", false, true
}

// TestRoster_ListStaysResponsiveDuringSlowProbe is the regression test for the
// startup/refresh hang: Refresh must probe without holding the roster lock, so
// List() returns the last good snapshot instead of blocking on a slow probe.
func TestRoster_ListStaysResponsiveDuringSlowProbe(t *testing.T) {
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

func (p *flakyProber) Probe(rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool) {
	if p.fail {
		return "", "", false, false
	}
	return p.sessionID, p.status, false, true
}

func TestPreferLiveEntry(t *testing.T) {
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

func TestProcessAlive(t *testing.T) {
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

func (p *statusProber) Probe(rendezvous.Entry) (sessionID, status string, pendingAsk, ok bool) {
	return p.sessionID, p.status, false, true
}

// TestRoster_OnStatusChangeFiresForTransitioningSession is the regression
// test for the tree-freshness fix: a session's Status changing between two
// consecutive Refresh snapshots must fire the per-session hook with that
// session's id, so the hub can re-read just that session's on-disk meta
// instead of waiting for the next full past-index rebuild.
func TestRoster_OnStatusChangeFiresForTransitioningSession(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})

	prober := &statusProber{sessionID: "01SESS", status: "working"}
	r := NewRoster(dir, prober)
	r.Refresh() // seed: no prior snapshot, so no transition to report

	var got []string
	r.SetOnStatusChange(func(sessionID string) { got = append(got, sessionID) })

	prober.status = "idle"
	r.Refresh()

	if len(got) != 1 || got[0] != "01SESS" {
		t.Fatalf("expected onStatusChange(01SESS) once, got %v", got)
	}
}

// TestRoster_OnStatusChangeNotFiredWhenStatusUnchanged pins the other half:
// a Refresh whose per-session status set is identical to the prior snapshot
// must not fire the hook, so a targeted re-read isn't triggered on every
// roster poll (only genuine transitions).
func TestRoster_OnStatusChangeNotFiredWhenStatusUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})

	prober := &statusProber{sessionID: "01SESS", status: "working"}
	r := NewRoster(dir, prober)
	r.Refresh()

	fired := false
	r.SetOnStatusChange(func(sessionID string) { fired = true })

	r.Refresh() // same status both times
	if fired {
		t.Fatal("onStatusChange fired for an unchanged status")
	}
}

func TestNewRosterWithEntries(t *testing.T) {
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
