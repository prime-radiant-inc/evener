package hub

// evener/host/running tests (deploy pipeline 08b §6 step 2, §10, §12's "The
// running probe" and "The running-health definition" rows): the handler's
// direction-scoped admission, the required fencing epoch with the serving hub's
// persist/validate semantics, the revision/health/process-start-time response,
// and the health predicate's forced-false cases as data — never a probe
// failure. The catalog registration itself is pinned by
// TestHubRouterMatchesCatalog; one test here drives the real server to prove
// the registered path admits the attached session and refuses a browser one.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// runningTestManager builds a manager whose running handler is wired the way
// the tests need it: a temp state root, injected free-space and write-probe
// seams (so no test depends on the host's real disk), and a local roster the
// test controls.
func runningTestManager(t *testing.T, seams hostRunningConfig) *hubHostManager {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	cfg := hubcore.WebConfig{
		HubStateRoot:       t.TempDir(),
		RemoteHostOpsStore: store,
		HubBootID:          "test-boot",
		HubProcessStart:    time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	}
	m := newHubHostManager(nil, nil, cfg, "", nil, nil)
	if seams.freeSpace != nil {
		m.cfg.running.freeSpace = seams.freeSpace
	}
	if seams.writeProbe != nil {
		m.cfg.running.writeProbe = seams.writeProbe
	}
	m.cfg.running.roster = seams.roster
	m.cfg.running.stateRoots = []string{cfg.HubStateRoot}
	m.cfg.running.minFreeSpaceBytes = seams.minFreeSpaceBytes
	return m
}

// bridgeCtx is the context an attached controller session's request carries
// (the /rpc edge stamps the bridge origin the attach bridge presents).
func bridgeCtx() context.Context {
	return withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
}

// TestHostRunningServesItsOwnRevisionHealthAndStartTime pins §10's response:
// this hub's own revision from the same source as the controllerBuild input,
// its health flag, and its process start time when known.
func TestHostRunningServesItsOwnRevisionHealthAndStartTime(t *testing.T) {
	m := runningTestManager(t, hostRunningConfig{
		freeSpace:         func(string) (uint64, error) { return 1 << 40, nil },
		writeProbe:        func(string) error { return nil },
		minFreeSpaceBytes: DefaultHostMinFreeSpaceBytes,
	})
	response, err := m.HostRunning(bridgeCtx(), appwire.HostRunningParams{
		FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1},
	})
	if err != nil {
		t.Fatalf("HostRunning: %v", err)
	}
	if response.BuildRevision != buildinfo.Version() {
		t.Fatalf("buildRevision = %q, want %q", response.BuildRevision, buildinfo.Version())
	}
	if !response.Healthy {
		t.Fatal("a hub with a writable, spacious state root and no restart-required work reported unhealthy")
	}
	if response.ProcessStartTime != "2026-09-27T12:00:00Z" {
		t.Fatalf("processStartTime = %q, want the hub's own start instant", response.ProcessStartTime)
	}
}

// TestHostRunningRefusesBrowserAndForwardedOrigins pins the direction-scoped
// admission: only the attached session the probe arrived on is admitted — a
// browser or local request and any other forwarded origin are refused with the
// unavailable-class unauthenticated probe refusal.
func TestHostRunningRefusesBrowserAndForwardedOrigins(t *testing.T) {
	m := runningTestManager(t, hostRunningConfig{
		freeSpace:  func(string) (uint64, error) { return 1 << 40, nil },
		writeProbe: func(string) error { return nil },
	})
	params := appwire.HostRunningParams{FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1}}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "a local (browser) request", ctx: context.Background()},
		{name: "a forwarded request", ctx: withHostRoutingOrigin(context.Background(), "forwarded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.HostRunning(tc.ctx, params)
			assertWireCode(t, err, appwire.CodeUnavailable)
			if !strings.Contains(err.Error(), "unauthenticated probe") {
				t.Fatalf("refusal %q does not name the unauthenticated probe", err)
			}
		})
	}
}

// TestHostRunningRequiresTheEpoch pins §10's required-epoch rule: an epoch
// absent or defaulted on the wire — an all-zero pair — is a typed
// `probe-failed` refusal ("no epoch presented"), never served as an unfenced
// write, and nothing is persisted.
func TestHostRunningRequiresTheEpoch(t *testing.T) {
	m := runningTestManager(t, hostRunningConfig{
		freeSpace:  func(string) (uint64, error) { return 1 << 40, nil },
		writeProbe: func(string) error { return nil },
	})
	for _, epoch := range []appwire.FencingEpoch{{}, {BootID: "boot-1"}, {OpSeq: 3}} {
		_, err := m.HostRunning(bridgeCtx(), appwire.HostRunningParams{FencingEpoch: epoch})
		assertWireCode(t, err, appwire.CodeUnavailable)
		if !strings.Contains(err.Error(), "no epoch presented") {
			t.Fatalf("epoch %+v refusal %q does not name the missing epoch", epoch, err)
		}
	}
	if _, ok := m.cfg.ops.GuardEpoch(); ok {
		t.Fatal("an epoch-less call persisted a guard epoch")
	}
}

// TestHostRunningPersistsAndRefusesStaleEpochs pins §10's serving-side
// semantics: the presented epoch is persisted before the answer, a replay of it
// is idempotent, and an older epoch for the same boot is refused without
// probing — the store's row does not move.
func TestHostRunningPersistsAndRefusesStaleEpochs(t *testing.T) {
	probed := 0
	m := runningTestManager(t, hostRunningConfig{
		freeSpace: func(string) (uint64, error) {
			probed++
			return 1 << 40, nil
		},
		writeProbe: func(string) error { return nil },
	})
	if _, err := m.HostRunning(bridgeCtx(), appwire.HostRunningParams{FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 5}}); err != nil {
		t.Fatalf("HostRunning(5): %v", err)
	}
	stored, ok := m.cfg.ops.GuardEpoch()
	if !ok || stored.BootID != "boot-1" || stored.OpSeq != 5 {
		t.Fatalf("persisted guard epoch = %+v/%v, want boot-1/5", stored, ok)
	}
	if probed == 0 {
		t.Fatal("the first call never evaluated the health predicate")
	}
	before := probed
	if _, err := m.HostRunning(bridgeCtx(), appwire.HostRunningParams{FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 4}}); err == nil {
		t.Fatal("a stale epoch was served")
	} else if !strings.Contains(err.Error(), "stale fencing epoch") {
		t.Fatalf("stale refusal = %q, want the stale-epoch refusal", err)
	}
	if probed != before {
		t.Fatal("a stale epoch was probed; the refusal must happen without probing")
	}
	if stored, _ := m.cfg.ops.GuardEpoch(); stored.OpSeq != 5 {
		t.Fatalf("the stale refusal moved the persisted epoch to %+v", stored)
	}
	if _, err := m.HostRunning(bridgeCtx(), appwire.HostRunningParams{FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 5}}); err != nil {
		t.Fatalf("replayed current epoch refused: %v", err)
	}
}

// TestHostRunningHealthForcedFalseCasesAreData pins §10's health definition as
// forced-false cases: each condition returns healthy: false while the response
// itself still succeeds, and the free-space floor is checked before the write
// probe runs.
func TestHostRunningHealthForcedFalseCasesAreData(t *testing.T) {
	params := appwire.HostRunningParams{FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1}}

	t.Run("a restart-required daemon on the local roster", func(t *testing.T) {
		probedWrite := false
		m := runningTestManager(t, hostRunningConfig{
			freeSpace:  func(string) (uint64, error) { return 1 << 40, nil },
			writeProbe: func(string) error { probedWrite = true; return nil },
			roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{
				PID:    4242,
				Status: appwire.ThreadStatusRestartRequired,
			}),
		})
		response, err := m.HostRunning(bridgeCtx(), params)
		if err != nil {
			t.Fatalf("HostRunning: %v", err)
		}
		if response.Healthy {
			t.Fatal("a hub with a restart-required daemon reported healthy")
		}
		if response.BuildRevision == "" {
			t.Fatal("the forced-false response dropped the build revision")
		}
		if probedWrite {
			t.Fatal("the health predicate wrote a probe despite the failed restart-required check")
		}
	})

	t.Run("free space below the owner-set floor", func(t *testing.T) {
		probedWrite := false
		m := runningTestManager(t, hostRunningConfig{
			freeSpace:         func(string) (uint64, error) { return uint64(DefaultHostMinFreeSpaceBytes - 1), nil },
			writeProbe:        func(string) error { probedWrite = true; return nil },
			minFreeSpaceBytes: DefaultHostMinFreeSpaceBytes,
		})
		response, err := m.HostRunning(bridgeCtx(), params)
		if err != nil {
			t.Fatalf("HostRunning: %v", err)
		}
		if response.Healthy {
			t.Fatal("a hub below its minimum-free-space knob reported healthy")
		}
		if probedWrite {
			t.Fatal("the write probe ran below the free-space floor; the floor is checked first")
		}
	})

	t.Run("an unwritable state root", func(t *testing.T) {
		m := runningTestManager(t, hostRunningConfig{
			freeSpace:  func(string) (uint64, error) { return 1 << 40, nil },
			writeProbe: func(string) error { return errors.New("read-only file system") },
		})
		response, err := m.HostRunning(bridgeCtx(), params)
		if err != nil {
			t.Fatalf("HostRunning: %v", err)
		}
		if response.Healthy {
			t.Fatal("a hub whose state-root write probe failed reported healthy")
		}
	})

	t.Run("an unanswerable free-space query", func(t *testing.T) {
		m := runningTestManager(t, hostRunningConfig{
			freeSpace:  func(string) (uint64, error) { return 0, errors.New("statfs failed") },
			writeProbe: func(string) error { return nil },
		})
		response, err := m.HostRunning(bridgeCtx(), params)
		if err != nil {
			t.Fatalf("HostRunning: %v", err)
		}
		if response.Healthy {
			t.Fatal("a hub that could not answer the free-space query reported healthy")
		}
	})
}

// TestHostRunningWriteProbeIsAtomicAndLeavesNoStrays pins §10's write-probe
// shape: a real temp-plus-rename probe inside the state dir, fsynced, then
// removed — no file survives the probe — and the boot prune removes exactly the
// prefix-matching strays a crash left, never an operator file.
func TestHostRunningWriteProbeIsAtomicAndLeavesNoStrays(t *testing.T) {
	dir := t.TempDir()
	if err := hostStateRootWriteProbe(dir); err != nil {
		t.Fatalf("hostStateRootWriteProbe: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the write probe left %v behind, want nothing", names)
	}

	// A crash between the rename and the remove leaves a prefix-matching stray;
	// the boot prune removes it and only it.
	orphan := filepath.Join(dir, hostStateRootProbePrefix+"target-deadbeef")
	operator := filepath.Join(dir, "operator-file")
	for _, path := range []string{orphan, operator} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	if pruned := pruneHostRunningProbeStrays(dir); pruned != 1 {
		t.Fatalf("prune removed %d files, want exactly the one stray", pruned)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stray probe file survived the boot prune")
	}
	if _, err := os.Stat(operator); err != nil {
		t.Fatalf("the boot prune touched an operator file: %v", err)
	}
}

// TestHostRunningThroughTheRealServer pins the registration end to end: the
// real server construction installs the method, a browser-origin (markerless)
// call is refused, and a call carrying the bridge marker — the attached
// session's own origin — is served.
func TestHostRunningThroughTheRealServer(t *testing.T) {
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	cfg := hubcore.WebConfig{
		HubStateRoot:          t.TempDir(),
		RemoteHostOpsStore:    store,
		HubBootID:             "test-boot",
		HubProcessStart:       time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		HostMinFreeSpaceBytes: DefaultHostMinFreeSpaceBytes,
	}
	server, web := newHubRPCTestServerWithWeb(t, cfg)
	defer server.Close()
	if web.hostManage == nil {
		t.Fatal("the real server construction did not install the host-management surface")
	}
	// The handler's state root is the temp root from cfg; the predicate runs
	// the real free-space query against it, so its free bytes must clear the
	// floor — a temp root on a nearly-full volume would flake the healthy
	// assertion. The refusal/admission halves do not depend on the health flag.
	params := appwire.HostRunningParams{FencingEpoch: appwire.FencingEpoch{BootID: "boot-1", OpSeq: 1}}

	local := dialHubRPC(t, server)
	defer local.Close()
	if _, err := local.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var refused appwire.HostRunningResponse
	assertWireCode(t, local.Request(context.Background(), appwire.MethodEvenerHostRunning, params, &refused), appwire.CodeUnavailable)

	bridge := dialHubRPCWithHeaders(t, server, http.Header{bridgeOriginHeader: []string{"1"}})
	defer bridge.Close()
	if _, err := bridge.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("bridge Initialize: %v", err)
	}
	var served appwire.HostRunningResponse
	if err := bridge.Request(context.Background(), appwire.MethodEvenerHostRunning, params, &served); err != nil {
		t.Fatalf("evener/host/running over the bridge: %v", err)
	}
	if served.BuildRevision != buildinfo.Version() {
		t.Fatalf("buildRevision = %q, want %q", served.BuildRevision, buildinfo.Version())
	}
	if served.ProcessStartTime != "2026-09-27T12:00:00Z" {
		t.Fatalf("processStartTime = %q, want the hub's own start instant", served.ProcessStartTime)
	}
}

// dialHubRPCWithHeaders dials the hub's /rpc edge with extra request headers,
// the way the attach bridge presents its cooperative origin marker.
func dialHubRPCWithHeaders(t *testing.T, hub *httptest.Server, header http.Header) *appwire.Client {
	t.Helper()
	transport, err := appwire.DialWebSocketWithHeaders(context.Background(), "ws"+hub.URL[len("http"):]+"/rpc", hub.Client(), header)
	if err != nil {
		t.Fatalf("dial hub rpc with headers: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	return client
}
