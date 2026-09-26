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

// TestListDaemonsRetireRequiresStrongOwnership pins round 7's Low finding: the
// hub must not offer "retire now" on a platform where the daemon refuses
// retirement outright because rendezvous strong ownership is unavailable
// (cmd/evener/serve.go refuses every retirement there). The capability is
// platform-constant, so retireOwnershipCapability — nil in production — is the
// seam that exercises both branches, plus the real platform default.
func TestListDaemonsRetireRequiresStrongOwnership(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 6101)
	writeRendezvous(t, runDir, entry)
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{
			OK: true, SessionID: e.SessionID, Status: "idle",
			Lifecycle: residentLifecycleForTest(), LifecycleFresh: true,
		}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster}

	restore := setRetireOwnershipCapability(nil)
	t.Cleanup(restore)

	for _, tc := range []struct {
		name      string
		available bool
		want      bool
	}{
		{name: "strong ownership unavailable", available: false, want: false},
		{name: "strong ownership available", available: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRetireOwnershipCapability(func() bool { return tc.available })
			list, err := listDaemons(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Daemons) != 1 {
				t.Fatalf("rows=%d, want 1", len(list.Daemons))
			}
			if got := list.Daemons[0].CanRetire; got != tc.want {
				t.Fatalf("CanRetire=%v with strong ownership available=%v, want %v", got, tc.available, tc.want)
			}
		})
	}

	// The unseamed production path must agree with the platform capability.
	t.Run("platform default", func(t *testing.T) {
		setRetireOwnershipCapability(nil)
		list, err := listDaemons(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Daemons) != 1 {
			t.Fatalf("rows=%d, want 1", len(list.Daemons))
		}
		if got, want := list.Daemons[0].CanRetire, rendezvous.StrongOwnershipAvailable(); got != want {
			t.Fatalf("CanRetire=%v on the production path, want the platform capability %v", got, want)
		}
	})
}

// TestListDaemonsRetireRequiresConfiguredPrerequisites pins the Low finding:
// listDaemons must advertise Retire only when the action it names can succeed.
// retireDaemon refuses outright when RunDir is empty or ResumeLocks is nil, so
// a resident inventory built without them must report CanRetire:false — even on
// a platform whose strong ownership contract is available. A fully configured
// inventory must still report CanRetire:true, so this is not a blanket disable.
func TestListDaemonsRetireRequiresConfiguredPrerequisites(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 6201)
	writeRendezvous(t, runDir, entry)
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{
			OK: true, SessionID: e.SessionID, Status: "idle",
			Lifecycle: residentLifecycleForTest(), LifecycleFresh: true,
		}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()

	restore := setRetireOwnershipCapability(func() bool { return true })
	t.Cleanup(restore)

	for _, tc := range []struct {
		name string
		cfg  hubcore.WebConfig
		want bool
	}{
		{
			name: "configured reports retire",
			cfg:  hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster},
			want: true,
		},
		{
			name: "missing run dir reports no retire",
			cfg:  hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks(), Roster: roster},
			want: false,
		},
		{
			name: "missing resume locks reports no retire",
			cfg:  hubcore.WebConfig{RunDir: runDir, Roster: roster},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := listDaemons(t.Context(), tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Daemons) != 1 {
				t.Fatalf("rows=%d, want 1", len(list.Daemons))
			}
			if got := list.Daemons[0].CanRetire; got != tc.want {
				t.Fatalf("CanRetire=%v, want %v", got, tc.want)
			}
			if !tc.want {
				// The inventory must agree with what the action itself answers:
				// retireDaemon refuses before touching any state here.
				if _, retireErr := retireDaemon(t.Context(), tc.cfg, nil, appwire.DaemonRetireParams{
					Identity: appwire.DaemonIdentity{Ref: localAppRef(entry.SessionID)},
				}); retireErr == nil {
					t.Fatal("retire answered success on a configuration the inventory disabled")
				}
			}
		})
	}
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

// TestListDaemonsStaleProbeOffersNoRetireAfterFailure is the end-to-end half of
// the stale-lifecycle regression: a failed probe that still matches the
// daemon's PID and rendezvous identity keeps the row visible, but the rendered
// resident must report a stale probe with no lifecycle and no retire action.
func TestListDaemonsStaleProbeOffersNoRetireAfterFailure(t *testing.T) {
	runDir := t.TempDir()
	entry := residentEntryForTest(t, 6001)
	writeRendezvous(t, runDir, entry)

	fail := false
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		if fail {
			return hubcore.ProbeResult{}
		}
		return hubcore.ProbeResult{
			OK: true, SessionID: e.SessionID, Status: "idle",
			Lifecycle: residentLifecycleForTest(), LifecycleFresh: true,
		}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()

	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster}
	list, err := listDaemons(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Daemons) != 1 {
		t.Fatalf("rows=%d, want 1", len(list.Daemons))
	}
	if list.Daemons[0].ProbeState != "current" || list.Daemons[0].Lifecycle == nil || !list.Daemons[0].CanRetire {
		t.Fatalf("fresh row not retirable: %+v", list.Daemons[0])
	}

	fail = true
	roster.Refresh()
	list, err = listDaemons(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Daemons) != 1 {
		t.Fatalf("stale row must stay visible, rows=%d", len(list.Daemons))
	}
	row := list.Daemons[0]
	if row.ProbeState != "stale" || row.Lifecycle != nil || row.CanRetire {
		t.Fatalf("failed probe still offered a current, retirable row: %+v", row)
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

// TestRetireDaemonRefusesDeletedSiblingAlias is the retire-path instance of
// the ownership-group deletion fence (the e68ec81fa class): retireDaemon locks
// every ownership alias but validated the deletion fence only for the requested
// ref and its thread id, so a deletion record naming a sibling alias — here
// the thread id of a resident whose rendered ref names the session id — was
// bypassed and safe retire reached the daemon on a deleted group.
func TestRetireDaemonRefusesDeletedSiblingAlias(t *testing.T) {
	runDir := t.TempDir()
	// One resident, two distinct ownership aliases: the rendered ref names the
	// session id; the sibling thread id carries the deletion record.
	session, thread := hubtest.SessionID(t), hubtest.SessionID(t)
	retireCalls := 0
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonRetire, func(_ context.Context, _ appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		retireCalls++
		return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: *residentLifecycleForTest()}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry := residentEntryForTest(t, 4201)
	entry.SessionID, entry.ThreadID, entry.WorkspaceRef = session, thread, "local:"+session
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	writeRendezvous(t, runDir, entry)
	identity := daemonIdentity(entry)
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(filepath.Base(hubtest.ProjectDir(t, t.TempDir(), "deleted")), []hubcore.DeletionTarget{{Ref: "local:" + thread, ThreadID: thread}}); err != nil {
		t.Fatal(err)
	}
	// The roster must keep the fixture resident: a probed, process-alive entry
	// survives crash-marker GC exactly as in the stale-identity retire test.
	prober := forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", Lifecycle: residentLifecycleForTest(), LifecycleFresh: true}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DeletionStore: store, Roster: roster, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	_, err = client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: identity})
	var wire appwire.WireError
	if err == nil || !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || !strings.Contains(err.Error(), "target has been deleted") {
		t.Fatalf("retire of a sibling-deleted daemon = %v, want the target-deleted refusal", err)
	}
	if retireCalls != 0 {
		t.Fatalf("safe retire reached the daemon %d times on a deleted ownership group", retireCalls)
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
	// Use a sub-second interval: overlapFirst is at an exact second boundary
	// (formats as "…T00:00:00Z", no fractional part) while overlapSecond is
	// 500ms later ("…T00:00:00.5Z"). RFC3339Nano trims trailing zeros, so
	// 'Z' (0x5A) > '.' (0x2E) and the lexicographic string comparison in the
	// old code produces the wrong order. The time-parsing oracle below is the
	// proof that catches this.
	overlapSecond.StartedAt = started.Add(500 * time.Millisecond)

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
	if err := archive.Set("", "session", archivedSession, true, time.Now()); err != nil {
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
	for range 2 {
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

	// (a) Archived stays visible with retirement disabled (docs/daemon-idle-
	// retirement.md); name comes from saved metadata.
	row := byPID[archived.PID]
	assertIdentity(row, archived)
	if !row.Archived || row.Name != "Archived resident" {
		t.Errorf("archived row archived=%v name=%q", row.Archived, row.Name)
	}
	if row.Compatibility != "compatible" || row.ProbeState != "current" || row.Lifecycle == nil || row.CanRetire || !row.CanForceStop {
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
	// The index assertion above (firstIdx < secondIdx) is the load-bearing
	// detection for the start-time ordering: it is comparator-free and fires
	// first in the RED run. chronologicalOrder below is a secondary
	// consistency check over the whole slice; in the RED run it is never
	// reached because the index assertion fails first.
	// chronologicalOrder is independent of the implementation because it
	// parses StartedAt as time.Time (RFC3339Nano trims trailing zeros, making
	// lexicographic comparison non-chronological) rather than using
	// strings.Compare. Ref, PID, and Generation use the same comparison as
	// the implementation: those keys are fixed-width or integer-valued and
	// carry no variable-width hazard.
	// No time-vs-PID precedence fixture is present: the only same-ref group
	// (overlapFirst/overlapSecond) differs in both time and PID, so no row
	// ordering that differs only on the PID key is exercised. A fixture that
	// could not fail on any permutation the sort produces would prove nothing,
	// so none was added.
	chronologicalOrder := func(a, b appwire.DaemonResident) int {
		if c := strings.Compare(a.Identity.Ref, b.Identity.Ref); c != 0 {
			return c
		}
		aTime, _ := time.Parse(time.RFC3339Nano, a.Identity.StartedAt)
		bTime, _ := time.Parse(time.RFC3339Nano, b.Identity.StartedAt)
		if c := aTime.Compare(bTime); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Identity.PID, b.Identity.PID); c != 0 {
			return c
		}
		return strings.Compare(a.Identity.Generation, b.Identity.Generation)
	}
	if !slices.IsSortedFunc(response.Daemons, chronologicalOrder) {
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

// TestDaemonArchivedRowDisablesRetire proves the documented contract that an
// archived session's resident daemon stays visible with retirement disabled:
// the rendered row reports CanRetire:false even with a fresh compatible
// lifecycle, and a retire action against it is refused before any retire RPC
// reaches the daemon.
func TestDaemonArchivedRowDisablesRetire(t *testing.T) {
	runDir := t.TempDir()
	root := t.TempDir()
	entry := residentEntryForTest(t, 5201)
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
		return hubcore.ProbeResult{OK: true, SessionID: e.SessionID, Status: "idle", Lifecycle: residentLifecycleForTest(), LifecycleFresh: true}
	})
	roster := hubcore.NewRoster(runDir, prober).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	archive := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	if err := archive.Set("", "session", entry.SessionID, true, time.Now()); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster, Archive: archive, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	list, err := client.DaemonList(t.Context(), appwire.DaemonListParams{})
	if err != nil {
		t.Fatalf("DaemonList: %v", err)
	}
	if len(list.Daemons) != 1 {
		t.Fatalf("rows=%d, want 1: %+v", len(list.Daemons), list.Daemons)
	}
	row := list.Daemons[0]
	if !row.Archived || row.CanRetire {
		t.Fatalf("archived row archived=%v CanRetire=%v, want visible with retirement disabled: %+v", row.Archived, row.CanRetire, row)
	}
	if row.Compatibility != "compatible" || row.ProbeState != "current" || row.Lifecycle == nil || !row.CanForceStop {
		t.Fatalf("archived row lost its fresh lifecycle or force-stop capability: %+v", row)
	}
	_, err = client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: row.Identity})
	if err == nil {
		t.Fatal("retire of an archived daemon was accepted")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("archived retire err=%v, want conflict", err)
	}
	if retireCalls != 0 {
		t.Fatalf("archived retire reached the daemon %d times, want 0", retireCalls)
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

// TestDaemonActionRetireResolvesExactIdentityAmongSameRefResidents is the M5
// retire-path regression: when two live residents share a ref but differ in
// identity, a retire request carrying the addressed daemon's full rendered
// identity must reach that daemon instead of being rejected as ref-ambiguous
// before the identity is consulted.
func TestDaemonActionRetireResolvesExactIdentityAmongSameRefResidents(t *testing.T) {
	const shared = "shared-retire"
	runDir := t.TempDir()
	retireCalls := 0
	var forwarded appwire.DaemonIdentity
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodEvenerDaemonRetire, func(_ context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
		retireCalls++
		forwarded = params.Identity
		return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: *residentLifecycleForTest()}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	addressed := rendezvous.Entry{PID: 4411, SessionID: shared, ThreadID: shared, WorkspaceRef: "local:" + shared, StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], StartedAt: time.Now()}
	other := addressed
	other.PID = 4412
	other.StartedAt = addressed.StartedAt.Add(time.Second)
	other.Endpoint = "ws://127.0.0.1:1/rpc"
	other.StateDir = t.TempDir()
	writeRendezvous(t, runDir, addressed)
	writeRendezvous(t, runDir, other)

	// Both residents must look live to the ambiguity probe: a fake PID that the
	// real controller reports exited would be collapsed by the pre-existing
	// exited-claim path before the ambiguity check this test is about.
	var probeEvents []string
	controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &probeEvents}, nil
	})
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: controller, DaemonIdleTimeout: time.Hour}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	identity := daemonIdentity(addressed)
	retired, err := client.DaemonRetire(t.Context(), appwire.DaemonRetireParams{Identity: identity})
	if err != nil || !retired.Accepted {
		t.Fatalf("addressed retire=%+v err=%v", retired, err)
	}
	if retireCalls != 1 || forwarded != identity {
		t.Fatalf("retire forwarded identity=%+v calls=%d, want %+v exactly once", forwarded, retireCalls, identity)
	}
}
