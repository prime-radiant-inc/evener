package hubcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	r.SetProcessAlive(func(int) bool { return false }) // process is gone → stale file
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
	r.SetProcessAlive(func(int) bool { return true }) // process starts out alive
	r.Refresh()
	if live, ok := r.Find("01CRASHED"); !ok || live.Crashed {
		t.Fatalf("reachable entry = %+v, want present and not crashed", live)
	}

	// kill -9: the probe now fails AND the process is confirmed gone.
	prober.fail = true
	r.SetProcessAlive(func(int) bool { return false })
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
	r.SetProcessAlive(func(int) bool { return false }) // never seen alive by THIS roster
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
	r.SetProcessAlive(func(int) bool { return true }) // PID was reused by an unrelated process.

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
	r.SetProcessAlive(func(int) bool { return true }) // process stays alive throughout
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
	r.SetProcessAlive(func(int) bool { return false })
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
	r.SetProcessAlive(func(pid int) bool { return pid == 1004 })
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

// A crash-retained entry keeps the running-subagent list its daemon reported
// before it died (Refresh copies the previous richer snapshot onto the crashed
// record). That daemon is gone, so none of those children is running in any
// process: subagent activity must not read a dead parent's last word as
// liveness, or a stopped persisted delegate stays daemon-owned for the whole
// crash-retention window.
func TestRosterCrashedParentDoesNotOwnItsChildren(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:       1001,
		Address:   "127.0.0.1:50001",
		SessionID: "01PARENT",
		StartedAt: time.Now().UTC(), // fresh: within the crash-retention window
	})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID:             "01PARENT",
		Status:                "active",
		RunningSubagentIDs:    []string{"01CHILD"},
		RunningSubagentStates: map[string]string{"01CHILD": "active"},
		OK:                    true,
	}}
	r := NewRoster(dir, prober)
	r.SetProcessAlive(func(int) bool { return true })
	r.Refresh()
	if state, live := r.SubagentState("01CHILD"); !live || state != "active" {
		t.Fatalf("SubagentState(01CHILD) = %q, %v while the parent daemon is alive, want active, true", state, live)
	}

	// kill -9 the parent: its probe fails and the process is confirmed gone.
	prober.result = ProbeResult{}
	r.SetProcessAlive(func(int) bool { return false })
	r.Refresh()

	parent, ok := r.Find("01PARENT")
	if !ok || !parent.Crashed {
		t.Fatalf("parent entry = %+v, ok=%v, want a retained crashed record", parent, ok)
	}
	if !slices.Contains(parent.RunningSubagentIDs, "01CHILD") {
		t.Fatalf("crash retention dropped the child list (%v); this test no longer covers the case it names", parent.RunningSubagentIDs)
	}
	if state, live := r.SubagentState("01CHILD"); live || state != "" {
		t.Fatalf("SubagentState(01CHILD) = %q, %v after the parent crashed, want \"\", false", state, live)
	}
	if r.IsSubagentActive("01CHILD") {
		t.Fatal("a crashed parent's retained child list still reported the child as daemon-owned")
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

// A daemon that raises a recovery flag while staying idle changes what the hub
// may offer for that session — the fork capability is projected from these
// flags — so the fingerprint has to move, or onChange never invalidates
// navigation and clients keep an action the fork RPC would refuse. Order is not
// part of the signal: a daemon that reports the same flags in a different order
// has not changed anything.
func TestRosterFingerprintIncludesStatusFlagsRegardlessOfOrder(t *testing.T) {
	base := map[string]LiveEntry{"parent": {Status: "idle"}}
	flagged := map[string]LiveEntry{"parent": {Status: "idle", ActiveFlags: []string{"resumeRequired"}}}
	if rosterFingerprint(base) == rosterFingerprint(flagged) {
		t.Fatal("roster fingerprint must change when a daemon raises a status flag without changing status")
	}
	two := map[string]LiveEntry{"parent": {Status: "idle", ActiveFlags: []string{"resumeRequired", "compacting"}}}
	reordered := map[string]LiveEntry{"parent": {Status: "idle", ActiveFlags: []string{"compacting", "resumeRequired"}}}
	if rosterFingerprint(two) != rosterFingerprint(reordered) {
		t.Fatal("roster fingerprint must not change when a daemon reports the same status flags in another order")
	}
	if rosterFingerprint(two) == rosterFingerprint(flagged) {
		t.Fatal("roster fingerprint must change when a status flag is added")
	}
	if got := two["parent"].ActiveFlags; !slices.Equal(got, []string{"resumeRequired", "compacting"}) {
		t.Fatalf("fingerprinting reordered its caller's flags in place: %v", got)
	}
}

// An unanswered ask or a blocked escalation arriving or resolving changes
// what the hub may offer for the session — the list-row fallback folds both
// flags out of Clear, matching the daemon's clear gate — so each must move
// the fingerprint on its own, or onChange never invalidates and clients keep
// a clear affordance the RPC would refuse (or lose one it would accept).
func TestRosterFingerprintIncludesPendingAskAndPendingEscalation(t *testing.T) {
	base := map[string]LiveEntry{"parent": {Status: "idle"}}
	ask := map[string]LiveEntry{"parent": {Status: "idle", PendingAsk: true}}
	escalated := map[string]LiveEntry{"parent": {Status: "idle", PendingEscalation: true}}
	if rosterFingerprint(base) == rosterFingerprint(ask) {
		t.Fatal("roster fingerprint must change when an unanswered ask appears without a status change")
	}
	if rosterFingerprint(base) == rosterFingerprint(escalated) {
		t.Fatal("roster fingerprint must change when a blocked escalation appears without a status change")
	}
	if rosterFingerprint(ask) == rosterFingerprint(escalated) {
		t.Fatal("roster fingerprint must distinguish an ask from an escalation")
	}
}

// The observable half: an escalation arriving or resolving must fire
// onChange through a real Refresh — both transitions change the Clear bit
// list rows advertise, so a silent refresh leaves clients acting on the
// old answer. This also pins the probe-to-roster hop: the flag the prober
// reports must survive into the entry whose fingerprint Refresh hashes.
func TestRosterRefreshFiresOnChangeOnEscalationTransitions(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001"})
	prober := &runningSubagentProber{result: ProbeResult{
		SessionID: "01PARENT",
		Status:    "idle",
		OK:        true,
	}}
	r := NewRoster(dir, prober)
	r.Refresh()

	changes := 0
	r.SetOnChange(func() { changes++ })
	r.Refresh()
	if changes != 0 {
		t.Fatalf("no-op refresh fired onChange %d times", changes)
	}

	prober.result.PendingEscalation = true
	r.Refresh()
	if changes != 1 {
		t.Fatalf("escalation appearing fired onChange %d times, want 1", changes)
	}

	prober.result.PendingEscalation = false
	r.Refresh()
	if changes != 2 {
		t.Fatalf("escalation resolving fired onChange %d times total, want 2", changes)
	}
}

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

// DeliveryTimes feeds the activity panel's timeline. A delivery can change the
// ring without changing the count (the daemon-restore case rebuilds it empty),
// so it must move the fingerprint on its own or navigation never invalidates.
func TestRosterFingerprintIncludesWatchDeliveryTimes(t *testing.T) {
	watch := func(times []string) map[string]LiveEntry {
		return map[string]LiveEntry{"parent": {Watches: []appwire.EvenerWatchInfo{{
			ID: "watch-1", Source: "self", Deliveries: 2, DeliveryTimes: times,
			CreatedAt: "2026-09-12T10:00:00Z", Active: true,
		}}}}
	}
	base := watch([]string{"2026-09-12T10:00:01Z", "2026-09-12T10:00:02Z"})
	same := watch([]string{"2026-09-12T10:00:01Z", "2026-09-12T10:00:02Z"})
	changed := watch([]string{"2026-09-12T10:00:01Z", "2026-09-12T10:00:03Z"})
	emptied := watch(nil)
	if rosterFingerprint(base) != rosterFingerprint(same) {
		t.Fatal("roster fingerprint must not change when the delivery instants are identical")
	}
	if rosterFingerprint(base) == rosterFingerprint(changed) {
		t.Fatal("roster fingerprint must change when a delivery instant changes")
	}
	if rosterFingerprint(base) == rosterFingerprint(emptied) {
		t.Fatal("roster fingerprint must change when the delivery ring is rebuilt empty")
	}
}

// A listed child's own watches render on the child's row, so any change the
// sidebar shows there — a new watch, a delivery, a flip to inactive — must move
// the fingerprint or onChange never invalidates navigation and the child keeps a
// stale watch list.
func TestRosterFingerprintIncludesChildWatches(t *testing.T) {
	withChild := func(watches []appwire.EvenerWatchInfo) map[string]LiveEntry {
		return map[string]LiveEntry{"parent": {
			RunningSubagentIDs: []string{"child"},
			ChildWatches:       map[string][]appwire.EvenerWatchInfo{"child": watches},
		}}
	}
	base := withChild([]appwire.EvenerWatchInfo{{
		ID: "watch-child", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true, Deliveries: 1,
	}})
	same := withChild([]appwire.EvenerWatchInfo{{
		ID: "watch-child", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true, Deliveries: 1,
	}})
	renamed := withChild([]appwire.EvenerWatchInfo{{
		ID: "watch-renamed", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true, Deliveries: 1,
	}})
	disarmed := withChild([]appwire.EvenerWatchInfo{{
		ID: "watch-child", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: false, Deliveries: 1,
	}})
	emptied := withChild(nil)
	if rosterFingerprint(base) != rosterFingerprint(same) {
		t.Fatal("roster fingerprint must not change when a child's watches are identical")
	}
	if rosterFingerprint(base) == rosterFingerprint(renamed) {
		t.Fatal("roster fingerprint must change when a child's watch identity changes")
	}
	if rosterFingerprint(base) == rosterFingerprint(disarmed) {
		t.Fatal("roster fingerprint must change when a child's watch flips inactive")
	}
	if rosterFingerprint(base) == rosterFingerprint(emptied) {
		t.Fatal("roster fingerprint must change when a child's watches are emptied")
	}
}

// An event watch's every-Nth throttle and its filter change how often it fires,
// so changing either must move the fingerprint or the sidebar keeps a row whose
// cadence no longer matches the daemon. A daemon that reports the same watches
// in another order has not changed anything.
func TestRosterFingerprintIncludesEventCadenceEveryAndFilter(t *testing.T) {
	watch := func(cadence appwire.EvenerWatchCadence) map[string]LiveEntry {
		return map[string]LiveEntry{"parent": {Watches: []appwire.EvenerWatchInfo{{
			ID: "watch-1", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true,
			Cadence: []appwire.EvenerWatchCadence{cadence},
		}}}}
	}
	base := watch(appwire.EvenerWatchCadence{Kind: "events"})
	same := watch(appwire.EvenerWatchCadence{Kind: "events"})
	throttled := watch(appwire.EvenerWatchCadence{Kind: "events", Every: 3})
	filtered := watch(appwire.EvenerWatchCadence{Kind: "events", Filter: "status=error"})
	if rosterFingerprint(base) != rosterFingerprint(same) {
		t.Fatal("roster fingerprint must not change when the same cadence is reported")
	}
	if rosterFingerprint(base) == rosterFingerprint(throttled) {
		t.Fatal("roster fingerprint must change when only the event cadence every count changes")
	}
	if rosterFingerprint(base) == rosterFingerprint(filtered) {
		t.Fatal("roster fingerprint must change when only the event cadence filter changes")
	}

	a := appwire.EvenerWatchInfo{ID: "watch-a", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true}
	b := appwire.EvenerWatchInfo{ID: "watch-b", Source: "self", CreatedAt: "2026-09-12T10:00:00Z", Active: true}
	forward := map[string]LiveEntry{"parent": {Watches: []appwire.EvenerWatchInfo{a, b}}}
	reverse := map[string]LiveEntry{"parent": {Watches: []appwire.EvenerWatchInfo{b, a}}}
	if rosterFingerprint(forward) != rosterFingerprint(reverse) {
		t.Fatal("roster fingerprint must not change when a daemon lists the same watches in another order")
	}
}

type overlappingRefreshProber struct {
	calls         atomic.Int32
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
	failSecond    bool
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
		if p.failSecond {
			return ProbeResult{}
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
	r.SetProcessAlive(func(int) bool { return true })
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
			roster.SetProcessAlive(func(int) bool { return alive })
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
	roster.SetProcessAlive(func(int) bool { return true })
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

// A crash marker must not replace a surviving daemon's ownership or status.
func TestRosterLiveOwnerWinsOverCrashMarker(t *testing.T) {
	for _, livePID := range []int{1001, 1002} {
		t.Run(strconv.Itoa(livePID), func(t *testing.T) {
			dir := t.TempDir()
			for _, pid := range []int{1001, 1002} {
				protocol := appwire.ProtocolVersion
				if pid == livePID {
					protocol = "evener-appwire-v3"
				}
				writeRendezvous(t, dir, rendezvous.Entry{PID: pid, SessionID: "owner", ThreadID: "owner", Protocol: protocol, Endpoint: "ws://unused", StartedAt: time.Now().UTC()})
			}
			prober := &survivingOwnerProber{livePID: livePID}
			r := NewRoster(dir, prober)
			r.SetProcessAlive(func(pid int) bool { return pid == livePID })
			for _, failProbe := range []bool{false, true} {
				prober.fail = failProbe
				r.Refresh()
				got, ok := r.Find("owner")
				if !ok || got.Crashed || got.PID != livePID || got.Status != "restartRequired" {
					t.Errorf("Find after probe failure=%v: %+v, present=%v", failProbe, got, ok)
				}
				listed := r.List()
				if len(listed) != 1 || listed[0].Crashed || listed[0].PID != livePID {
					t.Errorf("List after probe failure=%v: %+v", failProbe, listed)
				}
			}
		})
	}
}

type survivingOwnerProber struct {
	livePID int
	fail    bool
}

// TestHasConfirmedEntryMissingPIDFailsFast pins the missing-PID branch: a PID
// absent from byPID must answer false without consulting the entry's
// SessionID. With the PID absent there is no SessionID to route by, and the
// lookup would otherwise index bySess at the zero-value key "" -- a key the
// roster's own scan never populates, but one a test can seed to prove the
// lookup does not depend on it. The boolean outcome is the same either way
// (the result is gated by `ok`), so this is a guard on the fail-fast
// contract, not a behavior change.
func TestHasConfirmedEntryMissingPIDFailsFast(t *testing.T) {
	roster := NewRosterWithEntries()
	// Seed the zero-value session key so a lookup that ignores the missing PID
	// has something to find there.
	seeded := LiveEntry{PID: 4242}
	roster.bySess[""] = seeded
	roster.byPID[4242] = seeded

	if roster.HasConfirmedEntry(rendezvous.Entry{PID: 9999}) {
		t.Fatal("a PID absent from byPID was confirmed through the empty session key")
	}
	// The same seeded maps still confirm a PID that IS present, so the guard
	// above cannot pass vacuously and the fail-fast branch leaves ok == true
	// untouched.
	if !roster.HasConfirmedEntry(roster.byPID[4242].Entry) {
		t.Fatal("a PID present in byPID with a matching route must still confirm")
	}
}

func (p *survivingOwnerProber) Probe(e rendezvous.Entry) ProbeResult {
	if e.PID != p.livePID || p.fail {
		return ProbeResult{}
	}
	return ProbeResult{SessionID: e.SessionID, Status: "restartRequired", OK: true}
}

// TestRosterIdentityUsesCanonicalOwnershipFingerprint is the regression test for
// the second hand-rolled identity comparison: the roster must decide exact
// daemon ownership through the same canonical fingerprint the daemon and hub
// enforce (rendezvous.OwnershipFingerprint), not a hand-picked field subset.
// The old roster copy ignored WorkingDir and StateDir (so a daemon that merely
// moved its working or state directory still confirmed as the same owner) while
// including HubToken, which the canonical fingerprint deliberately excludes.
// The assertions go through the public HasConfirmedEntry route, not the
// unexported helper.
func TestRosterIdentityUsesCanonicalOwnershipFingerprint(t *testing.T) {
	base := rendezvous.Entry{
		PID:          4242,
		Address:      "127.0.0.1:50001",
		Endpoint:     "ws://daemon/rpc",
		Protocol:     appwire.ProtocolVersion,
		SourceID:     "local",
		ThreadID:     "thread-1",
		SessionID:    "session-1",
		InstanceID:   "instance-1",
		WorkspaceRef: "local/thread-1",
		WorkingDir:   "/work/a",
		StateDir:     "/state/a",
		HubToken:     "token-a",
		StartedAt:    time.Unix(1700000000, 0).UTC(),
	}
	roster := NewRosterWithEntries(LiveEntry{Entry: base, SessionID: base.SessionID})
	if !roster.HasConfirmedEntry(base) {
		t.Fatal("identical entry did not confirm its own route")
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*rendezvous.Entry)
		confirm bool
	}{
		// Fields the hand-rolled copy omitted: an ownership change must no longer
		// confirm the stale route.
		{"WorkingDir", func(e *rendezvous.Entry) { e.WorkingDir = "/work/b" }, false},
		{"StateDir", func(e *rendezvous.Entry) { e.StateDir = "/state/b" }, false},
		// Field the hand-rolled copy wrongly included: the canonical fingerprint
		// treats it as outside exact ownership, so it must still confirm.
		{"HubToken", func(e *rendezvous.Entry) { e.HubToken = "token-b" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.mutate(&changed)
			if got := roster.HasConfirmedEntry(changed); got != tc.confirm {
				t.Fatalf("HasConfirmedEntry(%s changed)=%v, want %v", tc.name, got, tc.confirm)
			}
		})
	}
}

func TestRosterRefreshEntryDoesNotSucceedWithoutRouteAfterNewerMiss(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "parent", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	writeRendezvous(t, dir, entry)
	prober := &overlappingRefreshProber{firstStarted: make(chan struct{}), secondStarted: make(chan struct{}), releaseFirst: make(chan struct{}), failSecond: true}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	done := make(chan error, 1)
	go func() { done <- roster.RefreshEntry(t.Context(), entry) }()
	<-prober.firstStarted
	roster.Refresh()
	close(prober.releaseFirst)
	err := <-done
	live, ok := roster.Find("parent")
	if err == nil && (!ok || live.Crashed || live.PID != entry.PID) {
		t.Fatalf("confirmation returned success without a route: %+v, present=%v", live, ok)
	}
}

func TestRosterRetainsChangedClaimAfterProbeFailure(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "before", ThreadID: "before", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	writeRendezvous(t, dir, entry)
	prober := &flakyProber{sessionID: "before"}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	previous := entry
	prober.fail = true
	roster.Refresh()
	if !roster.HasConfirmedEntry(previous) {
		t.Fatal("unchanged identity lost its route during a transient probe failure")
	}
	entry.SessionID, entry.ThreadID = "after", "after"
	writeRendezvous(t, dir, entry)
	prober.fail = true
	for range 2 {
		roster.Refresh()
		if _, ok := roster.Find("before"); ok || roster.HasConfirmedEntry(previous) {
			t.Fatal("previous identity remains routable after its PID changed identity")
		}
		if len(roster.List()) != 0 {
			t.Fatal("unconfirmed replacement published a live route")
		}
		claims := roster.UnconfirmedEntries()
		if len(claims) != 2 || !slices.Contains(claims, previous) || !slices.Contains(claims, entry) {
			t.Fatalf("old and changed claims must remain unresolved: %+v", claims)
		}
	}
	prober.fail, prober.sessionID = false, "after"
	roster.Refresh()
	if _, ok := roster.Find("after"); !ok || !roster.HasConfirmedEntry(entry) {
		t.Fatal("confirmed replacement did not acquire its route")
	}
	if _, ok := roster.Find("before"); ok || len(roster.UnconfirmedEntries()) != 0 {
		t.Fatal("confirmed replacement retained previous ownership")
	}
}

func TestRosterOwnershipErrorRequiresNewerCompleteScan(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "parent", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	writeRendezvous(t, dir, entry)
	prober := &overlappingRefreshProber{firstStarted: make(chan struct{}), secondStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	roster := NewRoster(dir, prober)
	var changes atomic.Int32
	roster.SetOnChange(func() { changes.Add(1) })
	done := make(chan struct{})
	go func() { roster.Refresh(); close(done) }()
	<-prober.firstStarted
	badClaim := filepath.Join(dir, "2.json")
	if err := os.WriteFile(badClaim, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if roster.OwnershipError() == nil || changes.Load() != 1 {
		t.Fatal("failed discovery did not publish uncertainty")
	}
	close(prober.releaseFirst)
	<-done
	if roster.OwnershipError() == nil {
		t.Fatal("older successful scan cleared newer uncertainty")
	}
	if err := roster.RefreshEntry(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	if roster.OwnershipError() == nil {
		t.Fatal("individual confirmation cleared global uncertainty")
	}
	if err := os.Remove(badClaim); err != nil {
		t.Fatal(err)
	}
	before := changes.Load()
	roster.Refresh()
	if roster.OwnershipError() != nil || changes.Load() <= before {
		t.Fatal("complete scan did not publish recovered ownership")
	}
}

// ReadSpawnedThread publishes a freshly spawned daemon from the caller's own
// read rather than from a scan, so it is the one path into the roster that does
// not go through the prober. Everything the roster carries about a daemon's
// status has to survive it, the recovery flags included: the hub projects and
// enforces the fork capability from those flags, so a confirmation that dropped
// them would advertise fork for a daemon reporting resumeRequired until the
// next full scan.
func TestRosterReadSpawnedThreadPublishesStatusFlags(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	entry := rendezvous.Entry{
		PID: 1001, SourceID: "local", Protocol: appwire.ProtocolVersion,
		Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "01SPAWNED", SessionID: "01SPAWNED",
	}
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle, ActiveFlags: []string{"resumeRequired"}},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok {
		t.Fatal("the confirmed daemon was not published into the roster")
	}
	if live.Status != appwire.ThreadStatusIdle {
		t.Fatalf("published status = %q, want idle", live.Status)
	}
	if !slices.Contains(live.ActiveFlags, "resumeRequired") {
		t.Fatalf("published entry = %+v, want the status flags the daemon reported", live)
	}
	live.ActiveFlags[0] = "mutated"
	if again, _ := r.Find("01SPAWNED"); !slices.Contains(again.ActiveFlags, "resumeRequired") {
		t.Fatalf("Find must return a defensive copy of the status flags: %+v", again)
	}
}

// TestRosterReclaimsOnlyExactlyOwnedRendezvousEntries is the regression test
// for the PID-only removal defect. The crash-reclamation paths may delete a
// rendezvous file only while it still carries the exact identity the roster
// probed. A replacement daemon that rewrites <pid>.json during the probe window
// (PID reuse, or a hub respawn racing a slow exit) must keep its entry, while a
// genuinely dead stale file is still reclaimed.
func TestRosterReclaimsOnlyExactlyOwnedRendezvousEntries(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().UTC().Add(-25 * time.Hour)
	// PID 1001: stale and never resolved a session id -> the unlink path that
	// runs before the crash-retention window.
	writeRendezvous(t, dir, rendezvous.Entry{PID: 1001, Address: "127.0.0.1:50001", StartedAt: old})
	// PID 1002: stale with a resolved session id -> the crash-retention-expired
	// unlink path.
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1002, Address: "127.0.0.1:50002", SessionID: "01DEAD", ThreadID: "01DEAD", StartedAt: old,
	})
	// PID 1003: genuinely dead and stale -> must still be reclaimed.
	writeRendezvous(t, dir, rendezvous.Entry{
		PID: 1003, Address: "127.0.0.1:50003", SessionID: "01RECLAIM", ThreadID: "01RECLAIM", StartedAt: old,
	})

	// The entries a live replacement daemon publishes when it reuses each PID
	// with a new exact identity, racing the roster's read-then-unlink window.
	replacements := map[int]rendezvous.Entry{
		1001: {
			PID: 1001, Address: "127.0.0.1:50001", Protocol: appwire.ProtocolVersion,
			Endpoint: "ws://replacement/rpc", SourceID: "local",
			SessionID: "01NEW1", ThreadID: "01NEW1", StartedAt: time.Now().UTC(),
		},
		1002: {
			PID: 1002, Address: "127.0.0.1:50002", Protocol: appwire.ProtocolVersion,
			Endpoint: "ws://replacement/rpc", SourceID: "local",
			SessionID: "01NEW2", ThreadID: "01NEW2", StartedAt: time.Now().UTC(),
		},
	}
	prober := &rendezvousRewriteProber{dir: dir, replacements: replacements}
	r := NewRoster(dir, prober)
	r.SetProcessAlive(func(int) bool { return false })
	r.Refresh()

	if err := prober.firstError(); err != nil {
		t.Fatalf("replacement write: %v", err)
	}
	read := func(pid int) (rendezvous.Entry, bool) {
		t.Helper()
		entries, err := rendezvous.List(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.PID == pid {
				return e, true
			}
		}
		return rendezvous.Entry{}, false
	}
	for _, pid := range []int{1001, 1002} {
		got, ok := read(pid)
		if !ok {
			t.Fatalf("reclamation deleted the live replacement's rendezvous for PID %d", pid)
		}
		if got.SessionID != replacements[pid].SessionID {
			t.Fatalf("PID %d holds %q, want the replacement %q", pid, got.SessionID, replacements[pid].SessionID)
		}
	}
	if _, ok := read(1003); ok {
		t.Fatal("genuinely dead stale rendezvous file was not reclaimed")
	}
}

// rendezvousRewriteProber fails every probe but first rewrites the rendezvous
// file for selected PIDs, simulating a replacement daemon racing a slow exit.
type rendezvousRewriteProber struct {
	dir          string
	replacements map[int]rendezvous.Entry
	mu           sync.Mutex
	firstErr     error
}

func (p *rendezvousRewriteProber) Probe(e rendezvous.Entry) ProbeResult {
	if replacement, ok := p.replacements[e.PID]; ok {
		if _, err := rendezvous.Write(p.dir, replacement); err != nil {
			p.mu.Lock()
			if p.firstErr == nil {
				p.firstErr = err
			}
			p.mu.Unlock()
		}
	}
	return ProbeResult{}
}

func (p *rendezvousRewriteProber) firstError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firstErr
}

// A daemon that crashed leaves its rendezvous file, and the kernel may hand
// its PID to anything. To liveness that reuse looks exactly like a busy
// daemon missing one probe, so a confirmed entry stayed listed for as long as
// the unrelated process lived and the relay never announced the daemon gone
// (see ProcessIdentity). The process-identity probe tells the two apart;
// only the reused PID is dropped, and it reads as a crash.
func TestRosterDropsRetainedEntryWhoseProcessIsNoLongerItsDaemon(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "01REUSED", ThreadID: "01REUSED", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StartedAt: time.Now().UTC()}
	writeRendezvous(t, dir, entry)
	prober := &flakyProber{sessionID: "01REUSED"}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	roster.SetProcessIdentity(func(rendezvous.Entry) ProcessIdentity { return ProcessOwnsEntry })
	roster.Refresh()
	if !roster.HasConfirmedEntry(entry) {
		t.Fatal("confirmed entry did not acquire its route")
	}

	prober.fail = true
	roster.SetProcessIdentity(func(rendezvous.Entry) ProcessIdentity { return ProcessNotOwner })
	roster.Refresh()
	if roster.HasConfirmedEntry(entry) {
		t.Fatal("a PID that no longer belongs to the daemon kept the daemon's route")
	}
	live, ok := roster.Find("01REUSED")
	if !ok || !live.Crashed {
		t.Fatalf("the daemon behind a reused PID should read as crashed, got ok=%v entry=%+v", ok, live)
	}
}

// The other side of the same probe: a daemon that is alive and still itself,
// merely too busy to answer, keeps its route exactly as before, and so does a
// daemon on a host that cannot say either way.
func TestRosterRetainsBusyDaemonThatIsStillItself(t *testing.T) {
	for _, identity := range []ProcessIdentity{ProcessOwnsEntry, ProcessIdentityUnknown} {
		dir := t.TempDir()
		entry := rendezvous.Entry{PID: 1001, SessionID: "01BUSY", ThreadID: "01BUSY", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StartedAt: time.Now().UTC()}
		writeRendezvous(t, dir, entry)
		prober := &flakyProber{sessionID: "01BUSY"}
		roster := NewRoster(dir, prober)
		roster.SetProcessAlive(func(int) bool { return true })
		roster.SetProcessIdentity(func(rendezvous.Entry) ProcessIdentity { return identity })
		roster.Refresh()
		prober.fail = true
		for range 3 {
			roster.Refresh()
			if !roster.HasConfirmedEntry(entry) {
				t.Fatalf("identity %d: a busy daemon lost its route on a transient probe failure", identity)
			}
			if live, ok := roster.Find("01BUSY"); !ok || live.Crashed {
				t.Fatalf("identity %d: a busy daemon reads as crashed: ok=%v entry=%+v", identity, ok, live)
			}
		}
	}
}

// A roster built without a prober admits every entry as listed (offline or
// synthetic rosters); it must not then ask the host about a PID that is
// nobody's on this machine and mark a complete entry crashed.
func TestRosterWithoutProberDoesNotAskTheHostAboutAPID(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "01OFFLINE", ThreadID: "01OFFLINE", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc", StateDir: "/private/state", StartedAt: time.Now().UTC()}
	writeRendezvous(t, dir, entry)
	roster := NewRoster(dir, nil)
	roster.SetProcessIdentity(func(rendezvous.Entry) ProcessIdentity {
		t.Fatal("identity probe consulted by a prober-less roster")
		return ProcessNotOwner
	})
	roster.Refresh()
	// A prober-less roster keys nothing by a probed session id, so the
	// listing is the observable: one entry, live, not crashed.
	listed := roster.List()
	if len(listed) != 1 || listed[0].Crashed || listed[0].Entry.SessionID != "01OFFLINE" {
		t.Fatalf("prober-less roster did not list the entry as live: %+v", listed)
	}
}

// The identity probe is a real inspection of a process: never for an entry
// whose probe answered for its own session, once per entry per refresh
// otherwise (two calls in one pass could disagree with each other).
func TestRosterAsksTheProcessIdentityOncePerEntryPerRefresh(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "01ONCE", ThreadID: "01ONCE", Protocol: appwire.ThreadStatusIdle, Endpoint: "ws://daemon/rpc", StartedAt: time.Now().UTC()}
	entry.Protocol = appwire.ProtocolVersion
	writeRendezvous(t, dir, entry)
	prober := &flakyProber{sessionID: "01ONCE"}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	var calls atomic.Int32
	roster.SetProcessIdentity(func(rendezvous.Entry) ProcessIdentity {
		calls.Add(1)
		return ProcessOwnsEntry
	})
	for i, fail := range []bool{false, true, true} {
		prober.fail = fail
		calls.Store(0)
		roster.Refresh()
		want := int32(0)
		if fail {
			want = 1
		}
		if got := calls.Load(); got != want {
			t.Fatalf("refresh %d (probe fails=%v): identity probe called %d times, want %d", i, fail, got, want)
		}
		if !roster.HasConfirmedEntry(entry) {
			t.Fatalf("refresh %d: an owning daemon lost its route", i)
		}
	}
}

// Refresh announces a session gone once, and only when its daemon has left
// for good: crashed (process gone) or exited (file gone). A claim parked
// unresolved - a probe missed while the process answers - is not announced;
// that daemon may still be there.
func TestRosterAnnouncesASessionGoneOnceAndOnlyForGood(t *testing.T) {
	dir := t.TempDir()
	entry := rendezvous.Entry{PID: 1001, SessionID: "01GONE", ThreadID: "01GONE", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	writeRendezvous(t, dir, entry)
	prober := &flakyProber{sessionID: "01GONE"}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	var announced []string
	roster.SetOnSessionGone(func(gone LiveEntry) { announced = append(announced, gone.SessionID) })
	roster.Refresh()
	if len(announced) != 0 {
		t.Fatalf("a confirmed daemon was announced gone: %v", announced)
	}

	// Unresolved: the claim changes identity while a probe is missed.
	moved := entry
	moved.SessionID, moved.ThreadID = "01MOVED", "01MOVED"
	writeRendezvous(t, dir, moved)
	prober.fail = true
	roster.Refresh()
	if len(roster.UnconfirmedEntries()) == 0 || len(announced) != 0 {
		t.Fatalf("an unresolved claim was announced gone: announced=%v unconfirmed=%+v", announced, roster.UnconfirmedEntries())
	}

	// Confirmed again under its own identity: the moved claim that was parked
	// has vanished without confirming, and is announced as gone.
	writeRendezvous(t, dir, entry)
	prober.fail = false
	roster.Refresh()
	if len(announced) != 1 || announced[0] != "01MOVED" {
		t.Fatalf("the vanished unresolved claim should be the one announced, got %v", announced)
	}
	announced = nil

	// The process dies: the crashed path announces once.
	prober.fail = true
	roster.SetProcessAlive(func(int) bool { return false })
	roster.Refresh()
	roster.Refresh()
	if len(announced) != 1 || announced[0] != "01GONE" {
		t.Fatalf("crashed daemon announced %v, want once", announced)
	}
	announced = nil

	// Confirmed again (the crashed path removed the stale file; a daemon
	// writes a fresh one), then the file goes (a clean exit): announced once.
	writeRendezvous(t, dir, entry)
	roster.SetProcessAlive(func(int) bool { return true })
	prober.fail = false
	roster.Refresh()
	if err := os.Remove(filepath.Join(dir, "1001.json")); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if len(announced) != 1 || announced[0] != "01GONE" {
		t.Fatalf("exited daemon announced %v, want once", announced)
	}
	announced = nil

	// Unresolved, then the claim vanishes altogether: the session that was
	// parked is gone now, and is announced.
	writeRendezvous(t, dir, entry)
	roster.Refresh()
	writeRendezvous(t, dir, moved)
	prober.fail = true
	roster.Refresh()
	if len(announced) != 0 {
		t.Fatalf("an unresolved claim was announced gone: %v", announced)
	}
	if err := os.Remove(filepath.Join(dir, "1001.json")); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if !slices.Contains(announced, "01GONE") {
		t.Fatalf("a vanished unresolved claim was not announced gone: %v", announced)
	}
}

// heldProber blocks each probe of the gated session until released, so two
// refreshes can be held open together; every other session fails its probe.
// A probe reports itself on started with the channel that releases it, so
// the test decides which refresh a release reaches by the probe it saw
// start, not by which probe parked first.
type heldProber struct {
	gated   string
	started chan chan struct{}
}

func (p *heldProber) Probe(entry rendezvous.Entry) ProbeResult {
	if entry.SessionID != p.gated {
		return ProbeResult{}
	}
	release := make(chan struct{})
	p.started <- release
	<-release
	return ProbeResult{SessionID: p.gated, Status: appwire.ThreadStatusIdle, OK: true}
}

// Two refreshes that overlap each start from the same earlier snapshot; the
// departure of a session must be announced by whichever publishes first, and
// by the other not at all.
func TestRosterOverlappingRefreshesAnnounceADepartureOnce(t *testing.T) {
	dir := t.TempDir()
	parked := rendezvous.Entry{PID: 1001, SessionID: "01PARKED", ThreadID: "01PARKED", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	steady := rendezvous.Entry{PID: 1002, SessionID: "01STEADY", ThreadID: "01STEADY", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	writeRendezvous(t, dir, parked)
	writeRendezvous(t, dir, steady)
	prober := &heldProber{gated: "01STEADY", started: make(chan chan struct{}, 2)}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	var mu sync.Mutex
	var announced []string
	roster.SetOnSessionGone(func(gone LiveEntry) {
		mu.Lock()
		defer mu.Unlock()
		announced = append(announced, gone.SessionID)
	})
	// First publication: the steady daemon confirmed, the other parked
	// unresolved (its probe missed, its process answers).
	initial := make(chan struct{})
	go func() { roster.Refresh(); close(initial) }()
	close(<-prober.started)
	<-initial
	if len(roster.UnconfirmedEntries()) != 1 {
		t.Fatalf("unconfirmed = %+v, want the parked claim", roster.UnconfirmedEntries())
	}

	// The parked claim's file goes. Two refreshes start before either
	// publishes, then publish in order.
	if err := os.Remove(filepath.Join(dir, "1001.json")); err != nil {
		t.Fatal(err)
	}
	first, second := make(chan struct{}), make(chan struct{})
	go func() { roster.Refresh(); close(first) }()
	releaseFirst := <-prober.started
	go func() { roster.Refresh(); close(second) }()
	releaseSecond := <-prober.started
	close(releaseFirst)
	<-first
	close(releaseSecond)
	<-second
	mu.Lock()
	defer mu.Unlock()
	if len(announced) != 1 || announced[0] != "01PARKED" {
		t.Fatalf("overlapping refreshes announced %v, want the departure once", announced)
	}
}

// mismatchProber answers the way StatusProber does for an incompatible
// daemon: a restart-required entry, confirmed.
type mismatchProber struct{ sessionID string }

func (p mismatchProber) Probe(rendezvous.Entry) ProbeResult {
	return ProbeResult{SessionID: p.sessionID, Status: appwire.ThreadStatusRestartRequired, ProtocolMismatch: true, OK: true}
}

// A protocol-mismatch answer is not bound to the entry's session - the
// incompatible process never says which session it serves - so the process
// behind the PID is asked as for a failed probe: a stale file whose endpoint
// something incompatible re-bound is not listed as a daemon awaiting restart
// unless its process is (or may be) the daemon.
func TestRosterListsARestartRequiredEntryOnlyForItsOwnProcess(t *testing.T) {
	for _, tc := range []struct {
		identity ProcessIdentity
		listed   bool
	}{{ProcessOwnsEntry, true}, {ProcessIdentityUnknown, true}, {ProcessNotOwner, false}} {
		dir := t.TempDir()
		entry := rendezvous.Entry{PID: 1001, SessionID: "01OLD", ThreadID: "01OLD", Protocol: "evener-appwire-v1", Endpoint: "ws://daemon/rpc"}
		writeRendezvous(t, dir, entry)
		roster := NewRoster(dir, mismatchProber{sessionID: "01OLD"}).
			SetProcessIdentity(func(rendezvous.Entry) ProcessIdentity { return tc.identity })
		roster.Refresh()
		live, ok := roster.Find("01OLD")
		listed := ok && !live.Crashed && live.Status == appwire.ThreadStatusRestartRequired
		if listed != tc.listed {
			t.Fatalf("identity %d: listed as restart-required = %v (ok=%v entry=%+v), want %v", tc.identity, listed, ok, live, tc.listed)
		}
	}
}

// A spawned daemon's confirmation can replace a prior session on the same PID
// before any full refresh sees the swap; the old session's subscribers are
// owed the same departure announcement a refresh would give them, once.
func TestRosterConfirmedReplacementOnAPIDAnnouncesTheOldSessionGone(t *testing.T) {
	dir := t.TempDir()
	old := rendezvous.Entry{PID: 1001, SessionID: "01OLD", ThreadID: "01OLD", Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon/rpc"}
	writeRendezvous(t, dir, old)
	prober := &flakyProber{sessionID: "01OLD"}
	roster := NewRoster(dir, prober)
	roster.SetProcessAlive(func(int) bool { return true })
	var announced []string
	roster.SetOnSessionGone(func(gone LiveEntry) { announced = append(announced, gone.SessionID) })
	roster.Refresh()
	if _, ok := roster.Find("01OLD"); !ok {
		t.Fatal("the old session was not confirmed")
	}

	// The spawner writes the replacement's file for the same PID, and the
	// daemon behind it answers for the new session.
	replacement := old
	replacement.SessionID, replacement.ThreadID = "01NEW", "01NEW"
	writeRendezvous(t, dir, replacement)
	prober.sessionID = "01NEW"
	if _, err := roster.ReadSpawnedThread(context.Background(), replacement, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "01NEW", SessionID: "01NEW"}}, nil
	}); err != nil {
		t.Fatalf("confirm the replacement: %v", err)
	}
	if _, ok := roster.Find("01NEW"); !ok {
		t.Fatal("the replacement was not published")
	}
	if _, ok := roster.Find("01OLD"); ok {
		t.Fatal("the replaced session is still listed")
	}
	if len(announced) != 1 || announced[0] != "01OLD" {
		t.Fatalf("replacement announced %v, want the old session gone once", announced)
	}
	roster.Refresh()
	if len(announced) != 1 {
		t.Fatalf("the next refresh announced the departure again: %v", announced)
	}
}
