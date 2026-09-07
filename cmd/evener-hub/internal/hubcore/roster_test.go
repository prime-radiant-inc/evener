package hubcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
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
		StartedAt: time.Now().UTC(), // fresh: within the crash-retention window
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
		StartedAt: time.Now().UTC(), // fresh: within the crash-retention window
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
		StartedAt: time.Now().UTC(), // fresh: within the crash-retention window
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

// fuzzScenarioRoster_GarbageCollectsStaleDeadRendezvousFiles pins the
// reclamation half of the crash-marker contract: a dead PID's rendezvous file
// is retained (as an "errored" entry, kata zm6s) only while its crash is
// fresh enough to matter. Once StartedAt is more than 24h in the past, or the
// file never resolved a session id at all (nothing to attribute a crash to),
// Refresh unlinks the file so dead-pid files stop accumulating forever. A
// live PID's file is never touched.
func fuzzScenarioRoster_GarbageCollectsStaleDeadRendezvousFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1001, Address: "127.0.0.1:50001", SessionID: "01OLDCRASH", StartedAt: now.Add(-25 * time.Hour),
	})
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1002, Address: "127.0.0.1:50002", SessionID: "01FRESHCRASH", StartedAt: now.Add(-time.Hour),
	})
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1003, Address: "127.0.0.1:50003", StartedAt: now.Add(-time.Minute), // no session id ever resolved
	})
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1004, Address: "127.0.0.1:50004", SessionID: "01LIVE", StartedAt: now.Add(-48 * time.Hour),
	})

	r := NewRoster(dir, fakeProber{shouldFail: true})
	r.procAlive = func(pid int) bool { return pid == 1004 }
	r.Refresh()

	fileExists := func(pid int) bool {
		_, err := os.Stat(filepath.Join(dir, strconv.Itoa(pid)+".json"))
		return err == nil
	}
	if fileExists(1001) {
		t.Fatal("dead pid with a >24h-old StartedAt: rendezvous file must be garbage-collected")
	}
	if _, ok := r.Find("01OLDCRASH"); ok {
		t.Fatal("dead pid with a >24h-old StartedAt: entry must be gone from the roster")
	}
	if !fileExists(1002) {
		t.Fatal("fresh crash: rendezvous file must be kept so the crash row survives a hub restart")
	}
	if got, ok := r.Find("01FRESHCRASH"); !ok || !got.Crashed || got.Status != "errored" {
		t.Fatalf("fresh crash must stay retained as errored: ok=%v entry=%+v", ok, got)
	}
	if fileExists(1003) {
		t.Fatal("dead pid with no session id: the file is pure garbage and must be removed regardless of age")
	}
	if !fileExists(1004) {
		t.Fatal("a live pid's rendezvous file must never be garbage-collected")
	}
}

func TestRosterGarbageCollectsStaleDeadRendezvousFiles(t *testing.T) {
	fuzzScenarioRoster_GarbageCollectsStaleDeadRendezvousFiles(t)
}

func fuzzScenarioRoster_FindMissing(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	if _, ok := r.Find("missing"); ok {
		t.Fatal("expected missing to return false")
	}
}

func fuzzScenarioRoster_DefaultRunDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	t.Setenv("XDG_STATE_HOME", "")
	want := filepath.Join("/tmp/fakehome", ".local", "state", "evener", "run") //nolint:gocritic // filepathJoin: base is a full home path; mirrors rendezvous.DefaultDir
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

// The roster carries each in-process child's own projected status beside its
// ID, so consumers can render a settled (idle) delegate without treating
// liveness as activity. SubagentState reports ("", true) for a live child
// whose daemon carried no state (old daemon), and ("", false) for a child no
// live parent owns. List/Find hand out defensive copies, like the IDs slice.
func fuzzScenarioRoster_CarriesRunningSubagentStates(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID:             "01PARENT",
		Status:                "idle",
		RunningSubagentIDs:    []string{"01IDLE", "01BUSY", "01NOSTATE"},
		RunningSubagentStates: map[string]string{"01IDLE": "idle", "01BUSY": "active"},
		OK:                    true,
	}}
	r := NewRoster(dir, prober)
	r.Refresh()

	if got, live := r.SubagentState("01IDLE"); !live || got != "idle" {
		t.Fatalf("SubagentState(01IDLE) = %q, %v, want idle, true", got, live)
	}
	if got, live := r.SubagentState("01BUSY"); !live || got != "active" {
		t.Fatalf("SubagentState(01BUSY) = %q, %v, want active, true", got, live)
	}
	if got, live := r.SubagentState("01NOSTATE"); !live || got != "" {
		t.Fatalf("SubagentState(01NOSTATE) = %q, %v, want empty, true (old-daemon fallback)", got, live)
	}
	if _, live := r.SubagentState("01GONE"); live {
		t.Fatal("SubagentState(01GONE) reported live for a child no parent owns")
	}

	entries := r.List()
	if entries[0].RunningSubagentStates["01IDLE"] != "idle" {
		t.Fatalf("List entry states = %v, want 01IDLE idle", entries[0].RunningSubagentStates)
	}
	entries[0].RunningSubagentStates["01IDLE"] = "mutated"
	if r.List()[0].RunningSubagentStates["01IDLE"] != "idle" {
		t.Fatal("List must return a defensive copy of running subagent states")
	}
	entry, ok := r.Find("01PARENT")
	if !ok || entry.RunningSubagentStates["01BUSY"] != "active" {
		t.Fatalf("Find entry states = %v, ok %v, want 01BUSY active", entry.RunningSubagentStates, ok)
	}
	entry.RunningSubagentStates["01BUSY"] = "mutated"
	if again, _ := r.Find("01PARENT"); again.RunningSubagentStates["01BUSY"] != "active" {
		t.Fatal("Find must return a defensive copy of running subagent states")
	}
}

func TestRosterRunningSubagentStates(t *testing.T) {
	fuzzScenarioRoster_CarriesRunningSubagentStates(t)
}

func TestRosterCarriesRunningJobsDefensively(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	resumable := true
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID: "01PARENT",
		Status:    "active",
		RunningJobs: []appwire.EvenerJobInfo{{
			JobID: "job_shell", JobType: "shell", Status: "running", Resumable: &resumable,
		}},
		OK: true,
	}}
	r := NewRoster(dir, prober)
	r.Refresh()

	prober.result.RunningJobs[0].Status = "mutated"
	*prober.result.RunningJobs[0].Resumable = false
	listed := r.List()
	if len(listed) != 1 || len(listed[0].RunningJobs) != 1 {
		t.Fatalf("roster entries = %+v, want one running shell job", listed)
	}
	job := listed[0].RunningJobs[0]
	if job.JobID != "job_shell" || job.JobType != "shell" || job.Status != "running" || job.Resumable == nil || !*job.Resumable {
		t.Fatalf("roster running job = %+v, want original identity and status", job)
	}

	listed[0].RunningJobs[0].Status = "changed"
	*listed[0].RunningJobs[0].Resumable = false
	found, ok := r.Find("01PARENT")
	if !ok || found.RunningJobs[0].Status != "running" || found.RunningJobs[0].Resumable == nil || !*found.RunningJobs[0].Resumable {
		t.Fatalf("List must return a defensive copy of running jobs; Find = %+v, ok %v", found.RunningJobs, ok)
	}
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

func TestRosterFingerprintIncludesRunningJobIdentityAndStatus(t *testing.T) {
	base := map[string]LiveEntry{"parent": {RunningJobs: []appwire.EvenerJobInfo{{JobID: "job_shell", JobType: "shell", Status: "running"}}}}
	statusChanged := map[string]LiveEntry{"parent": {RunningJobs: []appwire.EvenerJobInfo{{JobID: "job_shell", JobType: "shell", Status: "awaiting"}}}}
	identityChanged := map[string]LiveEntry{"parent": {RunningJobs: []appwire.EvenerJobInfo{{JobID: "job_watch", JobType: "watch", Status: "running"}}}}
	if rosterFingerprint(base) == rosterFingerprint(statusChanged) {
		t.Fatal("roster fingerprint must change when a running job status changes")
	}
	if rosterFingerprint(base) == rosterFingerprint(identityChanged) {
		t.Fatal("roster fingerprint must change when running job identity changes")
	}
}

type overlappingRefreshProber struct {
	calls         atomic.Int32
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
}

func (p *overlappingRefreshProber) Probe(rendezvous.Entry) ProbeResult {
	switch p.calls.Add(1) {
	case 1:
		close(p.firstStarted)
		<-p.releaseFirst
		return ProbeResult{SessionID: "parent", Status: "old", RunningSubagentIDs: []string{"old-child"}, OK: true}
	case 2:
		close(p.secondStarted)
		if p.releaseSecond != nil {
			<-p.releaseSecond
		}
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
	<-newDone
	close(prober.releaseFirst)
	<-oldDone

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
// full tree-freshness fix end to end, mirroring how cmd/evener-hub/main.go
// wires the pieces together: a session's status transition (as detected by
// Roster.Refresh) drives PastIndex.RefreshOne, which re-reads the session's
// on-disk meta and, on a genuine content delta, bumps the shared
// InputsVersion counter the navigation memo keys on. Before this fix, that
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
		LiveEntry{PID: 1, Address: "127.0.0.1:1", SessionID: "01A"},
		LiveEntry{PID: 2, Address: "127.0.0.1:2", SessionID: "01B"},
		LiveEntry{PID: 3, Address: "127.0.0.1:3", SessionID: ""},
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

func TestRosterOwnershipRefreshPublishesDespiteLaterProbe(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001})
	synctest.Test(t, func(t *testing.T) {
		prober := &overlappingRefreshProber{firstStarted: make(chan struct{}), secondStarted: make(chan struct{}), releaseFirst: make(chan struct{}), releaseSecond: make(chan struct{})}
		r := NewRoster(dir, prober)
		ownerDone := make(chan struct{})
		go func() {
			if err := r.RefreshAndWait(context.Background()); err != nil {
				t.Error(err)
			}
			close(ownerDone)
		}()
		<-prober.firstStarted
		backgroundDone := make(chan struct{})
		go func() { r.Refresh(); close(backgroundDone) }()
		<-prober.secondStarted
		close(prober.releaseFirst)
		synctest.Wait()
		select {
		case <-ownerDone:
		default:
			t.Error("ownership check waited for a later probe instead of publishing its own scan")
		}
		close(prober.releaseSecond)
		<-ownerDone
		<-backgroundDone
		entry, ok := r.Find("parent")
		if !ok || entry.Status != "new" {
			t.Fatalf("ownership snapshot=%+v, found=%v", entry, ok)
		}
	})
}

type ownershipBatchProber struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *ownershipBatchProber) Probe(entry rendezvous.Entry) ProbeResult {
	if p.calls.Add(1) == 1 {
		close(p.started)
	}
	<-p.release
	return ProbeResult{SessionID: entry.ThreadID, Status: appwire.ThreadStatusIdle, OK: true}
}

func TestRosterOwnershipRefreshesCoalesceWithoutLosingFreshness(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, ThreadID: "old"})
	synctest.Test(t, func(t *testing.T) {
		prober := &ownershipBatchProber{started: make(chan struct{}), release: make(chan struct{})}
		roster := NewRoster(dir, prober)
		done := make(chan struct{}, 17)
		go func() {
			if err := roster.RefreshAndWait(context.Background()); err != nil {
				t.Error(err)
			}
			done <- struct{}{}
		}()
		<-prober.started
		writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, ThreadID: "new"})
		for range 16 {
			go func() {
				if err := roster.RefreshAndWait(context.Background()); err != nil {
					t.Error(err)
				}
				done <- struct{}{}
			}()
		}
		synctest.Wait()
		if got := prober.calls.Load(); got != 1 {
			t.Errorf("started %d probes while the first was still running, want one", got)
		}
		close(prober.release)
		for range 17 {
			<-done
		}
		if got := prober.calls.Load(); got != 2 {
			t.Errorf("probe passes=%d, want the active pass and one fresh coalesced pass", got)
		}
		if _, ok := roster.Find("new"); !ok {
			t.Error("waiting callers did not receive a fresh rendezvous snapshot")
		}
	})
}

func TestRosterOwnershipRefreshCancellation(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, ThreadID: "owner"})
	synctest.Test(t, func(t *testing.T) {
		prober := &ownershipBatchProber{started: make(chan struct{}), release: make(chan struct{})}
		roster := NewRoster(dir, prober)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- roster.RefreshAndWait(ctx) }()
		<-prober.started
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("refresh error=%v, want cancellation", err)
			}
		default:
			t.Error("canceled caller is still waiting for the probe")
		}
		close(prober.release)
		synctest.Wait()
	})
}

func TestRosterOwnershipRefreshReportsReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	roster := NewRoster(path, nil)
	if err := roster.RefreshAndWait(context.Background()); err == nil {
		t.Fatal("ownership refresh succeeded without reading the rendezvous directory")
	}
}

func TestRosterUnconfirmedOwnershipClearsOnProbeOrExit(t *testing.T) {
	for _, resolvesByProbe := range []bool{false, true} {
		t.Run(strconv.FormatBool(resolvesByProbe), func(t *testing.T) {
			dir := t.TempDir()
			writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, SessionID: "owner", Protocol: "evener-appwire-v3"})
			roster := NewRoster(dir, fakeProber{shouldFail: true})
			alive := true
			roster.procAlive = func(int) bool { return alive }
			roster.Refresh()
			claims := roster.UnconfirmedEntries()
			if len(claims) != 1 || claims[0].SessionID != "owner" {
				t.Fatalf("claims=%+v", claims)
			}
			if len(roster.List()) != 0 {
				t.Fatal("unconfirmed process published as live daemon")
			}
			claims[0].SessionID = "modified"
			if roster.UnconfirmedEntries()[0].SessionID != "owner" {
				t.Fatal("caller modified roster ownership")
			}
			if resolvesByProbe {
				roster.prober = fakeProber{sessionID: "owner", status: appwire.ThreadStatusRestartRequired}
			} else {
				alive = false
			}
			roster.Refresh()
			if len(roster.UnconfirmedEntries()) != 0 {
				t.Fatal("resolved ownership remained unconfirmed")
			}
		})
	}
}

func TestRosterUnconfirmedOwnershipInvalidatesNavigation(t *testing.T) {
	dir := t.TempDir()
	roster := NewRoster(dir, fakeProber{shouldFail: true})
	roster.procAlive = func(int) bool { return true }
	roster.Refresh()
	changes := 0
	roster.SetOnChange(func() { changes++ })
	entry := rendezvous.Entry{PID: 1001, SessionID: "owner"}
	writeRendezvous(t, dir, entry)
	roster.Refresh()
	if changes != 1 {
		t.Fatalf("new claim callbacks=%d, want 1", changes)
	}
	roster.Refresh()
	if changes != 1 {
		t.Fatal("unchanged claim invalidated navigation")
	}
	entry.WorkspaceRef = "local:workspace"
	writeRendezvous(t, dir, entry)
	roster.Refresh()
	if changes != 2 {
		t.Fatalf("changed identity callbacks=%d, want 2", changes)
	}
	roster.Refresh()
	if changes != 2 {
		t.Fatal("unchanged identity invalidated navigation")
	}
	if err := rendezvous.Remove(dir, entry.PID); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if changes != 3 {
		t.Fatalf("removed claim callbacks=%d, want 3", changes)
	}
}

func TestRosterRefreshEntryPreservesOtherOwnership(t *testing.T) {
	otherEntry := rendezvous.Entry{PID: 1001}
	roster := NewRosterWithEntries(LiveEntry{Entry: otherEntry, SessionID: "other", Status: "active"})
	roster.runDir = t.TempDir()
	roster.prober = fakeProber{sessionID: "resumed", status: "idle"}
	roster.unconfirmed = []rendezvous.Entry{{PID: 1002, SessionID: "resumed"}, {PID: 1003, SessionID: "uncertain"}}
	if err := os.WriteFile(filepath.Join(roster.runDir, "1.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := roster.RefreshAndWait(t.Context()); err == nil {
		t.Fatal("fixture did not fail discovery")
	}
	entry := rendezvous.Entry{PID: 1002, SessionID: "resumed", ThreadID: "resumed", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	if err := roster.RefreshEntry(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if live, ok := roster.Find("resumed"); !ok || live.PID != 1002 || live.Status != "idle" {
		t.Fatalf("resumed=%+v, %v", live, ok)
	}
	if other, ok := roster.Find("other"); !ok || other.Status != "active" {
		t.Fatal("other owner changed")
	}
	if claims := roster.UnconfirmedEntries(); len(claims) != 1 || claims[0].SessionID != "uncertain" {
		t.Fatalf("claims=%+v", claims)
	}
	roster.prober = fakeProber{shouldFail: true}
	if err := roster.RefreshEntry(t.Context(), entry); err == nil {
		t.Fatal("unconfirmed entry accepted")
	}
	if len(roster.List()) != 2 {
		t.Fatal("failed confirmation changed roster")
	}
}

func TestRosterRefreshEntryDoesNotOverwriteNewerRefresh(t *testing.T) {
	for _, fullScan := range []bool{false, true} {
		t.Run(strconv.FormatBool(fullScan), func(t *testing.T) {
			dir := t.TempDir()
			entry := rendezvous.Entry{PID: 1001, SessionID: "parent", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
			writeRendezvous(t, dir, entry)
			prober := &overlappingRefreshProber{firstStarted: make(chan struct{}), secondStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
			roster := NewRoster(dir, prober)
			done := make(chan error, 1)
			go func() { done <- roster.RefreshEntry(t.Context(), entry) }()
			<-prober.firstStarted
			if fullScan {
				roster.Refresh()
			} else if err := roster.RefreshEntry(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			close(prober.releaseFirst)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if live, ok := roster.Find("parent"); !ok || live.Status != "new" {
				t.Fatalf("stale confirmation replaced newer snapshot: %+v", live)
			}

		})
	}
}

func TestRosterRefreshEntryCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		prober := &gateProber{sessionID: "parent", gate: make(chan struct{}), started: make(chan struct{}, 1)}
		roster := NewRoster(t.TempDir(), prober)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- roster.RefreshEntry(ctx, rendezvous.Entry{PID: 1001, Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"})
		}()
		<-prober.started
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
		default:
			t.Fatal("cancellation waited for probe")
		}
		close(prober.gate)
		synctest.Wait()
		if len(roster.List()) != 0 {
			t.Fatal("cancelled confirmation published")
		}
	})
}

type entryConfirmationProber struct {
	started chan struct{}
	release chan struct{}
}

func (p *entryConfirmationProber) Probe(entry rendezvous.Entry) ProbeResult {
	if entry.PID == 1001 {
		close(p.started)
		<-p.release
	}
	return ProbeResult{OK: true, SessionID: entry.SessionID, Status: "idle"}
}

func TestRosterConcurrentConfirmationPreservesEveryRoute(t *testing.T) {
	for _, fullScan := range []bool{false, true} {
		t.Run(strconv.FormatBool(fullScan), func(t *testing.T) {
			dir := t.TempDir()
			first := rendezvous.Entry{PID: 1001, SessionID: "first", Protocol: appwire.ProtocolVersion, Endpoint: "ws://first/rpc"}
			second := rendezvous.Entry{PID: 1002, SessionID: "second", Protocol: appwire.ProtocolVersion, Endpoint: "ws://second/rpc"}
			writeRendezvous(t, dir, first)
			prober := &entryConfirmationProber{started: make(chan struct{}), release: make(chan struct{})}
			roster := NewRoster(dir, prober)
			done := make(chan error, 1)
			go func() {
				if fullScan {
					done <- roster.RefreshAndWait(t.Context())
				} else {
					done <- roster.RefreshEntry(t.Context(), first)
				}
			}()
			<-prober.started
			writeRendezvous(t, dir, second)
			if err := roster.RefreshEntry(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			close(prober.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"first", "second"} {
				if _, ok := roster.Find(id); !ok {
					t.Errorf("confirmed session %s lost its route", id)
				}
			}
		})
	}
}
