package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestHubRestartCommandWaitsForNewIncarnation(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	_, root := endpointTestRoot(t)
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	previousStarted := hubProcessStartedAt
	hubProcessStartedAt = started
	t.Cleanup(func() { hubProcessStartedAt = previousStarted })
	scheduled := false
	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {
		mu.Lock()
		started = started.Add(time.Second)
		scheduled = true
		mu.Unlock()
	}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })

	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at := started
		mu.Unlock()
		restartFixtureHealth(t, w, r, at)
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, started)

	var stdout, stderr bytes.Buffer
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: "0.0.0.0:0", HubStateRoot: root}, nil
	}
	deps.stdout = &stdout

	if err := runHubRestart([]string{"--timeout", "5s"}, &stderr, deps); err != nil {
		t.Fatalf("runHubRestart: %v (stderr %q)", err, stderr.String())
	}
	if !scheduled {
		t.Fatal("restart was not sent to the hub")
	}
	if !strings.Contains(stdout.String(), "abc1234") {
		t.Fatalf("stdout = %q, want the target build", stdout.String())
	}
}

func TestHubRestartCommandUsesResponseIncarnation(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	_, root := endpointTestRoot(t)
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	healthStarted := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	responseStarted := healthStarted.Add(time.Second)
	previousStarted := hubProcessStartedAt
	hubProcessStartedAt = responseStarted
	t.Cleanup(func() { hubProcessStartedAt = previousStarted })

	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at := healthStarted
		mu.Unlock()
		restartFixtureHealth(t, w, r, at)
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, healthStarted)

	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {
		// Process B accepted the request but never restarted. Its health
		// answer is B, not the A the CLI read before sending.
		mu.Lock()
		healthStarted = responseStarted
		mu.Unlock()
	}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })

	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	if err := runHubRestart([]string{"--addr", addr, "--timeout", "300ms"}, &stderr, deps); err == nil {
		t.Fatal("runHubRestart confirmed a process that never restarted")
	}
	if stderr.Len() == 0 {
		t.Fatal("restart confirmation failure wrote no diagnostic")
	}
}

func TestHubRestartSubcommandDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"restart", "--not-a-flag"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("restart with an unknown flag exited 0")
	}
	if stdout.Len() != 0 {
		t.Fatalf("usage error wrote to stdout: %q", stdout.String())
	}
}

func TestHubRestartCommandReportsRefusal(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return false }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	_, root := endpointTestRoot(t)
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		restartFixtureHealth(t, w, r, started)
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, started)

	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	if err := runHubRestart([]string{"--addr", addr, "--timeout", "2s"}, &stderr, deps); err == nil {
		t.Fatal("runHubRestart accepted a refused restart")
	}
}

func TestWaitForNewIncarnationTimesOut(t *testing.T) {
	fixed := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(hubapi.HealthResponse{StartedAt: fixed})
	}))
	defer srv.Close()
	origin, err := attachOrigin(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := waitForNewIncarnation(ctx, origin, fixed); err == nil {
		t.Fatal("waitForNewIncarnation accepted an unchanged process")
	}
}

func TestIsAmbiguousRestartFailure(t *testing.T) {
	if !isAmbiguousRestartFailure(appwire.TransportFailureError{}) {
		t.Fatal("transport failure was treated as a refusal")
	}
	if isAmbiguousRestartFailure(appwire.WireError{}) {
		t.Fatal("server refusal was treated as ambiguous")
	}
	if isAmbiguousRestartFailure(appwire.RequestNotSentError{Err: errors.New("context canceled")}) {
		t.Fatal("a request that never left was treated as ambiguous")
	}
	if !isAmbiguousRestartFailure(errors.New("write: broken pipe")) {
		t.Fatal("a raw transport send failure was treated as final")
	}
}

func TestHubRestartCommandRefusesNonLoopbackBeforeSendingToken(t *testing.T) {
	root := t.TempDir()
	if _, err := hubedge.LoadOrCreateAuthToken(root); err != nil {
		t.Fatal(err)
	}
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		t.Fatal("non-loopback address reached endpoint discovery")
		return Config{Addr: "192.168.1.5:9180", HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	err := runHubRestart([]string{"--addr", "192.168.1.5:9180", "--timeout", "1s"}, &stderr, deps)
	if !errors.Is(err, errNonLoopbackAddr) {
		t.Fatalf("runHubRestart error = %v, want %v", err, errNonLoopbackAddr)
	}
}

func TestHubRestartCommandRefusesUserinfoAddress(t *testing.T) {
	root := t.TempDir()
	if _, err := hubedge.LoadOrCreateAuthToken(root); err != nil {
		t.Fatal(err)
	}
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		t.Fatal("userinfo address reached endpoint discovery")
		return Config{Addr: "127.0.0.1:9180@evil.example", HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	err := runHubRestart([]string{"--addr", "127.0.0.1:9180@evil.example", "--timeout", "1s"}, &stderr, deps)
	if !errors.Is(err, errNonLoopbackAddr) {
		t.Fatalf("runHubRestart error = %v, want %v", err, errNonLoopbackAddr)
	}
}

func TestHubRestartCommandRefusesDifferentEndpointBeforeTokenRead(t *testing.T) {
	_, root := endpointTestRoot(t) // No token exists, so reading it would mask the refusal.
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Error("mismatched endpoint received a control request")
		}
		restartFixtureHealth(t, w, r, started)
	}))
	defer srv.Close()
	publishRestartFixture(t, root, strings.TrimPrefix(srv.URL, "http://"), started)
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: "0.0.0.0:0", HubStateRoot: root}, nil
	}
	err := runHubRestart([]string{"--addr", "127.0.0.1:1"}, io.Discard, deps)
	if !errors.Is(err, hubendpoint.ErrIdentityMismatch) {
		t.Fatalf("different endpoint: %v", err)
	}
}

func TestHubRestartControlRefusesChangedIdentityBeforeSendingToken(t *testing.T) {
	_, root := endpointTestRoot(t)
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Error("changed identity received a control request")
		}
		restartFixtureHealth(t, w, r, started.Add(time.Second))
	}))
	defer srv.Close()
	publishRestartFixture(t, root, strings.TrimPrefix(srv.URL, "http://"), started)
	record, err := hubendpoint.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := attachOrigin(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := dialHubControl(t.Context(), origin, "restart-test-token", record)
	if client != nil {
		_ = client.Close()
		t.Fatal("changed identity was attached")
	}
	if !errors.Is(err, hubendpoint.ErrIdentityMismatch) {
		t.Fatalf("changed identity: %v", err)
	}
}

func restartFixtureHealth(t *testing.T, w http.ResponseWriter, r *http.Request, started time.Time) {
	t.Helper()
	if r.Header.Get("Authorization") != "" {
		t.Error("health probe sent a credential")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": "restart-fixture", "hub_addr": r.Host, "started_at": started})
}

func publishRestartFixture(t *testing.T, root, addr string, started time.Time) {
	t.Helper()
	release, err := hostlock.AcquireLock(filepath.Join(root, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	config, err := filepath.Abs(DefaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	remove, err := hubendpoint.Publish(root, hubendpoint.Record{
		SchemaVersion: hubendpoint.SchemaVersion, BootID: "restart-fixture", PID: os.Getpid(),
		StartedAt: started, ConfigPath: config, HubStateRoot: root,
		RequestedListenAddr: "0.0.0.0:0", ListenSource: "config", BoundAddr: addr,
		LocalURL: "http://" + addr, LoopbackURL: "http://" + addr,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := remove(); err != nil {
			t.Error(err)
		}
	})
}
