package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// residentEntryForTest builds discovery input for one daemon: a minted session
// id, a fixture state dir, a loopback endpoint, the current protocol and a
// fixed start instant. It fabricates no ownership algorithm — identity is
// derived from the entry by the code under test.
func residentEntryForTest(t *testing.T, pid int) rendezvous.Entry {
	t.Helper()
	sessionID := hubtest.SessionID(t)
	return rendezvous.Entry{
		PID:          pid,
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws://127.0.0.1:1/rpc",
		SourceID:     "local",
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: "local:" + sessionID,
		WorkingDir:   t.TempDir(),
		StateDir:     t.TempDir(),
		StartedAt:    time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
	}
}

func residentLifecycleForTest() *appwire.DaemonLifecycle {
	return &appwire.DaemonLifecycle{Phase: "resident", TimeoutMillis: 0, Blockers: []appwire.DaemonBlocker{}}
}

func TestDaemonIdentityChangesWithReplacement(t *testing.T) {
	first := residentEntryForTest(t, 101)
	second := first
	second.PID = 102
	second.StartedAt = first.StartedAt.Add(time.Second)
	if daemonIdentity(first) == daemonIdentity(second) {
		t.Fatal("rendered target cannot distinguish replacement")
	}
	reused := first
	reused.StartedAt = first.StartedAt.Add(time.Second)
	if daemonIdentity(first) == daemonIdentity(reused) {
		t.Fatal("PID reuse cannot be distinguished")
	}
}

// TestDaemonActionRefusesStaleRenderedIdentity is the decisive Task 10 test:
// an action aimed at a row rendered before the daemon was replaced must never
// reach the replacement — no retire RPC, no signal, no recovery fence. The
// valid arm then proves a freshly rendered identity forwards retire verbatim
// and still runs the verified force-stop path with explicit ResumeRequired.
func TestDaemonActionRefusesStaleRenderedIdentity(t *testing.T) {
	runDir := t.TempDir()
	entryA := residentEntryForTest(t, 4101)
	writeRendezvous(t, runDir, entryA)
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", Lifecycle: residentLifecycleForTest(), LifecycleFresh: true}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()

	var events []string
	controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})
	locks := hubcore.NewResumeLocks()
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, Roster: roster, DaemonProcesses: controller, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	listRows := func() []appwire.DaemonResident {
		t.Helper()
		response, err := client.DaemonList(t.Context(), appwire.DaemonListParams{})
		if err != nil {
			t.Fatalf("DaemonList: %v", err)
		}
		return response.Daemons
	}

	rows := listRows()
	if len(rows) != 1 || rows[0].Identity.PID != entryA.PID {
		t.Fatalf("initial rows=%+v", rows)
	}
	staleIdentity := rows[0].Identity
	if staleIdentity.Generation != rendezvous.OwnershipFingerprint(entryA) {
		t.Fatalf("row generation is not the rendezvous ownership fingerprint: %+v", staleIdentity)
	}

	// Replace the daemon: the old record is removed and a new process with a
	// real retire endpoint claims the same session.
	if err := rendezvous.Remove(runDir, entryA.PID); err != nil {
		t.Fatal(err)
	}
	retireCalls := 0
	var gotIdentity appwire.DaemonIdentity
	daemonB := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemonB.Router(), appwire.MethodEvenerDaemonRetire, func(_ context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		retireCalls++
		gotIdentity = params.Identity
		return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: *residentLifecycleForTest()}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemonB.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entryB := entryA
	entryB.PID = 4102
	entryB.StartedAt = entryA.StartedAt.Add(time.Second)
	entryB.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	writeRendezvous(t, runDir, entryB)
	roster.Refresh()

	// Stale arm: refuse before any RPC, signal, or recovery fence.
	if _, err := client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: staleIdentity}); err != nil {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("stale retire err=%v, want conflict", err)
		}
	} else {
		t.Fatal("stale retire identity was accepted")
	}
	if retireCalls != 0 {
		t.Fatalf("stale retire reached the replacement daemon %d times", retireCalls)
	}
	var stopped appwire.EmptyResponse
	err := client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: staleIdentity.Ref, ExpectedDaemon: &staleIdentity}, &stopped)
	if err == nil {
		t.Fatal("stale force-stop identity was accepted")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("stale force stop err=%v, want conflict", err)
	}
	if len(events) != 0 {
		t.Fatalf("stale force stop touched the replacement process: %v", events)
	}
	if state := locks.RecoveryState(entryB.SessionID); state.Stopping != 0 || state.ResumeRequired {
		t.Fatalf("stale actions fenced replacement recovery: %+v", state)
	}

	// Valid arm: the freshly rendered identity is forwarded verbatim, and the
	// verified Kill/Wait path runs unchanged with explicit ResumeRequired.
	rows = listRows()
	if len(rows) != 1 || rows[0].Identity.PID != entryB.PID {
		t.Fatalf("replacement rows=%+v", rows)
	}
	validIdentity := rows[0].Identity
	if validIdentity.Generation != rendezvous.OwnershipFingerprint(entryB) {
		t.Fatalf("replacement row generation is not the ownership fingerprint: %+v", validIdentity)
	}
	retired, err := client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: validIdentity})
	if err != nil || !retired.Accepted {
		t.Fatalf("valid retire=%+v err=%v", retired, err)
	}
	if retireCalls != 1 || gotIdentity != validIdentity {
		t.Fatalf("retire forwarded identity=%+v calls=%d, want %+v exactly once", gotIdentity, retireCalls, validIdentity)
	}
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: validIdentity.Ref, ExpectedDaemon: &validIdentity}, &stopped); err != nil {
		t.Fatalf("valid force stop: %v", err)
	}
	if !reflect.DeepEqual(events, []string{"open", "kill", "wait", "close"}) {
		t.Fatalf("valid force stop events=%v", events)
	}
	if state := locks.RecoveryState(entryB.SessionID); !state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("confirmed stop lost the explicit resume requirement: %+v", state)
	}
}

// TestDaemonResidentInventoryRows pins the discovery half of the feature: one
// row per exact discovered process identity across archived, aliased,
// incompatible, stale, unconfirmed, and overlapping claims; dead markers are
// not residents; no token crosses the wire; polling never probes or opens.
func TestDaemonResidentInventoryRows(t *testing.T) {
	root := t.TempDir()
	runDir := t.TempDir()
	started := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	// (a) Archived live root with a saved display name.
	projectDir := hubtest.ProjectDir(t, filepath.Join(root, "projects"), "resident-archived")
	archivedSession := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(projectDir, schema.SessionMeta{ID: archivedSession, Name: "Archived resident", CreatedAt: started, UpdatedAt: started}); err != nil {
		t.Fatal(err)
	}
	archived := residentEntryForTest(t, 5101)
	archived.SessionID, archived.ThreadID, archived.WorkspaceRef = archivedSession, archivedSession, "local:"+archivedSession

	// (b) Compatible root reporting many in-process delegates (aliases that
	// must not become rows of their own).
	aliased := residentEntryForTest(t, 5102)

	// (c) Incompatible live process (older protocol).
	incompatible := residentEntryForTest(t, 5103)
	incompatible.Protocol = "evener-appwire-v3"

	// (d1) Confirmed current-protocol daemon whose lifecycle probe is stale.
	stale := residentEntryForTest(t, 5104)

	// (d2) Unconfirmed claim: live process, failed probe.
	unconfirmed := residentEntryForTest(t, 5105)

	// (e) Terminal dead marker: retained by the roster as crash evidence, but
	// not a resident.
	dead := residentEntryForTest(t, 5106)
	dead.StartedAt = time.Now() // recent enough to survive crash-marker GC

	// (f) Two live processes claiming overlapping session aliases: two rows,
	// one per exact identity.
	overlapSession := hubtest.SessionID(t)
	overlapFirst := residentEntryForTest(t, 5107)
	overlapFirst.SessionID, overlapFirst.ThreadID, overlapFirst.WorkspaceRef = overlapSession, overlapSession, "local:"+overlapSession
	overlapSecond := residentEntryForTest(t, 5108)
	overlapSecond.SessionID, overlapSecond.ThreadID, overlapSecond.WorkspaceRef = overlapSession, overlapSession, "local:"+overlapSession
	overlapSecond.StartedAt = started.Add(time.Second)

	entries := []rendezvous.Entry{archived, aliased, incompatible, stale, unconfirmed, dead, overlapFirst, overlapSecond}
	for i := range entries {
		entries[i].HubToken = "secret-canary-token"
		writeRendezvous(t, runDir, entries[i])
	}
	archived, aliased, incompatible, stale, unconfirmed, dead, overlapFirst, overlapSecond = entries[0], entries[1], entries[2], entries[3], entries[4], entries[5], entries[6], entries[7]

	var probes atomic.Int32
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		probes.Add(1)
		base := hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", Lifecycle: residentLifecycleForTest(), LifecycleFresh: true}
		switch e.PID {
		case aliased.PID:
			base.RunningSubagentIDs = []string{"child-1", "child-2", "child-3"}
		case incompatible.PID:
			base.Status = appwire.ThreadStatusRestartRequired
			base.Lifecycle, base.LifecycleFresh = nil, false
		case stale.PID:
			base.Lifecycle, base.LifecycleFresh = nil, false
		case unconfirmed.PID, dead.PID:
			return hubcore.ProbeResult{}
		}
		return base
	})
	alive := map[int]bool{unconfirmed.PID: true}
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(pid int) bool { return alive[pid] })
	roster.Refresh()

	dbPath := filepath.Join(root, "index.db")
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(dbPath)
	if err := archive.Set("session", archivedSession, true, time.Now()); err != nil {
		t.Fatal(err)
	}

	var events []string
	controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster, Past: past, Archive: archive, DaemonProcesses: controller, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}

	probesBefore := probes.Load()
	var response appwire.DaemonListResponse
	for i := 0; i < 2; i++ {
		var err error
		response, err = client.DaemonList(t.Context(), appwire.DaemonListParams{})
		if err != nil {
			t.Fatalf("DaemonList: %v", err)
		}
	}
	if probes.Load() != probesBefore {
		t.Fatalf("polling the list probed daemons: %d -> %d", probesBefore, probes.Load())
	}
	if len(events) != 0 {
		t.Fatalf("listing opened process handles: %v", events)
	}

	if response.DefaultTimeoutMillis != int64(time.Hour/time.Millisecond) {
		t.Fatalf("DefaultTimeoutMillis=%d, want the configured hub default", response.DefaultTimeoutMillis)
	}
	if response.Daemons == nil {
		t.Fatal("Daemons must be non-nil so an empty inventory decodes as empty")
	}
	byPID := make(map[int]appwire.DaemonResident, len(response.Daemons))
	for _, row := range response.Daemons {
		if _, dup := byPID[row.Identity.PID]; dup {
			t.Fatalf("duplicate row for PID %d", row.Identity.PID)
		}
		byPID[row.Identity.PID] = row
	}
	// (e) Dead confirmed-exited markers are not residents.
	if _, ok := byPID[dead.PID]; ok {
		t.Fatalf("dead marker rendered as a resident: %+v", byPID[dead.PID])
	}
	if len(response.Daemons) != 7 {
		t.Fatalf("rows=%d, want 7 (dead marker excluded): %+v", len(response.Daemons), response.Daemons)
	}

	assertIdentity := func(row appwire.DaemonResident, entry rendezvous.Entry) {
		t.Helper()
		if row.Identity.Generation != rendezvous.OwnershipFingerprint(entry) {
			t.Errorf("PID %d generation is not the rendezvous ownership fingerprint", entry.PID)
		}
		if row.Identity.Ref != "local:"+entry.SessionID || row.Identity.PID != entry.PID ||
			row.Identity.StartedAt != entry.StartedAt.UTC().Format(time.RFC3339Nano) {
			t.Errorf("PID %d identity=%+v, want the exact rendezvous identity", entry.PID, row.Identity)
		}
	}

	// (a) Archived stays visible and actionable; name comes from saved metadata.
	row := byPID[archived.PID]
	assertIdentity(row, archived)
	if !row.Archived || row.Name != "Archived resident" {
		t.Errorf("archived row archived=%v name=%q", row.Archived, row.Name)
	}
	if row.Compatibility != "compatible" || row.ProbeState != "current" || row.Lifecycle == nil || !row.CanRetire || !row.CanForceStop {
		t.Errorf("archived row=%+v", row)
	}
	if row.Lifecycle.Blockers == nil {
		t.Errorf("archived row lifecycle blockers must stay non-nil")
	}

	// (b) Many delegate aliases, exactly one row.
	row = byPID[aliased.PID]
	assertIdentity(row, aliased)
	if row.Archived || row.Compatibility != "compatible" || row.ProbeState != "current" || row.Lifecycle == nil || !row.CanRetire {
		t.Errorf("aliased row=%+v", row)
	}

	// (c) Incompatible live process: visible, never retirable.
	row = byPID[incompatible.PID]
	assertIdentity(row, incompatible)
	if row.Protocol != "evener-appwire-v3" || row.Compatibility != "incompatible" || row.ProbeState != "stale" ||
		row.Lifecycle != nil || row.CanRetire || !row.CanForceStop {
		t.Errorf("incompatible row=%+v", row)
	}

	// (d1) Stale lifecycle probe: confirmed but not retirable.
	row = byPID[stale.PID]
	if row.Compatibility != "compatible" || row.ProbeState != "stale" || row.Lifecycle != nil || row.CanRetire {
		t.Errorf("stale row=%+v", row)
	}

	// (d2) Unconfirmed claim stays visible with unknown status.
	row = byPID[unconfirmed.PID]
	assertIdentity(row, unconfirmed)
	if row.Compatibility != "unknown" || row.ProbeState != "unknown" || row.Lifecycle != nil || row.CanRetire || !row.CanForceStop {
		t.Errorf("unconfirmed row=%+v", row)
	}

	// (f) Overlapping alias claims: two rows ordered by start instant.
	firstIdx := slices.IndexFunc(response.Daemons, func(r appwire.DaemonResident) bool { return r.Identity.PID == overlapFirst.PID })
	secondIdx := slices.IndexFunc(response.Daemons, func(r appwire.DaemonResident) bool { return r.Identity.PID == overlapSecond.PID })
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatalf("overlapping claims did not produce one row each: %+v", response.Daemons)
	}
	overlapRow := response.Daemons[firstIdx]
	if overlapRow.Identity.Ref != response.Daemons[secondIdx].Identity.Ref {
		t.Fatalf("overlapping claims must share the session ref: %+v vs %+v", overlapRow.Identity, response.Daemons[secondIdx].Identity)
	}
	if overlapRow.Identity.Generation == response.Daemons[secondIdx].Identity.Generation {
		t.Fatal("overlapping processes rendered one indistinguishable identity")
	}
	if firstIdx > secondIdx {
		t.Fatal("same-ref rows are not ordered by start instant")
	}

	// Deterministic sort by ref, start, PID, generation.
	compareRows := func(a, b appwire.DaemonResident) int {
		if c := strings.Compare(a.Identity.Ref, b.Identity.Ref); c != 0 {
			return c
		}
		if c := strings.Compare(a.Identity.StartedAt, b.Identity.StartedAt); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Identity.PID, b.Identity.PID); c != 0 {
			return c
		}
		return strings.Compare(a.Identity.Generation, b.Identity.Generation)
	}
	if !slices.IsSortedFunc(response.Daemons, compareRows) {
		t.Fatalf("rows are not deterministically sorted: %+v", response.Daemons)
	}

	// No token crosses the wire; zero blockers stay distinct from unknown.
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-canary-token") {
		t.Fatal("serialized response carries the rendezvous hub token")
	}
	raw, err = json.Marshal(response.Daemons)
	if err != nil {
		t.Fatal(err)
	}
	var rawRows []map[string]any
	if err := json.Unmarshal(raw, &rawRows); err != nil {
		t.Fatal(err)
	}
	for i, row := range response.Daemons {
		identity, _ := rawRows[i]["identity"].(map[string]any)
		if _, leaked := identity["hubToken"]; leaked {
			t.Errorf("row %d identity carries a token field", i)
		}
		_, hasLifecycle := rawRows[i]["lifecycle"]
		if wantNil := row.Lifecycle == nil; hasLifecycle == wantNil {
			t.Errorf("row %d lifecycle presence=%v, want nil=%v", i, hasLifecycle, wantNil)
		}
	}
	if !strings.Contains(string(raw), `"blockers":[]`) {
		t.Error("zero blockers did not survive the round-trip as an empty array")
	}
}

// TestDaemonResidentEntriesCombineConfirmedAndUnconfirmed pins the roster
// snapshot shape the hub list consumes: confirmed entries first, unresolved
// claims visible with no confirmation, and crash evidence retained (the hub
// list, not the roster, decides dead markers are not residents).
func TestDaemonResidentEntriesCombineConfirmedAndUnconfirmed(t *testing.T) {
	runDir := t.TempDir()
	confirmed := residentEntryForTest(t, 4501)
	unresolved := residentEntryForTest(t, 4502)
	dead := residentEntryForTest(t, 4503)
	dead.StartedAt = time.Now()
	for _, e := range []rendezvous.Entry{confirmed, unresolved, dead} {
		writeRendezvous(t, runDir, e)
	}
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		if e.PID == confirmed.PID {
			return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", Lifecycle: residentLifecycleForTest(), LifecycleFresh: true}
		}
		return hubcore.ProbeResult{}
	})
	alive := map[int]bool{unresolved.PID: true}
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(pid int) bool { return alive[pid] })
	roster.Refresh()

	entries := roster.ResidentEntries()
	if len(entries) != 3 {
		t.Fatalf("ResidentEntries=%d, want confirmed + unconfirmed + crash evidence: %+v", len(entries), entries)
	}
	byPID := make(map[int]hubcore.ResidentEntry, len(entries))
	for _, e := range entries {
		byPID[e.Entry.PID] = e
	}
	if got := byPID[confirmed.PID]; got.Confirmed == nil || got.Confirmed.Crashed || got.Entry != confirmed {
		t.Errorf("confirmed entry=%+v", got)
	}
	if got := byPID[unresolved.PID]; got.Confirmed != nil || got.Entry != unresolved {
		t.Errorf("unconfirmed claim=%+v, want the raw claim with no confirmation", got)
	}
	if got := byPID[dead.PID]; got.Confirmed == nil || !got.Confirmed.Crashed {
		t.Errorf("dead marker=%+v, want retained crash evidence", got)
	}
	// Confirmed entries come before unresolved claims.
	if entries[0].Confirmed == nil {
		t.Errorf("confirmed entries must lead the snapshot: %+v", entries)
	}
}

// TestDaemonResidentEntriesCloneIsolation proves the snapshot never aliases
// roster state a caller can mutate.
func TestDaemonResidentEntriesCloneIsolation(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 4601)
	writeRendezvous(t, runDir, entry)
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", RunningSubagentIDs: []string{"child-1"},
			Lifecycle:      &appwire.DaemonLifecycle{Phase: "resident", Blockers: []appwire.DaemonBlocker{{Category: "question", SessionID: "s"}}},
			LifecycleFresh: true}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()

	first := roster.ResidentEntries()
	if len(first) != 1 || first[0].Confirmed == nil || first[0].Confirmed.Lifecycle == nil || len(first[0].Confirmed.Lifecycle.Blockers) != 1 {
		t.Fatalf("fixture snapshot=%+v", first)
	}
	first[0].Confirmed.Lifecycle.Blockers[0].Category = "MUTATED"
	first[0].Confirmed.RunningSubagentIDs[0] = "MUTATED"
	first[0].Entry.SessionID = "MUTATED"

	second := roster.ResidentEntries()
	if len(second) != 1 || second[0].Confirmed == nil {
		t.Fatalf("second snapshot=%+v", second)
	}
	if second[0].Confirmed.Lifecycle.Blockers[0].Category != "question" ||
		second[0].Confirmed.RunningSubagentIDs[0] != "child-1" ||
		second[0].Entry.SessionID == "MUTATED" {
		t.Fatalf("roster snapshot aliases caller-mutated state: %+v", second[0])
	}
}

// TestDaemonActionRetireReturnsFreshBlockers proves a stale UI eligibility
// hint (CanRetire from the last probe) never overrides the daemon's fresh
// refusal: blockers come back verbatim.
func TestDaemonActionRetireReturnsFreshBlockers(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 4201)
	retireCalls := 0
	var gotIdentity appwire.DaemonIdentity
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonRetire, func(_ context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		retireCalls++
		gotIdentity = params.Identity
		return appwire.DaemonRetireResponse{Accepted: false, Lifecycle: appwire.DaemonLifecycle{
			Phase: "resident", TimeoutMillis: 0,
			Blockers: []appwire.DaemonBlocker{{Category: "question", SessionID: entry.SessionID}},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	writeRendezvous(t, runDir, entry)
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", Lifecycle: residentLifecycleForTest(), LifecycleFresh: true}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	list, err := client.DaemonList(t.Context(), appwire.DaemonListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Daemons) != 1 || !list.Daemons[0].CanRetire {
		t.Fatalf("fixture row must render retirable from the fresh probe: %+v", list.Daemons)
	}
	retired, err := client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: list.Daemons[0].Identity})
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if retired.Accepted {
		t.Fatal("daemon refusal with fresh blockers was reported as accepted")
	}
	if len(retired.Lifecycle.Blockers) != 1 || retired.Lifecycle.Blockers[0].Category != "question" || retired.Lifecycle.Blockers[0].SessionID != entry.SessionID {
		t.Fatalf("fresh blockers did not pass through verbatim: %+v", retired.Lifecycle)
	}
	if retireCalls != 1 || gotIdentity != list.Daemons[0].Identity {
		t.Fatalf("retire forwarded identity=%+v calls=%d", gotIdentity, retireCalls)
	}
}

// TestDaemonActionRetireRefusesIncompatiblePeer proves there is no automatic
// fallback against a daemon that cannot speak the current protocol: the hub
// refuses before dialing, and the peer never sees an RPC.
func TestDaemonActionRetireRefusesIncompatiblePeer(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 4301)
	entry.Protocol = "evener-appwire-v3"
	retireCalls := 0
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonRetire, func(_ context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		retireCalls++
		return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: *residentLifecycleForTest()}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	writeRendezvous(t, runDir, entry)
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: appwire.ThreadStatusRestartRequired}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	list, err := client.DaemonList(t.Context(), appwire.DaemonListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Daemons) != 1 || list.Daemons[0].Compatibility != "incompatible" || list.Daemons[0].CanRetire {
		t.Fatalf("fixture row=%+v", list.Daemons)
	}
	_, err = client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: list.Daemons[0].Identity})
	if err == nil {
		t.Fatal("retire against an incompatible peer was accepted")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("incompatible retire err=%v, want unavailable", err)
	}
	if retireCalls != 0 {
		t.Fatalf("incompatible peer received %d retire RPCs", retireCalls)
	}
}

// TestDaemonActionDiscoveryReadFailure proves an incomplete ownership read
// fails closed: neither retire nor an identity-fenced force stop may act on
// discovery the hub could not fully verify.
func TestDaemonActionDiscoveryReadFailure(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 4401)
	retireCalls := 0
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonRetire, func(_ context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		retireCalls++
		return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: *residentLifecycleForTest()}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	writeRendezvous(t, runDir, entry)
	// A pid-named but undecodable rendezvous file fails the strict read.
	if err := os.WriteFile(filepath.Join(runDir, "9999.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rendezvous.ListStrict(runDir); err == nil {
		t.Fatal("fixture discovery did not fail")
	}
	locks := hubcore.NewResumeLocks()
	var events []string
	controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: controller, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	identity := daemonIdentity(entry)
	if _, err := client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: identity}); err != nil {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("retire over failed discovery err=%v, want unavailable", err)
		}
	} else {
		t.Fatal("retire acted on an incomplete ownership read")
	}
	if retireCalls != 0 {
		t.Fatalf("retire reached the daemon over failed discovery %d times", retireCalls)
	}
	var stopped appwire.EmptyResponse
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: identity.Ref, ExpectedDaemon: &identity}, &stopped); err == nil {
		t.Fatal("force stop acted on an incomplete ownership read")
	}
	if len(events) != 0 {
		t.Fatalf("force stop touched the process over failed discovery: %v", events)
	}
	if state := locks.RecoveryState(entry.SessionID); state.Stopping != 0 || state.ResumeRequired {
		t.Fatalf("failed discovery left a recovery fence: %+v", state)
	}
}
